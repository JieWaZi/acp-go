package pi

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	acp "github.com/coder/acp-go-sdk"
)

// testAgent 启动只理解 Pi RPC 的本地测试进程，拒绝任何 ACP 桥命令。
func testAgent(t *testing.T) (*Agent, acp.SessionId) {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	agent, err := NewAgent(context.Background(), Config{PiPath: executable, PrefixArgs: []string{"-test.run=^TestPiRPCProcess$", "--"}, Environment: append(os.Environ(), "ACP_GO_PI_FIXTURE=1", "PI_CODING_AGENT_DIR="+root), Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := agent.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	result, err := agent.NewSession(context.Background(), acp.NewSessionRequest{Cwd: root})
	if err != nil {
		t.Fatal(err)
	}
	return agent, result.SessionId
}

// TestPiConfigurationUsesModelThinkingLevels 验证会话只公布当前模型真实支持的思考等级。
func TestPiConfigurationUsesModelThinkingLevels(t *testing.T) {
	agent, id := testAgent(t)
	s, err := agent.get(id)
	if err != nil {
		t.Fatal(err)
	}
	options, err := s.configuration(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	response, err := convert[acp.NewSessionResponse](map[string]any{
		"sessionId":     id,
		"configOptions": options["configOptions"],
		"modes":         options["modes"],
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.ConfigOptions) != 2 || response.ConfigOptions[1].Select == nil || response.ConfigOptions[1].Select.Options.Ungrouped == nil {
		t.Fatalf("缺少 Pi 思考配置：%+v", response.ConfigOptions)
	}
	got := *response.ConfigOptions[1].Select.Options.Ungrouped
	want := []acp.SessionConfigValueId{"off", "high", "max"}
	if len(got) != len(want) {
		t.Fatalf("思考等级数量错误：got=%+v want=%+v", got, want)
	}
	for index, value := range want {
		if got[index].Value != value {
			t.Fatalf("思考等级错误：got=%+v want=%+v", got, want)
		}
	}
}

// TestPiBlobResourceMarker 验证二进制嵌入上下文不会被静默丢弃。
func TestPiBlobResourceMarker(t *testing.T) {
	agent, id := testAgent(t)
	mimeType := "application/octet-stream"
	result, err := agent.Prompt(context.Background(), acp.PromptRequest{
		SessionId: id,
		Prompt: []acp.ContentBlock{acp.ResourceBlock(acp.EmbeddedResourceResource{
			BlobResourceContents: &acp.BlobResourceContents{
				Blob:     "AAEC",
				MimeType: &mimeType,
				Uri:      "file:///tmp/a.bin",
			},
		})},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.StopReason != acp.StopReasonEndTurn {
		t.Fatalf("BlobResource 提示未完成：%+v", result)
	}
}

// TestPiSettledAndQueueCancellation 验证中间 agent_end 不结束提示，取消会阻止已排队提示迟到执行。
func TestPiSettledAndQueueCancellation(t *testing.T) {
	a, id := testAgent(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	start := time.Now()
	if result, err := a.Prompt(ctx, acp.PromptRequest{SessionId: id, Prompt: []acp.ContentBlock{acp.TextBlock("normal")}}); err != nil || result.StopReason != acp.StopReasonEndTurn || time.Since(start) < 80*time.Millisecond {
		t.Fatalf("settled barrier: %+v %v", result, err)
	}
	active := make(chan error, 1)
	go func() {
		result, err := a.Prompt(ctx, acp.PromptRequest{SessionId: id, Prompt: []acp.ContentBlock{acp.TextBlock("wait")}})
		if err == nil && result.StopReason != acp.StopReasonCancelled {
			err = errors.New("active prompt not cancelled")
		}
		active <- err
	}()
	s, _ := a.get(id)
	marker := filepath.Join(s.cwd, "waiting")
	for {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(time.Millisecond):
		}
	}
	queueCtx, stop := context.WithTimeout(ctx, 30*time.Millisecond)
	_, err := a.Prompt(queueCtx, acp.PromptRequest{SessionId: id, Prompt: []acp.ContentBlock{acp.TextBlock("must-not-run")}})
	stop()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("queued cancellation: %v", err)
	}
	if err := a.Cancel(ctx, acp.CancelNotification{SessionId: id}); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-active:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if _, err := a.Prompt(ctx, acp.PromptRequest{SessionId: id, Prompt: []acp.ContentBlock{acp.TextBlock("normal")}}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Prompt(ctx, acp.PromptRequest{SessionId: id, Prompt: []acp.ContentBlock{acp.TextBlock("/compact preserve API")}}); err != nil {
		t.Fatal(err)
	}
}

// TestPiDirectoryAliasesAndPagination 验证原生历史通过目录别名恢复并正确分页。
func TestPiDirectoryAliasesAndPagination(t *testing.T) {
	a, id := testAgent(t)
	s, _ := a.get(id)
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(s.cwd, alias); err != nil {
		t.Skip(err)
	}
	if _, err := a.findSession(id, alias); err != nil {
		t.Fatal(err)
	}
	if _, err := a.findSession(id, t.TempDir()); err == nil {
		t.Fatal("cross-workspace history accepted")
	}
	for i := 0; i < 51; i++ {
		data, _ := json.Marshal(map[string]any{"type": "session", "id": strings.Repeat("x", i+1), "cwd": s.cwd})
		if err := os.WriteFile(filepath.Join(a.sessionRoot(), strings.Repeat("x", i+1)+".jsonl"), append(data, '\n'), 0600); err != nil {
			t.Fatal(err)
		}
	}
	response, err := a.ListSessions(context.Background(), acp.ListSessionsRequest{Cwd: &alias})
	if err != nil || len(response.Sessions) != 50 || response.NextCursor == nil {
		t.Fatalf("pagination: %+v %v", response, err)
	}
	second, err := a.ListSessions(context.Background(), acp.ListSessionsRequest{Cwd: &alias, Cursor: response.NextCursor})
	if err != nil || len(second.Sessions) != 2 || second.NextCursor != nil {
		t.Fatalf("last page: %+v %v", second, err)
	}
	header, _ := json.Marshal(map[string]any{"type": "session", "id": "duplicate", "cwd": s.cwd})
	oldFile := filepath.Join(a.sessionRoot(), "a-duplicate.jsonl")
	newFile := filepath.Join(a.sessionRoot(), "z-duplicate.jsonl")
	for _, path := range []string{oldFile, newFile} {
		if err := os.WriteFile(path, append(header, '\n'), 0600); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now()
	if err := os.Chtimes(oldFile, now.Add(-time.Hour), now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if got, err := a.findSession("duplicate", s.cwd); err != nil || got != newFile {
		t.Fatalf("duplicate session selected %q, %v", got, err)
	}
}

// TestPiHistoryCacheRefreshesOnChange 验证原生历史追加后列表摘要会刷新。
func TestPiHistoryCacheRefreshesOnChange(t *testing.T) {
	a, id := testAgent(t)
	s, err := a.get(id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.stored(); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(s.file, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, writeErr := file.WriteString("{\"type\":\"session_info\",\"name\":\"updated title\"}\n")
	if err := errors.Join(writeErr, file.Close()); err != nil {
		t.Fatal(err)
	}
	entries, err := a.stored()
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.id == string(id) {
			if entry.title != "updated title" {
				t.Fatalf("cached history title = %q", entry.title)
			}
			file, err := os.OpenFile(s.file, os.O_APPEND|os.O_WRONLY, 0600)
			if err != nil {
				t.Fatal(err)
			}
			_, writeErr := file.WriteString(strings.Repeat("x", (16<<20)+1) + "\n{\"type\":\"session_info\",\"name\":\"after oversized\"}\n")
			if err := errors.Join(writeErr, file.Close()); err != nil {
				t.Fatal(err)
			}
			again, err := a.stored()
			if err != nil {
				t.Fatal(err)
			}
			for _, refreshed := range again {
				if refreshed.id == string(id) && refreshed.title == "after oversized" {
					return
				}
			}
			t.Fatal("oversized history line hid a later title")
		}
	}
	t.Fatal("Pi history disappeared from listing")
}

// TestPiFileDiffAndTerminalDeltas 验证结构化差异、行号、终端增量与退出状态不丢失。
func TestPiFileDiffAndTerminalDeltas(t *testing.T) {
	s := &session{cwd: t.TempDir(), tools: map[string]string{}, snapshots: map[string]fileSnapshot{}, bashOutput: map[string]bashOutputState{}}
	file := filepath.Join(s.cwd, "file.txt")
	_ = os.WriteFile(file, []byte("first\nbefore\n"), 0600)
	args := map[string]any{"path": "file.txt", "oldText": "before"}
	s.snapshotFile(map[string]any{"toolCallId": "edit-1", "toolName": "edit", "args": args})
	start := s.toolUpdate("edit-1", "edit", "in_progress", args, nil, false)
	if object(list(start["locations"])[0])["line"] != 2 {
		t.Fatal("missing edit line")
	}
	_ = os.WriteFile(file, []byte("first\nafter\n"), 0600)
	end := s.toolUpdate("edit-1", "edit", "completed", nil, map[string]any{"content": []any{}}, true)
	if block := object(list(end["content"])[0]); block["type"] != "diff" || block["newText"] != "first\nafter\n" {
		t.Fatalf("diff: %v", end)
	}
	if _, err := convert[acp.SessionUpdate](end); err != nil {
		t.Fatal(err)
	}
	s.toolUpdate("bash-1", "bash", "in_progress", map[string]any{"command": "fixture"}, nil, false)
	s.toolUpdate("bash-1", "bash", "in_progress", nil, map[string]any{"content": "one"}, false)
	output := s.toolUpdate("bash-1", "bash", "failed", nil, map[string]any{"content": "one two", "details": map[string]any{"exitCode": float64(7)}}, true)
	meta := object(output["_meta"])
	if object(meta["terminal_output"])["data"] != " two" || object(meta["terminal_exit"])["exit_code"] != 7 {
		t.Fatalf("terminal: %v", output)
	}
	s.toolUpdate("bash-2", "bash", "in_progress", map[string]any{"command": "fixture"}, nil, false)
	large := strings.Repeat("x", 1<<20)
	s.toolUpdate("bash-2", "bash", "in_progress", nil, map[string]any{"content": large}, false)
	if s.bashOutput["bash-2"].length != len(large) {
		t.Fatal("Bash output state lost cumulative length")
	}
	reset := s.toolUpdate("bash-2", "bash", "completed", nil, map[string]any{"content": "reset"}, true)
	if object(object(reset["_meta"])["terminal_output"])["data"] != "reset" {
		t.Fatal("Bash output reset did not produce a full delta")
	}
	if len(s.tools) != 0 || len(s.snapshots) != 0 || len(s.bashOutput) != 0 {
		t.Fatal("tool state retained after completion")
	}
}

// TestPiBadFramePreservesRawError 验证协议坏帧与 CLI 原始 stderr 一起返回。
func TestPiBadFramePreservesRawError(t *testing.T) {
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	p, err := startRPC(Config{PiPath: binary, PrefixArgs: []string{"-test.run=^TestPiRPCProcess$", "--"}, Environment: append(os.Environ(), "ACP_GO_PI_FIXTURE=bad-frame")}, t.TempDir(), "", "")
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-p.done:
	case <-time.After(3 * time.Second):
		t.Fatal("Pi bad-frame process did not exit")
	}
	detail := p.exitError().Error()
	if !strings.Contains(detail, "invalid JSON") || !strings.Contains(detail, "api_key=literal-secret") {
		t.Fatalf("Pi failure lost raw diagnostics: %q", detail)
	}
}

// TestPiEventQueueOverloadTerminatesBoundedly 验证宿主停止消费事件时不会永久卡住 RPC reader。
func TestPiEventQueueOverloadTerminatesBoundedly(t *testing.T) {
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	p, err := startRPC(Config{PiPath: binary, PrefixArgs: []string{"-test.run=^TestPiRPCProcess$", "--"}, Environment: append(os.Environ(), "ACP_GO_PI_FIXTURE=event-overflow")}, t.TempDir(), "", "")
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-p.done:
	case <-time.After(3 * time.Second):
		t.Fatal("Pi reader blocked on a full event queue")
	}
	if detail := p.exitError().Error(); !strings.Contains(detail, "queue stalled") {
		t.Fatalf("Pi overload reason = %q", detail)
	}
}

// TestPiRPCProcess 为直接 RPC 测试提供权威响应、错误和 settled 时序。
func TestPiRPCProcess(t *testing.T) {
	if os.Getenv("ACP_GO_PI_FIXTURE") == "event-overflow" {
		for range 257 {
			_, _ = os.Stdout.WriteString("{\"type\":\"agent_start\"}\n")
		}
		os.Exit(0)
	}
	if os.Getenv("ACP_GO_PI_FIXTURE") == "bad-frame" {
		_, _ = os.Stdout.WriteString("{\"type\":\"response\",\"id\":\"1\",\"success\":true,\"data\":{}}\n{invalid\n")
		_, _ = os.Stderr.WriteString("api_key=literal-secret\n")
		os.Exit(7)
	}
	if os.Getenv("ACP_GO_PI_FIXTURE") != "1" {
		return
	}
	cwd, _ := os.Getwd()
	file := filepath.Join(os.Getenv("PI_CODING_AGENT_DIR"), "sessions", "fixture.jsonl")
	_ = os.MkdirAll(filepath.Dir(file), 0700)
	header, _ := json.Marshal(map[string]any{"type": "session", "id": "fixture-session", "cwd": cwd})
	_ = os.WriteFile(file, append(header, '\n'), 0600)
	write := func(value any) {
		if err := json.NewEncoder(os.Stdout).Encode(value); err != nil {
			os.Exit(2)
		}
	}
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		var request map[string]any
		if json.Unmarshal(scanner.Bytes(), &request) != nil {
			os.Exit(3)
		}
		result := any(map[string]any{})
		switch request["type"] {
		case "get_state":
			result = map[string]any{"sessionId": "fixture-session", "sessionFile": file, "model": map[string]any{"provider": "fixture", "id": "model"}, "thinkingLevel": "high"}
		case "get_available_models":
			result = map[string]any{"models": []any{map[string]any{"provider": "fixture", "id": "model"}}}
		case "get_available_thinking_levels":
			result = map[string]any{"levels": []any{"off", "high", "max"}}
		case "get_commands":
			result = map[string]any{"commands": []any{}}
		case "compact":
			if request["customInstructions"] != "preserve API" {
				os.Exit(4)
			}
			result = map[string]any{"tokensBefore": 100, "summary": "compacted"}
		case "prompt":
			if strings.Contains(text(request["message"]), "file:///tmp/a.bin") && request["message"] != "\n[Embedded Context] file:///tmp/a.bin (application/octet-stream, 3 bytes)" {
				os.Exit(7)
			}
			if request["message"] == "must-not-run" {
				os.Exit(5)
			}
			if request["message"] == "wait" {
				_ = os.WriteFile(filepath.Join(cwd, "waiting"), nil, 0600)
			} else {
				switch request["message"] {
				case "model failure", "model recovered":
					write(map[string]any{"type": "message_end", "message": map[string]any{"role": "assistant", "stopReason": "error", "errorMessage": "provider unavailable"}})
					if request["message"] == "model recovered" {
						write(map[string]any{"type": "auto_retry_end", "success": true})
					}
				case "retry failure":
					write(map[string]any{"type": "auto_retry_end", "success": false, "finalError": "529 overloaded_error: Overloaded"})
				case "extension failure":
					write(map[string]any{"type": "extension_error", "error": "extension rejected operation"})
				}
				if request["message"] == "usage" {
					for _, usage := range []map[string]any{{"input": 10, "output": 5, "cacheRead": 3, "cacheWrite": 2, "totalTokens": 20}, {"input": 20, "output": 6, "cacheRead": 4, "cacheWrite": 1, "totalTokens": 31}} {
						write(map[string]any{"type": "message_end", "message": map[string]any{"role": "assistant", "stopReason": "stop", "usage": usage}})
					}
				}
				write(map[string]any{"type": "agent_end"})
				time.Sleep(80 * time.Millisecond)
				write(map[string]any{"type": "agent_settled"})
			}
		case "abort":
			write(map[string]any{"type": "agent_settled"})
		default:
			os.Exit(6)
		}
		write(map[string]any{"type": "response", "id": request["id"], "success": true, "data": result})
	}
	os.Exit(0)
}

// TestPiBlockedWriteCancellation 验证 Pi 不读 stdin 时，大提示写入仍能有界取消。
func TestPiBlockedWriteCancellation(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	defer writer.Close()
	stopped := make(chan struct{})
	p := &rpcProcess{input: writer, pending: map[string]chan rpcResponse{}, cancel: func() { close(stopped) }}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	_, err = p.begin(ctx, "prompt", map[string]any{"message": strings.Repeat("x", 1024*1024)})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("blocked write: %v", err)
	}
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("unresponsive process not stopped")
	}
	if len(p.pending) != 0 {
		t.Fatal("cancelled write retained pending request")
	}
}

// TestPiPromptUsagePerTurn 验证多次模型调用累计用量，下一轮无数据不能复用上一轮统计。
func TestPiPromptUsagePerTurn(t *testing.T) {
	a, id := testAgent(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result, err := a.Prompt(ctx, acp.PromptRequest{SessionId: id, Prompt: []acp.ContentBlock{acp.TextBlock("usage")}})
	if err != nil {
		t.Fatal(err)
	}
	u := result.Usage
	if u == nil || u.InputTokens != 30 || u.OutputTokens != 11 || u.TotalTokens != 51 || u.CachedReadTokens == nil || *u.CachedReadTokens != 7 || u.CachedWriteTokens == nil || *u.CachedWriteTokens != 3 {
		t.Fatalf("missing or incorrect Pi token statistics: %+v", u)
	}
	result, err = a.Prompt(ctx, acp.PromptRequest{SessionId: id, Prompt: []acp.ContentBlock{acp.TextBlock("normal")}})
	if err != nil || result.Usage != nil {
		t.Fatalf("previous turn usage leaked: %+v, %v", result.Usage, err)
	}
}

// TestPiPromptErrorContract 验证错误经完整 RPC 回合返回，恢复后的下一轮仍可正常完成。
func TestPiPromptErrorContract(t *testing.T) {
	agent, id := testAgent(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, message := range []string{"model failure", "extension failure", "retry failure", "model recovered", "explain error handling"} {
		response, err := agent.Prompt(ctx, acp.PromptRequest{SessionId: id, Prompt: []acp.ContentBlock{acp.TextBlock(message)}})
		if message == "model failure" || message == "extension failure" || message == "retry failure" {
			if err == nil {
				t.Fatalf("%s 伪装为成功：%+v", message, response)
			}
		} else if err != nil || response.StopReason != acp.StopReasonEndTurn {
			t.Fatalf("%s 误判失败：%+v %v", message, response, err)
		}
	}
}

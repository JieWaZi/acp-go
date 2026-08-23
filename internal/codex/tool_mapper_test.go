package codex

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	acp "github.com/coder/acp-go-sdk"
)

// TestEventRouterMapsCommandLifecycle 验证命令 start/delta/completed 使用同一 ACP toolCallId。
func TestEventRouterMapsCommandLifecycle(t *testing.T) {
	t.Parallel()

	updater := &recordingSessionUpdater{}
	router := newTestEventRouter(updater, &fixedGenerationGuard{current: true}, slog.Default())
	for _, raw := range []string{
		`{"method":"item/started","params":{"threadId":"thread-1","turnId":"turn-1","startedAtMs":0,"item":{"type":"commandExecution","id":"command-1","command":"/bin/zsh -c 'echo hello'","cwd":"/work","status":"inProgress","commandActions":[]}}}`,
		`{"method":"item/commandExecution/outputDelta","params":{"threadId":"thread-1","turnId":"turn-1","itemId":"command-1","delta":"hello\n"}}`,
		`{"method":"item/commandExecution/terminalInteraction","params":{"threadId":"thread-1","turnId":"turn-1","itemId":"command-1","processId":"process-1","stdin":"yes"}}`,
		`{"method":"item/completed","params":{"threadId":"thread-1","turnId":"turn-1","completedAtMs":1,"item":{"type":"commandExecution","id":"command-1","command":"/bin/zsh -c 'echo hello'","cwd":"/work","status":"completed","commandActions":[],"aggregatedOutput":"hello\n","exitCode":0}}}`,
	} {
		if err := router.HandleJSON(context.Background(), []byte(raw)); err != nil {
			t.Fatalf("HandleJSON 返回错误: %v", err)
		}
	}
	if got, want := len(updater.notifications), 4; got != want {
		t.Fatalf("command update 数 = %d，期望 %d", got, want)
	}
	started := updater.notifications[0].Update.ToolCall
	if started == nil || started.ToolCallId != "command-1" || started.Kind != acp.ToolKindExecute || started.Title != "echo hello" || started.Status != acp.ToolCallStatusInProgress {
		t.Fatalf("command start = %#v", started)
	}
	if len(started.Content) != 1 || started.Content[0].Terminal == nil || started.Content[0].Terminal.TerminalId != "command-1" {
		t.Fatalf("terminal content = %#v", started.Content)
	}
	if started.Meta["terminal_info"].(map[string]any)["cwd"] != "/work" {
		t.Fatalf("terminal meta = %#v", started.Meta)
	}
	delta := updater.notifications[1].Update.ToolCallUpdate
	if delta == nil || delta.ToolCallId != "command-1" || delta.Meta["terminal_output_delta"].(map[string]any)["data"] != "hello\n" {
		t.Fatalf("command delta = %#v", delta)
	}
	assertMetaWire(t, delta.Meta, `{"terminal_output_delta":{"data":"hello\n","terminal_id":"command-1"}}`)
	interaction := updater.notifications[2].Update.ToolCallUpdate
	if interaction == nil || interaction.ToolCallId != "command-1" {
		t.Fatalf("terminal interaction = %#v", interaction)
	}
	assertMetaWire(t, interaction.Meta, `{"terminal_output_delta":{"data":"\nyes\n","terminal_id":"command-1"}}`)
	completed := updater.notifications[3].Update.ToolCallUpdate
	if completed == nil || completed.ToolCallId != "command-1" || completed.Status == nil || *completed.Status != acp.ToolCallStatusCompleted {
		t.Fatalf("command completed = %#v", completed)
	}
	if completed.Meta["terminal_output"] != nil {
		t.Fatalf("已有 delta 时不应重复 terminal_output: %#v", completed.Meta)
	}
	exit, ok := completed.Meta["terminal_exit"].(map[string]any)
	if !ok {
		t.Fatalf("command completion 缺 terminal_exit: %#v", completed.Meta)
	}
	if exit["exit_code"] != int64(0) || exit["signal"] != nil || exit["terminal_id"] != "command-1" {
		t.Fatalf("terminal exit = %#v", exit)
	}
	rawOutput := completed.RawOutput.(map[string]any)
	if rawOutput["formatted_output"] != "hello\n" || rawOutput["exit_code"] != int64(0) {
		t.Fatalf("command raw output = %#v", rawOutput)
	}
}

// TestEventRouterFallsBackToTerminalOutputOnCommandCompletion 锁定固定 upstream completion fallback fixture。
func TestEventRouterFallsBackToTerminalOutputOnCommandCompletion(t *testing.T) {
	t.Parallel()

	updater := &recordingSessionUpdater{}
	router := newTestEventRouter(updater, &fixedGenerationGuard{current: true}, slog.Default())
	started := `{"method":"item/started","params":{"threadId":"thread-1","turnId":"turn-1","startedAtMs":0,"item":{"type":"commandExecution","id":"command-terminal-output-completion","command":"git status --short","cwd":"/test/project","status":"inProgress","commandActions":[]}}}`
	completed := `{"method":"item/completed","params":{"threadId":"thread-1","turnId":"turn-1","completedAtMs":1,"item":{"type":"commandExecution","id":"command-terminal-output-completion","command":"git status --short","cwd":"/test/project","status":"completed","commandActions":[],"aggregatedOutput":"M src/CodexEventHandler.ts\n","exitCode":0}}}`
	for _, raw := range []string{started, completed, completed} {
		if err := router.HandleJSON(context.Background(), []byte(raw)); err != nil {
			t.Fatalf("HandleJSON 返回错误: %v", err)
		}
	}
	if got, want := len(updater.notifications), 2; got != want {
		t.Fatalf("completion fallback update 数 = %d，期望 %d", got, want)
	}
	update := updater.notifications[1].Update.ToolCallUpdate
	if update == nil {
		t.Fatal("缺少 command completion update")
	}
	output, ok := update.Meta["terminal_output_delta"].(map[string]any)
	if !ok {
		t.Fatalf("默认客户端的 command completion 缺 terminal_output_delta fallback: %#v", update.Meta)
	}
	if output["data"] != "M src/CodexEventHandler.ts\n" || output["terminal_id"] != "command-terminal-output-completion" {
		t.Fatalf("terminal output fallback = %#v", output)
	}
	if update.Meta["terminal_output"] != nil {
		t.Fatalf("默认客户端不应收到 terminal_output: %#v", update.Meta)
	}
	assertMetaWire(t, update.Meta, `{"terminal_exit":{"exit_code":0,"signal":null,"terminal_id":"command-terminal-output-completion"},"terminal_output_delta":{"data":"M src/CodexEventHandler.ts\n","terminal_id":"command-terminal-output-completion"}}`)
	exit, ok := update.Meta["terminal_exit"].(map[string]any)
	if !ok {
		t.Fatalf("command completion 缺 terminal_exit: %#v", update.Meta)
	}
	if exit["exit_code"] != int64(0) || exit["signal"] != nil || exit["terminal_id"] != "command-terminal-output-completion" {
		t.Fatalf("terminal exit = %#v", exit)
	}
}

// assertMetaWire 把 SDK 扩展元数据编码为真实 JSON，并与手工 upstream fixture 精确比较。
func assertMetaWire(t *testing.T, meta map[string]any, want string) {
	t.Helper()
	got, err := json.Marshal(meta)
	if err != nil {
		t.Fatalf("编码扩展元数据失败: %v", err)
	}
	if string(got) != want {
		t.Fatalf("扩展元数据 wire = %s，期望 %s", got, want)
	}
}

// TestEventRouterMapsParsedCommandActions 验证上游已解析 read/search/listFiles 时不伪装成终端。
func TestEventRouterMapsParsedCommandActions(t *testing.T) {
	t.Parallel()

	updater := &recordingSessionUpdater{}
	router := newTestEventRouter(updater, &fixedGenerationGuard{current: true}, slog.Default())
	for _, raw := range []string{
		`{"method":"item/started","params":{"threadId":"thread-1","turnId":"turn-1","startedAtMs":0,"item":{"type":"commandExecution","id":"read-1","command":"cat README.md","cwd":"/work","status":"inProgress","commandActions":[{"type":"read","command":"cat README.md","name":"cat","path":"/work/README.md"}]}}}`,
		`{"method":"item/started","params":{"threadId":"thread-1","turnId":"turn-1","startedAtMs":0,"item":{"type":"commandExecution","id":"search-1","command":"rg Service src","cwd":"/work","status":"inProgress","commandActions":[{"type":"search","command":"rg Service src","query":"Service","path":"src"}]}}}`,
		`{"method":"item/started","params":{"threadId":"thread-1","turnId":"turn-1","startedAtMs":0,"item":{"type":"commandExecution","id":"list-1","command":"ls /work","cwd":"/work","status":"completed","commandActions":[{"type":"listFiles","command":"ls /work","path":"/work"}]}}}`,
	} {
		if err := router.HandleJSON(context.Background(), []byte(raw)); err != nil {
			t.Fatalf("HandleJSON 返回错误: %v", err)
		}
	}
	if got, want := len(updater.notifications), 3; got != want {
		t.Fatalf("parsed command update 数 = %d，期望 %d", got, want)
	}
	want := []struct {
		kind  acp.ToolKind
		title string
	}{
		{acp.ToolKindRead, "Read file '/work/README.md'"},
		{acp.ToolKindSearch, "Search for 'Service' in src"},
		{acp.ToolKindRead, "List files in '/work'"},
	}
	for index, expected := range want {
		tool := updater.notifications[index].Update.ToolCall
		if tool == nil || tool.Kind != expected.kind || tool.Title != expected.title || len(tool.Content) != 0 {
			t.Fatalf("parsed command[%d] = %#v", index, tool)
		}
	}
}

// TestEventRouterMapsMCPProgressAndCompletion 验证 MCP start/progress/completed 的 raw 输入输出和重复进度。
func TestEventRouterMapsMCPProgressAndCompletion(t *testing.T) {
	t.Parallel()

	updater := &recordingSessionUpdater{}
	router := newTestEventRouter(updater, &fixedGenerationGuard{current: true}, slog.Default())
	for _, raw := range []string{
		`{"method":"item/started","params":{"threadId":"thread-1","turnId":"turn-1","startedAtMs":0,"item":{"type":"mcpToolCall","id":"mcp-1","server":"server-name","tool":"tool-name","status":"inProgress","arguments":{"argument":"example"}}}}`,
		`{"method":"item/mcpToolCall/progress","params":{"threadId":"thread-1","turnId":"turn-1","itemId":"mcp-1","message":"Polling"}}`,
		`{"method":"item/mcpToolCall/progress","params":{"threadId":"thread-1","turnId":"turn-1","itemId":"mcp-1","message":"Polling"}}`,
		`{"method":"item/completed","params":{"threadId":"thread-1","turnId":"turn-1","completedAtMs":1,"item":{"type":"mcpToolCall","id":"mcp-1","server":"server-name","tool":"tool-name","status":"failed","arguments":{"argument":"example"},"error":{"message":"Polling"}}}}`,
	} {
		if err := router.HandleJSON(context.Background(), []byte(raw)); err != nil {
			t.Fatalf("HandleJSON 返回错误: %v", err)
		}
	}
	if got, want := len(updater.notifications), 4; got != want {
		t.Fatalf("MCP update 数 = %d，期望 %d", got, want)
	}
	started := updater.notifications[0].Update.ToolCall
	if started == nil || started.Title != "mcp.server-name.tool-name" || started.Meta["is_mcp_tool_call"] != true {
		t.Fatalf("MCP start = %#v", started)
	}
	for index := 1; index <= 2; index++ {
		progress := updater.notifications[index].Update.ToolCallUpdate
		if progress == nil || progress.Meta["mcp_output_delta"].(map[string]any)["data"] != "Polling" {
			t.Fatalf("MCP progress[%d] = %#v", index, progress)
		}
	}
	completed := updater.notifications[3].Update.ToolCallUpdate
	if completed == nil || completed.Status == nil || *completed.Status != acp.ToolCallStatusFailed || completed.RawInput == nil || completed.RawOutput == nil {
		t.Fatalf("MCP completed = %#v", completed)
	}
}

// TestEventRouterMapsFileAddsDeletesAndPreservesRawUpdates 验证 add/delete rich diff 与 update/move raw 保留策略。
func TestEventRouterMapsFileAddsDeletesAndPreservesRawUpdates(t *testing.T) {
	t.Parallel()

	updater := &recordingSessionUpdater{}
	router := newTestEventRouter(updater, &fixedGenerationGuard{current: true}, slog.Default())
	for _, raw := range []string{
		`{"method":"item/started","params":{"threadId":"thread-1","turnId":"turn-1","startedAtMs":0,"item":{"type":"fileChange","id":"file-add","status":"completed","changes":[{"path":"/work/New.go","kind":{"type":"add"},"diff":"package main\n"}]}}}`,
		`{"method":"item/started","params":{"threadId":"thread-1","turnId":"turn-1","startedAtMs":0,"item":{"type":"fileChange","id":"file-delete","status":"completed","changes":[{"path":"/work/Old.go","kind":{"type":"delete"},"diff":"package old\n"}]}}}`,
		`{"method":"item/started","params":{"threadId":"thread-1","turnId":"turn-1","startedAtMs":0,"item":{"type":"fileChange","id":"file-update","status":"inProgress","changes":[{"path":"/work/Old.go","kind":{"type":"update","move_path":"/work/New.go"},"diff":"@@ -1 +1 @@\n-old\n+new\n"}]}}}`,
	} {
		if err := router.HandleJSON(context.Background(), []byte(raw)); err != nil {
			t.Fatalf("HandleJSON 返回错误: %v", err)
		}
	}
	if got, want := len(updater.notifications), 3; got != want {
		t.Fatalf("file update 数 = %d，期望 %d", got, want)
	}
	add := updater.notifications[0].Update.ToolCall
	if add == nil || len(add.Content) != 1 || add.Content[0].Diff == nil || add.Content[0].Diff.OldText != nil || add.Content[0].Diff.NewText != "package main\n" {
		t.Fatalf("file add = %#v", add)
	}
	deleted := updater.notifications[1].Update.ToolCall
	if deleted == nil || len(deleted.Content) != 1 || deleted.Content[0].Diff == nil || deleted.Content[0].Diff.OldText == nil || *deleted.Content[0].Diff.OldText != "package old\n" || deleted.Content[0].Diff.NewText != "" {
		t.Fatalf("file delete = %#v", deleted)
	}
	updated := updater.notifications[2].Update.ToolCall
	if updated == nil || len(updated.Content) != 0 || updated.RawInput == nil {
		t.Fatalf("file update = %#v，期望无自造 rich diff 且保留 raw", updated)
	}
}

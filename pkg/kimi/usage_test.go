package kimi

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	acp "github.com/coder/acp-go-sdk"
)

// TestWireUsageExcludesHistory 验证官方事件只累计新增本轮，缺失统计不能返回假零值。
func TestWireUsageExcludesHistory(t *testing.T) {
	file := filepath.Join(t.TempDir(), "wire.jsonl")
	event := `{"message":{"type":"StatusUpdate","payload":{"token_usage":{"input_other":7,"output":5,"input_cache_read":3,"input_cache_creation":2},"context_tokens":100,"max_context_tokens":100000}}}` + "\n"
	if err := os.WriteFile(file, []byte(event+event+event), 0600); err != nil {
		t.Fatal(err)
	}
	usage, update := readWireUsage(file, int64(len(event)))
	if usage == nil || usage.InputTokens != 14 || usage.OutputTokens != 10 || *usage.CachedReadTokens != 6 || *usage.CachedWriteTokens != 4 || usage.TotalTokens != 34 || update == nil || update.Used != 100 || update.Size != 100000 {
		t.Fatalf("wrong incremental usage: %+v %+v", usage, update)
	}
	if usage, update = readWireUsage(file, int64(3*len(event))); usage != nil || update != nil {
		t.Fatal("empty turn fabricated usage")
	}
	if usage, update = readWireUsage(file+".missing", 0); usage != nil || update != nil {
		t.Fatal("missing file fabricated usage")
	}
}

// TestKimiResumeSessionRestoresWireUsage 验证新进程恢复会话后仍从官方 wire 读取本轮用量。
func TestKimiResumeSessionRestoresWireUsage(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cwd := t.TempDir()
	agent, err := NewAgent(context.Background(), Config{
		KimiPath:         executable,
		PrefixArgs:       []string{"-test.run=^TestKimiUsageACPProcess$", "--"},
		Environment:      append(os.Environ(), "ACP_GO_KIMI_USAGE_FIXTURE=1"),
		WorkingDirectory: cwd,
		StateDirectory:   t.TempDir(),
		Logger:           slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
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
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := agent.Initialize(ctx, acp.InitializeRequest{ProtocolVersion: 1}); err != nil {
		t.Fatal(err)
	}
	sessionID := acp.SessionId("resume-session")
	if _, err := agent.ResumeSession(ctx, acp.ResumeSessionRequest{
		SessionId: sessionID,
		Cwd:       cwd,
	}); err != nil {
		t.Fatal(err)
	}
	result, err := agent.Prompt(ctx, acp.PromptRequest{
		SessionId: sessionID,
		Prompt:    []acp.ContentBlock{acp.TextBlock("usage")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Usage == nil || result.Usage.InputTokens != 7 || result.Usage.OutputTokens != 5 {
		t.Fatalf("恢复会话缺少本轮用量：%+v", result.Usage)
	}
}

// TestKimiUsageACPProcess 提供支持 resume 与官方 wire 用量的 Python Kimi ACP 替身。
func TestKimiUsageACPProcess(t *testing.T) {
	if os.Getenv("ACP_GO_KIMI_USAGE_FIXTURE") != "1" {
		return
	}
	var cwd string
	ready := make(chan struct{})
	var connection *acp.Connection
	connection = acp.NewConnection(func(_ context.Context, method string, data json.RawMessage) (any, *acp.RequestError) {
		<-ready
		var request map[string]any
		if err := json.Unmarshal(data, &request); err != nil {
			return nil, acp.NewInvalidParams(nil)
		}
		switch method {
		case "initialize":
			return map[string]any{
				"protocolVersion": 1,
				"agentInfo":       map[string]any{"name": "Kimi Code CLI", "version": "fixture"},
				"agentCapabilities": map[string]any{
					"sessionCapabilities": map[string]any{"resume": map[string]any{}},
				},
			}, nil
		case "session/resume":
			cwd, _ = request["cwd"].(string)
			return map[string]any{"sessionId": request["sessionId"]}, nil
		case "session/prompt":
			digest := md5.Sum([]byte(cwd))
			wire := filepath.Join(
				os.Getenv("KIMI_SHARE_DIR"),
				"sessions",
				hex.EncodeToString(digest[:]),
				"resume-session",
				"wire.jsonl",
			)
			if err := os.MkdirAll(filepath.Dir(wire), 0o700); err != nil {
				return nil, acp.NewInternalError(nil)
			}
			event := `{"message":{"type":"StatusUpdate","payload":{"token_usage":{"input_other":7,"output":5,"input_cache_read":3,"input_cache_creation":2}}}}` + "\n"
			if err := os.WriteFile(wire, []byte(event), 0o600); err != nil {
				return nil, acp.NewInternalError(nil)
			}
			return map[string]any{"stopReason": "end_turn"}, nil
		default:
			return nil, acp.NewMethodNotFound(method)
		}
	}, os.Stdout, os.Stdin)
	close(ready)
	<-connection.Done()
	os.Exit(0)
}

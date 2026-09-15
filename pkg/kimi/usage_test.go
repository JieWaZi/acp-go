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
	"strconv"
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
	usage, update := readWireUsage(file, int64(len(event)), "")
	if usage == nil || usage.InputTokens != 14 || usage.OutputTokens != 10 || *usage.CachedReadTokens != 6 || *usage.CachedWriteTokens != 4 || usage.TotalTokens != 34 || update == nil || update.Used != 100 || update.Size != 100000 {
		t.Fatalf("wrong incremental usage: %+v %+v", usage, update)
	}
	if usage, update = readWireUsage(file, int64(3*len(event)), ""); usage != nil || update != nil {
		t.Fatal("empty turn fabricated usage")
	}
	if usage, update = readWireUsage(file+".missing", 0, ""); usage != nil || update != nil {
		t.Fatal("missing file fabricated usage")
	}
}

// TestCurrentWireUsage 验证新版 Kimi Code 会话索引和顶层统计事件可转换为标准 ACP 用量。
func TestCurrentWireUsage(t *testing.T) {
	home := t.TempDir()
	cwd := filepath.Join(t.TempDir(), "workspace")
	sessionID := acp.SessionId("session-current")
	sessionDirectory := filepath.Join(home, "sessions", "wd_workspace", string(sessionID))
	wire := filepath.Join(sessionDirectory, "agents", "main", "wire.jsonl")
	if err := os.MkdirAll(filepath.Dir(wire), 0o700); err != nil {
		t.Fatal(err)
	}
	index := `{"sessionDir":` + strconv.Quote(sessionDirectory) + `,"sessionId":"session-current","workDir":` + strconv.Quote(cwd) + `}` + "\n"
	if err := os.WriteFile(filepath.Join(home, "session_index.jsonl"), []byte(index), 0o600); err != nil {
		t.Fatal(err)
	}
	config := `[models."deepseek/deepseek-flash"]
max_context_size = 1000000
`
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	history := `{"type":"usage.record","model":"deepseek/deepseek-flash","usage":{"inputOther":99,"output":99,"inputCacheRead":99,"inputCacheCreation":99},"usageScope":"turn"}` + "\n"
	if err := os.WriteFile(wire, []byte(history), 0o600); err != nil {
		t.Fatal(err)
	}
	path := resolveWirePath(home, t.TempDir(), sessionID, cwd)
	if path != wire {
		t.Fatalf("wire path = %q, want %q", path, wire)
	}
	events := `{"type":"usage.record","model":"deepseek/deepseek-flash","usage":{"inputOther":7,"output":5,"inputCacheRead":3,"inputCacheCreation":2},"usageScope":"turn"}` + "\n" +
		`{"type":"token_counting.measured","tokens":123,"length":3}` + "\n" +
		`{"type":"usage.record","model":"deepseek/deepseek-flash","usage":{"inputOther":11,"output":13,"inputCacheRead":17,"inputCacheCreation":19},"usageScope":"turn"}` + "\n" +
		`{"type":"token_counting.turn_recorded","tokens":170,"length":7}` + "\n"
	file, err := os.OpenFile(wire, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = file.WriteString(events); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err = file.Close(); err != nil {
		t.Fatal(err)
	}
	usage, update := readWireUsage(wire, int64(len(history)), filepath.Join(home, "config.toml"))
	if usage == nil || usage.InputTokens != 18 || usage.OutputTokens != 18 ||
		*usage.CachedReadTokens != 20 || *usage.CachedWriteTokens != 21 ||
		usage.TotalTokens != 77 {
		t.Fatalf("wrong current usage: %+v", usage)
	}
	if update == nil || update.Used != 170 || update.Size != 1000000 {
		t.Fatalf("wrong current context: %+v", update)
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

// TestKimiCurrentSessionReportsUsage 验证新版会话目录经完整 Agent.Prompt 返回本轮 Token。
func TestKimiCurrentSessionReportsUsage(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cwd := t.TempDir()
	codeDirectory := t.TempDir()
	config := `[models."deepseek/deepseek-flash"]
max_context_size = 1000000
`
	if err := os.WriteFile(filepath.Join(codeDirectory, "config.toml"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	agent, err := NewAgent(context.Background(), Config{
		KimiPath:         executable,
		PrefixArgs:       []string{"-test.run=^TestKimiCurrentUsageACPProcess$", "--"},
		Environment:      append(os.Environ(), "ACP_GO_KIMI_CURRENT_USAGE_FIXTURE=1", "KIMI_CODE_HOME="+codeDirectory),
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
	session, err := agent.NewSession(ctx, acp.NewSessionRequest{Cwd: cwd})
	if err != nil {
		t.Fatal(err)
	}
	result, err := agent.Prompt(ctx, acp.PromptRequest{
		SessionId: session.SessionId,
		Prompt:    []acp.ContentBlock{acp.TextBlock("usage")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Usage == nil || result.Usage.InputTokens != 7 || result.Usage.OutputTokens != 5 ||
		*result.Usage.CachedReadTokens != 3 || *result.Usage.CachedWriteTokens != 2 {
		t.Fatalf("新版会话缺少本轮用量：%+v", result.Usage)
	}
}

// TestKimiCurrentUsageACPProcess 提供新版索引和顶层 usage 事件的原生 ACP 替身。
func TestKimiCurrentUsageACPProcess(t *testing.T) {
	if os.Getenv("ACP_GO_KIMI_CURRENT_USAGE_FIXTURE") != "1" {
		return
	}
	const sessionID = "session-current"
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
			}, nil
		case "session/new":
			cwd, _ = request["cwd"].(string)
			home := os.Getenv("KIMI_CODE_HOME")
			sessionDirectory := filepath.Join(home, "sessions", "wd_workspace", sessionID)
			wire := filepath.Join(sessionDirectory, "agents", "main", "wire.jsonl")
			if err := os.MkdirAll(filepath.Dir(wire), 0o700); err != nil {
				return nil, acp.NewInternalError(nil)
			}
			index := `{"sessionDir":` + strconv.Quote(sessionDirectory) + `,"sessionId":"session-current","workDir":` + strconv.Quote(cwd) + `}` + "\n"
			if err := os.WriteFile(filepath.Join(home, "session_index.jsonl"), []byte(index), 0o600); err != nil {
				return nil, acp.NewInternalError(nil)
			}
			if err := os.WriteFile(wire, []byte(`{"type":"metadata"}`+"\n"), 0o600); err != nil {
				return nil, acp.NewInternalError(nil)
			}
			return map[string]any{"sessionId": sessionID}, nil
		case "session/prompt":
			home := os.Getenv("KIMI_CODE_HOME")
			wire := filepath.Join(home, "sessions", "wd_workspace", sessionID, "agents", "main", "wire.jsonl")
			events := `{"type":"usage.record","model":"deepseek/deepseek-flash","usage":{"inputOther":7,"output":5,"inputCacheRead":3,"inputCacheCreation":2},"usageScope":"turn"}` + "\n" +
				`{"type":"token_counting.turn_recorded","tokens":120,"length":3}` + "\n"
			file, err := os.OpenFile(wire, os.O_APPEND|os.O_WRONLY, 0o600)
			if err != nil {
				return nil, acp.NewInternalError(nil)
			}
			if _, err = file.WriteString(events); err != nil {
				_ = file.Close()
				return nil, acp.NewInternalError(nil)
			}
			if err = file.Close(); err != nil {
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

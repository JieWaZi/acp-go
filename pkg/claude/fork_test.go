package claude

import (
	"context"
	"encoding/json"
	"fmt"
	acp "github.com/coder/acp-go-sdk"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// TestForkNativeTranscriptPreservesToolsAndIndependentIdentity 验证原生分叉身份、边界及隔离约束。
func TestForkNativeTranscriptPreservesToolsAndIndependentIdentity(t *testing.T) {
	home := t.TempDir()
	directory := filepath.Join(home, "projects", "project")
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	original := `{"type":"user","uuid":"u","parentUuid":null,"sessionId":"parent","message":{"role":"user","content":"before"}}
{"type":"progress","uuid":"p","parentUuid":"u","sessionId":"parent"}
{"type":"assistant","uuid":"a","parentUuid":"p","sessionId":"parent","message":{"role":"assistant","content":[{"type":"tool_use","id":"tool","name":"Read","input":{"path":"source"}}]}}
{"type":"user","uuid":"r","parentUuid":"a","sessionId":"parent","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"tool","content":"context-marker"}]}}
{"type":"user","uuid":"side","isSidechain":true,"sessionId":"parent","message":{"role":"user","content":"sidechain-must-not-leak"}}
`
	source := filepath.Join(directory, "parent.jsonl")
	if err := os.WriteFile(source, []byte(original), 0600); err != nil {
		t.Fatal(err)
	}
	count := 0
	agent := &Agent{environment: []string{"CLAUDE_CONFIG_DIR=" + home}, idGenerator: func() (string, error) { count++; return fmt.Sprintf("new-%d", count), nil }}
	targetCwd := t.TempDir()
	if err := agent.persistForkHistory("parent", "child", targetCwd); err != nil {
		t.Fatal(err)
	}
	path, err := findHistoryPath("child", agent.environment)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := filepath.EvalSymlinks(targetCwd)
	if err != nil {
		t.Fatal(err)
	}
	expected := filepath.Join(home, "projects", regexp.MustCompile(`[^a-zA-Z0-9]`).ReplaceAllString(canonical, "-"), "child.jsonl")
	if path != expected {
		t.Fatalf("CLI 按目标 cwd 查找 transcript，实际 %s，期望 %s", path, expected)
	}
	data, _ := os.ReadFile(path)
	text := string(data)
	if !strings.Contains(text, "context-marker") || !strings.Contains(text, "tool_result") || strings.Contains(text, "sidechain-must-not-leak") || strings.Contains(text, `"type":"progress"`) {
		t.Fatal(text)
	}
	lines := strings.Split(strings.TrimSpace(text), "\n")
	var entries []map[string]any
	for _, line := range lines {
		var entry map[string]any
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatal(err)
		}
		entries = append(entries, entry)
	}
	if entries[1]["parentUuid"] != entries[0]["uuid"] || entries[0]["sessionId"] != "child" || entries[len(entries)-1]["relocatedCwd"] != targetCwd {
		t.Fatal(entries)
	}
	before, _ := os.ReadFile(source)
	if string(before) != original {
		t.Fatal("parent mutated")
	}
	if err := os.Remove(source); err != nil {
		t.Fatal(err)
	}
	if _, err := findHistoryPath("child", agent.environment); err != nil {
		t.Fatal("cold child depended on parent", err)
	}
	if _, err := findHistoryPath("child", []string{"CLAUDE_CONFIG_DIR=" + t.TempDir()}); err == nil {
		t.Fatal("custom profile isolation lost")
	}
	_, err = (&Agent{initialized: true}).UnstableForkSession(context.Background(), acp.UnstableForkSessionRequest{Meta: map[string]any{"forkPosition": "earlier"}})
	if err == nil {
		t.Fatal("unsupported historical position accepted")
	}
}

// TestClaudeForkRejectsActiveSource 验证未完成的 Prompt 不能被复制成子历史。
func TestClaudeForkRejectsActiveSource(t *testing.T) {
	sessions := newClaudeSessionStore()
	source := &claudeSession{id: "parent", turns: make(chan *claudeTurn, 1)}
	sessions.install(source)
	agent := &Agent{initialized: true, sessions: sessions}
	source.forkMu.RLock()
	defer source.forkMu.RUnlock()
	_, err := agent.UnstableForkSession(context.Background(), acp.UnstableForkSessionRequest{SessionId: "parent", Cwd: t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "active") {
		t.Fatalf("active source forked: %v", err)
	}
}

// TestClaudeForkOpensAndColdResumesNewNativeIdentity 验证实际 CLI 传输收到独立 resume 身份。
func TestClaudeForkOpensAndColdResumesNewNativeIdentity(t *testing.T) {
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	directory := filepath.Join(home, "projects", "parent-project")
	if err = os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(directory, "parent.jsonl"), []byte(`{"type":"user","uuid":"u","sessionId":"parent","message":{"role":"user","content":"native-context"}}`+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	config := Config{ClaudePath: binary, Environment: append(os.Environ(), fakeClaudeProcessEnv+"=1", "CLAUDE_CONFIG_DIR="+home), Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	agent, err := NewAgent(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = agent.Initialize(ctx, acp.InitializeRequest{ProtocolVersion: 1}); err != nil {
		t.Fatal(err)
	}
	cwd := t.TempDir()
	child, err := agent.UnstableForkSession(ctx, acp.UnstableForkSessionRequest{SessionId: "parent", Cwd: cwd, Meta: map[string]any{"forkReceiptDirectory": t.TempDir()}})
	if err != nil {
		t.Fatal(err)
	}
	if child.SessionId == "parent" || child.SessionId == "" {
		t.Fatal("parent identity reused")
	}
	if err = agent.Close(ctx); err != nil {
		t.Fatal(err)
	}
	cold, err := NewAgent(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer cold.Close(context.Background())
	if _, err = cold.Initialize(ctx, acp.InitializeRequest{ProtocolVersion: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err = cold.ResumeSession(ctx, acp.ResumeSessionRequest{SessionId: child.SessionId, Cwd: cwd}); err != nil {
		t.Fatal(err)
	}
	if _, err = cold.Prompt(ctx, acp.PromptRequest{SessionId: child.SessionId, Prompt: []acp.ContentBlock{acp.TextBlock("hello")}}); err != nil {
		t.Fatal(err)
	}
}

// TestClaudeProjectKeyMatchesUpstreamVectors 验证上游 JavaScript 对 Unicode 和长路径的固定计算结果。
func TestClaudeProjectKeyMatchesUpstreamVectors(t *testing.T) {
	for input, expected := range map[string]string{
		"/workspace/中文/🙂":              "-workspace------",
		"/" + strings.Repeat("a", 210): "-" + strings.Repeat("a", 199) + "-djaaup",
		"/" + strings.Repeat("🙂", 110): strings.Repeat("-", 200) + "-4zkkrv",
	} {
		if got := claudeProjectKey(input); got != expected {
			t.Fatalf("项目键 %q，期望 %q", got, expected)
		}
	}
}

// TestReconcileForkRelocatesOnlyConfirmedChild 验证恢复已确认原生身份时重绑子文件且保留父文件。
func TestReconcileForkRelocatesOnlyConfirmedChild(t *testing.T) {
	root, cwd := t.TempDir(), t.TempDir()
	source := filepath.Join(root, "projects", "source")
	if err := os.MkdirAll(source, 0700); err != nil {
		t.Fatal(err)
	}
	content := []byte("native-child-context\n")
	for _, id := range []string{"parent", "child"} {
		if err := os.WriteFile(filepath.Join(source, id+".jsonl"), content, 0600); err != nil {
			t.Fatal(err)
		}
	}
	agent := &Agent{environment: []string{"CLAUDE_CONFIG_DIR=" + root}}
	if err := agent.reconcileForkHistory("child", cwd); err != nil {
		t.Fatal(err)
	}
	child, err := findHistoryPath("child", agent.environment)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(child) == source {
		t.Fatal("仍绑定父工作区")
	}
	data, err := os.ReadFile(child)
	if err != nil || string(data) != string(content) {
		t.Fatal("原生上下文改变", err)
	}
	if _, err := os.Stat(filepath.Join(source, "parent.jsonl")); err != nil {
		t.Fatal("父会话被移动", err)
	}
	if err := agent.reconcileForkHistory("child", cwd); err != nil {
		t.Fatal("重试不幂等", err)
	}
}

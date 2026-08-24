package claude

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JieWaZi/acp-go/pkg/claude/protocol"
)

// TestReplaySessionHistory 验证 load 保持消息顺序、过滤本地命令并映射工具生命周期。
func TestReplaySessionHistory(t *testing.T) {
	configDirectory := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", configDirectory)
	projectDirectory := filepath.Join(configDirectory, "projects", "-workspace")
	if err := os.MkdirAll(projectDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	transcript := strings.Join([]string{
		`{"type":"user","uuid":"u1","parent_tool_use_id":null,"message":{"role":"user","content":[{"type":"text","text":"<command-name>/model</command-name>hello"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"IMAGE"}}]}}`,
		`{"type":"assistant","uuid":"a1","parent_tool_use_id":null,"message":{"id":"m1","role":"assistant","content":[{"type":"text","text":"answer"},{"type":"tool_use","id":"t1","name":"Read","input":{"file_path":"/tmp/a"}}]}}`,
		`{"type":"user","uuid":"u2","parent_tool_use_id":null,"message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"done"}]}}`,
	}, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(projectDirectory, "history-session.jsonl"), []byte(transcript), 0o600); err != nil {
		t.Fatal(err)
	}
	client := &recordingClaudeClient{}
	agent := &Agent{logger: slog.Default(), updater: client}
	session := &claudeSession{
		agent: agent, id: "history-session", tools: make(map[string]*toolState), tasks: make(map[string]taskState),
	}
	if err := agent.replaySessionHistory(context.Background(), session); err != nil {
		t.Fatal(err)
	}
	updates, _ := client.snapshot()
	if len(updates) != 5 || updates[0].Update.UserMessageChunk == nil ||
		updates[0].Update.UserMessageChunk.Content.Text == nil || updates[0].Update.UserMessageChunk.Content.Text.Text != "hello" ||
		updates[1].Update.UserMessageChunk == nil || updates[1].Update.UserMessageChunk.Content.Image == nil ||
		updates[2].Update.AgentMessageChunk == nil || updates[3].Update.ToolCall == nil || updates[4].Update.ToolCallUpdate == nil {
		t.Fatalf("history updates = %#v", updates)
	}
}

// TestCompleteUnknownToolPublishesLifecycle 验证缺失开始事件的工具结果会补齐 generic 开始与完成更新。
func TestCompleteUnknownToolPublishesLifecycle(t *testing.T) {
	client := &recordingClaudeClient{}
	agent := &Agent{logger: slog.Default(), updater: client}
	session := &claudeSession{agent: agent, id: "session", tools: make(map[string]*toolState)}
	if err := session.completeTool(context.Background(), protocol.ContentBlock{
		Type: "tool_result", ToolUseID: "unknown-tool", Content: json.RawMessage(`"done"`),
	}); err != nil {
		t.Fatal(err)
	}
	updates, _ := client.snapshot()
	if len(updates) != 2 || updates[0].Update.ToolCall == nil || updates[1].Update.ToolCallUpdate == nil {
		t.Fatalf("tool updates = %#v", updates)
	}
}

// TestFindHistoryPathRejectsUnsafeSessionID 验证路径和 Glob 元字符不能逃逸 transcript 文件名。
func TestFindHistoryPathRejectsUnsafeSessionID(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	for _, sessionID := range []string{"../secret", "*", "nested/session"} {
		if _, err := findHistoryPath(sessionID); !errors.Is(err, ErrInvalidClaudeHistorySessionID) {
			t.Fatalf("findHistoryPath(%q) error = %v", sessionID, err)
		}
	}
}

// TestFindHistoryPathNotFound 验证缺失 transcript 返回稳定错误。
func TestFindHistoryPathNotFound(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	if _, err := findHistoryPath("missing"); !strings.Contains(err.Error(), ErrClaudeHistoryNotFound.Error()) {
		t.Fatalf("error = %v", err)
	}
}

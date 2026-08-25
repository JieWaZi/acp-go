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

	acp "github.com/coder/acp-go-sdk"
)

// TestReplaySessionHistory 验证 load 保持消息顺序、过滤本地命令并映射工具生命周期。
func TestReplaySessionHistory(t *testing.T) {
	configDirectory := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
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
	agent := &Agent{
		logger:      slog.Default(),
		updater:     client,
		environment: []string{"CLAUDE_CONFIG_DIR=" + configDirectory},
	}
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
	}, nil); err != nil {
		t.Fatal(err)
	}
	updates, _ := client.snapshot()
	if len(updates) != 2 || updates[0].Update.ToolCall == nil || updates[1].Update.ToolCallUpdate == nil {
		t.Fatalf("tool updates = %#v", updates)
	}
}

// TestCompleteDiffToolUsesStructuredPatch 验证 Edit/Write 完成态按 Claude upstream 修正标准 diff。
func TestCompleteDiffToolUsesStructuredPatch(t *testing.T) {
	t.Parallel()

	client := &recordingClaudeClient{}
	agent := &Agent{logger: slog.Default(), updater: client}
	session := &claudeSession{agent: agent, id: "session", tools: make(map[string]*toolState)}
	if err := session.startTool(context.Background(), protocol.ContentBlock{
		Type:  "tool_use",
		ID:    "edit-1",
		Name:  "Edit",
		Input: json.RawMessage(`{"file_path":"/work/file.ts","old_string":"old","new_string":"new"}`),
	}); err != nil {
		t.Fatal(err)
	}
	toolUseResult := json.RawMessage(`{
		"filePath":"/work/file.ts",
		"structuredPatch":[{"oldStart":3,"oldLines":3,"newStart":3,"newLines":3,"lines":[" before","-old","+new"," after"]}]
	}`)
	if err := session.completeTool(context.Background(), protocol.ContentBlock{
		Type: "tool_result", ToolUseID: "edit-1", Content: json.RawMessage(`"updated"`),
	}, toolUseResult); err != nil {
		t.Fatal(err)
	}
	updates, _ := client.snapshot()
	if len(updates) != 2 || updates[1].Update.ToolCallUpdate == nil {
		t.Fatalf("tool updates = %#v", updates)
	}
	completed := updates[1].Update.ToolCallUpdate
	if len(completed.Content) != 1 || completed.Content[0].Diff == nil ||
		completed.Content[0].Diff.OldText == nil || *completed.Content[0].Diff.OldText != "before\nold\nafter" ||
		completed.Content[0].Diff.NewText != "before\nnew\nafter" ||
		len(completed.Locations) != 1 || completed.Locations[0].Line == nil || *completed.Locations[0].Line != 3 {
		t.Fatalf("completed diff update = %#v", completed)
	}
}

// TestCompleteDiffToolWithoutStructuredPatchKeepsOptimisticDiff 验证普通结果不会清空开始态文件 diff。
func TestCompleteDiffToolWithoutStructuredPatchKeepsOptimisticDiff(t *testing.T) {
	t.Parallel()

	client := &recordingClaudeClient{}
	agent := &Agent{logger: slog.Default(), updater: client}
	session := &claudeSession{agent: agent, id: "session", tools: make(map[string]*toolState)}
	if err := session.startTool(context.Background(), protocol.ContentBlock{
		Type:  "tool_use",
		ID:    "edit-without-patch",
		Name:  "Edit",
		Input: json.RawMessage(`{"file_path":"/work/file.ts","old_string":"old","new_string":"new"}`),
	}); err != nil {
		t.Fatal(err)
	}
	if err := session.completeTool(context.Background(), protocol.ContentBlock{
		Type: "tool_result", ToolUseID: "edit-without-patch", Content: json.RawMessage(`"updated"`),
	}, nil); err != nil {
		t.Fatal(err)
	}
	updates, _ := client.snapshot()
	if len(updates) != 2 || updates[1].Update.ToolCallUpdate == nil {
		t.Fatalf("tool updates = %#v", updates)
	}
	completed := updates[1].Update.ToolCallUpdate
	if completed.Status == nil || *completed.Status != acp.ToolCallStatusCompleted ||
		len(completed.Content) != 0 || completed.RawOutput == nil {
		t.Fatalf("completed update = %#v", completed)
	}
}

// TestFindHistoryPathRejectsUnsafeSessionID 验证路径和 Glob 元字符不能逃逸 transcript 文件名。
func TestFindHistoryPathRejectsUnsafeSessionID(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	for _, sessionID := range []string{"../secret", "*", "nested/session"} {
		if _, err := findHistoryPath(sessionID, nil); !errors.Is(err, ErrInvalidClaudeHistorySessionID) {
			t.Fatalf("findHistoryPath(%q) error = %v", sessionID, err)
		}
	}
}

// TestFindHistoryPathNotFound 验证缺失 transcript 返回稳定错误。
func TestFindHistoryPathNotFound(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	if _, err := findHistoryPath("missing", nil); !strings.Contains(err.Error(), ErrClaudeHistoryNotFound.Error()) {
		t.Fatalf("error = %v", err)
	}
}

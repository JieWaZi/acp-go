package claude

import (
	"testing"

	acp "github.com/coder/acp-go-sdk"
)

// TestToolInfoFromToolUseMatchesClaudeUpstream 验证常见工具使用标准 ACP 语义。
func TestToolInfoFromToolUseMatchesClaudeUpstream(t *testing.T) {
	t.Parallel()

	tests := []struct {
		// name 是 Claude 工具名称。
		name string
		// input 是 Claude 工具原始输入。
		input map[string]any
		// wantTitle 是 upstream 约定的 ACP 标题。
		wantTitle string
		// wantKind 是 upstream 约定的 ACP 工具类别。
		wantKind acp.ToolKind
		// wantContent 表示工具卡片必须携带标准 ACP 内容。
		wantContent bool
	}{
		{name: "Task", input: map[string]any{"description": "Inspect runtime", "prompt": "Read the runtime code"}, wantTitle: "Inspect runtime", wantKind: acp.ToolKindThink, wantContent: true},
		{name: "Read", input: map[string]any{"file_path": "/work/main.go", "offset": float64(3), "limit": float64(5)}, wantTitle: "Read /work/main.go (3 - 7)", wantKind: acp.ToolKindRead},
		{name: "Write", input: map[string]any{"file_path": "/work/main.go", "content": "package main\n"}, wantTitle: "Write /work/main.go", wantKind: acp.ToolKindEdit, wantContent: true},
		{name: "Edit", input: map[string]any{"file_path": "/work/main.go", "old_string": "old", "new_string": "new"}, wantTitle: "Edit /work/main.go", wantKind: acp.ToolKindEdit, wantContent: true},
		{name: "Glob", input: map[string]any{"path": "/work", "pattern": "**/*.go"}, wantTitle: "Find `/work` `**/*.go`", wantKind: acp.ToolKindSearch},
		{name: "Grep", input: map[string]any{"pattern": "Runtime", "path": "/work"}, wantTitle: "grep \"Runtime\" /work", wantKind: acp.ToolKindSearch},
		{name: "WebFetch", input: map[string]any{"url": "https://example.com", "prompt": "Summarize"}, wantTitle: "Fetch https://example.com", wantKind: acp.ToolKindFetch, wantContent: true},
		{name: "WebSearch", input: map[string]any{"query": "ACP protocol", "allowed_domains": []any{"agentclientprotocol.com"}}, wantTitle: "\"ACP protocol\" (allowed: agentclientprotocol.com)", wantKind: acp.ToolKindFetch},
		{name: "TodoWrite", input: map[string]any{"todos": []any{map[string]any{"content": "Map tools"}}}, wantTitle: "Update TODOs: Map tools", wantKind: acp.ToolKindThink},
		{name: "Skill", input: map[string]any{"skill": "diagnosing-bugs"}, wantTitle: "Load skill: diagnosing-bugs", wantKind: acp.ToolKindOther},
		{name: "AskUserQuestion", input: map[string]any{"questions": []any{map[string]any{"question": "Continue?"}}}, wantTitle: "Continue?", wantKind: acp.ToolKindOther, wantContent: true},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			info := toolInfoFromToolUse(test.name, test.input)
			if info.Title != test.wantTitle || info.Kind != test.wantKind {
				t.Fatalf("tool info = %#v", info)
			}
			if test.wantContent && len(info.Content) == 0 {
				t.Fatalf("tool content is empty: %#v", info)
			}
		})
	}
}

// TestToolInfoFromToolUsePreservesMCPIdentity 验证 MCP 名称不会被基础库改写为产品文案。
func TestToolInfoFromToolUsePreservesMCPIdentity(t *testing.T) {
	t.Parallel()

	info := toolInfoFromToolUse(
		"mcp__github__search_code",
		map[string]any{"query": "runtime"},
	)
	if info.Title != "mcp.github.search_code" || info.Kind != acp.ToolKindExecute ||
		info.Meta["is_mcp_tool_call"] != true {
		t.Fatalf("MCP tool info = %#v", info)
	}
	rawInput, ok := info.RawInput.(map[string]any)
	if !ok || rawInput["server"] != "github" || rawInput["tool"] != "search_code" ||
		rawInput["arguments"] == nil {
		t.Fatalf("MCP raw input = %#v", info.RawInput)
	}
}

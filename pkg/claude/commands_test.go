package claude

import (
	"reflect"
	"testing"

	"github.com/JieWaZi/acp-go/pkg/claude/protocol"
)

// TestAvailableSlashCommandsMatchesClaudeUpstream 验证过滤、MCP 重命名和参数提示与 upstream 一致。
func TestAvailableSlashCommandsMatchesClaudeUpstream(t *testing.T) {
	t.Parallel()
	commands := []protocol.SlashCommand{
		{Name: "diagnosing-bugs", Description: "Diagnose a bug", ArgumentHint: "<symptom>"},
		{Name: "doctor", Description: "Inspect the terminal"},
		{Name: "clear", Description: "Clear terminal state"},
		{Name: "github (MCP)", Description: "Use GitHub MCP", ArgumentHint: []any{"<owner>", "<repo>"}},
	}
	got := availableSlashCommands(commands, []string{"doctor"})
	if len(got) != 2 {
		t.Fatalf("available commands = %#v", got)
	}
	if got[0].Name != "diagnosing-bugs" || got[0].Description != "Diagnose a bug" ||
		got[0].Input == nil || got[0].Input.Unstructured == nil ||
		got[0].Input.Unstructured.Hint != "<symptom>" {
		t.Fatalf("skill command = %#v", got[0])
	}
	if got[1].Name != "mcp:github" || got[1].Input == nil || got[1].Input.Unstructured == nil ||
		got[1].Input.Unstructured.Hint != "<owner> <repo>" {
		t.Fatalf("MCP command = %#v", got[1])
	}
}

// TestUnsupportedSlashCommandsMatchesClaudeUpstream 锁定固定 upstream 的静态隐藏列表。
func TestUnsupportedSlashCommandsMatchesClaudeUpstream(t *testing.T) {
	t.Parallel()
	want := []string{"clear", "cost", "keybindings-help", "login", "logout", "output-style:new", "release-notes", "todos"}
	got := make([]string, 0, len(want))
	for _, name := range want {
		if isUnsupportedSlashCommand(name) {
			got = append(got, name)
		}
	}
	if !reflect.DeepEqual(got, want) || isUnsupportedSlashCommand("compact") {
		t.Fatalf("unsupported commands = %#v", got)
	}
}

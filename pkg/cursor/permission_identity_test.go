package cursor

import "testing"

// TestVisiblePermissionRejectsBatchMisattribution 回放真实并行 Read 与 Shell 检查点，批准身份必须对应审批区域中的 Shell。
func TestVisiblePermissionRejectsBatchMisattribution(t *testing.T) {
	screen := "Read sample.txt\n$ old-command Waiting for approval...\n────────────────────────\n$ printf rejected > denied.txt in .\nRun this command?\nRun (once) (y)"
	read := pendingCall{id: "read", name: "Read", args: map[string]any{"path": "sample.txt"}}
	shell := pendingCall{id: "shell", name: "Shell", args: map[string]any{"command": "printf rejected > denied.txt"}}
	old := pendingCall{id: "old", name: "Shell", args: map[string]any{"command": "old-command"}}
	got, ok := visiblePermission(screen, []pendingCall{read, old, shell})
	if !ok || got.id != "shell" {
		t.Fatalf("wrong approval: %+v %t", got, ok)
	}
	duplicate := shell
	duplicate.id = "another-shell"
	if _, ok = visiblePermission(screen, []pendingCall{shell, duplicate}); ok {
		t.Fatal("ambiguous identity guessed")
	}
	if permissionMatches(screen, read) || permissionMatches(screen, old) {
		t.Fatal("unrelated approval accepted")
	}
}

// TestVisibleMCPPermissionUsesServerAndTool 验证并行 MCP 审批按真实命名空间及工具对应，不误选另一个服务的待处理调用。
func TestVisibleMCPPermissionUsesServerAndTool(t *testing.T) {
	screen := "────────────────────────\nplugin-plugin-ally_acceptance_echo: echo\n{\"text\":\"marker\"}\nRun this MCP tool?\nRun (once) (y)"
	other := pendingCall{id: "other", name: "CallDynamicTool", args: map[string]any{"namespace": "other", "toolName": "echo"}}
	selected := pendingCall{id: "selected", name: "CallDynamicTool", args: map[string]any{"namespace": "plugin-plugin-ally_acceptance_echo", "toolName": "echo"}}
	got, ok := visiblePermission(screen, []pendingCall{other, selected})
	if !ok || got.id != "selected" {
		t.Fatalf("wrong MCP approval: %+v %t", got, ok)
	}
}

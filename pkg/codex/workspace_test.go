package codex

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/JieWaZi/acp-go/pkg/codex/protocol"
)

// TestNormalizeCodexWorkspace 验证附加目录保持有序、去重并拒绝非法路径。
func TestNormalizeCodexWorkspace(t *testing.T) {
	t.Parallel()

	scope, err := normalizeCodexWorkspace(
		"/workspace",
		[]string{"/shared", "/workspace", "/shared", "/generated"},
	)
	if err != nil {
		t.Fatalf("normalizeCodexWorkspace 返回错误：%v", err)
	}
	if !reflect.DeepEqual(scope.AdditionalDirectories, []string{"/shared", "/generated"}) {
		t.Fatalf("AdditionalDirectories = %#v", scope.AdditionalDirectories)
	}

	for _, invalid := range [][]string{{""}, {"relative"}} {
		if _, err = normalizeCodexWorkspace("/workspace", invalid); err == nil {
			t.Fatalf("非法附加目录 %#v 未返回错误", invalid)
		}
	}
}

// TestCodexWorkspaceConfigAddsTrustedProjectsAndWritableRoots 验证 Session 配置覆盖全部授权根。
func TestCodexWorkspaceConfigAddsTrustedProjectsAndWritableRoots(t *testing.T) {
	t.Parallel()

	config, err := codexWorkspaceConfig(codexWorkspace{
		CWD:                   "/workspace",
		AdditionalDirectories: []string{"/shared", "/generated"},
	})
	if err != nil {
		t.Fatalf("codexWorkspaceConfig 返回错误：%v", err)
	}
	var projects map[string]map[string]string
	if err = json.Unmarshal(config["projects"], &projects); err != nil {
		t.Fatalf("解析 projects 失败：%v", err)
	}
	for _, root := range []string{"/workspace", "/shared", "/generated"} {
		if projects[root]["trust_level"] != "trusted" {
			t.Fatalf("projects[%q] = %#v", root, projects[root])
		}
	}
	var sandbox protocol.SandboxWorkspaceWriteClass
	if err = json.Unmarshal(config["sandbox_workspace_write"], &sandbox); err != nil {
		t.Fatalf("解析 sandbox_workspace_write 失败：%v", err)
	}
	if !reflect.DeepEqual(sandbox.WritableRoots, []string{"/shared", "/generated"}) {
		t.Fatalf("WritableRoots = %#v", sandbox.WritableRoots)
	}
}

// TestSandboxPolicyWithAdditionalDirectories 只在 workspaceWrite 模式扩展可写根。
func TestSandboxPolicyWithAdditionalDirectories(t *testing.T) {
	t.Parallel()

	policy := protocol.SandboxPolicy{
		Type:          protocol.SandboxPolicyTypeWorkspaceWrite,
		WritableRoots: []string{"/existing"},
	}
	updated := sandboxPolicyWithAdditionalDirectories(policy, []string{"/shared", "/existing"})
	if !reflect.DeepEqual(updated.WritableRoots, []string{"/existing", "/shared"}) {
		t.Fatalf("WritableRoots = %#v", updated.WritableRoots)
	}

	readOnly := protocol.SandboxPolicy{Type: protocol.SandboxPolicyTypeReadOnly}
	if got := sandboxPolicyWithAdditionalDirectories(readOnly, []string{"/shared"}); len(got.WritableRoots) != 0 {
		t.Fatalf("read-only WritableRoots = %#v", got.WritableRoots)
	}
}

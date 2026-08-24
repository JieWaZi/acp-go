package codex

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/JieWaZi/acp-go/pkg/codex/protocol"
)

// codexWorkspace 保存一次 ACP Session 冻结的主工作目录与附加授权目录。
type codexWorkspace struct {
	// CWD 是 Codex thread 使用的主工作目录。
	CWD string
	// AdditionalDirectories 是不含 CWD 且顺序稳定的附加授权目录。
	AdditionalDirectories []string
}

// normalizeCodexWorkspace 校验绝对路径，并按首次出现顺序去重附加目录。
func normalizeCodexWorkspace(
	cwd string,
	additionalDirectories []string,
) (codexWorkspace, error) {
	if cwd == "" || !filepath.IsAbs(cwd) {
		return codexWorkspace{}, fmt.Errorf("Codex cwd must be an absolute path")
	}
	cleanCWD := filepath.Clean(cwd)
	seen := map[string]struct{}{cleanCWD: {}}
	result := make([]string, 0, len(additionalDirectories))
	for _, directory := range additionalDirectories {
		if directory == "" || !filepath.IsAbs(directory) {
			return codexWorkspace{}, fmt.Errorf(
				"Codex additional directory %q must be an absolute path",
				directory,
			)
		}
		cleaned := filepath.Clean(directory)
		if _, exists := seen[cleaned]; exists {
			continue
		}
		seen[cleaned] = struct{}{}
		result = append(result, cleaned)
	}
	return codexWorkspace{
		CWD:                   cleanCWD,
		AdditionalDirectories: result,
	}, nil
}

// roots 返回以 CWD 开头的完整有效根集合副本。
func (w codexWorkspace) roots() []string {
	roots := make([]string, 0, 1+len(w.AdditionalDirectories))
	roots = append(roots, w.CWD)
	roots = append(roots, w.AdditionalDirectories...)
	return roots
}

// skillExtraRoots 返回 additional root 中符合 Codex 约定的 Skill 目录。
func (w codexWorkspace) skillExtraRoots() []string {
	roots := make([]string, 0, len(w.AdditionalDirectories))
	for _, directory := range w.AdditionalDirectories {
		roots = append(roots, filepath.Join(directory, ".agents", "skills"))
	}
	return roots
}

// hasSkillDirectory 判断任一有效根是否存在标准 `.agents/skills` 目录。
func (w codexWorkspace) hasSkillDirectory() (bool, error) {
	for _, root := range w.roots() {
		path := filepath.Join(root, ".agents", "skills")
		info, err := os.Stat(path)
		switch {
		case err == nil && info.IsDir():
			return true, nil
		case err == nil:
			return false, fmt.Errorf("Codex Skill path %q is not a directory", path)
		case os.IsNotExist(err):
			continue
		default:
			return false, fmt.Errorf("checking Codex Skill path %q: %w", path, err)
		}
	}
	return false, nil
}

// codexWorkspaceConfig 构造 Session 级 trusted projects 与 workspace-write roots。
func codexWorkspaceConfig(workspace codexWorkspace) (map[string]json.RawMessage, error) {
	projects := make(map[string]map[string]string, len(workspace.roots()))
	for _, root := range workspace.roots() {
		projects[root] = map[string]string{"trust_level": "trusted"}
	}
	rawProjects, err := json.Marshal(projects)
	if err != nil {
		return nil, fmt.Errorf("encoding Codex trusted projects: %w", err)
	}
	config := map[string]json.RawMessage{"projects": rawProjects}
	if len(workspace.AdditionalDirectories) == 0 {
		return config, nil
	}
	rawSandbox, err := json.Marshal(map[string]any{
		"writable_roots": workspace.AdditionalDirectories,
	})
	if err != nil {
		return nil, fmt.Errorf("encoding Codex workspace-write roots: %w", err)
	}
	config["sandbox_workspace_write"] = rawSandbox
	return config, nil
}

// sandboxPolicyWithAdditionalDirectories 把附加根合并到 workspaceWrite 策略副本。
func sandboxPolicyWithAdditionalDirectories(
	policy protocol.SandboxPolicy,
	additionalDirectories []string,
) protocol.SandboxPolicy {
	policy.WritableRoots = append([]string{}, policy.WritableRoots...)
	if policy.Type != protocol.SandboxPolicyTypeWorkspaceWrite {
		return policy
	}
	seen := make(map[string]struct{}, len(policy.WritableRoots)+len(additionalDirectories))
	for _, root := range policy.WritableRoots {
		seen[root] = struct{}{}
	}
	for _, root := range additionalDirectories {
		if _, exists := seen[root]; exists {
			continue
		}
		seen[root] = struct{}{}
		policy.WritableRoots = append(policy.WritableRoots, root)
	}
	return policy
}

package claude

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/JieWaZi/acp-go/pkg/agent_next/claude/protocol"
)

const writeHookID = "neux.workspace.write"

type workspacePolicy struct {
	cwd      string
	roots    []string
	denied   []string
	settings string
}

func newWorkspacePolicy(o LaunchOptions, cwd string, additional []string) (*workspacePolicy, error) {
	if !o.WorkspaceOnly {
		return nil, nil
	}
	p := &workspacePolicy{cwd: cwd, roots: append([]string{cwd}, additional...)}
	temp, err := realDirectory(os.TempDir())
	if err != nil {
		return nil, err
	}
	p.roots = append(p.roots, temp)
	slices.Sort(p.roots)
	p.roots = slices.Compact(p.roots)
	settings := make(map[string]any)
	if o.SettingsPath != "" {
		data, err := readBoundedFile(o.SettingsPath, 1<<20)
		if err != nil || json.Unmarshal(data, &settings) != nil || settings == nil {
			return nil, errors.New("Claude workspace settings require a bounded JSON object")
		}
	}
	paths := append(slices.Clone(o.ExcludedWriteDirectories), o.PluginDirectories...)
	if o.SettingsPath != "" {
		paths = append(paths, o.SettingsPath)
	}
	for _, root := range p.roots {
		paths = append(paths, filepath.Join(root, ".claude", "settings.json"), filepath.Join(root, ".claude", "settings.local.json"))
	}
	for _, raw := range paths {
		path, err := resolveWorkspacePath(p.cwd, raw)
		if err != nil {
			return nil, err
		}
		p.denied = append(p.denied, path)
	}
	settings["sandbox"] = map[string]any{
		"enabled": true, "failIfUnavailable": true, "allowUnsandboxedCommands": false, "autoAllowBashIfSandboxed": true,
		"filesystem": map[string]any{"allowWrite": p.roots, "denyWrite": p.denied},
	}
	data, err := json.Marshal(settings)
	if err != nil {
		return nil, err
	}
	p.settings = string(data)
	return p, nil
}

func readBoundedFile(path string, limit int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > limit {
		return nil, errors.New("Claude file exceeds permitted read boundary")
	}
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if int64(len(data)) > limit {
		return nil, errors.New("Claude file exceeds permitted read boundary")
	}
	return data, err
}

func (p *workspacePolicy) hooks() map[string][]protocol.HookCallbackMatcher {
	return map[string][]protocol.HookCallbackMatcher{"PreToolUse": {{Matcher: "Write|Edit|MultiEdit|NotebookEdit", CallbackIDs: []string{writeHookID}}}}
}

func (p *workspacePolicy) verify(response protocol.SettingsControlResponse) error {
	e := response.Effective
	if e == nil || e.AllowManagedHooksOnly == nil || !*e.AllowManagedHooksOnly || e.Sandbox == nil {
		return errors.New("Claude did not confirm the managed workspace policy")
	}
	sandbox := e.Sandbox
	if sandbox.Enabled == nil || !*sandbox.Enabled || sandbox.FailIfUnavailable == nil || !*sandbox.FailIfUnavailable ||
		sandbox.AllowUnsandboxed == nil || *sandbox.AllowUnsandboxed || len(sandbox.ExcludedCommands) > 0 ||
		sandbox.Filesystem == nil || sandbox.Filesystem.Disabled {
		return errors.New("Claude did not confirm the fail-closed Bash sandbox")
	}
	writes := slices.Clone(sandbox.Filesystem.AllowWrite)
	slices.Sort(writes)
	if !slices.Equal(slices.Compact(writes), p.roots) {
		return errors.New("Claude Bash writable paths differ from the workspace policy")
	}
	for _, path := range p.denied {
		if !slices.Contains(sandbox.Filesystem.DenyWrite, path) {
			return errors.New("Claude omitted a protected Bash path")
		}
	}
	for _, allow := range e.Permissions.Allow {
		name, _, _ := strings.Cut(allow, "(")
		if writesFile(strings.TrimSpace(name)) {
			return errors.New("Claude reported a file allow rule that can override the write hook")
		}
	}
	return nil
}

func writesFile(tool string) bool {
	switch tool {
	case "Write", "Edit", "MultiEdit", "NotebookEdit":
		return true
	default:
		return false
	}
}

// workspaceWrite binds a file approval to the native tool's own directory and
// the physical target checked by PreToolUse.
type workspaceWrite struct {
	cwd     string
	tool    string
	rawPath string
	target  string
}

func (p *workspacePolicy) verifyNativeDirectory(cwd string) error {
	actual, err := realDirectory(cwd)
	if err != nil || actual != p.cwd {
		return errors.New("Claude reported a working directory different from the admitted workspace")
	}
	return nil
}

func (p *workspacePolicy) hook(sessionID string, req protocol.HookCallbackRequest) (string, *workspaceWrite, protocol.HookCallbackResult) {
	input := decodeOptional[protocol.PreToolUseInput](req.Input)
	if req.CallbackID != writeHookID || input == nil || input.SessionID != sessionID ||
		input.Event != "PreToolUse" || !writesFile(input.Tool) {
		return "", nil, workspaceHookDecision("deny", "Unverified workspace write request.")
	}
	id := input.ToolUseID
	cwd, err := realDirectory(input.CWD)
	path := fileWritePath(input.Tool, input.Input)
	if err != nil || p.pathPolicy(cwd) != "" {
		return id, nil, workspaceHookDecision("deny", "The native working directory is outside the admitted workspace.")
	}
	target, err := resolveWorkspacePath(cwd, path)
	if err != nil || p.pathPolicy(target) == "deny" {
		return id, nil, workspaceHookDecision("deny", "This path is protected or could not be verified.")
	}
	write := &workspaceWrite{cwd: cwd, tool: input.Tool, rawPath: path, target: target}
	if p.pathPolicy(target) == "ask" {
		return id, write, workspaceHookDecision("ask", "This path is outside the permitted workspace roots.")
	}
	return id, write, protocol.HookCallbackResult{}
}

func workspaceHookDecision(decision, reason string) protocol.HookCallbackResult {
	return protocol.HookCallbackResult{Output: &protocol.HookSpecificOutput{
		Event: "PreToolUse", Decision: decision, Reason: reason,
	}}
}

func (p *workspacePolicy) approval(write *workspaceWrite, req protocol.CanUseToolControlRequest) string {
	if !writesFile(req.ToolName) {
		return ""
	}
	if write == nil || write.tool != req.ToolName || write.rawPath != fileWritePath(req.ToolName, req.Input) {
		return "deny"
	}
	cwd, err := realDirectory(write.cwd)
	if err != nil || cwd != write.cwd || p.pathPolicy(cwd) != "" {
		return "deny"
	}
	target, err := resolveWorkspacePath(cwd, write.rawPath)
	if err != nil || target != write.target {
		return "deny"
	}
	return p.pathPolicy(target)
}

func fileWritePath(tool string, raw json.RawMessage) string {
	input := decodeOptional[struct {
		File     string `json:"file_path"`
		Notebook string `json:"notebook_path"`
	}](raw)
	if input == nil || input.File != "" && input.Notebook != "" {
		return ""
	}
	if tool == "NotebookEdit" {
		return input.Notebook
	}
	return input.File
}

func (p *workspacePolicy) pathPolicy(path string) string {
	if filepath.Base(filepath.Dir(path)) == ".claude" &&
		(filepath.Base(path) == "settings.json" || filepath.Base(path) == "settings.local.json") {
		return "deny"
	}
	for _, denied := range p.denied {
		if containsPath(denied, path) {
			return "deny"
		}
	}
	for _, root := range p.roots {
		if containsPath(root, path) {
			return ""
		}
	}
	return "ask"
}

func resolveWorkspacePath(cwd, raw string) (string, error) {
	if raw == "" || strings.ContainsRune(raw, 0) {
		return "", errors.New("missing Claude workspace path")
	}
	for _, part := range strings.FieldsFunc(raw, func(r rune) bool { return r == '/' || r == '\\' }) {
		if part == ".." {
			return "", errors.New("unresolved parent in Claude workspace path")
		}
	}
	if !filepath.IsAbs(raw) {
		raw = filepath.Join(cwd, raw)
	}
	return resolveExistingParent(filepath.Clean(raw), 0)
}

func resolveExistingParent(path string, depth int) (string, error) {
	if depth > 64 {
		return "", errors.New("Claude workspace symlink resolution exceeded limit")
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err == nil {
		return resolved, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	info, statErr := os.Lstat(path)
	if statErr == nil && info.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(path)
		if err != nil {
			return "", err
		}
		if !filepath.IsAbs(target) {
			target = filepath.Join(filepath.Dir(path), target)
		}
		return resolveExistingParent(target, depth+1)
	}
	if !errors.Is(statErr, os.ErrNotExist) {
		return "", err
	}
	parent := filepath.Dir(path)
	if parent == path {
		return "", err
	}
	root, err := resolveExistingParent(parent, depth+1)
	if err != nil {
		return "", err
	}
	return filepath.Join(root, filepath.Base(path)), nil
}

func containsPath(root, target string) bool {
	relative, err := filepath.Rel(root, target)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

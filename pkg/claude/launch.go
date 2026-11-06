package claude

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"

	acp "github.com/JieWaZi/acp-go-sdk"
	"github.com/JieWaZi/acp-go/pkg/acpmeta"
)

// LaunchOptions declares native resources prepared by the host.
type LaunchOptions struct {
	// SettingSources selects the native settings scopes. Nil uses standard scopes.
	SettingSources []string
	// Tools selects the built-in tools. Nil preserves native defaults.
	Tools []string
	// PluginDirectories contains local, physical plugin directories.
	PluginDirectories []string
	// SettingsPath points to a native flag settings file supplied by the host.
	SettingsPath string
	// WorkspaceOnly requires native Bash sandbox and write-hook verification.
	WorkspaceOnly bool
	// ExcludedWriteDirectories are protected even when a permission is approved.
	ExcludedWriteDirectories []string
}

type launchPlan struct {
	cwd         string
	args        []string
	fingerprint [sha256.Size]byte
	// instructions preserves nil and explicit empty overrides.
	instructions  *string
	snapshotOff   bool
	mcpConfigured bool
	// mcpOnlyPrefixes identifies host-configured MCP servers when builtin tools are disabled.
	mcpOnlyPrefixes []string
	// initializePlugins is set only when the managed-hook startup barrier owns delivery.
	initializePlugins []string
	workspace         *workspacePolicy
}

func validateLaunchPrefix(args []string) error {
	for _, arg := range args {
		// A wrapper can consume its own -- and forward the remaining flags.
		// The executable itself remains trusted, but its prefix cannot carry
		// native flags that compete with the adapter's launch inputs.
		if claudeLaunchOverride(arg) {
			return errors.New("Claude startup arguments cannot override managed launch settings")
		}
	}
	return nil
}

func claudeLaunchOverride(arg string) bool {
	flag, _, _ := strings.Cut(arg, "=")
	switch flag {
	case "--settings", "--managed-settings", "--setting-sources", "--client-data-url", "--host-auth-is-org-sign-in",
		"--bare", "--safe-mode":
		return true
	case "--permission-mode", "--inherit-permission-mode", "--permission-prompt-tool", "--permission-prompts",
		"--dangerously-skip-permissions", "--allow-dangerously-skip-permissions", "--enable-auto-mode", "--restricted",
		"--tools", "--allowedTools", "--allowed-tools", "--disallowedTools", "--disallowed-tools":
		return true
	case "--cwd", "--add-dir", "--project-config-root", "--worktree", "--tmux", "--file", "--debug-file", "--deep-link-cwd-b64",
		"--mcp-config", "--strict-mcp-config", "--plugin-dir", "--plugin-dir-no-mcp", "--plugin-url",
		"--channels", "--dangerously-load-development-channels", "--chrome", "--ide":
		return true
	case "--model", "--fallback-model", "--allow-other-models", "--include-unsupported-models", "--effort", "--fast-mode",
		"--agent", "--agents", "--system-prompt", "--system-prompt-file", "--append-system-prompt",
		"--append-system-prompt-file", "--system-prompt-snapshot":
		return true
	case "--continue", "--resume", "--fork-session", "--session-id", "--no-session-persistence", "--from-pr",
		"--resume-session-at", "--resume-drops-turn", "--reply-on-resume", "--rewind-files",
		"--bg", "--background", "--remote", "--cloud", "--teleport", "--remote-control", "--rc", "--attach-serve", "--desktop":
		return true
	case "--print", "--input-format", "--output-format", "--sdk-url", "--await-initialize", "--await-claim",
		"--verbose", "--include-partial-messages", "--replay-user-messages", "--init", "--init-only", "--maintenance":
		return true
	}
	if len(flag) < 2 || flag[0] != '-' || flag[1] == '-' {
		return false
	}
	// Commander accepts attached values for -r and -w and splits boolean
	// short options. Unknown prefixes and value-taking diagnostic options
	// are not clusters of native flags (for example, -test.run or -dworktree).
	for _, option := range flag[1:] {
		switch option {
		case 'p', 'c', 'r', 'w':
			return true
		case 'h', 'v':
			continue
		default:
			return false
		}
	}
	return false
}

func freezeLaunch(o LaunchOptions) (LaunchOptions, error) {
	o.SettingSources = slices.Clone(o.SettingSources)
	o.Tools = slices.Clone(o.Tools)
	o.PluginDirectories = slices.Clone(o.PluginDirectories)
	o.ExcludedWriteDirectories = slices.Clone(o.ExcludedWriteDirectories)
	for _, source := range o.SettingSources {
		if source != "user" && source != "project" && source != "local" {
			return LaunchOptions{}, errors.New("invalid Claude settings source")
		}
		if o.WorkspaceOnly && source != "user" {
			return LaunchOptions{}, errors.New("workspace policy requires trusted settings sources")
		}
	}
	for _, tool := range o.Tools {
		if tool == "" || strings.ContainsAny(tool, ", \r\n\t") {
			return LaunchOptions{}, errors.New("invalid Claude tool selector")
		}
	}
	for i, path := range o.PluginDirectories {
		dir, err := realDirectory(path)
		if err != nil {
			return LaunchOptions{}, errors.New("invalid Claude plugin directory")
		}
		o.PluginDirectories[i] = dir
	}
	if o.SettingsPath != "" {
		info, err := os.Stat(o.SettingsPath)
		if !filepath.IsAbs(o.SettingsPath) || err != nil || !info.Mode().IsRegular() {
			return LaunchOptions{}, errors.New("invalid Claude settings file")
		}
	}
	return o, nil
}

func realDirectory(path string) (string, error) {
	if !filepath.IsAbs(path) {
		return "", errors.New("Claude directory must be absolute")
	}
	dir, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", errors.New("Claude directory is unavailable")
	}
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return "", errors.New("Claude directory is unavailable")
	}
	return dir, nil
}

func streamArgs() []string {
	return []string{"--output-format", "stream-json", "--verbose", "--input-format", "stream-json",
		"--permission-prompt-tool", "stdio", "--include-partial-messages", "--replay-user-messages"}
}

func (c *Client) prepareLaunch(req openRequest, old *session) (launchPlan, error) {
	cwd, err := realDirectory(req.cwd)
	if err != nil {
		return launchPlan{}, err
	}
	roots := make([]string, 0, len(req.additional))
	for _, path := range req.additional {
		root, err := realDirectory(path)
		if err != nil {
			return launchPlan{}, err
		}
		if root != cwd && !slices.Contains(roots, root) {
			roots = append(roots, root)
		}
	}
	slices.Sort(roots)
	instructions, err := acpmeta.DeveloperInstructions(req.meta)
	if err != nil {
		return launchPlan{}, acp.NewInvalidParams(map[string]any{"message": err.Error()})
	}
	if instructions == nil && old != nil {
		instructions = old.launch.instructions
	}
	snapshotOff := instructions != nil && c.canReplaceInstructions()
	if instructions != nil && req.resume && !snapshotOff {
		return launchPlan{}, acp.NewInvalidParams(map[string]any{"message": "overriding resumed Claude instructions requires CLI 2.1.263 or later"})
	}
	mcp, err := nativeMCP(req.mcp)
	if err != nil {
		return launchPlan{}, err
	}
	o := c.options.Launch
	policy, err := newWorkspacePolicy(o, cwd, roots)
	if err != nil {
		return launchPlan{}, err
	}
	sources := o.SettingSources
	if sources == nil {
		sources = []string{"user", "project", "local"}
		if policy != nil {
			sources = []string{"user"}
		}
	}
	// The host may apply another advertised mode after opening. Begin from
	// an explicit default so a settings file cannot silently select bypass.
	args := append(streamArgs(), "--setting-sources="+strings.Join(sources, ","), "--permission-mode", "default")
	if o.Tools != nil {
		args = append(args, "--tools="+strings.Join(o.Tools, ","))
	}
	if policy != nil {
		args = append(args, "--settings", policy.settings, "--managed-settings", `{"allowManagedHooksOnly":true}`)
	} else if o.SettingsPath != "" {
		args = append(args, "--settings", o.SettingsPath)
	}
	// The native SDK chooses one plugin delivery route at process launch.
	// Managed hooks already require an initialize barrier, so deliver plugins
	// there too; otherwise use the SDK's argv path without MCP discovery.
	var initializePlugins []string
	if policy != nil {
		args = append(args, "--await-initialize")
		if len(o.PluginDirectories) > 0 {
			if acpmeta.VerifiedForkMode(c.version, "2.1.261", "initialize") != "initialize" {
				return launchPlan{}, errors.New("Claude CLI 2.1.261 or later is required for plugins with managed workspace hooks")
			}
			initializePlugins = o.PluginDirectories
		}
	} else {
		for _, path := range o.PluginDirectories {
			args = append(args, "--plugin-dir-no-mcp="+path)
		}
	}
	if snapshotOff {
		args = append(args, "--system-prompt-snapshot", "off")
	}
	if c.bypassEnabled() && policy == nil {
		args = append(args, "--allow-dangerously-skip-permissions")
	}
	if mcp != "" {
		args = append(args, "--strict-mcp-config", "--mcp-config", mcp)
	}
	for _, root := range roots {
		args = append(args, "--add-dir", root)
	}
	data, _ := json.Marshal([]any{cwd, args, instructions, initializePlugins})
	fingerprint := sha256.Sum256(data)
	switch {
	case req.forkSource != "":
		args = append(args, "--resume="+req.forkSource, "--fork-session", "--session-id="+string(req.id))
	case req.resume:
		args = append(args, "--resume="+string(req.id))
	default:
		args = append(args, "--session-id="+string(req.id))
	}
	var mcpOnlyPrefixes []string
	if o.Tools != nil && len(o.Tools) == 0 && o.SettingSources != nil && len(o.SettingSources) == 0 &&
		len(o.PluginDirectories) == 0 && o.SettingsPath == "" && policy == nil {
		for _, server := range req.mcp {
			switch {
			case server.Stdio != nil:
				mcpOnlyPrefixes = append(mcpOnlyPrefixes, "mcp__"+server.Stdio.Name+"__")
			case server.Http != nil:
				mcpOnlyPrefixes = append(mcpOnlyPrefixes, "mcp__"+server.Http.Name+"__")
			case server.Sse != nil:
				mcpOnlyPrefixes = append(mcpOnlyPrefixes, "mcp__"+server.Sse.Name+"__")
			}
		}
	}
	return launchPlan{cwd: cwd, args: args, fingerprint: fingerprint, instructions: instructions, snapshotOff: snapshotOff,
		mcpConfigured: len(req.mcp) > 0, mcpOnlyPrefixes: mcpOnlyPrefixes, initializePlugins: initializePlugins, workspace: policy}, nil
}

func nativeMCP(servers []acp.McpServer) (string, error) {

	rows := make(map[string]any, len(servers))
	for _, server := range servers {
		var name string
		var row map[string]any
		switch {
		case server.Stdio != nil:
			s := server.Stdio
			name = s.Name
			if s.Command == "" || strings.ContainsRune(s.Command, 0) {
				return "", errors.New("invalid Claude MCP stdio command")
			}
			env := make(map[string]string, len(s.Env))
			for _, pair := range s.Env {
				if pair.Name == "" || strings.ContainsAny(pair.Name, "=\x00") {
					return "", errors.New("invalid Claude MCP environment name")
				}
				if _, duplicate := env[pair.Name]; duplicate {
					return "", errors.New("duplicate Claude MCP environment name")
				}
				env[pair.Name] = pair.Value
			}
			args := s.Args
			if args == nil {
				args = []string{}
			}
			row = map[string]any{"type": "stdio", "command": s.Command, "args": args, "env": env}
		case server.Http != nil:
			name = server.Http.Name
			var err error
			row, err = remoteMCP("http", server.Http.Url, server.Http.Headers)
			if err != nil {
				return "", err
			}
		case server.Sse != nil:
			name = server.Sse.Name
			var err error
			row, err = remoteMCP("sse", server.Sse.Url, server.Sse.Headers)
			if err != nil {
				return "", err
			}
		default:
			return "", errors.New("unsupported Claude MCP transport")
		}
		if name == "" || strings.ContainsAny(name, " \r\n\t") {
			return "", errors.New("invalid Claude MCP server name")
		}
		if _, duplicate := rows[name]; duplicate {
			return "", errors.New("duplicate Claude MCP server name")
		}
		rows[name] = row
	}
	data, err := json.Marshal(map[string]any{"mcpServers": rows})
	return string(data), err
}

func remoteMCP(kind, raw string, headers []acp.HttpHeader) (map[string]any, error) {
	u, err := url.Parse(raw)
	if err != nil || u == nil || u.Host == "" || u.Scheme != "http" && u.Scheme != "https" || u.User != nil {
		return nil, errors.New("invalid Claude remote MCP URL")
	}
	values := make(map[string]string, len(headers))
	for _, header := range headers {
		key := strings.ToLower(header.Name)
		if key == "" || strings.ContainsAny(key, " \r\n\t:") || strings.ContainsAny(header.Value, "\r\n") {
			return nil, errors.New("invalid Claude MCP header")
		}
		if _, duplicate := values[key]; duplicate {
			return nil, errors.New("duplicate Claude MCP header")
		}
		values[key] = header.Value
	}
	return map[string]any{"type": kind, "url": raw, "headers": values}, nil
}

func (c *Client) canReplaceInstructions() bool {
	return acpmeta.VerifiedForkMode(c.version, "2.1.263", acpmeta.ForkLatest) == acpmeta.ForkLatest
}

func (c *Client) instructionCapability() map[string]any {
	capability := acpmeta.DeveloperInstructionsCapability(c.forkMode() != acpmeta.ForkUnsupported)
	capability["resume"] = c.canReplaceInstructions()
	capability["load"] = c.canReplaceInstructions()
	return capability
}

// streamEnvironment follows the CLI's SDK transport negotiation. The
// compatibility entrypoint enables SDK control; this client still owns Go I/O.
func (c *Client) streamEnvironment() []string {
	env := make([]string, 0, len(c.options.Environment)+5)
	for _, entry := range c.options.Environment {
		key, _, ok := strings.Cut(entry, "=")
		if ok && !strings.EqualFold(key, "NODE_OPTIONS") {
			env = append(env, entry)
		}
	}
	return append(env, "CLAUDE_CODE_ENTRYPOINT=sdk-ts", "CLAUDE_CODE_EMIT_SESSION_STATE_EVENTS=1",
		"CLAUDE_CODE_STARTUP_FAILURE_RESULTS=1", "CLAUDE_CODE_ENABLE_TODO_TOOLS=1",
		"CLAUDE_AGENT_SDK_CLIENT_APP=acp-go/claude")
}

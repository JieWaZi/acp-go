// Package cursor supplies Cursor startup, account management and native ACP differences.
package cursor

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"

	acp "github.com/JieWaZi/acp-go-sdk"
	"github.com/JieWaZi/acp-go/pkg/acpclient"
	agentclient "github.com/JieWaZi/acp-go/pkg/agentclient"
)

// Options freezes the selected Cursor executable, credentials, and startup scope.
type Options struct {
	// Executable is the selected Cursor CLI path or command name.
	Executable string
	// PrefixArgs precedes each subcommand and cannot override the selected account, policy, or workspace.
	PrefixArgs []string
	// PluginDirectories lists local plugins loaded by the ACP process only.
	PluginDirectories []string
	// Environment is the complete launch environment; nil snapshots the current host environment.
	Environment []string
	// WorkingDirectory is the initial process working directory.
	WorkingDirectory string
	// Logger receives only safe SDK diagnostics.
	Logger *slog.Logger
	// PermissionMode requests the native permission policy; unsupported policies are rejected.
	PermissionMode string
	// RequireVerifiedMCP rejects session opening when MCP registration cannot be verified.
	RequireVerifiedMCP bool
	// Dashboard enables quota reads for an explicitly bound native login.
	Dashboard *Dashboard
	// Credential identifies the exact login available to management operations.
	Credential CredentialSelection
	// UsageHook optionally collects native turn usage while official ACP retains execution and streaming ownership.
	UsageHook bool
}

// Client adds Cursor management commands and account reads to the ACP owner.
type Client struct {
	// Client owns the shared ACP transport, sessions, and native configuration.
	*acpclient.Client
	// opts holds the frozen launch and account configuration.
	opts Options
	// apiKey holds explicit launch credentials independently of native login.
	apiKey string
	// mu protects management admission, command ownership, and quota-reader state.
	mu sync.Mutex
	// command retains ownership until the entire native command tree has been reclaimed.
	command *agentclient.Process
	// closed rejects new management operations after shutdown begins.
	closed bool
	// quotaReaders counts quota callbacks and HTTP operations that have not returned.
	quotaReaders int
	// quotaIdle closes when the last quota reader exits and is nil while no reader is active.
	quotaIdle chan struct{}
	// management is the shared cancellable lifetime of management operations.
	management context.Context
	// stopManagement cancels management work during shutdown.
	stopManagement context.CancelFunc
	// usage retains the optional bridge directory and current input association without owning execution state.
	usage *cursorUsageState
}

var (
	ErrBusy            = acpclient.ErrBusy
	ErrRestartRequired = acpclient.ErrRestartRequired

	// ErrPermissionModeUnsupported identifies a policy the ACP implementation does not apply.
	ErrPermissionModeUnsupported = errors.New("Cursor ACP does not support this permission mode")
)

var _ agentclient.Client = (*Client)(nil)

// NewClient resolves and freezes options without starting any native process.
func NewClient(ctx context.Context, options Options, host agentclient.HostCallbacks) (*Client, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	discover := options.Executable == ""
	if discover {
		options.Executable = "cursor-agent"
	}
	if options.Environment == nil {
		options.Environment = os.Environ()
	}
	options.Environment = slices.Clone(options.Environment)
	options.PrefixArgs = slices.Clone(options.PrefixArgs)
	if envValue(options.Environment, "CURSOR_AUTH_TOKEN") != "" {
		return nil, invalid("Cursor launch does not support a raw authentication token")
	}
	for _, arg := range options.PrefixArgs {
		if cursorLaunchOverride(arg) {
			return nil, invalid("Cursor launcher arguments cannot override the selected account, policy, or workspace")
		}
	}
	if options.Dashboard != nil {
		dashboard := *options.Dashboard
		if dashboard.HTTPClient != nil {
			client := *dashboard.HTTPClient
			dashboard.HTTPClient = &client
		}
		options.Dashboard = &dashboard
	}
	if options.WorkingDirectory == "" {
		dir, err := os.Getwd()
		if err != nil {
			return nil, errors.New("resolve Cursor working directory failed")
		}
		options.WorkingDirectory = dir
	}
	if !filepath.IsAbs(options.WorkingDirectory) {
		dir, err := filepath.Abs(options.WorkingDirectory)
		if err != nil {
			return nil, errors.New("resolve Cursor working directory failed")
		}
		options.WorkingDirectory = dir
	}
	directories, err := agentclient.PhysicalPluginDirectories(options.PluginDirectories, options.PrefixArgs)
	if err != nil {
		return nil, err
	}
	apiKey := envValue(options.Environment, "CURSOR_API_KEY")
	args := slices.Clone(options.PrefixArgs)
	for _, dir := range directories {
		args = append(args, "--plugin-dir", dir)
	}
	switch options.PermissionMode {
	case "", "default":
	case "auto":
		// Cursor 2026.10.22-27dcf5f accepts the global flag, but its ACP
		// session resource builder only reads force.
		return nil, ErrPermissionModeUnsupported
	case "full-access":
		args = append(args, "--force")
	default:
		return nil, invalid("invalid Cursor startup permission mode")
	}
	spec := agentclient.ProcessSpec{
		Executable: options.Executable, Args: append(args, "acp"), Env: options.Environment, Dir: options.WorkingDirectory,
		Redact: func(record string) string {
			if apiKey != "" && strings.Contains(record, apiKey) {
				return ""
			}
			return agentclient.RedactProcessDiagnostic(record)
		},
	}
	proc, err := agentclient.NewProcess(spec)
	if err != nil && discover {
		spec.Executable = "agent"
		if alias, aliasErr := agentclient.NewProcess(spec); aliasErr == nil {
			if target, targetErr := filepath.EvalSymlinks(alias.Executable()); targetErr == nil &&
				target != alias.Executable() && cliName(target) == "cursor-agent" {
				proc, err = alias, nil
			}
		}
	}
	if err != nil {
		return nil, err
	}
	options.Executable = proc.Executable()
	target, err := filepath.EvalSymlinks(options.Executable)
	if err != nil || cliName(options.Executable) == "cursor" || cliName(target) == "cursor" {
		return nil, errors.New("selected executable is not a Cursor CLI launcher")
	}
	// The official updater atomically replaces the launcher symlink; management, version probes, and ACP must share one frozen installation target.
	options.Executable = target
	management, stop := context.WithCancel(context.WithoutCancel(ctx))
	client := &Client{opts: options, apiKey: apiKey, management: management, stopManagement: stop}
	spec.Executable = options.Executable
	if options.UsageHook {
		client.usage = &cursorUsageState{directory: filepath.Join(os.TempDir(), "acp-go-cursor-usage-"+newCursorUsageNonce()), nonces: make(map[acp.SessionId]string)}
		spec.Env = cursorUsageEnvironment(options.Executable, client.usage.directory, options.Environment)
	}
	shared, err := acpclient.NewClient(ctx, acpclient.Options{
		Process: spec, RuntimeID: "cursor", Logger: options.Logger,
		Version:  acpclient.VersionProbe{Args: append(slices.Clone(options.PrefixArgs), "--version"), Required: true, Parse: parseVersion},
		Protocol: cursorProtocol(client), RequireVerifiedMCP: options.RequireVerifiedMCP,
	}, host)
	if err != nil {
		stop()
		return nil, err
	}
	client.Client = shared
	return client, nil
}

func cursorLaunchOverride(arg string) bool {
	flag, _, _ := strings.Cut(arg, "=")
	switch flag {
	case "--api-key", "--auth-token", "--header", "--endpoint", "--website-url", "--insecure", "--data-dir",
		"--force", "--yolo", "--auto-review", "--sandbox", "--approve-mcps", "--trust",
		"--workspace", "--add-dir", "--worktree", "--worktree-base", "--skip-worktree-setup", "--plugin-dir":
		return true
	}
	if len(flag) < 2 || flag[0] != '-' || flag[1] == '-' {
		return false
	}
	// Cursor's Commander parser can split known boolean options in a group
	// such as -pf. An unrecognized leading option can be wrapper syntax.
	for _, option := range flag[1:] {
		switch option {
		case 'f', 'k', 'H', 'e', 'w':
			return true
		case 'p', 'v', 'h', 'c', 'b':
			continue
		default:
			return false
		}
	}
	return false
}

func envValue(env []string, key string) string {
	for i := len(env) - 1; i >= 0; i-- {
		name, value, ok := strings.Cut(env[i], "=")
		if ok && (name == key || runtime.GOOS == "windows" && strings.EqualFold(name, key)) {
			return value
		}
	}
	return ""
}

// Close cancels management work and retains unfinished consumers for retry.
func (c *Client) Close(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	c.closed = true
	c.stopManagement()
	command, quotaIdle := c.command, c.quotaIdle
	c.mu.Unlock()
	var commandErr error
	if command != nil {
		commandErr = c.closeCommand(ctx, command)
	}
	nativeErr := c.Client.Close(ctx)
	if quotaIdle != nil {
		select {
		case <-quotaIdle:
		default:
			select {
			case <-quotaIdle:
			case <-ctx.Done():
				return errors.Join(nativeErr, commandErr, ctx.Err())
			}
		}
	}
	var usageErr error
	if nativeErr == nil {
		usageErr = c.closeUsage(ctx)
	}
	return errors.Join(nativeErr, commandErr, usageErr)
}

func invalid(message string) *acp.RequestError {
	return acp.NewInvalidParams(map[string]any{"message": message})
}

// MCPObservation is the result of Cursor's standard ACP MCP handoff.
type MCPObservation = acpclient.MCPObservation

// ReadMCPObservation reads the shared ACP session's MCP observation.
func ReadMCPObservation(meta map[string]any) (MCPObservation, bool) {
	return acpclient.ReadMCPObservation(meta)
}

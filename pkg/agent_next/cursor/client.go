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

	agentclient "github.com/JieWaZi/acp-go/pkg/agent_next"
	"github.com/JieWaZi/acp-go/pkg/agent_next/acpclient"
	acp "github.com/JieWaZi/acp-go/sdk"
)

// Options freezes the selected Cursor executable, credentials, and startup scope.
type Options struct {
	// Executable is the selected Cursor CLI path or command name.
	Executable string
	// PrefixArgs precede each Cursor subcommand. Native account and policy flags are reserved.
	PrefixArgs []string
	// Environment is the complete launch environment, or nil to freeze the host environment.
	Environment []string
	// WorkingDirectory is the initial process directory.
	WorkingDirectory string
	// Logger is reserved for safe SDK diagnostics.
	Logger *slog.Logger
	// PermissionMode selects native defaults or the CLI force policy (full-access).
	PermissionMode string
	// RequireVerifiedMCP rejects servers when Cursor cannot verify their registration.
	RequireVerifiedMCP bool
	// Dashboard optionally enables quota reads for the explicitly bound native login.
	Dashboard *Dashboard
	// Credential identifies the exact login authorized for management reads.
	Credential CredentialSelection
}

// Client adds Cursor management commands and account reads to the ACP owner.
type Client struct {
	*acpclient.Client
	// opts contains the frozen Cursor launch and account inputs.
	opts Options
	// apiKey identifies an explicit launch credential independently of sign-in.
	apiKey string
	// mu protects management admission, commands and quota consumers.
	mu sync.Mutex
	// command remains owned until its native tree has been reaped.
	command *agentclient.Process
	// closed rejects new management operations.
	closed bool
	// quotaReaders counts admitted Dashboard operations until all callbacks and HTTP work exit.
	quotaReaders int
	// quotaIdle is closed on the last exit and is nil when no Dashboard operation is active.
	quotaIdle chan struct{}
	// management is canceled when the Cursor wrapper is closed.
	management     context.Context
	stopManagement context.CancelFunc
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
			return nil, invalid("Cursor launcher arguments cannot override the selected account or policy")
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
	apiKey := envValue(options.Environment, "CURSOR_API_KEY")
	args := slices.Clone(options.PrefixArgs)
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
	management, stop := context.WithCancel(context.WithoutCancel(ctx))
	client := &Client{opts: options, apiKey: apiKey, management: management, stopManagement: stop}
	spec.Executable = options.Executable
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
		"--force", "--yolo", "--auto-review", "--sandbox", "--approve-mcps", "--trust", "-k":
		return true
	}
	if len(flag) < 2 || flag[0] != '-' || flag[1] == '-' {
		return false
	}
	// Cursor accepts short options with attached values and combined force flags.
	short := strings.TrimLeft(flag[1:], "f")
	return short == "" || strings.HasPrefix(short, "H") || strings.HasPrefix(short, "e")
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
	return errors.Join(nativeErr, commandErr)
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

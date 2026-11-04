// Package qoder supplies the Qoder and Qoder CN launch profiles for native ACP.
package qoder

import (
	"context"
	"log/slog"
	"slices"

	agentclient "github.com/JieWaZi/acp-go/pkg/agent_next"
	"github.com/JieWaZi/acp-go/pkg/agent_next/acpclient"
)

// Options freezes the executable selected by the Qoder runtime descriptor.
type Options struct {
	// RuntimeID selects qoder or qoder-cn.
	RuntimeID string
	// Executable is the installed regional CLI selected by the caller.
	Executable string
	// PrefixArgs precedes the native ACP or version argument.
	PrefixArgs []string
	// Environment is the complete launch environment, or nil to snapshot the host.
	Environment []string
	// WorkingDirectory is the launch directory for both native commands.
	WorkingDirectory string
	// Logger receives safe process diagnostics.
	Logger *slog.Logger
}

// NewClient constructs the common owner with Qoder's observed option category.
func NewClient(ctx context.Context, options Options, host agentclient.HostCallbacks) (*acpclient.Client, error) {
	runtime := options.RuntimeID
	if runtime == "" {
		runtime = "qoder"
	}
	executable := options.Executable
	if executable == "" {
		executable = "qoder"
		if runtime == "qoder-cn" {
			executable = "qodercn"
		}
	}
	return acpclient.NewClient(ctx, acpclient.Options{
		Process: agentclient.ProcessSpec{
			Executable: executable, Args: append(slices.Clone(options.PrefixArgs), "--acp"),
			Env: options.Environment, Dir: options.WorkingDirectory, Redact: agentclient.RedactProcessDiagnostic,
		},
		Version:   acpclient.VersionProbe{Args: append(slices.Clone(options.PrefixArgs), "--version")},
		RuntimeID: runtime, Logger: options.Logger, Protocol: acpclient.Protocol{NormalizeOption: normalizeOption},
	}, host)
}

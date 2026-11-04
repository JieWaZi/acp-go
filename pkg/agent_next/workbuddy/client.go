// Package workbuddy connects to the installed CodeBuddy Code ACP process.
package workbuddy

import (
	"context"
	"errors"
	"log/slog"
	"os/exec"
	"slices"

	agentclient "github.com/JieWaZi/acp-go/pkg/agent_next"
	"github.com/JieWaZi/acp-go/pkg/agent_next/acpclient"
)

// Options freezes the executable and the account's launch environment.
type Options struct {
	// Executable selects a CodeBuddy CLI; otherwise codebuddy and cbc are tried in order.
	Executable string
	// PrefixArgs precedes the fixed native ACP and version arguments.
	PrefixArgs []string
	// Environment is the complete launch environment, or nil to snapshot the host.
	Environment []string
	// WorkingDirectory is the workspace selected for this CLI process.
	WorkingDirectory string
	// Logger receives safe SDK diagnostics.
	Logger *slog.Logger
	// ExplicitCredential disables interactive account changes for a selected API key or token.
	ExplicitCredential bool
}

// NewClient freezes the CodeBuddy launch without starting a process.
func NewClient(ctx context.Context, options Options, host agentclient.HostCallbacks) (*acpclient.Client, error) {
	discover := options.Executable == ""
	if discover {
		options.Executable = "codebuddy"
	}
	launch := acpclient.Options{
		Process: agentclient.ProcessSpec{
			Executable: options.Executable, Args: append(slices.Clone(options.PrefixArgs), "--acp"),
			Env: options.Environment, Dir: options.WorkingDirectory, Redact: agentclient.RedactProcessDiagnostic,
		},
		Version:   acpclient.VersionProbe{Args: append(slices.Clone(options.PrefixArgs), "--version")},
		RuntimeID: "workbuddy", Logger: options.Logger,
		Protocol: acpclient.Protocol{
			PromptUsage: acpclient.UsageUnavailable, Private: interactionCodec{}, PrepareInitialize: prepareInitialize,
			CwdBoundProcess: true, RequireLoadEvidence: true, ModeOption: "mode", BlockUntypedExtensions: true,
			ReadPromptEvidence: readPromptEvidence, NormalizeUsage: normalizeUsage,
			ExecutionOption: executionOption, Authentication: acpclient.Authentication{Disabled: options.ExplicitCredential},
		},
	}
	client, err := acpclient.NewClient(ctx, launch, host)
	if discover && errors.Is(err, exec.ErrNotFound) {
		launch.Process.Executable = "cbc"
		return acpclient.NewClient(ctx, launch, host)
	}
	return client, err
}

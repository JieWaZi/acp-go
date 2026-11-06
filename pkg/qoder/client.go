// Package qoder supplies the regional Qoder profiles for native ACP.
package qoder

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"

	acp "github.com/JieWaZi/acp-go-sdk"
	"github.com/JieWaZi/acp-go/pkg/acpclient"
	agentclient "github.com/JieWaZi/acp-go/pkg/agentclient"
)

// Options freezes the regional launcher and credentials selected by the caller.
type Options struct {
	// RuntimeID selects qoder or qoder-cn.
	RuntimeID string
	// Executable overrides executable discovery for the selected edition.
	Executable string
	// PrefixArgs precedes native ACP or management command arguments.
	PrefixArgs []string
	// Environment is the complete process environment; nil snapshots the host environment.
	Environment []string
	// WorkingDirectory is the native command's launch directory.
	WorkingDirectory string
	// PersonalAccessToken overrides the selected region's authentication environment variable.
	PersonalAccessToken string
	// ExpectedUserID binds native account and quota reads to the confirmed user.
	ExpectedUserID string
	// Model is the native model ID requested for this ACP session.
	Model string
	// ContextWindow requests a session context size; zero inherits the native setting.
	ContextWindow int64
	// Logger receives sanitized process diagnostics.
	Logger *slog.Logger
}

// Client delegates sessions and invocations to the shared ACP owner and owns
// one independent, read-only CLI management operation at a time.
type Client struct {
	*acpclient.Client
	// management owns the frozen launcher and any currently running read.
	management management
}

var _ agentclient.Client = (*Client)(nil)

// ErrContextWindowUnsupported reports a context size not advertised by the selected model.
var ErrContextWindowUnsupported = errors.New("Qoder model does not advertise the requested context window")

// NewClient freezes a Qoder launch without starting a child process.
func NewClient(ctx context.Context, options Options, host agentclient.HostCallbacks) (*Client, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	options, err := freezeOptions(options)
	if err != nil {
		return nil, err
	}
	token := selectedToken(options)
	acpArgs := slices.Clone(options.PrefixArgs)
	if options.Model != "" {
		acpArgs = append(acpArgs, "--model="+options.Model)
	}
	if options.ContextWindow > 0 {
		acpArgs = append(acpArgs, "--context-window="+strconv.FormatInt(options.ContextWindow, 10))
	}
	client, err := acpclient.NewClient(ctx, acpclient.Options{
		Process: agentclient.ProcessSpec{
			Executable: options.Executable, Args: append(acpArgs, "--acp"),
			Env: append(slices.Clone(options.Environment), tokenUsageEnvironment(options.RuntimeID)), Dir: options.WorkingDirectory,
			Redact: func(record string) string {
				if token != "" && strings.Contains(record, token) {
					return ""
				}
				return agentclient.RedactProcessDiagnostic(record)
			},
		},
		Version:   acpclient.VersionProbe{Args: append(slices.Clone(options.PrefixArgs), "--version"), Required: true},
		RuntimeID: options.RuntimeID, Logger: options.Logger,
		Protocol: acpclient.Protocol{
			// Preserve unknown counters from builds without the verified receipt.
			PromptUsage:        acpclient.UsageUnattributed,
			ReadPromptUsage:    readPromptUsage,
			ReadPromptEvidence: readPromptEvidence,
			CapturePromptJournal: func(ctx context.Context, scope acpclient.PromptJournalScope) func(context.Context, acpclient.PromptJournalReceipt) acpclient.PromptJournalEvidence {
				return captureQoderHistory(ctx, scope, qoderHistoryHome(options))
			},
			NormalizeOption: normalizeOption,
			SessionMissing:  sessionMissing,
			ClassifySessionOpenError: func(peer *acp.RequestError) error {
				return classifySessionOpenError(options.Model, peer)
			},
			Authentication: acpclient.Authentication{Disabled: token != ""},
			// Both editions use the persistent native setter for ACP model changes.
			ReadOnlyModelCatalog: true,
		},
	}, host)
	if err != nil {
		return nil, err
	}
	options.Executable = client.Executable()
	return &Client{Client: client, management: management{options: options}}, nil
}

// Initialize validates the account's advertised model context sizes before launching with an override.
func (c *Client) Initialize(ctx context.Context, request acp.InitializeRequest) (acp.InitializeResponse, error) {
	options := c.management.options
	if options.ContextWindow > 0 {
		rows, err := c.ReadModelDirectory(ctx)
		if err != nil {
			return acp.InitializeResponse{}, err
		}
		if !slices.ContainsFunc(rows, func(row ModelDirectoryEntry) bool {
			return row.ID == options.Model && slices.Contains(row.AvailableContextWindows, options.ContextWindow)
		}) {
			return acp.InitializeResponse{}, ErrContextWindowUnsupported
		}
	}
	return c.Client.Initialize(ctx, request)
}

func freezeOptions(options Options) (Options, error) {
	if options.RuntimeID == "" {
		options.RuntimeID = "qoder"
	}
	candidates := []string{"qodercli", "qoder"}
	switch options.RuntimeID {
	case "qoder":
	case "qoder-cn":
		candidates = []string{"qoderclicn", "qodercn", "qoder-cn"}
	default:
		return Options{}, errors.New("invalid Qoder edition")
	}
	var err error
	options.WorkingDirectory, err = filepath.Abs(options.WorkingDirectory)
	if err != nil {
		return Options{}, errors.New("invalid Qoder launch directory")
	}
	options.PrefixArgs = slices.Clone(options.PrefixArgs)
	if options.Environment == nil {
		options.Environment = os.Environ()
	}
	options.Environment = slices.Clone(options.Environment)
	if options.PersonalAccessToken != "" {
		if strings.ContainsAny(options.PersonalAccessToken, "\x00\r\n") {
			return Options{}, errors.New("invalid Qoder access token")
		}
		options.Environment = append(options.Environment, tokenEnvironment(options.RuntimeID)+"="+options.PersonalAccessToken)
		// Retain the credential in its only native channel.
		options.PersonalAccessToken = ""
	}
	if options.Executable == "" {
		for _, name := range candidates {
			process, err := agentclient.NewProcess(agentclient.ProcessSpec{Executable: name, Env: options.Environment, Dir: options.WorkingDirectory})
			if err == nil {
				options.Executable = process.Executable()
				return options, nil
			}
			if !errors.Is(err, exec.ErrNotFound) {
				return Options{}, err
			}
		}
		return Options{}, exec.ErrNotFound
	}
	return options, nil
}

func tokenEnvironment(runtimeID string) string {
	if runtimeID == "qoder-cn" {
		return "QODERCN_PERSONAL_ACCESS_TOKEN"
	}
	return "QODER_PERSONAL_ACCESS_TOKEN"
}

func selectedToken(options Options) string {
	key := tokenEnvironment(options.RuntimeID)
	for i := len(options.Environment) - 1; i >= 0; i-- {
		name, value, valid := strings.Cut(options.Environment[i], "=")
		if valid && (name == key || runtime.GOOS == "windows" && strings.EqualFold(name, key)) {
			return value
		}
	}
	return ""
}

// Close reaps the same management and ACP processes and permits cleanup retry.
func (c *Client) Close(ctx context.Context) error {
	return errors.Join(c.management.close(ctx), c.Client.Close(ctx))
}

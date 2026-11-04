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
	"strings"

	agentclient "github.com/JieWaZi/acp-go/pkg/agent_next"
	"github.com/JieWaZi/acp-go/pkg/agent_next/acpclient"
)

// Options freezes the regional launcher and credentials selected by the caller.
type Options struct {
	// RuntimeID selects qoder or qoder-cn.
	RuntimeID string
	// Executable overrides discovery of the regional CLI.
	Executable string
	// PrefixArgs precedes the native ACP or management arguments.
	PrefixArgs []string
	// Environment is the complete launch environment, or nil to snapshot the host.
	Environment []string
	// WorkingDirectory is the launch directory for all native commands.
	WorkingDirectory string
	// PersonalAccessToken overrides the selected region's token environment variable.
	PersonalAccessToken string
	// ExpectedUserID binds native account and quota reads to a previously observed subject.
	ExpectedUserID string
	// Logger receives safe process diagnostics.
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

// NewClient freezes a Qoder launch without starting a child process.
func NewClient(ctx context.Context, options Options, host agentclient.HostCallbacks) (*Client, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	options, err := freezeOptions(options)
	if err != nil {
		return nil, err
	}
	client, err := acpclient.NewClient(ctx, acpclient.Options{
		Process: agentclient.ProcessSpec{
			Executable: options.Executable, Args: append(slices.Clone(options.PrefixArgs), "--acp"),
			Env: options.Environment, Dir: options.WorkingDirectory, Redact: agentclient.RedactProcessDiagnostic,
		},
		Version:   acpclient.VersionProbe{Args: append(slices.Clone(options.PrefixArgs), "--version"), Required: true},
		RuntimeID: options.RuntimeID, Logger: options.Logger,
		Protocol: acpclient.Protocol{
			NormalizeOption: normalizeOption,
			SessionMissing:  sessionMissing,
			Authentication:  acpclient.Authentication{Disabled: tokenSelected(options)},
		},
	}, host)
	if err != nil {
		return nil, err
	}
	options.Executable = client.Executable()
	return &Client{Client: client, management: management{options: options}}, nil
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

func tokenSelected(options Options) bool {
	key := tokenEnvironment(options.RuntimeID)
	for i := len(options.Environment) - 1; i >= 0; i-- {
		name, value, valid := strings.Cut(options.Environment[i], "=")
		if valid && (name == key || runtime.GOOS == "windows" && strings.EqualFold(name, key)) {
			return value != ""
		}
	}
	return false
}

// Close reaps the same management and ACP processes and permits cleanup retry.
func (c *Client) Close(ctx context.Context) error {
	return errors.Join(c.management.close(ctx), c.Client.Close(ctx))
}

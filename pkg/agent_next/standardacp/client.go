// Package standardacp configures native CLI profiles for the shared ACP client.
package standardacp

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/JieWaZi/acp-go/pkg/acpmeta"
	agentclient "github.com/JieWaZi/acp-go/pkg/agent_next"
	"github.com/JieWaZi/acp-go/pkg/agent_next/acpclient"
	acp "github.com/JieWaZi/acp-go/sdk"
)

// Provider identifies a native standard ACP launcher.
type Provider string

const (
	Kimi     Provider = "kimi"
	Gemini   Provider = "gemini"
	Grok     Provider = "grok"
	OpenCode Provider = "opencode"
)

// Options freezes the executable, credential environment, and native launch policy.
type Options struct {
	// Provider selects the documented native CLI profile.
	Provider Provider
	// Executable overrides lookup in the frozen environment.
	Executable string
	// PrefixArgs precede the CLI's native ACP or version arguments.
	PrefixArgs []string
	// Environment is the complete child environment. Nil snapshots the host environment.
	Environment []string
	// WorkingDirectory is the absolute native workspace after resolution.
	WorkingDirectory string
	// PermissionMode selects a supported native approval policy.
	PermissionMode string
	// Logger receives safe SDK diagnostics.
	Logger *slog.Logger
	// RequireVerifiedMCP rejects MCP requests that lack a native loading receipt.
	RequireVerifiedMCP bool
}

// NewClient freezes the selected CLI without launching it.
func NewClient(ctx context.Context, options Options, host agentclient.HostCallbacks) (*acpclient.Client, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	cwd, err := filepath.Abs(options.WorkingDirectory)
	if err != nil {
		return nil, errors.New("cannot resolve native ACP working directory")
	}
	if options.Environment == nil {
		options.Environment = os.Environ()
	}
	environment := slices.Clone(options.Environment)
	policy := options.PermissionMode
	switch policy {
	case "", "default", "auto", "full-access":
	default:
		return nil, errors.New("unknown native ACP permission mode")
	}
	protocol := acpclient.Protocol{
		PromptUsage: acpclient.UsageUnattributed, RequireExactMissing: true,
		SessionMissing: exactResourceSessionMissing,
		ValidateInitialize: func(response acp.InitializeResponse) error {
			return validateIdentity(options.Provider, response)
		},
	}
	args := []string{"acp"}
	switch options.Provider {
	case Kimi:
		protocol.OpeningMode = acp.SessionModeId(policy)
		if policy == "full-access" {
			protocol.OpeningMode = "yolo"
		}
		home := kimiProfileHome(environment, cwd, "KIMI_CODE_HOME", ".kimi-code")
		protocol.LegacyModel = kimiLegacyModelBehavior(environment, cwd)
		protocol.CapturePromptJournal = func(ctx context.Context, scope acpclient.PromptJournalScope) func(context.Context, *acp.StopReason) acpclient.PromptJournalEvidence {
			return captureKimiJournal(ctx, scope, home)
		}
		protocol.ForkAllowed = func(response acp.InitializeResponse) bool {
			// The fixed 2.0.2 contract returns the new native ID and supports cleanup.
			return response.AgentInfo != nil &&
				response.AgentInfo.Version == kimiWireVersion &&
				response.AgentCapabilities.SessionCapabilities.Close != nil
		}
		protocol.Authentication = acpclient.Authentication{Method: "login", RetireOnLogout: true}
		protocol.SessionMissing = kimiSessionMissing
	case Gemini:
		args = []string{"--acp"}
		// Gemini restores project state using the process launch workspace.
		protocol.CwdBoundProcess = true
		protocol.RequireLoadEvidence = true
		protocol.ValidateRestoreID = func(id acp.SessionId) bool {
			value := string(id)
			return strings.TrimSpace(value) == value && value != "latest" && strings.Trim(value, "0123456789") != ""
		}
		// The pinned setter updates the current native session's model before ACK.
		protocol.LegacyModel.ConfirmsValue = func(version string) bool { return version == "0.62.0" }
		protocol.SessionMissing = func(err *acp.RequestError, id acp.SessionId) bool {
			return exactResourceSessionMissing(err, id) || err.Code == -32602 && err.Message == "Session not found: "+string(id)
		}
		protocol.ReadPromptUsage = func(_ string, _ *acpclient.PromptUsageCounters, meta map[string]any) (*acpmeta.TurnUsage, acpclient.PromptUsageScope) {
			return geminiPromptUsage(meta), acpclient.UsageUnattributed
		}
		if policy == "auto" {
			return nil, errors.New("Gemini has no native automatic risk review mode")
		}
		if policy == "full-access" {
			args = append(args, "--approval-mode", "yolo")
		} else if policy == "default" {
			args = append(args, "--approval-mode", "default")
		}
	case Grok:
		args = []string{"--no-auto-update", "agent", "stdio"}
		protocol.BlockUntypedExtensions = true
		protocol.ReadPromptUsage = func(_ string, _ *acpclient.PromptUsageCounters, meta map[string]any) (*acpmeta.TurnUsage, acpclient.PromptUsageScope) {
			return grokPromptUsage(meta), acpclient.UsageUnattributed
		}
		protocol.ReadModelConfig = grokModelConfig
		protocol.StartupAuthentication = func(response acp.InitializeResponse) string {
			// The native handshake already activates cached authentication. Calling
			// cached_token can fall through to a browser if the token expired.
			if response.Meta["defaultAuthMethodId"] == "xai.api_key" {
				return "xai.api_key"
			}
			return ""
		}
		protocol.PrepareSessionMeta = func(meta map[string]any) {
			// The native requirements gate still decides if auto is available.
			meta["autoMode"] = policy == "auto"
			meta["yoloMode"] = policy == "full-access"
		}
	case OpenCode:
		if policy == "auto" {
			return nil, errors.New("OpenCode has no native automatic risk review mode")
		}
		if policy == "full-access" {
			environment = append(environment, `OPENCODE_PERMISSION={"*":"allow"}`)
		}
		// The advertised ACP method is a terminal redirect or a no-op.
		protocol.Authentication.Disabled = true
		protocol.SessionMissing = openCodeSessionMissing
		protocol.RecoverRestore = openCodeRestoreRecovery
		protocol.EmptyAnswerFailure = openCodeEmptyAnswer
		protocol.DirectoryScopedMCP = true
		protocol.ReadPromptUsage = openCodePromptUsage
	default:
		return nil, errors.New("unknown standard ACP provider")
	}
	executable := options.Executable
	if executable == "" {
		executable = string(options.Provider)
	}
	return acpclient.NewClient(ctx, acpclient.Options{
		Process: agentclient.ProcessSpec{
			Executable: executable, Args: append(slices.Clone(options.PrefixArgs), args...),
			Env: environment, Dir: cwd,
		},
		RuntimeID: string(options.Provider), Logger: options.Logger, Protocol: protocol,
		Version:            acpclient.VersionProbe{Args: append(slices.Clone(options.PrefixArgs), "--version")},
		RequireVerifiedMCP: options.RequireVerifiedMCP,
	}, host)
}

func validateIdentity(provider Provider, response acp.InitializeResponse) error {
	name := ""
	if response.AgentInfo != nil {
		name = response.AgentInfo.Name
	}
	var valid bool
	switch provider {
	case Kimi:
		valid = name == "Kimi Code CLI"
	case Gemini:
		valid = name == "gemini-cli"
	case Grok:
		// Grok Build emits no agentInfo in its ACP initialize response.
		valid = response.Meta["grokShell"] == true
	case OpenCode:
		valid = strings.EqualFold(name, "OpenCode")
	}
	if !valid {
		return errors.New("ACP initialization did not identify the selected native CLI")
	}
	return nil
}

func kimiSessionMissing(err *acp.RequestError, id acp.SessionId) bool {
	data, _ := err.Data.(map[string]any)
	return exactResourceSessionMissing(err, id) || err.Code == -32602 && (data["session_id"] == "Session not found" ||
		data["sessionId"] == string(id) && err.Message == "Invalid params: Unknown sessionId: "+string(id))
}

func openCodeSessionMissing(err *acp.RequestError, id acp.SessionId) bool {
	data, _ := err.Data.(map[string]any)
	return exactResourceSessionMissing(err, id) || err.Code == -32602 && data["sessionId"] == string(id) &&
		err.Message == "Invalid params: session not found: "+string(id)
}

func exactResourceSessionMissing(err *acp.RequestError, id acp.SessionId) bool {
	data, _ := err.Data.(map[string]any)
	return err.Code == -32002 && data["uri"] == string(id)
}

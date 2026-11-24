package standardacp

import (
	"errors"
	"path/filepath"
	"regexp"
	"strings"

	acp "github.com/JieWaZi/acp-go-sdk"
	"github.com/JieWaZi/acp-go/pkg/acpclient"
	"github.com/JieWaZi/acp-go/pkg/acpmeta"
)

var antigravitySessionID = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
var antigravityRelease = regexp.MustCompile(`^v?(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$`)

func configureAntigravity(options Options, protocol *acpclient.Protocol) ([]string, error) {
	// agy advertises HTTP MCP only when each session has its own worker.
	args := []string{"acp", "--process-per-session"}
	profile := environmentValue(options.Environment, "GEMINI_HOME")
	if catalog := options.AntigravityCatalogHome; catalog != "" {
		if !filepath.IsAbs(catalog) || filepath.Clean(catalog) == filepath.Clean(profile) {
			return nil, errors.New("Antigravity model discovery requires a separate profile")
		}
		profile = catalog
	}
	if profile != "" {
		// CLI 1.5.x takes its data root from this flag and otherwise uses ~/.gemini.
		args = append(args, "--gemini-dir", profile)
	}
	switch options.PermissionMode {
	case "default", "auto":
		protocol.OpeningMode = acp.SessionModeId(options.PermissionMode)
	case "full-access":
		args = append(args, "--dangerously-skip-permissions")
	}
	protocol.CwdBoundProcess = true
	protocol.RequireLoadHistory = true
	protocol.ValidateRestoreID = func(id acp.SessionId) bool { return antigravitySessionID.MatchString(string(id)) }
	protocol.SessionMissing = antigravitySessionMissing
	protocol.RecoverRestore = antigravityRestoreRecovery
	protocol.DisposableModelCatalog = options.AntigravityCatalogHome != ""
	protocol.ReadOnlyModelCatalog = !protocol.DisposableModelCatalog
	protocol.NormalizeOption = antigravityConfigOption
	protocol.ReadPromptUsage = antigravityPromptUsage
	protocol.Authentication.RetireOnLogout = true
	validate := protocol.ValidateInitialize
	protocol.ValidateInitialize = func(response acp.InitializeResponse) error {
		if validate != nil {
			if err := validate(response); err != nil {
				return err
			}
		}
		if response.AgentInfo == nil || !SupportsAntigravityVersion(response.AgentInfo.Version) {
			return errors.New("Antigravity CLI ACP requires a stable release at version 1.5.0 or newer")
		}
		return nil
	}
	return args, nil
}

// SupportsAntigravityVersion reports whether a native version token supports the official ACP entry point.
func SupportsAntigravityVersion(version string) bool {
	parts := antigravityRelease.FindStringSubmatch(version)
	if parts == nil {
		return false
	}
	major, minor := parts[1], parts[2]
	// The expression enforces canonical decimals. Only minor versions within
	// major 1 need a second comparison against the introduction of agy acp.
	return major > "1" || major == "1" && (len(minor) > 1 || minor >= "5")
}

func antigravitySessionMissing(err *acp.RequestError, id acp.SessionId) bool {
	return exactResourceSessionMissing(err, id) || err.Code == -32002 && err.Message == "Session not found: "+string(id)
}

func antigravityRestoreRecovery(method string, err *acp.RequestError) acpclient.RestoreRecovery {
	if method == acp.AgentMethodSessionResume && err.Code == -32603 {
		// This native error is ambiguous. A bounded, workspace-scoped list can
		// prove the requested ID absent before the shared client retries.
		id, prefixed := strings.CutPrefix(err.Message, "Failed to read session ")
		id, suffixed := strings.CutSuffix(id, "; retry, or load it with session/load")
		if prefixed && suffixed && antigravitySessionID.MatchString(id) {
			return acpclient.RestoreCheckListing
		}
	}
	return acpclient.RestoreNoRecovery
}

func antigravityConfigOption(option acp.SessionConfigOption) acp.SessionConfigOption {
	if option.Select == nil || option.Select.Category != nil {
		return option
	}
	if option.Select.Id == "model" {
		option.Select.Category = acp.Ptr(acp.SessionConfigOptionCategoryModel)
	}
	return option
}

// The native prompt receipt covers this prompt's main agent. Antigravity
// excludes cache reads from input and includes thinking in output.
func antigravityPromptUsage(_ string, raw *acpclient.PromptUsageCounters, _ map[string]any) (*acpmeta.TurnUsage, acpclient.PromptUsageScope) {
	if raw == nil {
		return nil, acpclient.UsageUnavailable
	}
	report := &acpmeta.TurnUsage{Source: "antigravity.prompt.usage", Coverage: "main_loop"}
	if exactCounter(raw.Output) {
		report.OutputTokens = raw.Output
		if exactCounter(raw.Thought) && *raw.Thought <= *raw.Output {
			report.ReasoningTokens = raw.Thought
		}
	}
	if exactCounter(raw.CacheRead) {
		report.CacheReadTokens = raw.CacheRead
	}
	if exactCounter(raw.CacheWrite) {
		report.CacheWriteTokens = raw.CacheWrite
		if exactCounter(raw.Input) && *raw.CacheWrite <= *raw.Input {
			report.InputTokens = acp.Ptr(*raw.Input - *raw.CacheWrite)
		}
	}
	if report.InputTokens == nil && report.OutputTokens == nil && report.CacheReadTokens == nil && report.CacheWriteTokens == nil {
		// Retain an ambiguous input total as an observation without billing it.
		return nil, acpclient.UsageUnattributed
	}
	if report.InputTokens == nil && exactCounter(raw.Input) {
		observation := acpmeta.UsageObservation{Source: report.Source, Coverage: "native_report", InputTotalTokens: raw.Input}
		if exactCounter(raw.Total) {
			observation.TotalTokens = raw.Total
		}
		report.Observations = []acpmeta.UsageObservation{observation}
	}
	return report, acpclient.UsageUnavailable
}

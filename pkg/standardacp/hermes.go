package standardacp

import (
	"errors"
	"strings"

	acp "github.com/JieWaZi/acp-go-sdk"
	"github.com/JieWaZi/acp-go/pkg/acpclient"
	"github.com/JieWaZi/acp-go/pkg/acpmeta"
)

func configureHermes(protocol *acpclient.Protocol, policy string) error {
	// The native modes govern edit approval. Sensitive-path checks still apply.
	switch policy {
	case "":
	case "default":
		protocol.OpeningMode = "default"
	case "accept-edits":
		protocol.OpeningMode = "accept_edits"
	case "dont-ask":
		protocol.OpeningMode = "dont_ask"
	default:
		return errors.New("Hermes ACP has no verified equivalent for the selected approval policy")
	}
	// Older Hermes returns an ID-less success after silently creating a new
	// session for a missing ID. Load can prove the requested history instead.
	protocol.PreferLoadForContinuation = true
	protocol.RequireLoadHistory = true
	// The verified latest-only native fork has no caller-selected boundary.
	protocol.ForkAllowed = func(response acp.InitializeResponse) bool {
		return acpmeta.ForkMode(response.Meta) == acpmeta.ForkTurn
	}
	protocol.ReadOnlyModelCatalog = true
	// The v0.17.0 setter acknowledges after recreating and saving the session agent.
	protocol.LegacyModel.ConfirmsValue = func(version string) bool { return version == "0.17.0" }
	protocol.LegacyModel.ResetsUsageCounter = true
	protocol.ReadPromptUsage = hermesPromptUsage
	protocol.FreshUsageOnRestore = func(response acp.InitializeResponse) bool {
		return response.AgentInfo != nil && hermesLegacyCounters(response.AgentInfo.Version)
	}
	return nil
}

func hermesPromptUsage(version string, _ *acpclient.PromptUsageCounters, _ map[string]any) (*acpmeta.TurnUsage, acpclient.PromptUsageScope) {
	if hermesLegacyCounters(version) {
		return nil, acpclient.UsageSessionCumulative
	}
	// The gateway-backed adapter can return the latest session usage on a
	// recovered prompt receipt. Keep it as an observation until correlated.
	return nil, acpclient.UsageUnattributed
}

func hermesLegacyCounters(version string) bool {
	// The counter shape cannot distinguish the verified in-process owner
	// from a Gateway session that also counts prompts from other clients.
	return strings.TrimPrefix(version, "v") == "0.17.0"
}

package workbuddy

import (
	"encoding/json"
	"math"
	"regexp"

	"github.com/JieWaZi/acp-go/pkg/acpmeta"
	"github.com/JieWaZi/acp-go/pkg/acpclient"
	acp "github.com/JieWaZi/acp-go/sdk"
)

// CodeBuddy's ACP docs specify a lowercase UUIDv7 without separators.
var conversationID = regexp.MustCompile(`^[0-9a-f]{12}7[0-9a-f]{3}[89ab][0-9a-f]{15}$`)

func readPromptEvidence(reason acp.StopReason, userMessageID string, meta map[string]any) acpclient.PromptEvidence {
	evidence := acpclient.PromptEvidence{InputAccepted: userMessageID != "" && len(userMessageID) <= 512}
	if id, ok := meta["codebuddy.ai/conversationRequestId"].(string); ok && conversationID.MatchString(id) {
		evidence.NativeTurnID = id
	}
	if detail, ok := meta["codebuddy.ai/errorMessage"].(string); ok && detail != "" && reason == acp.StopReasonRefusal {
		evidence.Failure = &acp.RequestError{Code: -32603, Message: "CodeBuddy reported a prompt failure"}
		var structured struct {
			// Code is the provider's ACP error class when emitted as structured text.
			Code int `json:"code"`
		}
		if len(detail) <= 64<<10 && json.Unmarshal([]byte(detail), &structured) == nil && structured.Code == -32000 {
			evidence.Failure = acp.NewAuthRequired(map[string]any{"classification": "authentication_lost", "restartRequired": true})
		}
	}
	return evidence
}

func executionOption(category acp.SessionConfigOptionCategory) bool {
	switch category {
	case "mode", "sandbox", "context_window", "agent", "_codebuddy.ai/multitask", "_codebuddy.ai/codeMode":
		return true
	default:
		return false
	}
}

func normalizeUsage(update *acp.SessionUsageUpdate) *acpmeta.UsageObservation {
	if update.Cost != nil && update.Cost.Currency == "" {
		if amount := update.Cost.Amount; amount >= 0 {
			if update.Meta == nil {
				update.Meta = make(map[string]any)
			}
			update.Meta["codebuddy.ai/credit"] = amount
		}
		update.Cost = nil
	}
	raw, ok := update.Meta["usage"].(map[string]any)
	if !ok {
		return nil
	}
	// CodeBuddy 2.151 selects a single request's provider-defined usage. Raw
	// input/cache semantics vary by model, and a credit is not a currency.
	// Record only directly comparable counters as an auxiliary observation.
	output := readCounter(raw, "output_tokens", "outputTokens", "completion_tokens")
	if output == nil {
		return nil
	}
	return &acpmeta.UsageObservation{
		Source: "codebuddy/usage_update", Coverage: "latest_model_request",
		Tokens: acpmeta.ModelUsage{OutputTokens: output},
	}
}

func readCounter(raw map[string]any, keys ...string) *int64 {
	var result *int64
	for _, key := range keys {
		value, present := raw[key]
		if !present {
			continue
		}
		number, ok := value.(float64)
		if !ok || number < 0 || number > 1<<53-1 || math.Trunc(number) != number ||
			(result != nil && *result != int64(number)) {
			return nil
		}
		result = acp.Ptr(int64(number))
	}
	return result
}

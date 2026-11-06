package standardacp

import (
	acp "github.com/JieWaZi/acp-go-sdk"
	"github.com/JieWaZi/acp-go/pkg/acpclient"
)

func openCodeFailureKind(err *acp.RequestError) string {
	if err.Code != -32603 {
		return ""
	}
	data, _ := err.Data.(map[string]any)
	switch data["errorName"] {
	case "provider.quota", "provider.free-limit", "provider.go-limit", "provider.chatgpt-limit", "provider.budget-limit", "provider.model-budget-limit":
		return "quota_exhausted"
	case "provider.rate-limit":
		return "rate_limit"
	default:
		return ""
	}
}

func openCodeRestoreRecovery(method string, native *acp.RequestError) acpclient.RestoreRecovery {
	if native.Code != -32603 {
		return acpclient.RestoreNoRecovery
	}
	data, _ := native.Data.(map[string]any)
	if method == acp.AgentMethodSessionResume && data["errorName"] == "ServiceUnavailableError" {
		// OpenCode's internal server can still be starting its plugins.
		return acpclient.RestoreRetryAfterStartup
	}
	if native.Message == "OpenCode service failure" || native.Message == "Internal error: OpenCode service failure" {
		// The internal-server wrapper cannot distinguish unavailable history.
		return acpclient.RestoreCheckListing
	}
	return acpclient.RestoreNoRecovery
}

func openCodeEmptyAnswer(reasoned bool) *acp.RequestError {
	message, classification := "OpenCode ended the prompt without an answer", "empty_answer"
	if reasoned {
		message, classification = "OpenCode reported reasoning without an answer", "reasoning_without_answer"
	}
	return &acp.RequestError{Code: -32603, Message: message, Data: map[string]any{
		"method": acp.AgentMethodSessionPrompt, "classification": classification,
	}}
}

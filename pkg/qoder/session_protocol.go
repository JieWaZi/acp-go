package qoder

import (
	"errors"

	acp "github.com/JieWaZi/acp-go-sdk"
)

// ErrModelUnavailable reports native refusal to open the selected model for this account.
var ErrModelUnavailable = errors.New("Qoder refused to open the requested model")

func classifySessionOpenError(model string, peer *acp.RequestError) error {
	// Qoder 1.1.83 uses generic code 500; only the exact refusal naming the requested model proves unavailability.
	if model != "" && peer.Code == 500 && peer.Message == `Model "`+model+`" is not available.` {
		return errors.Join(ErrModelUnavailable, &acp.RequestError{Code: peer.Code, Message: peer.Message, Data: map[string]any{"classification": "model_unavailable"}})
	}
	return nil
}

// Both Qoder editions can report reasoning_effort as a second model selector.
func normalizeOption(option acp.SessionConfigOption) acp.SessionConfigOption {
	if option.Select != nil && option.Select.Id == "reasoning_effort" && option.Select.Category != nil &&
		*option.Select.Category == acp.SessionConfigOptionCategoryModel {
		selection := *option.Select
		selection.Category = acp.Ptr(acp.SessionConfigOptionCategoryThoughtLevel)
		option.Select = &selection
	}
	return option
}

// Qoder 1.1.53 uses an invalid-parameters response for an absent session.
func sessionMissing(err *acp.RequestError, id acp.SessionId) bool {
	data, ok := err.Data.(map[string]any)
	return err.Code == -32602 && ok && data["code"] == "INVALID_SESSION_IDENTIFIER" && data["sessionId"] == string(id)
}

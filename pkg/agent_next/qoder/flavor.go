package qoder

import acp "github.com/JieWaZi/acp-go/sdk"

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

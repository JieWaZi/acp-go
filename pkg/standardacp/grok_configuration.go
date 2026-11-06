package standardacp

import (
	acp "github.com/JieWaZi/acp-go-sdk"
	"github.com/JieWaZi/acp-go/pkg/acpmeta"
)

type grokEffortChoice struct {
	// ID is what the native setter accepts.
	ID string `json:"id"`
	// Value is the native ReasoningEffort enum for this choice.
	Value string `json:"value"`
	// Label is the name supplied by the native menu.
	Label string `json:"label"`
	// Description is the native menu's optional explanation.
	Description *string `json:"description"`
	// Default marks the model's preferred choice.
	Default *bool `json:"default"`
}

func grokModelConfig(meta map[string]any) (acpmeta.ModelConfigCatalog, bool) {
	supported, known := meta["supportsReasoningEffort"].(bool)
	if !known {
		return acpmeta.ModelConfigCatalog{}, false
	}
	if !supported {
		return acpmeta.ModelConfigCatalog{Options: []acp.SessionConfigOption{}}, true
	}
	var native []grokEffortChoice
	if raw, exists := meta["reasoningEfforts"]; exists && (!decodeMetadata(raw, &native) || native == nil) {
		return acpmeta.ModelConfigCatalog{}, false
	}
	if len(native) > 64 {
		return acpmeta.ModelConfigCatalog{}, false
	}
	if len(native) == 0 {
		// The pinned session_config.rs uses these five values when a model
		// explicitly supports effort but does not advertise its own menu.
		native = []grokEffortChoice{
			{ID: "minimal", Value: "minimal", Label: "Minimal", Default: acp.Ptr(false)},
			{ID: "low", Value: "low", Label: "Low", Default: acp.Ptr(false)},
			{ID: "medium", Value: "medium", Label: "Medium", Default: acp.Ptr(false)},
			{ID: "high", Value: "high", Label: "High", Default: acp.Ptr(false)},
			{ID: "xhigh", Value: "xhigh", Label: "X-High", Default: acp.Ptr(false)},
		}
	}
	choices := make(acp.SessionConfigSelectOptionsUngrouped, 0, len(native))
	var preferred acp.SessionConfigValueId
	seen := make(map[string]bool, len(native))
	for _, entry := range native {
		if entry.ID == "" || seen[entry.ID] || entry.Label == "" || entry.Default == nil || !grokEffortValue(entry.Value) {
			return acpmeta.ModelConfigCatalog{}, false
		}
		seen[entry.ID] = true
		id := acp.SessionConfigValueId(entry.ID)
		choices = append(choices, acp.SessionConfigSelectOption{
			Value: id, Name: entry.Label, Description: entry.Description, Meta: map[string]any{"default": *entry.Default},
		})
		if preferred == "" && *entry.Default {
			preferred = id
		}
	}
	if preferred == "" {
		// This mirrors the native menu's default selection when no row is marked.
		preferred = choices[0].Value
	}
	return acpmeta.ModelConfigCatalog{Options: []acp.SessionConfigOption{{Select: &acp.SessionConfigOptionSelect{
		Id: "reasoning_effort", Name: "Reasoning Effort", Category: acp.Ptr(acp.SessionConfigOptionCategory("thought_level")),
		Type: "select", CurrentValue: preferred, Options: acp.SessionConfigSelectOptions{Ungrouped: &choices},
	}}}}, true
}

func grokEffortValue(value string) bool {
	switch value {
	case "none", "minimal", "low", "medium", "high", "xhigh", "max":
		return true
	default:
		return false
	}
}

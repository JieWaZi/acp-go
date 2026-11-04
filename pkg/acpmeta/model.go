package acpmeta

import (
	"encoding/json"

	acp "github.com/JieWaZi/acp-go/sdk"
)

const (
	modelConfigOptionsKey = "acp-go/model-config-options"
	partialModelConfigKey = "acp-go/partial-model-config"
)

// ModelConfigCatalog describes known choices and the configurations still unknown.
// An empty Options list with no UnknownConfigIDs confirms that there are no choices.
type ModelConfigCatalog struct {
	// Options lists the configurations the provider described for this model.
	Options []acp.SessionConfigOption `json:"options"`
	// UnknownConfigIDs lists configurations for which the provider supplied no catalog.
	UnknownConfigIDs []acp.SessionConfigId `json:"unknown_config_ids,omitempty"`
}

// WithModelConfigOptions records a complete model catalog.
func WithModelConfigOptions(meta map[string]any, options []acp.SessionConfigOption) map[string]any {
	return WithModelConfigCatalog(meta, ModelConfigCatalog{Options: options})
}

// WithModelConfigCatalog copies metadata and records a complete or partial catalog.
// A partial catalog omits the legacy key so existing consumers treat it as unknown.
func WithModelConfigCatalog(meta map[string]any, catalog ModelConfigCatalog) map[string]any {
	result := make(map[string]any, len(meta)+1)
	for key, value := range meta {
		if key != modelConfigOptionsKey && key != partialModelConfigKey {
			result[key] = value
		}
	}
	if catalog.Options == nil {
		catalog.Options = []acp.SessionConfigOption{}
	}
	if len(catalog.UnknownConfigIDs) == 0 {
		result[modelConfigOptionsKey] = catalog.Options
	} else {
		result[partialModelConfigKey] = catalog
	}
	return result
}

// ModelConfigOptions reads the complete model catalog. Partial and absent catalogs return false.
func ModelConfigOptions(meta map[string]any) ([]acp.SessionConfigOption, bool) {
	if _, partial := meta[partialModelConfigKey]; partial {
		return nil, false
	}
	var options []acp.SessionConfigOption
	if !decodeModelConfig(meta[modelConfigOptionsKey], &options) || options == nil {
		return nil, false
	}
	return options, true
}

// ReadModelConfigCatalog reads a reported catalog with any per-configuration unknowns.
func ReadModelConfigCatalog(meta map[string]any) (ModelConfigCatalog, bool) {
	if value, partial := meta[partialModelConfigKey]; partial {
		var catalog ModelConfigCatalog
		if !decodeModelConfig(value, &catalog) || catalog.Options == nil || len(catalog.UnknownConfigIDs) == 0 {
			return ModelConfigCatalog{}, false
		}
		return catalog, true
	}
	options, reported := ModelConfigOptions(meta)
	return ModelConfigCatalog{Options: options}, reported
}

// decodeModelConfig accepts both in-process metadata and maps decoded from ACP JSON.
func decodeModelConfig(value any, dst any) bool {
	encoded, err := json.Marshal(value)
	return err == nil && json.Unmarshal(encoded, dst) == nil
}

// ModelCatalogMethod reads the native model catalog without sending a prompt.
const ModelCatalogMethod = "_model/list"

// ModelCatalog describes known model options from one native read.
type ModelCatalog struct {
	// DefaultID is present only when the provider identified the default row.
	DefaultID *acp.SessionConfigValueId `json:"default_id,omitempty"`
	// Options preserves provider grouping and distinguishes an empty catalog from unknown.
	Options acp.SessionConfigSelectOptions `json:"options"`
}

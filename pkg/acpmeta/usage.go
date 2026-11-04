package acpmeta

import (
	"encoding/json"
	"math"
)

const turnUsageKey = "acp-go/turn-usage"

// TurnUsage is a replaceable invocation snapshot with independent token and valuation evidence.
type TurnUsage struct {
	// InputTokens counts input excluding cache reads and writes.
	InputTokens *int64 `json:"inputTokens,omitempty"`
	// CacheReadTokens counts input read from cache.
	CacheReadTokens *int64 `json:"cacheReadTokens,omitempty"`
	// CacheWriteTokens counts input written to cache.
	CacheWriteTokens *int64 `json:"cacheWriteTokens,omitempty"`
	// OutputTokens includes reasoning tokens when those are also reported separately.
	OutputTokens *int64 `json:"outputTokens,omitempty"`
	// ReasoningTokens is a separately observed subset of output.
	ReasoningTokens *int64 `json:"reasoningTokens,omitempty"`
	// Source identifies the native token observation.
	Source string `json:"source"`
	// Coverage describes which native calls the token snapshot covers.
	Coverage string `json:"coverage,omitempty"`
	// Complete means the snapshot covers the stated invocation; individual components can still be unknown.
	Complete bool `json:"complete"`
	// ContextKnown means the same notification has evidence for context used and size.
	ContextKnown bool `json:"contextKnown"`
	// Model is present only when the native report identifies one model for these totals.
	Model string `json:"model,omitempty"`
	// Cost is independent of token coverage and is absent without a reliable valuation.
	Cost *UsageCost `json:"cost,omitempty"`
	// Models attributes the same totals to models. It is not additional usage.
	// Nil is unknown; an empty map is a known empty attribution.
	Models map[string]ModelUsage `json:"models"`
}

// ModelUsage attributes known counters and cost to one native model.
type ModelUsage struct {
	// InputTokens counts uncached input.
	InputTokens *int64 `json:"inputTokens,omitempty"`
	// CacheReadTokens counts input read from cache.
	CacheReadTokens *int64 `json:"cacheReadTokens,omitempty"`
	// CacheWriteTokens counts input written to cache.
	CacheWriteTokens *int64 `json:"cacheWriteTokens,omitempty"`
	// OutputTokens includes any separately reported reasoning tokens.
	OutputTokens *int64 `json:"outputTokens,omitempty"`
	// ReasoningTokens is an optional subset of output.
	ReasoningTokens *int64 `json:"reasoningTokens,omitempty"`
	// Cost describes only this model, independently of known token components.
	Cost *UsageCost `json:"cost,omitempty"`
}

// UsageCost describes an independently measured or estimated monetary amount.
type UsageCost struct {
	// Amount is encoded as decimal text in metadata to survive ACP's generic JSON maps.
	Amount json.Number `json:"amount,string"`
	// Currency names the reported currency.
	Currency string `json:"currency"`
	// Source identifies the native report or calculation that produced the amount.
	Source string `json:"source"`
	// Scope identifies the work covered by the amount independently of token coverage.
	Scope string `json:"scope"`
	// Aggregation distinguishes a turn delta from a cumulative observation.
	Aggregation string `json:"aggregation"`
	// Valuation distinguishes an estimate from reported charges.
	Valuation string `json:"valuation"`
}

// valid checks valuation metadata and rejects invalid monetary numbers.
func (c UsageCost) valid() bool {
	if c.Currency == "" || c.Source == "" || c.Scope == "" || c.Aggregation == "" || c.Valuation == "" {
		return false
	}
	value, err := c.Amount.Float64()
	if err != nil || value < 0 || math.IsNaN(value) || math.IsInf(value, 0) {
		return false
	}
	_, err = json.Marshal(c.Amount)
	return err == nil && c.Amount != ""
}

// WithTurnUsage copies metadata and writes independent invocation consumption evidence.
func WithTurnUsage(meta map[string]any, usage TurnUsage) map[string]any {
	result := copyMetadata(meta)
	value := encodeUsageCounters(ModelUsage{
		InputTokens: usage.InputTokens, OutputTokens: usage.OutputTokens,
		CacheReadTokens: usage.CacheReadTokens, CacheWriteTokens: usage.CacheWriteTokens,
		ReasoningTokens: usage.ReasoningTokens, Cost: usage.Cost,
	})
	value["source"], value["complete"], value["contextKnown"] = usage.Source, usage.Complete, usage.ContextKnown
	if usage.Coverage != "" {
		value["coverage"] = usage.Coverage
	}
	if usage.Model != "" {
		value["model"] = usage.Model
	}
	if usage.Models != nil {
		models := make(map[string]any, len(usage.Models))
		for id, model := range usage.Models {
			models[id] = encodeUsageCounters(model)
		}
		value["models"] = models
	}
	result[turnUsageKey] = value
	return result
}

// ReadTurnUsage decodes invocation evidence before or after an ACP JSON round trip.
func ReadTurnUsage(meta map[string]any) (TurnUsage, bool) {
	value, ok := meta[turnUsageKey].(map[string]any)
	if !ok {
		return TurnUsage{}, false
	}
	source, sourceOK := value["source"].(string)
	complete, completeOK := value["complete"].(bool)
	contextKnown, contextOK := value["contextKnown"].(bool)
	counters, countersOK := decodeUsageCounters(value)
	if !sourceOK || source == "" || !completeOK || !contextOK || !countersOK {
		return TurnUsage{}, false
	}
	result := TurnUsage{
		Source: source, Complete: complete, ContextKnown: contextKnown,
		InputTokens: counters.InputTokens, OutputTokens: counters.OutputTokens,
		CacheReadTokens: counters.CacheReadTokens, CacheWriteTokens: counters.CacheWriteTokens,
		ReasoningTokens: counters.ReasoningTokens, Cost: counters.Cost,
	}
	if coverage, exists := value["coverage"]; exists {
		var valid bool
		result.Coverage, valid = coverage.(string)
		if !valid {
			return TurnUsage{}, false
		}
	}
	if model, exists := value["model"]; exists {
		var valid bool
		result.Model, valid = model.(string)
		if !valid {
			return TurnUsage{}, false
		}
	}
	if raw, exists := value["models"]; exists {
		models, ok := raw.(map[string]any)
		if !ok {
			return TurnUsage{}, false
		}
		result.Models = make(map[string]ModelUsage, len(models))
		for id, rawModel := range models {
			model, object := rawModel.(map[string]any)
			detail, valid := decodeUsageCounters(model)
			if id == "" || !object || !valid {
				return TurnUsage{}, false
			}
			result.Models[id] = detail
		}
	}
	return result, true
}

// encodeUsageCounters shares the precise token and cost encoding between totals and attribution.
func encodeUsageCounters(usage ModelUsage) map[string]any {
	value := make(map[string]any)
	fields := map[string]*int64{
		"inputTokens": usage.InputTokens, "cacheReadTokens": usage.CacheReadTokens,
		"cacheWriteTokens": usage.CacheWriteTokens, "outputTokens": usage.OutputTokens,
		"reasoningTokens": usage.ReasoningTokens,
	}
	for key, counter := range fields {
		if counter != nil {
			value[key] = *counter
		}
	}
	if usage.Cost != nil && usage.Cost.valid() {
		c := *usage.Cost
		value["cost"] = map[string]any{
			"amount": c.Amount.String(), "currency": c.Currency, "source": c.Source,
			"scope": c.Scope, "aggregation": c.Aggregation, "valuation": c.Valuation,
		}
	}
	return value
}

// decodeUsageCounters validates optional counters without coercing unknown values to zero.
func decodeUsageCounters(value map[string]any) (ModelUsage, bool) {
	var result ModelUsage
	targets := map[string]**int64{
		"inputTokens": &result.InputTokens, "cacheReadTokens": &result.CacheReadTokens,
		"cacheWriteTokens": &result.CacheWriteTokens, "outputTokens": &result.OutputTokens,
		"reasoningTokens": &result.ReasoningTokens,
	}
	for key, target := range targets {
		if raw, exists := value[key]; exists && raw != nil {
			counter, valid := usageInteger(raw)
			if !valid {
				return ModelUsage{}, false
			}
			*target = &counter
		}
	}
	if raw, exists := value["cost"]; exists {
		data, err := json.Marshal(raw)
		var cost UsageCost
		if err != nil || json.Unmarshal(data, &cost) != nil || !cost.valid() {
			return ModelUsage{}, false
		}
		result.Cost = &cost
	}
	return result, true
}

// usageInteger 校验非负整数并拒绝 JSON 浮点精度不可靠的计数。
func usageInteger(value any) (int64, bool) {
	var result int64
	switch number := value.(type) {
	case int64:
		result = number
	case int:
		result = int64(number)
	case json.Number:
		var err error
		result, err = number.Int64()
		if err != nil {
			return 0, false
		}
	case float64:
		if number < 0 || number > (1<<53)-1 || math.Trunc(number) != number {
			return 0, false
		}
		result = int64(number)
	default:
		return 0, false
	}
	return result, result >= 0
}

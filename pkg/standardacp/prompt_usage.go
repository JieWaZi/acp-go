package standardacp

import (
	"encoding/json"

	acp "github.com/JieWaZi/acp-go-sdk"
	"github.com/JieWaZi/acp-go/pkg/acpclient"
	"github.com/JieWaZi/acp-go/pkg/acpmeta"
)

const maxExactMetadataCounter = 1<<53 - 1

type grokCounters struct {
	// Input is the full input including cache reads and writes.
	Input *int64 `json:"inputTokens"`
	// Output includes reasoning.
	Output *int64 `json:"outputTokens"`
	// CacheRead counts cached input reads.
	CacheRead *int64 `json:"cachedReadTokens"`
	// CacheWrite counts cache creation input.
	CacheWrite *int64 `json:"cacheCreationTokens"`
	// Reasoning is the reasoning subset of output.
	Reasoning *int64 `json:"reasoningTokens"`
}

func (usage grokCounters) tokens() (acpmeta.ModelUsage, bool) {
	for _, count := range []*int64{usage.Input, usage.Output, usage.CacheRead, usage.CacheWrite} {
		if !exactCounter(count) {
			return acpmeta.ModelUsage{}, false
		}
	}
	if *usage.CacheRead > *usage.Input || *usage.CacheWrite > *usage.Input-*usage.CacheRead {
		return acpmeta.ModelUsage{}, false
	}
	if usage.Reasoning != nil && (!exactCounter(usage.Reasoning) || *usage.Reasoning > *usage.Output) {
		return acpmeta.ModelUsage{}, false
	}
	return acpmeta.ModelUsage{InputTokens: acp.Ptr(*usage.Input - *usage.CacheRead - *usage.CacheWrite),
		OutputTokens: usage.Output, CacheReadTokens: usage.CacheRead, CacheWriteTokens: usage.CacheWrite,
		ReasoningTokens: usage.Reasoning}, true
}

func grokPromptUsage(meta map[string]any) *acpmeta.TurnUsage {
	var native struct {
		grokCounters
		// Incomplete marks the provider's potentially undercounted invocation ledger.
		Incomplete bool `json:"usageIsIncomplete"`
		// Models contains a decomposition of the same invocation counters.
		Models map[string]grokCounters `json:"modelUsage"`
	}
	// The outer flat usage fields describe the last main turn and have a
	// different coverage from the nested prompt ledger.
	if !decodeMetadata(meta["usage"], &native) {
		return nil
	}
	tokens, valid := native.grokCounters.tokens()
	if !valid {
		return nil
	}
	report := &acpmeta.TurnUsage{Source: "grok.prompt.usage", Coverage: "invocation", Complete: !native.Incomplete,
		InputTokens: tokens.InputTokens, OutputTokens: tokens.OutputTokens,
		CacheReadTokens: tokens.CacheReadTokens, CacheWriteTokens: tokens.CacheWriteTokens, ReasoningTokens: tokens.ReasoningTokens}
	if len(native.Models) > 0 && len(native.Models) <= 128 {
		models := make(map[string]acpmeta.ModelUsage, len(native.Models))
		for model, row := range native.Models {
			tokens, valid := row.tokens()
			if model == "" || !valid {
				return report
			}
			models[model] = tokens
		}
		report.Models = models
	}
	return report
}

type geminiCounts struct {
	// Input is provider input before a separately proven cache breakdown.
	Input *int64 `json:"input_tokens"`
	// Output counts the observed candidate tokens in the request loop.
	Output *int64 `json:"output_tokens"`
}

type geminiQuota struct {
	// Counts is the sum from this prompt's request loop.
	Counts geminiCounts `json:"token_count"`
	// Models groups the same observations by the native reported model.
	Models []struct {
		// ID identifies the model reported by the native request loop.
		ID string `json:"model"`
		// Counts describes the observed request counters for this model.
		Counts geminiCounts `json:"token_count"`
	} `json:"model_usage"`
}

// geminiNativePromptUsage reads the per-prompt ledger emitted by verified ACP releases.
func geminiNativePromptUsage(version string, raw *acpclient.PromptUsageCounters, meta map[string]any) (*acpmeta.TurnUsage, acpclient.PromptUsageScope) {
	fallback := geminiPromptUsage(meta)
	if (version != "0.65.0" && version != "0.67.0") || raw == nil || !exactCounter(raw.Input) || !exactCounter(raw.Output) || !exactCounter(raw.Total) ||
		*raw.Input+*raw.Output != *raw.Total || *raw.Total == 0 || raw.CacheWrite != nil {
		return fallback, acpclient.UsageUnattributed
	}
	read, thought := raw.CacheRead, raw.Thought
	// Native prompt-local accumulators serialize these counters only when positive.
	if read == nil {
		read = acp.Ptr(int64(0))
	}
	if thought == nil {
		thought = acp.Ptr(int64(0))
	}
	if !exactCounter(read) || !exactCounter(thought) || *read > *raw.Input || *thought > maxExactMetadataCounter-*raw.Output {
		return fallback, acpclient.UsageUnattributed
	}
	// Gemini input includes cached content; its ACP output and total exclude thoughts.
	// Cache creation is not reported, and auxiliary requests are outside this ledger.
	report := &acpmeta.TurnUsage{Source: "gemini.prompt.usage", Coverage: "observed_requests",
		InputTokens: acp.Ptr(*raw.Input - *read), CacheReadTokens: read,
		OutputTokens: acp.Ptr(*raw.Output + *thought), ReasoningTokens: thought}
	if fallback != nil {
		report.Observations = fallback.Observations
		report.Models = fallback.Models
		if len(fallback.Models) > 0 {
			// Per-model quota lacks cache and thoughts; keep its candidate evidence separate.
			report.Observations = append(report.Observations, acpmeta.UsageObservation{
				Source: fallback.Source, Coverage: "provider_candidate_output", Models: fallback.Models})
		}
	}
	var quota geminiQuota
	if decodeMetadata(meta["quota"], &quota) && len(quota.Models) == 1 {
		model := quota.Models[0]
		if model.ID != "" && exactCounter(model.Counts.Input) && exactCounter(model.Counts.Output) &&
			*model.Counts.Input == *raw.Input && *model.Counts.Output == *raw.Output {
			// One matching model covers the same ledger; mixed models retain only candidate subtotals.
			report.Model = model.ID
			report.Models = map[string]acpmeta.ModelUsage{model.ID: {InputTokens: report.InputTokens, OutputTokens: report.OutputTokens,
				CacheReadTokens: report.CacheReadTokens, ReasoningTokens: report.ReasoningTokens}}
		}
	}
	return report, acpclient.UsageUnavailable
}

func geminiPromptUsage(meta map[string]any) *acpmeta.TurnUsage {
	var quota geminiQuota
	if !decodeMetadata(meta["quota"], &quota) {
		return nil
	}
	report := &acpmeta.TurnUsage{Source: "gemini.prompt.quota", Coverage: "observed_requests"}
	// Gemini defaults absent metadata to zero, and reports candidate output
	// without reasoning. Only positive counters prove measured consumption.
	if exactCounter(quota.Counts.Output) && *quota.Counts.Output > 0 {
		report.OutputTokens = quota.Counts.Output
	}
	if exactCounter(quota.Counts.Input) && *quota.Counts.Input > 0 {
		report.Observations = []acpmeta.UsageObservation{{Source: report.Source, Coverage: "provider_input_total",
			InputTotalTokens: quota.Counts.Input}}
	}
	if report.OutputTokens == nil && len(report.Observations) == 0 {
		return nil
	}
	if report.OutputTokens != nil && len(quota.Models) > 0 && len(quota.Models) <= 128 {
		models := make(map[string]acpmeta.ModelUsage, len(quota.Models))
		for _, row := range quota.Models {
			if _, duplicate := models[row.ID]; row.ID == "" || duplicate || !exactCounter(row.Counts.Output) {
				return report
			}
			if *row.Counts.Output > 0 {
				models[row.ID] = acpmeta.ModelUsage{OutputTokens: row.Counts.Output}
			}
		}
		if len(models) > 0 {
			report.Models = models
		}
	}
	return report
}

func decodeMetadata(value, destination any) bool {
	raw, err := json.Marshal(value)
	return err == nil && len(raw) <= 1<<20 && json.Unmarshal(raw, destination) == nil
}

func exactCounter(value *int64) bool {
	return value != nil && *value >= 0 && *value <= maxExactMetadataCounter
}

// Verified OpenCode releases report either the last assistant message or the current main loop, with separate cache and reasoning counters.
func openCodePromptUsage(version string, raw *acpclient.PromptUsageCounters, _ map[string]any) (*acpmeta.TurnUsage, acpclient.PromptUsageScope) {
	source, coverage := "opencode.assistant.usage", "observed_requests"
	switch version {
	case "1.18.34":
	case "2.0.29":
		// translate.recordStep sums this turn's root-session steps and excludes child sessions.
		source, coverage = "opencode.prompt.usage", "main_loop"
	default:
		return nil, acpclient.UsageUnattributed
	}
	if raw == nil || !exactCounter(raw.Input) || !exactCounter(raw.Output) || !exactCounter(raw.Total) {
		return nil, acpclient.UsageUnattributed
	}
	counts := []*int64{raw.CacheRead, raw.CacheWrite, raw.Thought}
	total := *raw.Input + *raw.Output
	for i, count := range counts {
		if count == nil {
			// The verified native implementation emits these optional counters only when positive.
			counts[i] = acp.Ptr(int64(0))
			continue
		}
		if !exactCounter(count) {
			return nil, acpclient.UsageUnattributed
		}
		total += *count
	}
	if total != *raw.Total {
		return nil, acpclient.UsageUnattributed
	}
	return &acpmeta.TurnUsage{Source: source, Coverage: coverage, InputTokens: raw.Input,
		OutputTokens: acp.Ptr(*raw.Output + *counts[2]), ReasoningTokens: counts[2],
		CacheReadTokens: counts[0], CacheWriteTokens: counts[1]}, acpclient.UsageUnavailable
}

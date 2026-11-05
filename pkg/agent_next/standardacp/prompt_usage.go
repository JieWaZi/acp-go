package standardacp

import (
	"encoding/json"

	"github.com/JieWaZi/acp-go/pkg/acpmeta"
	"github.com/JieWaZi/acp-go/pkg/agent_next/acpclient"
	acp "github.com/JieWaZi/acp-go/sdk"
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
	for _, count := range []*int64{usage.Input, usage.Output, usage.CacheRead, usage.CacheWrite, usage.Reasoning} {
		if !exactCounter(count) {
			return acpmeta.ModelUsage{}, false
		}
	}
	if *usage.CacheRead > *usage.Input || *usage.CacheWrite > *usage.Input-*usage.CacheRead || *usage.Reasoning > *usage.Output {
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

func geminiPromptUsage(meta map[string]any) *acpmeta.TurnUsage {
	var quota struct {
		// Counts is the sum from this prompt's request loop.
		Counts geminiCounts `json:"token_count"`
		// Models groups the same observations by actual returned model.
		Models []struct {
			// ID is the observed model rather than the selected routing alias.
			ID string `json:"model"`
			// Counts describes the observed request counters for the model.
			Counts geminiCounts `json:"token_count"`
		} `json:"model_usage"`
	}
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

// OpenCode 1.18.34 reports the final assistant message, with input and
// output excluding cache and thought tokens respectively.
func openCodePromptUsage(version string, raw *acpclient.PromptUsageCounters, _ map[string]any) (*acpmeta.TurnUsage, acpclient.PromptUsageScope) {
	if version != "1.18.34" || raw == nil || !exactCounter(raw.Input) || !exactCounter(raw.Output) || !exactCounter(raw.Total) {
		return nil, acpclient.UsageUnattributed
	}
	counts := []*int64{raw.CacheRead, raw.CacheWrite, raw.Thought}
	total := *raw.Input + *raw.Output
	for i, count := range counts {
		if count == nil {
			// The source deliberately omits exactly-zero optional counters.
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
	return &acpmeta.TurnUsage{Source: "opencode.assistant.usage", Coverage: "observed_requests", InputTokens: raw.Input,
		OutputTokens: acp.Ptr(*raw.Output + *counts[2]), ReasoningTokens: counts[2],
		CacheReadTokens: counts[0], CacheWriteTokens: counts[1]}, acpclient.UsageUnavailable
}

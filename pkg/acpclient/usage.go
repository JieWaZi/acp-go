package acpclient

import (
	"github.com/JieWaZi/acp-go/pkg/acpmeta"
	acp "github.com/JieWaZi/acp-go/sdk"
)

const maxPromptUsageObservations = 16

type promptUsage struct {
	// invocation is the latest explicitly attributable metadata snapshot.
	invocation *acpmeta.TurnUsage
	// observations holds the latest auxiliary snapshot for each source and scope.
	observations []acpmeta.UsageObservation
	// truncated records evidence lost to the independent observation limit.
	truncated bool
}

func (usage *promptUsage) recordMetadata(meta map[string]any) (decoded, attributable bool) {
	report, ok := acpmeta.ReadTurnUsage(meta)
	if !ok {
		return false, false
	}
	for _, observed := range report.Observations {
		usage.recordObservation(observed)
	}
	tokens := acpmeta.ModelUsage{
		InputTokens: report.InputTokens, OutputTokens: report.OutputTokens,
		CacheReadTokens: report.CacheReadTokens, CacheWriteTokens: report.CacheWriteTokens,
		ReasoningTokens: report.ReasoningTokens, Cost: report.Cost,
	}
	if tokens == (acpmeta.ModelUsage{}) && report.Models == nil {
		return true, false
	}
	switch report.Coverage {
	case "invocation", "native_turn", "observed_turn", "main_loop", "observed_requests", "query_pipeline":
		report.Observations = nil
		usage.invocation = &report
		return true, true
	}
	coverage := report.Coverage
	if coverage == "" {
		coverage = "unknown"
	}
	usage.recordObservation(acpmeta.UsageObservation{
		Source: report.Source, Coverage: coverage, Complete: report.Complete, Tokens: tokens, Models: report.Models,
	})
	return true, false
}

func (usage *promptUsage) recordObservation(observed acpmeta.UsageObservation) {
	for i, old := range usage.observations {
		if old.Source == observed.Source && old.Coverage == observed.Coverage {
			usage.observations[i] = observed
			return
		}
	}
	if len(usage.observations) == maxPromptUsageObservations {
		// Keep the bounded set available even when a terminal report adds a new source.
		copy(usage.observations, usage.observations[1:])
		usage.observations[len(usage.observations)-1] = observed
		usage.truncated = true
		return
	}
	usage.observations = append(usage.observations, observed)
}

func (usage *promptUsage) settle(counter *usageCounter, scope PromptUsageScope, response *promptResponse) *acpmeta.TurnUsage {
	if response != nil {
		standard := counter.consume(scope, response.Usage)
		_, explicitFinal := usage.recordMetadata(response.Meta)
		if standard != nil {
			if !explicitFinal && (usage.invocation == nil || scope == UsageInvocation) {
				usage.invocation = standard
			}
			for _, observed := range standard.Observations {
				usage.recordObservation(observed)
			}
		}
	}
	if usage.invocation == nil && len(usage.observations) == 0 {
		return nil
	}
	result := acpmeta.TurnUsage{Source: "acp/prompt", Coverage: "invocation"}
	if usage.invocation != nil {
		result = *usage.invocation
		result.Observations = nil
	}
	for _, observed := range usage.observations {
		if observed.Source != result.Source || observed.Coverage != result.Coverage {
			result.Observations = append(result.Observations, observed)
		}
	}
	return &result
}

// PromptUsageCounters preserves the presence of native ACP prompt counters.
type PromptUsageCounters struct {
	// Total retains the native sum without deriving its inclusion rules.
	Total *int64 `json:"totalTokens"`
	// Input is the input counter before interpreting provider-specific cache inclusion.
	Input *int64 `json:"inputTokens"`
	// Output is the reported output counter.
	Output *int64 `json:"outputTokens"`
	// CacheRead is the input served from cache.
	CacheRead *int64 `json:"cachedReadTokens"`
	// CacheWrite is the input written to cache.
	CacheWrite *int64 `json:"cachedWriteTokens"`
	// Thought is the separately reported reasoning count.
	Thought *int64 `json:"thoughtTokens"`
}

type usageCounter struct {
	// fresh proves a zero baseline only for the first response of a new session.
	fresh bool
	// highWater survives gaps and regressions of the same native counter.
	highWater PromptUsageCounters
	// previous is the immediate prior response; a missing or regressed component
	// cannot bound the usage of the next invocation.
	previous PromptUsageCounters
}

func (counter *usageCounter) consume(scope PromptUsageScope, value *PromptUsageCounters) *acpmeta.TurnUsage {
	if scope == UsageUnavailable {
		return nil
	}
	if scope == UsageUnattributed {
		if value == nil {
			return nil
		}
		observed := value.normalized()
		observed.InputTokens = nil // Cache inclusion is also unproven for this native payload.
		return &acpmeta.TurnUsage{Source: "acp/prompt", Coverage: "invocation", Observations: []acpmeta.UsageObservation{{
			Source: "acp/prompt", Coverage: "unknown", Tokens: observed,
			InputTotalTokens: nonnegative(value.Input), TotalTokens: nonnegative(value.Total),
		}}}
	}
	var sample PromptUsageCounters
	if scope == UsageSessionCumulative {
		sample = counter.advance(value)
	} else if value != nil {
		sample = *value
	}
	if value == nil {
		return nil
	}
	invocation := sample.normalized()
	result := &acpmeta.TurnUsage{
		InputTokens: invocation.InputTokens, CacheReadTokens: invocation.CacheReadTokens,
		CacheWriteTokens: invocation.CacheWriteTokens, OutputTokens: invocation.OutputTokens,
		ReasoningTokens: invocation.ReasoningTokens, Source: "acp/prompt", Coverage: "invocation",
		Complete: value.covers(invocation),
	}
	if scope == UsageSessionCumulative {
		observed := value.normalized()
		result.Observations = []acpmeta.UsageObservation{{
			Source: result.Source, Coverage: "session_cumulative", Complete: value.covers(observed), Tokens: observed,
		}}
	}
	return result
}

func (counter *usageCounter) advance(value *PromptUsageCounters) PromptUsageCounters {
	fresh, previous := counter.fresh, counter.previous
	counter.fresh = false
	if value == nil {
		counter.previous = PromptUsageCounters{}
		return PromptUsageCounters{}
	}
	counter.previous = *value
	delta := func(current, adjacent *int64, highWater **int64) *int64 {
		current = nonnegative(current)
		if current == nil {
			return nil
		}
		baseline := *highWater
		if baseline != nil && *current < *baseline {
			return nil
		}
		*highWater = current
		if fresh {
			return current
		}
		if baseline == nil || adjacent == nil || *adjacent != *baseline {
			return nil
		}
		return acp.Ptr(*current - *baseline)
	}
	high := &counter.highWater
	return PromptUsageCounters{
		Input:      delta(value.Input, previous.Input, &high.Input),
		Output:     delta(value.Output, previous.Output, &high.Output),
		CacheRead:  delta(value.CacheRead, previous.CacheRead, &high.CacheRead),
		CacheWrite: delta(value.CacheWrite, previous.CacheWrite, &high.CacheWrite),
		Thought:    delta(value.Thought, previous.Thought, &high.Thought),
	}
}

func (value PromptUsageCounters) normalized() acpmeta.ModelUsage {
	input, read, write := nonnegative(value.Input), nonnegative(value.CacheRead), nonnegative(value.CacheWrite)
	output, thought := nonnegative(value.Output), nonnegative(value.Thought)
	var uncached *int64
	if input != nil && read != nil && write != nil && *read <= *input && *write <= *input-*read {
		uncached = acp.Ptr(*input - *read - *write)
	}
	if output != nil && thought != nil && *thought > *output {
		thought = nil
	}
	return acpmeta.ModelUsage{InputTokens: uncached, CacheReadTokens: read, CacheWriteTokens: write, OutputTokens: output, ReasoningTokens: thought}
}

func (value PromptUsageCounters) covers(usage acpmeta.ModelUsage) bool {
	return (value.Input == nil || usage.InputTokens != nil) && (value.Output == nil || usage.OutputTokens != nil) &&
		(value.CacheRead == nil || usage.CacheReadTokens != nil) && (value.CacheWrite == nil || usage.CacheWriteTokens != nil) &&
		(value.Thought == nil || usage.ReasoningTokens != nil)
}

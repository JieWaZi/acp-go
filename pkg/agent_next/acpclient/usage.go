package acpclient

import (
	"github.com/JieWaZi/acp-go/pkg/acpmeta"
	acp "github.com/JieWaZi/acp-go/sdk"
)

// wireUsage retains independent presence for ACP's cumulative session counters.
type wireUsage struct {
	// Input includes both cache reads and cache writes.
	Input *int64 `json:"inputTokens"`
	// Output is the total output counter.
	Output *int64 `json:"outputTokens"`
	// CacheRead is the cumulative count served from cache.
	CacheRead *int64 `json:"cachedReadTokens"`
	// CacheWrite is the cumulative count written to cache.
	CacheWrite *int64 `json:"cachedWriteTokens"`
	// Thought is the separately reported reasoning subset.
	Thought *int64 `json:"thoughtTokens"`
}

type usageCounter struct {
	// fresh proves a zero baseline only for the first response of a new session.
	fresh bool
	// previous is the immediate prior response; gaps cannot be billed as one turn.
	previous wireUsage
}

func (counter *usageCounter) consume(scope PromptUsageScope, value *wireUsage) *acpmeta.TurnUsage {
	if scope == UsageUnavailable {
		return nil
	}
	fresh, previous := counter.fresh || scope == UsageInvocation, counter.previous
	counter.fresh = false
	if value == nil {
		counter.previous = wireUsage{}
		return nil
	}
	counter.previous = *value
	delta := func(current, baseline *int64) *int64 {
		current = nonnegative(current)
		if current == nil {
			return nil
		}
		if fresh {
			return current
		}
		baseline = nonnegative(baseline)
		if baseline == nil || *current < *baseline {
			return nil
		}
		return acp.Ptr(*current - *baseline)
	}
	invocation := wireUsage{
		Input: delta(value.Input, previous.Input), Output: delta(value.Output, previous.Output),
		CacheRead: delta(value.CacheRead, previous.CacheRead), CacheWrite: delta(value.CacheWrite, previous.CacheWrite),
		Thought: delta(value.Thought, previous.Thought),
	}.normalized()
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

func (value wireUsage) normalized() acpmeta.ModelUsage {
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

func (value wireUsage) covers(usage acpmeta.ModelUsage) bool {
	return (value.Input == nil || usage.InputTokens != nil) && (value.Output == nil || usage.OutputTokens != nil) &&
		(value.CacheRead == nil || usage.CacheReadTokens != nil) && (value.CacheWrite == nil || usage.CacheWriteTokens != nil) &&
		(value.Thought == nil || usage.ReasoningTokens != nil)
}

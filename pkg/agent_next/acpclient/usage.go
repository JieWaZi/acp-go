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
	// highWater survives gaps and regressions of the same native counter.
	highWater wireUsage
	// previous is the immediate prior response; a missing or regressed component
	// cannot bound the usage of the next invocation.
	previous wireUsage
}

func (counter *usageCounter) consume(scope PromptUsageScope, value *wireUsage) *acpmeta.TurnUsage {
	if scope == UsageUnavailable {
		return nil
	}
	var sample wireUsage
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

func (counter *usageCounter) advance(value *wireUsage) wireUsage {
	fresh, previous := counter.fresh, counter.previous
	counter.fresh = false
	if value == nil {
		counter.previous = wireUsage{}
		return wireUsage{}
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
	return wireUsage{
		Input:      delta(value.Input, previous.Input, &high.Input),
		Output:     delta(value.Output, previous.Output, &high.Output),
		CacheRead:  delta(value.CacheRead, previous.CacheRead, &high.CacheRead),
		CacheWrite: delta(value.CacheWrite, previous.CacheWrite, &high.CacheWrite),
		Thought:    delta(value.Thought, previous.Thought, &high.Thought),
	}
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

package claude

import (
	"encoding/json"
	"math"
	"math/big"
	"strings"

	"github.com/JieWaZi/acp-go/pkg/acpmeta"
	"github.com/JieWaZi/acp-go/pkg/agent_next/claude/protocol"
	"github.com/JieWaZi/acp-go/sdk"
)

const (
	mainLoopCoverage = "main_loop"
	queryCoverage    = "query_pipeline"
	maxUsageRows     = 256
)

type requestUsage struct {
	model    string
	counters acpmeta.ModelUsage
}

type queryCounters struct {
	// models is nil until a resumed pipeline reports its first baseline.
	models map[string]protocol.ModelUsage
	// cost is independently known and can be absent from a model report.
	cost *json.Number
}

func newQueryCounters(resume bool) queryCounters {
	if resume {
		return queryCounters{}
	}
	return queryCounters{models: make(map[string]protocol.ModelUsage), cost: acp.Ptr(json.Number("0"))}
}

func nonnegative(value int64) *int64 {
	if value < 0 {
		return nil
	}
	return acp.Ptr(value)
}

func reportedUsage(u protocol.Usage) acpmeta.TurnUsage {
	field := func(key string, value int64) *int64 {
		if !u.KnownFields[key] {
			return nil
		}
		return nonnegative(value)
	}
	return acpmeta.TurnUsage{
		InputTokens: field("input_tokens", u.InputTokens), OutputTokens: field("output_tokens", u.OutputTokens),
		CacheReadTokens:  field("cache_read_input_tokens", u.CacheReadInputTokens),
		CacheWriteTokens: field("cache_creation_input_tokens", u.CacheCreationInputTokens),
		Source:           "claude.result.usage", Coverage: mainLoopCoverage, Complete: true,
	}
}

func knownUsage(u acpmeta.TurnUsage) bool {
	return u.InputTokens != nil || u.OutputTokens != nil || u.CacheReadTokens != nil || u.CacheWriteTokens != nil
}

func positiveUsage(u acpmeta.TurnUsage) bool {
	for _, v := range []*int64{u.InputTokens, u.OutputTokens, u.CacheReadTokens, u.CacheWriteTokens} {
		if v != nil && *v > 0 {
			return true
		}
	}
	return false
}

func (inv *invocation) recordUsage(key, model string, usage acpmeta.TurnUsage) {
	if key == "" || !knownUsage(usage) {
		return
	}
	old, exists := inv.partialUsage[key]
	if !exists && len(inv.partialUsage) >= maxUsageRows {
		return
	}
	if model != "" {
		old.model = model
	}
	fields := []struct {
		target **int64
		value  *int64
	}{
		{&old.counters.InputTokens, usage.InputTokens}, {&old.counters.OutputTokens, usage.OutputTokens},
		{&old.counters.CacheReadTokens, usage.CacheReadTokens}, {&old.counters.CacheWriteTokens, usage.CacheWriteTokens},
	}
	for _, field := range fields {
		if field.value != nil && *field.value >= 0 {
			*field.target = field.value
		}
	}
	inv.partialUsage[key] = old
}

func partialSnapshot(inv *invocation) *acpmeta.TurnUsage {
	if len(inv.partialUsage) == 0 {
		return nil
	}
	var all []acpmeta.ModelUsage
	var byModel = make(map[string][]acpmeta.ModelUsage)
	for _, request := range inv.partialUsage {
		all = append(all, request.counters)
		if request.model != "" {
			byModel[request.model] = append(byModel[request.model], request.counters)
		}
	}
	sum := sumCounters(all)
	u := turnCounters(sum, "claude.assistant.usage", mainLoopCoverage, false)
	if len(byModel) == 1 && len(byModel[inv.model]) == len(all) {
		u.Model = inv.model
	}
	if u.Model != "" {
		u.Models = map[string]acpmeta.ModelUsage{u.Model: sum}
	}
	if !knownUsage(u) {
		return nil
	}
	return &u
}

func turnCounters(value acpmeta.ModelUsage, source, coverage string, complete bool) acpmeta.TurnUsage {
	return acpmeta.TurnUsage{
		InputTokens: value.InputTokens, OutputTokens: value.OutputTokens, CacheReadTokens: value.CacheReadTokens,
		CacheWriteTokens: value.CacheWriteTokens, ReasoningTokens: value.ReasoningTokens,
		Source: source, Coverage: coverage, Complete: complete,
	}
}

func sumCounters(rows []acpmeta.ModelUsage) acpmeta.ModelUsage {
	sum := func(get func(acpmeta.ModelUsage) *int64) *int64 {
		var total int64
		for _, row := range rows {
			n := get(row)
			if n == nil || *n < 0 || *n > math.MaxInt64-total {
				return nil
			}
			total += *n
		}
		return acp.Ptr(total)
	}
	return acpmeta.ModelUsage{
		InputTokens:      sum(func(r acpmeta.ModelUsage) *int64 { return r.InputTokens }),
		OutputTokens:     sum(func(r acpmeta.ModelUsage) *int64 { return r.OutputTokens }),
		CacheReadTokens:  sum(func(r acpmeta.ModelUsage) *int64 { return r.CacheReadTokens }),
		CacheWriteTokens: sum(func(r acpmeta.ModelUsage) *int64 { return r.CacheWriteTokens }),
		ReasoningTokens:  sum(func(r acpmeta.ModelUsage) *int64 { return r.ReasoningTokens }),
	}
}

func (q *queryCounters) final(inv *invocation, result *protocol.ResultMessage, reported acpmeta.TurnUsage) *acpmeta.TurnUsage {
	partial := partialSnapshot(inv)
	// An errored startup or interruption may report an empty/zeroed aggregate
	// despite earlier consumption. Such a frame cannot reset a proven baseline.
	zeroFailure := nativeFailed(result) && !positiveUsage(reported) && zeroPipeline(result.ModelUsage)
	if !zeroFailure {
		report := result.ModelUsage
		if len(report) > maxUsageRows {
			report = nil
		}
		models, complete := modelDeltas(q.models, report)
		q.models = report
		if complete {
			rows := make([]acpmeta.ModelUsage, 0, len(models))
			for _, row := range models {
				rows = append(rows, row)
			}
			summary := turnCounters(sumCounters(rows), "claude.result.modelUsage", queryCoverage, true)
			summary.Models = models
			if len(models) == 1 {
				for id := range models {
					summary.Model = id
				}
			}
			reported = summary
		}
	}
	if !zeroFailure {
		reported.Cost = costDelta(q.cost, result.TotalCostUSD)
		q.cost = result.TotalCostUSD
	}
	if partial != nil && (!knownUsage(reported) || zeroFailure && positiveUsage(*partial)) {
		partial.Cost = reported.Cost
		return partial
	}
	if !knownUsage(reported) && reported.Cost == nil {
		return nil
	}
	if reported.Coverage == mainLoopCoverage && inv.model != "" && len(inv.partialUsage) > 0 {
		single := true
		for _, request := range inv.partialUsage {
			if request.model != inv.model {
				single = false
				break
			}
		}
		if single {
			reported.Model = inv.model
		}
	}
	reported.ContextKnown = inv.outcome.ContextUsage.UsedTokens != nil && inv.outcome.ContextUsage.WindowTokens != nil
	return &reported
}

func zeroPipeline(rows map[string]protocol.ModelUsage) bool {
	for _, row := range rows {
		if row.InputTokens > 0 || row.OutputTokens > 0 || row.CacheReadInputTokens > 0 || row.CacheCreationInputTokens > 0 {
			return false
		}
	}
	return true
}

func modelDeltas(previous, current map[string]protocol.ModelUsage) (map[string]acpmeta.ModelUsage, bool) {
	if previous == nil || current == nil {
		return nil, false
	}
	for model := range previous {
		if _, exists := current[model]; !exists {
			return nil, false
		}
	}
	rows := make(map[string]acpmeta.ModelUsage, len(current))
	for model, to := range current {
		from, had := previous[model]
		if !to.CompleteCounters || had && !from.CompleteCounters || model == "" {
			return nil, false
		}
		row := acpmeta.ModelUsage{
			InputTokens: deltaCounter(from.InputTokens, to.InputTokens), OutputTokens: deltaCounter(from.OutputTokens, to.OutputTokens),
			CacheReadTokens:  deltaCounter(from.CacheReadInputTokens, to.CacheReadInputTokens),
			CacheWriteTokens: deltaCounter(from.CacheCreationInputTokens, to.CacheCreationInputTokens),
		}
		if row.InputTokens == nil || row.OutputTokens == nil || row.CacheReadTokens == nil || row.CacheWriteTokens == nil {
			return nil, false
		}
		if to.ThinkingTokens != nil && (!had || from.ThinkingTokens != nil) {
			var baseline int64
			if had {
				baseline = *from.ThinkingTokens
			}
			row.ReasoningTokens = deltaCounter(baseline, *to.ThinkingTokens)
			if row.ReasoningTokens != nil && *row.ReasoningTokens > *row.OutputTokens {
				row.ReasoningTokens = nil
			}
		}
		if !had {
			from.CostUSD = acp.Ptr(json.Number("0"))
		}
		row.Cost = costDelta(from.CostUSD, to.CostUSD)
		if row.Cost != nil && had && from.CostBasis != to.CostBasis {
			row.Cost = nil
		}
		rows[model] = row
	}
	return rows, true
}

func deltaCounter(from, to int64) *int64 {
	if from < 0 || to < from {
		return nil
	}
	return acp.Ptr(to - from)
}

func costDelta(previous, current *json.Number) *acpmeta.UsageCost {
	if previous == nil || current == nil || len(*previous) > 32 || len(*current) > 32 {
		return nil
	}
	// Bound exponent magnitude before parsing decimal estimates.
	parse := func(n json.Number) *big.Rat {
		f, err := n.Float64()
		if err != nil || f < 0 || math.IsInf(f, 0) || math.IsNaN(f) || f == 0 && strings.ContainsAny(string(n), "eE") {
			return nil
		}
		r, ok := new(big.Rat).SetString(string(n))
		if !ok {
			return nil
		}
		return r
	}
	from, to := parse(*previous), parse(*current)
	if from == nil || to == nil || to.Cmp(from) < 0 {
		return nil
	}
	diff := to.Sub(to, from)
	return &acpmeta.UsageCost{Amount: json.Number(diff.FloatString(18)), Currency: "USD", Source: "claude.result.cost", Scope: queryCoverage, Aggregation: "turn_delta", Valuation: "estimated"}
}

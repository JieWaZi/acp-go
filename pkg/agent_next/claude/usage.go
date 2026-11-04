package claude

import (
	"cmp"
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

// cumulativeCounter keeps the epoch floor independently of the evidence that
// the next report can be differenced against it for an individual invocation.
type cumulativeCounter[T any] struct {
	high     T
	known    bool
	adjacent bool
}

// advance never lowers the floor. After a gap only a sample at or above the
// floor establishes an adjacent boundary, and that sample has no turn delta.
func (c *cumulativeCounter[T]) advance(sample T, compare func(T, T) int) (T, bool) {
	before := c.high
	if c.known && compare(sample, before) < 0 {
		return before, false
	}
	attributable := c.known && c.adjacent
	c.high, c.known, c.adjacent = sample, true, true
	return before, attributable
}

type modelCounters struct {
	input, output, read, write, thinking cumulativeCounter[int64]
	cost                                 cumulativeCounter[*big.Rat]
	costBasis                            *string
}

func newModelCounters(fromZero bool) *modelCounters {
	zero := cumulativeCounter[int64]{known: fromZero, adjacent: fromZero}
	m := &modelCounters{input: zero, output: zero, read: zero, write: zero, thinking: zero}
	if fromZero {
		m.cost = cumulativeCounter[*big.Rat]{high: new(big.Rat), known: true, adjacent: true}
	}
	return m
}

func (m *modelCounters) breakContinuity() {
	m.input.adjacent, m.output.adjacent = false, false
	m.read.adjacent, m.write.adjacent = false, false
	m.thinking.adjacent, m.cost.adjacent = false, false
}

type queryCounters struct {
	// Model floors remain present even when a report omits them. A new model
	// may start at zero only when the preceding model report was observed.
	models         map[string]*modelCounters
	modelsAdjacent bool
	cost           cumulativeCounter[*big.Rat]
	// Reset identities span all epochs of this native stream.
	resetEvents        map[string]struct{}
	resetConversations map[string]struct{}
}

func newQueryCounters(resume bool) queryCounters {
	var q queryCounters
	if !resume {
		q.startEpoch()
	}
	return q
}

func (q *queryCounters) startEpoch() {
	q.models = nil
	q.modelsAdjacent = true
	q.cost = cumulativeCounter[*big.Rat]{high: new(big.Rat), known: true, adjacent: true}
}

func (q *queryCounters) breakModelContinuity() {
	q.modelsAdjacent = false
	for _, model := range q.models {
		model.breakContinuity()
	}
}

func (q *queryCounters) breakContinuity() {
	q.breakModelContinuity()
	q.cost.adjacent = false
}

// reset applies each validated native boundary once for this stream.
// It reports exhaustion so the session can fail closed without forgetting IDs.
func (q *queryCounters) reset(eventID, conversationID string) bool {
	eventID, conversationID = strings.ToLower(eventID), strings.ToLower(conversationID)
	_, seenEvent := q.resetEvents[eventID]
	_, seenConversation := q.resetConversations[conversationID]
	if seenEvent || seenConversation {
		return true
	}
	if len(q.resetEvents) >= maxRememberedIDs {
		return false
	}
	if q.resetEvents == nil {
		q.resetEvents = make(map[string]struct{})
		q.resetConversations = make(map[string]struct{})
	}
	q.resetEvents[eventID] = struct{}{}
	q.resetConversations[conversationID] = struct{}{}
	q.startEpoch()
	return true
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
	if len(byModel) > 0 {
		u.Models = make(map[string]acpmeta.ModelUsage, len(byModel))
		for model, requests := range byModel {
			u.Models[model] = sumCounters(requests)
		}
	}
	if !knownUsage(u) && len(u.Models) == 0 {
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
	unreliableTerminal := nativeFailed(result) || result.TerminalReason == "aborted_streaming" || result.TerminalReason == "aborted_tools"
	modelReport := result.ModelUsage
	if len(modelReport) > maxUsageRows || unreliableTerminal && zeroPipeline(modelReport) {
		modelReport = nil
	}
	costReport := result.TotalCostUSD
	// The query estimate cannot be separated into price bases. If any model
	// reports an organization rate or a fallback guess, keep only the token
	// counts and independently supported model estimates.
	for _, row := range result.ModelUsage {
		if !listCostBasis(row.CostBasis) {
			costReport = nil
			break
		}
	}
	if costReport != nil && unreliableTerminal {
		if amount, err := costReport.Float64(); err != nil || amount == 0 {
			costReport = nil
		}
	}
	// A missing or zeroized report breaks adjacency for that counter alone.
	// The next usable report seeds a new boundary without claiming the gap.
	models := q.modelDeltas(modelReport)
	cost := observeCost(&q.cost, costReport)

	var observations []acpmeta.UsageObservation
	if models != nil {
		rows := make([]acpmeta.ModelUsage, 0, len(models))
		for _, row := range models {
			rows = append(rows, row)
		}
		totals := sumCounters(rows)
		complete := totals.InputTokens != nil && totals.OutputTokens != nil &&
			totals.CacheReadTokens != nil && totals.CacheWriteTokens != nil
		summary := turnCounters(totals, "claude.result.modelUsage", queryCoverage, complete)
		summary.Models = models
		if len(models) == 1 {
			for id := range models {
				summary.Model = id
			}
		}
		// Select one scope for the primary totals. A partial pipeline
		// cannot replace an independently verified main-loop component.
		hasEvidence := knownUsage(summary) || totals.ReasoningTokens != nil
		for _, row := range models {
			if row.InputTokens != nil || row.OutputTokens != nil || row.CacheReadTokens != nil ||
				row.CacheWriteTokens != nil || row.ReasoningTokens != nil || row.Cost != nil {
				hasEvidence = true
				break
			}
		}
		if complete || !knownUsage(reported) && partial == nil && hasEvidence {
			reported = summary
		} else if hasEvidence {
			observations = append(observations, acpmeta.UsageObservation{
				Source: summary.Source, Coverage: summary.Coverage, Complete: summary.Complete, Tokens: totals, Models: models,
			})
		}
	}
	if reported.Coverage == mainLoopCoverage {
		zeroized := unreliableTerminal && !positiveUsage(reported) && modelReport == nil
		if !knownUsage(reported) || zeroized {
			if partial != nil {
				reported = *partial
			} else {
				reported = turnCounters(acpmeta.ModelUsage{}, "claude.result.usage", mainLoopCoverage, false)
			}
		} else if partial != nil {
			reported.Model = partial.Model
			reported.Models = partial.Models
		}
	}
	reported.Observations = observations
	reported.Cost = cost
	reported.ContextKnown = inv.outcome.ContextUsage.UsedTokens != nil && inv.outcome.ContextUsage.WindowTokens != nil
	if !knownUsage(reported) && len(reported.Models) == 0 && reported.Cost == nil {
		return nil
	}
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

func (q *queryCounters) modelDeltas(report map[string]protocol.ModelUsage) map[string]acpmeta.ModelUsage {
	if report == nil {
		q.breakModelContinuity()
		return nil
	}
	count := len(q.models)
	for id := range report {
		if q.models[id] == nil {
			count++
		}
		if id == "" || count > maxUsageRows {
			// Preserve the epoch floors even if this frame cannot be
			// reconciled within the bounded model registry.
			q.breakModelContinuity()
			return nil
		}
	}
	rows := make(map[string]acpmeta.ModelUsage, count)
	for id, model := range q.models {
		if _, exists := report[id]; !exists {
			model.breakContinuity()
			rows[id] = acpmeta.ModelUsage{}
		}
	}
	if q.models == nil && len(report) > 0 {
		q.models = make(map[string]*modelCounters, len(report))
	}
	for id, sample := range report {
		model := q.models[id]
		if model == nil {
			model = newModelCounters(q.modelsAdjacent)
			q.models[id] = model
		}
		rows[id] = model.observe(sample)
	}
	wasAdjacent := q.modelsAdjacent
	q.modelsAdjacent = true
	if len(rows) == 0 && !wasAdjacent {
		return nil
	}
	return rows
}

func (m *modelCounters) observe(sample protocol.ModelUsage) acpmeta.ModelUsage {
	row := acpmeta.ModelUsage{
		InputTokens:      observeTokens(&m.input, sample.InputTokens, sample.KnownCounters["input"]),
		OutputTokens:     observeTokens(&m.output, sample.OutputTokens, sample.KnownCounters["output"]),
		CacheReadTokens:  observeTokens(&m.read, sample.CacheReadInputTokens, sample.KnownCounters["read"]),
		CacheWriteTokens: observeTokens(&m.write, sample.CacheCreationInputTokens, sample.KnownCounters["write"]),
	}
	if sample.ThinkingTokens != nil {
		reasoning := observeTokens(&m.thinking, *sample.ThinkingTokens, true)
		if reasoning != nil && row.OutputTokens != nil && *reasoning <= *row.OutputTokens {
			row.ReasoningTokens = reasoning
		}
	} else {
		m.thinking.adjacent = false
	}
	if m.costBasis == nil {
		m.costBasis = acp.Ptr(sample.CostBasis)
	} else if *m.costBasis != sample.CostBasis {
		m.cost.adjacent = false
		*m.costBasis = sample.CostBasis
	}
	cost := sample.CostUSD
	if !listCostBasis(sample.CostBasis) {
		cost = nil
	}
	row.Cost = observeCost(&m.cost, cost)
	return row
}

// listCostBasis accepts the CLI list estimate, including versions that
// predate costBasis. Managed rates and guesses have no comparable list cost.
func listCostBasis(basis string) bool {
	return basis == "" || basis == "list"
}

func observeTokens(counter *cumulativeCounter[int64], sample int64, present bool) *int64 {
	if !present || sample < 0 {
		counter.adjacent = false
		return nil
	}
	before, attributable := counter.advance(sample, cmp.Compare[int64])
	if !attributable {
		return nil
	}
	return acp.Ptr(sample - before)
}

func parseCumulativeCost(n *json.Number) *big.Rat {
	// Bound exponent magnitude before parsing decimal estimates.
	if n == nil || len(*n) > 32 {
		return nil
	}
	f, err := n.Float64()
	if err != nil || f < 0 || math.IsInf(f, 0) || math.IsNaN(f) || f == 0 && strings.ContainsAny(string(*n), "eE") {
		return nil
	}
	r, ok := new(big.Rat).SetString(string(*n))
	if !ok {
		return nil
	}
	return r
}

func observeCost(counter *cumulativeCounter[*big.Rat], raw *json.Number) *acpmeta.UsageCost {
	sample := parseCumulativeCost(raw)
	if sample == nil {
		counter.adjacent = false
		return nil
	}
	before, attributable := counter.advance(sample, (*big.Rat).Cmp)
	if !attributable {
		return nil
	}
	diff := new(big.Rat).Sub(sample, before)
	return &acpmeta.UsageCost{Amount: json.Number(diff.FloatString(18)), Currency: "USD", Source: "claude.result.cost", Scope: queryCoverage, Aggregation: "turn_delta", Valuation: "estimated"}
}

package codex

import (
	"math"
	"testing"

	"github.com/JieWaZi/acp-go/pkg/acpmeta"
)

func TestOptionalCacheWriteDeltaUsesNativeDefaultAndPreservesPresence(t *testing.T) {
	zero, two := pointer(int64(0)), pointer(int64(2))
	for _, tc := range []struct {
		// name describes the cumulative cache-write transition.
		name string
		// start and end preserve the two native field presences.
		start, end *int64
		// want preserves the expected closing field presence.
		want *int64
		// regressed marks a decrease in the actual native count.
		regressed bool
	}{
		{"omitted_to_positive", nil, two, two, false},
		{"explicit_zero_to_positive", zero, two, two, false},
		{"explicit_zero_to_omitted", zero, nil, nil, false},
		{"omitted_to_explicit_zero", nil, zero, zero, false},
		{"positive_to_omitted", two, nil, nil, true},
		{"positive_to_explicit_zero", two, zero, nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			start := tokenCounters{Input: pointer(int64(10)), Read: zero, Write: tc.start, Output: zero, Reasoning: zero, Context: pointer(int64(10))}
			end := tokenCounters{Input: pointer(int64(13)), Read: zero, Write: tc.end, Output: zero, Reasoning: zero, Context: pointer(int64(13))}
			delta, comparable := subtractCounters(&end, &start)
			if countersRegressed(&end, &start) != tc.regressed || comparable == tc.regressed ||
				(delta.Write == nil) != (tc.want == nil) || value(delta.Write) != value(tc.want) {
				t.Fatalf("cache-write transition: comparable=%t, regressed=%t, delta=%v (present=%t)",
					comparable, countersRegressed(&end, &start), value(delta.Write), delta.Write != nil)
			}
		})
	}
}

func TestLastRequestProofRequiresRealMatchAndRequiredCounters(t *testing.T) {
	zero := pointer(int64(0))
	delta := tokenCounters{Input: pointer(int64(3)), Read: zero, Output: zero, Reasoning: zero, Context: pointer(int64(3))}
	last := delta
	last.Write = zero
	if !matchesLastRequest(delta, &last) {
		t.Fatal("explicit last zero must corroborate an omitted cache-write delta")
	}
	last.Write = pointer(int64(1))
	if matchesLastRequest(delta, &last) {
		t.Fatal("different cache writes corroborated the delta")
	}
	last = delta
	last.Output = nil
	if matchesLastRequest(delta, &last) {
		t.Fatal("a missing required last counter corroborated the delta")
	}
	delta.Output = nil
	if matchesLastRequest(delta, &last) {
		t.Fatal("two absent required counters corroborated each other")
	}
	if _, comparable := subtractCounters(&last, &delta); comparable {
		t.Fatal("two absent required cumulative counters were comparable")
	}
}

func TestUsageInputBreakdownRejectsInvalidCacheCounts(t *testing.T) {
	client, owner := Client{}, turn{}
	maxCount, zero, negative, one := pointer(int64(math.MaxInt64)), pointer(int64(0)), pointer(int64(-1)), pointer(int64(1))
	for _, counts := range []tokenCounters{
		{Input: maxCount, Read: zero, Write: negative, Output: one},
		{Input: maxCount, Read: maxCount, Write: one, Output: one},
		{Input: maxCount, Read: negative, Output: one},
	} {
		for _, observed := range []acpmeta.ModelUsage{intervalConsumption(counts), *addConsumption(nil, intervalConsumption(counts))} {
			client.publishUsageLocked(&owner, observed, "last_request")
			usage := owner.outcome.Turn.Usage
			if usage.InputTokens != nil || usage.OutputTokens == nil || *usage.OutputTokens != 1 {
				t.Fatalf("invalid cache breakdown must leave uncached input unknown and preserve output: %+v", usage)
			}
		}
	}
}
func TestUsageOnlyDecomposesCacheEvidenceWithinSameInterval(t *testing.T) {
	client := &Client{}
	owner := &turn{session: &session{}}
	counts := func(input int64, read *int64) *tokenCounters {
		return &tokenCounters{
			Input: pointer(input), Read: read, Output: pointer(int64(0)),
			Reasoning: pointer(int64(0)), Context: pointer(input),
		}
	}
	// First sample identifies this request's inclusive input, but says nothing
	// about cache reads. The next sample provides a cumulative cache count.
	// Its cache-read delta remains unknown until both endpoints have a count.
	client.captureUsageLocked(owner, tokenSnapshot{Total: counts(100, nil), Last: counts(100, nil)})
	client.captureUsageLocked(owner, tokenSnapshot{Total: counts(200, pointer(int64(60))), Last: counts(100, pointer(int64(20)))})
	// The final interval has both endpoints. Only its 75 uncached tokens
	// and 25 cache reads can enter the known disjoint subtotals.
	client.captureUsageLocked(owner, tokenSnapshot{Total: counts(300, pointer(int64(85))), Last: counts(100, pointer(int64(25)))})
	got := owner.outcome.Turn.Usage
	if got == nil || value(got.InputTokens) != 75 || value(got.CacheReadTokens) != 25 {
		if got == nil {
			t.Fatal("expected known request counters")
		}
		t.Fatalf("uncached known=%d, cached known=%d, coverage=%s, complete=%t; want uncached known=75, cached known=25", value(got.InputTokens), value(got.CacheReadTokens), got.Coverage, got.Complete)
	}
}

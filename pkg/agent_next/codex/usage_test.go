package codex

import (
	"math"
	"testing"
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
		for _, observed := range []*tokenCounters{&counts, addCounters(nil, counts)} {
			client.publishUsageLocked(&owner, observed, "last_request")
			usage := owner.outcome.Turn.Usage
			if usage.InputTokens != nil || usage.OutputTokens == nil || *usage.OutputTokens != 1 {
				t.Fatalf("invalid cache breakdown must leave uncached input unknown and preserve output: %+v", usage)
			}
		}
	}
}

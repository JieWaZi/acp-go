package acpmeta

import (
	"encoding/json"
	"reflect"
	"testing"
)

// TestNativeTurnMetadataRoundTripPreservesIndependentCost checks ACP serialization and snapshot independence.
func TestNativeTurnMetadataRoundTripPreservesIndependentCost(t *testing.T) {
	input := int64(21)
	event := "result-event"
	turn := NativeTurn{
		InvocationID: "local-run", TerminalEventID: &event, Origin: "provider", ConsumedInputIDs: []string{"one", "two"},
		Status: "failed", Usage: &TurnUsage{InputTokens: &input, Source: "synthetic", Coverage: "main_loop", Cost: &UsageCost{
			Amount: json.Number("0.1234567890123456789"), Currency: "USD", Source: "provider", Scope: "query_pipeline",
			Aggregation: "turn_delta", Valuation: "estimated",
		}, Models: map[string]ModelUsage{"one-model": {InputTokens: &input, Cost: &UsageCost{
			Amount: "0.0000000000000000001", Currency: "USD", Source: "model", Scope: "query_pipeline",
			Aggregation: "turn_delta", Valuation: "estimated",
		}}}},
	}
	meta := WithNativeTurn(nil, turn)
	original := turn
	original.ConsumedInputIDs = []string{"one", "two"}
	original.TerminalEventID = new(string)
	*original.TerminalEventID = event
	*turn.TerminalEventID = "changed"
	turn.ConsumedInputIDs[0] = "changed"
	encoded, err := json.Marshal(meta)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	actual, ok := ReadNativeTurn(decoded)
	if !ok || !reflect.DeepEqual(original, actual) {
		t.Fatalf("actual = %#v, known = %v, wire = %s", actual, ok, encoded)
	}
}

// TestNativeBackgroundUnknownAndKnownEmpty distinguishes an unknown native snapshot from a known empty level.
func TestNativeBackgroundUnknownAndKnownEmpty(t *testing.T) {
	for _, value := range []*int64{nil, new(int64)} {
		result, ok := ReadNativeBackground(WithNativeBackground(nil, NativeBackground{ActiveCount: value}))
		if !ok || !reflect.DeepEqual(result.ActiveCount, value) {
			t.Fatalf("background count = %#v, observed = %v", result.ActiveCount, ok)
		}
	}
}

// TestTurnUsageCostKeepsDecimalAndRejectsInvalidAmounts checks decimal precision and rejects invalid observations.
func TestTurnUsageCostKeepsDecimalAndRejectsInvalidAmounts(t *testing.T) {
	const amount = "0.000001234567890123456789"
	usage := TurnUsage{Source: "native", Cost: &UsageCost{
		Amount: amount, Currency: "USD", Source: "native", Scope: "query_pipeline", Aggregation: "turn_delta", Valuation: "estimated",
	}}
	meta := WithTurnUsage(nil, usage)
	usage.Cost.Amount = "5"
	encoded, err := json.Marshal(meta)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	actual, ok := ReadTurnUsage(decoded)
	if !ok || actual.Cost == nil || actual.Cost.Amount != amount {
		t.Fatalf("cost = %#v, wire = %s", actual.Cost, encoded)
	}
	for _, invalid := range []string{"-1", "NaN", "01", "1e1000"} {
		actual.Cost.Amount = json.Number(invalid)
		bad := WithTurnUsage(nil, actual)
		got, _ := ReadTurnUsage(bad)
		if got.Cost != nil {
			t.Fatalf("invalid amount %q survived", invalid)
		}
	}
}

// TestTurnUsageDistinguishesUnknownAndEmptyModelAttribution checks metadata field presence.
func TestTurnUsageDistinguishesUnknownAndEmptyModelAttribution(t *testing.T) {
	for _, models := range []map[string]ModelUsage{nil, {}} {
		got, ok := ReadTurnUsage(WithTurnUsage(nil, TurnUsage{Source: "synthetic", Models: models}))
		if !ok || (got.Models == nil) != (models == nil) {
			t.Fatalf("models = %#v, known = %v, want %#v", got.Models, ok, models)
		}
	}
}

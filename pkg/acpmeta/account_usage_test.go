package acpmeta

import (
	"encoding/json"
	"math"
	"testing"
)

// TestAccountUsagePreservesFractionAndPresence protects shared quota observations across providers.
func TestAccountUsagePreservesFractionAndPresence(t *testing.T) {
	var usage AccountUsage
	if err := json.Unmarshal([]byte(`{"buckets":[{"id":"a","windows":[{"kind":"primary","used_percent":12.75}]}]}`), &usage); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(usage)
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]any
	if err = json.Unmarshal(raw, &wire); err != nil {
		t.Fatal(err)
	}
	window := wire["buckets"].([]any)[0].(map[string]any)["windows"].([]any)[0].(map[string]any)
	if window["used_percent"] != 12.75 {
		t.Fatalf("fraction lost: %s", raw)
	}
	for _, input := range []string{`{"buckets":null}`, `{"buckets":[]}`} {
		var value AccountUsage
		if err = json.Unmarshal([]byte(input), &value); err != nil {
			t.Fatal(err)
		}
		raw, err = json.Marshal(value)
		if err != nil || string(raw) != input {
			t.Fatalf("presence %s => %s, %v", input, raw, err)
		}
	}
	var invalid AccountUsage
	if err = json.Unmarshal([]byte(`{"buckets":[{"id":"x","windows":[{"kind":"primary","used_percent":0}]}]}`), &invalid); err != nil {
		t.Fatal(err)
	}
	// A dynamically observed non-finite value is not valid JSON and must not escape the contract.
	value := math.NaN()
	invalid.Buckets[0].Windows[0].UsedPercent = &value
	if _, err = json.Marshal(invalid); err == nil {
		t.Fatal("NaN accepted")
	}
	value = math.Inf(1)
	if _, err = json.Marshal(invalid); err == nil {
		t.Fatal("infinity accepted")
	}
}

// TestAccountUsagePreservesExactExtraAllowanceNumbers protects native allowance precision.
func TestAccountUsagePreservesExactExtraAllowanceNumbers(t *testing.T) {
	raw := []byte(`{"available":false,"buckets":[{"name":"Model quota","windows":null,"extra":{"enabled":false,"used":0,"limit":9007199254740993}}]}`)
	var usage AccountUsage
	if err := json.Unmarshal(raw, &usage); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(usage)
	if err != nil || string(encoded) != string(raw) {
		t.Fatalf("quota observation changed: %s / %v", encoded, err)
	}
}

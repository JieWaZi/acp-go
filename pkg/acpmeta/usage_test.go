package acpmeta

import (
	"encoding/json"
	"testing"
)

// TestTurnUsageRoundTripPreservesUnknownAndZero 验证快照不混淆缺失与显式零。
func TestTurnUsageRoundTripPreservesUnknownAndZero(t *testing.T) {
	zero := int64(0)
	huge := int64(30)
	original := map[string]any{"other": "kept"}
	meta := WithTurnUsage(original, TurnUsage{InputTokens: &zero, OutputTokens: &huge, Source: "fixture", Coverage: "native_turn", Complete: false})
	if _, exists := original["acp-go/turn-usage"]; exists {
		t.Fatal("mutated caller metadata")
	}
	for _, raw := range []bool{false, true} {
		value := meta
		if raw {
			data, err := json.Marshal(meta)
			if err != nil {
				t.Fatal(err)
			}
			if err = json.Unmarshal(data, &value); err != nil {
				t.Fatal(err)
			}
		}
		got, ok := ReadTurnUsage(value)
		if !ok || got.InputTokens == nil || *got.InputTokens != 0 || got.CacheReadTokens != nil || got.OutputTokens == nil || *got.OutputTokens != huge || got.Complete || got.ContextKnown || got.Coverage != "native_turn" || value["other"] != "kept" {
			t.Fatalf("roundtrip=%t: %+v / %v", raw, got, ok)
		}
	}
}

// TestTurnUsageRejectsInvalidCounters 拒绝不可解释为消费的异常计数。
func TestTurnUsageRejectsInvalidCounters(t *testing.T) {
	for _, value := range []any{-1.0, 1.5, "wrong"} {
		meta := map[string]any{"acp-go/turn-usage": map[string]any{"source": "fixture", "complete": true, "contextKnown": false, "inputTokens": value}}
		if _, ok := ReadTurnUsage(meta); ok {
			t.Fatalf("accepted invalid counter %v", value)
		}
	}
}

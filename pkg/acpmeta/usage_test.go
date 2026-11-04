package acpmeta

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestTurnUsageRetainsOverlappingObservationWithoutReplacingAccounting(t *testing.T) {
	input, output, zero := int64(7), int64(4), int64(0)
	usage := TurnUsage{InputTokens: &input, OutputTokens: &output, Source: "result.usage", Coverage: "main_loop", Complete: true,
		Observations: []UsageObservation{{Source: "model_usage_delta", Coverage: "query_pipeline", Tokens: ModelUsage{InputTokens: &input}, Models: map[string]ModelUsage{"model": {InputTokens: &input, CacheReadTokens: &zero}}}},
	}
	for _, wire := range []bool{false, true} {
		meta := WithTurnUsage(nil, usage)
		if wire {
			data, err := json.Marshal(meta)
			if err != nil {
				t.Fatal(err)
			}
			decoder := json.NewDecoder(strings.NewReader(string(data)))
			decoder.UseNumber()
			if err := decoder.Decode(&meta); err != nil {
				t.Fatal(err)
			}
		}
		got, ok := ReadTurnUsage(meta)
		if !ok || got.Coverage != "main_loop" || got.InputTokens == nil || *got.InputTokens != 7 || got.OutputTokens == nil || *got.OutputTokens != 4 || len(got.Observations) != 1 {
			t.Fatalf("wire=%t: overlapping evidence replaced or disappeared from canonical usage: %+v", wire, got)
		}
		observation := got.Observations[0]
		model := observation.Models["model"]
		if observation.Coverage != "query_pipeline" || observation.Complete || observation.Tokens.OutputTokens != nil || model.CacheReadTokens == nil || *model.CacheReadTokens != 0 || model.CacheWriteTokens != nil {
			t.Fatalf("wire=%t: partial observation became complete or unknown became zero: %+v", wire, observation)
		}
	}
}

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

// TestTurnUsageJSONIntegerPrecision 拒绝默认 JSON 解码已舍入的整数，精确通道保留 int64 范围。
func TestTurnUsageJSONIntegerPrecision(t *testing.T) {
	for _, test := range []struct {
		// name 标识解码通道及边界。
		name string
		// literal 是原始 JSON 中的十进制整数。
		literal string
		// useNumber 表示保持原始十进制精度。
		useNumber bool
		// want 是合法输入的精确计数。
		want int64
		// valid 表示该通道可以证明计数精确且合法。
		valid bool
	}{
		{name: "default_safe", literal: "9007199254740991", want: 9007199254740991, valid: true},
		{name: "default_boundary", literal: "9007199254740992"},
		{name: "default_rounded", literal: "9007199254740993"},
		{name: "number_boundary", literal: "9007199254740992", useNumber: true, want: 9007199254740992, valid: true},
		{name: "number_exact", literal: "9007199254740993", useNumber: true, want: 9007199254740993, valid: true},
		{name: "number_max", literal: "9223372036854775807", useNumber: true, want: 9223372036854775807, valid: true},
		{name: "number_overflow", literal: "9223372036854775808", useNumber: true},
		{name: "number_negative", literal: "-1", useNumber: true},
		{name: "number_fraction", literal: "1.5", useNumber: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			wire := `{"acp-go/turn-usage":{"source":"fixture","complete":false,"contextKnown":false,"inputTokens":` + test.literal + `}}`
			decoder := json.NewDecoder(strings.NewReader(wire))
			if test.useNumber {
				decoder.UseNumber()
			}
			var meta map[string]any
			if err := decoder.Decode(&meta); err != nil {
				t.Fatal(err)
			}
			if test.name == "default_rounded" && meta[turnUsageKey].(map[string]any)["inputTokens"] != float64(9007199254740992) {
				t.Fatal("fixture did not reproduce default JSON rounding")
			}
			got, ok := ReadTurnUsage(meta)
			if ok != test.valid || ok && (got.InputTokens == nil || *got.InputTokens != test.want) {
				t.Fatalf("literal=%s useNumber=%t: accepted=%t counter=%v; want accepted=%t count=%d", test.literal, test.useNumber, ok, got.InputTokens, test.valid, test.want)
			}
		})
	}
	for _, value := range []int64{9007199254740993, 9223372036854775807} {
		got, ok := ReadTurnUsage(WithTurnUsage(nil, TurnUsage{Source: "fixture", InputTokens: &value}))
		if !ok || got.InputTokens == nil || *got.InputTokens != value {
			t.Fatalf("exact int64 rejected: %d, %+v, %t", value, got, ok)
		}
	}
}

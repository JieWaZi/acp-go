package standardacp

import (
	"encoding/json"
	"testing"

	acp "github.com/JieWaZi/acp-go-sdk"
	"github.com/JieWaZi/acp-go/pkg/acpclient"
)

func TestGrokPromptUsageAllowsUnknownReasoning(t *testing.T) {
	var meta map[string]any
	if err := json.Unmarshal([]byte(`{"usage":{"inputTokens":100,"outputTokens":30,"cachedReadTokens":20,"cacheCreationTokens":5,"modelUsage":{"native-model":{"inputTokens":100,"outputTokens":30,"cachedReadTokens":20,"cacheCreationTokens":5}}}}`), &meta); err != nil {
		t.Fatal(err)
	}
	report := grokPromptUsage(meta)
	if report == nil || !report.Complete || report.Coverage != "invocation" {
		t.Fatalf("missing complete invocation counters without reasoning: %+v", report)
	}
	model, exists := report.Models["native-model"]
	if !exists {
		t.Fatal("missing model counters without reasoning")
	}
	for _, counts := range []struct {
		// input is the uncached share of the inclusive native input.
		input *int64
		// output includes any unreported reasoning subset.
		output *int64
		// read is the measured cached-input count.
		read *int64
		// write is the measured cache-creation count.
		write *int64
		// reasoning remains unknown when omitted by the native producer.
		reasoning *int64
	}{
		{report.InputTokens, report.OutputTokens, report.CacheReadTokens, report.CacheWriteTokens, report.ReasoningTokens},
		{model.InputTokens, model.OutputTokens, model.CacheReadTokens, model.CacheWriteTokens, model.ReasoningTokens},
	} {
		if counts.input == nil || *counts.input != 75 || counts.output == nil || *counts.output != 30 ||
			counts.read == nil || *counts.read != 20 || counts.write == nil || *counts.write != 5 || counts.reasoning != nil {
			t.Fatalf("incorrect counters without reasoning: %+v", counts)
		}
	}
}

func TestGrokCountersValidateOptionalReasoning(t *testing.T) {
	for _, tc := range []struct {
		// name identifies the optional reasoning evidence.
		name string
		// reasoning preserves presence and invalid native counts.
		reasoning *int64
		// valid indicates whether the native evidence can be normalized.
		valid bool
	}{
		{name: "absent", valid: true},
		{name: "zero", reasoning: acp.Ptr(int64(0)), valid: true},
		{name: "measured subset", reasoning: acp.Ptr(int64(3)), valid: true},
		{name: "negative", reasoning: acp.Ptr(int64(-1))},
		{name: "exceeds output", reasoning: acp.Ptr(int64(31))},
		{name: "inexact metadata", reasoning: acp.Ptr(int64(1 << 53))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			native := grokCounters{Input: acp.Ptr(int64(100)), Output: acp.Ptr(int64(30)),
				CacheRead: acp.Ptr(int64(20)), CacheWrite: acp.Ptr(int64(5)), Reasoning: tc.reasoning}
			got, valid := native.tokens()
			if valid != tc.valid || valid && (got.OutputTokens == nil || *got.OutputTokens != 30 || got.ReasoningTokens != tc.reasoning) {
				t.Fatalf("unexpected reasoning normalization: valid=%t counts=%+v", valid, got)
			}
		})
	}
}

func TestGeminiNativeAllZeroCountersRemainUnknown(t *testing.T) {
	raw := &acpclient.PromptUsageCounters{Input: acp.Ptr(int64(0)), Output: acp.Ptr(int64(0)), Total: acp.Ptr(int64(0))}
	meta := map[string]any{"quota": map[string]any{"token_count": map[string]any{"input_tokens": 0, "output_tokens": 0}}}
	report, scope := geminiNativePromptUsage("0.65.0", raw, meta)
	if report != nil || scope != acpclient.UsageUnattributed {
		t.Fatalf("defaulted native zeros became measured consumption: %+v %v", report, scope)
	}
}

func TestOpenCodePromptUsageKeepsNativeTurnCounters(t *testing.T) {
	for _, row := range []struct {
		// name identifies the native usage rule protected by this case.
		name string
		// version is the native release whose counter semantics were verified.
		version string
		// wire preserves native omission of zero-valued fields.
		wire string
		// source identifies the actual counter source.
		source string
		// coverage identifies the request scope covered by native counters.
		coverage string
		// input excludes cache reads and writes.
		input int64
		// output includes separately reported reasoning tokens.
		output int64
		// reasoning is the reasoning subset of output.
		reasoning int64
		// read counts cache reads separately from uncached input.
		read int64
		// write counts cache writes separately from uncached input.
		write int64
	}{
		{name: "2.0.29 first live prompt", version: "2.0.29", wire: `{"inputTokens":4629,"outputTokens":2,"totalTokens":4631}`,
			source: "opencode.prompt.usage", coverage: "main_loop", input: 4629, output: 2},
		{name: "2.0.29 second live prompt excludes prior turn", version: "2.0.29", wire: `{"inputTokens":40,"outputTokens":2,"totalTokens":4650,"cachedReadTokens":4608}`,
			source: "opencode.prompt.usage", coverage: "main_loop", input: 40, output: 2, read: 4608},
		{name: "2.0.29 additive reasoning and cache writes", version: "2.0.29", wire: `{"inputTokens":120,"outputTokens":15,"thoughtTokens":3,"cachedReadTokens":150,"cachedWriteTokens":50,"totalTokens":338}`,
			source: "opencode.prompt.usage", coverage: "main_loop", input: 120, output: 18, reasoning: 3, read: 150, write: 50},
		{name: "1.18.34 retains last assistant coverage", version: "1.18.34", wire: `{"inputTokens":16,"outputTokens":5,"thoughtTokens":2,"cachedReadTokens":10,"cachedWriteTokens":3,"totalTokens":36}`,
			source: "opencode.assistant.usage", coverage: "observed_requests", input: 16, output: 7, reasoning: 2, read: 10, write: 3},
	} {
		t.Run(row.name, func(t *testing.T) {
			var raw acpclient.PromptUsageCounters
			if err := json.Unmarshal([]byte(row.wire), &raw); err != nil {
				t.Fatal(err)
			}
			report, scope := openCodePromptUsage(row.version, &raw, nil)
			if report == nil || scope != acpclient.UsageUnavailable {
				t.Fatalf("verified native counters were not attributed: report=%#v scope=%v", report, scope)
			}
			if report.Source != row.source || report.Coverage != row.coverage || report.Complete || report.Model != "" || report.Models != nil {
				t.Fatalf("invented invocation coverage or model attribution: %#v", report)
			}
			for field, pair := range map[string]struct {
				// got is the normalized counter produced by the adapter.
				got *int64
				// want is the counter checked by hand against the native example.
				want int64
			}{
				"input": {report.InputTokens, row.input}, "output": {report.OutputTokens, row.output},
				"reasoning": {report.ReasoningTokens, row.reasoning}, "cache read": {report.CacheReadTokens, row.read},
				"cache write": {report.CacheWriteTokens, row.write},
			} {
				if pair.got == nil || *pair.got != pair.want {
					t.Errorf("%s: got %v, want %d", field, pair.got, pair.want)
				}
			}
		})
	}
}

func TestOpenCodePromptUsageRejectsUnprovenCounters(t *testing.T) {
	for _, row := range []struct {
		// name identifies an input that invalidates counter evidence.
		name string
		// version is the native handshake version.
		version string
		// raw contains native counters to validate.
		raw *acpclient.PromptUsageCounters
	}{
		{name: "unknown version", version: "2.0.30", raw: &acpclient.PromptUsageCounters{Input: acp.Ptr(int64(1)), Output: acp.Ptr(int64(1)), Total: acp.Ptr(int64(2))}},
		{name: "missing usage", version: "2.0.29"},
		{name: "missing input", version: "2.0.29", raw: &acpclient.PromptUsageCounters{Output: acp.Ptr(int64(1)), Total: acp.Ptr(int64(1))}},
		{name: "inconsistent total", version: "2.0.29", raw: &acpclient.PromptUsageCounters{Input: acp.Ptr(int64(1)), Output: acp.Ptr(int64(1)), Total: acp.Ptr(int64(3))}},
		{name: "negative optional counter", version: "2.0.29", raw: &acpclient.PromptUsageCounters{Input: acp.Ptr(int64(1)), Output: acp.Ptr(int64(1)), Total: acp.Ptr(int64(2)), CacheRead: acp.Ptr(int64(-1))}},
		{name: "inexact JSON counter", version: "2.0.29", raw: &acpclient.PromptUsageCounters{Input: acp.Ptr(int64(1 << 53)), Output: acp.Ptr(int64(0)), Total: acp.Ptr(int64(1 << 53))}},
	} {
		t.Run(row.name, func(t *testing.T) {
			report, scope := openCodePromptUsage(row.version, row.raw, nil)
			if report != nil || scope != acpclient.UsageUnattributed {
				t.Fatalf("unproven counters became consumption: report=%#v scope=%v", report, scope)
			}
		})
	}
}

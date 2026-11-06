package workbuddy

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	acp "github.com/JieWaZi/acp-go-sdk"
	"github.com/JieWaZi/acp-go/pkg/acpclient"
	"github.com/JieWaZi/acp-go/pkg/acpmeta"
)

func TestWorkBuddyHistoryInputUsesCurrentRequestAndNativeCacheDetails(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	cwd, _ = filepath.EvalSymlinks(cwd)
	scope := acpclient.PromptJournalScope{SessionID: "session", Cwd: cwd, Version: workBuddyHistoryVersion}
	path := filepath.Join(home, "projects", workBuddyProjectSlug(cwd), "session.jsonl")
	firstRequest, secondRequest := "0198a1b2c3d47e5f8a9b0c1d2e3f4a5b", "0198a1b2c3d47e5f8a9b0c1d2e3f4a6b"
	writeHistoryInputRows(t, path, inputHistoryRow(cwd, firstRequest, "stale", 999, 100, 0, 0))
	stop := acp.StopReasonEndTurn
	for _, tc := range []struct {
		// request selects only this native prompt's appended response records.
		request string
		// input is the inclusive native prompt counter.
		input int64
		// read is the detailed cache hit count; the top-level alias is a zero placeholder.
		read int64
		// output includes the independently reported reasoning subset.
		output int64
		// reasoning is the measured subset of output.
		reasoning int64
	}{
		{firstRequest, 2610, 0, 2, 0},
		{secondRequest, 2627, 2432, 17, 15},
	} {
		capture := captureWorkBuddyHistory(t.Context(), scope, home)
		if capture == nil {
			t.Fatal("missing supported history capture")
		}
		row := inputHistoryRow(cwd, tc.request, "response-"+tc.request, tc.input, tc.read, tc.output, tc.reasoning)
		foreign := inputHistoryRow(cwd, firstRequest, "foreign", 8123, 0, 31, 0)
		foreign["sessionId"] = "another-session"
		writeHistoryInputRows(t, path, foreign, row, row)
		usage := capture(t.Context(), acpclient.PromptJournalReceipt{NativeTurnID: tc.request, StopReason: &stop}).Usage
		if usage == nil || usage.Complete || usage.Coverage != "observed_requests" {
			t.Fatalf("history must remain observed request evidence: %+v", usage)
		}
		if usage.Model != "deepseek-v4.1-flash" {
			t.Fatalf("sole actual native model was not attributed: %+v", usage)
		}
		if usage.InputTokens != nil || usage.CacheWriteTokens != nil {
			t.Fatalf("zero cache-write placeholders proved an uncached split: %+v", usage)
		}
		assertHistoryInputCount(t, "cache read", usage.CacheReadTokens, tc.read)
		assertHistoryInputCount(t, "output", usage.OutputTokens, tc.output)
		assertHistoryInputCount(t, "reasoning", usage.ReasoningTokens, tc.reasoning)
		part := usage.Models["deepseek-v4.1-flash"]
		if part.InputTokens != nil || part.CacheWriteTokens != nil {
			t.Fatalf("zero placeholders proved model input or write: %+v", part)
		}
		assertHistoryInputCount(t, "model cache read", part.CacheReadTokens, tc.read)
		assertHistoryInputTotal(t, usage, tc.input)
	}
}

func TestWorkBuddyHistoryKeepsUnprovenInputBreakdownsUnknown(t *testing.T) {
	for _, scenario := range []string{"missing-cache", "placeholder-zero", "conflicting-input", "invalid-cache", "conflicting-duplicate", "unsupported-shape"} {
		t.Run(scenario, func(t *testing.T) {
			home, cwd := t.TempDir(), t.TempDir()
			cwd, _ = filepath.EvalSymlinks(cwd)
			path := filepath.Join(home, "projects", workBuddyProjectSlug(cwd), "session.jsonl")
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				t.Fatal(err)
			}
			capture := captureWorkBuddyHistory(t.Context(), acpclient.PromptJournalScope{SessionID: "session", Cwd: cwd, Version: workBuddyHistoryVersion}, home)
			if capture == nil {
				t.Fatal("missing capture")
			}
			request := "0198a1b2c3d47e5f8a9b0c1d2e3f4a5b"
			row := inputHistoryRow(cwd, request, "one", 300, 200, 30, 5)
			data := row["providerData"].(map[string]any)
			raw := data["rawUsage"].(map[string]any)
			normalized := data["usage"].(map[string]any)
			wantRaw := true
			switch scenario {
			case "missing-cache":
				delete(raw, "prompt_tokens_details")
				delete(raw, "prompt_cache_hit_tokens")
				delete(raw, "prompt_cache_miss_tokens")
				delete(raw, "cache_creation_input_tokens")
				delete(raw, "prompt_cache_write_tokens")
			case "placeholder-zero":
				row = inputHistoryRow(cwd, request, "one", 0, 0, 0, 0)
				wantRaw = false
			case "conflicting-input":
				normalized["inputTokens"] = int64(301)
				wantRaw = false
			case "invalid-cache":
				raw["prompt_tokens_details"] = map[string]any{"cached_tokens": int64(301)}
			case "conflicting-duplicate":
				writeHistoryInputRows(t, path, inputHistoryRow(cwd, request, "one", 301, 200, 30, 5))
				wantRaw = false
			case "unsupported-shape":
				delete(raw, "prompt_tokens")
				raw["inputTokens"] = int64(300)
			}
			writeHistoryInputRows(t, path, row)
			stop := acp.StopReasonEndTurn
			usage := capture(context.Background(), acpclient.PromptJournalReceipt{NativeTurnID: request, StopReason: &stop}).Usage
			if usage == nil || usage.Complete || usage.InputTokens != nil || usage.CacheReadTokens != nil {
				t.Fatalf("unproven breakdown became additive consumption: %+v", usage)
			}
			if usage.CacheWriteTokens != nil {
				t.Fatalf("unproven cache write was retained: %+v", usage)
			}
			if wantRaw {
				assertHistoryInputTotal(t, usage, 300)
			} else if len(usage.Observations) != 0 {
				t.Fatalf("unproven raw input was retained: %+v", usage.Observations)
			}
		})
	}
}

func TestWorkBuddyHistoryUnknownVersionDoesNotReadInput(t *testing.T) {
	if captureWorkBuddyHistory(t.Context(), acpclient.PromptJournalScope{Version: "2.181.0", SessionID: "session", Cwd: t.TempDir()}, t.TempDir()) != nil {
		t.Fatal("unsupported history protocol was read")
	}
}

func TestWorkBuddyHistorySplitsInputWithMeasuredPositiveCacheWrite(t *testing.T) {
	data := inputHistoryRow("/workspace", "request", "response", 300, 200, 30, 5)
	raw := data["providerData"].(map[string]any)["rawUsage"].(map[string]any)
	raw["prompt_cache_write_tokens"] = int64(5)
	encoded, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	var row workBuddyHistoryRow
	if err := json.Unmarshal(encoded, &row); err != nil {
		t.Fatal(err)
	}
	total, input, read, write := row.inputTokens(30)
	assertHistoryInputCount(t, "native input", total, 300)
	assertHistoryInputCount(t, "uncached input", input, 95)
	assertHistoryInputCount(t, "cache read", read, 200)
	assertHistoryInputCount(t, "measured cache write", write, 5)
}

func TestWorkBuddyHistoryRetainsKnownCacheWhenWriteIsUnknown(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	cwd, _ = filepath.EvalSymlinks(cwd)
	path := filepath.Join(home, "projects", workBuddyProjectSlug(cwd), "session.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	request := "0198a1b2c3d47e5f8a9b0c1d2e3f4a5b"
	capture := captureWorkBuddyHistory(t.Context(), acpclient.PromptJournalScope{SessionID: "session", Cwd: cwd, Version: workBuddyHistoryVersion}, home)
	row := inputHistoryRow(cwd, request, "one", 300, 200, 30, 5)
	raw := row["providerData"].(map[string]any)["rawUsage"].(map[string]any)
	delete(raw, "cache_creation_input_tokens")
	delete(raw, "prompt_cache_write_tokens")
	writeHistoryInputRows(t, path, row, row)
	stop := acp.StopReasonEndTurn
	usage := capture(t.Context(), acpclient.PromptJournalReceipt{NativeTurnID: request, StopReason: &stop}).Usage
	if usage == nil || usage.InputTokens != nil || usage.CacheWriteTokens != nil || usage.Complete {
		t.Fatalf("unknown write became known: %+v", usage)
	}
	assertHistoryInputCount(t, "known cache read", usage.CacheReadTokens, 200)
	assertHistoryInputCount(t, "known model cache read", usage.Models["deepseek-v4.1-flash"].CacheReadTokens, 200)
	assertHistoryInputTotal(t, usage, 300)
}

func inputHistoryRow(cwd, request, message string, input, read, output, reasoning int64) map[string]any {
	return map[string]any{"type": "message", "role": "assistant", "cwd": cwd, "sessionId": "session", "providerData": map[string]any{
		"conversationRequestId": request, "messageId": message, "model": "deepseek-v4.1-flash",
		"rawUsage": map[string]any{"prompt_tokens": input, "completion_tokens": output, "total_tokens": input + output,
			"cache_read_input_tokens": int64(0), "cache_creation_input_tokens": int64(0), "prompt_cache_write_tokens": int64(0),
			"prompt_cache_hit_tokens": read, "prompt_cache_miss_tokens": input - read,
			"prompt_tokens_details": map[string]any{"cached_tokens": read}, "completion_tokens_details": map[string]any{"reasoning_tokens": reasoning}},
		"usage": map[string]any{"inputTokens": input, "outputTokens": output, "totalTokens": input + output, "requests": int64(1)},
	}}
}

func writeHistoryInputRows(t *testing.T, path string, rows ...map[string]any) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	for _, row := range rows {
		if err := json.NewEncoder(file).Encode(row); err != nil {
			t.Fatal(err)
		}
	}
}

func assertHistoryInputCount(t *testing.T, name string, got *int64, want int64) {
	t.Helper()
	if got == nil || *got != want {
		t.Fatalf("%s = %v, want %d", name, got, want)
	}
}

func assertHistoryInputTotal(t *testing.T, usage *acpmeta.TurnUsage, want int64) {
	t.Helper()
	if len(usage.Observations) != 1 || usage.Observations[0].Source != usage.Source || usage.Observations[0].Coverage != usage.Coverage || usage.Observations[0].Complete {
		t.Fatalf("raw input must be one nonadditive observation of the same requests: %+v", usage)
	}
	assertHistoryInputCount(t, "native inclusive input", usage.Observations[0].InputTotalTokens, want)
}

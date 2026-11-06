package workbuddy

import (
	"encoding/json"

	acp "github.com/JieWaZi/acp-go-sdk"
)

// inputTokens uses the preserved provider counters, never the cumulative session
// context. The verified Chat Completions path has inclusive prompt_tokens;
// other representations retain their native input without assuming a breakdown.
func (row workBuddyHistoryRow) inputTokens(output int64) (total, input, read, write *int64) {
	var raw map[string]json.RawMessage
	var normalized struct {
		// Input corroborates the preserved provider input for this response.
		Input *int64 `json:"inputTokens"`
	}
	if json.Unmarshal(row.Data.RawUsage, &raw) != nil || json.Unmarshal(row.Data.Usage, &normalized) != nil {
		return
	}
	total = historyRawCounter(raw, "prompt_tokens", "input_tokens", "inputTokens")
	// Providers can synthesize an all-zero usage object when no measured
	// counters arrived. A zero prompt does not prove measured consumption.
	if total == nil || *total == 0 || !sameHistoryCounter(total, normalized.Input) {
		return nil, nil, nil, nil
	}
	if _, inclusive := raw["prompt_tokens"]; !inclusive {
		return total, nil, nil, nil
	}
	if nativeTotal, exists := raw["total_tokens"]; exists {
		count := historyRawCounter(map[string]json.RawMessage{"total": nativeTotal}, "total")
		if count == nil || *total > maxWorkBuddyTokenCount-output || *count != *total+output {
			return nil, nil, nil, nil
		}
	}
	read = historyCacheRead(raw)
	write = historyCacheCounter(raw, "cache_creation_input_tokens", "cacheCreationInputTokens", "prompt_cache_write_tokens")
	if write != nil && *write == 0 {
		// The native usage utilities default unavailable cache creation to zero.
		// A raw zero has no verified provenance distinguishing it from that
		// placeholder, so it cannot prove an uncached input decomposition.
		write = nil
	}
	if read != nil && *read > *total {
		return total, nil, nil, nil
	}
	if write != nil && (*write > *total || read != nil && *write > *total-*read) {
		return total, nil, nil, nil
	}
	if _, present := raw["prompt_cache_miss_tokens"]; present && read != nil {
		miss := historyRawCounter(raw, "prompt_cache_miss_tokens")
		// A DeepSeek miss is the uncached prompt share, not cache creation.
		if miss == nil || *miss != *total-*read {
			return total, nil, nil, nil
		}
	}
	if read == nil || write == nil {
		return total, nil, read, write
	}
	return total, acp.Ptr(*total - *read - *write), read, write
}

func historyRawCounter(raw map[string]json.RawMessage, keys ...string) *int64 {
	var count *int64
	for _, key := range keys {
		value, present := raw[key]
		if !present {
			continue
		}
		var next *int64
		if json.Unmarshal(value, &next) != nil || next == nil || *next < 0 || *next > maxWorkBuddyTokenCount || count != nil && *count != *next {
			return nil
		}
		count = next
	}
	return count
}

// historyCacheCounter follows the native positive-counter precedence, while
// rejecting contradictory positive aliases instead of choosing one arbitrarily.
func historyCacheCounter(raw map[string]json.RawMessage, keys ...string) *int64 {
	var count *int64
	for _, key := range keys {
		if _, present := raw[key]; !present {
			continue
		}
		next := historyRawCounter(raw, key)
		if next == nil {
			return nil
		}
		if count == nil || *count == 0 {
			count = next
		} else if *next != 0 && *next != *count {
			return nil
		}
	}
	return count
}

func historyCacheRead(raw map[string]json.RawMessage) *int64 {
	fields := make(map[string]json.RawMessage, len(raw)+1)
	for key, value := range raw {
		fields[key] = value
	}
	if details, exists := raw["prompt_tokens_details"]; exists {
		var nested map[string]json.RawMessage
		if json.Unmarshal(details, &nested) != nil || nested == nil {
			return nil
		}
		if value, present := nested["cached_tokens"]; present {
			fields["detailed_cached_tokens"] = value
		}
	}
	count := historyCacheCounter(fields, "cache_read_input_tokens", "cacheReadInputTokens", "detailed_cached_tokens", "prompt_cache_hit_tokens")
	if count != nil && *count == 0 && fields["detailed_cached_tokens"] == nil && fields["prompt_cache_hit_tokens"] == nil {
		// WorkBuddy emits top-level zero placeholders alongside valid detailed
		// cache hits. Without a detailed observation, those zeros prove nothing.
		return nil
	}
	return count
}

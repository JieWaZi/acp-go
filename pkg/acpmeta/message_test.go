package acpmeta

import (
	"encoding/json"
	"testing"
)

// TestMessageInfoPreservesMetadata 验证消息事实穿过 JSON 后仍可读取，且不修改原元数据。
func TestMessageInfoPreservesMetadata(t *testing.T) {
	original := map[string]any{"vendor": "keep"}
	metadata := WithMessageInfo(original, MessageInfo{Phase: MessagePhaseUnknown, Complete: true, Source: MessageSourceNativeMessage})
	encoded, err := json.Marshal(metadata)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	info, ok := ReadMessageInfo(decoded)
	if !ok || info.Phase != "unknown" || !info.Complete || info.Source != "native_message" || decoded["vendor"] != "keep" {
		t.Fatalf("facts lost: %#v %#v", info, decoded)
	}
	if len(original) != 1 {
		t.Fatalf("input mutated: %#v", original)
	}
	for _, raw := range []map[string]any{nil, {"acp-go/message": map[string]any{"phase": "final", "complete": true}}, {"acp-go/message": map[string]any{"phase": "invented", "complete": true, "source": "native_phase"}}, {"acp-go/message": map[string]any{"phase": "final", "complete": true, "source": "adapter_segment"}}} {
		if value, ok := ReadMessageInfo(raw); ok {
			t.Fatalf("unproven facts accepted: %#v", value)
		}
	}
}

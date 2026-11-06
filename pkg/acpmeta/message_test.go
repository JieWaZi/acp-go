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

func TestMessageInfoReplacementIsExplicitAndIndependent(t *testing.T) {
	body := ""
	info := MessageInfo{Phase: MessagePhaseCommentary, Complete: true, Source: MessageSourceNativePhase, ReplacementText: &body}
	wire := WithMessageInfo(nil, info)
	body = "mutated input"
	encoded, err := json.Marshal(wire)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	first, ok := ReadMessageInfo(decoded)
	if !ok || first.ReplacementText == nil || *first.ReplacementText != "" {
		t.Fatalf("explicit empty replacement lost: %+v", first)
	}
	*first.ReplacementText = "mutated snapshot"
	second, ok := ReadMessageInfo(decoded)
	if !ok || second.ReplacementText == nil || *second.ReplacementText != "" {
		t.Fatalf("replacement aliases metadata: %+v", second)
	}
	info.ReplacementText = nil
	if legacy, ok := ReadMessageInfo(WithMessageInfo(nil, info)); !ok || legacy.ReplacementText != nil {
		t.Fatalf("legacy metadata invented replacement: %+v", legacy)
	}
	for _, raw := range []map[string]any{
		{"phase": "commentary", "complete": false, "source": "native_phase", "replacement_text": "invalid"},
		{"phase": "unknown", "complete": true, "source": "adapter_segment", "replacement_text": "invalid"},
		{"phase": "commentary", "complete": true, "source": "native_phase", "replacement_text": nil},
	} {
		if actual, ok := ReadMessageInfo(map[string]any{messageInfoKey: raw}); ok {
			t.Fatalf("invalid replacement accepted: %+v", actual)
		}
	}
}

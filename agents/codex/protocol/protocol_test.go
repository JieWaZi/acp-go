package protocol

import (
	"encoding/json"
	"testing"
)

// TestThreadStartParamsOmitMissingOptionalFields 防止可选字段被错误序列化为显式 null。
func TestThreadStartParamsOmitMissingOptionalFields(t *testing.T) {
	encoded, err := json.Marshal(ThreadStartParams{})
	if err != nil {
		t.Fatalf("序列化 thread/start 参数失败：%v", err)
	}
	if string(encoded) != `{}` {
		t.Fatalf("空 thread/start 参数 = %s，期望 {}", encoded)
	}
}

// TestTurnStartParamsPreserveRequiredFields 防止 required 数组和 threadId 被 omitempty 丢弃。
func TestTurnStartParamsPreserveRequiredFields(t *testing.T) {
	params := TurnStartParams{
		Input:    []InputElement{},
		ThreadID: "thread-1",
	}

	encoded, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("序列化 turn/start 参数失败：%v", err)
	}
	var wire map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &wire); err != nil {
		t.Fatalf("解析 turn/start wire 失败：%v", err)
	}
	if string(wire["input"]) != `[]` {
		t.Fatalf("input wire = %s，期望 []", wire["input"])
	}
	if string(wire["threadId"]) != `"thread-1"` {
		t.Fatalf("threadId wire = %s，期望 thread-1", wire["threadId"])
	}
}

// TestUserInputTaggedUnionRoundTrip 防止 UserInput 的 type discriminator 或变体字段在往返中丢失。
func TestUserInputTaggedUnionRoundTrip(t *testing.T) {
	raw := []byte(`{"type":"text","text":"hello","text_elements":[]}`)
	var input UserInput
	if err := json.Unmarshal(raw, &input); err != nil {
		t.Fatalf("解析 text UserInput 失败：%v", err)
	}
	if input.Type != UserInputTypeText {
		t.Fatalf("UserInput type = %q，期望 %q", input.Type, UserInputTypeText)
	}
	if input.Text == nil || *input.Text != "hello" {
		t.Fatalf("UserInput text = %v，期望 hello", input.Text)
	}

	encoded, err := json.Marshal(input)
	if err != nil {
		t.Fatalf("序列化 text UserInput 失败：%v", err)
	}
	var wire map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &wire); err != nil {
		t.Fatalf("解析 text UserInput wire 失败：%v", err)
	}
	if string(wire["type"]) != `"text"` || string(wire["text"]) != `"hello"` {
		t.Fatalf("UserInput wire 未保留 tagged union：%s", encoded)
	}
}

// TestApprovalDecisionUnionRoundTrip 防止审批响应的字符串 union 被退化为不透明 map。
func TestApprovalDecisionUnionRoundTrip(t *testing.T) {
	raw := []byte(`{"decision":"cancel"}`)
	var response CommandExecutionRequestApprovalResponse
	if err := json.Unmarshal(raw, &response); err != nil {
		t.Fatalf("解析 command approval 响应失败：%v", err)
	}
	if response.Decision == nil || response.Decision.Enum == nil {
		t.Fatal("command approval decision 未解析为枚举 union")
	}

	encoded, err := json.Marshal(response)
	if err != nil {
		t.Fatalf("序列化 command approval 响应失败：%v", err)
	}
	if string(encoded) != string(raw) {
		t.Fatalf("command approval wire = %s，期望 %s", encoded, raw)
	}
}

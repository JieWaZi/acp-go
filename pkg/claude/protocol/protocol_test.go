package protocol

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"testing"
)

// TestDecodeMessageVariants 验证顶层判别先于具体 payload 解码，未知消息仍保留原文。
func TestDecodeMessageVariants(t *testing.T) {
	tests := []struct {
		// name 是子测试名称。
		name string
		// raw 是待解码的单行 JSON。
		raw string
		// want 表示预期的具体消息类型。
		want any
	}{
		{
			name: "system init",
			raw:  `{"type":"system","subtype":"init","session_id":"s1","uuid":"u1","claude_code_version":"2.1.0","cwd":"/tmp","model":"sonnet","permissionMode":"default","tools":[],"terminal_slash_commands":["doctor"],"mcp_servers":[],"future":true}`,
			want: &SystemInitMessage{},
		},
		{
			name: "commands changed",
			raw:  `{"type":"system","subtype":"commands_changed","session_id":"s1","uuid":"u-command","commands":[{"name":"diagnosing-bugs","description":"Diagnose","argumentHint":"<symptom>"}]}`,
			want: &SystemMessage{},
		},
		{
			name: "assistant",
			raw:  `{"type":"assistant","message":{"id":"m1","role":"assistant","content":[{"type":"text","text":"hi","future":1}]},"parent_tool_use_id":null,"uuid":"u2","session_id":"s1"}`,
			want: &AssistantMessage{},
		},
		{
			name: "control request",
			raw:  `{"type":"control_request","request_id":"r1","request":{"subtype":"can_use_tool","tool_name":"Bash","input":{},"tool_use_id":"t1"}}`,
			want: &ControlRequestMessage{},
		},
		{name: "future top level", raw: `{"type":"future_event","value":1}`, want: &UnknownMessage{}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			message, err := DecodeMessage([]byte(test.raw))
			if err != nil {
				t.Fatalf("解码失败：%v", err)
			}
			switch test.want.(type) {
			case *SystemInitMessage:
				init, ok := message.(*SystemInitMessage)
				if !ok || init.SessionID != "s1" || init.ClaudeCodeVersion != "2.1.0" ||
					!reflect.DeepEqual(init.TerminalSlashCommands, []string{"doctor"}) {
					t.Fatalf("init = %#v", message)
				}
			case *SystemMessage:
				system, ok := message.(*SystemMessage)
				if !ok || system.Subtype != "commands_changed" || len(system.Commands) != 1 ||
					system.Commands[0].Name != "diagnosing-bugs" {
					t.Fatalf("system = %#v", message)
				}
			case *AssistantMessage:
				assistant, ok := message.(*AssistantMessage)
				if !ok || len(assistant.Message.Content) != 1 || string(assistant.Message.Content[0].Raw) == "" {
					t.Fatalf("assistant = %#v", message)
				}
			case *ControlRequestMessage:
				request, ok := message.(*ControlRequestMessage)
				if !ok || request.ControlSubtype() != ControlCanUseTool {
					t.Fatalf("control request = %#v", message)
				}
			case *UnknownMessage:
				if _, ok := message.(*UnknownMessage); !ok || string(message.RawJSON()) != test.raw {
					t.Fatalf("unknown = %#v raw=%s", message, message.RawJSON())
				}
			}
		})
	}
}

// TestFrozenSessionFixture 验证冻结 Session 消息序列持续覆盖核心判别类型。
func TestFrozenSessionFixture(t *testing.T) {
	file, err := os.Open("testdata/session-turn.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	types := []string{}
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		message, decodeErr := DecodeMessage(scanner.Bytes())
		if decodeErr != nil {
			t.Fatalf("fixture 解码失败：%v", decodeErr)
		}
		types = append(types, message.MessageType())
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"system", "system", "user", "stream_event", "stream_event", "stream_event",
		"assistant", "tool_progress", "user", "result", "system",
	}
	if !reflect.DeepEqual(types, want) {
		t.Fatalf("types = %#v, want %#v", types, want)
	}
}

// TestDecodeMessageRejectsMalformed 验证坏 JSON 和缺失 type 不会伪装为 unknown。
func TestDecodeMessageRejectsMalformed(t *testing.T) {
	for _, raw := range []string{`{`, `{}`, `{"type":1}`} {
		if _, err := DecodeMessage([]byte(raw)); !errors.Is(err, ErrInvalidMessage) {
			t.Fatalf("raw=%q err=%v，期望 ErrInvalidMessage", raw, err)
		}
	}
}

// TestControlEnvelopes 验证 request/response/cancel 的 wire 形状保持稳定。
func TestControlEnvelopes(t *testing.T) {
	request, err := NewControlRequest("r1", InterruptControlRequest{
		Subtype: ControlInterrupt, CancelQueued: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertJSONEqual(t,
		`{"type":"control_request","request_id":"r1","request":{"subtype":"interrupt","cancel_queued":true}}`,
		request,
	)

	response, err := NewControlSuccess("r2", PermissionResult{
		Behavior: "deny", Message: "cancelled", ToolUseID: "tool-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	assertJSONEqual(t,
		`{"type":"control_response","response":{"subtype":"success","request_id":"r2","response":{"behavior":"deny","message":"cancelled","toolUseID":"tool-1"}}}`,
		response,
	)

	cancel, err := NewControlCancelRequest("r3")
	if err != nil {
		t.Fatal(err)
	}
	assertJSONEqual(t, `{"type":"control_cancel_request","request_id":"r3"}`, cancel)
}

// TestControlRequestDecodePermission 验证权限请求使用窄类型读取，同时保留 input/suggestions 原文。
func TestControlRequestDecodePermission(t *testing.T) {
	message, err := DecodeMessage([]byte(`{"type":"control_request","request_id":"r1","request":{"subtype":"can_use_tool","tool_name":"Edit","input":{"file_path":"a.go"},"permission_suggestions":[{"type":"addRules"}],"tool_use_id":"t1"}}`))
	if err != nil {
		t.Fatal(err)
	}
	request := message.(*ControlRequestMessage)
	var permission CanUseToolControlRequest
	if err := request.DecodeRequest(&permission); err != nil {
		t.Fatal(err)
	}
	if permission.ToolName != "Edit" || permission.ToolUseID != "t1" || len(permission.Input) == 0 || len(permission.PermissionSuggestions) == 0 {
		t.Fatalf("permission = %#v", permission)
	}
}

// assertJSONEqual 比较 JSON 语义，避免字段顺序影响协议测试。
func assertJSONEqual(t *testing.T, want string, got any) {
	t.Helper()
	wantRaw := json.RawMessage(want)
	gotRaw, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("编码结果失败：%v", err)
	}
	if !jsonEqual(wantRaw, gotRaw) {
		t.Fatalf("JSON = %s，期望 %s", gotRaw, wantRaw)
	}
}

// jsonEqual 把对象解码为 any 后比较规范化 JSON。
func jsonEqual(left, right []byte) bool {
	var leftValue any
	var rightValue any
	if json.Unmarshal(left, &leftValue) != nil || json.Unmarshal(right, &rightValue) != nil {
		return false
	}
	leftJSON, _ := json.Marshal(leftValue)
	rightJSON, _ := json.Marshal(rightValue)
	return string(leftJSON) == string(rightJSON)
}

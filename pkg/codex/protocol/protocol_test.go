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

// TestOptionalNullableRoundTrip 锁住 optional+nullable 的 absent、null、value 三种 wire 状态。
func TestOptionalNullableRoundTrip(t *testing.T) {
	type fixture struct {
		// Value 是需要验证 absent、null、value 三态的字段。
		Value OptionalNullable[string] `json:"value,omitzero"`
	}

	testCases := []struct {
		// name 是子测试名称。
		name string
		// wire 是输入和预期输出使用的 JSON。
		wire string
		// present 表示字段是否出现在 wire 中。
		present bool
		// null 表示字段是否显式为 null。
		null bool
		// wantValue 是 value 状态下的期望字符串。
		wantValue string
		// valueValid 表示本用例是否应断言具体值。
		valueValid bool
	}{
		{name: "absent", wire: `{}`},
		{name: "null", wire: `{"value":null}`, present: true, null: true},
		{name: "value", wire: `{"value":"/workspace"}`, present: true, wantValue: "/workspace", valueValid: true},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			var decoded fixture
			if err := json.Unmarshal([]byte(testCase.wire), &decoded); err != nil {
				t.Fatalf("解析三态值失败：%v", err)
			}
			if decoded.Value.IsPresent() != testCase.present {
				t.Fatalf("present = %v，期望 %v", decoded.Value.IsPresent(), testCase.present)
			}
			if decoded.Value.IsNull() != testCase.null {
				t.Fatalf("null = %v，期望 %v", decoded.Value.IsNull(), testCase.null)
			}
			value, valid := decoded.Value.Value()
			if valid != testCase.valueValid || value != testCase.wantValue {
				t.Fatalf("value = %q, %v，期望 %q, %v", value, valid, testCase.wantValue, testCase.valueValid)
			}

			encoded, err := json.Marshal(decoded)
			if err != nil {
				t.Fatalf("序列化三态值失败：%v", err)
			}
			if string(encoded) != testCase.wire {
				t.Fatalf("三态 wire = %s，期望 %s", encoded, testCase.wire)
			}
		})
	}
}

// TestServerNotificationKeepsMethodParamsCoupled 防止通知 method 与不相干的 params 被压成字段并集。
func TestServerNotificationKeepsMethodParamsCoupled(t *testing.T) {
	raw := []byte(`{"method":"item/started","params":{"item":{"id":"item-1","type":"agentMessage"},"startedAtMs":7,"threadId":"thread-1","turnId":"turn-1"}}`)

	notification, err := DecodeServerNotification(raw)
	if err != nil {
		t.Fatalf("解析 item/started 通知失败：%v", err)
	}
	started, ok := notification.(*ItemStartedEnvelope)
	if !ok {
		t.Fatalf("通知类型 = %T，期望 *ItemStartedEnvelope", notification)
	}
	if started.Method() != "item/started" {
		t.Fatalf("通知 method = %q，期望 item/started", started.Method())
	}
	if started.Params.Item.ID != "item-1" {
		t.Fatalf("typed Item id = %q，期望 item-1", started.Params.Item.ID)
	}

	encoded, err := json.Marshal(notification)
	if err != nil {
		t.Fatalf("序列化 typed 通知失败：%v", err)
	}
	if string(encoded) != string(raw) {
		t.Fatalf("typed 通知 wire = %s，期望 %s", encoded, raw)
	}
}

// TestUnknownServerNotificationPreservesRawParams 防止前向扩展通知经过 fallback 后丢失未知字段或整数精度。
func TestUnknownServerNotificationPreservesRawParams(t *testing.T) {
	raw := []byte(`{"method":"extension/future","params":{"counter":9007199254740993}}`)

	notification, err := DecodeServerNotification(raw)
	if err != nil {
		t.Fatalf("解析未知通知失败：%v", err)
	}
	unknown, ok := notification.(*UnknownServerNotification)
	if !ok {
		t.Fatalf("未知通知类型 = %T，期望 *UnknownServerNotification", notification)
	}
	if string(unknown.Params) != `{"counter":9007199254740993}` {
		t.Fatalf("未知 Params = %s，未保持原始 JSON", unknown.Params)
	}
	encoded, err := json.Marshal(unknown)
	if err != nil {
		t.Fatalf("序列化未知通知失败：%v", err)
	}
	if string(encoded) != string(raw) {
		t.Fatalf("未知通知 wire = %s，期望 %s", encoded, raw)
	}
}

// TestClientRequestKeepsMethodParamsCoupled 防止调用方为 thread/start 构造其他方法的 Params。
func TestClientRequestKeepsMethodParamsCoupled(t *testing.T) {
	idValue := int64(9)
	request := NewThreadStartRequest(RequestID{Integer: &idValue}, ThreadStartParams{})

	encoded, err := json.Marshal(request)
	if err != nil {
		t.Fatalf("序列化 thread/start 请求失败：%v", err)
	}
	var wire struct {
		// Method 是请求 envelope 的方法名。
		Method string `json:"method"`
		// Params 是 thread/start 的强类型参数。
		Params ThreadStartParams `json:"params"`
	}
	if err := json.Unmarshal(encoded, &wire); err != nil {
		t.Fatalf("解析 thread/start wire 失败：%v", err)
	}
	if wire.Method != "thread/start" {
		t.Fatalf("请求 method = %q，期望 thread/start", wire.Method)
	}
}

// TestClientRequestIDRoundTrip 锁住客户端请求整数和字符串 RequestID 的标量 wire 语义。
func TestClientRequestIDRoundTrip(t *testing.T) {
	integerID := int64(9)
	stringID := "request-9"
	testCases := []struct {
		// name 是子测试名称。
		name string
		// id 是需要往返的强类型请求标识。
		id RequestID
		// wire 是标识的预期 JSON 标量。
		wire string
	}{
		{name: "integer", id: RequestID{Integer: &integerID}, wire: `9`},
		{name: "string", id: RequestID{String: &stringID}, wire: `"request-9"`},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			request := NewThreadStartRequest(testCase.id, ThreadStartParams{})
			encoded, err := json.Marshal(request)
			if err != nil {
				t.Fatalf("序列化客户端请求失败：%v", err)
			}
			var wire map[string]json.RawMessage
			if err := json.Unmarshal(encoded, &wire); err != nil {
				t.Fatalf("解析客户端请求 wire 失败：%v", err)
			}
			if string(wire["id"]) != testCase.wire {
				t.Fatalf("客户端 RequestID wire = %s，期望 %s", wire["id"], testCase.wire)
			}

			decoded, err := DecodeClientRequest(encoded)
			if err != nil {
				t.Fatalf("往返解码客户端请求失败：%v", err)
			}
			if _, ok := decoded.(*ThreadStartRequest); !ok {
				t.Fatalf("客户端请求类型 = %T，期望 *ThreadStartRequest", decoded)
			}
		})
	}
}

// TestServerRequestIDRoundTrip 锁住服务端请求整数和字符串 RequestID 的标量 wire 语义。
func TestServerRequestIDRoundTrip(t *testing.T) {
	testCases := []struct {
		// name 是子测试名称。
		name string
		// id 是嵌入请求 envelope 的原始标识。
		id string
	}{
		{name: "integer", id: `9`},
		{name: "string", id: `"request-9"`},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			raw := []byte(`{"id":` + testCase.id + `,"method":"item/fileChange/requestApproval","params":{"itemId":"item-1","startedAtMs":1,"threadId":"thread-1","turnId":"turn-1"}}`)
			request, err := DecodeServerRequest(raw)
			if err != nil {
				t.Fatalf("初次解码服务端请求失败：%v", err)
			}
			encoded, err := json.Marshal(request)
			if err != nil {
				t.Fatalf("序列化服务端请求失败：%v", err)
			}
			var wire map[string]json.RawMessage
			if err := json.Unmarshal(encoded, &wire); err != nil {
				t.Fatalf("解析服务端请求 wire 失败：%v", err)
			}
			if string(wire["id"]) != testCase.id {
				t.Fatalf("服务端 RequestID wire = %s，期望 %s", wire["id"], testCase.id)
			}

			decoded, err := DecodeServerRequest(encoded)
			if err != nil {
				t.Fatalf("往返解码服务端请求失败：%v", err)
			}
			if _, ok := decoded.(*FileChangeApprovalRequest); !ok {
				t.Fatalf("服务端请求类型 = %T，期望 *FileChangeApprovalRequest", decoded)
			}
		})
	}
}

// TestApprovalOptionalNullableFieldsRoundTrip 用实际 DTO 锁住 grantRoot 与 strictAutoReview 三态。
func TestApprovalOptionalNullableFieldsRoundTrip(t *testing.T) {
	fileRaw := []byte(`{"grantRoot":null,"itemId":"item-1","startedAtMs":1,"threadId":"thread-1","turnId":"turn-1"}`)
	var fileParams FileChangeRequestApprovalParams
	if err := json.Unmarshal(fileRaw, &fileParams); err != nil {
		t.Fatalf("解析 file approval 参数失败：%v", err)
	}
	if !fileParams.GrantRoot.IsPresent() || !fileParams.GrantRoot.IsNull() {
		t.Fatal("grantRoot 显式 null 未被保留")
	}
	fileEncoded, err := json.Marshal(fileParams)
	if err != nil {
		t.Fatalf("序列化 file approval 参数失败：%v", err)
	}
	if string(fileEncoded) != string(fileRaw) {
		t.Fatalf("file approval wire = %s，期望 %s", fileEncoded, fileRaw)
	}
	fileAbsentRaw := []byte(`{"itemId":"item-1","startedAtMs":1,"threadId":"thread-1","turnId":"turn-1"}`)
	var fileAbsent FileChangeRequestApprovalParams
	if err := json.Unmarshal(fileAbsentRaw, &fileAbsent); err != nil {
		t.Fatalf("解析缺省 grantRoot 失败：%v", err)
	}
	if fileAbsent.GrantRoot.IsPresent() {
		t.Fatal("缺省 grantRoot 被误判为已出现")
	}
	fileAbsent.GrantRoot = NewOptionalValue("/workspace")
	fileValueEncoded, err := json.Marshal(fileAbsent)
	if err != nil {
		t.Fatalf("序列化 grantRoot value 失败：%v", err)
	}
	var fileValueWire map[string]json.RawMessage
	if err := json.Unmarshal(fileValueEncoded, &fileValueWire); err != nil {
		t.Fatalf("解析 grantRoot value wire 失败：%v", err)
	}
	if string(fileValueWire["grantRoot"]) != `"/workspace"` {
		t.Fatalf("grantRoot value wire = %s，期望 /workspace", fileValueWire["grantRoot"])
	}

	permissionsRaw := []byte(`{"permissions":{},"strictAutoReview":null}`)
	var permissionsResponse PermissionsRequestApprovalResponse
	if err := json.Unmarshal(permissionsRaw, &permissionsResponse); err != nil {
		t.Fatalf("解析 permissions approval 响应失败：%v", err)
	}
	if !permissionsResponse.StrictAutoReview.IsPresent() || !permissionsResponse.StrictAutoReview.IsNull() {
		t.Fatal("strictAutoReview 显式 null 未被保留")
	}
	permissionsEncoded, err := json.Marshal(permissionsResponse)
	if err != nil {
		t.Fatalf("序列化 permissions approval 响应失败：%v", err)
	}
	if string(permissionsEncoded) != string(permissionsRaw) {
		t.Fatalf("permissions approval wire = %s，期望 %s", permissionsEncoded, permissionsRaw)
	}
	var permissionsAbsent PermissionsRequestApprovalResponse
	if err := json.Unmarshal([]byte(`{"permissions":{}}`), &permissionsAbsent); err != nil {
		t.Fatalf("解析缺省 strictAutoReview 失败：%v", err)
	}
	if permissionsAbsent.StrictAutoReview.IsPresent() {
		t.Fatal("缺省 strictAutoReview 被误判为已出现")
	}
	permissionsAbsent.StrictAutoReview = NewOptionalValue(true)
	permissionsValueEncoded, err := json.Marshal(permissionsAbsent)
	if err != nil {
		t.Fatalf("序列化 strictAutoReview value 失败：%v", err)
	}
	var permissionsValueWire map[string]json.RawMessage
	if err := json.Unmarshal(permissionsValueEncoded, &permissionsValueWire); err != nil {
		t.Fatalf("解析 strictAutoReview value wire 失败：%v", err)
	}
	if string(permissionsValueWire["strictAutoReview"]) != `true` {
		t.Fatalf("strictAutoReview value wire = %s，期望 true", permissionsValueWire["strictAutoReview"])
	}
}

// TestAccountLoginCompletedNotificationIsTyped 锁住登录流程订阅所需的完成通知 payload。
func TestAccountLoginCompletedNotificationIsTyped(t *testing.T) {
	raw := []byte(`{"method":"account/login/completed","params":{"loginId":"login-1","success":true,"error":null,"onboardingEntrypoint":null}}`)

	notification, err := DecodeServerNotification(raw)
	if err != nil {
		t.Fatalf("解析登录完成通知失败：%v", err)
	}
	completed, ok := notification.(*AccountLoginCompletedEnvelope)
	if !ok {
		t.Fatalf("登录完成通知类型 = %T，期望 *AccountLoginCompletedEnvelope", notification)
	}
	if !completed.Params.Success || completed.Params.LoginID == nil || *completed.Params.LoginID != "login-1" {
		t.Fatalf("登录完成 Params = %+v，期望 success/loginId 强类型值", completed.Params)
	}
}

// TestV1MethodConstantsAreCanonical 防止 runtime 与 events 重复散落硬编码方法字符串。
func TestV1MethodConstantsAreCanonical(t *testing.T) {
	testCases := map[string]string{
		"thread start":            MethodThreadStart,
		"turn start":              MethodTurnStart,
		"command approval":        MethodCommandExecutionRequestApproval,
		"item started":            MethodItemStarted,
		"account login completed": MethodAccountLoginCompleted,
	}
	want := map[string]string{
		"thread start":            "thread/start",
		"turn start":              "turn/start",
		"command approval":        "item/commandExecution/requestApproval",
		"item started":            "item/started",
		"account login completed": "account/login/completed",
	}
	for name, method := range testCases {
		if method != want[name] {
			t.Fatalf("%s method = %q，期望 %q", name, method, want[name])
		}
	}
}

// TestEnvelopeDecodeFailureReturnsNil 防止调用方在错误路径误用只完成部分解码的变体。
func TestEnvelopeDecodeFailureReturnsNil(t *testing.T) {
	testCases := []struct {
		// name 是子测试名称。
		name string
		// decode 是当前 envelope 类别的解码入口。
		decode func([]byte) (any, error)
		// wire 是必须失败且不能返回部分变体的输入。
		wire string
	}{
		{
			name: "client request",
			decode: func(data []byte) (any, error) {
				return DecodeClientRequest(data)
			},
			wire: `{"id":1,"method":"thread/start","params":42}`,
		},
		{
			name: "server request",
			decode: func(data []byte) (any, error) {
				return DecodeServerRequest(data)
			},
			wire: `{"id":1,"method":"item/fileChange/requestApproval","params":42}`,
		},
		{
			name: "server notification",
			decode: func(data []byte) (any, error) {
				return DecodeServerNotification(data)
			},
			wire: `{"method":"item/started","params":42}`,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			value, err := testCase.decode([]byte(testCase.wire))
			if err == nil {
				t.Fatal("畸形 params 未返回错误")
			}
			if value != nil {
				t.Fatalf("错误路径返回了部分值 %T", value)
			}
		})
	}
}

// TestCoreOpenJSONUsesRawMessage 防止核心 request/item 的开放 JSON 退化为会改写数值的 interface{}。
func TestCoreOpenJSONUsesRawMessage(t *testing.T) {
	raw := json.RawMessage(`{"type":"object"}`)
	turn := TurnStartParams{OutputSchema: raw}
	var outputSchema json.RawMessage = turn.OutputSchema
	if string(outputSchema) != string(raw) {
		t.Fatalf("outputSchema = %s，期望 %s", outputSchema, raw)
	}

	item := ThreadItem{Arguments: raw}
	var arguments json.RawMessage = item.Arguments
	if string(arguments) != string(raw) {
		t.Fatalf("arguments = %s，期望 %s", arguments, raw)
	}

	capabilities := InitializeCapabilities{
		Extensions: map[string]json.RawMessage{"openai/form": raw},
	}
	if string(capabilities.Extensions["openai/form"]) != string(raw) {
		t.Fatal("initialize extension 未保留原始 JSON")
	}

	thread := ThreadStartParams{Config: map[string]json.RawMessage{"feature": raw}}
	if string(thread.Config["feature"]) != string(raw) {
		t.Fatal("thread config 未保留原始 JSON")
	}
}

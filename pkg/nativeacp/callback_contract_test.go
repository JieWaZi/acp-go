package nativeacp

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	acp "github.com/coder/acp-go-sdk"
)

// callbackObservation 保存宿主实际收到的完整请求，便于校验字段和元数据。
type callbackObservation struct {
	// method 标识到达外层客户端的真实方法。
	method string
	// params 保存 SDK 解码后再次序列化的 payload。
	params any
}

// captureCallback 记录实际宿主调用并生成固定响应元数据。
func (host *lifecycleHost) captureCallback(method string, request any) map[string]any {
	raw, err := json.Marshal(request)
	if err != nil {
		panic(err)
	}
	var params any
	if err := json.Unmarshal(raw, &params); err != nil {
		panic(err)
	}
	host.callbacks <- callbackObservation{method: method, params: params}
	return map[string]any{"reply": "preserved"}
}

// ReadTextFile 返回宿主文件内容，并提供原生错误透传 fixture。
func (host *lifecycleHost) ReadTextFile(_ context.Context, request acp.ReadTextFileRequest) (acp.ReadTextFileResponse, error) {
	meta := host.captureCallback("fs/read_text_file", request)
	if request.Path == "/error" {
		return acp.ReadTextFileResponse{}, &acp.RequestError{Code: -32077, Message: "read refused", Data: map[string]any{"reason": "opaque", "_meta": map[string]any{"trace": "file"}}}
	}
	return acp.ReadTextFileResponse{Content: "file body", Meta: meta}, nil
}

// WriteTextFile 返回写入确认，保留请求中的内容和元数据。
func (host *lifecycleHost) WriteTextFile(_ context.Context, request acp.WriteTextFileRequest) (acp.WriteTextFileResponse, error) {
	return acp.WriteTextFileResponse{Meta: host.captureCallback("fs/write_text_file", request)}, nil
}

// RequestPermission 使用真实外层审批端口返回选项。
func (host *lifecycleHost) RequestPermission(_ context.Context, request acp.RequestPermissionRequest) (acp.RequestPermissionResponse, error) {
	return acp.RequestPermissionResponse{Meta: host.captureCallback("session/request_permission", request), Outcome: acp.NewRequestPermissionOutcomeSelected("once")}, nil
}

// CreateTerminal 返回宿主创建的终端标识。
func (host *lifecycleHost) CreateTerminal(_ context.Context, request acp.CreateTerminalRequest) (acp.CreateTerminalResponse, error) {
	return acp.CreateTerminalResponse{TerminalId: "terminal", Meta: host.captureCallback("terminal/create", request)}, nil
}

// KillTerminal 确认终端中断请求。
func (host *lifecycleHost) KillTerminal(_ context.Context, request acp.KillTerminalRequest) (acp.KillTerminalResponse, error) {
	return acp.KillTerminalResponse{Meta: host.captureCallback("terminal/kill", request)}, nil
}

// TerminalOutput 返回宿主终端输出。
func (host *lifecycleHost) TerminalOutput(_ context.Context, request acp.TerminalOutputRequest) (acp.TerminalOutputResponse, error) {
	return acp.TerminalOutputResponse{Output: "terminal body", Meta: host.captureCallback("terminal/output", request)}, nil
}

// ReleaseTerminal 确认宿主释放终端资源。
func (host *lifecycleHost) ReleaseTerminal(_ context.Context, request acp.ReleaseTerminalRequest) (acp.ReleaseTerminalResponse, error) {
	return acp.ReleaseTerminalResponse{Meta: host.captureCallback("terminal/release", request)}, nil
}

// WaitForTerminalExit 返回原始退出状态。
func (host *lifecycleHost) WaitForTerminalExit(_ context.Context, request acp.WaitForTerminalExitRequest) (acp.WaitForTerminalExitResponse, error) {
	return acp.WaitForTerminalExitResponse{ExitCode: acp.Ptr(7), Meta: host.captureCallback("terminal/wait_for_exit", request)}, nil
}

// UnstableCreateElicitation 返回宿主的真实问答结果。
func (host *lifecycleHost) UnstableCreateElicitation(_ context.Context, request acp.UnstableCreateElicitationRequest) (acp.UnstableCreateElicitationResponse, error) {
	meta := host.captureCallback("elicitation/create", request)
	result := acp.NewUnstableCreateElicitationResponseAccept()
	result.Accept.Meta = meta
	result.Accept.Content = map[string]any{"answer": "chosen"}
	return result, nil
}

// UnstableCompleteElicitation 记录结构化问答完成通知。
func (host *lifecycleHost) UnstableCompleteElicitation(_ context.Context, request acp.UnstableCompleteElicitationNotification) error {
	host.captureNotification("elicitation/complete", request)
	return nil
}

// UnstableConnectMcp 返回真实外层 MCP 连接标识。
func (host *lifecycleHost) UnstableConnectMcp(_ context.Context, request acp.UnstableConnectMcpRequest) (acp.UnstableConnectMcpResponse, error) {
	return acp.UnstableConnectMcpResponse{ConnectionId: "mcp-connection", Meta: host.captureCallback("mcp/connect", request)}, nil
}

// UnstableDisconnectMcp 确认 MCP 连接释放。
func (host *lifecycleHost) UnstableDisconnectMcp(_ context.Context, request acp.UnstableDisconnectMcpRequest) (acp.UnstableDisconnectMcpResponse, error) {
	return acp.UnstableDisconnectMcpResponse{Meta: host.captureCallback("mcp/disconnect", request)}, nil
}

// HandleExtensionMethod 用请求型用户输入 fixture 检查扩展反向转发不会自循环。
func (host *lifecycleHost) HandleExtensionMethod(_ context.Context, method string, params json.RawMessage) (any, error) {
	if method != "_user_input" {
		return nil, acp.NewMethodNotFound(method)
	}
	host.captureCallback(method, params)
	return map[string]any{"_meta": map[string]any{"reply": "preserved"}, "answer": "chosen"}, nil
}

// TestNativeCallbacksPreservePayloadMetadataAndErrors 验证现有回调边界的完整双向请求。
func TestNativeCallbacksPreservePayloadMetadataAndErrors(t *testing.T) {
	agent, peer, host := startLifecycleBridge(t, true, true)
	running := promptAgentAsync(agent, context.Background(), "a", "interactive")
	expectUpdate(t, host, "a:interactive")
	cases := []struct {
		// method 是 CLI 使用的标准或扩展回调名称。
		method string
		// params 是手工推导的原始字段与元数据 fixture。
		params string
	}{
		{method: "fs/read_text_file", params: `{"sessionId":"a","path":"/fixture","line":2,"limit":3,"_meta":{"trace":"file"}}`},
		{method: "fs/write_text_file", params: `{"sessionId":"a","path":"/fixture","content":"write body","_meta":{"trace":"file"}}`},
		{method: "session/request_permission", params: `{"sessionId":"a","toolCall":{"toolCallId":"permission","title":"Approve","_meta":{"tool":"retained"}},"options":[{"optionId":"once","kind":"allow_once","name":"Once"}],"_meta":{"trace":"permission"}}`},
		{method: "terminal/create", params: `{"sessionId":"a","command":"fixture-command","args":["literal"],"env":[{"name":"KEY","value":"value"}],"cwd":"/fixture","outputByteLimit":1024,"_meta":{"trace":"terminal"}}`},
		{method: "terminal/kill", params: `{"sessionId":"a","terminalId":"terminal","_meta":{"trace":"terminal"}}`},
		{method: "terminal/output", params: `{"sessionId":"a","terminalId":"terminal","_meta":{"trace":"terminal"}}`},
		{method: "terminal/release", params: `{"sessionId":"a","terminalId":"terminal","_meta":{"trace":"terminal"}}`},
		{method: "terminal/wait_for_exit", params: `{"sessionId":"a","terminalId":"terminal","_meta":{"trace":"terminal"}}`},
		{method: "elicitation/create", params: `{"mode":"form","message":"Choose","requestedSchema":{"type":"object","properties":{"answer":{"type":"string"}}},"_meta":{"sessionId":"a","trace":"question"}}`},
		{method: "mcp/connect", params: `{"acpId":"server","_meta":{"trace":"mcp"}}`},
		{method: "mcp/disconnect", params: `{"connectionId":"mcp-connection","_meta":{"trace":"mcp"}}`},
		{method: "_user_input", params: `{"sessionId":"a","questions":[{"text":"Choose"}],"_meta":{"trace":"user"}}`},
	}
	for _, fixture := range cases {
		t.Run(fixture.method, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			raw, err := peer.CallExtension(ctx, "_callback", map[string]any{"method": fixture.method, "params": json.RawMessage(fixture.params)})
			if err != nil {
				t.Fatal(err)
			}
			var result map[string]any
			if err := json.Unmarshal(raw, &result); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(result["_meta"], map[string]any{"reply": "preserved"}) {
				t.Fatalf("callback response metadata: %s", raw)
			}
			select {
			case observed := <-host.callbacks:
				var want any
				if err := json.Unmarshal([]byte(fixture.params), &want); err != nil {
					t.Fatal(err)
				}
				if observed.method != fixture.method || !reflect.DeepEqual(observed.params, want) {
					t.Fatalf("callback changed request: %+v", observed)
				}
			case <-ctx.Done():
				t.Fatal("callback did not reach host")
			}
		})
	}
	_, err := peer.CallExtension(context.Background(), "_callback", map[string]any{"method": "fs/read_text_file", "params": json.RawMessage(`{"sessionId":"a","path":"/error"}`)})
	var requestErr *acp.RequestError
	if !errors.As(err, &requestErr) || requestErr.Code != -32077 || requestErr.Message != "read refused" || !reflect.DeepEqual(requestErr.Data, map[string]any{"reason": "opaque", "_meta": map[string]any{"trace": "file"}}) {
		t.Fatalf("callback changed error: %v", err)
	}
	releaseNative(t, peer, "a")
	awaitPrompt(t, running, false)
}

// captureNotification 记录宿主实际收到的通知及其元数据。
func (host *lifecycleHost) captureNotification(method string, request any) {
	raw, err := json.Marshal(request)
	if err != nil {
		panic(err)
	}
	var params any
	if err := json.Unmarshal(raw, &params); err != nil {
		panic(err)
	}
	host.notifications <- callbackObservation{method: method, params: params}
}

// TestNativeNotificationsPreserveMetadata 验证流式消息与问答完成通知保持原样。
func TestNativeNotificationsPreserveMetadata(t *testing.T) {
	_, peer, host := startLifecycleBridge(t, true, true)
	fixtures := []struct {
		// method 是标准原生通知名称。
		method string
		// params 是包含自定义元数据的固定通知。
		params string
	}{
		{method: "session/update", params: `{"sessionId":"a","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"notice"}},"_meta":{"trace":"update"}}`},
		{method: "elicitation/complete", params: `{"elicitationId":"question","_meta":{"trace":"complete"}}`},
	}
	for _, fixture := range fixtures {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		_, err := peer.CallExtension(ctx, "_notify", map[string]any{"method": fixture.method, "params": json.RawMessage(fixture.params)})
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		select {
		case observed := <-host.notifications:
			var want any
			if err := json.Unmarshal([]byte(fixture.params), &want); err != nil {
				cancel()
				t.Fatal(err)
			}
			if observed.method != fixture.method || !reflect.DeepEqual(observed.params, want) {
				cancel()
				t.Fatalf("changed notification: %+v", observed)
			}
		case <-ctx.Done():
			cancel()
			t.Fatal("notification did not reach host")
		}
		cancel()
	}
}

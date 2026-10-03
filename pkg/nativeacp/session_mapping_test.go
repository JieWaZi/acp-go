package nativeacp

import (
	"context"
	"encoding/json"
	"errors"
	acp "github.com/coder/acp-go-sdk"
	"reflect"
	"strings"
	"testing"
	"time"
)

// TestPublicSessionMappingCoversEveryCallback 验证现有回调边界的完整双向请求。
func TestPublicSessionMappingCoversEveryCallback(t *testing.T) {
	agent, peer, host := startLifecycleBridge(t, true, true)
	agent.config.PublicSessionID = func(acp.SessionId) acp.SessionId { return "public-session" }
	running := promptAgentAsync(agent, context.Background(), "a", "interactive")
	expectUpdate(t, host, "public-session:interactive")
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
				if err := json.Unmarshal([]byte(strings.ReplaceAll(fixture.params, `"sessionId":"a"`, `"sessionId":"public-session"`)), &want); err != nil {
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

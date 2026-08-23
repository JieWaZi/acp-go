package codex

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"acp-go/agents/codex/protocol"
	acp "github.com/coder/acp-go-sdk"
)

// steeringRawParams 编码 SDK extension 输入，确保 ContentBlock 仍由 SDK union 解码。
func steeringRawParams(t *testing.T, sessionID, text string) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(map[string]any{
		"sessionId": sessionID,
		"prompt":    []acp.ContentBlock{{Text: &acp.ContentBlockText{Type: "text", Text: text}}},
	})
	if err != nil {
		t.Fatalf("编码 steering params 失败: %v", err)
	}
	return data
}

// TestSteeringFIFOStartsThenInjects 验证同 session 并发 steering 严格先启动新 turn、再注入该 turn。
func TestSteeringFIFOStartsThenInjects(t *testing.T) {
	t.Parallel()
	rpc := newFakeAppServerRPC()
	turnStarted := make(chan struct{})
	steered := make(chan string, 1)
	rpc.handleCall = func(_ context.Context, request protocol.ClientRequest, result any) error {
		switch request.Method() {
		case protocol.MethodInitialize:
			return nil
		case protocol.MethodThreadStart:
			result.(*protocol.ThreadStartResponse).Thread.ID = "thread-1"
			return nil
		case protocol.MethodTurnStart:
			result.(*protocol.TurnStartResponse).Turn = protocol.TurnElement{
				ID: "steering-turn", Items: []protocol.ThreadItem{}, Status: protocol.PurpleInProgress,
			}
			close(turnStarted)
			return nil
		case protocol.MethodTurnSteer:
			steered <- request.Method()
			result.(*protocol.TurnSteerResponse).TurnID = "steering-turn"
			return nil
		default:
			return errors.New("unexpected call: " + request.Method())
		}
	}
	agent := newRuntimeTestAgent(t, rpc)
	if _, err := agent.NewSession(context.Background(), acp.NewSessionRequest{Cwd: "/tmp", McpServers: []acp.McpServer{}}); err != nil {
		t.Fatalf("创建 session 失败: %v", err)
	}

	firstResult := make(chan any, 1)
	firstErr := make(chan error, 1)
	go func() {
		result, err := agent.HandleExtensionMethod(
			context.Background(), steeringExtensionMethod, steeringRawParams(t, "thread-1", "first"),
		)
		firstResult <- result
		firstErr <- err
	}()
	<-turnStarted
	secondResult := make(chan any, 1)
	secondErr := make(chan error, 1)
	go func() {
		result, err := agent.HandleExtensionMethod(
			context.Background(), steeringExtensionMethod, steeringRawParams(t, "thread-1", "second"),
		)
		secondResult <- result
		secondErr <- err
	}()
	if err := <-firstErr; err != nil {
		t.Fatalf("第一 steering 错误: %v", err)
	}
	if result := <-firstResult; result != (steeringResponse{Outcome: steeringStartedNewTurn}) {
		t.Fatalf("第一 steering 结果为 %#v", result)
	}
	<-steered
	if err := <-secondErr; err != nil {
		t.Fatalf("第二 steering 错误: %v", err)
	}
	if result := <-secondResult; result != (steeringResponse{Outcome: steeringInjected}) {
		t.Fatalf("第二 steering 结果为 %#v", result)
	}
	agent.client.HandleNotification(context.Background(), completeNotification(
		t, "thread-1", "steering-turn", protocol.FluffyCompleted,
	))
}

// TestSteeringFallbackInheritsSessionConfiguration 验证无活动 turn 的 steering 复用当前 session 配置。
// 若 fallback 绕过 Prompt 的 model/effort/mode 映射，本测试应失败。
func TestSteeringFallbackInheritsSessionConfiguration(t *testing.T) {
	t.Parallel()
	rpc := newFakeAppServerRPC()
	turnParams := make(chan protocol.TurnStartParams, 1)
	rpc.handleCall = func(_ context.Context, request protocol.ClientRequest, result any) error {
		switch request.Method() {
		case protocol.MethodInitialize:
			return nil
		case protocol.MethodThreadStart:
			result.(*protocol.ThreadStartResponse).Thread.ID = "configured-steering-thread"
			return nil
		case protocol.MethodTurnStart:
			wire, err := json.Marshal(request)
			if err != nil {
				return err
			}
			var envelope struct {
				// Params 保留 steering fallback 真实发送的 turn/start 参数。
				Params protocol.TurnStartParams `json:"params"`
			}
			if err = json.Unmarshal(wire, &envelope); err != nil {
				return err
			}
			turnParams <- envelope.Params
			result.(*protocol.TurnStartResponse).Turn = protocol.TurnElement{
				ID: "configured-steering-turn", Items: []protocol.ThreadItem{}, Status: protocol.PurpleInProgress,
			}
			return nil
		default:
			return errors.New("unexpected call: " + request.Method())
		}
	}
	agent := newRuntimeTestAgent(t, rpc)
	created, err := agent.NewSession(context.Background(), acp.NewSessionRequest{
		Cwd: "/configured-workspace", McpServers: []acp.McpServer{},
	})
	if err != nil {
		t.Fatalf("创建 session 失败: %v", err)
	}
	if _, err = agent.SetSessionMode(context.Background(), acp.SetSessionModeRequest{
		SessionId: created.SessionId, ModeId: "agent-full-access",
	}); err != nil {
		t.Fatalf("设置 mode 失败: %v", err)
	}
	if _, err = agent.SetSessionConfigOption(context.Background(), acp.SetSessionConfigOptionRequest{
		ValueId: &acp.SetSessionConfigOptionValueId{
			SessionId: created.SessionId, ConfigId: modelConfigID, Value: "slow-model",
		},
	}); err != nil {
		t.Fatalf("设置 model 失败: %v", err)
	}
	if _, err = agent.SetSessionConfigOption(context.Background(), acp.SetSessionConfigOptionRequest{
		ValueId: &acp.SetSessionConfigOptionValueId{
			SessionId: created.SessionId, ConfigId: reasoningEffortConfigID, Value: "low",
		},
	}); err != nil {
		t.Fatalf("设置 effort 失败: %v", err)
	}

	result, err := agent.HandleExtensionMethod(
		context.Background(),
		steeringExtensionMethod,
		steeringRawParams(t, string(created.SessionId), "continue"),
	)
	if err != nil || result != (steeringResponse{Outcome: steeringStartedNewTurn}) {
		t.Fatalf("steering fallback 响应为 %#v, %v", result, err)
	}
	params := <-turnParams
	if params.Model == nil || *params.Model != "slow-model" ||
		params.Effort == nil || *params.Effort != "low" ||
		params.ApprovalPolicy == nil || params.ApprovalPolicy.Enum == nil ||
		*params.ApprovalPolicy.Enum != protocol.Never || params.SandboxPolicy == nil ||
		params.SandboxPolicy.Type != protocol.SandboxPolicyTypeDangerFullAccess ||
		params.Cwd == nil || *params.Cwd != "/configured-workspace" {
		t.Fatalf("steering fallback turn/start 参数为 %#v", params)
	}
	agent.client.HandleNotification(context.Background(), completeNotification(
		t, "configured-steering-thread", "configured-steering-turn", protocol.FluffyCompleted,
	))
}

// TestSteeringFailureDoesNotStallNextRequest 验证单项 unexpected steer 失败返回 failed，FIFO 继续下一项。
func TestSteeringFailureDoesNotStallNextRequest(t *testing.T) {
	t.Parallel()
	rpc := newFakeAppServerRPC()
	turnStarted := make(chan struct{})
	firstSteerCalled := make(chan struct{})
	releaseFirstSteer := make(chan struct{})
	var steerMu sync.Mutex
	steerCount := 0
	rpc.handleCall = func(_ context.Context, request protocol.ClientRequest, result any) error {
		switch request.Method() {
		case protocol.MethodInitialize:
			return nil
		case protocol.MethodThreadStart:
			result.(*protocol.ThreadStartResponse).Thread.ID = "thread-1"
			return nil
		case protocol.MethodTurnStart:
			result.(*protocol.TurnStartResponse).Turn = protocol.TurnElement{
				ID: "active-turn", Items: []protocol.ThreadItem{}, Status: protocol.PurpleInProgress,
			}
			close(turnStarted)
			return nil
		case protocol.MethodTurnSteer:
			steerMu.Lock()
			steerCount++
			current := steerCount
			steerMu.Unlock()
			if current == 1 {
				close(firstSteerCalled)
				<-releaseFirstSteer
				return errors.New("unexpected steer failure")
			}
			result.(*protocol.TurnSteerResponse).TurnID = "active-turn"
			return nil
		default:
			return errors.New("unexpected call: " + request.Method())
		}
	}
	agent := newRuntimeTestAgent(t, rpc)
	if _, err := agent.NewSession(context.Background(), acp.NewSessionRequest{Cwd: "/tmp", McpServers: []acp.McpServer{}}); err != nil {
		t.Fatalf("创建 session 失败: %v", err)
	}
	promptResult := make(chan error, 1)
	go func() {
		_, err := agent.Prompt(context.Background(), acp.PromptRequest{
			SessionId: "thread-1",
			Prompt:    []acp.ContentBlock{{Text: &acp.ContentBlockText{Type: "text", Text: "working"}}},
		})
		promptResult <- err
	}()
	<-turnStarted

	firstResult := make(chan any, 1)
	go func() {
		result, _ := agent.HandleExtensionMethod(
			context.Background(), steeringExtensionMethod, steeringRawParams(t, "thread-1", "first"),
		)
		firstResult <- result
	}()
	<-firstSteerCalled
	secondResult := make(chan any, 1)
	go func() {
		result, _ := agent.HandleExtensionMethod(
			context.Background(), steeringExtensionMethod, steeringRawParams(t, "thread-1", "second"),
		)
		secondResult <- result
	}()
	close(releaseFirstSteer)
	if result := <-firstResult; result != (steeringResponse{Outcome: steeringFailed}) {
		t.Fatalf("失败 steering 结果为 %#v", result)
	}
	if result := <-secondResult; result != (steeringResponse{Outcome: steeringInjected}) {
		t.Fatalf("后继 steering 结果为 %#v", result)
	}
	agent.client.HandleNotification(context.Background(), completeNotification(
		t, "thread-1", "active-turn", protocol.FluffyCompleted,
	))
	if err := <-promptResult; err != nil {
		t.Fatalf("活动 prompt 错误: %v", err)
	}
}

// TestNoActiveTurnToSteerErrorMatchesUpstreamDataShapes 验证固定上游同时识别 string data 与 object.details。
func TestNoActiveTurnToSteerErrorMatchesUpstreamDataShapes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		// name 描述 app-server 返回的 data 形状。
		name string
		// data 是 rpc error 的原始 JSON data。
		data json.RawMessage
	}{
		{name: "字符串", data: json.RawMessage(`"No active turn to steer"`)},
		{name: "对象 details", data: json.RawMessage(`{"details":"No active turn to steer"}`)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := &rpcError{Code: -32000, Message: "turn changed", Data: tt.data}
			if !isNoActiveTurnToSteerError(err) {
				t.Fatalf("未识别 no-active-turn data: %s", tt.data)
			}
		})
	}
}

// TestSteeringRejectsMalformedParamsAndUnknownMethod 验证 extension 输入校验与 SDK 标准 method-not-found。
func TestSteeringRejectsMalformedParamsAndUnknownMethod(t *testing.T) {
	t.Parallel()
	rpc := newFakeAppServerRPC()
	rpc.handleCall = func(_ context.Context, request protocol.ClientRequest, _ any) error {
		if request.Method() == protocol.MethodInitialize {
			return nil
		}
		return errors.New("unexpected call")
	}
	agent := newRuntimeTestAgent(t, rpc)

	if _, err := agent.HandleExtensionMethod(
		context.Background(), steeringExtensionMethod, json.RawMessage(`{"sessionId":"missing-prompt"}`),
	); err == nil {
		t.Fatal("缺少 prompt 的 steering 未返回错误")
	}
	_, err := agent.HandleExtensionMethod(context.Background(), "_unknown", json.RawMessage(`{}`))
	var requestErr *acp.RequestError
	if !errors.As(err, &requestErr) || requestErr.Code != -32601 {
		t.Fatalf("未知 extension 错误为 %v", err)
	}
}

// TestSteeringQueueIsBoundedAndCloseRejectsPending 验证等待上限与 session close 会确定性解除排队项。
func TestSteeringQueueIsBoundedAndCloseRejectsPending(t *testing.T) {
	t.Parallel()
	rpc := newFakeAppServerRPC()
	turnStarted := make(chan struct{})
	steerCalled := make(chan struct{})
	var steerOnce sync.Once
	rpc.handleCall = func(ctx context.Context, request protocol.ClientRequest, result any) error {
		switch request.Method() {
		case protocol.MethodInitialize:
			return nil
		case protocol.MethodThreadStart:
			result.(*protocol.ThreadStartResponse).Thread.ID = "thread-1"
			return nil
		case protocol.MethodTurnStart:
			result.(*protocol.TurnStartResponse).Turn = protocol.TurnElement{
				ID: "active-turn", Items: []protocol.ThreadItem{}, Status: protocol.PurpleInProgress,
			}
			close(turnStarted)
			return nil
		case protocol.MethodTurnSteer:
			steerOnce.Do(func() { close(steerCalled) })
			<-ctx.Done()
			return ctx.Err()
		default:
			return errors.New("unexpected call: " + request.Method())
		}
	}
	agent := newRuntimeTestAgent(t, rpc)
	if _, err := agent.NewSession(context.Background(), acp.NewSessionRequest{Cwd: "/tmp", McpServers: []acp.McpServer{}}); err != nil {
		t.Fatalf("创建 session 失败: %v", err)
	}
	promptResult := make(chan error, 1)
	go func() {
		_, err := agent.Prompt(context.Background(), acp.PromptRequest{
			SessionId: "thread-1",
			Prompt:    []acp.ContentBlock{{Text: &acp.ContentBlockText{Type: "text", Text: "working"}}},
		})
		promptResult <- err
	}()
	<-turnStarted
	state, _ := agent.sessions.get("thread-1")
	queue := newSteeringQueue(agent.steering, "thread-1", 1)
	params := steeringParams{
		SessionID: "thread-1",
		Prompt:    []acp.ContentBlock{{Text: &acp.ContentBlockText{Type: "text", Text: "queued"}}},
	}
	first := &queuedSteering{
		ctx: context.Background(), params: params, generation: state.generation,
		result: make(chan steeringResult, 1), enqueued: make(chan struct{}),
	}
	firstResult := make(chan error, 1)
	go func() {
		_, err := queue.enqueue(first)
		firstResult <- err
	}()
	<-first.enqueued
	<-steerCalled
	second := &queuedSteering{
		ctx: context.Background(), params: params, generation: state.generation,
		result: make(chan steeringResult, 1), enqueued: make(chan struct{}),
	}
	secondResult := make(chan error, 1)
	go func() {
		_, err := queue.enqueue(second)
		secondResult <- err
	}()
	<-second.enqueued
	third := &queuedSteering{
		ctx: context.Background(), params: params, generation: state.generation,
		result: make(chan steeringResult, 1),
	}
	if _, err := queue.enqueue(third); err == nil {
		t.Fatal("超出 steering queue 上限未返回错误")
	}
	queue.close(ErrSessionClosing)
	if err := <-secondResult; !errors.Is(err, ErrSessionClosing) {
		t.Fatalf("关闭后 pending 错误为 %v", err)
	}
	if err := <-firstResult; !errors.Is(err, ErrSessionClosing) {
		t.Fatalf("关闭后 active steering 错误为 %v", err)
	}
	agent.client.HandleNotification(context.Background(), completeNotification(
		t, "thread-1", "active-turn", protocol.FluffyCompleted,
	))
	if err := <-promptResult; err != nil {
		t.Fatalf("活动 prompt 错误: %v", err)
	}
}

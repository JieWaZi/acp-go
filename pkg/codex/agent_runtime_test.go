package codex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JieWaZi/acp-go/pkg/acpmeta"
	"github.com/JieWaZi/acp-go/pkg/codex/protocol"
	acp "github.com/JieWaZi/acp-go/sdk"
)

// runtimeWireRequest 保存真实 transport 写向 fake app-server 的请求 envelope。
type runtimeWireRequest struct {
	// ID 是 fake app-server 回应时必须原样回显的请求标识。
	ID json.RawMessage `json:"id"`
	// Method 是生成协议请求的固定 discriminator。
	Method string `json:"method"`
	// Params 保留测试需要核对的完整请求参数。
	Params json.RawMessage `json:"params"`
}

// readRuntimeWireRequest 从真实 NDJSON writer 读取并解码一条 app-server 请求。
func readRuntimeWireRequest(t *testing.T, harness *transportHarness) runtimeWireRequest {
	t.Helper()
	line, err := harness.requests.ReadBytes('\n')
	if err != nil {
		t.Fatalf("读取 fake app-server 请求失败: %v", err)
	}
	var request runtimeWireRequest
	if err = json.Unmarshal(line, &request); err != nil {
		t.Fatalf("解码 fake app-server 请求失败: %v", err)
	}
	return request
}

// writeRuntimeWireResult 向真实 NDJSON reader 写入与请求 ID 匹配的 result。
func writeRuntimeWireResult(
	t *testing.T,
	harness *transportHarness,
	id json.RawMessage,
	result json.RawMessage,
) {
	t.Helper()
	line, err := json.Marshal(struct {
		// ID 原样回显 fake app-server 收到的请求标识。
		ID json.RawMessage `json:"id"`
		// Result 是由测试手工构造的服务端响应 fixture。
		Result json.RawMessage `json:"result"`
	}{ID: id, Result: result})
	if err != nil {
		t.Fatalf("编码 fake app-server 响应失败: %v", err)
	}
	if _, err = harness.responses.Write(append(line, '\n')); err != nil {
		t.Fatalf("写 fake app-server 响应失败: %v", err)
	}
}

// newRuntimeTestAgent 使用 fake typed client 创建已完成 connection barrier 的 Agent。
func newRuntimeTestAgent(t *testing.T, rpc *fakeAppServerRPC) *Agent {
	t.Helper()
	originalHandler := rpc.handleCall
	rpc.handleCall = func(ctx context.Context, request protocol.ClientRequest, result any) error {
		// 通用 fixture 补齐 schema 必需的 model，并为 Session 配置提供稳定目录。
		if request.Method() == protocol.MethodModelList {
			result.(*protocol.ModelListResponse).Data = testModels()
			return nil
		}
		if err := originalHandler(ctx, request, result); err != nil {
			return err
		}
		switch response := result.(type) {
		case *protocol.ThreadStartResponse:
			if response.Model == "" {
				response.Model = "fast-model"
			}
		case *protocol.ThreadResumeResponse:
			if response.Model == "" {
				response.Model = "fast-model"
			}
		}
		return nil
	}
	runtimeCtx, cancel := context.WithCancel(context.Background())
	client := newAppServerClient(runtimeCtx, rpc)
	agent := newAgentWithClient(
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		runtimeCtx,
		cancel,
		client,
	)
	agent.markConnectionReady()
	if _, err := agent.Initialize(context.Background(), acp.InitializeRequest{
		ProtocolVersion: acp.ProtocolVersionNumber,
	}); err != nil {
		t.Fatalf("初始化测试 Agent 失败: %v", err)
	}
	t.Cleanup(func() { cancel() })
	return agent
}

// TestAgentNewAndLoadSessionUseThreadFlow 验证 new 与 load 的 thread/start、resume→read 顺序。
func TestAgentNewAndLoadSessionUseThreadFlow(t *testing.T) {
	t.Parallel()
	rpc := newFakeAppServerRPC()
	rpc.handleCall = func(_ context.Context, request protocol.ClientRequest, result any) error {
		switch request.Method() {
		case protocol.MethodInitialize:
			return nil
		case protocol.MethodThreadStart:
			result.(*protocol.ThreadStartResponse).Thread.ID = "new-thread"
			return nil
		case protocol.MethodThreadResume:
			result.(*protocol.ThreadResumeResponse).Thread.ID = "saved-thread"
			return nil
		case protocol.MethodThreadRead:
			result.(*protocol.ThreadReadResponse).Thread = protocol.Thread{ID: "saved-thread", Turns: []protocol.TurnElement{}}
			return nil
		default:
			return errors.New("unexpected call: " + request.Method())
		}
	}
	agent := newRuntimeTestAgent(t, rpc)

	created, err := agent.NewSession(context.Background(), acp.NewSessionRequest{Cwd: "/tmp", McpServers: []acp.McpServer{}})
	if err != nil {
		t.Fatalf("创建 session 失败: %v", err)
	}
	if created.SessionId != "new-thread" {
		t.Fatalf("新 SessionID 为 %q", created.SessionId)
	}
	_, err = agent.LoadSession(context.Background(), acp.LoadSessionRequest{
		SessionId: "saved-thread", Cwd: "/tmp", McpServers: []acp.McpServer{},
	})
	if err != nil {
		t.Fatalf("加载 session 失败: %v", err)
	}
	if got := rpc.calls; len(got) != 8 || got[1] != protocol.MethodAccountRead ||
		got[2] != protocol.MethodThreadStart || got[3] != protocol.MethodModelList ||
		got[4] != protocol.MethodAccountRead || got[5] != protocol.MethodThreadResume ||
		got[6] != protocol.MethodThreadRead || got[7] != protocol.MethodModelList {
		t.Fatalf("请求顺序为 %v", got)
	}
}

// TestAgentNewSessionRequiresAuthenticationBeforeThreadStart 验证固定 upstream 的前置鉴权检查。
func TestAgentNewSessionRequiresAuthenticationBeforeThreadStart(t *testing.T) {
	t.Parallel()
	rpc := newFakeAppServerRPC()
	rpc.accountResponse = &protocol.GetAccountResponse{RequiresOpenaiAuth: true}
	rpc.handleCall = func(_ context.Context, request protocol.ClientRequest, _ any) error {
		if request.Method() == protocol.MethodInitialize {
			return nil
		}
		return errors.New("unexpected call: " + request.Method())
	}
	agent := newRuntimeTestAgent(t, rpc)

	_, err := agent.NewSession(context.Background(), acp.NewSessionRequest{
		Cwd: "/tmp", McpServers: []acp.McpServer{},
	})
	var requestErr *acp.RequestError
	if !errors.As(err, &requestErr) || requestErr.Code != acp.NewAuthRequired(nil).Code {
		t.Fatalf("NewSession() error = %v，期望 AuthRequired", err)
	}
	if got := rpc.calls; len(got) != 2 || got[1] != protocol.MethodAccountRead {
		t.Fatalf("鉴权失败调用顺序为 %v", got)
	}
}

// TestAgentResumeCloseGenerationFenceRejectsLateOpen 验证 close 后迟到 resume 不会重新安装 session。
func TestAgentResumeCloseGenerationFenceRejectsLateOpen(t *testing.T) {
	t.Parallel()
	rpc := newFakeAppServerRPC()
	resumeCalled := make(chan struct{})
	releaseResume := make(chan struct{})
	var resumeOnce sync.Once
	rpc.handleCall = func(_ context.Context, request protocol.ClientRequest, result any) error {
		switch request.Method() {
		case protocol.MethodInitialize:
			return nil
		case protocol.MethodThreadResume:
			resumeOnce.Do(func() { close(resumeCalled) })
			<-releaseResume
			result.(*protocol.ThreadResumeResponse).Thread.ID = "thread-race"
			return nil
		case protocol.MethodThreadUnsubscribe:
			return nil
		default:
			return errors.New("unexpected call: " + request.Method())
		}
	}
	agent := newRuntimeTestAgent(t, rpc)

	resumeResult := make(chan error, 1)
	go func() {
		_, err := agent.ResumeSession(context.Background(), acp.ResumeSessionRequest{
			SessionId: "thread-race", Cwd: "/tmp",
		})
		resumeResult <- err
	}()
	<-resumeCalled
	if _, err := agent.CloseSession(context.Background(), acp.CloseSessionRequest{SessionId: "thread-race"}); err != nil {
		t.Fatalf("关闭 session 失败: %v", err)
	}
	close(releaseResume)
	if err := <-resumeResult; !errors.Is(err, ErrSessionClosing) {
		t.Fatalf("迟到 resume 错误为 %v", err)
	}
	if _, ok := agent.sessions.get("thread-race"); ok {
		t.Fatal("迟到 resume 覆盖了已关闭 session")
	}
}

// TestAgentCancelBeforeTurnStartInterruptsLateTurnOnce 验证取消立即释放前台槽位，迟到 turn 仍被 stale+interrupt 一次。
func TestAgentCancelBeforeTurnStartInterruptsLateTurnOnce(t *testing.T) {
	t.Parallel()
	rpc := newFakeAppServerRPC()
	turnStartCalled := make(chan struct{})
	releaseTurnStart := make(chan struct{})
	secondTurnStarted := make(chan struct{})
	interruptCalled := make(chan struct{}, 2)
	var turnMu sync.Mutex
	turnNumber := 0
	rpc.handleCall = func(_ context.Context, request protocol.ClientRequest, result any) error {
		switch request.Method() {
		case protocol.MethodInitialize:
			return nil
		case protocol.MethodThreadStart:
			response := result.(*protocol.ThreadStartResponse)
			response.Thread.ID = "thread-1"
			response.Model = "fast-model"
			return nil
		case protocol.MethodModelList:
			result.(*protocol.ModelListResponse).Data = testModels()
			return nil
		case protocol.MethodTurnStart:
			turnMu.Lock()
			turnNumber++
			currentTurn := turnNumber
			turnMu.Unlock()
			if currentTurn == 1 {
				close(turnStartCalled)
				<-releaseTurnStart
				result.(*protocol.TurnStartResponse).Turn = protocol.TurnElement{
					ID: "late-turn", Items: []protocol.ThreadItem{}, Status: protocol.PurpleInProgress,
				}
				return nil
			}
			result.(*protocol.TurnStartResponse).Turn = protocol.TurnElement{
				ID: "second-turn", Items: []protocol.ThreadItem{}, Status: protocol.PurpleInProgress,
			}
			close(secondTurnStarted)
			return nil
		case protocol.MethodTurnInterrupt:
			interruptCalled <- struct{}{}
			return nil
		default:
			return errors.New("unexpected call: " + request.Method())
		}
	}
	agent := newRuntimeTestAgent(t, rpc)
	if _, err := agent.NewSession(context.Background(), acp.NewSessionRequest{Cwd: "/tmp", McpServers: []acp.McpServer{}}); err != nil {
		t.Fatalf("创建 session 失败: %v", err)
	}

	promptCtx, cancelPrompt := context.WithCancel(context.Background())
	promptResult := make(chan acp.PromptResponse, 1)
	promptErr := make(chan error, 1)
	go func() {
		response, err := agent.Prompt(promptCtx, acp.PromptRequest{
			SessionId: "thread-1",
			Prompt:    []acp.ContentBlock{{Text: &acp.ContentBlockText{Type: "text", Text: "hello"}}},
		})
		promptResult <- response
		promptErr <- err
	}()
	<-turnStartCalled
	state, ok := agent.sessions.get("thread-1")
	if !ok {
		t.Fatal("取消前 session 状态不存在")
	}
	backgroundPrompt := sessionActiveTurn(state)
	if backgroundPrompt == nil {
		t.Fatal("turn/start pending 时应存在活动 prompt")
	}
	defer func() {
		select {
		case <-releaseTurnStart:
		default:
			close(releaseTurnStart)
		}
	}()

	cancelPrompt()
	if err := <-promptErr; err != nil {
		t.Fatalf("取消 prompt 返回错误: %v", err)
	}
	if response := <-promptResult; response.StopReason != acp.StopReasonCancelled {
		t.Fatalf("取消 prompt 响应为 %#v", response)
	}
	compactCtx, stopCompact := context.WithTimeout(context.Background(), time.Second)
	defer stopCompact()
	_, err := agent.HandleExtensionMethod(compactCtx, acpmeta.CompactMethod, json.RawMessage(`{"session_id":"thread-1"}`))
	if !errors.Is(err, ErrPromptActive) {
		t.Fatalf("compact overlapped a prior unresolved turn/start: %v", err)
	}

	select {
	case <-interruptCalled:
		t.Fatal("turn/start 返回前不应发送 interrupt")
	default:
	}
	if active := sessionActiveTurn(state); active != nil {
		t.Fatal("取消 turn/start pending prompt 后未立即释放前台活动槽位")
	}

	secondResponse := make(chan acp.PromptResponse, 1)
	secondErr := make(chan error, 1)
	go func() {
		response, err := agent.Prompt(context.Background(), acp.PromptRequest{
			SessionId: "thread-1",
			Prompt:    []acp.ContentBlock{{Text: &acp.ContentBlockText{Type: "text", Text: "second"}}},
		})
		secondResponse <- response
		secondErr <- err
	}()
	select {
	case <-secondTurnStarted:
	case err := <-secondErr:
		t.Fatalf("取消后的后续 prompt 未启动: %v", err)
	case <-time.After(time.Second):
		t.Fatal("取消后的后续 prompt 未进入 turn/start")
	}
	agent.client.HandleNotification(context.Background(), completeNotification(
		t, "thread-1", "second-turn", protocol.FluffyCompleted,
	))
	if err := <-secondErr; err != nil {
		t.Fatalf("取消后的后续 prompt 返回错误: %v", err)
	}
	if response := <-secondResponse; response.StopReason != acp.StopReasonEndTurn {
		t.Fatalf("取消后的后续 prompt 响应为 %#v", response)
	}

	close(releaseTurnStart)
	<-interruptCalled
	select {
	case <-backgroundPrompt.backgroundDone:
	case <-time.After(time.Second):
		t.Fatal("迟到 turn interrupt 后未合成 completion 释放后台 prompt")
	}
	if err := agent.Cancel(context.Background(), acp.CancelNotification{SessionId: "thread-1"}); err != nil {
		t.Fatalf("重复 cancel 返回错误: %v", err)
	}
	select {
	case <-interruptCalled:
		t.Fatal("重复 cancel 不应再次发送 interrupt")
	default:
	}
}

// TestAgentCloseSessionCancelsPendingTurnStartAndReapsBackground 验证 close 会取消无 turn ID 的请求并回收后台 goroutine。
func TestAgentCloseSessionCancelsPendingTurnStartAndReapsBackground(t *testing.T) {
	t.Parallel()
	rpc := newFakeAppServerRPC()
	turnStartCalled := make(chan struct{})
	turnStartReturned := make(chan struct{})
	var startOnce sync.Once
	var returnOnce sync.Once
	rpc.handleCall = func(ctx context.Context, request protocol.ClientRequest, result any) error {
		switch request.Method() {
		case protocol.MethodInitialize:
			return nil
		case protocol.MethodThreadStart:
			result.(*protocol.ThreadStartResponse).Thread.ID = "thread-close"
			return nil
		case protocol.MethodTurnStart:
			startOnce.Do(func() { close(turnStartCalled) })
			<-ctx.Done()
			returnOnce.Do(func() { close(turnStartReturned) })
			return ctx.Err()
		case protocol.MethodThreadUnsubscribe:
			return nil
		default:
			return errors.New("unexpected call: " + request.Method())
		}
	}
	agent := newRuntimeTestAgent(t, rpc)
	if _, err := agent.NewSession(context.Background(), acp.NewSessionRequest{Cwd: "/tmp", McpServers: []acp.McpServer{}}); err != nil {
		t.Fatalf("创建 session 失败: %v", err)
	}

	promptResult := make(chan acp.PromptResponse, 1)
	promptErr := make(chan error, 1)
	go func() {
		response, err := agent.Prompt(context.Background(), acp.PromptRequest{
			SessionId: "thread-close",
			Prompt:    []acp.ContentBlock{{Text: &acp.ContentBlockText{Type: "text", Text: "pending"}}},
		})
		promptResult <- response
		promptErr <- err
	}()
	<-turnStartCalled
	state, ok := agent.sessions.get("thread-close")
	if !ok {
		t.Fatal("关闭前 session 状态不存在")
	}
	backgroundPrompt := sessionActiveTurn(state)
	if backgroundPrompt == nil {
		t.Fatal("关闭前 pending prompt 不存在")
	}

	closeResult := make(chan error, 1)
	go func() {
		_, err := agent.CloseSession(context.Background(), acp.CloseSessionRequest{SessionId: "thread-close"})
		closeResult <- err
	}()
	select {
	case err := <-closeResult:
		if err != nil {
			t.Fatalf("关闭 pending session 失败: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("关闭 pending session 被 turn/start 阻塞")
	}
	select {
	case <-turnStartReturned:
	default:
		t.Fatal("CloseSession 返回时 pending turn/start RPC 尚未取消")
	}
	select {
	case <-backgroundPrompt.backgroundDone:
	default:
		t.Fatal("CloseSession 返回时 pending prompt 后台 goroutine 尚未回收")
	}
	if err := <-promptErr; err != nil {
		t.Fatalf("session close 后 Prompt 返回错误: %v", err)
	}
	if response := <-promptResult; response.StopReason != acp.StopReasonCancelled {
		t.Fatalf("session close 后 Prompt 响应为 %#v", response)
	}
}

// TestAgentCloseSessionObservesLateTurnStartOverRealTransport 验证 close/prompt 先返回后，
// 迟到的 turn/start 仍被标记为 stale 并且只 interrupt 一次。
func TestAgentCloseSessionObservesLateTurnStartOverRealTransport(t *testing.T) {
	harness := newTransportHarness(t, 4096, nil, nil)
	runtimeCtx, cancelRuntime := context.WithCancel(context.Background())
	t.Cleanup(cancelRuntime)
	client := newAppServerClient(runtimeCtx, harness.transport)
	agent := newAgentWithClient(
		slog.New(slog.NewTextHandler(io.Discard, nil)), runtimeCtx, cancelRuntime, client,
	)
	agent.transport = harness.transport
	agent.markConnectionReady()
	agent.initializeMu.Lock()
	agent.initialized = true
	agent.initializeMu.Unlock()
	generation, err := agent.sessions.beginOpen("thread-wire-close")
	if err != nil {
		t.Fatalf("建立 wire session open 身份失败: %v", err)
	}
	state, installed := agent.sessions.install(
		"thread-wire-close",
		"/tmp",
		generation,
		nil,
		terminalOutputModeDelta,
	)
	if !installed {
		t.Fatal("安装 wire session 状态失败")
	}

	promptResult := make(chan acp.PromptResponse, 1)
	promptErr := make(chan error, 1)
	go func() {
		response, promptRunErr := agent.Prompt(context.Background(), acp.PromptRequest{
			SessionId: "thread-wire-close",
			Prompt:    []acp.ContentBlock{{Text: &acp.ContentBlockText{Type: "text", Text: "late start"}}},
		})
		promptResult <- response
		promptErr <- promptRunErr
	}()
	turnStart := readRuntimeWireRequest(t, harness)
	if turnStart.Method != protocol.MethodTurnStart {
		t.Fatalf("首个 wire 请求为 %q，期望 turn/start", turnStart.Method)
	}
	prompt := sessionActiveTurn(state)
	if prompt == nil {
		t.Fatal("turn/start pending 时没有活动 prompt")
	}

	closeResult := make(chan error, 1)
	go func() {
		_, closeErr := agent.CloseSession(context.Background(), acp.CloseSessionRequest{SessionId: "thread-wire-close"})
		closeResult <- closeErr
	}()
	unsubscribe := readRuntimeWireRequest(t, harness)
	if unsubscribe.Method != protocol.MethodThreadUnsubscribe {
		t.Fatalf("close wire 请求为 %q，期望 thread/unsubscribe", unsubscribe.Method)
	}
	writeRuntimeWireResult(t, harness, unsubscribe.ID, json.RawMessage(`{"status":"unsubscribed"}`))
	if err = <-closeResult; err != nil {
		t.Fatalf("迟到 turn/start 前 CloseSession 返回错误: %v", err)
	}
	if err = <-promptErr; err != nil {
		t.Fatalf("迟到 turn/start 前 Prompt 返回错误: %v", err)
	}
	if response := <-promptResult; response.StopReason != acp.StopReasonCancelled {
		t.Fatalf("session close 后 Prompt 响应为 %#v", response)
	}

	writeRuntimeWireResult(t, harness, turnStart.ID, json.RawMessage(
		`{"turn":{"id":"turn-wire-late","items":[],"status":"inProgress"}}`,
	))
	sentinelResult := make(chan error, 1)
	go func() {
		_, readErr := client.ThreadRead(context.Background(), protocol.ThreadReadParams{ThreadID: "thread-wire-close"})
		sentinelResult <- readErr
	}()
	interruptCount := 0
	for {
		request := readRuntimeWireRequest(t, harness)
		switch request.Method {
		case protocol.MethodTurnInterrupt:
			interruptCount++
			var params protocol.TurnInterruptParams
			if err = json.Unmarshal(request.Params, &params); err != nil {
				t.Fatalf("解码迟到 interrupt 参数失败: %v", err)
			}
			if params.ThreadID != "thread-wire-close" || params.TurnID != "turn-wire-late" {
				t.Fatalf("迟到 interrupt identity 为 %#v", params)
			}
			client.handlerMu.Lock()
			_, markedStale := client.staleTurns[params.ThreadID][params.TurnID]
			client.handlerMu.Unlock()
			if !markedStale {
				t.Fatal("迟到 turn/start 未在 interrupt 前标记 stale")
			}
			writeRuntimeWireResult(t, harness, request.ID, json.RawMessage(`{}`))
		case protocol.MethodThreadRead:
			writeRuntimeWireResult(t, harness, request.ID, json.RawMessage(
				`{"thread":{"id":"thread-wire-close","turns":[]}}`,
			))
			goto sentinelWritten
		default:
			t.Fatalf("迟到 response 后出现意外 wire 请求 %q", request.Method)
		}
	}

sentinelWritten:
	if err = <-sentinelResult; err != nil {
		t.Fatalf("readLoop sentinel 返回错误: %v", err)
	}
	turnID, _ := prompt.currentTurn()
	if turnID != "turn-wire-late" {
		t.Fatalf("迟到 turn/start observer 未安装 identity，当前 turn=%q", turnID)
	}
	if interruptCount == 0 {
		request := readRuntimeWireRequest(t, harness)
		if request.Method != protocol.MethodTurnInterrupt {
			t.Fatalf("sentinel 后请求为 %q，期望 turn/interrupt", request.Method)
		}
		interruptCount++
		writeRuntimeWireResult(t, harness, request.ID, json.RawMessage(`{}`))
	}
	<-prompt.interruptDone
	if interruptCount != 1 {
		t.Fatalf("迟到 turn/start interrupt 次数为 %d", interruptCount)
	}
}

// TestAgentCloseSessionReapsReleasedPendingObserver 验证普通取消已释放槽位后，session close 仍会回收迟到观察者。
func TestAgentCloseSessionReapsReleasedPendingObserver(t *testing.T) {
	t.Parallel()
	rpc := newFakeAppServerRPC()
	turnStartCalled := make(chan struct{})
	turnStartReturned := make(chan struct{})
	rpc.handleCall = func(ctx context.Context, request protocol.ClientRequest, result any) error {
		switch request.Method() {
		case protocol.MethodInitialize:
			return nil
		case protocol.MethodThreadStart:
			result.(*protocol.ThreadStartResponse).Thread.ID = "thread-orphan"
			return nil
		case protocol.MethodTurnStart:
			close(turnStartCalled)
			<-ctx.Done()
			close(turnStartReturned)
			return ctx.Err()
		case protocol.MethodThreadUnsubscribe:
			return nil
		default:
			return errors.New("unexpected call: " + request.Method())
		}
	}
	agent := newRuntimeTestAgent(t, rpc)
	if _, err := agent.NewSession(context.Background(), acp.NewSessionRequest{Cwd: "/tmp", McpServers: []acp.McpServer{}}); err != nil {
		t.Fatalf("创建 session 失败: %v", err)
	}
	promptCtx, cancelPrompt := context.WithCancel(context.Background())
	promptErr := make(chan error, 1)
	go func() {
		_, err := agent.Prompt(promptCtx, acp.PromptRequest{
			SessionId: "thread-orphan",
			Prompt:    []acp.ContentBlock{{Text: &acp.ContentBlockText{Type: "text", Text: "cancel"}}},
		})
		promptErr <- err
	}()
	<-turnStartCalled
	cancelPrompt()
	if err := <-promptErr; err != nil {
		t.Fatalf("取消 pending prompt 失败: %v", err)
	}
	state, ok := agent.sessions.get("thread-orphan")
	if !ok || sessionActiveTurn(state) != nil {
		t.Fatal("普通取消后前台活动槽位未释放")
	}

	if _, err := agent.CloseSession(context.Background(), acp.CloseSessionRequest{SessionId: "thread-orphan"}); err != nil {
		t.Fatalf("关闭含迟到观察者的 session 失败: %v", err)
	}
	select {
	case <-turnStartReturned:
	default:
		t.Fatal("CloseSession 未回收已释放前台槽位的 pending 观察者")
	}
}

// TestAgentCloseWaitsForReleasedPendingObserver 验证 Adapter Close 在返回前等待普通取消留下的观察 goroutine。
func TestAgentCloseWaitsForReleasedPendingObserver(t *testing.T) {
	t.Parallel()
	rpc := newFakeAppServerRPC()
	turnStartCalled := make(chan struct{})
	runCanceled := make(chan struct{})
	releaseRun := make(chan struct{})
	defer func() {
		select {
		case <-releaseRun:
		default:
			close(releaseRun)
		}
	}()
	rpc.handleCall = func(ctx context.Context, request protocol.ClientRequest, result any) error {
		switch request.Method() {
		case protocol.MethodInitialize:
			return nil
		case protocol.MethodThreadStart:
			result.(*protocol.ThreadStartResponse).Thread.ID = "thread-adapter-close"
			return nil
		case protocol.MethodTurnStart:
			close(turnStartCalled)
			<-ctx.Done()
			close(runCanceled)
			<-releaseRun
			return ctx.Err()
		default:
			return errors.New("unexpected call: " + request.Method())
		}
	}
	agent := newRuntimeTestAgent(t, rpc)
	if _, err := agent.NewSession(context.Background(), acp.NewSessionRequest{Cwd: "/tmp", McpServers: []acp.McpServer{}}); err != nil {
		t.Fatalf("创建 session 失败: %v", err)
	}
	promptCtx, cancelPrompt := context.WithCancel(context.Background())
	promptErr := make(chan error, 1)
	go func() {
		_, err := agent.Prompt(promptCtx, acp.PromptRequest{
			SessionId: "thread-adapter-close",
			Prompt:    []acp.ContentBlock{{Text: &acp.ContentBlockText{Type: "text", Text: "cancel"}}},
		})
		promptErr <- err
	}()
	<-turnStartCalled
	cancelPrompt()
	if err := <-promptErr; err != nil {
		t.Fatalf("取消 pending prompt 失败: %v", err)
	}

	closeResult := make(chan error, 1)
	go func() { closeResult <- agent.Close(context.Background()) }()
	<-runCanceled
	select {
	case err := <-closeResult:
		t.Fatalf("Adapter Close 在观察 goroutine 退出前返回: %v", err)
	default:
	}
	close(releaseRun)
	if err := <-closeResult; err != nil {
		t.Fatalf("Adapter Close 返回错误: %v", err)
	}
}

// TestAgentPromptSupportsMultipleTurnsAndIgnoresOldCompletion 验证同 session 多轮各由自身 completion 结束。
func TestAgentPromptSupportsMultipleTurnsAndIgnoresOldCompletion(t *testing.T) {
	t.Parallel()
	rpc := newFakeAppServerRPC()
	turnNumber := 0
	turnStarted := make(chan string, 2)
	rpc.handleCall = func(_ context.Context, request protocol.ClientRequest, result any) error {
		switch request.Method() {
		case protocol.MethodInitialize:
			return nil
		case protocol.MethodThreadStart:
			result.(*protocol.ThreadStartResponse).Thread.ID = "thread-1"
			return nil
		case protocol.MethodTurnStart:
			turnNumber++
			turnID := "turn-1"
			if turnNumber == 2 {
				turnID = "turn-2"
			}
			result.(*protocol.TurnStartResponse).Turn = protocol.TurnElement{
				ID: turnID, Items: []protocol.ThreadItem{}, Status: protocol.PurpleInProgress,
			}
			turnStarted <- turnID
			return nil
		default:
			return errors.New("unexpected call: " + request.Method())
		}
	}
	agent := newRuntimeTestAgent(t, rpc)
	if _, err := agent.NewSession(context.Background(), acp.NewSessionRequest{Cwd: "/tmp", McpServers: []acp.McpServer{}}); err != nil {
		t.Fatalf("创建 session 失败: %v", err)
	}

	for index := 1; index <= 2; index++ {
		responseResult := make(chan acp.PromptResponse, 1)
		errResult := make(chan error, 1)
		go func() {
			response, err := agent.Prompt(context.Background(), acp.PromptRequest{
				SessionId: "thread-1",
				Prompt:    []acp.ContentBlock{{Text: &acp.ContentBlockText{Type: "text", Text: "next"}}},
			})
			responseResult <- response
			errResult <- err
		}()
		turnID := <-turnStarted
		if index == 2 {
			agent.client.HandleNotification(context.Background(), completeNotification(t, "thread-1", "turn-1", protocol.FluffyCompleted))
			select {
			case <-responseResult:
				t.Fatal("旧 turn completion 错误结束第二轮")
			default:
			}
		}
		agent.client.HandleNotification(context.Background(), completeNotification(t, "thread-1", turnID, protocol.FluffyCompleted))
		if err := <-errResult; err != nil {
			t.Fatalf("第 %d 轮 prompt 错误: %v", index, err)
		}
		if response := <-responseResult; response.StopReason != acp.StopReasonEndTurn {
			t.Fatalf("第 %d 轮响应为 %#v", index, response)
		}
	}
}

// TestAgentPromptReturnsFailedTurnError 验证失败 turn 的 app-server 原始错误进入 ACP 响应。
func TestAgentPromptReturnsFailedTurnError(t *testing.T) {
	t.Parallel()
	rpc := newFakeAppServerRPC()
	turnStarted := make(chan struct{})
	rpc.handleCall = func(_ context.Context, request protocol.ClientRequest, result any) error {
		switch request.Method() {
		case protocol.MethodInitialize:
			return nil
		case protocol.MethodThreadStart:
			result.(*protocol.ThreadStartResponse).Thread.ID = "thread-1"
			return nil
		case protocol.MethodTurnStart:
			result.(*protocol.TurnStartResponse).Turn = protocol.TurnElement{
				ID: "turn-1", Items: []protocol.ThreadItem{}, Status: protocol.PurpleInProgress,
			}
			close(turnStarted)
			return nil
		default:
			return errors.New("unexpected call: " + request.Method())
		}
	}
	agent := newRuntimeTestAgent(t, rpc)
	if _, err := agent.NewSession(context.Background(), acp.NewSessionRequest{Cwd: "/tmp", McpServers: []acp.McpServer{}}); err != nil {
		t.Fatalf("创建 session 失败: %v", err)
	}
	promptError := make(chan error, 1)
	go func() {
		_, err := agent.Prompt(context.Background(), acp.PromptRequest{
			SessionId: "thread-1",
			Prompt:    []acp.ContentBlock{{Text: &acp.ContentBlockText{Type: "text", Text: "hello"}}},
		})
		promptError <- err
	}()
	<-turnStarted
	notification := completeNotification(t, "thread-1", "turn-1", protocol.Failed).(*protocol.TurnCompletedEnvelope)
	kind := protocol.Unauthorized
	notification.Params.Turn.Error = &protocol.Error{Message: "API Error: 402 Insufficient Balance", CodexErrorInfo: &protocol.CodexErrorInfoUnion{Enum: &kind}}
	agent.client.HandleNotification(context.Background(), notification)
	var requestError *acp.RequestError
	if err := <-promptError; !errors.As(err, &requestError) || requestError.Message != "API Error: 402 Insufficient Balance" {
		t.Fatalf("Prompt() error = %v，期望 app-server 原始错误", err)
	}
	data, _ := requestError.Data.(map[string]any)
	if data["errorKind"] != "authentication_failed" {
		t.Fatalf("结构化类别丢失：%+v", requestError)
	}
}

// TestAgentPromptWaitsForConnectionBinder 验证 SDK connection 注入前不会启动可能产生事件的 turn。
func TestAgentPromptWaitsForConnectionBinder(t *testing.T) {
	t.Parallel()
	rpc := newFakeAppServerRPC()
	turnStartCalled := make(chan struct{})
	rpc.handleCall = func(_ context.Context, request protocol.ClientRequest, result any) error {
		switch request.Method() {
		case protocol.MethodInitialize:
			return nil
		case protocol.MethodThreadStart:
			response := result.(*protocol.ThreadStartResponse)
			response.Thread.ID = "thread-1"
			response.Model = "fast-model"
			return nil
		case protocol.MethodModelList:
			result.(*protocol.ModelListResponse).Data = testModels()
			return nil
		case protocol.MethodTurnStart:
			result.(*protocol.TurnStartResponse).Turn = protocol.TurnElement{
				ID: "turn-1", Items: []protocol.ThreadItem{}, Status: protocol.PurpleInProgress,
			}
			close(turnStartCalled)
			return nil
		default:
			return errors.New("unexpected call: " + request.Method())
		}
	}
	runtimeCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	agent := newAgentWithClient(
		slog.New(slog.NewTextHandler(io.Discard, nil)), runtimeCtx, cancel, newAppServerClient(runtimeCtx, rpc),
	)
	if _, err := agent.Initialize(context.Background(), acp.InitializeRequest{ProtocolVersion: acp.ProtocolVersionNumber}); err != nil {
		t.Fatalf("初始化 Agent 失败: %v", err)
	}
	if _, err := agent.NewSession(context.Background(), acp.NewSessionRequest{Cwd: "/tmp", McpServers: []acp.McpServer{}}); err != nil {
		t.Fatalf("创建 session 失败: %v", err)
	}
	promptResult := make(chan error, 1)
	go func() {
		_, err := agent.Prompt(context.Background(), acp.PromptRequest{
			SessionId: "thread-1",
			Prompt:    []acp.ContentBlock{{Text: &acp.ContentBlockText{Type: "text", Text: "hello"}}},
		})
		promptResult <- err
	}()
	select {
	case <-turnStartCalled:
		t.Fatal("connection binder 前启动了 turn")
	default:
	}
	agent.markConnectionReady()
	<-turnStartCalled
	agent.client.HandleNotification(context.Background(), completeNotification(
		t, "thread-1", "turn-1", protocol.FluffyCompleted,
	))
	if err := <-promptResult; err != nil {
		t.Fatalf("binder 后 prompt 错误: %v", err)
	}
}

// TestAgentCancelLetsCompletedTurnWinOverInterrupt 验证取消竞态中 completed completion 仍返回 end_turn。
func TestAgentCancelLetsCompletedTurnWinOverInterrupt(t *testing.T) {
	t.Parallel()
	rpc := newFakeAppServerRPC()
	turnStarted := make(chan struct{})
	interruptCalled := make(chan struct{})
	releaseInterrupt := make(chan struct{})
	rpc.handleCall = func(_ context.Context, request protocol.ClientRequest, result any) error {
		switch request.Method() {
		case protocol.MethodInitialize:
			return nil
		case protocol.MethodThreadStart:
			result.(*protocol.ThreadStartResponse).Thread.ID = "thread-1"
			return nil
		case protocol.MethodTurnStart:
			result.(*protocol.TurnStartResponse).Turn = protocol.TurnElement{
				ID: "turn-1", Items: []protocol.ThreadItem{}, Status: protocol.PurpleInProgress,
			}
			close(turnStarted)
			return nil
		case protocol.MethodTurnInterrupt:
			close(interruptCalled)
			<-releaseInterrupt
			return nil
		default:
			return errors.New("unexpected call: " + request.Method())
		}
	}
	agent := newRuntimeTestAgent(t, rpc)
	if _, err := agent.NewSession(context.Background(), acp.NewSessionRequest{Cwd: "/tmp", McpServers: []acp.McpServer{}}); err != nil {
		t.Fatalf("创建 session 失败: %v", err)
	}
	responseResult := make(chan acp.PromptResponse, 1)
	errResult := make(chan error, 1)
	go func() {
		response, err := agent.Prompt(context.Background(), acp.PromptRequest{
			SessionId: "thread-1",
			Prompt:    []acp.ContentBlock{{Text: &acp.ContentBlockText{Type: "text", Text: "finish"}}},
		})
		responseResult <- response
		errResult <- err
	}()
	<-turnStarted
	if err := agent.Cancel(context.Background(), acp.CancelNotification{SessionId: "thread-1"}); err != nil {
		t.Fatalf("取消 prompt 失败: %v", err)
	}
	<-interruptCalled
	agent.client.HandleNotification(context.Background(), completeNotification(
		t, "thread-1", "turn-1", protocol.FluffyCompleted,
	))
	if err := <-errResult; err != nil {
		t.Fatalf("completion-first 返回错误: %v", err)
	}
	if response := <-responseResult; response.StopReason != acp.StopReasonEndTurn {
		t.Fatalf("completion-first 响应为 %#v", response)
	}
	close(releaseInterrupt)
}

// TestAgentNotificationUsesVerifiedEventRouter 验证 runtime 只把当前 generation 的 typed 通知交给 sibling event mapper。
func TestAgentNotificationUsesVerifiedEventRouter(t *testing.T) {
	t.Parallel()
	rpc := newFakeAppServerRPC()
	turnStarted := make(chan struct{})
	rpc.handleCall = func(_ context.Context, request protocol.ClientRequest, result any) error {
		switch request.Method() {
		case protocol.MethodInitialize:
			return nil
		case protocol.MethodThreadStart:
			result.(*protocol.ThreadStartResponse).Thread.ID = "thread-events"
			return nil
		case protocol.MethodTurnStart:
			result.(*protocol.TurnStartResponse).Turn = protocol.TurnElement{
				ID: "turn-events", Items: []protocol.ThreadItem{}, Status: protocol.PurpleInProgress,
			}
			close(turnStarted)
			return nil
		default:
			return errors.New("unexpected call: " + request.Method())
		}
	}
	agent := newRuntimeTestAgent(t, rpc)
	if _, err := agent.NewSession(context.Background(), acp.NewSessionRequest{Cwd: "/tmp", McpServers: []acp.McpServer{}}); err != nil {
		t.Fatalf("创建 session 失败: %v", err)
	}
	promptResponse := make(chan acp.PromptResponse, 1)
	promptErr := make(chan error, 1)
	go func() {
		response, err := agent.Prompt(context.Background(), acp.PromptRequest{
			SessionId: "thread-events",
			Prompt:    []acp.ContentBlock{{Text: &acp.ContentBlockText{Type: "text", Text: "events"}}},
		})
		promptResponse <- response
		promptErr <- err
	}()
	<-turnStarted
	state, ok := agent.sessions.get("thread-events")
	if !ok {
		t.Fatal("事件测试 session 不存在")
	}
	prompt := sessionActiveTurn(state)
	if prompt == nil {
		t.Fatal("事件测试活动 Prompt 不存在")
	}
	select {
	case <-prompt.turnStarted:
	case <-time.After(time.Second):
		t.Fatal("等待事件测试 Turn 身份安装超时")
	}
	generation, ok := agent.currentTurnGeneration("thread-events", "turn-events")
	if !ok {
		t.Fatal("事件测试当前 Turn generation 不存在")
	}
	updater := &recordingSessionUpdater{}
	prompt.setEventRouter(newEventRouter(
		updater,
		generation,
		agent,
		agent.logger,
		state.terminalOutputMode,
	))

	current, err := protocol.DecodeServerNotification([]byte(
		`{"method":"item/agentMessage/delta","params":{"threadId":"thread-events","turnId":"turn-events","itemId":"message-1","delta":"hello"}}`,
	))
	if err != nil {
		t.Fatalf("解码当前事件失败: %v", err)
	}
	agent.client.HandleNotification(context.Background(), current)
	if len(updater.notifications) != 1 {
		t.Fatalf("当前事件 update 数为 %d", len(updater.notifications))
	}
	message := updater.notifications[0].Update.AgentMessageChunk
	if message == nil || message.MessageId == nil || *message.MessageId != "message-1" {
		t.Fatalf("runtime 未复用 event mapper 的 message identity: %#v", message)
	}
	stale, err := protocol.DecodeServerNotification([]byte(
		`{"method":"item/agentMessage/delta","params":{"threadId":"thread-events","turnId":"turn-old","itemId":"message-old","delta":"stale"}}`,
	))
	if err != nil {
		t.Fatalf("解码 stale 事件失败: %v", err)
	}
	agent.client.HandleNotification(context.Background(), stale)
	if len(updater.notifications) != 1 {
		t.Fatalf("stale 事件污染当前 generation: %#v", updater.notifications)
	}

	agent.client.HandleNotification(context.Background(), completeNotification(
		t, "thread-events", "turn-events", protocol.FluffyCompleted,
	))
	if err = <-promptErr; err != nil {
		t.Fatalf("事件测试 prompt 返回错误: %v", err)
	}
	if response := <-promptResponse; response.StopReason != acp.StopReasonEndTurn {
		t.Fatalf("事件测试 prompt 响应为 %#v", response)
	}
}

// TestAgentSnapshotsAdvertisedTerminalOutputModeIntoLiveEvents 验证 session 保存 initialize 时的输出模式。
// 若后续 initialize 覆盖既有 session，或 live delta/interaction/completion 仍硬编码 legacy key，本测试应失败。
func TestAgentSnapshotsAdvertisedTerminalOutputModeIntoLiveEvents(t *testing.T) {
	t.Parallel()

	rpc := newFakeAppServerRPC()
	rpc.handleCall = func(_ context.Context, request protocol.ClientRequest, result any) error {
		switch request.Method() {
		case protocol.MethodInitialize:
			return nil
		case protocol.MethodThreadStart:
			response := result.(*protocol.ThreadStartResponse)
			response.Thread.ID = "terminal-mode-thread"
			response.Model = "fast-model"
			return nil
		case protocol.MethodModelList:
			result.(*protocol.ModelListResponse).Data = testModels()
			return nil
		default:
			return errors.New("unexpected call: " + request.Method())
		}
	}
	runtimeCtx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	agent := newAgentWithClient(
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		runtimeCtx,
		cancel,
		newAppServerClient(runtimeCtx, rpc),
	)
	updater := &recordingSessionUpdater{}
	agent.connectionMu.Lock()
	agent.sessionUpdater = updater
	agent.connectionMu.Unlock()

	if _, err := agent.Initialize(context.Background(), acp.InitializeRequest{
		ProtocolVersion: acp.ProtocolVersionNumber,
		ClientCapabilities: acp.ClientCapabilities{Meta: map[string]any{
			"terminal_output": true,
		}},
	}); err != nil {
		t.Fatalf("初始化 terminal_output 客户端失败: %v", err)
	}
	created, err := agent.NewSession(context.Background(), acp.NewSessionRequest{
		Cwd: "/workspace", McpServers: []acp.McpServer{},
	})
	if err != nil {
		t.Fatalf("创建 terminal_output session 失败: %v", err)
	}
	// Session 安装时保存能力快照；后续 initialize 只影响新 Session。
	if _, err = agent.Initialize(context.Background(), acp.InitializeRequest{
		ProtocolVersion: acp.ProtocolVersionNumber,
	}); err != nil {
		t.Fatalf("重新初始化默认客户端失败: %v", err)
	}
	state, ok := agent.sessions.get(string(created.SessionId))
	if !ok {
		t.Fatal("terminal_output session 未安装")
	}
	prompt := newNativeTurn(agent.runtimeCtx, agent.nextTurnGeneration.Add(1))
	t.Cleanup(prompt.cancelRun)
	if err = agent.installTurn(state, prompt); err != nil {
		t.Fatalf("安装 terminal_output prompt 失败: %v", err)
	}
	agent.onTurnStarted(state, prompt, "terminal-mode-turn")
	router := prompt.currentEventRouter()
	if router == nil {
		t.Fatal("terminal_output event router 未安装")
	}

	for _, raw := range []string{
		`{"method":"item/started","params":{"threadId":"terminal-mode-thread","turnId":"terminal-mode-turn","startedAtMs":0,"item":{"type":"commandExecution","id":"command-modern","command":"echo live","cwd":"/workspace","status":"inProgress","commandActions":[]}}}`,
		`{"method":"item/commandExecution/outputDelta","params":{"threadId":"terminal-mode-thread","turnId":"terminal-mode-turn","itemId":"command-modern","delta":"live\n"}}`,
		`{"method":"item/commandExecution/terminalInteraction","params":{"threadId":"terminal-mode-thread","turnId":"terminal-mode-turn","itemId":"command-modern","processId":"process-modern","stdin":"yes"}}`,
		`{"method":"item/completed","params":{"threadId":"terminal-mode-thread","turnId":"terminal-mode-turn","completedAtMs":1,"item":{"type":"commandExecution","id":"command-modern","command":"echo live","cwd":"/workspace","status":"completed","commandActions":[],"aggregatedOutput":"live\n","exitCode":0}}}`,
		`{"method":"item/started","params":{"threadId":"terminal-mode-thread","turnId":"terminal-mode-turn","startedAtMs":2,"item":{"type":"commandExecution","id":"command-modern-fallback","command":"echo fallback","cwd":"/workspace","status":"inProgress","commandActions":[]}}}`,
		`{"method":"item/completed","params":{"threadId":"terminal-mode-thread","turnId":"terminal-mode-turn","completedAtMs":3,"item":{"type":"commandExecution","id":"command-modern-fallback","command":"echo fallback","cwd":"/workspace","status":"completed","commandActions":[],"aggregatedOutput":"fallback\n","exitCode":0}}}`,
		`{"method":"item/started","params":{"threadId":"terminal-mode-thread","turnId":"terminal-mode-turn","startedAtMs":4,"item":{"type":"commandExecution","id":"read-modern","command":"cat README.md","cwd":"/workspace","status":"inProgress","commandActions":[{"type":"read","command":"cat README.md","name":"cat","path":"/workspace/README.md"}]}}}`,
		`{"method":"item/commandExecution/outputDelta","params":{"threadId":"terminal-mode-thread","turnId":"terminal-mode-turn","itemId":"read-modern","delta":"parsed\n"}}`,
	} {
		if err = router.HandleJSON(context.Background(), []byte(raw)); err != nil {
			t.Fatalf("处理 terminal_output fixture 失败: %v", err)
		}
	}
	if err := prompt.waitDelivery(); err != nil {
		t.Fatalf("draining terminal updates: %v", err)
	}
	if got, want := len(updater.notifications), 8; got != want {
		t.Fatalf("terminal_output update 数 = %d，期望 %d", got, want)
	}
	assertMetaWire(t, updater.notifications[1].Update.ToolCallUpdate.Meta, `{"terminal_output":{"data":"live\n","terminal_id":"command-modern"}}`)
	assertMetaWire(t, updater.notifications[2].Update.ToolCallUpdate.Meta, `{"terminal_output":{"data":"\nyes\n","terminal_id":"command-modern"}}`)
	assertMetaWire(t, updater.notifications[5].Update.ToolCallUpdate.Meta, `{"terminal_exit":{"exit_code":0,"signal":null,"terminal_id":"command-modern-fallback"},"terminal_output":{"data":"fallback\n","terminal_id":"command-modern-fallback"}}`)
	assertMetaWire(t, updater.notifications[7].Update.ToolCallUpdate.Meta, `{"terminal_output_delta":{"data":"parsed\n","terminal_id":"read-modern"}}`)
}

// TestAgentApprovalFailsClosedWhenTurnGenerationChanges 验证 permission 回调期间完成旧 turn 后，旧审批不会授权新 turn。
func TestAgentApprovalFailsClosedWhenTurnGenerationChanges(t *testing.T) {
	t.Parallel()
	rpc := newFakeAppServerRPC()
	turnStarted := make(chan string, 2)
	var turnMu sync.Mutex
	turnNumber := 0
	rpc.handleCall = func(_ context.Context, request protocol.ClientRequest, result any) error {
		switch request.Method() {
		case protocol.MethodInitialize:
			return nil
		case protocol.MethodThreadStart:
			result.(*protocol.ThreadStartResponse).Thread.ID = "thread-approval"
			return nil
		case protocol.MethodTurnStart:
			turnMu.Lock()
			turnNumber++
			turnID := fmt.Sprintf("turn-%d", turnNumber)
			turnMu.Unlock()
			result.(*protocol.TurnStartResponse).Turn = protocol.TurnElement{
				ID: turnID, Items: []protocol.ThreadItem{}, Status: protocol.PurpleInProgress,
			}
			turnStarted <- turnID
			return nil
		default:
			return errors.New("unexpected call: " + request.Method())
		}
	}
	agent := newRuntimeTestAgent(t, rpc)
	requester := &blockingPermissionRequester{entered: make(chan struct{}), release: make(chan struct{})}
	agent.connectionMu.Lock()
	agent.approvalRequester = requester
	agent.connectionMu.Unlock()
	if _, err := agent.NewSession(context.Background(), acp.NewSessionRequest{Cwd: "/tmp", McpServers: []acp.McpServer{}}); err != nil {
		t.Fatalf("创建 session 失败: %v", err)
	}

	startPrompt := func(text string) (<-chan acp.PromptResponse, <-chan error) {
		responses := make(chan acp.PromptResponse, 1)
		errorsResult := make(chan error, 1)
		go func() {
			response, err := agent.Prompt(context.Background(), acp.PromptRequest{
				SessionId: "thread-approval",
				Prompt:    []acp.ContentBlock{{Text: &acp.ContentBlockText{Type: "text", Text: text}}},
			})
			responses <- response
			errorsResult <- err
		}()
		return responses, errorsResult
	}
	firstResponse, firstErr := startPrompt("first")
	if turnID := <-turnStarted; turnID != "turn-1" {
		t.Fatalf("第一轮 turn ID 为 %q", turnID)
	}
	request, err := protocol.DecodeServerRequest([]byte(
		`{"id":"approval-1","method":"item/commandExecution/requestApproval","params":{"command":"pwd","cwd":"/tmp","itemId":"item-1","startedAtMs":1,"threadId":"thread-approval","turnId":"turn-1"}}`,
	))
	if err != nil {
		t.Fatalf("解码审批请求失败: %v", err)
	}
	type approvalResult struct {
		// value 是 app-server typed 审批响应。
		value any
		// err 是 server request 路由错误。
		err error
	}
	approvalResults := make(chan approvalResult, 1)
	go func() {
		value, routeErr := agent.handleServerRequest(context.Background(), request)
		approvalResults <- approvalResult{value: value, err: routeErr}
	}()
	<-requester.entered

	agent.client.HandleNotification(context.Background(), completeNotification(
		t, "thread-approval", "turn-1", protocol.FluffyCompleted,
	))
	if err = <-firstErr; err != nil {
		t.Fatalf("第一轮 prompt 返回错误: %v", err)
	}
	if response := <-firstResponse; response.StopReason != acp.StopReasonEndTurn {
		t.Fatalf("第一轮 prompt 响应为 %#v", response)
	}
	secondResponse, secondErr := startPrompt("second")
	if turnID := <-turnStarted; turnID != "turn-2" {
		t.Fatalf("第二轮 turn ID 为 %q", turnID)
	}

	close(requester.release)
	approval := <-approvalResults
	if approval.err != nil {
		t.Fatalf("stale approval 路由错误: %v", approval.err)
	}
	command, ok := approval.value.(protocol.CommandExecutionRequestApprovalResponse)
	if !ok || command.Decision == nil || command.Decision.Enum == nil || *command.Decision.Enum != protocol.Cancel {
		t.Fatalf("stale approval 未 fail-closed: %#v", approval.value)
	}
	agent.client.HandleNotification(context.Background(), completeNotification(
		t, "thread-approval", "turn-2", protocol.FluffyCompleted,
	))
	if err = <-secondErr; err != nil {
		t.Fatalf("第二轮 prompt 返回错误: %v", err)
	}
	if response := <-secondResponse; response.StopReason != acp.StopReasonEndTurn {
		t.Fatalf("第二轮 prompt 响应为 %#v", response)
	}
}

// TestSessionDeveloperInstructions preserves omitted and explicitly empty instructions on native open.
func TestSessionDeveloperInstructions(t *testing.T) {
	for _, value := range []*string{nil, new(string), stringPointer("project guidance")} {
		for _, resume := range []bool{false, true} {
			rpc := newFakeAppServerRPC()
			var got *string
			rpc.handleCall = func(_ context.Context, request protocol.ClientRequest, result any) error {
				switch request.Method() {
				case protocol.MethodInitialize:
				case protocol.MethodThreadStart:
					got = request.(protocol.ThreadStartRequest).Params.DeveloperInstructions
					result.(*protocol.ThreadStartResponse).Thread.ID = "thread"
				case protocol.MethodThreadResume:
					got = request.(protocol.ThreadResumeRequest).Params.DeveloperInstructions
					result.(*protocol.ThreadResumeResponse).Thread.ID = "thread"
				default:
					return errors.New("unexpected call: " + request.Method())
				}
				return nil
			}
			agent := newRuntimeTestAgent(t, rpc)
			var meta map[string]any
			if value != nil {
				meta = map[string]any{"developerInstructions": *value}
			}
			var err error
			if resume {
				_, err = agent.ResumeSession(context.Background(), acp.ResumeSessionRequest{Cwd: t.TempDir(), SessionId: "thread", Meta: meta})
			} else {
				_, err = agent.NewSession(context.Background(), acp.NewSessionRequest{Cwd: t.TempDir(), Meta: meta})
			}
			if err != nil {
				t.Fatal(err)
			}
			if (got == nil) != (value == nil) || (got != nil && *got != *value) {
				t.Fatalf("instructions = %v, want %v", got, value)
			}
		}
	}
}

// TestAccountUsageReadsAllNativeBucketsWithoutPrompt checks normalized units and zero presence.
func TestAccountUsageReadsAllNativeBucketsWithoutPrompt(t *testing.T) {
	rpc := newFakeAppServerRPC()
	rpc.handleCall = func(_ context.Context, request protocol.ClientRequest, result any) error {
		switch request.Method() {
		case protocol.MethodInitialize:
			return nil
		case "account/rateLimits/read":
			return json.Unmarshal([]byte(`{"rateLimits":{},"rateLimitsByLimitId":{"a":{"limitName":"A","primary":{"usedPercent":0,"windowDurationMins":60,"resetsAt":1700000000}},"b":{"secondary":{"usedPercent":40}}}}`), result)
		default:
			return errors.New("unexpected call: " + request.Method())
		}
	}
	agent := newRuntimeTestAgent(t, rpc)
	result, err := agent.HandleExtensionMethod(context.Background(), "_account/usage", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		// Buckets are independently metered quotas.
		Buckets []struct {
			// ID identifies one native quota bucket.
			ID string `json:"id"`
			// Windows contain all observed windows.
			Windows []struct {
				// UsedPercent includes zero and remains absent when unknown.
				UsedPercent *int64 `json:"used_percent"`
				// DurationSeconds uses standardized seconds.
				DurationSeconds *int64 `json:"duration_seconds"`
				// ResetAtUnixSeconds uses Unix seconds.
				ResetAtUnixSeconds *int64 `json:"reset_at_unix_seconds"`
			} `json:"windows"`
		} `json:"buckets"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Buckets) != 2 {
		t.Fatalf("buckets = %s", raw)
	}
	byID := map[string]int{}
	for i, b := range got.Buckets {
		byID[b.ID] = i
	}
	a := got.Buckets[byID["a"]]
	if len(a.Windows) != 1 || a.Windows[0].UsedPercent == nil || *a.Windows[0].UsedPercent != 0 || a.Windows[0].DurationSeconds == nil || *a.Windows[0].DurationSeconds != 3600 || a.Windows[0].ResetAtUnixSeconds == nil || *a.Windows[0].ResetAtUnixSeconds != 1700000000 {
		t.Fatalf("bucket a = %s", raw)
	}
	b := got.Buckets[byID["b"]]
	if len(b.Windows) != 1 || b.Windows[0].UsedPercent == nil || *b.Windows[0].UsedPercent != 40 || b.Windows[0].DurationSeconds != nil {
		t.Fatalf("bucket b = %s", raw)
	}
}

// TestTierObservationDoesNotChangeRequestedSelection checks native response and local ACK authority.
func TestTierObservationDoesNotChangeRequestedSelection(t *testing.T) {
	rpc := newFakeAppServerRPC()
	rpc.handleCall = func(_ context.Context, request protocol.ClientRequest, result any) error {
		switch request.Method() {
		case protocol.MethodInitialize:
		case protocol.MethodThreadStart:
			response := result.(*protocol.ThreadStartResponse)
			response.Thread.ID = "thread"
			response.Model = "fast-model"
			response.ServiceTier = protocol.NewOptionalValue("fast-native")
		case protocol.MethodModelList:
			models := testModels()
			models[0].ServiceTiers = []protocol.ServiceTierElement{{ID: "fast-native", Name: "Fast"}}
			result.(*protocol.ModelListResponse).Data = models
		default:
			return errors.New("unexpected call: " + request.Method())
		}
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	agent := newAgentWithClient(slog.New(slog.NewTextHandler(io.Discard, nil)), ctx, cancel, newAppServerClient(ctx, rpc))
	agent.markConnectionReady()
	if _, err := agent.Initialize(ctx, acp.InitializeRequest{}); err != nil {
		t.Fatal(err)
	}
	response, err := agent.NewSession(ctx, acp.NewSessionRequest{Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	tier := selectOption(t, response.ConfigOptions, "service_tier")
	if tier.CurrentValue != "inherit" || tier.Meta["observed_service_tier"] != "fast-native" {
		t.Fatalf("tier = %#v", tier)
	}
	state, _ := agent.sessions.get("thread")
	state.mu.Lock()
	err = state.configuration.Select("service_tier", "off")
	state.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	_, options, _ := sessionConfigurationResponse(state)
	tier = selectOption(t, options, "service_tier")
	if tier.Meta["observed_service_tier"] != "fast-native" {
		t.Fatalf("local ACK changed observation: %#v", tier.Meta)
	}
}

// TestCompactPreservesNativeTurnWhenCompletionPrecedesAck checks the separate native turn identity.
func TestCompactPreservesNativeTurnWhenCompletionPrecedesAck(t *testing.T) {
	rpc := newFakeAppServerRPC()
	var agent *Agent
	rpc.handleCall = func(_ context.Context, request protocol.ClientRequest, result any) error {
		switch request.Method() {
		case protocol.MethodInitialize:
		case protocol.MethodThreadStart:
			result.(*protocol.ThreadStartResponse).Thread.ID = "thread"
		case "thread/compact/start":
			agent.client.HandleNotification(context.Background(), decodeTestNotification(t, "turn/started", map[string]any{"threadId": "thread", "turn": map[string]any{"id": "compact-turn", "status": "inProgress", "items": []any{}}}))
			agent.client.HandleNotification(context.Background(), completeNotification(t, "thread", "compact-turn", protocol.FluffyCompleted))
		default:
			return errors.New("unexpected call: " + request.Method())
		}
		return nil
	}
	agent = newRuntimeTestAgent(t, rpc)
	if _, err := agent.NewSession(context.Background(), acp.NewSessionRequest{Cwd: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	result, err := agent.HandleExtensionMethod(context.Background(), "_session/compact", json.RawMessage(`{"session_id":"thread"}`))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]any
	if err = json.Unmarshal(raw, &wire); err != nil {
		t.Fatal(err)
	}
	if wire["turn_id"] != "compact-turn" || wire["status"] != "completed" {
		t.Fatalf("compact result: %s", raw)
	}
}

// TestCompactHoldsSessionUntilNativeCompletion prevents a rival prompt after a bare ACK.
func TestCompactHoldsSessionUntilNativeCompletion(t *testing.T) {
	rpc := newFakeAppServerRPC()
	entered := make(chan struct{})
	rpc.handleCall = func(_ context.Context, request protocol.ClientRequest, result any) error {
		switch request.Method() {
		case protocol.MethodInitialize:
		case protocol.MethodThreadStart:
			result.(*protocol.ThreadStartResponse).Thread.ID = "thread"
		case "thread/compact/start":
			close(entered)
		default:
			return errors.New("unexpected call: " + request.Method())
		}
		return nil
	}
	agent := newRuntimeTestAgent(t, rpc)
	if _, err := agent.NewSession(context.Background(), acp.NewSessionRequest{Cwd: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := agent.HandleExtensionMethod(context.Background(), "_session/compact", json.RawMessage(`{"session_id":"thread"}`))
		done <- err
	}()
	<-entered
	_, err := agent.Prompt(context.Background(), acp.PromptRequest{SessionId: "thread", Prompt: []acp.ContentBlock{{Text: &acp.ContentBlockText{Type: "text", Text: "hello"}}}})
	if !errors.Is(err, ErrPromptActive) {
		t.Fatalf("rival prompt: %v", err)
	}
	agent.client.HandleNotification(context.Background(), decodeTestNotification(t, "turn/started", map[string]any{"threadId": "thread", "turn": map[string]any{"id": "compact-turn", "status": "inProgress", "items": []any{}}}))
	_, steerErr := agent.HandleExtensionMethod(context.Background(), steeringExtensionMethod, steeringRawParams(t, "thread", "do not submit"))
	var rejected *acp.RequestError
	if !errors.As(steerErr, &rejected) || rejected.Code != acp.NewInvalidRequest(nil).Code {
		t.Fatalf("steering did not reject the compaction lane: %v", steerErr)
	}
	agent.client.HandleNotification(context.Background(), completeNotification(t, "thread", "compact-turn", protocol.FluffyCompleted))
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("compact did not complete")
	}
}

// TestResumeRejectsMismatchedNativeIdentity prevents a different thread being installed as the requested session.
func TestResumeRejectsMismatchedNativeIdentity(t *testing.T) {
	rpc := newFakeAppServerRPC()
	rpc.handleCall = func(_ context.Context, request protocol.ClientRequest, result any) error {
		switch request.Method() {
		case protocol.MethodInitialize:
		case protocol.MethodThreadResume:
			result.(*protocol.ThreadResumeResponse).Thread.ID = "different"
		case protocol.MethodThreadUnsubscribe:
			return nil
		default:
			return errors.New("unexpected call: " + request.Method())
		}
		return nil
	}
	agent := newRuntimeTestAgent(t, rpc)
	_, err := agent.ResumeSession(context.Background(), acp.ResumeSessionRequest{Cwd: t.TempDir(), SessionId: "expected"})
	if err == nil {
		t.Fatal("mismatched native thread accepted")
	}
	if _, ok := agent.sessions.get("expected"); ok {
		t.Fatal("mismatched thread installed")
	}
}

// TestCompactTerminalOutranksLateRequestError protects the observed native outcome.
func TestCompactTerminalOutranksLateRequestError(t *testing.T) {
	rpc := newFakeAppServerRPC()
	var agent *Agent
	rpc.handleCall = func(_ context.Context, request protocol.ClientRequest, result any) error {
		switch request.Method() {
		case protocol.MethodInitialize:
		case protocol.MethodThreadStart:
			result.(*protocol.ThreadStartResponse).Thread.ID = "thread"
		case "thread/compact/start":
			agent.client.HandleNotification(context.Background(), decodeTestNotification(t, "turn/started", map[string]any{"threadId": "thread", "turn": map[string]any{"id": "compact-turn", "status": "inProgress", "items": []any{}}}))
			agent.client.HandleNotification(context.Background(), completeNotification(t, "thread", "compact-turn", protocol.FluffyCompleted))
			return errors.New("response lost")
		default:
			return errors.New("unexpected call: " + request.Method())
		}
		return nil
	}
	agent = newRuntimeTestAgent(t, rpc)
	if _, err := agent.NewSession(context.Background(), acp.NewSessionRequest{Cwd: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	result, err := agent.HandleExtensionMethod(context.Background(), "_session/compact", json.RawMessage(`{"session_id":"thread"}`))
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(result)
	var wire map[string]any
	_ = json.Unmarshal(raw, &wire)
	if wire["turn_id"] != "compact-turn" || wire["status"] != "completed" {
		t.Fatalf("compact result: %s", raw)
	}
}

// TestEmptyModelCatalogDoesNotEraseObservedSession verifies that a successful empty list is not a transport failure.
func TestEmptyModelCatalogDoesNotEraseObservedSession(t *testing.T) {
	rpc := newFakeAppServerRPC()
	rpc.handleCall = func(_ context.Context, request protocol.ClientRequest, result any) error {
		switch request.Method() {
		case protocol.MethodInitialize:
		case protocol.MethodThreadStart:
			response := result.(*protocol.ThreadStartResponse)
			response.Thread.ID = "thread"
			response.Model = "custom"
		case protocol.MethodModelList:
			result.(*protocol.ModelListResponse).Data = []protocol.ModelListResponseDatum{}
		default:
			return errors.New("unexpected call: " + request.Method())
		}
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	agent := newAgentWithClient(slog.New(slog.NewTextHandler(io.Discard, nil)), ctx, cancel, newAppServerClient(ctx, rpc))
	agent.markConnectionReady()
	if _, err := agent.Initialize(ctx, acp.InitializeRequest{}); err != nil {
		t.Fatal(err)
	}
	response, err := agent.NewSession(ctx, acp.NewSessionRequest{Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	model := selectOption(t, response.ConfigOptions, modelConfigID)
	if model.CurrentValue != "custom" {
		t.Fatalf("observed model lost: %#v", model)
	}
}

// TestPromptPreservesNativeRejectionFacts verifies the ACP boundary retains native error data.
func TestPromptPreservesNativeRejectionFacts(t *testing.T) {
	rpc := newFakeAppServerRPC()
	rpc.handleCall = func(_ context.Context, request protocol.ClientRequest, result any) error {
		switch request.Method() {
		case protocol.MethodInitialize:
		case protocol.MethodThreadStart:
			result.(*protocol.ThreadStartResponse).Thread.ID = "thread"
		case protocol.MethodTurnStart:
			return &rpcError{Code: -32015, Message: "native rejected", Data: json.RawMessage(`{"reason":"limit","reset":0}`)}
		default:
			return errors.New("unexpected call: " + request.Method())
		}
		return nil
	}
	agent := newRuntimeTestAgent(t, rpc)
	if _, err := agent.NewSession(context.Background(), acp.NewSessionRequest{Cwd: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	_, err := agent.Prompt(context.Background(), acp.PromptRequest{SessionId: "thread", Prompt: []acp.ContentBlock{{Text: &acp.ContentBlockText{Type: "text", Text: "hello"}}}})
	var requestErr *acp.RequestError
	if !errors.As(err, &requestErr) {
		t.Fatalf("no structured error: %v", err)
	}
	wire, marshalErr := json.Marshal(requestErr.Data)
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	var data map[string]any
	if err = json.Unmarshal(wire, &data); err != nil {
		t.Fatal(err)
	}
	native, _ := data["native"].(map[string]any)
	if data["phase"] != "turn_start" || data["submission"] != "rejected" || native["code"] != float64(-32015) {
		t.Fatalf("native error data: %s", wire)
	}
	details, _ := native["data"].(map[string]any)
	if details["reason"] != "limit" || details["reset"] != float64(0) {
		t.Fatalf("native details: %s", wire)
	}
}

// TestAccountReadDoesNotInventSubscriptionForAPIKey checks the management account facts.
func TestAccountReadDoesNotInventSubscriptionForAPIKey(t *testing.T) {
	rpc := newFakeAppServerRPC()
	rpc.handleCall = func(_ context.Context, request protocol.ClientRequest, result any) error {
		switch request.Method() {
		case protocol.MethodInitialize:
			return nil
		case protocol.MethodAccountRead:
			return json.Unmarshal([]byte(`{"account":{"type":"apiKey"},"requiresOpenaiAuth":false}`), result)
		default:
			return errors.New("unexpected call: " + request.Method())
		}
	}
	agent := newRuntimeTestAgent(t, rpc)
	result, err := agent.HandleExtensionMethod(context.Background(), "_account/read", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]any
	if err = json.Unmarshal(raw, &wire); err != nil {
		t.Fatal(err)
	}
	account, _ := wire["account"].(map[string]any)
	if account["kind"] != "api_key" {
		t.Fatalf("account: %s", raw)
	}
	if _, known := account["plan"]; known {
		t.Fatalf("invented plan: %s", raw)
	}
}

// TestAccountUsageRejectsDurationOverflow avoids publishing a wrapped reset window.
func TestAccountUsageRejectsDurationOverflow(t *testing.T) {
	rpc := newFakeAppServerRPC()
	rpc.handleCall = func(_ context.Context, request protocol.ClientRequest, result any) error {
		switch request.Method() {
		case protocol.MethodInitialize:
			return nil
		case "account/rateLimits/read":
			return json.Unmarshal([]byte(`{"rateLimits":{"primary":{"usedPercent":0,"windowDurationMins":9223372036854775807}}}`), result)
		default:
			return errors.New("unexpected call: " + request.Method())
		}
	}
	agent := newRuntimeTestAgent(t, rpc)
	if _, err := agent.HandleExtensionMethod(context.Background(), "_account/usage", json.RawMessage(`{}`)); err == nil {
		t.Fatal("overflowed duration accepted")
	}
}

// TestAccountUsagePreservesNativeError exposes quota read failures without inventing empty results.
func TestAccountUsagePreservesNativeError(t *testing.T) {
	rpc := newFakeAppServerRPC()
	rpc.handleCall = func(_ context.Context, request protocol.ClientRequest, result any) error {
		if request.Method() == protocol.MethodInitialize {
			return nil
		}
		if request.Method() == "account/rateLimits/read" {
			return &rpcError{Code: -32005, Message: "unavailable", Data: json.RawMessage(`{"retry":false}`)}
		}
		return errors.New("unexpected call: " + request.Method())
	}
	agent := newRuntimeTestAgent(t, rpc)
	_, err := agent.HandleExtensionMethod(context.Background(), "_account/usage", json.RawMessage(`{}`))
	var failure *acp.RequestError
	if !errors.As(err, &failure) {
		t.Fatalf("missing structured error: %v", err)
	}
	data, _ := failure.Data.(map[string]any)
	if data["phase"] != "account_usage" || data["submission"] != "rejected" {
		t.Fatalf("failure = %#v", failure)
	}
}

// TestAccountUsageKeepsUnknownAndKnownEmptyBucketsDistinct protects omitted quota observations.
func TestAccountUsageKeepsUnknownAndKnownEmptyBucketsDistinct(t *testing.T) {
	for _, tc := range []struct {
		// input is the native quota snapshot.
		input string
		// want is the normalized bucket JSON value.
		want string
	}{{`{}`, `null`}, {`{"rateLimits":{},"rateLimitsByLimitId":{}}`, `[]`}} {
		rpc := newFakeAppServerRPC()
		rpc.handleCall = func(_ context.Context, request protocol.ClientRequest, result any) error {
			if request.Method() == protocol.MethodInitialize {
				return nil
			}
			if request.Method() == "account/rateLimits/read" {
				return json.Unmarshal([]byte(tc.input), result)
			}
			return errors.New("unexpected call: " + request.Method())
		}
		agent := newRuntimeTestAgent(t, rpc)
		result, err := agent.HandleExtensionMethod(context.Background(), "_account/usage", json.RawMessage(`{}`))
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(result)
		var wire map[string]json.RawMessage
		_ = json.Unmarshal(raw, &wire)
		if string(wire["buckets"]) != tc.want {
			t.Fatalf("input %s: got %s, want %s", tc.input, raw, tc.want)
		}
	}
}

// TestCompactCancelBeforeIdentityInterruptsLateNativeTurn retains the lane until real termination.
func TestCompactCancelBeforeIdentityInterruptsLateNativeTurn(t *testing.T) {
	rpc := newFakeAppServerRPC()
	entered := make(chan struct{})
	interrupted := make(chan protocol.TurnInterruptParams, 1)
	rpc.handleCall = func(_ context.Context, request protocol.ClientRequest, result any) error {
		switch request.Method() {
		case protocol.MethodInitialize:
		case protocol.MethodThreadStart:
			result.(*protocol.ThreadStartResponse).Thread.ID = "thread"
		case "thread/compact/start":
			close(entered)
		case protocol.MethodTurnInterrupt:
			interrupted <- request.(protocol.TurnInterruptRequest).Params
		default:
			return errors.New("unexpected call: " + request.Method())
		}
		return nil
	}
	agent := newRuntimeTestAgent(t, rpc)
	if _, err := agent.NewSession(context.Background(), acp.NewSessionRequest{Cwd: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := agent.HandleExtensionMethod(ctx, "_session/compact", json.RawMessage(`{"session_id":"thread"}`))
		done <- err
	}()
	<-entered
	cancel()
	cancelErr := <-done
	var failure *acp.RequestError
	if !errors.As(cancelErr, &failure) || failure.Code != acp.NewRequestCancelled(nil).Code {
		t.Fatalf("compact cancellation: %v", cancelErr)
	}
	_, err := agent.Prompt(context.Background(), acp.PromptRequest{SessionId: "thread", Prompt: []acp.ContentBlock{{Text: &acp.ContentBlockText{Type: "text", Text: "hello"}}}})
	if !errors.Is(err, ErrPromptActive) {
		t.Fatalf("lane released before native identity: %v", err)
	}
	agent.client.HandleNotification(context.Background(), decodeTestNotification(t, "turn/started", map[string]any{"threadId": "thread", "turn": map[string]any{"id": "compact-turn", "status": "inProgress", "items": []any{}}}))
	select {
	case got := <-interrupted:
		if got.ThreadID != "thread" || got.TurnID != "compact-turn" {
			t.Fatalf("wrong interrupt: %#v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("missing late interrupt")
	}
	agent.client.HandleNotification(context.Background(), completeNotification(t, "thread", "compact-turn", protocol.FluffyInterrupted))
}

// TestCompactMethodNotFoundReleasesLaneAndReportsUnsupported distinguishes explicit rejection from unknown submission.
func TestCompactMethodNotFoundReleasesLaneAndReportsUnsupported(t *testing.T) {
	rpc := newFakeAppServerRPC()
	rpc.handleCall = func(_ context.Context, request protocol.ClientRequest, result any) error {
		switch request.Method() {
		case protocol.MethodInitialize:
		case protocol.MethodThreadStart:
			result.(*protocol.ThreadStartResponse).Thread.ID = "thread"
		case "thread/compact/start":
			return &rpcError{Code: -32601, Message: "method not found"}
		default:
			return errors.New("unexpected call: " + request.Method())
		}
		return nil
	}
	agent := newRuntimeTestAgent(t, rpc)
	if _, err := agent.NewSession(context.Background(), acp.NewSessionRequest{Cwd: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	_, err := agent.HandleExtensionMethod(context.Background(), "_session/compact", json.RawMessage(`{"session_id":"thread"}`))
	var failure *acp.RequestError
	if !errors.As(err, &failure) || failure.Code != -32601 {
		t.Fatalf("unsupported: %v", err)
	}
	state, _ := agent.sessions.get("thread")
	state.mu.Lock()
	active := state.activeTurn
	state.mu.Unlock()
	if active != nil {
		t.Fatal("explicit rejection retained the session lane")
	}
}

// TestFailedTurnPreservesObservedUsageInFinalError prevents consumed tokens disappearing on failure.
func TestFailedTurnPreservesObservedUsageInFinalError(t *testing.T) {
	rpc := newFakeAppServerRPC()
	started := make(chan struct{})
	rpc.handleCall = func(_ context.Context, request protocol.ClientRequest, result any) error {
		switch request.Method() {
		case protocol.MethodInitialize:
		case protocol.MethodThreadStart:
			result.(*protocol.ThreadStartResponse).Thread.ID = "thread"
		case protocol.MethodTurnStart:
			result.(*protocol.TurnStartResponse).Turn = protocol.TurnElement{ID: "turn", Items: []protocol.ThreadItem{}, Status: protocol.PurpleInProgress}
			close(started)
		default:
			return errors.New("unexpected call: " + request.Method())
		}
		return nil
	}
	agent := newRuntimeTestAgent(t, rpc)
	agent.connectionMu.Lock()
	agent.sessionUpdater = &recordingSessionUpdater{}
	agent.connectionMu.Unlock()
	if _, err := agent.NewSession(context.Background(), acp.NewSessionRequest{Cwd: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := agent.Prompt(context.Background(), acp.PromptRequest{SessionId: "thread", Prompt: []acp.ContentBlock{{Text: &acp.ContentBlockText{Type: "text", Text: "hello"}}}})
		done <- err
	}()
	<-started
	state, _ := agent.sessions.get("thread")
	state.mu.Lock()
	prompt := state.activeTurn
	state.mu.Unlock()
	<-prompt.turnStarted
	usage := usageSample(12, 3, 12, 3)
	usage.ThreadID = "thread"
	usage.TurnID = "turn"
	agent.client.HandleNotification(context.Background(), decodeTestNotification(t, "thread/tokenUsage/updated", usage))
	terminal := completeNotification(t, "thread", "turn", protocol.Failed).(*protocol.TurnCompletedEnvelope)
	terminal.Params.Turn.Error = &protocol.Error{Message: "failure"}
	agent.client.HandleNotification(context.Background(), terminal)
	var failure *acp.RequestError
	if err := <-done; !errors.As(err, &failure) {
		t.Fatalf("failure: %v", err)
	}
	data, _ := failure.Data.(map[string]any)
	observed, ok := acpmeta.ReadTurnUsage(data)
	if !ok || observed.InputTokens == nil || *observed.InputTokens != 12 || observed.OutputTokens == nil || *observed.OutputTokens != 3 || observed.Complete {
		t.Fatalf("usage lost: %#v", data)
	}
}

// TestAccountUsageDoesNotInventZeroForMissingPercent preserves an unknown window percentage.
func TestAccountUsageDoesNotInventZeroForMissingPercent(t *testing.T) {
	rpc := newFakeAppServerRPC()
	rpc.handleCall = func(_ context.Context, request protocol.ClientRequest, result any) error {
		if request.Method() == protocol.MethodInitialize {
			return nil
		}
		if request.Method() == "account/rateLimits/read" {
			return json.Unmarshal([]byte(`{"rateLimits":{"primary":{"windowDurationMins":60}}}`), result)
		}
		return errors.New("unexpected call: " + request.Method())
	}
	agent := newRuntimeTestAgent(t, rpc)
	result, err := agent.HandleExtensionMethod(context.Background(), "_account/usage", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(result)
	var wire map[string]any
	_ = json.Unmarshal(raw, &wire)
	window := wire["buckets"].([]any)[0].(map[string]any)["windows"].([]any)[0].(map[string]any)
	if _, reported := window["used_percent"]; reported {
		t.Fatalf("missing value became zero: %s", raw)
	}
}

// TestCompactIgnoresCompletedTurnReplay prevents a prior compact from claiming a new lane.
func TestCompactIgnoresCompletedTurnReplay(t *testing.T) {
	rpc := newFakeAppServerRPC()
	var agent *Agent
	compacts := 0
	rpc.handleCall = func(ctx context.Context, request protocol.ClientRequest, result any) error {
		switch request.Method() {
		case protocol.MethodInitialize:
		case protocol.MethodThreadStart:
			result.(*protocol.ThreadStartResponse).Thread.ID = "thread"
		case "thread/compact/start":
			compacts++
			send := func(id string) {
				agent.client.HandleNotification(ctx, decodeTestNotification(t, "turn/started", map[string]any{
					"threadId": "thread", "turn": map[string]any{"id": id, "status": "inProgress", "items": []any{}},
				}))
				agent.client.HandleNotification(ctx, completeNotification(t, "thread", id, protocol.FluffyCompleted))
			}
			send("first")
			if compacts == 2 {
				send("second")
			}
		default:
			return fmt.Errorf("unexpected call: %s", request.Method())
		}
		return nil
	}
	agent = newRuntimeTestAgent(t, rpc)
	if _, err := agent.NewSession(context.Background(), acp.NewSessionRequest{Cwd: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"first", "second"} {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		result, err := agent.HandleExtensionMethod(ctx, acpmeta.CompactMethod, json.RawMessage(`{"session_id":"thread"}`))
		cancel()
		if err != nil {
			t.Fatal(err)
		}
		if got := result.(acpmeta.CompactResponse).TurnID; got != want {
			t.Fatalf("compact turn = %q, want %q", got, want)
		}
	}
}

// TestCompactFailureRetainsItsOwnUsage verifies the native compact turn is accounted separately.
func TestCompactFailureRetainsItsOwnUsage(t *testing.T) {
	rpc := newFakeAppServerRPC()
	var agent *Agent
	rpc.handleCall = func(ctx context.Context, request protocol.ClientRequest, result any) error {
		switch request.Method() {
		case protocol.MethodInitialize:
		case protocol.MethodThreadStart:
			result.(*protocol.ThreadStartResponse).Thread.ID = "thread"
		case "thread/compact/start":
			agent.client.HandleNotification(ctx, completeNotification(t, "thread", "other", protocol.FluffyCompleted))
			foreign := usageSample(5, 0, 5, 0)
			foreign.ThreadID, foreign.TurnID = "thread", "other"
			for range 2 * maxEarlyUsageSamples {
				agent.client.HandleNotification(ctx, decodeTestNotification(t, protocol.MethodThreadTokenUsageUpdated, foreign))
			}
			agent.client.HandleNotification(ctx, decodeTestNotification(t, "thread/tokenUsage/updated", map[string]any{
				"threadId": "thread", "turnId": "compact", "tokenUsage": map[string]any{
					"last":  map[string]any{"inputTokens": 8, "outputTokens": 2, "totalTokens": 10},
					"total": map[string]any{"inputTokens": 13, "outputTokens": 2, "totalTokens": 15},
				},
			}))
			agent.client.HandleNotification(ctx, decodeTestNotification(t, "turn/started", map[string]any{
				"threadId": "thread", "turn": map[string]any{"id": "compact", "status": "inProgress", "items": []any{}},
			}))
			agent.client.HandleNotification(ctx, decodeTestNotification(t, "turn/completed", map[string]any{
				"threadId": "thread", "turn": map[string]any{"id": "compact", "status": "failed", "items": []any{},
					"error": map[string]any{"message": "native compact failure"}},
			}))
		default:
			return fmt.Errorf("unexpected call: %s", request.Method())
		}
		return nil
	}
	agent = newRuntimeTestAgent(t, rpc)
	if _, err := agent.NewSession(context.Background(), acp.NewSessionRequest{Cwd: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err := agent.HandleExtensionMethod(ctx, acpmeta.CompactMethod, json.RawMessage(`{"session_id":"thread"}`))
	var failure *acp.RequestError
	if !errors.As(err, &failure) {
		t.Fatalf("compact error = %v", err)
	}
	data, _ := failure.Data.(map[string]any)
	usage, ok := acpmeta.ReadTurnUsage(data)
	if !ok || usage.InputTokens == nil || *usage.InputTokens != 8 || usage.Complete ||
		data["turn_id"] != "compact" || data["phase"] != "compact_completion" || data["submission"] != "accepted" {
		t.Fatalf("compact failure facts = %#v", data)
	}
}

// TestCompactReleasesLaneWhenServerExitsBeforeIdentity exercises an ACK without a started notification.
func TestCompactReleasesLaneWhenServerExitsBeforeIdentity(t *testing.T) {
	rpc := newFakeAppServerRPC()
	ack := make(chan struct{})
	rpc.handleCall = func(_ context.Context, request protocol.ClientRequest, result any) error {
		switch request.Method() {
		case protocol.MethodInitialize:
		case protocol.MethodThreadStart:
			result.(*protocol.ThreadStartResponse).Thread.ID = "thread"
		case protocol.MethodThreadCompactStart:
			close(ack)
		default:
			return fmt.Errorf("unexpected call %s", request.Method())
		}
		return nil
	}
	agent := newRuntimeTestAgent(t, rpc)
	if _, err := agent.NewSession(context.Background(), acp.NewSessionRequest{Cwd: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := agent.HandleExtensionMethod(context.Background(), acpmeta.CompactMethod, json.RawMessage(`{"session_id":"thread"}`))
		done <- err
	}()
	<-ack
	close(rpc.done)
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("process exit returned successful compact")
		}
	case <-time.After(time.Second):
		t.Fatal("compact did not return after process exit")
	}
	state, _ := agent.sessions.get("thread")
	if sessionActiveTurn(state) != nil {
		t.Fatal("compact still owns the session lane after process exit")
	}
}

// TestPromptDrainsNativeEventsAcrossStartAck checks that the terminal retains early output and usage.
func TestPromptDrainsNativeEventsAcrossStartAck(t *testing.T) {
	rpc := newFakeAppServerRPC()
	var agent *Agent
	rpc.handleCall = func(ctx context.Context, request protocol.ClientRequest, result any) error {
		switch request.Method() {
		case protocol.MethodInitialize:
		case protocol.MethodThreadStart:
			result.(*protocol.ThreadStartResponse).Thread.ID = "thread"
		case protocol.MethodTurnStart:
			agent.client.HandleNotification(ctx, decodeTestNotification(t, protocol.MethodAgentMessageDelta, map[string]any{
				"threadId": "thread", "turnId": "native", "itemId": "reply", "delta": "early",
			}))
			usage := usageSample(12, 4, 12, 4)
			usage.ThreadID, usage.TurnID = "thread", "native"
			agent.client.HandleNotification(ctx, decodeTestNotification(t, protocol.MethodThreadTokenUsageUpdated, usage))
			agent.client.HandleNotification(ctx, completeNotification(t, "thread", "native", protocol.FluffyCompleted))
			result.(*protocol.TurnStartResponse).Turn.ID = "native"
		default:
			return fmt.Errorf("unexpected call %s", request.Method())
		}
		return nil
	}
	agent = newRuntimeTestAgent(t, rpc)
	updater := &recordingSessionUpdater{}
	agent.sessionUpdater = updater
	if _, err := agent.NewSession(context.Background(), acp.NewSessionRequest{Cwd: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	response, err := agent.Prompt(context.Background(), acp.PromptRequest{SessionId: "thread"})
	if err != nil {
		t.Fatal(err)
	}
	if response.Usage == nil || response.Usage.InputTokens != 12 || len(updater.notifications) != 2 {
		t.Fatalf("early frames lost: usage=%#v updates=%d", response.Usage, len(updater.notifications))
	}
}

// TestPromptDeliveryFailureOutranksNativeSuccess checks separate native and ACP terminal facts.
func TestPromptDeliveryFailureOutranksNativeSuccess(t *testing.T) {
	rpc := newFakeAppServerRPC()
	var agent *Agent
	rpc.handleCall = func(ctx context.Context, request protocol.ClientRequest, result any) error {
		switch request.Method() {
		case protocol.MethodInitialize:
		case protocol.MethodThreadStart:
			result.(*protocol.ThreadStartResponse).Thread.ID = "thread"
		case protocol.MethodTurnStart:
			result.(*protocol.TurnStartResponse).Turn.ID = "native"
		default:
			return fmt.Errorf("unexpected call %s", request.Method())
		}
		return nil
	}
	agent = newRuntimeTestAgent(t, rpc)
	agent.sessionUpdater = &recordingHistoryUpdater{err: errors.New("ACP pipe closed")}
	rpc.afterObserved = func() {
		agent.client.HandleNotification(context.Background(), decodeTestNotification(t, protocol.MethodAgentMessageDelta, map[string]any{
			"threadId": "thread", "turnId": "native", "itemId": "reply", "delta": "not delivered",
		}))
		agent.client.HandleNotification(context.Background(), completeNotification(t, "thread", "native", protocol.FluffyCompleted))
	}
	if _, err := agent.NewSession(context.Background(), acp.NewSessionRequest{Cwd: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	response, err := agent.Prompt(context.Background(), acp.PromptRequest{SessionId: "thread"})
	var failure *acp.RequestError
	if !errors.As(err, &failure) {
		t.Fatalf("failed update reported as normal completion: %+v / %v", response, err)
	}
	facts, _ := failure.Data.(map[string]any)
	if facts["phase"] != "event_delivery" || facts["native_status"] != "completed" {
		t.Fatalf("delivery failure erased native terminal facts: %#v", facts)
	}
}

// TestQuotaReadRetainsIndividualWindowAndResetCredits preserves known zero, false, and empty details.
func TestQuotaReadRetainsIndividualWindowAndResetCredits(t *testing.T) {
	rpc := newFakeAppServerRPC()
	rpc.accountResponse = &protocol.GetAccountResponse{Account: &protocol.Account{Type: protocol.AccountTypeChatgpt}}
	rpc.handleCall = func(_ context.Context, request protocol.ClientRequest, result any) error {
		switch request.Method() {
		case protocol.MethodInitialize:
			return nil
		case protocol.MethodAccountRateLimitsRead:
			*result.(*json.RawMessage) = json.RawMessage(`{"rateLimits":{"individualLimit":{"remainingPercent":0,"resetsAt":1700},"rateLimitReachedType":"workspace_member_usage_limit_reached","spendControlReached":false},"rateLimitResetCredits":{"availableCount":0,"credits":[]}}`)
			return nil
		default:
			return fmt.Errorf("unexpected call %s", request.Method())
		}
	}
	agent := newRuntimeTestAgent(t, rpc)
	result, err := agent.HandleExtensionMethod(context.Background(), acpmeta.AccountUsageMethod, json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	wire, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"kind":"individual"`, `"remaining_percent":0`, `"reset_at_unix_seconds":1700`, `"reached_type":"workspace_member_usage_limit_reached"`, `"spend_control_reached":false`, `"reset_credits":{"available_count":0,"details":[]}`} {
		if !strings.Contains(string(wire), want) {
			t.Fatalf("quota missing %s: %s", want, wire)
		}
	}
}

// TestNativeThreadArchiveOperationsUseExactIdentity checks both explicit management operations.
func TestNativeThreadArchiveOperationsUseExactIdentity(t *testing.T) {
	rpc := newFakeAppServerRPC()
	var called []string
	rpc.handleCall = func(_ context.Context, request protocol.ClientRequest, result any) error {
		if request.Method() == protocol.MethodInitialize {
			return nil
		}
		frame, err := json.Marshal(request)
		if err != nil || !strings.Contains(string(frame), `"threadId":"saved"`) {
			return fmt.Errorf("wrong management request: %s, %v", frame, err)
		}
		called = append(called, request.Method())
		if request.Method() == "thread/unarchive" {
			return json.Unmarshal([]byte(`{"thread":{"id":"saved"}}`), result)
		}
		return nil
	}
	agent := newRuntimeTestAgent(t, rpc)
	for _, method := range []string{"_codex/thread/archive", "_codex/thread/unarchive"} {
		if _, err := agent.HandleExtensionMethod(context.Background(), method, json.RawMessage(`{"thread_id":"saved"}`)); err != nil {
			t.Fatalf("%s: %v", method, err)
		}
	}
	if !slices.Equal(called, []string{"thread/archive", "thread/unarchive"}) {
		t.Fatalf("native management requests: %v", called)
	}
}

// TestNativeSettingsCarryOpenIntentAndIndependentObservations checks the real open and late receipt path.
func TestNativeSettingsCarryOpenIntentAndIndependentObservations(t *testing.T) {
	rpc := newFakeAppServerRPC()
	var wire []byte
	rpc.handleCall = func(_ context.Context, request protocol.ClientRequest, result any) error {
		switch request.Method() {
		case protocol.MethodInitialize, protocol.MethodModelList:
			return nil
		case protocol.MethodThreadStart:
			wire, _ = json.Marshal(request)
			return json.Unmarshal([]byte(`{"thread":{"id":"settings"},"model":"observed","reasoningEffort":"low","serviceTier":"standard"}`), result)
		default:
			return fmt.Errorf("unexpected call %s", request.Method())
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	agent := newAgentWithClient(slog.Default(), ctx, cancel, newAppServerClient(ctx, rpc))
	initializeTestAgent(t, agent)
	response, err := agent.NewSession(ctx, acp.NewSessionRequest{Cwd: t.TempDir(), Meta: map[string]any{
		"codex/open-settings": map[string]any{"model": "requested", "reasoning_effort": "high", "service_tier": nil},
	}})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"model":"requested"`, `"model_reasoning_effort":"high"`, `"serviceTier":null`} {
		if !strings.Contains(string(wire), want) {
			t.Fatalf("open did not submit %s: %s", want, wire)
		}
	}
	encoded, _ := json.Marshal(response.Meta)
	for _, want := range []string{`"requested":{"model":"requested"`, `"observed":{"model":"observed"`, `"service_tier":"standard"`} {
		if !strings.Contains(string(encoded), want) {
			t.Fatalf("open result did not retain %s: %s", want, encoded)
		}
	}
	agent.client.HandleNotification(ctx, decodeTestNotification(t, "thread/settings/updated", map[string]any{
		"threadId": "settings", "threadSettings": map[string]any{"model": "rerouted", "effort": "medium", "serviceTier": nil},
	}))
	result, err := agent.HandleExtensionMethod(ctx, "_codex/session/settings", json.RawMessage(`{"session_id":"settings"}`))
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ = json.Marshal(result)
	for _, want := range []string{`"model":"requested"`, `"model":"rerouted"`, `"service_tier":null`, `"source":"thread_settings_updated"`} {
		if !strings.Contains(string(encoded), want) {
			t.Fatalf("late receipt did not retain %s: %s", want, encoded)
		}
	}
}

// TestFailedTurnPreservesObservedOutputAndRetry verifies positive evidence survives an empty failure.
func TestFailedTurnPreservesObservedOutputAndRetry(t *testing.T) {
	rpc := newFakeAppServerRPC()
	var agent *Agent
	rpc.handleCall = func(ctx context.Context, request protocol.ClientRequest, result any) error {
		switch request.Method() {
		case protocol.MethodInitialize:
		case protocol.MethodThreadStart:
			result.(*protocol.ThreadStartResponse).Thread.ID = "thread"
		case protocol.MethodTurnStart:
			result.(*protocol.TurnStartResponse).Turn.ID = "turn"
		default:
			return fmt.Errorf("unexpected call %s", request.Method())
		}
		return nil
	}
	agent = newRuntimeTestAgent(t, rpc)
	rpc.afterObserved = func() {
		ctx := context.Background()
		agent.client.HandleNotification(ctx, decodeTestNotification(t, protocol.MethodAgentMessageDelta, map[string]any{
			"threadId": "thread", "turnId": "turn", "itemId": "output", "delta": "partial",
		}))
		agent.client.HandleNotification(ctx, decodeTestNotification(t, protocol.MethodItemCompleted, map[string]any{
			"threadId": "thread", "turnId": "turn", "item": map[string]any{"type": "fileChange", "id": "write", "status": "completed", "changes": []any{map[string]any{"path": "/workspace/result", "kind": map[string]any{"type": "add"}, "diff": "+result"}}},
		}))
		agent.client.HandleNotification(ctx, decodeTestNotification(t, protocol.MethodError, map[string]any{
			"threadId": "thread", "turnId": "turn", "willRetry": true, "error": map[string]any{"message": "temporary native connection loss"},
		}))
		agent.client.HandleNotification(ctx, decodeTestNotification(t, protocol.MethodModelRerouted, map[string]any{
			"threadId": "thread", "turnId": "turn", "fromModel": "original", "toModel": "new-model", "reason": "provider",
		}))
		agent.client.HandleNotification(ctx, completeNotification(t, "thread", "turn", protocol.Failed))
	}
	if _, err := agent.NewSession(context.Background(), acp.NewSessionRequest{Cwd: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	_, err := agent.Prompt(context.Background(), acp.PromptRequest{SessionId: "thread"})
	var failure *acp.RequestError
	if !errors.As(err, &failure) {
		t.Fatalf("failed native turn: %v", err)
	}
	data, _ := failure.Data.(map[string]any)
	wire, _ := json.Marshal(data)
	for _, want := range []string{`"output_observed":true`, `"tool_completion_observed":true`, `"file_change_observed":true`, `"will_retry":true`, `"temporary native connection loss"`, `"to":"new-model"`} {
		if !strings.Contains(string(wire), want) {
			t.Fatalf("failure lost %s: %s", want, wire)
		}
	}
}

// TestResumePreservesUnrelatedNativeFault verifies a non-missing rejection survives the ACP boundary.
func TestResumePreservesUnrelatedNativeFault(t *testing.T) {
	rpc := newFakeAppServerRPC()
	rpc.handleCall = func(ctx context.Context, request protocol.ClientRequest, result any) error {
		switch request.Method() {
		case protocol.MethodInitialize:
			return nil
		case protocol.MethodThreadResume:
			return &rpcError{Code: -32099, Message: "model quota", Data: json.RawMessage(`{"resetsAt":9000}`)}
		default:
			return fmt.Errorf("unexpected call %s", request.Method())
		}
	}
	agent := newRuntimeTestAgent(t, rpc)
	_, err := agent.ResumeSession(context.Background(), acp.ResumeSessionRequest{SessionId: "thread", Cwd: t.TempDir()})
	var failure *acp.RequestError
	if !errors.As(err, &failure) {
		t.Fatalf("native error not exposed as ACP request error: %v", err)
	}
	wire, _ := json.Marshal(failure.Data)
	for _, want := range []string{`"phase":"thread_resume"`, `"code":-32099`, `"resetsAt":9000`} {
		if !strings.Contains(string(wire), want) {
			t.Fatalf("resume error lost %s: %s", want, wire)
		}
	}
}

// TestPromptUsesMessageIdentityWhenStartAckIsLost verifies a proven terminal remains authoritative.
func TestPromptUsesMessageIdentityWhenStartAckIsLost(t *testing.T) {
	rpc := newFakeAppServerRPC()
	var agent *Agent
	rpc.handleCall = func(ctx context.Context, request protocol.ClientRequest, result any) error {
		switch value := request.(type) {
		case protocol.InitializeRequest:
		case protocol.ThreadStartRequest:
			result.(*protocol.ThreadStartResponse).Thread.ID = "thread"
		case protocol.TurnStartRequest:
			messageID := stringValue(value.Params.ClientUserMessageID)
			agent.client.HandleNotification(ctx, decodeTestNotification(t, protocol.MethodItemStarted, map[string]any{
				"threadId": "thread", "turnId": "native", "item": map[string]any{"type": "userMessage", "id": "user", "clientId": messageID},
			}))
			agent.client.HandleNotification(ctx, completeNotification(t, "thread", "native", protocol.FluffyCompleted))
			return context.DeadlineExceeded
		default:
			return fmt.Errorf("unexpected call %s", request.Method())
		}
		return nil
	}
	agent = newRuntimeTestAgent(t, rpc)
	if _, err := agent.NewSession(context.Background(), acp.NewSessionRequest{Cwd: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	got, err := agent.Prompt(context.Background(), acp.PromptRequest{SessionId: "thread"})
	if err != nil || got.StopReason != acp.StopReasonEndTurn {
		t.Fatalf("proven native terminal was lost: %+v / %v", got, err)
	}
}

// TestForeignUsageCannotInflateAnActivePrompt checks both pending and acknowledged identities.
func TestForeignUsageCannotInflateAnActivePrompt(t *testing.T) {
	for _, beforeAck := range []bool{true, false} {
		t.Run(fmt.Sprint(beforeAck), func(t *testing.T) {
			rpc := newFakeAppServerRPC()
			var agent *Agent
			sendUsage := func(threadID, turnID string, own, total int64) {
				usage := usageSample(own, 0, total, 0)
				usage.ThreadID, usage.TurnID = threadID, turnID
				agent.client.HandleNotification(context.Background(), decodeTestNotification(t, protocol.MethodThreadTokenUsageUpdated, usage))
			}
			rpc.handleCall = func(ctx context.Context, request protocol.ClientRequest, result any) error {
				switch request.Method() {
				case protocol.MethodInitialize:
				case protocol.MethodThreadStart:
					result.(*protocol.ThreadStartResponse).Thread.ID = "thread"
				case protocol.MethodTurnStart:
					if beforeAck {
						sendUsage("thread", "foreign", 30, 30)
					}
					result.(*protocol.TurnStartResponse).Turn.ID = "owned"
				default:
					return fmt.Errorf("unexpected call %s", request.Method())
				}
				return nil
			}
			agent = newRuntimeTestAgent(t, rpc)
			rpc.afterObserved = func() {
				if !beforeAck {
					sendUsage("thread", "foreign", 30, 30)
				}
				sendUsage("thread", "owned", 10, 40)
				agent.client.HandleNotification(context.Background(), completeNotification(t, "thread", "owned", protocol.FluffyCompleted))
			}
			if _, err := agent.NewSession(context.Background(), acp.NewSessionRequest{Cwd: t.TempDir()}); err != nil {
				t.Fatal(err)
			}
			response, err := agent.Prompt(context.Background(), acp.PromptRequest{SessionId: "thread"})
			report, reported := acpmeta.ReadTurnUsage(response.Meta)
			if err != nil || !reported || report.Complete || report.InputTokens == nil || *report.InputTokens != 10 {
				t.Fatalf("foreign usage polluted the result: %+v / %v", response, err)
			}
		})
	}
}

// TestProvenNativeTerminalDoesNotWaitForMissingAck checks independently observed completion.
func TestProvenNativeTerminalDoesNotWaitForMissingAck(t *testing.T) {
	rpc := newFakeAppServerRPC()
	var agent *Agent
	rpc.handleCall = func(ctx context.Context, request protocol.ClientRequest, result any) error {
		switch value := request.(type) {
		case protocol.InitializeRequest:
		case protocol.ThreadStartRequest:
			result.(*protocol.ThreadStartResponse).Thread.ID = "thread"
		case protocol.TurnStartRequest:
			agent.client.HandleNotification(ctx, decodeTestNotification(t, protocol.MethodItemStarted, map[string]any{
				"threadId": "thread", "turnId": "turn", "item": map[string]any{
					"type": "userMessage", "id": "user", "clientId": stringValue(value.Params.ClientUserMessageID),
				},
			}))
			agent.client.HandleNotification(ctx, completeNotification(t, "thread", "turn", protocol.FluffyCompleted))
			<-ctx.Done()
			return ctx.Err()
		default:
			return fmt.Errorf("unexpected call %s", request.Method())
		}
		return nil
	}
	agent = newRuntimeTestAgent(t, rpc)
	if _, err := agent.NewSession(context.Background(), acp.NewSessionRequest{Cwd: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		result, err := agent.Prompt(context.Background(), acp.PromptRequest{SessionId: "thread"})
		if err == nil && result.StopReason != acp.StopReasonEndTurn {
			err = fmt.Errorf("expected successful native terminal, got %s", result.StopReason)
		}
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("prompt still waiting for ACK after proven native terminal")
	}
}

// TestApprovalBeforeStartAckUsesProvenUserMessage checks that an early native request can reach ACP.
func TestApprovalBeforeStartAckUsesProvenUserMessage(t *testing.T) {
	rpc := newFakeAppServerRPC()
	var agent *Agent
	requester := &recordingPermissionRequester{response: selectedPermission("allow_once")}
	rpc.handleCall = func(ctx context.Context, request protocol.ClientRequest, result any) error {
		switch value := request.(type) {
		case protocol.InitializeRequest:
		case protocol.ThreadStartRequest:
			result.(*protocol.ThreadStartResponse).Thread.ID = "thread"
		case protocol.TurnStartRequest:
			agent.client.HandleNotification(ctx, decodeTestNotification(t, protocol.MethodItemStarted, map[string]any{
				"threadId": "thread", "turnId": "turn", "item": map[string]any{
					"type": "userMessage", "id": "user", "clientId": stringValue(value.Params.ClientUserMessageID),
				},
			}))
			approval, err := protocol.DecodeServerRequest([]byte(`{"id":"approval","method":"item/commandExecution/requestApproval","params":{"command":"pwd","cwd":"/tmp","itemId":"item","startedAtMs":1,"threadId":"thread","turnId":"turn"}}`))
			if err != nil {
				return err
			}
			response, err := agent.handleServerRequest(ctx, approval)
			if err != nil {
				return err
			}
			decision := response.(protocol.CommandExecutionRequestApprovalResponse).Decision
			if decision == nil || decision.Enum == nil || *decision.Enum != protocol.Accept {
				return fmt.Errorf("approval denied before ACK: %+v", response)
			}
			result.(*protocol.TurnStartResponse).Turn.ID = "turn"
			agent.client.HandleNotification(ctx, completeNotification(t, "thread", "turn", protocol.FluffyCompleted))
		default:
			return fmt.Errorf("unexpected call %s", request.Method())
		}
		return nil
	}
	agent = newRuntimeTestAgent(t, rpc)
	agent.approvalRequester = requester
	if _, err := agent.NewSession(context.Background(), acp.NewSessionRequest{Cwd: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	if _, err := agent.Prompt(context.Background(), acp.PromptRequest{SessionId: "thread"}); err != nil {
		t.Fatal(err)
	}
	if len(requester.requests) != 1 {
		t.Fatalf("approval requests: %d", len(requester.requests))
	}
}

// blockingSessionUpdater holds an ACP write while the fake app server sends other frames.
type blockingSessionUpdater struct {
	// entered signals the ACP write is blocked.
	entered chan struct{}
	// release lets the test finish the write.
	release chan struct{}
}

// SessionUpdate simulates a slow ACP peer.
func (u *blockingSessionUpdater) SessionUpdate(context.Context, acp.SessionNotification) error {
	close(u.entered)
	<-u.release
	return nil
}

// TestNativeReadRemainsResponsiveDuringBlockedACPWrite verifies real NDJSON ordering and drain.
func TestNativeReadRemainsResponsiveDuringBlockedACPWrite(t *testing.T) {
	var client *appServerClient
	harness := newTransportHarness(t, 4096, func(ctx context.Context, n protocol.ServerNotification) {
		client.HandleNotification(ctx, n)
	}, nil)
	runtime, cancel := context.WithCancel(context.Background())
	defer cancel()
	client = newAppServerClient(runtime, harness.transport)
	agent := newAgentWithClient(slog.Default(), runtime, cancel, client)
	agent.initialized = true
	agent.markConnectionReady()
	updater := &blockingSessionUpdater{entered: make(chan struct{}), release: make(chan struct{})}
	defer func() {
		select {
		case <-updater.release:
		default:
			close(updater.release)
		}
	}()
	agent.sessionUpdater = updater
	generation, err := agent.sessions.beginOpen("thread")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := agent.sessions.install("thread", t.TempDir(), generation, nil, terminalOutputModeDelta); !ok {
		t.Fatal("failed to install thread fixture")
	}
	done := make(chan error, 1)
	go func() {
		_, err := agent.Prompt(context.Background(), acp.PromptRequest{SessionId: "thread"})
		done <- err
	}()
	turn := readRuntimeWireRequest(t, harness)
	writeRuntimeWireResult(t, harness, turn.ID, json.RawMessage(`{"turn":{"id":"turn","status":"inProgress","items":[]}}`))
	if _, err := io.WriteString(harness.responses, `{"method":"item/agentMessage/delta","params":{"threadId":"thread","turnId":"turn","itemId":"output","delta":"hello"}}`+"\n"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-updater.entered:
	case <-time.After(time.Second):
		t.Fatal("ACP notification was never attempted")
	}
	sentinelDone := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_, err := client.ThreadRead(ctx, protocol.ThreadReadParams{ThreadID: "thread"})
		sentinelDone <- err
	}()
	sentinel := readRuntimeWireRequest(t, harness)
	go writeRuntimeWireResult(t, harness, sentinel.ID, json.RawMessage(`{"thread":{"id":"thread"}}`))
	if err := <-sentinelDone; err != nil {
		t.Fatalf("blocked ACP event delayed unrelated native RPC: %v", err)
	}
	if _, err := io.WriteString(harness.responses, `{"method":"turn/completed","params":{"threadId":"thread","turn":{"id":"turn","status":"completed","items":[]}}}`+"\n"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
		t.Fatal("terminal crossed an undrained ACP update")
	default:
	}
	close(updater.release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("terminal did not follow the drained ACP update")
	}
}

// TestProofBearingTerminalSurvivesPreAckOverflow checks that losing optional events does not lose terminal evidence.
func TestProofBearingTerminalSurvivesPreAckOverflow(t *testing.T) {
	for _, test := range []struct {
		// name identifies the sequence around display capacity.
		name string
		// overflow drops one display frame before the terminal.
		overflow bool
		// separateProof supplies identity on a user item after the terminal.
		separateProof bool
	}{
		{name: "full"},
		{name: "overflow", overflow: true},
		{name: "terminal_before_proof", overflow: true, separateProof: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			rpc := newFakeAppServerRPC()
			sent := make(chan struct{})
			var agent *Agent
			rpc.handleCall = func(ctx context.Context, request protocol.ClientRequest, result any) error {
				switch request := request.(type) {
				case protocol.InitializeRequest:
				case protocol.ThreadStartRequest:
					result.(*protocol.ThreadStartResponse).Thread.ID = "review-thread"
				case protocol.TurnStartRequest:
					frames := maxEarlyTurnUpdates
					if test.overflow {
						frames++
					}
					for range frames {
						frame := decodeTestNotification(t, protocol.MethodAgentMessageDelta, map[string]any{
							"threadId": "review-thread", "turnId": "review-turn", "itemId": "review-item", "delta": "a",
						})
						agent.client.HandleNotification(ctx, frame)
					}
					terminal := completeNotification(t, "review-thread", "review-turn", protocol.FluffyCompleted).(*protocol.TurnCompletedEnvelope)
					if !test.separateProof {
						terminal.Params.Turn.Items = []protocol.ThreadItem{{Type: protocol.UserMessage, ClientID: request.Params.ClientUserMessageID}}
					}
					agent.client.HandleNotification(ctx, terminal)
					if test.separateProof {
						agent.client.HandleNotification(ctx, decodeTestNotification(t, protocol.MethodItemCompleted, map[string]any{
							"threadId": "review-thread", "turnId": "review-turn", "item": map[string]any{
								"type": "userMessage", "id": "user", "clientId": stringValue(request.Params.ClientUserMessageID),
							},
						}))
					}
					close(sent)
					<-ctx.Done()
					return ctx.Err()
				default:
					return fmt.Errorf("unexpected native method %s", request.Method())
				}
				return nil
			}
			agent = newRuntimeTestAgent(t, rpc)
			if _, err := agent.NewSession(context.Background(), acp.NewSessionRequest{Cwd: t.TempDir()}); err != nil {
				t.Fatal(err)
			}
			state, _ := agent.sessions.get("review-thread")
			done := make(chan error, 1)
			go func() {
				response, err := agent.Prompt(context.Background(), acp.PromptRequest{SessionId: "review-thread", Prompt: []acp.ContentBlock{{Text: &acp.ContentBlockText{Type: "text", Text: "hi"}}}})
				if err == nil && response.StopReason != acp.StopReasonEndTurn {
					err = fmt.Errorf("unexpected terminal %q", response.StopReason)
				}
				done <- err
			}()
			select {
			case <-sent:
			case <-time.After(time.Second):
				t.Fatal("fixture did not send terminal")
			}
			select {
			case err := <-done:
				if !test.overflow {
					if err != nil {
						t.Fatalf("full display buffer lost terminal: %v", err)
					}
					return
				}
				var failure *acp.RequestError
				if !errors.As(err, &failure) || failure.Data.(map[string]any)["native_status"] != "completed" {
					t.Fatalf("want delivery failure retaining native completed, got %v", err)
				}
			case <-time.After(time.Second):
				prompt := sessionActiveTurn(state)
				if prompt == nil {
					t.Fatal("fixture lost active turn before returning result")
				}
				prompt.eventsMu.Lock()
				id, terminal, overflow := prompt.confirmedTurnID(), prompt.terminal != nil, len(prompt.early.droppedUpdates) != 0
				prompt.eventsMu.Unlock()
				t.Fatalf("proof-bearing completed was received, but Prompt still waits for missing ACK; bound=%q terminal=%t overflow=%t", id, terminal, overflow)
			}
		})
	}
}

// TestUsageDoesNotCountDuplicateAfterForeignFrame checks a repeated cumulative observation after continuity breaks.
func TestUsageDoesNotCountDuplicateAfterForeignFrame(t *testing.T) {
	rpc := newFakeAppServerRPC()
	ready := make(chan struct{})
	rpc.handleCall = func(_ context.Context, request protocol.ClientRequest, result any) error {
		switch request.(type) {
		case protocol.InitializeRequest:
		case protocol.ThreadStartRequest:
			result.(*protocol.ThreadStartResponse).Thread.ID = "review-thread"
		case protocol.TurnStartRequest:
			result.(*protocol.TurnStartResponse).Turn.ID = "review-turn"
			close(ready)
		default:
			return fmt.Errorf("unexpected native method %s", request.Method())
		}
		return nil
	}
	agent := newRuntimeTestAgent(t, rpc)
	if _, err := agent.NewSession(context.Background(), acp.NewSessionRequest{Cwd: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	state, _ := agent.sessions.get("review-thread")
	result := make(chan acp.PromptResponse, 1)
	done := make(chan error, 1)
	go func() {
		response, err := agent.Prompt(ctx, acp.PromptRequest{SessionId: "review-thread", Prompt: []acp.ContentBlock{{Text: &acp.ContentBlockText{Type: "text", Text: "hi"}}}})
		result <- response
		done <- err
	}()
	select {
	case <-ready:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	select {
	case <-sessionActiveTurn(state).turnStarted:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	send := func(id string, last, total int64) {
		sample := usageSample(last, 0, total, 0)
		sample.ThreadID, sample.TurnID = "review-thread", id
		agent.client.HandleNotification(ctx, decodeTestNotification(t, protocol.MethodThreadTokenUsageUpdated, sample))
	}
	send("review-turn", 10, 10)
	send("other-turn", 5, 15)
	send("review-turn", 10, 10)
	send("review-turn", 7, 22)
	send("review-turn", 10, 10)
	send("review-turn", 7, 22)
	agent.client.HandleNotification(ctx, completeNotification(t, "review-thread", "review-turn", protocol.FluffyCompleted))
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	usage, ok := acpmeta.ReadTurnUsage((<-result).Meta)
	if !ok || usage.InputTokens == nil {
		t.Fatalf("missing usage: %+v", usage)
	}
	if *usage.InputTokens != 17 || usage.Complete {
		t.Fatalf("expected exactly two owned samples with incomplete coverage, got input=%d complete=%t", *usage.InputTokens, usage.Complete)
	}
}

// TestSessionOpenConfigReadPreservesNativeFailure checks a real preflight error.
func TestSessionOpenConfigReadPreservesNativeFailure(t *testing.T) {
	rpc := newFakeAppServerRPC()
	rpc.handleCall = func(_ context.Context, request protocol.ClientRequest, _ any) error {
		switch request.Method() {
		case protocol.MethodInitialize:
			return nil
		case protocol.MethodConfigRead:
			return &rpcError{Code: -32042, Message: "configuration rejected", Data: json.RawMessage(`{"reason":"policy"}`)}
		default:
			return fmt.Errorf("unexpected method %s", request.Method())
		}
	}
	agent := newRuntimeTestAgent(t, rpc)
	agent.defaultModeRequestUserInput = true
	_, err := agent.NewSession(context.Background(), acp.NewSessionRequest{Cwd: t.TempDir()})
	var failure *acp.RequestError
	if !errors.As(err, &failure) {
		t.Fatalf("expected ACP request failure, got %v", err)
	}
	wire, _ := json.Marshal(failure.Data)
	for _, want := range []string{`"phase":"config_read"`, `"code":-32042`, `"data":{"reason":"policy"}`} {
		if !strings.Contains(string(wire), want) {
			t.Fatalf("config failure lost %s: %s", want, wire)
		}
	}
}

// TestFailedTurnReportsNativeToolCompletionWithoutClaimingAnEffect checks both tool variants.
func TestFailedTurnReportsNativeToolCompletionWithoutClaimingAnEffect(t *testing.T) {
	for _, tool := range []map[string]any{
		{"type": "commandExecution", "id": "tool", "status": "completed", "command": "echo result"},
		{"type": "mcpToolCall", "id": "tool", "status": "failed", "server": "remote", "tool": "query"},
		{"type": "commandExecution", "id": "tool", "status": "declined", "command": "echo result"},
	} {
		t.Run(fmt.Sprintf("%s_%s", tool["type"], tool["status"]), func(t *testing.T) {
			rpc := newFakeAppServerRPC()
			rpc.handleCall = func(_ context.Context, request protocol.ClientRequest, result any) error {
				switch request.Method() {
				case protocol.MethodInitialize:
				case protocol.MethodThreadStart:
					result.(*protocol.ThreadStartResponse).Thread.ID = "thread"
				case protocol.MethodTurnStart:
					result.(*protocol.TurnStartResponse).Turn.ID = "turn"
				default:
					return fmt.Errorf("unexpected method %s", request.Method())
				}
				return nil
			}
			agent := newRuntimeTestAgent(t, rpc)
			rpc.afterObserved = func() {
				ctx := context.Background()
				agent.client.HandleNotification(ctx, decodeTestNotification(t, protocol.MethodItemCompleted,
					map[string]any{"threadId": "thread", "turnId": "turn", "item": tool}))
				agent.client.HandleNotification(ctx, completeNotification(t, "thread", "turn", protocol.Failed))
			}
			if _, err := agent.NewSession(context.Background(), acp.NewSessionRequest{Cwd: t.TempDir()}); err != nil {
				t.Fatal(err)
			}
			_, err := agent.Prompt(context.Background(), acp.PromptRequest{SessionId: "thread"})
			var failure *acp.RequestError
			if !errors.As(err, &failure) {
				t.Fatalf("failed native turn: %v", err)
			}
			data := failure.Data.(map[string]any)
			var wantToolCompletion any
			if tool["status"] != "declined" {
				wantToolCompletion = true
			}
			if data["tool_completion_observed"] != wantToolCompletion || data["file_change_observed"] != nil || data["output_observed"] != nil {
				t.Fatalf("unexpected tool and effect evidence: %+v", data)
			}
		})
	}
}

// TestThreadSettingsDuringOpenBelongToTheInstalledGeneration covers a notification on either side of the ACK.
func TestThreadSettingsDuringOpenBelongToTheInstalledGeneration(t *testing.T) {
	for _, stage := range []string{"start_before_ack", "fork_before_ack", "resume_before_ack", "load_during_read", "resume_during_catalog"} {
		t.Run(stage, func(t *testing.T) {
			rpc := newFakeAppServerRPC()
			var agent *Agent
			notify := func(ctx context.Context) {
				agent.client.HandleNotification(ctx, decodeTestNotification(t, "thread/settings/updated", map[string]any{
					"threadId": "settings", "threadSettings": map[string]any{"model": "newer", "effort": "medium", "serviceTier": nil},
				}))
			}
			rpc.handleCall = func(ctx context.Context, request protocol.ClientRequest, result any) error {
				switch request.Method() {
				case protocol.MethodInitialize:
					return nil
				case protocol.MethodThreadStart:
					notify(ctx)
					return json.Unmarshal([]byte(`{"thread":{"id":"settings"},"model":"old","reasoningEffort":"low","serviceTier":"old-tier"}`), result)
				case protocol.MethodThreadFork:
					notify(ctx)
					return json.Unmarshal([]byte(`{"thread":{"id":"settings"},"model":"old","reasoningEffort":"low","serviceTier":"old-tier"}`), result)
				case protocol.MethodThreadResume:
					if stage == "resume_before_ack" {
						notify(ctx)
					}
					return json.Unmarshal([]byte(`{"thread":{"id":"settings"},"model":"old","reasoningEffort":"low","serviceTier":"old-tier"}`), result)
				case protocol.MethodThreadRead:
					notify(ctx)
					return json.Unmarshal([]byte(`{"thread":{"id":"settings","turns":[]}}`), result)
				case protocol.MethodModelList:
					if stage == "resume_during_catalog" {
						notify(ctx)
					}
					result.(*protocol.ModelListResponse).Data = testModels()
					return nil
				default:
					return fmt.Errorf("unexpected method %s", request.Method())
				}
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			agent = newAgentWithClient(slog.Default(), ctx, cancel, newAppServerClient(ctx, rpc))
			initializeTestAgent(t, agent)
			cwd := t.TempDir()
			var err error
			switch stage {
			case "start_before_ack":
				_, err = agent.NewSession(ctx, acp.NewSessionRequest{Cwd: cwd})
			case "fork_before_ack":
				_, err = agent.UnstableForkSession(ctx, acp.UnstableForkSessionRequest{
					Cwd: cwd, SessionId: "source", Meta: map[string]any{"forkReceiptDirectory": t.TempDir()},
				})
			case "load_during_read":
				_, err = agent.LoadSession(ctx, acp.LoadSessionRequest{Cwd: cwd, SessionId: "settings"})
			default:
				_, err = agent.ResumeSession(ctx, acp.ResumeSessionRequest{Cwd: cwd, SessionId: "settings"})
			}
			if err != nil {
				t.Fatal(err)
			}
			result, err := agent.HandleExtensionMethod(ctx, "_codex/session/settings", json.RawMessage(`{"session_id":"settings"}`))
			if err != nil {
				t.Fatal(err)
			}
			wire, _ := json.Marshal(result)
			for _, want := range []string{`"model":"newer"`, `"reasoning_effort":"medium"`, `"service_tier":null`, `"source":"thread_settings_updated"`} {
				if !strings.Contains(string(wire), want) {
					t.Fatalf("settings lost %s: %s", want, wire)
				}
			}
		})
	}
}

// TestThreadArchiveReservationOutlivesCaller checks close and reopen while the native RPC is pending.
func TestThreadArchiveReservationOutlivesCaller(t *testing.T) {
	rpc := newFakeAppServerRPC()
	pending, release := make(chan struct{}), make(chan struct{})
	rpc.handleCall = func(ctx context.Context, request protocol.ClientRequest, result any) error {
		switch request.Method() {
		case protocol.MethodInitialize, protocol.MethodThreadUnsubscribe:
		case "thread/archive":
			close(pending)
			select {
			case <-release:
			case <-ctx.Done():
				return ctx.Err()
			}
		case protocol.MethodThreadResume:
			result.(*protocol.ThreadResumeResponse).Thread.ID = "saved"
		default:
			return fmt.Errorf("unexpected method %s", request.Method())
		}
		return nil
	}
	agent := newRuntimeTestAgent(t, rpc)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	callCtx, abandon := context.WithCancel(ctx)
	defer abandon()
	finished := make(chan error, 1)
	go func() {
		_, err := agent.HandleExtensionMethod(callCtx, "_codex/thread/archive", json.RawMessage(`{"thread_id":"saved"}`))
		finished <- err
	}()
	select {
	case <-pending:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	cwd := t.TempDir()
	checkBlocked := func() {
		t.Helper()
		if _, err := agent.CloseSession(ctx, acp.CloseSessionRequest{SessionId: "saved"}); err == nil {
			t.Error("close crossed an in-flight archive")
		}
		if _, err := agent.ResumeSession(ctx, acp.ResumeSessionRequest{Cwd: cwd, SessionId: "saved"}); err == nil {
			t.Error("resume crossed an in-flight archive")
		}
	}
	checkBlocked()
	abandon()
	select {
	case err := <-finished:
		if err == nil {
			t.Error("cancelled caller received success")
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	checkBlocked()
	close(release)
	for {
		_, err := agent.ResumeSession(ctx, acp.ResumeSessionRequest{Cwd: cwd, SessionId: "saved"})
		if err == nil {
			break
		}
		if ctx.Err() != nil {
			t.Fatalf("archive never released its lane: %v", err)
		}
		time.Sleep(time.Millisecond)
	}
}

// TestPreAckTerminalSealsUsageAtReceiveBoundary verifies both the sealed result and the next baseline.
func TestPreAckTerminalSealsUsageAtReceiveBoundary(t *testing.T) {
	for _, name := range []string{"retained_late_sample", "dropped_late_sample"} {
		t.Run(name, func(t *testing.T) {
			rpc := newFakeAppServerRPC()
			var agent *Agent
			starts := 0
			rpc.handleCall = func(ctx context.Context, request protocol.ClientRequest, result any) error {
				switch request := request.(type) {
				case protocol.InitializeRequest:
				case protocol.ThreadStartRequest:
					result.(*protocol.ThreadStartResponse).Thread.ID = "thread"
				case protocol.TurnStartRequest:
					starts++
					send := func(id string, last, total int64) {
						sample := usageSample(last, 0, total, 0)
						sample.ThreadID, sample.TurnID = "thread", id
						agent.client.HandleNotification(ctx, decodeTestNotification(t, protocol.MethodThreadTokenUsageUpdated, sample))
					}
					if starts == 2 {
						send("next", 7, 22)
						agent.client.HandleNotification(ctx, completeNotification(t, "thread", "next", protocol.FluffyCompleted))
						result.(*protocol.TurnStartResponse).Turn.ID = "next"
						break
					}
					send("turn", 10, 10)
					if name == "dropped_late_sample" {
						for range maxEarlyUsageSamples - 1 {
							send("turn", 10, 10)
						}
					}
					agent.client.HandleNotification(ctx, completeNotification(t, "thread", "turn", protocol.FluffyCompleted))
					send("other", 5, 15)
					result.(*protocol.TurnStartResponse).Turn.ID = "turn"
				default:
					return fmt.Errorf("unexpected method %s", request.Method())
				}
				return nil
			}
			agent = newRuntimeTestAgent(t, rpc)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if _, err := agent.NewSession(ctx, acp.NewSessionRequest{Cwd: t.TempDir()}); err != nil {
				t.Fatal(err)
			}
			checkUsage := func(want int64, complete bool) {
				t.Helper()
				response, err := agent.Prompt(ctx, acp.PromptRequest{SessionId: "thread"})
				if err != nil {
					t.Fatal(err)
				}
				usage, ok := acpmeta.ReadTurnUsage(response.Meta)
				if !ok || usage.InputTokens == nil || *usage.InputTokens != want || usage.Complete != complete {
					t.Fatalf("got %+v, want %d input, complete=%t", usage, want, complete)
				}
			}
			checkUsage(10, true)
			checkUsage(7, false)
		})
	}
}

// TestPreAckRetiredForeignUsageBreaksContinuity keeps late shared-counter evidence.
func TestPreAckRetiredForeignUsageBreaksContinuity(t *testing.T) {
	for _, name := range []string{"retired", "stale"} {
		t.Run(name, func(t *testing.T) {
			rpc := newFakeAppServerRPC()
			var agent *Agent
			rpc.handleCall = func(ctx context.Context, request protocol.ClientRequest, result any) error {
				switch request := request.(type) {
				case protocol.InitializeRequest:
				case protocol.ThreadStartRequest:
					result.(*protocol.ThreadStartResponse).Thread.ID = "s"
				case protocol.TurnStartRequest:
					send := func(id string, last, total int64) {
						sample := usageSample(last, 0, total, 0)
						sample.ThreadID, sample.TurnID = "s", id
						agent.client.HandleNotification(ctx, decodeTestNotification(t, protocol.MethodThreadTokenUsageUpdated, sample))
					}
					send("turn", 10, 10)
					if name == "retired" {
						agent.client.HandleNotification(ctx, completeNotification(t, "s", "other", protocol.FluffyCompleted))
					} else {
						agent.client.MarkTurnStale("s", "other")
					}
					send("other", 5, 15)
					send("turn", 7, 22)
					agent.client.HandleNotification(ctx, completeNotification(t, "s", "turn", protocol.FluffyCompleted))
					result.(*protocol.TurnStartResponse).Turn.ID = "turn"
				default:
					return fmt.Errorf("unexpected method %s", request.Method())
				}
				return nil
			}
			agent = newRuntimeTestAgent(t, rpc)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if _, err := agent.NewSession(ctx, acp.NewSessionRequest{Cwd: t.TempDir()}); err != nil {
				t.Fatal(err)
			}
			response, err := agent.Prompt(ctx, acp.PromptRequest{SessionId: "s"})
			if err != nil {
				t.Fatal(err)
			}
			usage, ok := acpmeta.ReadTurnUsage(response.Meta)
			if !ok || usage.InputTokens == nil {
				t.Fatalf("missing usage: %+v", usage)
			}
			if *usage.InputTokens != 17 || usage.Complete {
				t.Fatalf("retired foreign frame polluted own usage: input=%d complete=%t, want 17 partial", *usage.InputTokens, usage.Complete)
			}
		})
	}
}

// TestFailedPatchReportsToolCompletionWithoutEffect distinguishes a terminal attempt from a proven file change.
func TestFailedPatchReportsToolCompletionWithoutEffect(t *testing.T) {
	for _, source := range []string{"item_completed", "terminal_snapshot"} {
		t.Run(source, func(t *testing.T) {
			rpc := newFakeAppServerRPC()
			rpc.handleCall = func(_ context.Context, request protocol.ClientRequest, result any) error {
				switch request.Method() {
				case protocol.MethodInitialize:
				case protocol.MethodThreadStart:
					result.(*protocol.ThreadStartResponse).Thread.ID = "s"
				case protocol.MethodTurnStart:
					result.(*protocol.TurnStartResponse).Turn.ID = "turn"
				default:
					return fmt.Errorf("unexpected method %s", request.Method())
				}
				return nil
			}
			agent := newRuntimeTestAgent(t, rpc)
			rpc.afterObserved = func() {
				ctx := context.Background()
				item := map[string]any{"type": "fileChange", "id": "patch", "status": "failed",
					"changes": []any{map[string]any{"path": "example.txt", "diff": "-old\n+new", "kind": map[string]any{"type": "update"}}}}
				terminal := completeNotification(t, "s", "turn", protocol.Failed).(*protocol.TurnCompletedEnvelope)
				if source == "terminal_snapshot" {
					itemJSON, err := json.Marshal(item)
					if err != nil {
						t.Error(err)
					}
					var patch protocol.ThreadItem
					if err := json.Unmarshal(itemJSON, &patch); err != nil {
						t.Error(err)
					}
					terminal.Params.Turn.Items = []protocol.ThreadItem{patch}
				} else {
					agent.client.HandleNotification(ctx, decodeTestNotification(t, protocol.MethodItemCompleted,
						map[string]any{"threadId": "s", "turnId": "turn", "item": item}))
				}
				agent.client.HandleNotification(ctx, terminal)
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if _, err := agent.NewSession(ctx, acp.NewSessionRequest{Cwd: t.TempDir()}); err != nil {
				t.Fatal(err)
			}
			_, err := agent.Prompt(ctx, acp.PromptRequest{SessionId: "s"})
			var failure *acp.RequestError
			if !errors.As(err, &failure) {
				t.Fatalf("expected native turn failure, got %v", err)
			}
			facts := failure.Data.(map[string]any)
			if facts["tool_completion_observed"] != true || facts["file_change_observed"] != nil {
				t.Fatalf("expected terminal patch attempt without proven file effect, got %+v", facts)
			}
		})
	}
}

// TestPreAckUpdateAndUsageBudgetsKeepIndependentFacts exercises both limits through Prompt.
func TestPreAckUpdateAndUsageBudgetsKeepIndependentFacts(t *testing.T) {
	for _, test := range []struct {
		// name selects the native frame sequence.
		name string
		// input is the owned usage preserved in the terminal.
		input int64
		// complete is whether the result covers all native usage.
		complete bool
		// deliveryError matches the loss classification when delivery fails.
		deliveryError string
	}{
		{name: "retired_usage", input: 17},
		{name: "stale_usage", input: 17},
		{name: "usage_budget", input: maxEarlyUsageSamples},
		{name: "foreign_updates", input: 10, complete: true},
		{name: "unprojected_items", input: 10, complete: true},
		{name: "owned_update_loss", deliveryError: "queue is full"},
		{name: "unattributed_update_loss", deliveryError: "could not establish"},
	} {
		t.Run(test.name, func(t *testing.T) {
			rpc := newFakeAppServerRPC()
			var agent *Agent
			rpc.handleCall = func(ctx context.Context, request protocol.ClientRequest, result any) error {
				switch request := request.(type) {
				case protocol.InitializeRequest:
				case protocol.ThreadStartRequest:
					result.(*protocol.ThreadStartResponse).Thread.ID = "thread"
				case protocol.TurnStartRequest:
					sendUsage := func(id string, last, total int64) {
						sample := usageSample(last, 0, total, 0)
						sample.ThreadID, sample.TurnID = "thread", id
						agent.client.HandleNotification(ctx, decodeTestNotification(t, protocol.MethodThreadTokenUsageUpdated, sample))
					}
					sendUpdate := func(id, delta string) {
						agent.client.HandleNotification(ctx, decodeTestNotification(t, protocol.MethodAgentMessageDelta,
							map[string]any{"threadId": "thread", "turnId": id, "itemId": "display", "delta": delta}))
					}
					sendUpdate("turn", "owned")
					switch test.name {
					case "retired_usage", "stale_usage":
						sendUsage("turn", 10, 10)
						if test.name == "retired_usage" {
							agent.client.HandleNotification(ctx, completeNotification(t, "thread", "foreign", protocol.FluffyCompleted))
						} else {
							agent.client.MarkTurnStale("thread", "foreign")
						}
						for range 2 * maxEarlyTurnUpdates {
							sendUsage("foreign", 5, 15)
						}
						sendUsage("turn", 7, 22)
						sendUpdate("turn", " after flood")
					case "usage_budget":
						for i := range maxEarlyUsageSamples + 1 {
							sendUsage("turn", 1, int64(i+1))
						}
						sendUpdate("turn", " after flood")
					case "foreign_updates":
						for range 2 * maxEarlyTurnUpdates {
							sendUpdate("foreign", "not owned")
						}
						sendUsage("turn", 10, 10)
					case "unprojected_items":
						for range 2 * maxEarlyTurnUpdates {
							agent.client.HandleNotification(ctx, decodeTestNotification(t, protocol.MethodItemCompleted,
								map[string]any{"threadId": "thread", "turnId": "turn", "item": map[string]any{"type": "sleep", "id": "sleep"}}))
						}
						sendUpdate("turn", " after ignored items")
						sendUsage("turn", 10, 10)
					case "owned_update_loss":
						for range maxEarlyTurnUpdates {
							sendUpdate("turn", "more")
						}
					case "unattributed_update_loss":
						for i := range maxEarlyTurnUpdates + maxEarlyDroppedTurnIDs + 1 {
							sendUpdate(fmt.Sprintf("candidate-%d", i), "unconfirmed")
						}
					}
					agent.client.HandleNotification(ctx, completeNotification(t, "thread", "turn", protocol.FluffyCompleted))
					result.(*protocol.TurnStartResponse).Turn.ID = "turn"
				default:
					return fmt.Errorf("unexpected method %s", request.Method())
				}
				return nil
			}
			agent = newRuntimeTestAgent(t, rpc)
			updater := &recordingSessionUpdater{}
			agent.sessionUpdater = updater
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if _, err := agent.NewSession(ctx, acp.NewSessionRequest{Cwd: t.TempDir()}); err != nil {
				t.Fatal(err)
			}
			response, err := agent.Prompt(ctx, acp.PromptRequest{SessionId: "thread"})
			if test.deliveryError != "" {
				var failure *acp.RequestError
				if !errors.As(err, &failure) || !strings.Contains(failure.Message, test.deliveryError) {
					t.Fatalf("expected delivery failure %q, got %v", test.deliveryError, err)
				}
				data := failure.Data.(map[string]any)
				if data["phase"] != "event_delivery" || data["native_status"] != "completed" {
					t.Fatalf("delivery failure lost terminal facts: %+v", data)
				}
				return
			}
			if err != nil || response.StopReason != acp.StopReasonEndTurn {
				t.Fatalf("native success changed by independent budget, got %+v / %v", response, err)
			}
			usage, ok := acpmeta.ReadTurnUsage(response.Meta)
			if !ok || usage.InputTokens == nil || *usage.InputTokens != test.input || usage.Complete != test.complete {
				t.Fatalf("got %+v, want input=%d complete=%t", usage, test.input, test.complete)
			}
			updates, err := json.Marshal(updater.notifications)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(updates), `"text":"owned"`) || strings.Contains(string(updates), "not owned") ||
				!test.complete && !strings.Contains(string(updates), "after flood") ||
				test.name == "unprojected_items" && !strings.Contains(string(updates), "after ignored items") {
				t.Fatalf("unexpected delivered output: %s", updates)
			}
		})
	}
}

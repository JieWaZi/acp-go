package codex

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"acp-go/agents/codex/protocol"
	acp "github.com/coder/acp-go-sdk"
)

// newRuntimeTestAgent 使用 fake typed client 创建已完成 connection barrier 的 Agent。
func newRuntimeTestAgent(t *testing.T, rpc *fakeAppServerRPC) *Agent {
	t.Helper()
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

// TestAgentNewAndLoadSessionUseUpstreamThreadFlow 验证 new 与 load 的 thread/start、resume→read 顺序。
func TestAgentNewAndLoadSessionUseUpstreamThreadFlow(t *testing.T) {
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
	if got := rpc.calls; len(got) != 4 || got[1] != protocol.MethodThreadStart ||
		got[2] != protocol.MethodThreadResume || got[3] != protocol.MethodThreadRead {
		t.Fatalf("请求顺序为 %v", got)
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
			result.(*protocol.ThreadStartResponse).Thread.ID = "thread-1"
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
	backgroundPrompt := sessionActivePrompt(state)
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
	select {
	case <-interruptCalled:
		t.Fatal("turn/start 返回前不应发送 interrupt")
	default:
	}
	if active := sessionActivePrompt(state); active != nil {
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
	backgroundPrompt := sessionActivePrompt(state)
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
	if !ok || sessionActivePrompt(state) != nil {
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
			result.(*protocol.ThreadStartResponse).Thread.ID = "thread-1"
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

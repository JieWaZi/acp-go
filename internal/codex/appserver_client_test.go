package codex

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"acp-go/agents/codex/protocol"

	acp "github.com/coder/acp-go-sdk"
)

// fakeAppServerRPC 为 typed client 测试提供可编排的请求/通知边界。
type fakeAppServerRPC struct {
	// mu 保护 calls 与 notifications。
	mu sync.Mutex
	// calls 记录请求方法顺序。
	calls []string
	// notifications 记录客户端通知方法顺序。
	notifications []string
	// handleCall 由测试按请求类型填充响应或插入乱序通知。
	handleCall func(ctx context.Context, request protocol.ClientRequest, result any) error
	// done 模拟 transport fatal 通道。
	done chan struct{}
	// err 模拟 transport 稳定错误。
	err error
}

// observedTurnStartRPC 验证 RunTurn 会消费 transport 提供的同步 response observer 小接口。
type observedTurnStartRPC struct {
	// done 模拟运行中的 transport。
	done chan struct{}
	// observed 在 turn identity observer 已执行时关闭。
	observed chan struct{}
}

// Call 拒绝普通调用，确保被测 RunTurn 不能绕过同步 observer。
func (f *observedTurnStartRPC) Call(
	context.Context,
	func(protocol.RequestID) protocol.ClientRequest,
	any,
) error {
	return errors.New("RunTurn used ordinary Call instead of CallObserved")
}

// CallObserved 填充 turn/start result 后同步执行 observer。
func (f *observedTurnStartRPC) CallObserved(
	_ context.Context,
	build func(protocol.RequestID) protocol.ClientRequest,
	result any,
	observer func() error,
) error {
	idNumber := int64(1)
	if request := build(protocol.RequestID{Integer: &idNumber}); request.Method() != protocol.MethodTurnStart {
		return errors.New("observed call was not turn/start")
	}
	result.(*protocol.TurnStartResponse).Turn = protocol.TurnElement{
		ID: "turn-observed", Items: []protocol.ThreadItem{}, Status: protocol.PurpleInProgress,
	}
	if err := observer(); err != nil {
		return err
	}
	close(f.observed)
	return nil
}

// Notify 满足 appServerRPC；该测试不发送客户端通知。
func (f *observedTurnStartRPC) Notify(context.Context, protocol.ClientNotification) error {
	return nil
}

// Done 返回模拟 transport fatal 通道。
func (f *observedTurnStartRPC) Done() <-chan struct{} {
	return f.done
}

// Err 返回运行中 transport 的空 fatal 错误。
func (f *observedTurnStartRPC) Err() error {
	return nil
}

// newFakeAppServerRPC 创建尚未 fatal 的 fake RPC。
func newFakeAppServerRPC() *fakeAppServerRPC {
	return &fakeAppServerRPC{done: make(chan struct{})}
}

// Call 构造强类型请求并交给测试回调。
func (f *fakeAppServerRPC) Call(
	ctx context.Context,
	build func(protocol.RequestID) protocol.ClientRequest,
	result any,
) error {
	idNumber := int64(1)
	request := build(protocol.RequestID{Integer: &idNumber})
	f.mu.Lock()
	f.calls = append(f.calls, request.Method())
	f.mu.Unlock()
	if f.handleCall == nil {
		return errors.New("unexpected fake app-server call")
	}
	return f.handleCall(ctx, request, result)
}

// Notify 记录 typed client 发出的 initialized 通知。
func (f *fakeAppServerRPC) Notify(_ context.Context, notification protocol.ClientNotification) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.notifications = append(f.notifications, notification.Method())
	return nil
}

// Done 返回 fake fatal 通道。
func (f *fakeAppServerRPC) Done() <-chan struct{} {
	return f.done
}

// Err 返回 fake fatal 错误。
func (f *fakeAppServerRPC) Err() error {
	return f.err
}

// completeNotification 创建通过 protocol discriminator 解码的 turn/completed 通知。
func completeNotification(t *testing.T, threadID, turnID string, status protocol.TurnStatus) protocol.ServerNotification {
	t.Helper()
	data, err := json.Marshal(map[string]any{
		"method": protocol.MethodTurnCompleted,
		"params": map[string]any{
			"threadId": threadID,
			"turn": map[string]any{
				"id": turnID, "items": []any{}, "status": status,
			},
		},
	})
	if err != nil {
		t.Fatalf("编码 completion fixture 失败: %v", err)
	}
	notification, err := protocol.DecodeServerNotification(data)
	if err != nil {
		t.Fatalf("解码 completion fixture 失败: %v", err)
	}
	return notification
}

// TestAppServerClientInitializesOnce 验证并发 ACP initialize 只触发一次 app-server initialize/initialized。
func TestAppServerClientInitializesOnce(t *testing.T) {
	t.Parallel()
	rpc := newFakeAppServerRPC()
	rpc.handleCall = func(_ context.Context, request protocol.ClientRequest, result any) error {
		if request.Method() != protocol.MethodInitialize {
			t.Fatalf("请求方法为 %q", request.Method())
		}
		response := result.(*protocol.InitializeResponse)
		response.CodexHome = "/tmp/codex-home"
		return nil
	}
	client := newAppServerClient(context.Background(), rpc)

	var group sync.WaitGroup
	errorsResult := make(chan error, 2)
	for range 2 {
		group.Add(1)
		go func() {
			defer group.Done()
			_, err := client.Initialize(context.Background(), protocol.ClientInfo{Name: "acp-client", Version: "1"})
			errorsResult <- err
		}()
	}
	group.Wait()
	close(errorsResult)
	for err := range errorsResult {
		if err != nil {
			t.Fatalf("initialize 返回错误: %v", err)
		}
	}
	if len(rpc.calls) != 1 || rpc.calls[0] != protocol.MethodInitialize {
		t.Fatalf("initialize 调用为 %v", rpc.calls)
	}
	if len(rpc.notifications) != 1 || rpc.notifications[0] != protocol.MethodInitialized {
		t.Fatalf("initialized 通知为 %v", rpc.notifications)
	}
}

// TestRunTurnCapturesCompletionBeforeStartResponse 验证固定上游 runTurn 的 early completion 捕获窗口。
func TestRunTurnCapturesCompletionBeforeStartResponse(t *testing.T) {
	t.Parallel()
	rpc := newFakeAppServerRPC()
	client := newAppServerClient(context.Background(), rpc)
	rpc.handleCall = func(_ context.Context, request protocol.ClientRequest, result any) error {
		if request.Method() != protocol.MethodTurnStart {
			t.Fatalf("请求方法为 %q", request.Method())
		}
		client.HandleNotification(context.Background(), completeNotification(t, "thread-1", "turn-fast", protocol.FluffyCompleted))
		response := result.(*protocol.TurnStartResponse)
		response.Turn = protocol.TurnElement{ID: "turn-fast", Items: []protocol.ThreadItem{}, Status: protocol.PurpleInProgress}
		return nil
	}

	completion, err := client.RunTurn(context.Background(), protocol.TurnStartParams{
		ThreadID: "thread-1",
		Input:    []protocol.InputElement{},
	}, nil)
	if err != nil {
		t.Fatalf("RunTurn 返回错误: %v", err)
	}
	if completion.Turn.ID != "turn-fast" {
		t.Fatalf("completion 为 %#v", completion)
	}
}

// TestResolveTurnInterruptedClearsStaleAfterEarlyCompletion 验证真实 completion 已提前到达时，
// 迟到 start 的合成 interrupted 清理不会永久保留 stale turn identity。
func TestResolveTurnInterruptedClearsStaleAfterEarlyCompletion(t *testing.T) {
	t.Parallel()
	rpc := newFakeAppServerRPC()
	client := newAppServerClient(context.Background(), rpc)
	rpc.handleCall = func(_ context.Context, request protocol.ClientRequest, result any) error {
		if request.Method() != protocol.MethodTurnStart {
			t.Fatalf("请求方法为 %q", request.Method())
		}
		client.HandleNotification(context.Background(), completeNotification(
			t, "thread-early-stale", "turn-early-stale", protocol.FluffyInterrupted,
		))
		result.(*protocol.TurnStartResponse).Turn = protocol.TurnElement{
			ID: "turn-early-stale", Items: []protocol.ThreadItem{}, Status: protocol.PurpleInProgress,
		}
		return nil
	}

	_, err := client.RunTurn(context.Background(), protocol.TurnStartParams{
		ThreadID: "thread-early-stale",
		Input:    []protocol.InputElement{},
	}, func(turnID string) {
		client.MarkTurnStale("thread-early-stale", turnID)
		client.ResolveTurnInterrupted("thread-early-stale", turnID)
	})
	if err != nil {
		t.Fatalf("RunTurn 返回错误: %v", err)
	}
	client.handlerMu.Lock()
	defer client.handlerMu.Unlock()
	if turns := client.staleTurns["thread-early-stale"]; len(turns) != 0 {
		t.Fatalf("合成 completion 后仍保留 stale turns: %v", turns)
	}
}

// TestRunTurnActivatesIdentityInsideObservedResponse 验证 turn/start identity 在 transport 读取下一帧前安装。
func TestRunTurnActivatesIdentityInsideObservedResponse(t *testing.T) {
	t.Parallel()
	rpc := &observedTurnStartRPC{done: make(chan struct{}), observed: make(chan struct{})}
	client := newAppServerClient(context.Background(), rpc)
	started := make(chan string, 1)
	result := make(chan error, 1)
	go func() {
		_, err := client.RunTurn(context.Background(), protocol.TurnStartParams{
			ThreadID: "thread-observed",
			Input:    []protocol.InputElement{},
		}, func(turnID string) {
			started <- turnID
		})
		result <- err
	}()

	select {
	case <-rpc.observed:
	case err := <-result:
		t.Fatalf("RunTurn 未使用同步 response observer: %v", err)
	}
	if turnID := <-started; turnID != "turn-observed" {
		t.Fatalf("同步 observer 收到 turn ID %q", turnID)
	}
	client.HandleNotification(context.Background(), completeNotification(
		t, "thread-observed", "turn-observed", protocol.FluffyCompleted,
	))
	if err := <-result; err != nil {
		t.Fatalf("RunTurn 返回错误: %v", err)
	}
}

// TestRunTurnMatchesThreadAndTurn 验证旧 turn 与跨 session completion 不会结束当前等待者。
func TestRunTurnMatchesThreadAndTurn(t *testing.T) {
	t.Parallel()
	rpc := newFakeAppServerRPC()
	client := newAppServerClient(context.Background(), rpc)
	startReturned := make(chan struct{})
	rpc.handleCall = func(_ context.Context, _ protocol.ClientRequest, result any) error {
		response := result.(*protocol.TurnStartResponse)
		response.Turn = protocol.TurnElement{ID: "turn-new", Items: []protocol.ThreadItem{}, Status: protocol.PurpleInProgress}
		close(startReturned)
		return nil
	}

	result := make(chan protocol.TurnCompletedNotification, 1)
	errResult := make(chan error, 1)
	go func() {
		completion, err := client.RunTurn(context.Background(), protocol.TurnStartParams{
			ThreadID: "thread-1", Input: []protocol.InputElement{},
		}, nil)
		result <- completion
		errResult <- err
	}()
	<-startReturned
	client.HandleNotification(context.Background(), completeNotification(t, "thread-1", "turn-old", protocol.FluffyCompleted))
	client.HandleNotification(context.Background(), completeNotification(t, "thread-2", "turn-new", protocol.FluffyCompleted))
	select {
	case completion := <-result:
		t.Fatalf("错误 completion 提前结束等待: %#v", completion)
	default:
	}
	client.HandleNotification(context.Background(), completeNotification(t, "thread-1", "turn-new", protocol.FluffyCompleted))
	if err := <-errResult; err != nil {
		t.Fatalf("RunTurn 返回错误: %v", err)
	}
	if completion := <-result; completion.Turn.ID != "turn-new" {
		t.Fatalf("completion 为 %#v", completion)
	}
}

// TestRunTurnFatalUnblocksCompletion 验证 transport fatal 会解除已建立的 completion 等待者。
func TestRunTurnFatalUnblocksCompletion(t *testing.T) {
	t.Parallel()
	rpc := newFakeAppServerRPC()
	client := newAppServerClient(context.Background(), rpc)
	startReturned := make(chan struct{})
	rpc.handleCall = func(_ context.Context, _ protocol.ClientRequest, result any) error {
		result.(*protocol.TurnStartResponse).Turn = protocol.TurnElement{
			ID: "turn-1", Items: []protocol.ThreadItem{}, Status: protocol.PurpleInProgress,
		}
		close(startReturned)
		return nil
	}
	errResult := make(chan error, 1)
	go func() {
		_, err := client.RunTurn(context.Background(), protocol.TurnStartParams{
			ThreadID: "thread-1", Input: []protocol.InputElement{},
		}, nil)
		errResult <- err
	}()
	<-startReturned
	rpc.err = ErrAppServerUnavailable
	close(rpc.done)
	if err := <-errResult; !errors.Is(err, ErrAppServerUnavailable) {
		t.Fatalf("fatal 后 RunTurn 错误为 %v", err)
	}
}

// TestAppServerClientAuthSubscriptionsRespectGenerationAndFatal 验证共享认证通知不会跨代串线。
// 若已关闭订阅仍接收后继通知、重复通知被下一代消费或 fatal 未解除 waiter，本测试应失败。
func TestAppServerClientAuthSubscriptionsRespectGenerationAndFatal(t *testing.T) {
	t.Parallel()

	t.Run("release and duplicate", func(t *testing.T) {
		t.Parallel()
		rpc := newFakeAppServerRPC()
		runtimeCtx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)
		client := newAppServerClient(runtimeCtx, rpc)

		closed, err := client.SubscribeLoginCompleted()
		if err != nil {
			t.Fatalf("创建首个登录订阅失败: %v", err)
		}
		closed.Close()
		if _, err = closed.Wait(context.Background()); !errors.Is(err, context.Canceled) {
			t.Fatalf("已释放订阅错误为 %v，期望 context.Canceled", err)
		}

		first, err := client.SubscribeLoginCompleted()
		if err != nil {
			t.Fatalf("创建第一代登录订阅失败: %v", err)
		}
		client.HandleNotification(context.Background(), decodeTestNotification(
			t,
			protocol.MethodAccountLoginCompleted,
			protocol.AccountLoginCompletedNotification{Success: true, LoginID: acp.Ptr("first")},
		))
		client.HandleNotification(context.Background(), decodeTestNotification(
			t,
			protocol.MethodAccountLoginCompleted,
			protocol.AccountLoginCompletedNotification{Success: false, LoginID: acp.Ptr("duplicate")},
		))
		firstResult, err := first.Wait(context.Background())
		first.Close()
		if err != nil || firstResult.LoginID == nil || *firstResult.LoginID != "first" {
			t.Fatalf("第一代通知为 %#v, %v", firstResult, err)
		}

		second, err := client.SubscribeLoginCompleted()
		if err != nil {
			t.Fatalf("创建第二代登录订阅失败: %v", err)
		}
		client.HandleNotification(context.Background(), decodeTestNotification(
			t,
			protocol.MethodAccountLoginCompleted,
			protocol.AccountLoginCompletedNotification{Success: true, LoginID: acp.Ptr("second")},
		))
		secondResult, err := second.Wait(context.Background())
		second.Close()
		if err != nil || secondResult.LoginID == nil || *secondResult.LoginID != "second" {
			t.Fatalf("第二代通知为 %#v, %v", secondResult, err)
		}
	})

	t.Run("transport fatal", func(t *testing.T) {
		t.Parallel()
		rpc := newFakeAppServerRPC()
		runtimeCtx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)
		client := newAppServerClient(runtimeCtx, rpc)
		login, err := client.SubscribeLoginCompleted()
		if err != nil {
			t.Fatalf("创建登录订阅失败: %v", err)
		}
		account := client.SubscribeAccountUpdated()
		fatalErr := errors.New("fake app-server exited")
		rpc.err = fatalErr
		close(rpc.done)
		if _, err = login.Wait(context.Background()); !errors.Is(err, fatalErr) {
			t.Fatalf("登录 fatal 错误为 %v", err)
		}
		if err = account.Wait(context.Background()); !errors.Is(err, fatalErr) {
			t.Fatalf("logout fatal 错误为 %v", err)
		}
		login.Close()
		account.Close()
	})
}

// TestAppServerClientRejectsRepeatedModelCursor 验证破损分页不会无界请求和累积模型。
func TestAppServerClientRejectsRepeatedModelCursor(t *testing.T) {
	t.Parallel()
	rpc := newFakeAppServerRPC()
	calls := 0
	rpc.handleCall = func(_ context.Context, request protocol.ClientRequest, result any) error {
		if request.Method() != protocol.MethodModelList {
			return errors.New("unexpected call: " + request.Method())
		}
		calls++
		if calls > 2 {
			return errors.New("model/list pagination was not bounded")
		}
		cursor := "same-cursor"
		response := result.(*protocol.ModelListResponse)
		response.Data = testModels()[:1]
		response.NextCursor = &cursor
		return nil
	}
	runtimeCtx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	client := newAppServerClient(runtimeCtx, rpc)

	_, err := client.ListModels(context.Background())
	if err == nil || !strings.Contains(err.Error(), "repeated model/list cursor") {
		t.Fatalf("重复 cursor 错误为 %v", err)
	}
	if calls != 2 {
		t.Fatalf("model/list 调用了 %d 次，期望在第二页停止", calls)
	}
}

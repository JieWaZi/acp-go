package codex

import (
	"context"
	"errors"
	"sync"

	"acp-go/agents/codex/protocol"
)

const maxCapturedCompletionsPerTurnStart = 64

// appServerRPC 是 typed client 消费的最小传输接口，不泄漏进程或 ACP 生命周期。
type appServerRPC interface {
	// Call 发送由 protocol 构造器创建的请求并解码 result。
	Call(
		ctx context.Context,
		build func(protocol.RequestID) protocol.ClientRequest,
		result any,
	) error
	// Notify 发送由 protocol 包封闭的客户端通知。
	Notify(ctx context.Context, notification protocol.ClientNotification) error
	// Done 在传输永久失效时关闭。
	Done() <-chan struct{}
	// Err 返回传输的稳定 fatal 错误。
	Err() error
}

// observedResponseRPC 是真实 transport 为严格响应顺序提供的可选小接口。
// fake RPC 不必实现；RunTurn 才需要在 turn/start 响应与下一帧之间同步安装 identity。
type observedResponseRPC interface {
	// CallObserved 解码 result 后同步执行 observer，再允许 transport 继续处理后续帧。
	CallObserved(
		ctx context.Context,
		build func(protocol.RequestID) protocol.ClientRequest,
		result any,
		observer func() error,
	) error
}

// completionResult 保存一个 turn/completed 或解除等待的 fatal 错误。
type completionResult struct {
	// notification 是匹配 thread/turn 的完成通知。
	notification protocol.TurnCompletedNotification
	// err 是 context、进程或 transport 失败。
	err error
}

// completionCapture 保存 turn/start 响应前同 thread 到达的有界 completion。
type completionCapture struct {
	// events 按到达顺序保存捕获窗口内的完成通知。
	events []protocol.TurnCompletedNotification
}

// completionTracker 等价移植 CodexAppServerClient.runTurn、captureTurnCompletions 与 recordTurnCompleted。
// Go 版本用同一互斥锁原子完成“检查 early→安装 waiter”，补足 JS 单事件循环隐含的无间隙语义。
type completionTracker struct {
	// mu 保护 captures、waiters 和 fatalErr。
	mu sync.Mutex
	// captures 按 thread 保存当前 turn/start 捕获窗口。
	captures map[string]map[*completionCapture]struct{}
	// waiters 按 thread 再按 turn 保存唯一完成等待者。
	waiters map[string]map[string]chan completionResult
	// fatalErr 是解除所有当前及后续等待者的稳定错误。
	fatalErr error
}

// newCompletionTracker 创建空的完成路由器。
func newCompletionTracker() *completionTracker {
	return &completionTracker{
		captures: make(map[string]map[*completionCapture]struct{}),
		waiters:  make(map[string]map[string]chan completionResult),
	}
}

// beginCapture 在发送 turn/start 之前安装 thread 级捕获窗口。
func (t *completionTracker) beginCapture(threadID string) (*completionCapture, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.fatalErr != nil {
		return nil, t.fatalErr
	}
	capture := &completionCapture{}
	captures := t.captures[threadID]
	if captures == nil {
		captures = make(map[*completionCapture]struct{})
		t.captures[threadID] = captures
	}
	captures[capture] = struct{}{}
	return capture, nil
}

// waitAfterStart 原子查找 early completion 或把捕获窗口转换为精确 turn waiter。
func (t *completionTracker) waitAfterStart(
	ctx context.Context,
	threadID string,
	turnID string,
	capture *completionCapture,
) (protocol.TurnCompletedNotification, error) {
	channel := make(chan completionResult, 1)
	t.mu.Lock()
	if t.fatalErr != nil {
		err := t.fatalErr
		t.releaseCaptureLocked(threadID, capture)
		t.mu.Unlock()
		return protocol.TurnCompletedNotification{}, err
	}
	for _, event := range capture.events {
		if event.Turn.ID == turnID {
			t.releaseCaptureLocked(threadID, capture)
			t.mu.Unlock()
			return event, nil
		}
	}
	t.releaseCaptureLocked(threadID, capture)
	threadWaiters := t.waiters[threadID]
	if threadWaiters == nil {
		threadWaiters = make(map[string]chan completionResult)
		t.waiters[threadID] = threadWaiters
	}
	threadWaiters[turnID] = channel
	t.mu.Unlock()

	select {
	case result := <-channel:
		return result.notification, result.err
	case <-ctx.Done():
		t.removeWaiter(threadID, turnID, channel)
		return protocol.TurnCompletedNotification{}, ctx.Err()
	}
}

// record 先完成精确 waiter；若 turn ID 尚未知则广播给同 thread 捕获窗口。
func (t *completionTracker) record(event protocol.TurnCompletedNotification) {
	t.mu.Lock()
	threadWaiters := t.waiters[event.ThreadID]
	if channel := threadWaiters[event.Turn.ID]; channel != nil {
		delete(threadWaiters, event.Turn.ID)
		if len(threadWaiters) == 0 {
			delete(t.waiters, event.ThreadID)
		}
		t.mu.Unlock()
		channel <- completionResult{notification: event}
		return
	}
	for capture := range t.captures[event.ThreadID] {
		if len(capture.events) == maxCapturedCompletionsPerTurnStart {
			copy(capture.events, capture.events[1:])
			capture.events = capture.events[:len(capture.events)-1]
		}
		capture.events = append(capture.events, event)
	}
	t.mu.Unlock()
}

// releaseCapture 删除失败 turn/start 留下的捕获窗口。
func (t *completionTracker) releaseCapture(threadID string, capture *completionCapture) {
	t.mu.Lock()
	t.releaseCaptureLocked(threadID, capture)
	t.mu.Unlock()
}

// releaseCaptureLocked 在已持锁时删除捕获窗口。
func (t *completionTracker) releaseCaptureLocked(threadID string, capture *completionCapture) {
	captures := t.captures[threadID]
	delete(captures, capture)
	if len(captures) == 0 {
		delete(t.captures, threadID)
	}
}

// removeWaiter 只删除仍指向当前调用 channel 的 waiter，避免误删后继等待者。
func (t *completionTracker) removeWaiter(threadID, turnID string, channel chan completionResult) {
	t.mu.Lock()
	defer t.mu.Unlock()
	threadWaiters := t.waiters[threadID]
	if threadWaiters[turnID] == channel {
		delete(threadWaiters, turnID)
		if len(threadWaiters) == 0 {
			delete(t.waiters, threadID)
		}
	}
}

// fail 使用同一稳定错误解除所有 completion waiter，并拒绝后续捕获。
func (t *completionTracker) fail(err error) {
	if err == nil {
		err = ErrAppServerUnavailable
	}
	t.mu.Lock()
	if t.fatalErr != nil {
		t.mu.Unlock()
		return
	}
	t.fatalErr = err
	waiters := t.waiters
	t.waiters = make(map[string]map[string]chan completionResult)
	t.captures = make(map[string]map[*completionCapture]struct{})
	t.mu.Unlock()
	for _, threadWaiters := range waiters {
		for _, channel := range threadWaiters {
			channel <- completionResult{err: err}
		}
	}
}

// appServerClient 为当前 V1 主链路提供生成 DTO 的窄 typed client。
// 文件职责直接对应 upstream CodexAppServerClient.ts，未引入统一大 Runtime 接口。
type appServerClient struct {
	// rpc 是无 jsonrpc NDJSON 传输。
	rpc appServerRPC
	// completions 管理 early/stale/fatal completion 路由。
	completions *completionTracker
	// initializeOnce 保证一个 app-server 连接只握手一次。
	initializeOnce sync.Once
	// initializeResponse 保存首次握手结果供并发调用复用。
	initializeResponse protocol.InitializeResponse
	// initializeErr 保存首次握手失败供并发调用复用。
	initializeErr error
	// handlerMu 保护通知消费方和 stale 集合。
	handlerMu sync.Mutex
	// handler 接收 completion 记录与 stale 过滤之后的通知。
	handler notificationHandler
	// staleTurns 按 thread/turn 标记取消后只用于清理、不再路由的旧 turn。
	staleTurns map[string]map[string]struct{}
}

// newAppServerClient 创建 typed client 并监听 transport fatal。
func newAppServerClient(runtimeCtx context.Context, rpc appServerRPC) *appServerClient {
	client := &appServerClient{
		rpc:         rpc,
		completions: newCompletionTracker(),
		staleTurns:  make(map[string]map[string]struct{}),
	}
	go func() {
		select {
		case <-rpc.Done():
			client.completions.fail(rpc.Err())
		case <-runtimeCtx.Done():
			client.completions.fail(runtimeCtx.Err())
		}
	}()
	return client
}

// Initialize 发送一次 app-server initialize，成功后紧接 initialized 通知。
// 参数对照 CodexAcpClient.initialize；当前生成 V1 capabilities 不含旧 experimentalApi 字段。
func (c *appServerClient) Initialize(
	ctx context.Context,
	clientInfo protocol.ClientInfo,
) (protocol.InitializeResponse, error) {
	c.initializeOnce.Do(func() {
		requestAttestation := false
		params := protocol.InitializeParams{
			Capabilities: &protocol.InitializeCapabilities{RequestAttestation: &requestAttestation},
			ClientInfo:   clientInfo,
		}
		c.initializeErr = c.rpc.Call(ctx, func(id protocol.RequestID) protocol.ClientRequest {
			return protocol.NewInitializeRequest(id, params)
		}, &c.initializeResponse)
		if c.initializeErr == nil {
			c.initializeErr = c.rpc.Notify(ctx, protocol.InitializedNotification{})
		}
	})
	return c.initializeResponse, c.initializeErr
}

// ThreadStart 创建一个新 Codex thread。
func (c *appServerClient) ThreadStart(ctx context.Context, params protocol.ThreadStartParams) (protocol.ThreadStartResponse, error) {
	var response protocol.ThreadStartResponse
	err := c.rpc.Call(ctx, func(id protocol.RequestID) protocol.ClientRequest {
		return protocol.NewThreadStartRequest(id, params)
	}, &response)
	return response, err
}

// ThreadResume 恢复并订阅一个现有 Codex thread。
func (c *appServerClient) ThreadResume(ctx context.Context, params protocol.ThreadResumeParams) (protocol.ThreadResumeResponse, error) {
	var response protocol.ThreadResumeResponse
	err := c.rpc.Call(ctx, func(id protocol.RequestID) protocol.ClientRequest {
		return protocol.NewThreadResumeRequest(id, params)
	}, &response)
	return response, err
}

// ThreadRead 读取 Codex thread，并可请求完整 turns 历史。
func (c *appServerClient) ThreadRead(ctx context.Context, params protocol.ThreadReadParams) (protocol.ThreadReadResponse, error) {
	var response protocol.ThreadReadResponse
	err := c.rpc.Call(ctx, func(id protocol.RequestID) protocol.ClientRequest {
		return protocol.NewThreadReadRequest(id, params)
	}, &response)
	return response, err
}

// ThreadUnsubscribe 释放 app-server 对 thread 的订阅。
func (c *appServerClient) ThreadUnsubscribe(ctx context.Context, threadID string) error {
	var response protocol.ThreadUnsubscribeResponse
	return c.rpc.Call(ctx, func(id protocol.RequestID) protocol.ClientRequest {
		return protocol.NewThreadUnsubscribeRequest(id, protocol.ThreadUnsubscribeParams{ThreadID: threadID})
	}, &response)
}

// RunTurn 在 turn/start 前安装 completion 捕获，并等待精确 thread/turn 完成。
// 对照 upstream CodexAppServerClient.runTurn 与 early completion 测试。
func (c *appServerClient) RunTurn(
	ctx context.Context,
	params protocol.TurnStartParams,
	onTurnStarted func(turnID string),
) (protocol.TurnCompletedNotification, error) {
	capture, err := c.completions.beginCapture(params.ThreadID)
	if err != nil {
		return protocol.TurnCompletedNotification{}, err
	}
	var response protocol.TurnStartResponse
	buildRequest := func(id protocol.RequestID) protocol.ClientRequest {
		return protocol.NewTurnStartRequest(id, params)
	}
	responseObserved := false
	if rpc, ok := c.rpc.(observedResponseRPC); ok {
		err = rpc.CallObserved(ctx, buildRequest, &response, func() error {
			if response.Turn.ID == "" {
				return errors.New("codex turn/start response has empty turn id")
			}
			if onTurnStarted != nil {
				onTurnStarted(response.Turn.ID)
			}
			responseObserved = true
			return nil
		})
	} else {
		err = c.rpc.Call(ctx, buildRequest, &response)
	}
	if err != nil {
		c.completions.releaseCapture(params.ThreadID, capture)
		return protocol.TurnCompletedNotification{}, err
	}
	if response.Turn.ID == "" {
		c.completions.releaseCapture(params.ThreadID, capture)
		return protocol.TurnCompletedNotification{}, errors.New("codex turn/start response has empty turn id")
	}
	if !responseObserved && onTurnStarted != nil {
		onTurnStarted(response.Turn.ID)
	}
	return c.completions.waitAfterStart(ctx, params.ThreadID, response.Turn.ID, capture)
}

// TurnInterrupt 请求 app-server 中断精确 thread/turn。
func (c *appServerClient) TurnInterrupt(ctx context.Context, threadID, turnID string) error {
	var response map[string]any
	return c.rpc.Call(ctx, func(id protocol.RequestID) protocol.ClientRequest {
		return protocol.NewTurnInterruptRequest(id, protocol.TurnInterruptParams{ThreadID: threadID, TurnID: turnID})
	}, &response)
}

// TurnSteer 把输入注入精确活动 turn。
func (c *appServerClient) TurnSteer(
	ctx context.Context,
	params protocol.TurnSteerParams,
) (protocol.TurnSteerResponse, error) {
	var response protocol.TurnSteerResponse
	err := c.rpc.Call(ctx, func(id protocol.RequestID) protocol.ClientRequest {
		return protocol.NewTurnSteerRequest(id, params)
	}, &response)
	return response, err
}

// SetNotificationHandler 安装 session/event 薄路由消费方。
func (c *appServerClient) SetNotificationHandler(handler notificationHandler) {
	c.handlerMu.Lock()
	c.handler = handler
	c.handlerMu.Unlock()
}

// MarkTurnStale 标记取消后迟到的 turn，使其通知不会污染重新打开的 session。
func (c *appServerClient) MarkTurnStale(threadID, turnID string) {
	c.handlerMu.Lock()
	turns := c.staleTurns[threadID]
	if turns == nil {
		turns = make(map[string]struct{})
		c.staleTurns[threadID] = turns
	}
	turns[turnID] = struct{}{}
	c.handlerMu.Unlock()
}

// ResolveTurnInterrupted 在 close/进程清理无法依赖真实通知时解除精确 completion waiter。
// 等价 upstream CodexAppServerClient.resolveTurnInterrupted 的合成 interrupted completion。
func (c *appServerClient) ResolveTurnInterrupted(threadID, turnID string) {
	// 合成 completion 是该 stale turn 的终止边界；真实 completion 已提前到达时不能等待不存在的第二条通知清理标记。
	c.handlerMu.Lock()
	c.clearTurnStaleLocked(threadID, turnID)
	c.handlerMu.Unlock()
	c.completions.record(protocol.TurnCompletedNotification{
		ThreadID: threadID,
		Turn: protocol.TurnElement{
			ID: turnID, Items: []protocol.ThreadItem{}, Status: protocol.FluffyInterrupted,
		},
	})
}

// HandleNotification 先记录 completion，再过滤 stale，保持 upstream 构造器的因果顺序。
func (c *appServerClient) HandleNotification(ctx context.Context, notification protocol.ServerNotification) {
	threadID, turnID := notificationRouting(notification)
	if completed, ok := notification.(*protocol.TurnCompletedEnvelope); ok {
		c.completions.record(completed.Params)
	}

	c.handlerMu.Lock()
	_, stale := c.staleTurns[threadID][turnID]
	if stale && notification.Method() == protocol.MethodTurnCompleted {
		c.clearTurnStaleLocked(threadID, turnID)
	}
	handler := c.handler
	c.handlerMu.Unlock()
	if stale || handler == nil {
		return
	}
	handler(ctx, notification)
}

// clearTurnStaleLocked 删除一个 stale identity，并回收已经为空的 thread 容器。
// 调用方必须持有 handlerMu。
func (c *appServerClient) clearTurnStaleLocked(threadID, turnID string) {
	turns := c.staleTurns[threadID]
	delete(turns, turnID)
	if len(turns) == 0 {
		delete(c.staleTurns, threadID)
	}
}

// notificationRouting 提取当前 runtime 需要保护的 thread/turn 身份。
func notificationRouting(notification protocol.ServerNotification) (string, string) {
	switch value := notification.(type) {
	case *protocol.TurnCompletedEnvelope:
		return value.Params.ThreadID, value.Params.Turn.ID
	case *protocol.TurnStartedEnvelope:
		return value.Params.ThreadID, value.Params.Turn.ID
	case *protocol.AgentMessageDeltaEnvelope:
		return value.Params.ThreadID, value.Params.TurnID
	case *protocol.ReasoningSummaryTextDeltaEnvelope:
		return value.Params.ThreadID, value.Params.TurnID
	case *protocol.ReasoningTextDeltaEnvelope:
		return value.Params.ThreadID, value.Params.TurnID
	default:
		threadID, turnID, scoped := notificationScope(notification)
		if scoped {
			return threadID, turnID
		}
		return "", ""
	}
}

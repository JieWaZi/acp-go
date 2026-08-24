package codex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync"

	"github.com/JieWaZi/acp-go/pkg/codex/protocol"
)

const (
	// maxCapturedCompletionsPerTurnStart 限制 turn/start 响应前捕获的 completion 数。
	maxCapturedCompletionsPerTurnStart = 64
	// maxModelListPages 限制单次配置加载的 app-server 分页数。
	maxModelListPages = 128
	// maxListedModels 限制单次配置加载累积的模型数。
	maxListedModels = 4096
)

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

// loginCompletionState 表示多个等待方共享的一次登录完成通知。
// 并发认证订阅复用同一个状态，避免为同一 app-server 通知创建无界 waiter。
type loginCompletionState struct {
	// done 在通知到达、transport 失败或最后一个订阅关闭时关闭。
	done chan struct{}
	// notification 保存生成协议解码后的登录结果。
	notification protocol.AccountLoginCompletedNotification
	// err 保存 transport 或提前关闭错误。
	err error
	// references 是仍拥有该共享状态的订阅数。
	references int
	// completed 防止多个终止来源重复关闭 done。
	completed bool
}

// clientLoginCompletionSubscription 把共享通知状态适配为 authenticator 消费的小接口。
type clientLoginCompletionSubscription struct {
	// client 拥有 pendingLogin 的生命周期。
	client *appServerClient
	// state 是本订阅创建时绑定的精确共享状态。
	state *loginCompletionState
	// closeOnce 保证重复 Close 不会重复减少引用。
	closeOnce sync.Once
}

// Wait 等待一次 account/login/completed 或调用方取消。
func (s *clientLoginCompletionSubscription) Wait(
	ctx context.Context,
) (protocol.AccountLoginCompletedNotification, error) {
	select {
	case <-s.state.done:
		return s.state.notification, s.state.err
	case <-ctx.Done():
		return protocol.AccountLoginCompletedNotification{}, ctx.Err()
	}
}

// Close 释放订阅；最后一个订阅会丢弃尚未完成的旧通知身份。
func (s *clientLoginCompletionSubscription) Close() {
	s.closeOnce.Do(func() {
		s.client.releaseLoginSubscription(s.state)
	})
}

// accountUpdateState 表示 logout 前安装的共享 account/updated 信号。
// V1 不消费该通知 payload，因此不为它重复定义 app-server DTO。
type accountUpdateState struct {
	// done 在通知、fatal 或最后一个订阅关闭时关闭。
	done chan struct{}
	// err 保存 transport 或提前关闭错误。
	err error
	// references 是仍等待该信号的订阅数。
	references int
	// completed 防止重复关闭 done。
	completed bool
}

// accountUpdateSubscription 等待一次账号更新并支持显式取消订阅。
type accountUpdateSubscription struct {
	// client 拥有 pendingAccountUpdate。
	client *appServerClient
	// state 是本订阅绑定的精确信号状态。
	state *accountUpdateState
	// closeOnce 保证引用只释放一次。
	closeOnce sync.Once
}

// Wait 等待 account/updated 或调用方取消。
func (s *accountUpdateSubscription) Wait(ctx context.Context) error {
	select {
	case <-s.state.done:
		return s.state.err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Close 释放 logout 通知订阅。
func (s *accountUpdateSubscription) Close() {
	s.closeOnce.Do(func() {
		s.client.releaseAccountUpdateSubscription(s.state)
	})
}

// completionCapture 保存 turn/start 响应前同 thread 到达的有界 completion。
type completionCapture struct {
	// events 按到达顺序保存捕获窗口内的完成通知。
	events []protocol.TurnCompletedNotification
}

// completionTracker 关联 Turn 启动响应、提前到达的完成通知与活动等待方。
// 同一互斥锁原子完成“检查提前结果→安装 waiter”，避免两个阶段之间丢失通知。
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
// 它只封装 typed 调用与通知关联，不承担 Agent 状态管理。
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
	// authMu 保护登录与 logout 的共享单次通知状态。
	authMu sync.Mutex
	// pendingLogin 保存等待登录完成通知的共享状态。
	pendingLogin *loginCompletionState
	// pendingAccountUpdate 保存等待账号更新的订阅集合。
	pendingAccountUpdate *accountUpdateState
	// skillsMu 串行化全局 Skill extra roots 更新与紧随其后的刷新。
	skillsMu sync.Mutex
	// skillExtraRoots 保存 app-server 当前已经安装的额外 Skill 根快照。
	skillExtraRoots []string
}

// newAppServerClient 创建 typed client 并监听 transport fatal。
func newAppServerClient(runtimeCtx context.Context, rpc appServerRPC) *appServerClient {
	client := &appServerClient{
		rpc:         rpc,
		completions: newCompletionTracker(),
		staleTurns:  make(map[string]map[string]struct{}),
	}
	go func() {
		var err error
		select {
		case <-rpc.Done():
			err = rpc.Err()
		case <-runtimeCtx.Done():
			err = runtimeCtx.Err()
		}
		client.completions.fail(err)
		client.failAuthSubscriptions(err)
	}()
	return client
}

// Initialize 发送一次 app-server initialize，成功后紧接 initialized 通知。
// 已接入的 request_user_input 需要显式开启 experimentalApi。
func (c *appServerClient) Initialize(
	ctx context.Context,
	clientInfo protocol.ClientInfo,
) (protocol.InitializeResponse, error) {
	c.initializeOnce.Do(func() {
		requestAttestation := false
		experimentalAPI := true
		params := protocol.InitializeParams{
			Capabilities: &protocol.InitializeCapabilities{
				ExperimentalAPI:    &experimentalAPI,
				RequestAttestation: &requestAttestation,
			},
			ClientInfo: clientInfo,
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

// ConfigRead 读取当前 cwd 的有效配置与各层，用于避免 ACP MCP 配置覆盖同名用户配置。
func (c *appServerClient) ConfigRead(ctx context.Context, params protocol.ConfigReadParams) (protocol.ConfigReadResponse, error) {
	var response protocol.ConfigReadResponse
	err := c.rpc.Call(ctx, func(id protocol.RequestID) protocol.ClientRequest {
		return protocol.NewConfigReadRequest(id, params)
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

// ListModels 按 model/list 游标顺序读取全部模型。
func (c *appServerClient) ListModels(ctx context.Context) ([]protocol.ModelListResponseDatum, error) {
	models := []protocol.ModelListResponseDatum{}
	var cursor *string
	seenCursors := make(map[string]struct{})
	for page := 0; page < maxModelListPages; page++ {
		var response protocol.ModelListResponse
		err := c.rpc.Call(ctx, func(id protocol.RequestID) protocol.ClientRequest {
			return protocol.NewModelListRequest(id, protocol.ModelListParams{Cursor: cursor})
		}, &response)
		if err != nil {
			return nil, err
		}
		if len(models)+len(response.Data) > maxListedModels {
			return nil, fmt.Errorf("model/list exceeded %d models", maxListedModels)
		}
		models = append(models, response.Data...)
		if response.NextCursor == nil || *response.NextCursor == "" {
			return models, nil
		}
		nextCursor := *response.NextCursor
		if _, repeated := seenCursors[nextCursor]; repeated {
			return nil, fmt.Errorf("repeated model/list cursor %q", nextCursor)
		}
		seenCursors[nextCursor] = struct{}{}
		cursor = &nextCursor
	}
	return nil, fmt.Errorf("model/list exceeded %d pages", maxModelListPages)
}

// RefreshSkills 为当前工作范围安装额外 Skill 根，并强制重新扫描全部 cwd。
func (c *appServerClient) RefreshSkills(
	ctx context.Context,
	workspace codexWorkspace,
) error {
	c.skillsMu.Lock()
	defer c.skillsMu.Unlock()

	extraRoots := workspace.skillExtraRoots()
	if !slices.Equal(c.skillExtraRoots, extraRoots) {
		var response map[string]json.RawMessage
		err := c.rpc.Call(ctx, func(id protocol.RequestID) protocol.ClientRequest {
			return protocol.NewSkillsExtraRootsSetRequest(
				id,
				protocol.SkillsExtraRootsSetParams{ExtraRoots: extraRoots},
			)
		}, &response)
		if err != nil {
			return fmt.Errorf("setting Codex Skill extra roots: %w", err)
		}
		c.skillExtraRoots = append([]string{}, extraRoots...)
	}

	forceReload := true
	var response protocol.SkillsListResponse
	err := c.rpc.Call(ctx, func(id protocol.RequestID) protocol.ClientRequest {
		return protocol.NewSkillsListRequest(id, protocol.SkillsListParams{
			Cwds:        workspace.roots(),
			ForceReload: &forceReload,
		})
	}, &response)
	if err != nil {
		return fmt.Errorf("refreshing Codex Skills: %w", err)
	}
	return nil
}

// AccountRead 直接发送生成协议的 account/read 请求。
func (c *appServerClient) AccountRead(
	ctx context.Context,
	params protocol.GetAccountParams,
) (protocol.GetAccountResponse, error) {
	var response protocol.GetAccountResponse
	err := c.rpc.Call(ctx, func(id protocol.RequestID) protocol.ClientRequest {
		return protocol.NewAccountReadRequest(id, params)
	}, &response)
	return response, err
}

// AccountLogin 直接发送生成协议的 account/login/start 请求。
func (c *appServerClient) AccountLogin(
	ctx context.Context,
	params protocol.LoginAccountParams,
) (protocol.LoginAccountResponse, error) {
	var response protocol.LoginAccountResponse
	err := c.rpc.Call(ctx, func(id protocol.RequestID) protocol.ClientRequest {
		return protocol.NewAccountLoginStartRequest(id, params)
	}, &response)
	return response, err
}

// AccountLoginCancel 直接发送生成协议的 account/login/cancel 请求。
func (c *appServerClient) AccountLoginCancel(
	ctx context.Context,
	params protocol.CancelLoginAccountParams,
) (protocol.CancelLoginAccountResponse, error) {
	var response protocol.CancelLoginAccountResponse
	err := c.rpc.Call(ctx, func(id protocol.RequestID) protocol.ClientRequest {
		return protocol.NewAccountLoginCancelRequest(id, params)
	}, &response)
	return response, err
}

// AccountLogout 发送不带 params 的 account/logout 请求。
func (c *appServerClient) AccountLogout(ctx context.Context) error {
	var response map[string]json.RawMessage
	return c.rpc.Call(ctx, func(id protocol.RequestID) protocol.ClientRequest {
		return protocol.NewAccountLogoutRequest(id)
	}, &response)
}

// SubscribeLoginCompleted 在登录请求前安装共享的单次完成订阅。
func (c *appServerClient) SubscribeLoginCompleted() (loginCompletionSubscription, error) {
	c.authMu.Lock()
	defer c.authMu.Unlock()
	if c.pendingLogin == nil {
		c.pendingLogin = &loginCompletionState{done: make(chan struct{})}
	}
	c.pendingLogin.references++
	return &clientLoginCompletionSubscription{client: c, state: c.pendingLogin}, nil
}

// SubscribeAccountUpdated 在 logout 请求前安装共享的单次账号更新订阅。
func (c *appServerClient) SubscribeAccountUpdated() *accountUpdateSubscription {
	c.authMu.Lock()
	defer c.authMu.Unlock()
	if c.pendingAccountUpdate == nil {
		c.pendingAccountUpdate = &accountUpdateState{done: make(chan struct{})}
	}
	c.pendingAccountUpdate.references++
	return &accountUpdateSubscription{client: c, state: c.pendingAccountUpdate}
}

// RunTurn 在 turn/start 前安装 completion 捕获，并等待精确 thread/turn 完成。
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

// ResolveTurnInterrupted 在 close 或进程清理无法依赖真实通知时解除精确 completion waiter。
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

// HandleNotification 先记录 completion，再过滤 stale，避免提前完成通知丢失。
func (c *appServerClient) HandleNotification(ctx context.Context, notification protocol.ServerNotification) {
	if completed, ok := notification.(*protocol.AccountLoginCompletedEnvelope); ok {
		c.completeLoginSubscription(completed.Params)
		return
	}
	if unknown, ok := notification.(*protocol.UnknownServerNotification); ok && unknown.Method() == protocol.MethodAccountUpdated {
		c.completeAccountUpdateSubscription()
		return
	}

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

// completeLoginSubscription 完成当前共享登录通知，并为下一次登录释放身份。
func (c *appServerClient) completeLoginSubscription(notification protocol.AccountLoginCompletedNotification) {
	c.authMu.Lock()
	state := c.pendingLogin
	c.pendingLogin = nil
	if state != nil && !state.completed {
		state.notification = notification
		state.completed = true
		close(state.done)
	}
	c.authMu.Unlock()
}

// completeAccountUpdateSubscription 完成 logout 前安装的账号更新信号。
func (c *appServerClient) completeAccountUpdateSubscription() {
	c.authMu.Lock()
	state := c.pendingAccountUpdate
	c.pendingAccountUpdate = nil
	if state != nil && !state.completed {
		state.completed = true
		close(state.done)
	}
	c.authMu.Unlock()
}

// releaseLoginSubscription 释放一个引用，并在无人等待时取消旧通知身份。
func (c *appServerClient) releaseLoginSubscription(state *loginCompletionState) {
	c.authMu.Lock()
	defer c.authMu.Unlock()
	if state.references > 0 {
		state.references--
	}
	if state.references != 0 || c.pendingLogin != state {
		return
	}
	c.pendingLogin = nil
	if !state.completed {
		state.err = context.Canceled
		state.completed = true
		close(state.done)
	}
}

// releaseAccountUpdateSubscription 释放一个 logout 通知引用。
func (c *appServerClient) releaseAccountUpdateSubscription(state *accountUpdateState) {
	c.authMu.Lock()
	defer c.authMu.Unlock()
	if state.references > 0 {
		state.references--
	}
	if state.references != 0 || c.pendingAccountUpdate != state {
		return
	}
	c.pendingAccountUpdate = nil
	if !state.completed {
		state.err = context.Canceled
		state.completed = true
		close(state.done)
	}
}

// failAuthSubscriptions 用 transport fatal 同时解除认证与 logout 等待者。
func (c *appServerClient) failAuthSubscriptions(err error) {
	if err == nil {
		err = ErrAppServerUnavailable
	}
	c.authMu.Lock()
	login := c.pendingLogin
	account := c.pendingAccountUpdate
	c.pendingLogin = nil
	c.pendingAccountUpdate = nil
	if login != nil && !login.completed {
		login.err = err
		login.completed = true
		close(login.done)
	}
	if account != nil && !account.completed {
		account.err = err
		account.completed = true
		close(account.done)
	}
	c.authMu.Unlock()
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

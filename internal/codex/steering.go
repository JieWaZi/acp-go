package codex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"acp-go/agents/codex/protocol"
	acp "github.com/coder/acp-go-sdk"
)

const (
	// steeringExtensionMethod 直接复用 upstream AcpExtensions.SESSION_STEERING_METHOD。
	steeringExtensionMethod = "_session/steering"
	// defaultSteeringQueueCapacity 是每 session 等待项硬上限。
	// 固定 TS 上游队列无界；Go V1 按父规格增加此资源保护，行为差异记录于 UPSTREAM.md。
	defaultSteeringQueueCapacity = 64
	// steeringInjected 表示输入已注入活动 turn。
	steeringInjected = "injected"
	// steeringStartedNewTurn 表示无活动 turn 时新 turn 已被 app-server 接受。
	steeringStartedNewTurn = "startedNewTurn"
	// steeringFailed 表示当前单项遇到非协议性失败，队列仍继续。
	steeringFailed = "failed"
)

// steeringParams 是 `_session/steering` extension 的固定 upstream 参数。
type steeringParams struct {
	// SessionID 是目标 ACP SessionID/Codex ThreadID。
	SessionID string `json:"sessionId"`
	// Prompt 是 SDK discriminator-first ContentBlock union。
	Prompt []acp.ContentBlock `json:"prompt"`
}

// steeringResponse 是 `_session/steering` extension 的固定 upstream 响应。
type steeringResponse struct {
	// Outcome 是 injected、startedNewTurn 或 failed。
	Outcome string `json:"outcome"`
}

// queuedSteering 保存一个等待 FIFO consumer 的请求及其入队 generation。
type queuedSteering struct {
	// ctx 是外层 SDK extension 请求上下文。
	ctx context.Context
	// params 是已验证的 steering 内容。
	params steeringParams
	// generation 防止排队期间 close/reopen 后错误投递。
	generation uint64
	// result 向唯一调用方交付结果。
	result chan steeringResult
	// enqueued 是白盒竞态测试使用的可选 barrier；生产请求保持 nil。
	enqueued chan struct{}
}

// steeringResult 保存一个 FIFO 项目的响应或协议错误。
type steeringResult struct {
	// response 是成功或 unexpected failure 的稳定 outcome。
	response steeringResponse
	// err 是应直接传播的 RequestError、取消或 close 错误。
	err error
}

// steeringQueue 串行消费单 session 请求；职责对应 upstream SteeringQueue.ts。
type steeringQueue struct {
	// manager 执行具体 inject/start 规则并负责 idle identity 删除。
	manager *steeringManager
	// sessionID 是该队列唯一归属。
	sessionID string
	// capacity 是 pending 等待项硬上限。
	capacity int
	// ctx 在 session/Adapter close 时取消当前处理项。
	ctx context.Context
	// cancel 终止当前处理项与后续入队。
	cancel context.CancelFunc
	// mu 保护 pending、processing、closed。
	mu sync.Mutex
	// pending 按到达顺序保存等待项。
	pending []*queuedSteering
	// processing 表示唯一 consumer 正在运行。
	processing bool
	// closed 表示队列永久拒绝新项。
	closed bool
	// closeErr 是所有被拒绝 pending 共享的关闭原因。
	closeErr error
}

// newSteeringQueue 创建尚未启动 consumer 的 session 队列。
func newSteeringQueue(manager *steeringManager, sessionID string, capacity int) *steeringQueue {
	ctx, cancel := context.WithCancel(manager.agent.runtimeCtx)
	return &steeringQueue{
		manager: manager, sessionID: sessionID, capacity: capacity, ctx: ctx, cancel: cancel,
	}
}

// enqueue 加入有界 FIFO，并确保同一时间只有一个 consumer。
func (q *steeringQueue) enqueue(request *queuedSteering) (steeringResponse, error) {
	q.mu.Lock()
	if q.closed {
		err := q.closeErr
		q.mu.Unlock()
		return steeringResponse{}, err
	}
	if len(q.pending) >= q.capacity {
		q.mu.Unlock()
		return steeringResponse{}, acp.NewInvalidRequest("steering queue is full")
	}
	q.pending = append(q.pending, request)
	if request.enqueued != nil {
		close(request.enqueued)
	}
	if !q.processing {
		q.processing = true
		go q.consume()
	}
	q.mu.Unlock()

	select {
	case result := <-request.result:
		return result.response, result.err
	case <-request.ctx.Done():
		return steeringResponse{}, request.ctx.Err()
	case <-q.ctx.Done():
		q.mu.Lock()
		err := q.closeErr
		q.mu.Unlock()
		if err == nil {
			err = ErrRuntimeUnavailable
		}
		return steeringResponse{}, err
	}
}

// consume 逐项执行并隔离错误；一个失败绝不终止后继项。
func (q *steeringQueue) consume() {
	for {
		q.mu.Lock()
		if len(q.pending) == 0 || q.closed {
			q.processing = false
			q.mu.Unlock()
			q.manager.removeIfIdle(q.sessionID, q)
			return
		}
		request := q.pending[0]
		copy(q.pending, q.pending[1:])
		q.pending = q.pending[:len(q.pending)-1]
		q.mu.Unlock()

		itemCtx, cancel := context.WithCancel(q.ctx)
		stopRequestCancel := context.AfterFunc(request.ctx, cancel)
		response, err := q.manager.perform(itemCtx, request.params, request.generation)
		stopRequestCancel()
		cancel()
		select {
		case request.result <- steeringResult{response: response, err: err}:
		default:
			// 调用方已取消；结果不应阻塞唯一 consumer。
		}
	}
}

// close 取消当前项并拒绝全部 pending；可重复调用。
func (q *steeringQueue) close(err error) {
	q.mu.Lock()
	if q.closed {
		q.mu.Unlock()
		return
	}
	q.closed = true
	q.closeErr = err
	pending := q.pending
	q.pending = nil
	q.mu.Unlock()
	q.cancel()
	for _, request := range pending {
		select {
		case request.result <- steeringResult{err: err}:
		default:
		}
	}
}

// isIdle 返回没有 consumer 且没有 pending 的瞬时状态。
func (q *steeringQueue) isIdle() bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	return !q.processing && len(q.pending) == 0
}

// steeringManager 保存 session→FIFO queue 映射，并实现固定 fallback 语义。
type steeringManager struct {
	// agent 提供 session identity、typed client 与 prompt 生命周期。
	agent *Agent
	// capacity 传给每个新建 session 队列。
	capacity int
	// mu 保护 queues 与 closed。
	mu sync.Mutex
	// queues 保存非 idle session 队列。
	queues map[string]*steeringQueue
	// closed 表示 Adapter 已开始关闭。
	closed bool
	// closeErr 是 Adapter 关闭原因。
	closeErr error
}

// newSteeringManager 创建空的 per-session queue registry。
func newSteeringManager(agent *Agent, capacity int) *steeringManager {
	return &steeringManager{agent: agent, capacity: capacity, queues: make(map[string]*steeringQueue)}
}

// Handle 校验 extension params、捕获 generation 并等待 FIFO 接受结果。
func (m *steeringManager) Handle(ctx context.Context, raw json.RawMessage) (steeringResponse, error) {
	var params steeringParams
	if err := json.Unmarshal(raw, &params); err != nil {
		return steeringResponse{}, acp.NewInvalidParams(map[string]any{"error": err.Error()})
	}
	if params.SessionID == "" || params.Prompt == nil {
		return steeringResponse{}, acp.NewInvalidParams("sessionId and prompt are required")
	}
	state, ok := m.agent.sessions.get(params.SessionID)
	if !ok {
		return steeringResponse{}, acp.NewInvalidRequest("session not found")
	}
	queue, err := m.getQueue(params.SessionID)
	if err != nil {
		return steeringResponse{}, err
	}
	response, err := queue.enqueue(&queuedSteering{
		ctx: ctx, params: params, generation: state.generation, result: make(chan steeringResult, 1),
	})
	if err == nil {
		return response, nil
	}
	var requestErr *acp.RequestError
	if errors.As(err, &requestErr) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return steeringResponse{}, err
	}
	m.agent.logger.Warn("Codex steering 单项失败", "session_id", params.SessionID, "error", err)
	return steeringResponse{Outcome: steeringFailed}, nil
}

// getQueue 返回现有 session 队列或在锁内创建唯一实例。
func (m *steeringManager) getQueue(sessionID string) (*steeringQueue, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil, m.closeErr
	}
	queue := m.queues[sessionID]
	if queue == nil {
		queue = newSteeringQueue(m, sessionID, m.capacity)
		m.queues[sessionID] = queue
	}
	return queue, nil
}

// removeIfIdle 仅在 map 仍指向同一空闲实例时删除，防止误删复用队列。
func (m *steeringManager) removeIfIdle(sessionID string, queue *steeringQueue) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.queues[sessionID] == queue && queue.isIdle() {
		delete(m.queues, sessionID)
	}
}

// perform 尝试注入活动 turn；无活动或明确竞态时启动新 turn。
func (m *steeringManager) perform(
	ctx context.Context,
	params steeringParams,
	generation uint64,
) (steeringResponse, error) {
	state, ok := m.agent.sessions.get(params.SessionID)
	if !ok || state.generation != generation || !m.agent.sessions.isCurrent(state) {
		return steeringResponse{}, acp.NewInvalidRequest("session generation is stale")
	}
	input, err := buildPromptInput(params.Prompt)
	if err != nil {
		return steeringResponse{}, acp.NewInvalidParams(map[string]any{"error": err.Error()})
	}

	prompt := sessionActivePrompt(state)
	if prompt != nil {
		turnID, _ := prompt.currentTurn()
		if turnID == "" {
			select {
			case <-prompt.turnStarted:
				turnID, _ = prompt.currentTurn()
			case <-prompt.backgroundDone:
				turnID = ""
			case <-ctx.Done():
				return steeringResponse{}, ctx.Err()
			}
		}
		if turnID != "" {
			_, steerErr := m.agent.client.TurnSteer(ctx, protocol.TurnSteerParams{
				ThreadID: params.SessionID, ExpectedTurnID: turnID, Input: input,
			})
			if steerErr == nil {
				return steeringResponse{Outcome: steeringInjected}, nil
			}
			currentPrompt := sessionActivePrompt(state)
			currentTurnID := ""
			if currentPrompt != nil {
				currentTurnID, _ = currentPrompt.currentTurn()
			}
			if currentTurnID == turnID && !isNoActiveTurnToSteerError(steerErr) {
				return steeringResponse{}, steerErr
			}
		}
	}

	return m.startNewTurn(ctx, state, params, input, generation)
}

// startNewTurn 等待旧 prompt 完整释放后启动控制 turn，并在 onTurnStarted 时立即接受 steering。
func (m *steeringManager) startNewTurn(
	ctx context.Context,
	state *sessionState,
	params steeringParams,
	input []protocol.InputElement,
	generation uint64,
) (steeringResponse, error) {
	if previous := sessionActivePrompt(state); previous != nil {
		select {
		case <-previous.backgroundDone:
		case <-ctx.Done():
			return steeringResponse{}, ctx.Err()
		}
	}
	if !m.agent.sessions.isCurrent(state) || state.generation != generation {
		return steeringResponse{}, acp.NewInvalidRequest("session is closing")
	}
	prompt := newActivePrompt(m.agent.runtimeCtx, m.agent.nextTurnGeneration.Add(1))
	if err := m.agent.installActivePrompt(state, prompt); err != nil {
		prompt.cancelRun()
		return steeringResponse{}, err
	}
	defer m.agent.finishPromptForeground(prompt)

	result := make(chan error, 1)
	go func() {
		defer m.agent.finishPromptBackground(prompt)
		_, runErr := m.agent.client.RunTurn(prompt.runCtx, protocol.TurnStartParams{
			ThreadID: params.SessionID, Input: input,
		}, func(turnID string) {
			m.agent.onTurnStarted(state, prompt, turnID)
		})
		prompt.cancelRun()
		m.agent.clearActivePrompt(state, prompt)
		result <- runErr
	}()

	select {
	case <-prompt.turnStarted:
		return steeringResponse{Outcome: steeringStartedNewTurn}, nil
	case err := <-result:
		if err == nil {
			return steeringResponse{Outcome: steeringStartedNewTurn}, nil
		}
		return steeringResponse{}, err
	case <-ctx.Done():
		prompt.requestCancel()
		prompt.markForegroundFinished()
		m.agent.releaseActivePrompt(state, prompt)
		return steeringResponse{}, ctx.Err()
	}
}

// sessionActivePrompt 返回 session 当前 prompt 指针快照。
func sessionActivePrompt(state *sessionState) *activePrompt {
	state.mu.Lock()
	defer state.mu.Unlock()
	return state.activePrompt
}

// isNoActiveTurnToSteerError 对照 upstream isNoActiveTurnToSteerError 检查 message/data.details。
func isNoActiveTurnToSteerError(err error) bool {
	messages := []string{err.Error()}
	var rpcErr *rpcError
	if errors.As(err, &rpcErr) && len(rpcErr.Data) > 0 {
		if rpcErr.Data[0] == '"' {
			// 固定 upstream 兼容 data 直接为错误文本的旧 app-server 形状。
			var details string
			if json.Unmarshal(rpcErr.Data, &details) == nil {
				messages = append(messages, details)
			}
		} else {
			var data struct {
				// Details 是 app-server 对无活动 turn 竞态的附加文本。
				Details string `json:"details"`
			}
			if json.Unmarshal(rpcErr.Data, &data) == nil {
				messages = append(messages, data.Details)
			}
		}
	}
	for _, message := range messages {
		if strings.Contains(strings.ToLower(message), "no active turn to steer") {
			return true
		}
	}
	return false
}

// CloseSession 关闭并移除精确 session 队列。
func (m *steeringManager) CloseSession(sessionID string, err error) {
	m.mu.Lock()
	queue := m.queues[sessionID]
	delete(m.queues, sessionID)
	m.mu.Unlock()
	if queue != nil {
		queue.close(err)
	}
}

// Close 关闭全部队列并拒绝后续 extension 请求。
func (m *steeringManager) Close(err error) {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	m.closed = true
	m.closeErr = fmt.Errorf("closing steering queues: %w", err)
	queues := m.queues
	m.queues = make(map[string]*steeringQueue)
	m.mu.Unlock()
	for _, queue := range queues {
		queue.close(m.closeErr)
	}
}

package codex

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/JieWaZi/acp-go/pkg/codex/protocol"
	acp "github.com/JieWaZi/acp-go/sdk"
)

const (
	// steeringExtensionMethod 是 Session steering 扩展的稳定方法名。
	steeringExtensionMethod = "_session/steering"
	// defaultSteeringQueueCapacity 是每 session 等待项硬上限。
	// maxPendingSteeringPerSession 限制单个 Session 的待处理请求，避免无界占用内存。
	defaultSteeringQueueCapacity = 64
	// steeringInjected 表示输入已注入活动 turn。
	steeringInjected = "injected"
	// steeringStartedNewTurn 表示无活动 turn 时新 turn 已被 app-server 接受。
	steeringStartedNewTurn = "startedNewTurn"
)

// steeringParams 是 `_session/steering` 扩展的参数。
type steeringParams struct {
	// SessionID 是目标 ACP SessionID/Codex ThreadID。
	SessionID string `json:"sessionId"`
	// Prompt 是 SDK discriminator-first ContentBlock union。
	Prompt []acp.ContentBlock `json:"prompt"`
}

// steeringResponse 是 `_session/steering` 扩展的响应。
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

// steeringQueue 串行消费单个 Session 的 steering 请求。
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

// steeringManager owns the bounded FIFO of ACP input requests for each native thread.
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

// newSteeringManager creates the queue registry for the Agent.
func newSteeringManager(agent *Agent, capacity int) *steeringManager {
	return &steeringManager{agent: agent, capacity: capacity, queues: make(map[string]*steeringQueue)}
}

// Handle validates and enqueues input against the current native thread generation.
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
		return steeringResponse{}, codexSessionNotFoundError(params.SessionID)
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
	if errors.As(err, &requestErr) {
		return steeringResponse{}, requestErr
	}
	return steeringResponse{}, err
}

// getQueue returns the existing FIFO or registers a new queue for the thread.
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

// removeIfIdle releases only the exact queue instance when it is idle.
func (m *steeringManager) removeIfIdle(sessionID string, queue *steeringQueue) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.queues[sessionID] == queue && queue.isIdle() {
		delete(m.queues, sessionID)
	}
}

// perform steers an active input turn or starts one when no input was submitted.
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

	prompt := sessionActiveTurn(state)
	if prompt != nil {
		if prompt.kind == compactionTurn {
			return steeringResponse{}, acp.NewInvalidRequest("native compaction owns the session")
		}
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
			if err := ctx.Err(); err != nil {
				return steeringResponse{}, err
			}
			response, err := m.agent.client.TurnSteer(ctx, protocol.TurnSteerParams{
				ThreadID: params.SessionID, ExpectedTurnID: turnID, Input: input,
			})
			if err != nil {
				// A changed turn identity does not prove the provider refused the input.
				return steeringResponse{}, codexRequestFailure("turn_steer", err)
			}
			if response.TurnID != turnID {
				return steeringResponse{}, codexRequestFailure("turn_steer", errors.New("Codex steer response did not identify the expected turn"))
			}
			return steeringResponse{Outcome: steeringInjected}, nil
		}
	}

	return m.startNewTurn(ctx, state, input, generation)
}

// startNewTurn admits input only if no previous native submission consumed it.
func (m *steeringManager) startNewTurn(
	ctx context.Context,
	state *sessionState,
	input []protocol.InputElement,
	generation uint64,
) (steeringResponse, error) {
	if previous := sessionActiveTurn(state); previous != nil {
		select {
		case <-previous.backgroundDone:
		case <-ctx.Done():
			return steeringResponse{}, ctx.Err()
		}
	}
	if err := ctx.Err(); err != nil {
		return steeringResponse{}, err
	}
	if !m.agent.sessions.isCurrent(state) || state.generation != generation {
		return steeringResponse{}, acp.NewInvalidRequest("session is closing")
	}
	prompt := newNativeTurn(m.agent.runtimeCtx, m.agent.nextTurnGeneration.Add(1))
	prompt.kind = steeringTurn
	prompt.clientMessageID = rand.Text()
	if err := m.agent.installTurn(state, prompt); err != nil {
		prompt.cancelRun()
		return steeringResponse{}, err
	}
	defer m.agent.finishTurnForeground(prompt)

	result := make(chan error, 1)
	turnParams, err := turnParamsForSession(state, input, &prompt.clientMessageID)
	if err != nil {
		prompt.cancelRun()
		m.agent.releaseTurn(state, prompt)
		m.agent.finishTurnBackground(prompt)
		return steeringResponse{}, err
	}
	go func() {
		defer m.agent.finishTurnBackground(prompt)
		_, runErr := m.agent.client.RunTurn(prompt.runCtx, turnParams, func(turnID string) {
			m.agent.onTurnStarted(state, prompt, turnID)
		}, prompt.startObservation())
		runErr = errors.Join(runErr, prompt.waitDelivery())
		prompt.cancelRun()
		m.agent.releaseTurn(state, prompt)
		result <- runErr
	}()

	select {
	case <-prompt.turnStarted:
		return steeringResponse{Outcome: steeringStartedNewTurn}, nil
	case err := <-result:
		if err == nil {
			return steeringResponse{Outcome: steeringStartedNewTurn}, nil
		}
		return steeringResponse{}, codexRequestFailure("turn_start", err)
	case <-ctx.Done():
		prompt.requestCancel()
		prompt.markForegroundFinished()
		m.agent.releaseTurn(state, prompt)
		return steeringResponse{}, ctx.Err()
	}
}

// sessionActiveTurn returns the current native lane owner.
func sessionActiveTurn(state *sessionState) *nativeTurn {
	state.mu.Lock()
	defer state.mu.Unlock()
	return state.activeTurn
}

// CloseSession closes and removes the identified thread queue.
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

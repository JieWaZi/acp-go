package codex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/JieWaZi/acp-go/pkg/acpmeta"
	"github.com/JieWaZi/acp-go/pkg/codex/protocol"
	acp "github.com/JieWaZi/acp-go/sdk"
	"sync"
)

// activePrompt 保存一个 session 唯一 pending/active turn 的取消身份。
// 字段同时覆盖 turn/start 前后的 Prompt 身份与等待状态。
type activePrompt struct {
	// compact identifies the standalone native compaction lane.
	compact bool
	// mu 保护 turnID、cancelRequested 和 finished。
	mu sync.Mutex
	// generation 是 Adapter 为每个 turn 分配的单调 runtime generation。
	generation uint64
	// eventRouter 把当前 turn 的 typed 通知映射到 ACP connection。
	eventRouter *eventRouter
	// runCtx 只约束当前 RunTurn；普通 ACP 取消保留它以观察迟到 start，session close 则显式终止它。
	runCtx context.Context
	// runCancel 在 session/Adapter close 时解除 pending turn/start 或 completion waiter。
	runCancel context.CancelFunc
	// turnID 在 turn/start 响应前为空，迟到响应仍会填入以便 interrupt。
	turnID string
	// cancelRequested 表示 ACP cancel、请求 context 或 close 已到达。
	cancelRequested bool
	// finished 表示前台 Prompt 已决定结果；迟到 start 必须视为 stale。
	finished bool
	// cancelSignal 在首次取消时关闭，解除 turn-start 前的前台等待。
	cancelSignal chan struct{}
	// cancelOnce 保证 cancelSignal 只关闭一次。
	cancelOnce sync.Once
	// interruptOnce 保证同一 turn 最多发送一次 turn/interrupt。
	interruptOnce sync.Once
	// interruptDone 在 interrupt 请求结束时关闭；尚未触发时保持开放。
	interruptDone chan struct{}
	// turnStarted 在 turn/start 返回身份后关闭，steering 可等待 pending start 而不制造 rival turn。
	turnStarted chan struct{}
	// turnStartedOnce 保证迟到回调或异常重复回调不会重复关闭。
	turnStartedOnce sync.Once
	// foregroundDone 在 ACP Prompt 或 steering 启动调用完成其前台返回后关闭。
	foregroundDone chan struct{}
	// foregroundDoneOnce 保证前台返回信号只关闭一次。
	foregroundDoneOnce sync.Once
	// backgroundDone 在 RunTurn 完整结束、迟到身份已处理后关闭。
	backgroundDone chan struct{}
	// backgroundDoneOnce 保证清理路径幂等。
	backgroundDoneOnce sync.Once
}

// newActivePrompt 创建尚未取消、尚无 turn ID 的 prompt 身份与独立 RunTurn 生命周期。
func newActivePrompt(parent context.Context, generation uint64) *activePrompt {
	runCtx, runCancel := context.WithCancel(parent)
	return &activePrompt{
		generation:     generation,
		runCtx:         runCtx,
		runCancel:      runCancel,
		cancelSignal:   make(chan struct{}),
		interruptDone:  make(chan struct{}),
		turnStarted:    make(chan struct{}),
		foregroundDone: make(chan struct{}),
		backgroundDone: make(chan struct{}),
	}
}

// markForegroundDone 通知 close 路径前台调用已执行完 finally 清理。
func (p *activePrompt) markForegroundDone() {
	p.foregroundDoneOnce.Do(func() { close(p.foregroundDone) })
}

// cancelRun 终止底层 pending turn/start 或 completion waiter；普通请求取消不会调用它，以保留迟到观察者。
func (p *activePrompt) cancelRun() {
	p.runCancel()
}

// requestCancel 幂等记录取消，并返回当前已知 turn ID。
func (p *activePrompt) requestCancel() string {
	p.mu.Lock()
	p.cancelRequested = true
	turnID := p.turnID
	p.mu.Unlock()
	p.cancelOnce.Do(func() { close(p.cancelSignal) })
	return turnID
}

// setTurn 记录 turn/start 结果，并返回它是否已经属于取消/结束后的迟到 turn。
func (p *activePrompt) setTurn(turnID string) bool {
	p.mu.Lock()
	p.turnID = turnID
	late := p.cancelRequested || p.finished
	p.mu.Unlock()
	return late
}

// claimCompactTurn atomically accepts the first native identity for the held lane.
func (p *activePrompt) claimCompactTurn(turnID string) (bool, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.turnID != "" {
		return false, false
	}
	p.turnID = turnID
	return true, p.cancelRequested || p.finished
}

// signalTurnStarted publishes the initialized native turn identity to waiters.
func (p *activePrompt) signalTurnStarted() { p.turnStartedOnce.Do(func() { close(p.turnStarted) }) }

// markBackgroundDone 通知 steering/close 当前 prompt 已完全释放 session 槽位。
func (p *activePrompt) markBackgroundDone() {
	p.backgroundDoneOnce.Do(func() { close(p.backgroundDone) })
}

// currentTurn 返回当前 turn ID 与取消状态快照。
func (p *activePrompt) currentTurn() (string, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.turnID, p.cancelRequested
}

// setEventRouter 安装与已知 turn ID/generation 绑定的事件路由器。
func (p *activePrompt) setEventRouter(router *eventRouter) {
	p.mu.Lock()
	p.eventRouter = router
	p.mu.Unlock()
}

// currentEventRouter 返回当前 prompt 的事件路由器快照。
func (p *activePrompt) currentEventRouter() *eventRouter {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.eventRouter
}

// markForegroundFinished 标记 ACP Prompt 已返回；后台只负责 late interrupt/completion 清理。
func (p *activePrompt) markForegroundFinished() {
	p.mu.Lock()
	p.finished = true
	p.mu.Unlock()
}

// compact submits a standalone native turn and holds admission until its terminal notification.
func (a *Agent) compact(ctx context.Context, raw json.RawMessage) (acpmeta.CompactResponse, error) {
	var request acpmeta.CompactRequest
	if err := json.Unmarshal(raw, &request); err != nil || request.SessionID == "" {
		return acpmeta.CompactResponse{}, acp.NewInvalidParams(map[string]any{"error": "session_id is required"})
	}
	if err := a.requireInitialized(); err != nil {
		return acpmeta.CompactResponse{}, err
	}
	state, ok := a.sessions.get(request.SessionID)
	if !ok {
		return acpmeta.CompactResponse{}, codexSessionNotFoundError(request.SessionID)
	}
	prompt := newActivePrompt(a.runtimeCtx, a.nextTurnGeneration.Add(1))
	prompt.compact = true
	if err := a.installActivePrompt(state, prompt); err != nil {
		prompt.cancelRun()
		return acpmeta.CompactResponse{}, err
	}
	capture, err := a.client.completions.beginCapture(state.id)
	if err != nil {
		a.releaseActivePrompt(state, prompt)
		prompt.cancelRun()
		a.finishPromptForeground(prompt)
		a.finishPromptBackground(prompt)
		return acpmeta.CompactResponse{}, err
	}
	type result struct {
		// response carries the precise native terminal result.
		response acpmeta.CompactResponse
		// err is the request or completion failure.
		err error
	}
	results := make(chan result, 1)
	go func() {
		var finished sync.Once
		finish := func() {
			finished.Do(func() {
				prompt.markForegroundFinished()
				a.releaseActivePrompt(state, prompt)
				prompt.cancelRun()
				a.finishPromptBackground(prompt)
			})
		}
		defer finish()
		err := a.client.ThreadCompactStart(prompt.runCtx, protocol.ThreadCompactStartParams{ThreadID: state.id})
		reported := false
		if err != nil {
			select {
			case <-prompt.turnStarted:
				// An observed native turn outranks the failed request response.
			default:
				var rejection *rpcError
				if errors.As(err, &rejection) {
					a.client.completions.releaseCapture(state.id, capture)
					finish()
					results <- result{err: codexRequestFailure("compact_start", err)}
					return
				}
				results <- result{err: codexRequestFailure("compact_start", err)}
				reported = true
			}
		}
		select {
		case <-prompt.turnStarted:
		case <-prompt.runCtx.Done():
			a.client.completions.releaseCapture(state.id, capture)
			if !reported {
				results <- result{err: prompt.runCtx.Err()}
			}
			return
		}
		turnID, _ := prompt.currentTurn()
		completion, completionErr := a.client.completions.waitAfterStart(prompt.runCtx, state.id, turnID, capture)
		router := prompt.currentEventRouter()
		usage, observed := router.Usage()
		if !observed || completionErr != nil || completion.Turn.Status != protocol.FluffyCompleted {
			state.mu.Lock()
			state.usageTotal = nil
			state.mu.Unlock()
		}
		if reported {
			return
		}
		if completionErr != nil {
			finish()
			results <- result{err: turnFailureWithUsage(prompt, "compact_completion", codexRequestFailure("compact_completion", completionErr))}
			return
		}
		if completion.Turn.Status == protocol.Failed && completion.Turn.Error != nil {
			finish()
			results <- result{err: turnFailureWithUsage(prompt, "compact_completion", codexTurnRequestError(completion.Turn.Error))}
			return
		}
		finish()
		response := acpmeta.CompactResponse{TurnID: turnID, Status: string(completion.Turn.Status)}
		if observed {
			response.Meta = acpmeta.WithTurnUsage(nil, usage.Metadata(completion.Turn.Status == protocol.FluffyCompleted))
		}
		results <- result{response: response}
	}()
	defer a.finishPromptForeground(prompt)
	select {
	case result := <-results:
		return result.response, result.err
	case <-ctx.Done():
		turnID := prompt.requestCancel()
		if turnID != "" {
			a.requestPromptInterrupt(state, prompt, turnID, false)
		}
		select {
		case result := <-results:
			return result.response, result.err
		default:
		}
		failure := acp.NewRequestCancelled(map[string]any{"cause": ctx.Err().Error()})
		return acpmeta.CompactResponse{}, turnFailureWithUsage(prompt, "compact_cancel", failure)
	case <-a.runtimeCtx.Done():
		return acpmeta.CompactResponse{}, ErrRuntimeUnavailable
	}
}

// Prompt 提交 turn/start，并按精确 completion 产生 ACP stop reason。
func (a *Agent) Prompt(ctx context.Context, request acp.PromptRequest) (response acp.PromptResponse, promptErr error) {
	if err := a.requireInitialized(); err != nil {
		return acp.PromptResponse{}, err
	}
	state, ok := a.sessions.get(string(request.SessionId))
	if !ok {
		return acp.PromptResponse{}, codexSessionNotFoundError(string(request.SessionId))
	}
	if _, err := a.waitConnection(ctx); err != nil {
		return acp.PromptResponse{}, err
	}
	if err := a.refreshSkills(ctx, state.workspace()); err != nil {
		return acp.PromptResponse{}, err
	}
	input, err := buildPromptInput(request.Prompt)
	if err != nil {
		return acp.PromptResponse{}, err
	}
	prompt := newActivePrompt(a.runtimeCtx, a.nextTurnGeneration.Add(1))
	if err := a.installActivePrompt(state, prompt); err != nil {
		prompt.cancelRun()
		return acp.PromptResponse{}, fmt.Errorf("prompting session %q: %w", request.SessionId, err)
	}
	// 前台返回后立即释放 Session 槽位；普通提前取消时，RunTurn 观察者继续
	// 等待迟到的 turn/start 并执行 stale interrupt。
	defer func() {
		if promptErr != nil || response.StopReason == acp.StopReasonCancelled {
			// 失败或提前取消后可能仍有未观测尾部，下一轮不能借旧样本收取这些消费。
			state.mu.Lock()
			state.usageTotal = nil
			state.mu.Unlock()
		}
		prompt.markForegroundFinished()
		a.releaseActivePrompt(state, prompt)
		a.finishPromptForeground(prompt)
	}()

	// turnResult 在后台 runtime 生命周期与前台 ACP 请求之间传递一次精确 turn 结果。
	type turnResult struct {
		// completion 是精确 turn 的完成通知。
		completion protocol.TurnCompletedNotification
		// err 是 turn/start、completion 或 process fatal 错误。
		err error
	}
	resultChannel := make(chan turnResult, 1)
	turnParams := turnParamsForSession(state, input, request.MessageId)
	go func() {
		defer a.finishPromptBackground(prompt)
		completion, runErr := a.client.RunTurn(prompt.runCtx, turnParams, func(turnID string) {
			a.onTurnStarted(state, prompt, turnID)
		})
		prompt.cancelRun()
		resultChannel <- turnResult{completion: completion, err: runErr}
	}()

	requestDone := ctx.Done()
	cancelSignal := prompt.cancelSignal
	for {
		select {
		case result := <-resultChannel:
			if result.err != nil {
				if errors.Is(result.err, context.Canceled) {
					_, cancelled := prompt.currentTurn()
					if cancelled {
						return codexPromptResponse(
							prompt,
							acp.StopReasonCancelled,
							request.MessageId,
						), nil
					}
				}
				phase := "turn_start"
				if id, _ := prompt.currentTurn(); id != "" {
					phase = "turn_completion"
				}
				return acp.PromptResponse{}, turnFailureWithUsage(prompt, phase, codexRequestFailure(phase, result.err))
			}
			switch result.completion.Turn.Status {
			case protocol.FluffyInterrupted:
				return codexPromptResponse(prompt, acp.StopReasonCancelled, request.MessageId), nil
			case protocol.FluffyCompleted:
				return codexPromptResponse(prompt, acp.StopReasonEndTurn, request.MessageId), nil
			case protocol.Failed:
				if turnError := result.completion.Turn.Error; turnError != nil {
					return acp.PromptResponse{}, turnFailureWithUsage(prompt, "turn_completion", codexTurnRequestError(turnError))
				}
				return acp.PromptResponse{}, turnFailureWithUsage(prompt, "turn_completion", &acp.RequestError{Code: -32603, Message: fmt.Sprintf("codex turn %q failed", result.completion.Turn.ID)})
			default:
				return acp.PromptResponse{}, fmt.Errorf(
					"codex turn %q completed with unexpected status %q",
					result.completion.Turn.ID,
					result.completion.Turn.Status,
				)
			}
		case <-requestDone:
			turnID := prompt.requestCancel()
			if turnID == "" {
				return codexPromptResponse(prompt, acp.StopReasonCancelled, request.MessageId), nil
			}
			a.requestPromptInterrupt(state, prompt, turnID, false)
			requestDone = nil
			cancelSignal = nil
		case <-cancelSignal:
			turnID, _ := prompt.currentTurn()
			if turnID == "" {
				return codexPromptResponse(prompt, acp.StopReasonCancelled, request.MessageId), nil
			}
			a.requestPromptInterrupt(state, prompt, turnID, false)
			requestDone = nil
			cancelSignal = nil
		case <-a.runtimeCtx.Done():
			return acp.PromptResponse{}, fmt.Errorf("running codex turn: %w", ErrRuntimeUnavailable)
		}
	}
}

// turnFailureWithUsage carries observed consumption across an unsuccessful terminal boundary.
func turnFailureWithUsage(prompt *activePrompt, phase string, failure *acp.RequestError) *acp.RequestError {
	data, _ := failure.Data.(map[string]any)
	if data == nil {
		data = map[string]any{}
	}
	if _, exists := data["phase"]; !exists {
		data["phase"] = phase
	}
	if turnID, _ := prompt.currentTurn(); turnID != "" {
		data["turn_id"] = turnID
		if value, ok := data["submission"]; !ok || value == "unknown" {
			data["submission"] = "accepted"
		}
	}
	if router := prompt.currentEventRouter(); router != nil {
		if usage, ok := router.Usage(); ok {
			data = acpmeta.WithTurnUsage(data, usage.Metadata(false))
		}
	}
	failure.Data = data
	return failure
}

// codexPromptResponse 把当前 Turn 的最终 Usage 与消息确认写入终态响应。
func codexPromptResponse(
	prompt *activePrompt,
	stopReason acp.StopReason,
	messageID *string,
) acp.PromptResponse {
	response := acp.PromptResponse{
		StopReason:    stopReason,
		UserMessageId: messageID,
	}
	turnID, _ := prompt.currentTurn()
	response.Meta = acpmeta.PositionMetadata(turnID)
	router := prompt.currentEventRouter()
	if router == nil {
		return response
	}
	usage, ok := router.Usage()
	if ok {
		response.Usage = usage.PromptUsage()
		response.Meta = acpmeta.WithTurnUsage(response.Meta, usage.Metadata(stopReason == acp.StopReasonEndTurn))
	}
	return response
}

// installActivePrompt 在统一锁序下同时安装 session 活动槽位与后台 registry 身份。
func (a *Agent) installActivePrompt(state *sessionState, prompt *activePrompt) error {
	a.promptMu.Lock()
	defer a.promptMu.Unlock()
	state.mu.Lock()
	defer state.mu.Unlock()
	if a.promptsClosing {
		return ErrRuntimeUnavailable
	}
	if state.promptClosed {
		return ErrSessionClosing
	}
	if state.activePrompt != nil {
		return ErrPromptActive
	}
	if prompt.compact {
		// Compact has no turn ID in its ACK. Do not let an unresolved start from a
		// prior foreground request provide a candidate identity for this lane.
		for _, owner := range a.prompts {
			if owner.id == state.id {
				return ErrPromptActive
			}
		}
	}
	state.activePrompt = prompt
	a.prompts[prompt] = state
	return nil
}

// closeSessionPrompts 原子关闭旧 session state 的 prompt 安装入口，并返回其所有前台/后台身份。
func (a *Agent) closeSessionPrompts(state *sessionState) []*activePrompt {
	if state == nil {
		return nil
	}
	a.promptMu.Lock()
	defer a.promptMu.Unlock()
	state.mu.Lock()
	state.promptClosed = true
	state.mu.Unlock()
	prompts := make([]*activePrompt, 0)
	for prompt, owner := range a.prompts {
		if owner == state {
			prompts = append(prompts, prompt)
		}
	}
	return prompts
}

// closeAllPrompts 关闭 Adapter 级安装入口，并返回所有尚未完全结束的 prompt。
func (a *Agent) closeAllPrompts() []*activePrompt {
	a.promptMu.Lock()
	defer a.promptMu.Unlock()
	a.promptsClosing = true
	prompts := make([]*activePrompt, 0, len(a.prompts))
	for prompt := range a.prompts {
		prompts = append(prompts, prompt)
	}
	return prompts
}

// finishPromptForeground 标记前台 finally 完成，并在后台也结束后移除 registry 身份。
func (a *Agent) finishPromptForeground(prompt *activePrompt) {
	prompt.markForegroundDone()
	a.untrackPromptIfDone(prompt)
}

// finishPromptBackground 标记 RunTurn goroutine 完成，并在前台也结束后移除 registry 身份。
func (a *Agent) finishPromptBackground(prompt *activePrompt) {
	prompt.markBackgroundDone()
	a.untrackPromptIfDone(prompt)
}

// untrackPromptIfDone 仅在前台与后台信号都关闭后删除精确 prompt 身份。
func (a *Agent) untrackPromptIfDone(prompt *activePrompt) {
	select {
	case <-prompt.foregroundDone:
	default:
		return
	}
	select {
	case <-prompt.backgroundDone:
	default:
		return
	}
	a.promptMu.Lock()
	delete(a.prompts, prompt)
	a.promptMu.Unlock()
}

// onTurnStarted 处理正常或取消后迟到的 turn identity，并触发一次 interrupt。
func (a *Agent) onTurnStarted(state *sessionState, prompt *activePrompt, turnID string) {
	defer prompt.signalTurnStarted()
	late := prompt.setTurn(turnID) || !a.sessions.isCurrent(state)
	if late {
		a.requestPromptInterrupt(state, prompt, turnID, true)
		return
	}
	a.attachTurnEvents(state, prompt, turnID, a.currentSessionUpdater())
}

// attachTurnEvents binds accounting and event delivery to the admitted native identity.
func (a *Agent) attachTurnEvents(state *sessionState, prompt *activePrompt, turnID string, updater sessionUpdater) {
	router := newEventRouter(updater, turnGeneration{
		SessionID:  acp.SessionId(state.id),
		ThreadID:   state.id,
		TurnID:     turnID,
		Generation: prompt.generation,
	}, a, a.logger, state.terminalOutputMode)
	state.mu.Lock()
	if state.usageTotal != nil {
		baseline := *state.usageTotal
		router.handler.previous = &baseline
		router.handler.baselineKnown = true
	}
	state.mu.Unlock()
	router.handler.checkpoint = func(total *turnUsage) {
		state.mu.Lock()
		defer state.mu.Unlock()
		if state.activePrompt == prompt {
			state.usageTotal = total
		}
	}
	prompt.setEventRouter(router)
}

// requestPromptInterrupt 使用 activePrompt 的 Once 保证所有取消来源合计只请求一次。
// resolveInterrupted 为没有前台等待方的迟到 Turn 合成 interrupted 结果。
func (a *Agent) requestPromptInterrupt(
	state *sessionState,
	prompt *activePrompt,
	turnID string,
	resolveInterrupted bool,
) {
	if resolveInterrupted {
		a.client.MarkTurnStale(state.id, turnID)
	}
	prompt.interruptOnce.Do(func() {
		go func() {
			defer close(prompt.interruptDone)
			if resolveInterrupted {
				defer a.client.ResolveTurnInterrupted(state.id, turnID)
			}
			if err := a.client.TurnInterrupt(a.runtimeCtx, state.id, turnID); err != nil {
				a.logger.Warn("Failed to interrupt Codex turn", "thread_id", state.id, "turn_id", turnID, "error", err)
			}
		}()
	})
}

// releaseActivePrompt 只清理仍指向当前身份的前台活动槽位，绝不影响已安装的后继 prompt。
func (a *Agent) releaseActivePrompt(state *sessionState, prompt *activePrompt) {
	state.mu.Lock()
	if state.activePrompt == prompt {
		state.activePrompt = nil
	}
	state.mu.Unlock()
}

// clearActivePrompt 释放无独立 ACP Prompt 所有者的 steering 活动槽位；后台 registry 由 defer 回收。
func (a *Agent) clearActivePrompt(state *sessionState, prompt *activePrompt) {
	a.releaseActivePrompt(state, prompt)
}

// Cancel 幂等取消 session 的 pending/active prompt；turn 未知时不发送 interrupt。
func (a *Agent) Cancel(_ context.Context, notification acp.CancelNotification) error {
	state, ok := a.sessions.get(string(notification.SessionId))
	if !ok {
		return nil
	}
	state.mu.Lock()
	prompt := state.activePrompt
	state.mu.Unlock()
	if prompt == nil {
		return nil
	}
	turnID := prompt.requestCancel()
	if turnID != "" {
		a.requestPromptInterrupt(state, prompt, turnID, false)
	}
	return nil
}

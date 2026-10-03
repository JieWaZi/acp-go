package nativeacp

import (
	"context"
	"time"

	acp "github.com/coder/acp-go-sdk"
)

// promptQueue 保存一个会话的待执行请求，所有字段由 Agent.mutex 保护。
type promptQueue struct {
	// waiting 按入队顺序保存尚未执行的请求。
	waiting []*promptWaiter
	// current 是已经获得原生执行权的请求。
	current *promptWaiter
	// closed 阻止旧会话句柄再次启动执行。
	closed bool
}

// promptWaiter 拥有一次请求的独立取消上下文和执行许可。
type promptWaiter struct {
	// context 同时响应调用者取消与会话生命周期结束。
	context context.Context
	// cancel 解除本地正在等待的 SDK 请求。
	cancel context.CancelFunc
	// ready 在该请求获得执行权时关闭。
	ready chan struct{}
}

// advancePrompt 在当前执行结束且跨代原生取消已经发出后，放行最早的有效请求。
func (agent *Agent) advancePrompt(id acp.SessionId, queue *promptQueue) {
	busy := queue.current != nil || agent.promptCanceling[id] != 0
	if queue.closed || busy {
		return
	}
	for len(queue.waiting) > 0 {
		next := queue.waiting[0]
		queue.waiting = queue.waiting[1:]
		if next.context.Err() != nil {
			continue
		}
		queue.current = next
		close(next.ready)
		return
	}
}

// close 永久废弃这一代队列并解除正在执行和排队请求的等待。
func (queue *promptQueue) close() {
	queue.closed = true
	if queue.current != nil {
		queue.current.cancel()
	}
	for _, waiter := range queue.waiting {
		waiter.cancel()
	}
	queue.waiting = []*promptWaiter{}
}

// acquirePrompt 按进入 Go API 的顺序排队，排队取消不会向原生进程发送 prompt。
func (agent *Agent) acquirePrompt(ctx context.Context, id acp.SessionId) (*promptQueue, *promptWaiter, error) {
	agent.mutex.Lock()
	state := agent.sessions[id]
	if state == nil || state.queue == nil || state.queue.closed {
		agent.mutex.Unlock()
		return nil, nil, acp.NewInvalidParams(map[string]any{"message": "session is not open"})
	}
	queue := state.queue
	turnCtx, cancel := context.WithCancel(ctx)
	waiter := &promptWaiter{context: turnCtx, cancel: cancel, ready: make(chan struct{})}
	queue.waiting = append(queue.waiting, waiter)
	agent.advancePrompt(id, queue)
	agent.mutex.Unlock()
	select {
	case <-waiter.ready:
		if err := turnCtx.Err(); err == nil {
			return queue, waiter, nil
		}
	case <-turnCtx.Done():
	case <-agent.closed:
	case <-agent.conn.Done():
	}
	waiter.cancel()
	agent.releasePrompt(id, queue, waiter)
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	return nil, nil, acp.NewInternalError(map[string]any{"message": "session execution ended"})
}

// releasePrompt 只移除自己所属代的执行状态，恢复后的新队列不会受旧执行清理影响。
func (agent *Agent) releasePrompt(id acp.SessionId, queue *promptQueue, waiter *promptWaiter) {
	agent.mutex.Lock()
	defer agent.mutex.Unlock()
	if agent.turnOwners[id] == waiter {
		delete(agent.turnOwners, id)
		delete(agent.active, id)
		delete(agent.prompts, id)
		delete(agent.turnContexts, id)
		delete(agent.turnCancels, id)
		for toolID, sid := range agent.toolSessions {
			if sid == id {
				delete(agent.toolSessions, toolID)
				delete(agent.toolDetails, toolID)
				delete(agent.toolRevisions, toolID)
			}
		}
	}
	if queue.current == waiter {
		queue.current = nil
	}
	for i, pending := range queue.waiting {
		if pending == waiter {
			queue.waiting = append(queue.waiting[:i], queue.waiting[i+1:]...)
			break
		}
	}
	agent.advancePrompt(id, queue)
}

// promptFIFO 使用会话独立队列转发原生执行，并保留真实响应与错误。
func (agent *Agent) promptFIFO(ctx context.Context, request acp.PromptRequest) (acp.PromptResponse, error) {
	queue, waiter, err := agent.acquirePrompt(ctx, request.SessionId)
	if err != nil {
		return acp.PromptResponse{}, err
	}
	defer waiter.cancel()
	defer agent.releasePrompt(request.SessionId, queue, waiter)
	agent.mutex.Lock()
	if queue.closed || waiter.context.Err() != nil {
		agent.mutex.Unlock()
		return acp.PromptResponse{}, context.Canceled
	}
	agent.turnOwners[request.SessionId] = waiter
	agent.active[request.SessionId] = true
	agent.prompts[request.SessionId] = append([]acp.ContentBlock{}, request.Prompt...)
	agent.turnContexts[request.SessionId] = waiter.context
	agent.turnCancels[request.SessionId] = waiter.cancel
	agent.mutex.Unlock()
	response, err := sendNativeRequest[acp.PromptResponse](agent, waiter.context, "session/prompt", request)
	if ctx.Err() != nil {
		cancelCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
		defer cancel()
		_ = agent.cancelPromptOwner(cancelCtx, acp.CancelNotification{SessionId: request.SessionId}, waiter)
	}
	return response, err
}

// cancelFIFO 取消当前本地等待，并在放行下一轮前保留原生会话取消通知。
func (agent *Agent) cancelFIFO(ctx context.Context, request acp.CancelNotification) error {
	return agent.cancelPromptOwner(ctx, request, nil)
}

// cancelPromptOwner 在同一锁下确认自动取消所属执行代，并建立跨代原生通知屏障。
// 显式 Cancel 没有 owner 限制；旧执行的自动取消不能中断已恢复的新执行。
func (agent *Agent) cancelPromptOwner(
	ctx context.Context,
	request acp.CancelNotification,
	owner *promptWaiter,
) error {
	agent.mutex.Lock()
	var queue *promptQueue
	if state := agent.sessions[request.SessionId]; state != nil {
		queue = state.queue
	}
	if owner != nil {
		stale := queue == nil || queue.current != owner
		if stale || agent.turnOwners[request.SessionId] != owner {
			agent.mutex.Unlock()
			return nil
		}
	}
	agent.promptCanceling[request.SessionId]++
	if queue != nil {
		if queue.current != nil {
			queue.current.cancel()
		}
	}
	agent.mutex.Unlock()
	err := agent.conn.SendNotification(ctx, "session/cancel", request)
	agent.mutex.Lock()
	agent.promptCanceling[request.SessionId]--
	if agent.promptCanceling[request.SessionId] == 0 {
		delete(agent.promptCanceling, request.SessionId)
	}
	// 发送期间可能发生 close/load/resume；释放的是当前代，不是已废弃的旧队列。
	if state := agent.sessions[request.SessionId]; state != nil && state.queue != nil {
		agent.advancePrompt(request.SessionId, state.queue)
	}
	agent.mutex.Unlock()
	return err
}

// closePromptQueues 在进程关闭时一次解除所有会话的本地等待。
func (agent *Agent) closePromptQueues() {
	agent.mutex.Lock()
	defer agent.mutex.Unlock()
	for _, state := range agent.sessions {
		if state.queue != nil {
			state.queue.close()
		}
	}
}

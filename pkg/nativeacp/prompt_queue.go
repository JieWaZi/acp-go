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
	// canceling 阻止取消通知发出前启动下一轮，避免原生 cancel 误伤后续请求。
	canceling int
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

// advance 在原生取消通知已经发出且当前执行结束后，放行最早的有效请求。
func (queue *promptQueue) advance() {
	if queue.closed || queue.current != nil || queue.canceling != 0 {
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
	queue.advance()
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
	queue.advance()
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
		_ = agent.cancelFIFO(cancelCtx, acp.CancelNotification{SessionId: request.SessionId})
	}
	return response, err
}

// cancelFIFO 取消当前本地等待，并在放行下一轮前保留原生会话取消通知。
func (agent *Agent) cancelFIFO(ctx context.Context, request acp.CancelNotification) error {
	agent.mutex.Lock()
	var queue *promptQueue
	if state := agent.sessions[request.SessionId]; state != nil {
		queue = state.queue
	}
	if queue != nil {
		queue.canceling++
		if queue.current != nil {
			queue.current.cancel()
		}
	}
	agent.mutex.Unlock()
	err := agent.conn.SendNotification(ctx, "session/cancel", request)
	agent.mutex.Lock()
	if queue != nil {
		queue.canceling--
		queue.advance()
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

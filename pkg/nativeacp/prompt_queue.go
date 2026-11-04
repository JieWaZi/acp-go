package nativeacp

import (
	"context"
	"time"

	acp "github.com/JieWaZi/acp-go/sdk"
)

// PromptDelivery describes local frame delivery, not native acceptance.
type PromptDelivery string

const (
	DeliveryNotSent   PromptDelivery = "not_sent"
	DeliverySent      PromptDelivery = "sent"
	DeliveryUncertain PromptDelivery = "uncertain"
)

// promptQueue owns admission for one opened session generation.
type promptQueue struct {
	// waiting preserves FIFO admission without sending queued requests.
	waiting []*promptWaiter
	// current retains execution until native completion and cancellation delivery.
	current *promptWaiter
	// closed rejects admission to a retired session generation.
	closed bool
}

// promptWaiter owns preparation, interaction and delivery for one operation.
// All mutable fields are protected by Agent.mutex.
type promptWaiter struct {
	// context is cancelled by the caller, explicit cancellation, or owner shutdown.
	context context.Context
	// cancel revokes preparation and interactions without abandoning native completion.
	cancel context.CancelFunc
	// ready grants this operation native admission.
	ready chan struct{}
	// done closes after native completion and any ordered cancellation write.
	done chan struct{}
	// prompt holds only the currently executing user evidence.
	prompt []acp.ContentBlock
	// committed marks the write-gate decision, before transport Write returns.
	committed bool
	// delivery records the transport's final byte result.
	delivery PromptDelivery
	// cancelDone retains the one ordered native cancellation operation.
	cancelDone chan struct{}
	// finished prevents delayed cancellation from targeting a successor.
	finished bool
}

func (agent *Agent) advancePrompt(id acp.SessionId, queue *promptQueue) {
	if queue.closed || queue.current != nil || agent.turnOwners[id] != nil {
		return
	}
	for len(queue.waiting) > 0 {
		next := queue.waiting[0]
		queue.waiting = queue.waiting[1:]
		if next.context.Err() != nil {
			continue
		}
		queue.current = next
		agent.turnOwners[id] = next
		close(next.ready)
		return
	}
}

func (queue *promptQueue) close() {
	queue.closed = true
	if queue.current != nil {
		queue.current.cancel()
	}
	for _, waiter := range queue.waiting {
		waiter.cancel()
	}
	queue.waiting = nil
}

func (agent *Agent) acquirePrompt(ctx context.Context, request acp.PromptRequest) (*promptQueue, *promptWaiter, error) {
	agent.mutex.Lock()
	state := agent.sessions[request.SessionId]
	if state == nil || state.queue == nil || state.queue.closed {
		agent.mutex.Unlock()
		return nil, nil, acp.NewInvalidParams(map[string]any{"message": "session is not open"})
	}
	queue := state.queue
	if !agent.config.PromptFIFO && (queue.current != nil || agent.turnOwners[request.SessionId] != nil) {
		agent.mutex.Unlock()
		return nil, nil, acp.NewInvalidParams(map[string]any{"message": "session already has an active prompt"})
	}
	turnCtx, cancel := context.WithCancel(ctx)
	waiter := &promptWaiter{context: turnCtx, cancel: cancel, ready: make(chan struct{}), done: make(chan struct{}),
		prompt: append([]acp.ContentBlock(nil), request.Prompt...), delivery: DeliveryNotSent}
	queue.waiting = append(queue.waiting, waiter)
	agent.advancePrompt(request.SessionId, queue)
	agent.mutex.Unlock()
	select {
	case <-waiter.ready:
		if turnCtx.Err() == nil {
			return queue, waiter, nil
		}
	case <-turnCtx.Done():
	case <-agent.closed:
	case <-agent.conn.Done():
	}
	agent.releasePrompt(request.SessionId, queue, waiter)
	return nil, nil, acp.NewRequestCancelled(map[string]any{"message": "prompt admission cancelled"})
}

// releasePrompt seals cancellation before allowing another operation to enter.
func (agent *Agent) releasePrompt(id acp.SessionId, queue *promptQueue, waiter *promptWaiter) {
	agent.mutex.Lock()
	waiter.finished = true
	cancelDone := waiter.cancelDone
	agent.mutex.Unlock()
	if cancelDone != nil {
		<-cancelDone
	}
	waiter.cancel()
	agent.mutex.Lock()
	defer agent.mutex.Unlock()
	close(waiter.done)
	if agent.turnOwners[id] == waiter {
		delete(agent.turnOwners, id)
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
	if state := agent.sessions[id]; state != nil && state.queue != nil {
		agent.advancePrompt(id, state.queue)
	}
}

// PromptWithReceipt runs preparation and submission under the same native owner.
func (agent *Agent) PromptWithReceipt(ctx context.Context, request acp.PromptRequest) (acp.PromptResponse, PromptDelivery, error) {
	queue, waiter, err := agent.acquirePrompt(ctx, request)
	if err != nil {
		return acp.PromptResponse{}, DeliveryNotSent, err
	}
	defer agent.releasePrompt(request.SessionId, queue, waiter)
	stop := context.AfterFunc(ctx, func() {
		cancelCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
		defer cancel()
		_ = agent.cancelPromptOwner(cancelCtx, acp.CancelNotification{SessionId: request.SessionId}, waiter)
	})
	defer stop()
	if prepare := agent.config.PreparePrompt; prepare != nil {
		if err := prepare(waiter.context, request); err != nil {
			return acp.PromptResponse{}, DeliveryNotSent, err
		}
	}
	response, err := acp.SendRequestWithWriteHooks[acp.PromptResponse](agent.conn, context.WithoutCancel(ctx), "session/prompt", request, acp.RequestWriteHooks{
		Context: waiter.context,
		Commit: func() error {
			agent.mutex.Lock()
			defer agent.mutex.Unlock()
			if queue.closed || waiter.context.Err() != nil || agent.turnOwners[request.SessionId] != waiter {
				return context.Canceled
			}
			waiter.committed = true
			waiter.delivery = DeliveryUncertain
			if state := agent.sessions[request.SessionId]; state != nil {
				state.messageID, state.messageKnownID = "", ""
			}
			return nil
		},
		Complete: func(result acp.RequestWriteResult) {
			agent.mutex.Lock()
			defer agent.mutex.Unlock()
			if result.Err == nil && result.BytesWritten == result.FrameBytes {
				waiter.delivery = DeliverySent
			}
		},
	})
	agent.mutex.Lock()
	delivery := waiter.delivery
	cancelled := waiter.context.Err() != nil
	agent.mutex.Unlock()
	if delivery == DeliveryNotSent && cancelled {
		err = acp.NewRequestCancelled(map[string]any{"message": "prompt cancelled before submission"})
	}
	return response, delivery, agent.transportError(err)
}

// PromptCommitted can be read by a notification filter outside its own locks.
func (agent *Agent) PromptCommitted(id acp.SessionId) bool {
	agent.mutex.Lock()
	defer agent.mutex.Unlock()
	owner := agent.turnOwners[id]
	return owner != nil && owner.committed && !owner.finished
}

// cancelPromptOwner never sends a cancellation for an unsubmitted or retired owner.
func (agent *Agent) cancelPromptOwner(ctx context.Context, request acp.CancelNotification, owner *promptWaiter) error {
	agent.mutex.Lock()
	current := agent.turnOwners[request.SessionId]
	if current == nil || current.finished || (owner != nil && current != owner) {
		agent.mutex.Unlock()
		return nil
	}
	current.cancel()
	if !current.committed || current.cancelDone != nil {
		agent.mutex.Unlock()
		return nil
	}
	current.cancelDone = make(chan struct{})
	agent.mutex.Unlock()
	// A bounded cancellation write also breaks a blocked prompt write through
	// connection invalidation. Releasing the owner before this write is unsafe.
	cancelCtx, cancel := context.WithTimeout(ctx, time.Second)
	stopWrite := context.AfterFunc(cancelCtx, agent.conn.Close)
	err := agent.conn.SendNotification(cancelCtx, "session/cancel", request)
	stopWrite()
	cancel()
	if err != nil {
		agent.conn.Close()
	}
	close(current.cancelDone)
	go func() {
		timer := time.NewTimer(time.Second)
		defer timer.Stop()
		select {
		case <-current.done:
		case <-agent.conn.Done():
		case <-timer.C:
			// Check ownership under the admission lock: an expired timer must
			// never close a successor after the old terminal already arrived.
			agent.mutex.Lock()
			if !current.finished && agent.turnOwners[request.SessionId] == current {
				agent.conn.Close()
			}
			agent.mutex.Unlock()
		}
	}()
	return err
}

func (agent *Agent) closePromptQueues() {
	agent.mutex.Lock()
	defer agent.mutex.Unlock()
	for _, state := range agent.sessions {
		if state.queue != nil {
			state.queue.close()
		}
	}
}

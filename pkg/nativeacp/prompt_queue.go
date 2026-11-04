package nativeacp

import (
	"context"
	"errors"
	"sync"
	"time"

	acp "github.com/JieWaZi/acp-go/sdk"
)

// nativeStopGrace matches the observed native cancellation window, independently of wire writes.
const nativeStopGrace = 10 * time.Second

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
	if queue.closed || agent.turnOwners[id] != nil {
		return
	}
	for len(queue.waiting) > 0 {
		next := queue.waiting[0]
		queue.waiting = queue.waiting[1:]
		if next.context.Err() != nil {
			continue
		}
		agent.turnOwners[id] = next
		close(next.ready)
		return
	}
}

func (agent *Agent) closePromptQueue(id acp.SessionId, queue *promptQueue) {
	queue.closed = true
	if owner := agent.turnOwners[id]; owner != nil {
		owner.cancel()
	}
	for _, waiter := range queue.waiting {
		waiter.cancel()
	}
	queue.waiting = nil
}

func (agent *Agent) reservePrompt(ctx context.Context, request acp.PromptRequest) (*promptQueue, *promptWaiter, error) {
	agent.mutex.Lock()
	if err := agent.conn.Err(); err != nil {
		agent.mutex.Unlock()
		return nil, nil, err
	}
	state := agent.sessions[request.SessionId]
	if state == nil || state.transitioning || state.queue == nil || state.queue.closed {
		agent.mutex.Unlock()
		return nil, nil, acp.NewInvalidParams(map[string]any{"message": "session is not open"})
	}
	queue := state.queue
	if !agent.config.PromptFIFO && agent.turnOwners[request.SessionId] != nil {
		agent.mutex.Unlock()
		return nil, nil, acp.NewInvalidParams(map[string]any{"message": "session already has an active prompt"})
	}
	turnCtx, cancel := context.WithCancel(ctx)
	waiter := &promptWaiter{context: turnCtx, cancel: cancel, ready: make(chan struct{}), done: make(chan struct{}),
		prompt: append([]acp.ContentBlock(nil), request.Prompt...), delivery: DeliveryNotSent}
	queue.waiting = append(queue.waiting, waiter)
	agent.advancePrompt(request.SessionId, queue)
	agent.mutex.Unlock()
	return queue, waiter, nil
}

func (agent *Agent) awaitPromptAdmission(waiter *promptWaiter) error {
	select {
	case <-waiter.ready:
		if waiter.context.Err() == nil && agent.conn.Err() == nil {
			return nil
		}
	case <-waiter.context.Done():
	case <-agent.closed:
	case <-agent.conn.Done():
	}
	if err := agent.conn.Err(); err != nil {
		return agent.transportError(err)
	}
	return acp.NewRequestCancelled(map[string]any{"message": "prompt admission cancelled"})
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

// PromptWithReceipt uses the same reservation as receive-order admission.
func (agent *Agent) PromptWithReceipt(ctx context.Context, request acp.PromptRequest) (acp.PromptResponse, PromptDelivery, error) {
	run, err := agent.ReservePromptWithReceipt(ctx, request, nil)
	if err != nil {
		return acp.PromptResponse{}, DeliveryNotSent, err
	}
	return run()
}

// ReservePromptWithReceipt registers the execution before returning its one-shot runner.
// It performs no preparation, native I/O, or admission waiting on the receive path.
func (agent *Agent) ReservePromptWithReceipt(ctx context.Context, request acp.PromptRequest, prepare acp.PromptPreparation) (func() (acp.PromptResponse, PromptDelivery, error), error) {
	queue, waiter, err := agent.reservePrompt(ctx, request)
	if err != nil {
		return nil, err
	}
	var once sync.Once
	return func() (acp.PromptResponse, PromptDelivery, error) {
		accepted := false
		once.Do(func() { accepted = true })
		if !accepted {
			return acp.PromptResponse{}, DeliveryNotSent, acp.NewInvalidParams(map[string]any{"message": "prompt runner already consumed"})
		}
		return agent.runPrompt(ctx, request, prepare, queue, waiter)
	}, nil
}

func (agent *Agent) runPrompt(ctx context.Context, request acp.PromptRequest, prepare acp.PromptPreparation, queue *promptQueue, waiter *promptWaiter) (acp.PromptResponse, PromptDelivery, error) {
	defer agent.releasePrompt(request.SessionId, queue, waiter)
	if err := agent.awaitPromptAdmission(waiter); err != nil {
		return acp.PromptResponse{}, DeliveryNotSent, err
	}
	if prepare != nil {
		cleanup, err := prepare(waiter.context)
		if cleanup != nil {
			defer cleanup()
		}
		if err != nil {
			return acp.PromptResponse{}, DeliveryNotSent, agent.transportError(errors.Join(err, agent.conn.Err()))
		}
	}
	stop := context.AfterFunc(ctx, func() {
		cancelCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
		defer cancel()
		_ = agent.cancelPromptOwner(cancelCtx, acp.CancelNotification{SessionId: request.SessionId}, waiter)
	})
	defer stop()
	if prepare := agent.config.PreparePrompt; prepare != nil {
		if err := prepare(waiter.context, request); err != nil {
			return acp.PromptResponse{}, DeliveryNotSent, agent.transportError(errors.Join(err, agent.conn.Err()))
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
			switch result.BytesWritten {
			case 0:
				waiter.delivery = DeliveryNotSent
			case result.FrameBytes:
				waiter.delivery = DeliverySent
			default:
				waiter.delivery = DeliveryUncertain
			}
		},
	})
	agent.mutex.Lock()
	delivery := waiter.delivery
	cancelled := waiter.context.Err() != nil
	agent.mutex.Unlock()
	if delivery == DeliveryNotSent && cancelled && !waiter.committed {
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

// BeginPromptCancel captures the exact owner while receiving the cancellation frame.
// A nil runner means the cancellation was handled without native submission.
func (agent *Agent) BeginPromptCancel(request acp.CancelNotification) (func(context.Context) error, error) {
	return agent.beginPromptCancel(request, nil), nil
}

func (agent *Agent) cancelPromptOwner(ctx context.Context, request acp.CancelNotification, owner *promptWaiter) error {
	if run := agent.beginPromptCancel(request, owner); run != nil {
		return run(ctx)
	}
	return nil
}

func (agent *Agent) beginPromptCancel(request acp.CancelNotification, owner *promptWaiter) func(context.Context) error {
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
	var once sync.Once
	return func(ctx context.Context) error {
		accepted := false
		once.Do(func() { accepted = true })
		if !accepted {
			return nil
		}
		return agent.deliverPromptCancel(ctx, request, current)
	}
}

func (agent *Agent) deliverPromptCancel(ctx context.Context, request acp.CancelNotification, current *promptWaiter) error {
	// A bounded cancellation write also breaks a blocked prompt write through
	// connection invalidation. Releasing the owner before this write is unsafe.
	cancelCtx, cancel := context.WithTimeout(ctx, time.Second)
	writeStopped := make(chan struct{})
	stopWrite := context.AfterFunc(cancelCtx, func() { agent.conn.Close(); close(writeStopped) })
	err := agent.conn.SendNotification(cancelCtx, "session/cancel", request)
	if !stopWrite() {
		<-writeStopped
	}
	cancel()
	if err != nil {
		agent.conn.Close()
	}
	close(current.cancelDone)
	go func() {
		timer := time.NewTimer(nativeStopGrace)
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
	for id, state := range agent.sessions {
		if state.queue != nil {
			agent.closePromptQueue(id, state.queue)
		}
	}
}

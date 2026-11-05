package pi

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/JieWaZi/acp-go/pkg/acpmeta"
	agentclient "github.com/JieWaZi/acp-go/pkg/agent_next"
	acp "github.com/JieWaZi/acp-go/sdk"
)

type invocation struct {
	scope      agentclient.Scope
	ctx        context.Context
	cancel     context.CancelCauseFunc
	eventQueue chan []byte
	uiQueue    chan uiRequest

	mu          sync.Mutex
	queuedBytes int
	submitted   bool
	sealed      bool
	controlDone chan struct{}
}

func (t *invocation) enqueueEvent(line []byte) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.submitted || t.sealed {
		return true
	}
	if t.queuedBytes+len(line) > maxRecord || len(t.eventQueue) == cap(t.eventQueue) {
		return false
	}
	t.queuedBytes += len(line)
	t.eventQueue <- append([]byte(nil), line...)
	return true
}

func (t *invocation) enqueueUI(ui uiRequest, bytes int) (queued, withinBudget bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.submitted || t.sealed {
		return false, true
	}
	if t.queuedBytes+bytes > maxRecord || len(t.uiQueue) == cap(t.uiQueue) {
		return false, false
	}
	ui.bytes = bytes
	t.queuedBytes += bytes
	t.uiQueue <- ui
	return true, true
}

func (c *Client) ReservePrompt(ctx context.Context, r acp.PromptRequest, options agentclient.PromptOptions) (func() agentclient.PromptResult, error) {
	message, images, err := promptContent(r.Prompt)
	if err != nil {
		return nil, err
	}
	s, err := c.session(r.SessionId)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.busy || s.closing || s.rpc.err() != nil {
		return nil, errors.New("Pi session cannot accept another prompt")
	}
	c.mu.Lock()
	ledger := c.ledger[r.SessionId]
	if c.closing || ledger == nil || len(ledger.outcomes)+ledger.producers >= 32 || c.retainedOutcomes >= 128 {
		c.mu.Unlock()
		return nil, errors.New("Pi session is closing or its native outcome budget is exhausted")
	}
	ledger.producers++
	c.retainedOutcomes++
	retain := c.foregroundReceipts
	signalLedger(ledger)
	c.mu.Unlock()
	if options.HostInvocationID == "" {
		options.HostInvocationID = newID()
	}
	input := agentclient.PromptSubmission{SessionID: r.SessionId, HostInvocationID: options.HostInvocationID, InputID: newID()}
	lifetime, cancel := context.WithCancelCause(ctx)
	t := &invocation{
		scope: agentclient.Scope{SessionID: r.SessionId, HostInvocationID: options.HostInvocationID, NativeInvocationID: newID()},
		ctx:   lifetime, cancel: cancel, eventQueue: make(chan []byte, maxQueued), uiQueue: make(chan uiRequest, 8),
	}
	s.busy, s.turn = true, t
	stop := context.AfterFunc(lifetime, func() { s.interrupt(t) })
	var continued atomic.Bool
	return func() agentclient.PromptResult {
		if !continued.CompareAndSwap(false, true) {
			return agentclient.PromptResult{Receipt: agentclient.PromptReceipt{
				Input: input, Submission: agentclient.SubmissionNotSent, Admission: agentclient.AdmissionNotAccepted,
				OperationError: errors.New("Pi prompt continuation was already consumed"),
			}}
		}
		defer stop()
		defer cancel(context.Canceled)
		result := s.prompt(t, input, options, message, images)
		c.mu.Lock()
		ledger.producers--
		if result.Outcome != nil && (retain || t.ctx.Err() != nil || result.Outcome.DeliveryError != nil || result.Outcome.OperationError != nil) {
			ledger.outcomes = append(ledger.outcomes, result.Outcome.Snapshot())
		} else {
			c.retainedOutcomes--
		}
		if ledger.producers == 0 && len(ledger.outcomes) == 0 && c.sessions[r.SessionId] == nil {
			delete(c.ledger, r.SessionId)
		}
		signalLedger(ledger)
		c.mu.Unlock()
		s.mu.Lock()
		s.turn, s.busy = nil, false
		s.mu.Unlock()
		return result
	}, nil
}

func signalLedger(l *outcomeLedger) {
	close(l.changed)
	l.changed = make(chan struct{})
}

func (c *Client) ReadNativeOutcomes(ctx context.Context, r acpmeta.NativeTurnReceiptsRequest, wait bool) (agentclient.NativeOutcomePage, error) {
	if r.SessionID == "" || len(r.AckInvocationIDs) > 128 {
		return agentclient.NativeOutcomePage{}, acp.NewInvalidParams(nil)
	}
	if err := context.Cause(ctx); err != nil {
		return agentclient.NativeOutcomePage{}, err
	}
	acked := make(map[string]bool, len(r.AckInvocationIDs))
	for _, id := range r.AckInvocationIDs {
		acked[id] = true
	}
	for {
		c.mu.Lock()
		ledger := c.ledger[acp.SessionId(r.SessionID)]
		if ledger == nil {
			c.mu.Unlock()
			return agentclient.NativeOutcomePage{}, nil
		}
		remaining := ledger.outcomes[:0]
		for _, outcome := range ledger.outcomes {
			if !acked[outcome.Turn.InvocationID] {
				remaining = append(remaining, outcome)
			}
		}
		c.retainedOutcomes -= len(ledger.outcomes) - len(remaining)
		clear(ledger.outcomes[len(remaining):])
		ledger.outcomes = remaining
		var nativeDone <-chan struct{}
		if opened := c.sessions[acp.SessionId(r.SessionID)]; opened != nil && !c.closing {
			select {
			case <-opened.rpc.done:
			default:
				nativeDone = opened.rpc.done
			}
		}
		if len(remaining) > 0 || !wait || ledger.producers == 0 && nativeDone == nil {
			page := agentclient.NativeOutcomePage{More: len(remaining) > 16}
			for _, outcome := range remaining[:min(16, len(remaining))] {
				page.Outcomes = append(page.Outcomes, outcome.Snapshot())
			}
			if ledger.producers == 0 && len(remaining) == 0 && c.sessions[acp.SessionId(r.SessionID)] == nil {
				delete(c.ledger, acp.SessionId(r.SessionID))
			}
			c.mu.Unlock()
			return page, nil
		}
		changed := ledger.changed
		c.mu.Unlock()
		select {
		case <-changed:
		case <-nativeDone:
		case <-ctx.Done():
			return agentclient.NativeOutcomePage{}, context.Cause(ctx)
		}
	}
}

func (c *Client) Cancel(n acp.CancelNotification) error {
	s, err := c.session(n.SessionId)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.turn != nil {
		s.turn.cancel(context.Canceled)
		s.interrupt(s.turn)
	}
	return nil
}

func (s *session) interrupt(t *invocation) {
	t.mu.Lock()
	if !t.submitted || t.sealed || t.controlDone != nil {
		t.mu.Unlock()
		return
	}
	done := make(chan struct{})
	t.controlDone = done
	t.mu.Unlock()
	go func() {
		defer close(done)
		ctx, cancel := context.WithTimeout(context.Background(), controlTimeout)
		defer cancel()
		if err := s.rpc.call(ctx, "clear_queue", nil, nil); err == nil {
			if err := s.rpc.call(ctx, "abort", nil, nil); err == nil {
				return
			}
		}
		// Older Pi versions lack clear_queue. Restarting from the same
		// history is the only safe way to discard an unverified queue.
		s.poison(errors.New("Pi cancellation could not confirm native queue cleanup"))
	}()
}

func (s *session) poison(err error) {
	s.rpc.fail(err)
	ctx, cancel := context.WithTimeout(context.Background(), controlTimeout)
	defer cancel()
	_ = s.rpc.process.Close(ctx)
}

func (s *session) prompt(t *invocation, input agentclient.PromptSubmission, options agentclient.PromptOptions, message string, images []any) (result agentclient.PromptResult) {
	result.Receipt = agentclient.PromptReceipt{Input: input, Submission: agentclient.SubmissionNotSent, Admission: agentclient.AdmissionNotAccepted}
	cleanup, err := options.PrepareSubmission(t.ctx, input)
	if cleanup != nil {
		defer cleanup()
	}
	if err != nil {
		result.Receipt.OperationError = err
		return result
	}
	interactiveDone := make(chan struct{})
	uiCtx, cancelUI := context.WithCancel(t.ctx)
	go func() {
		defer close(interactiveDone)
		for {
			select {
			case <-uiCtx.Done():
				return
			case ui := <-t.uiQueue:
				s.handleUI(uiCtx, t.scope, ui)
				t.mu.Lock()
				t.queuedBytes -= ui.bytes
				t.mu.Unlock()
			}
		}
	}()
	s.rpc.mu.Lock()
	s.rpc.active = t
	s.rpc.mu.Unlock()
	defer func() {
		t.mu.Lock()
		t.sealed = true
		control := t.controlDone
		t.mu.Unlock()
		s.rpc.mu.Lock()
		if s.rpc.active == t {
			s.rpc.active = nil
		}
		s.rpc.mu.Unlock()
		cancelUI()
		deadline := time.NewTimer(controlTimeout + time.Second)
		defer deadline.Stop()
		if control != nil {
			select {
			case <-control:
			case <-deadline.C:
				s.poison(errors.New("Pi cancellation control did not finish"))
				return
			}
		}
		select {
		case <-interactiveDone:
		case <-deadline.C:
			s.poison(errors.New("Pi dialog delivery did not finish"))
		}
	}()

	args := map[string]any{"message": message}
	if len(images) > 0 {
		args["images"] = images
	}
	var ack struct{ Disposition string }
	// Context cancellation independently starts clear_queue/abort. Continue
	// collecting the response and native terminal through that control task.
	ackCtx, cancelAck := context.WithTimeout(context.WithoutCancel(t.ctx), 20*time.Second)
	sent, err := s.rpc.request(ackCtx, s.rpc.nextID(), "prompt", args, &ack, func() bool {
		t.mu.Lock()
		defer t.mu.Unlock()
		if t.ctx.Err() != nil {
			return false
		}
		t.submitted = true
		return true
	})
	cancelAck()
	result.Receipt.Submission = sent
	if err != nil {
		var rejection *rpcRejection
		switch {
		case errors.As(err, &rejection):
			result.Receipt.OperationError = nativeFailure(rejection.detail)
		case sent == agentclient.SubmissionNotSent:
			result.Receipt.OperationError = err
		default:
			result.Receipt.Admission = agentclient.AdmissionUnknown
			result.Receipt.OperationError = err
			s.rpc.fail(err)
		}
		if result.Receipt.Admission != agentclient.AdmissionUnknown {
			return result
		}
		defer s.poison(err)
	} else {
		result.Receipt.Admission = agentclient.AdmissionConfirmed
	}
	if err == nil && ack.Disposition == "handled" {
		result.Outcome = &agentclient.NativeOutcome{
			Turn: acpmeta.NativeTurn{
				InvocationID: t.scope.NativeInvocationID, Origin: acpmeta.NativeTurnOriginHost,
				ConsumedInputIDs: []string{input.InputID}, Status: acpmeta.NativeTurnCompleted,
			},
			StopReason: pointer(acp.StopReasonEndTurn), PossibleSideEffects: true,
		}
		return result
	}

	o := &agentclient.NativeOutcome{
		Turn:                acpmeta.NativeTurn{InvocationID: t.scope.NativeInvocationID, Origin: acpmeta.NativeTurnOriginUnknown},
		PossibleSideEffects: true,
	}
	result.Outcome = o
	stream := newTurnEvents(s, t, o, input.InputID)
	waiting := t.ctx.Done()
	var cancelTimer *time.Timer
	var forced <-chan time.Time
	defer func() {
		if cancelTimer != nil {
			cancelTimer.Stop()
		}
	}()
	apply := func(line []byte) bool {
		t.mu.Lock()
		t.queuedBytes -= len(line)
		t.mu.Unlock()
		return stream.apply(line)
	}
	defer func() {
		stream.finish()
		if stream.started {
			result.Receipt.Admission = agentclient.AdmissionConfirmed
			if o.Turn.Status != "" {
				// The native terminal subsumes an ACK lost to disconnect.
				result.Receipt.OperationError = nil
			}
		} else if result.Receipt.Admission == agentclient.AdmissionUnknown {
			result.Outcome = nil
		}
	}()
	for {
		select {
		case line := <-t.eventQueue:
			if apply(line) {
				return result
			}
		case <-s.rpc.done:
			// The pipe reader publishes every complete record before its EOF.
			for {
				select {
				case line := <-t.eventQueue:
					if apply(line) {
						return result
					}
				default:
					o.OperationError = s.rpc.err()
					return result
				}
			}
		case <-waiting:
			waiting = nil
			cancelTimer = time.NewTimer(controlTimeout + time.Second)
			forced = cancelTimer.C
		case <-forced:
			o.OperationError = context.Cause(t.ctx)
			s.poison(o.OperationError)
			return result
		}
	}
}

func newID() string {
	var raw [16]byte
	rand.Read(raw[:])
	raw[6] = raw[6]&0x0f | 0x40
	raw[8] = raw[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", raw[:4], raw[4:6], raw[6:8], raw[8:10], raw[10:])
}

func promptContent(blocks []acp.ContentBlock) (string, []any, error) {
	var text strings.Builder
	var images []any
	for _, block := range blocks {
		switch {
		case block.Text != nil:
			text.WriteString(block.Text.Text)
		case block.Image != nil:
			images = append(images, map[string]any{"type": "image", "data": block.Image.Data, "mimeType": block.Image.MimeType})
		case block.ResourceLink != nil:
			fmt.Fprintf(&text, "\n[Context] %s", block.ResourceLink.Uri)
		case block.Resource != nil:
			resource := block.Resource.Resource
			switch {
			case resource.TextResourceContents != nil:
				fmt.Fprintf(&text, "\n[Embedded Context] %s\n%s", resource.TextResourceContents.Uri, resource.TextResourceContents.Text)
			case resource.BlobResourceContents != nil:
				data, err := base64.StdEncoding.DecodeString(resource.BlobResourceContents.Blob)
				if err != nil {
					return "", nil, acp.NewInvalidParams(nil)
				}
				fmt.Fprintf(&text, "\n[Embedded Context] %s (%d bytes)", resource.BlobResourceContents.Uri, len(data))
			default:
				return "", nil, acp.NewInvalidParams(nil)
			}
		default:
			return "", nil, acp.NewInvalidParams(nil)
		}
	}
	if text.Len() == 0 && len(images) == 0 {
		return "", nil, acp.NewInvalidParams(nil)
	}
	return text.String(), images, nil
}

func (c *Client) Compact(ctx context.Context, r acpmeta.CompactRequest) (acpmeta.CompactResponse, error) {
	s, err := c.session(acp.SessionId(r.SessionID))
	if err != nil {
		return acpmeta.CompactResponse{}, err
	}
	end, err := s.begin()
	if err != nil {
		return acpmeta.CompactResponse{}, err
	}
	defer end()
	var raw json.RawMessage
	sent, err := s.rpc.request(ctx, s.rpc.nextID(), "compact", nil, &raw, nil)
	if err != nil {
		var rejected *rpcRejection
		if sent != agentclient.SubmissionNotSent && !errors.As(err, &rejected) {
			s.poison(err)
		}
		return acpmeta.CompactResponse{}, err
	}
	return acpmeta.CompactResponse{Status: acpmeta.NativeTurnCompleted}, nil
}

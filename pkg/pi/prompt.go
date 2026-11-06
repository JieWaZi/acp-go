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

	acp "github.com/JieWaZi/acp-go-sdk"
	"github.com/JieWaZi/acp-go/pkg/acpmeta"
	agentclient "github.com/JieWaZi/acp-go/pkg/agentclient"
)

type invocation struct {
	// scope freezes the identities carried by observations and interactions.
	scope agentclient.Scope
	// ctx is the prompt lifetime shared by preparation and dialogs.
	ctx context.Context
	// cancel captures the first cause that interrupts this prompt.
	cancel context.CancelCauseFunc
	// events folds native facts exclusively in the pipe reader until sealed.
	events *turnEvents
	// eventDone closes after the reader folds a terminal or invalid event.
	eventDone chan struct{}
	// uiQueue holds interactive requests that can precede the prompt acknowledgment.
	uiQueue chan uiRequest

	// mu guards event ownership, the dialog budget, and the send boundary.
	mu sync.Mutex
	// queuedBytes counts pending interactive dialog payloads.
	queuedBytes int
	// submitted marks that the prompt passed its final write gate.
	submitted bool
	// sealed prevents further records from joining the settled prompt.
	sealed bool
	// controlDone joins the single cancellation task before session reuse.
	controlDone chan struct{}
}

func (t *invocation) applyEvent(line []byte) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.submitted || t.sealed {
		return nil
	}
	// This fold performs no host callbacks or I/O. The reader remains the
	// sole event writer, so responses cannot overtake preceding native facts.
	if t.events.apply(line) {
		t.sealed = true
		close(t.eventDone)
		return t.events.outcome.OperationError
	}
	return nil
}

func (t *invocation) releaseBytes(bytes int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.queuedBytes -= bytes
}

func (t *invocation) seal() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.sealed = true
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
		ctx:   lifetime, cancel: cancel, eventDone: make(chan struct{}), uiQueue: make(chan uiRequest, 8),
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
	o := &agentclient.NativeOutcome{
		Turn:                acpmeta.NativeTurn{InvocationID: t.scope.NativeInvocationID, Origin: acpmeta.NativeTurnOriginUnknown},
		PossibleSideEffects: true,
	}
	result.Outcome = o
	stream := newTurnEvents(s, t, o, input.InputID)
	t.events = stream
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
				t.releaseBytes(ui.bytes)
			}
		}
	}()
	s.rpc.mu.Lock()
	s.rpc.active = t
	s.rpc.mu.Unlock()
	defer func() {
		t.seal()
		t.mu.Lock()
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
	type acknowledgment struct {
		// submission preserves the actual write boundary independently of admission.
		submission agentclient.Submission
		// disposition distinguishes a handled extension command from native execution.
		disposition string
		// err records rejection or failure to receive the acknowledgment.
		err error
	}
	acknowledged := make(chan acknowledgment, 1)
	// A completed execution can precede its ACK. The request waiter has no
	// ownership of execution facts; the reader folds events while it waits.
	ackCtx, cancelAck := context.WithTimeout(context.WithoutCancel(t.ctx), 20*time.Second)
	go func() {
		var ack struct {
			// Disposition distinguishes started, queued, and handled prompts when supplied.
			Disposition string
		}
		sent, err := s.rpc.request(ackCtx, s.rpc.nextID(), "prompt", args, &ack, func() bool {
			t.mu.Lock()
			defer t.mu.Unlock()
			if t.ctx.Err() != nil {
				return false
			}
			t.submitted = true
			return true
		})
		acknowledged <- acknowledgment{submission: sent, disposition: ack.Disposition, err: err}
	}()
	var ack acknowledgment
	ackWaiting := (<-chan acknowledgment)(acknowledged)
	waiting := t.ctx.Done()
	var cancelTimer *time.Timer
	var forced <-chan time.Time
	defer func() {
		if cancelTimer != nil {
			cancelTimer.Stop()
		}
	}()
	defer func() {
		// Sealing joins the reader's last fold and transfers exclusive access
		// here before telemetry, delivery, or admitting the next invocation.
		t.seal()
		cancelAck()
		if ackWaiting != nil {
			ack = <-ackWaiting
		}
		result.Receipt.Submission = ack.submission
		var rejection *rpcRejection
		rejected := errors.As(ack.err, &rejection)
		switch {
		case stream.started:
			result.Receipt.Admission = agentclient.AdmissionConfirmed
			if rejected {
				// A refusal conflicts with an observed execution. Preserve both
				// facts without erasing the native terminal or measured usage.
				result.Receipt.OperationError = nativeFailure(rejection.detail)
			} else if o.Turn.Status == "" {
				result.Receipt.OperationError = ack.err
			}
		case ack.err == nil:
			result.Receipt.Admission = agentclient.AdmissionConfirmed
			if ack.disposition == "handled" && o.OperationError == nil {
				o.Turn.Origin = acpmeta.NativeTurnOriginHost
				o.Turn.ConsumedInputIDs = []string{input.InputID}
				o.Turn.Status = acpmeta.NativeTurnCompleted
				o.StopReason = pointer(acp.StopReasonEndTurn)
				o.OperationError = nil
			}
		case rejected:
			result.Receipt.OperationError = nativeFailure(rejection.detail)
			result.Outcome = nil
		case ack.submission == agentclient.SubmissionNotSent:
			result.Receipt.OperationError = ack.err
			result.Outcome = nil
		default:
			result.Receipt.Admission = agentclient.AdmissionUnknown
			result.Receipt.OperationError = ack.err
			result.Outcome = nil
		}
		stream.finish()
	}()
	for {
		select {
		case <-t.eventDone:
			return result
		case ack = <-ackWaiting:
			ackWaiting = nil
			// Inspect or withdraw routing atomically with native event folding.
			// A response is published only after all preceding facts are folded.
			t.mu.Lock()
			started, settled := stream.started, t.sealed
			stop := settled || !started && (ack.err != nil || ack.disposition == "handled")
			if stop {
				t.sealed = true
			}
			t.mu.Unlock()
			if settled {
				return result
			}
			if ack.err == nil {
				if stop {
					return result
				}
				continue
			}
			if started {
				// Native execution confirms admission independently of its ACK.
				// Continue to its terminal, cancellation, or transport failure.
				continue
			}
			var rejection *rpcRejection
			if ack.submission != agentclient.SubmissionNotSent && !errors.As(ack.err, &rejection) {
				s.poison(ack.err)
			}
			return result
		case <-s.rpc.done:
			t.seal()
			// Every complete native record was folded before the reader's EOF.
			if o.Turn.Status == "" && o.OperationError == nil {
				o.OperationError = s.rpc.err()
			}
			return result
		case <-waiting:
			waiting = nil
			cancelTimer = time.NewTimer(controlTimeout + time.Second)
			forced = cancelTimer.C
		case <-forced:
			t.seal()
			if o.Turn.Status == "" && o.OperationError == nil {
				o.OperationError = context.Cause(t.ctx)
				s.poison(o.OperationError)
			}
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

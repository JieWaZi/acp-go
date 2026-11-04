package claude

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/JieWaZi/acp-go/pkg/acpmeta"
	agentclient "github.com/JieWaZi/acp-go/pkg/agent_next"
	"github.com/JieWaZi/acp-go/pkg/agent_next/claude/protocol"
	acp "github.com/JieWaZi/acp-go/sdk"
)

const (
	maxPendingInputs = 64
	maxRememberedIDs = 16384
)

type inputPhase uint8

const (
	queued inputPhase = iota
	preparing
	ready
	writing
	written
	consumed
	retired
)

type input struct {
	// identity and native payload are frozen before returning admission.
	identity agentclient.PromptSubmission
	message  protocol.UserInputMessage
	options  agentclient.PromptOptions
	// detached has no caller waiting for a PromptResult.
	detached    bool
	compact     bool
	ctx         context.Context
	cancel      context.CancelFunc
	request     context.Context
	stopRequest func() bool
	// state below belongs exclusively to session.mu.
	phase           inputPhase
	cancelRequested bool
	controlStarted  bool
	echoed          bool
	// mayFollowProvider distinguishes an input sent while P was already visible.
	mayFollowProvider bool
	inv               *invocation
	submission        agentclient.Submission
	admission         agentclient.Admission
	err               error
	done              chan struct{}
	settled           bool
}

type invocation struct {
	// outcome is accumulated by the ordered reader and frozen before delivery.
	outcome          agentclient.NativeOutcome
	scope            agentclient.Scope
	inputs           []*input
	seenInputs       map[string]struct{}
	ctx              context.Context
	cancel           context.CancelFunc
	terminal         bool
	sealedAt         time.Time
	finalized        bool
	retain           bool
	compact          bool
	compactConfirmed bool
	cancelRequested  bool
	closed           chan struct{}
	deliveryErr      error
	lastMessage      string
	completeText     string
	nativeMessage    string
	model            string
	streamText       string
	streamThought    string
	partialUsage     map[string]requestUsage
	tools            map[string]toolRecord
	rejectedLimit    *protocol.RateLimitInfo
}

// ReservePrompt freezes input and its exact generation without starting native I/O.
func (c *Client) ReservePrompt(ctx context.Context, req acp.PromptRequest, opts agentclient.PromptOptions) (func() agentclient.PromptResult, error) {
	s, err := c.getSession(req.SessionId)
	if err != nil {
		return nil, err
	}
	return s.reserve(ctx, req, opts, false, false)
}

func (s *session) reserve(ctx context.Context, req acp.PromptRequest, opts agentclient.PromptOptions, detached, compact bool) (func() agentclient.PromptResult, error) {
	content, err := promptContent(req.Prompt)
	if err != nil {
		return nil, err
	}
	generated, err := newUUID()
	if err != nil {
		return nil, err
	}
	id := generated
	if req.MessageId != nil && isUUID(*req.MessageId) {
		id = strings.ToLower(*req.MessageId)
	}
	if opts.HostInvocationID == "" {
		opts.HostInvocationID = generated
	}

	s.mu.Lock()
	if err := s.admissibleLocked(); err != nil {
		s.mu.Unlock()
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		s.mu.Unlock()
		return nil, err
	}
	if len(s.queued) >= maxPendingInputs || len(s.used) >= maxRememberedIDs || s.ledger.full() {
		s.mu.Unlock()
		return nil, errCapacity
	}
	if compact && s.busyLocked() {
		s.mu.Unlock()
		return nil, errBusy
	}
	if _, exists := s.used[id]; exists {
		id = generated
	}
	in := s.newInputLocked(ctx, id, content, opts, detached, compact)
	s.queued = append(s.queued, in)
	s.signalLocked()
	s.mu.Unlock()

	run := sync.OnceValue(func() agentclient.PromptResult { return s.runInput(in) })
	return func() agentclient.PromptResult { return run().Snapshot() }, nil
}

func (s *session) newInputLocked(ctx context.Context, id string, content json.RawMessage, opts agentclient.PromptOptions, detached, compact bool) *input {
	owner, cancel := context.WithCancel(s.ctx)
	in := &input{
		identity: agentclient.PromptSubmission{SessionID: s.id, HostInvocationID: opts.HostInvocationID, InputID: id, NativeInputID: &id},
		message: protocol.UserInputMessage{Type: "user", SessionID: string(s.id), UUID: id,
			Message: protocol.UserInputBody{Role: "user", Content: content}, Origin: protocol.UserInputOrigin{Kind: "human"}},
		options: opts, detached: detached, compact: compact, ctx: owner, cancel: cancel, request: ctx,
		submission: agentclient.SubmissionNotSent, admission: agentclient.AdmissionNotAccepted, done: make(chan struct{}),
	}
	s.used[id] = struct{}{}
	in.stopRequest = context.AfterFunc(ctx, func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.cancelInputLocked(in, ctx.Err())
	})
	return in
}

func (s *session) runInput(in *input) agentclient.PromptResult {
	defer func() {
		in.stopRequest()
		in.cancel()
		s.mu.Lock()
		i := slices.Index(s.queued, in)
		if i >= 0 {
			s.queued = slices.Delete(s.queued, i, i+1)
		}
		s.signalLocked()
		s.mu.Unlock()
	}()
	for {
		s.mu.Lock()
		if in.settled || s.admissibleLocked() != nil || in.request.Err() != nil {
			s.finishInputLocked(in, firstError(in.err, in.request.Err(), s.fault, errClosed))
			result := inputResult(in)
			s.mu.Unlock()
			return result
		}
		if s.queued[0] == in && !s.setting && !s.interruptingLocked() {
			in.phase = preparing
			s.mu.Unlock()
			break
		}
		changed := s.changed
		s.mu.Unlock()
		_ = await(in.ctx, changed)
	}

	var cleanup func()
	var err error
	if in.options.Prepare != nil {
		cleanup, err = in.options.Prepare(in.ctx)
	}
	if cleanup != nil {
		defer cleanup()
	}
	if err == nil {
		err = in.ctx.Err()
	}
	if err == nil && in.options.BeforeSubmit != nil {
		err = in.options.BeforeSubmit(in.ctx, in.identity.Snapshot())
	}
	if err == nil {
		err = in.ctx.Err()
	}
	if err == nil {
		s.mu.Lock()
		if !in.settled {
			in.phase = ready
		}
		s.mu.Unlock()
		err = s.submitHostInput(in)
	}
	if err != nil {
		s.mu.Lock()
		if in.phase < writing {
			s.finishInputLocked(in, firstError(in.err, err))
		}
		s.mu.Unlock()
	}
	<-in.done
	s.mu.Lock()
	defer s.mu.Unlock()
	return inputResult(in)
}

func (s *session) interruptingLocked() bool {
	return s.current != nil && s.current.cancelRequested
}

func (s *session) submitHostInput(in *input) error {
	for {
		err := s.submit(in, nil)
		if !errors.Is(err, errInterrupting) {
			return err
		}
		s.mu.Lock()
		wait := s.interruptingLocked()
		changed := s.changed
		s.mu.Unlock()
		if wait {
			if err := await(in.ctx, changed); err != nil {
				return err
			}
		}
	}
}

func (s *session) submit(in *input, target *invocation) error {
	return s.wire.write(s.ctx, in.message, func() error {
		s.mu.Lock()
		defer s.mu.Unlock()
		if err := s.guardSessionLocked(); err != nil {
			return err
		}
		if err := in.request.Err(); err != nil {
			return err
		}
		if in.settled || in.cancelRequested || in.phase != ready {
			return firstError(in.err, context.Canceled)
		}
		if target != nil && (s.current != target || target.cancelRequested || target.terminal) {
			return errStale
		}
		if target == nil && s.interruptingLocked() {
			return errInterrupting
		}
		if in.compact && (s.current != nil || s.nativeQueued > 0 || s.background > 0) {
			return errBusy
		}
		if len(s.sent) >= maxPendingInputs || s.ledger.full() {
			return errCapacity
		}
		in.mayFollowProvider = s.current != nil
		in.phase = writing
		in.submission = agentclient.SubmissionUnknown
		in.admission = agentclient.AdmissionUnknown
		s.sent[in.identity.InputID] = in
		return nil
	}, func(evidence agentclient.Submission, err error) {
		s.mu.Lock()
		defer s.mu.Unlock()
		if in.submission != agentclient.SubmissionSent {
			in.submission = evidence
		}
		if err == nil {
			if in.phase == writing {
				in.phase = written
			}
			s.signalLocked()
			return
		}
		if evidence == agentclient.SubmissionNotSent && in.admission != agentclient.AdmissionConfirmed {
			in.admission = agentclient.AdmissionNotAccepted
			s.finishInputLocked(in, err)
		}
		// A partial/failed frame cannot share its pipe with later messages.
		s.faultAndStopLocked(err)
	})
}

func inputResult(in *input) agentclient.PromptResult {
	result := agentclient.PromptResult{Receipt: agentclient.PromptReceipt{
		Input: in.identity, Submission: in.submission, Admission: in.admission, OperationError: in.err,
	}}
	if in.inv != nil && in.inv.finalized {
		result.Outcome = &in.inv.outcome
	}
	return result.Snapshot()
}

func (s *session) finishInputLocked(in *input, err error) {
	if in.settled {
		return
	}
	in.err = firstError(in.err, err)
	in.phase = retired
	in.settled = true
	if s.sent[in.identity.InputID] == in {
		delete(s.sent, in.identity.InputID)
	}
	close(in.done)
	s.signalLocked()
}

func firstError(errs ...error) error {
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

// Cancel captures current native and local owners synchronously. Actual controls
// never wait behind host observation and always recheck the captured pointer.
func (c *Client) Cancel(req acp.CancelNotification) error {
	s, err := c.getSession(req.SessionId)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.admissibleLocked(); err != nil {
		return err
	}
	if s.current != nil {
		s.interruptLocked(s.current)
	}
	for _, in := range s.queued {
		s.cancelInputLocked(in, context.Canceled)
	}
	for _, in := range s.sent {
		s.cancelInputLocked(in, context.Canceled)
	}
	return nil
}

func (s *session) cancelInputLocked(in *input, reason error) {
	if in.settled || in.cancelRequested {
		return
	}
	in.cancelRequested = true
	switch in.phase {
	case queued, preparing, ready:
		in.cancel()
		s.finishInputLocked(in, reason)
	case writing, written:
		s.recallLocked(in)
	case consumed:
		if in.inv != nil && !in.inv.terminal {
			// Caller cancellation is scoped to its invocation, even when a
			// provider invocation also consumed the user's native UUID.
			if in.inv.outcome.Turn.Origin == acpmeta.NativeTurnOriginHost {
				s.interruptLocked(in.inv)
			} else {
				s.finishInputLocked(in, reason)
			}
		}
	}
}

func (s *session) recallLocked(in *input) {
	if in.controlStarted {
		return
	}
	in.controlStarted = true
	epoch := s.epoch
	s.taskLocked(func() {
		ctx, cancel := context.WithTimeout(s.ctx, s.client.options.ControlTimeout)
		defer cancel()
		request := struct {
			Subtype string `json:"subtype"`
			UUID    string `json:"message_uuid"`
		}{Subtype: "cancel_async_message", UUID: in.identity.InputID}
		_ = s.wire.call(ctx, request, nil, func() error {
			s.mu.Lock()
			defer s.mu.Unlock()
			if err := s.guardSessionLocked(); err != nil {
				return err
			}
			if s.sent[in.identity.InputID] != in {
				return errStale
			}
			return nil
		}, func(raw json.RawMessage) {
			r := decodeOptional[struct {
				Cancelled bool `json:"cancelled"`
			}](raw)
			if r == nil || !r.Cancelled {
				return
			}
			s.mu.Lock()
			defer s.mu.Unlock()
			if s.sent[in.identity.InputID] != in {
				return
			}
			s.retiredInputs[in.identity.InputID] = struct{}{}
			in.admission = agentclient.AdmissionNotAccepted
			s.finishInputLocked(in, context.Canceled)
		})
		select {
		case <-in.done:
			return
		case <-ctx.Done():
		}
		s.watchdog(func() bool {
			return s.sent[in.identity.InputID] == in && s.current == nil && s.epoch == epoch
		}, in)
	})
}

func (s *session) interruptLocked(inv *invocation) {
	if inv.cancelRequested || inv.terminal {
		return
	}
	inv.cancelRequested = true
	inv.cancel()
	s.signalLocked()
	s.taskLocked(func() {
		ctx, cancel := context.WithTimeout(s.ctx, s.client.options.ControlTimeout)
		defer cancel()
		// The CLI's interrupt has no expected turn ID. This gate only
		// excludes successors and terminals that Go has already observed.
		_ = s.wire.call(ctx, protocol.InterruptControlRequest{Subtype: protocol.ControlInterrupt}, nil,
			func() error {
				s.mu.Lock()
				defer s.mu.Unlock()
				if err := s.guardSessionLocked(); err != nil {
					return err
				}
				if s.current != inv || inv.terminal {
					return errStale
				}
				return nil
			}, nil)
		select {
		case <-inv.closed:
			return
		case <-ctx.Done():
		}
		s.watchdog(func() bool { return s.current == inv && !inv.terminal }, nil)
	})
}

func (s *session) watchdog(stillOwner func() bool, in *input) {
	ctx, cancel := context.WithTimeout(s.ctx, s.client.options.ControlTimeout)
	defer cancel()
	if err := s.wire.lock(ctx); err != nil {
		return
	}
	defer s.wire.unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fault != nil || s.closing {
		return
	}
	if stillOwner() {
		s.faultAndStopLocked(context.DeadlineExceeded)
	} else if in != nil && !in.settled && in.inv == nil {
		// No safe global target exists. End this waiter; retain the UUID
		// for a later exact consumption fact on the same native process.
		in.err = context.DeadlineExceeded
		in.settled = true
		close(in.done)
		s.signalLocked()
	}
}

func (s *session) faultAndStopLocked(err error) {
	if s.fault != nil {
		return
	}
	s.settleTransportLocked(err)
	s.taskLocked(func() {
		ctx, cancel := context.WithTimeout(context.Background(), s.client.options.ControlTimeout)
		defer cancel()
		if err := s.process.Close(ctx); err != nil {
			if log := s.client.options.Logger; log != nil {
				log.Warn("Claude native process release needs retry", "error", err)
			}
			return
		}
		if log := s.client.options.Logger; log != nil {
			if exit, err := s.process.Wait(ctx); err == nil {
				log.Debug("Claude native stream ended", "code", exit.Code, "diagnostic", exit.Diagnostic)
			}
		}
	})
}

// Steer accepts priority input only for an observed native invocation.
func (c *Client) Steer(ctx context.Context, req agentclient.SteerRequest) (agentclient.SteerResponse, error) {
	s, err := c.getSession(req.SessionID)
	if err != nil {
		return agentclient.SteerResponse{}, err
	}
	content, err := promptContent(req.Prompt)
	if err != nil {
		return agentclient.SteerResponse{}, err
	}
	id, err := newUUID()
	if err != nil {
		return agentclient.SteerResponse{}, err
	}
	s.mu.Lock()
	if err := s.admissibleLocked(); err != nil {
		s.mu.Unlock()
		return agentclient.SteerResponse{}, err
	}
	target := s.current
	if target == nil || target.cancelRequested {
		s.mu.Unlock()
		if req.PromptRequired {
			return agentclient.SteerResponse{Outcome: "promptRequired", Reason: "noRunningTurn"}, nil
		}
		if err := ctx.Err(); err != nil {
			return agentclient.SteerResponse{}, err
		}
		run, err := s.reserve(s.ctx, acp.PromptRequest{SessionId: s.id, Prompt: req.Prompt}, agentclient.PromptOptions{}, true, false)
		if err != nil {
			return agentclient.SteerResponse{}, err
		}
		go run()
		return agentclient.SteerResponse{Outcome: "startedNewTurn"}, nil
	}
	if len(s.sent) >= maxPendingInputs || len(s.used) >= maxRememberedIDs || s.ledger.full() {
		s.mu.Unlock()
		return agentclient.SteerResponse{}, errCapacity
	}
	in := s.newInputLocked(ctx, id, content, agentclient.PromptOptions{}, true, false)
	in.phase = ready
	in.message.Priority = "now"
	s.tasks++
	s.mu.Unlock()
	defer func() {
		in.stopRequest()
		in.cancel()
		s.mu.Lock()
		s.tasks--
		s.signalLocked()
		s.mu.Unlock()
	}()
	if err := s.submit(in, target); err != nil {
		return agentclient.SteerResponse{}, err
	}
	return agentclient.SteerResponse{Outcome: "injected"}, nil
}

// Compact reserves the same FIFO owner, then requires a native manual boundary.
func (c *Client) Compact(ctx context.Context, req acpmeta.CompactRequest) (acpmeta.CompactResponse, error) {
	s, err := c.getSession(acp.SessionId(req.SessionID))
	if err != nil {
		return acpmeta.CompactResponse{}, err
	}
	run, err := s.reserve(ctx, acp.PromptRequest{SessionId: s.id, Prompt: []acp.ContentBlock{acp.TextBlock("/compact")}}, agentclient.PromptOptions{}, true, true)
	if err != nil {
		return acpmeta.CompactResponse{}, err
	}
	r := run()
	if r.Outcome == nil {
		return acpmeta.CompactResponse{}, firstError(r.Receipt.OperationError, errors.New("Claude did not report compaction outcome"))
	}
	out := r.Outcome
	response := acpmeta.CompactResponse{Status: out.Turn.Status, Meta: acpmeta.WithNativeTurn(nil, out.Turn)}
	if out.Turn.NativeTurnID != nil {
		response.TurnID = *out.Turn.NativeTurnID
	}
	return response, errors.Join(r.Receipt.OperationError, out.OperationError, out.NativeFailure, out.DeliveryError)
}

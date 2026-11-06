package claude

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"time"

	acp "github.com/JieWaZi/acp-go-sdk"
	"github.com/JieWaZi/acp-go/pkg/acpmeta"
	agentclient "github.com/JieWaZi/acp-go/pkg/agentclient"
	"github.com/JieWaZi/acp-go/pkg/claude/protocol"
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
	cancelReason    error
	cancelDeadline  time.Time
	controlStarted  bool
	// mayFollowProvider marks an input that may run behind an observed provider operation.
	mayFollowProvider bool
	// legacyEchoed records exact stdin acknowledgement from the legacy native stream.
	legacyEchoed bool
	inv          *invocation
	submission   agentclient.Submission
	admission    agentclient.Admission
	err          error
	done         chan struct{}
	settled      bool
	waitResult   agentclient.PromptResult
}

type invocation struct {
	// outcome is accumulated by the ordered reader and frozen before delivery.
	outcome         agentclient.NativeOutcome
	scope           agentclient.Scope
	inputs          []*input
	seenInputs      map[string]struct{}
	ctx             context.Context
	cancel          context.CancelFunc
	terminal        bool
	sealedAt        time.Time
	finalized       bool
	opening         nativeOpening
	compact         bool
	manualCompact   *input
	cancelRequested bool
	closed          chan struct{}
	deliveryErr     error
	model           string
	output          assistantOutput
	partialUsage    map[string]requestUsage
	rejectedLimit   *protocol.RateLimitInfo
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
	s.mu.Lock()
	in, err := s.reserveInputLocked(ctx, id, generated, content, opts, detached, compact)
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	run := sync.OnceValue(func() agentclient.PromptResult { return s.runInput(in) })
	return func() agentclient.PromptResult { return run().Snapshot() }, nil
}

func (s *session) reserveInputLocked(ctx context.Context, id, generated string, content json.RawMessage, opts agentclient.PromptOptions, detached, compact bool) (*input, error) {
	if err := s.admissibleLocked(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(s.queued) >= maxPendingInputs || len(s.used) >= maxRememberedIDs || s.ledger.full() {
		return nil, errCapacity
	}
	if s.client.usesLegacyReplay() && (len(s.queued) != 0 || s.legacyWireBusyLocked()) {
		return nil, errBusy
	}
	if compact && s.busyLocked() {
		return nil, errBusy
	}
	if _, exists := s.used[id]; exists {
		id = generated
	}
	if opts.HostInvocationID == "" {
		opts.HostInvocationID = generated
	}
	in := s.newInputLocked(ctx, id, content, opts, detached, compact)
	s.queued = append(s.queued, in)
	s.signalLocked()
	return in, nil
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

	cleanup, err := in.options.PrepareSubmission(in.ctx, in.identity)
	if cleanup != nil {
		defer cleanup()
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

func (s *session) legacyWireBusyLocked() bool {
	if s.current != nil || len(s.sent) != 0 || s.nativeQueued > 0 || s.background > 0 {
		return true
	}
	// 2.1.159 reports task lifecycle events without a background snapshot.
	for _, task := range s.nativeTasks {
		if task.phase == taskRunning && !task.ambient {
			return true
		}
	}
	return false
}

func (s *session) interruptingLocked() bool {
	return s.current != nil && s.current.cancelRequested
}

func (s *session) submitHostInput(in *input) error {
	for {
		err := s.submit(in, nil, agentclient.Scope{})
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

func (s *session) submit(in *input, target *invocation, scope agentclient.Scope) error {
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
		if target != nil && (s.current != target || target.cancelRequested || target.terminal ||
			scope != (agentclient.Scope{}) && target.scope != scope) {
			return errStale
		}
		if target == nil && s.interruptingLocked() {
			return errInterrupting
		}
		if in.compact && (s.current != nil || s.nativeQueued > 0 || s.background > 0) {
			return errBusy
		}
		// The replay protocol confirms a single foreground input. It cannot
		// resolve priority inputs or associate overlapping native results.
		if s.client.usesLegacyReplay() && s.legacyWireBusyLocked() {
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
	return in.waitResult.Snapshot()
}

func (s *session) finishInputLocked(in *input, err error) {
	in.phase = retired
	if s.sent[in.identity.InputID] == in {
		delete(s.sent, in.identity.InputID)
	}
	s.finishWaiterLocked(in, err)
}

func (s *session) finishWaiterLocked(in *input, err error) {
	if in.settled {
		return
	}
	in.err = firstError(in.err, err)
	in.waitResult = agentclient.PromptResult{Receipt: agentclient.PromptReceipt{
		Input: in.identity, Submission: in.submission, Admission: in.admission, OperationError: in.err,
	}}
	if in.inv != nil && in.inv.finalized {
		in.waitResult.Outcome = &in.inv.outcome
	}
	in.settled = true
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
	in.cancelReason = reason
	in.cancelDeadline = time.Now().Add(s.client.options.ControlTimeout)
	switch in.phase {
	case queued, preparing, ready:
		in.cancel()
		s.finishInputLocked(in, reason)
	case writing, written, consumed:
		s.reconcileCancellationLocked(in)
		if !in.settled && !in.controlStarted {
			s.startInputCancellationLocked(in, in.phase < consumed)
		}
	}
}

// A cancelled waiter can leave a live native input or provider invocation.
// Late native consumption still belongs to that invocation and its receipt.
func (s *session) reconcileCancellationLocked(in *input) {
	if !in.cancelRequested || in.settled || in.inv == nil || in.inv.terminal {
		return
	}
	switch in.inv.outcome.Turn.Origin {
	case acpmeta.NativeTurnOriginHost:
		if in.inv.opening.initiator == in && time.Now().Before(in.cancelDeadline) {
			s.interruptByLocked(in.inv, in.cancelDeadline)
		}
	case acpmeta.NativeTurnOriginProvider:
		s.finishInputLocked(in, in.cancelReason)
	}
}

func (s *session) startInputCancellationLocked(in *input, recall bool) {
	in.controlStarted = true
	epoch, deadline := s.epoch, in.cancelDeadline
	s.taskLocked(func() {
		ctx, cancel := context.WithDeadline(s.ctx, deadline)
		defer cancel()
		if recall {
			s.recallInput(ctx, in)
		}
		select {
		case <-in.done:
			return
		case <-ctx.Done():
		}
		s.mu.Lock()
		pending := s.sent[in.identity.InputID] == in
		if !in.settled && (in.inv == nil || !in.inv.terminal) {
			// Release only the waiter. Keep a pending UUID for late native
			// consumption and an associated invocation for its native result.
			s.finishWaiterLocked(in, in.cancelReason)
		}
		s.mu.Unlock()
		if pending {
			s.watchdog(func() bool {
				return s.sent[in.identity.InputID] == in && s.current == nil && s.epoch == epoch
			})
		}
	})
}

func (s *session) recallInput(ctx context.Context, in *input) {
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
		s.withdrawnInputs[in.identity.InputID] = struct{}{}
		in.admission = agentclient.AdmissionNotAccepted
		s.finishInputLocked(in, in.cancelReason)
	})
}

func (s *session) interruptLocked(inv *invocation) {
	s.interruptByLocked(inv, time.Now().Add(s.client.options.ControlTimeout))
}

func (s *session) interruptByLocked(inv *invocation, deadline time.Time) {
	if inv.cancelRequested || inv.terminal {
		return
	}
	inv.cancelRequested = true
	inv.cancel()
	s.signalLocked()
	s.taskLocked(func() {
		ctx, cancel := context.WithDeadline(s.ctx, deadline)
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
		s.watchdog(func() bool { return s.current == inv && !inv.terminal })
	})
}

func (s *session) watchdog(stillOwner func() bool) {
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
	if req.Target != (agentclient.Scope{}) && (target == nil || target.scope != req.Target || target.cancelRequested || target.terminal) {
		s.mu.Unlock()
		return agentclient.SteerResponse{}, errStale
	}
	if err := ctx.Err(); err != nil {
		s.mu.Unlock()
		return agentclient.SteerResponse{}, err
	}
	if target != nil && c.usesLegacyReplay() {
		s.mu.Unlock()
		return agentclient.SteerResponse{}, errBusy
	}
	if target == nil || target.cancelRequested {
		if req.PromptRequired {
			s.mu.Unlock()
			return agentclient.SteerResponse{Outcome: "promptRequired", Reason: "noRunningTurn"}, nil
		}
		turnCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
		in, err := s.reserveInputLocked(turnCtx, id, id, content, req.Options, true, false)
		if err != nil {
			s.mu.Unlock()
			cancel()
			return agentclient.SteerResponse{}, err
		}
		// ACP callers without admission options acknowledge the FIFO reservation.
		// A host gate needs the actual submission receipt before it can proceed.
		legacy := req.Options.HostInvocationID == "" && req.Options.Prepare == nil && req.Options.BeforeSubmit == nil
		reserved, _ := steerReply("startedNewTurn", in, nil)
		s.mu.Unlock()
		done := make(chan struct{})
		go func() {
			defer close(done)
			defer cancel()
			s.runInput(in)
		}()
		if legacy {
			return reserved, nil
		}
		return s.waitIdleSteer(ctx, in, cancel, done)
	}
	if len(s.sent) >= maxPendingInputs || len(s.used) >= maxRememberedIDs || s.ledger.full() {
		s.mu.Unlock()
		return agentclient.SteerResponse{}, errCapacity
	}
	if req.Options.HostInvocationID == "" {
		req.Options.HostInvocationID = id
	}
	in := s.newInputLocked(ctx, id, content, req.Options, true, false)
	in.phase = preparing
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

	gateCtx, cancel := context.WithCancelCause(in.ctx)
	stop := context.AfterFunc(target.ctx, func() { cancel(errStale) })
	defer func() { stop(); cancel(nil) }()
	cleanup, err := in.options.PrepareSubmission(gateCtx, in.identity)
	if cleanup != nil {
		defer cleanup()
	}
	if err == nil {
		s.mu.Lock()
		if !in.settled {
			in.phase = ready
		}
		s.mu.Unlock()
		err = s.submit(in, target, req.Target)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err != nil && in.phase < writing {
		s.finishInputLocked(in, err)
	}
	return steerReply("injected", in, err)
}

func (s *session) waitIdleSteer(ctx context.Context, in *input, cancel context.CancelFunc, done <-chan struct{}) (agentclient.SteerResponse, error) {
	for {
		s.mu.Lock()
		if in.submission == agentclient.SubmissionSent {
			response, err := steerReply("startedNewTurn", in, nil)
			s.mu.Unlock()
			return response, err
		}
		if in.settled {
			response, err := steerReply("", in, firstError(in.err, errTransport))
			s.mu.Unlock()
			_ = await(ctx, done)
			return response, err
		}
		changed := s.changed
		s.mu.Unlock()
		if err := await(ctx, changed); err != nil {
			cancel()
			s.mu.Lock()
			defer s.mu.Unlock()
			return steerReply("", in, err)
		}
	}
}

// steerReply copies receipt evidence under the input owner's state lock.
func steerReply(outcome string, in *input, err error) (agentclient.SteerResponse, error) {
	if err != nil {
		outcome = ""
	}
	return agentclient.SteerResponse{Outcome: outcome, Receipt: &agentclient.PromptReceipt{
		Input: in.identity.Snapshot(), Submission: in.submission, Admission: in.admission, OperationError: err,
	}}, err
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

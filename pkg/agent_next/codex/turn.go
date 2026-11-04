package codex

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"sync"
	"time"

	"github.com/JieWaZi/acp-go/pkg/acpmeta"
	agentclient "github.com/JieWaZi/acp-go/pkg/agent_next"
	"github.com/JieWaZi/acp-go/pkg/codex/protocol"
	acp "github.com/JieWaZi/acp-go/sdk"
)

const (
	maxEarlyBytes     = 4 << 20
	maxPendingUpdates = 256
)

var (
	errStopped           = fmt.Errorf("Codex turn cancelled: %w", context.Canceled)
	errObservationBudget = errors.New("Codex turn exceeded the observation backlog budget")
	errUnconfirmed       = errors.New("Codex turn ended without conclusive native evidence")
	errTerminal          = errors.New("Codex native terminal observed")
	uuidPattern          = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
)

type turn struct {
	// session is the exact opened generation that owns this turn.
	session *session
	// input is the frozen identity shared between the gate and receipt.
	input agentclient.PromptSubmission
	// request contains the native input frozen at reservation.
	request protocol.TurnStartParams
	// mode freezes permission behavior for the reserved turn.
	mode acp.SessionModeId
	// cleanup releases host preparation before owner release.
	cleanup func()
	// options is the host's preparation and business gate.
	options agentclient.PromptOptions
	// prepare is cancelled at synchronous ingress or by the caller context.
	prepare context.Context
	// stopPrepare also releases preparation context resources.
	stopPrepare context.CancelCauseFunc
	// native is independent of the foreground waiter after the write commits.
	native context.Context
	// stopNative ends response and observation work after terminal or retirement.
	stopNative context.CancelCauseFunc
	// observeCtx is independent of wire acknowledgement and turn cancellation.
	observeCtx context.Context
	// stopObserve bounds delivery cleanup.
	stopObserve context.CancelFunc
	// ready publishes the first confirmed native turn ID.
	ready chan struct{}
	// terminal publishes only actual provider completion evidence.
	terminal chan struct{}
	// released publishes release of the owner after preparation and observers.
	released chan struct{}
	// updates receives immutable observations after evidence capture.
	updates chan observation
	// interactions tracks the native requests captured for this exact turn.
	interactions sync.WaitGroup
	// observerDone publishes delivery completion.
	observerDone chan struct{}
	// outcomeEntry is the ordered receipt slot opened by the native terminal.
	outcomeEntry *outcomeEntry
	// retainOutcome reserves one slot for this turn's canonical outcome.
	retainOutcome bool
	// autonomous identifies a native execution with no host input submission.
	autonomous bool
	// compact distinguishes native compaction from a user prompt.
	compact bool
	// writing is the linearized decision to allow native write.
	writing bool
	// tierRevision is the last native tier report when this turn allowed its write.
	tierRevision uint64
	// stopped is the synchronous cancellation intent.
	stopped bool
	// finishing fences reader updates before observation drains.
	finishing bool
	// rejected records a trusted native rejection before any turn was accepted.
	rejected bool
	// early holds possible evidence before a native identity is proven.
	early []earlyFrame
	// earlyBytes bounds storage across unconfirmed candidate turn IDs.
	earlyBytes int
	// admission tracks only correlated native acknowledgement or input evidence.
	admission agentclient.Admission
	// outcome is owned by the native reader and the finalization path.
	outcome agentclient.NativeOutcome
	// model is the model observed at turn start, possibly superseded by reroute evidence.
	model string
	// rerouted records that one model cannot be used for aggregate attribution.
	rerouted bool
	// previous is the cumulative floor used for delta accounting.
	previous *tokenCheckpoint
	// consumed is the sum of deltas supported by turn attribution.
	consumed *acpmeta.ModelUsage
	// usageGap indicates an unscoped observation or a missing start baseline.
	usageGap bool
	// usageOnlyLast means the attributed sum is proven to be one request.
	usageOnlyLast bool
	// usageHigh keeps the total-token floor if this turn loses its cumulative boundary.
	usageHigh *int64
	// nativeStatus retains the actual status for provider-specific controls.
	nativeStatus string
	// finalCandidate is a completed legacy message whose phase was not declared.
	finalCandidate *agentclient.FinalMessage
	// completedMessages suppresses duplicate native item completion events.
	completedMessages map[string]struct{}
	// observationBytes accounts retained wire updates until delivery returns.
	observationBytes int
	// earlyEvidenceCount and earlyEvidenceBytes reserve space for technical facts.
	earlyEvidenceCount, earlyEvidenceBytes int
	// tools maps native item IDs to their most recent observed tool status.
	tools map[string]acp.ToolCallStatus
	// toolOutput holds the accumulated terminal text for snapshot consumers.
	toolOutput map[string]string
	// streamed retains bounded prefix evidence for incomplete native messages.
	streamed map[string]*messagePrefix
	// steerInputs retains attempted native input identities for this exact turn.
	steerInputs map[string]struct{}
	// steerPending counts committed controls awaiting their own native evidence.
	steerPending int
	// steerSettled closes when the current set of committed controls has settled.
	steerSettled chan struct{}
}

type earlyFrame struct {
	// method names the native notification.
	method string
	// params is retained only until exact turn identity is available.
	params json.RawMessage
}

func newID() string {
	var bytes [16]byte
	_, _ = rand.Read(bytes[:])
	bytes[6] = (bytes[6] & 0x0f) | 0x40
	bytes[8] = (bytes[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", bytes[:4], bytes[4:6], bytes[6:8], bytes[8:10], bytes[10:])
}

// ReservePrompt captures the session and its settings synchronously with no native I/O.
func (c *Client) ReservePrompt(ctx context.Context, request acp.PromptRequest, options agentclient.PromptOptions) (func() agentclient.PromptResult, error) {
	input, err := encodePrompt(request.Prompt)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	s, err := c.idleSessionLocked(request.SessionId)
	if err != nil {
		return nil, err
	}
	t, err := c.reservePromptLocked(ctx, s, input, request.MessageId, options, c.foregroundReceipts)
	if err != nil {
		return nil, err
	}
	var once sync.Once
	var result agentclient.PromptResult
	return func() agentclient.PromptResult {
		once.Do(func() { result = c.runPrompt(t) })
		return result.Snapshot()
	}, nil
}

func (c *Client) reservePromptLocked(ctx context.Context, s *session, input []protocol.InputElement, requestedID *string, options agentclient.PromptOptions, retain bool) (*turn, error) {
	t, err := c.installTurnLocked(ctx, s, false, retain)
	if err != nil {
		return nil, err
	}
	t.input = newInputIdentity(s.id, options.HostInvocationID)
	if requestedID != nil && uuidPattern.MatchString(*requestedID) {
		if _, used := s.inputs[*requestedID]; !used {
			t.input.InputID = *requestedID
			t.input.NativeInputID = pointer(*requestedID)
		}
	}
	t.options = options
	s.inputs[t.input.InputID] = struct{}{}
	t.request.Input = input
	t.request.ClientUserMessageID = pointer(t.input.InputID)
	return t, nil
}

func (c *Client) installTurnLocked(ctx context.Context, s *session, compact, retain bool) (*turn, error) {
	if retain && c.retainedOutcomes == maxRetainedOutcomes {
		return nil, acp.NewInvalidRequest("Codex native receipts are full")
	}
	prepare, stopPrepare := context.WithCancelCause(ctx)
	native, stopNative := context.WithCancelCause(c.life)
	observeCtx, stopObserve := context.WithCancel(c.life)
	t := &turn{
		session: s, compact: compact, mode: s.mode, prepare: prepare, stopPrepare: stopPrepare, native: native, stopNative: stopNative,
		observeCtx: observeCtx, stopObserve: stopObserve, ready: make(chan struct{}), terminal: make(chan struct{}), released: make(chan struct{}),
		updates: make(chan observation, maxPendingUpdates), observerDone: make(chan struct{}),
		admission: agentclient.AdmissionNotAccepted, tools: make(map[string]acp.ToolCallStatus), toolOutput: make(map[string]string), streamed: make(map[string]*messagePrefix),
		steerInputs: make(map[string]struct{}), completedMessages: make(map[string]struct{}), model: value(s.settings.Observed.Model),
		previous: s.usageCheckpoint, usageGap: s.usageCheckpoint == nil || s.usageCheckpoint.disputed,
		outcome: agentclient.NativeOutcome{Turn: acpmeta.NativeTurn{InvocationID: newID(), Origin: acpmeta.NativeTurnOriginHost}},
	}
	if t.previous != nil {
		t.usageHigh = t.previous.total.Context
	}
	t.request = s.turnParams()
	if t.request.Model != nil && *t.request.Model != t.model {
		t.model = ""
	}
	if retain {
		c.retainedOutcomes++
		t.retainOutcome = true
	}
	s.active = t
	c.interactions.Add(1)
	return t, nil
}

func (c *Client) runPrompt(t *turn) (result agentclient.PromptResult) {
	written, err := c.executeTurn(t)
	return c.finishTurn(t, written, err)
}

func (c *Client) executeTurn(t *turn) (frameWrite, error) {
	stop := context.AfterFunc(t.prepare, func() {
		c.mu.Lock()
		c.cancelTurnLocked(t)
		c.mu.Unlock()
	})
	defer stop()
	c.startTurnObserver(t)
	cleanup, err := t.options.PrepareSubmission(t.prepare, t.input)
	t.cleanup = cleanup
	if err != nil {
		return frameWrite{}, err
	}
	releaseSkills, err := c.claimSkills(t.prepare, append([]string{t.session.cwd}, t.session.roots...))
	if err != nil {
		return frameWrite{}, err
	}
	defer releaseSkills()

	method := "turn/start"
	var params any = t.request
	if t.compact {
		method = "thread/compact/start"
		params = protocol.ThreadCompactStartParams{ThreadID: string(t.session.id)}
	}
	written, err := c.call(t.native, method, params, nil, func() error {
		if !c.activeLocked(t) {
			return errTurnChanged
		}
		if cause := context.Cause(t.prepare); cause != nil {
			return cause
		}
		t.writing = true
		t.tierRevision = t.session.tierRevision
		t.session.skillRoots = slices.Clone(c.skillRoots)
		t.admission = agentclient.AdmissionUnknown
		return nil
	}, func(frame rpcFrame) {
		if !c.activeLocked(t) || !t.writing {
			return
		}
		if frame.Error != nil {
			if t.outcome.Turn.NativeTurnID == nil && knownPreAdmissionRejection(frame.Error) {
				t.rejected = true
				t.admission = agentclient.AdmissionNotAccepted
			}
			return
		}
		if !t.compact {
			var reply protocol.TurnStartResponse
			if json.Unmarshal(frame.Result, &reply) == nil && reply.Turn.ID != "" {
				c.bindTurnLocked(t, reply.Turn.ID)
				c.captureTerminalLocked(t, reply.Turn)
			}
		}
	})
	if written.submission() == agentclient.SubmissionNotSent {
		return written, err
	}
	c.mu.Lock()
	terminal := t.outcome.Turn.Status != ""
	rejected := t.rejected
	if !t.compact && !terminal && !rejected && t.admission == agentclient.AdmissionUnknown && err == nil {
		err = errors.New("Codex turn/start did not identify the accepted turn")
	}
	c.mu.Unlock()
	if terminal || rejected {
		if errors.Is(err, errTerminal) {
			err = nil
		}
		return written, err
	}
	if err != nil {
		c.mu.Lock()
		c.cancelTurnLocked(t)
		c.mu.Unlock()
	}
	select {
	case <-t.terminal:
		if errors.Is(err, errTerminal) {
			err = nil
		}
		return written, err
	case <-t.native.Done():
		cause := context.Cause(t.native)
		if errors.Is(cause, errTerminal) {
			return written, err
		}
		return written, errors.Join(err, cause)
	}
}

func knownPreAdmissionRejection(err *acp.RequestError) bool {
	// Invalid request/params and method absence are protocol-level rejection.
	return err.Code == -32600 || err.Code == -32601 || err.Code == -32602
}

func (c *Client) activeLocked(t *turn) bool {
	return c.sessions[t.session.id] == t.session && t.session.active == t
}

func (c *Client) ownsTurnLocked(t *turn) bool {
	if c.sessions[t.session.id] != t.session {
		return false
	}
	_, draining := t.session.draining[t]
	return t.session.active == t || draining
}

func (c *Client) bindTurnLocked(t *turn, id string) {
	if t.outcome.Turn.NativeTurnID != nil || id == "" || len(id) > maxNativeIDBytes {
		return
	}
	if c.seenTurnLocked(t.session.id, id) {
		return
	}
	c.rememberTurnLocked(t.session.id, id)
	t.outcome.Turn.NativeTurnID = pointer(id)
	t.admission = agentclient.AdmissionConfirmed
	if t.writing && !t.compact {
		if model := value(t.request.Model); model != "" {
			t.session.admittedModel = model
		}
		if t.request.ServiceTier.IsPresent() && t.tierRevision == t.session.tierRevision {
			// Admission supplies inheritance evidence only. The effective tier is
			// still unknown until Codex reports it.
			t.session.inheritedTier, _ = t.request.ServiceTier.Value()
		}
	}
	if t.input.NativeInputID != nil {
		t.outcome.Turn.ConsumedInputIDs = append(t.outcome.Turn.ConsumedInputIDs, *t.input.NativeInputID)
	}
	close(t.ready)
	early := t.early
	t.early = nil
	t.earlyBytes = 0
	t.earlyEvidenceBytes = 0
	t.earlyEvidenceCount = 0
	for _, frame := range early {
		c.receiveLocked(frame.method, frame.params)
	}
}

func (c *Client) finishTurn(t *turn, written frameWrite, err error) agentclient.PromptResult {
	t.stopPrepare(err)
	t.stopNative(err)
	c.mu.Lock()
	t.finishing = true
	steerSettled := t.steerSettled
	close(t.updates)
	c.mu.Unlock()
	if steerSettled != nil {
		<-steerSettled
	}
	drained := make(chan struct{})
	go func() {
		<-t.observerDone
		t.interactions.Wait()
		close(drained)
	}()
	wait := time.NewTimer(controlTimeout)
	select {
	case <-drained:
	case <-wait.C:
		c.mu.Lock()
		if t.outcome.DeliveryError == nil {
			t.outcome.DeliveryError = context.DeadlineExceeded
		}
		c.fail(context.DeadlineExceeded)
		c.mu.Unlock()
	}
	wait.Stop()
	t.stopObserve()
	if t.cleanup != nil {
		t.cleanup()
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.activeLocked(t) {
		t.session.active = nil
		if t.outcome.Turn.Status == "" || t.consumed == nil && (t.previous == nil || !t.previous.disputed) {
			t.session.usageCheckpoint = nil
		}
	}
	delete(t.session.draining, t)
	close(t.released)
	submission := written.submission()
	admission := t.admission
	if submission == agentclient.SubmissionNotSent {
		admission = agentclient.AdmissionNotAccepted
	}
	result := agentclient.PromptResult{Receipt: agentclient.PromptReceipt{Input: t.input, Submission: submission, Admission: admission, OperationError: err}}
	if (submission != agentclient.SubmissionNotSent || t.autonomous) && t.outcome.Turn.NativeTurnID != nil {
		t.outcome.PossibleSideEffects = true
		if t.outcome.Turn.Status == "" {
			t.outcome.OperationError = err
			if t.outcome.OperationError == nil {
				t.outcome.OperationError = errUnconfirmed
			}
		}
		o := t.outcome.Snapshot()
		if t.retainOutcome {
			capOutcome(&o)
		}
		result.Outcome = &o
	}
	c.publishOutcomeLocked(t, result.Outcome)
	return result
}

// Cancel synchronously marks the captured owner and schedules bounded native control.
func (c *Client) Cancel(notification acp.CancelNotification) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if s := c.sessions[notification.SessionId]; s != nil && s.active != nil {
		c.cancelTurnLocked(s.active)
	}
	return nil
}

func (c *Client) cancelTurnLocked(t *turn) {
	if !c.activeLocked(t) || t.stopped || t.outcome.Turn.Status != "" {
		return
	}
	t.stopped = true
	t.stopPrepare(errStopped)
	if !t.writing {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), controlTimeout)
	go c.interrupt(ctx, cancel, t)
}

func (c *Client) interrupt(ctx context.Context, cancel context.CancelFunc, t *turn) {
	defer cancel()
	select {
	case <-t.ready:
	case <-t.native.Done():
		return
	case <-ctx.Done():
		c.expireTurn(t)
		return
	}
	c.mu.Lock()
	id := value(t.outcome.Turn.NativeTurnID)
	c.mu.Unlock()
	_, _ = c.call(ctx, "turn/interrupt", protocol.TurnInterruptParams{ThreadID: string(t.session.id), TurnID: id}, nil, func() error {
		if !c.activeLocked(t) || t.outcome.Turn.Status != "" || value(t.outcome.Turn.NativeTurnID) != id {
			return errTurnChanged
		}
		return nil
	}, nil)
	select {
	case <-t.native.Done():
	case <-ctx.Done():
		c.expireTurn(t)
	}
}

func (c *Client) expireTurn(t *turn) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.activeLocked(t) && t.outcome.Turn.Status == "" && !t.rejected {
		// An unconfirmed write may already have changed the native thread.
		c.fail(errUnconfirmed)
	}
}

func (c *Client) Steer(ctx context.Context, request agentclient.SteerRequest) (agentclient.SteerResponse, error) {
	input, err := encodePrompt(request.Prompt)
	if err != nil {
		return agentclient.SteerResponse{}, err
	}
	c.mu.Lock()
	s, err := c.sessionLocked(request.SessionID)
	if err != nil {
		c.mu.Unlock()
		return agentclient.SteerResponse{}, err
	}
	t := s.active
	exact := request.Target != (agentclient.Scope{})
	if exact && (t == nil || request.Target != (agentclient.Scope{
		SessionID: s.id, HostInvocationID: t.input.HostInvocationID, NativeInvocationID: t.outcome.Turn.InvocationID,
	}) || t.outcome.Turn.NativeTurnID == nil) {
		c.mu.Unlock()
		return agentclient.SteerResponse{}, errTurnChanged
	}
	if t == nil && len(s.draining) == 0 && !s.busy && !s.closing && !request.PromptRequired {
		if err := ctx.Err(); err != nil {
			c.mu.Unlock()
			return agentclient.SteerResponse{}, err
		}
		turnCtx, cancel := context.WithCancelCause(context.WithoutCancel(ctx))
		t, err = c.reservePromptLocked(turnCtx, s, input, nil, request.Options, true)
		if err != nil {
			cancel(err)
			c.mu.Unlock()
			return agentclient.SteerResponse{}, err
		}
		c.mu.Unlock()
		return c.runIdleSteer(ctx, t, cancel)
	}
	if t == nil || t.compact || t.stopped || t.finishing || t.outcome.Turn.Status != "" || s.closing {
		c.mu.Unlock()
		return agentclient.SteerResponse{}, errTurnBusy
	}
	c.mu.Unlock()
	select {
	case <-t.ready:
	case <-t.released:
		return agentclient.SteerResponse{}, errTurnChanged
	case <-ctx.Done():
		return agentclient.SteerResponse{}, ctx.Err()
	}
	c.mu.Lock()
	nativeID := value(t.outcome.Turn.NativeTurnID)
	if !c.steerTargetLocked(t, nativeID) {
		c.mu.Unlock()
		return agentclient.SteerResponse{}, errTurnChanged
	}
	identity := newInputIdentity(s.id, request.Options.HostInvocationID)
	s.inputs[identity.InputID] = struct{}{}
	t.interactions.Add(1)
	c.mu.Unlock()
	defer t.interactions.Done()

	gateCtx, cancel := context.WithCancelCause(ctx)
	stopNative := context.AfterFunc(t.native, func() { cancel(errTurnChanged) })
	stopPrepare := context.AfterFunc(t.prepare, func() { cancel(context.Cause(t.prepare)) })
	defer func() { stopNative(); stopPrepare(); cancel(nil) }()
	cleanup, err := request.Options.PrepareSubmission(gateCtx, identity)
	if cleanup != nil {
		defer cleanup()
	}
	if err != nil {
		return steerReply("", identity, agentclient.SubmissionNotSent, agentclient.AdmissionNotAccepted, err)
	}
	params := protocol.TurnSteerParams{ThreadID: string(s.id), ExpectedTurnID: nativeID, Input: input, ClientUserMessageID: pointer(identity.InputID)}
	result := make(chan agentclient.PromptReceipt, 1)
	go func() { result <- c.submitSteer(ctx, t, params, identity) }()
	select {
	case receipt := <-result:
		return steerReply("injected", identity, receipt.Submission, receipt.Admission, receipt.OperationError)
	case <-ctx.Done():
		// Cancellation can race a committed write. Preserve the canonical
		// reader's ability to capture native evidence even after we return.
		select {
		case receipt := <-result:
			return steerReply("injected", identity, receipt.Submission, receipt.Admission, receipt.OperationError)
		default:
		}
		c.mu.Lock()
		submission, admission := agentclient.SubmissionNotSent, agentclient.AdmissionNotAccepted
		if _, attempted := t.steerInputs[identity.InputID]; attempted {
			submission, admission = agentclient.SubmissionUnknown, agentclient.AdmissionUnknown
		}
		if slices.Contains(t.outcome.Turn.ConsumedInputIDs, identity.InputID) {
			submission, admission = agentclient.SubmissionSent, agentclient.AdmissionConfirmed
		}
		c.mu.Unlock()
		return steerReply("", identity, submission, admission, ctx.Err())
	}
}

func newInputIdentity(sessionID acp.SessionId, hostID string) agentclient.PromptSubmission {
	id := newID()
	if hostID == "" {
		hostID = newID()
	}
	return agentclient.PromptSubmission{SessionID: sessionID, HostInvocationID: hostID, InputID: id, NativeInputID: pointer(id)}
}

func steerReply(outcome string, identity agentclient.PromptSubmission, submission agentclient.Submission, admission agentclient.Admission, err error) (agentclient.SteerResponse, error) {
	if err != nil {
		outcome = ""
	}
	return agentclient.SteerResponse{Outcome: outcome, Receipt: &agentclient.PromptReceipt{
		Input: identity.Snapshot(), Submission: submission, Admission: admission, OperationError: err,
	}}, err
}

func (c *Client) runIdleSteer(ctx context.Context, t *turn, cancel context.CancelCauseFunc) (agentclient.SteerResponse, error) {
	results := make(chan agentclient.PromptResult, 1)
	go func() {
		defer cancel(nil)
		results <- c.runPrompt(t)
	}()
	select {
	case <-t.ready:
		return steerReply("startedNewTurn", t.input, agentclient.SubmissionSent, agentclient.AdmissionConfirmed, nil)
	case result := <-results:
		err := result.Receipt.OperationError
		if result.Receipt.Admission == agentclient.AdmissionConfirmed {
			err = nil
		} else if err == nil {
			err = errUnconfirmed
		}
		return steerReply("startedNewTurn", t.input, result.Receipt.Submission, result.Receipt.Admission, err)
	case <-ctx.Done():
		cancel(ctx.Err())
		c.mu.Lock()
		admission := t.admission
		submission := agentclient.SubmissionNotSent
		if t.writing {
			submission = agentclient.SubmissionUnknown
		}
		if admission == agentclient.AdmissionConfirmed {
			submission = agentclient.SubmissionSent
		}
		c.mu.Unlock()
		return steerReply("", t.input, submission, admission, ctx.Err())
	}
}

func (c *Client) steerTargetLocked(t *turn, nativeID string) bool {
	return nativeID != "" && c.activeLocked(t) && !t.stopped && !t.finishing && t.outcome.Turn.Status == "" &&
		!t.session.closing && value(t.outcome.Turn.NativeTurnID) == nativeID
}

// submitSteer owns one independent write-to-ack interval. After the write is
// committed, foreground cancellation does not discard native input evidence.
func (c *Client) submitSteer(waiter context.Context, t *turn, params protocol.TurnSteerParams, input agentclient.PromptSubmission) agentclient.PromptReceipt {
	ctx, cancel := context.WithTimeout(c.life, controlTimeout)
	defer cancel()
	id := input.InputID
	admitted := false
	admission := agentclient.AdmissionNotAccepted
	var reply protocol.TurnSteerResponse
	written, err := c.call(ctx, "turn/steer", params, &reply, func() error {
		if err := waiter.Err(); err != nil {
			return err
		}
		if !c.steerTargetLocked(t, params.ExpectedTurnID) {
			return errTurnChanged
		}
		if len(t.steerInputs) >= maxPendingUpdates {
			return acp.NewInvalidRequest("Codex steering input retention is full")
		}
		if t.steerPending == 0 {
			t.steerSettled = make(chan struct{})
		}
		t.steerPending++
		t.steerInputs[id] = struct{}{}
		admitted = true
		admission = agentclient.AdmissionUnknown
		return nil
	}, func(frame rpcFrame) {
		if !admitted || !c.ownsTurnLocked(t) {
			return
		}
		var ack protocol.TurnSteerResponse
		if frame.Error == nil && json.Unmarshal(frame.Result, &ack) == nil && ack.TurnID == params.ExpectedTurnID {
			consumeInput(t, id)
		} else if frame.Error != nil && knownPreAdmissionRejection(frame.Error) {
			admission = agentclient.AdmissionNotAccepted
		}
	})
	if err == nil && reply.TurnID != params.ExpectedTurnID {
		err = errTurnChanged
	}
	submission := written.submission()
	c.mu.Lock()
	defer c.mu.Unlock()
	if admitted {
		t.steerPending--
		if t.steerPending == 0 {
			close(t.steerSettled)
		}
	}
	if slices.Contains(t.outcome.Turn.ConsumedInputIDs, id) {
		submission, admission = agentclient.SubmissionSent, agentclient.AdmissionConfirmed
	} else if submission == agentclient.SubmissionNotSent {
		admission = agentclient.AdmissionNotAccepted
	}
	return agentclient.PromptReceipt{Input: input, Submission: submission, Admission: admission, OperationError: err}
}

func consumeInput(t *turn, id string) {
	if !slices.Contains(t.outcome.Turn.ConsumedInputIDs, id) {
		t.outcome.Turn.ConsumedInputIDs = append(t.outcome.Turn.ConsumedInputIDs, id)
	}
}

func (c *Client) Compact(ctx context.Context, request acpmeta.CompactRequest) (acpmeta.CompactResponse, error) {
	c.mu.Lock()
	s, err := c.idleSessionLocked(acp.SessionId(request.SessionID))
	if err != nil {
		c.mu.Unlock()
		return acpmeta.CompactResponse{}, err
	}
	t, err := c.installTurnLocked(ctx, s, true, false)
	c.mu.Unlock()
	if err != nil {
		return acpmeta.CompactResponse{}, err
	}
	written, err := c.executeTurn(t)
	r := c.finishTurn(t, written, err)
	if r.Outcome == nil {
		return acpmeta.CompactResponse{}, r.Receipt.OperationError
	}
	o := r.Outcome
	meta := acpmeta.WithNativeTurn(nil, o.Turn)
	if o.Turn.Usage != nil {
		meta = acpmeta.WithTurnUsage(meta, *o.Turn.Usage)
	}
	return acpmeta.CompactResponse{TurnID: value(o.Turn.NativeTurnID), Status: t.nativeStatus, Meta: meta}, errors.Join(err, o.NativeFailure, o.OperationError, o.DeliveryError)
}

func value[T any](v *T) (out T) {
	if v != nil {
		return *v
	}
	return out
}

type turnIdentity struct {
	// thread scopes the native turn to its persisted owner.
	thread acp.SessionId
	// turn is the provider-issued ID on that thread.
	turn string
}

func (c *Client) seenTurnLocked(thread acp.SessionId, id string) bool {
	_, seen := c.seenTurns[turnIdentity{thread: thread, turn: id}]
	return seen
}

func (c *Client) rememberTurnLocked(thread acp.SessionId, id string) {
	const retention = 4096
	key := turnIdentity{thread: thread, turn: id}
	if len(c.recentTurns) < retention {
		c.recentTurns = append(c.recentTurns, key)
	} else {
		delete(c.seenTurns, c.recentTurns[c.nextRecentTurn])
		c.recentTurns[c.nextRecentTurn] = key
		c.nextRecentTurn = (c.nextRecentTurn + 1) % retention
	}
	c.seenTurns[key] = struct{}{}
}

func (c *Client) startTurnObserver(t *turn) {
	go func() { defer c.interactions.Done(); c.observeTurn(t) }()
}

func (c *Client) startAutonomousLocked(s *session, event turnEvent) *turn {
	id := event.Turn.ID
	if c.closing || c.life.Err() != nil || s.busy || s.closing || id == "" || len(id) > maxNativeIDBytes ||
		event.Turn.Status != protocol.PurpleInProgress || c.seenTurnLocked(s.id, id) {
		return nil
	}
	previous := s.active
	if previous != nil {
		if previous.writing && previous.outcome.Turn.Status == "" {
			return nil
		}
		c.cancelTurnLocked(previous)
	}
	t, err := c.installTurnLocked(c.life, s, false, true)
	if err != nil {
		c.fail(err)
		return nil
	}
	if previous != nil {
		if s.draining == nil {
			s.draining = make(map[*turn]struct{})
		}
		s.draining[previous] = struct{}{}
	}
	t.autonomous = true
	t.model = value(s.settings.Observed.Model)
	t.request.Model = nil
	t.writing = true
	t.input.SessionID = s.id
	t.outcome.Turn.Origin = acpmeta.NativeTurnOriginUnknown
	if event.Unprompted {
		t.outcome.Turn.Origin = acpmeta.NativeTurnOriginProvider
	}
	c.bindTurnLocked(t, id)
	lease := !c.skillChanging && slices.Equal(c.skillRoots, s.skillRoots)
	if lease {
		c.skillUsers++
	} else {
		c.cancelTurnLocked(t)
	}
	go func() {
		c.startTurnObserver(t)
		<-t.native.Done()
		cause := context.Cause(t.native)
		if errors.Is(cause, errTerminal) {
			cause = nil
		}
		if lease {
			c.releaseSkills()
		}
		c.finishTurn(t, frameWrite{}, cause)
	}()
	return t
}

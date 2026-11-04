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
	// compact distinguishes native compaction from a user prompt.
	compact bool
	// writing is the linearized decision to allow native write.
	writing bool
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
	// previous is the latest trustworthy cumulative point for delta accounting.
	previous *tokenCounters
	// consumed is the sum of deltas supported by turn attribution.
	consumed *tokenCounters
	// usageGap indicates an unscoped observation or a missing start baseline.
	usageGap bool
	// usageHigh records the highest total sampled on the exact native turn.
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
	// streamed tracks text that has already been emitted for a native message.
	streamed map[string]int
	// steerInputs binds additional inputs to this exact owner.
	steerInputs map[string]struct{}
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
	id := newID()
	if request.MessageId != nil && uuidPattern.MatchString(*request.MessageId) {
		if _, used := s.inputs[*request.MessageId]; !used {
			id = *request.MessageId
		}
	}
	if options.HostInvocationID == "" {
		options.HostInvocationID = newID()
	}
	t := c.installTurnLocked(ctx, s, false)
	t.input = agentclient.PromptSubmission{SessionID: s.id, HostInvocationID: options.HostInvocationID, InputID: id, NativeInputID: pointer(id)}
	t.options = options
	s.inputs[id] = struct{}{}
	t.request.Input = input
	t.request.ClientUserMessageID = pointer(id)
	var once sync.Once
	var result agentclient.PromptResult
	return func() agentclient.PromptResult {
		once.Do(func() { result = c.runPrompt(t) })
		return result.Snapshot()
	}, nil
}

func (c *Client) installTurnLocked(ctx context.Context, s *session, compact bool) *turn {
	prepare, stopPrepare := context.WithCancelCause(ctx)
	native, stopNative := context.WithCancelCause(c.life)
	observeCtx, stopObserve := context.WithCancel(c.life)
	t := &turn{
		session: s, compact: compact, mode: s.mode, prepare: prepare, stopPrepare: stopPrepare, native: native, stopNative: stopNative,
		observeCtx: observeCtx, stopObserve: stopObserve, ready: make(chan struct{}), terminal: make(chan struct{}), released: make(chan struct{}),
		updates: make(chan observation, maxPendingUpdates), observerDone: make(chan struct{}),
		admission: agentclient.AdmissionNotAccepted, tools: make(map[string]acp.ToolCallStatus), toolOutput: make(map[string]string), streamed: make(map[string]int),
		steerInputs: make(map[string]struct{}), completedMessages: make(map[string]struct{}), model: value(s.settings.Observed.Model),
		previous: s.usageCheckpoint, usageGap: s.usageCheckpoint == nil,
		outcome: agentclient.NativeOutcome{Turn: acpmeta.NativeTurn{InvocationID: newID(), Origin: acpmeta.NativeTurnOriginHost}},
	}
	t.request = s.turnParams()
	if t.request.Model != nil && *t.request.Model != t.model {
		t.model = ""
	}
	s.active = t
	c.interactions.Add(1)
	return t
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
	go func() { defer c.interactions.Done(); c.observeTurn(t) }()
	if err := context.Cause(t.prepare); err != nil {
		return frameWrite{}, err
	}
	if t.options.Prepare != nil {
		cleanup, err := t.options.Prepare(t.prepare)
		t.cleanup = cleanup
		if err != nil {
			return frameWrite{}, err
		}
	}
	if cause := context.Cause(t.prepare); cause != nil {
		return frameWrite{}, cause
	}
	if !t.compact {
		if err := c.refreshSkills(t.prepare, append([]string{t.session.cwd}, t.session.roots...)); err != nil {
			return frameWrite{}, err
		}
	}
	if t.options.BeforeSubmit != nil {
		if err := t.options.BeforeSubmit(t.prepare, t.input.Snapshot()); err != nil {
			return frameWrite{}, err
		}
	}
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

func (c *Client) bindTurnLocked(t *turn, id string) {
	if t.outcome.Turn.NativeTurnID != nil || id == "" {
		return
	}
	if c.seenTurnLocked(t.session.id, id) {
		return
	}
	c.rememberTurnLocked(t.session.id, id)
	t.outcome.Turn.NativeTurnID = pointer(id)
	t.admission = agentclient.AdmissionConfirmed
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
	close(t.updates)
	c.mu.Unlock()
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
		if t.outcome.Turn.Status == "" || t.consumed == nil {
			t.session.usageCheckpoint = nil
		}
	}
	close(t.released)
	submission := written.submission()
	admission := t.admission
	if submission == agentclient.SubmissionNotSent {
		admission = agentclient.AdmissionNotAccepted
	}
	result := agentclient.PromptResult{Receipt: agentclient.PromptReceipt{Input: t.input, Submission: submission, Admission: admission, OperationError: err}}
	if submission != agentclient.SubmissionNotSent && t.outcome.Turn.NativeTurnID != nil {
		t.outcome.PossibleSideEffects = true
		if t.outcome.Turn.Status == "" {
			t.outcome.OperationError = err
			if t.outcome.OperationError == nil {
				t.outcome.OperationError = errUnconfirmed
			}
		}
		o := t.outcome.Snapshot()
		result.Outcome = &o
	}
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
	if t == nil && !s.busy && !s.closing && !request.PromptRequired {
		if err := ctx.Err(); err != nil {
			c.mu.Unlock()
			return agentclient.SteerResponse{}, err
		}
		turnCtx, cancel := context.WithCancelCause(context.WithoutCancel(ctx))
		t = c.installTurnLocked(turnCtx, s, false)
		id := newID()
		t.input = agentclient.PromptSubmission{SessionID: s.id, HostInvocationID: newID(), InputID: id, NativeInputID: pointer(id)}
		t.request.Input = input
		t.request.ClientUserMessageID = pointer(id)
		s.inputs[id] = struct{}{}
		c.mu.Unlock()
		results := make(chan agentclient.PromptResult, 1)
		go func() {
			defer cancel(nil)
			results <- c.runPrompt(t)
		}()
		select {
		case <-t.ready:
			return agentclient.SteerResponse{Outcome: "startedNewTurn"}, nil
		case result := <-results:
			if result.Receipt.Admission == agentclient.AdmissionConfirmed {
				return agentclient.SteerResponse{Outcome: "startedNewTurn"}, nil
			}
			err := result.Receipt.OperationError
			if err == nil {
				err = errUnconfirmed
			}
			return agentclient.SteerResponse{}, err
		case <-ctx.Done():
			cancel(ctx.Err())
			return agentclient.SteerResponse{}, ctx.Err()
		}
	}
	if t == nil || t.compact || t.stopped || t.outcome.Turn.Status != "" || s.closing {
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
	id := newID()
	c.mu.Lock()
	nativeID := value(t.outcome.Turn.NativeTurnID)
	c.mu.Unlock()
	params := protocol.TurnSteerParams{ThreadID: string(s.id), ExpectedTurnID: nativeID, Input: input, ClientUserMessageID: &id}
	var reply protocol.TurnSteerResponse
	_, err = c.call(ctx, "turn/steer", params, &reply, func() error {
		if !c.activeLocked(t) || t.stopped || t.outcome.Turn.Status != "" || s.closing || value(t.outcome.Turn.NativeTurnID) != nativeID {
			return errTurnChanged
		}
		t.steerInputs[id] = struct{}{}
		return nil
	}, func(frame rpcFrame) {
		var ack protocol.TurnSteerResponse
		if c.activeLocked(t) && t.outcome.Turn.Status == "" && json.Unmarshal(frame.Result, &ack) == nil && ack.TurnID == nativeID {
			consumeInput(t, id)
		}
	})
	if err != nil {
		return agentclient.SteerResponse{}, err
	}
	if reply.TurnID != nativeID {
		return agentclient.SteerResponse{}, errTurnChanged
	}
	return agentclient.SteerResponse{Outcome: "injected"}, nil
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
	t := c.installTurnLocked(ctx, s, true)
	c.mu.Unlock()
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

package acpclient

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/JieWaZi/acp-go/pkg/acpmeta"
	agentclient "github.com/JieWaZi/acp-go/pkg/agentclient"
	acp "github.com/JieWaZi/acp-go/sdk"
)

const instructionRefreshPrompts = 10

type turn struct {
	// session is the exact generation reserved before preparation.
	session *session
	// conn is the only transport allowed to send input and cancellation.
	conn *acp.ClientSideConnection
	// scope and input are frozen identities, independent of provider support.
	scope agentclient.Scope
	input agentclient.PromptSubmission
	// preparation is canceled immediately on captured stop.
	preparation     context.Context
	stopPreparation context.CancelFunc
	// interactions close on cancel or terminal evidence.
	interactions     context.Context
	stopInteractions context.CancelFunc
	// settings freezes the model and independent execution choices before Prepare.
	settings []setting
	// delivery owns only observations for this turn.
	delivery *delivery
	// claimed makes the returned continuation one-shot.
	claimed atomic.Bool
	// writing starts at the native write gate, before stdout can respond.
	writing bool
	// writeDone waits for local byte evidence before cancellation.
	writeDone chan struct{}
	// write holds the SDK's actual prompt byte receipt.
	write acp.RequestWriteResult
	// received closes when a matched response is captured on the reader.
	received chan struct{}
	// settled stops an already captured control task when no response can arrive.
	settled chan struct{}
	// cancelDone blocks releasing the owner until this turn's control task ends.
	cancelDone chan struct{}
	// cancelled marks the original owner at Stop capture time.
	cancelled bool
	// terminal blocks later notification and reverse-request attribution.
	terminal bool
	// observedOutput records a matched execution frame, independent of delivery.
	observedOutput bool
	// expectsAnswer excludes native slash commands from empty-answer correction.
	expectsAnswer bool
	// answered accepts visible answer content or a native tool action.
	answered bool
	// reasoned records a native thought that did not itself answer the prompt.
	reasoned bool
	// inputAccepted records explicit input admission on peers that report it.
	inputAccepted bool
	// associated records an explicit turn-scoped native update.
	associated bool
	// authLost is set only by an exact native sign-in failure chunk.
	authLost bool
	// elicitations binds completion to its original URL request.
	elicitations map[acp.UnstableElicitationId]struct{}
	// followups prevent duplicate native reverse requests from submitting an answer twice.
	followups map[acp.ToolCallId]struct{}
	// contextUsage holds known session context counters separately from consumption.
	contextUsage agentclient.ContextUsage
	// usage keeps invocation accounting separate from auxiliary observations.
	usage promptUsage
	// outcome freezes terminal facts before observation drain.
	outcome *agentclient.NativeOutcome
	// controlErr preserves bounded native Cancel failure independently of delivery.
	controlErr error
}

type promptResponse struct {
	// StopReason is validated before it is treated as a native terminal.
	StopReason acp.StopReason `json:"stopReason"`
	// UserMessageID is an optional provider receipt for persisted user input.
	UserMessageID string `json:"userMessageId"`
	// Usage is an optional ACP counter whose scope depends on the provider.
	Usage *PromptUsageCounters `json:"usage"`
	// Meta can carry explicitly associated complete usage or a fork position.
	Meta map[string]any `json:"_meta"`
}

func (c *Client) ReservePrompt(ctx context.Context, request acp.PromptRequest, options agentclient.PromptOptions) (func() agentclient.PromptResult, error) {
	if request.SessionId == "" || len(request.Prompt) == 0 {
		return nil, invalid("ACP agent prompt requires an open session and input")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	frozen, err := clone(request)
	if err != nil {
		return nil, invalid("invalid ACP agent prompt content")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.admissionLocked(ctx); err != nil {
		return nil, err
	}
	s := c.sessions[frozen.SessionId]
	if s == nil {
		return nil, invalid("ACP agent session is not open")
	}
	prep, stopPrep := context.WithCancel(ctx)
	interactions, stopInteractions := context.WithCancel(context.WithoutCancel(ctx))
	inputID := newID()
	if frozen.MessageId != nil && len(s.usedInputIDs) < 4096 {
		provided := *frozen.MessageId
		key := strings.ToLower(provided)
		if _, reused := s.usedInputIDs[key]; !reused && inputIDPattern.MatchString(provided) {
			if s.usedInputIDs == nil {
				s.usedInputIDs = make(map[string]struct{})
			}
			s.usedInputIDs[key] = struct{}{}
			inputID = provided
		}
	}
	if options.HostInvocationID == "" {
		options.HostInvocationID = newID()
	}
	t := &turn{session: s, conn: c.conn, preparation: prep, stopPreparation: stopPrep, interactions: interactions,
		stopInteractions: stopInteractions, settings: c.freezeSettings(s.config), delivery: newDelivery(c.ctx), writeDone: make(chan struct{}),
		received: make(chan struct{}), settled: make(chan struct{}),
		scope: agentclient.Scope{SessionID: s.id, HostInvocationID: options.HostInvocationID, NativeInvocationID: newID()},
		input: agentclient.PromptSubmission{SessionID: s.id, HostInvocationID: options.HostInvocationID, InputID: inputID}}
	if c.opts.Protocol.EmptyAnswerFailure != nil {
		t.expectsAnswer = promptExpectsAnswer(frozen.Prompt)
	}
	// The supported native launch profiles have no proven messageId contract.
	frozen.MessageId = nil
	c.busy, c.turn = true, t
	c.workers.Add(1)
	stopCaller := context.AfterFunc(ctx, func() { c.cancelTurn(t) })
	stopClient := context.AfterFunc(c.ctx, func() { c.cancelTurn(t) })
	return func() agentclient.PromptResult {
		if !t.claimed.CompareAndSwap(false, true) {
			return agentclient.PromptResult{Receipt: agentclient.PromptReceipt{
				Input: t.input, Submission: agentclient.SubmissionNotSent, Admission: agentclient.AdmissionNotAccepted,
				OperationError: errors.New("ACP agent prompt continuation already consumed"),
			}}
		}
		defer stopCaller()
		defer stopClient()
		return c.runPrompt(t, frozen, options)
	}, nil
}

func (c *Client) runPrompt(t *turn, request acp.PromptRequest, options agentclient.PromptOptions) (result agentclient.PromptResult) {
	result.Receipt = agentclient.PromptReceipt{Input: t.input, Submission: agentclient.SubmissionNotSent, Admission: agentclient.AdmissionNotAccepted}
	defer c.releaseTurn(t)
	if err := t.preparation.Err(); err != nil {
		result.Receipt.OperationError = err
		return result
	}
	if options.Prepare != nil {
		cleanup, err := options.Prepare(t.preparation)
		if cleanup != nil {
			defer cleanup()
		}
		if err != nil {
			result.Receipt.OperationError = err
			return result
		}
	}
	configCtx, stopConfig := context.WithTimeout(t.preparation, requestTimeout)
	err := c.prepareSettings(configCtx, t)
	stopConfig()
	if err != nil {
		result.Receipt.OperationError = err
		return result
	}
	c.mu.Lock()
	instructions := t.session.instructions
	epoch := t.session.instructionEpoch
	if t.session.instructionPrompts > 0 && (t.session.contextUsed != nil || t.session.instructionPrompts < instructionRefreshPrompts) {
		instructions = ""
	}
	c.mu.Unlock()
	if instructions != "" {
		request.Prompt = append([]acp.ContentBlock{acp.TextBlock("<developer-instructions>\n" + instructions + "\n</developer-instructions>")}, request.Prompt...)
	}
	if err := t.preparation.Err(); err != nil {
		result.Receipt.OperationError = err
		return result
	}
	if options.BeforeSubmit != nil {
		if err := options.BeforeSubmit(t.preparation, t.input.Snapshot()); err != nil {
			result.Receipt.OperationError = err
			return result
		}
	}
	var settleJournal func(context.Context, *acp.StopReason) PromptJournalEvidence
	if capture := c.opts.Protocol.CapturePromptJournal; capture != nil {
		settleJournal = capture(t.preparation, PromptJournalScope{Version: c.verifiedNativeVersion(), SessionID: t.session.id, Cwd: t.session.cwd})
	}
	wait, stopWait := context.WithCancel(c.ctx)
	defer stopWait()
	var writeDeadline *time.Timer
	writeCtx, stopWrite := context.WithTimeout(t.preparation, requestTimeout)
	defer stopWrite()
	r := acp.SendClientRequestWithReceipt[promptResponse](t.conn, wait, acp.AgentMethodSessionPrompt, request, acp.RequestWriteHooks{
		Context: writeCtx,
		Commit: func() error {
			c.mu.Lock()
			defer c.mu.Unlock()
			if c.closed || c.unusable || c.turn != t || c.sessions[t.session.id] != t.session {
				return ErrRestartRequired
			}
			if t.cancelled || t.preparation.Err() != nil {
				return context.Canceled
			}
			for _, wanted := range t.settings {
				if !c.settingActiveLocked(t.session, wanted) {
					return errSettingsChanged
				}
			}
			t.writing = true
			writeDeadline = time.AfterFunc(requestTimeout, func() {
				c.mu.Lock()
				blocked := c.turn == t && t.write.FrameBytes == 0
				c.mu.Unlock()
				if blocked {
					c.breakTransport(t)
				}
			})
			return nil
		},
		Complete: func(w acp.RequestWriteResult) {
			c.mu.Lock()
			t.write = w
			close(t.writeDone)
			c.mu.Unlock()
			writeDeadline.Stop()
		},
	}, func(r acp.RequestReceipt[promptResponse]) {
		c.capturePromptResponse(t, r)
	})
	if r.Write.BytesWritten > 0 {
		result.Receipt.Submission = agentclient.SubmissionUnknown
		result.Receipt.Admission = agentclient.AdmissionUnknown
		if r.Write.BytesWritten == r.Write.FrameBytes {
			result.Receipt.Submission = agentclient.SubmissionSent
		}
	}
	operationErr := promptOperationError(c, r, t.session.id)
	preAcceptanceRejection := c.preAcceptanceRejection(r.PeerErr, t.session.id)
	var journal PromptJournalEvidence
	if settleJournal != nil && !preAcceptanceRejection && r.Write.BytesWritten > 0 && r.Write.BytesWritten == r.Write.FrameBytes {
		var reason *acp.StopReason
		if r.ResultValid && knownStopReason(r.Result.StopReason) {
			reason = &r.Result.StopReason
		}
		journal = settleJournal(c.ctx, reason)
	}
	c.mu.Lock()
	if !t.terminal {
		t.terminal = true
		t.stopInteractions()
		t.outcome = c.nativeOutcomeLocked(t, nil, nil)
	}
	if journal.NativeTurnID != "" {
		t.associated = true
		if t.outcome == nil {
			t.outcome = c.nativeOutcomeLocked(t, nil, r.PeerErr)
		}
		t.outcome.Turn.NativeTurnID = &journal.NativeTurnID
	}
	if t.outcome != nil {
		if journal.UnverifiedTerminal && t.outcome.NativeFailure == nil {
			t.outcome.StopReason, t.outcome.Turn.Status = nil, ""
			t.outcome.Unavailable = append(t.outcome.Unavailable, "turn.terminal")
			operationErr = errors.Join(operationErr, errors.New("ACP agent journal did not verify the prompt terminal"))
			c.unusable = true
		}
		if journal.Failure != nil && t.outcome.NativeFailure == nil {
			t.outcome.NativeFailure = freezeProtocolError(journal.Failure)
			t.outcome.Turn.Status = acpmeta.NativeTurnFailed
			t.authLost = journal.Failure.Code == -32000
		}
		if journal.Usage != nil {
			report := *journal.Usage
			if previous := t.outcome.Turn.Usage; previous != nil {
				report.Observations = append(report.Observations, previous.Observations...)
			}
			report.ContextKnown = t.outcome.ContextUsage.UsedTokens != nil && t.outcome.ContextUsage.WindowTokens != nil
			t.outcome.Turn.Usage = &report
		}
	}
	if t.authLost || r.Write.BytesWritten > 0 && !knownStopReason(r.Result.StopReason) && !preAcceptanceRejection {
		c.unusable = true
	}
	associated := t.outcome != nil && (t.associated || r.ResultValid && t.outcome.StopReason != nil)
	if c.opts.Protocol.ReadPromptEvidence != nil && r.ResultValid {
		associated = t.inputAccepted
	}
	switch {
	case preAcceptanceRejection:
		// An explicit matched refusal outweighs notifications stamped only
		// with the session. They remain observations, not input acceptance.
		result.Receipt.Admission = agentclient.AdmissionNotAccepted
	case t.outcome != nil && r.Write.BytesWritten > 0:
		if associated || t.authLost && c.opts.Protocol.ReadPromptEvidence == nil {
			result.Receipt.Admission = agentclient.AdmissionConfirmed
		}
		if associated {
			// Standard ACP never supplied a native input ID.
			t.outcome.Turn.ConsumedInputIDs = []string{t.input.InputID}
		}
	}
	if result.Receipt.Admission == agentclient.AdmissionConfirmed && t.session.instructionEpoch == epoch {
		if instructions != "" {
			t.session.instructionPrompts = 1
		} else if t.session.instructionPrompts > 0 && t.session.instructionPrompts < instructionRefreshPrompts {
			t.session.instructionPrompts++
		}
	}
	c.sealDeliveryLocked(t.delivery)
	outcome := t.outcome
	close(t.settled)
	control := t.cancelDone
	c.mu.Unlock()
	if control != nil {
		<-control
	}
	drain, cancel := context.WithTimeout(c.ctx, deliveryTimeout)
	dispatchErr := t.conn.WaitForNotifications(drain, r.NotificationWatermark)
	cancel()
	deliveryErr := errors.Join(c.waitDelivery(t.delivery), dispatchErr)
	c.mu.Lock()
	controlErr := t.controlErr
	c.mu.Unlock()
	if outcome != nil && outcome.NativeFailure != nil && r.PeerErr != nil {
		// A provider refusal/failure belongs to NativeFailure, while transport
		// errors remain independent even if both facts arrived together.
		operationErr = nil
		if cause := errors.Join(r.TransportErr, r.Write.Err); cause != nil {
			operationErr = safeOperationError(acp.AgentMethodSessionPrompt, cause)
		}
	}
	if controlErr == nil {
		select {
		case <-t.conn.Done():
			if r.TransportErr != nil || operationErr == nil && r.ResultValid {
				operationErr = c.explainExit(acp.AgentMethodSessionPrompt, t.conn.Err())
			}
		default:
		}
	}
	if outcome != nil {
		if preAcceptanceRejection {
			result.Receipt.OperationError = operationErr
			operationErr = nil
		}
		outcome.OperationError = errors.Join(operationErr, controlErr)
		outcome.DeliveryError = deliveryErr
		result.Outcome = outcome
	} else {
		result.Receipt.OperationError = errors.Join(operationErr, controlErr)
	}
	return result.Snapshot()
}

func (c *Client) capturePromptResponse(t *turn, r acp.RequestReceipt[promptResponse]) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.turn != t || t.terminal {
		return
	}
	t.terminal = true
	t.stopInteractions()
	close(t.received)
	if r.ResultValid {
		t.outcome = c.nativeOutcomeLocked(t, &r.Result, nil)
	} else {
		t.outcome = c.nativeOutcomeLocked(t, nil, r.PeerErr)
	}
}

func (c *Client) nativeOutcomeLocked(t *turn, response *promptResponse, peer *acp.RequestError) *agentclient.NativeOutcome {
	outcome := &agentclient.NativeOutcome{
		Turn:         acpmeta.NativeTurn{InvocationID: t.scope.NativeInvocationID, Origin: acpmeta.NativeTurnOriginHost},
		ContextUsage: t.contextUsage, ObservedOutput: t.observedOutput, PossibleSideEffects: true,
	}
	scope := c.opts.Protocol.PromptUsage
	if read := c.opts.Protocol.ReadPromptUsage; response != nil && read != nil {
		nativeUsage, classified := read(c.verifiedNativeVersion(), response.Usage, response.Meta)
		scope = classified
		if nativeUsage != nil {
			t.usage.recordMetadata(acpmeta.WithTurnUsage(nil, *nativeUsage))
		}
	}
	outcome.Turn.Usage = t.usage.settle(&t.session.usage, scope, response)
	if t.usage.truncated {
		outcome.Unavailable = append(outcome.Unavailable, "turn.usage.observations")
	}
	if response != nil {
		switch response.StopReason {
		case acp.StopReasonEndTurn, acp.StopReasonMaxTokens, acp.StopReasonMaxTurnRequests, acp.StopReasonRefusal:
			outcome.StopReason, outcome.Turn.Status = &response.StopReason, acpmeta.NativeTurnCompleted
		case acp.StopReasonCancelled:
			outcome.StopReason, outcome.Turn.Status = &response.StopReason, acpmeta.NativeTurnCancelled
		}
		if read := c.opts.Protocol.ReadPromptEvidence; read != nil && outcome.StopReason != nil {
			evidence := read(response.StopReason, response.UserMessageID, response.Meta)
			t.inputAccepted = evidence.InputAccepted
			if evidence.NativeTurnID != "" {
				outcome.Turn.NativeTurnID = acp.Ptr(evidence.NativeTurnID)
			}
			if evidence.Failure != nil {
				outcome.NativeFailure = freezeProtocolError(evidence.Failure)
				outcome.Turn.Status = acpmeta.NativeTurnFailed
				t.authLost = evidence.Failure.Code == -32000
			}
		}
		if outcome.StopReason != nil && acpmeta.ForkMode(c.initialized.Meta) == acpmeta.ForkTurn {
			outcome.ForkPosition = acpmeta.ForkPosition(response.Meta)
		}
	}
	rejected := c.preAcceptanceRejection(peer, t.session.id)
	if rejected {
		outcome.Turn.Origin = acpmeta.NativeTurnOriginUnknown
	} else if t.authLost && outcome.NativeFailure == nil {
		outcome.NativeFailure = freezeProtocolError(acp.NewAuthRequired(map[string]any{"method": acp.AgentMethodSessionPrompt, "classification": "authentication_lost", "restartRequired": true}))
		if outcome.StopReason != nil || peer != nil {
			outcome.Turn.Status = acpmeta.NativeTurnFailed
		}
	} else if peer != nil && t.associated {
		outcome.NativeFailure = freezeProtocolError(c.safeSessionPeerError(peer, acp.AgentMethodSessionPrompt, t.session.id))
		outcome.Turn.Status = acpmeta.NativeTurnFailed
	}
	if fail := c.opts.Protocol.EmptyAnswerFailure; fail != nil && response != nil &&
		response.StopReason == acp.StopReasonEndTurn && outcome.NativeFailure == nil &&
		t.expectsAnswer && !t.answered && !t.cancelled && t.delivery.err == nil {
		outcome.NativeFailure = freezeProtocolError(fail(t.reasoned))
		outcome.Turn.Status = acpmeta.NativeTurnFailed
	}
	if outcome.StopReason == nil && !t.associated && !t.authLost && outcome.Turn.Usage == nil {
		return nil
	}
	if outcome.Turn.Usage != nil {
		usage := *outcome.Turn.Usage
		usage.ContextKnown = outcome.ContextUsage.UsedTokens != nil && outcome.ContextUsage.WindowTokens != nil
		outcome.Turn.Usage = &usage
	}
	return outcome
}

func promptExpectsAnswer(blocks []acp.ContentBlock) bool {
	for _, block := range blocks {
		if block.Text != nil {
			if text := strings.TrimSpace(block.Text.Text); text != "" {
				return !strings.HasPrefix(text, "/")
			}
		}
	}
	return true
}

func promptOperationError(c *Client, r acp.RequestReceipt[promptResponse], id acp.SessionId) error {
	if err := sessionReceiptFailure(c, r, acp.AgentMethodSessionPrompt, id); err != nil {
		var protocol *acp.RequestError
		if errors.As(err, &protocol) {
			return freezeProtocolError(protocol)
		}
		return err
	}
	if knownStopReason(r.Result.StopReason) {
		return nil
	}
	return errors.New("ACP agent did not report a known prompt terminal reason")
}

func knownStopReason(reason acp.StopReason) bool {
	switch reason {
	case acp.StopReasonEndTurn, acp.StopReasonMaxTokens, acp.StopReasonMaxTurnRequests, acp.StopReasonRefusal, acp.StopReasonCancelled:
		return true
	default:
		return false
	}
}

func (c *Client) preAcceptanceRejection(err *acp.RequestError, id acp.SessionId) bool {
	if err == nil {
		return false
	}
	switch err.Code {
	case -32700, -32600, -32601, -32602, -32000:
		// JSON-RPC parse, request, method and parameter errors precede
		// dispatch. ACP authentication refusal also prevents acceptance.
		return true
	default:
		return c.missingSession(err, id)
	}
}

func (c *Client) missingSession(err *acp.RequestError, id acp.SessionId) bool {
	if err == nil || id == "" {
		return false
	}
	return !c.opts.Protocol.RequireExactMissing && err.Code == -32002 ||
		c.opts.Protocol.SessionMissing != nil && c.opts.Protocol.SessionMissing(err, id)
}

func sessionReceiptFailure[T any](c *Client, r acp.RequestReceipt[T], method string, id acp.SessionId) error {
	if r.PeerErr != nil {
		return c.safeSessionPeerError(r.PeerErr, method, id)
	}
	return receiptFailure(r, method)
}

func (c *Client) safeSessionPeerError(peer *acp.RequestError, method string, id acp.SessionId) *acp.RequestError {
	result := safePeerError(peer, method)
	if c.missingSession(peer, id) {
		result.Data = map[string]any{"method": method, "classification": "session_missing"}
	} else if c.opts.Protocol.RequireExactMissing && peer.Code == -32002 {
		// Preserve an unrelated missing resource as a provider error. A bare
		// -32002 would otherwise authorize session replacement in the host.
		result = &acp.RequestError{Code: -32603, Message: "ACP agent resource unavailable", Data: map[string]any{"method": method}}
	}
	return result
}

func nonnegative(value *int64) *int64 {
	if value == nil || *value < 0 {
		return nil
	}
	return value
}

func (c *Client) releaseTurn(t *turn) {
	t.stopPreparation()
	t.stopInteractions()
	t.delivery.cancel()
	c.mu.Lock()
	if c.turn == t {
		c.turn = nil
		c.busy = false
	}
	c.mu.Unlock()
	c.workers.Done()
}

func (c *Client) Cancel(notification acp.CancelNotification) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.turn != nil && c.turn.session.id == notification.SessionId {
		c.captureCancelLocked(c.turn)
	}
	return nil
}

func (c *Client) cancelTurn(t *turn) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.turn == t {
		c.captureCancelLocked(t)
	}
}

func (c *Client) captureCancelLocked(t *turn) {
	if t.cancelled || t.terminal {
		return
	}
	t.cancelled = true
	t.stopPreparation()
	t.stopInteractions()
	if !t.writing {
		return
	}
	t.cancelDone = make(chan struct{})
	deadline := time.Now().Add(controlTimeout)
	go c.sendCancel(t, deadline)
}

func (c *Client) sendCancel(t *turn, deadline time.Time) {
	defer close(t.cancelDone)
	ctx, cancel := context.WithDeadline(c.ctx, deadline)
	defer cancel()
	select {
	case <-t.received:
		return
	case <-t.settled:
		return
	case <-t.writeDone:
	case <-ctx.Done():
		c.recordControlFailure(t, ctx.Err())
		return
	}
	c.mu.Lock()
	mayWrite := c.turn == t && !t.terminal && t.write.BytesWritten > 0
	c.mu.Unlock()
	if !mayWrite {
		return
	}
	// Even an OS pipe write that has entered the SDK gate is bounded.
	watchdog := time.AfterFunc(time.Until(deadline), func() { c.recordControlFailure(t, context.DeadlineExceeded) })
	r := acp.SendClientNotificationWithWriteHooks(t.conn, ctx, acp.AgentMethodSessionCancel, acp.CancelNotification{SessionId: t.session.id}, acp.RequestWriteHooks{
		Commit: func() error {
			c.mu.Lock()
			defer c.mu.Unlock()
			if c.turn != t || t.terminal || c.closed || c.sessions[t.session.id] != t.session {
				return errControlRetired
			}
			return nil
		},
	})
	watchdog.Stop()
	if errors.Is(r.Err, errControlRetired) {
		return
	}
	if r.Err != nil {
		c.recordControlFailure(t, r.Err)
		return
	}
	grace := time.NewTimer(time.Until(deadline.Add(cancelGrace)))
	defer grace.Stop()
	select {
	case <-t.received:
	case <-t.settled:
	case <-c.ctx.Done():
	case <-grace.C:
		c.recordControlFailure(t, context.DeadlineExceeded)
	}
}

var (
	errControlRetired  = errors.New("ACP agent control owner already retired")
	errSettingsChanged = errors.New("ACP agent settings changed before prompt submission")
)

func (c *Client) recordControlFailure(t *turn, err error) {
	c.mu.Lock()
	if c.turn != t || t.terminal || c.closed {
		c.mu.Unlock()
		return
	}
	t.controlErr = safeOperationError(acp.AgentMethodSessionCancel, err)
	c.unusable = true
	c.mu.Unlock()
	t.conn.Close()
}

func (c *Client) breakTransport(t *turn) {
	c.mu.Lock()
	if c.turn != t || c.closed || t.write.FrameBytes != 0 {
		c.mu.Unlock()
		return
	}
	c.unusable = true
	c.mu.Unlock()
	t.conn.Close()
}

// An immutable safe RPC error provides a fresh Data value to each projection.
type protocolErrorSnapshot string

func freezeProtocolError(err *acp.RequestError) error { return protocolErrorSnapshot(err.Error()) }
func (err protocolErrorSnapshot) Error() string       { return string(err) }
func (err protocolErrorSnapshot) As(target any) bool {
	value, ok := target.(**acp.RequestError)
	if !ok {
		return false
	}
	var projected acp.RequestError
	if json.Unmarshal([]byte(err), &projected) != nil {
		return false
	}
	*value = &projected
	return true
}

func rememberTool(t *turn, id acp.ToolCallId) bool {
	if id == "" {
		return false
	}
	owners := t.session.toolOwners
	if owner, known := owners[id]; known {
		return owner == t.scope.NativeInvocationID
	}
	if len(owners) >= 4096 {
		return false
	}
	if owners == nil {
		owners = make(map[acp.ToolCallId]string)
		t.session.toolOwners = owners
	}
	owners[id] = t.scope.NativeInvocationID
	return true
}

func approvalOption(request acp.RequestPermissionRequest, id acp.PermissionOptionId) bool {
	return slices.ContainsFunc(request.Options, func(option acp.PermissionOption) bool { return option.OptionId == id })
}

var inputIDPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[1-8][0-9a-fA-F]{3}-[89aAbB][0-9a-fA-F]{3}-[0-9a-fA-F]{12}$`)

func newID() string {
	var value [16]byte
	_, _ = rand.Read(value[:])
	value[6] = value[6]&0x0f | 0x40
	value[8] = value[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", value[:4], value[4:6], value[6:8], value[8:10], value[10:])
}

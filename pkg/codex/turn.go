package codex

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/JieWaZi/acp-go/pkg/acpmeta"
	"github.com/JieWaZi/acp-go/pkg/codex/protocol"
	acp "github.com/JieWaZi/acp-go/sdk"
)

// turnKind distinguishes native operations with different admission semantics.
type turnKind uint8

const (
	promptTurn turnKind = iota
	steeringTurn
	compactionTurn
)

// nativeTurn owns the exact submission, native identity, ordered observations and terminal lifecycle.
type nativeTurn struct {
	// kind specifies whether the native turn can accept ordinary input.
	kind turnKind
	// mu protects the native identity, cancellation state, and delivery binding.
	mu sync.Mutex
	// generation is the Agent-wide monotonic invocation identity.
	generation uint64
	// eventRouter maps frames only after the native identity is proven.
	eventRouter *eventRouter
	// clientMessageID is the unique submitted user-message identity used as pre-ACK evidence.
	clientMessageID string
	// eventsMu serializes identity binding, pre-ACK frames, and mapping.
	eventsMu sync.Mutex
	// completionCapture holds unmatched completions received before this invocation binds.
	completionCapture *completionCapture
	// earlyEvents holds frames that cannot be published until the native identity is proven.
	earlyEvents []protocol.ServerNotification
	// earlyOverflow means an unknown-identity frame exceeded the bounded buffer.
	earlyOverflow bool
	// terminal stores the exact native completion independently of buffered display frames.
	terminal *protocol.TurnCompletedNotification
	// terminalSignal closes when this exact native turn reaches a terminal notification.
	terminalSignal chan struct{}
	// observed collects positive native output, effect and retry evidence.
	observed turnObservation
	// eventFailure records mapping/queue failures independently of the native terminal.
	eventFailure error
	// delivery joins queued ACP writes before exposing a terminal result.
	delivery *turnDelivery
	// runCtx outlives a locally cancelled caller while a late native identity is still possible.
	runCtx context.Context
	// runCancel releases protocol waiters when the thread or connection closes.
	runCancel context.CancelFunc
	// turnID is bound by an ACK or a matching native user-message event.
	turnID string
	// cancelRequested records the first local cancellation signal.
	cancelRequested bool
	// finished fences late identities from a foreground operation that has returned.
	finished bool
	// cancelSignal wakes a foreground caller waiting for an unknown turn ID.
	cancelSignal chan struct{}
	// cancelOnce publishes cancellation once.
	cancelOnce sync.Once
	// interruptOnce prevents duplicate native interrupt requests.
	interruptOnce sync.Once
	// interruptDone closes when the native interrupt call returns.
	interruptDone chan struct{}
	// turnStarted publishes the bound turn identity and initialized event mapping.
	turnStarted chan struct{}
	// turnStartedOnce protects the identity barrier from duplicate evidence.
	turnStartedOnce sync.Once
	// foregroundDone signals that ACP response cleanup finished.
	foregroundDone chan struct{}
	// foregroundDoneOnce guards the foreground completion signal.
	foregroundDoneOnce sync.Once
	// backgroundDone signals native waiter cleanup.
	backgroundDone chan struct{}
	// backgroundDoneOnce guards the native cleanup signal.
	backgroundDoneOnce sync.Once
}

// turnObservation contains provider facts scoped to one proven turn.
type turnObservation struct {
	// output records positive evidence of generated assistant text.
	output bool
	// fileChange records a native report of completed file changes.
	fileChange bool
	// toolCompletion records a native tool reaching its terminal item boundary.
	toolCompletion bool
	// lastError retains the last native error notification including willRetry.
	lastError *protocol.ErrorNotification
	// reroutes holds a bounded sequence of native model changes for this turn.
	reroutes []modelReroute
}

// recordCompletedItem retains native completion evidence independently of ACP display.
func (o *turnObservation) recordCompletedItem(item protocol.ThreadItem) {
	switch item.Type {
	case protocol.AgentMessage:
		o.output = o.output || stringValue(item.Text) != ""
	case protocol.FileChange:
		o.fileChange = o.fileChange || stringValue(item.Status) == "completed" && len(item.Changes) > 0
	case protocol.CommandExecution, protocol.MCPToolCall, protocol.DynamicToolCall, protocol.CollabAgentToolCall, protocol.WebSearch:
		o.toolCompletion = o.toolCompletion || stringValue(item.Status) != "declined"
	}
}

// modelReroute records a provider-reported change without reallocating token counts.
type modelReroute struct {
	// From is the native model before the reported change.
	From string `json:"from"`
	// To is the native model named by the provider.
	To string `json:"to"`
	// Reason preserves the native reroute reason.
	Reason string `json:"reason"`
}

// newNativeTurn creates the admission identity before any native submission.
func newNativeTurn(parent context.Context, generation uint64) *nativeTurn {
	runCtx, runCancel := context.WithCancel(parent)
	return &nativeTurn{
		kind:           promptTurn,
		generation:     generation,
		runCtx:         runCtx,
		runCancel:      runCancel,
		cancelSignal:   make(chan struct{}),
		interruptDone:  make(chan struct{}),
		turnStarted:    make(chan struct{}),
		terminalSignal: make(chan struct{}),
		foregroundDone: make(chan struct{}),
		backgroundDone: make(chan struct{}),
	}
}

// markForegroundDone signals that the foreground caller released its ownership.
func (p *nativeTurn) markForegroundDone() {
	p.foregroundDoneOnce.Do(func() { close(p.foregroundDone) })
}

// cancelRun releases this invocation’s native RPC and completion waits.
func (p *nativeTurn) cancelRun() {
	p.runCancel()
}

// requestCancel records cancellation and returns the current native identity.
func (p *nativeTurn) requestCancel() string {
	p.mu.Lock()
	p.cancelRequested = true
	turnID := p.turnID
	p.mu.Unlock()
	p.cancelOnce.Do(func() { close(p.cancelSignal) })
	return turnID
}

// setTurn binds the native identity and reports whether the foreground caller already left.
func (p *nativeTurn) setTurn(turnID string) bool {
	p.mu.Lock()
	p.turnID = turnID
	late := p.cancelRequested || p.finished
	p.mu.Unlock()
	return late
}

// signalTurnStarted publishes the initialized native turn identity to waiters.
func (p *nativeTurn) signalTurnStarted() { p.turnStartedOnce.Do(func() { close(p.turnStarted) }) }

// markBackgroundDone signals that no native wait remains.
func (p *nativeTurn) markBackgroundDone() {
	p.backgroundDoneOnce.Do(func() { close(p.backgroundDone) })
}

// currentTurn returns a synchronized snapshot of the identity and cancellation state.
func (p *nativeTurn) currentTurn() (string, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.turnID, p.cancelRequested
}

// setEventRouter installs the mapper for the proven native identity.
func (p *nativeTurn) setEventRouter(router *eventRouter) {
	p.mu.Lock()
	p.eventRouter = router
	p.mu.Unlock()
}

// currentEventRouter returns the mapper associated with this turn.
func (p *nativeTurn) currentEventRouter() *eventRouter {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.eventRouter
}

// markForegroundFinished prevents late identity evidence from publishing to a finished caller.
func (p *nativeTurn) markForegroundFinished() {
	p.mu.Lock()
	p.finished = true
	p.mu.Unlock()
}

// compact submits a standalone native turn and holds admission until its terminal notification.
func (a *Agent) compact(ctx context.Context, raw json.RawMessage) (acpmeta.CompactResponse, error) {
	var request acpmeta.CompactRequest
	if err := json.Unmarshal(raw, &request); err != nil || request.SessionID == "" {
		return acpmeta.CompactResponse{}, acp.NewInvalidParams(map[string]any{"error": "session_id is required"})
	}
	if err := a.requireInitialized(); err != nil {
		return acpmeta.CompactResponse{}, err
	}
	state, ok := a.sessions.get(request.SessionID)
	if !ok {
		return acpmeta.CompactResponse{}, codexSessionNotFoundError(request.SessionID)
	}
	prompt := newNativeTurn(a.runtimeCtx, a.nextTurnGeneration.Add(1))
	prompt.kind = compactionTurn
	if err := a.installTurn(state, prompt); err != nil {
		prompt.cancelRun()
		return acpmeta.CompactResponse{}, err
	}
	capture, err := a.client.completions.beginCapture(state.id)
	if err != nil {
		a.releaseTurn(state, prompt)
		prompt.cancelRun()
		a.finishTurnForeground(prompt)
		a.finishTurnBackground(prompt)
		return acpmeta.CompactResponse{}, err
	}
	prompt.setCompletionCapture(capture)
	type result struct {
		// response carries the precise native terminal result.
		response acpmeta.CompactResponse
		// err is the request or completion failure.
		err error
	}
	results := make(chan result, 1)
	go func() {
		var finished sync.Once
		finish := func() {
			finished.Do(func() {
				prompt.markForegroundFinished()
				a.releaseTurn(state, prompt)
				prompt.cancelRun()
				a.finishTurnBackground(prompt)
			})
		}
		defer finish()
		err := awaitNativeAcknowledgment(prompt.runCtx, prompt.terminalSignal, func(ctx context.Context) error {
			return a.client.ThreadCompactStart(ctx, protocol.ThreadCompactStartParams{ThreadID: state.id})
		})
		reported := false
		if err != nil {
			select {
			case <-prompt.turnStarted:
				// An observed native turn outranks the failed request response.
			default:
				var rejection *rpcError
				if errors.As(err, &rejection) {
					a.client.completions.releaseCapture(state.id, capture)
					finish()
					results <- result{err: codexRequestFailure("compact_start", err)}
					return
				}
				results <- result{err: codexRequestFailure("compact_start", err)}
				reported = true
			}
		}
		select {
		case <-prompt.turnStarted:
		case <-a.client.rpc.Done():
			// Keep an identity/terminal that was already drained before EOF.
			select {
			case <-prompt.turnStarted:
			default:
				a.client.completions.releaseCapture(state.id, capture)
				finish()
				if !reported {
					err := a.client.rpc.Err()
					if err == nil {
						err = ErrAppServerUnavailable
					}
					results <- result{err: codexRequestFailure("compact_identity", err)}
				}
				return
			}
		case <-prompt.runCtx.Done():
			a.client.completions.releaseCapture(state.id, capture)
			if !reported {
				results <- result{err: prompt.runCtx.Err()}
			}
			return
		}
		turnID, _ := prompt.currentTurn()
		var completion protocol.TurnCompletedNotification
		var completionErr error
		if observed := prompt.observedCompletion(); observed != nil {
			completion = *observed
			a.client.completions.releaseCapture(state.id, capture)
		} else {
			completion, completionErr = a.client.completions.waitAfterStart(prompt.runCtx, state.id, turnID, capture)
		}
		router := prompt.currentEventRouter()
		usage, observed := router.Usage()
		if !observed || completionErr != nil || completion.Turn.Status != protocol.FluffyCompleted {
			state.mu.Lock()
			state.usageTotal = nil
			state.mu.Unlock()
		}
		if reported {
			return
		}
		if completionErr != nil {
			finish()
			results <- result{err: turnFailureWithUsage(prompt, "compact_completion", codexRequestFailure("compact_completion", completionErr))}
			return
		}
		if completion.Turn.Status == protocol.Failed && completion.Turn.Error != nil {
			finish()
			results <- result{err: turnFailureWithUsage(prompt, "compact_completion", codexTurnRequestError(completion.Turn.Error))}
			return
		}
		finish()
		response := acpmeta.CompactResponse{TurnID: turnID, Status: string(completion.Turn.Status)}
		if observed {
			response.Meta = acpmeta.WithTurnUsage(nil, usage.Metadata(completion.Turn.Status == protocol.FluffyCompleted))
		}
		results <- result{response: response}
	}()
	defer a.finishTurnForeground(prompt)
	select {
	case result := <-results:
		return result.response, result.err
	case <-ctx.Done():
		turnID := prompt.requestCancel()
		if turnID != "" {
			a.requestTurnInterrupt(state, prompt, turnID, false)
		}
		select {
		case result := <-results:
			return result.response, result.err
		default:
		}
		failure := acp.NewRequestCancelled(map[string]any{"cause": ctx.Err().Error()})
		return acpmeta.CompactResponse{}, turnFailureWithUsage(prompt, "compact_cancel", failure)
	case <-a.runtimeCtx.Done():
		return acpmeta.CompactResponse{}, ErrRuntimeUnavailable
	}
}

// Prompt admits one native submission and joins its terminal result with ACP delivery.
func (a *Agent) Prompt(ctx context.Context, request acp.PromptRequest) (response acp.PromptResponse, promptErr error) {
	if err := a.requireInitialized(); err != nil {
		return acp.PromptResponse{}, err
	}
	state, ok := a.sessions.get(string(request.SessionId))
	if !ok {
		return acp.PromptResponse{}, codexSessionNotFoundError(string(request.SessionId))
	}
	if _, err := a.waitConnection(ctx); err != nil {
		return acp.PromptResponse{}, err
	}
	if err := a.refreshSkills(ctx, state.workspace()); err != nil {
		return acp.PromptResponse{}, err
	}
	input, err := buildPromptInput(request.Prompt)
	if err != nil {
		return acp.PromptResponse{}, err
	}
	prompt := newNativeTurn(a.runtimeCtx, a.nextTurnGeneration.Add(1))
	prompt.clientMessageID = rand.Text()
	if request.MessageId != nil && *request.MessageId != "" {
		prompt.clientMessageID = *request.MessageId
	}
	if err := a.installTurn(state, prompt); err != nil {
		prompt.cancelRun()
		return acp.PromptResponse{}, fmt.Errorf("prompting session %q: %w", request.SessionId, err)
	}
	// Release the foreground lane on return. After early cancellation, the
	// native observer may still need to interrupt a late start.
	defer func() {
		if promptErr != nil || response.StopReason == acp.StopReasonCancelled {
			// A missing tail after failure or cancellation makes the next baseline uncertain.
			state.mu.Lock()
			state.usageTotal = nil
			state.mu.Unlock()
		}
		prompt.markForegroundFinished()
		a.releaseTurn(state, prompt)
		a.finishTurnForeground(prompt)
	}()

	// turnResult transfers one exact native result to the foreground request.
	type turnResult struct {
		// completion is the exact native terminal notification.
		completion protocol.TurnCompletedNotification
		// err carries native submission and completion failures.
		err error
		// deliveryErr carries ACP write failures independently of the native result.
		deliveryErr error
	}
	resultChannel := make(chan turnResult, 1)
	turnParams, err := turnParamsForSession(state, input, &prompt.clientMessageID)
	if err != nil {
		prompt.cancelRun()
		a.finishTurnBackground(prompt)
		return acp.PromptResponse{}, err
	}
	go func() {
		defer a.finishTurnBackground(prompt)
		completion, runErr := a.client.RunTurn(prompt.runCtx, turnParams, func(turnID string) {
			a.onTurnStarted(state, prompt, turnID)
		}, prompt.startObservation())
		deliveryErr := prompt.waitDelivery()
		prompt.cancelRun()
		resultChannel <- turnResult{completion: completion, err: runErr, deliveryErr: deliveryErr}
	}()

	requestDone := ctx.Done()
	cancelSignal := prompt.cancelSignal
	for {
		select {
		case result := <-resultChannel:
			if result.deliveryErr != nil && (result.err != nil || result.completion.Turn.Status != protocol.Failed) {
				failure := codexRequestFailure("event_delivery", result.deliveryErr)
				data := failure.Data.(map[string]any)
				if result.completion.Turn.Status != "" {
					data["native_status"] = string(result.completion.Turn.Status)
				}
				if result.err != nil {
					data["native_failure"] = codexRequestFailure("turn_completion", result.err).Data
				}
				return acp.PromptResponse{}, turnFailureWithUsage(prompt, "event_delivery", failure)
			}
			if result.err != nil {
				if errors.Is(result.err, context.Canceled) {
					_, cancelled := prompt.currentTurn()
					if cancelled {
						return codexPromptResponse(
							prompt,
							acp.StopReasonCancelled,
							request.MessageId,
						), nil
					}
				}
				phase := "turn_start"
				if id, _ := prompt.currentTurn(); id != "" {
					phase = "turn_completion"
				}
				return acp.PromptResponse{}, turnFailureWithUsage(prompt, phase, codexRequestFailure(phase, result.err))
			}
			switch result.completion.Turn.Status {
			case protocol.FluffyInterrupted:
				return codexPromptResponse(prompt, acp.StopReasonCancelled, request.MessageId), nil
			case protocol.FluffyCompleted:
				return codexPromptResponse(prompt, acp.StopReasonEndTurn, request.MessageId), nil
			case protocol.Failed:
				failure := &acp.RequestError{Code: -32603, Message: fmt.Sprintf("codex turn %q failed", result.completion.Turn.ID)}
				if turnError := result.completion.Turn.Error; turnError != nil {
					failure = codexTurnRequestError(turnError)
				}
				failure = turnFailureWithUsage(prompt, "turn_completion", failure)
				if result.deliveryErr != nil {
					failure.Data.(map[string]any)["event_delivery_error"] = result.deliveryErr.Error()
				}
				return acp.PromptResponse{}, failure
			default:
				return acp.PromptResponse{}, fmt.Errorf(
					"codex turn %q completed with unexpected status %q",
					result.completion.Turn.ID,
					result.completion.Turn.Status,
				)
			}
		case <-requestDone:
			turnID := prompt.requestCancel()
			if turnID == "" {
				return codexPromptResponse(prompt, acp.StopReasonCancelled, request.MessageId), nil
			}
			a.requestTurnInterrupt(state, prompt, turnID, false)
			requestDone = nil
			cancelSignal = nil
		case <-cancelSignal:
			turnID, _ := prompt.currentTurn()
			if turnID == "" {
				return codexPromptResponse(prompt, acp.StopReasonCancelled, request.MessageId), nil
			}
			a.requestTurnInterrupt(state, prompt, turnID, false)
			requestDone = nil
			cancelSignal = nil
		case <-a.runtimeCtx.Done():
			return acp.PromptResponse{}, fmt.Errorf("running codex turn: %w", ErrRuntimeUnavailable)
		}
	}
}

// turnFailureWithUsage carries observed consumption across an unsuccessful terminal boundary.
func turnFailureWithUsage(prompt *nativeTurn, phase string, failure *acp.RequestError) *acp.RequestError {
	data, _ := failure.Data.(map[string]any)
	if data == nil {
		data = map[string]any{}
	}
	if _, exists := data["phase"]; !exists {
		data["phase"] = phase
	}
	if turnID, _ := prompt.currentTurn(); turnID != "" {
		data["turn_id"] = turnID
		if value, ok := data["submission"]; !ok || value == "unknown" {
			data["submission"] = "accepted"
		}
	}
	if router := prompt.currentEventRouter(); router != nil {
		if usage, ok := router.Usage(); ok {
			data = acpmeta.WithTurnUsage(data, usage.Metadata(false))
		}
	}
	prompt.eventsMu.Lock()
	observation := prompt.observed
	reroutes := append([]modelReroute(nil), observation.reroutes...)
	prompt.eventsMu.Unlock()
	if observation.output {
		data["output_observed"] = true
	}
	if observation.fileChange {
		data["file_change_observed"] = true
	}
	if observation.toolCompletion {
		data["tool_completion_observed"] = true
	}
	if observation.lastError != nil {
		data["native_error_observation"] = map[string]any{
			"source": "error", "will_retry": observation.lastError.WillRetry, "error": observation.lastError.Error,
		}
	}
	if len(reroutes) > 0 {
		data["native_model_reroutes"] = reroutes
	}
	failure.Data = data
	return failure
}

// codexPromptResponse projects the final native counters and user-message confirmation.
func codexPromptResponse(
	prompt *nativeTurn,
	stopReason acp.StopReason,
	messageID *string,
) acp.PromptResponse {
	response := acp.PromptResponse{
		StopReason:    stopReason,
		UserMessageId: messageID,
	}
	turnID, _ := prompt.currentTurn()
	response.Meta = acpmeta.PositionMetadata(turnID)
	prompt.eventsMu.Lock()
	if len(prompt.observed.reroutes) > 0 {
		if response.Meta == nil {
			response.Meta = make(map[string]any)
		}
		response.Meta["native_model_reroutes"] = append([]modelReroute(nil), prompt.observed.reroutes...)
	}
	prompt.eventsMu.Unlock()
	router := prompt.currentEventRouter()
	if router == nil {
		return response
	}
	usage, ok := router.Usage()
	if ok {
		response.Usage = usage.PromptUsage()
		response.Meta = acpmeta.WithTurnUsage(response.Meta, usage.Metadata(stopReason == acp.StopReasonEndTurn))
	}
	return response
}

// installTurn reserves the exact thread and registers the invocation before native I/O.
func (a *Agent) installTurn(state *sessionState, prompt *nativeTurn) error {
	a.sessions.mu.Lock()
	defer a.sessions.mu.Unlock()
	if a.sessions.sessions[state.id] != state || a.sessions.closing[state.id] > 0 {
		return ErrSessionClosing
	}
	a.turnMu.Lock()
	defer a.turnMu.Unlock()
	state.mu.Lock()
	defer state.mu.Unlock()
	if a.turnsClosing {
		return ErrRuntimeUnavailable
	}
	if state.turnAdmissionClosed {
		return ErrSessionClosing
	}
	if state.replayingHistory {
		return ErrSessionOpening
	}
	if state.activeTurn != nil {
		return ErrPromptActive
	}
	if prompt.kind == compactionTurn {
		// Compact has no turn ID in its ACK. Do not let an unresolved start from a
		// prior foreground request provide a candidate identity for this lane.
		for _, owner := range a.turns {
			if owner.id == state.id {
				return ErrPromptActive
			}
		}
	}
	state.activeTurn = prompt
	a.turns[prompt] = state
	return nil
}

// closeSessionTurns fences admission and returns every invocation owned by the retiring thread.
func (a *Agent) closeSessionTurns(state *sessionState) []*nativeTurn {
	if state == nil {
		return nil
	}
	a.turnMu.Lock()
	defer a.turnMu.Unlock()
	state.mu.Lock()
	state.turnAdmissionClosed = true
	state.mu.Unlock()
	prompts := make([]*nativeTurn, 0)
	for prompt, owner := range a.turns {
		if owner == state {
			prompts = append(prompts, prompt)
		}
	}
	return prompts
}

// closeAllTurns fences Agent-wide admission and snapshots unfinished invocations.
func (a *Agent) closeAllTurns() []*nativeTurn {
	a.turnMu.Lock()
	defer a.turnMu.Unlock()
	a.turnsClosing = true
	prompts := make([]*nativeTurn, 0, len(a.turns))
	for prompt := range a.turns {
		prompts = append(prompts, prompt)
	}
	return prompts
}

// finishTurnForeground releases the foreground half of the invocation.
func (a *Agent) finishTurnForeground(prompt *nativeTurn) {
	prompt.markForegroundDone()
	a.untrackTurnIfDone(prompt)
}

// finishTurnBackground releases the native waiter half of the invocation.
func (a *Agent) finishTurnBackground(prompt *nativeTurn) {
	prompt.markBackgroundDone()
	a.untrackTurnIfDone(prompt)
}

// untrackTurnIfDone removes the invocation after both ownership halves have ended.
func (a *Agent) untrackTurnIfDone(prompt *nativeTurn) {
	select {
	case <-prompt.foregroundDone:
	default:
		return
	}
	select {
	case <-prompt.backgroundDone:
	default:
		return
	}
	a.turnMu.Lock()
	delete(a.turns, prompt)
	a.turnMu.Unlock()
}

// onTurnStarted binds the identity in the ACK and drains previously isolated frames.
func (a *Agent) onTurnStarted(state *sessionState, prompt *nativeTurn, turnID string) {
	prompt.eventsMu.Lock()
	defer prompt.eventsMu.Unlock()
	a.bindTurnLocked(state, prompt, turnID)
}

// bindTurnLocked publishes an identity proved by an ACK or the submitted user-message ID.
func (a *Agent) bindTurnLocked(state *sessionState, prompt *nativeTurn, turnID string) {
	if id := prompt.confirmedTurnID(); id != "" {
		return
	}
	late := prompt.setTurn(turnID) || !a.sessions.isCurrent(state)
	if late && prompt.kind != compactionTurn {
		a.requestTurnInterrupt(state, prompt, turnID, true)
		prompt.signalTurnStarted()
		return
	}
	var updater sessionUpdater
	if prompt.kind != compactionTurn {
		updater = a.currentSessionUpdater()
	}
	a.attachTurnEvents(state, prompt, turnID, updater)
	for _, notification := range prompt.earlyEvents {
		_, id := notificationRouting(notification)
		a.routeBoundTurnLocked(prompt.runCtx, state, prompt, turnID, id, notification)
	}
	prompt.earlyEvents = nil
	if prompt.earlyOverflow {
		if prompt.eventFailure == nil {
			prompt.eventFailure = errEventDeliveryBackpressure
		}
		prompt.currentEventRouter().handler.breakUsageContinuity()
	}
	if prompt.completionCapture != nil {
		if completion := a.client.completions.capturedCompletion(prompt.completionCapture, turnID); completion != nil {
			prompt.recordTerminalLocked(*completion)
		}
	}
	prompt.signalTurnStarted()
	if late {
		a.requestTurnInterrupt(state, prompt, turnID, false)
	}
}

// startObservation connects protocol ACK waiting to this turn's native evidence.
func (p *nativeTurn) startObservation() turnStartObservation {
	return turnStartObservation{
		confirmedID: p.confirmedTurnID,
		terminal:    p.terminalSignal,
		capture:     p.setCompletionCapture,
		completion:  p.observedCompletion,
	}
}

// setCompletionCapture attaches the exact protocol capture before native submission.
func (p *nativeTurn) setCompletionCapture(capture *completionCapture) {
	p.eventsMu.Lock()
	p.completionCapture = capture
	p.eventsMu.Unlock()
}

// observedCompletion returns the exact terminal payload once it is immutable.
func (p *nativeTurn) observedCompletion() *protocol.TurnCompletedNotification {
	select {
	case <-p.terminalSignal:
		return p.terminal
	default:
		return nil
	}
}

// recordTerminalLocked seals the owner after retaining the provider's terminal payload.
func (p *nativeTurn) recordTerminalLocked(completion protocol.TurnCompletedNotification) {
	if p.terminal != nil {
		return
	}
	for _, item := range completion.Turn.Items {
		switch item.Type {
		case protocol.AgentMessage:
			p.observed.recordCompletedItem(item)
		default:
			switch stringValue(item.Status) {
			case "completed", "failed":
				p.observed.recordCompletedItem(item)
			}
		}
	}
	p.terminal = &completion
	close(p.terminalSignal)
}

var errNativeTerminalObserved = errors.New("native terminal observed before acknowledgment")

// awaitNativeAcknowledgment lets independently proven completion end the ACK wait.
// The transport keeps its bounded, one-shot response observer after local cancellation.
func awaitNativeAcknowledgment(ctx context.Context, terminal <-chan struct{}, call func(context.Context) error) error {
	if terminal == nil {
		return call(ctx)
	}
	ackCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	ack := make(chan error, 1)
	go func() { ack <- call(ackCtx) }()
	select {
	case err := <-ack:
		return err
	case <-terminal:
		cancel()
		<-ack
		return errNativeTerminalObserved
	case <-ctx.Done():
		// Join the locally cancelled request. The protocol transport retains any
		// detached observer, but the invocation must not leave a waiter behind.
		err := <-ack
		select {
		case <-terminal:
			return errNativeTerminalObserved
		default:
			if err != nil {
				return err
			}
			return ctx.Err()
		}
	}
}

// confirmedTurnID returns a native ID bound to this submission.
func (p *nativeTurn) confirmedTurnID() string {
	id, _ := p.currentTurn()
	return id
}

// attachTurnEvents binds accounting and event delivery to the admitted native identity.
func (a *Agent) attachTurnEvents(state *sessionState, prompt *nativeTurn, turnID string, updater sessionUpdater) {
	if a.delivery != nil && updater != nil {
		prompt.mu.Lock()
		prompt.delivery = &turnDelivery{
			owner: a.delivery, updater: updater, ctx: prompt.runCtx,
			stillOwned: func() bool {
				return prompt.runCtx.Err() == nil && a.sessions.isCurrent(state) && sessionActiveTurn(state) == prompt
			},
		}
		updater = prompt.delivery
		prompt.mu.Unlock()
	}
	router := newEventRouter(updater, turnGeneration{
		SessionID:  acp.SessionId(state.id),
		ThreadID:   state.id,
		TurnID:     turnID,
		Generation: prompt.generation,
	}, a, a.logger, state.terminalOutputMode)
	state.mu.Lock()
	if state.usageTotal != nil {
		baseline := *state.usageTotal
		router.handler.previous = &baseline
		router.handler.baselineKnown = true
	}
	state.mu.Unlock()
	router.handler.checkpoint = func(total *turnUsage) {
		state.mu.Lock()
		defer state.mu.Unlock()
		if state.activeTurn == prompt {
			state.usageTotal = total
		}
	}
	prompt.setEventRouter(router)
}

// waitDelivery waits for the ACP updates already mapped from this native turn.
func (p *nativeTurn) waitDelivery() error {
	p.eventsMu.Lock()
	err := p.eventFailure
	p.eventsMu.Unlock()
	p.mu.Lock()
	delivery := p.delivery
	p.mu.Unlock()
	if delivery != nil {
		err = errors.Join(err, delivery.wait(p.runCtx))
	}
	return err
}

// requestTurnInterrupt sends at most one interrupt for a proven native identity.
func (a *Agent) requestTurnInterrupt(
	state *sessionState,
	prompt *nativeTurn,
	turnID string,
	resolveInterrupted bool,
) {
	if resolveInterrupted {
		a.client.MarkTurnStale(state.id, turnID)
	}
	prompt.interruptOnce.Do(func() {
		go func() {
			defer close(prompt.interruptDone)
			if resolveInterrupted {
				defer a.client.ResolveTurnInterrupted(state.id, turnID)
			}
			if err := a.client.TurnInterrupt(a.runtimeCtx, state.id, turnID); err != nil {
				a.logger.Warn("Failed to interrupt Codex turn", "thread_id", state.id, "turn_id", turnID, "error", err)
			}
		}()
	})
}

// releaseTurn clears the lane only if the exact invocation still owns it.
func (a *Agent) releaseTurn(state *sessionState, prompt *nativeTurn) {
	state.mu.Lock()
	if state.activeTurn == prompt {
		state.activeTurn = nil
	}
	state.mu.Unlock()
}

// Cancel asks the native turn owner to interrupt the current ACP session.
func (a *Agent) Cancel(_ context.Context, notification acp.CancelNotification) error {
	state, ok := a.sessions.get(string(notification.SessionId))
	if !ok {
		return nil
	}
	state.mu.Lock()
	prompt := state.activeTurn
	state.mu.Unlock()
	if prompt == nil {
		return nil
	}
	turnID := prompt.requestCancel()
	if turnID != "" {
		a.requestTurnInterrupt(state, prompt, turnID, false)
	}
	return nil
}

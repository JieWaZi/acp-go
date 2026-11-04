package codex

import (
	"context"
	"errors"

	"github.com/JieWaZi/acp-go/pkg/codex/protocol"
)

const (
	maxEarlyTurnUpdates    = 32
	maxEarlyUsageSamples   = 32
	maxEarlyDroppedTurnIDs = 32
)

var errEarlyUpdateOwnershipUnknown = errors.New("could not establish Codex pre-acknowledgment delivery completeness")

// turnFrame retains the receive position needed to replay around a captured terminal.
type turnFrame struct {
	// position orders this frame relative to native terminal notifications.
	position uint64
	// turnID is the native identity extracted when the frame arrived.
	turnID string
	// notification retains the decoded frame. Nil marks a break in usage continuity.
	notification protocol.ServerNotification
}

// earlyTurn holds the unbound turn's ordered updates and accounting evidence.
type earlyTurn struct {
	// frames holds at most 64 full frames and 65 coalesced usage gaps.
	frames []turnFrame
	// updates counts retained frames that can contribute turn updates or terminal facts.
	updates int
	// usageSamples counts retained native samples whose ownership is still possible.
	usageSamples int
	// droppedUpdates keeps the first lost update position for up to 32 native IDs.
	droppedUpdates map[string]uint64
	// unattributedUpdateLoss is the first loss whose ID did not fit the bounded index.
	unattributedUpdateLoss uint64
}

// retain separates replaceable usage samples from candidate updates.
func (e *earlyTurn) retain(frame turnFrame, retired bool) {
	if _, usage := frame.notification.(*protocol.ThreadTokenUsageUpdatedEnvelope); usage {
		if retired || e.usageSamples == maxEarlyUsageSamples {
			e.noteUsageGap(frame.position)
			return
		}
		e.usageSamples++
		e.frames = append(e.frames, frame)
		return
	}
	if retired || !carriesTurnUpdate(frame.notification) {
		return
	}
	if e.updates < maxEarlyTurnUpdates {
		e.updates++
		e.frames = append(e.frames, frame)
		return
	}
	if _, recorded := e.droppedUpdates[frame.turnID]; recorded {
		return
	}
	if len(e.droppedUpdates) == maxEarlyDroppedTurnIDs {
		if e.unattributedUpdateLoss == 0 {
			e.unattributedUpdateLoss = frame.position
		}
		return
	}
	if e.droppedUpdates == nil {
		e.droppedUpdates = make(map[string]uint64)
	}
	e.droppedUpdates[frame.turnID] = frame.position
}

// noteUsageGap keeps the earliest accounting break between retained frames.
// An open gap already clears the next baseline, even if it straddles the terminal.
func (e *earlyTurn) noteUsageGap(position uint64) {
	if n := len(e.frames); n == 0 || e.frames[n-1].notification != nil {
		e.frames = append(e.frames, turnFrame{position: position})
	}
}

// deliveryFailure identifies known or unresolved update loss before the exact terminal.
func (e *earlyTurn) deliveryFailure(turnID string, terminal *receivedCompletion) error {
	beforeTerminal := func(position uint64) bool {
		return position != 0 && (terminal == nil || position < terminal.position)
	}
	if position, recorded := e.droppedUpdates[turnID]; recorded {
		if beforeTerminal(position) {
			return errEventDeliveryBackpressure
		}
		return nil
	}
	if beforeTerminal(e.unattributedUpdateLoss) {
		return errEarlyUpdateOwnershipUnknown
	}
	return nil
}

// carriesTurnUpdate recognizes frames that can change ACP output or terminal facts.
func carriesTurnUpdate(notification protocol.ServerNotification) bool {
	switch frame := notification.(type) {
	case *protocol.ItemStartedEnvelope:
		switch frame.Params.Item.Type {
		case protocol.AgentMessage:
			return frame.Params.Item.Phase != nil
		case protocol.CommandExecution, protocol.FileChange, protocol.MCPToolCall, protocol.WebSearch, protocol.ImageView:
			return true
		default:
			return false
		}
	case *protocol.ItemCompletedEnvelope:
		switch frame.Params.Item.Type {
		case protocol.AgentMessage, protocol.Reasoning, protocol.ThreadItemType("plan"),
			protocol.CommandExecution, protocol.FileChange, protocol.MCPToolCall, protocol.WebSearch, protocol.ImageView,
			protocol.DynamicToolCall, protocol.CollabAgentToolCall:
			return true
		default:
			return false
		}
	case *protocol.TurnDiffUpdatedEnvelope, *protocol.ThreadTokenUsageUpdatedEnvelope:
		return false
	default:
		_, _, scoped := notificationScope(notification)
		return scoped
	}
}

// handleNotification assigns native frames to the current thread owner and turn.
func (a *Agent) handleNotification(ctx context.Context, position uint64, stale bool, notification protocol.ServerNotification) {
	if changed, ok := notification.(*protocol.ThreadSettingsUpdatedEnvelope); ok {
		a.sessions.observeSettings(changed.Params.ThreadID, changed.Params.ThreadSettings)
		return
	}
	if status, ok := notification.(*protocol.MCPServerStatusUpdatedEnvelope); ok {
		a.handleMCPStartupStatus(ctx, status.Params)
		return
	}
	if resolved, ok := notification.(*protocol.ServerRequestResolvedEnvelope); ok {
		a.completePendingURLElicitations(ctx, resolved.Params.ThreadID)
	}
	threadID, turnID := notificationRouting(notification)
	if threadID == "" {
		if unknown, ok := notification.(*protocol.UnknownServerNotification); ok {
			threadID, turnID = unknownNotificationIdentity(unknown.Params)
		}
	}
	if threadID == "" {
		return
	}
	state, ok := a.sessions.get(threadID)
	if !ok || !a.sessions.isCurrent(state) {
		return
	}
	_, usage := notification.(*protocol.ThreadTokenUsageUpdatedEnvelope)
	state.mu.Lock()
	prompt := state.activeTurn
	if prompt == nil {
		if usage {
			state.usageTotal = nil
		}
		state.mu.Unlock()
		return
	}
	state.mu.Unlock()
	if turnID == "" && !usage {
		return
	}
	accountingOnly := stale || turnID == ""
	frame := turnFrame{position: position, turnID: turnID, notification: notification}
	prompt.eventsMu.Lock()
	defer prompt.eventsMu.Unlock()
	activeTurnID := prompt.confirmedTurnID()
	if activeTurnID != "" {
		if accountingOnly {
			frame.notification = nil
		}
		a.routeBoundTurnLocked(ctx, state, prompt, activeTurnID, frame)
		return
	}
	alreadyCompleted := a.client.completions.hasCompleted(threadID, turnID)
	if prompt.kind == compactionTurn {
		if _, started := notification.(*protocol.TurnStartedEnvelope); started && !alreadyCompleted {
			a.bindTurnLocked(state, prompt, turnID)
		}
		if !usage {
			return
		}
	}
	if prompt.clientMessageID != "" && notificationConfirmsUserMessage(notification, prompt.clientMessageID) {
		if alreadyCompleted && a.client.completions.capturedCompletion(prompt.completionCapture, turnID) == nil {
			return
		}
		a.bindTurnLocked(state, prompt, turnID)
		a.routeBoundTurnLocked(ctx, state, prompt, turnID, frame)
		return
	}
	// Proof and terminal candidates are handled separately from retained updates.
	prompt.early.retain(frame, accountingOnly || alreadyCompleted)
}

// notificationConfirmsUserMessage checks the client identity on native user-message items.
func notificationConfirmsUserMessage(notification protocol.ServerNotification, messageID string) bool {
	match := func(item protocol.ThreadItem) bool {
		return item.Type == protocol.UserMessage && item.ClientID != nil && *item.ClientID == messageID
	}
	switch event := notification.(type) {
	case *protocol.ItemStartedEnvelope:
		return match(event.Params.Item)
	case *protocol.ItemCompletedEnvelope:
		return match(event.Params.Item)
	case *protocol.TurnStartedEnvelope:
		for _, item := range event.Params.Turn.Items {
			if match(item) {
				return true
			}
		}
	case *protocol.TurnCompletedEnvelope:
		for _, item := range event.Params.Turn.Items {
			if match(item) {
				return true
			}
		}
	}
	return false
}

// routeBoundTurnLocked maps a proven turn's frames and handles interruptions of usage continuity.
func (a *Agent) routeBoundTurnLocked(ctx context.Context, state *sessionState, prompt *nativeTurn, activeID string, frame turnFrame) {
	notification := frame.notification
	router := prompt.currentEventRouter()
	if router == nil {
		return
	}
	usage, hasUsage := notification.(*protocol.ThreadTokenUsageUpdatedEnvelope)
	usageGap := notification == nil
	if prompt.terminal != nil {
		if hasUsage || usageGap {
			// The earlier terminal is already immutable. A late sample only affects
			// whether the next invocation can safely use the thread checkpoint.
			state.invalidateUsageCheckpoint()
		}
		return
	}
	if frame.turnID != activeID || usageGap {
		if hasUsage || usageGap {
			router.handler.breakUsageContinuity()
		}
		return
	}
	if terminal, ok := notification.(*protocol.TurnCompletedEnvelope); ok {
		prompt.recordTerminalLocked(receivedCompletion{notification: terminal.Params, position: frame.position})
		return
	}
	switch event := notification.(type) {
	case *protocol.AgentMessageDeltaEnvelope:
		prompt.observed.output = prompt.observed.output || event.Params.Delta != ""
	case *protocol.ItemCompletedEnvelope:
		prompt.observed.recordItem(event.Params.Item, true)
	case *protocol.ErrorEnvelope:
		native := event.Params
		prompt.observed.lastError = &native
	case *protocol.ModelReroutedEnvelope:
		native := event.Params
		if native.ToModel != "" && len(prompt.observed.reroutes) < 16 {
			prompt.observed.reroutes = append(prompt.observed.reroutes, modelReroute{
				From: native.FromModel, To: native.ToModel, Reason: string(native.Reason),
			})
		}
	}
	_, cancelled := prompt.currentTurn()
	if prompt.kind == compactionTurn || cancelled {
		if hasUsage {
			_ = router.handler.recordTokenUsage(usage.Params)
		}
		return
	}
	rawSize := 0
	if unknown, ok := notification.(*protocol.UnknownServerNotification); ok {
		rawSize = len(unknown.Params)
	}
	if err := router.Handle(ctx, notification, rawSize); err != nil {
		if !hasUsage && prompt.eventFailure == nil {
			prompt.eventFailure = err
		}
		a.logger.Warn("Failed to map Codex session event", "session_id", state.id, "method", notification.Method(), "error", err)
	}
}

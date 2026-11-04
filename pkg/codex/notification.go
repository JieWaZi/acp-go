package codex

import (
	"context"

	"github.com/JieWaZi/acp-go/pkg/codex/protocol"
)

const maxEarlyTurnEvents = 32

// turnFrame retains the receive position needed to replay around a captured terminal.
type turnFrame struct {
	// position orders this frame relative to native terminal notifications.
	position uint64
	// turnID is the native identity extracted when the frame arrived.
	turnID string
	// notification retains the decoded frame until native identity is proven.
	notification protocol.ServerNotification
}

// handleNotification assigns native frames to the current thread owner and turn.
func (a *Agent) handleNotification(ctx context.Context, position uint64, notification protocol.ServerNotification) {
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
	state.mu.Lock()
	prompt := state.activeTurn
	if prompt == nil {
		if _, usage := notification.(*protocol.ThreadTokenUsageUpdatedEnvelope); usage {
			state.usageTotal = nil
		}
		state.mu.Unlock()
		return
	}
	state.mu.Unlock()
	if turnID == "" {
		return
	}
	frame := turnFrame{position: position, turnID: turnID, notification: notification}
	prompt.eventsMu.Lock()
	defer prompt.eventsMu.Unlock()
	activeTurnID := prompt.confirmedTurnID()
	if activeTurnID != "" {
		a.routeBoundTurnLocked(ctx, state, prompt, activeTurnID, frame)
		return
	}
	alreadyCompleted := a.client.completions.hasCompleted(threadID, turnID)
	_, usage := notification.(*protocol.ThreadTokenUsageUpdatedEnvelope)
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
	// The tracker retains terminals separately. Retired display is redundant,
	// while late usage remains evidence about the shared thread counter.
	if _, terminal := notification.(*protocol.TurnCompletedEnvelope); terminal || alreadyCompleted && !usage {
		return
	}
	if len(prompt.earlyEvents) < maxEarlyTurnEvents {
		prompt.earlyEvents = append(prompt.earlyEvents, frame)
		return
	}
	if prompt.firstDroppedFrame == 0 {
		prompt.firstDroppedFrame = position
	}
	if usage {
		prompt.lastDroppedUsage = position
	}
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
	if prompt.terminal != nil {
		if hasUsage {
			// The earlier terminal is already immutable. A late sample only affects
			// whether the next invocation can safely use the thread checkpoint.
			state.invalidateUsageCheckpoint()
		}
		return
	}
	if frame.turnID != activeID {
		if hasUsage {
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

package codex

import (
	"context"

	"github.com/JieWaZi/acp-go/pkg/codex/protocol"
)

const maxEarlyTurnEvents = 32

// handleNotification assigns native frames to the current thread owner and turn.
func (a *Agent) handleNotification(ctx context.Context, notification protocol.ServerNotification) {
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
	prompt.eventsMu.Lock()
	defer prompt.eventsMu.Unlock()
	activeTurnID := prompt.confirmedTurnID()
	if activeTurnID != "" {
		a.routeBoundTurnLocked(ctx, state, prompt, activeTurnID, turnID, notification)
		return
	}
	alreadyCompleted := a.client.completions.hasCompleted(threadID, turnID)
	if prompt.kind == compactionTurn {
		if _, started := notification.(*protocol.TurnStartedEnvelope); started && !alreadyCompleted {
			a.bindTurnLocked(state, prompt, turnID)
		}
		return
	}
	if prompt.clientMessageID != "" && notificationConfirmsUserMessage(notification, prompt.clientMessageID) {
		if alreadyCompleted && (prompt.completionCapture == nil || a.client.completions.capturedCompletion(prompt.completionCapture, turnID) == nil) {
			return
		}
		a.bindTurnLocked(state, prompt, turnID)
		a.routeBoundTurnLocked(ctx, state, prompt, turnID, turnID, notification)
		return
	}
	// The protocol tracker retains unmatched terminals. Only display and
	// observation frames compete for this pre-identity buffer.
	if _, terminal := notification.(*protocol.TurnCompletedEnvelope); terminal || alreadyCompleted {
		return
	}
	if len(prompt.earlyEvents) < maxEarlyTurnEvents {
		prompt.earlyEvents = append(prompt.earlyEvents, notification)
	} else {
		prompt.earlyOverflow = true
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
func (a *Agent) routeBoundTurnLocked(ctx context.Context, state *sessionState, prompt *nativeTurn, activeID, eventID string, notification protocol.ServerNotification) {
	router := prompt.currentEventRouter()
	if router == nil {
		return
	}
	usage, hasUsage := notification.(*protocol.ThreadTokenUsageUpdatedEnvelope)
	if prompt.terminal != nil {
		if hasUsage {
			// The earlier terminal is already immutable. A late sample only affects
			// whether the next invocation can safely use the thread checkpoint.
			state.mu.Lock()
			if state.activeTurn == prompt {
				state.usageTotal = nil
			}
			state.mu.Unlock()
		}
		return
	}
	if eventID != activeID {
		if hasUsage {
			router.handler.breakUsageContinuity()
		}
		return
	}
	if terminal, ok := notification.(*protocol.TurnCompletedEnvelope); ok {
		prompt.recordTerminalLocked(terminal.Params)
		return
	}
	switch event := notification.(type) {
	case *protocol.AgentMessageDeltaEnvelope:
		prompt.observed.output = prompt.observed.output || event.Params.Delta != ""
	case *protocol.ItemCompletedEnvelope:
		prompt.observed.recordCompletedItem(event.Params.Item)
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

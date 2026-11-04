package codex

import (
	"context"

	"github.com/JieWaZi/acp-go/pkg/codex/protocol"
)

const maxEarlyTurnEvents = 32

// handleNotification assigns native frames to the current thread owner and turn.
func (a *Agent) handleNotification(ctx context.Context, notification protocol.ServerNotification) {
	if changed, ok := notification.(*protocol.ThreadSettingsUpdatedEnvelope); ok {
		native := changed.Params.ThreadSettings
		_ = a.sessions.withCurrent(changed.Params.ThreadID, func(state *sessionState) error {
			if state.configuration != nil {
				state.configuration.observe("thread_settings_updated", native.Model, native.Effort, native.ServiceTier)
			}
			return nil
		})
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
	if a.client.completions.hasCompleted(threadID, turnID) {
		return
	}
	if prompt.kind == compactionTurn {
		if _, started := notification.(*protocol.TurnStartedEnvelope); started {
			a.bindTurnLocked(state, prompt, turnID)
		}
		return
	}
	if len(prompt.earlyEvents) < maxEarlyTurnEvents {
		prompt.earlyEvents = append(prompt.earlyEvents, notification)
	} else {
		prompt.earlyOverflow = true
	}
	if prompt.clientMessageID != "" && notificationConfirmsUserMessage(notification, prompt.clientMessageID) {
		a.bindTurnLocked(state, prompt, turnID)
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
	if prompt.nativeTerminal {
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
	if _, terminal := notification.(*protocol.TurnCompletedEnvelope); terminal {
		prompt.nativeTerminal = true
		close(prompt.terminalSignal)
		return
	}
	switch event := notification.(type) {
	case *protocol.AgentMessageDeltaEnvelope:
		prompt.observed.output = prompt.observed.output || event.Params.Delta != ""
	case *protocol.ItemCompletedEnvelope:
		item := event.Params.Item
		switch item.Type {
		case protocol.AgentMessage:
			prompt.observed.output = prompt.observed.output || stringValue(item.Text) != ""
		case protocol.FileChange:
			prompt.observed.fileChange = prompt.observed.fileChange || stringValue(item.Status) == "completed" && len(item.Changes) > 0
		}
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

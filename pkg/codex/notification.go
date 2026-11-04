package codex

import (
	"context"

	"github.com/JieWaZi/acp-go/pkg/codex/protocol"
)

// handleNotification routes native frames to the current turn identity and generation.
func (a *Agent) handleNotification(ctx context.Context, notification protocol.ServerNotification) {
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
	prompt := state.activePrompt
	if prompt == nil {
		// An unowned sample may include consumption outside ACP. A later Prompt
		// must start from its own first request instead of charging that delta.
		if _, usage := notification.(*protocol.ThreadTokenUsageUpdatedEnvelope); usage {
			state.usageTotal = nil
		}
		state.mu.Unlock()
		return
	}
	state.mu.Unlock()
	if prompt.compact {
		if started, ok := notification.(*protocol.TurnStartedEnvelope); ok && turnID != "" &&
			!a.client.completions.hasCompleted(threadID, turnID) {
			if claimed, late := prompt.claimCompactTurn(started.Params.Turn.ID); claimed {
				a.attachTurnEvents(state, prompt, turnID, nil)
				prompt.signalTurnStarted()
				if late {
					a.requestPromptInterrupt(state, prompt, turnID, false)
				}
			}
		}
	}
	activeTurnID, cancelled := prompt.currentTurn()
	if activeTurnID == "" || activeTurnID != turnID {
		return
	}
	router := prompt.currentEventRouter()
	if router == nil {
		return
	}
	if prompt.compact || cancelled {
		// A cancelled or standalone turn still owns the consumption reported for
		// its exact native ID. Keep it for the terminal result without streaming UI updates.
		if usage, ok := notification.(*protocol.ThreadTokenUsageUpdatedEnvelope); ok {
			if err := router.handler.recordTokenUsage(usage.Params); err != nil {
				a.logger.Warn("Invalid Codex turn usage", "session_id", threadID, "error", err)
			}
		}
		return
	}
	rawSize := 0
	if unknown, ok := notification.(*protocol.UnknownServerNotification); ok {
		rawSize = len(unknown.Params)
	}
	if err := router.Handle(ctx, notification, rawSize); err != nil {
		a.logger.Warn("Failed to map Codex session event", "session_id", threadID, "method", notification.Method(), "error", err)
	}
}

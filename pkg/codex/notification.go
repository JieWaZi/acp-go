package codex

import (
	"context"

	"github.com/JieWaZi/acp-go/pkg/codex/protocol"
)

// handleNotification 以 runtime 当前 turn generation 选择已验证 eventRouter，避免复制事件 mapper。
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
	state.mu.Unlock()
	if prompt == nil {
		return
	}
	activeTurnID, cancelled := prompt.currentTurn()
	if cancelled || activeTurnID == "" || activeTurnID != turnID {
		return
	}
	router := prompt.currentEventRouter()
	if router == nil {
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

package codex

import (
	"context"

	"acp-go/agents/codex/protocol"
	acp "github.com/coder/acp-go-sdk"
)

// handleNotification 是 runtime 子变更预留给完整 events mapper 的薄路由。
// 当前只转发主链路可验证的 agent message/reasoning 文本；tool/approval/config 由兄弟子变更实现。
func (a *Agent) handleNotification(ctx context.Context, notification protocol.ServerNotification) {
	threadID, turnID := notificationRouting(notification)
	if threadID == "" || !a.notificationMatchesActiveTurn(threadID, turnID) {
		return
	}

	var update acp.SessionUpdate
	switch value := notification.(type) {
	case *protocol.AgentMessageDeltaEnvelope:
		update.AgentMessageChunk = &acp.SessionUpdateAgentMessageChunk{
			SessionUpdate: "agent_message_chunk",
			Content:       textContentBlock(value.Params.Delta),
		}
	case *protocol.ReasoningSummaryTextDeltaEnvelope:
		update.AgentThoughtChunk = &acp.SessionUpdateAgentThoughtChunk{
			SessionUpdate: "agent_thought_chunk",
			Content:       textContentBlock(value.Params.Delta),
		}
	case *protocol.ReasoningTextDeltaEnvelope:
		update.AgentThoughtChunk = &acp.SessionUpdateAgentThoughtChunk{
			SessionUpdate: "agent_thought_chunk",
			Content:       textContentBlock(value.Params.Delta),
		}
	default:
		return
	}

	connection, err := a.waitConnection(ctx)
	if err != nil || connection == nil {
		return
	}
	if err = connection.SessionUpdate(ctx, acp.SessionNotification{
		SessionId: acp.SessionId(threadID),
		Update:    update,
	}); err != nil {
		a.logger.Warn("发送 ACP session update 失败", "session_id", threadID, "error", err)
	}
}

// notificationMatchesActiveTurn 验证 thread/turn/generation 三重身份，隔离 stale 与跨 session 事件。
func (a *Agent) notificationMatchesActiveTurn(threadID, turnID string) bool {
	state, ok := a.sessions.get(threadID)
	if !ok || !a.sessions.isCurrent(state) {
		return false
	}
	state.mu.Lock()
	prompt := state.activePrompt
	state.mu.Unlock()
	if prompt == nil {
		return false
	}
	activeTurnID, _ := prompt.currentTurn()
	return activeTurnID != "" && activeTurnID == turnID
}

// textContentBlock 创建 SDK discriminator-first 的文本 ContentBlock。
func textContentBlock(text string) acp.ContentBlock {
	return acp.ContentBlock{Text: &acp.ContentBlockText{Type: "text", Text: text}}
}

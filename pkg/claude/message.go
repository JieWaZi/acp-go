package claude

import (
	"context"
	"strconv"

	"github.com/JieWaZi/acp-go/pkg/acpmeta"
	acp "github.com/JieWaZi/acp-go/sdk"
)

// streamedMessage 保存当前原生消息的分组身份与边界，不保存累计正文。
type streamedMessage struct {
	// id 优先使用 Anthropic 消息身份，缺失时由当前回合分配。
	id string
	// complete 阻止 stream 与聚合 assistant 帧重复交付完成边界。
	complete bool
	// streaming 表示已接收 message_start，只能由 message_stop 确认完整消息结束。
	streaming bool
}

// messageIdentity 为同一父工具槽中的消息提供稳定身份，wire envelope UUID 不参与分组。
func (s *claudeSession) messageIdentity(turn *claudeTurn, parent *string, nativeID string, start bool) string {
	turn.mu.Lock()
	defer turn.mu.Unlock()
	if turn.streamMessages == nil {
		turn.streamMessages = make(map[string]*streamedMessage)
	}
	owner := streamedBlockOwner(parent)
	state := turn.streamMessages[owner]
	if start || state == nil || nativeID != "" && state.id != nativeID {
		id := nativeID
		if id == "" {
			turn.messageSequence++
			id = "acp-go/claude/" + turn.id + "/" + strconv.FormatUint(turn.messageSequence, 10)
		}
		state = &streamedMessage{id: id, streaming: start}
		turn.streamMessages[owner] = state
	}
	return state.id
}

// messageInfo 区分顶层答复与子代理过程消息；消息边界不会推断最终答案。
func messageInfo(parent *string, complete bool) acpmeta.MessageInfo {
	phase := acpmeta.MessagePhaseUnknown
	if parent != nil {
		phase = acpmeta.MessagePhaseCommentary
	}
	return acpmeta.MessageInfo{Phase: phase, Complete: complete, Source: acpmeta.MessageSourceNativeMessage}
}

// completeMessage 发布原生消息完成；只有发送成功后才记账，允许失败重试。
func (s *claudeSession) completeMessage(ctx context.Context, turn *claudeTurn, parent *string) error {
	turn.mu.Lock()
	state := turn.streamMessages[streamedBlockOwner(parent)]
	if state == nil || state.complete {
		turn.mu.Unlock()
		return nil
	}
	id := state.id
	turn.mu.Unlock()
	update := acp.UpdateAgentMessageText("")
	update.AgentMessageChunk.MessageId = &id
	update.AgentMessageChunk.Meta = acpmeta.WithMessageInfo(nil, messageInfo(parent, true))
	if err := s.agent.sendUpdate(ctx, s.id, update); err != nil {
		return err
	}
	turn.mu.Lock()
	state.complete = true
	turn.mu.Unlock()
	return nil
}

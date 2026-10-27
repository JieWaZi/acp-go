package nativeacp

import (
	"strconv"

	"github.com/JieWaZi/acp-go/pkg/acpmeta"
	acp "github.com/JieWaZi/acp-go/sdk"
)

// normalizeMessageLocked 只保存一个当前消息段，不累计正文，也不从 Prompt 返回值制造最终阶段。
func (agent *Agent) normalizeMessageLocked(request *acp.SessionNotification) {
	state := agent.sessions[request.SessionId]
	if state == nil {
		return
	}
	chunk := request.Update.AgentMessageChunk
	if chunk == nil {
		if request.Update.ToolCall != nil || request.Update.ToolCallUpdate != nil || request.Update.AgentThoughtChunk != nil || request.Update.UserMessageChunk != nil || request.Update.Plan != nil {
			state.messageID = ""
		}
		return
	}
	info, known := acpmeta.ReadMessageInfo(chunk.Meta)
	if chunk.MessageId != nil && *chunk.MessageId != "" {
		if state.messageID != *chunk.MessageId {
			state.messageInfo = acpmeta.MessageInfo{Phase: acpmeta.MessagePhaseUnknown, Source: acpmeta.MessageSourceNativeMessage}
		}
		state.messageID = *chunk.MessageId
	} else {
		if state.messageID == "" {
			agent.messageSequence++
			state.messageID = "acp-go/segment/" + strconv.FormatUint(agent.messageSequence, 10)
			state.messageInfo = acpmeta.MessageInfo{Phase: acpmeta.MessagePhaseUnknown, Source: acpmeta.MessageSourceAdapterSegment}
		}
		id := state.messageID
		chunk.MessageId = &id
	}
	if state.messageKnownID == *chunk.MessageId && (!known || info.Phase == acpmeta.MessagePhaseUnknown) {
		info.Phase = state.messageKnownInfo.Phase
		info.Source = state.messageKnownInfo.Source
		if !known {
			info.Complete = false
		}
		known = true
		chunk.Meta = acpmeta.WithMessageInfo(chunk.Meta, info)
	}
	if known {
		state.messageInfo = info
		if info.Phase != acpmeta.MessagePhaseUnknown {
			state.messageKnownID = *chunk.MessageId
			state.messageKnownInfo = info
		}
	} else {
		chunk.Meta = acpmeta.WithMessageInfo(chunk.Meta, state.messageInfo)
	}
	if known && info.Complete {
		state.messageID = ""
	}
}

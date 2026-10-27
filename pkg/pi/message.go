package pi

import (
	"strconv"

	"github.com/JieWaZi/acp-go/pkg/acpmeta"
)

// beginMessage 为 Pi 原生消息生命周期分配稳定身份；本地身份只在当前会话内有意义。
func (s *session) beginMessage(nativeID string) string {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	return s.beginMessageLocked(nativeID)
}

// beginMessageLocked 在会话锁内初始化消息身份，供正文与原生开始事件共用。
func (s *session) beginMessageLocked(nativeID string) string {
	if nativeID == "" {
		s.messageSequence++
		nativeID = "acp-go/pi/" + string(s.id) + "/" + strconv.FormatUint(s.messageSequence, 10)
	}
	s.messageID = nativeID
	return nativeID
}

// messageUpdate 发布消息事实；完成只代表 message_end，不代表 agent_settled。
func (s *session) messageUpdate(content map[string]any, complete bool) map[string]any {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	if s.messageID == "" {
		s.beginMessageLocked("")
	}
	update := map[string]any{"sessionUpdate": "agent_message_chunk", "messageId": s.messageID, "content": content, "_meta": acpmeta.WithMessageInfo(nil, acpmeta.MessageInfo{Phase: acpmeta.MessagePhaseUnknown, Complete: complete, Source: acpmeta.MessageSourceNativeMessage})}
	if complete {
		s.messageID = ""
	}
	return update
}

// resetMessage 在工具与整轮边界清空当前身份，迟到文本不能合并进上一条消息。
func (s *session) resetMessage() {
	s.mutex.Lock()
	s.messageID = ""
	s.mutex.Unlock()
}

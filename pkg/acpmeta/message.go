package acpmeta

const (
	// MessagePhaseUnknown 表示原生消息没有声明可验证的答复阶段。
	MessagePhaseUnknown = "unknown"
	// MessagePhaseCommentary 表示过程说明或局部答复，不作为最终答案。
	MessagePhaseCommentary = "commentary"
	// MessagePhaseFinal 表示原生协议明确证明的最终答复。
	MessagePhaseFinal = "final"
	// MessageSourceNativePhase 表示阶段来自原生协议的明确声明。
	MessageSourceNativePhase = "native_phase"
	// MessageSourceNativeMessage 表示身份或完成边界来自原生消息生命周期。
	MessageSourceNativeMessage = "native_message"
	// MessageSourceAdapterSegment 表示适配器为无身份的连续文本分配了本地消息段。
	MessageSourceAdapterSegment = "adapter_segment"
	// messageInfoKey 是公开消息事实的独立元数据命名空间。
	messageInfoKey = "acp-go/message"
)

// MessageInfo 描述消息阶段与消息完成，消息完成本身不表示整个 Prompt 成功。
type MessageInfo struct {
	// Phase 是已知阶段；最终阶段必须由原生阶段证据支持。
	Phase string
	// Complete 表示当前原生消息已经结束，不表示工具或整轮执行已经结束。
	Complete bool
	// Source 区分原生阶段、原生消息与本地消息段的证据来源。
	Source string
}

// WithMessageInfo 克隆元数据后写入消息事实，保留所有厂商的并行证据。
func WithMessageInfo(meta map[string]any, info MessageInfo) map[string]any {
	result := make(map[string]any, len(meta)+1)
	for key, value := range meta {
		result[key] = value
	}
	result[messageInfoKey] = map[string]any{"phase": info.Phase, "complete": info.Complete, "source": info.Source}
	return result
}

// ReadMessageInfo 只接受完整且有明确来源的公开事实，不把消息完成猜测成最终阶段。
func ReadMessageInfo(meta map[string]any) (MessageInfo, bool) {
	value, ok := meta[messageInfoKey].(map[string]any)
	if !ok {
		return MessageInfo{}, false
	}
	phase, phaseOK := value["phase"].(string)
	complete, completeOK := value["complete"].(bool)
	source, sourceOK := value["source"].(string)
	validPhase := phase == MessagePhaseUnknown || phase == MessagePhaseCommentary || phase == MessagePhaseFinal
	validSource := source == MessageSourceNativePhase || source == MessageSourceNativeMessage || source == MessageSourceAdapterSegment
	if !phaseOK || !completeOK || !sourceOK || !validPhase || !validSource || phase == MessagePhaseFinal && source != MessageSourceNativePhase {
		return MessageInfo{}, false
	}
	return MessageInfo{Phase: phase, Complete: complete, Source: source}, true
}

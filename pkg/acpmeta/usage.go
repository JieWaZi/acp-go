package acpmeta

import (
	"encoding/json"
	"math"
)

const turnUsageKey = "acp-go/turn-usage"

// TurnUsage 是当前 Prompt 的累计消费快照；可空字段区分未知与已报告零。
type TurnUsage struct {
	// InputTokens 是不含缓存读写的输入 token 数。
	InputTokens *int64 `json:"inputTokens,omitempty"`
	// CacheReadTokens 是缓存读取 token 数。
	CacheReadTokens *int64 `json:"cacheReadTokens,omitempty"`
	// CacheWriteTokens 是缓存写入 token 数。
	CacheWriteTokens *int64 `json:"cacheWriteTokens,omitempty"`
	// OutputTokens 是输出 token 数，包含已单列的推理子集。
	OutputTokens *int64 `json:"outputTokens,omitempty"`
	// ReasoningTokens 是输出中的推理 token 子集。
	ReasoningTokens *int64 `json:"reasoningTokens,omitempty"`
	// Source 是原生计数来源。
	Source string `json:"source"`
	// Coverage 声明原生报告覆盖的调用范围，例如 main_loop 或 native_turn。
	Coverage string `json:"coverage,omitempty"`
	// Complete 表示快照覆盖产品整轮消费，不代表所有分量均已报告。
	Complete bool `json:"complete"`
	// ContextKnown 表示同一通知的 Used 和 Size 有真实上下文证据。
	ContextKnown bool `json:"contextKnown"`
	// Model 仅在原生明确报告时填写。
	Model string `json:"model,omitempty"`
}

// WithTurnUsage 克隆元数据并写入可替换快照，不修改调用方的元数据。
func WithTurnUsage(meta map[string]any, usage TurnUsage) map[string]any {
	result := make(map[string]any, len(meta)+1)
	for key, value := range meta {
		result[key] = value
	}
	value := map[string]any{"source": usage.Source, "complete": usage.Complete, "contextKnown": usage.ContextKnown}
	fields := map[string]*int64{"inputTokens": usage.InputTokens, "cacheReadTokens": usage.CacheReadTokens, "cacheWriteTokens": usage.CacheWriteTokens, "outputTokens": usage.OutputTokens, "reasoningTokens": usage.ReasoningTokens}
	for key, counter := range fields {
		if counter != nil {
			value[key] = *counter
		}
	}
	if usage.Coverage != "" {
		value["coverage"] = usage.Coverage
	}
	if usage.Model != "" {
		value["model"] = usage.Model
	}
	result[turnUsageKey] = value
	return result
}

// ReadTurnUsage 读取直接或 JSON 往返的快照；拒绝负数、分数与不安全浮点整数。
func ReadTurnUsage(meta map[string]any) (TurnUsage, bool) {
	value, ok := meta[turnUsageKey].(map[string]any)
	if !ok {
		return TurnUsage{}, false
	}
	source, sourceOK := value["source"].(string)
	complete, completeOK := value["complete"].(bool)
	contextKnown, contextOK := value["contextKnown"].(bool)
	if !sourceOK || source == "" || !completeOK || !contextOK {
		return TurnUsage{}, false
	}
	result := TurnUsage{Source: source, Complete: complete, ContextKnown: contextKnown}
	targets := map[string]**int64{"inputTokens": &result.InputTokens, "cacheReadTokens": &result.CacheReadTokens, "cacheWriteTokens": &result.CacheWriteTokens, "outputTokens": &result.OutputTokens, "reasoningTokens": &result.ReasoningTokens}
	for key, target := range targets {
		if raw, exists := value[key]; exists && raw != nil {
			counter, valid := usageInteger(raw)
			if !valid {
				return TurnUsage{}, false
			}
			*target = &counter
		}
	}
	if coverage, exists := value["coverage"]; exists {
		var valid bool
		result.Coverage, valid = coverage.(string)
		if !valid {
			return TurnUsage{}, false
		}
	}
	if model, exists := value["model"]; exists {
		var valid bool
		result.Model, valid = model.(string)
		if !valid {
			return TurnUsage{}, false
		}
	}
	return result, true
}

// usageInteger 校验非负整数并拒绝 JSON 浮点精度不可靠的计数。
func usageInteger(value any) (int64, bool) {
	var result int64
	switch number := value.(type) {
	case int64:
		result = number
	case int:
		result = int64(number)
	case json.Number:
		var err error
		result, err = number.Int64()
		if err != nil {
			return 0, false
		}
	case float64:
		if number < 0 || number > 1<<53 || math.Trunc(number) != number {
			return 0, false
		}
		result = int64(number)
	default:
		return 0, false
	}
	return result, result >= 0
}

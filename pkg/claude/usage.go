package claude

import (
	"context"
	"math"
	"strings"
	"time"
	"unicode"

	"github.com/JieWaZi/acp-go/pkg/acpmeta"
	"github.com/JieWaZi/acp-go/pkg/claude/protocol"
	acp "github.com/JieWaZi/acp-go/sdk"
)

const (
	// defaultClaudeContextWindow 是尚无权威模型证据时的默认窗口。
	defaultClaudeContextWindow int64 = 200_000
	// extendedClaudeContextWindow 是模型标识明确声明 1m lane 时的推断窗口。
	extendedClaudeContextWindow int64 = 1_000_000
)

// recordContextUsage 用 assistant 的完整累计快照替换当前上下文用量。
func recordContextUsage(turn *claudeTurn, usage protocol.Usage, model string) {
	turn.mu.Lock()
	if !validClaudeContextUsage(usage) {
		turn.mu.Unlock()
		return
	}
	turn.contextUsage = usage
	turn.contextUsageSet = usage.KnownFields == nil || len(usage.KnownFields) == 4
	if model != "" {
		turn.assistantModel = model
	}
	turn.mu.Unlock()
}

// recordContextUsageDelta 合并 Anthropic message_delta 的可空累计字段。
func recordContextUsageDelta(turn *claudeTurn, delta protocol.UsageDelta) {
	for _, counter := range []*int64{&delta.OutputTokens, delta.InputTokens, delta.CacheCreationInputTokens, delta.CacheReadInputTokens} {
		if counter != nil && *counter < 0 {
			return
		}
	}

	turn.mu.Lock()
	usage := turn.contextUsage
	if usage.KnownFields != nil {
		fields := make(map[string]bool, len(usage.KnownFields)+4)
		for key, value := range usage.KnownFields {
			fields[key] = value
		}
		usage.KnownFields = fields
	}
	mark := func(key string) {
		if usage.KnownFields != nil {
			usage.KnownFields[key] = true
		}
	}
	mark("output_tokens")
	if delta.InputTokens != nil {
		usage.InputTokens = nonNegativeToken(*delta.InputTokens)
		mark("input_tokens")
	}
	usage.OutputTokens = nonNegativeToken(delta.OutputTokens)
	if delta.CacheCreationInputTokens != nil {
		usage.CacheCreationInputTokens = nonNegativeToken(*delta.CacheCreationInputTokens)
		mark("cache_creation_input_tokens")
	}
	if delta.CacheReadInputTokens != nil {
		usage.CacheReadInputTokens = nonNegativeToken(*delta.CacheReadInputTokens)
		mark("cache_read_input_tokens")
	}
	turn.contextUsage = usage
	turn.contextUsageSet = validClaudeContextUsage(usage) && (usage.KnownFields == nil || len(usage.KnownFields) == 4)
	turn.mu.Unlock()
}

// sendUsageUpdate 发布当前顶层 assistant 的上下文快照，而不是 result 的 Turn 累计量。
func (s *claudeSession) sendUsageUpdate(
	ctx context.Context,
	turn *claudeTurn,
	force bool,
) error {
	turn.mu.Lock()
	reported, hasUsage := claudeTurnUsageLocked(turn)
	used := int64(0)
	contextKnown := turn.contextUsageSet
	if contextKnown {
		used = totalClaudeUsage(turn.contextUsage)
	}
	turn.mu.Unlock()
	if !hasUsage && !contextKnown {
		return nil
	}
	s.mu.Lock()
	window := s.contextWindowSize
	s.mu.Unlock()
	contextKnown = contextKnown && window > 0
	reported.ContextKnown = contextKnown
	if !contextKnown {
		used = 0
		window = 0
	}
	// 同样的上下文大小可能对应消费累计的变化，不能只按 Used 去重。
	_ = force
	meta := map[string]any(nil)
	if hasUsage {
		meta = acpmeta.WithTurnUsage(nil, reported)
	}

	notifyCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	if err := s.agent.sendUpdate(notifyCtx, s.id, acp.SessionUpdate{
		UsageUpdate: &acp.SessionUsageUpdate{
			Meta: meta,
			Used: boundedInt(used),
			Size: boundedInt(window),
		},
	}); err != nil {
		return err
	}

	turn.mu.Lock()
	turn.contextUsagePublished = used
	turn.contextUsagePublishedSet = true
	turn.mu.Unlock()
	return nil
}

// useAssistantModel 让实际 assistant 模型修正初始化别名推断的窗口。
func (s *claudeSession) useAssistantModel(model string) {
	if model == "" {
		return
	}
	s.mu.Lock()
	if s.contextWindowModel != model {
		s.seedContextWindowLocked(model)
	}
	s.mu.Unlock()
}

// updateContextWindowFromResult 使用与当前 assistant 最匹配的 modelUsage 权威窗口。
func (s *claudeSession) updateContextWindowFromResult(
	turn *claudeTurn,
	models map[string]protocol.ModelUsage,
) {
	turn.mu.Lock()
	model := turn.assistantModel
	turn.mu.Unlock()
	key, usage, ok := matchingClaudeModelUsage(models, model)
	if !ok || usage.ContextWindow <= 0 {
		return
	}

	s.mu.Lock()
	s.contextWindowSize = usage.ContextWindow
	s.contextWindowAuthoritative = true
	s.contextWindowModel = model
	s.mu.Unlock()
	s.agent.cacheContextWindow(usage.ContextWindow, key, model)
}

// seedContextWindowLocked 从当前 Agent 的权威缓存、模型文本或默认值建立窗口。
// 调用方必须持有 Session mu。
func (s *claudeSession) seedContextWindowLocked(model string) {
	keys, texts := contextWindowEvidence(model, s.configuration.models)
	if window, ok := s.agent.cachedContextWindow(keys...); ok {
		s.contextWindowSize = window
		s.contextWindowAuthoritative = true
		s.contextWindowModel = model
		return
	}
	window := inferClaudeContextWindow(texts...)
	s.contextWindowSize = window
	s.contextWindowAuthoritative = false
	s.contextWindowModel = model
}

// cachedContextWindow 返回第一个已经由 result.modelUsage 确认的模型窗口。
func (a *Agent) cachedContextWindow(keys ...string) (int64, bool) {
	a.contextWindowsMu.RLock()
	defer a.contextWindowsMu.RUnlock()
	for _, key := range keys {
		if key == "" {
			continue
		}
		if window := a.contextWindows[key]; window > 0 {
			return window, true
		}
	}
	return 0, false
}

// cacheContextWindow 保存同一模型的 resolved 与消息标识，供后续 Session 直接复用。
func (a *Agent) cacheContextWindow(window int64, keys ...string) {
	if window <= 0 {
		return
	}
	a.contextWindowsMu.Lock()
	if a.contextWindows == nil {
		a.contextWindows = make(map[string]int64)
	}
	for _, key := range keys {
		if key != "" {
			a.contextWindows[key] = window
		}
	}
	a.contextWindowsMu.Unlock()
}

// contextWindowEvidence 返回缓存键和文本推断使用的模型证据。
func contextWindowEvidence(model string, models []protocol.ModelInfo) ([]string, []string) {
	keys := []string{model}
	texts := []string{model}
	for _, info := range models {
		if info.Value != model && info.ResolvedModel != model {
			continue
		}
		keys = append(keys, info.ResolvedModel, info.Value)
		texts = append(texts, info.ResolvedModel, info.DisplayName, info.Description)
		break
	}
	return keys, texts
}

// inferClaudeContextWindow 识别独立 1m token，否则采用默认窗口。
func inferClaudeContextWindow(texts ...string) int64 {
	for _, text := range texts {
		tokens := strings.FieldsFunc(strings.ToLower(text), func(value rune) bool {
			return !unicode.IsLetter(value) && !unicode.IsDigit(value)
		})
		for _, token := range tokens {
			if token == "1m" {
				return extendedClaudeContextWindow
			}
		}
	}
	return defaultClaudeContextWindow
}

// matchingClaudeModelUsage 按的最长公共前缀选择 assistant 对应模型。
func matchingClaudeModelUsage(
	models map[string]protocol.ModelUsage,
	model string,
) (string, protocol.ModelUsage, bool) {
	bestKey := ""
	bestLength := 0
	for key := range models {
		length := commonPrefixLength(key, model)
		if length > bestLength {
			bestKey = key
			bestLength = length
		}
	}
	if bestKey == "" {
		return "", protocol.ModelUsage{}, false
	}
	return bestKey, models[bestKey], true
}

// commonPrefixLength 返回两个模型标识从开头连续相同的字节数。
func commonPrefixLength(left, right string) int {
	limit := min(len(left), len(right))
	for index := range limit {
		if left[index] != right[index] {
			return index
		}
	}
	return limit
}

// sanitizeClaudeUsage 把第三方 backend 的负 token 防御性收紧为零。
func sanitizeClaudeUsage(usage protocol.Usage) protocol.Usage {
	usage.InputTokens = nonNegativeToken(usage.InputTokens)
	usage.OutputTokens = nonNegativeToken(usage.OutputTokens)
	usage.CacheCreationInputTokens = nonNegativeToken(usage.CacheCreationInputTokens)
	usage.CacheReadInputTokens = nonNegativeToken(usage.CacheReadInputTokens)
	return usage
}

// nonNegativeToken 把无效负 token 收紧为零。
func nonNegativeToken(value int64) int64 {
	if value < 0 {
		return 0
	}
	return value
}

// totalClaudeUsage 对四类 token 做饱和求和，避免异常上游值溢出。
func totalClaudeUsage(usage protocol.Usage) int64 {
	usage = sanitizeClaudeUsage(usage)
	values := [...]int64{
		usage.InputTokens,
		usage.OutputTokens,
		usage.CacheCreationInputTokens,
		usage.CacheReadInputTokens,
	}
	var total int64
	for _, value := range values {
		if value > math.MaxInt64-total {
			return math.MaxInt64
		}
		total += value
	}
	return total
}

// reportedClaudeUsage 保留上游字段 presence，非法分量不投影成可信零。
func reportedClaudeUsage(usage protocol.Usage) (acpmeta.TurnUsage, bool) {
	result := acpmeta.TurnUsage{Source: "claude.usage", Coverage: "main_loop"}
	fields := []struct {
		// key 是原生字段名。
		key string
		// value 是原生 token 值。
		value int64
		// target 是归一化字段地址。
		target **int64
	}{{"input_tokens", usage.InputTokens, &result.InputTokens}, {"output_tokens", usage.OutputTokens, &result.OutputTokens}, {"cache_creation_input_tokens", usage.CacheCreationInputTokens, &result.CacheWriteTokens}, {"cache_read_input_tokens", usage.CacheReadInputTokens, &result.CacheReadTokens}}
	known := false
	for _, field := range fields {
		present := usage.KnownFields[field.key]
		if usage.KnownFields == nil {
			present = field.value != 0
		}
		if present && field.value >= 0 {
			value := field.value
			*field.target = &value
			known = true
		}
	}
	return result, known
}

// recordClaudeMessageUsage 替换同一原生消息的累计请求用量；无身份时只保留一个部分槽位。
func recordClaudeMessageUsage(turn *claudeTurn, usage protocol.Usage, id string) {
	reported, known := reportedClaudeUsage(usage)
	if !known {
		return
	}
	turn.mu.Lock()
	defer turn.mu.Unlock()
	if id == "" {
		id = turn.usageMessageID
	}
	if turn.usageMessages == nil {
		turn.usageMessages = make(map[string]acpmeta.TurnUsage)
	}
	turn.usageMessages[id] = reported
}

// claudeTurnUsageLocked 以最终回执为准；此前按消息身份累计可证实的部分消费。
// 调用方持有 turn.mu。
func claudeTurnUsageLocked(turn *claudeTurn) (acpmeta.TurnUsage, bool) {
	if turn.reportedUsage != nil {
		return *turn.reportedUsage, true
	}
	result := acpmeta.TurnUsage{Source: "claude.assistant.usage", Coverage: "main_loop", Model: turn.assistantModel}
	if len(turn.usageMessages) == 0 {
		return result, false
	}
	first := true
	for _, usage := range turn.usageMessages {
		targets := []struct {
			// target 指向累计字段。
			target **int64
			// value 是该消息的可选计数。
			value *int64
		}{{&result.InputTokens, usage.InputTokens}, {&result.OutputTokens, usage.OutputTokens}, {&result.CacheReadTokens, usage.CacheReadTokens}, {&result.CacheWriteTokens, usage.CacheWriteTokens}}
		for _, field := range targets {
			if first {
				if field.value != nil {
					value := *field.value
					*field.target = &value
				}
			} else if *field.target != nil {
				if field.value == nil || *field.value > math.MaxInt64-**field.target {
					*field.target = nil
				} else {
					value := **field.target + *field.value
					*field.target = &value
				}
			}
		}
		first = false
	}
	return result, true
}

// claudePromptUsage 兼容标准 ACP 数值字段，同时用元数据保留未知字段。
func claudePromptUsage(reported acpmeta.TurnUsage) *acp.Usage {
	value := &acp.Usage{}
	if reported.InputTokens != nil {
		value.InputTokens = boundedInt(*reported.InputTokens)
	}
	if reported.OutputTokens != nil {
		value.OutputTokens = boundedInt(*reported.OutputTokens)
	}
	if reported.CacheReadTokens != nil {
		converted := boundedInt(*reported.CacheReadTokens)
		value.CachedReadTokens = &converted
	}
	if reported.CacheWriteTokens != nil {
		converted := boundedInt(*reported.CacheWriteTokens)
		value.CachedWriteTokens = &converted
	}
	total := int64(0)
	for _, counter := range []*int64{reported.InputTokens, reported.OutputTokens, reported.CacheReadTokens, reported.CacheWriteTokens} {
		if counter != nil {
			if *counter > int64(math.MaxInt)-total {
				return nil
			}
			total += *counter
		}
	}
	value.TotalTokens = int(total)
	return value
}

// validClaudeContextUsage 拒绝不可靠的上下文总量，避免负数与溢出变为可信零。
func validClaudeContextUsage(usage protocol.Usage) bool {
	total := int64(0)
	for _, value := range []int64{usage.InputTokens, usage.OutputTokens, usage.CacheReadInputTokens, usage.CacheCreationInputTokens} {
		if value < 0 || value > int64(math.MaxInt)-total {
			return false
		}
		total += value
	}
	return true
}

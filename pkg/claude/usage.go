package claude

import (
	"context"
	"math"
	"strings"
	"unicode"

	"github.com/JieWaZi/acp-go/pkg/claude/protocol"
	acp "github.com/coder/acp-go-sdk"
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
	turn.contextUsage = sanitizeClaudeUsage(usage)
	turn.contextUsageSet = true
	if model != "" {
		turn.assistantModel = model
	}
	turn.mu.Unlock()
}

// recordContextUsageDelta 合并 Anthropic message_delta 的可空累计字段。
func recordContextUsageDelta(turn *claudeTurn, delta protocol.UsageDelta) {
	turn.mu.Lock()
	usage := turn.contextUsage
	if delta.InputTokens != nil {
		usage.InputTokens = nonNegativeToken(*delta.InputTokens)
	}
	usage.OutputTokens = nonNegativeToken(delta.OutputTokens)
	if delta.CacheCreationInputTokens != nil {
		usage.CacheCreationInputTokens = nonNegativeToken(*delta.CacheCreationInputTokens)
	}
	if delta.CacheReadInputTokens != nil {
		usage.CacheReadInputTokens = nonNegativeToken(*delta.CacheReadInputTokens)
	}
	turn.contextUsage = usage
	turn.contextUsageSet = true
	turn.mu.Unlock()
}

// sendUsageUpdate 发布当前顶层 assistant 的上下文快照，而不是 result 的 Turn 累计量。
func (s *claudeSession) sendUsageUpdate(
	ctx context.Context,
	turn *claudeTurn,
	force bool,
) error {
	turn.mu.Lock()
	if !turn.contextUsageSet {
		turn.mu.Unlock()
		return nil
	}
	used := totalClaudeUsage(turn.contextUsage)
	alreadyPublished := turn.contextUsagePublishedSet && turn.contextUsagePublished == used
	turn.mu.Unlock()
	if alreadyPublished && !force {
		return nil
	}

	s.mu.Lock()
	window := s.contextWindowSize
	s.mu.Unlock()
	if window <= 0 {
		return nil
	}
	if err := s.agent.sendUpdate(ctx, s.id, acp.SessionUpdate{
		UsageUpdate: &acp.SessionUsageUpdate{
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

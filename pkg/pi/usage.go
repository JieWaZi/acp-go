package pi

import (
	"encoding/json"
	acp "github.com/coder/acp-go-sdk"
)

// accumulateUsage 对照 Pi AssistantMessage.usage，累计本轮每次完成的模型调用。
func (s *session) accumulateUsage(value map[string]any) {
	if value == nil {
		return
	}
	data, err := json.Marshal(value)
	if err != nil {
		return
	}
	var message piUsage
	if json.Unmarshal(data, &message) != nil || message.Input < 0 || message.Output < 0 || message.CacheRead < 0 || message.CacheWrite < 0 {
		return
	}
	s.mutex.Lock()
	defer s.mutex.Unlock()
	if s.turn == nil {
		return
	}
	if s.usage == nil {
		s.usage = &acp.Usage{CachedReadTokens: new(int), CachedWriteTokens: new(int)}
	}
	s.usage.InputTokens += message.Input
	s.usage.OutputTokens += message.Output
	*s.usage.CachedReadTokens += message.CacheRead
	*s.usage.CachedWriteTokens += message.CacheWrite
	s.usage.TotalTokens += message.Input + message.Output + message.CacheRead + message.CacheWrite
}

// piUsage 保留 Pi 官方互斥的四类 Token，不以会话累计值冒充本轮用量。
type piUsage struct {
	// Input 是非缓存输入 Token。
	Input int `json:"input"`
	// Output 是提供方返回的输出 Token，包含未单独报告的推理用量。
	Output int `json:"output"`
	// CacheRead 是命中缓存的输入 Token。
	CacheRead int `json:"cacheRead"`
	// CacheWrite 是新写入缓存的输入 Token。
	CacheWrite int `json:"cacheWrite"`
}

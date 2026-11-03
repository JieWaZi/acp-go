package pi

import (
	"encoding/json"
	"math"

	"github.com/JieWaZi/acp-go/pkg/acpmeta"
	acp "github.com/JieWaZi/acp-go/sdk"
)

// accumulateUsage 累计完成请求的已报告字段；空对象和无效计数不制造零消费。
func (s *session) accumulateUsage(value map[string]any) {
	if value == nil {
		return
	}
	data, err := json.Marshal(value)
	if err != nil {
		return
	}
	var message piUsage
	if json.Unmarshal(data, &message) != nil {
		return
	}
	known := false
	for _, counter := range []*int64{message.Input, message.Output, message.CacheRead, message.CacheWrite} {
		if counter != nil {
			if *counter < 0 || *counter > int64(math.MaxInt) {
				return
			}
			known = true
		}
	}
	if !known {
		return
	}
	s.mutex.Lock()
	defer s.mutex.Unlock()
	if s.turn == nil {
		return
	}
	first := s.reportedUsage == nil
	result := acpmeta.TurnUsage{Source: "pi.assistant.usage", Coverage: "observed_requests"}
	if !first {
		result = *s.reportedUsage
	}
	fields := []struct {
		// target 指向累计的可选分类。
		target **int64
		// incoming 是当前请求明确报告的分类。
		incoming *int64
	}{{&result.InputTokens, message.Input}, {&result.OutputTokens, message.Output}, {&result.CacheReadTokens, message.CacheRead}, {&result.CacheWriteTokens, message.CacheWrite}}
	for _, field := range fields {
		if first {
			if field.incoming != nil {
				counter := *field.incoming
				*field.target = &counter
			}
		} else if *field.target != nil {
			if field.incoming == nil || *field.incoming > int64(math.MaxInt)-**field.target {
				*field.target = nil
			} else {
				counter := **field.target + *field.incoming
				*field.target = &counter
			}
		}
	}
	usage := &acp.Usage{}
	if result.InputTokens != nil {
		usage.InputTokens = int(*result.InputTokens)
	}
	if result.OutputTokens != nil {
		usage.OutputTokens = int(*result.OutputTokens)
	}
	if result.CacheReadTokens != nil {
		counter := int(*result.CacheReadTokens)
		usage.CachedReadTokens = &counter
	}
	if result.CacheWriteTokens != nil {
		counter := int(*result.CacheWriteTokens)
		usage.CachedWriteTokens = &counter
	}
	total := int64(0)
	for _, counter := range []*int64{result.InputTokens, result.OutputTokens, result.CacheReadTokens, result.CacheWriteTokens} {
		if counter != nil {
			if *counter > int64(math.MaxInt)-total {
				return
			}
			total += *counter
		}
	}
	usage.TotalTokens = int(total)
	s.reportedUsage = &result
	s.usage = usage
}

// piUsage 对应 Pi 官方互斥分类，nil 表示当前请求没有报告该字段。
type piUsage struct {
	// Input 是非缓存输入 token 数。
	Input *int64 `json:"input"`
	// Output 是含未单列推理的输出 token 数。
	Output *int64 `json:"output"`
	// CacheRead 是缓存读取 token 数。
	CacheRead *int64 `json:"cacheRead"`
	// CacheWrite 是缓存写入 token 数。
	CacheWrite *int64 `json:"cacheWrite"`
}

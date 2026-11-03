package pi

import (
	acp "github.com/JieWaZi/acp-go/sdk"
	"testing"
)

// TestUsageFoundationPiEmptyAndPartial 保证空用量不生成已知零，部分字段保留缺失。
func TestUsageFoundationPiEmptyAndPartial(t *testing.T) {
	s := &session{turn: make(chan acp.StopReason, 1)}
	s.accumulateUsage(map[string]any{})
	if s.usage != nil {
		t.Fatalf("empty usage became known: %+v", s.usage)
	}
	s.accumulateUsage(map[string]any{"input": int64(10), "output": int64(0)})
	if s.usage == nil || s.usage.InputTokens != 10 || s.usage.OutputTokens != 0 || s.usage.CachedReadTokens != nil || s.usage.CachedWriteTokens != nil {
		t.Fatalf("partial fields lost: %+v", s.usage)
	}
}

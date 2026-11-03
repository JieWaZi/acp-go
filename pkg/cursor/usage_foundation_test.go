package cursor

import (
	"github.com/JieWaZi/acp-go/pkg/acpmeta"
	acp "github.com/JieWaZi/acp-go/sdk"
	"testing"
)

// TestUsageFoundationCursorConsumptionSnapshot 验证失败前收到的 hook 消费能独立于上下文交付。
func TestUsageFoundationCursorConsumptionSnapshot(t *testing.T) {
	read, write := 80, 0
	p := &turnProjection{usage: &acp.Usage{InputTokens: 20, OutputTokens: 5, CachedReadTokens: &read, CachedWriteTokens: &write, TotalTokens: 105}}
	update := p.usageUpdate()
	if update == nil {
		t.Fatal("missing consumption update")
	}
	got, ok := acpmeta.ReadTurnUsage(update.Meta)
	if !ok || got.InputTokens == nil || *got.InputTokens != 20 || got.CacheWriteTokens == nil || *got.CacheWriteTokens != 0 || got.Complete || got.ContextKnown || update.Used != 0 || update.Size != 0 {
		t.Fatalf("wrong snapshot: %+v", got)
	}
}

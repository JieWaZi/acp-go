package cursor

import (
	"context"
	"github.com/JieWaZi/acp-go/pkg/acpmeta"
	acp "github.com/JieWaZi/acp-go/sdk"
	"os"
	"path/filepath"
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

// TestUsageFixCursorWireCachePresence 从真实 hook 文件读取，区分缺失缓存分类与明确零。
func TestUsageFixCursorWireCachePresence(t *testing.T) {
	for _, caches := range []string{"", `,"cache_read_tokens":0,"cache_write_tokens":0`} {
		directory := t.TempDir()
		raw := `{"hook_event_name":"stop","conversation_id":"s","generation_id":"g","input_tokens":100,"output_tokens":5` + caches + `}`
		if err := os.WriteFile(filepath.Join(directory, "stop.json"), []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
		events, err := readHooks(context.Background(), directory)
		if err != nil {
			t.Fatal(err)
		}
		usage := events[0].usage()
		if usage == nil {
			t.Fatal("reported totals lost")
		}
		p := &turnProjection{usage: usage}
		reported, ok := acpmeta.ReadTurnUsage(p.usageUpdate().Meta)
		if !ok || reported.OutputTokens == nil || *reported.OutputTokens != 5 || usage.TotalTokens != 105 {
			t.Fatalf("totals lost: %+v / %+v", usage, reported)
		}
		if caches == "" {
			if reported.CacheReadTokens != nil || reported.CacheWriteTokens != nil || reported.InputTokens != nil {
				t.Fatalf("missing cache became known: %+v", reported)
			}
		} else if reported.CacheReadTokens == nil || *reported.CacheReadTokens != 0 || reported.CacheWriteTokens == nil || *reported.CacheWriteTokens != 0 || reported.InputTokens == nil || *reported.InputTokens != 100 {
			t.Fatalf("explicit zero lost: %+v", reported)
		}
	}
}

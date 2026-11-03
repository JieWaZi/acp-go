package kimi

import (
	"github.com/JieWaZi/acp-go/pkg/acpmeta"
	"os"
	"path/filepath"
	"testing"
)

// TestUsageFoundationKimiConsumptionWithoutContext 验证没有上下文数据时消费仍有标准通知。
func TestUsageFoundationKimiConsumptionWithoutContext(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wire.jsonl")
	if err := os.WriteFile(path, []byte(`{"type":"usage.record","model":"fixture","usageScope":"turn","usage":{"inputOther":7,"output":5,"inputCacheRead":3,"inputCacheCreation":2}}`+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	usage, update := readWireUsage(path, 0, "")
	if usage == nil || update == nil {
		t.Fatalf("consumption notification missing: %+v %+v", usage, update)
	}
	reported, ok := acpmeta.ReadTurnUsage(update.Meta)
	if !ok || reported.InputTokens == nil || *reported.InputTokens != 7 || reported.ContextKnown || update.Used != 0 || update.Size != 0 || reported.Complete {
		t.Fatalf("wrong partial snapshot: %+v %+v", reported, update)
	}
}

// TestUsageFoundationKimiFieldPresence 验证缺失分类不被反序列化默认值伪造为零。
func TestUsageFoundationKimiFieldPresence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wire.jsonl")
	if err := os.WriteFile(path, []byte(`{"type":"usage.record","usageScope":"turn","usage":{"inputOther":7,"output":0}}`+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	usage, update := readWireUsage(path, 0, "")
	if usage == nil || update == nil {
		t.Fatal("partial usage lost")
	}
	got, ok := acpmeta.ReadTurnUsage(update.Meta)
	if !ok || got.InputTokens == nil || *got.InputTokens != 7 || got.OutputTokens == nil || *got.OutputTokens != 0 || got.CacheReadTokens != nil || got.CacheWriteTokens != nil {
		t.Fatalf("unknown became zero: %+v", got)
	}
}

package kimi

import (
	"os"
	"path/filepath"
	"testing"
)

// TestWireUsageExcludesHistory 验证官方事件只累计新增本轮，缺失统计不能返回假零值。
func TestWireUsageExcludesHistory(t *testing.T) {
	file := filepath.Join(t.TempDir(), "wire.jsonl")
	event := `{"message":{"type":"StatusUpdate","payload":{"token_usage":{"input_other":7,"output":5,"input_cache_read":3,"input_cache_creation":2},"context_tokens":100,"max_context_tokens":100000}}}` + "\n"
	if err := os.WriteFile(file, []byte(event+event+event), 0600); err != nil {
		t.Fatal(err)
	}
	usage, update := readWireUsage(file, int64(len(event)))
	if usage == nil || usage.InputTokens != 14 || usage.OutputTokens != 10 || *usage.CachedReadTokens != 6 || *usage.CachedWriteTokens != 4 || usage.TotalTokens != 34 || update == nil || update.Used != 100 || update.Size != 100000 {
		t.Fatalf("wrong incremental usage: %+v %+v", usage, update)
	}
	if usage, update = readWireUsage(file, int64(3*len(event))); usage != nil || update != nil {
		t.Fatal("empty turn fabricated usage")
	}
	if usage, update = readWireUsage(file+".missing", 0); usage != nil || update != nil {
		t.Fatal("missing file fabricated usage")
	}
}

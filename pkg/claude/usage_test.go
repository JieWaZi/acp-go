package claude

import (
	"math"
	"testing"

	"github.com/JieWaZi/acp-go/pkg/claude/protocol"
)

// TestClaudeUsageHelpersMatchUpstreamSemantics 覆盖累计 delta、模型匹配与饱和求和。
func TestClaudeUsageHelpersMatchUpstreamSemantics(t *testing.T) {
	t.Parallel()
	input := int64(12)
	turn := &claudeTurn{
		contextUsage: protocol.Usage{
			InputTokens: 3, OutputTokens: 1,
			CacheReadInputTokens: 5, CacheCreationInputTokens: 2,
		},
		contextUsageSet: true,
	}
	recordContextUsageDelta(turn, protocol.UsageDelta{
		InputTokens: &input, OutputTokens: 7,
	})
	if turn.contextUsage.InputTokens != 12 || turn.contextUsage.OutputTokens != 7 ||
		turn.contextUsage.CacheReadInputTokens != 5 ||
		turn.contextUsage.CacheCreationInputTokens != 2 {
		t.Fatalf("merged Usage = %#v", turn.contextUsage)
	}

	models := map[string]protocol.ModelUsage{
		"claude-opus-4-6-1m": {ContextWindow: extendedClaudeContextWindow},
		"claude-sonnet-4":    {ContextWindow: defaultClaudeContextWindow},
	}
	key, usage, ok := matchingClaudeModelUsage(models, "claude-opus-4-6")
	if !ok || key != "claude-opus-4-6-1m" || usage.ContextWindow != extendedClaudeContextWindow {
		t.Fatalf("matching model Usage = %q, %#v, %v", key, usage, ok)
	}
	if inferClaudeContextWindow("Opus 4.6 [1M]") != extendedClaudeContextWindow ||
		inferClaudeContextWindow("claude-10m-preview") != defaultClaudeContextWindow {
		t.Fatal("Claude context window inference 不符合独立 1m token 规则")
	}
	if totalClaudeUsage(protocol.Usage{InputTokens: math.MaxInt64, OutputTokens: 1}) != math.MaxInt64 {
		t.Fatal("Claude Usage 饱和求和失败")
	}
}

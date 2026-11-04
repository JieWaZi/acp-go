package acpclient

import (
	acp "github.com/JieWaZi/acp-go/sdk"
	"testing"
)

func TestUnattributedPromptUsageKeepsEvidenceWithoutLedgerCounters(t *testing.T) {
	counter := usageCounter{fresh: true}
	for _, output := range []int64{4, 9, 2} {
		var pending promptUsage
		usage := pending.settle(&counter, UsageUnattributed, &promptResponse{Usage: &wireUsage{Input: acp.Ptr(int64(23)), Output: &output, Total: acp.Ptr(int64(100))}})
		if usage == nil || usage.OutputTokens != nil || usage.InputTokens != nil || usage.Complete || len(usage.Observations) != 1 {
			t.Fatalf("unproven scope entered ledger: %#v", usage)
		}
		observed := usage.Observations[0]
		if observed.Coverage != "unknown" || observed.InputTotalTokens == nil || *observed.InputTotalTokens != 23 || observed.TotalTokens == nil || *observed.TotalTokens != 100 || observed.Tokens.OutputTokens == nil || *observed.Tokens.OutputTokens != output {
			t.Fatalf("native evidence lost: %#v", observed)
		}
	}
}

package agentclient

import (
	"errors"
	"testing"

	acp "github.com/JieWaZi/acp-go-sdk"
	"github.com/JieWaZi/acp-go/pkg/acpmeta"
)

// A host may mutate its returned value while a detached receipt is still retained.
// Sharing any pointer or map here would corrupt the next consumer's usage evidence.
func TestPromptResultSnapshotDoesNotMutateRetainedOutcome(t *testing.T) {
	input, native, terminal := "input-1", "native-1", "terminal-1"
	count, used := int64(17), int64(23)
	stop := acp.StopReasonEndTurn
	deliveryErr := errors.New("observer unavailable")
	cost := &acpmeta.UsageCost{Amount: "0.25", Currency: "USD", Source: "native"}
	retained := PromptResult{
		Receipt: PromptReceipt{Input: PromptSubmission{InputID: input, NativeInputID: &input}, Admission: AdmissionConfirmed},
		Outcome: &NativeOutcome{
			Turn: acpmeta.NativeTurn{
				InvocationID: "provider-1", NativeTurnID: &native, TerminalEventID: &terminal,
				ConsumedInputIDs: []string{"input-1"}, Origin: acpmeta.NativeTurnOriginProvider,
				Usage: &acpmeta.TurnUsage{InputTokens: &count, Cost: cost, Models: map[string]acpmeta.ModelUsage{
					"model": {InputTokens: &count, OutputTokens: &count, CacheReadTokens: &count, CacheWriteTokens: &count, ReasoningTokens: &count, Cost: cost},
				}},
			},
			StopReason: &stop, FinalMessage: &FinalMessage{ID: "message-1", Text: "complete"},
			ContextUsage: ContextUsage{UsedTokens: &used}, DeliveryError: deliveryErr,
			Unavailable: []string{"tool_detail"},
		},
	}
	retained.Outcome.Turn.Usage.Observations = []acpmeta.UsageObservation{{
		Source: "cumulative_delta", Coverage: "query_pipeline",
		InputTotalTokens: &count, TotalTokens: &used,
		Tokens: acpmeta.ModelUsage{InputTokens: &count, Cost: cost},
		Models: map[string]acpmeta.ModelUsage{"model": {InputTokens: &count, Cost: cost}},
	}}
	consumer := retained.Snapshot()
	observation := &consumer.Outcome.Turn.Usage.Observations[0]
	observation.Coverage = "changed"
	*observation.InputTotalTokens = 999
	*observation.TotalTokens = 999
	*observation.Tokens.InputTokens = 999
	observation.Tokens.Cost.Amount = "999"
	*observation.Models["model"].InputTokens = 999
	observation.Models["model"].Cost.Amount = "999"
	delete(observation.Models, "model")
	*consumer.Receipt.Input.NativeInputID = "changed"
	*consumer.Outcome.Turn.NativeTurnID = "changed"
	*consumer.Outcome.Turn.TerminalEventID = "changed"
	consumer.Outcome.Turn.ConsumedInputIDs[0] = "changed"
	*consumer.Outcome.Turn.Usage.InputTokens = 999
	consumer.Outcome.Turn.Usage.Cost.Amount = "999"
	model := consumer.Outcome.Turn.Usage.Models["model"]
	for _, value := range []*int64{model.InputTokens, model.OutputTokens, model.CacheReadTokens, model.CacheWriteTokens, model.ReasoningTokens} {
		*value = 999
	}
	model.Cost.Amount = "999"
	delete(consumer.Outcome.Turn.Usage.Models, "model")
	*consumer.Outcome.StopReason = acp.StopReasonCancelled
	consumer.Outcome.FinalMessage.Text = "changed"
	*consumer.Outcome.ContextUsage.UsedTokens = 999
	consumer.Outcome.Unavailable[0] = "changed"
	if *retained.Outcome.Turn.Usage.Observations[0].InputTotalTokens != 17 || *retained.Outcome.Turn.Usage.Observations[0].TotalTokens != 23 {
		t.Fatal("consumer changed retained raw usage evidence")
	}

	if *retained.Receipt.Input.NativeInputID != "input-1" || *retained.Outcome.Turn.NativeTurnID != "native-1" || *retained.Outcome.Turn.TerminalEventID != "terminal-1" || retained.Outcome.Turn.ConsumedInputIDs[0] != "input-1" {
		t.Fatal("consumer changed retained identity")
	}
	usage := retained.Outcome.Turn.Usage
	if usage.Observations[0].Coverage != "query_pipeline" || *usage.Observations[0].Tokens.InputTokens != 17 || usage.Observations[0].Tokens.Cost.Amount != "0.25" || len(usage.Observations[0].Models) != 1 || *usage.Observations[0].Models["model"].InputTokens != 17 || usage.Observations[0].Models["model"].Cost.Amount != "0.25" {
		t.Fatal("consumer changed retained scoped observations")
	}
	if *usage.InputTokens != 17 || usage.Cost.Amount != "0.25" || len(usage.Models) != 1 {
		t.Fatal("consumer changed retained usage")
	}
	model = usage.Models["model"]
	for _, value := range []*int64{model.InputTokens, model.OutputTokens, model.CacheReadTokens, model.CacheWriteTokens, model.ReasoningTokens} {
		if *value != 17 {
			t.Fatal("consumer changed model attribution")
		}
	}
	if model.Cost.Amount != "0.25" || *retained.Outcome.StopReason != acp.StopReasonEndTurn || retained.Outcome.FinalMessage.Text != "complete" || *retained.Outcome.ContextUsage.UsedTokens != 23 || retained.Outcome.Unavailable[0] != "tool_detail" {
		t.Fatal("consumer changed retained terminal evidence")
	}
	if consumer.Outcome.ContextUsage.WindowTokens != nil || consumer.Outcome.Turn.Status != "" || consumer.Outcome.Turn.Usage.OutputTokens != nil || !errors.Is(consumer.Outcome.DeliveryError, deliveryErr) {
		t.Fatal("snapshot invented unknown facts or erased delivery failure")
	}
}

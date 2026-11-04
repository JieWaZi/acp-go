package codex

import (
	"context"
	"github.com/JieWaZi/acp-go/pkg/acpmeta"
	"github.com/JieWaZi/acp-go/pkg/codex/protocol"
	acp "github.com/JieWaZi/acp-go/sdk"
	"testing"
)

// usageSample 构造最近请求与线程累计独立的原生样本。
func usageSample(input, output, totalInput, totalOutput int64) protocol.ThreadTokenUsageUpdatedNotification {
	return protocol.ThreadTokenUsageUpdatedNotification{TokenUsage: protocol.TokenUsage{
		Last:  protocol.Last{InputTokens: input, OutputTokens: output, TotalTokens: input + output},
		Total: protocol.Last{InputTokens: totalInput, OutputTokens: totalOutput, TotalTokens: totalInput + totalOutput},
	}}
}

// TestUsageFoundationCodexMultiRequest 验证恢复、多请求和重复累计不会漏计或重计。
func TestUsageFoundationCodexMultiRequest(t *testing.T) {
	for _, history := range []int64{0, 1000} {
		h := newEventHandler(&recordingSessionUpdater{}, "s", nil, terminalOutputModeDelta)
		for _, sample := range []protocol.ThreadTokenUsageUpdatedNotification{usageSample(100, 10, history+100, 10), usageSample(150, 20, history+250, 30), usageSample(150, 20, history+250, 30)} {
			if err := h.handleTokenUsage(context.Background(), sample); err != nil {
				t.Fatal(err)
			}
		}
		usage, _ := h.Usage()
		got := usage.PromptUsage()
		if got.InputTokens != 250 || got.OutputTokens != 30 || got.TotalTokens != 280 {
			t.Fatalf("history=%d: got %+v; want 250/30/280", history, got)
		}
	}
}

// TestUsageFoundationCodexCacheWriteExclusive 验证输入分类互斥。
func TestUsageFoundationCodexCacheWriteExclusive(t *testing.T) {
	sample := usageSample(100, 10, 100, 10)
	write := int64(30)
	sample.TokenUsage.Last.CachedInputTokens = 20
	sample.TokenUsage.Last.CacheWriteInputTokens = &write
	usage, err := turnUsageFromTokenUsage(sample.TokenUsage)
	if err != nil {
		t.Fatal(err)
	}
	if got := usage.PromptUsage(); got.InputTokens != 50 {
		t.Fatalf("got %+v; want noncache=50", got)
	}
}

// TestUsageFoundationCodexInvalidCacheAndOverflow 拒绝缓存超出输入和求和溢出。
func TestUsageFoundationCodexInvalidCacheAndOverflow(t *testing.T) {
	for _, write := range []int64{81, 9223372036854775807} {
		sample := usageSample(100, 10, 100, 10)
		sample.TokenUsage.Last.CachedInputTokens = 20
		sample.TokenUsage.Last.CacheWriteInputTokens = &write
		if _, err := turnUsageFromTokenUsage(sample.TokenUsage); err == nil {
			t.Fatalf("accepted read20/write%d in input100", write)
		}
	}
}

// TestUsageFoundationCodexRegressionIsPartial 验证无 epoch 证据的回退保留已确认消费。
func TestUsageFoundationCodexRegressionIsPartial(t *testing.T) {
	updater := &recordingSessionUpdater{}
	h := newEventHandler(updater, "s", nil, terminalOutputModeDelta)
	for _, sample := range []protocol.ThreadTokenUsageUpdatedNotification{usageSample(100, 10, 1100, 110), usageSample(150, 20, 1250, 130), usageSample(10, 1, 10, 1), usageSample(150, 20, 1250, 130)} {
		if err := h.handleTokenUsage(context.Background(), sample); err != nil {
			t.Fatal(err)
		}
	}
	usage, _ := h.Usage()
	if got := usage.PromptUsage(); got.InputTokens != 250 || got.OutputTokens != 30 {
		t.Fatalf("regression changed confirmed usage: %+v", got)
	}
	if len(updater.notifications) != 4 {
		t.Fatalf("usage without context was not delivered: %d", len(updater.notifications))
	}
}

// TestUsageFoundationCodexReliableBaselineFinalMetadata 验证最终与取消快照的覆盖声明。
func TestUsageFoundationCodexReliableBaselineFinalMetadata(t *testing.T) {
	h := newEventHandler(&recordingSessionUpdater{}, "s", nil, terminalOutputModeDelta)
	h.previous = &turnUsage{InputTokens: 1000, OutputTokens: 100, TurnTokens: 1100}
	h.baselineKnown = true
	if err := h.handleTokenUsage(context.Background(), usageSample(150, 20, 1250, 130)); err != nil {
		t.Fatal(err)
	}
	prompt := newNativeTurn(context.Background(), 1)
	defer prompt.cancelRun()
	prompt.setEventRouter(&eventRouter{handler: h})
	response := codexPromptResponse(prompt, acp.StopReasonEndTurn, nil)
	reported, ok := acpmeta.ReadTurnUsage(response.Meta)
	if !ok || !reported.Complete || reported.InputTokens == nil || *reported.InputTokens != 250 || response.Usage.InputTokens != 250 {
		t.Fatalf("final snapshot incomplete: %+v / %+v", response, reported)
	}
	cancelled := codexPromptResponse(prompt, acp.StopReasonCancelled, nil)
	reported, ok = acpmeta.ReadTurnUsage(cancelled.Meta)
	if !ok || reported.Complete {
		t.Fatalf("cancelled completeness wrong: %+v", reported)
	}
}

// TestUsageFoundationCodexFieldRegressionKeepsOtherDeltas 验证单字段回退不吞掉其余可靠增量。
func TestUsageFoundationCodexFieldRegressionKeepsOtherDeltas(t *testing.T) {
	h := newEventHandler(&recordingSessionUpdater{}, "s", nil, terminalOutputModeDelta)
	for _, sample := range []protocol.ThreadTokenUsageUpdatedNotification{usageSample(100, 10, 1100, 110), usageSample(150, 20, 1000, 130)} {
		if err := h.handleTokenUsage(context.Background(), sample); err != nil {
			t.Fatal(err)
		}
	}
	got, _ := h.Usage()
	if got.InputTokens != 100 || got.OutputTokens != 30 || got.Complete {
		t.Fatalf("field regression swallowed valid output: %+v", got)
	}
}

// TestUsageFoundationCodexHotSessionCheckpoint 验证同会话下一轮复用的是上一轮累计终值。
func TestUsageFoundationCodexHotSessionCheckpoint(t *testing.T) {
	store := newSessionStore()
	generation, err := store.beginOpen("s")
	if err != nil {
		t.Fatal(err)
	}
	state, ok := store.install("s", "/tmp", generation, nil, terminalOutputModeDelta)
	if !ok {
		t.Fatal("install session")
	}
	agent := &Agent{sessions: store, sessionUpdater: &recordingSessionUpdater{}}
	first := newNativeTurn(context.Background(), 1)
	defer first.cancelRun()
	state.activeTurn = first
	agent.onTurnStarted(state, first, "first")
	if err = first.currentEventRouter().handler.handleTokenUsage(context.Background(), usageSample(100, 10, 100, 10)); err != nil {
		t.Fatal(err)
	}
	second := newNativeTurn(context.Background(), 2)
	defer second.cancelRun()
	state.activeTurn = second
	agent.onTurnStarted(state, second, "second")
	if err = second.currentEventRouter().handler.handleTokenUsage(context.Background(), usageSample(40, 5, 140, 15)); err != nil {
		t.Fatal(err)
	}
	got := codexPromptResponse(second, acp.StopReasonEndTurn, nil)
	reported, known := acpmeta.ReadTurnUsage(got.Meta)
	if got.Usage == nil || got.Usage.InputTokens != 40 || got.Usage.OutputTokens != 5 || !known || !reported.Complete {
		t.Fatalf("hot session baseline lost: %+v / %+v", got, reported)
	}
}

// TestUsageFoundationCodexCombinedOverflow 拒绝输入输出求和溢出，即使单字段仍在 int64 范围。
func TestUsageFoundationCodexCombinedOverflow(t *testing.T) {
	sample := protocol.TokenUsage{Last: protocol.Last{InputTokens: 9223372036854775807, OutputTokens: 1, TotalTokens: 9223372036854775807}}
	if _, err := turnUsageFromTokenUsage(sample); err == nil {
		t.Fatal("combined input/output overflow accepted")
	}
}

// TestUnownedUsageInvalidatesTheNextPromptBaseline prevents background work from being charged to a new prompt.
func TestUnownedUsageInvalidatesTheNextPromptBaseline(t *testing.T) {
	store := newSessionStore()
	generation, err := store.beginOpen("thread")
	if err != nil {
		t.Fatal(err)
	}
	state, ok := store.install("thread", "/tmp", generation, nil, terminalOutputModeDelta)
	if !ok {
		t.Fatal("install session")
	}
	state.usageTotal = &turnUsage{InputTokens: 100, TurnTokens: 100}
	agent := &Agent{sessions: store}
	background := usageSample(30, 0, 130, 0)
	background.ThreadID, background.TurnID = "thread", "unowned"
	agent.handleNotification(context.Background(), 0, false, decodeTestNotification(t, protocol.MethodThreadTokenUsageUpdated, background))
	prompt := newNativeTurn(context.Background(), 1)
	defer prompt.cancelRun()
	state.activeTurn = prompt
	agent.onTurnStarted(state, prompt, "prompt")
	if err := prompt.currentEventRouter().handler.recordTokenUsage(usageSample(10, 0, 140, 0)); err != nil {
		t.Fatal(err)
	}
	response := codexPromptResponse(prompt, acp.StopReasonEndTurn, nil)
	reported, ok := acpmeta.ReadTurnUsage(response.Meta)
	if !ok || reported.InputTokens == nil || *reported.InputTokens != 10 || reported.Complete {
		t.Fatalf("unowned usage contaminated the next prompt: %#v", reported)
	}
}

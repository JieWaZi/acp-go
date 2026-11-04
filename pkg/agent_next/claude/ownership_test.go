package claude_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/JieWaZi/acp-go/pkg/acpmeta"
	agentclient "github.com/JieWaZi/acp-go/pkg/agent_next"
	"github.com/JieWaZi/acp-go/pkg/agent_next/claude"
	acp "github.com/JieWaZi/acp-go/sdk"
)

func launchPrompt(t *testing.T, client sessionClient, ctx context.Context, text string) <-chan agentclient.PromptResult {
	t.Helper()
	run, err := client.ReservePrompt(ctx, client.prompt(text), agentclient.PromptOptions{})
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan agentclient.PromptResult, 1)
	go func() { result <- run() }()
	return result
}

func receive[T any](t *testing.T, ctx context.Context, ch <-chan T) T {
	t.Helper()
	select {
	case value := <-ch:
		return value
	case <-ctx.Done():
		t.Fatal(ctx.Err())
		var value T
		return value
	}
}

func marker(t *testing.T, ctx context.Context, path string) []byte {
	t.Helper()
	timer := time.NewTicker(2 * time.Millisecond)
	defer timer.Stop()
	for {
		if data, err := os.ReadFile(path); err == nil {
			return data
		}
		select {
		case <-timer.C:
		case <-ctx.Done():
			t.Fatalf("missing fixture marker %s: %v", filepath.Base(path), ctx.Err())
		}
	}
}

func notifyText(dst chan<- string) agentclient.HostCallbacks {
	return agentclient.HostCallbacks{Observe: func(_ context.Context, _ agentclient.Scope, n acp.SessionNotification) error {
		if chunk := n.Update.AgentMessageChunk; chunk != nil && chunk.Content.Text != nil {
			dst <- chunk.Content.Text.Text
		}
		return nil
	}}
}

func TestCapturedCancelSkipsObservedSuccessorAtNativeWriteGate(t *testing.T) {
	text := make(chan string, 16)
	client, input, ctx := startMode(t, notifyText(text), "stale", func(o *claude.Options) { o.ControlTimeout = 900 * time.Millisecond })
	dir := filepath.Dir(input)
	result := launchPrompt(t, client, ctx, "hold A")
	if v := receive(t, ctx, text); v != "foreground A" {
		t.Fatalf("text = %q", v)
	}
	steered := make(chan error, 1)
	go func() {
		_, err := client.Steer(ctx, agentclient.SteerRequest{SessionID: client.id, Prompt: client.prompt(strings.Repeat("x", 2<<20)).Prompt, PromptRequired: true})
		steered <- err
	}()
	marker(t, ctx, filepath.Join(dir, "write-blocked"))
	if err := client.Cancel(acp.CancelNotification{SessionId: client.id}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "finish-old"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	r := receive(t, ctx, result)
	if r.Outcome == nil || r.Outcome.Turn.Status != acpmeta.NativeTurnCompleted {
		t.Fatalf("old result = %+v", r.Outcome)
	}
	if v := receive(t, ctx, text); v != "provider successor" {
		t.Fatalf("text = %q", v)
	}
	if err := os.WriteFile(filepath.Join(dir, "resume-stdin"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := receive(t, ctx, steered); err != nil {
		t.Fatal(err)
	}
	marker(t, ctx, filepath.Join(dir, "recall"))
	time.Sleep(100 * time.Millisecond)
	if _, err := os.Stat(filepath.Join(dir, "interrupt")); !os.IsNotExist(err) {
		t.Fatalf("stale interrupt reached successor: %v", err)
	}
	// A new cancellation genuinely targets P and proves the control path works.
	if err := client.Cancel(acp.CancelNotification{SessionId: client.id}); err != nil {
		t.Fatal(err)
	}
	marker(t, ctx, filepath.Join(dir, "interrupt"))
}

func TestMissingInterruptAckDoesNotCloseSuccessor(t *testing.T) {
	text := make(chan string, 16)
	client, input, ctx := startMode(t, notifyText(text), "watchdog", func(o *claude.Options) { o.ControlTimeout = 80 * time.Millisecond })
	result := launchPrompt(t, client, ctx, "A")
	receive(t, ctx, text)
	if err := client.Cancel(acp.CancelNotification{SessionId: client.id}); err != nil {
		t.Fatal(err)
	}
	r := receive(t, ctx, result)
	if r.Outcome == nil || r.Outcome.Turn.Status != acpmeta.NativeTurnCompleted {
		t.Fatalf("old result = %+v", r.Outcome)
	}
	if v := receive(t, ctx, text); v != "provider successor" {
		t.Fatalf("text = %q", v)
	}
	time.Sleep(200 * time.Millisecond)
	path := filepath.Join(filepath.Dir(input), "interrupt")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := client.Cancel(acp.CancelNotification{SessionId: client.id}); err != nil {
		t.Fatal(err)
	}
	marker(t, ctx, path)
}

func TestProviderConsumesHostInputInSameCanonicalOutcome(t *testing.T) {
	text := make(chan string, 16)
	client, _, ctx := startMode(t, notifyText(text), "provider", nil)
	if v := receive(t, ctx, text); v != "provider started" {
		t.Fatal(v)
	}
	r := receive(t, ctx, launchPrompt(t, client, ctx, "fold me"))
	if r.Receipt.Admission != agentclient.AdmissionConfirmed || r.Outcome == nil || r.Outcome.Turn.Origin != acpmeta.NativeTurnOriginProvider ||
		!reflect.DeepEqual(r.Outcome.Turn.ConsumedInputIDs, []string{r.Receipt.Input.InputID}) {
		t.Fatalf("folded result = %+v, outcome = %+v", r, r.Outcome)
	}
	page, err := client.ReadNativeOutcomes(ctx, acpmeta.NativeTurnReceiptsRequest{SessionID: string(client.id)}, true)
	if err != nil || len(page.Outcomes) != 1 || !reflect.DeepEqual(page.Outcomes[0], *r.Outcome) {
		t.Fatalf("page = %+v, %v", page, err)
	}
}

func TestNumberedFailureStillConfirmsExactHostInput(t *testing.T) {
	client, _, ctx := startMode(t, agentclient.HostCallbacks{}, "numbered-failure", nil)
	r := receive(t, ctx, launchPrompt(t, client, ctx, "A"))
	if r.Receipt.Admission != agentclient.AdmissionConfirmed || r.Outcome == nil || r.Outcome.Turn.Status != acpmeta.NativeTurnFailed ||
		r.Outcome.Turn.Usage == nil || r.Outcome.Turn.Usage.InputTokens == nil || *r.Outcome.Turn.Usage.InputTokens != 43 ||
		!reflect.DeepEqual(r.Outcome.Turn.ConsumedInputIDs, []string{r.Receipt.Input.InputID}) {
		t.Fatalf("numbered failure = %+v, outcome = %+v", r, r.Outcome)
	}
}

func TestForeignDeliveryErrorAndDuplicateResultDoNotSettleAnotherTurn(t *testing.T) {
	client, _, ctx := startMode(t, agentclient.HostCallbacks{}, "foreign-duplicate", nil)
	first := receive(t, ctx, launchPrompt(t, client, ctx, "first"))
	second := receive(t, ctx, launchPrompt(t, client, ctx, "second"))
	for _, result := range []agentclient.PromptResult{first, second} {
		if result.Outcome == nil || result.Outcome.Turn.Status != acpmeta.NativeTurnCompleted || result.Receipt.Admission != agentclient.AdmissionConfirmed {
			t.Fatalf("result = %+v", result)
		}
	}
	page, err := client.ReadNativeOutcomes(ctx, acpmeta.NativeTurnReceiptsRequest{SessionID: string(client.id)}, false)
	if err != nil || len(page.Outcomes) != 2 || page.Outcomes[0].Turn.InvocationID == page.Outcomes[1].Turn.InvocationID {
		t.Fatalf("page = %+v, %v", page, err)
	}
}

func TestTransportExitRetainsUsageWithoutInventingNativeTerminal(t *testing.T) {
	client, _, ctx := startMode(t, agentclient.HostCallbacks{}, "transport", nil)
	r := receive(t, ctx, launchPrompt(t, client, ctx, "one"))
	if r.Outcome == nil || r.Outcome.Turn.Status != "" || r.Outcome.Turn.TerminalEventID != nil || r.Outcome.StopReason != nil ||
		r.Outcome.NativeFailure != nil || r.Outcome.OperationError == nil || r.Outcome.Turn.Usage == nil || r.Outcome.Turn.Usage.InputTokens == nil ||
		*r.Outcome.Turn.Usage.InputTokens != 43 {
		t.Fatalf("transport result = %+v", r.Outcome)
	}
}

func TestAutonomousReceiptsSurviveCloseAndAckExactIDs(t *testing.T) {
	client, input, ctx := startMode(t, agentclient.HostCallbacks{Observe: func(context.Context, agentclient.Scope, acp.SessionNotification) error {
		return errors.New("host offline")
	}}, "pages", nil)
	marker(t, ctx, filepath.Join(filepath.Dir(input), "produced"))
	request := acpmeta.NativeTurnReceiptsRequest{SessionID: string(client.id)}
	for {
		page, err := client.ReadNativeOutcomes(ctx, request, true)
		if err != nil {
			t.Fatal(err)
		}
		if page.More {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(2 * time.Millisecond):
		}
	}
	if _, err := client.CloseSession(ctx, acp.CloseSessionRequest{SessionId: client.id}); err != nil {
		t.Fatal(err)
	}
	first, err := client.ReadNativeOutcomes(ctx, request, false)
	if err != nil || len(first.Outcomes) != 16 || !first.More {
		t.Fatalf("first page = %+v, %v", first, err)
	}
	for i, out := range first.Outcomes {
		if out.Turn.Origin != acpmeta.NativeTurnOriginProvider || out.DeliveryError == nil || out.FinalMessage == nil {
			t.Fatalf("receipt %d = %+v", i, out)
		}
		request.AckInvocationIDs = append(request.AckInvocationIDs, out.Turn.InvocationID)
	}
	second, err := client.ReadNativeOutcomes(ctx, request, false)
	if err != nil || len(second.Outcomes) != 4 || second.More {
		t.Fatalf("second page = %+v, %v", second, err)
	}
	again, err := client.ReadNativeOutcomes(ctx, request, false)
	if err != nil || len(again.Outcomes) != 4 || !reflect.DeepEqual(second, again) {
		t.Fatalf("retry = %+v, %v", again, err)
	}
}

func TestCloseWaitsForPreparedCleanupAndCanRetry(t *testing.T) {
	client, _, ctx := start(t, agentclient.HostCallbacks{})
	preparing := make(chan struct{})
	cleaning := make(chan struct{})
	release := make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	run, err := client.ReservePrompt(ctx, client.prompt("hold"), agentclient.PromptOptions{
		Prepare: func(ctx context.Context) (func(), error) {
			close(preparing)
			<-ctx.Done()
			return func() { close(cleaning); <-release }, ctx.Err()
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan agentclient.PromptResult, 1)
	go func() { done <- run() }()
	receive(t, ctx, preparing)
	deadline, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
	defer cancel()
	_, err = client.CloseSession(deadline, acp.CloseSessionRequest{SessionId: client.id})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("first close = %v", err)
	}
	receive(t, ctx, cleaning)
	if _, err := client.ReservePrompt(ctx, client.prompt("do not replace"), agentclient.PromptOptions{}); err == nil {
		t.Fatal("old session admitted after close timeout")
	}
	close(release)
	r := receive(t, ctx, done)
	if r.Receipt.Submission != agentclient.SubmissionNotSent {
		t.Fatalf("receipt = %+v", r.Receipt)
	}
	if _, err := client.CloseSession(ctx, acp.CloseSessionRequest{SessionId: client.id}); err != nil {
		t.Fatal(err)
	}
}

func TestHostReservationWaitsForProviderCancellationBoundary(t *testing.T) {
	text := make(chan string, 16)
	client, input, ctx := startMode(t, notifyText(text), "provider-cancel-fifo", nil)
	receive(t, ctx, text)
	if err := client.Cancel(acp.CancelNotification{SessionId: client.id}); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Dir(input)
	marker(t, ctx, filepath.Join(dir, "interrupt"))
	result := launchPrompt(t, client, ctx, "next admitted host")
	select {
	case r := <-result:
		t.Fatalf("host retired before provider terminal: %+v", r)
	case <-time.After(80 * time.Millisecond):
	}
	if _, err := os.Stat(input); !os.IsNotExist(err) {
		t.Fatalf("host wrote during interrupt barrier: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "allow-interrupt"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	r := receive(t, ctx, result)
	if r.Outcome == nil || r.Outcome.Turn.Status != acpmeta.NativeTurnCompleted {
		t.Fatalf("result after boundary = %+v", r)
	}
}

func TestMissingCumulativeModelReportInvalidatesPerTurnBaseline(t *testing.T) {
	client, _, ctx := startMode(t, agentclient.HostCallbacks{}, "usage-gap", nil)
	first := receive(t, ctx, launchPrompt(t, client, ctx, "first"))
	_ = receive(t, ctx, launchPrompt(t, client, ctx, "no cumulative report"))
	third := receive(t, ctx, launchPrompt(t, client, ctx, "cumulative again"))
	if first.Outcome == nil || first.Outcome.Turn.Usage == nil || first.Outcome.Turn.Usage.Coverage != "query_pipeline" || *first.Outcome.Turn.Usage.InputTokens != 20 {
		t.Fatalf("first usage = %+v", first.Outcome)
	}
	if third.Outcome == nil || third.Outcome.Turn.Usage == nil || third.Outcome.Turn.Usage.Coverage != "main_loop" || *third.Outcome.Turn.Usage.InputTokens != 43 {
		t.Fatalf("third usage = %+v", third.Outcome.Turn.Usage)
	}
}

func TestCancelRecallsWrittenUnconsumedInputByNativeUUID(t *testing.T) {
	client, input, ctx := startMode(t, agentclient.HostCallbacks{}, "recall", func(o *claude.Options) { o.ControlTimeout = 150 * time.Millisecond })
	result := launchPrompt(t, client, ctx, "pending native input")
	native := string(marker(t, ctx, input))
	if err := client.Cancel(acp.CancelNotification{SessionId: client.id}); err != nil {
		t.Fatal(err)
	}
	r := receive(t, ctx, result)
	if r.Outcome != nil || r.Receipt.Submission != agentclient.SubmissionSent || r.Receipt.Admission != agentclient.AdmissionNotAccepted ||
		!errors.Is(r.Receipt.OperationError, context.Canceled) {
		t.Fatalf("recall = %+v", r)
	}
	if got := string(marker(t, ctx, filepath.Join(filepath.Dir(input), "recall"))); got != native {
		t.Fatalf("recall UUID = %q, want %q", got, native)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(input), "interrupt")); !os.IsNotExist(err) {
		t.Fatal("no native invocation was available to interrupt")
	}
}

func TestReceiptOverflowPreservesAlreadyFinalizedPrefix(t *testing.T) {
	client, input, ctx := startMode(t, agentclient.HostCallbacks{}, "overflow", nil)
	if err := os.WriteFile(filepath.Join(filepath.Dir(input), "produce"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	marker(t, ctx, filepath.Join(filepath.Dir(input), "produced"))
	// Let the single reader fold the bounded native flood before sending
	// any ACK that would make room for the final overflow frame.
	time.Sleep(100 * time.Millisecond)
	req := acpmeta.NativeTurnReceiptsRequest{SessionID: string(client.id)}
	var complete int
	for {
		page, err := client.ReadNativeOutcomes(ctx, req, true)
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Outcomes) == 0 {
			break
		}
		req.AckInvocationIDs = nil
		for _, out := range page.Outcomes {
			if out.FinalMessage == nil || out.Turn.Status != acpmeta.NativeTurnCompleted {
				t.Fatalf("lost prefix result = %+v", out)
			}
			req.AckInvocationIDs = append(req.AckInvocationIDs, out.Turn.InvocationID)
		}
		complete += len(page.Outcomes)
	}
	if complete != 128 {
		t.Fatalf("retained %d native results, want 128", complete)
	}
}

func TestLargeNativeAnswerDoesNotOverrunReceiptProjection(t *testing.T) {
	client, _, ctx := startMode(t, agentclient.HostCallbacks{}, "large", nil)
	r := receive(t, ctx, launchPrompt(t, client, ctx, "large response"))
	if r.Outcome == nil || r.Outcome.FinalMessage != nil || !reflect.DeepEqual(r.Outcome.Unavailable, []string{acpmeta.ReceiptResultTextUnavailable}) ||
		r.Outcome.Turn.Usage == nil || *r.Outcome.Turn.Usage.InputTokens != 43 {
		t.Fatalf("result = %+v", r.Outcome)
	}
	page, err := client.ReadNativeOutcomes(ctx, acpmeta.NativeTurnReceiptsRequest{SessionID: string(client.id)}, false)
	if err != nil || len(page.Outcomes) != 1 || !reflect.DeepEqual(page.Outcomes[0], *r.Outcome) {
		t.Fatalf("page = %+v, %v", page, err)
	}
}

func TestObserverTimeoutPublishesTerminalBeforeCallbackCleanup(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	client, _, ctx := startMode(t, agentclient.HostCallbacks{Observe: func(_ context.Context, scope agentclient.Scope, _ acp.SessionNotification) error {
		if scope.NativeInvocationID != "" {
			close(entered)
			<-release
		}
		return nil
	}}, "", func(o *claude.Options) { o.ObservationTimeout = 30 * time.Millisecond })
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	result := launchPrompt(t, client, ctx, "observe slowly")
	receive(t, ctx, entered)
	r := receive(t, ctx, result)
	if r.Outcome == nil || !errors.Is(r.Outcome.DeliveryError, context.DeadlineExceeded) ||
		r.Outcome.Turn.Status != acpmeta.NativeTurnCompleted || r.Outcome.Turn.Usage == nil {
		t.Fatalf("result = %+v", r.Outcome)
	}
	closeCtx, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
	defer cancel()
	if _, err := client.CloseSession(closeCtx, acp.CloseSessionRequest{SessionId: client.id}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("close before observer cleanup = %v", err)
	}
	close(release)
	if _, err := client.CloseSession(ctx, acp.CloseSessionRequest{SessionId: client.id}); err != nil {
		t.Fatal(err)
	}
}

func TestResultSequenceGapDoesNotAssignSuccessorToHost(t *testing.T) {
	client, _, ctx := startMode(t, agentclient.HostCallbacks{}, "result-gap", nil)
	r := receive(t, ctx, launchPrompt(t, client, ctx, "original host"))
	if r.Outcome == nil || r.Outcome.Turn.Status != "" || r.Outcome.OperationError == nil || r.Outcome.Turn.TerminalEventID != nil ||
		r.Outcome.Turn.Usage == nil || *r.Outcome.Turn.Usage.InputTokens != 43 {
		t.Fatalf("host = %+v", r.Outcome)
	}
	req := acpmeta.NativeTurnReceiptsRequest{SessionID: string(client.id)}
	for {
		page, err := client.ReadNativeOutcomes(ctx, req, true)
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Outcomes) == 3 {
			last := page.Outcomes[2]
			if last.Turn.Origin != acpmeta.NativeTurnOriginProvider || last.Turn.Status != acpmeta.NativeTurnCompleted ||
				last.FinalMessage == nil || last.FinalMessage.Text != "provider after gap" || *last.Turn.Usage.InputTokens != 7 {
				t.Fatalf("successor = %+v", last)
			}
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(2 * time.Millisecond):
		}
	}
}

func TestTurnUsageKeepsKnownRowsAcrossMultipleModels(t *testing.T) {
	client, _, ctx := startMode(t, agentclient.HostCallbacks{}, "review-partial-models", nil)
	result := receive(t, ctx, launchPrompt(t, client, ctx, "observe two models"))
	if result.Outcome == nil || result.Outcome.Turn.Usage == nil {
		t.Fatalf("result = %+v", result)
	}
	usage := result.Outcome.Turn.Usage
	if usage.Coverage != "main_loop" || usage.InputTokens == nil || *usage.InputTokens != 7 || len(usage.Models) != 2 {
		t.Fatalf("usage = %+v", usage)
	}
	for model, want := range map[string]int64{"sonnet": 3, "opus": 4} {
		row := usage.Models[model]
		if row.InputTokens == nil || *row.InputTokens != want || row.OutputTokens != nil {
			t.Fatalf("%s = %+v", model, row)
		}
	}
}

func TestCumulativeModelAndCostBoundariesAreIndependent(t *testing.T) {
	client, _, ctx := startMode(t, agentclient.HostCallbacks{}, "review-usage-boundaries", nil)
	check := func(r agentclient.PromptResult, input int64, coverage, cost string) {
		t.Helper()
		if r.Outcome == nil || r.Outcome.Turn.Usage == nil {
			t.Fatalf("outcome = %+v", r.Outcome)
		}
		usage := r.Outcome.Turn.Usage
		amount := ""
		if usage.Cost != nil {
			amount = string(usage.Cost.Amount)
		}
		if usage.InputTokens == nil || *usage.InputTokens != input || usage.Coverage != coverage || amount != cost {
			t.Fatalf("usage = %+v cost=%s, want %d %s %s", usage, amount, input, coverage, cost)
		}
	}
	check(receive(t, ctx, launchPrompt(t, client, ctx, "baseline")), 10, "query_pipeline", "0.010000000000000000")
	check(receive(t, ctx, launchPrompt(t, client, ctx, "auto")), 3, "main_loop", "0.010000000000000000")
	compact, err := client.Compact(ctx, acpmeta.CompactRequest{SessionID: string(client.id)})
	if err != nil || compact.Status != acpmeta.NativeTurnCompleted {
		t.Fatalf("compact = %+v, %v", compact, err)
	}
	turn, ok := acpmeta.ReadNativeTurn(compact.Meta)
	if !ok || turn.Usage == nil {
		t.Fatalf("compact usage = %+v", turn)
	}
	check(agentclient.PromptResult{Outcome: &agentclient.NativeOutcome{Turn: turn}}, 3, "main_loop", "")
	check(receive(t, ctx, launchPrompt(t, client, ctx, "after compact")), 10, "query_pipeline", "")
	check(receive(t, ctx, launchPrompt(t, client, ctx, "clear")), 5, "query_pipeline", "0.005000000000000000")
}

func TestReceiptDeadlineIncludesEarlierSessionObservations(t *testing.T) {
	client, _, ctx := startMode(t, agentclient.HostCallbacks{
		Observe: func(ctx context.Context, scope agentclient.Scope, _ acp.SessionNotification) error {
			if scope.NativeInvocationID == "" {
				timer := time.NewTimer(75 * time.Millisecond)
				defer timer.Stop()
				select {
				case <-timer.C:
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			return nil
		},
	}, "rereview-observation-prefix", func(o *claude.Options) {
		o.ObservationTimeout = 100 * time.Millisecond
	})
	deadline, stop := context.WithTimeout(ctx, 300*time.Millisecond)
	defer stop()
	r := receive(t, deadline, launchPrompt(t, client, ctx, "complete within the receipt budget"))
	if r.Outcome == nil || r.Outcome.Turn.Status != acpmeta.NativeTurnCompleted || r.Outcome.Turn.Usage == nil {
		t.Fatalf("native outcome = %+v", r.Outcome)
	}
}

func TestMultipleAssistantBlocksReconcileStreamAndNativeFinalText(t *testing.T) {
	text := make(chan string, 8)
	client, _, ctx := startMode(t, notifyText(text), "rereview-multiple-assistant-blocks", nil)
	r := receive(t, ctx, launchPrompt(t, client, ctx, "collect both blocks"))
	if r.Outcome == nil || r.Outcome.FinalMessage == nil || r.Outcome.FinalMessage.Text != "first second" {
		t.Fatalf("final message = %+v", r.Outcome)
	}
	var output strings.Builder
	for len(text) > 0 {
		output.WriteString(<-text)
	}
	if output.String() != "first second" {
		t.Fatalf("stream = %q", output.String())
	}
}

func TestToolNarrationDoesNotBecomeFinalAnswer(t *testing.T) {
	text := make(chan string, 8)
	client, _, ctx := startMode(t, notifyText(text), "rereview-tool-narration", nil)
	r := receive(t, ctx, launchPrompt(t, client, ctx, "use a tool"))
	if r.Outcome == nil || r.Outcome.FinalMessage != nil || r.Outcome.Turn.Status != acpmeta.NativeTurnCompleted {
		t.Fatalf("outcome = %+v", r.Outcome)
	}
	if got := receive(t, ctx, text); got != "I will use a tool." {
		t.Fatalf("narration = %q", got)
	}
}

func TestRefusalFallbackSupersedesNativeTextInCanonicalAnswer(t *testing.T) {
	client, _, ctx := startMode(t, agentclient.HostCallbacks{}, "rereview-fallback-supersedes", nil)
	r := receive(t, ctx, launchPrompt(t, client, ctx, "retry fallback"))
	if r.Outcome == nil || r.Outcome.FinalMessage == nil || r.Outcome.FinalMessage.Text != "replacement answer" {
		t.Fatalf("native outcome = %+v", r.Outcome)
	}
}

func TestMaxTurnsKeepsNativeFailureStatusAndACPStopReason(t *testing.T) {
	client, _, ctx := startMode(t, agentclient.HostCallbacks{}, "rereview-max-turns", nil)
	r := receive(t, ctx, launchPrompt(t, client, ctx, "reach native turn budget"))
	if r.Outcome == nil || r.Outcome.Turn.Status != acpmeta.NativeTurnFailed ||
		r.Outcome.StopReason == nil || *r.Outcome.StopReason != acp.StopReasonMaxTurnRequests ||
		r.Outcome.Turn.Usage == nil {
		t.Fatalf("native outcome = %+v", r.Outcome)
	}
}

func TestLegacyReplayAcknowledgesOnlyExactHostInput(t *testing.T) {
	for _, mode := range []string{"legacy-echo-current", "legacy-echo-early", "legacy-echo-foreign", "legacy-echo-provider", "legacy-echo-provider-origin", "legacy-echo-background"} {
		t.Run(mode, func(t *testing.T) {
			client, _, ctx := startMode(t, agentclient.HostCallbacks{}, mode, nil)
			promptCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
			defer cancel()
			first := receive(t, promptCtx, launchPrompt(t, client, promptCtx, "legacy prompt"))
			if mode == "legacy-echo-current" || mode == "legacy-echo-early" {
				for _, result := range []agentclient.PromptResult{first, receive(t, promptCtx, launchPrompt(t, client, promptCtx, "legacy follow-up"))} {
					if result.Receipt.Admission != agentclient.AdmissionConfirmed || result.Outcome == nil ||
						result.Outcome.Turn.Origin != acpmeta.NativeTurnOriginHost || result.Outcome.Turn.Status != acpmeta.NativeTurnCompleted ||
						!reflect.DeepEqual(result.Outcome.Turn.ConsumedInputIDs, []string{result.Receipt.Input.InputID}) {
						t.Fatalf("legacy host result = %+v", result)
					}
				}
				return
			}
			if first.Receipt.Admission != agentclient.AdmissionUnknown || first.Receipt.OperationError == nil || first.Outcome != nil {
				t.Fatalf("foreign echo attached to the host input: %+v", first)
			}
			page, err := client.ReadNativeOutcomes(ctx, acpmeta.NativeTurnReceiptsRequest{SessionID: string(client.id)}, true)
			if err != nil || len(page.Outcomes) != 1 {
				t.Fatalf("foreign native result page = %+v %v", page, err)
			}
			native := page.Outcomes[0].Turn
			if native.Status != acpmeta.NativeTurnCompleted || len(native.ConsumedInputIDs) != 0 ||
				native.Usage == nil || native.Usage.InputTokens == nil || *native.Usage.InputTokens != 43 {
				t.Fatalf("unassociated native result lost evidence: %+v", native)
			}
		})
	}
}

func TestLegacyReplayRejectsOverlappingHostInputsBeforeAdmission(t *testing.T) {
	opened := make(chan agentclient.Scope, 1)
	client, input, ctx := startMode(t, agentclient.HostCallbacks{Observe: func(_ context.Context, scope agentclient.Scope, update acp.SessionNotification) error {
		if update.Update.AgentMessageChunk != nil {
			opened <- scope
		}
		return nil
	}}, "legacy-echo-held", nil)
	capabilities, err := client.Initialize(ctx, acp.InitializeRequest{})
	steering, _ := capabilities.Meta["steering"].(map[string]any)
	if err != nil || steering["supported"] != false {
		t.Fatalf("legacy steering capability = %+v, error = %v", steering, err)
	}
	first := launchPrompt(t, client, ctx, "first")
	scope := receive(t, ctx, opened)
	if scope.HostInvocationID == "" {
		t.Fatal("legacy foreground input was not associated")
	}
	// Let teardown complete if one of the overlapping operations fails this assertion.
	release := filepath.Join(filepath.Dir(input), "finish-legacy")
	defer os.WriteFile(release, nil, 0600)
	if _, err := client.ReservePrompt(ctx, client.prompt("overlapping prompt"), agentclient.PromptOptions{}); err == nil {
		t.Fatal("legacy prompt overlapped a foreground invocation")
	}
	_, err = client.Steer(ctx, agentclient.SteerRequest{
		SessionID: client.id, Target: scope, Prompt: []acp.ContentBlock{acp.TextBlock("overlapping steer")},
		Options: agentclient.PromptOptions{BeforeSubmit: func(context.Context, agentclient.PromptSubmission) error {
			t.Error("legacy steer reached the host admission gate")
			return nil
		}},
	})
	if err == nil {
		t.Fatal("legacy priority input overlapped a foreground invocation")
	}
	if err := os.WriteFile(release, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if result := receive(t, ctx, first); result.Outcome == nil || result.Receipt.OperationError != nil {
		t.Fatalf("legacy foreground result = %+v", result)
	}
}

func TestLegacyReplayRejectsKnownTaskWithoutBackgroundSnapshot(t *testing.T) {
	ready := make(chan struct{}, 1)
	client, _, ctx := startMode(t, agentclient.HostCallbacks{Observe: func(_ context.Context, _ agentclient.Scope, notification acp.SessionNotification) error {
		if update := notification.Update.AvailableCommandsUpdate; update != nil && len(update.AvailableCommands) == 1 &&
			update.AvailableCommands[0].Name == "task-observed" {
			select {
			case ready <- struct{}{}:
			default:
			}
		}
		return nil
	}}, "legacy-echo-task-started", nil)
	receive(t, ctx, ready)
	if _, err := client.ReservePrompt(ctx, client.prompt("overlapping prompt"), agentclient.PromptOptions{}); err == nil {
		t.Fatal("legacy prompt overlapped an already reported native task")
	}
}

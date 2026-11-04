package nativeacp

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	acp "github.com/JieWaZi/acp-go/sdk"
)

// promptEncodingGate exposes the interval after preparation and before wire admission.
type promptEncodingGate struct {
	// entered signals that the SDK began marshaling the request.
	entered chan struct{}
	// release finishes marshaling without altering transport code.
	release chan struct{}
}

func (gate promptEncodingGate) MarshalJSON() ([]byte, error) {
	close(gate.entered)
	<-gate.release
	return []byte(`true`), nil
}

func TestPromptCancellationDuringEncodingNeverSubmits(t *testing.T) {
	agent, peer, host := startLifecycleBridge(t, true, true)
	gate := promptEncodingGate{entered: make(chan struct{}), release: make(chan struct{})}
	done := make(chan error, 1)
	go func() {
		_, delivery, err := agent.PromptWithReceipt(context.Background(), acp.PromptRequest{SessionId: "a", Prompt: []acp.ContentBlock{acp.TextBlock("cancel-before-write")}, Meta: map[string]any{"gate": gate}})
		if delivery != DeliveryNotSent {
			done <- errors.New("cancelled operation wrote a native frame")
			return
		}
		var protocol *acp.RequestError
		if !errors.As(err, &protocol) || protocol.Code != -32800 {
			done <- errors.New("missing precommit cancellation result")
			return
		}
		done <- nil
	}()
	awaitGate(t, gate.entered)
	if err := agent.Cancel(context.Background(), acp.CancelNotification{SessionId: "a"}); err != nil {
		t.Fatal(err)
	}
	close(gate.release)
	awaitPrompt(t, done, false)
	raw, err := peer.CallExtension(context.Background(), "_cancels", nil)
	if err != nil || string(raw) != `[]` {
		t.Fatalf("precommit cancel reached native: %s %v", raw, err)
	}
	next := promptAgentAsync(agent, context.Background(), "a", "next")
	expectUpdate(t, host, "a:next")
	releaseNative(t, peer, "a")
	awaitPrompt(t, next, false)
}

func TestPromptUnacknowledgedCancelInvalidatesStream(t *testing.T) {
	agent, peer, host := startLifecycleBridge(t, true, true)
	if _, err := peer.CallExtension(context.Background(), "_ignore_cancel", nil); err != nil {
		t.Fatal(err)
	}
	first := promptAgentAsync(agent, context.Background(), "a", "first")
	expectUpdate(t, host, "a:first")
	next := promptAgentAsync(agent, context.Background(), "a", "next")
	waitQueue(t, agent, "a", 1)
	if err := agent.Cancel(context.Background(), acp.CancelNotification{SessionId: "a"}); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-first:
		if err == nil {
			t.Fatal("unacknowledged native turn succeeded")
		}
	case <-time.After(nativeStopGrace + 3*time.Second):
		t.Fatal("native cancellation deadline did not close the connection")
	}
	awaitPrompt(t, next, true)
	select {
	case update := <-host.updates:
		t.Fatalf("successor entered unknown native execution: %s", update)
	default:
	}
	if agent.conn.Err() == nil {
		t.Fatal("unacknowledged cancellation retained a reusable connection")
	}
	// Expected process termination has already been observed by the test.
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_ = agent.Close(ctx)
}

func TestPromptPreparationCancellationPreservesFailure(t *testing.T) {
	agent, peer, host := startLifecycleBridge(t, true, true)
	entered := make(chan struct{})
	agent.config.PreparePrompt = func(ctx context.Context, _ acp.PromptRequest) error {
		close(entered)
		<-ctx.Done()
		return &acp.RequestError{Code: -32042, Message: "configuration outcome unknown", Data: map[string]any{"phase": "prepare"}}
	}
	done := make(chan error, 1)
	go func() {
		_, delivery, err := agent.PromptWithReceipt(context.Background(), acp.PromptRequest{SessionId: "a", Prompt: []acp.ContentBlock{acp.TextBlock("prepare")}})
		var protocol *acp.RequestError
		if delivery != DeliveryNotSent || !errors.As(err, &protocol) || protocol.Code != -32042 {
			done <- errors.New("preparation failure lost its phase or identity")
			return
		}
		done <- nil
	}()
	awaitGate(t, entered)
	if err := agent.Cancel(context.Background(), acp.CancelNotification{SessionId: "a"}); err != nil {
		t.Fatal(err)
	}
	awaitPrompt(t, done, false)
	agent.config.PreparePrompt = nil
	next := promptAgentAsync(agent, context.Background(), "a", "next")
	expectUpdate(t, host, "a:next")
	releaseNative(t, peer, "a")
	awaitPrompt(t, next, false)
}

func TestPromptReservationPrecedesReadinessAndCancellation(t *testing.T) {
	agent, peer, host := startLifecycleBridge(t, true, true)
	prepared := false
	run, err := agent.ReservePromptWithReceipt(context.Background(), acp.PromptRequest{SessionId: "a", Prompt: []acp.ContentBlock{acp.TextBlock("never-submitted")}}, func(context.Context) (func(), error) { prepared = true; return nil, nil })
	if err != nil {
		t.Fatal(err)
	}
	cancelRun, err := agent.BeginPromptCancel(acp.CancelNotification{SessionId: "a"})
	if err != nil || cancelRun != nil {
		t.Fatalf("precommit cancel reserved native write: %v", err)
	}
	_, delivery, err := run()
	var requestErr *acp.RequestError
	if delivery != DeliveryNotSent || !errors.As(err, &requestErr) || requestErr.Code != -32800 || prepared {
		t.Fatalf("canceled reservation: delivery=%s prepared=%v err=%v", delivery, prepared, err)
	}
	if _, _, err = run(); err == nil {
		t.Fatal("one-shot prompt runner executed twice")
	}
	raw, err := peer.CallExtension(context.Background(), "_cancels", nil)
	if err != nil || string(raw) != `[]` {
		t.Fatalf("precommit cancellation reached CLI: %s %v", raw, err)
	}
	next := promptAgentAsync(agent, context.Background(), "a", "next")
	expectUpdate(t, host, "a:next")
	releaseNative(t, peer, "a")
	awaitPrompt(t, next, false)
}

func TestPromptPreparationCleanupPrecedesSuccessor(t *testing.T) {
	agent, peer, host := startLifecycleBridge(t, true, true)
	entered, cleaning, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var unblock sync.Once
	defer unblock.Do(func() { close(release) })
	run, err := agent.ReservePromptWithReceipt(context.Background(), acp.PromptRequest{SessionId: "a"}, func(ctx context.Context) (func(), error) {
		close(entered)
		<-ctx.Done()
		return func() { close(cleaning); <-release }, ctx.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, _, err := run(); done <- err }()
	awaitGate(t, entered)
	next := promptAgentAsync(agent, context.Background(), "a", "next")
	waitQueue(t, agent, "a", 1)
	if err := agent.Cancel(context.Background(), acp.CancelNotification{SessionId: "a"}); err != nil {
		t.Fatal(err)
	}
	awaitGate(t, cleaning)
	select {
	case update := <-host.updates:
		t.Fatalf("successor bypassed preparation cleanup: %s", update)
	default:
	}
	unblock.Do(func() { close(release) })
	awaitPrompt(t, done, true)
	expectUpdate(t, host, "a:next")
	releaseNative(t, peer, "a")
	awaitPrompt(t, next, false)
}

func TestCapturedCancelRetainsOwnerUntilContinuation(t *testing.T) {
	agent, peer, host := startLifecycleBridge(t, true, true)
	terminal := make(chan acp.PromptResponse, 1)
	failed := make(chan error, 1)
	go func() {
		response, err := agent.Prompt(context.Background(), acp.PromptRequest{SessionId: "a", Prompt: []acp.ContentBlock{acp.TextBlock("first")}})
		terminal <- response
		failed <- err
	}()
	expectUpdate(t, host, "a:first")
	run, err := agent.BeginPromptCancel(acp.CancelNotification{SessionId: "a"})
	if err != nil || run == nil {
		t.Fatalf("missing committed cancellation runner: %v", err)
	}
	next := promptAgentAsync(agent, context.Background(), "a", "next")
	waitQueue(t, agent, "a", 1)
	if err := run(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case response := <-terminal:
		if response.StopReason != acp.StopReasonCancelled {
			t.Fatalf("native terminal replaced: %+v", response)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("native terminal missing")
	}
	awaitPrompt(t, failed, false)
	expectUpdate(t, host, "a:next")
	if err := run(context.Background()); err != nil {
		t.Fatal(err)
	}
	raw, err := peer.CallExtension(context.Background(), "_cancels", nil)
	if err != nil || string(raw) != `["a"]` {
		t.Fatalf("captured cancellation reached successor: %s %v", raw, err)
	}
	releaseNative(t, peer, "a")
	awaitPrompt(t, next, false)
}

// failingPromptWriter returns a precise byte receipt alongside a transport failure.
type failingPromptWriter struct {
	// full selects a complete frame rather than a zero-byte failure.
	full bool
}

func (w failingPromptWriter) Write(frame []byte) (int, error) {
	if w.full {
		return len(frame), io.ErrClosedPipe
	}
	return 0, io.ErrClosedPipe
}

func TestPromptReceiptUsesByteCountOnWriteError(t *testing.T) {
	for _, check := range []struct {
		// name identifies the byte boundary.
		name string
		// full selects full delivery.
		full bool
		// want is the locally observed delivery.
		want PromptDelivery
	}{
		{"zero", false, DeliveryNotSent}, {"full", true, DeliverySent},
	} {
		t.Run(check.name, func(t *testing.T) {
			reader, output := io.Pipe()
			defer output.Close()
			conn, err := acp.NewConnectionWithOptions(nil, failingPromptWriter{full: check.full}, reader, acp.ConnectionOptions{Context: context.Background(), NotificationQueueCapacity: 4, NotificationQueueBytes: 4096, CloseTransport: reader.Close})
			if err != nil {
				t.Fatal(err)
			}
			done := make(chan struct{})
			close(done)
			agent := &Agent{conn: conn, done: done, closed: make(chan struct{}), sessions: map[acp.SessionId]*sessionOptions{"a": {queue: &promptQueue{}}}, turnOwners: map[acp.SessionId]*promptWaiter{}}
			_, receipt, err := agent.PromptWithReceipt(context.Background(), acp.PromptRequest{SessionId: "a"})
			if err == nil || receipt != check.want || conn.Err() == nil {
				t.Fatalf("got receipt=%s error=%v connection=%v, want receipt=%s and write/connection error", receipt, err, conn.Err(), check.want)
			}
		})
	}
}

func TestCapturedCancelAbortedContinuationReleasesOwner(t *testing.T) {
	agent, _, host := startLifecycleBridge(t, true, true)
	first := promptAgentAsync(agent, context.Background(), "a", "first")
	expectUpdate(t, host, "a:first")
	run, err := agent.BeginPromptCancel(acp.CancelNotification{SessionId: "a"})
	if err != nil || run == nil {
		t.Fatalf("capture: %v", err)
	}
	next := promptAgentAsync(agent, context.Background(), "a", "next")
	waitQueue(t, agent, "a", 1)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := run(ctx); err == nil {
		t.Fatal("aborted native cancel write was hidden")
	}
	awaitPrompt(t, first, true)
	awaitPrompt(t, next, true)
	agent.mutex.Lock()
	owner := agent.turnOwners["a"]
	agent.mutex.Unlock()
	if owner != nil {
		t.Fatal("aborted cancellation stranded the execution owner")
	}
	select {
	case update := <-host.updates:
		t.Fatalf("successor entered invalid native connection: %s", update)
	default:
	}
	// The connection was intentionally invalidated; observe termination before fixture cleanup.
	closeCtx, closeCancel := context.WithTimeout(context.Background(), time.Second)
	defer closeCancel()
	_ = agent.Close(closeCtx)

}

package nativeacp

import (
	"context"
	"errors"
	acp "github.com/JieWaZi/acp-go/sdk"
	"testing"
	"time"
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
	awaitPrompt(t, first, true)
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

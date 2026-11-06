package acp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"testing"
	"time"
)

// borrowedLifecycleReader deliberately has no Close method. Its owner must
// release an in-progress Read independently of the logical connection.
type borrowedLifecycleReader struct {
	// reading closes when the SDK enters its first physical read.
	reading chan struct{}
	// release ends that read when the caller gives up the borrowed transport.
	release chan struct{}
}

func (r borrowedLifecycleReader) Read([]byte) (int, error) {
	close(r.reading)
	<-r.release
	return 0, io.EOF
}

func TestAgentConnectionCloseCancelsPendingControlWithoutWaitingForBorrowedRead(t *testing.T) {
	input := borrowedLifecycleReader{reading: make(chan struct{}), release: make(chan struct{})}
	defer close(input.release)
	peer, output := io.Pipe()
	t.Cleanup(func() { _ = peer.Close(); _ = output.Close() })
	connection := NewAgentSideConnection(&agentFuncs{}, output, input)
	// Assert the public lifecycle boundary without making the RED stage a
	// compile failure against an SDK that has not exposed it yet.
	closer, ok := any(connection).(interface{ Close() })
	if !ok {
		t.Fatal("agent-side connection has no explicit logical close")
	}
	defer closer.Close()
	receiveLifecycle(t, input.reading)
	control := make(chan error, 1)
	go func() {
		_, err := connection.CallExtension(context.Background(), "_pending", nil)
		control <- err
	}()
	var request anyMessage
	if err := json.NewDecoder(peer).Decode(&request); err != nil {
		t.Fatal(err)
	}
	closed := make(chan struct{})
	go func() { closer.Close(); closer.Close(); close(closed) }()
	receiveLifecycle(t, closed)
	receiveLifecycle(t, connection.Done())
	receiveLifecycle(t, connection.conn.inboundCtx.Done())
	if err := receiveLifecycle(t, control); err == nil {
		t.Fatal("logical close left a control request waiting for a peer reply")
	}
}

func TestAgentConnectionCloseCancelsActiveInboundPrompt(t *testing.T) {
	input, writer := io.Pipe()
	t.Cleanup(func() { _ = input.Close(); _ = writer.Close() })
	agent := &lifecycleTestAgent{ownLifecycle: true, started: make(chan context.Context, 1), cancelled: make(chan struct{}), closed: make(chan struct{})}
	connection := NewAgentSideConnection(agent, io.Discard, struct{ io.Reader }{input})
	closer, ok := any(connection).(interface{ Close() })
	if !ok {
		t.Fatal("agent-side connection has no explicit logical close")
	}
	defer closer.Close()
	if _, err := io.WriteString(writer, admissionPromptFrame); err != nil {
		t.Fatal(err)
	}
	promptCtx := receiveLifecycle(t, agent.started)
	closer.Close()
	receiveLifecycle(t, promptCtx.Done())
}

// lifecycleTestAgent exposes prompt request contexts and owns its native cancel result.
type lifecycleTestAgent struct {
	// Agent provides unused protocol methods.
	Agent
	// ownLifecycle selects adapter-owned or legacy SDK cancellation.
	ownLifecycle bool
	// started publishes the context of each prompt received over the wire.
	started chan context.Context
	// cancelled closes when the agent handles a session cancel notification.
	cancelled chan struct{}
	// closed closes when the agent handles a session close request.
	closed chan struct{}
}

// ManagesSessionLifecycle opts in to handling session-scoped interruption in the agent.
func (a *lifecycleTestAgent) ManagesSessionLifecycle() bool { return a.ownLifecycle }

// Prompt reports cancellation only after the owner or the real request context is done.
func (a *lifecycleTestAgent) Prompt(ctx context.Context, _ PromptRequest) (PromptResponse, error) {
	a.started <- ctx
	select {
	case <-ctx.Done():
		return PromptResponse{}, ctx.Err()
	case <-a.cancelled:
		return PromptResponse{StopReason: StopReasonCancelled}, nil
	case <-a.closed:
		return PromptResponse{StopReason: StopReasonEndTurn}, nil
	}
}

// Cancel supplies the native terminal result for the synthetic agent.
func (a *lifecycleTestAgent) Cancel(_ context.Context, _ CancelNotification) error {
	close(a.cancelled)
	return nil
}

// CloseSession supplies a native end-of-session result.
func (a *lifecycleTestAgent) CloseSession(_ context.Context, _ CloseSessionRequest) (CloseSessionResponse, error) {
	close(a.closed)
	return CloseSessionResponse{}, nil
}

// startLifecycleConnection creates paired ACP wire endpoints with no external process.
func startLifecycleConnection(t *testing.T, ownLifecycle bool) (*ClientSideConnection, *lifecycleTestAgent, net.Conn) {
	t.Helper()
	client, server := net.Pipe()
	t.Cleanup(func() { _ = client.Close(); _ = server.Close() })
	agent := &lifecycleTestAgent{ownLifecycle: ownLifecycle, started: make(chan context.Context, 2), cancelled: make(chan struct{}), closed: make(chan struct{})}
	NewAgentSideConnection(agent, server, server)
	return NewClientSideConnection(nil, client, client), agent, client
}

// receiveLifecycle waits with a test deadline without changing the production request context.
func receiveLifecycle[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case value := <-ch:
		return value
	case <-time.After(time.Second):
		t.Fatal("ACP lifecycle operation did not finish")
		var zero T
		return zero
	}
}

// TestAdapterOwnedSessionCancelKeepsPromptContext lets the adapter publish its native result.
func TestAdapterOwnedSessionCancelKeepsPromptContext(t *testing.T) {
	client, agent, _ := startLifecycleConnection(t, true)
	type result struct {
		// response contains the observed prompt outcome.
		response PromptResponse
		// err is the wire or native request error.
		err error
	}
	results := make(chan result, 2)
	var promptContexts []context.Context
	for i := 0; i < 2; i++ {
		go func() {
			response, err := client.Prompt(context.Background(), PromptRequest{SessionId: "session", Prompt: []ContentBlock{}})
			results <- result{response, err}
		}()
		promptContexts = append(promptContexts, receiveLifecycle(t, agent.started))
	}
	for _, prompt := range promptContexts {
		if prompt.Err() != nil {
			t.Fatal("second prompt cancelled an earlier adapter-owned request")
		}
	}
	if err := client.Cancel(context.Background(), CancelNotification{SessionId: "session"}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		got := receiveLifecycle(t, results)
		if got.err != nil || got.response.StopReason != StopReasonCancelled {
			t.Fatalf("SDK replaced the native terminal: %+v", got)
		}
	}
}

// TestLegacySessionCancelStillCancelsPromptContext protects callers that do not opt in.
func TestLegacySessionCancelStillCancelsPromptContext(t *testing.T) {
	client, agent, _ := startLifecycleConnection(t, false)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = client.Prompt(context.Background(), PromptRequest{SessionId: "session", Prompt: []ContentBlock{}})
	}()
	prompt := receiveLifecycle(t, agent.started)
	if err := client.Cancel(context.Background(), CancelNotification{SessionId: "session"}); err != nil {
		t.Fatal(err)
	}
	receiveLifecycle(t, prompt.Done())
	receiveLifecycle(t, done)
}

// TestAdapterOwnedSessionRespectsRequestCancelAndClose checks connection-level cancellation is preserved.
func TestAdapterOwnedSessionRespectsRequestCancelAndClose(t *testing.T) {
	t.Run("caller request cancellation", func(t *testing.T) {
		client, agent, _ := startLifecycleConnection(t, true)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := make(chan error, 1)
		go func() {
			_, err := client.Prompt(ctx, PromptRequest{SessionId: "session", Prompt: []ContentBlock{}})
			done <- err
		}()
		prompt := receiveLifecycle(t, agent.started)
		cancel()
		receiveLifecycle(t, prompt.Done())
		err := receiveLifecycle(t, done)
		var failure *RequestError
		if !errors.Is(err, context.Canceled) && (!errors.As(err, &failure) || failure.Code != NewRequestCancelled(nil).Code) {
			t.Fatalf("caller cancellation = %v", err)
		}
	})
	t.Run("session close", func(t *testing.T) {
		client, agent, _ := startLifecycleConnection(t, true)
		done := make(chan error, 1)
		go func() {
			result, err := client.Prompt(context.Background(), PromptRequest{SessionId: "session", Prompt: []ContentBlock{}})
			if err == nil && result.StopReason != StopReasonEndTurn {
				err = errors.New("session close erased the adapter's terminal")
			}
			done <- err
		}()
		receiveLifecycle(t, agent.started)
		if _, err := client.CloseSession(context.Background(), CloseSessionRequest{SessionId: "session"}); err != nil {
			t.Fatal(err)
		}
		if err := receiveLifecycle(t, done); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("peer close", func(t *testing.T) {
		client, agent, pipe := startLifecycleConnection(t, true)
		done := make(chan struct{})
		go func() {
			defer close(done)
			_, _ = client.Prompt(context.Background(), PromptRequest{SessionId: "session", Prompt: []ContentBlock{}})
		}()
		prompt := receiveLifecycle(t, agent.started)
		if err := pipe.Close(); err != nil {
			t.Fatal(err)
		}
		receiveLifecycle(t, prompt.Done())
		receiveLifecycle(t, done)
	})
}

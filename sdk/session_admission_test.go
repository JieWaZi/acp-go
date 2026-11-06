package acp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const (
	admissionPromptFrame = `{"jsonrpc":"2.0","id":1,"method":"session/prompt","params":{"sessionId":"session","prompt":[]}}` + "\n"
	admissionCancelFrame = `{"jsonrpc":"2.0","method":"session/cancel","params":{"sessionId":"session"}}` + "\n"
)

type admissionTestAgent struct {
	// Agent supplies unused methods for the SDK's standard handler.
	Agent
	// fallback counts calls that bypass the explicitly installed owner.
	fallback atomic.Int32
}

func (a *admissionTestAgent) Prompt(context.Context, PromptRequest) (PromptResponse, error) {
	a.fallback.Add(1)
	return PromptResponse{StopReason: StopReasonEndTurn}, nil
}

func (a *admissionTestAgent) Cancel(context.Context, CancelNotification) error {
	a.fallback.Add(1)
	return nil
}

func admissionTestPipe(t *testing.T) (net.Conn, net.Conn) {
	t.Helper()
	client, server := net.Pipe()
	t.Cleanup(func() { _ = client.Close(); _ = server.Close() })
	if err := client.SetDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	return client, server
}

func TestSessionAdmissionCapturesAdjacentCancelDuringPreparation(t *testing.T) {
	client, server := admissionTestPipe(t)
	agent := &admissionTestAgent{}
	prepared := make(chan struct{})
	var owner context.CancelFunc
	var order []string // Both hooks run on the single reader.
	NewAgentSideConnectionWithBinder(agent, server, server, func(connection *AgentSideConnection) {
		connection.SetPromptPreparation(func(request PromptRequest) PromptPreparation {
			order = append(order, "capture:"+string(request.SessionId))
			return func(ctx context.Context) (func(), error) {
				<-ctx.Done()
				close(prepared)
				return nil, ctx.Err()
			}
		})
		connection.SetSessionAdmission(SessionAdmission{
			Prompt: func(ctx context.Context, _ PromptRequest, prepare PromptPreparation) (func() (PromptResponse, error), error) {
				order = append(order, "prompt")
				ownerCtx, cancel := context.WithCancel(ctx)
				owner = cancel
				return func() (PromptResponse, error) {
					defer cancel()
					if _, err := prepare(ownerCtx); !errors.Is(err, context.Canceled) || ctx.Err() != nil {
						return PromptResponse{}, errors.New("session stop canceled the wrong scope")
					}
					return PromptResponse{StopReason: StopReasonCancelled}, nil
				}, nil
			},
			Cancel: func(CancelNotification) (func(context.Context) error, error) {
				order = append(order, "cancel")
				if owner == nil {
					return nil, errors.New("cancel missed the previous prompt")
				}
				owner()
				return nil, nil
			},
		})
	})
	if _, err := io.WriteString(client, admissionPromptFrame+admissionCancelFrame); err != nil {
		t.Fatal(err)
	}
	var response anyMessage
	if err := json.NewDecoder(client).Decode(&response); err != nil {
		t.Fatal(err)
	}
	receiveLifecycle(t, prepared)
	if response.Error != nil || string(response.Result) != `{"stopReason":"cancelled"}` {
		t.Fatalf("prompt terminal = %s, %v", response.Result, response.Error)
	}
	if got := strings.Join(order, ","); got != "capture:session,prompt,cancel" {
		t.Fatalf("reader dispatched %q", got)
	}
	if got := agent.fallback.Load(); got != 0 {
		t.Fatalf("legacy dispatch called %d times", got)
	}
}

func TestSessionAdmissionValidatesBeforeCaptureAndHandlesIdleCancel(t *testing.T) {
	client, server := admissionTestPipe(t)
	agent := &admissionTestAgent{}
	observed := make(chan string, 10)
	connection := NewAgentSideConnectionWithBinder(agent, server, server, func(connection *AgentSideConnection) {
		connection.SetPromptPreparation(func(PromptRequest) PromptPreparation {
			observed <- "capture"
			return nil
		})
		connection.SetSessionAdmission(SessionAdmission{
			Prompt: func(context.Context, PromptRequest, PromptPreparation) (func() (PromptResponse, error), error) {
				observed <- "prompt"
				return func() (PromptResponse, error) {
					return PromptResponse{StopReason: StopReasonEndTurn}, nil
				}, nil
			},
			Cancel: func(CancelNotification) (func(context.Context) error, error) {
				observed <- "idle"
				return nil, nil
			},
		})
	})
	malformed := `{"jsonrpc":"2.0","id":2,"method":"session/prompt","params":{"sessionId":"session"}}` + "\n" +
		`{"jsonrpc":"2.0","id":3,"method":"session/prompt","params":{"prompt":[]}}` + "\n" +
		`{"jsonrpc":"2.0","method":"session/cancel","params":null}` + "\n" +
		`{"jsonrpc":"2.0","id":4,"method":"session/cancel","params":{"sessionId":"session"}}` + "\n" +
		`{"jsonrpc":"1.0","id":5,"method":"session/prompt","params":{"sessionId":"session","prompt":[]}}` + "\n" +
		`{"jsonrpc":"2.0","method":"session/prompt","params":{"sessionId":"session","prompt":[]}}` + "\n"
	if _, err := io.WriteString(client, malformed+admissionCancelFrame+admissionPromptFrame); err != nil {
		t.Fatal(err)
	}
	codes := map[string]int{"1": 0, "2": -32602, "3": -32602, "4": -32600, "5": -32600}
	decoder := json.NewDecoder(client)
	for len(codes) != 0 {
		var response anyMessage
		if err := decoder.Decode(&response); err != nil {
			t.Fatal(err)
		}
		if response.ID == nil {
			t.Fatalf("unexpected response with no id: %+v", response)
		}
		id := string(*response.ID)
		want, ok := codes[id]
		got := 0
		if response.Error != nil {
			got = response.Error.Code
		}
		if !ok || got != want {
			t.Fatalf("response %s returned code %d, want %d", id, got, want)
		}
		delete(codes, id)
	}
	for _, want := range []string{"idle", "capture", "prompt"} {
		if got := receiveLifecycle(t, observed); got != want {
			t.Fatalf("reader dispatched %q, want %q", got, want)
		}
	}
	select {
	case got := <-observed:
		t.Fatalf("reader dispatched malformed frame: %q", got)
	default:
	}
	connection.conn.notifyMu.Lock()
	watermark := connection.conn.lastEnqueuedNotificationSeq
	connection.conn.notifyMu.Unlock()
	connection.conn.waitForNotificationDrain(watermark, time.Second)
	if got := agent.fallback.Load(); got != 0 {
		t.Fatalf("legacy dispatch called %d times", got)
	}
}

func TestSessionAdmissionRunsCapturedCancelAfterEnqueueFailure(t *testing.T) {
	agent := &AgentSideConnection{}
	released := make(chan error, 2)
	agent.SetSessionAdmission(SessionAdmission{
		Prompt: func(context.Context, PromptRequest, PromptPreparation) (func() (PromptResponse, error), error) {
			return nil, errors.New("unexpected prompt")
		},
		Cancel: func(CancelNotification) (func(context.Context) error, error) {
			return func(ctx context.Context) error {
				released <- context.Cause(ctx)
				return nil
			}, nil
		},
	})
	const holdFrame = `{"jsonrpc":"2.0","method":"_hold"}` + "\n"
	unblock := make(chan struct{})
	defer close(unblock)
	handler := func(context.Context, string, json.RawMessage) (any, *RequestError) {
		<-unblock
		return nil, nil
	}
	connection := newConnection(handler, agent.prepareSessionInbound, io.Discard, strings.NewReader(holdFrame+admissionCancelFrame), &ConnectionOptions{
		Context:                   context.Background(),
		NotificationQueueCapacity: 1,
		NotificationQueueBytes:    int64(len(holdFrame)),
		CloseTransport:            func() error { return nil },
	})
	connection.start()
	if err := receiveLifecycle(t, released); !errors.Is(err, ErrNotificationQueueLimit) {
		t.Fatalf("captured cancel did not observe enqueue failure: %v", err)
	}
	if connection.inboundCtx.Err() != nil {
		t.Fatal("cancel waited for earlier notifications to drain")
	}
}

func TestConnectionCloseAbortsQueuedNotificationsAndReleasesCapturedCancel(t *testing.T) {
	input, writer := io.Pipe()
	entered, captured := make(chan struct{}), make(chan struct{})
	released := make(chan error, 2)
	var ordinary atomic.Int32
	agent := &AgentSideConnection{}
	agent.SetSessionAdmission(SessionAdmission{
		Prompt: func(context.Context, PromptRequest, PromptPreparation) (func() (PromptResponse, error), error) {
			return nil, errors.New("unexpected prompt")
		},
		Cancel: func(CancelNotification) (func(context.Context) error, error) {
			return func(ctx context.Context) error { released <- context.Cause(ctx); return nil }, nil
		},
	})
	handler := func(ctx context.Context, method string, _ json.RawMessage) (any, *RequestError) {
		if method == "_hold" {
			close(entered)
			<-ctx.Done()
		} else {
			ordinary.Add(1)
		}
		return nil, nil
	}
	prepare := func(ctx context.Context, message *anyMessage) inboundContinuation {
		if message.Method == "_fence" {
			close(captured)
		}
		return agent.prepareSessionInbound(ctx, message)
	}
	connection := newConnection(handler, prepare, io.Discard, struct{ io.Reader }{input}, nil)
	t.Cleanup(func() { connection.Close(); _ = input.Close(); _ = writer.Close() })
	workerDone, readerDone := make(chan struct{}), make(chan struct{})
	go func() { connection.processNotifications(); close(workerDone) }()
	go func() { connection.receive(); close(readerDone) }()
	frames := `{"jsonrpc":"2.0","method":"_hold"}` + "\n" +
		`{"jsonrpc":"2.0","method":"_ordinary"}` + "\n" + admissionCancelFrame +
		`{"jsonrpc":"2.0","method":"_fence"}` + "\n"
	if _, err := io.WriteString(writer, frames); err != nil {
		t.Fatal(err)
	}
	receiveLifecycle(t, entered)
	receiveLifecycle(t, captured)
	connection.notifyMu.Lock()
	target := connection.lastEnqueuedNotificationSeq
	connection.notifyMu.Unlock()
	drainDone := make(chan struct{})
	go func() { connection.waitForNotificationDrain(target, time.Hour); close(drainDone) }()
	connection.Close()
	receiveLifecycle(t, workerDone)
	receiveLifecycle(t, drainDone)
	if err := receiveLifecycle(t, released); !errors.Is(err, context.Canceled) {
		t.Fatalf("captured cancel lost owner abort: %v", err)
	}
	if got := ordinary.Load(); got != 0 {
		t.Fatalf("owner close dispatched %d queued ordinary notifications", got)
	}
	// Only the caller can unblock its borrowed reader. No later frame may
	// acquire a new operation after the logical connection was closed.
	if _, err := io.WriteString(writer, admissionCancelFrame); err != nil {
		t.Fatal(err)
	}
	_ = writer.Close()
	receiveLifecycle(t, readerDone)
	select {
	case <-released:
		t.Fatal("captured cancel ran more than once or a post-close frame was admitted")
	default:
	}
}

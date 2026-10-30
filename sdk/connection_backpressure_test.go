package acp

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func backpressureTestClient(t *testing.T, client Client, count int, bytes int64) (*ClientSideConnection, *AgentSideConnection, context.CancelFunc) {
	t.Helper()
	cr, cw := io.Pipe()
	ar, aw := io.Pipe()
	owner, cancel := context.WithCancel(context.Background())
	cc, err := NewClientSideConnectionWithOptions(client, cw, ar, ConnectionOptions{Context: owner, NotificationQueueCapacity: count, NotificationQueueBytes: bytes, CloseTransport: func() error { return errors.Join(cr.Close(), cw.Close(), ar.Close(), aw.Close()) }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cancel(); cc.Close() })
	return cc, NewAgentSideConnection(&agentFuncs{}, aw, cr), cancel
}
func TestBackpressurePreservesBurstAndResponseBarrier(t *testing.T) {
	var delivered atomic.Int64
	release := make(chan struct{})
	client := &clientFuncs{RequestPermissionFunc: func(context.Context, RequestPermissionRequest) (RequestPermissionResponse, error) {
		return RequestPermissionResponse{Outcome: NewRequestPermissionOutcomeCancelled()}, nil
	}, SessionUpdateFunc: func(ctx context.Context, _ SessionNotification) error {
		if delivered.Load() == 0 {
			select {
			case <-release:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		delivered.Add(1)
		return nil
	}}
	cc, ac, _ := backpressureTestClient(t, client, 4, 4096)
	done := make(chan error, 1)
	ac.agent.(*agentFuncs).PromptFunc = func(ctx context.Context, req PromptRequest) (PromptResponse, error) {
		for i := 0; i < 2200; i++ {
			if err := ac.SessionUpdate(ctx, testSessionUpdate(req.SessionId, i)); err != nil {
				return PromptResponse{}, err
			}
		}
		_, err := ac.RequestPermission(ctx, RequestPermissionRequest{SessionId: req.SessionId, ToolCall: ToolCallUpdate{ToolCallId: "tool"}, Options: []PermissionOption{}})
		return PromptResponse{StopReason: StopReasonEndTurn}, err
	}
	go func() {
		_, err := cc.Prompt(context.Background(), PromptRequest{SessionId: "s", Prompt: []ContentBlock{TextBlock("go")}})
		done <- err
	}()
	select {
	case err := <-done:
		t.Fatalf("burst did not wait: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	close(release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("burst/permission deadlocked")
	}
	if delivered.Load() != 2200 {
		t.Fatalf("delivered=%d", delivered.Load())
	}
}
func TestBackpressureOwnerCancelUnblocksReadWriteAndHandler(t *testing.T) {
	entered := make(chan struct{})
	client := &clientFuncs{SessionUpdateFunc: func(ctx context.Context, _ SessionNotification) error {
		select {
		case <-entered:
		default:
			close(entered)
		}
		<-ctx.Done()
		return ctx.Err()
	}}
	cc, ac, cancel := backpressureTestClient(t, client, 1, 4096)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 100; i++ {
			if ac.SessionUpdate(context.Background(), testSessionUpdate("s", 1)) != nil {
				return
			}
		}
	}()
	<-entered
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("writer blocked")
	}
	select {
	case <-cc.Done():
	case <-time.After(time.Second):
		t.Fatal("connection alive")
	}
	if !errors.Is(cc.Err(), context.Canceled) {
		t.Fatalf("cause=%v", cc.Err())
	}
}
func TestBackpressureRejectsNotificationLargerThanByteBudget(t *testing.T) {
	cc, ac, _ := backpressureTestClient(t, &clientFuncs{}, 4, 512)
	_ = ac.SessionUpdate(context.Background(), SessionNotification{SessionId: "s", Update: UpdateAgentMessageText(strings.Repeat("x", 1024))})
	select {
	case <-cc.Done():
	case <-time.After(time.Second):
		t.Fatal("oversized notification waited forever")
	}
	if !errors.Is(cc.Err(), ErrNotificationQueueLimit) {
		t.Fatalf("cause=%v", cc.Err())
	}
}

func TestBackpressureByteBudgetAppliesBeforeCountBudget(t *testing.T) {
	release := make(chan struct{})
	entered := make(chan struct{})
	client := &clientFuncs{SessionUpdateFunc: func(ctx context.Context, _ SessionNotification) error {
		select {
		case <-entered:
		default:
			close(entered)
		}
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}}
	cc, ac, _ := backpressureTestClient(t, client, 8, 512)
	done := make(chan error, 1)
	go func() {
		for i := 0; i < 3; i++ {
			if err := ac.SessionUpdate(context.Background(), SessionNotification{SessionId: "s", Update: UpdateAgentMessageText(strings.Repeat("x", 180))}); err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}()
	<-entered
	select {
	case err := <-done:
		t.Fatalf("byte-limited queue did not apply backpressure: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	close(release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("byte budget was not returned")
	}
	waitForNotificationBarrierDrain(t, cc.conn, time.Second)
}

func TestBackpressureOwnerCancelUnblocksOutboundRequestWrite(t *testing.T) {
	cr, cw := io.Pipe()
	ar, aw := io.Pipe()
	owner, cancel := context.WithCancel(context.Background())
	cc, err := NewClientSideConnectionWithOptions(&clientFuncs{}, cw, ar, ConnectionOptions{Context: owner, NotificationQueueCapacity: 1, NotificationQueueBytes: 4096, CloseTransport: func() error { return errors.Join(cr.Close(), cw.Close(), ar.Close(), aw.Close()) }})
	if err != nil {
		t.Fatal(err)
	}
	defer cc.Close()
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := cc.Prompt(context.Background(), PromptRequest{SessionId: "s", Prompt: []ContentBlock{TextBlock("go")}})
		done <- err
	}()
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("write unexpectedly succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("owner cancellation left request blocked in Write")
	}
}

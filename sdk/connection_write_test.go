package acp

import (
	"context"
	"io"
	"sync/atomic"
	"testing"
	"time"
)

// shortFrameWriter models a transport that accepts only a prefix without returning an error.
type shortFrameWriter struct{ writes atomic.Int32 }

// Write reports an incomplete frame to exercise stream invalidation.
func (w *shortFrameWriter) Write(p []byte) (int, error) {
	w.writes.Add(1)
	return len(p) / 2, nil
}

// TestConnectionShortWriteInvalidatesStream prevents a truncated request from sharing a live stream.
func TestConnectionShortWriteInvalidatesStream(t *testing.T) {
	reader, peer := io.Pipe()
	defer peer.Close()
	writer := &shortFrameWriter{}
	connection, err := NewConnectionWithOptions(nil, writer, reader, ConnectionOptions{
		Context: context.Background(), NotificationQueueCapacity: 4, NotificationQueueBytes: 4096,
		CloseTransport: reader.Close,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err = SendRequest[map[string]any](connection, ctx, "session/prompt", map[string]string{"sessionId": "s"})
	if err == nil || connection.Err() == nil {
		t.Fatalf("partial frame left stream usable: request=%v connection=%v", err, connection.Err())
	}
}

// encodingGate holds JSON preparation before the write gate can be acquired.
type encodingGate struct {
	// entered signals that parameter encoding has begun.
	entered chan struct{}
	// release allows the encoder to return.
	release chan struct{}
}

func (gate encodingGate) MarshalJSON() ([]byte, error) {
	close(gate.entered)
	<-gate.release
	return []byte(`{"sessionId":"s"}`), nil
}

func TestConnectionCancelDuringEncodingDoesNotWrite(t *testing.T) {
	reader, peer := io.Pipe()
	defer peer.Close()
	writer := &shortFrameWriter{}
	connection := NewConnection(nil, writer, reader)
	defer connection.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	gate := encodingGate{entered: make(chan struct{}), release: make(chan struct{})}
	done := make(chan error, 1)
	go func() {
		_, err := SendRequest[map[string]any](connection, ctx, "session/prompt", gate)
		done <- err
	}()
	<-gate.entered
	cancel()
	close(gate.release)
	select {
	case err := <-done:
		if err == nil || writer.writes.Load() != 0 || connection.Err() != nil {
			t.Fatalf("cancelled preparation reached wire: err=%v writes=%d connection=%v", err, writer.writes.Load(), connection.Err())
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled request remained blocked")
	}
}

func TestConnectionRejectedCommitDoesNotWrite(t *testing.T) {
	reader, peer := io.Pipe()
	defer peer.Close()
	writer := &shortFrameWriter{}
	connection := NewConnection(nil, writer, reader)
	defer connection.Close()
	completed := false
	_, err := SendRequestWithWriteHooks[map[string]any](connection, context.Background(), "session/prompt", nil, RequestWriteHooks{
		Commit:   func() error { return context.Canceled },
		Complete: func(RequestWriteResult) { completed = true },
	})
	if err == nil || completed || writer.writes.Load() != 0 || connection.Err() != nil {
		t.Fatalf("rejected commit wrote bytes: err=%v complete=%t writes=%d", err, completed, writer.writes.Load())
	}
}

func TestConnectionShortWriteReceipt(t *testing.T) {
	reader, peer := io.Pipe()
	defer peer.Close()
	connection := NewConnection(nil, &shortFrameWriter{}, reader)
	defer connection.Close()
	var result RequestWriteResult
	_, err := SendRequestWithWriteHooks[map[string]any](connection, context.Background(), "session/prompt", nil, RequestWriteHooks{
		Complete: func(value RequestWriteResult) { result = value },
	})
	if err == nil || result.BytesWritten == 0 || result.BytesWritten >= result.FrameBytes || result.Err != io.ErrShortWrite {
		t.Fatalf("missing partial delivery evidence: err=%v result=%+v", err, result)
	}
}

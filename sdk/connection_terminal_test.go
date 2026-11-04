package acp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

const (
	bufferedTerminalFrame = `{"jsonrpc":"2.0","id":1,"result":{"stopReason":"cancelled","usage":{"inputTokens":3,"outputTokens":4,"totalTokens":7}}}` + "\n"
	terminalUpdateFrame   = `{"jsonrpc":"2.0","method":"_before_terminal"}` + "\n"
)

type responseThenEOFWriter struct {
	// frames are the complete responses and notifications preceding peer EOF.
	frames string
	// response delivers the peer frames to the connection's reader.
	response *io.PipeWriter
	// conn lets the writer wait until the reader has observed EOF.
	conn *Connection
}

func (writer *responseThenEOFWriter) Write(frame []byte) (int, error) {
	if _, err := io.WriteString(writer.response, writer.frames); err != nil {
		return 0, err
	}
	_ = writer.response.Close()
	<-writer.conn.Done()
	return len(frame), nil
}

func terminalEOFConnection(t *testing.T, handler MethodHandler, frames string) *Connection {
	t.Helper()
	reader, output := io.Pipe()
	writer := &responseThenEOFWriter{frames: frames, response: output}
	conn, err := NewConnectionWithOptions(handler, writer, reader, ConnectionOptions{
		Context:                   context.Background(),
		NotificationQueueCapacity: 4,
		NotificationQueueBytes:    4096,
		CloseTransport:            reader.Close,
	})
	if err != nil {
		t.Fatal(err)
	}
	writer.conn = conn
	t.Cleanup(func() {
		conn.Close()
		_ = output.Close()
	})
	return conn
}

func assertBufferedTerminal(t *testing.T, response PromptResponse) {
	t.Helper()
	if response.StopReason != StopReasonCancelled || response.Usage == nil || response.Usage.TotalTokens != 7 {
		t.Fatalf("native terminal was lost: %+v", response)
	}
}

func TestConnectionBufferedTerminalSurvivesEOF(t *testing.T) {
	for attempt := 0; attempt < 32; attempt++ {
		conn := terminalEOFConnection(t, nil, bufferedTerminalFrame)
		terminal, err := SendRequest[PromptResponse](conn, context.Background(), AgentMethodSessionPrompt, nil)
		if err != nil {
			t.Fatalf("complete terminal returned a disconnect error: %v", err)
		}
		assertBufferedTerminal(t, terminal)
	}
}

func TestConnectionBufferedTerminalPreservesNotificationBarrierAfterEOF(t *testing.T) {
	tests := []struct {
		// name describes the outcome of the notification barrier.
		name string
		// abort cancels the connection while the notification is still blocked.
		abort bool
	}{
		{name: "notification delivered"},
		{name: "notification delivery interrupted", abort: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			entered := make(chan struct{})
			release := make(chan struct{})
			defer func() {
				select {
				case <-release:
				default:
					close(release)
				}
			}()
			handler := func(context.Context, string, json.RawMessage) (any, *RequestError) {
				close(entered)
				<-release
				return nil, nil
			}
			conn := terminalEOFConnection(t, handler, terminalUpdateFrame+bufferedTerminalFrame)
			type observation struct {
				// response is the terminal observed on the wire.
				response PromptResponse
				// err reports any transport or notification barrier failure.
				err error
			}
			done := make(chan observation, 1)
			go func() {
				response, err := SendRequest[PromptResponse](conn, context.Background(), AgentMethodSessionPrompt, nil)
				done <- observation{response, err}
			}()
			receiveLifecycle(t, entered)
			receiveLifecycle(t, conn.Done())
			select {
			case got := <-done:
				t.Fatalf("request bypassed its notification barrier after EOF: %+v", got)
			case <-time.After(20 * time.Millisecond):
			}
			if tt.abort {
				conn.Close()
			} else {
				close(release)
			}
			got := receiveLifecycle(t, done)
			assertBufferedTerminal(t, got.response)
			if tt.abort {
				var deliveryErr *RequestError
				if !errors.As(got.err, &deliveryErr) || !strings.Contains(deliveryErr.Error(), "waiting for pre-response notifications") {
					t.Fatalf("notification barrier failure was lost: %v", got.err)
				}
			} else if got.err != nil {
				t.Fatalf("delivered notification left a response error: %v", got.err)
			}
		})
	}
}

type fullWriteErrorWriter struct {
	// wire forwards the entire request to the peer.
	wire io.Writer
	// terminalObserved closes after the reader dispatches a frame following the terminal.
	terminalObserved <-chan struct{}
	// failure is returned after the full write and response.
	failure error
}

func (writer fullWriteErrorWriter) Write(frame []byte) (int, error) {
	n, err := writer.wire.Write(frame)
	if err != nil {
		return n, err
	}
	select {
	case <-writer.terminalObserved:
		return n, writer.failure
	case <-time.After(time.Second):
		return n, errors.New("peer terminal was not observed")
	}
}

func TestConnectionBufferedTerminalSurvivesFullWriteError(t *testing.T) {
	requests, sendRequest := io.Pipe()
	responses, sendResponse := io.Pipe()
	t.Cleanup(func() {
		_ = requests.Close()
		_ = sendRequest.Close()
		_ = responses.Close()
		_ = sendResponse.Close()
	})
	terminalObserved := make(chan struct{})
	failure := errors.New("transport failed after a complete write")
	writer := fullWriteErrorWriter{sendRequest, terminalObserved, failure}
	handler := func(context.Context, string, json.RawMessage) (any, *RequestError) {
		close(terminalObserved)
		return nil, nil
	}
	conn, err := NewConnectionWithOptions(handler, writer, responses, ConnectionOptions{
		Context:                   context.Background(),
		NotificationQueueCapacity: 4,
		NotificationQueueBytes:    4096,
		CloseTransport:            responses.Close,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(conn.Close)
	peerError := make(chan error, 1)
	go func() {
		var request anyMessage
		if err := json.NewDecoder(requests).Decode(&request); err != nil {
			peerError <- err
			return
		}
		if request.Method != AgentMethodSessionPrompt || request.ID == nil || string(*request.ID) != "1" {
			peerError <- errors.New("peer did not receive the expected full request")
			return
		}
		_, err := io.WriteString(sendResponse, bufferedTerminalFrame+terminalUpdateFrame)
		peerError <- err
	}()

	terminal, err := SendRequest[PromptResponse](conn, context.Background(), AgentMethodSessionPrompt, nil)
	if peerErr := receiveLifecycle(t, peerError); peerErr != nil {
		t.Fatal(peerErr)
	}
	if err == nil || !strings.Contains(err.Error(), failure.Error()) || !errors.Is(conn.Err(), failure) {
		t.Fatalf("transport failure was lost: request=%v, connection=%v", err, conn.Err())
	}
	assertBufferedTerminal(t, terminal)
}

package acp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

const receiptConfigFrame = `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"first","update":{"sessionUpdate":"config_option_update","configOptions":[{"id":"model","name":"Model","type":"select","currentValue":"peer-model","options":[{"value":"peer-model","name":"Peer Model"}]}]}}}` + "\n"

func receiptPipe(t *testing.T, client Client, receive func(ReceiveNotification), decorate func(io.Writer) io.Writer) (*ClientSideConnection, *io.PipeReader, *io.PipeWriter) {
	t.Helper()
	requests, sendRequest := io.Pipe()
	responses, sendResponse := io.Pipe()
	var writer io.Writer = sendRequest
	if decorate != nil {
		writer = decorate(writer)
	}
	conn, err := NewClientSideConnectionWithOptions(client, writer, responses, ConnectionOptions{
		Context:                   context.Background(),
		NotificationQueueCapacity: 2,
		NotificationQueueBytes:    4096,
		ReceiveNotification:       receive,
		CloseTransport: func() error {
			return errors.Join(requests.Close(), sendRequest.Close(), responses.Close(), sendResponse.Close())
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(conn.Close)
	return conn, requests, sendResponse
}

func receiptPeerReply(requests io.Reader, responses io.Writer, method, frames string) <-chan error {
	done := make(chan error, 1)
	go func() {
		var request anyMessage
		if err := json.NewDecoder(requests).Decode(&request); err != nil {
			done <- err
			return
		}
		if request.Method != method || request.ID == nil || string(*request.ID) != "1" {
			done <- errors.New("peer did not receive the expected full request")
			return
		}
		_, err := io.WriteString(responses, frames)
		done <- err
	}()
	return done
}

func TestRequestReceipt_PreResponseConfigAvailableBeforeNotificationHandlerDrains(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseFirst := func() { releaseOnce.Do(func() { close(release) }) }
	defer releaseFirst()
	postEntered := make(chan struct{})
	postRelease := make(chan struct{})
	defer close(postRelease)
	postSequence := make(chan uint64, 1)
	client := &clientFuncs{
		SessionUpdateFunc: func(_ context.Context, update SessionNotification) error {
			if update.SessionId == "first" {
				close(entered)
				<-release
			}
			return nil
		},
		HandleExtensionMethodFunc: func(_ context.Context, method string, _ json.RawMessage) (any, error) {
			if method == "_after_session" {
				close(postEntered)
				<-postRelease
			}
			return nil, nil
		},
	}
	var folded struct {
		sequence uint64
		model    SessionConfigValueId
	}
	receive := func(frame ReceiveNotification) {
		if frame.Method == "_after_session" {
			postSequence <- frame.Sequence
			return
		}
		if frame.Method != ClientMethodSessionUpdate {
			return
		}
		var notification SessionNotification
		if json.Unmarshal(frame.Params, &notification) != nil || notification.SessionId != "first" {
			return
		}
		if update := notification.Update.ConfigOptionUpdate; update != nil && len(update.ConfigOptions) == 1 {
			if option := update.ConfigOptions[0].Select; option != nil {
				folded.sequence = frame.Sequence
				folded.model = option.CurrentValue
			}
		}
	}
	conn, requests, responses := receiptPipe(t, client, receive, nil)
	peerDone := receiptPeerReply(requests, responses, AgentMethodSessionNew, receiptConfigFrame+
		`{"jsonrpc":"2.0","id":1,"result":{"sessionId":"first"}}`+"\n"+
		`{"jsonrpc":"2.0","method":"_after_session"}`+"\n")
	got := make(chan RequestReceipt[NewSessionResponse], 1)
	go func() {
		got <- SendClientRequestWithReceipt[NewSessionResponse](conn, context.Background(), AgentMethodSessionNew, NewSessionRequest{Cwd: "/project", McpServers: []McpServer{}}, RequestWriteHooks{})
	}()
	receiveLifecycle(t, entered)
	receipt := receiveLifecycle(t, got)
	if err := receiveLifecycle(t, peerDone); err != nil {
		t.Fatal(err)
	}
	if !receipt.ResultValid || receipt.Result.SessionId != "first" || receipt.PrepareErr != nil || receipt.TransportErr != nil || receipt.Write.Err != nil || receipt.DecodeErr != nil || receipt.PeerErr != nil {
		t.Fatalf("valid session result was lost while notification handler was blocked: %+v", receipt)
	}
	if folded.model != "peer-model" || folded.sequence == 0 || folded.sequence != receipt.NotificationWatermark || folded.sequence >= receipt.Sequence {
		t.Fatalf("pre-response configuration was not folded in receive order: fold=%+v receipt=%+v", folded, receipt)
	}
	if after := receiveLifecycle(t, postSequence); after <= receipt.Sequence {
		t.Fatalf("post-response notification sequence %d did not follow response %d", after, receipt.Sequence)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var drainErr *RequestError
	if err := conn.WaitForNotifications(ctx, receipt.NotificationWatermark); !errors.As(err, &drainErr) || drainErr.Code != -32800 {
		t.Fatalf("notification drain interruption was lost: %v", err)
	}
	releaseFirst()
	receiveLifecycle(t, postEntered)
	drained := make(chan error, 1)
	go func() {
		drained <- conn.WaitForNotifications(context.Background(), receipt.NotificationWatermark)
	}()
	if err := receiveLifecycle(t, drained); err != nil {
		t.Fatalf("failed to drain through pre-response notification: %v", err)
	}
}

func TestRequestReceipt_ValidTerminalSurvivesEOF(t *testing.T) {
	raw := terminalEOFConnection(t, nil, bufferedTerminalFrame)
	conn := &ClientSideConnection{conn: raw}
	got := SendClientRequestWithReceipt[PromptResponse](conn, context.Background(), AgentMethodSessionPrompt, nil, RequestWriteHooks{})
	if !got.ResultValid || got.Sequence == 0 || got.NotificationWatermark != 0 || got.TransportErr != nil || got.Write.Err != nil || got.DecodeErr != nil || got.PeerErr != nil {
		t.Fatalf("EOF erased response evidence: %+v", got)
	}
	assertBufferedTerminal(t, got.Result)
	receiveLifecycle(t, conn.Done())
	if conn.Err() == nil {
		t.Fatal("EOF did not terminate the transport")
	}
}

func TestRequestReceipt_TransportFailureRetainsDisconnectCause(t *testing.T) {
	failure := errors.New("peer stdout failed")
	startPeer := func(t *testing.T) *ClientSideConnection {
		t.Helper()
		conn, requests, responses := receiptPipe(t, &clientFuncs{}, nil, nil)
		go func() {
			var request anyMessage
			if err := json.NewDecoder(requests).Decode(&request); err != nil {
				_ = responses.CloseWithError(err)
				return
			}
			_ = responses.CloseWithError(failure)
		}()
		return conn
	}
	t.Run("receipt", func(t *testing.T) {
		got := SendClientRequestWithReceipt[PromptResponse](startPeer(t), context.Background(), AgentMethodSessionPrompt, nil, RequestWriteHooks{})
		if got.ResultValid || got.Sequence != 0 || got.Write.Err != nil || got.DecodeErr != nil || got.PeerErr != nil || !errors.Is(got.TransportErr, failure) {
			t.Fatalf("failed response wait did not retain its transport cause: %+v", got)
		}
	})
	t.Run("legacy", func(t *testing.T) {
		_, err := startPeer(t).Prompt(context.Background(), PromptRequest{})
		var rpcErr *RequestError
		if !errors.As(err, &rpcErr) || rpcErr.Code != -32603 {
			t.Fatalf("legacy request did not project the disconnect as an internal error: %v", err)
		}
	})
}

func TestRequestReceipt_ValidTerminalSurvivesFullWriteError(t *testing.T) {
	terminalObserved := make(chan struct{}, 1)
	failure := errors.New("transport failed after a complete write")
	receive := func(frame ReceiveNotification) {
		if frame.Method == "_before_terminal" {
			terminalObserved <- struct{}{}
		}
	}
	conn, requests, responses := receiptPipe(t, &clientFuncs{}, receive, func(w io.Writer) io.Writer {
		return fullWriteErrorWriter{w, terminalObserved, failure}
	})
	peerDone := receiptPeerReply(requests, responses, AgentMethodSessionPrompt, bufferedTerminalFrame+terminalUpdateFrame)
	got := SendClientRequestWithReceipt[PromptResponse](conn, context.Background(), AgentMethodSessionPrompt, nil, RequestWriteHooks{})
	if err := receiveLifecycle(t, peerDone); err != nil {
		t.Fatal(err)
	}
	if !got.ResultValid || got.Sequence == 0 || got.NotificationWatermark != 0 || got.Write.FrameBytes == 0 || got.Write.BytesWritten != got.Write.FrameBytes ||
		!errors.Is(got.Write.Err, failure) || got.TransportErr != nil || got.DecodeErr != nil || got.PeerErr != nil {
		t.Fatalf("full write error and received terminal were not retained independently: %+v", got)
	}
	assertBufferedTerminal(t, got.Result)
}

func TestRequestReceipt_DistinguishesPeerErrorFromDecodeFailure(t *testing.T) {
	tests := []struct {
		name     string
		response string
		peerCode int
	}{
		{"peer error", `{"jsonrpc":"2.0","id":1,"error":{"code":-32002,"message":"missing session"}}`, -32002},
		{"error is a string", `{"jsonrpc":"2.0","id":1,"error":"synthetic-peer-detail"}`, 0},
		{"error is null", `{"jsonrpc":"2.0","id":1,"error":null}`, 0},
		{"error lacks required fields", `{"jsonrpc":"2.0","id":1,"error":{}}`, 0},
		{"error has wrong code type", `{"jsonrpc":"2.0","id":1,"error":{"code":"wrong","message":"synthetic-peer-detail"}}`, 0},
		{"error has null code", `{"jsonrpc":"2.0","id":1,"error":{"code":null,"message":"synthetic-peer-detail"}}`, 0},
		{"error has null message", `{"jsonrpc":"2.0","id":1,"error":{"code":-32002,"message":null}}`, 0},
		{"error has duplicate code", `{"jsonrpc":"2.0","id":1,"error":{"code":-32000,"code":-32002,"message":"missing session"}}`, 0},
		{"wrong result type", `{"jsonrpc":"2.0","id":1,"result":{"stopReason":7}}`, 0},
		{"missing result", `{"jsonrpc":"2.0","id":1}`, 0},
		{"null result", `{"jsonrpc":"2.0","id":1,"result":null}`, 0},
		{"conflicting result and error", `{"jsonrpc":"2.0","id":1,"result":{"stopReason":"end_turn"},"error":{"code":-32002,"message":"missing session"}}`, 0},
		{"result and null error", `{"jsonrpc":"2.0","id":1,"result":{"stopReason":"end_turn"},"error":null}`, 0},
		{"duplicate result", `{"jsonrpc":"2.0","id":1,"result":{},"result":{"stopReason":"end_turn"}}`, 0},
		{"invalid protocol version", `{"jsonrpc":"1.0","id":1,"result":{"stopReason":"end_turn"}}`, 0},
		{"invalid protocol version type", `{"jsonrpc":2,"id":1,"result":{"stopReason":"end_turn"}}`, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			conn, requests, responses := receiptPipe(t, &clientFuncs{}, nil, nil)
			peerDone := receiptPeerReply(requests, responses, AgentMethodSessionPrompt, terminalUpdateFrame+tt.response+"\n")
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			got := SendClientRequestWithReceipt[PromptResponse](conn, ctx, AgentMethodSessionPrompt, nil, RequestWriteHooks{})
			if err := receiveLifecycle(t, peerDone); err != nil {
				t.Fatal(err)
			}
			if got.ResultValid || got.NotificationWatermark == 0 || got.Sequence <= got.NotificationWatermark || got.TransportErr != nil || got.Write.Err != nil {
				t.Fatalf("invalid result was mistaken for a valid terminal or transport error: %+v", got)
			}
			if tt.peerCode != 0 {
				if got.PeerErr == nil || got.PeerErr.Code != tt.peerCode || got.DecodeErr != nil {
					t.Fatalf("peer error was not preserved: %+v", got)
				}
			} else if got.DecodeErr == nil || got.PeerErr != nil {
				t.Fatalf("invalid result was not classified as a decode failure: %+v", got)
			}
		})
	}
}

func TestRequestReceipt_DisregardsAmbiguousAndReverseRequestIDs(t *testing.T) {
	reverse := make(chan struct{}, 1)
	client := &clientFuncs{
		HandleExtensionMethodFunc: func(_ context.Context, method string, _ json.RawMessage) (any, error) {
			if method == "_reverse" {
				reverse <- struct{}{}
			}
			return nil, nil
		},
	}
	conn, requests, responses := receiptPipe(t, client, nil, nil)
	done := make(chan error, 1)
	go func() {
		decoder := json.NewDecoder(requests)
		var request anyMessage
		if err := decoder.Decode(&request); err != nil {
			done <- err
			return
		}
		if _, err := io.WriteString(responses,
			`{"jsonrpc":"2.0","id":null,"result":{"stopReason":"end_turn"}}`+"\n"+
				`{"jsonrpc":"2.0","id":99,"id":1,"result":{"stopReason":"end_turn"}}`+"\n"+
				`{"jsonrpc":"2.0","id":1,"method":"_reverse","result":null}`+"\n"); err != nil {
			done <- err
			return
		}
		var reverseResponse anyMessage
		if err := decoder.Decode(&reverseResponse); err != nil {
			done <- err
			return
		}
		if reverseResponse.ID == nil || string(*reverseResponse.ID) != "1" || string(reverseResponse.Result) != "null" {
			done <- errors.New("reverse request did not receive its own response")
			return
		}
		_, err := io.WriteString(responses, bufferedTerminalFrame)
		done <- err
	}()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	got := SendClientRequestWithReceipt[PromptResponse](conn, ctx, AgentMethodSessionPrompt, nil, RequestWriteHooks{})
	if !got.ResultValid || got.Sequence == 0 {
		t.Fatal("the unambiguous outbound response was not received")
	}
	assertBufferedTerminal(t, got.Result)
	receiveLifecycle(t, reverse)
	if err := receiveLifecycle(t, done); err != nil {
		t.Fatal(err)
	}
}

const invalidNativeValue = "12345678901234567890123456789012345678901234567890"

const invalidNativeResult = `{"jsonrpc":"2.0","id":1,"result":{"stopReason":"end_turn","usage":{"inputTokens":` + invalidNativeValue + `}}}`

func TestRequestReceipt_DecodeErrorExcludesPeerPayload(t *testing.T) {
	conn, requests, responses := receiptPipe(t, &clientFuncs{}, nil, nil)
	peerDone := receiptPeerReply(requests, responses, AgentMethodSessionPrompt,
		invalidNativeResult+"\n")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	got := SendClientRequestWithReceipt[PromptResponse](conn, ctx, AgentMethodSessionPrompt, nil, RequestWriteHooks{})
	if err := receiveLifecycle(t, peerDone); err != nil {
		t.Fatal(err)
	}
	if got.ResultValid || got.DecodeErr == nil || got.TransportErr != nil {
		t.Fatal("the invalid typed result was not classified as a decode failure")
	}
	if strings.Contains(got.DecodeErr.Error(), invalidNativeValue) || errors.Unwrap(got.DecodeErr) != nil {
		t.Fatal("the public decode error exposes native payload")
	}
}

func TestRequestReceipt_LegacyResponseProjection(t *testing.T) {
	tests := []struct {
		name       string
		frame      string
		wantCode   int
		wantDetail string
	}{
		{name: "null result", frame: `{"jsonrpc":"2.0","id":1,"result":null,"params":null}`},
		{name: "valid peer error", frame: `{"jsonrpc":"2.0","id":1,"error":{"code":-32002,"message":"missing","data":{"detail":"session unavailable"}}}`, wantCode: -32002, wantDetail: "session unavailable"},
		{name: "malformed envelope", frame: `{"jsonrpc":"2.0","id":1,"error":"synthetic-peer-detail"}`, wantCode: -32603},
		{name: "malformed result", frame: invalidNativeResult, wantCode: -32603},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			conn, requests, responses := receiptPipe(t, &clientFuncs{}, nil, nil)
			peerDone := receiptPeerReply(requests, responses, AgentMethodSessionPrompt, tt.frame+"\n")
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			_, err := conn.Prompt(ctx, PromptRequest{})
			if peerErr := receiveLifecycle(t, peerDone); peerErr != nil {
				t.Fatal(peerErr)
			}
			if tt.wantCode == 0 {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			var rpcErr *RequestError
			if !errors.As(err, &rpcErr) || rpcErr.Code != tt.wantCode {
				t.Fatalf("wrong legacy JSON-RPC error code: %v", err)
			}
			if tt.wantDetail != "" {
				details, _ := rpcErr.Data.(map[string]any)
				if details["detail"] != tt.wantDetail {
					t.Fatal("valid peer error lost its details")
				}
			}
			if strings.Contains(err.Error(), "synthetic-peer-detail") || strings.Contains(err.Error(), invalidNativeValue) {
				t.Fatal("legacy decode error exposes native payload")
			}
		})
	}
}

func TestRequestReceipt_MalformedFrameDoesNotExposePayload(t *testing.T) {
	var logs receiptLogBuffer
	oldLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(oldLogger) })
	ingress := make(chan string, 2)
	conn, requests, responses := receiptPipe(t, &clientFuncs{}, func(frame ReceiveNotification) { ingress <- frame.Method }, nil)
	const secret = "private-malformed-frame-content"
	peerDone := receiptPeerReply(requests, responses, AgentMethodSessionPrompt,
		`{"jsonrpc":"2.0","method":"_bad","params":{"token":"`+secret+`"`+"\n"+
			`{"jsonrpc":"1.0","method":"_bad_version","params":{"token":"`+secret+`"}}`+"\n"+bufferedTerminalFrame)
	got := SendClientRequestWithReceipt[PromptResponse](conn, context.Background(), AgentMethodSessionPrompt, nil, RequestWriteHooks{})
	if err := receiveLifecycle(t, peerDone); err != nil {
		t.Fatal(err)
	}
	if !got.ResultValid || got.Sequence == 0 {
		t.Fatalf("malformed frame interfered with valid response: %+v", got)
	}
	select {
	case method := <-ingress:
		t.Fatalf("malformed frame reached the technical hook: %s", method)
	default:
	}
	if output := logs.String(); !strings.Contains(output, "failed to parse incoming message") || strings.Contains(output, secret) {
		t.Fatalf("malformed frame diagnostics did not preserve payload privacy: %s", output)
	}
}

type receiptLogBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *receiptLogBuffer) Write(data []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(data)
}

func (b *receiptLogBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

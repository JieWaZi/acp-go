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
	"sync/atomic"
	"testing"
	"time"
)

type receiptWriterFunction func([]byte) (int, error)

func (write receiptWriterFunction) Write(p []byte) (int, error) { return write(p) }

func TestRequestReceipt_NotificationRechecksOwnerAtSharedWriteGate(t *testing.T) {
	entered := make(chan struct{})
	var writes atomic.Int32
	conn, requests, _ := receiptPipe(t, &clientFuncs{}, nil, func(writer io.Writer) io.Writer {
		return receiptWriterFunction(func(p []byte) (int, error) {
			if writes.Add(1) == 1 {
				close(entered)
			}
			return writer.Write(p)
		})
	})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	first := make(chan RequestWriteResult, 1)
	go func() { first <- SendClientNotificationWithWriteHooks(conn, ctx, "_occupy", nil, RequestWriteHooks{}) }()
	receiveLifecycle(t, entered)
	retired := errors.New("captured native owner retired")
	var live atomic.Bool
	live.Store(true)
	second := make(chan RequestWriteResult, 1)
	go func() {
		second <- SendClientNotificationWithWriteHooks(conn, ctx, AgentMethodSessionCancel, CancelNotification{SessionId: "session"}, RequestWriteHooks{Commit: func() error {
			if !live.Load() {
				return retired
			}
			return nil
		}})
	}()
	live.Store(false)
	var sent anyMessage
	if err := json.NewDecoder(requests).Decode(&sent); err != nil || sent.Method != "_occupy" {
		t.Fatalf("unexpected first frame: %+v %v", sent, err)
	}
	if result := receiveLifecycle(t, first); result.Err != nil {
		t.Fatal(result.Err)
	}
	result := receiveLifecycle(t, second)
	if !errors.Is(result.Err, retired) || result.BytesWritten != 0 || writes.Load() != 1 {
		t.Fatalf("stale native cancellation reached transport: %+v, writes=%d", result, writes.Load())
	}
}

func TestRequestReceipt_ReceiveHookPrecedesLaterFramesAndWriteReturn(t *testing.T) {
	var frozen atomic.Bool
	observed := make(chan bool, 1)
	releaseWrite := make(chan struct{})
	conn, requests, responses := receiptPipe(t, &clientFuncs{}, func(frame ReceiveNotification) {
		if frame.Method == "_after_terminal" {
			observed <- frozen.Load()
			close(releaseWrite)
		}
	}, func(writer io.Writer) io.Writer {
		return receiptWriterFunction(func(p []byte) (int, error) {
			n, err := writer.Write(p)
			<-releaseWrite
			return n, err
		})
	})
	peerDone := receiptPeerReply(requests, responses, AgentMethodSessionPrompt,
		`{"jsonrpc":"2.0","id":1,"result":{"stopReason":"end_turn"}}`+"\n"+
			`{"jsonrpc":"2.0","method":"_after_terminal"}`+"\n")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	got := SendClientRequestWithReceipt[PromptResponse](conn, ctx, AgentMethodSessionPrompt, nil, RequestWriteHooks{}, func(receipt RequestReceipt[PromptResponse]) {
		if receipt.ResultValid && receipt.Sequence == 1 && receipt.Result.StopReason == StopReasonEndTurn {
			frozen.Store(true)
		}
	})
	if err := receiveLifecycle(t, peerDone); err != nil {
		t.Fatal(err)
	}
	if !receiveLifecycle(t, observed) || !got.ResultValid {
		t.Fatal("later native frame crossed an unfrozen terminal owner")
	}
}

func TestRequestReceipt_PrepareRequestCapturesOwnerBeforeLaterNotification(t *testing.T) {
	requests, sendRequest := io.Pipe()
	responses, sendResponse := io.Pipe()
	release := make(chan struct{})
	current := "first"
	conn, err := NewClientSideConnectionWithOptions(&clientFuncs{}, sendRequest, responses, ConnectionOptions{
		Context: context.Background(), NotificationQueueCapacity: 4, NotificationQueueBytes: 4096,
		CloseTransport: func() error {
			return errors.Join(requests.Close(), sendRequest.Close(), responses.Close(), sendResponse.Close())
		},
		ReceiveNotification: func(frame ReceiveNotification) {
			if frame.Method == "_retire" {
				current = "next"
				close(release)
			}
		},
		PrepareRequest: func(_ context.Context, frame ReceiveRequest) (func(context.Context) (any, *RequestError), bool) {
			if frame.Method != "cursor/ask_question" {
				return nil, false
			}
			captured := current
			return func(context.Context) (any, *RequestError) {
				<-release
				return map[string]any{"owner": captured, "sequence": frame.Sequence}, nil
			}, true
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(conn.Close)
	peerResult := make(chan anyMessage, 1)
	go func() {
		_, _ = io.WriteString(sendResponse, `{"jsonrpc":"2.0","id":71,"method":"cursor/ask_question","params":{"sessionId":"session"}}`+"\n"+`{"jsonrpc":"2.0","method":"_retire"}`+"\n")
		var message anyMessage
		_ = json.NewDecoder(requests).Decode(&message)
		peerResult <- message
	}()
	message := receiveLifecycle(t, peerResult)
	var result struct {
		Owner    string `json:"owner"`
		Sequence uint64 `json:"sequence"`
	}
	if message.Error != nil || json.Unmarshal(message.Result, &result) != nil || result.Owner != "first" || result.Sequence != 1 {
		t.Fatalf("native request lost its receive-time owner: %#v %s", message.Error, message.Result)
	}
}

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
				`{"jsonrpc":"2.0","id":1,"method":"_reverse"}`+"\n"); err != nil {
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

type receiptDecodeProbe struct {
	entered chan struct{}
	release chan struct{}
	calls   atomic.Int32
}

var receiptDecodeControl atomic.Pointer[receiptDecodeProbe]

type receiptBlockingResult struct{}

func (*receiptBlockingResult) UnmarshalJSON([]byte) error {
	probe := receiptDecodeControl.Load()
	if probe.calls.Add(1) == 1 {
		close(probe.entered)
	}
	<-probe.release
	return nil
}

func TestRequestReceipt_NoHookDecoderDoesNotOwnReader(t *testing.T) {
	for _, explicitNil := range []bool{false, true} {
		name := "omitted"
		if explicitNil {
			name = "explicit_nil"
		}
		t.Run(name, func(t *testing.T) {
			probe := &receiptDecodeProbe{entered: make(chan struct{}), release: make(chan struct{})}
			receiptDecodeControl.Store(probe)
			release := sync.OnceFunc(func() { close(probe.release) })
			defer release()
			next := make(chan struct{})
			conn, requests, responses := receiptPipe(t, &clientFuncs{}, func(frame ReceiveNotification) {
				if frame.Method == "_after" {
					close(next)
				}
			}, nil)
			peerDone := receiptPeerReply(requests, responses, "_decode",
				`{"jsonrpc":"2.0","id":1,"result":{}}`+"\n"+`{"jsonrpc":"2.0","method":"_after"}`+"\n")
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			done := make(chan RequestReceipt[receiptBlockingResult], 1)
			var hooks []func(RequestReceipt[receiptBlockingResult])
			if explicitNil {
				hooks = append(hooks, nil)
			}
			go func() {
				done <- SendClientRequestWithReceipt[receiptBlockingResult](conn, ctx, "_decode", nil, RequestWriteHooks{}, hooks...)
			}()
			receiveLifecycle(t, probe.entered)
			select {
			case <-next:
			case <-ctx.Done():
				t.Fatal("no-hook result decoder blocked the reader from observing the next frame")
			}
			release()
			if got := receiveLifecycle(t, done); !got.ResultValid || probe.calls.Load() != 1 {
				t.Fatalf("receipt result valid = %v, decoder calls = %d", got.ResultValid, probe.calls.Load())
			}
			if err := receiveLifecycle(t, peerDone); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestRequestReceipt_ResponseRequiresSuccessfulWriteCommit(t *testing.T) {
	for _, reject := range []bool{false, true} {
		name := "accepted"
		if reject {
			name = "rejected"
		}
		t.Run(name, func(t *testing.T) {
			commitEntered := make(chan struct{})
			commitRelease := make(chan struct{})
			release := sync.OnceFunc(func() { close(commitRelease) })
			defer release()
			witness := make(chan struct{})
			conn, requests, responses := receiptPipe(t, &clientFuncs{}, func(frame ReceiveNotification) {
				if frame.Method == "_witness" {
					close(witness)
				}
			}, nil)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			rejection := errors.New("test write admission rejection")
			var captures atomic.Int32
			done := make(chan RequestReceipt[PromptResponse], 1)
			go func() {
				done <- SendClientRequestWithReceipt[PromptResponse](conn, ctx, AgentMethodSessionPrompt, nil,
					RequestWriteHooks{Commit: func() error {
						close(commitEntered)
						<-commitRelease
						if reject {
							return rejection
						}
						return nil
					}}, func(RequestReceipt[PromptResponse]) { captures.Add(1) })
			}()
			receiveLifecycle(t, commitEntered)
			if _, err := io.WriteString(responses, `{"jsonrpc":"2.0","id":1,"result":{"stopReason":"end_turn"}}`+"\n"+
				`{"jsonrpc":"2.0","method":"_witness"}`+"\n"); err != nil {
				t.Fatal(err)
			}
			receiveLifecycle(t, witness)
			release()
			if reject {
				got := receiveLifecycle(t, done)
				if !errors.Is(got.Write.Err, rejection) || got.Write.BytesWritten != 0 || got.Sequence != 0 || got.ResultValid || captures.Load() != 0 {
					t.Fatalf("rejected write obtained response evidence: %+v, captures=%d", got, captures.Load())
				}
				return
			}
			peerDone := receiptPeerReply(requests, responses, AgentMethodSessionPrompt,
				`{"jsonrpc":"2.0","id":1,"result":{"stopReason":"cancelled"}}`+"\n")
			got := receiveLifecycle(t, done)
			if !got.ResultValid || got.Result.StopReason != StopReasonCancelled || got.Sequence != 3 || got.NotificationWatermark != 2 || captures.Load() != 1 {
				t.Fatalf("request did not retain the actual post-admission response: %+v, captures=%d", got, captures.Load())
			}
			if err := receiveLifecycle(t, peerDone); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestRequestReceipt_ClaimedResponseSurvivesCancelWhileCaptureRuns(t *testing.T) {
	entered := make(chan struct{})
	unblock := make(chan struct{})
	release := sync.OnceFunc(func() { close(unblock) })
	defer release()
	conn, requests, responses := receiptPipe(t, &clientFuncs{}, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	firstDone := make(chan RequestReceipt[PromptResponse], 1)
	go func() {
		firstDone <- SendClientRequestWithReceipt[PromptResponse](conn, ctx, AgentMethodSessionPrompt, nil,
			RequestWriteHooks{}, func(RequestReceipt[PromptResponse]) {
				close(entered)
				<-unblock
			})
	}()
	peerDone := receiptPeerReply(requests, responses, AgentMethodSessionPrompt, bufferedTerminalFrame)
	receiveLifecycle(t, entered)
	cancel()
	// The deliberately paused capture tests lock ownership. An independent
	// request must still reach the same transport while that capture is paused.
	secondDone := make(chan RequestReceipt[PromptResponse], 1)
	go func() {
		secondDone <- SendClientRequestWithReceipt[PromptResponse](conn, context.Background(), "_independent", nil, RequestWriteHooks{})
	}()
	sent := make(chan anyMessage, 1)
	go func() {
		var message anyMessage
		_ = json.NewDecoder(requests).Decode(&message)
		sent <- message
	}()
	select {
	case message := <-sent:
		if message.Method != "_independent" {
			t.Fatalf("expected the independent request before releasing capture, got %s", message.Method)
		}
	case <-time.After(time.Second):
		t.Fatal("running response capture locked registration and writing of an independent request")
	}
	release()
	if got := receiveLifecycle(t, firstDone); !got.ResultValid || got.TransportErr != nil {
		t.Fatalf("cancellation lost the response already claimed by the reader: %+v", got)
	}
	if err := receiveLifecycle(t, peerDone); err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(responses, `{"jsonrpc":"2.0","id":2,"result":{"stopReason":"end_turn"}}`+"\n"); err != nil {
		t.Fatal(err)
	}
	if got := receiveLifecycle(t, secondDone); !got.ResultValid {
		t.Fatalf("independent response lost: %+v", got)
	}
}

func TestConnectionInboundAdmission_PreservesCanonicalOwner(t *testing.T) {
	for _, prepare := range []bool{false, true} {
		name := "legacy_handler"
		if prepare {
			name = "prepare_request"
		}
		t.Run(name, func(t *testing.T) {
			requests, connWrites := io.Pipe()
			connReads, responses := io.Pipe()
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			owners := make(chan context.Context, 4)
			invalidNotifications := make(chan string, 4)
			observed := make(chan struct{}, 2)
			run := func(ctx context.Context) (any, *RequestError) {
				<-ctx.Done()
				return "original response", nil
			}
			opts := ConnectionOptions{
				Context: ctx, NotificationQueueCapacity: 4, NotificationQueueBytes: 4096,
				CloseTransport: func() error {
					return errors.Join(requests.Close(), connWrites.Close(), connReads.Close(), responses.Close())
				},
				ReceiveNotification: func(frame ReceiveNotification) {
					if frame.Method == "_witness" {
						observed <- struct{}{}
					}
				},
			}
			if prepare {
				opts.PrepareRequest = func(ctx context.Context, frame ReceiveRequest) (func(context.Context) (any, *RequestError), bool) {
					if frame.Method == "_native" {
						owners <- ctx
						return run, true
					}
					return nil, false
				}
			}
			conn, err := NewConnectionWithOptions(func(ctx context.Context, method string, params json.RawMessage) (any, *RequestError) {
				switch method {
				case "_native":
					owners <- ctx
					return run(ctx)
				case "_malformed":
					invalidNotifications <- method
				case "_barrier":
					invalidNotifications <- method
				}
				return nil, nil
			}, connWrites, connReads, opts)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			if _, err := io.WriteString(responses, `{"jsonrpc":"2.0","id":1,"method":"_native"}`+"\n"); err != nil {
				t.Fatal(err)
			}
			owner := receiveLifecycle(t, owners)
			if _, err := io.WriteString(responses,
				`{"jsonrpc":"2.0","id":1.0,"method":"_native"}`+"\n"+
					`{"jsonrpc":"2.0","id":1e0,"method":"_native","result":null}`+"\n"+
					`{"jsonrpc":"1.0","method":"$/cancel_request","params":{"requestId":1}}`+"\n"+
					`{"jsonrpc":"2.0","method":"$/cancel_request","params":{"requestId":42,"requestId":1}}`+"\n"+
					`{"jsonrpc":"2.0","method":"$/cancel_request","result":null,"params":{"requestId":1}}`+"\n"+
					`{"jsonrpc":"1.0","method":"_malformed"}`+"\n"+
					`{"jsonrpc":"2.0","method":"_barrier"}`+"\n"+
					`{"jsonrpc":"2.0","method":"_witness"}`+"\n"); err != nil {
				t.Fatal(err)
			}
			receiveLifecycle(t, observed)
			if owner.Err() != nil {
				t.Fatalf("invalid cancellation reached the original owner: %v", context.Cause(owner))
			}
			if method := receiveLifecycle(t, invalidNotifications); method != "_barrier" {
				t.Fatalf("invalid notification reached the legacy handler: %s", method)
			}
			if _, err := io.WriteString(responses, `{"jsonrpc":"2.0","method":"$/cancel_request","params":{"requestId":1e0}}`+"\n"); err != nil {
				t.Fatal(err)
			}
			receiveLifecycle(t, owner.Done())
			var message anyMessage
			if err := json.NewDecoder(requests).Decode(&message); err != nil {
				t.Fatal(err)
			}
			if message.ID == nil || string(*message.ID) != "1" || message.Error != nil || string(message.Result) != `"original response"` {
				t.Fatalf("duplicate response displaced the first request: %+v", message)
			}
			select {
			case <-owners:
				t.Fatal("duplicate canonical request captured a second owner")
			default:
			}
		})
	}
}

type receiptWriteWaitContext struct {
	context.Context
	onWait func()
}

func (ctx receiptWriteWaitContext) Done() <-chan struct{} {
	ctx.onWait()
	return ctx.Context.Done()
}

func TestRequestReceipt_QueuedRequestCannotClaimResponse(t *testing.T) {
	writerEntered := make(chan struct{})
	observed := make(chan struct{})
	conn, _, responses := receiptPipe(t, &clientFuncs{}, func(frame ReceiveNotification) {
		if frame.Method == "_witness" {
			close(observed)
		}
	}, func(writer io.Writer) io.Writer {
		return receiptWriterFunction(func(p []byte) (int, error) {
			close(writerEntered)
			return writer.Write(p)
		})
	})
	go SendClientNotificationWithWriteHooks(conn, context.Background(), "_occupy_gate", nil, RequestWriteHooks{})
	receiveLifecycle(t, writerEntered)
	waiting := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var captures atomic.Int32
	done := make(chan RequestReceipt[PromptResponse], 1)
	go func() {
		done <- SendClientRequestWithReceipt[PromptResponse](conn, ctx, AgentMethodSessionPrompt, nil,
			RequestWriteHooks{Context: receiptWriteWaitContext{
				Context: ctx,
				onWait:  sync.OnceFunc(func() { close(waiting) }),
			}}, func(RequestReceipt[PromptResponse]) { captures.Add(1) })
	}()
	receiveLifecycle(t, waiting)
	if _, err := io.WriteString(responses, bufferedTerminalFrame+
		`{"jsonrpc":"2.0","method":"_witness"}`+"\n"); err != nil {
		t.Fatal(err)
	}
	receiveLifecycle(t, observed)
	cancel()
	got := receiveLifecycle(t, done)
	if !errors.Is(got.Write.Err, context.Canceled) || got.Write.FrameBytes != 0 || got.Write.BytesWritten != 0 ||
		got.Sequence != 0 || got.ResultValid || captures.Load() != 0 {
		t.Fatalf("request waiting for write gate claimed response evidence: %+v, captures=%d", got, captures.Load())
	}
}

func TestRequestReceipt_ClaimedResponseSurvivesWriteErrorWhileCaptureRuns(t *testing.T) {
	failure := errors.New("write completed with transport failure")
	entered := make(chan struct{})
	unblock := make(chan struct{})
	release := sync.OnceFunc(func() { close(unblock) })
	defer release()
	conn, requests, responses := receiptPipe(t, &clientFuncs{}, nil, func(writer io.Writer) io.Writer {
		return receiptWriterFunction(func(p []byte) (int, error) {
			n, err := writer.Write(p)
			if err != nil {
				return n, err
			}
			<-entered
			return n, failure
		})
	})
	peerDone := receiptPeerReply(requests, responses, AgentMethodSessionPrompt, bufferedTerminalFrame)
	done := make(chan RequestReceipt[PromptResponse], 1)
	go func() {
		done <- SendClientRequestWithReceipt[PromptResponse](conn, context.Background(), AgentMethodSessionPrompt, nil,
			RequestWriteHooks{}, func(RequestReceipt[PromptResponse]) {
				close(entered)
				<-unblock
			})
	}()
	receiveLifecycle(t, conn.Done())
	release()
	if err := receiveLifecycle(t, peerDone); err != nil {
		t.Fatal(err)
	}
	got := receiveLifecycle(t, done)
	if !got.ResultValid || !errors.Is(got.Write.Err, failure) || got.Write.FrameBytes == 0 ||
		got.Write.BytesWritten != got.Write.FrameBytes {
		t.Fatalf("write failure obscured the response already claimed by the reader: %+v", got)
	}
}

type inboundResultEncoding func()

func (encode inboundResultEncoding) MarshalJSON() ([]byte, error) {
	encode()
	return []byte(`"first response"`), nil
}

func TestConnectionInboundAdmission_ReusesIDOnceReplyOwnsWriteGate(t *testing.T) {
	requests, connWrites := io.Pipe()
	connReads, responses := io.Pipe()
	firstWriterEntered := make(chan struct{})
	firstReplyEncoded := make(chan struct{})
	replyCanReturn := make(chan struct{})
	releaseReply := sync.OnceFunc(func() { close(replyCanReturn) })
	defer releaseReply()
	witness := make(chan struct{}, 2)
	type capture struct {
		method string
		ctx    context.Context
	}
	captured := make(chan capture, 3)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var writes int
	conn, err := NewConnectionWithOptions(nil, receiptWriterFunction(func(frame []byte) (int, error) {
		writes++
		if writes == 1 {
			close(firstWriterEntered)
		}
		n, err := connWrites.Write(frame)
		if writes == 2 {
			<-replyCanReturn
		}
		return n, err
	}), connReads, ConnectionOptions{
		Context: ctx, NotificationQueueCapacity: 4, NotificationQueueBytes: 4096,
		CloseTransport: func() error {
			return errors.Join(requests.Close(), connWrites.Close(), connReads.Close(), responses.Close())
		},
		ReceiveNotification: func(frame ReceiveNotification) {
			if frame.Method == "_witness" {
				witness <- struct{}{}
			}
		},
		PrepareRequest: func(ctx context.Context, frame ReceiveRequest) (func(context.Context) (any, *RequestError), bool) {
			captured <- capture{method: frame.Method, ctx: ctx}
			if frame.Method == "_first" {
				return func(context.Context) (any, *RequestError) {
					return inboundResultEncoding(func() { close(firstReplyEncoded) }), nil
				}, true
			}
			return func(ctx context.Context) (any, *RequestError) {
				<-ctx.Done()
				return "second response", nil
			}, true
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	go conn.SendNotification(ctx, "_hold_gate", nil)
	receiveLifecycle(t, firstWriterEntered)

	send := func(frames string) {
		t.Helper()
		if _, err := io.WriteString(responses, frames); err != nil {
			t.Fatal(err)
		}
	}
	decoder := json.NewDecoder(requests)
	read := func() anyMessage {
		t.Helper()
		var message anyMessage
		if err := decoder.Decode(&message); err != nil {
			t.Fatal(err)
		}
		return message
	}

	send(`{"jsonrpc":"2.0","id":1,"method":"_first"}` + "\n")
	first := receiveLifecycle(t, captured)
	receiveLifecycle(t, firstReplyEncoded)
	send(`{"jsonrpc":"2.0","id":1.0,"method":"_too_early"}` + "\n" +
		`{"jsonrpc":"2.0","method":"_witness"}` + "\n")
	receiveLifecycle(t, witness)
	select {
	case duplicate := <-captured:
		t.Fatalf("request captured while the first reply waited for the write gate: %s", duplicate.method)
	default:
	}

	if message := read(); message.Method != "_hold_gate" {
		t.Fatalf("unexpected frame ahead of the first reply: %+v", message)
	}
	if message := read(); message.ID == nil || string(*message.ID) != "1" || string(message.Result) != `"first response"` {
		t.Fatalf("unexpected first reply: %+v", message)
	}

	send(`{"jsonrpc":"2.0","id":1.0,"method":"_second"}` + "\n" +
		`{"jsonrpc":"2.0","method":"_witness"}` + "\n")
	receiveLifecycle(t, witness)
	var second capture
	select {
	case second = <-captured:
		if second.method != "_second" {
			t.Fatalf("unexpected next owner: %s", second.method)
		}
	default:
		t.Fatal("reused ID was dropped even though the peer had read the first reply")
	}

	releaseReply()
	receiveLifecycle(t, first.ctx.Done())
	send(`{"jsonrpc":"2.0","method":"$/cancel_request","params":{"requestId":1e0}}` + "\n")
	receiveLifecycle(t, second.ctx.Done())
	if message := read(); message.ID == nil || string(*message.ID) != "1.0" || string(message.Result) != `"second response"` {
		t.Fatalf("unexpected reply from the next owner: %+v", message)
	}
}

package codex

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/JieWaZi/acp-go/pkg/codex/protocol"
)

// transportHarness 保存一对可由测试精确控制的 app-server 管道。
type transportHarness struct {
	// transport 是被测的 Codex 内层 NDJSON transport。
	transport *appServerTransport
	// requests 读取 Adapter 写向 fake app-server 的请求。
	requests *bufio.Reader
	// responses 向 Adapter 写入 fake app-server 的响应或通知。
	responses *io.PipeWriter
	// closeOnce 保证测试清理可重复调用。
	closeOnce sync.Once
}

// controlledReaderError 让 scanner 已进入 Read 后，再由测试显式释放指定错误。
type controlledReaderError struct {
	// entered 在 scanner 首次读取时关闭，作为安装 pending 请求的同步点。
	entered chan struct{}
	// release 控制底层读取错误何时暴露给 scanner。
	release chan struct{}
	// err 是 scanner 应观察到的底层读取错误。
	err error
	// enterOnce 保证 entered 只关闭一次。
	enterOnce sync.Once
}

// Read 实现 io.Reader，并用显式通道固定 scanner error 与 pending 安装的先后顺序。
func (r *controlledReaderError) Read(_ []byte) (int, error) {
	r.enterOnce.Do(func() { close(r.entered) })
	<-r.release
	return 0, r.err
}

// signalingWriter 在真实 transport 写帧后通知测试，不检查实现内部调用次数。
type signalingWriter struct {
	// written 为每条已完整写入的请求提供同步信号。
	written chan struct{}
}

// Write 实现 io.Writer，并在接受完整帧后发送同步信号。
func (w *signalingWriter) Write(data []byte) (int, error) {
	w.written <- struct{}{}
	return len(data), nil
}

// newTransportHarness 创建不依赖真实进程的有界 transport 测试夹具。
func newTransportHarness(
	t *testing.T,
	maxLine int,
	notification notificationHandler,
	serverRequest serverRequestHandler,
) *transportHarness {
	t.Helper()
	adapterInput, fakeOutput := io.Pipe()
	fakeInput, adapterOutput := io.Pipe()
	harness := &transportHarness{
		requests:  bufio.NewReader(fakeInput),
		responses: fakeOutput,
	}
	harness.transport = newAppServerTransport(
		context.Background(),
		adapterInput,
		adapterOutput,
		io.NopCloser(strings.NewReader("")),
		transportOptions{
			Logger:               slog.New(slog.NewTextHandler(io.Discard, nil)),
			MaxLineBytes:         maxLine,
			MaxServerRequests:    2,
			NotificationHandler:  notification,
			ServerRequestHandler: serverRequest,
		},
	)
	t.Cleanup(func() { harness.close(t) })
	return harness
}

// close 释放测试管道与 transport。
func (h *transportHarness) close(t *testing.T) {
	t.Helper()
	h.closeOnce.Do(func() {
		_ = h.responses.Close()
		_ = h.transport.Close()
	})
}

// TestAppServerTransportOmitsJSONRPCAndMatchesResponse 验证 wire 不带 jsonrpc 且按 ID 回填结果。
func TestAppServerTransportOmitsJSONRPCAndMatchesResponse(t *testing.T) {
	t.Parallel()
	harness := newTransportHarness(t, 4096, nil, nil)

	result := make(chan protocol.ThreadReadResponse, 1)
	errResult := make(chan error, 1)
	go func() {
		var response protocol.ThreadReadResponse
		err := harness.transport.Call(context.Background(), func(id protocol.RequestID) protocol.ClientRequest {
			return protocol.NewThreadReadRequest(id, protocol.ThreadReadParams{ThreadID: "thread-1"})
		}, &response)
		result <- response
		errResult <- err
	}()

	line, err := harness.requests.ReadBytes('\n')
	if err != nil {
		t.Fatalf("读取请求失败: %v", err)
	}
	if strings.Contains(string(line), "jsonrpc") {
		t.Fatalf("Codex app-server 请求不应包含 jsonrpc: %s", line)
	}
	var request struct {
		// ID 保存 fake server 回应所需的原始请求标识。
		ID json.RawMessage `json:"id"`
		// Method 保存请求方法。
		Method string `json:"method"`
	}
	if err = json.Unmarshal(line, &request); err != nil {
		t.Fatalf("解析请求失败: %v", err)
	}
	if request.Method != protocol.MethodThreadRead {
		t.Fatalf("请求方法为 %q", request.Method)
	}
	_, err = harness.responses.Write([]byte(`{"id":` + string(request.ID) + `,"result":{"thread":{"id":"thread-1"}}}` + "\n"))
	if err != nil {
		t.Fatalf("写响应失败: %v", err)
	}
	if err = <-errResult; err != nil {
		t.Fatalf("Call 返回错误: %v", err)
	}
	if response := <-result; response.Thread.ID != "thread-1" {
		t.Fatalf("响应 thread 为 %#v", response.Thread)
	}
}

// TestAppServerTransportObservedResponseBlocksFollowingFrames 验证 response observer 完成前读取循环不能继续处理后续帧。
// 这验证 observer 与下一帧之间不存在 goroutine 调度窗口。
func TestAppServerTransportObservedResponseBlocksFollowingFrames(t *testing.T) {
	harness := newTransportHarness(t, 4096, nil, nil)

	observerEntered := make(chan struct{})
	releaseObserver := make(chan struct{})
	callResult := make(chan error, 1)
	go func() {
		var response protocol.TurnStartResponse
		callResult <- harness.transport.CallObserved(
			context.Background(),
			func(id protocol.RequestID) protocol.ClientRequest {
				return protocol.NewTurnStartRequest(id, protocol.TurnStartParams{
					ThreadID: "thread-1",
					Input:    []protocol.InputElement{},
				})
			},
			&response,
			func() error {
				if response.Turn.ID != "turn-1" {
					return errors.New("observer saw undecoded turn/start response")
				}
				close(observerEntered)
				<-releaseObserver
				return nil
			},
		)
	}()

	line, err := harness.requests.ReadBytes('\n')
	if err != nil {
		t.Fatalf("读取 turn/start 请求失败: %v", err)
	}
	var request map[string]json.RawMessage
	if err = json.Unmarshal(line, &request); err != nil {
		t.Fatalf("解析 turn/start 请求失败: %v", err)
	}

	dispatchReturned := make(chan struct{})
	go func() {
		harness.transport.dispatchLine([]byte(
			`{"id":` + string(request["id"]) + `,"result":{"turn":{"id":"turn-1","items":[],"status":"inProgress"}}}`,
		))
		close(dispatchReturned)
	}()
	<-observerEntered
	select {
	case <-dispatchReturned:
		t.Fatal("response observer 返回前 dispatchLine 已继续")
	default:
	}
	close(releaseObserver)
	<-dispatchReturned
	if err = <-callResult; err != nil {
		t.Fatalf("观察 turn/start response 失败: %v", err)
	}
}

// TestAppServerTransportObservedCancellationRetainsOneShotObserver 验证调用方取消只分离 waiter，
// 迟到 response 仍执行一次 observer，重复 response 不会重复执行或重新占用 pending。
func TestAppServerTransportObservedCancellationRetainsOneShotObserver(t *testing.T) {
	harness := newTransportHarness(t, 4096, nil, nil)
	callCtx, cancelCall := context.WithCancel(context.Background())
	callResult := make(chan error, 1)
	var observed atomic.Int64
	go func() {
		var response protocol.TurnStartResponse
		callResult <- harness.transport.CallObserved(
			callCtx,
			func(id protocol.RequestID) protocol.ClientRequest {
				return protocol.NewTurnStartRequest(id, protocol.TurnStartParams{
					ThreadID: "thread-observed-cancel",
					Input:    []protocol.InputElement{},
				})
			},
			&response,
			func() error {
				observed.Add(1)
				return nil
			},
		)
	}()
	line, err := harness.requests.ReadBytes('\n')
	if err != nil {
		t.Fatalf("读取 observed 请求失败: %v", err)
	}
	var request runtimeWireRequest
	if err = json.Unmarshal(line, &request); err != nil {
		t.Fatalf("解码 observed 请求失败: %v", err)
	}
	cancelCall()
	if err = <-callResult; !errors.Is(err, context.Canceled) {
		t.Fatalf("observed 调用取消错误为 %v", err)
	}

	response := []byte(
		`{"id":` + string(request.ID) + `,"result":{"turn":{"id":"turn-late","items":[],"status":"inProgress"}}}` + "\n",
	)
	if _, err = harness.responses.Write(response); err != nil {
		t.Fatalf("写迟到 observed response 失败: %v", err)
	}
	if _, err = harness.responses.Write(response); err != nil {
		t.Fatalf("写重复 observed response 失败: %v", err)
	}

	sentinelResult := make(chan error, 1)
	go func() {
		var result protocol.ThreadReadResponse
		sentinelResult <- harness.transport.Call(context.Background(), func(id protocol.RequestID) protocol.ClientRequest {
			return protocol.NewThreadReadRequest(id, protocol.ThreadReadParams{ThreadID: "thread-observed-cancel"})
		}, &result)
	}()
	sentinel := readRuntimeWireRequest(t, harness)
	writeRuntimeWireResult(t, harness, sentinel.ID, json.RawMessage(
		`{"thread":{"id":"thread-observed-cancel","turns":[]}}`,
	))
	if err = <-sentinelResult; err != nil {
		t.Fatalf("observed 顺序 sentinel 返回错误: %v", err)
	}
	if count := observed.Load(); count != 1 {
		t.Fatalf("迟到/重复 response 执行 observer 次数为 %d", count)
	}
	harness.transport.pendingMu.Lock()
	pendingCount := len(harness.transport.pending)
	harness.transport.pendingMu.Unlock()
	if pendingCount != 0 {
		t.Fatalf("one-shot observer 完成后 pending 数为 %d", pendingCount)
	}
}

// TestAppServerTransportFatalClearsDetachedObserver 验证 caller 已取消后，transport fatal 会回收 observer 且不执行它。
func TestAppServerTransportFatalClearsDetachedObserver(t *testing.T) {
	harness := newTransportHarness(t, 4096, nil, nil)
	callCtx, cancelCall := context.WithCancel(context.Background())
	callResult := make(chan error, 1)
	var observed atomic.Int64
	go func() {
		var response protocol.TurnStartResponse
		callResult <- harness.transport.CallObserved(
			callCtx,
			func(id protocol.RequestID) protocol.ClientRequest {
				return protocol.NewTurnStartRequest(id, protocol.TurnStartParams{
					ThreadID: "thread-observed-fatal",
					Input:    []protocol.InputElement{},
				})
			},
			&response,
			func() error {
				observed.Add(1)
				return nil
			},
		)
	}()
	request := readRuntimeWireRequest(t, harness)
	cancelCall()
	if err := <-callResult; !errors.Is(err, context.Canceled) {
		t.Fatalf("fatal 前 observed 调用取消错误为 %v", err)
	}
	if err := harness.transport.Close(); err != nil {
		t.Fatalf("关闭含 detached observer 的 transport 失败: %v", err)
	}
	harness.transport.dispatchLine([]byte(
		`{"id":` + string(request.ID) + `,"result":{"turn":{"id":"turn-ignored","items":[],"status":"inProgress"}}}`,
	))
	if count := observed.Load(); count != 0 {
		t.Fatalf("transport fatal 后 observer 执行次数为 %d", count)
	}
	harness.transport.pendingMu.Lock()
	pendingCount := len(harness.transport.pending)
	harness.transport.pendingMu.Unlock()
	if pendingCount != 0 {
		t.Fatalf("transport fatal 后 pending 数为 %d", pendingCount)
	}
}

// TestAppServerTransportOrdinaryCancellationRemovesPending 验证无 observer 的普通调用取消后不会占用 pending。
func TestAppServerTransportOrdinaryCancellationRemovesPending(t *testing.T) {
	harness := newTransportHarness(t, 4096, nil, nil)
	callCtx, cancelCall := context.WithCancel(context.Background())
	callResult := make(chan error, 1)
	go func() {
		var response protocol.ThreadReadResponse
		callResult <- harness.transport.Call(callCtx, func(id protocol.RequestID) protocol.ClientRequest {
			return protocol.NewThreadReadRequest(id, protocol.ThreadReadParams{ThreadID: "thread-ordinary-cancel"})
		}, &response)
	}()
	_ = readRuntimeWireRequest(t, harness)
	cancelCall()
	if err := <-callResult; !errors.Is(err, context.Canceled) {
		t.Fatalf("普通调用取消错误为 %v", err)
	}
	harness.transport.pendingMu.Lock()
	pendingCount := len(harness.transport.pending)
	harness.transport.pendingMu.Unlock()
	if pendingCount != 0 {
		t.Fatalf("普通调用取消后 pending 数为 %d", pendingCount)
	}
}

// TestAppServerTransportRoutesNotificationBeforeResponse 验证通知可在对应请求响应前被路由。
func TestAppServerTransportRoutesNotificationBeforeResponse(t *testing.T) {
	t.Parallel()
	notified := make(chan protocol.ServerNotification, 1)
	harness := newTransportHarness(t, 4096, func(_ context.Context, notification protocol.ServerNotification) {
		notified <- notification
	}, nil)

	errResult := make(chan error, 1)
	go func() {
		var response protocol.TurnStartResponse
		errResult <- harness.transport.Call(context.Background(), func(id protocol.RequestID) protocol.ClientRequest {
			return protocol.NewTurnStartRequest(id, protocol.TurnStartParams{ThreadID: "thread-1", Input: []protocol.InputElement{}})
		}, &response)
	}()
	line, err := harness.requests.ReadBytes('\n')
	if err != nil {
		t.Fatalf("读取请求失败: %v", err)
	}
	var request map[string]json.RawMessage
	if err = json.Unmarshal(line, &request); err != nil {
		t.Fatalf("解析请求失败: %v", err)
	}
	completion := `{"method":"turn/completed","params":{"threadId":"thread-1","turn":{"id":"turn-1","items":[],"status":"completed"}}}` + "\n"
	if _, err = harness.responses.Write([]byte("not-json\n" + completion)); err != nil {
		t.Fatalf("写通知失败: %v", err)
	}
	select {
	case notification := <-notified:
		if notification.Method() != protocol.MethodTurnCompleted {
			t.Fatalf("通知方法为 %q", notification.Method())
		}
	case <-time.After(time.Second):
		t.Fatal("通知未在响应前路由")
	}
	if _, err = harness.responses.Write([]byte(`{"id":` + string(request["id"]) + `,"result":{"turn":{"id":"turn-1","items":[],"status":"inProgress"}}}` + "\n")); err != nil {
		t.Fatalf("写响应失败: %v", err)
	}
	if err = <-errResult; err != nil {
		t.Fatalf("Call 返回错误: %v", err)
	}
}

// TestAppServerTransportRepliesToTypedServerRequest 验证服务端请求按协议 discriminator 解码并原 ID 回复。
func TestAppServerTransportRepliesToTypedServerRequest(t *testing.T) {
	t.Parallel()
	handled := make(chan string, 1)
	harness := newTransportHarness(t, 4096, nil, func(_ context.Context, request protocol.ServerRequest) (any, error) {
		handled <- request.Method()
		return map[string]string{"decision": "decline"}, nil
	})

	request := `{"id":"approval-1","method":"item/commandExecution/requestApproval","params":{"command":"pwd","cwd":"/tmp","itemId":"item-1","startedAtMs":1,"threadId":"thread-1","turnId":"turn-1"}}` + "\n"
	if _, err := harness.responses.Write([]byte(request)); err != nil {
		t.Fatalf("写服务端请求失败: %v", err)
	}
	select {
	case method := <-handled:
		if method != protocol.MethodCommandExecutionRequestApproval {
			t.Fatalf("处理方法为 %q", method)
		}
	case <-time.After(time.Second):
		t.Fatal("服务端请求未处理")
	}
	line, err := harness.requests.ReadBytes('\n')
	if err != nil {
		t.Fatalf("读取服务端请求回复失败: %v", err)
	}
	if strings.Contains(string(line), "jsonrpc") || !strings.Contains(string(line), `"id":"approval-1"`) {
		t.Fatalf("服务端请求回复为 %s", line)
	}
}

// TestAppServerTransportFansOutStableFatalError 验证 EOF 会用同一稳定错误解除所有 pending 请求。
func TestAppServerTransportFansOutStableFatalError(t *testing.T) {
	t.Parallel()
	harness := newTransportHarness(t, 4096, nil, nil)

	errorsResult := make(chan error, 2)
	for range 2 {
		go func() {
			var response protocol.ThreadReadResponse
			errorsResult <- harness.transport.Call(context.Background(), func(id protocol.RequestID) protocol.ClientRequest {
				return protocol.NewThreadReadRequest(id, protocol.ThreadReadParams{ThreadID: "thread"})
			}, &response)
		}()
	}
	for range 2 {
		if _, err := harness.requests.ReadBytes('\n'); err != nil {
			t.Fatalf("读取 pending 请求失败: %v", err)
		}
	}
	if err := harness.responses.Close(); err != nil {
		t.Fatalf("关闭 fake stdout 失败: %v", err)
	}
	first := <-errorsResult
	second := <-errorsResult
	if !errors.Is(first, ErrAppServerUnavailable) || !errors.Is(second, ErrAppServerUnavailable) {
		t.Fatalf("pending 错误为 %v / %v", first, second)
	}
	if first != second {
		t.Fatalf("fatal fan-out 应共享同一稳定错误实例: %p / %p", first, second)
	}
}

// TestAppServerTransportPrefersConfirmedProcessExitToClosedPipe 验证 Go Wait 关闭 StdoutPipe 时，
// 已确认的进程异常退出必须覆盖并发出现的 closed-pipe 错误。
func TestAppServerTransportPrefersConfirmedProcessExitToClosedPipe(t *testing.T) {
	t.Parallel()
	reader := &controlledReaderError{
		entered: make(chan struct{}),
		release: make(chan struct{}),
		err:     os.ErrClosed,
	}
	writer := &signalingWriter{written: make(chan struct{}, 2)}
	processExitErr := fmt.Errorf("%w with code 7: fatal app-server fixture", ErrAppServerExited)
	transport := newAppServerTransport(
		context.Background(),
		reader,
		writer,
		nil,
		transportOptions{
			Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
			EOFError: func() error { return processExitErr },
		},
	)
	t.Cleanup(func() { _ = transport.Close() })
	<-reader.entered

	results := make(chan error, 2)
	for range 2 {
		go func() {
			var response protocol.ThreadReadResponse
			results <- transport.Call(context.Background(), func(id protocol.RequestID) protocol.ClientRequest {
				return protocol.NewThreadReadRequest(id, protocol.ThreadReadParams{ThreadID: "thread"})
			}, &response)
		}()
	}
	<-writer.written
	<-writer.written
	close(reader.release)

	first := <-results
	second := <-results
	if !errors.Is(first, ErrAppServerExited) || !errors.Is(second, ErrAppServerExited) {
		t.Fatalf("closed-pipe 竞态丢失进程退出根因: %v / %v", first, second)
	}
	if !strings.Contains(first.Error(), "code 7") || !strings.Contains(first.Error(), "fatal app-server fixture") {
		t.Fatalf("进程退出诊断不完整: %v", first)
	}
	if first != second {
		t.Fatalf("process fatal fan-out 应共享同一稳定错误实例: %p / %p", first, second)
	}
}

// TestAppServerTransportPreservesOrdinaryReaderError 验证非 closed-pipe 的 scanner I/O 错误
// 不会因为进程错误回调存在而被改写为 app-server 退出。
func TestAppServerTransportPreservesOrdinaryReaderError(t *testing.T) {
	t.Parallel()
	readerErr := errors.New("ordinary reader sentinel")
	reader := &controlledReaderError{
		entered: make(chan struct{}),
		release: make(chan struct{}),
		err:     readerErr,
	}
	processExitErr := fmt.Errorf("%w with code 7", ErrAppServerExited)
	transport := newAppServerTransport(
		context.Background(),
		reader,
		io.Discard,
		nil,
		transportOptions{
			Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
			EOFError: func() error { return processExitErr },
		},
	)
	t.Cleanup(func() { _ = transport.Close() })
	<-reader.entered
	close(reader.release)
	<-transport.Done()

	if err := transport.Err(); !errors.Is(err, readerErr) || errors.Is(err, ErrAppServerExited) {
		t.Fatalf("普通 reader 错误优先级被改写: %v", err)
	}
}

// TestAppServerTransportPreservesCleanEOF 验证进程未报告异常时，干净 EOF 仍保持 EOF 语义。
func TestAppServerTransportPreservesCleanEOF(t *testing.T) {
	t.Parallel()
	transport := newAppServerTransport(
		context.Background(),
		strings.NewReader(""),
		io.Discard,
		nil,
		transportOptions{
			Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
			EOFError: func() error { return nil },
		},
	)
	t.Cleanup(func() { _ = transport.Close() })
	<-transport.Done()

	if err := transport.Err(); !errors.Is(err, io.EOF) || errors.Is(err, ErrAppServerExited) {
		t.Fatalf("clean EOF 优先级被改写: %v", err)
	}
}

// TestAppServerTransportActiveCloseWinsClosedPipeRace 验证主动关闭先建立 fatal 后，
// readLoop 的 closed-pipe/process-exit 竞争不能覆盖关闭语义或反向等待 Close。
func TestAppServerTransportActiveCloseWinsClosedPipeRace(t *testing.T) {
	t.Parallel()
	reader := &controlledReaderError{
		entered: make(chan struct{}),
		release: make(chan struct{}),
		err:     os.ErrClosed,
	}
	processExitErr := fmt.Errorf("%w with code 7", ErrAppServerExited)
	processChecked := make(chan struct{})
	transport := newAppServerTransport(
		context.Background(),
		reader,
		io.Discard,
		nil,
		transportOptions{
			Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
			EOFError: func() error {
				close(processChecked)
				return processExitErr
			},
		},
	)
	<-reader.entered
	if err := transport.Close(); err != nil {
		t.Fatalf("主动关闭 transport 失败: %v", err)
	}
	close(reader.release)
	<-processChecked

	if err := transport.Err(); !errors.Is(err, ErrAppServerClosed) || errors.Is(err, ErrAppServerExited) {
		t.Fatalf("主动关闭优先级被覆盖: %v", err)
	}
}

// TestAppServerTransportOversizedFrameWinsProcessExit 验证超长帧在 process sentinel
// 同时可用时仍保持有界帧错误优先级。
func TestAppServerTransportOversizedFrameWinsProcessExit(t *testing.T) {
	t.Parallel()
	processExitErr := fmt.Errorf("%w with code 7", ErrAppServerExited)
	transport := newAppServerTransport(
		context.Background(),
		strings.NewReader(strings.Repeat("x", 64)+"\n"),
		io.Discard,
		nil,
		transportOptions{
			Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
			MaxLineBytes: 32,
			EOFError:     func() error { return processExitErr },
		},
	)
	t.Cleanup(func() { _ = transport.Close() })
	<-transport.Done()

	if err := transport.Err(); !errors.Is(err, ErrAppServerFrameTooLarge) || errors.Is(err, ErrAppServerExited) {
		t.Fatalf("超长帧优先级被覆盖: %v", err)
	}
}

// TestAppServerTransportRejectsOversizedLine 验证超限 NDJSON 帧会关闭 transport 而非无界分配。
func TestAppServerTransportRejectsOversizedLine(t *testing.T) {
	t.Parallel()
	harness := newTransportHarness(t, 32, nil, nil)

	writeResult := make(chan error, 1)
	go func() {
		_, err := harness.responses.Write([]byte(strings.Repeat("x", 64) + "\n"))
		writeResult <- err
	}()
	select {
	case <-harness.transport.Done():
		if !errors.Is(harness.transport.Err(), ErrAppServerFrameTooLarge) {
			t.Fatalf("transport 错误为 %v", harness.transport.Err())
		}
	case <-time.After(time.Second):
		t.Fatal("超限帧未终止 transport")
	}
	_ = harness.responses.Close()
	<-writeResult
}

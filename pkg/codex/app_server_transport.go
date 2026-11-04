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
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/JieWaZi/acp-go/pkg/codex/protocol"
)

const (
	// defaultMaxAppServerLineBytes 限制单条 app-server NDJSON 消息，防止无界缓冲。
	defaultMaxAppServerLineBytes = 8 << 20
	// defaultMaxServerRequests 限制同时执行的 app-server→ACP 回调数。
	defaultMaxServerRequests = 16
	defaultMaxPendingCalls   = 256
)

var (
	// ErrAppServerUnavailable 表示内层连接已永久不可用。
	ErrAppServerUnavailable = errors.New("codex app-server unavailable")
	// ErrAppServerClosed 表示 Adapter 主动关闭了内层连接。
	ErrAppServerClosed = errors.New("codex app-server transport closed")
	// ErrAppServerFrameTooLarge 表示 app-server 输出了超过配置上限的 NDJSON 帧。
	ErrAppServerFrameTooLarge = errors.New("codex app-server frame too large")
)

// frameDelivery describes how many bytes of one outbound NDJSON frame reached the writer.
type frameDelivery string

const (
	frameNotWritten frameDelivery = "not_written"
	framePartial    frameDelivery = "partial"
	frameWritten    frameDelivery = "written"
)

// transportCallError keeps frame delivery evidence separate from native admission.
type transportCallError struct {
	// delivery describes the outbound frame independently of the reply.
	delivery frameDelivery
	// cause is the original transport, context, or decode error.
	cause error
}

// Error returns the underlying diagnostic.
func (e *transportCallError) Error() string { return e.cause.Error() }

// Unwrap retains errors.Is and errors.As for the original cause.
func (e *transportCallError) Unwrap() error { return e.cause }

// notificationHandler 消费已按 method discriminator 解码的 app-server 通知。
type notificationHandler func(ctx context.Context, notification protocol.ServerNotification)

// serverRequestHandler 消费已按 method discriminator 解码的 app-server 请求。
type serverRequestHandler func(ctx context.Context, request protocol.ServerRequest) (any, error)

// transportOptions 保存 Codex 内层 transport 的有界策略和消费方回调。
type transportOptions struct {
	// Logger 只向诊断流记录 malformed 等非协议信息。
	Logger *slog.Logger
	// MaxLineBytes 是单条 NDJSON 消息的最大字节数。
	MaxLineBytes int
	// MaxPendingCalls bounds response waiters, including detached turn observers.
	MaxPendingCalls int
	// MaxServerRequests 是并行 server request 的硬上限。
	MaxServerRequests int
	// NotificationHandler 接收服务端通知；nil 表示忽略通知。
	NotificationHandler notificationHandler
	// ServerRequestHandler 接收审批等服务端请求；nil 时返回 method-not-found。
	ServerRequestHandler serverRequestHandler
	// EOFError 在真实进程 stdout EOF 后返回已包含退出码/stderr 的稳定错误。
	EOFError func() error
}

// pendingResponse carries the exact matching native response or failure.
type pendingResponse struct {
	// result 是响应成功时的原始 JSON result。
	result json.RawMessage
	// err 是 RPC 错误、连接 fatal 或解码错误。
	err error
}

// pendingCall owns the response waiter and optional pre-notification observer.
type pendingCall struct {
	// waiter 接收唯一一次响应或 transport fatal；调用方取消后可置空而不丢 observer。
	waiter chan pendingResponse
	// observe 在 result 解码后同步安装依赖该响应的 runtime 身份；普通调用为 nil。
	observe func(result json.RawMessage) error
}

// rpcResponseWire 是 Codex 无 jsonrpc 字段响应的最小 envelope。
type rpcResponseWire struct {
	// ID 与请求 ID 精确匹配。
	ID json.RawMessage `json:"id"`
	// Result 保留给调用方的生成类型解码。
	Result json.RawMessage `json:"result"`
	// Error 保存 app-server 返回的 JSON-RPC 风格错误对象。
	Error *rpcError `json:"error"`
}

// rpcError 是 app-server 错误响应的稳定 Go 表示。
type rpcError struct {
	// Code 是 app-server 错误码。
	Code int `json:"code"`
	// Message 是可诊断错误文本。
	Message string `json:"message"`
	// Data 保留未解释的服务端附加信息。
	Data json.RawMessage `json:"data,omitempty"`
}

// Error 实现 error，并保留错误码用于 steering 的无活动 turn 判定。
func (e *rpcError) Error() string {
	return fmt.Sprintf("codex app-server error %d: %s", e.Code, e.Message)
}

// appServerTransport owns native frame order, request pairing and fatal state.
type appServerTransport struct {
	// ctx 由 transport 关闭或父进程取消时结束。
	ctx context.Context
	// cancel 终止 transport 拥有的回调上下文。
	cancel context.CancelFunc
	// reader 只由 readLoop goroutine 读取。
	reader io.Reader
	// writer is owned by the caller holding writeSlot.
	writer io.Writer
	// closer 释放拥有 reader/writer 的底层进程或连接。
	closer io.Closer
	// options 保存构造时归一化后的有界策略。
	options transportOptions
	// nextID 生成当前连接内单调递增的整数请求 ID。
	nextID atomic.Int64
	// pendingMu 保护 pending、fatalErr 和 closed。
	pendingMu sync.Mutex
	// pending 按 JSON ID 文本保存等待响应的调用及其可选激活屏障。
	pending map[string]pendingCall
	// fatalErr 是首次致命失败创建的共享稳定错误实例。
	fatalErr error
	// writeSlot serializes complete frames and permits cancellation while queued.
	writeSlot chan struct{}
	// serverSlots 限制服务端请求的并行回调数。
	serverSlots chan struct{}
	// overloadSlot permits a bounded failure reply without blocking the native reader.
	overloadSlot chan struct{}
	// done closes on the first fatal error or explicit shutdown.
	done chan struct{}
	// closeOnce 保证底层资源只释放一次。
	closeOnce sync.Once
}

// newAppServerTransport starts the sole native protocol reader.
func newAppServerTransport(
	parent context.Context,
	reader io.Reader,
	writer io.Writer,
	closer io.Closer,
	options transportOptions,
) *appServerTransport {
	if options.Logger == nil {
		options.Logger = slog.Default()
	}
	if options.MaxLineBytes <= 0 {
		options.MaxLineBytes = defaultMaxAppServerLineBytes
	}
	if options.MaxServerRequests <= 0 {
		options.MaxServerRequests = defaultMaxServerRequests
	}
	if options.MaxPendingCalls <= 0 {
		options.MaxPendingCalls = defaultMaxPendingCalls
	}
	ctx, cancel := context.WithCancel(parent)
	transport := &appServerTransport{
		ctx:          ctx,
		cancel:       cancel,
		reader:       reader,
		writer:       writer,
		closer:       closer,
		options:      options,
		pending:      make(map[string]pendingCall),
		writeSlot:    make(chan struct{}, 1),
		serverSlots:  make(chan struct{}, options.MaxServerRequests),
		overloadSlot: make(chan struct{}, 1),
		done:         make(chan struct{}),
	}
	go transport.readLoop()
	return transport
}

// Call writes a generated native request and joins its matching response.
func (t *appServerTransport) Call(
	ctx context.Context,
	build func(id protocol.RequestID) protocol.ClientRequest,
	result any,
) error {
	return t.call(ctx, build, result, nil)
}

// CallObserved installs response identity before the reader accepts another frame.
func (t *appServerTransport) CallObserved(
	ctx context.Context,
	build func(id protocol.RequestID) protocol.ClientRequest,
	result any,
	observer func() error,
) error {
	return t.call(ctx, build, nil, func(raw json.RawMessage) error {
		if result != nil {
			if err := json.Unmarshal(raw, result); err != nil {
				return fmt.Errorf("decoding observed codex app-server response: %w", err)
			}
		}
		if observer == nil {
			return nil
		}
		return observer()
	})
}

// call owns one bounded native request from admission through response or cancellation.
func (t *appServerTransport) call(
	ctx context.Context,
	build func(id protocol.RequestID) protocol.ClientRequest,
	result any,
	observer func(result json.RawMessage) error,
) error {
	idNumber := t.nextID.Add(1)
	id := protocol.RequestID{Integer: &idNumber}
	key := strconv.FormatInt(idNumber, 10)
	responseChannel := make(chan pendingResponse, 1)

	request := build(id)
	if request == nil {
		return &transportCallError{frameNotWritten, errors.New("codex app-server request builder returned nil")}
	}
	t.pendingMu.Lock()
	if t.fatalErr != nil {
		err := t.fatalErr
		t.pendingMu.Unlock()
		return &transportCallError{frameNotWritten, err}
	}
	if len(t.pending) >= t.options.MaxPendingCalls {
		t.pendingMu.Unlock()
		return &transportCallError{frameNotWritten, errors.New("too many pending Codex app-server requests")}
	}
	t.pending[key] = pendingCall{waiter: responseChannel, observe: observer}
	t.pendingMu.Unlock()

	if err := t.writeJSON(ctx, request); err != nil {
		t.removePending(key)
		return err
	}

	var response pendingResponse
	select {
	case response = <-responseChannel:
	case <-ctx.Done():
		select {
		case response = <-responseChannel:
		default:
			t.detachPendingWaiter(key, responseChannel)
			return &transportCallError{frameWritten, ctx.Err()}
		}
	case <-t.done:
		select {
		case response = <-responseChannel:
		default:
			return &transportCallError{frameWritten, t.Err()}
		}
	}
	if response.err != nil {
		var native *rpcError
		if errors.As(response.err, &native) {
			return response.err
		}
		return &transportCallError{frameWritten, response.err}
	}
	if result != nil {
		if err := json.Unmarshal(response.result, result); err != nil {
			return &transportCallError{frameWritten, fmt.Errorf("decoding %s response: %w", request.Method(), err)}
		}
	}
	return nil
}

// Notify 写入由 protocol 包封闭的客户端通知变体。
func (t *appServerTransport) Notify(ctx context.Context, notification protocol.ClientNotification) error {
	if notification == nil {
		return errors.New("codex app-server notification is nil")
	}
	return t.writeJSON(ctx, notification)
}

// Done 返回 transport 永久结束的通知通道。
func (t *appServerTransport) Done() <-chan struct{} {
	return t.done
}

// Err 返回首次连接致命错误；运行中返回 nil。
func (t *appServerTransport) Err() error {
	t.pendingMu.Lock()
	defer t.pendingMu.Unlock()
	return t.fatalErr
}

// Close 幂等终止回调、解除 pending 并释放底层资源。
func (t *appServerTransport) Close() error {
	var closeErr error
	t.closeOnce.Do(func() {
		t.fail(ErrAppServerClosed)
		if t.closer != nil {
			closeErr = t.closer.Close()
		}
	})
	return closeErr
}

// readLoop drains native NDJSON frames until EOF or an unrecoverable read failure.
func (t *appServerTransport) readLoop() {
	scanner := bufio.NewScanner(t.reader)
	initialBuffer := min(64<<10, t.options.MaxLineBytes)
	scanner.Buffer(make([]byte, initialBuffer), t.options.MaxLineBytes)
	for scanner.Scan() {
		if t.ctx.Err() != nil {
			return
		}
		line := append([]byte(nil), scanner.Bytes()...)
		if len(line) == 0 {
			continue
		}
		t.dispatchLine(line)
	}
	if err := scanner.Err(); err != nil {
		// Scanner 仅在 token 超上限或底层 I/O 失败时返回错误；两者都使帧边界不可恢复。
		if errors.Is(err, bufio.ErrTooLong) || len(scanner.Bytes()) >= t.options.MaxLineBytes {
			t.fail(ErrAppServerFrameTooLarge)
			return
		}
		if (errors.Is(err, os.ErrClosed) || errors.Is(err, os.ErrDeadlineExceeded)) && t.options.EOFError != nil {
			// Go exec.Cmd.Wait 会在返回前关闭 StdoutPipe，scanner 可能先观察到 os.ErrClosed；
			// 仅当唯一 Wait owner 随后确认异常退出时，才用 exit code/stderr
			// 覆盖这个运行时竞态；普通 reader I/O 错误仍保持原因。
			if processErr := t.options.EOFError(); errors.Is(processErr, ErrAppServerExited) {
				t.fail(processErr)
				return
			}
		}
		t.fail(fmt.Errorf("reading codex app-server stdout: %w", err))
		return
	}
	if t.options.EOFError != nil {
		if err := t.options.EOFError(); err != nil {
			t.fail(err)
			return
		}
	}
	t.fail(io.EOF)
}

// dispatchLine 先读取 envelope discriminator，再分派 response、request 或 notification。
func (t *appServerTransport) dispatchLine(line []byte) {
	var envelope struct {
		// ID 非空表示 response 或 server request。
		ID json.RawMessage `json:"id"`
		// Method 非空表示 request 或 notification。
		Method string `json:"method"`
		// Result 标识成功 response。
		Result json.RawMessage `json:"result"`
		// Error 标识失败 response。
		Error json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(line, &envelope); err != nil {
		// malformed 行不会进入协议分发，仅写 stderr logger 便于诊断。
		t.options.Logger.Debug("Ignoring malformed Codex app-server NDJSON", "error", err)
		return
	}
	switch {
	case len(envelope.ID) > 0 && envelope.Method == "" && (len(envelope.Result) > 0 || len(envelope.Error) > 0):
		t.handleResponse(line)
	case len(envelope.ID) > 0 && envelope.Method != "":
		t.handleServerRequest(line, envelope.ID)
	case len(envelope.ID) == 0 && envelope.Method != "":
		t.handleNotification(line)
	default:
		t.options.Logger.Debug("Ignoring unknown Codex app-server envelope")
	}
}

// handleResponse 将结果投递给匹配 ID 的唯一 pending 调用。
func (t *appServerTransport) handleResponse(line []byte) {
	var response rpcResponseWire
	if err := json.Unmarshal(line, &response); err != nil {
		t.options.Logger.Debug("Ignoring undecodable Codex app-server response", "error", err)
		return
	}
	key := normalizeJSONID(response.ID)
	t.pendingMu.Lock()
	call, ok := t.pending[key]
	if ok {
		delete(t.pending, key)
	}
	t.pendingMu.Unlock()
	if !ok {
		return
	}
	var responseErr error
	if response.Error != nil {
		responseErr = response.Error
	}
	if responseErr == nil && call.observe != nil {
		// observer 只同步安装 identity；迟到 turn 的 interrupt 由 Agent 交给自有 goroutine，绝不在 readLoop 内等待新 RPC。
		responseErr = call.observe(response.Result)
	}
	if call.waiter != nil {
		call.waiter <- pendingResponse{result: response.Result, err: responseErr}
	}
}

// handleNotification 使用 protocol 的 discriminator-first union 解码已知通知。
func (t *appServerTransport) handleNotification(line []byte) {
	notification, err := protocol.DecodeServerNotification(line)
	if err != nil {
		t.options.Logger.Debug("Ignoring undecodable Codex app-server notification", "error", err)
		return
	}
	if t.options.NotificationHandler != nil {
		t.options.NotificationHandler(t.ctx, notification)
	}
}

// handleServerRequest handles native callbacks within bounded reverse-request slots.
func (t *appServerTransport) handleServerRequest(line, id json.RawMessage) {
	select {
	case t.serverSlots <- struct{}{}:
		go func() {
			defer func() { <-t.serverSlots }()
			request, err := protocol.DecodeServerRequest(line)
			if err != nil {
				t.writeServerError(id, -32602, err.Error())
				return
			}
			if t.options.ServerRequestHandler == nil {
				t.writeServerError(id, -32601, "server request handler is not installed")
				return
			}
			result, err := t.options.ServerRequestHandler(t.ctx, request)
			if err != nil {
				t.writeServerError(id, -32603, err.Error())
				return
			}
			_ = t.writeJSON(t.ctx, struct {
				// ID 原样回显 app-server 的整数或字符串标识。
				ID json.RawMessage `json:"id"`
				// Result 是消费方生成的审批结果。
				Result any `json:"result"`
			}{ID: id, Result: result})
		}()
	default:
		select {
		case t.overloadSlot <- struct{}{}:
			go func() {
				defer func() { <-t.overloadSlot }()
				t.writeServerError(id, -32000, "too many concurrent server requests")
			}()
		default:
			t.fail(errors.New("Codex server requests exceeded callback and overload reply capacity"))
		}
	}
}

// writeServerError 为服务端请求写入不含 jsonrpc 的错误响应。
func (t *appServerTransport) writeServerError(id json.RawMessage, code int, message string) {
	_ = t.writeJSON(t.ctx, struct {
		// ID 原样回显服务端请求标识。
		ID json.RawMessage `json:"id"`
		// Error 保存 app-server 可识别的 JSON-RPC 错误对象。
		Error *rpcError `json:"error"`
	}{ID: id, Error: &rpcError{Code: code, Message: message}})
}

// writeJSON acquires a cancellable writer slot and emits exactly one complete frame.
func (t *appServerTransport) writeJSON(ctx context.Context, message any) error {
	data, err := json.Marshal(message)
	if err != nil {
		return &transportCallError{frameNotWritten, fmt.Errorf("encoding codex app-server message: %w", err)}
	}
	data = append(data, '\n')
	select {
	case <-ctx.Done():
		return &transportCallError{frameNotWritten, ctx.Err()}
	case <-t.done:
		return &transportCallError{frameNotWritten, t.Err()}
	case t.writeSlot <- struct{}{}:
	}
	defer func() { <-t.writeSlot }()
	if err := ctx.Err(); err != nil {
		return &transportCallError{frameNotWritten, err}
	}
	if err := t.Err(); err != nil {
		return &transportCallError{frameNotWritten, err}
	}
	n, err := t.writeFrame(ctx, data)
	if err == nil && n != len(data) {
		err = io.ErrShortWrite
	}
	if err != nil {
		delivery := framePartial
		switch {
		case n <= 0:
			delivery = frameNotWritten
		case n == len(data):
			delivery = frameWritten
		}
		if ctx.Err() != nil && delivery == frameNotWritten {
			return &transportCallError{frameNotWritten, ctx.Err()}
		}
		t.fail(fmt.Errorf("writing codex app-server stdin: %w", err))
		return &transportCallError{delivery, t.Err()}
	}
	return nil
}

// writeFrame interrupts an in-progress write when the writer supports deadlines.
// The write slot stays held until the callback is stopped and the deadline cleared.
func (t *appServerTransport) writeFrame(ctx context.Context, data []byte) (int, error) {
	if writer, ok := t.writer.(interface {
		// SetWriteDeadline sets or clears the current write deadline.
		SetWriteDeadline(time.Time) error
	}); ok {
		if err := writer.SetWriteDeadline(time.Time{}); err == nil {
			stopped := make(chan struct{})
			stop := context.AfterFunc(ctx, func() {
				_ = writer.SetWriteDeadline(time.Now())
				close(stopped)
			})
			defer func() {
				if !stop() {
					<-stopped
				}
				_ = writer.SetWriteDeadline(time.Time{})
			}()
		}
	}
	return t.writer.Write(data)
}

// removePending 仅在调用方取消或写失败时删除仍属于它的 pending。
func (t *appServerTransport) removePending(key string) {
	t.pendingMu.Lock()
	delete(t.pending, key)
	t.pendingMu.Unlock()
}

// detachPendingWaiter 仅分离仍属于当前调用方的 waiter。
// 带 observer 的 turn/start 继续由 transport 拥有，直到迟到 response、fatal 或 Adapter Close 形成有界终点。
func (t *appServerTransport) detachPendingWaiter(key string, waiter chan pendingResponse) {
	t.pendingMu.Lock()
	defer t.pendingMu.Unlock()
	call, ok := t.pending[key]
	if !ok || call.waiter != waiter {
		return
	}
	if call.observe == nil {
		delete(t.pending, key)
		return
	}
	call.waiter = nil
	t.pending[key] = call
}

// fail publishes one immutable fatal cause and releases pending request waiters.
func (t *appServerTransport) fail(cause error) {
	t.pendingMu.Lock()
	if t.fatalErr != nil {
		t.pendingMu.Unlock()
		return
	}
	if errors.Is(cause, ErrAppServerFrameTooLarge) {
		t.fatalErr = fmt.Errorf("%w: %w", ErrAppServerUnavailable, ErrAppServerFrameTooLarge)
	} else if errors.Is(cause, ErrAppServerClosed) {
		t.fatalErr = fmt.Errorf("%w: %w", ErrAppServerUnavailable, ErrAppServerClosed)
	} else {
		// 保留 process/I/O 根因，调用方既能按稳定 unavailable 分类，也能继续 errors.Is/As 诊断具体退出。
		t.fatalErr = fmt.Errorf("%w: %w", ErrAppServerUnavailable, cause)
	}
	stableError := t.fatalErr
	pending := t.pending
	t.pending = make(map[string]pendingCall)
	close(t.done)
	t.pendingMu.Unlock()

	// 不持锁投递，避免等待者恢复后立刻查询 transport 状态造成锁反转。
	for _, call := range pending {
		if call.waiter != nil {
			call.waiter <- pendingResponse{err: stableError}
		}
	}
	t.cancel()
}

// normalizeJSONID 把整数或字符串 ID 归一为互不冲突的 map key。
func normalizeJSONID(id json.RawMessage) string {
	if len(id) > 0 && id[0] == '"' {
		return "s:" + string(id)
	}
	return string(id)
}

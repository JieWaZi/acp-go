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

	"github.com/JieWaZi/acp-go/pkg/codex/protocol"
)

const (
	// defaultMaxAppServerLineBytes 限制单条 app-server NDJSON 消息，防止无界缓冲。
	defaultMaxAppServerLineBytes = 8 << 20
	// defaultMaxServerRequests 限制同时执行的 app-server→ACP 回调数。
	defaultMaxServerRequests = 16
)

var (
	// ErrAppServerUnavailable 表示内层连接已永久不可用。
	ErrAppServerUnavailable = errors.New("codex app-server unavailable")
	// ErrAppServerClosed 表示 Adapter 主动关闭了内层连接。
	ErrAppServerClosed = errors.New("codex app-server transport closed")
	// ErrAppServerFrameTooLarge 表示 app-server 输出了超过配置上限的 NDJSON 帧。
	ErrAppServerFrameTooLarge = errors.New("codex app-server frame too large")
)

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
	// MaxServerRequests 是并行 server request 的硬上限。
	MaxServerRequests int
	// NotificationHandler 接收服务端通知；nil 表示忽略通知。
	NotificationHandler notificationHandler
	// ServerRequestHandler 接收审批等服务端请求；nil 时返回 method-not-found。
	ServerRequestHandler serverRequestHandler
	// EOFError 在真实进程 stdout EOF 后返回已包含退出码/stderr 的稳定错误。
	EOFError func() error
}

// pendingResponse 保存一个等待 app-server 响应的调用。
type pendingResponse struct {
	// result 是响应成功时的原始 JSON result。
	result json.RawMessage
	// err 是 RPC 错误、连接 fatal 或解码错误。
	err error
}

// pendingCall 保存等待通道和可选的同步 response observer。
// observer 只用于 turn/start 激活屏障，必须在 readLoop 继续读取下一帧前返回。
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

// appServerTransport 实现无 jsonrpc 的 NDJSON 边界。
// transport 显式限制帧大小、待处理请求数量和反向请求并发。
type appServerTransport struct {
	// ctx 由 transport 关闭或父进程取消时结束。
	ctx context.Context
	// cancel 终止 transport 拥有的回调上下文。
	cancel context.CancelFunc
	// reader 只由 readLoop goroutine 读取。
	reader io.Reader
	// writer 写入由 writeMu 串行化，保持每帧原子顺序。
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
	// writeMu 防止并发请求或服务端回复交叉写入同一 NDJSON 行。
	writeMu sync.Mutex
	// serverSlots 限制服务端请求的并行回调数。
	serverSlots chan struct{}
	// done 在首次 fatal/close 时关闭。
	done chan struct{}
	// closeOnce 保证底层资源只释放一次。
	closeOnce sync.Once
}

// newAppServerTransport 创建 transport 并立即启动唯一读取循环。
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
	ctx, cancel := context.WithCancel(parent)
	transport := &appServerTransport{
		ctx:         ctx,
		cancel:      cancel,
		reader:      reader,
		writer:      writer,
		closer:      closer,
		options:     options,
		pending:     make(map[string]pendingCall),
		serverSlots: make(chan struct{}, options.MaxServerRequests),
		done:        make(chan struct{}),
	}
	go transport.readLoop()
	return transport
}

// Call 先安装 pending，再发送由强类型构造器绑定 method/params 的请求并等待响应。
func (t *appServerTransport) Call(
	ctx context.Context,
	build func(id protocol.RequestID) protocol.ClientRequest,
	result any,
) error {
	return t.call(ctx, build, result, nil)
}

// CallObserved 在 result 解码后、readLoop 继续下一帧前同步执行 observer。
// observer 用于在后续通知可见前安装 Turn identity。
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

// call 实现普通响应交付与可选同步 observer 共用的 pending 生命周期。
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

	t.pendingMu.Lock()
	if t.fatalErr != nil {
		err := t.fatalErr
		t.pendingMu.Unlock()
		return err
	}
	t.pending[key] = pendingCall{waiter: responseChannel, observe: observer}
	t.pendingMu.Unlock()

	request := build(id)
	if request == nil {
		t.removePending(key)
		return errors.New("codex app-server request builder returned nil")
	}
	if err := t.writeJSON(ctx, request); err != nil {
		t.removePending(key)
		return err
	}

	select {
	case response := <-responseChannel:
		if response.err != nil {
			return response.err
		}
		if result == nil {
			return nil
		}
		if err := json.Unmarshal(response.result, result); err != nil {
			return fmt.Errorf("decoding %s response: %w", request.Method(), err)
		}
		return nil
	case <-ctx.Done():
		// 普通调用可直接删除；turn/start 必须仅分离本地 waiter，保留 server 迟到响应的一次 observer。
		t.detachPendingWaiter(key, responseChannel)
		return ctx.Err()
	case <-t.done:
		t.pendingMu.Lock()
		err := t.fatalErr
		t.pendingMu.Unlock()
		if err == nil {
			return ErrAppServerUnavailable
		}
		return err
	}
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

// readLoop 按 newline 拆分并分发 app-server 帧。
func (t *appServerTransport) readLoop() {
	scanner := bufio.NewScanner(t.reader)
	initialBuffer := min(64<<10, t.options.MaxLineBytes)
	scanner.Buffer(make([]byte, initialBuffer), t.options.MaxLineBytes)
	for scanner.Scan() {
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
		if errors.Is(err, os.ErrClosed) && t.options.EOFError != nil {
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
		t.options.Logger.Debug("忽略 Codex app-server malformed NDJSON", "error", err)
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
		t.options.Logger.Debug("忽略 Codex app-server 未知 envelope")
	}
}

// handleResponse 将结果投递给匹配 ID 的唯一 pending 调用。
func (t *appServerTransport) handleResponse(line []byte) {
	var response rpcResponseWire
	if err := json.Unmarshal(line, &response); err != nil {
		t.options.Logger.Debug("忽略无法解码的 Codex app-server response", "error", err)
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
		t.options.Logger.Debug("忽略无法解码的 Codex app-server notification", "error", err)
		return
	}
	if t.options.NotificationHandler != nil {
		t.options.NotificationHandler(t.ctx, notification)
	}
}

// handleServerRequest 在有界槽位中解码服务端请求并写回原始 ID。
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
		t.writeServerError(id, -32000, "too many concurrent server requests")
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

// writeJSON 在单一锁内完成 marshal 和一行写入，禁止帧交叉。
func (t *appServerTransport) writeJSON(ctx context.Context, message any) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.done:
		return t.Err()
	default:
	}
	data, err := json.Marshal(message)
	if err != nil {
		return fmt.Errorf("encoding codex app-server message: %w", err)
	}
	data = append(data, '\n')
	t.writeMu.Lock()
	defer t.writeMu.Unlock()
	if _, err = t.writer.Write(data); err != nil {
		t.fail(fmt.Errorf("writing codex app-server stdin: %w", err))
		return t.Err()
	}
	return nil
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

// fail 只接受首次 fatal，并把同一错误实例广播给全部 pending。
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

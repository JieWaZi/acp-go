package claude

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

	"github.com/JieWaZi/acp-go/pkg/claude/protocol"
)

const (
	defaultMaxClaudeLineBytes       = 8 << 20
	defaultMaxClaudeControlRequests = 16
	defaultMaxClaudePendingCalls    = 256
)

var (
	// ErrClaudeTransportClosed 表示宿主主动关闭了 Session transport。
	ErrClaudeTransportClosed = errors.New("claude transport closed")
	// ErrClaudeTransportUnavailable 表示 transport 已不可恢复。
	ErrClaudeTransportUnavailable = errors.New("claude transport unavailable")
	// ErrClaudeFrameTooLarge 表示 CLI 输出超过单帧上限。
	ErrClaudeFrameTooLarge = errors.New("claude frame too large")
	// ErrClaudePendingLimit 表示破损调用方超过 control pending 硬上限。
	ErrClaudePendingLimit = errors.New("too many pending claude control requests")
)

// claudeMessageHandler 按 CLI 到达顺序处理非 control-response 消息。
type claudeMessageHandler func(ctx context.Context, message protocol.Message)

// claudeControlHandler 处理 CLI 发起的 can_use_tool 等 control request。
type claudeControlHandler func(ctx context.Context, request *protocol.ControlRequestMessage) (any, error)

// transportOptions 保存 Claude JSONL/control 的有界策略与回调。
type transportOptions struct {
	// Logger 记录不含协议原文的诊断信息。
	Logger *slog.Logger
	// MaxLineBytes 是单条 JSONL 消息的字节上限。
	MaxLineBytes int
	// MaxControlRequests 是 CLI 控制请求的最大并发数。
	MaxControlRequests int
	// MaxPendingCalls 是宿主等待控制响应的最大数量。
	MaxPendingCalls int
	// MessageHandler 按到达顺序接收普通消息。
	MessageHandler claudeMessageHandler
	// ControlHandler 处理 CLI 发起的控制请求。
	ControlHandler claudeControlHandler
	// EOFError 在输出结束时提供更具体的进程错误。
	EOFError func() error
}

// controlResult 保存一个匹配 control response 的结果。
type controlResult struct {
	// raw 是成功响应的原始结果字段。
	raw json.RawMessage
	// err 是控制请求失败或 transport 结束原因。
	err error
}

// claudeTransport 管理 Claude CLI 的 JSONL/control 读写、关联与关闭。
type claudeTransport struct {
	// ctx 在 transport 永久结束时取消。
	ctx context.Context
	// cancel 终止所有派生的控制请求处理。
	cancel context.CancelFunc
	// reader 是唯一 stdout 读取来源。
	reader io.Reader
	// writer 是唯一 stdin 写入目标。
	writer io.Writer
	// closer 用于主动中断阻塞中的 reader。
	closer io.Closer
	// options 保存边界和回调配置。
	options transportOptions
	// nextID 生成进程内单调递增的控制请求编号。
	nextID atomic.Uint64
	// pendingMu 保护等待表与首次致命错误。
	pendingMu sync.Mutex
	// pending 按请求编号关联等待中的调用。
	pending map[string]chan controlResult
	// fatalErr 是不可恢复的首次 transport 错误。
	fatalErr error
	// writeMu 保证每条 JSONL 帧完整写入。
	writeMu sync.Mutex
	// controlSlots 限制 CLI 控制请求处理并发度。
	controlSlots chan struct{}
	// incomingMu 保护 CLI 发起且仍在执行的控制请求。
	incomingMu sync.Mutex
	// incoming 按 request_id 保存反向控制请求的取消函数。
	incoming map[string]context.CancelFunc
	// done 在 transport 永久结束时关闭。
	done chan struct{}
	// failOnce 保证只发布一次失败并只关闭一次 done。
	failOnce sync.Once
	// closeOnce 保证底层 reader 只关闭一次。
	closeOnce sync.Once
}

// newClaudeTransport 创建 transport 并立即启动唯一 stdout reader。
func newClaudeTransport(parent context.Context, reader io.Reader, writer io.Writer, closer io.Closer, options transportOptions) *claudeTransport {
	if options.Logger == nil {
		options.Logger = slog.Default()
	}
	if options.MaxLineBytes <= 0 {
		options.MaxLineBytes = defaultMaxClaudeLineBytes
	}
	if options.MaxControlRequests <= 0 {
		options.MaxControlRequests = defaultMaxClaudeControlRequests
	}
	if options.MaxPendingCalls <= 0 {
		options.MaxPendingCalls = defaultMaxClaudePendingCalls
	}
	ctx, cancel := context.WithCancel(parent)
	transport := &claudeTransport{
		ctx: ctx, cancel: cancel, reader: reader, writer: writer, closer: closer,
		options: options, pending: make(map[string]chan controlResult),
		incoming:     make(map[string]context.CancelFunc),
		controlSlots: make(chan struct{}, options.MaxControlRequests), done: make(chan struct{}),
	}
	go transport.readLoop()
	return transport
}

// Call 发送 control request 并等待匹配 response；取消时向 CLI 写 control_cancel_request。
func (t *claudeTransport) Call(ctx context.Context, request any, result any) error {
	id := "go-" + strconv.FormatUint(t.nextID.Add(1), 10)
	wire, err := protocol.NewControlRequest(id, request)
	if err != nil {
		return err
	}
	waiter := make(chan controlResult, 1)
	t.pendingMu.Lock()
	if t.fatalErr != nil {
		err = t.fatalErr
		t.pendingMu.Unlock()
		return err
	}
	if len(t.pending) >= t.options.MaxPendingCalls {
		t.pendingMu.Unlock()
		return ErrClaudePendingLimit
	}
	t.pending[id] = waiter
	t.pendingMu.Unlock()
	if err := t.Write(ctx, wire); err != nil {
		t.removePending(id, waiter)
		return err
	}

	select {
	case response := <-waiter:
		if response.err != nil {
			return response.err
		}
		if result == nil || len(response.raw) == 0 || string(response.raw) == "null" {
			return nil
		}
		if err := json.Unmarshal(response.raw, result); err != nil {
			return fmt.Errorf("decoding Claude control response: %w", err)
		}
		return nil
	case <-ctx.Done():
		if t.removePending(id, waiter) {
			cancel, cancelErr := protocol.NewControlCancelRequest(id)
			if cancelErr == nil {
				_ = t.Write(context.WithoutCancel(ctx), cancel)
			}
		}
		return ctx.Err()
	case <-t.done:
		return t.Err()
	}
}

// Write 把任意已验证 wire 对象编码为原子 JSONL 帧。
func (t *claudeTransport) Write(ctx context.Context, message any) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.done:
		return t.Err()
	default:
	}
	data, err := json.Marshal(message)
	if err != nil {
		return fmt.Errorf("encoding Claude message: %w", err)
	}
	data = append(data, '\n')
	t.writeMu.Lock()
	defer t.writeMu.Unlock()
	if _, err := t.writer.Write(data); err != nil {
		t.fail(fmt.Errorf("writing Claude stdin: %w", err))
		return t.Err()
	}
	return nil
}

// Done 返回 transport 永久结束通知。
func (t *claudeTransport) Done() <-chan struct{} { return t.done }

// Err 返回首次 fatal；运行中为 nil。
func (t *claudeTransport) Err() error {
	t.pendingMu.Lock()
	defer t.pendingMu.Unlock()
	if t.fatalErr == nil {
		return ErrClaudeTransportUnavailable
	}
	return t.fatalErr
}

// Close 幂等解除 pending、终止 reader context 并关闭可选 reader。
func (t *claudeTransport) Close() error {
	var closeErr error
	t.closeOnce.Do(func() {
		t.fail(ErrClaudeTransportClosed)
		if t.closer != nil {
			closeErr = t.closer.Close()
		}
	})
	return closeErr
}

// readLoop 按 newline 拆帧并保持 CLI 消息到达顺序。
func (t *claudeTransport) readLoop() {
	scanner := bufio.NewScanner(t.reader)
	scanner.Buffer(make([]byte, min(64<<10, t.options.MaxLineBytes)), t.options.MaxLineBytes)
	for scanner.Scan() {
		line := append([]byte(nil), scanner.Bytes()...)
		if len(line) == 0 {
			continue
		}
		t.dispatchLine(line)
	}
	if err := scanner.Err(); err != nil {
		if errors.Is(err, bufio.ErrTooLong) || len(scanner.Bytes()) >= t.options.MaxLineBytes {
			t.fail(ErrClaudeFrameTooLarge)
			return
		}
		if errors.Is(err, os.ErrClosed) && t.options.EOFError != nil {
			if processErr := t.options.EOFError(); processErr != nil {
				t.fail(processErr)
				return
			}
		}
		t.fail(fmt.Errorf("reading Claude stdout: %w", err))
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

// dispatchLine 路由 response、CLI control request 与普通消息。
func (t *claudeTransport) dispatchLine(line []byte) {
	message, err := protocol.DecodeMessage(line)
	if err != nil {
		// 单条坏帧不破坏后续行边界；只记录长度和错误，避免原始内容泄露。
		t.options.Logger.Debug("Ignoring malformed Claude CLI JSONL", "bytes", len(line), "error", err)
		return
	}
	switch typed := message.(type) {
	case *protocol.ControlResponseMessage:
		t.handleResponse(typed)
	case *protocol.ControlRequestMessage:
		t.handleControlRequest(typed)
	case *protocol.ControlCancelRequestMessage:
		t.handleControlCancel(typed.RequestID)
	default:
		if t.options.MessageHandler != nil {
			t.options.MessageHandler(t.ctx, message)
		}
	}
}

// handleResponse 只完成 request_id 匹配的 pending 调用；迟到/重复响应安全忽略。
func (t *claudeTransport) handleResponse(message *protocol.ControlResponseMessage) {
	id := message.Response.RequestID
	t.pendingMu.Lock()
	waiter, ok := t.pending[id]
	if ok {
		delete(t.pending, id)
	}
	t.pendingMu.Unlock()
	if !ok {
		return
	}
	if message.Response.Subtype == "error" {
		waiter <- controlResult{err: fmt.Errorf("Claude control request failed: %s", message.Response.Error)}
		return
	}
	waiter <- controlResult{raw: append(json.RawMessage(nil), message.Response.Response...)}
}

// handleControlRequest 在有界槽位内执行 CLI 回调并写回 success/error。
func (t *claudeTransport) handleControlRequest(message *protocol.ControlRequestMessage) {
	select {
	case t.controlSlots <- struct{}{}:
		requestCtx, cancel := context.WithCancel(t.ctx)
		t.incomingMu.Lock()
		if _, exists := t.incoming[message.RequestID]; exists {
			t.incomingMu.Unlock()
			cancel()
			<-t.controlSlots
			t.writeControlError(message.RequestID, "duplicate control request id")
			return
		}
		t.incoming[message.RequestID] = cancel
		t.incomingMu.Unlock()
		go func() {
			defer func() {
				t.incomingMu.Lock()
				delete(t.incoming, message.RequestID)
				t.incomingMu.Unlock()
				cancel()
				<-t.controlSlots
			}()
			if t.options.ControlHandler == nil {
				t.writeControlError(message.RequestID, "control request handler is not installed")
				return
			}
			result, err := t.options.ControlHandler(requestCtx, message)
			if err != nil {
				t.writeControlError(message.RequestID, err.Error())
				return
			}
			response, err := protocol.NewControlSuccess(message.RequestID, result)
			if err == nil {
				_ = t.Write(t.ctx, response)
			}
		}()
	default:
		t.writeControlError(message.RequestID, "too many concurrent control requests")
	}
}

// handleControlCancel 取消 CLI 指定的反向控制请求；未知或已完成 ID 安全忽略。
func (t *claudeTransport) handleControlCancel(requestID string) {
	t.incomingMu.Lock()
	cancel := t.incoming[requestID]
	t.incomingMu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// writeControlError 只向 CLI 返回有界固定错误，不把 ACP 详情原样暴露到 wire。
func (t *claudeTransport) writeControlError(requestID, message string) {
	response, err := protocol.NewControlError(requestID, message)
	if err == nil {
		_ = t.Write(t.ctx, response)
	}
}

// removePending 只删除仍属于当前调用的 waiter，避免取消误删复用 ID。
func (t *claudeTransport) removePending(id string, waiter chan controlResult) bool {
	t.pendingMu.Lock()
	defer t.pendingMu.Unlock()
	current, ok := t.pending[id]
	if ok && current == waiter {
		delete(t.pending, id)
		return true
	}
	return false
}

// fail 发布首次 fatal 并解除全部 pending；后续失败不覆盖真实进程原因。
func (t *claudeTransport) fail(err error) {
	if err == nil {
		err = ErrClaudeTransportUnavailable
	}
	t.failOnce.Do(func() {
		t.cancel()
		t.incomingMu.Lock()
		incoming := t.incoming
		t.incoming = make(map[string]context.CancelFunc)
		t.incomingMu.Unlock()
		for _, cancel := range incoming {
			cancel()
		}
		t.pendingMu.Lock()
		t.fatalErr = err
		pending := t.pending
		t.pending = make(map[string]chan controlResult)
		t.pendingMu.Unlock()
		for _, waiter := range pending {
			waiter <- controlResult{err: err}
		}
		close(t.done)
	})
}

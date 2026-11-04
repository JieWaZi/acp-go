// NeuxBot 本地修改：在上游 v0.13.5 基础上增加显式通知背压、字节预算和所有者关闭。
// 原版权与 Apache-2.0 许可保留于 LICENSE，具体改动见 PATCHES.md。
package acp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	notificationQueueDrainTimeout = 5 * time.Second
	defaultMaxQueuedNotifications = 1024
)

var errNotificationQueueOverflow = errors.New("notification queue overflow")

// ErrNotificationQueueLimit 表示单条通知无法放入已配置的字节预算。
var ErrNotificationQueueLimit = errors.New("notification exceeds queue byte budget")

// ConnectionOptions 显式配置通知背压与传输资源的所有者；旧构造方法保持上游行为。
type ConnectionOptions struct {
	// Context 结束时独立取消处理器并关闭传输，不依赖通知消费者继续推进。
	Context context.Context
	// NotificationQueueCapacity 限制排队及正在处理的通知总数。
	NotificationQueueCapacity int
	// NotificationQueueBytes 限制排队及正在处理的通知帧总字节。
	NotificationQueueBytes int64
	// CloseTransport 中断底层阻塞读写；调用方明确把该关闭能力交给连接所有者。
	CloseTransport func() error
	// ReceiveNotification records technical evidence before notification dispatch.
	// It is fixed at construction and runs synchronously on the reader. Limit it
	// to a bounded local state update. It must not wait, perform I/O, call back
	// into the connection, or invoke application observers.
	ReceiveNotification func(ReceiveNotification)
}

// ReceiveNotification identifies a notification in the reader's frame order.
type ReceiveNotification struct {
	// Sequence shares the same per-connection order as response receipts.
	Sequence uint64
	// Method is the incoming notification method.
	Method string
	// Params is the borrowed read-only payload for the duration of the callback.
	Params json.RawMessage
}

type anyMessage struct {
	JSONRPC string           `json:"jsonrpc"`
	ID      *json.RawMessage `json:"id,omitempty"`
	Method  string           `json:"method,omitempty"`
	Params  json.RawMessage  `json:"params,omitempty"`
	Result  json.RawMessage  `json:"result,omitempty"`
	Error   *RequestError    `json:"error,omitempty"`
}

type queuedNotification struct {
	seq uint64
	msg *anyMessage
	// bytes 是该通知占用的原始帧字节，用于处理完成后归还预算。
	bytes int64
	// run is a continuation prepared before this notification entered the queue.
	run inboundContinuation
}

type responseEnvelope struct {
	msg                   anyMessage
	sequence              uint64
	notificationWatermark uint64
}

type pendingResponse struct {
	ch chan responseEnvelope
}

type cancelRequestParams struct {
	RequestID json.RawMessage `json:"requestId"`
}

type MethodHandler func(ctx context.Context, method string, params json.RawMessage) (any, *RequestError)

// A preparer runs on the reader. A non-nil continuation owns dispatch for
// that frame. A nil continuation leaves dispatch to the usual handler.
type inboundContinuation func(context.Context) (any, *RequestError)
type methodPreparer func(context.Context, *anyMessage) inboundContinuation

// Connection is a simple JSON-RPC 2.0 connection over line-delimited JSON.
type Connection struct {
	w       io.Writer
	r       io.Reader
	handler MethodHandler
	// prepareInbound is fixed before the connection starts reading.
	prepareInbound methodPreparer

	mu                   sync.Mutex
	writeGate            chan struct{}
	nextID               atomic.Uint64
	pending              map[string]*pendingResponse
	inflight             map[string]context.CancelCauseFunc
	pendingCancelRequest []string
	cancelRequestSignal  chan struct{}

	// ctx/cancel govern connection lifetime and are used for Done() and for canceling
	// callers waiting on responses when the peer disconnects.
	ctx    context.Context
	cancel context.CancelCauseFunc

	// inboundCtx/inboundCancel are used when invoking the inbound MethodHandler.
	// This ctx is intentionally kept alive long enough to process notifications
	// that were successfully received and queued just before a peer disconnect.
	// Otherwise, handlers that respect context cancellation may drop end-of-connection
	// messages that we already read off the wire.
	inboundCtx    context.Context
	inboundCancel context.CancelCauseFunc

	logger *slog.Logger

	notifyMu sync.Mutex
	// notifyCond coordinates response-scoped waits for sequential notification processing.
	notifyCond *sync.Cond
	// invariant: completedNotificationSeq <= lastEnqueuedNotificationSeq.
	lastEnqueuedNotificationSeq uint64
	completedNotificationSeq    uint64

	// notificationQueue serializes notification processing to maintain order.
	// It is bounded to keep memory usage predictable.
	notificationQueue chan queuedNotification
	// options 仅由显式构造方法启用，保留默认连接的上游失败策略。
	options *ConnectionOptions
	// queuedCount 包括队列中和正在执行的通知，受 notifyMu 保护。
	queuedCount int
	// queuedBytes 包括队列中和正在执行的通知帧字节，受 notifyMu 保护。
	queuedBytes int64
	// queueChanged 在消费者归还预算时唤醒等待者，受 notifyMu 保护。
	queueChanged chan struct{}
	// transportCloseOnce 保证强制取消或读端结束只关闭传输一次。
	transportCloseOnce sync.Once
}

func NewConnection(handler MethodHandler, peerInput io.Writer, peerOutput io.Reader) *Connection {
	c := newConnection(handler, nil, peerInput, peerOutput, nil)
	c.start()
	return c
}

// NewConnectionWithOptions 为有明确资源所有者的连接启用有界背压。
func NewConnectionWithOptions(handler MethodHandler, peerInput io.Writer, peerOutput io.Reader, options ConnectionOptions) (*Connection, error) {
	if options.Context == nil || options.CloseTransport == nil || options.NotificationQueueCapacity <= 0 || options.NotificationQueueBytes <= 0 {
		return nil, errors.New("backpressure requires owner context, transport close function and positive count/byte budgets")
	}
	c := newConnection(handler, nil, peerInput, peerOutput, &options)
	c.start()
	return c, nil
}

func newConnection(handler MethodHandler, prepare methodPreparer, peerInput io.Writer, peerOutput io.Reader, options *ConnectionOptions) *Connection {
	ctx, cancel := context.WithCancelCause(context.Background())
	inboundCtx, inboundCancel := context.WithCancelCause(context.Background())
	capacity := defaultMaxQueuedNotifications
	if options != nil {
		capacity = options.NotificationQueueCapacity
	}
	c := &Connection{
		w:                   peerInput,
		writeGate:           make(chan struct{}, 1),
		r:                   peerOutput,
		handler:             handler,
		prepareInbound:      prepare,
		pending:             make(map[string]*pendingResponse),
		inflight:            make(map[string]context.CancelCauseFunc),
		cancelRequestSignal: make(chan struct{}, 1),
		ctx:                 ctx,
		cancel:              cancel,
		inboundCtx:          inboundCtx,
		inboundCancel:       inboundCancel,
		notificationQueue:   make(chan queuedNotification, capacity),
		options:             options,
		queueChanged:        make(chan struct{}),
	}
	c.notifyCond = sync.NewCond(&c.notifyMu)
	return c
}

func (c *Connection) start() {
	go func() {
		<-c.ctx.Done()
		c.notifyMu.Lock()
		c.notifyCond.Broadcast()
		c.notifyMu.Unlock()
	}()
	go c.sendCancelRequests()
	go c.processNotifications()
	if options := c.options; options != nil {
		go func() {
			select {
			case <-options.Context.Done():
				c.closeWithCause(context.Cause(options.Context))
			case <-c.ctx.Done():
				c.closeTransport()
			}
		}()
	}
	go c.receive()
}

// Close 立即停止处理并中断显式拥有的传输；队列仍由唯一 reader 关闭。
func (c *Connection) Close() { c.closeWithCause(context.Canceled) }

// Err 返回首个连接失败或所有者取消原因。
func (c *Connection) Err() error { return context.Cause(c.ctx) }

func (c *Connection) closeWithCause(cause error) {
	if cause == nil {
		cause = context.Canceled
	}
	c.cancel(cause)
	c.inboundCancel(cause)
	c.closeTransport()
}

func (c *Connection) closeTransport() {
	if c.options != nil {
		c.transportCloseOnce.Do(func() { _ = c.options.CloseTransport() })
	}
}

// SetLogger installs a logger used for internal connection diagnostics.
// If unset, logs are written via the default logger.
func (c *Connection) SetLogger(l *slog.Logger) { c.logger = l }

func (c *Connection) loggerOrDefault() *slog.Logger {
	if c.logger != nil {
		return c.logger
	}
	return slog.Default()
}

const (
	maxCanonicalJSONRPCIDKeyLen   = 4096
	maxCanonicalJSONRPCIDAbsExp10 = 4096
	maxPendingCancelRequests      = 1024
)

var (
	errInvalidJSONRPCNumericID  = errors.New("invalid json-rpc numeric id")
	errJSONRPCNumericIDTooLarge = errors.New("json-rpc numeric id too large")
)

func canonicalJSONRPCIDKey(raw json.RawMessage) (string, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return "", errors.New("empty json-rpc id")
	}

	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.UseNumber()

	var id any
	if err := dec.Decode(&id); err != nil {
		return "", err
	}

	// Ensure the id contains a single JSON value.
	var trailing any
	if err := dec.Decode(&trailing); err == nil {
		return "", errors.New("invalid json-rpc id: trailing data")
	} else if !errors.Is(err, io.EOF) {
		return "", err
	}

	switch v := id.(type) {
	case nil:
		return "null", nil
	case string:
		canon, err := json.Marshal(v)
		if err != nil {
			return "", err
		}
		return string(canon), nil
	case json.Number:
		return canonicalJSONRPCNumericIDKey(v)
	default:
		return "", errors.New("json-rpc id must be string, number, or null")
	}
}

func canonicalJSONRPCNumericIDKey(v json.Number) (string, error) {
	raw := strings.TrimSpace(v.String())
	if raw == "" {
		return "", errInvalidJSONRPCNumericID
	}

	negative, digits, exp10, err := parseJSONRPCNumericID(raw)
	if err != nil {
		return "", err
	}

	return formatCanonicalJSONRPCNumericID(negative, digits, exp10)
}

func parseJSONRPCNumericID(raw string) (negative bool, digits string, exp10 int, err error) {
	i := 0
	if raw[i] == '-' {
		negative = true
		i++
		if i >= len(raw) {
			return false, "", 0, errInvalidJSONRPCNumericID
		}
	}

	intStart := i
	switch {
	case raw[i] == '0':
		i++
		if i < len(raw) && isASCIIDigit(raw[i]) {
			return false, "", 0, errInvalidJSONRPCNumericID
		}
	case raw[i] >= '1' && raw[i] <= '9':
		for i < len(raw) && isASCIIDigit(raw[i]) {
			i++
		}
	default:
		return false, "", 0, errInvalidJSONRPCNumericID
	}
	intDigits := raw[intStart:i]

	fracDigits := ""
	if i < len(raw) && raw[i] == '.' {
		i++
		fracStart := i
		for i < len(raw) && isASCIIDigit(raw[i]) {
			i++
		}
		if fracStart == i {
			return false, "", 0, errInvalidJSONRPCNumericID
		}
		fracDigits = raw[fracStart:i]
	}

	exponent := 0
	if i < len(raw) && (raw[i] == 'e' || raw[i] == 'E') {
		i++
		if i >= len(raw) {
			return false, "", 0, errInvalidJSONRPCNumericID
		}

		exponentSign := 1
		if raw[i] == '+' || raw[i] == '-' {
			if raw[i] == '-' {
				exponentSign = -1
			}
			i++
			if i >= len(raw) {
				return false, "", 0, errInvalidJSONRPCNumericID
			}
		}

		exponentStart := i
		for i < len(raw) && isASCIIDigit(raw[i]) {
			i++
		}
		if exponentStart == i {
			return false, "", 0, errInvalidJSONRPCNumericID
		}

		exponentMagnitude, parseErr := parseBoundedInt(raw[exponentStart:i], maxCanonicalJSONRPCIDAbsExp10)
		if parseErr != nil {
			return false, "", 0, parseErr
		}
		exponent = exponentSign * exponentMagnitude
	}

	if i != len(raw) {
		return false, "", 0, errInvalidJSONRPCNumericID
	}

	digits = strings.TrimLeft(intDigits+fracDigits, "0")
	if digits == "" {
		return false, "", 0, nil
	}
	if len(digits) > maxCanonicalJSONRPCIDKeyLen {
		return false, "", 0, errJSONRPCNumericIDTooLarge
	}

	exp10 = exponent - len(fracDigits)
	return negative, digits, exp10, nil
}

func parseBoundedInt(raw string, max int) (int, error) {
	if raw == "" {
		return 0, errInvalidJSONRPCNumericID
	}

	value := 0
	for i := 0; i < len(raw); i++ {
		if !isASCIIDigit(raw[i]) {
			return 0, errInvalidJSONRPCNumericID
		}
		digit := int(raw[i] - '0')
		if value > (max-digit)/10 {
			return 0, errJSONRPCNumericIDTooLarge
		}
		value = value*10 + digit
	}
	return value, nil
}

func isASCIIDigit(ch byte) bool {
	return ch >= '0' && ch <= '9'
}

func formatCanonicalJSONRPCNumericID(negative bool, digits string, exp10 int) (string, error) {
	for len(digits) > 0 && digits[len(digits)-1] == '0' {
		digits = digits[:len(digits)-1]
		exp10++
	}

	if digits == "" {
		return "0", nil
	}

	sign := ""
	if negative {
		sign = "-"
	}

	if exp10 >= 0 {
		if exp10 > maxCanonicalJSONRPCIDKeyLen-len(digits) {
			return "", errJSONRPCNumericIDTooLarge
		}
		result := digits + strings.Repeat("0", exp10)
		if sign != "" {
			result = sign + result
		}
		if len(result) > maxCanonicalJSONRPCIDKeyLen+len(sign) {
			return "", errJSONRPCNumericIDTooLarge
		}
		return result, nil
	}

	scale := -exp10
	if scale > maxCanonicalJSONRPCIDKeyLen {
		return "", errJSONRPCNumericIDTooLarge
	}

	if len(digits) > scale {
		intPart := digits[:len(digits)-scale]
		fracPart := digits[len(digits)-scale:]
		if len(intPart)+1+len(fracPart) > maxCanonicalJSONRPCIDKeyLen {
			return "", errJSONRPCNumericIDTooLarge
		}
		result := intPart + "." + fracPart
		if sign != "" {
			result = sign + result
		}
		return result, nil
	}

	leadingZeros := scale - len(digits)
	if leadingZeros > maxCanonicalJSONRPCIDKeyLen-len(digits)-2 {
		return "", errJSONRPCNumericIDTooLarge
	}
	result := "0." + strings.Repeat("0", leadingZeros) + digits
	if sign != "" {
		result = sign + result
	}
	return result, nil
}

func (c *Connection) receive() {
	const (
		initialBufSize = 1024 * 1024
		maxBufSize     = 10 * 1024 * 1024
	)

	scanner := bufio.NewScanner(c.r)
	buf := make([]byte, 0, initialBufSize)
	scanner.Buffer(buf, maxBufSize)

	var sequence uint64
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}

		var msg anyMessage
		if err := json.Unmarshal(line, &msg); err != nil {
			c.loggerOrDefault().Error("failed to parse incoming message", "frame_bytes", len(line))
			continue
		}

		sequence++

		// Handle $/cancel_request notifications synchronously so cancellations take effect
		// immediately and do not participate in notification ordering.
		if msg.ID == nil && msg.Method == "$/cancel_request" {
			c.handleCancelRequest(&msg)
			continue
		}

		switch {
		case msg.ID != nil && msg.Method == "":
			c.handleResponse(&msg, sequence)
		case msg.Method != "":
			if msg.ID != nil {
				idKey, err := canonicalJSONRPCIDKey(*msg.ID)
				if err != nil {
					c.loggerOrDefault().Error("failed to canonicalize inbound request id", "id_bytes", len(*msg.ID))
					idKey = string(*msg.ID)
				}
				reqCtx, cancel := context.WithCancelCause(c.ctx)

				c.mu.Lock()
				c.inflight[idKey] = cancel
				c.mu.Unlock()

				run := c.prepareMethod(reqCtx, &msg)
				m := msg
				go func(m *anyMessage, idKey string, reqCtx context.Context, cancel context.CancelCauseFunc) {
					defer func() {
						c.mu.Lock()
						delete(c.inflight, idKey)
						c.mu.Unlock()

						cancel(nil)
					}()
					c.handleInbound(reqCtx, m, run)
				}(&m, idKey, reqCtx, cancel)
				continue
			}

			if c.options != nil && c.options.ReceiveNotification != nil && msg.JSONRPC == "2.0" && msg.Result == nil && msg.Error == nil {
				c.options.ReceiveNotification(ReceiveNotification{Sequence: sequence, Method: msg.Method, Params: msg.Params})
			}
			run := c.prepareNotificationDispatch(&msg)
			if c.options != nil {
				if err := c.enqueueNotification(&msg, sequence, int64(len(line)), run); err != nil {
					c.shutdownReceive(err, &queuedNotification{msg: &msg, run: run})
					return
				}
				continue
			}

			// Queue the notification for sequential processing. The sequence number marks
			// the response-scoped barrier boundary for requests that observe later responses.
			m := msg
			c.notifyMu.Lock()
			select {
			case c.notificationQueue <- queuedNotification{seq: sequence, msg: &m, run: run}:
				c.lastEnqueuedNotificationSeq = sequence
				c.notifyMu.Unlock()
			default:
				c.notifyMu.Unlock()
				c.loggerOrDefault().Error("failed to queue notification; closing connection", "err", errNotificationQueueOverflow, "capacity", cap(c.notificationQueue), "queued", len(c.notificationQueue))
				c.shutdownReceive(errNotificationQueueOverflow, &queuedNotification{msg: &msg, run: run})
				return
			}
		default:
			c.loggerOrDefault().Error("received message with neither id nor method", "frame_bytes", len(line))
		}
	}

	cause := errors.New("peer connection closed")
	if err := scanner.Err(); err != nil {
		cause = err
	}
	c.shutdownReceive(cause, nil)
}

// enqueueNotification 在不持有消费者序号锁时等待条数和字节预算。
func (c *Connection) enqueueNotification(msg *anyMessage, sequence uint64, bytes int64, run inboundContinuation) error {
	if bytes > c.options.NotificationQueueBytes {
		return ErrNotificationQueueLimit
	}
	for {
		if err := c.ctx.Err(); err != nil {
			return context.Cause(c.ctx)
		}
		c.notifyMu.Lock()
		if c.queuedCount < c.options.NotificationQueueCapacity && c.queuedBytes+bytes <= c.options.NotificationQueueBytes {
			c.queuedCount++
			c.queuedBytes += bytes
			c.lastEnqueuedNotificationSeq = sequence
			// 预算包含正在处理的通知；成功预留后 channel 一定仍有空位。
			c.notificationQueue <- queuedNotification{seq: sequence, msg: msg, bytes: bytes, run: run}
			c.notifyMu.Unlock()
			return nil
		}
		changed := c.queueChanged
		c.notifyMu.Unlock()
		select {
		case <-changed:
		case <-c.ctx.Done():
			return context.Cause(c.ctx)
		}
	}
}

func (c *Connection) shutdownReceive(cause error, unqueued *queuedNotification) {
	if cause == nil {
		cause = errors.New("connection closed")
	}

	// First, signal disconnect to callers waiting on responses and release any
	// captured operation whose continuation could not be queued.
	c.cancel(cause)
	if unqueued != nil && unqueued.run != nil {
		go c.handleInbound(c.ctx, unqueued.msg, unqueued.run)
	}
	c.closeTransport()

	// Then close the notification queue so already-received messages can drain.
	// IMPORTANT: Do not block this receive goroutine waiting for the drain to complete;
	// notification handlers may legitimately block until their context is canceled.
	close(c.notificationQueue)

	c.notifyMu.Lock()
	finalEnqueuedSeq := c.lastEnqueuedNotificationSeq
	if c.completedNotificationSeq > finalEnqueuedSeq {
		c.notifyMu.Unlock()
		panic("completed notification sequence exceeded final enqueued sequence during shutdown")
	}
	c.notifyMu.Unlock()

	// Cancel inboundCtx after notifications finish, but ensure we don't leak forever if a
	// handler blocks waiting for cancellation.
	go func(finalEnqueuedSeq uint64) {
		c.waitForNotificationDrain(finalEnqueuedSeq, notificationQueueDrainTimeout)
		c.inboundCancel(cause)
	}(finalEnqueuedSeq)

	c.loggerOrDefault().Info("connection closed", "category", connectionFailureCategory(cause))
}

// processNotifications processes notifications sequentially to maintain order.
// It terminates when notificationQueue is closed (e.g. on disconnect in receive()).
func (c *Connection) processNotifications() {
	for queued := range c.notificationQueue {
		c.handleInbound(c.inboundCtx, queued.msg, queued.run)

		c.notifyMu.Lock()
		// Requests and responses also consume sequence numbers on the reader.
		if queued.seq <= c.completedNotificationSeq {
			c.notifyMu.Unlock()
			panic("notification sequence completed out of order")
		}
		c.completedNotificationSeq = queued.seq
		if c.options != nil {
			c.queuedCount--
			c.queuedBytes -= queued.bytes
			close(c.queueChanged)
			c.queueChanged = make(chan struct{})
		}
		if c.completedNotificationSeq > c.lastEnqueuedNotificationSeq {
			c.notifyMu.Unlock()
			panic("completed notification sequence exceeded enqueued notification sequence")
		}
		c.notifyCond.Broadcast()
		c.notifyMu.Unlock()
	}
}

func (c *Connection) handleResponse(msg *anyMessage, sequence uint64) {
	idStr, err := canonicalJSONRPCIDKey(*msg.ID)
	if err != nil {
		c.loggerOrDefault().Error("failed to canonicalize response id", "id_bytes", len(*msg.ID))
		idStr = string(*msg.ID)
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	pr := c.pending[idStr]
	if pr == nil {
		return
	}
	c.notifyMu.Lock()
	watermark := c.lastEnqueuedNotificationSeq
	if c.completedNotificationSeq > watermark {
		c.notifyMu.Unlock()
		panic("completed notification sequence exceeded response watermark")
	}
	c.notifyMu.Unlock()
	// Publishing the immutable response and retiring pending admission are one
	// operation, so cancellation cannot fall between them and lose the terminal.
	pr.ch <- responseEnvelope{msg: *msg, sequence: sequence, notificationWatermark: watermark}
	delete(c.pending, idStr)
}

func (c *Connection) handleCancelRequest(msg *anyMessage) {
	var p cancelRequestParams
	if err := json.Unmarshal(msg.Params, &p); err != nil {
		c.loggerOrDefault().Error("failed to parse $/cancel_request params", "params_bytes", len(msg.Params))
		return
	}
	if len(bytes.TrimSpace(p.RequestID)) == 0 {
		c.loggerOrDefault().Error("received $/cancel_request without requestId")
		return
	}

	idKey, err := canonicalJSONRPCIDKey(p.RequestID)
	if err != nil {
		c.loggerOrDefault().Error("failed to canonicalize $/cancel_request requestId", "id_bytes", len(p.RequestID))
		idKey = string(p.RequestID)
	}

	c.mu.Lock()
	cancel := c.inflight[idKey]
	c.mu.Unlock()
	if cancel == nil {
		return
	}

	cancel(context.Canceled)
}

func (c *Connection) prepareMethod(ctx context.Context, msg *anyMessage) inboundContinuation {
	if c.prepareInbound == nil {
		return nil
	}
	return c.prepareInbound(ctx, msg)
}

func (c *Connection) prepareNotificationDispatch(msg *anyMessage) inboundContinuation {
	run := c.prepareMethod(c.inboundCtx, msg)
	if run == nil {
		return nil
	}
	// A captured cancel may own a release barrier. Run it exactly once even
	// when the queue fails or an earlier handler prevents it from draining.
	var once sync.Once
	invoke := func(ctx context.Context) (result any, err *RequestError) {
		once.Do(func() { result, err = run(ctx) })
		return result, err
	}
	stop := context.AfterFunc(c.inboundCtx, func() {
		c.handleInbound(c.inboundCtx, msg, invoke)
	})
	return func(ctx context.Context) (any, *RequestError) {
		stop()
		return invoke(ctx)
	}
}

func (c *Connection) handleInbound(ctx context.Context, req *anyMessage, run inboundContinuation) {
	res := anyMessage{JSONRPC: "2.0"}

	// copy ID if present
	if req.ID != nil {
		res.ID = req.ID
	}
	var result any
	var err *RequestError
	switch {
	case run != nil:
		result, err = run(ctx)
	case c.handler != nil:
		result, err = c.handler(ctx, req.Method, req.Params)
	case req.ID != nil:
		res.Error = NewMethodNotFound(req.Method)
		_ = c.sendMessage(res)
		return
	default:
		return
	}
	if req.ID == nil {
		// Notification: no response is sent; log handler errors to surface decode failures.
		if err != nil {
			// Per ACP, unknown extension notifications should be ignored.
			if err.Code == -32601 && strings.HasPrefix(req.Method, "_") {
				return
			}
			c.loggerOrDefault().Error("failed to handle notification", "error_code", err.Code)
		}
		return
	}
	if err != nil {
		res.Error = err
	} else {
		// marshal result
		b, mErr := json.Marshal(result)
		if mErr != nil {
			res.Error = NewInternalError(map[string]any{"error": mErr.Error()})
		} else {
			res.Result = b
		}
	}
	_ = c.sendMessage(res)
}

// RequestWriteHooks binds an operation's admission decision to the actual wire write.
// Callbacks run while the connection owns its write gate. They must not block or
// call back into the connection. Complete is called only after Commit succeeds.
type RequestWriteHooks struct {
	// Context optionally bounds write admission independently of response waiting.
	Context context.Context
	// Commit may reject an operation immediately before its frame is written.
	Commit func() error
	// Complete records the actual transport result before another frame can write.
	Complete func(RequestWriteResult)
}

// RequestWriteResult describes local delivery, not peer acceptance.
type RequestWriteResult struct {
	// BytesWritten is the prefix accepted by the transport.
	BytesWritten int
	// FrameBytes includes the complete JSON-RPC frame and its delimiter.
	FrameBytes int
	// Err reports a failed or incomplete write.
	Err error
}

func (c *Connection) sendMessage(msg anyMessage) error {
	return c.writeMessage(c.ctx, msg, RequestWriteHooks{}).Err
}

func (c *Connection) writeMessage(ctx context.Context, msg anyMessage, hooks RequestWriteHooks) RequestWriteResult {
	if hooks.Context != nil {
		ctx = hooks.Context
	}
	msg.JSONRPC = "2.0"
	b, err := json.Marshal(msg)
	if err != nil {
		return RequestWriteResult{Err: err}
	}
	b = append(b, '\n')
	select {
	case c.writeGate <- struct{}{}:
	case <-ctx.Done():
		return RequestWriteResult{Err: context.Cause(ctx)}
	case <-c.ctx.Done():
		return RequestWriteResult{Err: context.Cause(c.ctx)}
	}
	defer func() { <-c.writeGate }()
	if err := ctx.Err(); err != nil {
		return RequestWriteResult{Err: context.Cause(ctx)}
	}
	if err := c.ctx.Err(); err != nil {
		return RequestWriteResult{Err: context.Cause(c.ctx)}
	}
	if hooks.Commit != nil {
		if err := hooks.Commit(); err != nil {
			return RequestWriteResult{Err: err}
		}
	}
	n, err := c.w.Write(b)
	if err == nil && n != len(b) {
		err = io.ErrShortWrite
	}
	result := RequestWriteResult{BytesWritten: n, FrameBytes: len(b), Err: err}
	if hooks.Complete != nil {
		hooks.Complete(result)
	}
	if err != nil {
		// A failed write leaves framing uncertain, so the stream cannot be reused.
		c.closeWithCause(err)
	}
	return result
}

// SendRequest sends a JSON-RPC request and returns a typed result.
// For methods that do not return a result, use SendRequestNoResult instead.
func SendRequest[T any](c *Connection, ctx context.Context, method string, params any) (T, error) {
	return SendRequestWithWriteHooks[T](c, ctx, method, params, RequestWriteHooks{})
}

// SendRequestWithWriteHooks uses the same request owner and notification barrier
// as SendRequest, with synchronous wire admission and delivery receipts.
func SendRequestWithWriteHooks[T any](c *Connection, ctx context.Context, method string, params any, hooks RequestWriteHooks) (T, error) {
	var result T
	exchange := c.exchangeRequest(ctx, method, params, hooks)
	if exchange.prepareErr != nil {
		return result, exchange.prepareErr
	}
	var writeErr error
	if exchange.write.Err != nil {
		writeErr = NewInternalError(map[string]any{"error": exchange.write.Err.Error()})
	}
	resp := exchange.response
	if resp.sequence == 0 {
		if writeErr != nil {
			return result, writeErr
		}
		if exchange.waitErr != nil {
			if _, disconnected := exchange.waitErr.(*requestDisconnectError); disconnected {
				return result, NewInternalError(map[string]any{"error": exchange.waitErr.Error()})
			}
			return result, toReqErr(exchange.waitErr)
		}
		return result, nil
	}
	var responseErr error
	if resp.msg.Error != nil {
		responseErr = resp.msg.Error
	} else if len(resp.msg.Result) > 0 {
		if err := json.Unmarshal(resp.msg.Result, &result); err != nil {
			responseErr = NewInternalError(map[string]any{"error": err.Error()})
		}
	}
	if responseErr == nil {
		responseErr = writeErr
	} else if writeErr != nil {
		responseErr = errors.Join(responseErr, writeErr)
	}
	if err := c.waitNotificationsUpTo(ctx, resp.notificationWatermark); err != nil {
		return result, errors.Join(responseErr, err)
	}
	return result, responseErr
}

type requestExchange struct {
	response   responseEnvelope
	write      RequestWriteResult
	prepareErr error
	waitErr    error
}

func (c *Connection) exchangeRequest(ctx context.Context, method string, params any, hooks RequestWriteHooks) requestExchange {
	msg, idKey, err := c.prepareRequest(method, params)
	if err != nil {
		return requestExchange{prepareErr: err}
	}

	pr := &pendingResponse{ch: make(chan responseEnvelope, 1)}
	c.mu.Lock()
	c.pending[idKey] = pr
	c.mu.Unlock()

	exchange := requestExchange{write: c.writeMessage(ctx, msg, hooks)}
	if exchange.write.Err != nil {
		if exchange.write.FrameBytes == 0 || exchange.write.BytesWritten != exchange.write.FrameBytes {
			c.cleanupPending(idKey)
			return exchange
		}
		// The transport failed, but a complete write may already have a
		// response on the same pending request. Never wait for one here.
		exchange.response, _ = c.takeBufferedResponse(pr, idKey)
		return exchange
	}

	exchange.response, exchange.waitErr = c.waitForResponse(ctx, pr, idKey)
	return exchange
}

func (c *Connection) prepareRequest(method string, params any) (anyMessage, string, error) {
	id := c.nextID.Add(1)
	idRaw, _ := json.Marshal(id)

	msg := anyMessage{
		JSONRPC: "2.0",
		ID:      (*json.RawMessage)(&idRaw),
		Method:  method,
	}

	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			return msg, "", NewInvalidParams(map[string]any{"error": err.Error()})
		}
		msg.Params = b
	}

	return msg, string(idRaw), nil
}

func (c *Connection) sendCancelRequests() {
	for {
		select {
		case <-c.Done():
			return
		case <-c.cancelRequestSignal:
			for {
				c.mu.Lock()
				if len(c.pendingCancelRequest) == 0 {
					c.mu.Unlock()
					break
				}
				idKey := c.pendingCancelRequest[0]
				c.pendingCancelRequest = c.pendingCancelRequest[1:]
				c.mu.Unlock()

				requestID := json.RawMessage(append([]byte(nil), idKey...))
				if err := c.SendNotification(context.Background(), "$/cancel_request", cancelRequestParams{RequestID: requestID}); err != nil {
					c.loggerOrDefault().Debug("failed to send $/cancel_request", "category", connectionFailureCategory(err))
				}
			}
		}
	}
}

func (c *Connection) sendCancelRequest(idKey string) {
	if strings.TrimSpace(idKey) == "" {
		return
	}

	select {
	case <-c.Done():
		return
	default:
	}

	queueFull := false
	c.mu.Lock()
	if len(c.pendingCancelRequest) >= maxPendingCancelRequests {
		queueFull = true
	} else {
		c.pendingCancelRequest = append(c.pendingCancelRequest, idKey)
	}
	c.mu.Unlock()

	if queueFull {
		c.loggerOrDefault().Debug("dropping $/cancel_request due to full queue", "queue_len", maxPendingCancelRequests)
		return
	}

	select {
	case c.cancelRequestSignal <- struct{}{}:
	default:
	}
}

// A disconnect has precedence over derived caller cancellation. Keep its
// underlying cause available to receipt callers without changing legacy errors.
type requestDisconnectError struct {
	cause error
}

func (*requestDisconnectError) Error() string { return "peer disconnected before response" }

func (e *requestDisconnectError) Unwrap() error { return e.cause }

func (c *Connection) waitForResponse(ctx context.Context, pr *pendingResponse, idKey string) (responseEnvelope, error) {
	select {
	case resp := <-pr.ch:
		return resp, nil
	case <-ctx.Done():
	case <-c.Done():
	}
	if resp, buffered := c.takeBufferedResponse(pr, idKey); buffered {
		return resp, nil
	}
	if cause := context.Cause(c.ctx); cause != nil {
		return responseEnvelope{}, &requestDisconnectError{cause: cause}
	}
	c.sendCancelRequest(idKey)
	if cause := context.Cause(ctx); cause != nil {
		return responseEnvelope{}, cause
	}
	return responseEnvelope{}, errors.New("request context ended without cause")
}

// takeBufferedResponse observes a published response or retires the pending
// request without waiting. The reader uses the same lock to publish responses.
func (c *Connection) takeBufferedResponse(pr *pendingResponse, idKey string) (responseEnvelope, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	select {
	case resp := <-pr.ch:
		return resp, true
	default:
		delete(c.pending, idKey)
		return responseEnvelope{}, false
	}
}

func (c *Connection) waitNotificationsUpTo(ctx context.Context, target uint64) error {
	if target == 0 {
		return nil
	}

	peerDisconnectedErr := NewInternalError(map[string]any{"error": "peer disconnected while waiting for pre-response notifications"})
	stopWake := make(chan struct{})
	defer close(stopWake)

	c.notifyMu.Lock()
	defer c.notifyMu.Unlock()
	if target > c.lastEnqueuedNotificationSeq {
		return errors.New("notification watermark was not received on this connection")
	}

	go func() {
		select {
		case <-ctx.Done():
		case <-c.inboundCtx.Done():
		case <-stopWake:
			return
		}
		c.notifyMu.Lock()
		c.notifyCond.Broadcast()
		c.notifyMu.Unlock()
	}()

	for c.completedNotificationSeq < target {
		if c.completedNotificationSeq > c.lastEnqueuedNotificationSeq {
			panic("completed notification sequence exceeded enqueued notification sequence while waiting")
		}

		select {
		case <-c.inboundCtx.Done():
			return peerDisconnectedErr
		default:
		}
		select {
		case <-ctx.Done():
			select {
			case <-c.Done():
				return peerDisconnectedErr
			default:
			}
			cause := context.Cause(ctx)
			if cause == nil {
				cause = ctx.Err()
			}
			if cause != nil {
				return toReqErr(cause)
			}
			return NewInternalError(map[string]any{"error": "request context ended without cause while waiting for notifications"})
		default:
		}

		c.notifyCond.Wait()
	}
	return nil
}

func (c *Connection) waitForNotificationDrain(target uint64, timeout time.Duration) {
	if target == 0 {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	stopWake := make(chan struct{})
	defer close(stopWake)

	c.notifyMu.Lock()
	defer c.notifyMu.Unlock()
	if target > c.lastEnqueuedNotificationSeq {
		panic("drain target exceeded last enqueued notification sequence")
	}

	go func() {
		select {
		case <-ctx.Done():
		case <-stopWake:
			return
		}
		c.notifyMu.Lock()
		c.notifyCond.Broadcast()
		c.notifyMu.Unlock()
	}()

	for c.completedNotificationSeq < target {
		if c.completedNotificationSeq > c.lastEnqueuedNotificationSeq {
			panic("completed notification sequence exceeded enqueued notification sequence during drain")
		}
		if ctx.Err() != nil {
			return
		}
		c.notifyCond.Wait()
	}
}

func (c *Connection) cleanupPending(idKey string) {
	c.mu.Lock()
	delete(c.pending, idKey)
	c.mu.Unlock()
}

// SendRequestNoResult sends a JSON-RPC request that returns no result payload.
func (c *Connection) SendRequestNoResult(ctx context.Context, method string, params any) error {
	_, err := SendRequest[json.RawMessage](c, ctx, method, params)
	return err
}

func (c *Connection) SendNotification(ctx context.Context, method string, params any) error {
	select {
	case <-ctx.Done():
		return NewInternalError(map[string]any{"error": ctx.Err().Error()})
	default:
	}

	msg, err := c.prepareNotification(method, params)
	if err != nil {
		return err
	}

	if err := c.writeMessage(ctx, msg, RequestWriteHooks{}).Err; err != nil {
		return NewInternalError(map[string]any{"error": err.Error()})
	}
	return nil
}

func (c *Connection) prepareNotification(method string, params any) (anyMessage, error) {
	msg := anyMessage{
		JSONRPC: "2.0",
		Method:  method,
	}

	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			return msg, NewInvalidParams(map[string]any{"error": err.Error()})
		}
		msg.Params = b
	}

	return msg, nil
}

// Done returns a channel that is closed when the underlying reader loop exits
// (typically when the peer disconnects or the input stream is closed).
func (c *Connection) Done() <-chan struct{} {
	return c.ctx.Done()
}

// connectionFailureCategory preserves safe transport diagnostics without native payloads.
func connectionFailureCategory(err error) string {
	switch {
	case errors.Is(err, context.Canceled):
		return "cancelled"
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline_exceeded"
	case errors.Is(err, io.EOF):
		return "eof"
	case errors.Is(err, io.ErrShortWrite):
		return "short_write"
	case errors.Is(err, ErrNotificationQueueLimit):
		return "notification_budget"
	default:
		return "transport_error"
	}
}

// NeuxBot 本地修改：在上游 v0.13.5 基础上增加显式背压构造及连接关闭、失败查询。
// 原版权与 Apache-2.0 许可保留于 LICENSE，具体改动见 PATCHES.md。
package acp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
)

// ClientSideConnection provides the client's view of the connection and implements Agent calls.
type ClientSideConnection struct {
	conn   *Connection
	client Client
}

// NewClientSideConnection creates a new client-side connection bound to the
// provided Client implementation.
func NewClientSideConnection(client Client, peerInput io.Writer, peerOutput io.Reader) *ClientSideConnection {
	csc := &ClientSideConnection{}
	csc.client = client
	csc.conn = NewConnection(csc.handleWithExtensions, peerInput, peerOutput)
	return csc
}

// NewClientSideConnectionWithOptions 为宿主拥有的连接启用可取消的通知背压。
func NewClientSideConnectionWithOptions(client Client, peerInput io.Writer, peerOutput io.Reader, options ConnectionOptions) (*ClientSideConnection, error) {
	csc := &ClientSideConnection{client: client}
	conn, err := NewConnectionWithOptions(csc.handleWithExtensions, peerInput, peerOutput, options)
	if err != nil {
		return nil, err
	}
	csc.conn = conn
	return csc, nil
}

// Close 结束本连接并中断其显式拥有的底层读写。
func (c *ClientSideConnection) Close() { c.conn.Close() }

// Err 返回连接首个失败，供宿主区分资源受限与进程不可用。
func (c *ClientSideConnection) Err() error { return c.conn.Err() }

// Done exposes a channel that closes when the peer disconnects.
func (c *ClientSideConnection) Done() <-chan struct{} { return c.conn.Done() }

// SetLogger directs connection diagnostics to the provided logger.
func (c *ClientSideConnection) SetLogger(l *slog.Logger) { c.conn.SetLogger(l) }

// RequestReceipt reports the independent facts from one request.
type RequestReceipt[T any] struct {
	// Result is the decoded result when ResultValid is true.
	Result T
	// ResultValid reports successful decoding of a non-null result.
	// The caller still validates method-specific semantics.
	ResultValid bool
	// PeerErr contains private native evidence for adapter classification.
	// Its message and data may contain secrets. Sanitize them before projecting
	// errors to the host, CLI or logs.
	PeerErr *RequestError
	// TransportErr reports failure while waiting for a response. It retains
	// the underlying connection or request-context cause.
	TransportErr error
	// Write reports the local write admission, bytes and error.
	Write RequestWriteResult
	// DecodeErr reports a safe classification for an invalid response envelope
	// or result without including the peer's payload.
	DecodeErr error
	// PrepareErr reports failure to encode the request parameters.
	PrepareErr error
	// Sequence is the matched response's receive order, or zero if none arrived.
	Sequence uint64
	// NotificationWatermark is the receive sequence of the latest notification
	// admitted to dispatch before the response, or zero when there was none.
	NotificationWatermark uint64
}

// SendClientRequestWithReceipt sends a typed request using the connection's
// existing pending owner. It returns response evidence before notification
// dispatch completes. The reader still applies the configured queue limits.
// WaitForNotifications can check dispatch separately.
// Without a receive hook the caller decodes T after the reader publishes the
// response. With a non-nil hook both decoding T and the hook run on the reader
// before the next frame, potentially before Write returns. Both must be bounded
// local work, without I/O, waits, observers or calls into this connection.
// Only response fields are available to the hook. Do not mutate its borrowed
// Result or PeerErr.
func SendClientRequestWithReceipt[T any](c *ClientSideConnection, ctx context.Context, method string, params any, hooks RequestWriteHooks, receive ...func(RequestReceipt[T])) RequestReceipt[T] {
	if len(receive) > 1 {
		return RequestReceipt[T]{PrepareErr: errors.New("only one response receive hook is allowed")}
	}
	var receipt RequestReceipt[T]
	var capture func(responseEnvelope)
	if len(receive) == 1 && receive[0] != nil {
		hook := receive[0]
		capture = func(response responseEnvelope) {
			receipt = decodeRequestReceipt[T](response)
			hook(receipt)
		}
	}
	exchange := c.conn.exchangeRequest(ctx, method, params, hooks, capture)
	if capture == nil {
		receipt = decodeRequestReceipt[T](exchange.response)
	}
	receipt.PrepareErr = exchange.prepareErr
	receipt.TransportErr = exchange.waitErr
	receipt.Write = exchange.write
	return receipt
}

func decodeRequestReceipt[T any](response responseEnvelope) RequestReceipt[T] {
	receipt := RequestReceipt[T]{
		Sequence:              response.sequence,
		NotificationWatermark: response.notificationWatermark,
	}
	if receipt.Sequence == 0 {
		return receipt
	}

	switch {
	case response.decodeErr != nil:
		receipt.DecodeErr = response.decodeErr
	case response.peerErr != nil:
		receipt.PeerErr = response.peerErr
	case bytes.Equal(response.result, []byte("null")):
		receipt.DecodeErr = errors.New("JSON-RPC response has no non-null result")
	default:
		var result T
		if err := decodeResponseResult(response.result, &result); err != nil {
			receipt.DecodeErr = err
		} else {
			receipt.Result = result
			receipt.ResultValid = true
		}
	}
	return receipt
}

// SendClientNotificationWithWriteHooks uses the same write gate as requests, so
// a captured control can recheck its native owner immediately before transmission.
func SendClientNotificationWithWriteHooks(c *ClientSideConnection, ctx context.Context, method string, params any, hooks RequestWriteHooks) RequestWriteResult {
	if method == "" {
		return RequestWriteResult{Err: errors.New("notification method is required")}
	}
	msg := anyMessage{Method: method}
	if params != nil {
		encoded, err := json.Marshal(params)
		if err != nil {
			return RequestWriteResult{Err: errors.New("invalid notification parameters")}
		}
		msg.Params = encoded
	}
	return c.conn.writeMessage(ctx, msg, hooks)
}

// WaitForNotifications waits for dispatch through a receipt watermark from
// this connection. An interrupted drain does not invalidate the response.
// Notification handler errors are logged by the SDK during dispatch.
func (c *ClientSideConnection) WaitForNotifications(ctx context.Context, watermark uint64) error {
	return c.conn.waitNotificationsUpTo(ctx, watermark)
}

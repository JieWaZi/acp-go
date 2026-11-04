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
	// PeerErr is the peer's JSON-RPC error, if it returned one.
	PeerErr *RequestError
	// TransportErr reports failure while waiting for a response. It retains
	// the underlying connection or request-context cause.
	TransportErr error
	// Write reports the local write admission, bytes and error.
	Write RequestWriteResult
	// DecodeErr reports an invalid response envelope or result.
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
func SendClientRequestWithReceipt[T any](c *ClientSideConnection, ctx context.Context, method string, params any, hooks RequestWriteHooks) RequestReceipt[T] {
	exchange := c.conn.exchangeRequest(ctx, method, params, hooks)
	response := exchange.response
	receipt := RequestReceipt[T]{
		PrepareErr:            exchange.prepareErr,
		TransportErr:          exchange.waitErr,
		Write:                 exchange.write,
		Sequence:              response.sequence,
		NotificationWatermark: response.notificationWatermark,
	}
	if receipt.Sequence == 0 {
		return receipt
	}

	msg := response.msg
	switch {
	case msg.JSONRPC != "2.0" || (msg.Error != nil && msg.Result != nil):
		receipt.DecodeErr = errors.New("invalid JSON-RPC response envelope")
	case msg.Error != nil:
		receipt.PeerErr = msg.Error
	case msg.Result == nil || bytes.Equal(msg.Result, []byte("null")):
		receipt.DecodeErr = errors.New("JSON-RPC response has no non-null result")
	default:
		var result T
		if err := json.Unmarshal(msg.Result, &result); err != nil {
			receipt.DecodeErr = err
		} else {
			receipt.Result = result
			receipt.ResultValid = true
		}
	}
	return receipt
}

// WaitForNotifications waits for dispatch through a receipt watermark from
// this connection. An interrupted drain does not invalidate the response.
// Notification handler errors are logged by the SDK during dispatch.
func (c *ClientSideConnection) WaitForNotifications(ctx context.Context, watermark uint64) error {
	return c.conn.waitNotificationsUpTo(ctx, watermark)
}

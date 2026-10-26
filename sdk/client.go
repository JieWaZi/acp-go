// NeuxBot 本地修改：在上游 v0.13.5 基础上增加显式背压构造及连接关闭、失败查询。
// 原版权与 Apache-2.0 许可保留于 LICENSE，具体改动见 PATCHES.md。
package acp

import (
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

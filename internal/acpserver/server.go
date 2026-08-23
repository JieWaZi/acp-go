// Package acpserver 负责把一个 SDK Agent 接到进程的 stdio 协议边界。
package acpserver

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	acp "github.com/coder/acp-go-sdk"
)

const adapterCloseTimeout = 5 * time.Second

var (
	// ErrInvalidAgent 表示协议服务缺少可交给 SDK 的 Agent 实现。
	ErrInvalidAgent = errors.New("invalid agent")
	// ErrInvalidInput 表示协议服务缺少读取 ACP 请求的输入流。
	ErrInvalidInput = errors.New("invalid protocol input")
	// ErrInvalidOutput 表示协议服务缺少写入 ACP 响应的输出流。
	ErrInvalidOutput = errors.New("invalid protocol output")
)

// connectionBinder 是协议服务消费的可选小接口。
// Agent 只有在需要主动发送 session update、权限请求等客户端调用时才实现它。
type connectionBinder interface {
	SetAgentConnection(connection *acp.AgentSideConnection)
}

// adapterCloser 是协议服务消费的可选资源清理接口。
// 只有拥有子进程、连接等资源的 Adapter 才需要实现，避免把关闭职责塞进统一大接口。
type adapterCloser interface {
	Close(ctx context.Context) error
}

// Server 保存启动 SDK AgentSideConnection 所需的显式依赖。
type Server struct {
	// agent 实现 acp-go-sdk 定义的 Agent 生命周期。
	agent acp.Agent
	// input 从客户端 stdin 读取逐行 ACP JSON-RPC 消息。
	input io.Reader
	// output 只向客户端 stdout 写入 ACP JSON-RPC 消息。
	output io.Writer
}

// New 校验依赖并创建尚未启动的协议 Server。
func New(agent acp.Agent, input io.Reader, output io.Writer) (*Server, error) {
	if agent == nil {
		return nil, fmt.Errorf("creating acp server: %w", ErrInvalidAgent)
	}
	if input == nil {
		return nil, fmt.Errorf("creating acp server: %w", ErrInvalidInput)
	}
	if output == nil {
		return nil, fmt.Errorf("creating acp server: %w", ErrInvalidOutput)
	}
	return &Server{
		agent:  agent,
		input:  input,
		output: output,
	}, nil
}

// Serve 使用 acp-go-sdk 建立 Agent 侧连接，并阻塞到客户端断开或上下文取消。
func (s *Server) Serve(ctx context.Context) error {
	// SDK 参数名从 peer 视角描述方向：Agent 的 output 是 peerInput，Agent 的 input 是 peerOutput。
	// 保持这一处直接组合可以避免项目内出现第二套 JSON-RPC、dispatch 或 cancel 实现。
	// v0.13.5 的构造函数会先启动读取 goroutine，随后调用 SetLogger 会产生无同步读写竞态；
	// 因此保留 SDK 默认 slog logger（该进程未改写全局默认值，输出仍为 stderr），绝不污染 stdout。
	connection := acp.NewAgentSideConnection(s.agent, s.output, s.input)

	// 该接口复用 acp-go-sdk 官方示例的注入时序；不需要主动调用客户端的 Agent 不承担此依赖。
	if binder, ok := s.agent.(connectionBinder); ok {
		binder.SetAgentConnection(connection)
	}

	select {
	case <-connection.Done():
		return s.closeAdapter(ctx)
	case <-ctx.Done():
		return s.closeAdapter(ctx)
	}
}

// closeAdapter 在独立且有界的上下文中释放 Adapter 资源。
// 即使进程上下文已经取消，清理仍需要一个短暂窗口来终止子进程并回收连接。
func (s *Server) closeAdapter(ctx context.Context) error {
	closer, ok := s.agent.(adapterCloser)
	if !ok {
		return nil
	}

	closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), adapterCloseTimeout)
	defer cancel()
	if err := closer.Close(closeCtx); err != nil {
		return fmt.Errorf("closing adapter: %w", err)
	}
	return nil
}

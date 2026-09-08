// Package nativeacp 使用现有 ACP SDK 连接原生 CLI 或开源 stdio Adapter。
package nativeacp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"strings"
	"sync"
	"time"

	acp "github.com/coder/acp-go-sdk"
)

// Config 描述外部 ACP 进程；不会下载程序或修改用户配置。
type Config struct {
	// Command 是已安装可执行文件的路径或名称。
	Command string
	// Args 是包含 ACP 子命令的完整参数列表。
	Args []string
	// Environment 是完整子进程环境；nil 表示继承当前环境。
	Environment []string
	// WorkingDirectory 是进程启动目录；Session cwd 仍由调用者指定。
	WorkingDirectory string
	// Logger 接收独立于协议 stdout 的诊断。
	Logger *slog.Logger
	// CursorExtensions 表示需要把 Cursor 交互请求转换为标准 ACP 交互。
	CursorExtensions bool
}

// Agent 实现标准 ACP Agent，协议传输、请求关联和通知排序全部由 Go SDK 拥有。
type Agent struct {
	// conn 是连接外部 Agent 的 SDK JSON-RPC 连接。
	conn *acp.Connection
	// command 拥有唯一外部进程。
	command *exec.Cmd
	// input 用于结束进程的标准输入。
	input io.WriteCloser
	// output 用于解除 SDK 的读取阻塞。
	output io.ReadCloser
	// cancel 终止进程生命周期。
	cancel context.CancelFunc
	// done 在进程被回收后关闭。
	done chan struct{}
	// closed 在主动关闭开始时关闭，解除回调等待。
	closed chan struct{}
	// closeOnce 保证关闭幂等。
	closeOnce sync.Once
	// mutex 保护宿主连接、初始化能力和会话配置。
	mutex sync.Mutex
	// host 是 acpserver 注入的宿主连接。
	host *acp.AgentSideConnection
	// bound 在宿主连接注入后关闭。
	bound chan struct{}
	// capabilities 保存上游的实际能力。
	capabilities acp.AgentCapabilities
	// sessions 保存配置标识与原生模型协议的会话映射。
	sessions map[acp.SessionId]*sessionOptions
	// active 保存活跃 Prompt，用于路由缺少 sessionId 的 Cursor 回调。
	active map[acp.SessionId]bool
	// toolSessions 保存已收到工具更新的所属会话。
	toolSessions map[acp.ToolCallId]acp.SessionId
	// config 保存协议特性开关。
	config Config
}

var _ acp.Agent = (*Agent)(nil)
var _ acp.AgentLoader = (*Agent)(nil)

// NewAgent 启动已安装的 ACP 程序；调用者通过 Close 释放进程。
func NewAgent(ctx context.Context, config Config) (*Agent, error) {
	if ctx == nil || config.Logger == nil || strings.TrimSpace(config.Command) == "" {
		return nil, errors.New("native ACP requires context, command and logger")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	resolved, err := ResolveCommand(config.Command, config.Environment)
	if err != nil {
		return nil, err
	}
	lifetime, cancel := context.WithCancel(context.WithoutCancel(ctx))
	command := exec.CommandContext(lifetime, resolved, config.Args...)
	prepareProcess(command)
	command.Env = config.Environment
	command.Dir = config.WorkingDirectory
	command.WaitDelay = 2 * time.Second
	command.Stderr = &diagnosticWriter{logger: config.Logger}
	input, err := command.StdinPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	output, err := command.StdoutPipe()
	if err != nil {
		cancel()
		_ = input.Close()
		return nil, err
	}
	if err := command.Start(); err != nil {
		cancel()
		_ = input.Close()
		_ = output.Close()
		return nil, fmt.Errorf("starting native ACP: %w", err)
	}
	agent := &Agent{command: command, input: input, output: output, cancel: cancel, done: make(chan struct{}), closed: make(chan struct{}), bound: make(chan struct{}), sessions: make(map[acp.SessionId]*sessionOptions), active: make(map[acp.SessionId]bool), toolSessions: make(map[acp.ToolCallId]acp.SessionId), config: config}
	agent.conn = acp.NewConnection(agent.handle, input, output)
	go func() { <-agent.conn.Done(); cancel(); _ = command.Wait(); close(agent.done) }()
	return agent, nil
}

// diagnosticWriter 不保存 CLI 的完整诊断，避免将凭据或模型原文放进返回错误。
type diagnosticWriter struct {
	// logger 是只写诊断通道的结构化日志器。
	logger *slog.Logger
}

// Write 消费原生进程诊断并仅记录数据量。
func (writer *diagnosticWriter) Write(data []byte) (int, error) {
	writer.logger.Debug("native ACP stderr", "bytes", len(data))
	return len(data), nil
}

// SetAgentConnection 绑定一次宿主连接，并释放启动期间的回调。
func (agent *Agent) SetAgentConnection(connection *acp.AgentSideConnection) {
	agent.mutex.Lock()
	defer agent.mutex.Unlock()
	if agent.host == nil && connection != nil {
		agent.host = connection
		close(agent.bound)
	}
}

// Close 先关闭 stdin，再在有界窗口内终止并回收程序；重复关闭安全。
func (agent *Agent) Close(ctx context.Context) error {
	agent.closeOnce.Do(func() { close(agent.closed); _ = agent.input.Close() })
	timer := time.NewTimer(500 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-agent.done:
		return nil
	case <-timer.C:
	case <-ctx.Done():
	}
	agent.cancel()
	_ = agent.output.Close()
	select {
	case <-agent.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Initialize 保留真实能力，不伪造上游不支持的 Session 或授权功能。
func (agent *Agent) Initialize(ctx context.Context, request acp.InitializeRequest) (acp.InitializeResponse, error) {
	response, err := acp.SendRequest[acp.InitializeResponse](agent.conn, ctx, "initialize", request)
	if err == nil {
		agent.mutex.Lock()
		agent.capabilities = response.AgentCapabilities
		agent.mutex.Unlock()
	}
	return response, err
}

// Prompt 原样转发内容，取消时补发标准 session/cancel。
func (agent *Agent) Prompt(ctx context.Context, request acp.PromptRequest) (acp.PromptResponse, error) {
	agent.mutex.Lock()
	agent.active[request.SessionId] = true
	agent.mutex.Unlock()
	defer func() {
		agent.mutex.Lock()
		delete(agent.active, request.SessionId)
		for id, sid := range agent.toolSessions {
			if sid == request.SessionId {
				delete(agent.toolSessions, id)
			}
		}
		agent.mutex.Unlock()
	}()
	response, err := acp.SendRequest[acp.PromptResponse](agent.conn, ctx, "session/prompt", request)
	if ctx.Err() != nil {
		cancelCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
		defer cancel()
		_ = agent.Cancel(cancelCtx, acp.CancelNotification{SessionId: request.SessionId})
	}
	return response, err
}

// Cancel 请求原生 Agent 中断当前 Session 的执行。
func (agent *Agent) Cancel(ctx context.Context, request acp.CancelNotification) error {
	return agent.conn.SendNotification(ctx, "session/cancel", request)
}

// CloseSession 对支持 close 的 Agent 释放 Session，否则取消并释放本地映射；上游资源随进程退出回收。
func (agent *Agent) CloseSession(ctx context.Context, request acp.CloseSessionRequest) (acp.CloseSessionResponse, error) {
	agent.mutex.Lock()
	supported := agent.capabilities.SessionCapabilities.Close != nil
	delete(agent.sessions, request.SessionId)
	agent.mutex.Unlock()
	if supported {
		return acp.SendRequest[acp.CloseSessionResponse](agent.conn, ctx, "session/close", request)
	}
	return acp.CloseSessionResponse{}, agent.Cancel(ctx, acp.CancelNotification{SessionId: request.SessionId})
}

// HandleExtensionMethod 转发调用者显式发出的 ACP 扩展。
func (agent *Agent) HandleExtensionMethod(ctx context.Context, method string, params json.RawMessage) (any, error) {
	return acp.SendRequest[json.RawMessage](agent.conn, ctx, method, params)
}

// handle 只负责类型化转交 SDK 回调；JSON-RPC、排序、取消和背压由 SDK 处理。
func (agent *Agent) handle(ctx context.Context, method string, params json.RawMessage) (any, *acp.RequestError) {
	select {
	case <-agent.bound:
	case <-agent.closed:
		return nil, acp.NewInternalError(nil)
	case <-ctx.Done():
		return nil, acp.NewInternalError(nil)
	}
	result, err := agent.dispatch(ctx, method, params)
	if err == nil {
		return result, nil
	}
	var requestErr *acp.RequestError
	if errors.As(err, &requestErr) {
		return nil, requestErr
	}
	return nil, acp.NewInternalError(map[string]any{"message": "native ACP client callback failed"})
}

// decodeCall 复用 SDK 类型解码上游回调，并保留 SDK 的校验与错误响应。
func decodeCall[P any, R any](ctx context.Context, data json.RawMessage, call func(context.Context, P) (R, error)) (any, error) {
	var request P
	if err := json.Unmarshal(data, &request); err != nil {
		return nil, acp.NewInvalidParams(nil)
	}
	if validator, ok := any(&request).(interface {
		// Validate 复用 SDK 的请求字段与联合类型校验。
		Validate() error
	}); ok {
		if err := validator.Validate(); err != nil {
			return nil, acp.NewInvalidParams(nil)
		}
	}
	return call(ctx, request)
}

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

const maxNativeDiagnosticBytes = 4 << 10

// Config 描述外部 ACP 进程；不会下载程序或修改用户配置。
type Config struct {
	// Command 是已安装可执行文件的路径或名称。
	Command string
	// Args 是包含 ACP 子命令的完整参数列表。
	Args []string
	// VersionArgs 可选的 CLI 版本参数，不包含 ACP 或权限参数。
	VersionArgs []string
	// RuntimeName 在上游完全省略实现信息时标识 CLI。
	RuntimeName string
	// Environment 是完整子进程环境；nil 表示继承当前环境。
	Environment []string
	// WorkingDirectory 是进程启动目录；Session cwd 仍由调用者指定。
	WorkingDirectory string
	// Logger 接收独立于协议 stdout 的诊断。
	Logger *slog.Logger
	// CallbackAdapter 处理当前 CLI 的私有反向请求；nil 表示只接受标准 ACP。
	CallbackAdapter CallbackAdapter
	// SessionAdapter 处理当前 CLI 的非标准模型或思考配置；nil 表示标准 ACP。
	SessionAdapter SessionAdapter
	// PermissionAdapter 处理当前 CLI 的非标准审批策略；nil 表示完全交给宿主。
	PermissionAdapter PermissionAdapter
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
	// waitErr 保存外部进程的最终退出状态。
	waitErr error
	// waitMu 保护 waitErr。
	waitMu sync.Mutex
	// stderr 保存 CLI 原始诊断尾部。
	stderr *diagnosticWriter
	// closed 在主动关闭开始时关闭，解除回调等待。
	closed chan struct{}
	// versionOnce 保证版本只在初始化时探测一次。
	versionOnce sync.Once
	// version 保存实际 CLI 版本，探测失败保持空。
	version string
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
	// active 保存活跃 Prompt，用于路由缺少 sessionId 的厂商回调。
	active map[acp.SessionId]bool
	// toolSessions 保存已收到工具更新的所属会话。
	toolSessions map[acp.ToolCallId]acp.SessionId
	// config 保存协议特性开关。
	config Config
	// prompts 保存当前执行的用户证据，执行结束后立即移除。
	prompts map[acp.SessionId][]acp.ContentBlock
	// toolDetails 保存上游增量工具参数，供执行前风险审查使用。
	toolDetails map[acp.ToolCallId]acp.ToolCallUpdate
	// toolChanged 在先行工具通知入账时唤醒并发到达的审批请求。
	toolChanged chan struct{}
	// silentLoads 屏蔽只用于恢复配置的历史回放，宿主 Resume 不重复展示旧消息。
	silentLoads map[acp.SessionId]bool
	// turnContexts 把阻塞交互限定到所属执行。
	turnContexts map[acp.SessionId]context.Context
	// turnCancels 使 session/cancel 在原生回复前撤销待审查授权。
	turnCancels map[acp.SessionId]context.CancelFunc
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
	stderr := &diagnosticWriter{logger: config.Logger}
	command.Stderr = stderr
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
	agent := &Agent{
		command:      command,
		input:        input,
		output:       output,
		cancel:       cancel,
		done:         make(chan struct{}),
		stderr:       stderr,
		closed:       make(chan struct{}),
		bound:        make(chan struct{}),
		sessions:     make(map[acp.SessionId]*sessionOptions),
		active:       make(map[acp.SessionId]bool),
		toolSessions: make(map[acp.ToolCallId]acp.SessionId),
		config:       config,
		prompts:      make(map[acp.SessionId][]acp.ContentBlock),
		toolDetails:  make(map[acp.ToolCallId]acp.ToolCallUpdate),
		turnContexts: make(map[acp.SessionId]context.Context),
		turnCancels:  make(map[acp.SessionId]context.CancelFunc),
	}
	agent.conn = acp.NewConnection(agent.handle, input, output)
	go func() {
		<-agent.conn.Done()
		cancel()
		waitErr := command.Wait()
		agent.waitMu.Lock()
		agent.waitErr = waitErr
		agent.waitMu.Unlock()
		close(agent.done)
	}()
	return agent, nil
}

// diagnosticWriter 将 CLI 原始诊断写入日志。
type diagnosticWriter struct {
	// logger 是只写诊断通道的结构化日志器。
	logger *slog.Logger
	// mutex 保护原始 stderr 尾部。
	mutex sync.Mutex
	// tail 最多保留最近 64 KiB 错误诊断。
	tail []byte
}

// Write 消费原生进程诊断，限制单次日志大小后交给宿主日志通道。
func (writer *diagnosticWriter) Write(data []byte) (int, error) {
	writer.mutex.Lock()
	const maxTailBytes = 64 << 10
	if len(data) >= maxTailBytes {
		writer.tail = append(writer.tail[:0], data[len(data)-maxTailBytes:]...)
	} else {
		writer.tail = append(writer.tail, data...)
		if len(writer.tail) > maxTailBytes {
			writer.tail = append([]byte(nil), writer.tail[len(writer.tail)-maxTailBytes:]...)
		}
	}
	writer.mutex.Unlock()
	diagnostic := string(data)
	if len(diagnostic) > maxNativeDiagnosticBytes {
		diagnostic = diagnostic[len(diagnostic)-maxNativeDiagnosticBytes:]
	}
	if diagnostic != "" {
		writer.logger.Warn("native ACP stderr", "stderr", diagnostic)
	}
	return len(data), nil
}

// String 返回已收集的 CLI 原始 stderr 尾部。
func (writer *diagnosticWriter) String() string {
	writer.mutex.Lock()
	defer writer.mutex.Unlock()
	return string(writer.tail)
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
	select {
	case <-agent.closed:
		select {
		case <-agent.done:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	default:
	}
	select {
	case <-agent.done:
		return agent.exitError()
	default:
	}
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

// exitError 仅在非主动关闭时报告真实退出错误。
func (agent *Agent) exitError() error {
	agent.waitMu.Lock()
	defer agent.waitMu.Unlock()
	if agent.waitErr == nil {
		return nil
	}
	if detail := strings.TrimSpace(agent.stderr.String()); detail != "" {
		return fmt.Errorf("native ACP process exited: %w: %s", agent.waitErr, detail)
	}
	return fmt.Errorf("native ACP process exited: %w", agent.waitErr)
}

// transportError 在连接结束时附加 CLI 退出状态和原始 stderr。
func (agent *Agent) transportError(err error) error {
	if err == nil {
		return nil
	}
	select {
	case <-agent.conn.Done():
		timer := time.NewTimer(200 * time.Millisecond)
		defer timer.Stop()
		select {
		case <-agent.done:
			return errors.Join(err, agent.exitError())
		case <-timer.C:
		}
	default:
	}
	return err
}

// sendNativeRequest 保留 SDK 协议错误，并在进程退出时附加原始 CLI 诊断。
func sendNativeRequest[T any](agent *Agent, ctx context.Context, method string, request any) (T, error) {
	response, err := acp.SendRequest[T](agent.conn, ctx, method, request)
	return response, agent.transportError(err)
}

// Initialize 保留真实能力，不伪造上游不支持的 Session 或授权功能。
func (agent *Agent) Initialize(ctx context.Context, request acp.InitializeRequest) (acp.InitializeResponse, error) {
	if agent.config.CallbackAdapter != nil {
		agent.config.CallbackAdapter.PrepareInitialize(&request)
	}
	response, err := sendNativeRequest[acp.InitializeResponse](agent, ctx, "initialize", request)
	if err == nil {
		agent.completeRuntimeVersion(ctx, &response)
		agent.mutex.Lock()
		agent.capabilities = response.AgentCapabilities
		agent.mutex.Unlock()
	}
	return response, err
}

// Prompt 原样转发内容，取消时补发标准 session/cancel。
func (agent *Agent) Prompt(ctx context.Context, request acp.PromptRequest) (acp.PromptResponse, error) {
	agent.mutex.Lock()
	if agent.active[request.SessionId] {
		agent.mutex.Unlock()
		return acp.PromptResponse{}, acp.NewInvalidParams(nil)
	}
	turnCtx, turnCancel := context.WithCancel(ctx)
	defer turnCancel()
	agent.turnContexts[request.SessionId] = turnCtx
	agent.turnCancels[request.SessionId] = turnCancel
	agent.active[request.SessionId] = true
	agent.prompts[request.SessionId] = append([]acp.ContentBlock{}, request.Prompt...)
	agent.mutex.Unlock()
	defer func() {
		agent.mutex.Lock()
		delete(agent.active, request.SessionId)
		delete(agent.prompts, request.SessionId)
		delete(agent.turnContexts, request.SessionId)
		delete(agent.turnCancels, request.SessionId)
		for id, sid := range agent.toolSessions {
			if sid == request.SessionId {
				delete(agent.toolSessions, id)
				delete(agent.toolDetails, id)
			}
		}
		agent.mutex.Unlock()
	}()
	response, err := sendNativeRequest[acp.PromptResponse](agent, ctx, "session/prompt", request)
	if ctx.Err() != nil {
		cancelCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
		defer cancel()
		_ = agent.Cancel(cancelCtx, acp.CancelNotification{SessionId: request.SessionId})
	}
	return response, err
}

// Cancel 请求原生 Agent 中断当前 Session 的执行。
func (agent *Agent) Cancel(ctx context.Context, request acp.CancelNotification) error {
	agent.mutex.Lock()
	cancel := agent.turnCancels[request.SessionId]
	agent.mutex.Unlock()
	if cancel != nil {
		cancel()
	}
	return agent.conn.SendNotification(ctx, "session/cancel", request)
}

// CloseSession 对支持 close 的 Agent 释放 Session，否则取消并释放本地映射；上游资源随进程退出回收。
func (agent *Agent) CloseSession(ctx context.Context, request acp.CloseSessionRequest) (acp.CloseSessionResponse, error) {
	agent.mutex.Lock()
	supported := agent.capabilities.SessionCapabilities.Close != nil
	delete(agent.sessions, request.SessionId)
	agent.mutex.Unlock()
	if agent.config.SessionAdapter != nil {
		agent.config.SessionAdapter.ForgetSession(request.SessionId)
	}
	if supported {
		return sendNativeRequest[acp.CloseSessionResponse](agent, ctx, "session/close", request)
	}
	return acp.CloseSessionResponse{}, agent.Cancel(ctx, acp.CancelNotification{SessionId: request.SessionId})
}

// HandleExtensionMethod 转发调用者显式发出的 ACP 扩展。
func (agent *Agent) HandleExtensionMethod(ctx context.Context, method string, params json.RawMessage) (any, error) {
	return sendNativeRequest[json.RawMessage](agent, ctx, method, params)
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

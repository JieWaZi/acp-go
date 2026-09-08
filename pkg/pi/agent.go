// Package pi 将 pi-acp 的 ACP 适配行为移植为 Go，直接管理 Pi CLI 原生 RPC。
package pi

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/JieWaZi/acp-go/pkg/acpmeta"
	"github.com/JieWaZi/acp-go/pkg/nativeacp"
	acp "github.com/coder/acp-go-sdk"
)

// Config 保存直接启动 Pi CLI 所需的配置。
type Config struct {
	// PiPath 是 Pi CLI 路径；空值从完整环境 PATH 发现 pi。
	PiPath string
	// PrefixArgs 是官方 RPC 参数之前的受控参数。
	PrefixArgs []string
	// Environment 是完整子进程环境；nil 继承宿主。
	Environment []string
	// WorkingDirectory 是探测和会话的默认工作目录。
	WorkingDirectory string
	// Logger 接收不包含凭据的诊断。
	Logger *slog.Logger
	// PermissionMode 选择 default 人工审批、auto 风险审查或 full-access。
	PermissionMode string
	// MCPModulePath 仅供显式开发覆盖；默认使用内置开源 MCP 扩展。
	MCPModulePath string
}

// bundledMCP 是通过固定 npm 依赖与生成命令构建的开源工厂。
//
//go:embed mcp.generated.ts
var bundledMCP []byte

// Agent 实现唯一的 ACP 接口，每个会话直接拥有一个 Pi 进程。
type Agent struct {
	// config 是本次 Agent 固定的启动配置。
	config Config
	// versionOnce 保证 CLI 版本仅探测一次。
	versionOnce sync.Once
	// version 保存 Pi 实际版本，不使用适配器版本替代。
	version string
	// directory 保存生命周期内的私有扩展文件。
	directory string
	// modulePath 是内置或显式覆盖的 MCP 工厂路径。
	modulePath string
	// opening 串行化创建和恢复，避免同一历史文件被重复打开。
	opening contextLock
	// mutex 保护会话集合、宿主与关闭状态。
	mutex sync.Mutex
	// sessions 保存仍可交互的独立 Pi 会话。
	sessions map[acp.SessionId]*session
	// host 是 ACP SDK 提供的宿主回调。
	host *acp.AgentSideConnection
	// formUI 表示宿主是否声明标准表单输入能力。
	formUI bool
	// closed 阻止关闭后创建新进程。
	closed bool
}

// session 将一个 Pi 进程、原生历史文件和 ACP 会话身份绑定。
type session struct {
	// id 是 Pi 原生会话标识。
	id acp.SessionId
	// cwd 是该会话固定工作目录。
	cwd string
	// file 是 Pi 拥有的原生 JSONL 历史路径。
	file string
	// directory 是仅该会话使用的扩展快照目录。
	directory string
	// process 是直接运行的 Pi CLI。
	process *rpcProcess
	// operation 串行化提示与配置变更，取消不获取此锁。
	operation contextLock
	// mutex 保护当前回合状态。
	mutex sync.Mutex
	// turn 是当前提示的终态通知，事件更新先于通知交付。
	turn chan acp.StopReason
	// turnContext 使扩展交互跟随本轮取消。
	turnContext context.Context
	// cancelTurn 取消当前回合等待的扩展交互。
	cancelTurn context.CancelFunc
	// generation 使取消前已排队的提示不会在取消后迟到执行。
	generation uint64
	// cancelled 表示宿主明确取消本轮。
	cancelled bool
	// usage 保存本次提示内各模型调用累计的真实用量。
	usage *acp.Usage
	// failure 保存模型或协议错误，不把上游失败伪装为完成。
	failure error
	// tools 记录当前回合的工具状态，避免重复创建与状态倒退。
	tools map[string]string
	// snapshots 保存文件变更前的文本，用于 ACP 差异展示。
	snapshots map[string]fileSnapshot
	// bashOutput 保存 Bash 累计输出，以计算终端增量。
	bashOutput map[string]string
	// eventsDone 在事件消费与所有扩展回调结束后关闭。
	eventsDone chan struct{}
	// callbacks 跟踪本会话发起的扩展 UI 回调。
	callbacks sync.WaitGroup
}

// NewAgent 解析 Pi 本身并准备内置扩展，不启动额外 ACP 程序。
func NewAgent(ctx context.Context, config Config) (*Agent, error) {
	if ctx == nil || config.Logger == nil {
		return nil, errors.New("Pi requires context and logger")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if config.PiPath == "" {
		config.PiPath = "pi"
	}
	executable, err := nativeacp.ResolveCommand(config.PiPath, config.Environment)
	if err != nil {
		return nil, err
	}
	config.PiPath = executable
	if config.PermissionMode == "" {
		config.PermissionMode = "default"
	}
	if config.PermissionMode != "default" && config.PermissionMode != "auto" && config.PermissionMode != "full-access" {
		return nil, errors.New("unsupported Pi permission mode")
	}
	if config.Environment == nil {
		config.Environment = os.Environ()
	}
	directory, err := os.MkdirTemp("", "acp-go-pi-")
	if err != nil {
		return nil, err
	}
	module := config.MCPModulePath
	if module == "" {
		module = filepath.Join(directory, "mcp.ts")
		err = os.WriteFile(module, bundledMCP, 0600)
	}
	if err != nil {
		_ = os.RemoveAll(directory)
		return nil, err
	}
	return &Agent{config: config, directory: directory, modulePath: module, sessions: map[acp.SessionId]*session{}}, nil
}

// SetAgentConnection 绑定 SDK 拥有的宿主连接。
func (a *Agent) SetAgentConnection(host *acp.AgentSideConnection) {
	a.mutex.Lock()
	defer a.mutex.Unlock()
	a.host = host
}

// Initialize 声明 Go 移植实现支持的能力，不再暴露 pi-acp 安装依赖。
func (a *Agent) Initialize(ctx context.Context, request acp.InitializeRequest) (acp.InitializeResponse, error) {
	a.mutex.Lock()
	a.formUI = request.ClientCapabilities.Elicitation != nil && request.ClientCapabilities.Elicitation.Form != nil
	a.mutex.Unlock()
	a.versionOnce.Do(func() {
		a.version = nativeacp.RuntimeVersion(ctx, a.config.PiPath, append(append([]string{}, a.config.PrefixArgs...), "--version"), a.config.Environment, a.config.WorkingDirectory)
	})
	return convert[acp.InitializeResponse](map[string]any{"protocolVersion": 1, "agentInfo": map[string]any{"name": "pi", "title": "Pi", "version": a.version, "_meta": acpmeta.RuntimeVersionMetadata(a.version)}, "authMethods": []any{map[string]any{"id": "pi_terminal_login", "name": "Launch Pi to configure credentials", "_meta": map[string]any{"terminal-auth": map[string]any{"command": a.config.PiPath, "args": []string{}, "label": "Launch Pi"}}}}, "agentCapabilities": map[string]any{"loadSession": true, "mcpCapabilities": map[string]bool{"http": true, "sse": true}, "promptCapabilities": map[string]bool{"image": true, "embeddedContext": true}, "sessionCapabilities": map[string]any{"list": map[string]any{}, "close": map[string]any{}, "resume": map[string]any{}, "delete": map[string]any{}}}})
}

// Authenticate 由用户在 Pi 原生终端配置提供方，本方法不自动执行登录。
func (a *Agent) Authenticate(context.Context, acp.AuthenticateRequest) (acp.AuthenticateResponse, error) {
	return acp.AuthenticateResponse{}, nil
}

// Logout 不冒充 Pi 未提供的 ACP 登出能力。
func (a *Agent) Logout(context.Context, acp.LogoutRequest) (acp.LogoutResponse, error) {
	return acp.LogoutResponse{}, acp.NewMethodNotFound("logout")
}

// Close 终止全部直接 Pi 进程后清理私有快照。
func (a *Agent) Close(ctx context.Context) error {
	a.mutex.Lock()
	a.closed = true
	all := make([]*session, 0, len(a.sessions))
	for _, s := range a.sessions {
		all = append(all, s)
	}
	a.sessions = map[acp.SessionId]*session{}
	a.mutex.Unlock()
	var failures []error
	for _, s := range all {
		failures = append(failures, s.close(ctx))
	}
	failures = append(failures, os.RemoveAll(a.directory))
	return errors.Join(failures...)
}

// close 同时取消待审批回调并回收 Pi 及 MCP 子进程。
func (s *session) close(ctx context.Context) error {
	s.mutex.Lock()
	if s.cancelTurn != nil {
		s.cancelTurn()
	}
	s.mutex.Unlock()
	if err := s.process.close(ctx); err != nil {
		return err
	}
	select {
	case <-s.eventsDone:
	case <-ctx.Done():
		return ctx.Err()
	}
	return os.RemoveAll(s.directory)
}

// get 返回仍存活的会话，未知身份使用标准错误。
func (a *Agent) get(id acp.SessionId) (*session, error) {
	a.mutex.Lock()
	defer a.mutex.Unlock()
	s := a.sessions[id]
	if s == nil {
		return nil, acp.NewInvalidParams(map[string]any{"message": "unknown Pi session; load it first"})
	}
	return s, nil
}

// CloseSession 保留历史文件，只释放活跃进程与本轮凭据。
func (a *Agent) CloseSession(ctx context.Context, request acp.CloseSessionRequest) (acp.CloseSessionResponse, error) {
	a.mutex.Lock()
	s := a.sessions[request.SessionId]
	delete(a.sessions, request.SessionId)
	a.mutex.Unlock()
	if s != nil {
		return acp.CloseSessionResponse{}, s.close(ctx)
	}
	return acp.CloseSessionResponse{}, nil
}

// Cancel 直接发送 Pi abort，审批回调同步取消，不等待提示互斥锁。
func (a *Agent) Cancel(ctx context.Context, request acp.CancelNotification) error {
	s, err := a.get(request.SessionId)
	if err != nil {
		return err
	}
	s.mutex.Lock()
	s.cancelled = true
	s.generation++
	if s.cancelTurn != nil {
		s.cancelTurn()
	}
	abortCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	call, err := s.process.begin(abortCtx, "abort", nil)
	s.mutex.Unlock()
	if err != nil {
		return err
	}
	return call.wait(abortCtx, nil)
}

// convert 只转换标准 ACP 形状，最终字段校验仍由 SDK 负责。
func convert[T any](value any) (T, error) {
	var out T
	data, err := json.Marshal(value)
	if err == nil {
		err = json.Unmarshal(data, &out)
	}
	return out, err
}

// object 读取可选的 Pi 对象字段。
func object(value any) map[string]any { out, _ := value.(map[string]any); return out }

// text 读取可选的 Pi 字符串字段。
func text(value any) string { out, _ := value.(string); return out }

// list 读取可选的 Pi 数组字段。
func list(value any) []any { out, _ := value.([]any); return out }

var _ acp.Agent = (*Agent)(nil)
var _ acp.AgentLoader = (*Agent)(nil)

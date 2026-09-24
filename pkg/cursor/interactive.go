package cursor

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/JieWaZi/acp-go/pkg/acpmeta"
	"github.com/JieWaZi/acp-go/pkg/nativeacp"
	acp "github.com/coder/acp-go-sdk"
	"github.com/gofrs/flock"
)

// Agent 统一官方配置协议与受管交互执行，对宿主只暴露 ACP。
type Agent struct {
	// Agent 保留官方模型、认证与配置协议的实现。
	*nativeacp.Agent
	// lifecycleMutex 串行化创建、恢复与关闭，避免快照覆盖仍在使用的会话。
	lifecycleMutex sync.Mutex
	// mutex 保护宿主、会话注册和关闭状态。
	mutex sync.Mutex
	// host 是执行通知、审批和表单的唯一出口。
	host *acp.AgentSideConnection
	// config 保存调用方明确选择的执行方式。
	config Config
	// options 拥有 Cursor 参数化模型与会话配置状态。
	options *cursorSessionAdapter
	// command 是已经解析的官方 CLI。
	command string
	// directory 是本次连接独占的临时配置目录。
	directory string
	// state 保存可跨连接恢复的原生聊天日志。
	state string
	// sessions 保存当前连接已建立的交互会话。
	sessions map[acp.SessionId]*interactiveSession
	// lifetime 绑定所有交互终端的生命周期。
	lifetime context.Context
	// cancel 关闭全部终端和阻塞交互。
	cancel context.CancelFunc
	// closed 阻止关闭后创建新会话。
	closed bool
}

// ProvidesUserInput 表示 Cursor 私有交互已转换为标准 ACP 表单，无需重复注入问答工具。
func (*Agent) ProvidesUserInput() bool { return true }

// interactiveSession 串行拥有一个原生会话的进程、日志与配置。
type interactiveSession struct {
	// mutex 保证 Prompt 与配置修改互斥。
	mutex sync.Mutex
	// cancelMutex 允许在 Prompt 持锁时并发取消。
	cancelMutex sync.Mutex
	// turnCancel 中断当前轮及其审批。
	turnCancel context.CancelFunc
	// id 是恢复时原样传递的 Cursor 会话身份。
	id acp.SessionId
	// cwd 是经过真实路径解析的工作目录。
	cwd string
	// servers 是该会话显式连接的 MCP。
	servers []acp.McpServer
	// directories 是宿主声明的附加工作目录。
	directories []string
	// mode 保存创建连接时冻结的三档权限。
	mode string
	// workMode 是与权限独立的 agent、plan 或 ask 工作模式。
	workMode string
	// terminal 在连续轮次间保持常驻。
	terminal *cursorTerminal
	// store 读取该会话精确路径的消息和待审查检查点。
	store *storeCursor
	// directory 保存本会话的私有 Hook 与 MCP 插件。
	directory string
	// hook 注册当前会话独占且可回收的项目 Hook。
	hook *hookLease
	// owner 持有跨进程会话锁，防止两个连接改写同一日志。
	owner *flock.Flock
	// model 是当前终端实际启动的完整参数模型。
	model string
}

// newCursorAgent 创建官方配置连接，并将交互终端归属同一个可取消生命周期。
func newCursorAgent(
	ctx context.Context,
	config Config,
	native nativeacp.Config,
	options *cursorSessionAdapter,
) (*Agent, error) {
	directory, state, environment, err := cursorEnvironment(config)
	if err != nil {
		return nil, err
	}
	native.Environment = environment
	config.Environment = environment
	child, err := nativeacp.NewAgent(ctx, native)
	if err != nil {
		if directory != "" {
			_ = os.RemoveAll(directory)
		}
		return nil, err
	}
	command, err := nativeacp.ResolveCommand(native.Command, native.Environment)
	if err != nil {
		_ = child.Close(ctx)
		_ = os.RemoveAll(directory)
		return nil, err
	}
	lifetime, cancel := context.WithCancel(context.WithoutCancel(ctx))
	return &Agent{
		Agent:     child,
		config:    config,
		options:   options,
		command:   command,
		directory: directory,
		state:     state,
		sessions:  map[acp.SessionId]*interactiveSession{},
		lifetime:  lifetime,
		cancel:    cancel,
	}, nil
}

// SetAgentConnection 将两条内部传输绑定到同一个宿主连接。
func (a *Agent) SetAgentConnection(host *acp.AgentSideConnection) {
	a.mutex.Lock()
	a.host = host
	a.mutex.Unlock()
	a.Agent.SetAgentConnection(host)
}

// Initialize 交互模式仅声明已实现的文本、恢复与会话关闭能力。
func (a *Agent) Initialize(ctx context.Context, request acp.InitializeRequest) (acp.InitializeResponse, error) {
	response, err := a.Agent.Initialize(ctx, request)
	if err == nil {
		if response.Meta == nil {
			response.Meta = map[string]any{}
		}
		version := ""
		if response.AgentInfo != nil {
			version = acpmeta.RuntimeVersion(response.AgentInfo.Meta)
		}
		mode := acpmeta.VerifiedForkModeInMinor(version, "2026.09.10", acpmeta.ForkLatest)
		response.AgentCapabilities.SessionCapabilities.Fork = acpmeta.ForkCapability(mode)
		response.Meta["fork"] = map[string]any{"mode": mode}
		response.AgentCapabilities.PromptCapabilities = acp.PromptCapabilities{}
		response.AgentCapabilities.McpCapabilities.Sse = false
		response.AgentCapabilities.McpCapabilities.Acp = false
		response.AgentCapabilities.SessionCapabilities.Resume = &acp.SessionResumeCapabilities{}
		response.AgentCapabilities.SessionCapabilities.Close = &acp.SessionCloseCapabilities{}
	}
	return response, err
}

// register 登记真实工作目录、原生身份及本连接持有的会话资源。
func (a *Agent) register(
	id acp.SessionId,
	cwd string,
	servers []acp.McpServer,
	directories []string,
	owner *flock.Flock,
) error {
	if id == "" || strings.ContainsAny(string(id), "/\\\x00") || id == "." || id == ".." {
		return errors.New("invalid Cursor session identity")
	}
	path, err := filepath.Abs(cwd)
	if err != nil {
		return err
	}
	path, err = filepath.EvalSymlinks(path)
	if err != nil {
		return err
	}
	sum := md5.Sum([]byte(path)) // 官方会话路径使用工作目录 MD5；不用于密码或完整性校验。
	directory := filepath.Join(a.directory, "sessions", string(id))
	if err = os.MkdirAll(directory, 0700); err != nil {
		return err
	}
	a.mutex.Lock()
	defer a.mutex.Unlock()
	if a.closed {
		return errors.New("Cursor agent closed")
	}
	if a.sessions[id] != nil {
		return errors.New("Cursor session already loaded")
	}
	a.sessions[id] = &interactiveSession{
		owner:       owner,
		id:          id,
		cwd:         path,
		servers:     servers,
		directories: directories,
		mode:        a.config.PermissionMode,
		directory:   directory,
		store: newStoreCursor(filepath.Join(
			a.stateForWorkspace(path),
			"chats",
			hex.EncodeToString(sum[:]),
			string(id),
			"store.db",
		)),
	}
	return nil
}

// NewSession 先由官方协议创建身份与配置，首次输入时启动交互终端。
func (a *Agent) NewSession(ctx context.Context, request acp.NewSessionRequest) (acp.NewSessionResponse, error) {
	a.lifecycleMutex.Lock()
	defer a.lifecycleMutex.Unlock()
	response, err := a.Agent.NewSession(ctx, request)
	if err == nil {
		var owner *flock.Flock
		owner, err = a.claim(response.SessionId, request.Cwd)
		if err == nil {
			err = a.register(response.SessionId, request.Cwd, request.McpServers, request.AdditionalDirectories, owner)
			if err != nil && owner != nil {
				_ = owner.Unlock()
			}
		}
	}
	return response, err
}

// LoadSession 恢复官方身份并保留新的输出边界，历史由上游标准协议回放。
func (a *Agent) LoadSession(ctx context.Context, request acp.LoadSessionRequest) (acp.LoadSessionResponse, error) {
	a.lifecycleMutex.Lock()
	defer a.lifecycleMutex.Unlock()
	owner, err := a.claim(request.SessionId, request.Cwd)
	if err != nil {
		return acp.LoadSessionResponse{}, err
	}
	defer func() {
		if owner != nil {
			_ = owner.Unlock()
		}
	}()
	if err := a.snapshotControlStore(ctx, request.SessionId, request.Cwd); err != nil {
		return acp.LoadSessionResponse{}, err
	}
	response, err := a.Agent.LoadSession(ctx, request)
	if err == nil {
		err = a.register(request.SessionId, request.Cwd, request.McpServers, request.AdditionalDirectories, owner)
		if err == nil {
			owner = nil
		}
	}

	return response, err
}

// ResumeSession 使用官方加载建立配置，但不更换原生聊天身份。
func (a *Agent) ResumeSession(ctx context.Context, request acp.ResumeSessionRequest) (acp.ResumeSessionResponse, error) {
	a.lifecycleMutex.Lock()
	defer a.lifecycleMutex.Unlock()
	owner, err := a.claim(request.SessionId, request.Cwd)
	if err != nil {
		return acp.ResumeSessionResponse{}, err
	}
	defer func() {
		if owner != nil {
			_ = owner.Unlock()
		}
	}()
	if err := a.snapshotControlStore(ctx, request.SessionId, request.Cwd); err != nil {
		return acp.ResumeSessionResponse{}, err
	}
	loadRequest := acp.LoadSessionRequest{
		SessionId:             request.SessionId,
		Cwd:                   request.Cwd,
		McpServers:            request.McpServers,
		AdditionalDirectories: request.AdditionalDirectories,
	}
	response, err := a.Agent.LoadSessionConfiguration(ctx, loadRequest)
	if err == nil {
		err = a.register(request.SessionId, request.Cwd, request.McpServers, request.AdditionalDirectories, owner)
		if err == nil {
			owner = nil
		}
	}
	return acp.ResumeSessionResponse{Meta: response.Meta, ConfigOptions: response.ConfigOptions, Modes: response.Modes}, err
}

// session 只返回当前未关闭连接中已登记的会话。
func (a *Agent) session(id acp.SessionId) (*interactiveSession, error) {
	a.mutex.Lock()
	defer a.mutex.Unlock()
	if a.closed || a.sessions[id] == nil {
		return nil, errors.New("Cursor session unavailable")
	}
	return a.sessions[id], nil
}

// start 按已确认模型和权限启动终端，配置相同时复用现有进程。
func (a *Agent) start(ctx context.Context, s *interactiveSession) error {
	selection, err := a.options.ExecutionModel(s.id)
	if err != nil {
		return err
	}
	encoded, _ := json.Marshal(selection)
	model := string(encoded)
	if s.terminal != nil && s.model == model {
		select {
		case <-s.terminal.done:
			s.stop()
		default:
			return a.ensurePermission(ctx, s)
		}
	}
	if err = s.stop(); err != nil {
		return err
	}
	plugin, err := createPlugin(s.directory, a.command, a.config.Environment, s.servers, s.id)
	if err != nil {
		return err
	}
	modelArgument, err := executionModelArgument(selection)
	if err != nil {
		return err
	}
	args := append([]string{}, a.config.PrefixArgs...)
	args = append(args, "--model", modelArgument)
	environment, err := sessionEnvironment(a.directory, a.stateForWorkspace(s.cwd), s.directory, a.config.Environment, selection)
	if err != nil {
		return err
	}
	args = append(args, "--trust", "--resume", string(s.id), "--plugin-dir", plugin, "--approve-mcps")
	switch s.mode {
	case "", "default":
	case "auto":
		args = append(args, "--auto-review")
	case "full-access":
		args = append(args, "--force")
	default:
		return errors.New("unsupported Cursor permission mode")
	}
	if s.workMode == "plan" || s.workMode == "ask" {
		args = append(args, "--mode", s.workMode)
	}
	for _, directory := range s.directories {
		args = append(args, "--add-dir", directory)
	}
	s.hook, err = installHookLease(ctx, s.cwd, filepath.Join(s.directory, "hooks.json"))
	if err != nil {
		return err
	}
	startCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	s.terminal, err = startSelectedTerminal(startCtx, a.lifetime, a.command, args, environment, s.cwd, modelArgument)
	if err != nil {
		s.stop()
		return err
	}
	s.model = model
	if err = a.ensurePermission(startCtx, s); err != nil {
		s.stop()
		return err
	}
	if err = verifySelection(s.directory, selection); err != nil {
		s.stop()
		return err
	}
	if err := s.store.seed(ctx); err != nil {
		return errors.Join(err, s.stop())
	}
	return nil
}

// stop 回收当前终端并撤回本会话的项目 Hook。
func (s *interactiveSession) stop() error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var err error
	if s.terminal != nil {
		err = s.terminal.close(ctx)
		s.terminal = nil
	}
	if s.hook != nil {
		cleanupErr := s.hook.close(ctx)
		err = errors.Join(err, cleanupErr)
		if cleanupErr == nil {
			s.hook = nil
		}
	}
	return err
}

// Prompt 在同一常驻终端中提交新轮，审批完成前不会发送允许按键。
func (a *Agent) Prompt(ctx context.Context, request acp.PromptRequest) (response acp.PromptResponse, err error) {
	s, err := a.session(request.SessionId)
	if err != nil {
		return response, err
	}
	if !s.mutex.TryLock() {
		return response, errors.New("Cursor session is busy")
	}
	defer s.mutex.Unlock()
	turn, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(a.lifetime, cancel)
	defer stop()
	defer cancel()
	s.cancelMutex.Lock()
	s.turnCancel = cancel
	s.cancelMutex.Unlock()
	defer func() { s.cancelMutex.Lock(); s.turnCancel = nil; s.cancelMutex.Unlock() }()
	defer func() {
		if err != nil || turn.Err() != nil || response.StopReason == acp.StopReasonCancelled {
			if s.terminal != nil {
				before := s.terminal.text()
				_ = s.terminal.write("\x03")
				settle, stop := context.WithTimeout(context.Background(), time.Second)
				_ = s.terminal.wait(settle, func(screen string) bool { return screen != before && terminalReady(screen) })
				stop()
			}
			err = cursorPromptError(err, s.stop())
		}
	}()
	parts := []string{}
	for _, block := range request.Prompt {
		if block.Text == nil {
			return response, errors.New("Cursor interactive prompt requires text content")
		}
		parts = append(parts, block.Text.Text)
	}
	prompt := strings.Join(parts, "\n")
	if strings.TrimSpace(prompt) == "" {
		return response, errors.New("Cursor prompt is empty")
	}
	if err = a.start(turn, s); err != nil {
		return response, err
	}
	if _, err = readHooks(turn, filepath.Join(s.directory, "events")); err != nil {
		return response, err
	}
	if s.terminal.outcome != nil {
		_ = s.terminal.outcome.failure()
	}
	if err = s.terminal.submit(turn, prompt); err != nil {
		return response, err
	}
	response, err = a.runTurn(turn, s)
	if errors.Is(err, context.Canceled) {
		response.StopReason = acp.StopReasonCancelled
		err = nil
	}
	return response, err
}

// Cancel 同时撤销待回填审批与本轮终端执行。
func (a *Agent) Cancel(ctx context.Context, request acp.CancelNotification) error {
	s, err := a.session(request.SessionId)
	if err != nil {
		return err
	}
	s.cancelMutex.Lock()
	defer s.cancelMutex.Unlock()
	if s.turnCancel != nil {
		s.turnCancel()
	}
	return nil
}

// SetSessionConfigOption 在回执确认后由下一轮重启终端并恢复同一原生聊天。
func (a *Agent) SetSessionConfigOption(
	ctx context.Context,
	request acp.SetSessionConfigOptionRequest,
) (acp.SetSessionConfigOptionResponse, error) {
	if request.ValueId == nil {
		return acp.SetSessionConfigOptionResponse{}, acp.NewInvalidParams(nil)
	}
	s, err := a.session(request.ValueId.SessionId)
	if err != nil {
		return acp.SetSessionConfigOptionResponse{}, err
	}
	if !s.mutex.TryLock() {
		return acp.SetSessionConfigOptionResponse{}, errors.New("Cursor session is busy")
	}
	defer s.mutex.Unlock()
	response, err := a.Agent.SetSessionConfigOption(ctx, request)
	if err == nil && request.ValueId.ConfigId == "mode" {
		for _, option := range response.ConfigOptions {
			if option.Select != nil && option.Select.Id == "mode" {
				mode := string(option.Select.CurrentValue)
				if s.workMode != mode {
					s.workMode = mode
					err = s.stop()
				}
			}
		}
	}
	return response, err
}

// SetSessionMode 切换官方工作模式，三档权限始终由独立启动策略控制。
func (a *Agent) SetSessionMode(ctx context.Context, request acp.SetSessionModeRequest) (acp.SetSessionModeResponse, error) {
	mode := string(request.ModeId)
	if mode != "agent" && mode != "plan" && mode != "ask" {
		return acp.SetSessionModeResponse{}, acp.NewInvalidParams(nil)
	}
	s, err := a.session(request.SessionId)
	if err != nil {
		return acp.SetSessionModeResponse{}, err
	}
	if !s.mutex.TryLock() {
		return acp.SetSessionModeResponse{}, errors.New("Cursor session is busy")
	}
	defer s.mutex.Unlock()
	response, err := a.Agent.SetSessionMode(ctx, request)
	if err == nil && s.workMode != mode {
		s.workMode = mode
		err = s.stop()
	}
	return response, err
}

// CloseSession 只释放当前连接的进程，持久聊天仍可恢复。
func (a *Agent) CloseSession(ctx context.Context, request acp.CloseSessionRequest) (acp.CloseSessionResponse, error) {
	a.lifecycleMutex.Lock()
	defer a.lifecycleMutex.Unlock()
	_ = a.Cancel(ctx, acp.CancelNotification{SessionId: request.SessionId})
	s, err := a.session(request.SessionId)
	if err != nil {
		return acp.CloseSessionResponse{}, err
	}
	s.mutex.Lock()
	cleanupErr := s.stop()
	s.mutex.Unlock()
	if cleanupErr != nil {
		return acp.CloseSessionResponse{}, cleanupErr
	}
	response, err := a.Agent.CloseSession(ctx, request)
	if s.owner != nil {
		ownerErr := s.owner.Unlock()
		err = errors.Join(err, ownerErr)
		if ownerErr == nil {
			s.owner = nil
		}
	}
	err = errors.Join(err, os.RemoveAll(s.directory))
	if err != nil {
		return response, err
	}
	a.mutex.Lock()
	delete(a.sessions, request.SessionId)
	a.mutex.Unlock()
	return response, nil
}

// Close 先撤销交互并回收进程，再删除包含临时凭据的隔离目录。
func (a *Agent) Close(ctx context.Context) error {
	a.lifecycleMutex.Lock()
	defer a.lifecycleMutex.Unlock()
	a.mutex.Lock()
	a.closed = true
	sessions := make([]*interactiveSession, 0, len(a.sessions))
	for _, s := range a.sessions {
		sessions = append(sessions, s)
	}
	a.mutex.Unlock()
	a.cancel()
	var cleanupErr error
	for _, s := range sessions {
		s.mutex.Lock()
		err := s.stop()
		s.mutex.Unlock()
		if err != nil {
			cleanupErr = errors.Join(cleanupErr, err)
			continue
		}
		if s.owner != nil {
			err = s.owner.Unlock()
			if err == nil {
				s.owner = nil
			}
		}
		if err != nil {
			cleanupErr = errors.Join(cleanupErr, err)
			continue
		}
		if err = os.RemoveAll(s.directory); err != nil {
			cleanupErr = errors.Join(cleanupErr, err)
			continue
		}
		a.mutex.Lock()
		delete(a.sessions, s.id)
		a.mutex.Unlock()
	}
	err := errors.Join(cleanupErr, a.Agent.Close(ctx))
	if cleanupErr == nil && a.directory != "" {
		err = errors.Join(err, os.RemoveAll(a.directory))
	}
	return err
}

// cursorPromptError 保留 SDK 必须直接识别的协议错误类型，清理错误仅在没有主错误时决定本轮结果。
func cursorPromptError(primary, cleanup error) error {
	if primary == nil {
		return cleanup
	}
	var request *acp.RequestError
	if errors.As(primary, &request) {
		return request
	}
	return errors.Join(primary, cleanup)
}

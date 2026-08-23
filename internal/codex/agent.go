// Package codex 提供 Codex app-server 到 ACP Agent 的生命周期适配。
package codex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"

	"acp-go/agents/codex/protocol"
	acp "github.com/coder/acp-go-sdk"
)

const (
	// agentName 是 ACP initialize 中的稳定实现名称。
	agentName = "codex"
	// agentTitle 是 ACP 客户端展示的 Adapter 名称。
	agentTitle = "Codex"
	// agentVersion 是当前仓库尚未注入构建版本时的显式开发标识。
	agentVersion = "development"
	// appServerCleanupTimeout 为已获得的远端资源提供不继承请求取消的有界释放窗口。
	appServerCleanupTimeout = 5 * time.Second
)

var (
	// ErrInvalidLogger 表示 Codex Adapter 缺少进程级诊断 logger。
	ErrInvalidLogger = errors.New("invalid codex logger")
	// ErrRuntimeUnavailable 兼容 foundation 的可检查错误，并用于 runtime 已关闭路径。
	ErrRuntimeUnavailable = errors.New("codex runtime unavailable")
	// ErrAgentNotInitialized 表示 thread/turn 请求早于 app-server initialize。
	ErrAgentNotInitialized = errors.New("codex agent not initialized")
	// ErrConnectionNotReady 表示事件发送前外层 SDK connection 尚未注入。
	ErrConnectionNotReady = errors.New("ACP agent connection not ready")
)

// Config 保存 Codex Adapter 组合根必须显式提供的依赖与启动选项。
type Config struct {
	// Logger 把版本警告、stderr 和异常诊断写到进程 stderr。
	Logger *slog.Logger
	// CodexPath 是 CODEX_PATH 的值；空值才允许从 PATH 查询。
	CodexPath string
}

// appServerRouter 解决 transport 必须先启动 reader，而 typed client/Agent 随后才可构造的依赖环。
type appServerRouter struct {
	// mu 保护 client 与 agent 发布。
	mu sync.RWMutex
	// client 是接收 discriminator-first 通知的 typed client。
	client *appServerClient
	// agent 是消费审批 server request 的 runtime；构造窗口内可为空。
	agent *Agent
}

// route 把通知交给已发布 client；构造窗口内到达的无请求通知安全忽略。
func (r *appServerRouter) route(ctx context.Context, notification protocol.ServerNotification) {
	r.mu.RLock()
	client := r.client
	r.mu.RUnlock()
	if client != nil {
		client.HandleNotification(ctx, notification)
	}
}

// publish 原子发布 typed client。
func (r *appServerRouter) publish(client *appServerClient) {
	r.mu.Lock()
	r.client = client
	r.mu.Unlock()
}

// publishAgent 原子发布已完成依赖注入的 runtime Agent。
func (r *appServerRouter) publishAgent(agent *Agent) {
	r.mu.Lock()
	r.agent = agent
	r.mu.Unlock()
}

// routeServerRequest 把生成协议变体交给 Agent；构造窗口内按请求类型返回拒绝安全结果。
func (r *appServerRouter) routeServerRequest(ctx context.Context, request protocol.ServerRequest) (any, error) {
	r.mu.RLock()
	agent := r.agent
	r.mu.RUnlock()
	if agent == nil {
		return failClosedServerRequest(request)
	}
	return agent.handleServerRequest(ctx, request)
}

// Agent 是 Codex Adapter 的 ACP 协议入口，并拥有唯一 app-server runtime。
type Agent struct {
	// logger 是进程级诊断入口，绝不写外层 ACP stdout。
	logger *slog.Logger
	// runtimeCtx 跨单次 ACP 请求存活，直到 Adapter Close。
	runtimeCtx context.Context
	// runtimeCancel 终止所有后台 turn、steering 和通知任务。
	runtimeCancel context.CancelFunc
	// client 是 app-server 生成 DTO typed client。
	client *appServerClient
	// transport 是 Codex 无 jsonrpc NDJSON 边界；测试组合可为空。
	transport *appServerTransport
	// process 是当前 Adapter 唯一 `codex app-server` 进程；测试组合可为空。
	process *appServerProcess
	// sessions 管理 session generation、close fence 与 active prompt。
	sessions *sessionStore
	// steering 管理每 session 有界 FIFO extension 请求。
	steering *steeringManager
	// auth 复用已验证的 API Key 与 ChatGPT 认证组件。
	auth *authenticator
	// initializeMu 保护 initialized。
	initializeMu sync.RWMutex
	// initialized 表示 app-server initialize/initialized 已成功完成。
	initialized bool
	// connectionMu 保护外层 SDK connection。
	connectionMu sync.RWMutex
	// connection 是 acp-go-sdk 创建的唯一 AgentSideConnection。
	connection *acp.AgentSideConnection
	// approvalRequester 是 approval 组件消费的窄接口；生产值始终与 connection 相同，测试可独立注入。
	approvalRequester permissionRequester
	// sessionUpdater 是 event/history 组件消费的 SDK 原生 session/update 窄接口。
	sessionUpdater sessionUpdater
	// connectionReady 是 notification/approval 路由的启动 barrier。
	connectionReady chan struct{}
	// connectionReadyOnce 保证 binder 重复调用不会 panic。
	connectionReadyOnce sync.Once
	// promptMu 保护所有前台已结束但仍在观察迟到 start 的 prompt 身份。
	promptMu sync.Mutex
	// prompts 把每个尚未完全结束的 prompt 关联到精确 session generation 状态。
	prompts map[*activePrompt]*sessionState
	// promptsClosing 阻止 Adapter Close 开始后安装新的 prompt 后台任务。
	promptsClosing bool
	// nextTurnGeneration 为每个 prompt/steering turn 分配 Adapter 内单调身份。
	nextTurnGeneration atomic.Uint64
	// closeOnce 保证 transport/process 只释放一次。
	closeOnce sync.Once
	// closeErr 保存首次 Close 的结果。
	closeErr error
}

var (
	_ acp.Agent                  = (*Agent)(nil)
	_ acp.AgentLoader            = (*Agent)(nil)
	_ acp.ExtensionMethodHandler = (*Agent)(nil)
)

// NewAgent 解析用户预装 Codex、探测版本并启动唯一 app-server 进程。
func NewAgent(ctx context.Context, config Config) (*Agent, error) {
	return newAgentWithVersionRunner(ctx, config, runVersionCommand)
}

// newAgentWithVersionRunner 保留组合根的真实进程装配，仅允许测试隔离短生命周期版本探测。
// 该缝直接复用 prepareExecutable 的 commandRunner，不引入覆盖 runtime 的大接口。
func newAgentWithVersionRunner(ctx context.Context, config Config, runVersion commandRunner) (*Agent, error) {
	if config.Logger == nil {
		return nil, fmt.Errorf("creating codex agent: %w", ErrInvalidLogger)
	}
	if runVersion == nil {
		return nil, errors.New("creating codex agent: version runner is nil")
	}
	executable, err := prepareExecutable(ctx, config.CodexPath, config.Logger, exec.LookPath, runVersion)
	if err != nil {
		return nil, fmt.Errorf("creating codex agent: %w", err)
	}
	// construction/Serve context 只约束启动；成功后 runtime 由 Agent.Close 单独拥有，
	// 否则信号取消会让 exec.CommandContext 抢在 acpserver 的有界清理窗口前杀死子进程。
	runtimeCtx, runtimeCancel := context.WithCancel(context.WithoutCancel(ctx))
	process, err := startAppServer(runtimeCtx, executable.Path, processOptions{Logger: config.Logger})
	if err != nil {
		runtimeCancel()
		return nil, fmt.Errorf("creating codex agent: %w", err)
	}

	router := &appServerRouter{}
	transport := newAppServerTransport(
		runtimeCtx,
		process.Stdout(),
		process.Stdin(),
		nil,
		transportOptions{
			Logger:               config.Logger,
			NotificationHandler:  router.route,
			ServerRequestHandler: router.routeServerRequest,
			EOFError:             process.FinalError,
		},
	)
	client := newAppServerClient(runtimeCtx, transport)
	router.publish(client)
	agent := newAgentWithClient(config.Logger, runtimeCtx, runtimeCancel, client)
	agent.transport = transport
	agent.process = process
	router.publishAgent(agent)
	return agent, nil
}

// newAgentWithClient 通过手工依赖注入创建 Agent，测试无需伪造大 Runtime 接口或真实进程。
func newAgentWithClient(
	logger *slog.Logger,
	runtimeCtx context.Context,
	runtimeCancel context.CancelFunc,
	client *appServerClient,
) *Agent {
	agent := &Agent{
		logger:          logger,
		runtimeCtx:      runtimeCtx,
		runtimeCancel:   runtimeCancel,
		client:          client,
		sessions:        newSessionStore(),
		connectionReady: make(chan struct{}),
		prompts:         make(map[*activePrompt]*sessionState),
	}
	agent.steering = newSteeringManager(agent, defaultSteeringQueueCapacity)
	agent.auth = newAuthenticator(client, client, systemBrowserOpener{}, os.Getenv, logger)
	client.SetNotificationHandler(agent.handleNotification)
	return agent
}

// SetAgentConnection 接收 acpserver 创建的 SDK connection，并释放事件路由 barrier。
// v0.13.5 connection 构造后已启动读取 goroutine；此处绝不调用存在竞态的 SetLogger。
func (a *Agent) SetAgentConnection(connection *acp.AgentSideConnection) {
	a.connectionMu.Lock()
	if a.connection == nil {
		a.connection = connection
		a.approvalRequester = connection
		a.sessionUpdater = connection
	}
	a.connectionMu.Unlock()
	a.markConnectionReady()
}

// markConnectionReady 关闭 binder barrier；测试可在不构造 SDK connection 时复用同一同步语义。
func (a *Agent) markConnectionReady() {
	a.connectionReadyOnce.Do(func() { close(a.connectionReady) })
}

// waitConnection 等待 acpserver 在 SDK goroutine 启动后完成 connection 注入。
func (a *Agent) waitConnection(ctx context.Context) (*acp.AgentSideConnection, error) {
	select {
	case <-a.connectionReady:
		a.connectionMu.RLock()
		connection := a.connection
		a.connectionMu.RUnlock()
		return connection, nil
	case <-ctx.Done():
		return nil, fmt.Errorf("waiting for ACP connection: %w", ctx.Err())
	case <-a.runtimeCtx.Done():
		return nil, fmt.Errorf("waiting for ACP connection: %w", ErrConnectionNotReady)
	}
}

// Authenticate 把 ACP 认证请求交给现有 authenticator，不在 Agent 重写登录状态机。
func (a *Agent) Authenticate(
	ctx context.Context,
	request acp.AuthenticateRequest,
) (acp.AuthenticateResponse, error) {
	if err := a.requireInitialized(); err != nil {
		return acp.AuthenticateResponse{}, err
	}
	if a.auth == nil {
		return acp.AuthenticateResponse{}, errors.New("codex authenticator is unavailable")
	}
	if err := a.auth.Authenticate(ctx, request); err != nil {
		return acp.AuthenticateResponse{}, err
	}
	return acp.AuthenticateResponse{}, nil
}

// Initialize 先完成唯一 app-server 握手，再声明真实可用的 session/prompt 能力。
func (a *Agent) Initialize(ctx context.Context, request acp.InitializeRequest) (acp.InitializeResponse, error) {
	clientInfo := protocol.ClientInfo{Name: "acp-client", Title: stringPointer("ACP Client"), Version: "unknown"}
	if request.ClientInfo != nil {
		clientInfo.Name = request.ClientInfo.Name
		clientInfo.Title = request.ClientInfo.Title
		clientInfo.Version = request.ClientInfo.Version
	}
	if _, err := a.client.Initialize(ctx, clientInfo); err != nil {
		return acp.InitializeResponse{}, fmt.Errorf("initializing codex app-server: %w", err)
	}
	a.initializeMu.Lock()
	a.initialized = true
	a.initializeMu.Unlock()
	title := agentTitle
	return acp.InitializeResponse{
		ProtocolVersion: acp.ProtocolVersionNumber,
		AgentCapabilities: acp.AgentCapabilities{
			Auth:        acp.AgentAuthCapabilities{Logout: &acp.LogoutCapabilities{}},
			LoadSession: true,
			PromptCapabilities: acp.PromptCapabilities{
				Image: true, EmbeddedContext: true,
			},
			SessionCapabilities: acp.SessionCapabilities{
				Close: &acp.SessionCloseCapabilities{}, Resume: &acp.SessionResumeCapabilities{},
			},
		},
		AgentInfo:   &acp.Implementation{Name: agentName, Title: &title, Version: agentVersion},
		AuthMethods: codexAuthMethods(true),
	}, nil
}

// Logout 等价执行 upstream 的 subscribe account/updated → account/logout → wait 顺序。
func (a *Agent) Logout(ctx context.Context, _ acp.LogoutRequest) (acp.LogoutResponse, error) {
	if err := a.requireInitialized(); err != nil {
		return acp.LogoutResponse{}, err
	}
	subscription := a.client.SubscribeAccountUpdated()
	defer subscription.Close()
	if err := a.client.AccountLogout(ctx); err != nil {
		return acp.LogoutResponse{}, fmt.Errorf("logging out Codex account: %w", err)
	}
	if err := subscription.Wait(ctx); err != nil {
		return acp.LogoutResponse{}, fmt.Errorf("waiting for Codex account update: %w", err)
	}
	return acp.LogoutResponse{}, nil
}

// NewSession 把 ACP session/new 映射为 Codex thread/start 并安装 generation 状态。
func (a *Agent) NewSession(ctx context.Context, request acp.NewSessionRequest) (acp.NewSessionResponse, error) {
	if err := a.requireInitialized(); err != nil {
		return acp.NewSessionResponse{}, err
	}
	cwd := request.Cwd
	response, err := a.client.ThreadStart(ctx, protocol.ThreadStartParams{Cwd: &cwd})
	if err != nil {
		return acp.NewSessionResponse{}, fmt.Errorf("starting codex thread: %w", err)
	}
	if response.Thread.ID == "" {
		return acp.NewSessionResponse{}, errors.New("starting codex thread: empty thread id")
	}
	configuration, err := a.configurationForSession(ctx, response.Model, response.ReasoningEffort)
	if err != nil {
		cleanupCtx, cancelCleanup := newAppServerCleanupContext(ctx)
		defer cancelCleanup()
		unsubscribeErr := a.client.ThreadUnsubscribe(cleanupCtx, response.Thread.ID)
		return acp.NewSessionResponse{}, fmt.Errorf(
			"configuring new codex thread %q: %w",
			response.Thread.ID,
			errors.Join(err, unsubscribeErr),
		)
	}
	generation, err := a.sessions.beginOpen(response.Thread.ID)
	if err != nil {
		cleanupCtx, cancelCleanup := newAppServerCleanupContext(ctx)
		defer cancelCleanup()
		return acp.NewSessionResponse{}, errors.Join(err, a.client.ThreadUnsubscribe(cleanupCtx, response.Thread.ID))
	}
	state, installed := a.sessions.install(response.Thread.ID, request.Cwd, generation, configuration)
	if !installed {
		return acp.NewSessionResponse{}, a.closeStaleOpen(ctx, response.Thread.ID, generation)
	}
	modes, options := sessionConfigurationResponse(state)
	return acp.NewSessionResponse{
		SessionId:     acp.SessionId(response.Thread.ID),
		Modes:         modes,
		ConfigOptions: options,
	}, nil
}

// ResumeSession 使用 thread/resume 恢复订阅，并受 generation/close fence 保护。
func (a *Agent) ResumeSession(
	ctx context.Context,
	request acp.ResumeSessionRequest,
) (acp.ResumeSessionResponse, error) {
	_, state, err := a.openExistingSession(ctx, string(request.SessionId), request.Cwd, false)
	if err != nil {
		return acp.ResumeSessionResponse{}, err
	}
	modes, options := sessionConfigurationResponse(state)
	return acp.ResumeSessionResponse{Modes: modes, ConfigOptions: options}, nil
}

// LoadSession 按固定 upstream 顺序执行 thread/resume→thread/read(includeTurns=true)，再安装状态。
func (a *Agent) LoadSession(ctx context.Context, request acp.LoadSessionRequest) (acp.LoadSessionResponse, error) {
	thread, state, err := a.openExistingSession(ctx, string(request.SessionId), request.Cwd, true)
	if err != nil {
		return acp.LoadSessionResponse{}, err
	}
	if err = a.replayThreadHistory(ctx, state, thread); err != nil {
		cleanupCtx, cancelCleanup := newAppServerCleanupContext(ctx)
		defer cancelCleanup()
		_, closeErr := a.CloseSession(cleanupCtx, acp.CloseSessionRequest{SessionId: request.SessionId})
		return acp.LoadSessionResponse{}, fmt.Errorf(
			"replaying codex thread %q: %w",
			request.SessionId,
			errors.Join(err, closeErr),
		)
	}
	modes, options := sessionConfigurationResponse(state)
	return acp.LoadSessionResponse{Modes: modes, ConfigOptions: options}, nil
}

// openExistingSession 复用 resume/load 的 generation 状态机，并按需在安装前读取历史。
func (a *Agent) openExistingSession(
	ctx context.Context,
	sessionID string,
	cwd string,
	includeHistory bool,
) (protocol.Thread, *sessionState, error) {
	if err := a.requireInitialized(); err != nil {
		return protocol.Thread{}, nil, err
	}
	generation, err := a.sessions.beginOpen(sessionID)
	if err != nil {
		return protocol.Thread{}, nil, err
	}
	response, err := a.client.ThreadResume(ctx, protocol.ThreadResumeParams{ThreadID: sessionID, Cwd: &cwd})
	if err != nil {
		a.sessions.abandonOpen(sessionID, generation)
		return protocol.Thread{}, nil, fmt.Errorf("resuming codex thread %q: %w", sessionID, err)
	}
	thread := response.Thread
	if includeHistory {
		includeTurns := true
		readResponse, readErr := a.client.ThreadRead(ctx, protocol.ThreadReadParams{
			ThreadID: sessionID, IncludeTurns: &includeTurns,
		})
		if readErr != nil {
			return protocol.Thread{}, nil, a.cleanupFailedSubscribedOpen(ctx, sessionID, generation, readErr)
		}
		thread = readResponse.Thread
	}
	configuration, err := a.configurationForSession(ctx, response.Model, response.ReasoningEffort)
	if err != nil {
		return protocol.Thread{}, nil, a.cleanupFailedSubscribedOpen(ctx, sessionID, generation, err)
	}
	state, installed := a.sessions.install(sessionID, cwd, generation, configuration)
	if !installed {
		return protocol.Thread{}, nil, a.closeStaleOpen(ctx, sessionID, generation)
	}
	return thread, state, nil
}

// configurationForSession 复用 model/list 与 sessionConfiguration 组装真实 session 配置。
// app-server 响应缺失 schema-required model 时显式失败，避免向 ACP 客户端静默降级能力。
func (a *Agent) configurationForSession(
	ctx context.Context,
	model string,
	effort *string,
) (*sessionConfiguration, error) {
	if model == "" {
		return nil, errors.New("Codex session response did not include a model")
	}
	models, err := a.client.ListModels(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing Codex models: %w", err)
	}
	if len(models) == 0 {
		return nil, errors.New("Codex did not return any models")
	}
	currentEffort := ""
	if effort != nil {
		currentEffort = *effort
	}
	configuration, err := newSessionConfiguration(models, model, currentEffort, "agent")
	if err != nil {
		return nil, fmt.Errorf("building session configuration: %w", err)
	}
	return configuration, nil
}

// sessionConfigurationResponse 在 session 锁内生成 ACP mode/config 快照。
func sessionConfigurationResponse(
	state *sessionState,
) (*acp.SessionModeState, []acp.SessionConfigOption) {
	if state == nil {
		return nil, nil
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.configuration == nil {
		return nil, nil
	}
	modes := state.configuration.ModeState()
	return &modes, state.configuration.Options()
}

// turnParamsForSession 在 session 锁内快照 cwd 与当前配置，供普通 Prompt 和 steering fallback 共用。
// 返回值不持有 session 内部指针，后续配置更新不会改写已发送的 turn/start。
func turnParamsForSession(
	state *sessionState,
	input []protocol.InputElement,
	messageID *string,
) protocol.TurnStartParams {
	state.mu.Lock()
	defer state.mu.Unlock()
	cwd := state.cwd
	params := protocol.TurnStartParams{
		ThreadID:            state.id,
		Input:               input,
		ClientUserMessageID: messageID,
		Cwd:                 &cwd,
	}
	if state.configuration == nil {
		return params
	}
	selection := state.configuration.Selection()
	mode := state.configuration.ModeDefinition()
	params.Model = &selection.Model
	if selection.Effort != "" {
		params.Effort = &selection.Effort
	}
	params.ApprovalPolicy = &mode.ApprovalPolicy
	params.SandboxPolicy = &mode.SandboxPolicy
	return params
}

// newAppServerCleanupContext 从原请求中只保留 value，用独立 deadline 确保 unsubscribe/cancel 能实际写出。
func newAppServerCleanupContext(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(parent), appServerCleanupTimeout)
}

// cleanupFailedSubscribedOpen 释放已订阅但后续 read/配置失败的 thread。
func (a *Agent) cleanupFailedSubscribedOpen(ctx context.Context, sessionID string, generation uint64, cause error) error {
	if a.sessions.beginStaleCleanup(sessionID, generation) {
		defer a.sessions.endClose(sessionID)
		cleanupCtx, cancelCleanup := newAppServerCleanupContext(ctx)
		defer cancelCleanup()
		if err := a.client.ThreadUnsubscribe(cleanupCtx, sessionID); err != nil {
			return fmt.Errorf("reading codex thread %q: %w", sessionID, errors.Join(cause, err))
		}
	}
	return fmt.Errorf("reading codex thread %q: %w", sessionID, cause)
}

// closeStaleOpen 仅在旧 open 仍是最新订阅时 unsubscribe，绝不影响新 reopen。
func (a *Agent) closeStaleOpen(ctx context.Context, sessionID string, generation uint64) error {
	if a.sessions.beginStaleCleanup(sessionID, generation) {
		defer a.sessions.endClose(sessionID)
		cleanupCtx, cancelCleanup := newAppServerCleanupContext(ctx)
		defer cancelCleanup()
		if err := a.client.ThreadUnsubscribe(cleanupCtx, sessionID); err != nil {
			return fmt.Errorf("closing stale session %q: %w", sessionID, errors.Join(ErrSessionClosing, err))
		}
	}
	return fmt.Errorf("opening session %q: %w", sessionID, ErrSessionClosing)
}

// CloseSession 提升 generation、取消活动 prompt，再 unsubscribe Codex thread。
func (a *Agent) CloseSession(ctx context.Context, request acp.CloseSessionRequest) (acp.CloseSessionResponse, error) {
	sessionID := string(request.SessionId)
	_, state := a.sessions.beginClose(sessionID)
	defer a.sessions.endClose(sessionID)
	a.steering.CloseSession(sessionID, ErrSessionClosing)
	var cleanupErr error
	prompts := a.closeSessionPrompts(state)
	for _, prompt := range prompts {
		turnID := prompt.requestCancel()
		prompt.markForegroundFinished()
		a.releaseActivePrompt(state, prompt)
		if turnID != "" {
			a.client.MarkTurnStale(sessionID, turnID)
			a.requestPromptInterrupt(state, prompt, turnID, false)
			select {
			case <-prompt.interruptDone:
			case <-ctx.Done():
			}
			a.client.ResolveTurnInterrupted(sessionID, turnID)
		}
		// TS Promise 无法主动取消 pending turn/start；Go transport 的 context 必须显式解除请求和 goroutine。
		prompt.cancelRun()
	}
	for _, prompt := range prompts {
		select {
		case <-prompt.backgroundDone:
		case <-ctx.Done():
			cleanupErr = fmt.Errorf("waiting for pending codex turn in session %q: %w", sessionID, ctx.Err())
		}
		select {
		case <-prompt.foregroundDone:
		case <-ctx.Done():
			cleanupErr = errors.Join(
				cleanupErr,
				fmt.Errorf("waiting for active ACP prompt in session %q: %w", sessionID, ctx.Err()),
			)
		}
	}
	if err := a.client.ThreadUnsubscribe(ctx, sessionID); err != nil {
		cleanupErr = errors.Join(cleanupErr, fmt.Errorf("unsubscribing codex thread %q: %w", sessionID, err))
	}
	return acp.CloseSessionResponse{}, cleanupErr
}

// Prompt 提交 turn/start，并按精确 completion 产生 ACP stop reason。
func (a *Agent) Prompt(ctx context.Context, request acp.PromptRequest) (acp.PromptResponse, error) {
	if err := a.requireInitialized(); err != nil {
		return acp.PromptResponse{}, err
	}
	state, ok := a.sessions.get(string(request.SessionId))
	if !ok {
		return acp.PromptResponse{}, fmt.Errorf("prompting session %q: %w", request.SessionId, ErrSessionNotFound)
	}
	if _, err := a.waitConnection(ctx); err != nil {
		return acp.PromptResponse{}, err
	}
	input, err := buildPromptInput(request.Prompt)
	if err != nil {
		return acp.PromptResponse{}, err
	}
	prompt := newActivePrompt(a.runtimeCtx, a.nextTurnGeneration.Add(1))
	if err := a.installActivePrompt(state, prompt); err != nil {
		prompt.cancelRun()
		return acp.PromptResponse{}, fmt.Errorf("prompting session %q: %w", request.SessionId, err)
	}
	// 对应 upstream prompt 的 finally/activePrompt.complete：前台返回立即释放槽位，
	// 而普通提前取消时 RunTurn 观察者继续等待迟到 turn/start 并执行 stale interrupt。
	defer func() {
		prompt.markForegroundFinished()
		a.releaseActivePrompt(state, prompt)
		a.finishPromptForeground(prompt)
	}()

	// turnResult 在后台 runtime 生命周期与前台 ACP 请求之间传递一次精确 turn 结果。
	type turnResult struct {
		// completion 是精确 turn 的完成通知。
		completion protocol.TurnCompletedNotification
		// err 是 turn/start、completion 或 process fatal 错误。
		err error
	}
	resultChannel := make(chan turnResult, 1)
	turnParams := turnParamsForSession(state, input, request.MessageId)
	go func() {
		defer a.finishPromptBackground(prompt)
		completion, runErr := a.client.RunTurn(prompt.runCtx, turnParams, func(turnID string) {
			a.onTurnStarted(state, prompt, turnID)
		})
		prompt.cancelRun()
		resultChannel <- turnResult{completion: completion, err: runErr}
	}()

	requestDone := ctx.Done()
	cancelSignal := prompt.cancelSignal
	for {
		select {
		case result := <-resultChannel:
			if result.err != nil {
				if errors.Is(result.err, context.Canceled) {
					_, cancelled := prompt.currentTurn()
					if cancelled {
						return acp.PromptResponse{StopReason: acp.StopReasonCancelled}, nil
					}
				}
				return acp.PromptResponse{}, fmt.Errorf("running codex turn: %w", result.err)
			}
			switch result.completion.Turn.Status {
			case protocol.FluffyInterrupted:
				return acp.PromptResponse{StopReason: acp.StopReasonCancelled}, nil
			case protocol.FluffyCompleted:
				return acp.PromptResponse{StopReason: acp.StopReasonEndTurn, UserMessageId: request.MessageId}, nil
			case protocol.Failed:
				return acp.PromptResponse{}, fmt.Errorf("codex turn %q failed", result.completion.Turn.ID)
			default:
				return acp.PromptResponse{}, fmt.Errorf(
					"codex turn %q completed with unexpected status %q",
					result.completion.Turn.ID,
					result.completion.Turn.Status,
				)
			}
		case <-requestDone:
			turnID := prompt.requestCancel()
			if turnID == "" {
				return acp.PromptResponse{StopReason: acp.StopReasonCancelled}, nil
			}
			a.requestPromptInterrupt(state, prompt, turnID, false)
			requestDone = nil
			cancelSignal = nil
		case <-cancelSignal:
			turnID, _ := prompt.currentTurn()
			if turnID == "" {
				return acp.PromptResponse{StopReason: acp.StopReasonCancelled}, nil
			}
			a.requestPromptInterrupt(state, prompt, turnID, false)
			requestDone = nil
			cancelSignal = nil
		case <-a.runtimeCtx.Done():
			return acp.PromptResponse{}, fmt.Errorf("running codex turn: %w", ErrRuntimeUnavailable)
		}
	}
}

// installActivePrompt 在统一锁序下同时安装 session 活动槽位与后台 registry 身份。
func (a *Agent) installActivePrompt(state *sessionState, prompt *activePrompt) error {
	a.promptMu.Lock()
	defer a.promptMu.Unlock()
	state.mu.Lock()
	defer state.mu.Unlock()
	if a.promptsClosing {
		return ErrRuntimeUnavailable
	}
	if state.promptClosed {
		return ErrSessionClosing
	}
	if state.activePrompt != nil {
		return ErrPromptActive
	}
	state.activePrompt = prompt
	a.prompts[prompt] = state
	return nil
}

// closeSessionPrompts 原子关闭旧 session state 的 prompt 安装入口，并返回其所有前台/后台身份。
func (a *Agent) closeSessionPrompts(state *sessionState) []*activePrompt {
	if state == nil {
		return nil
	}
	a.promptMu.Lock()
	defer a.promptMu.Unlock()
	state.mu.Lock()
	state.promptClosed = true
	state.mu.Unlock()
	prompts := make([]*activePrompt, 0)
	for prompt, owner := range a.prompts {
		if owner == state {
			prompts = append(prompts, prompt)
		}
	}
	return prompts
}

// closeAllPrompts 关闭 Adapter 级安装入口，并返回所有尚未完全结束的 prompt。
func (a *Agent) closeAllPrompts() []*activePrompt {
	a.promptMu.Lock()
	defer a.promptMu.Unlock()
	a.promptsClosing = true
	prompts := make([]*activePrompt, 0, len(a.prompts))
	for prompt := range a.prompts {
		prompts = append(prompts, prompt)
	}
	return prompts
}

// finishPromptForeground 标记前台 finally 完成，并在后台也结束后移除 registry 身份。
func (a *Agent) finishPromptForeground(prompt *activePrompt) {
	prompt.markForegroundDone()
	a.untrackPromptIfDone(prompt)
}

// finishPromptBackground 标记 RunTurn goroutine 完成，并在前台也结束后移除 registry 身份。
func (a *Agent) finishPromptBackground(prompt *activePrompt) {
	prompt.markBackgroundDone()
	a.untrackPromptIfDone(prompt)
}

// untrackPromptIfDone 仅在前台与后台信号都关闭后删除精确 prompt 身份。
func (a *Agent) untrackPromptIfDone(prompt *activePrompt) {
	select {
	case <-prompt.foregroundDone:
	default:
		return
	}
	select {
	case <-prompt.backgroundDone:
	default:
		return
	}
	a.promptMu.Lock()
	delete(a.prompts, prompt)
	a.promptMu.Unlock()
}

// onTurnStarted 处理正常或取消后迟到的 turn identity，并触发一次 interrupt。
func (a *Agent) onTurnStarted(state *sessionState, prompt *activePrompt, turnID string) {
	late := prompt.setTurn(turnID) || !a.sessions.isCurrent(state)
	if late {
		a.requestPromptInterrupt(state, prompt, turnID, true)
		return
	}
	if updater := a.currentSessionUpdater(); updater != nil {
		prompt.setEventRouter(newEventRouter(updater, turnGeneration{
			SessionID:  acp.SessionId(state.id),
			ThreadID:   state.id,
			TurnID:     turnID,
			Generation: prompt.generation,
		}, a, a.logger))
	}
}

// requestPromptInterrupt 使用 activePrompt 的 Once 保证所有取消来源合计只请求一次。
// resolveInterrupted 对应 upstream interruptLateStartedTurn：迟到 start 无前台等待真实 completion，故 finally 合成 interrupted。
func (a *Agent) requestPromptInterrupt(
	state *sessionState,
	prompt *activePrompt,
	turnID string,
	resolveInterrupted bool,
) {
	if resolveInterrupted {
		a.client.MarkTurnStale(state.id, turnID)
	}
	prompt.interruptOnce.Do(func() {
		go func() {
			defer close(prompt.interruptDone)
			if resolveInterrupted {
				defer a.client.ResolveTurnInterrupted(state.id, turnID)
			}
			if err := a.client.TurnInterrupt(a.runtimeCtx, state.id, turnID); err != nil {
				a.logger.Warn("Codex turn interrupt 失败", "thread_id", state.id, "turn_id", turnID, "error", err)
			}
		}()
	})
}

// releaseActivePrompt 只清理仍指向当前身份的前台活动槽位，绝不影响已安装的后继 prompt。
func (a *Agent) releaseActivePrompt(state *sessionState, prompt *activePrompt) {
	state.mu.Lock()
	if state.activePrompt == prompt {
		state.activePrompt = nil
	}
	state.mu.Unlock()
}

// clearActivePrompt 释放无独立 ACP Prompt 所有者的 steering 活动槽位；后台 registry 由 defer 回收。
func (a *Agent) clearActivePrompt(state *sessionState, prompt *activePrompt) {
	a.releaseActivePrompt(state, prompt)
}

// Cancel 幂等取消 session 的 pending/active prompt；turn 未知时不发送 interrupt。
func (a *Agent) Cancel(_ context.Context, notification acp.CancelNotification) error {
	state, ok := a.sessions.get(string(notification.SessionId))
	if !ok {
		return nil
	}
	state.mu.Lock()
	prompt := state.activePrompt
	state.mu.Unlock()
	if prompt == nil {
		return nil
	}
	turnID := prompt.requestCancel()
	if turnID != "" {
		a.requestPromptInterrupt(state, prompt, turnID, false)
	}
	return nil
}

// ListSessions 未在 runtime 子变更实现列表 mapper。
func (a *Agent) ListSessions(context.Context, acp.ListSessionsRequest) (acp.ListSessionsResponse, error) {
	return acp.ListSessionsResponse{}, acp.NewMethodNotFound(acp.AgentMethodSessionList)
}

// SetSessionConfigOption 校验 SDK union，并把选择写入精确当前 session 的现有配置组件。
func (a *Agent) SetSessionConfigOption(
	_ context.Context,
	request acp.SetSessionConfigOptionRequest,
) (acp.SetSessionConfigOptionResponse, error) {
	if request.ValueId == nil || request.Boolean != nil {
		return acp.SetSessionConfigOptionResponse{}, acp.NewInvalidParams(map[string]any{
			"error": "session config requires a string value",
		})
	}
	value := request.ValueId
	var options []acp.SessionConfigOption
	err := a.sessions.withCurrent(string(value.SessionId), func(state *sessionState) error {
		if state.configuration == nil {
			return acp.NewInvalidParams(map[string]any{
				"error": "session configuration is unavailable",
			})
		}
		if selectErr := state.configuration.Select(value.ConfigId, string(value.Value)); selectErr != nil {
			return acp.NewInvalidParams(map[string]any{"error": selectErr.Error()})
		}
		options = state.configuration.Options()
		return nil
	})
	if errors.Is(err, ErrSessionNotFound) {
		return acp.SetSessionConfigOptionResponse{}, fmt.Errorf(
			"setting config for session %q: %w",
			value.SessionId,
			ErrSessionNotFound,
		)
	}
	if err != nil {
		return acp.SetSessionConfigOptionResponse{}, err
	}
	return acp.SetSessionConfigOptionResponse{ConfigOptions: options}, nil
}

// SetSessionMode 复用 mode 配置选择；未知模式绝不回退到更宽松权限。
func (a *Agent) SetSessionMode(
	_ context.Context,
	request acp.SetSessionModeRequest,
) (acp.SetSessionModeResponse, error) {
	err := a.sessions.withCurrent(string(request.SessionId), func(state *sessionState) error {
		if state.configuration == nil {
			return acp.NewInvalidParams(map[string]any{
				"error": "session configuration is unavailable",
			})
		}
		if selectErr := state.configuration.Select(modeConfigID, string(request.ModeId)); selectErr != nil {
			return acp.NewInvalidParams(map[string]any{"error": selectErr.Error()})
		}
		return nil
	})
	if errors.Is(err, ErrSessionNotFound) {
		return acp.SetSessionModeResponse{}, fmt.Errorf(
			"setting mode for session %q: %w",
			request.SessionId,
			ErrSessionNotFound,
		)
	}
	if err != nil {
		return acp.SetSessionModeResponse{}, err
	}
	return acp.SetSessionModeResponse{}, nil
}

// HandleExtensionMethod 把唯一 V1 steering extension 交给有界 FIFO manager；其余使用 SDK 标准错误。
func (a *Agent) HandleExtensionMethod(ctx context.Context, method string, params json.RawMessage) (any, error) {
	if method != steeringExtensionMethod {
		return nil, acp.NewMethodNotFound(method)
	}
	return a.steering.Handle(ctx, params)
}

// Close 幂等终止 session、transport 与唯一 app-server 进程。
func (a *Agent) Close(ctx context.Context) error {
	a.closeOnce.Do(func() {
		a.steering.Close(ErrRuntimeUnavailable)
		prompts := a.closeAllPrompts()
		a.sessions.closeAll()
		for _, prompt := range prompts {
			prompt.requestCancel()
			prompt.markForegroundFinished()
			prompt.cancelRun()
		}
		for _, prompt := range prompts {
			select {
			case <-prompt.backgroundDone:
			case <-ctx.Done():
				a.closeErr = errors.Join(a.closeErr, ctx.Err())
			}
			select {
			case <-prompt.foregroundDone:
			case <-ctx.Done():
				a.closeErr = errors.Join(a.closeErr, ctx.Err())
			}
		}
		if a.transport != nil {
			a.closeErr = errors.Join(a.closeErr, a.transport.Close())
		}
		if a.process != nil {
			a.closeErr = errors.Join(a.closeErr, a.process.Close(ctx))
		}
		a.runtimeCancel()
	})
	return a.closeErr
}

// requireInitialized 拒绝任何早于 app-server initialize 的 thread/turn I/O。
func (a *Agent) requireInitialized() error {
	a.initializeMu.RLock()
	initialized := a.initialized
	a.initializeMu.RUnlock()
	if !initialized {
		return ErrAgentNotInitialized
	}
	return nil
}

// stringPointer 返回字段可选值指针。
func stringPointer(value string) *string {
	return &value
}

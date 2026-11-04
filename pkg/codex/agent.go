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

	"github.com/JieWaZi/acp-go/internal/buildinfo"
	"github.com/JieWaZi/acp-go/pkg/acpmeta"
	"github.com/JieWaZi/acp-go/pkg/codex/protocol"
	acp "github.com/JieWaZi/acp-go/sdk"
)

const (
	// agentName 是 ACP initialize 中的稳定实现名称。
	agentName = "codex"
	// agentTitle 是 ACP 客户端展示的 Adapter 名称。
	agentTitle = "Codex"
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
	// PrefixArgs 是放在 Codex 子命令之前的调用方启动参数。
	PrefixArgs []string
	// Environment 是 Adapter、版本探测和 app-server 使用的完整环境；nil 表示继承当前进程。
	Environment []string
	// DefaultModeRequestUserInput 在用户未配置对应 feature 时默认启用普通模式提问；false 保留 Codex 默认行为。
	DefaultModeRequestUserInput bool
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
	// executable 是构造时已解析并完成版本探测的 Codex CLI。
	executable executable
	// getenv 读取调用方完整环境中的 Adapter 开关与认证信息。
	getenv environmentLookup
	// defaultModeRequestUserInput 是调用方选择的普通模式提问默认策略，不覆盖 Codex 显式配置。
	defaultModeRequestUserInput bool
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
	// initializeMu 保护 initialized 与 terminalOutputMode。
	initializeMu sync.RWMutex
	// initialized 表示 app-server initialize/initialized 已成功完成。
	initialized bool
	// terminalOutputMode 是最近一次成功 initialize 协商的 session 输出模式。
	terminalOutputMode terminalOutputMode
	// elicitationCapabilities 是客户端在 initialize 中声明的 ACP 表单/URL 交互能力快照。
	elicitationCapabilities *acp.ElicitationCapabilities
	// connectionMu 保护外层 SDK connection。
	connectionMu sync.RWMutex
	// connection 是 acp-go-sdk 创建的唯一 AgentSideConnection。
	connection *acp.AgentSideConnection
	// approvalRequester 是 approval 组件消费的窄接口；生产值始终与 connection 相同，测试可独立注入。
	approvalRequester permissionRequester
	// sessionUpdater 是 event/history 组件消费的 SDK 原生 session/update 窄接口。
	sessionUpdater sessionUpdater
	// elicitationRequester 是 MCP 与 request_user_input 使用的 ACP 交互窄接口。
	elicitationRequester elicitationRequester
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
	// mcpMu 保护启动状态快照与已接受 URL elicitation。
	mcpMu sync.Mutex
	// mcpStatuses 保存每个清洗后 server 名称的最后启动状态，供早到通知在 session 安装后补发。
	mcpStatuses map[string]mcpStatusSnapshot
	// mcpStatusVersion 为每条启动状态分配单调水位，隔离后续同名 server 的旧终态。
	mcpStatusVersion uint64
	// pendingURLElicitations 按 thread 保存等待 serverRequest/resolved 的 URL elicitation。
	pendingURLElicitations map[string]map[acp.UnstableElicitationId]struct{}
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
	executable, err := prepareExecutable(ctx, config, exec.LookPath, runVersion)
	if err != nil {
		return nil, fmt.Errorf("creating codex agent: %w", err)
	}
	// construction/Serve context 只约束启动；成功后 runtime 由 Agent.Close 单独拥有，
	// 否则信号取消会让 exec.CommandContext 抢在 acpserver 的有界清理窗口前杀死子进程。
	runtimeCtx, runtimeCancel := context.WithCancel(context.WithoutCancel(ctx))
	process, err := startAppServer(runtimeCtx, executable.Path, processOptions{
		PrefixArgs:  config.PrefixArgs,
		Environment: config.Environment,
		Logger:      config.Logger,
	})
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
	agent.executable = executable
	agent.setEnvironment(config.Environment)
	agent.defaultModeRequestUserInput = config.DefaultModeRequestUserInput
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
		logger:                 logger,
		getenv:                 os.Getenv,
		runtimeCtx:             runtimeCtx,
		runtimeCancel:          runtimeCancel,
		client:                 client,
		sessions:               newSessionStore(),
		connectionReady:        make(chan struct{}),
		prompts:                make(map[*activePrompt]*sessionState),
		mcpStatuses:            make(map[string]mcpStatusSnapshot),
		pendingURLElicitations: make(map[string]map[acp.UnstableElicitationId]struct{}),
	}
	agent.steering = newSteeringManager(agent, defaultSteeringQueueCapacity)
	agent.auth = newAuthenticator(client, client, systemBrowserOpener{}, agent.getenv, logger)
	client.SetNotificationHandler(agent.handleNotification)
	return agent
}

// setEnvironment 让进程内 Adapter 行为与其探测和 app-server 使用同一环境快照。
func (a *Agent) setEnvironment(environment []string) {
	a.getenv = environmentLookupFromList(environment)
	a.auth.getenv = a.getenv
}

// SetAgentConnection 接收 acpserver 创建的 SDK connection，并释放事件路由 barrier。
// v0.13.5 connection 构造后已启动读取 goroutine；此处绝不调用存在竞态的 SetLogger。
func (a *Agent) SetAgentConnection(connection *acp.AgentSideConnection) {
	a.connectionMu.Lock()
	if a.connection == nil {
		a.connection = connection
		a.approvalRequester = connection
		a.sessionUpdater = connection
		a.elicitationRequester = connection
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

// Initialize 先完成唯一 app-server 握手，再声明真实可用的 session/prompt 能力。
func (a *Agent) Initialize(ctx context.Context, request acp.InitializeRequest) (acp.InitializeResponse, error) {
	terminalMode := resolveTerminalOutputMode(request.ClientCapabilities)
	clientInfo := protocol.ClientInfo{Name: "acp-client", Title: stringPointer("ACP Client"), Version: "unknown"}
	if request.ClientInfo != nil {
		clientInfo.Name = request.ClientInfo.Name
		clientInfo.Title = request.ClientInfo.Title
		clientInfo.Version = request.ClientInfo.Version
	}
	if _, err := a.client.Initialize(ctx, clientInfo); err != nil {
		return acp.InitializeResponse{}, fmt.Errorf("initializing codex app-server: %w", err)
	}
	// 只有 app-server 握手成功后才发布能力快照，失败重试不能污染后续 session。
	a.initializeMu.Lock()
	a.initialized = true
	a.terminalOutputMode = terminalMode
	a.elicitationCapabilities = cloneElicitationCapabilities(request.ClientCapabilities.Elicitation)
	a.initializeMu.Unlock()
	title := agentTitle
	return acp.InitializeResponse{
		ProtocolVersion: acp.ProtocolVersionNumber,
		AgentCapabilities: acp.AgentCapabilities{
			Auth:            acp.AgentAuthCapabilities{Logout: &acp.LogoutCapabilities{}},
			LoadSession:     true,
			McpCapabilities: acp.McpCapabilities{Http: true},
			PromptCapabilities: acp.PromptCapabilities{
				Image: true, EmbeddedContext: true,
			},
			SessionCapabilities: acp.SessionCapabilities{
				Fork:                  acpmeta.ForkCapability(acpmeta.VerifiedForkMode(a.executable.Version, "0.155.1", acpmeta.ForkTurn)),
				AdditionalDirectories: &acp.SessionAdditionalDirectoriesCapabilities{},
				Close:                 &acp.SessionCloseCapabilities{},
				Resume:                &acp.SessionResumeCapabilities{},
			},
		},
		AgentInfo: &acp.Implementation{
			Meta:    acpmeta.RuntimeVersionMetadata(a.executable.Version),
			Name:    agentName,
			Title:   &title,
			Version: buildinfo.Current(),
		},
		AuthMethods: codexAuthMethods(a.auth.browserAuthEnabled()),
		Meta: map[string]any{
			"developerInstructions": acpmeta.DeveloperInstructionsCapability(acpmeta.VerifiedForkMode(a.executable.Version, "0.155.1", acpmeta.ForkTurn) != acpmeta.ForkUnsupported),
			"steering":              map[string]any{"supported": true},
			"native_management":     map[string]any{"account_read": true, "account_usage": true, "compact": true, "schema_version": "0.148.0", "provider_support": "unknown"},
			"fork":                  map[string]any{"mode": acpmeta.VerifiedForkMode(a.executable.Version, "0.155.1", acpmeta.ForkTurn)},
		},
	}, nil
}

// currentTerminalOutputMode 返回最近一次成功 initialize 的协商结果。
func (a *Agent) currentTerminalOutputMode() terminalOutputMode {
	a.initializeMu.RLock()
	defer a.initializeMu.RUnlock()
	if a.terminalOutputMode == terminalOutputModeFull {
		return terminalOutputModeFull
	}
	return terminalOutputModeDelta
}

// HandleExtensionMethod 把唯一 V1 steering extension 交给有界 FIFO manager；其余使用 SDK 标准错误。
func (a *Agent) HandleExtensionMethod(ctx context.Context, method string, params json.RawMessage) (any, error) {
	switch method {
	case steeringExtensionMethod:
		return a.steering.Handle(ctx, params)
	case acpmeta.AccountReadMethod:
		return a.AccountInfo(ctx)
	case acpmeta.AccountUsageMethod:
		return a.AccountUsage(ctx)
	case acpmeta.CompactMethod:
		return a.compact(ctx, params)
	default:
		return nil, acp.NewMethodNotFound(method)
	}
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

// Package claude 提供 Claude Code CLI 到 ACP Agent 的生命周期适配。
package claude

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"sync"
	"time"

	acp "github.com/coder/acp-go-sdk"
)

const (
	// claudeAgentName 是 initialize 返回的稳定实现名称。
	claudeAgentName = "claude"
	// claudeAgentTitle 是客户端展示的 Adapter 名称。
	claudeAgentTitle = "Claude Agent"
	// claudeAgentVersion 是尚未注入构建版本时的开发标识。
	claudeAgentVersion = "development"
	// sessionCloseTimeout 限制单个 Session 的进程收尾时间。
	sessionCloseTimeout = 5 * time.Second
)

var (
	// ErrInvalidClaudeLogger 表示组合根没有提供诊断 logger。
	ErrInvalidClaudeLogger = errors.New("invalid Claude logger")
	// ErrClaudeAgentNotInitialized 表示 Session 请求早于 initialize。
	ErrClaudeAgentNotInitialized = errors.New("Claude agent not initialized")
	// ErrClaudeSessionNotFound 表示内存中没有目标 Session。
	ErrClaudeSessionNotFound = errors.New("Claude session not found")
	// ErrClaudeConnectionNotReady 表示权限或更新发生时 ACP connection 尚未注入。
	ErrClaudeConnectionNotReady = errors.New("Claude ACP connection not ready")
)

// Config 保存 Claude Adapter 组合根必须提供的依赖与启动选项。
type Config struct {
	// Logger 把版本、stderr 和异常诊断写到进程 stderr。
	Logger *slog.Logger
	// ClaudePath 是 CLAUDE_CODE_EXECUTABLE 的值；空值才允许查询 PATH。
	ClaudePath string
}

// sessionUpdater 是事件组件消费的 ACP session/update 窄接口。
type sessionUpdater interface {
	// SessionUpdate 向客户端发送一个 Session 增量。
	SessionUpdate(ctx context.Context, notification acp.SessionNotification) error
}

// permissionRequester 是权限组件消费的 ACP requestPermission 窄接口。
type permissionRequester interface {
	// RequestPermission 请求客户端选择明确的允许或拒绝选项。
	RequestPermission(ctx context.Context, request acp.RequestPermissionRequest) (acp.RequestPermissionResponse, error)
}

// Agent 是 Claude Adapter 的 ACP 协议入口，并拥有全部 Session 进程。
type Agent struct {
	// logger 是进程级诊断入口，绝不写 ACP stdout。
	logger *slog.Logger
	// executable 是构造时已经验证的 CLI。
	executable executable
	// runtimeCtx 跨单次 ACP 请求存活，直到 Adapter Close。
	runtimeCtx context.Context
	// runtimeCancel 终止全部 Session 与后台任务。
	runtimeCancel context.CancelFunc
	// sessions 按 Claude Session ID 保存独立进程状态。
	sessions *claudeSessionStore
	// openMu 串行化同一 ID 的恢复、替换和安装边界。
	openMu sync.Mutex
	// initializedMu 保护 initialized。
	initializedMu sync.RWMutex
	// initialized 表示 ACP initialize 已成功完成。
	initialized bool
	// connectionMu 保护外层 ACP connection 和窄接口。
	connectionMu sync.RWMutex
	// connection 是 acpserver 注入的唯一 AgentSideConnection。
	connection *acp.AgentSideConnection
	// updater 向客户端发送 Session 更新；测试可单独注入。
	updater sessionUpdater
	// permissionRequester 向客户端发起权限选择；测试可单独注入。
	permissionRequester permissionRequester
	// idGenerator 为新 Session 和消息生成 UUID。
	idGenerator func() (string, error)
	// closeOnce 保证全部 Session 只释放一次。
	closeOnce sync.Once
	// closeErr 保存第一次 Close 的结果。
	closeErr error
}

var (
	_ acp.Agent                  = (*Agent)(nil)
	_ acp.AgentLoader            = (*Agent)(nil)
	_ acp.ExtensionMethodHandler = (*Agent)(nil)
)

// NewAgent 解析用户预装 CLI；Session 进程直到 session/new、load 或 resume 才启动。
func NewAgent(ctx context.Context, config Config) (*Agent, error) {
	if config.Logger == nil {
		return nil, fmt.Errorf("creating Claude agent: %w", ErrInvalidClaudeLogger)
	}
	executable, err := prepareExecutable(ctx, config.ClaudePath, config.Logger, exec.LookPath, runClaudeVersion)
	if err != nil {
		return nil, fmt.Errorf("creating Claude agent: %w", err)
	}
	runtimeCtx, runtimeCancel := context.WithCancel(context.WithoutCancel(ctx))
	return &Agent{
		logger:        config.Logger,
		executable:    executable,
		runtimeCtx:    runtimeCtx,
		runtimeCancel: runtimeCancel,
		sessions:      newClaudeSessionStore(),
		idGenerator:   generateUUID,
	}, nil
}

// SetAgentConnection 接收 acpserver 创建的 ACP connection。
func (a *Agent) SetAgentConnection(connection *acp.AgentSideConnection) {
	a.connectionMu.Lock()
	if a.connection == nil {
		a.connection = connection
		a.updater = connection
		a.permissionRequester = connection
	}
	a.connectionMu.Unlock()
}

// Initialize 声明 Claude V1 已实现的能力，不宣告登录或其他未实现扩展。
func (a *Agent) Initialize(_ context.Context, _ acp.InitializeRequest) (acp.InitializeResponse, error) {
	a.initializedMu.Lock()
	a.initialized = true
	a.initializedMu.Unlock()
	title := claudeAgentTitle
	return acp.InitializeResponse{
		ProtocolVersion: acp.ProtocolVersionNumber,
		AgentCapabilities: acp.AgentCapabilities{
			LoadSession: true,
			PromptCapabilities: acp.PromptCapabilities{
				Image: true, EmbeddedContext: true,
			},
			McpCapabilities: acp.McpCapabilities{Http: true, Sse: true},
			SessionCapabilities: acp.SessionCapabilities{
				AdditionalDirectories: &acp.SessionAdditionalDirectoriesCapabilities{},
				Close:                 &acp.SessionCloseCapabilities{},
				Resume:                &acp.SessionResumeCapabilities{},
			},
		},
		AgentInfo:   &acp.Implementation{Name: claudeAgentName, Title: &title, Version: claudeAgentVersion},
		AuthMethods: []acp.AuthMethod{},
		Meta: map[string]any{
			"steering": map[string]any{"supported": true},
		},
	}, nil
}

// Authenticate 返回不支持错误；Claude V1 只使用 CLI 已有认证状态。
func (a *Agent) Authenticate(context.Context, acp.AuthenticateRequest) (acp.AuthenticateResponse, error) {
	return acp.AuthenticateResponse{}, acp.NewMethodNotFound("authenticate")
}

// Logout 返回不支持错误；Adapter 不修改用户的 CLI 登录状态。
func (a *Agent) Logout(context.Context, acp.LogoutRequest) (acp.LogoutResponse, error) {
	return acp.LogoutResponse{}, acp.NewMethodNotFound("logout")
}

// ListSessions 返回不支持错误；initialize 不宣告该能力。
func (a *Agent) ListSessions(context.Context, acp.ListSessionsRequest) (acp.ListSessionsResponse, error) {
	return acp.ListSessionsResponse{}, acp.NewMethodNotFound("session/list")
}

// NewSession 创建新 UUID，并在完整握手后发布独立 CLI Session。
func (a *Agent) NewSession(ctx context.Context, request acp.NewSessionRequest) (acp.NewSessionResponse, error) {
	if err := a.requireInitialized(); err != nil {
		return acp.NewSessionResponse{}, err
	}
	sessionID, err := a.idGenerator()
	if err != nil {
		return acp.NewSessionResponse{}, fmt.Errorf("creating Claude session id: %w", err)
	}
	session, err := a.openSession(ctx, openSessionRequest{
		SessionID: sessionID, CWD: request.Cwd, AdditionalDirectories: request.AdditionalDirectories,
		MCPServers: request.McpServers,
	})
	if err != nil {
		return acp.NewSessionResponse{}, err
	}
	return acp.NewSessionResponse{
		SessionId: acp.SessionId(session.id), ConfigOptions: session.configOptions(), Modes: session.modeState(),
	}, nil
}

// ResumeSession 恢复已有会话，但不向客户端回放历史消息。
func (a *Agent) ResumeSession(ctx context.Context, request acp.ResumeSessionRequest) (acp.ResumeSessionResponse, error) {
	if err := a.requireInitialized(); err != nil {
		return acp.ResumeSessionResponse{}, err
	}
	session, err := a.openSession(ctx, openSessionRequest{
		SessionID: string(request.SessionId), CWD: request.Cwd, Resume: true,
		AdditionalDirectories: request.AdditionalDirectories, MCPServers: request.McpServers,
	})
	if err != nil {
		return acp.ResumeSessionResponse{}, err
	}
	return acp.ResumeSessionResponse{ConfigOptions: session.configOptions(), Modes: session.modeState()}, nil
}

// LoadSession 恢复已有会话，并在返回前回放本地可表达的历史消息。
func (a *Agent) LoadSession(ctx context.Context, request acp.LoadSessionRequest) (acp.LoadSessionResponse, error) {
	if err := a.requireInitialized(); err != nil {
		return acp.LoadSessionResponse{}, err
	}
	session, err := a.openSession(ctx, openSessionRequest{
		SessionID: string(request.SessionId), CWD: request.Cwd, Resume: true,
		AdditionalDirectories: request.AdditionalDirectories, MCPServers: request.McpServers,
	})
	if err != nil {
		return acp.LoadSessionResponse{}, err
	}
	if err := a.replaySessionHistory(ctx, session); err != nil {
		if a.sessions.removeExact(session) {
			closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), sessionCloseTimeout)
			_ = session.close(closeCtx)
			cancel()
		}
		return acp.LoadSessionResponse{}, err
	}
	return acp.LoadSessionResponse{ConfigOptions: session.configOptions(), Modes: session.modeState()}, nil
}

// Prompt 把请求排入 Session FIFO，并等待该 turn 的唯一结果。
func (a *Agent) Prompt(ctx context.Context, request acp.PromptRequest) (acp.PromptResponse, error) {
	if err := a.requireInitialized(); err != nil {
		return acp.PromptResponse{}, err
	}
	session, ok := a.sessions.get(string(request.SessionId))
	if !ok {
		return acp.PromptResponse{}, fmt.Errorf("prompting Claude session %q: %w", request.SessionId, ErrClaudeSessionNotFound)
	}
	messageID := ""
	if request.MessageId != nil {
		messageID = *request.MessageId
	} else {
		var err error
		messageID, err = a.idGenerator()
		if err != nil {
			return acp.PromptResponse{}, fmt.Errorf("creating Claude message id: %w", err)
		}
	}
	message, err := promptToUserMessage(session.id, messageID, request.Prompt, "")
	if err != nil {
		return acp.PromptResponse{}, err
	}
	return session.prompt(ctx, message)
}

// Cancel 中断活动 turn，并取消尚未开始的 FIFO 请求。
func (a *Agent) Cancel(ctx context.Context, request acp.CancelNotification) error {
	session, ok := a.sessions.get(string(request.SessionId))
	if !ok {
		return nil
	}
	return session.cancelTurns(ctx)
}

// CloseSession 从 store 移除并释放一个 Session；重复关闭保持幂等。
func (a *Agent) CloseSession(ctx context.Context, request acp.CloseSessionRequest) (acp.CloseSessionResponse, error) {
	session, ok := a.sessions.remove(string(request.SessionId))
	if !ok {
		return acp.CloseSessionResponse{}, nil
	}
	if err := session.close(ctx); err != nil && !errors.Is(err, ErrClaudeTransportClosed) {
		return acp.CloseSessionResponse{}, fmt.Errorf("closing Claude session %q: %w", request.SessionId, err)
	}
	return acp.CloseSessionResponse{}, nil
}

// SetSessionConfigOption 校验 union 后把配置变化发送到活动 CLI。
func (a *Agent) SetSessionConfigOption(ctx context.Context, request acp.SetSessionConfigOptionRequest) (acp.SetSessionConfigOptionResponse, error) {
	sessionID := ""
	if request.Boolean != nil {
		sessionID = string(request.Boolean.SessionId)
	} else if request.ValueId != nil {
		sessionID = string(request.ValueId.SessionId)
	} else {
		return acp.SetSessionConfigOptionResponse{}, errors.New("setting Claude config: request has no value")
	}
	session, ok := a.sessions.get(sessionID)
	if !ok {
		return acp.SetSessionConfigOptionResponse{}, fmt.Errorf("setting Claude config: %w", ErrClaudeSessionNotFound)
	}
	if err := session.setConfigOption(ctx, request); err != nil {
		return acp.SetSessionConfigOptionResponse{}, err
	}
	return acp.SetSessionConfigOptionResponse{ConfigOptions: session.configOptions()}, nil
}

// SetSessionMode 更新权限模式，并保持 mode 与同名 config option 一致。
func (a *Agent) SetSessionMode(ctx context.Context, request acp.SetSessionModeRequest) (acp.SetSessionModeResponse, error) {
	session, ok := a.sessions.get(string(request.SessionId))
	if !ok {
		return acp.SetSessionModeResponse{}, fmt.Errorf("setting Claude mode: %w", ErrClaudeSessionNotFound)
	}
	if err := session.setMode(ctx, string(request.ModeId)); err != nil {
		return acp.SetSessionModeResponse{}, err
	}
	return acp.SetSessionModeResponse{}, nil
}

// HandleExtensionMethod 处理 `_session/steering`，其余扩展返回 method-not-found。
func (a *Agent) HandleExtensionMethod(ctx context.Context, method string, params json.RawMessage) (any, error) {
	if method != "_session/steering" {
		return nil, acp.NewMethodNotFound(method)
	}
	request, err := parseSteeringRequest(params)
	if err != nil {
		return nil, err
	}
	session, ok := a.sessions.get(request.SessionID)
	if !ok {
		return nil, fmt.Errorf("steering Claude session: %w", ErrClaudeSessionNotFound)
	}
	messageID, err := a.idGenerator()
	if err != nil {
		return nil, fmt.Errorf("creating Claude steering id: %w", err)
	}
	message, err := promptToUserMessage(session.id, messageID, request.Prompt, "now")
	if err != nil {
		return nil, err
	}
	return session.steer(ctx, message, request.PromptRequired)
}

// Close 幂等移除并关闭全部 Session。
func (a *Agent) Close(ctx context.Context) error {
	a.closeOnce.Do(func() {
		cleanupCtx, cancelCleanup := context.WithTimeout(context.WithoutCancel(ctx), sessionCloseTimeout)
		defer cancelCleanup()
		sessions := a.sessions.removeAll()
		for _, session := range sessions {
			err := session.close(cleanupCtx)
			if err != nil && !errors.Is(err, ErrClaudeTransportClosed) && a.closeErr == nil {
				a.closeErr = err
			}
		}
		// 所有 Session 已获得温和关闭窗口后，再取消进程级兜底上下文。
		a.runtimeCancel()
	})
	return a.closeErr
}

// requireInitialized 检查 ACP initialize barrier。
func (a *Agent) requireInitialized() error {
	a.initializedMu.RLock()
	initialized := a.initialized
	a.initializedMu.RUnlock()
	if !initialized {
		return ErrClaudeAgentNotInitialized
	}
	return nil
}

// sendUpdate 获取当前 connection 快照并发送 Session 更新。
func (a *Agent) sendUpdate(ctx context.Context, sessionID string, update acp.SessionUpdate) error {
	a.connectionMu.RLock()
	updater := a.updater
	a.connectionMu.RUnlock()
	if updater == nil {
		return ErrClaudeConnectionNotReady
	}
	return updater.SessionUpdate(ctx, acp.SessionNotification{SessionId: acp.SessionId(sessionID), Update: update})
}

// requestPermission 获取当前 connection 快照并发起权限请求。
func (a *Agent) requestPermission(ctx context.Context, request acp.RequestPermissionRequest) (acp.RequestPermissionResponse, error) {
	a.connectionMu.RLock()
	requester := a.permissionRequester
	a.connectionMu.RUnlock()
	if requester == nil {
		return acp.RequestPermissionResponse{}, ErrClaudeConnectionNotReady
	}
	return requester.RequestPermission(ctx, request)
}

// generateUUID 使用系统随机源生成 RFC 4122 version 4 UUID。
func generateUUID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	value[6] = value[6]&0x0f | 0x40
	value[8] = value[8]&0x3f | 0x80
	encoded := make([]byte, 36)
	hex.Encode(encoded[0:8], value[0:4])
	encoded[8] = '-'
	hex.Encode(encoded[9:13], value[4:6])
	encoded[13] = '-'
	hex.Encode(encoded[14:18], value[6:8])
	encoded[18] = '-'
	hex.Encode(encoded[19:23], value[8:10])
	encoded[23] = '-'
	hex.Encode(encoded[24:36], value[10:16])
	return string(encoded), nil
}

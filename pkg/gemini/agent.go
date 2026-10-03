// Package gemini 连接官方原生 ACP CLI。
package gemini

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"sync"

	"github.com/JieWaZi/acp-go/internal/buildinfo"
	"github.com/JieWaZi/acp-go/pkg/nativeacp"
	acp "github.com/coder/acp-go-sdk"
)

// Config 保存受控进程配置。
type Config struct {
	// StateDirectory 保存适配器持久状态；空值按账号和工作区选择私有缓存。
	StateDirectory string
	// GeminiPath 是原生 CLI 可执行文件路径。
	GeminiPath string
	// PrefixArgs 是协议参数前的受控参数。
	PrefixArgs []string
	// Environment 是完整环境；nil 继承宿主，非 nil 完全替换。
	Environment []string
	// WorkingDirectory 是进程启动目录。
	WorkingDirectory string
	// Logger 接收诊断。
	Logger *slog.Logger
	// PermissionMode 支持 default、auto 审查与 full-access 原生 yolo。
	PermissionMode string
}

// sessionChild 拥有一个不可变启动配置的官方 CLI 进程。
type sessionChild struct {
	// agent 复用唯一原生 ACP 传输。
	agent *nativeacp.Agent
	// directory 是本子进程的临时用户设置目录。
	directory string
	// profile 是保存新凭据与记录的受管状态。
	profile *profileState
	// once 保证并发关闭只清理一次。
	once sync.Once
	// closeErr 保存一次关闭结果。
	closeErr error
}

// close 回收子进程后持久化原生状态并清理临时设置。
func (child *sessionChild) close(ctx context.Context) error {
	child.once.Do(func() {
		closeErr := child.agent.Close(ctx)
		// 调用方取消只结束其等待；进程已经被终止，必须回收后才清理它仍可能写入的目录。
		<-child.agent.Done()
		persistErr := child.profile.persistOverlay(child.directory)
		var cleanupErr error
		if persistErr == nil {
			cleanupErr = os.RemoveAll(child.directory)
		}
		child.closeErr = errors.Join(closeErr, persistErr, cleanupErr)
	})
	return child.closeErr
}

// ownedSession 串行协调公开会话的执行代，不阻塞取消或关闭。
type ownedSession struct {
	// mutex 保护执行代与忙碌计数。
	mutex sync.Mutex
	// child 是当前已提交的原生子进程。
	child *sessionChild
	// record 是已提交的持久身份与配置。
	record sessionRecord
	// request 保存恢复所需的 MCP 与会话元数据。
	request acp.NewSessionRequest
	// options 是最近真实配置目录。
	options []acp.SessionConfigOption
	// modes 是真实原生模式目录。
	modes *acp.SessionModeState
	// queue 在公共会话层保持跨执行代的 FIFO 与取消边界。
	queue []*sessionPrompt
	// pending 包含正在执行和排队的用户请求。
	pending int
	// changing 在配置替换期间阻止新轮次进入旧代。
	changing chan struct{}
	// operationCancel 取消正在准备的替换或模式请求。
	operationCancel context.CancelFunc
	// closed 在关闭后拒绝任何新请求。
	closed bool
}

// Agent 为每个公开会话管理独立官方进程，bootstrap 仅负责初始化与账号操作。
type Agent struct {
	// Agent 提供无会话级原生方法的兼容入口。
	*nativeacp.Agent
	// mutex 保护宿主、初始化及子进程登记。
	mutex sync.Mutex
	// config 是不可变的调用方配置副本。
	config Config
	// profile 保存受管持久状态。
	profile *profileState
	// bootstrap 是初始化与认证进程。
	bootstrap *sessionChild
	// children 包含正在准备与已提交的所有子进程。
	children map[*sessionChild]bool
	// sessions 按稳定公开标识管理会话。
	sessions map[acp.SessionId]*ownedSession
	// host 是真实宿主连接。
	host *acp.AgentSideConnection
	// initialize 是传给每个子进程的初始化快照。
	initialize *acp.InitializeRequest
	// authentication 是最近成功的显式认证输入，供新子进程重放。
	authentication *acp.AuthenticateRequest
	// closed 表示父适配器已经开始关闭。
	closed bool
}

// NewAgent 验证配置并启动引导进程；生命周期不绑定单次构造请求。
func NewAgent(ctx context.Context, config Config) (*Agent, error) {
	if ctx == nil || config.Logger == nil {
		return nil, errors.New("Gemini requires context and logger")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	switch config.PermissionMode {
	case "", "default", "auto", "full-access":
	default:
		return nil, errors.New("invalid Gemini permission mode")
	}
	config.PrefixArgs = slices.Clone(config.PrefixArgs)
	if config.Environment == nil {
		config.Environment = os.Environ()
	} else {
		config.Environment = slices.Clone(config.Environment)
	}
	command := config.GeminiPath
	if command == "" {
		command = "gemini"
	}
	resolved, err := nativeacp.ResolveCommand(command, config.Environment)
	if err != nil {
		return nil, err
	}
	config.GeminiPath = resolved
	if config.WorkingDirectory == "" {
		config.WorkingDirectory, err = os.Getwd()
	} else {
		config.WorkingDirectory, err = filepath.Abs(config.WorkingDirectory)
	}
	if err != nil {
		return nil, err
	}
	config.WorkingDirectory, err = filepath.EvalSymlinks(config.WorkingDirectory)
	if err != nil {
		return nil, err
	}
	profile, err := newProfileState(config.Environment, config.StateDirectory, config.WorkingDirectory)
	if err != nil {
		return nil, err
	}
	agent := &Agent{
		config: config, profile: profile,
		children: map[*sessionChild]bool{}, sessions: map[acp.SessionId]*ownedSession{},
	}
	child, err := agent.startChild(ctx, childConfiguration{reasoning: "default", cwd: config.WorkingDirectory})
	if err != nil {
		return nil, err
	}
	agent.bootstrap = child
	agent.Agent = child.agent
	return agent, nil
}

// childConfiguration 固定一个原生子进程的启动身份与工作区。
type childConfiguration struct {
	// id 是宿主可见身份，空值仅用于引导进程。
	id acp.SessionId
	// model 是不可变启动模型。
	model string
	// reasoning 是不可变思考策略。
	reasoning string
	// cwd 是该会话的真实工作目录。
	cwd string
}

// startChild 登记待初始化进程，确保父关闭能回收失败或尚未提交的执行代。
func (agent *Agent) startChild(ctx context.Context, settings childConfiguration) (*sessionChild, error) {
	directory, environment, err := agent.profile.environment(agent.config.Environment, settings.model, settings.reasoning)
	if err != nil {
		return nil, err
	}
	args := append(slices.Clone(agent.config.PrefixArgs), "--acp")
	if agent.config.PermissionMode == "auto" {
		args = append(args, "--approval-mode", "default")
	}
	if agent.config.PermissionMode == "full-access" {
		args = append(args, "--approval-mode", "yolo")
	}
	adapter := newSessionAdapter()
	config := nativeacp.Config{
		Command: agent.config.GeminiPath, Args: args, Environment: environment,
		WorkingDirectory: settings.cwd, Logger: agent.config.Logger,
		SessionAdapter: adapter, RuntimeName: "gemini",
		VersionArgs: append(slices.Clone(agent.config.PrefixArgs), "--version"),
		PromptFIFO:  true, StrictCloseSession: true,
	}
	if settings.id != "" {
		config.PublicSessionID = func(acp.SessionId) acp.SessionId { return settings.id }
	}
	var upstream *nativeacp.Agent
	if agent.config.PermissionMode == "auto" {
		config.PermissionAdapter = nativeacp.NewReviewPermissionAdapter(nativeacp.ReviewPermissionConfig{
			Mode: "auto", Reviewer: agent.permissionReviewer,
			CurrentSession: func(id acp.SessionId) (nativeacp.SessionContext, bool) { return upstream.CurrentSession(id) },
		})
	}
	upstream, err = nativeacp.NewAgent(ctx, config)
	if err != nil {
		_ = os.RemoveAll(directory)
		return nil, err
	}
	child := &sessionChild{agent: upstream, directory: directory, profile: agent.profile}
	agent.mutex.Lock()
	if agent.closed {
		agent.mutex.Unlock()
		_ = child.close(context.Background())
		return nil, errors.New("Gemini agent closed")
	}
	agent.children[child] = true
	if agent.host != nil {
		upstream.SetAgentConnection(agent.host)
	}
	agent.mutex.Unlock()
	go func() {
		<-upstream.Done()
		if err := agent.retireChild(context.Background(), child); err != nil {
			agent.config.Logger.Debug("Gemini child exited", "error", err)
		}
	}()
	return child, nil
}

// retireChild 释放不再使用的进程并移除登记。
func (agent *Agent) retireChild(ctx context.Context, child *sessionChild) error {
	err := child.close(ctx)
	agent.mutex.Lock()
	delete(agent.children, child)
	agent.mutex.Unlock()
	return err
}

// SetAgentConnection 绑定引导进程和正在准备的所有会话进程。
func (agent *Agent) SetAgentConnection(connection *acp.AgentSideConnection) {
	agent.mutex.Lock()
	defer agent.mutex.Unlock()
	if agent.host != nil || connection == nil {
		return
	}
	agent.host = connection
	for child := range agent.children {
		child.agent.SetAgentConnection(connection)
	}
}

// Initialize 保存能力输入并声明真正实现的关闭和静默恢复。
func (agent *Agent) Initialize(ctx context.Context, request acp.InitializeRequest) (acp.InitializeResponse, error) {
	response, err := agent.Agent.Initialize(ctx, request)
	if err != nil {
		return response, err
	}
	data, err := json.Marshal(request)
	if err != nil {
		return response, err
	}
	var snapshot acp.InitializeRequest
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return response, err
	}
	agent.mutex.Lock()
	agent.initialize = &snapshot
	agent.mutex.Unlock()
	if response.Meta == nil {
		response.Meta = map[string]any{}
	}
	response.Meta["steering"] = map[string]any{"supported": false}
	if response.AgentInfo != nil {
		response.AgentInfo.Version = buildinfo.Current()
	}
	response.AgentCapabilities.SessionCapabilities.Close = &acp.SessionCloseCapabilities{}
	if response.AgentCapabilities.LoadSession {
		response.AgentCapabilities.SessionCapabilities.Resume = &acp.SessionResumeCapabilities{}
	}
	return response, nil
}

// initializeChild 对新执行代使用同一份宿主真实能力。
func (agent *Agent) initializeChild(ctx context.Context, child *sessionChild) error {
	agent.mutex.Lock()
	request := agent.initialize
	authentication := agent.authentication
	agent.mutex.Unlock()
	if request == nil {
		return errors.New("Gemini agent not initialized")
	}
	if _, err := child.agent.Initialize(ctx, *request); err != nil {
		return err
	}
	if authentication != nil {
		_, err := child.agent.Authenticate(ctx, *authentication)
		return err
	}
	return nil
}

// Authenticate 保存原生认证创建的新凭据供后续会话共享。
func (agent *Agent) Authenticate(ctx context.Context, request acp.AuthenticateRequest) (acp.AuthenticateResponse, error) {
	response, err := agent.Agent.Authenticate(ctx, request)
	if err != nil {
		return response, err
	}
	if err := agent.profile.persistOverlay(agent.bootstrap.directory); err != nil {
		return response, err
	}
	data, err := json.Marshal(request)
	if err != nil {
		return response, err
	}
	var snapshot acp.AuthenticateRequest
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return response, err
	}
	agent.mutex.Lock()
	agent.authentication = &snapshot
	agent.mutex.Unlock()
	return response, nil
}

// Close 先标记所有会话关闭，再并行回收全部原生进程与临时配置。
func (agent *Agent) Close(ctx context.Context) error {
	agent.mutex.Lock()
	agent.closed = true
	sessions := make([]*ownedSession, 0, len(agent.sessions))
	for _, session := range agent.sessions {
		sessions = append(sessions, session)
	}
	children := make([]*sessionChild, 0, len(agent.children))
	for child := range agent.children {
		children = append(children, child)
	}
	agent.mutex.Unlock()
	for _, session := range sessions {
		session.mutex.Lock()
		session.closed = true
		for _, prompt := range session.queue {
			prompt.cancel()
		}
		if session.operationCancel != nil {
			session.operationCancel()
		}
		session.mutex.Unlock()
	}
	results := make(chan error, len(children))
	for _, child := range children {
		go func() { results <- agent.retireChild(ctx, child) }()
	}
	var result error
	for range children {
		result = errors.Join(result, <-results)
	}
	return result
}

// publicID 生成与任何原生重建无关的稳定公共身份。
func publicID() (acp.SessionId, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return acp.SessionId("gemini-" + hex.EncodeToString(value[:])), nil
}

// Prompt 保留思考流，并将官方 quota 本轮累计值投影为标准 usage；不构造上下文占用率。
func projectUsage(response acp.PromptResponse, err error) (acp.PromptResponse, error) {
	if err != nil || response.Usage != nil {
		return response, err
	}
	data, marshalErr := json.Marshal(response.Meta["quota"])
	if marshalErr != nil {
		return response, err
	}
	var quota struct {
		// TokenCount 是官方本轮模型调用累计值。
		TokenCount *struct {
			// InputTokens 是本轮输入 token。
			InputTokens int `json:"input_tokens"`
			// OutputTokens 是本轮输出 token。
			OutputTokens int `json:"output_tokens"`
		} `json:"token_count"`
	}
	if json.Unmarshal(data, &quota) == nil && quota.TokenCount != nil {
		input, output := quota.TokenCount.InputTokens, quota.TokenCount.OutputTokens
		if input >= 0 && output >= 0 {
			response.Usage = &acp.Usage{InputTokens: input, OutputTokens: output, TotalTokens: input + output}
		}
	}
	return response, err
}

package codex

import (
	"context"
	"errors"
	"fmt"
	"github.com/JieWaZi/acp-go/pkg/acpmeta"
	acp "github.com/JieWaZi/acp-go/sdk"
	"sync"

	"github.com/JieWaZi/acp-go/pkg/codex/protocol"
)

var (
	// ErrSessionNotFound 表示 ACP SessionID 没有当前本地 generation。
	ErrSessionNotFound = errors.New("codex session not found")
	// ErrSessionClosing 表示 session 正处于 close fence 或 open 结果已经过期。
	ErrSessionClosing = errors.New("codex session is closing")
	// ErrSessionOpening indicates another request owns this session's open attempt.
	ErrSessionOpening = errors.New("codex session is opening")
	// ErrPromptActive 表示同 session 已有 pending/active turn，拒绝产生 rival turn。
	ErrPromptActive = errors.New("codex session already has an active prompt")
)

// sessionState 保存一个已安装 ACP session 的最小 runtime 状态。
type sessionState struct {
	// id 同时作为 ACP SessionID 与 Codex ThreadID。
	id string
	// cwd 是创建或恢复请求指定的工作目录。
	cwd string
	// additionalDirectories 是 Session 创建或恢复时冻结的附加工作目录。
	additionalDirectories []string
	// generation 防止旧 open、notification 和 control 覆盖重开状态。
	generation uint64
	// mu 保护 activePrompt。
	mu sync.Mutex
	// usageTotal 是同一会话最后确认的累计值；恢复或回退后未知。
	usageTotal *turnUsage
	// activePrompt 是该 session 唯一 pending/active turn。
	activePrompt *activePrompt
	// configuration 是 config 子组件维护的 model、effort 与安全模式状态。
	configuration *sessionConfiguration
	// terminalOutputMode 保存 session 创建时的客户端输出能力快照。
	terminalOutputMode terminalOutputMode
	// promptClosed 阻止 close fence 建立后仍持有旧 state 的并发请求安装 prompt。
	promptClosed bool
	// mcpServers 保存本次 ACP open 请求声明的清洗后 MCP server 名称。
	mcpServers map[string]struct{}
	// mcpStartupReported 防止重复启动状态产生重复失败工具项。
	mcpStartupReported map[string]protocol.MCPServerStatusUpdatedNotificationStatus
	// mcpStartupAfterVersion 只接受本次 thread open 之后产生的启动状态。
	mcpStartupAfterVersion uint64
}

// sessionStore 管理 session generation、open identity 与可重入 close fence。
// 状态机保证并发打开、关闭与替换 Session 时只有当前 generation 可以完成安装。
type sessionStore struct {
	// mu 保护全部 map 与 generation 变更。
	mu sync.Mutex
	// sessions 保存当前已安装状态。
	sessions map[string]*sessionState
	// generations 保存每个 SessionID 的单调 generation。
	generations map[string]uint64
	// opening 保存最新一次 in-flight open 的 generation 身份。
	opening map[string]uint64
	// closing 保存可重入 close fence 计数。
	closing map[string]int
}

// newSessionStore 创建空 session registry。
func newSessionStore() *sessionStore {
	return &sessionStore{
		sessions:    make(map[string]*sessionState),
		generations: make(map[string]uint64),
		opening:     make(map[string]uint64),
		closing:     make(map[string]int),
	}
}

// beginOpen 记录当前 generation 的打开身份；close fence 内拒绝启动远端 I/O。
func (s *sessionStore) beginOpen(sessionID string) (uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing[sessionID] > 0 {
		return 0, fmt.Errorf("opening session %q: %w", sessionID, ErrSessionClosing)
	}
	generation := s.generations[sessionID]
	if current, opening := s.opening[sessionID]; opening && current == generation {
		return 0, fmt.Errorf("opening session %q: %w", sessionID, ErrSessionOpening)
	}
	s.opening[sessionID] = generation
	return generation, nil
}

// install 仅在 generation、最新 open 身份与 close fence 全部匹配时安装状态。
func (s *sessionStore) install(
	sessionID string,
	cwd string,
	generation uint64,
	configuration *sessionConfiguration,
	terminalMode terminalOutputMode,
) (*sessionState, bool) {
	return s.installWorkspace(
		sessionID,
		codexWorkspace{CWD: cwd, AdditionalDirectories: []string{}},
		generation,
		configuration,
		terminalMode,
	)
}

// installWorkspace 仅在 generation 当前时安装完整工作范围状态。
func (s *sessionStore) installWorkspace(
	sessionID string,
	workspace codexWorkspace,
	generation uint64,
	configuration *sessionConfiguration,
	terminalMode terminalOutputMode,
) (*sessionState, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	openGeneration, opening := s.opening[sessionID]
	if s.closing[sessionID] > 0 || s.generations[sessionID] != generation || !opening || openGeneration != generation {
		return nil, false
	}
	state := &sessionState{
		id:  sessionID,
		cwd: workspace.CWD,
		additionalDirectories: append(
			[]string{},
			workspace.AdditionalDirectories...,
		),
		generation:         generation,
		configuration:      configuration,
		terminalOutputMode: terminalMode,
	}
	s.sessions[sessionID] = state
	delete(s.opening, sessionID)
	return state, true
}

// workspace 返回 Session 冻结工作范围的深拷贝。
func (s *sessionState) workspace() codexWorkspace {
	s.mu.Lock()
	defer s.mu.Unlock()
	return codexWorkspace{
		CWD:                   s.cwd,
		AdditionalDirectories: append([]string{}, s.additionalDirectories...),
	}
}

// abandonOpen 删除仍属于当前 generation 的失败 open 记录。
func (s *sessionStore) abandonOpen(sessionID string, generation uint64) {
	s.mu.Lock()
	if openGeneration, opening := s.opening[sessionID]; opening && openGeneration == generation {
		delete(s.opening, sessionID)
	}
	s.mu.Unlock()
}

// openCanProceed 验证前置配置读取结束后，本次 open 身份仍未被 close 或更新 open 取代。
func (s *sessionStore) openCanProceed(sessionID string, generation uint64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	openGeneration, opening := s.opening[sessionID]
	return opening && openGeneration == generation && s.closing[sessionID] == 0 && s.generations[sessionID] == generation
}

// beginStaleCleanup 为仍是最新 open 的过期结果建立 close fence。
// 若已有更新 open 覆盖身份，则返回 false，禁止旧请求 unsubscribe 新订阅。
func (s *sessionStore) beginStaleCleanup(sessionID string, generation uint64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	openGeneration, opening := s.opening[sessionID]
	if !opening || openGeneration != generation {
		return false
	}
	delete(s.opening, sessionID)
	if s.closing[sessionID] == 0 {
		s.generations[sessionID]++
	}
	s.closing[sessionID]++
	return true
}

// beginClose 提升 generation、建立 fence，并返回关闭前安装的状态。
func (s *sessionStore) beginClose(sessionID string) (uint64, *sessionState) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.generations[sessionID]++
	generation := s.generations[sessionID]
	s.closing[sessionID]++
	state := s.sessions[sessionID]
	delete(s.sessions, sessionID)
	return generation, state
}

// endClose 释放一层 close fence，保留并发 close/cleanup 的隔离。
func (s *sessionStore) endClose(sessionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	count := s.closing[sessionID]
	if count <= 1 {
		delete(s.closing, sessionID)
		return
	}
	s.closing[sessionID] = count - 1
}

// get 返回当前安装状态。
func (s *sessionStore) get(sessionID string) (*sessionState, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, ok := s.sessions[sessionID]
	return state, ok
}

// snapshot 返回当前安装 session 的指针快照；调用方仍须用 isCurrent 复核 generation。
func (s *sessionStore) snapshot() []*sessionState {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([]*sessionState, 0, len(s.sessions))
	for _, state := range s.sessions {
		result = append(result, state)
	}
	return result
}

// isCurrent 验证状态指针与 generation 仍是当前安装身份。
func (s *sessionStore) isCurrent(state *sessionState) bool {
	if state == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closing[state.id] == 0 && s.generations[state.id] == state.generation && s.sessions[state.id] == state
}

// withCurrent 在 store→state 统一锁序下验证当前身份并执行一次短变更。
// callback 不得调用 sessionStore；持有 store 锁直到变更结束，使 close 与配置更新形成全序。
func (s *sessionStore) withCurrent(sessionID string, callback func(*sessionState) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	state := s.sessions[sessionID]
	if state == nil || s.closing[sessionID] > 0 || s.generations[sessionID] != state.generation {
		return ErrSessionNotFound
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	return callback(state)
}

// closeAll 提升所有 generation、移除 session，并返回需要取消的状态快照。
func (s *sessionStore) closeAll() []*sessionState {
	s.mu.Lock()
	defer s.mu.Unlock()
	states := make([]*sessionState, 0, len(s.sessions))
	for sessionID, state := range s.sessions {
		s.generations[sessionID]++
		states = append(states, state)
		delete(s.sessions, sessionID)
	}
	return states
}

// NewSession 把 ACP session/new 映射为 Codex thread/start 并安装 generation 状态。
func (a *Agent) NewSession(ctx context.Context, request acp.NewSessionRequest) (acp.NewSessionResponse, error) {
	if err := a.requireInitialized(); err != nil {
		return acp.NewSessionResponse{}, err
	}
	if err := a.checkAuthorization(ctx); err != nil {
		return acp.NewSessionResponse{}, err
	}
	instructions, err := acpmeta.DeveloperInstructions(request.Meta)
	if err != nil {
		return acp.NewSessionResponse{}, acp.NewInvalidParams(map[string]any{"error": err.Error()})
	}
	workspace, err := normalizeCodexWorkspace(request.Cwd, request.AdditionalDirectories)
	if err != nil {
		return acp.NewSessionResponse{}, acp.NewInvalidParams(map[string]any{"error": err.Error()})
	}
	if err = a.refreshSkills(ctx, workspace); err != nil {
		return acp.NewSessionResponse{}, err
	}
	config, mcpNames, err := a.sessionConfig(ctx, workspace, request.McpServers)
	if err != nil {
		return acp.NewSessionResponse{}, err
	}
	mcpAfterVersion := a.currentMCPStatusVersion()
	response, err := a.client.ThreadStart(ctx, protocol.ThreadStartParams{
		Cwd:                   &workspace.CWD,
		Config:                config,
		DeveloperInstructions: instructions,
	})
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
	configuration.observedServiceTier = response.ServiceTier
	generation, err := a.sessions.beginOpen(response.Thread.ID)
	if err != nil {
		cleanupCtx, cancelCleanup := newAppServerCleanupContext(ctx)
		defer cancelCleanup()
		return acp.NewSessionResponse{}, errors.Join(err, a.client.ThreadUnsubscribe(cleanupCtx, response.Thread.ID))
	}
	state, installed := a.sessions.installWorkspace(
		response.Thread.ID,
		workspace,
		generation,
		configuration,
		a.currentTerminalOutputMode(),
	)
	if !installed {
		return acp.NewSessionResponse{}, a.closeStaleOpen(ctx, response.Thread.ID, generation)
	}
	// thread/start 创建的新线程具有独立的零基线，不能套用到恢复或 fork。
	state.mu.Lock()
	state.usageTotal = &turnUsage{CacheWriteTokens: new(int64)}
	state.mu.Unlock()
	a.publishKnownMCPStartupFailures(state, mcpNames, mcpAfterVersion)
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
	_, state, err := a.openExistingSession(
		ctx,
		string(request.SessionId),
		request.Cwd,
		request.AdditionalDirectories,
		request.McpServers,
		request.Meta,
		false,
	)
	if err != nil {
		return acp.ResumeSessionResponse{}, err
	}
	modes, options := sessionConfigurationResponse(state)
	return acp.ResumeSessionResponse{Modes: modes, ConfigOptions: options}, nil
}

// LoadSession 先恢复并读取完整 Thread 历史，再安装 Session 状态。
func (a *Agent) LoadSession(ctx context.Context, request acp.LoadSessionRequest) (acp.LoadSessionResponse, error) {
	thread, state, err := a.openExistingSession(
		ctx,
		string(request.SessionId),
		request.Cwd,
		request.AdditionalDirectories,
		request.McpServers,
		request.Meta,
		true,
	)
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
	additionalDirectories []string,
	mcpServers []acp.McpServer,
	meta map[string]any,
	includeHistory bool,
) (protocol.Thread, *sessionState, error) {
	if err := a.requireInitialized(); err != nil {
		return protocol.Thread{}, nil, err
	}
	if err := a.checkAuthorization(ctx); err != nil {
		return protocol.Thread{}, nil, err
	}
	instructions, err := acpmeta.DeveloperInstructions(meta)
	if err != nil {
		return protocol.Thread{}, nil, acp.NewInvalidParams(map[string]any{"error": err.Error()})
	}
	generation, err := a.sessions.beginOpen(sessionID)
	if err != nil {
		return protocol.Thread{}, nil, err
	}
	workspace, err := normalizeCodexWorkspace(cwd, additionalDirectories)
	if err != nil {
		a.sessions.abandonOpen(sessionID, generation)
		return protocol.Thread{}, nil, acp.NewInvalidParams(map[string]any{"error": err.Error()})
	}
	if err = a.refreshSkills(ctx, workspace); err != nil {
		a.sessions.abandonOpen(sessionID, generation)
		return protocol.Thread{}, nil, err
	}
	config, mcpNames, err := a.sessionConfig(ctx, workspace, mcpServers)
	if err != nil {
		a.sessions.abandonOpen(sessionID, generation)
		return protocol.Thread{}, nil, err
	}
	if !a.sessions.openCanProceed(sessionID, generation) {
		a.sessions.abandonOpen(sessionID, generation)
		return protocol.Thread{}, nil, fmt.Errorf("resuming codex thread %q: %w", sessionID, ErrSessionClosing)
	}
	mcpAfterVersion := a.currentMCPStatusVersion()
	response, err := a.client.ThreadResume(ctx, protocol.ThreadResumeParams{
		ThreadID:              sessionID,
		Cwd:                   &workspace.CWD,
		Config:                config,
		DeveloperInstructions: instructions,
	})
	if err != nil {
		a.sessions.abandonOpen(sessionID, generation)
		return protocol.Thread{}, nil, mapCodexSessionOpenError(
			sessionID,
			fmt.Errorf("resuming codex thread %q: %w", sessionID, err),
		)
	}
	if response.Thread.ID != sessionID {
		return protocol.Thread{}, nil, a.cleanupFailedSubscribedOpen(ctx, sessionID, generation, fmt.Errorf("resumed thread identity %q does not match %q", response.Thread.ID, sessionID))
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
		if readResponse.Thread.ID != sessionID {
			return protocol.Thread{}, nil, a.cleanupFailedSubscribedOpen(ctx, sessionID, generation, fmt.Errorf("read thread identity %q does not match %q", readResponse.Thread.ID, sessionID))
		}
		thread = readResponse.Thread
	}
	configuration, err := a.configurationForSession(ctx, response.Model, response.ReasoningEffort)
	if err != nil {
		return protocol.Thread{}, nil, a.cleanupFailedSubscribedOpen(ctx, sessionID, generation, err)
	}
	configuration.observedServiceTier = response.ServiceTier
	state, installed := a.sessions.installWorkspace(
		sessionID,
		workspace,
		generation,
		configuration,
		a.currentTerminalOutputMode(),
	)
	if !installed {
		return protocol.Thread{}, nil, a.closeStaleOpen(ctx, sessionID, generation)
	}
	a.publishKnownMCPStartupFailures(state, mcpNames, mcpAfterVersion)
	return thread, state, nil
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

// ListSessions 未在 runtime 子变更实现列表 mapper。
func (a *Agent) ListSessions(context.Context, acp.ListSessionsRequest) (acp.ListSessionsResponse, error) {
	return acp.ListSessionsResponse{}, acp.NewMethodNotFound(acp.AgentMethodSessionList)
}

package claude

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/JieWaZi/acp-go/pkg/claude/protocol"
	acp "github.com/coder/acp-go-sdk"
)

const (
	// defaultTurnQueueCapacity 限制每个 Session 尚未执行的 prompt 数量。
	defaultTurnQueueCapacity = 64
	// cancelDrainGrace 限制中断后等待 CLI turn 尾帧的时间。
	cancelDrainGrace = 3 * time.Second
	// sessionInitializeTimeout 限制进程双通道握手时间。
	sessionInitializeTimeout = 30 * time.Second
)

var (
	// ErrClaudeSessionClosed 表示 Session 已永久结束，调用方需要创建新 Session。
	ErrClaudeSessionClosed = errors.New("Claude session is closed")
	// ErrClaudeTurnQueueFull 表示客户端提交速度超过有界 FIFO 容量。
	ErrClaudeTurnQueueFull = errors.New("Claude turn queue is full")
	// ErrClaudeTurnEndedWithoutResult 表示 CLI 进入空闲但没有发送 turn result。
	ErrClaudeTurnEndedWithoutResult = errors.New("Claude turn ended without result")
)

// openSessionRequest 保存 new/load/resume 共用的 Session 定义参数。
type openSessionRequest struct {
	// SessionID 是待创建或恢复的持久标识。
	SessionID string
	// CWD 是 Session 工作目录。
	CWD string
	// Resume 表示从已有记录恢复。
	Resume bool
	// AdditionalDirectories 是额外工作区目录。
	AdditionalDirectories []string
	// MCPServers 是客户端提供的 MCP 配置。
	MCPServers []acp.McpServer
}

// turnResult 保存一个 prompt 的唯一响应或错误。
type turnResult struct {
	// response 是成功的 ACP prompt 结果。
	response acp.PromptResponse
	// err 是无法表示为 stop reason 的运行时错误。
	err error
}

// streamedContentBlock 保存已经实时发送、等待与聚合 assistant 帧核对的内容块。
type streamedContentBlock struct {
	// index 是流式事件中的原始内容块位置。
	index int
	// kind 区分 text 与 thinking。
	kind string
	// text 是该块已经发送的累计正文。
	text string
}

// claudeTurn 保存 FIFO 中一个用户 prompt 的身份、输出去重和完成信号。
type claudeTurn struct {
	// id 是用户消息 UUID，并用于匹配 CLI echo。
	id string
	// epoch 是 turn 入队时的 Session cancel 世代。
	epoch uint64
	// message 是待写入 CLI 的完整用户输入。
	message protocol.UserInputMessage
	// result 向前台发布一次 PromptResponse。
	result chan turnResult
	// drained 在 CLI result/idle 已消费或 Session 失败时关闭。
	drained chan struct{}
	// settleOnce 保证前台结果只完成一次。
	settleOnce sync.Once
	// drainOnce 保证 worker 只被解除一次。
	drainOnce sync.Once
	// mu 保护取消与流式去重状态。
	mu sync.Mutex
	// cancelled 表示 turn 已被 cancel 请求覆盖。
	cancelled bool
	// gotResult 表示已收到明确 result 帧。
	gotResult bool
	// streamedBlocks 按父工具调用隔离并按文档顺序保存已发送内容块。
	streamedBlocks map[string][]streamedContentBlock
	// steeringIDs 保存注入当前 turn 的 priority 消息标识。
	steeringIDs map[string]struct{}
	// steered 表示本轮曾注入 priority 消息，最终在 idle 边界完成。
	steered bool
	// steeredResult 保存 steering 周期内最后一个可用结果。
	steeredResult *acp.PromptResponse
	// contextUsage 保存当前顶层 assistant 消息的累计上下文用量快照。
	contextUsage protocol.Usage
	// contextUsageSet 表示已经观察到当前 assistant 的用量字段。
	contextUsageSet bool
	// contextUsagePublished 保存最近一次成功发布的上下文 token 总量。
	contextUsagePublished int64
	// contextUsagePublishedSet 区分尚未发布与已发布零用量。
	contextUsagePublishedSet bool
	// assistantModel 保存当前顶层 assistant 消息实际使用的模型标识。
	assistantModel string
	// assistantError 保存固定 Agent SDK 声明的最近一次顶层 Provider 错误类别。
	assistantError string
	// assistantErrorMessage 保存错误消息正文，供缺少结果诊断的终态展示。
	assistantErrorMessage string
}

// claudeSession 保存一个 ACP Session 独占的 CLI、transport、FIFO 与事件状态。
type claudeSession struct {
	// forkMu 让 Prompt、steering 与原生历史复制互斥，避免分叉读取未结束的回合。
	forkMu sync.RWMutex
	// agent 提供 ACP update 与 permission connection。
	agent *Agent
	// id 是 ACP 与 Claude 共用的 Session ID。
	id string
	// cwd 是进程工作目录。
	cwd string
	// fingerprint 标识影响进程定义的创建参数。
	fingerprint string
	// ctx 在 Session 永久关闭时取消。
	ctx context.Context
	// cancel 终止 Session 后台任务。
	cancel context.CancelFunc
	// process 是当前 Session 独占的 CLI 进程。
	process *claudeProcess
	// transport 管理 CLI JSONL/control 读写。
	transport *claudeTransport
	// turns 是有界 prompt FIFO。
	turns chan *claudeTurn
	// queueMu 线性化 prompt 入队与 cancel 清空队列的边界。
	queueMu sync.Mutex
	// workerDone 在 FIFO worker 退出后关闭。
	workerDone chan struct{}
	// mu 保护活动 turn、关闭状态、世代和事件缓存。
	mu sync.Mutex
	// active 是已经写入 CLI 且尚未 drain 的 turn。
	active *claudeTurn
	// cancelEpoch 每次 cancel 递增，使并发旧请求失效。
	cancelEpoch uint64
	// owedTrailingIdles 记录已收到 result、但尚未消费的无身份尾随 idle 数量。
	owedTrailingIdles int
	// closed 表示 Session 不再接受任何输入。
	closed bool
	// fatalErr 是 Session 永久结束的首次原因。
	fatalErr error
	// systemInit 保存 CLI 报告的运行时真值。
	systemInit protocol.SystemInitMessage
	// initialization 保存 control initialize 返回的可选配置。
	initialization protocol.InitializeControlResponse
	// configuration 保存当前模型、effort、fast 和权限模式。
	configuration sessionConfiguration
	// contextWindowSize 是当前模型用于 ACP usage_update 的上下文窗口。
	contextWindowSize int64
	// contextWindowAuthoritative 表示窗口已经由 result.modelUsage 确认。
	contextWindowAuthoritative bool
	// contextWindowModel 是当前窗口状态对应的模型标识。
	contextWindowModel string
	// configMu 串行化会改变 CLI 配置的 control request。
	configMu sync.Mutex
	// tools 保存当前 Session 已发布工具调用。
	tools map[string]*toolState
	// tasks 保存当前 Session 的计划任务快照。
	tasks map[string]taskState
	// closeOnce 保证 transport 与进程只释放一次。
	closeOnce sync.Once
	// closeErr 保存首次关闭结果。
	closeErr error
}

// claudeSessionStore 并发安全地保存已完成握手的 Session。
type claudeSessionStore struct {
	// mu 保护 sessions。
	mu sync.RWMutex
	// sessions 按持久 Session ID 保存状态。
	sessions map[string]*claudeSession
}

// newClaudeSessionStore 创建空 Session store。
func newClaudeSessionStore() *claudeSessionStore {
	return &claudeSessionStore{sessions: make(map[string]*claudeSession)}
}

// get 返回当前安装的 Session。
func (s *claudeSessionStore) get(id string) (*claudeSession, bool) {
	s.mu.RLock()
	session, ok := s.sessions[id]
	s.mu.RUnlock()
	return session, ok
}

// install 替换同 ID 的 Session，并返回旧状态供锁外关闭。
func (s *claudeSessionStore) install(session *claudeSession) *claudeSession {
	s.mu.Lock()
	previous := s.sessions[session.id]
	s.sessions[session.id] = session
	s.mu.Unlock()
	return previous
}

// remove 仅移除当前 ID 的 Session。
func (s *claudeSessionStore) remove(id string) (*claudeSession, bool) {
	s.mu.Lock()
	session, ok := s.sessions[id]
	if ok {
		delete(s.sessions, id)
	}
	s.mu.Unlock()
	return session, ok
}

// removeExact 仅在指针仍是当前安装值时移除，避免失败清理删除并发替换的新 Session。
func (s *claudeSessionStore) removeExact(session *claudeSession) bool {
	if session == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sessions[session.id] != session {
		return false
	}
	delete(s.sessions, session.id)
	return true
}

// removeAll 原子移除并返回全部 Session 快照。
func (s *claudeSessionStore) removeAll() []*claudeSession {
	s.mu.Lock()
	result := make([]*claudeSession, 0, len(s.sessions))
	for id, session := range s.sessions {
		result = append(result, session)
		delete(s.sessions, id)
	}
	s.mu.Unlock()
	return result
}

// sessionOpenLock 只串行同一 Session ID，并在最后一个等待者离开后释放索引。
type sessionOpenLock struct {
	// mu 串行同一 Session 的启动和替换。
	mu sync.Mutex
	// users 统计等待或持有该锁的调用。
	users int
}

// lockSessionOpen 获取指定 Session 的打开锁，并返回负责释放索引的函数。
func (a *Agent) lockSessionOpen(id string) func() {
	a.openMu.Lock()
	if a.openLocks == nil {
		a.openLocks = make(map[string]*sessionOpenLock)
	}
	lock := a.openLocks[id]
	if lock == nil {
		lock = &sessionOpenLock{}
		a.openLocks[id] = lock
	}
	lock.users++
	a.openMu.Unlock()
	lock.mu.Lock()
	return func() {
		lock.mu.Unlock()
		a.openMu.Lock()
		lock.users--
		if lock.users == 0 {
			delete(a.openLocks, id)
		}
		a.openMu.Unlock()
	}
}

// beginSessionOpen 建立关闭栅栏，并统计在途的会话握手。
func (a *Agent) beginSessionOpen() (func(), error) {
	a.openMu.Lock()
	defer a.openMu.Unlock()
	if a.closing {
		return nil, errors.New("Claude agent closed")
	}
	if a.activeOpens == 0 {
		a.opensDone = make(chan struct{})
	}
	a.activeOpens++
	return func() {
		a.openMu.Lock()
		a.activeOpens--
		if a.activeOpens == 0 {
			close(a.opensDone)
			a.opensDone = nil
		}
		a.openMu.Unlock()
	}, nil
}

// openSession 校验参数、完成进程握手，并在成功后原子发布 Session。
func (a *Agent) openSession(ctx context.Context, request openSessionRequest) (*claudeSession, error) {
	endOpen, err := a.beginSessionOpen()
	if err != nil {
		return nil, err
	}
	defer endOpen()
	options, err := prepareLaunchOptions(
		request.CWD,
		request.SessionID,
		request.Resume,
		a.allowBypassPermissions,
		request.AdditionalDirectories,
		request.MCPServers,
	)
	if err != nil {
		return nil, fmt.Errorf("opening Claude session: %w", err)
	}
	fingerprint := launchFingerprint(options)

	// 同一 Session ID 的检查、关闭和新安装必须形成一个全序，否则迟到握手可能覆盖更新参数。
	unlock := a.lockSessionOpen(request.SessionID)
	defer unlock()
	if existing, ok := a.sessions.get(request.SessionID); ok && existing.fingerprint == fingerprint && existing.isOpen() {
		return existing, nil
	} else if ok {
		a.sessions.remove(request.SessionID)
		closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), sessionCloseTimeout)
		_ = existing.close(closeCtx)
		cancel()
	}

	sessionCtx, sessionCancel := context.WithCancel(a.runtimeCtx)
	arguments := append([]string{}, a.prefixArgs...)
	arguments = append(arguments, claudeLaunchArgs(options)...)
	process, err := startClaudeProcess(a.runtimeCtx, a.executable.Path, processOptions{
		CWD:    options.CWD,
		Args:   arguments,
		Env:    claudeProcessEnv(a.environment, nil),
		Logger: a.logger,
	})
	if err != nil {
		sessionCancel()
		return nil, fmt.Errorf("opening Claude session: %w", err)
	}
	session := &claudeSession{
		agent: a, id: request.SessionID, cwd: request.CWD, fingerprint: fingerprint,
		ctx: sessionCtx, cancel: sessionCancel, process: process,
		turns: make(chan *claudeTurn, defaultTurnQueueCapacity), workerDone: make(chan struct{}),
		tools: make(map[string]*toolState), tasks: make(map[string]taskState),
	}
	session.transport = newClaudeTransport(
		sessionCtx, process.Stdout(), process.Stdin(), process.Stdout(),
		transportOptions{
			Logger: a.logger, MessageHandler: session.handleMessage,
			ControlHandler: session.handleControlRequest, EOFError: process.FinalError,
		},
	)
	go session.watchTransport()
	if err := session.initialize(ctx, options); err != nil {
		closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), sessionCloseTimeout)
		_ = session.close(closeCtx)
		cancel()
		return nil, fmt.Errorf("opening Claude session: %w", err)
	}
	a.openMu.Lock()
	if a.closing {
		a.openMu.Unlock()
		closeCtx, cancel := context.WithTimeout(context.Background(), sessionCloseTimeout)
		_ = session.close(closeCtx)
		cancel()
		return nil, errors.New("Claude agent closed")
	}
	previous := a.sessions.install(session)
	a.openMu.Unlock()
	go session.runTurns()
	if previous != nil && previous != session {
		closeCtx, cancel := context.WithTimeout(context.Background(), sessionCloseTimeout)
		_ = previous.close(closeCtx)
		cancel()
	}
	return session, nil
}

// initialize 等待官方 SDK 同义的 control 初始化结果；system/init 会在首个 prompt 开始后异步校准运行时真值。
func (s *claudeSession) initialize(ctx context.Context, options launchOptions) error {
	initializeCtx, cancel := context.WithTimeout(ctx, sessionInitializeTimeout)
	defer cancel()
	var response protocol.InitializeControlResponse
	if err := s.transport.Call(initializeCtx, protocol.InitializeControlRequest{Subtype: protocol.ControlInitialize}, &response); err != nil {
		return fmt.Errorf("initializing Claude control channel: %w", err)
	}
	s.mu.Lock()
	s.initialization = response
	s.configuration = newSessionConfiguration(
		response,
		options.PermissionMode,
		s.agent.allowBypassPermissions,
	)
	s.seedContextWindowLocked(s.configuration.model)
	s.mu.Unlock()
	return nil
}

// prompt 入队一个 turn；取消请求上下文时同时触发 Session cancel，避免遗留后台 prompt。
func (s *claudeSession) prompt(ctx context.Context, message protocol.UserInputMessage) (acp.PromptResponse, error) {
	turn, err := s.enqueueTurn(ctx, message)
	if err != nil {
		return acp.PromptResponse{}, err
	}

	select {
	case result := <-turn.result:
		return result.response, result.err
	case <-ctx.Done():
		_ = s.cancelTurns(context.WithoutCancel(ctx))
		return acp.PromptResponse{}, ctx.Err()
	case <-s.ctx.Done():
		return acp.PromptResponse{}, s.sessionError()
	}
}

// enqueueTurn 在 cancel 队列边界内创建并提交 turn，确保 cancel 返回后新 prompt 不会被旧清空操作误取消。
func (s *claudeSession) enqueueTurn(ctx context.Context, message protocol.UserInputMessage) (*claudeTurn, error) {
	s.queueMu.Lock()
	defer s.queueMu.Unlock()
	s.mu.Lock()
	if s.closed {
		err := s.fatalErr
		if err == nil {
			err = ErrClaudeSessionClosed
		}
		s.mu.Unlock()
		return nil, err
	}
	turn := &claudeTurn{
		id: message.UUID, epoch: s.cancelEpoch, message: message,
		result: make(chan turnResult, 1), drained: make(chan struct{}),
		streamedBlocks: make(map[string][]streamedContentBlock),
		steeringIDs:    make(map[string]struct{}),
	}
	s.mu.Unlock()

	select {
	case s.turns <- turn:
		return turn, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-s.ctx.Done():
		return nil, s.sessionError()
	default:
		return nil, ErrClaudeTurnQueueFull
	}
}

// runTurns 是每个 Session 唯一的 prompt FIFO 写入者。
func (s *claudeSession) runTurns() {
	defer close(s.workerDone)
	for {
		select {
		case <-s.ctx.Done():
			return
		case turn := <-s.turns:
			s.mu.Lock()
			if s.closed {
				s.mu.Unlock()
				turn.settle(acp.PromptResponse{}, s.sessionError())
				turn.drain()
				continue
			}
			// cancel 世代在入队后变化时，该 turn 不能再写入进程。
			if turn.epoch != s.cancelEpoch {
				s.mu.Unlock()
				turn.settle(acp.PromptResponse{StopReason: acp.StopReasonCancelled}, nil)
				turn.drain()
				continue
			}
			s.active = turn
			s.mu.Unlock()
			if err := s.transport.Write(s.ctx, turn.message); err != nil {
				turn.settle(acp.PromptResponse{}, err)
				turn.drain()
				s.fail(err)
				go s.closeWithTimeout()
				return
			}
			select {
			case <-turn.drained:
			case <-s.ctx.Done():
				return
			}
			s.mu.Lock()
			if s.active == turn {
				s.active = nil
			}
			s.mu.Unlock()
		}
	}
}

// cancelTurns 提升 cancel 世代、立即取消排队 turn，并中断活动 turn。
func (s *claudeSession) cancelTurns(ctx context.Context) error {
	s.queueMu.Lock()
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		s.queueMu.Unlock()
		return nil
	}
	s.cancelEpoch++
	active := s.active
	if active != nil {
		active.mu.Lock()
		active.cancelled = true
		active.mu.Unlock()
		active.settle(acp.PromptResponse{StopReason: acp.StopReasonCancelled}, nil)
	}
	s.mu.Unlock()

	// 当前 channel 中尚未被 worker 取出的 turn 可以立即完成；并发入队的旧世代由 worker 二次校验。
	for {
		select {
		case turn := <-s.turns:
			turn.settle(acp.PromptResponse{StopReason: acp.StopReasonCancelled}, nil)
			turn.drain()
		default:
			goto drained
		}
	}

drained:
	s.queueMu.Unlock()
	if active == nil {
		return nil
	}
	var response struct {
		// Cancelled 是 CLI 确认移除的排队消息标识。
		Cancelled []string `json:"cancelled,omitempty"`
	}
	// interrupt 自身也必须有硬期限；外层通知没有 deadline 时不能无限等待 CLI 响应。
	interruptCtx, cancelInterrupt := context.WithTimeout(ctx, cancelDrainGrace)
	err := s.transport.Call(interruptCtx, protocol.InterruptControlRequest{
		Subtype: protocol.ControlInterrupt, CancelQueued: true,
	}, &response)
	cancelInterrupt()
	if err != nil {
		// 固定 upstream 在 query stream 已关闭后把 cancel 视为幂等成功。
		// CloseSession 可能在 interrupt 等待期间完成同一关闭动作，此时不把
		// fire-and-forget 的 session/cancel 误报为协议错误。
		if errors.Is(err, ErrClaudeTransportClosed) && !s.isOpen() {
			return nil
		}
		s.fail(fmt.Errorf("interrupting Claude turn: %w", err))
		go s.closeWithTimeout()
		return err
	}

	// 正常 CLI 会发送 result/idle 解除 drain；超过宽限仍没有尾帧时关闭 Session，避免后续 turn 串线。
	go func(turn *claudeTurn) {
		timer := time.NewTimer(cancelDrainGrace)
		defer timer.Stop()
		select {
		case <-turn.drained:
		case <-timer.C:
			s.fail(errors.New("Claude turn did not drain after interrupt"))
			_ = s.closeWithTimeout()
		case <-s.ctx.Done():
		}
	}(active)
	return nil
}

// isOpen 返回 Session 是否仍可接受请求。
func (s *claudeSession) isOpen() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.closed
}

// closeWithTimeout 为后台失败路径提供独立且有界的进程回收窗口。
func (s *claudeSession) closeWithTimeout() error {
	ctx, cancel := context.WithTimeout(context.Background(), sessionCloseTimeout)
	defer cancel()
	return s.close(ctx)
}

// settle 发布一次 prompt 结果，迟到的 result/idle 不会覆盖既有完成值。
func (t *claudeTurn) settle(response acp.PromptResponse, err error) {
	t.settleOnce.Do(func() { t.result <- turnResult{response: response, err: err} })
}

// drain 解除 FIFO worker，使下一 turn 只能在当前 CLI 周期确实结束后开始。
func (t *claudeTurn) drain() {
	t.drainOnce.Do(func() { close(t.drained) })
}

// watchTransport 把进程或 transport 的永久结束传播到 Session 所有等待者。
func (s *claudeSession) watchTransport() {
	<-s.transport.Done()
	s.fail(s.transport.Err())
	go s.closeWithTimeout()
}

// fail 发布 Session 首次 fatal，并解除活动与排队 turn。
func (s *claudeSession) fail(err error) {
	if err == nil {
		err = ErrClaudeSessionClosed
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	s.fatalErr = err
	active := s.active
	s.mu.Unlock()
	s.cancel()
	if active != nil {
		active.settle(acp.PromptResponse{}, err)
		active.drain()
	}
	for {
		select {
		case turn := <-s.turns:
			turn.settle(acp.PromptResponse{}, err)
			turn.drain()
		default:
			return
		}
	}
}

// sessionError 返回 Session 对外稳定的终止原因。
func (s *claudeSession) sessionError() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fatalErr != nil {
		return s.fatalErr
	}
	return ErrClaudeSessionClosed
}

// close 先解除所有等待，再关闭 transport、stdin 和子进程。
func (s *claudeSession) close(ctx context.Context) error {
	s.closeOnce.Do(func() {
		s.fail(ErrClaudeSessionClosed)
		// 每个关闭入口都使用独立硬期限，避免无 deadline 的 ACP 请求永久等待子进程退出。
		cleanupCtx, cancelCleanup := context.WithTimeout(ctx, sessionCloseTimeout)
		defer cancelCleanup()
		transportErr := s.transport.Close()
		processErr := s.process.Close(cleanupCtx)
		if processErr != nil && !errors.Is(processErr, ErrClaudeProcessExited) {
			s.closeErr = processErr
		} else {
			s.closeErr = transportErr
		}
	})
	return s.closeErr
}

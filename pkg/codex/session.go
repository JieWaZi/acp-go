package codex

import (
	"errors"
	"fmt"
	"sync"

	"github.com/JieWaZi/acp-go/pkg/codex/protocol"
)

var (
	// ErrSessionNotFound 表示 ACP SessionID 没有当前本地 generation。
	ErrSessionNotFound = errors.New("codex session not found")
	// ErrSessionClosing 表示 session 正处于 close fence 或 open 结果已经过期。
	ErrSessionClosing = errors.New("codex session is closing")
	// ErrPromptActive 表示同 session 已有 pending/active turn，拒绝产生 rival turn。
	ErrPromptActive = errors.New("codex session already has an active prompt")
)

// sessionState 保存一个已安装 ACP session 的最小 runtime 状态。
type sessionState struct {
	// id 同时作为 ACP SessionID 与 Codex ThreadID。
	id string
	// cwd 是创建或恢复请求指定的工作目录。
	cwd string
	// generation 防止旧 open、notification 和 control 覆盖重开状态。
	generation uint64
	// mu 保护 activePrompt。
	mu sync.Mutex
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
	s.mu.Lock()
	defer s.mu.Unlock()
	openGeneration, opening := s.opening[sessionID]
	if s.closing[sessionID] > 0 || s.generations[sessionID] != generation || !opening || openGeneration != generation {
		return nil, false
	}
	state := &sessionState{
		id:                 sessionID,
		cwd:                cwd,
		generation:         generation,
		configuration:      configuration,
		terminalOutputMode: terminalMode,
	}
	s.sessions[sessionID] = state
	delete(s.opening, sessionID)
	return state, true
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

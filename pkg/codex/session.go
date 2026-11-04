package codex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/JieWaZi/acp-go/pkg/acpmeta"
	acp "github.com/JieWaZi/acp-go/sdk"
	"sync"

	"github.com/JieWaZi/acp-go/pkg/codex/protocol"
)

var (
	// ErrSessionNotFound indicates no installed thread for the ACP session ID.
	ErrSessionNotFound = errors.New("codex session not found")
	// ErrSessionClosing indicates an active close fence or an obsolete open attempt.
	ErrSessionClosing = errors.New("codex session is closing")
	// ErrSessionOpening indicates another request owns this session's open attempt.
	ErrSessionOpening = errors.New("codex session is opening")
	// ErrSessionAlreadyOpen indicates that a native thread already has an installed owner.
	ErrSessionAlreadyOpen = errors.New("codex session is already open")
	// ErrPromptActive rejects concurrent input on an occupied native lane.
	ErrPromptActive = errors.New("codex session already has an active prompt")
)

// sessionState owns a native thread subscription and its current invocation.
type sessionState struct {
	// id is both the native thread ID and the ACP session ID.
	id string
	// cwd is the frozen native working directory for this subscription.
	cwd string
	// additionalDirectories holds additional project roots selected at open time.
	additionalDirectories []string
	// generation fences stale opens, notifications, and callbacks.
	generation uint64
	// mu protects mutable thread and invocation state.
	mu sync.Mutex
	// usageTotal is the last trustworthy cumulative native usage checkpoint.
	usageTotal *turnUsage
	// activeTurn is the sole invocation admitted to this thread.
	activeTurn *nativeTurn
	// configuration owns native settings, requested overrides, and observed values.
	configuration *sessionConfiguration
	// terminalOutputMode snapshots the ACP output capability at session open.
	terminalOutputMode terminalOutputMode
	// replayingHistory reserves the native thread until ACP history delivery finishes.
	replayingHistory bool
	// turnAdmissionClosed fences calls holding a retiring state pointer.
	turnAdmissionClosed bool
	// mcpServers contains MCP names requested on this thread.
	mcpServers map[string]struct{}
	// mcpStartupReported deduplicates startup reports within the subscription.
	mcpStartupReported map[string]protocol.MCPServerStatusUpdatedNotificationStatus
	// mcpStartupAfterVersion rejects status reports older than this open attempt.
	mcpStartupAfterVersion uint64
}

// sessionStore linearizes native thread admission, installation and release.
type sessionStore struct {
	// mu protects mutable thread and invocation state.
	mu sync.Mutex
	// sessions holds installed native thread owners.
	sessions map[string]*sessionState
	// generations records the monotonically increasing fence for each thread.
	generations map[string]uint64
	// opening records the generation reserving a pending native open.
	opening map[string]uint64
	// closing counts active release operations fencing a thread.
	closing map[string]int
}

// newSessionStore creates an empty thread registry.
func newSessionStore() *sessionStore {
	return &sessionStore{
		sessions:    make(map[string]*sessionState),
		generations: make(map[string]uint64),
		opening:     make(map[string]uint64),
		closing:     make(map[string]int),
	}
}

// beginOpen reserves a thread only when no current owner can be replaced.
func (s *sessionStore) beginOpen(sessionID string) (uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing[sessionID] > 0 {
		return 0, fmt.Errorf("opening session %q: %w", sessionID, ErrSessionClosing)
	}
	generation := s.generations[sessionID]
	if s.sessions[sessionID] != nil {
		return 0, fmt.Errorf("opening session %q: %w", sessionID, ErrSessionAlreadyOpen)
	}
	if current, opening := s.opening[sessionID]; opening && current == generation {
		return 0, fmt.Errorf("opening session %q: %w", sessionID, ErrSessionOpening)
	}
	s.opening[sessionID] = generation
	return generation, nil
}

// install commits a session with the given working directory.
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
		false,
	)
}

// installWorkspace publishes a subscription only for the still-current open attempt.
func (s *sessionStore) installWorkspace(
	sessionID string,
	workspace codexWorkspace,
	generation uint64,
	configuration *sessionConfiguration,
	terminalMode terminalOutputMode,
	replayingHistory bool,
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
		replayingHistory:   replayingHistory,
	}
	s.sessions[sessionID] = state
	delete(s.opening, sessionID)
	return state, true
}

// workspace returns a copy of the frozen project roots.
func (s *sessionState) workspace() codexWorkspace {
	s.mu.Lock()
	defer s.mu.Unlock()
	return codexWorkspace{
		CWD:                   s.cwd,
		AdditionalDirectories: append([]string{}, s.additionalDirectories...),
	}
}

// abandonOpen releases the matching failed open attempt.
func (s *sessionStore) abandonOpen(sessionID string, generation uint64) {
	s.mu.Lock()
	if openGeneration, opening := s.opening[sessionID]; opening && openGeneration == generation {
		delete(s.opening, sessionID)
	}
	s.mu.Unlock()
}

// openCanProceed revalidates the native open reservation after preparation.
func (s *sessionStore) openCanProceed(sessionID string, generation uint64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	openGeneration, opening := s.opening[sessionID]
	return opening && openGeneration == generation && s.closing[sessionID] == 0 && s.generations[sessionID] == generation
}

// beginStaleCleanup grants release authority only to the matching open attempt.
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

// beginClose fences an explicitly addressed ACP session.
func (s *sessionStore) beginClose(sessionID string) (uint64, *sessionState) {
	generation, state, _ := s.beginCloseOwned(sessionID, nil)
	return generation, state
}

// beginCloseOwned fences cleanup only while the expected state still owns the thread.
// A nil expected state indicates an explicit ACP close addressed by session ID.
func (s *sessionStore) beginCloseOwned(sessionID string, expected *sessionState) (uint64, *sessionState, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if expected != nil && s.sessions[sessionID] != expected {
		return 0, nil, false
	}
	s.generations[sessionID]++
	generation := s.generations[sessionID]
	s.closing[sessionID]++
	state := s.sessions[sessionID]
	delete(s.sessions, sessionID)
	return generation, state, true
}

// endClose releases one operation’s close fence.
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

// get returns the installed native thread owner.
func (s *sessionStore) get(sessionID string) (*sessionState, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, ok := s.sessions[sessionID]
	return state, ok
}

// snapshot copies the installed thread pointers for later generation checks.
func (s *sessionStore) snapshot() []*sessionState {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([]*sessionState, 0, len(s.sessions))
	for _, state := range s.sessions {
		result = append(result, state)
	}
	return result
}

// isCurrent checks both the generation and pointer identity.
func (s *sessionStore) isCurrent(state *sessionState) bool {
	if state == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closing[state.id] == 0 && s.generations[state.id] == state.generation && s.sessions[state.id] == state
}

// withCurrent serializes a short mutation with close using store-to-thread lock order.
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

// closeAll retires installed subscriptions and returns the former owners.
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

// NewSession opens and installs a native thread with independent settings evidence.
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
	settings, err := parseOpenSettings(request.Meta)
	if err != nil {

		return acp.NewSessionResponse{}, err
	}
	settings.applyOpenEffort(config)
	mcpAfterVersion := a.currentMCPStatusVersion()
	response, err := a.client.ThreadStart(ctx, protocol.ThreadStartParams{
		Cwd:                   &workspace.CWD,
		Config:                config,
		DeveloperInstructions: instructions,
		Model:                 settings.Model,
		ServiceTier:           settings.ServiceTier,
	})
	if err != nil {
		return acp.NewSessionResponse{}, codexOperationFailure("thread_start", err, nil)
	}
	if response.Thread.ID == "" {
		return acp.NewSessionResponse{}, errors.New("starting codex thread: empty thread id")
	}
	generation, err := a.sessions.beginOpen(response.Thread.ID)
	if err != nil {
		return acp.NewSessionResponse{}, err
	}
	state, err := a.installOpenedSession(ctx, response.Thread.ID, generation, workspace, settings,
		"thread_start", response.Model, response.ReasoningEffort, response.ServiceTier, false)
	if err != nil {
		return acp.NewSessionResponse{}, err
	}
	a.publishKnownMCPStartupFailures(state, mcpNames, mcpAfterVersion)
	modes, options := sessionConfigurationResponse(state)
	return acp.NewSessionResponse{
		SessionId:     acp.SessionId(response.Thread.ID),
		Meta:          sessionSettingsMetadata(state),
		Modes:         modes,
		ConfigOptions: options,
	}, nil
}

// ResumeSession restores a saved native thread without replaying transcript history.
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
	return acp.ResumeSessionResponse{Modes: modes, ConfigOptions: options, Meta: sessionSettingsMetadata(state)}, nil
}

// LoadSession restores a thread and reserves it until ACP history replay finishes.
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
		closeErr := a.closeThread(cleanupCtx, state.id, state)
		return acp.LoadSessionResponse{}, codexOperationFailure("thread_history_replay", err, closeErr)
	}
	state.mu.Lock()
	state.replayingHistory = false
	state.mu.Unlock()
	modes, options := sessionConfigurationResponse(state)
	return acp.LoadSessionResponse{Modes: modes, ConfigOptions: options, Meta: sessionSettingsMetadata(state)}, nil
}

// openExistingSession reserves, restores and validates a native thread.
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
	settings, err := parseOpenSettings(meta)
	if err != nil {
		a.sessions.abandonOpen(sessionID, generation)
		return protocol.Thread{}, nil, err
	}
	settings.applyOpenEffort(config)
	mcpAfterVersion := a.currentMCPStatusVersion()
	response, err := a.client.ThreadResume(ctx, protocol.ThreadResumeParams{
		ThreadID:              sessionID,
		Cwd:                   &workspace.CWD,
		Config:                config,
		DeveloperInstructions: instructions,
		Model:                 settings.Model,
		ServiceTier:           settings.ServiceTier,
	})
	if err != nil {
		a.sessions.abandonOpen(sessionID, generation)
		return protocol.Thread{}, nil, codexOperationFailure("thread_resume", mapCodexSessionOpenError(sessionID, err), nil)
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
			return protocol.Thread{}, nil, a.cleanupFailedSubscribedOpen(ctx, sessionID, generation, codexOperationFailure("thread_read", readErr, nil))
		}
		if readResponse.Thread.ID != sessionID {
			return protocol.Thread{}, nil, a.cleanupFailedSubscribedOpen(ctx, sessionID, generation, fmt.Errorf("read thread identity %q does not match %q", readResponse.Thread.ID, sessionID))
		}
		thread = readResponse.Thread
	}
	state, err := a.installOpenedSession(ctx, sessionID, generation, workspace, settings,
		"thread_resume", response.Model, response.ReasoningEffort, response.ServiceTier, includeHistory)
	if err != nil {
		return protocol.Thread{}, nil, err
	}
	a.publishKnownMCPStartupFailures(state, mcpNames, mcpAfterVersion)
	return thread, state, nil
}

// installOpenedSession commits one native subscription or releases that exact open attempt.
func (a *Agent) installOpenedSession(ctx context.Context, id string, generation uint64, workspace codexWorkspace, settings OpenSettings,
	source, model string, effort *string, tier protocol.OptionalNullable[string], replaying bool,
) (*sessionState, error) {
	configuration, err := a.configurationForSession(ctx, model, effort)
	if err != nil {
		return nil, a.cleanupFailedSubscribedOpen(ctx, id, generation, err)
	}
	configuration.observe(source, model, effort, tier)
	configuration.applyOpenIntent(settings)
	state, installed := a.sessions.installWorkspace(id, workspace, generation, configuration, a.currentTerminalOutputMode(), replaying)
	if !installed {
		return nil, a.closeStaleOpen(ctx, id, generation)
	}
	if source == "thread_start" {
		state.mu.Lock()
		state.usageTotal = &turnUsage{CacheWriteTokens: new(int64)}
		state.mu.Unlock()
	}
	return state, nil
}

// newAppServerCleanupContext provides a bounded release context independent of caller cancellation.
func newAppServerCleanupContext(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(parent), appServerCleanupTimeout)
}

// cleanupFailedSubscribedOpen releases a subscribed but uninstalled open attempt.
func (a *Agent) cleanupFailedSubscribedOpen(ctx context.Context, sessionID string, generation uint64, cause error) error {
	if a.sessions.beginStaleCleanup(sessionID, generation) {
		defer a.sessions.endClose(sessionID)
		cleanupCtx, cancelCleanup := newAppServerCleanupContext(ctx)
		defer cancelCleanup()
		if err := a.client.ThreadUnsubscribe(cleanupCtx, sessionID); err != nil {
			return codexOperationFailure("thread_open", cause, err)
		}
	}
	return codexOperationFailure("thread_open", cause, nil)
}

// closeStaleOpen releases only an obsolete attempt that still owns its subscription.
func (a *Agent) closeStaleOpen(ctx context.Context, sessionID string, generation uint64) error {
	if a.sessions.beginStaleCleanup(sessionID, generation) {
		defer a.sessions.endClose(sessionID)
		cleanupCtx, cancelCleanup := newAppServerCleanupContext(ctx)
		defer cancelCleanup()
		if err := a.client.ThreadUnsubscribe(cleanupCtx, sessionID); err != nil {
			return codexOperationFailure("thread_open", ErrSessionClosing, err)
		}
	}
	return fmt.Errorf("opening session %q: %w", sessionID, ErrSessionClosing)
}

// CloseSession fences the native thread, cancels its invocations, and unsubscribes.
func (a *Agent) CloseSession(ctx context.Context, request acp.CloseSessionRequest) (acp.CloseSessionResponse, error) {
	return acp.CloseSessionResponse{}, a.closeThread(ctx, string(request.SessionId), nil)
}

// closeThread releases only the identified thread owner.
func (a *Agent) closeThread(ctx context.Context, sessionID string, expected *sessionState) error {
	_, state, owned := a.sessions.beginCloseOwned(sessionID, expected)
	if !owned {
		return nil
	}
	defer a.sessions.endClose(sessionID)
	a.steering.CloseSession(sessionID, ErrSessionClosing)
	var cleanupErr error
	prompts := a.closeSessionTurns(state)
	for _, prompt := range prompts {
		turnID := prompt.requestCancel()
		prompt.markForegroundFinished()
		a.releaseTurn(state, prompt)
		if turnID != "" {
			a.client.MarkTurnStale(sessionID, turnID)
			a.requestTurnInterrupt(state, prompt, turnID, false)
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
		unsubscribeErr := codexOperationFailure("thread_unsubscribe", err, nil)
		return codexOperationFailure("thread_close", unsubscribeErr, cleanupErr)
	}
	return cleanupErr
}

const (
	// ThreadArchiveMethod archives an inactive native thread.
	ThreadArchiveMethod = "_codex/thread/archive"
	// ThreadUnarchiveMethod restores an archived native thread.
	ThreadUnarchiveMethod = "_codex/thread/unarchive"
)

// ThreadArchiveRequest identifies one persisted Codex thread.
type ThreadArchiveRequest struct {
	// ThreadID is the native Codex identity to archive or restore.
	ThreadID string `json:"thread_id"`
}

// ThreadArchiveResponse confirms the native identity and reported archive operation.
type ThreadArchiveResponse struct {
	// ThreadID is the native thread changed by the request.
	ThreadID string `json:"thread_id"`
	// Archived reports the state requested and acknowledged by Codex.
	Archived bool `json:"archived"`
}

// archiveThread changes persisted archive state while the native thread is inactive.
func (a *Agent) archiveThread(ctx context.Context, raw json.RawMessage, archive bool) (ThreadArchiveResponse, error) {
	if err := a.requireInitialized(); err != nil {
		return ThreadArchiveResponse{}, err
	}
	var request ThreadArchiveRequest
	if err := json.Unmarshal(raw, &request); err != nil || request.ThreadID == "" {
		return ThreadArchiveResponse{}, acp.NewInvalidParams("thread_id is required")
	}
	generation, err := a.sessions.beginOpen(request.ThreadID)
	if err != nil {
		return ThreadArchiveResponse{}, acp.NewInvalidRequest(err.Error())
	}
	defer a.sessions.abandonOpen(request.ThreadID, generation)
	if archive {
		if err := a.client.ThreadArchive(ctx, request.ThreadID); err != nil {
			return ThreadArchiveResponse{}, codexRequestFailure("thread_archive", err)
		}
	} else {
		response, err := a.client.ThreadUnarchive(ctx, request.ThreadID)
		if err != nil {
			return ThreadArchiveResponse{}, codexRequestFailure("thread_unarchive", err)
		}
		if response.Thread.ID != request.ThreadID {
			return ThreadArchiveResponse{}, codexRequestFailure("thread_unarchive", errors.New("Codex returned a different unarchived thread"))
		}
	}
	return ThreadArchiveResponse{ThreadID: request.ThreadID, Archived: archive}, nil
}

// ListSessions reports that this adapter has no ACP history-list mapping.
func (a *Agent) ListSessions(context.Context, acp.ListSessionsRequest) (acp.ListSessionsResponse, error) {
	return acp.ListSessionsResponse{}, acp.NewMethodNotFound(acp.AgentMethodSessionList)
}

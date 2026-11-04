package claude

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"sync"
	"time"

	agentclient "github.com/JieWaZi/acp-go/pkg/agent_next"
	"github.com/JieWaZi/acp-go/pkg/agent_next/claude/protocol"
	acp "github.com/JieWaZi/acp-go/sdk"
)

type openRequest struct {
	// id is the exact native session UUID that this generation must establish.
	id         acp.SessionId
	cwd        string
	additional []string
	mcp        []acp.McpServer
	meta       map[string]any
	resume     bool
	replay     bool
	// forkSource selects native fork launch. confirm proves the child transcript.
	forkSource string
	confirm    func(context.Context, *session) error
}

// session owns every transition for one physical generation. Native parsing,
// local ingress and gate validation take mu briefly and never wait on I/O.
type session struct {
	client  *Client
	id      acp.SessionId
	launch  launchPlan
	process *agentclient.Process
	wire    *wire
	ledger  *outcomeLedger
	// ctx ends native interaction and submission work, but not receipt delivery.
	ctx       context.Context
	cancel    context.CancelFunc
	closeGate chan struct{}
	mu        sync.Mutex
	changed   chan struct{}
	open      bool
	frozen    bool
	closing   bool
	closed    bool
	fault     error
	// queued contains host reservations in admission order, including the current preparer.
	queued []*input
	// sent contains exact native UUIDs until native consumption or recall.
	sent map[string]*input
	// used prevents reusing user-supplied native UUIDs inside one generation.
	used map[string]struct{}
	// current is the only observed main-thread native execution.
	current *invocation
	// epoch increases when the reader observes a new native execution boundary.
	epoch             uint64
	nativeQueued      int
	background        int
	lastResultIndex   int64
	lastResultSeen    bool
	terminalIDs       map[string]struct{}
	withdrawnInputs   map[string]struct{}
	tools             map[string]*toolRecord
	nativeTasks       map[string]*taskRecord
	taskOwnershipFull bool
	workspaceWrites   map[string]*workspaceWrite
	// tasks counts admitted controls, interaction callbacks and asynchronous cleanup.
	tasks            int
	setting          bool
	init             protocol.SystemInitMessage
	initialization   protocol.InitializeControlResponse
	settings         sessionSettings
	contextReading   bool
	deliveries       []delivery
	observationCount int
	observationBytes int
	observerErr      error
	deliveryStop     bool
	deliveryDone     chan struct{}
	interactions     map[string]context.CancelFunc
	counters         queryCounters
}

func (c *Client) open(ctx context.Context, req openRequest) (*session, error) {
	if !isUUID(string(req.id)) {
		return nil, acp.NewInvalidParams(map[string]any{"message": "invalid Claude session UUID"})
	}
	finish, err := c.beginSessionOperation(req.id)
	if err != nil {
		return nil, err
	}
	defer finish()
	operation, cancel := context.WithTimeout(ctx, startupTimeout)
	stop := context.AfterFunc(c.ctx, cancel)
	defer func() { stop(); cancel() }()

	c.mu.Lock()
	old := c.sessions[req.id]
	c.mu.Unlock()
	plan, err := c.prepareLaunch(req, old)
	if err != nil {
		return nil, err
	}
	if old != nil {
		old.mu.Lock()
		hot := old.admissibleLocked() == nil && old.launch.fingerprint == plan.fingerprint && !req.replay && req.confirm == nil
		if hot {
			old.mu.Unlock()
			return old, nil
		}
		if old.admissibleLocked() == nil && old.busyLocked() {
			old.mu.Unlock()
			return nil, errBusy
		}
		old.frozen = true
		old.mu.Unlock()
		if err := c.release(operation, old); err != nil {
			return nil, err
		}
	}
	process, err := c.process(plan.args, plan.cwd, c.streamEnvironment())
	if err != nil {
		return nil, err
	}
	lifetime, end := context.WithCancel(c.ctx)
	s := &session{client: c, id: req.id, launch: plan, process: process, ctx: lifetime, cancel: end,
		changed: make(chan struct{}), closeGate: make(chan struct{}, 1),
		deliveryDone: make(chan struct{}), sent: make(map[string]*input), used: make(map[string]struct{}),
		terminalIDs: make(map[string]struct{}), withdrawnInputs: make(map[string]struct{}), tools: make(map[string]*toolRecord), nativeTasks: make(map[string]*taskRecord), workspaceWrites: make(map[string]*workspaceWrite),
		interactions: make(map[string]context.CancelFunc), counters: newQueryCounters(req.resume || req.forkSource != ""),
	}
	c.mu.Lock()
	if c.closing || len(c.sessions) >= maxSessions || (len(c.ledgers) >= maxSessions && c.ledgers[req.id] == nil) {
		c.mu.Unlock()
		end()
		if c.ctx.Err() != nil {
			return nil, errClosed
		}
		return nil, errCapacity
	}
	s.ledger = c.ledgers[req.id]
	if s.ledger == nil {
		s.ledger = newOutcomeLedger()
		c.ledgers[req.id] = s.ledger
	}
	s.ledger.producer(true)
	c.sessions[req.id] = s
	c.mu.Unlock()

	err = process.Start(operation)
	if err == nil {
		s.wire = startWire(process, c.options.ControlTimeout)
		s.wire.onWriteFailure = s.failWrite
		go s.deliver()
		go s.wire.read(s.receive, s.disconnected)
		err = s.handshake(operation, !req.resume && req.forkSource == "")
	} else {
		close(s.deliveryDone)
	}
	if err == nil && req.confirm != nil {
		err = req.confirm(operation, s)
	}
	if err == nil && req.replay {
		err = s.replay(operation)
	}

	c.mu.Lock()
	s.mu.Lock()
	if err == nil {
		err = firstError(operation.Err(), ctx.Err())
	}
	if err == nil && c.closing {
		err = errClosed
	}
	err = firstError(s.fault, err)
	if err == nil {
		select {
		case <-s.process.Done():
			err = errTransport
		default:
		}
	}
	if err == nil {
		s.open = true
		s.announceCommandsLocked()
	}
	s.mu.Unlock()
	c.mu.Unlock()
	if err != nil {
		cleanup, stop := context.WithTimeout(context.Background(), c.options.ControlTimeout)
		defer stop()
		return nil, errors.Join(err, c.release(cleanup, s))
	}
	return s, nil
}

func (s *session) handshake(ctx context.Context, fresh bool) error {
	req := protocol.InitializeControlRequest{Subtype: protocol.ControlInitialize, AppendSystemPrompt: s.launch.instructions}
	if s.launch.snapshotOff {
		off := false
		req.SystemPromptSnapshot = &off
	}
	for _, path := range s.launch.initializePlugins {
		req.Plugins = append(req.Plugins, protocol.LocalPlugin{Type: "local", Path: path, SkipMCPDiscovery: true})
	}
	if s.launch.workspace != nil {
		req.Hooks = s.launch.workspace.hooks()
	}
	var response protocol.InitializeControlResponse
	if err := s.wire.call(ctx, req, &response, nil, nil); err != nil {
		return err
	}
	var effective protocol.SettingsControlResponse
	if s.launch.workspace != nil || len(response.Models) > 0 {
		err := s.wire.call(ctx, protocol.GetSettingsControlRequest{Subtype: "get_settings"}, &effective, nil, nil)
		if s.launch.workspace != nil {
			if err != nil {
				return err
			}
			if response.HooksApplied == nil || !*response.HooksApplied {
				return errors.New("Claude did not acknowledge workspace write hooks")
			}
			if err := s.launch.workspace.verify(effective); err != nil {
				return err
			}
		}
	}
	if response.PluginsApplied != nil && !*response.PluginsApplied && len(req.Plugins) > 0 {
		return errors.New("Claude rejected the configured plugins")
	}
	var status protocol.MCPStatusResponse
	if s.launch.mcpConfigured || len(s.client.options.Launch.PluginDirectories) > 0 {
		status = s.launchMCPStatus(ctx)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.init.SessionID != "" && s.init.SessionID != string(s.id) {
		return errors.New("Claude opened a different native session")
	}
	// Some CLI versions emit system/init only with the first input. Launch
	// explicitly selected this UUID; subsequent foreign frames remain rejected.
	if status.Servers != nil {
		s.init.MCPServers = slices.Clone(status.Servers)
	}
	s.initialization = response
	s.settings = initialSettings(s.init, response, effective, s.client.bypassEnabled())
	if fresh {
		s.settings.bindLegacyModel(s.client.initialModelAlias(effective.Effective), effective)
	}
	return nil
}

// launchMCPStatus waits for native asynchronous MCP handshakes. Claude 2.1.159
// and 2.1.309 can acknowledge initialize while servers are still pending.
func (s *session) launchMCPStatus(ctx context.Context) (latest protocol.MCPStatusResponse) {
	ctx, cancel := context.WithTimeout(ctx, s.client.options.ControlTimeout)
	defer cancel()
	for {
		var status protocol.MCPStatusResponse
		if err := s.wire.call(ctx, protocol.MCPStatusRequest{Subtype: "mcp_status"}, &status, nil, nil); err != nil || status.Servers == nil {
			return latest
		}
		latest = status
		if !slices.ContainsFunc(status.Servers, func(server protocol.MCPServerStatus) bool {
			return server.Status == "pending"
		}) {
			return latest
		}
		retry := time.NewTimer(100 * time.Millisecond)
		select {
		case <-ctx.Done():
			retry.Stop()
			return latest
		case <-retry.C:
		}
	}
}

func (s *session) response() acp.NewSessionResponse {
	s.mu.Lock()
	defer s.mu.Unlock()
	launch := map[string]any{}
	if s.initialization.PluginsApplied != nil {
		launch["plugins_applied"] = *s.initialization.PluginsApplied
	}
	if s.init.MCPServers != nil {
		launch["mcp_servers"] = slices.Clone(s.init.MCPServers)
	}
	var meta map[string]any
	if len(launch) > 0 {
		meta = map[string]any{"claude.launch": launch}
	}
	return acp.NewSessionResponse{SessionId: s.id, ConfigOptions: s.settings.options(), Modes: s.settings.modes(), Meta: meta}
}

func (s *session) signalLocked() {
	close(s.changed)
	s.changed = make(chan struct{})
}

func (s *session) busyLocked() bool {
	return len(s.queued) > 0 || len(s.sent) > 0 || s.current != nil || s.nativeQueued > 0 ||
		s.background > 0 || s.tasks > 0 || s.setting
}

func (s *session) admissibleLocked() error {
	if s.closing || !s.open || s.fault != nil || s.ctx.Err() != nil {
		return errClosed
	}
	if s.frozen {
		return errBusy
	}
	return nil
}

func (s *session) taskLocked(fn func()) {
	s.tasks++
	go func() {
		defer func() { s.mu.Lock(); s.tasks--; s.signalLocked(); s.mu.Unlock() }()
		fn()
	}()
}

func (s *session) disconnected(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.faultAndStopLocked(err)
	s.deliveryStop = true
	s.signalLocked()
}

func (s *session) settleTransportLocked(err error) {
	if s.fault == nil {
		s.fault = err
	}
	s.cancel()
	if s.current != nil {
		s.current.outcome.OperationError = s.fault
		s.sealLocked(s.current)
	}
	for _, in := range s.sent {
		s.finishInputLocked(in, s.fault)
	}
	s.signalLocked()
}

func (s *session) close(ctx context.Context) error {
	s.mu.Lock()
	s.closing = true
	s.cancel()
	s.signalLocked()
	s.mu.Unlock()
	select {
	case s.closeGate <- struct{}{}:
		defer func() { <-s.closeGate }()
	case <-ctx.Done():
		return ctx.Err()
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.mu.Unlock()
	// If the native leader has exited, consume trailing stdout before releasing
	// the shared process owner. Explicit forced shutdown ends the stream.
	if s.wire != nil {
		select {
		case <-s.process.Done():
			if err := await(ctx, s.wire.done); err != nil {
				return err
			}
		default:
		}
	}
	if err := s.process.Close(ctx); err != nil {
		return err
	}
	if s.wire != nil {
		if err := await(ctx, s.wire.done); err != nil {
			return err
		}
	}
	if err := await(ctx, s.deliveryDone); err != nil {
		return err
	}
	for {
		s.mu.Lock()
		if len(s.queued) == 0 && s.tasks == 0 && !s.setting {
			s.closed = true
			s.mu.Unlock()
			s.ledger.producer(false)
			return nil
		}
		ch := s.changed
		s.mu.Unlock()
		if err := await(ctx, ch); err != nil {
			return err
		}
	}
}

func (s *session) sessionUpdateLocked(update acp.SessionUpdate, meta map[string]any) {
	s.enqueueLocked(nil, acp.SessionNotification{SessionId: s.id, Update: update, Meta: meta})
}

func (s *session) sendTurnLocked(inv *invocation, update acp.SessionUpdate, meta map[string]any) {
	s.enqueueLocked(inv, acp.SessionNotification{SessionId: s.id, Update: update, Meta: meta})
}

func (s *session) guardSessionLocked() error {
	if !s.open || s.closing || s.frozen || s.fault != nil || s.ctx.Err() != nil {
		return errClosed
	}
	return nil
}

func metaEvent() acp.SessionUpdate {
	return acp.SessionUpdate{SessionInfoUpdate: &acp.SessionSessionInfoUpdate{SessionUpdate: "session_info_update"}}
}

func decodeOptional[T any](data json.RawMessage) *T {
	var out *T
	if json.Unmarshal(data, &out) != nil {
		return nil
	}
	return out
}

func (s *session) failWrite(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.faultAndStopLocked(err)
}

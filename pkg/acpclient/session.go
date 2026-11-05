package acpclient

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"slices"

	"github.com/JieWaZi/acp-go/pkg/acpmeta"
	acp "github.com/JieWaZi/acp-go/sdk"
)

const (
	maxOpeningFrames  = 256
	maxOpeningBytes   = 4 << 20
	mcpObservationKey = "acp-go/mcp"
)

// session is a single confirmed native session generation.
type session struct {
	// id becomes known only after an exact native binding.
	id acp.SessionId
	// cwd is the bound native workspace used by optional provider evidence readers.
	cwd string
	// config contains the current native options, including real setter keys.
	config configuration
	// usage keeps cumulative evidence for the native session counter.
	usage usageCounter
	// toolOwners fences known stale callback/tool identities across turns.
	toolOwners map[acp.ToolCallId]string
	// usedInputIDs prevents reuse of a caller-supplied ID within this binding.
	usedInputIDs map[string]struct{}
	// mcpIDs identifies ACP-transport MCP servers explicitly declared on open.
	mcpIDs []acp.UnstableMcpServerAcpId
	// mcpDirectory tracks process-local directory exposure when required by the provider.
	mcpDirectory *mcpDirectory
	// plan retains the native plan entries across incremental updates.
	plan []PlanItem
	// modes is the last advertised native mode state.
	modes *acp.SessionModeState
	// instructions are the frozen developer instructions for this binding.
	instructions string
	// instructionPrompts counts accepted prompts since the last instruction prefix.
	instructionPrompts int
	// instructionEpoch advances when native compaction may replace those instructions.
	instructionEpoch uint64
	// contextUsed is the last native context usage for refresh decisions.
	contextUsed *int64
}

// opening keeps pre-response evidence separate from confirmed sessions.
type opening struct {
	// session is the provisional generation.
	session *session
	// source is the original session for fork.
	source acp.SessionId
	// replay allows history only on explicit load.
	replay bool
	// started marks the native request write gate.
	started bool
	// restoredHistory records a transcript frame for the exact requested ID.
	restoredHistory bool
	// matched denotes a valid response binding even during final normalization.
	matched bool
	// previous contains the last confirmed generation of a known restore.
	previous *session
	// pending is bounded until a response identifies the target.
	pending []receivedUpdate
	// bytes accounts for retained native frames.
	bytes int
	// observations are matched frames retained until ready publication.
	observations []receivedUpdate
	// observationBytes charges matched frames until publication.
	observationBytes int
	// overflow preserves evidence that the opening lost updates.
	overflow bool
	// delivery tracks ordered non-turn observations belonging to the opening.
	delivery *delivery
	// err is a method-specific validation or normalization failure.
	err error
}

type setupResult struct {
	// ID is present for new and fork and may be echoed for restore.
	ID acp.SessionId `json:"sessionId"`
	// ConfigOptions preserves omission separately from an empty directory.
	ConfigOptions json.RawMessage `json:"configOptions"`
	// Models is the legacy native model catalog and separately reported current ID.
	Models json.RawMessage `json:"models"`
	// Modes preserves the negotiated native mode selection.
	Modes *acp.SessionModeState `json:"modes"`
	// Meta is the native setup response metadata.
	Meta map[string]any `json:"_meta"`
}

type setupRequest struct {
	// method is the native operation whose one response binds this generation.
	method string
	// id is the exact restore ID, or empty until new/fork responds.
	id acp.SessionId
	// source is the source ID for fork, never the child.
	source acp.SessionId
	// cwd and roots are the absolute native work scope.
	cwd   string
	roots []string
	// servers are the native MCP descriptors in that work scope.
	servers []acp.McpServer
	// meta carries only the host's explicitly selected setup metadata.
	meta map[string]any
	// params is the SDK request encoded to the selected native operation.
	params any
	// beforeWrite reserves the optional host fork receipt.
	beforeWrite func() error
	// afterBind persists the actual child ID before the owner can be released.
	afterBind func(acp.SessionId) error
}

func (c *Client) NewSession(ctx context.Context, request acp.NewSessionRequest) (acp.NewSessionResponse, error) {
	if request.McpServers == nil {
		request.McpServers = []acp.McpServer{}
	}
	meta := request.Meta
	request.Meta = c.nativeSessionMeta(meta)
	result, err := c.open(ctx, setupRequest{method: acp.AgentMethodSessionNew, cwd: request.Cwd,
		roots: request.AdditionalDirectories, servers: request.McpServers, meta: meta, params: request})
	return result, err
}

func (c *Client) ResumeSession(ctx context.Context, request acp.ResumeSessionRequest) (acp.ResumeSessionResponse, error) {
	if !c.validRestoreID(request.SessionId) {
		return acp.ResumeSessionResponse{}, invalid("ACP agent restore requires an exact session ID")
	}
	if request.McpServers == nil {
		request.McpServers = []acp.McpServer{}
	}
	meta := request.Meta
	request.Meta = c.nativeSessionMeta(meta)
	result, err := c.open(ctx, setupRequest{method: acp.AgentMethodSessionResume, id: request.SessionId, cwd: request.Cwd,
		roots: request.AdditionalDirectories, servers: request.McpServers, meta: meta, params: request})
	return acp.ResumeSessionResponse{ConfigOptions: result.ConfigOptions, Modes: result.Modes, Meta: result.Meta}, err
}

func (c *Client) LoadSession(ctx context.Context, request acp.LoadSessionRequest) (acp.LoadSessionResponse, error) {
	if !c.validRestoreID(request.SessionId) {
		return acp.LoadSessionResponse{}, invalid("ACP agent restore requires an exact session ID")
	}
	if request.McpServers == nil {
		request.McpServers = []acp.McpServer{}
	}
	meta := request.Meta
	request.Meta = c.nativeSessionMeta(meta)
	result, err := c.open(ctx, setupRequest{method: acp.AgentMethodSessionLoad, id: request.SessionId, cwd: request.Cwd,
		roots: request.AdditionalDirectories, servers: request.McpServers, meta: meta, params: request})
	return acp.LoadSessionResponse{ConfigOptions: result.ConfigOptions, Modes: result.Modes, Meta: result.Meta}, err
}

func (c *Client) validRestoreID(id acp.SessionId) bool {
	check := c.opts.Protocol.ValidateRestoreID
	return id != "" && (check == nil || check(id))
}

func (c *Client) UnstableForkSession(ctx context.Context, request acp.UnstableForkSessionRequest) (acp.UnstableForkSessionResponse, error) {
	if request.SessionId == "" {
		return acp.UnstableForkSessionResponse{}, invalid("ACP agent fork requires an exact source ID")
	}
	if acpmeta.ReconcileOnly(request) {
		id, err := acpmeta.ReadForkReceipt(request)
		return acp.UnstableForkSessionResponse{SessionId: id}, err
	}
	servers, err := acpmeta.ForkMCPServers(request.McpServers)
	if err != nil {
		return acp.UnstableForkSessionResponse{}, invalid("invalid ACP agent fork MCP servers")
	}
	receipt := request.Meta["forkReceiptDirectory"] != nil
	// The filesystem receipt path is private to the host and never goes to the CLI.
	native := request
	native.Meta = c.nativeSessionMeta(request.Meta)
	delete(native.Meta, "forkReceiptDirectory")
	delete(native.Meta, "reconcileOnly")
	setup := setupRequest{method: acp.AgentMethodSessionFork, source: request.SessionId,
		cwd: request.Cwd, roots: request.AdditionalDirectories, servers: servers, meta: request.Meta, params: native}
	if receipt {
		setup.beforeWrite = func() error { return acpmeta.BeginForkReceipt(request) }
		setup.afterBind = func(id acp.SessionId) error { return acpmeta.WriteForkReceipt(request, id) }
	}
	result, openErr := c.open(ctx, setup)
	if result.SessionId == "" {
		return acp.UnstableForkSessionResponse{}, openErr
	}
	projected, projectionErr := acpmeta.ForkResponse(request.SessionId, result)
	return projected, errors.Join(openErr, projectionErr)
}

func (c *Client) open(ctx context.Context, request setupRequest) (acp.NewSessionResponse, error) {
	conn, err := c.begin(ctx)
	if err != nil {
		return acp.NewSessionResponse{}, err
	}
	admissionReleased := false
	defer func() {
		if admissionReleased {
			c.workers.Done()
		} else {
			c.end()
		}
	}()
	if err := c.validateSetup(request); err != nil {
		return acp.NewSessionResponse{}, err
	}
	mcpDirectory, err := c.prepareMCPDirectory(request)
	if err != nil {
		return acp.NewSessionResponse{}, err
	}
	instructions, err := acpmeta.DeveloperInstructions(request.meta)
	if err != nil || instructions != nil && len(*instructions) > 1<<20 {
		return acp.NewSessionResponse{}, invalid("invalid ACP agent developer instructions")
	}
	op, cancel := c.operationContext(ctx, requestTimeout)
	defer cancel()
	p := &opening{session: &session{id: request.id, cwd: request.cwd, usage: usageCounter{fresh: request.method == acp.AgentMethodSessionNew}}, source: request.source, replay: request.method == acp.AgentMethodSessionLoad,
		delivery: newDelivery(op)}
	p.session.mcpDirectory = mcpDirectory
	c.mu.Lock()
	p.previous = c.sessions[request.id]
	for _, server := range request.servers {
		if server.Acp != nil {
			p.session.mcpIDs = append(p.session.mcpIDs, acp.UnstableMcpServerAcpId(server.Acp.Id))
		}
	}
	if p.previous == nil && len(c.sessions) >= maxSessions {
		c.mu.Unlock()
		return acp.NewSessionResponse{}, invalid("ACP agent session budget exhausted")
	}
	if p.previous != nil {
		p.session.config, p.session.modes = p.previous.config, p.previous.modes
		// Restore the same counter without treating the old generation as adjacent.
		p.session.usage.highWater = p.previous.usage.highWater
		p.session.instructions = p.previous.instructions
		p.session.usedInputIDs = p.previous.usedInputIDs
		p.session.toolOwners = p.previous.toolOwners
	}
	if instructions != nil {
		p.session.instructions = *instructions
	}
	c.opening = p
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		if c.opening == p {
			c.opening = nil
		}
		c.mu.Unlock()
		p.delivery.cancel()
	}()
	if prepare := c.opts.Protocol.PrepareRestore; request.method == acp.AgentMethodSessionLoad && prepare != nil {
		if err := prepare(op, c.verifiedNativeVersion(), request.cwd, request.id); err != nil {
			return acp.NewSessionResponse{}, err
		}
	}
	if request.beforeWrite != nil {
		if err := request.beforeWrite(); err != nil {
			return acp.NewSessionResponse{}, err
		}
	}
	restore := func() acp.RequestReceipt[setupResult] {
		return acp.SendClientRequestWithReceipt[setupResult](conn, op, request.method, request.params,
			boundedWrite(op, conn, func() error {
				c.mu.Lock()
				defer c.mu.Unlock()
				if c.closed || c.opening != p {
					return ErrRestartRequired
				}
				p.started = true
				return nil
			}), func(r acp.RequestReceipt[setupResult]) { c.bindOpening(p, r) })
	}
	receipt := restore()
	retry, recoveryErr := c.recoverRestore(op, conn, p, request, receipt.PeerErr)
	if retry {
		receipt = restore()
	}
	var receiptErr error
	if request.afterBind != nil && p.matched {
		// The receive hook already bound the ID. Keep admission while the host's
		// durable fork receipt is written and absorb config that arrives here.
		receiptErr = request.afterBind(p.session.id)
	}
	var modeErr error
	if receiptErr == nil && c.opts.Protocol.OpeningMode != "" {
		modeErr = c.applyOpeningMode(op, conn, p, c.opts.Protocol.OpeningMode)
	}
	c.mu.Lock()
	response := acp.NewSessionResponse{}
	if p.matched {
		response.SessionId = p.session.id
		if p.err == nil && receiptErr == nil && modeErr == nil && !p.overflow && !c.closed {
			response.ConfigOptions = p.session.config.options
			response.Modes, _ = c.sessionModeState(p.session)
			response.Meta = mcpMetadata(receipt.Result.Meta, len(request.servers))
			c.sessions[p.session.id] = p.session
			c.rememberMCPDirectory(p.session)
			// Opening can change the process's active model. Only an
			// acknowledged model set can establish the process-wide value.
			if model := modelOption(p.session.config.options); c.opts.Protocol.ModelScope == ModelPerProcess && model != nil && model.CurrentValue != c.processModel {
				c.processModel = ""
			}
			c.opening = nil
			if !p.replay {
				c.busy = false
				admissionReleased = true
			}
			c.publishOpeningLocked(p)
		}
	}
	c.sealDeliveryLocked(p.delivery)
	normalizeErr := errors.Join(p.err, modeErr)
	if p.overflow {
		normalizeErr = errors.Join(normalizeErr, errors.New("ACP agent session opening observation budget exhausted"))
	}
	if c.closed {
		normalizeErr = errors.Join(normalizeErr, ErrRestartRequired)
	}
	if normalizeErr != nil || !p.matched && receipt.Write.BytesWritten != 0 &&
		(receipt.PeerErr == nil || mcpDirectory != nil && mcpDirectory.hasRoutes) {
		c.unusable = true
	}
	c.mu.Unlock()
	projected, cloneErr := clone(response)
	if cloneErr != nil {
		return acp.NewSessionResponse{SessionId: response.SessionId}, errors.New("invalid ACP agent session setup evidence")
	}
	response = projected
	operationErr := sessionReceiptFailure(c, receipt, request.method, request.id)
	if recoveryErr != nil {
		operationErr = recoveryErr
	}
	if operationErr != nil || normalizeErr != nil || receiptErr != nil {
		return response, errors.Join(operationErr, normalizeErr, receiptErr)
	}
	if !p.matched {
		return response, errors.New("ACP agent returned an unconfirmed session ID")
	}
	// A replay must settle before returning. Session observations remain
	// independent and can call a setter as soon as the ready state is visible.
	if p.replay {
		return response, c.waitDelivery(p.delivery)
	}
	return response, nil
}

// bindOpening executes in receive order before later frames can change state.
func (c *Client) bindOpening(p *opening, r acp.RequestReceipt[setupResult]) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.opening != p || !r.ResultValid || c.closed {
		return
	}
	id := r.Result.ID
	if p.session.id != "" {
		if id != "" && id != p.session.id {
			p.err = errors.New("ACP agent restore returned a different native session")
			return
		}
		if p.replay && c.opts.Protocol.RequireLoadEvidence && id == "" && !p.restoredHistory {
			p.err = &acp.RequestError{Code: -32603, Message: "ACP agent did not verify restored history",
				Data: map[string]any{"method": acp.AgentMethodSessionLoad, "classification": "session_restore_unverified"}}
			return
		}
		id = p.session.id
	}
	if id == "" || id == p.source || c.sessions[id] != p.previous {
		p.err = errors.New("ACP agent did not return an independent native session")
		return
	}
	p.matched = true
	p.session.id = id
	for _, update := range p.pending {
		if update.id == id {
			c.applyOpeningUpdate(p, update)
		}
	}
	p.pending = nil
	p.bytes = 0
	if err := c.applySetupLocked(p.session, r.Result, r.Sequence); err != nil {
		p.err = err
	}
}

func (c *Client) validateSetup(request setupRequest) error {
	caps := c.initialized.AgentCapabilities
	sessions := caps.SessionCapabilities
	supported := request.method == acp.AgentMethodSessionNew ||
		request.method == acp.AgentMethodSessionResume && sessions.Resume != nil ||
		request.method == acp.AgentMethodSessionLoad && caps.LoadSession ||
		request.method == acp.AgentMethodSessionFork && sessions.Fork != nil
	if !supported {
		return acp.NewMethodNotFound(request.method)
	}
	if request.method == acp.AgentMethodSessionFork && acpmeta.ForkPosition(request.meta) != "" && acpmeta.ForkMode(c.initialized.Meta) != acpmeta.ForkTurn {
		return invalid("ACP agent did not advertise exact fork positions")
	}
	if !filepath.IsAbs(request.cwd) || len(request.roots) > 0 && sessions.AdditionalDirectories == nil {
		return invalid("ACP agent requires an absolute cwd and advertised additional directories")
	}
	if c.opts.Protocol.CwdBoundProcess && filepath.Clean(request.cwd) != c.opts.Process.Dir {
		return invalid("ACP agent requires the session cwd to match the process launch directory")
	}
	for _, root := range request.roots {
		if !filepath.IsAbs(root) {
			return invalid("ACP agent additional directories must be absolute")
		}
	}
	if len(request.servers) > 0 && c.opts.RequireVerifiedMCP {
		return invalid("ACP agent does not provide a verified MCP load receipt")
	}
	for _, s := range request.servers {
		mcp := caps.McpCapabilities
		if s.Acp != nil && c.peer == nil {
			return invalid("ACP agent ACP MCP requires a negotiated wire host")
		}
		if s.Http != nil && !mcp.Http || s.Sse != nil && !mcp.Sse || s.Acp != nil && !mcp.Acp {
			return invalid("ACP agent did not advertise the requested MCP transport")
		}
	}
	return nil
}

func (c *Client) ListSessions(ctx context.Context, request acp.ListSessionsRequest) (acp.ListSessionsResponse, error) {
	conn, err := c.begin(ctx)
	if err != nil {
		return acp.ListSessionsResponse{}, err
	}
	defer c.end()
	if c.initialized.AgentCapabilities.SessionCapabilities.List == nil {
		return acp.ListSessionsResponse{}, acp.NewMethodNotFound(acp.AgentMethodSessionList)
	}
	if request.Cwd != nil && !filepath.IsAbs(*request.Cwd) {
		return acp.ListSessionsResponse{}, invalid("ACP agent session list requires an absolute cwd")
	}
	op, cancel := c.operationContext(ctx, requestTimeout)
	defer cancel()
	r := send[acp.ListSessionsResponse](conn, op, acp.AgentMethodSessionList, request)
	return r.Result, receiptFailure(r, acp.AgentMethodSessionList)
}

func (c *Client) CloseSession(ctx context.Context, request acp.CloseSessionRequest) (acp.CloseSessionResponse, error) {
	conn, err := c.begin(ctx)
	if err != nil {
		return acp.CloseSessionResponse{}, err
	}
	defer c.end()
	if c.initialized.AgentCapabilities.SessionCapabilities.Close == nil {
		return acp.CloseSessionResponse{}, acp.NewMethodNotFound(acp.AgentMethodSessionClose)
	}
	if request.SessionId == "" {
		return acp.CloseSessionResponse{}, invalid("ACP agent close requires an exact session ID")
	}
	op, cancel := c.operationContext(ctx, requestTimeout)
	defer cancel()
	r := acp.SendClientRequestWithReceipt[acp.CloseSessionResponse](conn, op, acp.AgentMethodSessionClose, request, boundedWrite(op, conn, nil),
		func(r acp.RequestReceipt[acp.CloseSessionResponse]) {
			if r.ResultValid {
				c.mu.Lock()
				delete(c.sessions, request.SessionId)
				c.mu.Unlock()
			}
		})
	if r.PeerErr == nil && !r.ResultValid && r.Write.BytesWritten > 0 {
		c.invalidate()
	}
	return r.Result, sessionReceiptFailure(c, r, acp.AgentMethodSessionClose, request.SessionId)
}

func (c *Client) SetSessionMode(ctx context.Context, request acp.SetSessionModeRequest) (acp.SetSessionModeResponse, error) {
	conn, err := c.begin(ctx)
	if err != nil {
		return acp.SetSessionModeResponse{}, err
	}
	defer c.end()
	c.mu.Lock()
	s := c.sessions[request.SessionId]
	modes, optionID := c.sessionModeState(s)
	valid := hasMode(modes, request.ModeId)
	c.mu.Unlock()
	if !valid {
		return acp.SetSessionModeResponse{}, invalid("ACP agent did not advertise the requested mode")
	}
	op, cancel := c.operationContext(ctx, requestTimeout)
	defer cancel()
	if optionID != "" {
		wanted := setting{id: optionID, value: acp.SessionConfigValueId(request.ModeId)}
		params := acp.SetSessionConfigOptionRequest{ValueId: &acp.SetSessionConfigOptionValueId{
			SessionId: s.id, ConfigId: wanted.id, Value: wanted.value,
		}}
		result, err := c.setConfig(op, conn, s, wanted, params)
		return acp.SetSessionModeResponse{Meta: result.Meta}, err
	}
	return c.setNativeMode(op, conn, s, request)
}

// sessionModeState projects a modern mode option only when the peer did not
// supply its legacy mode menu. The returned option ID selects the setter.
func (c *Client) sessionModeState(s *session) (*acp.SessionModeState, acp.SessionConfigId) {
	if s == nil {
		return nil, ""
	}
	id := c.opts.Protocol.ModeOption
	if s.modes != nil && len(s.modes.AvailableModes) != 0 {
		return s.modes, id
	}
	var mode *acp.SessionConfigOptionSelect
	for _, option := range s.config.options {
		selectOption := option.Select
		if selectOption == nil || selectOption.Category == nil || *selectOption.Category != acp.SessionConfigOptionCategoryMode ||
			id != "" && selectOption.Id != id {
			continue
		}
		if mode != nil {
			return s.modes, id
		}
		mode = selectOption
	}
	if mode == nil {
		return s.modes, id
	}
	state := &acp.SessionModeState{}
	appendModes := func(choices []acp.SessionConfigSelectOption) {
		for _, choice := range choices {
			if choice.Value != "" {
				state.AvailableModes = append(state.AvailableModes, acp.SessionMode{
					Id: acp.SessionModeId(choice.Value), Name: choice.Name, Description: choice.Description,
				})
			}
		}
	}
	if mode.Options.Ungrouped != nil {
		appendModes(*mode.Options.Ungrouped)
	}
	if mode.Options.Grouped != nil {
		for _, group := range *mode.Options.Grouped {
			appendModes(group.Options)
		}
	}
	if len(state.AvailableModes) == 0 {
		return s.modes, id
	}
	if current := acp.SessionModeId(mode.CurrentValue); s.config.currentAt[mode.Id] != 0 && hasMode(state, current) {
		state.CurrentModeId = current
	}
	return state, mode.Id
}

func hasMode(state *acp.SessionModeState, id acp.SessionModeId) bool {
	return state != nil && slices.ContainsFunc(state.AvailableModes, func(mode acp.SessionMode) bool { return mode.Id == id })
}

func (c *Client) applyOpeningMode(ctx context.Context, conn *acp.ClientSideConnection, p *opening, id acp.SessionModeId) error {
	c.mu.Lock()
	if !p.matched || p.err != nil || c.closed {
		c.mu.Unlock()
		return nil
	}
	state, sessionID := p.session.modes, p.session.id
	c.mu.Unlock()
	if state == nil && id == "default" || state != nil && state.CurrentModeId == id {
		return nil
	}
	if !hasMode(state, id) {
		return invalid("ACP agent did not advertise the requested opening mode")
	}
	_, err := c.setNativeMode(ctx, conn, p.session, acp.SetSessionModeRequest{SessionId: sessionID, ModeId: id})
	return err
}

func (c *Client) setNativeMode(op context.Context, conn *acp.ClientSideConnection, s *session, request acp.SetSessionModeRequest) (acp.SetSessionModeResponse, error) {
	r := acp.SendClientRequestWithReceipt[acp.SetSessionModeResponse](conn, op, acp.AgentMethodSessionSetMode, request, boundedWrite(op, conn, nil),
		func(r acp.RequestReceipt[acp.SetSessionModeResponse]) {
			if r.ResultValid {
				c.mu.Lock()
				modes := *s.modes
				modes.CurrentModeId = request.ModeId
				s.modes = &modes
				c.mu.Unlock()
			}
		})
	if !r.ResultValid && r.PeerErr == nil && r.Write.BytesWritten > 0 {
		c.invalidate()
	}
	return r.Result, sessionReceiptFailure(c, r, acp.AgentMethodSessionSetMode, request.SessionId)
}

// MCPObservation reports only the MCP facts established during session setup.
type MCPObservation struct {
	// Requested counts native server descriptors sent with the open operation.
	Requested int `json:"requested"`
	// Status is either not_requested or load_unverified.
	Status string `json:"status"`
}

func ReadMCPObservation(meta map[string]any) (MCPObservation, bool) {
	raw, err := json.Marshal(meta[mcpObservationKey])
	var observation MCPObservation
	if err != nil || json.Unmarshal(raw, &observation) != nil {
		return MCPObservation{}, false
	}
	valid := observation.Requested == 0 && observation.Status == "not_requested" ||
		observation.Requested > 0 && observation.Status == "load_unverified"
	return observation, valid
}

func (c *Client) nativeSessionMeta(meta map[string]any) map[string]any {
	result := copyMeta(meta)
	delete(result, "developerInstructions")
	if prepare := c.opts.Protocol.PrepareSessionMeta; prepare != nil {
		prepare(result)
	}
	return result
}

func mcpMetadata(meta map[string]any, requested int) map[string]any {
	result := copyMeta(meta)
	status := "not_requested"
	if requested > 0 {
		status = "load_unverified"
	}
	result[mcpObservationKey] = MCPObservation{Requested: requested, Status: status}
	return result
}

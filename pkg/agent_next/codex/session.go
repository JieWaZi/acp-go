package codex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/JieWaZi/acp-go/pkg/acpmeta"
	agentclient "github.com/JieWaZi/acp-go/pkg/agent_next"
	"github.com/JieWaZi/acp-go/pkg/codex/protocol"
	acp "github.com/JieWaZi/acp-go/sdk"
)

const (
	openSettingsKey       = "codex/open-settings"
	sessionSettingsKey    = "codex/settings"
	fastServiceTierKey    = "codex/fast-service-tier"
	SessionSettingsMethod = "_codex/session/settings"
	ThreadArchiveMethod   = "_codex/thread/archive"
	ThreadUnarchiveMethod = "_codex/thread/unarchive"
)

// OpenSettings carries native thread overrides.
type OpenSettings struct {
	// Mode sets the native permission policy from the moment the thread opens.
	Mode string `json:"mode,omitempty"`
	// Model is the exact requested model ID, or nil to inherit.
	Model *string `json:"model,omitempty"`
	// ReasoningEffort is the requested native reasoning effort, or nil to inherit.
	ReasoningEffort *string `json:"reasoning_effort,omitempty"`
	// ServiceTier is absent to inherit, null to turn off, or a native ID.
	ServiceTier protocol.OptionalNullable[string] `json:"service_tier,omitzero"`
}

// ObservedSettings holds the settings in a native response or notification.
type ObservedSettings struct {
	// OpenSettings carries the effective values reported by Codex.
	OpenSettings
	// Source identifies the message that supplied the settings.
	Source string `json:"source"`
}

// SessionSettings separates requested values from confirmed values.
type SessionSettings struct {
	// Requested is the intent frozen by the next prompt reservation.
	Requested OpenSettings `json:"requested"`
	// Observed changes only on native evidence.
	Observed ObservedSettings `json:"observed"`
}

type session struct {
	// id is the native thread ID. The pointer to this session identifies its generation.
	id acp.SessionId
	// cwd is the actual native working directory.
	cwd string
	// roots are the host-authorized additional writable directories.
	roots []string
	// skillRoots are the most recently applied extra roots for this generation.
	skillRoots []string
	// mode is the local permission preset applied to the next turn.
	mode acp.SessionModeId
	// settings contains local intent and independent native settings receipts.
	settings SessionSettings
	// models is the catalog frozen when the session opened.
	models []protocol.ModelListResponseDatum
	// busy covers configuration, history replay, fork, and archive while active is nil.
	busy bool
	// closing prevents new work until exact-generation cleanup succeeds.
	closing bool
	// active is the sole native turn and control slot.
	active *turn
	// draining retains retired owners until their host and input cleanup completes.
	draining map[*turn]struct{}
	// inputs rejects reuse of input IDs within the opened generation.
	inputs map[string]struct{}
	// mcpServers stores the requested names and whether startup failure was published.
	mcpServers map[string]bool
	// usageCheckpoint keeps the cumulative floor and whether it needs corroboration.
	usageCheckpoint *tokenCheckpoint
}

func OpenSettingsMetadata(settings OpenSettings) map[string]any {
	return map[string]any{openSettingsKey: settings}
}

func ReadSessionSettingsMetadata(meta map[string]any) (SessionSettings, bool) {
	var settings SessionSettings
	raw, exists := meta[sessionSettingsKey]
	if !exists {
		return settings, false
	}
	data, err := json.Marshal(raw)
	if err != nil || string(data) == "null" || json.Unmarshal(data, &settings) != nil {
		return SessionSettings{}, false
	}
	return settings, true
}

func FastServiceTier(meta map[string]any) (string, bool) {
	tier, ok := meta[fastServiceTierKey].(string)
	return tier, ok && tier != ""
}

func (c *Client) sessionLocked(id acp.SessionId) (*session, error) {
	if err := c.checkReadyLocked(); err != nil {
		return nil, err
	}
	s := c.sessions[id]
	if s == nil {
		return nil, acp.NewInvalidParams(map[string]any{"session_id": id, "reason": "Codex session not open"})
	}
	return s, nil
}

func (c *Client) idleSessionLocked(id acp.SessionId) (*session, error) {
	s, err := c.sessionLocked(id)
	if err != nil {
		return nil, err
	}
	if s.active != nil || len(s.draining) > 0 || s.busy || s.closing {
		return nil, errTurnBusy
	}
	return s, nil
}

type openInput struct {
	// id is the thread to resume or fork, absent for a new thread.
	id acp.SessionId
	// cwd is the host-selected absolute working directory.
	cwd string
	// directories are the host-selected additional roots.
	directories []string
	// servers are the MCP definitions delivered with this open.
	servers []acp.McpServer
	// meta carries typed Codex settings, instructions and fork position.
	meta map[string]any
}

type openedThread struct {
	// Thread contains the native identity and optional rollout.
	Thread protocol.Thread `json:"thread"`
	// Model is the effective native routing ID.
	Model string `json:"model"`
	// Effort is the effective reasoning level when the server reports it.
	Effort *string `json:"reasoningEffort"`
	// Tier preserves absent, null and explicit native tier.
	Tier protocol.OptionalNullable[string] `json:"serviceTier,omitzero"`
	// Cwd records the native effective working directory.
	Cwd string `json:"cwd"`
	// Sandbox reports the effective permission policy.
	Sandbox protocol.SandboxClass `json:"sandbox"`
}

func (c *Client) NewSession(ctx context.Context, r acp.NewSessionRequest) (acp.NewSessionResponse, error) {
	return c.open(ctx, "thread/start", openInput{cwd: r.Cwd, directories: r.AdditionalDirectories, servers: r.McpServers, meta: r.Meta}, false, nil)
}

func (c *Client) ResumeSession(ctx context.Context, r acp.ResumeSessionRequest) (acp.ResumeSessionResponse, error) {
	o, err := c.open(ctx, "thread/resume", openInput{id: r.SessionId, cwd: r.Cwd, directories: r.AdditionalDirectories, servers: r.McpServers, meta: r.Meta}, false, nil)
	return acp.ResumeSessionResponse{Modes: o.Modes, ConfigOptions: o.ConfigOptions, Meta: o.Meta}, err
}

func (c *Client) LoadSession(ctx context.Context, r acp.LoadSessionRequest) (acp.LoadSessionResponse, error) {
	o, err := c.open(ctx, "thread/resume", openInput{id: r.SessionId, cwd: r.Cwd, directories: r.AdditionalDirectories, servers: r.McpServers, meta: r.Meta}, true, nil)
	return acp.LoadSessionResponse{Modes: o.Modes, ConfigOptions: o.ConfigOptions, Meta: o.Meta}, err
}

func (c *Client) UnstableForkSession(ctx context.Context, r acp.UnstableForkSessionRequest) (acp.UnstableForkSessionResponse, error) {
	if acpmeta.ReconcileOnly(r) {
		id, err := acpmeta.ReadForkReceipt(r)
		return acp.UnstableForkSessionResponse{SessionId: id}, err
	}
	if err := c.readyError(); err != nil {
		return acp.UnstableForkSessionResponse{}, err
	}
	c.mu.Lock()
	mode := c.forkMode
	c.mu.Unlock()
	if mode == acpmeta.ForkUnsupported || mode == "" || acpmeta.ForkPosition(r.Meta) != "" && mode != acpmeta.ForkTurn {
		return acp.UnstableForkSessionResponse{}, acp.NewMethodNotFound("session/fork")
	}
	servers, err := acpmeta.ForkMCPServers(r.McpServers)
	if err != nil {
		return acp.UnstableForkSessionResponse{}, err
	}
	o, err := c.open(ctx, "thread/fork", openInput{id: r.SessionId, cwd: r.Cwd, directories: r.AdditionalDirectories, servers: servers, meta: r.Meta}, false, &r)
	fork, projectionErr := acpmeta.ForkResponse(r.SessionId, o)
	return fork, errors.Join(err, projectionErr)
}

func (c *Client) open(ctx context.Context, method string, input openInput, replay bool, fork *acp.UnstableForkSessionRequest) (acp.NewSessionResponse, error) {
	roots, err := workspaceRoots(input.cwd, input.directories)
	if err != nil {
		return acp.NewSessionResponse{}, err
	}
	input.cwd = roots[0]
	intent, err := parseSettings(input.meta)
	if err != nil {
		return acp.NewSessionResponse{}, err
	}
	instructions, err := acpmeta.DeveloperInstructions(input.meta)
	if err != nil {
		return acp.NewSessionResponse{}, acp.NewInvalidParams(err.Error())
	}
	c.mu.Lock()
	if err := c.checkReadyLocked(); err != nil {
		c.mu.Unlock()
		return acp.NewSessionResponse{}, err
	}
	source := c.sessions[input.id]
	if method == "thread/resume" && input.id == "" {
		c.mu.Unlock()
		return acp.NewSessionResponse{}, acp.NewInvalidParams("session ID is required")
	}
	if source != nil {
		if source.busy || source.active != nil || len(source.draining) > 0 || source.closing || method == "thread/resume" {
			c.mu.Unlock()
			return acp.NewSessionResponse{}, errTurnBusy
		}
		source.busy = true
	}
	if c.managing == nil {
		c.managing = make(map[acp.SessionId]bool)
	}
	if input.id != "" && c.managing[input.id] {
		if source != nil {
			source.busy = false
		}
		c.mu.Unlock()
		return acp.NewSessionResponse{}, errTurnBusy
	}
	if input.id != "" {
		c.managing[input.id] = true
	}
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.managing, input.id)
		if source != nil {
			source.busy = false
		}
		c.mu.Unlock()
	}()
	if err := c.checkAccount(ctx); err != nil {
		return acp.NewSessionResponse{}, err
	}
	config, err := c.openConfig(ctx, roots, input.servers, intent)
	if err != nil {
		return acp.NewSessionResponse{}, err
	}
	models, err := c.readModelPages(ctx)
	if err != nil {
		return acp.NewSessionResponse{}, err
	}
	mode := acp.SessionModeId(intent.Mode)
	if mode == "" {
		mode = "agent"
	}
	approval, sandbox, _ := codexModePolicy(mode, roots[1:])
	params := map[string]any{"cwd": input.cwd, "config": config, "approvalPolicy": approval, "sandbox": sandbox}
	if instructions != nil {
		params["developerInstructions"] = *instructions
	}
	if intent.Model != nil {
		params["model"] = *intent.Model
	}
	if tier, ok := intent.ServiceTier.Value(); ok {
		params["serviceTier"] = tier
	} else if intent.ServiceTier.IsNull() {
		params["serviceTier"] = nil
	}
	if input.id != "" {
		params["threadId"] = input.id
	}
	if method == "thread/resume" {
		params["excludeTurns"] = true
	}
	releaseSkills, err := c.claimSkills(ctx, roots)
	if err != nil {
		return acp.NewSessionResponse{}, err
	}
	defer releaseSkills()
	if fork != nil {
		if position := acpmeta.ForkPosition(input.meta); position != "" {
			params["lastTurnId"] = position
		}
		if err := acpmeta.BeginForkReceipt(*fork); err != nil {
			return acp.NewSessionResponse{}, err
		}
	}
	s := &session{cwd: input.cwd, roots: roots[1:], mode: mode, models: models, busy: true,
		settings: SessionSettings{Requested: intent}, inputs: make(map[string]struct{}), mcpServers: make(map[string]bool)}
	for key := range config {
		if name, ok := strings.CutPrefix(key, "mcp_servers."); ok && name != "" {
			s.mcpServers[name] = false
		}
	}
	var opened openedThread
	var captureErr error
	var installed bool
	var nativeID acp.SessionId
	submit := func() (frameWrite, error) {
		return c.call(ctx, method, params, nil, func() error {
			if err := c.checkReadyLocked(); err != nil {
				return err
			}
			if source != nil && c.sessions[input.id] != source {
				return errTurnChanged
			}
			return nil
		}, func(frame rpcFrame) {
			if frame.Error != nil {
				return
			}
			if json.Unmarshal(frame.Result, &opened) != nil {
				captureErr = errors.New("Codex returned an invalid thread response")
				return
			}
			nativeID = acp.SessionId(opened.Thread.ID)
			if nativeID == "" || method == "thread/resume" && nativeID != input.id || fork != nil && nativeID == input.id {
				captureErr = errors.New("Codex returned an invalid thread identity")
				nativeID = ""
				return
			}
			if c.sessions[nativeID] != nil {
				captureErr = errTurnBusy
				return
			}
			if err := c.checkReadyLocked(); err != nil {
				captureErr = err
				return
			}
			s.id = nativeID
			s.skillRoots = slices.Clone(c.skillRoots)
			c.sessions[nativeID] = s
			installed = true
			s.observe(strings.ReplaceAll(method, "/", "_"), opened.Model, opened.Effort, opened.Tier)
			if opened.Cwd != "" && filepath.Clean(opened.Cwd) != input.cwd {
				captureErr = errors.New("Codex opened an unexpected working directory")
			}
		})
	}
	written, err := submit()
	if method == "thread/resume" && archivedThreadError(err, input.id) {
		if unarchiveErr := c.request(ctx, "thread/unarchive", map[string]any{"threadId": input.id}, nil); unarchiveErr != nil {
			return acp.NewSessionResponse{}, errors.Join(err, unarchiveErr)
		}
		written, err = submit()
	}
	releaseSkills()
	if nativeID == "" {
		var nativeRejection *acp.RequestError
		if written.submission() != agentclient.SubmissionNotSent && !errors.As(err, &nativeRejection) {
			c.fail(errors.New("Codex thread open ended without an attributable native response"))
		}
		return acp.NewSessionResponse{}, errors.Join(err, captureErr)
	}
	if fork != nil {
		err = errors.Join(err, acpmeta.WriteForkReceipt(*fork, nativeID))
	}
	err = errors.Join(err, captureErr)
	c.mu.Lock()
	if err == nil {
		err = c.checkReadyLocked()
	}
	if installed && err != nil {
		s.closing = true
		s.busy = false
	}
	if err == nil && replay {
		c.interactions.Add(1)
	}
	c.mu.Unlock()
	if err != nil {
		if installed {
			cleanup, cancel := context.WithTimeout(context.Background(), controlTimeout)
			defer cancel()
			_, cleanupErr := c.CloseSession(cleanup, acp.CloseSessionRequest{SessionId: nativeID})
			err = errors.Join(err, cleanupErr)
		}
		return acp.NewSessionResponse{SessionId: nativeID}, err
	}
	id := s.id
	if replay {
		historyCtx, historyCancel := context.WithCancel(ctx)
		stop := context.AfterFunc(c.life, historyCancel)
		err := c.replayHistory(historyCtx, s, opened.Thread.Turns)
		stop()
		historyCancel()
		c.interactions.Done()
		if err != nil {
			c.mu.Lock()
			s.closing = true
			s.busy = false
			c.mu.Unlock()
			cleanup, cancel := context.WithTimeout(context.Background(), controlTimeout)
			defer cancel()
			_, cleanupErr := c.CloseSession(cleanup, acp.CloseSessionRequest{SessionId: id})
			return acp.NewSessionResponse{SessionId: id}, errors.Join(err, cleanupErr)
		}
	}
	c.mu.Lock()
	s.busy = false
	result := s.response()
	err = c.checkReadyLocked()
	c.mu.Unlock()
	return result, err
}

func (c *Client) CloseSession(ctx context.Context, r acp.CloseSessionRequest) (acp.CloseSessionResponse, error) {
	c.mu.Lock()
	s := c.sessions[r.SessionId]
	if s == nil {
		c.mu.Unlock()
		return acp.CloseSessionResponse{}, nil
	}
	if s.busy {
		c.mu.Unlock()
		return acp.CloseSessionResponse{}, errTurnBusy
	}
	s.busy = true
	s.closing = true
	c.signalReceiptsLocked()
	closing := make([]*turn, 0, 1+len(s.draining))
	for t := range s.draining {
		closing = append(closing, t)
	}
	if s.active != nil {
		closing = append(closing, s.active)
		c.cancelTurnLocked(s.active)
	}
	c.mu.Unlock()
	defer func() { c.mu.Lock(); s.busy = false; c.mu.Unlock() }()
	for _, t := range closing {
		if err := waitDone(ctx, t.released); err != nil {
			return acp.CloseSessionResponse{}, err
		}
	}
	var response protocol.ThreadUnsubscribeResponse
	_, err := c.call(ctx, "thread/unsubscribe", protocol.ThreadUnsubscribeParams{ThreadID: string(s.id)}, &response, func() error {
		if c.sessions[s.id] != s || s.active != nil || len(s.draining) > 0 {
			return errTurnChanged
		}
		return nil
	}, nil)
	if err != nil {
		return acp.CloseSessionResponse{}, err
	}
	switch response.Status {
	case protocol.Unsubscribed, protocol.NotSubscribed, protocol.StatusNotLoaded:
	default:
		return acp.CloseSessionResponse{}, errors.New("Codex did not confirm thread release")
	}
	c.mu.Lock()
	if c.sessions[s.id] == s {
		delete(c.sessions, s.id)
	}
	c.mu.Unlock()
	return acp.CloseSessionResponse{}, nil
}

func (s *session) observe(source, model string, effort *string, tier protocol.OptionalNullable[string]) {
	s.settings.Observed = ObservedSettings{Source: source, OpenSettings: OpenSettings{ReasoningEffort: effort, ServiceTier: tier}}
	if model != "" {
		s.settings.Observed.Model = pointer(model)
	}
}

func (s *session) turnParams() protocol.TurnStartParams {
	p := protocol.TurnStartParams{
		ThreadID: string(s.id), Cwd: pointer(s.cwd), Model: s.settings.Requested.Model, Effort: s.settings.Requested.ReasoningEffort,
		ServiceTier: s.settings.Requested.ServiceTier,
	}
	approval, _, sandbox := codexModePolicy(s.mode, s.roots)
	p.ApprovalPolicy = &protocol.ApprovalPolicy{Enum: &approval}
	p.SandboxPolicy = &sandbox
	return p
}

func codexModePolicy(mode acp.SessionModeId, roots []string) (protocol.ApprovalPolicyEnum, string, protocol.SandboxPolicy) {
	networkDisabled := &protocol.NetworkAccessUnion{Bool: pointer(false)}
	switch mode {
	case "read-only":
		return protocol.OnRequest, "read-only", protocol.SandboxPolicy{Type: protocol.SandboxPolicyTypeReadOnly, NetworkAccess: networkDisabled}
	case "agent-full-access":
		return protocol.Never, "danger-full-access", protocol.SandboxPolicy{Type: protocol.SandboxPolicyTypeDangerFullAccess}
	default:
		return protocol.OnRequest, "workspace-write", protocol.SandboxPolicy{
			Type: protocol.SandboxPolicyTypeWorkspaceWrite, WritableRoots: slices.Clone(roots),
			NetworkAccess: networkDisabled, ExcludeTmpdirEnvVar: pointer(false), ExcludeSlashTmp: pointer(false),
		}
	}
}

func parseSettings(meta map[string]any) (OpenSettings, error) {
	raw, ok := meta[openSettingsKey]
	if !ok {
		return OpenSettings{}, nil
	}
	var settings OpenSettings
	data, err := json.Marshal(raw)
	if err != nil || string(data) == "null" || json.Unmarshal(data, &settings) != nil {
		return settings, acp.NewInvalidParams("invalid Codex open settings")
	}
	if settings.Mode != "" && settings.Mode != "read-only" && settings.Mode != "agent" && settings.Mode != "agent-full-access" {
		return settings, acp.NewInvalidParams("invalid Codex permission mode")
	}
	if settings.Model != nil && *settings.Model == "" || settings.ReasoningEffort != nil && *settings.ReasoningEffort == "" {
		return settings, acp.NewInvalidParams("model and reasoning effort cannot be empty")
	}
	if tier, ok := settings.ServiceTier.Value(); ok && (tier == "" || tier == "off" || tier == "inherit") {
		return settings, acp.NewInvalidParams("service tier must be a native ID or null")
	}
	return settings, nil
}

func workspaceRoots(cwd string, directories []string) ([]string, error) {
	roots := make([]string, 0, len(directories)+1)
	for _, directory := range append([]string{cwd}, directories...) {
		if !filepath.IsAbs(directory) {
			return nil, acp.NewInvalidParams("Codex workspace directories must be absolute")
		}
		dir := filepath.Clean(directory)
		if !slices.Contains(roots, dir) {
			roots = append(roots, dir)
		}
	}
	return roots, nil
}

func (c *Client) openConfig(ctx context.Context, roots []string, servers []acp.McpServer, settings OpenSettings) (map[string]json.RawMessage, error) {
	config := make(map[string]json.RawMessage, len(c.options.ConfigOverrides)+5)
	for key, val := range c.options.ConfigOverrides {
		config[key] = val
	}
	projects := make(map[string]map[string]string, len(roots))
	for _, root := range roots {
		projects[root] = map[string]string{"trust_level": "trusted"}
	}
	config["projects"], _ = json.Marshal(projects)
	if len(roots) > 1 {
		config["sandbox_workspace_write.writable_roots"], _ = json.Marshal(roots[1:])
	}
	if settings.ReasoningEffort != nil {
		config["model_reasoning_effort"], _ = json.Marshal(*settings.ReasoningEffort)
	}
	if len(servers) > 0 || c.options.DefaultModeRequestUserInput {
		var effective struct {
			Config struct {
				Features map[string]json.RawMessage `json:"features"`
				MCP      map[string]json.RawMessage `json:"mcp_servers"`
			} `json:"config"`
		}
		if err := c.request(ctx, "config/read", map[string]any{"cwd": roots[0], "includeLayers": false}, &effective); err != nil {
			return nil, err
		}
		if _, explicit := effective.Config.Features["default_mode_request_user_input"]; c.options.DefaultModeRequestUserInput && !explicit {
			config["features.default_mode_request_user_input"] = json.RawMessage("true")
		}
		configured := make(map[string]struct{}, len(servers))
		for _, server := range servers {
			name, native, err := mcpConfig(server)
			if err != nil {
				return nil, err
			}
			if _, duplicate := configured[name]; duplicate {
				return nil, acp.NewInvalidParams("duplicate Codex MCP server name")
			}
			configured[name] = struct{}{}
			if _, exists := effective.Config.MCP[name]; exists {
				return nil, acp.NewInvalidParams("Codex MCP server name conflicts with an existing native server")
			}
			config["mcp_servers."+name], _ = json.Marshal(native)
		}
	}
	return config, nil
}

// claimSkills pins the global Codex roots until the native consumer is done.
// Callers acquire after host preparation and release before observer draining.
func (c *Client) claimSkills(ctx context.Context, roots []string) (func(), error) {
	extra := make([]string, 0, len(roots)-1)
	hasSkills := false
	for i, root := range roots {
		path := filepath.Join(root, ".agents", "skills")
		info, err := os.Stat(path)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		if err == nil && !info.IsDir() {
			return nil, errors.New("Codex skill path is not a directory")
		}
		if err == nil {
			hasSkills = true
			if i > 0 {
				extra = append(extra, path)
			}
		}
	}
	for {
		c.mu.Lock()
		if cause := context.Cause(c.life); cause != nil {
			c.mu.Unlock()
			return nil, cause
		}
		if cause := context.Cause(ctx); cause != nil {
			c.mu.Unlock()
			return nil, cause
		}
		if !c.skillChanging && slices.Equal(c.skillRoots, extra) {
			c.skillUsers++
			c.mu.Unlock()
			break
		}
		if !c.skillChanging && c.skillUsers == 0 {
			c.skillChanging = true
			c.mu.Unlock()
			err := c.setSkillRoots(ctx, extra)
			c.mu.Lock()
			c.skillChanging = false
			if err == nil {
				c.skillRoots = extra
				c.skillUsers++
			}
			c.signalSkillsLocked()
			c.mu.Unlock()
			if err != nil {
				return nil, err
			}
			break
		}
		changed := c.skillChanged
		c.mu.Unlock()
		select {
		case <-changed:
		case <-ctx.Done():
			return nil, context.Cause(ctx)
		case <-c.life.Done():
			return nil, context.Cause(c.life)
		}
	}
	var once sync.Once
	release := func() { once.Do(c.releaseSkills) }
	if hasSkills {
		if err := c.request(ctx, protocol.MethodSkillsList, protocol.SkillsListParams{Cwds: roots, ForceReload: pointer(true)}, nil); err != nil {
			release()
			return nil, fmt.Errorf("reload Codex skills: %w", err)
		}
	}
	return release, nil
}

func (c *Client) setSkillRoots(ctx context.Context, extra []string) error {
	written, err := c.call(ctx, protocol.MethodSkillsExtraRootsSet, protocol.SkillsExtraRootsSetParams{ExtraRoots: extra}, nil, nil, nil)
	if err == nil {
		return nil
	}
	var rejected *acp.RequestError
	if written.submission() != agentclient.SubmissionNotSent && !errors.As(err, &rejected) {
		c.fail(errors.New("Codex global skill roots changed without a confirmed result"))
	}
	return fmt.Errorf("configure Codex skill roots: %w", err)
}

func (c *Client) releaseSkills() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.skillUsers--
	if c.skillUsers == 0 {
		c.signalSkillsLocked()
	}
}

func (c *Client) signalSkillsLocked() {
	close(c.skillChanged)
	c.skillChanged = make(chan struct{})
}

func mcpConfig(server acp.McpServer) (string, map[string]any, error) {
	var name string
	native := make(map[string]any)
	switch {
	case server.Stdio != nil && server.Http == nil && server.Sse == nil && server.Acp == nil:
		s := server.Stdio
		name = s.Name
		env := make(map[string]string, len(s.Env))
		for _, entry := range s.Env {
			env[entry.Name] = entry.Value
		}
		if s.Command == "" {
			return "", nil, acp.NewInvalidParams("Codex MCP command is required")
		}
		native["command"], native["args"], native["env"] = s.Command, s.Args, env
	case server.Http != nil && server.Stdio == nil && server.Sse == nil && server.Acp == nil:
		s := server.Http
		name = s.Name
		if s.Url == "" {
			return "", nil, acp.NewInvalidParams("Codex MCP URL is required")
		}
		headers := make(map[string]string, len(s.Headers))
		for _, header := range s.Headers {
			headers[header.Name] = header.Value
		}
		native["url"], native["http_headers"] = s.Url, headers
	default:
		return "", nil, acp.NewInvalidParams("unsupported Codex MCP transport")
	}
	name = strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return '_'
		}
		return r
	}, name)
	if name == "" || strings.ContainsAny(name, ".\n\r") {
		return "", nil, acp.NewInvalidParams("invalid Codex MCP server name")
	}
	return name, native, nil
}

// archivedThreadError recognizes the resume-specific rejection proven by
// rust-v0.163.0 read_stored_thread_for_resume. Both native IDs must be the
// requested thread. Unknown forms leave the original error untouched.
func archivedThreadError(err error, id acp.SessionId) bool {
	var rpc *acp.RequestError
	if !uuidPattern.MatchString(string(id)) || !errors.As(err, &rpc) || rpc.Code != -32600 {
		return false
	}
	want := fmt.Sprintf("session %s is archived. Run `codex unarchive %s` to unarchive it first.", id, id)
	return rpc.Message == want
}

// ThreadArchiveResponse reports the state confirmed by the native operation.
type ThreadArchiveResponse struct {
	// ThreadID names the native thread.
	ThreadID string `json:"thread_id"`
	// Archived is the state reached by the completed native operation.
	Archived bool `json:"archived"`
}

func (c *Client) SetThreadArchived(ctx context.Context, id string, archived bool) (ThreadArchiveResponse, error) {
	if id == "" {
		return ThreadArchiveResponse{}, acp.NewInvalidParams("thread_id is required")
	}
	threadID := acp.SessionId(id)
	c.mu.Lock()
	if err := c.checkReadyLocked(); err != nil {
		c.mu.Unlock()
		return ThreadArchiveResponse{}, err
	}
	s := c.sessions[threadID]
	if c.managing[threadID] || s != nil && (s.active != nil || len(s.draining) > 0 || s.busy || s.closing) {
		c.mu.Unlock()
		return ThreadArchiveResponse{}, errTurnBusy
	}
	if c.managing == nil {
		c.managing = make(map[acp.SessionId]bool)
	}
	c.managing[threadID] = true
	if s != nil {
		s.busy = true
	}
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.managing, threadID)
		if s != nil {
			s.busy = false
		}
		c.mu.Unlock()
	}()
	method := "thread/unarchive"
	if archived {
		method = "thread/archive"
	}
	if err := c.request(ctx, method, map[string]string{"threadId": id}, nil); err != nil {
		return ThreadArchiveResponse{}, err
	}
	if archived && s != nil {
		c.mu.Lock()
		if c.sessions[threadID] == s {
			delete(c.sessions, threadID)
		}
		c.mu.Unlock()
	}
	return ThreadArchiveResponse{ThreadID: id, Archived: archived}, nil
}

func (c *Client) ListSessions(ctx context.Context, r acp.ListSessionsRequest) (acp.ListSessionsResponse, error) {
	if r.Cwd != nil && !filepath.IsAbs(*r.Cwd) {
		return acp.ListSessionsResponse{}, acp.NewInvalidParams("cwd must be absolute")
	}
	c.mu.Lock()
	err := c.checkReadyLocked()
	c.mu.Unlock()
	if err != nil {
		return acp.ListSessionsResponse{}, err
	}
	var reply struct {
		Data []protocol.Thread `json:"data"`
		Next *string           `json:"nextCursor"`
	}
	params := map[string]any{}
	if r.Cwd != nil {
		params["cwd"] = *r.Cwd
	}
	if r.Cursor != nil {
		params["cursor"] = *r.Cursor
	}
	if err := c.request(ctx, "thread/list", params, &reply); err != nil {
		return acp.ListSessionsResponse{}, err
	}
	sessions := make([]acp.SessionInfo, 0, len(reply.Data))
	for _, thread := range reply.Data {
		if thread.ID != "" && filepath.IsAbs(thread.Cwd) {
			info := acp.SessionInfo{SessionId: acp.SessionId(thread.ID), Cwd: thread.Cwd, Title: thread.Name}
			if thread.UpdatedAt > 0 {
				info.UpdatedAt = pointer(time.Unix(thread.UpdatedAt, 0).UTC().Format(time.RFC3339))
			}
			sessions = append(sessions, info)
		}
	}
	return acp.ListSessionsResponse{Sessions: sessions, NextCursor: reply.Next}, nil
}

func (s *session) response() acp.NewSessionResponse {
	return acp.NewSessionResponse{SessionId: s.id, Modes: modeState(s.mode), ConfigOptions: s.configOptions(), Meta: map[string]any{sessionSettingsKey: s.settingsSnapshot()}}
}

func (s *session) settingsSnapshot() SessionSettings {
	snapshot := s.settings
	snapshot.Requested.Model = copyPointer(snapshot.Requested.Model)
	snapshot.Requested.ReasoningEffort = copyPointer(snapshot.Requested.ReasoningEffort)
	snapshot.Observed.Model = copyPointer(snapshot.Observed.Model)
	snapshot.Observed.ReasoningEffort = copyPointer(snapshot.Observed.ReasoningEffort)
	return snapshot
}

func copyPointer[T any](p *T) *T {
	if p == nil {
		return nil
	}
	return pointer(*p)
}

func (c *Client) ReadSessionSettings(_ context.Context, id acp.SessionId) (SessionSettings, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	s, err := c.sessionLocked(id)
	if err != nil {
		return SessionSettings{}, err
	}
	return s.settingsSnapshot(), nil
}

func modeState(mode acp.SessionModeId) *acp.SessionModeState {
	return &acp.SessionModeState{CurrentModeId: mode, AvailableModes: []acp.SessionMode{
		{Id: "read-only", Name: "Read-only"}, {Id: "agent", Name: "Agent"}, {Id: "agent-full-access", Name: "Agent (full access)"},
	}}
}

func (c *Client) SetSessionMode(_ context.Context, r acp.SetSessionModeRequest) (acp.SetSessionModeResponse, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	s, err := c.sessionLocked(r.SessionId)
	if err != nil {
		return acp.SetSessionModeResponse{}, err
	}
	if s.busy || s.closing {
		return acp.SetSessionModeResponse{}, errTurnBusy
	}
	return acp.SetSessionModeResponse{}, s.setConfig("mode", string(r.ModeId))
}

func (c *Client) SetSessionConfigOption(_ context.Context, r acp.SetSessionConfigOptionRequest) (acp.SetSessionConfigOptionResponse, error) {
	if r.ValueId == nil {
		return acp.SetSessionConfigOptionResponse{}, acp.NewInvalidParams("Codex config requires an option value")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	s, err := c.sessionLocked(r.ValueId.SessionId)
	if err != nil {
		return acp.SetSessionConfigOptionResponse{}, err
	}
	if s.busy || s.closing {
		return acp.SetSessionConfigOptionResponse{}, errTurnBusy
	}
	if err := s.setConfig(r.ValueId.ConfigId, string(r.ValueId.Value)); err != nil {
		return acp.SetSessionConfigOptionResponse{}, err
	}
	return acp.SetSessionConfigOptionResponse{ConfigOptions: s.configOptions()}, nil
}

// ConfigureTurn replaces all requested selectors as one validated intent for the next turn.
func (c *Client) ConfigureTurn(_ context.Context, id acp.SessionId, model, effort string, fast *bool) ([]acp.SessionConfigOption, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	s, err := c.idleSessionLocked(id)
	if err != nil {
		return nil, err
	}
	target := model
	if target == "" {
		target = value(s.settings.Observed.Model)
	}
	catalog := lookupModel(s.models, target)
	if model != "" && catalog == nil && model != value(s.settings.Observed.Model) {
		return nil, acp.NewInvalidParams("Codex model is unavailable")
	}
	var settings OpenSettings
	if model != "" {
		settings.Model = pointer(model)
	}
	if effort != "" {
		if !supportsEffort(catalog, effort) {
			return nil, acp.NewInvalidParams("Codex reasoning effort is unavailable for the model")
		}
		settings.ReasoningEffort = pointer(effort)
	}
	if fast != nil {
		tier := fastTier(catalog)
		if tier == "" {
			return nil, acp.NewInvalidParams("Codex Fast is unavailable for the model")
		}
		if *fast {
			settings.ServiceTier = protocol.NewOptionalValue(tier)
		} else {
			settings.ServiceTier = protocol.NewOptionalNull[string]()
		}
	}
	s.settings.Requested = settings
	return s.configOptions(), nil
}

func (s *session) currentModel() string {
	if s.settings.Requested.Model != nil {
		return *s.settings.Requested.Model
	}
	return value(s.settings.Observed.Model)
}

func (s *session) setConfig(id acp.SessionConfigId, selected string) error {
	model := lookupModel(s.models, s.currentModel())
	switch id {
	case "mode":
		if selected == "agent" || selected == "read-only" || selected == "agent-full-access" {
			s.mode = acp.SessionModeId(selected)
			return nil
		}
	case "model":
		target := lookupModel(s.models, selected)
		if target != nil || selected == s.currentModel() {
			if target != nil && s.settings.Requested.ReasoningEffort != nil && !supportsEffort(target, *s.settings.Requested.ReasoningEffort) {
				break
			}
			if tier, ok := s.settings.Requested.ServiceTier.Value(); ok && !supportsTier(target, tier) {
				break
			}
			s.settings.Requested.Model = pointer(selected)
			return nil
		}
	case "reasoning_effort":
		if supportsEffort(model, selected) {
			s.settings.Requested.ReasoningEffort = pointer(selected)
			return nil
		}
	case "service_tier":
		switch selected {
		case "inherit":
			s.settings.Requested.ServiceTier = protocol.OptionalNullable[string]{}
			return nil
		case "off":
			s.settings.Requested.ServiceTier = protocol.NewOptionalNull[string]()
			return nil
		}
		if supportsTier(model, selected) {
			s.settings.Requested.ServiceTier = protocol.NewOptionalValue(selected)
			return nil
		}
	}
	return acp.NewInvalidParams(fmt.Sprintf("unsupported Codex value %q for %s", selected, id))
}

func (s *session) hasPendingOutcome() bool {
	if s.active != nil && s.active.retainOutcome {
		return true
	}
	for t := range s.draining {
		if t.retainOutcome {
			return true
		}
	}
	return false
}

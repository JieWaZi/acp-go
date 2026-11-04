// Package claude is a direct client for the Claude Code stream-json protocol.
package claude

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/JieWaZi/acp-go/pkg/acpmeta"
	agentclient "github.com/JieWaZi/acp-go/pkg/agent_next"
	acp "github.com/JieWaZi/acp-go/sdk"
)

var (
	errClosed       = errors.New("Claude session is closed")
	errBusy         = errors.New("Claude session has admitted work")
	errStale        = errors.New("Claude native target has changed")
	errCapacity     = errors.New("Claude native outcome capacity exhausted")
	errInterrupting = errors.New("Claude native target is retiring after cancellation")
	errTransport    = errors.New("Claude stream disconnected")
)

const (
	defaultControlTimeout     = 8 * time.Second
	defaultObservationTimeout = 2 * time.Second
	startupTimeout            = 30 * time.Second
	maxSessions               = 128
)

// Options freezes the account, CLI and provider-specific launch inputs.
type Options struct {
	// Executable selects Claude Code, defaulting to claude on the frozen PATH.
	Executable string
	// PrefixArgs precedes the Claude arguments. Native launch controls are reserved.
	PrefixArgs []string
	// Environment is the full child environment; nil snapshots the current environment.
	Environment []string
	// WorkingDirectory is the real directory for account and version operations.
	WorkingDirectory string
	// Logger receives sanitized process diagnostics.
	Logger *slog.Logger
	// Launch contains native session constraints and resources.
	Launch LaunchOptions
	// ControlTimeout bounds native write, cancellation and control operations.
	ControlTimeout time.Duration
	// ObservationTimeout bounds one turn's host observation drain.
	ObservationTimeout time.Duration
}

// Client owns registered Claude generations, management processes and outcome ledgers.
type Client struct {
	// options and host are frozen when the client is constructed.
	options Options
	host    agentclient.HostCallbacks
	// ctx is the lifetime of this client, independent of any request.
	ctx    context.Context
	cancel context.CancelFunc
	// mu protects root admission and all registered owners.
	mu                 sync.Mutex
	closing            bool
	initialized        bool
	initializing       bool
	version            string
	forms              bool
	foregroundReceipts bool
	// sessions includes partial opens and owners whose release must be retried.
	sessions map[acp.SessionId]*session
	ledgers  map[acp.SessionId]*outcomeLedger
	// operations hold a stable session ID across open, load, fork and close.
	operations map[acp.SessionId]struct{}
	// management holds all children started outside session operation.
	management map[*agentclient.Process]struct{}
	// changed broadcasts registry and management transitions.
	changed chan struct{}
}

var (
	_ agentclient.Client              = (*Client)(nil)
	_ agentclient.SteeringClient      = (*Client)(nil)
	_ agentclient.CompactingClient    = (*Client)(nil)
	_ agentclient.NativeOutcomeReader = (*Client)(nil)
	_ acp.AgentLoader                 = (*Client)(nil)
)

// NewClient validates and freezes startup inputs without launching native work.
func NewClient(ctx context.Context, options Options, host agentclient.HostCallbacks) (*Client, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if options.Executable == "" {
		options.Executable = "claude"
	}
	if options.Environment == nil {
		options.Environment = os.Environ()
	}
	options.Environment = slices.Clone(options.Environment)
	options.PrefixArgs = slices.Clone(options.PrefixArgs)
	if err := validateLaunchPrefix(options.PrefixArgs); err != nil {
		return nil, err
	}
	dir, err := filepath.Abs(options.WorkingDirectory)
	if err != nil {
		return nil, err
	}
	options.WorkingDirectory = dir
	if options.ControlTimeout <= 0 {
		options.ControlTimeout = defaultControlTimeout
	}
	if options.ObservationTimeout <= 0 {
		options.ObservationTimeout = defaultObservationTimeout
	}
	options.Launch, err = freezeLaunch(options.Launch)
	if err != nil {
		return nil, err
	}
	frozen, err := agentclient.NewProcess(agentclient.ProcessSpec{
		Executable: options.Executable, Env: options.Environment, Dir: options.WorkingDirectory,
	})
	if err != nil {
		return nil, err
	}
	options.Executable = frozen.Executable()
	lifetime, cancel := context.WithCancel(context.WithoutCancel(ctx))
	return &Client{options: options, host: host, ctx: lifetime, cancel: cancel,
		sessions: make(map[acp.SessionId]*session), operations: make(map[acp.SessionId]struct{}),
		ledgers: make(map[acp.SessionId]*outcomeLedger), management: make(map[*agentclient.Process]struct{}), changed: make(chan struct{}),
	}, nil
}

func (c *Client) signalLocked() {
	close(c.changed)
	c.changed = make(chan struct{})
}

func (c *Client) requireReadyLocked() error {
	if c.closing {
		return errClosed
	}
	if !c.initialized {
		return acp.NewInvalidRequest(map[string]any{"message": "Claude client is not initialized"})
	}
	return nil
}

func (c *Client) getSession(id acp.SessionId) (*session, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.requireReadyLocked(); err != nil {
		return nil, err
	}
	s := c.sessions[id]
	if s == nil {
		return nil, acp.NewInvalidParams(map[string]any{"message": "Claude session not found"})
	}
	return s, nil
}

func (c *Client) beginSessionOperation(id acp.SessionId) (func(), error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.requireReadyLocked(); err != nil {
		return nil, err
	}
	if _, found := c.operations[id]; found {
		return nil, errBusy
	}
	c.operations[id] = struct{}{}
	return func() {
		c.mu.Lock()
		delete(c.operations, id)
		c.signalLocked()
		c.mu.Unlock()
	}, nil
}

// Initialize checks the selected executable and negotiates capabilities once.
func (c *Client) Initialize(ctx context.Context, req acp.InitializeRequest) (acp.InitializeResponse, error) {
	c.mu.Lock()
	if c.closing {
		c.mu.Unlock()
		return acp.InitializeResponse{}, errClosed
	}
	if c.initialized {
		response := c.capabilitiesLocked()
		c.mu.Unlock()
		return response, nil
	}
	if c.initializing {
		c.mu.Unlock()
		return acp.InitializeResponse{}, errBusy
	}
	c.initializing = true
	c.mu.Unlock()

	data, err := c.command(ctx, startupTimeout, "--version")
	version := strings.Fields(string(data))
	if err == nil && (len(version) == 0 || !claudeVersion.MatchString(version[0])) {
		err = errors.New("Claude did not report a supported CLI version")
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	c.initializing = false
	c.signalLocked()
	if err != nil {
		return acp.InitializeResponse{}, err
	}
	if c.closing {
		return acp.InitializeResponse{}, errClosed
	}
	if err := ctx.Err(); err != nil {
		return acp.InitializeResponse{}, err
	}
	c.version = version[0]
	c.forms = req.ClientCapabilities.Elicitation != nil && req.ClientCapabilities.Elicitation.Form != nil
	// Legacy ACP callers do not read or acknowledge foreground receipts.
	// Direct hosts negotiate retention. Detached and autonomous outcomes
	// always remain available even if foreground retention is disabled.
	c.foregroundReceipts, _ = req.ClientCapabilities.Meta[acpmeta.NativeTurnReceiptsCapability].(bool)
	c.initialized = true
	return c.capabilitiesLocked(), nil
}

func (c *Client) capabilitiesLocked() acp.InitializeResponse {
	fork := c.forkMode()
	title := "Claude Code"
	return acp.InitializeResponse{
		ProtocolVersion: acp.ProtocolVersionNumber,
		AgentInfo:       &acp.Implementation{Name: "claude", Title: &title, Version: c.version, Meta: acpmeta.RuntimeVersionMetadata(c.version)},
		AuthMethods:     c.authMethods(),
		AgentCapabilities: acp.AgentCapabilities{
			LoadSession: true, Auth: c.authCapabilities(),
			PromptCapabilities: acp.PromptCapabilities{Image: true, EmbeddedContext: true},
			McpCapabilities:    acp.McpCapabilities{Http: true, Sse: true},
			SessionCapabilities: acp.SessionCapabilities{
				AdditionalDirectories: &acp.SessionAdditionalDirectoriesCapabilities{},
				Close:                 &acp.SessionCloseCapabilities{}, Resume: &acp.SessionResumeCapabilities{},
				Fork: acpmeta.ForkCapability(fork),
			},
		},
		Meta: map[string]any{
			"steering":              map[string]any{"supported": true},
			"developerInstructions": c.instructionCapability(),
			"nativeTurn":            map[string]any{"notificationMetadata": "acp-go/native-turn", "backgroundMetadata": "acp-go/native-background"},
			"modelCatalog":          acpmeta.ModelCatalogMethod, "accountUsage": acpmeta.AccountUsageMethod,
			acpmeta.NativeTurnReceiptsCapability: acpmeta.NativeTurnReceiptsMethod,
			"fork":                               map[string]any{"mode": fork},
		},
	}
}

// ListSessions reports that the Claude stream protocol lacks this ACP operation.
func (c *Client) ListSessions(context.Context, acp.ListSessionsRequest) (acp.ListSessionsResponse, error) {
	return acp.ListSessionsResponse{}, acp.NewMethodNotFound("session/list")
}

// NewSession registers a fresh native session after its handshake succeeds.
func (c *Client) NewSession(ctx context.Context, req acp.NewSessionRequest) (acp.NewSessionResponse, error) {
	id, err := newUUID()
	if err != nil {
		return acp.NewSessionResponse{}, err
	}
	s, err := c.open(ctx, openRequest{id: acp.SessionId(id), cwd: req.Cwd, additional: req.AdditionalDirectories, mcp: req.McpServers, meta: req.Meta})
	if err != nil {
		return acp.NewSessionResponse{}, err
	}
	return s.response(), nil
}

// ResumeSession resumes the requested native ID without replaying history.
func (c *Client) ResumeSession(ctx context.Context, req acp.ResumeSessionRequest) (acp.ResumeSessionResponse, error) {
	s, err := c.open(ctx, openRequest{id: req.SessionId, cwd: req.Cwd, additional: req.AdditionalDirectories, mcp: req.McpServers, meta: req.Meta, resume: true})
	if err != nil {
		return acp.ResumeSessionResponse{}, err
	}
	r := s.response()
	return acp.ResumeSessionResponse{ConfigOptions: r.ConfigOptions, Modes: r.Modes, Meta: r.Meta}, nil
}

// LoadSession replays saved native history before publishing the resumed session.
func (c *Client) LoadSession(ctx context.Context, req acp.LoadSessionRequest) (acp.LoadSessionResponse, error) {
	s, err := c.open(ctx, openRequest{id: req.SessionId, cwd: req.Cwd, additional: req.AdditionalDirectories, mcp: req.McpServers, meta: req.Meta, resume: true, replay: true})
	if err != nil {
		return acp.LoadSessionResponse{}, err
	}
	r := s.response()
	return acp.LoadSessionResponse{ConfigOptions: r.ConfigOptions, Modes: r.Modes, Meta: r.Meta}, nil
}

// CloseSession seals the exact native generation and retains any unreleased owner.
func (c *Client) CloseSession(ctx context.Context, req acp.CloseSessionRequest) (acp.CloseSessionResponse, error) {
	finish, err := c.beginSessionOperation(req.SessionId)
	if err != nil {
		return acp.CloseSessionResponse{}, err
	}
	defer finish()
	c.mu.Lock()
	s := c.sessions[req.SessionId]
	c.mu.Unlock()
	if s == nil {
		return acp.CloseSessionResponse{}, nil
	}
	return acp.CloseSessionResponse{}, c.release(ctx, s)
}

func (c *Client) release(ctx context.Context, s *session) error {
	if err := s.close(ctx); err != nil {
		return err
	}
	c.mu.Lock()
	if c.sessions[s.id] == s {
		delete(c.sessions, s.id)
		if s.ledger.empty() {
			delete(c.ledgers, s.id)
		}
		c.signalLocked()
	}
	c.mu.Unlock()
	return nil
}

// Close seals root admission and releases every owner. A failed release is retryable.
func (c *Client) Close(ctx context.Context) error {
	c.mu.Lock()
	c.closing = true
	c.cancel()
	c.mu.Unlock()
	for {
		c.mu.Lock()
		if len(c.operations) == 0 && !c.initializing {
			c.mu.Unlock()
			break
		}
		change := c.changed
		c.mu.Unlock()
		if err := await(ctx, change); err != nil {
			return err
		}
	}
	c.mu.Lock()
	sessions := make([]*session, 0, len(c.sessions))
	for _, s := range c.sessions {
		sessions = append(sessions, s)
	}
	children := make([]*agentclient.Process, 0, len(c.management))
	for p := range c.management {
		children = append(children, p)
	}
	c.mu.Unlock()
	var err error
	for _, s := range sessions {
		err = errors.Join(err, c.release(ctx, s))
	}
	for _, p := range children {
		err = errors.Join(err, c.releaseProcess(ctx, p))
	}
	return err
}

func (c *Client) process(args []string, cwd string, environment []string) (*agentclient.Process, error) {
	return agentclient.NewProcess(agentclient.ProcessSpec{
		Executable: c.options.Executable, Env: environment, Dir: cwd,
		Args: append(slices.Clone(c.options.PrefixArgs), args...), Redact: c.redactDiagnostic,
	})
}

func await(ctx context.Context, done <-chan struct{}) error {
	select {
	case <-done:
		return nil
	default:
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func newUUID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("allocating Claude identity: %w", err)
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:]), nil
}

func isUUID(id string) bool {
	if len(id) != 36 || id[8] != '-' || id[13] != '-' || id[18] != '-' || id[23] != '-' {
		return false
	}
	raw, err := hex.DecodeString(strings.ReplaceAll(id, "-", ""))
	return err == nil && len(raw) == 16
}

func (c *Client) redactDiagnostic(record string) string {
	for _, key := range []string{"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "CLAUDE_CODE_OAUTH_TOKEN"} {
		if value := c.environment(key); value != "" && strings.Contains(record, value) {
			return ""
		}
	}
	return agentclient.RedactProcessDiagnostic(record)
}

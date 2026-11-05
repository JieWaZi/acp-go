// Package pi runs Pi directly over its native JSONL RPC interface.
package pi

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/JieWaZi/acp-go/pkg/acpmeta"
	agentclient "github.com/JieWaZi/acp-go/pkg/agent_next"
	acp "github.com/JieWaZi/acp-go/sdk"
)

// Options freezes a single Pi account and the launch policy used by its sessions.
type Options struct {
	// Executable selects the Pi binary. An empty value resolves pi in Environment.
	Executable string
	// PrefixArgs precedes the owned native RPC arguments.
	PrefixArgs []string
	// Environment is the complete child environment. Nil snapshots the host.
	Environment []string
	// WorkingDirectory selects the model-discovery and account root base directory.
	WorkingDirectory string
	// MCPModulePath is the installed Pi extension bridge module.
	MCPModulePath string
	// PermissionMode selects default, auto, or full-access for this launch policy.
	PermissionMode string
	// Logger receives redacted operational diagnostics.
	Logger *slog.Logger
}

// Client owns one Pi process per exact native session file.
type Client struct {
	// options freezes the account environment and launch policy.
	options Options
	// host receives scoped events and owns interaction responses.
	host agentclient.HostCallbacks
	// openGate serializes native session creation, restoration, and discovery.
	openGate chan struct{}

	// mu protects registration, closure, and receipt accounting.
	mu sync.Mutex
	// initialized marks the client ready for native operations.
	initialized bool
	// version is the version reported by the selected Pi executable.
	version string
	// closing withdraws admission before process teardown.
	closing bool
	// sessions indexes published sessions by their ACP identities.
	sessions map[acp.SessionId]*session
	// processes retains starting, live, and incompletely cleaned process owners.
	processes map[*session]struct{}
	// foregroundReceipts is negotiated by hosts that acknowledge results.
	foregroundReceipts bool
	// retainedOutcomes counts retained receipts and in-flight reservations.
	retainedOutcomes int
	// ledger retains receipts across process shutdown until acknowledgment.
	ledger map[acp.SessionId]*outcomeLedger
}

type session struct {
	// client supplies frozen launch policy and host callbacks.
	client *Client
	// id is the published ACP session identity.
	id acp.SessionId
	// file is the confirmed native JSONL transcript path.
	file string
	// cwd is the bound working directory.
	cwd string
	// directory holds this process's temporary instructions and extension.
	directory string
	// rpc owns this session's native process and transport.
	rpc *rpc

	// mu protects operation admission and the current prompt owner.
	mu sync.Mutex
	// busy reserves the session for one foreground operation.
	busy bool
	// closing prevents admission during teardown.
	closing bool
	// turn captures the prompt that an incoming cancellation may target.
	turn *invocation
}

type outcomeLedger struct {
	// changed wakes waiters after a receipt or producer transition.
	changed chan struct{}
	// outcomes stores unacknowledged results in completion order.
	outcomes []agentclient.NativeOutcome
	// producers counts prompt continuations that may still publish a result.
	producers int
}

var _ agentclient.Client = (*Client)(nil)
var _ agentclient.NativeOutcomeReader = (*Client)(nil)

// NewClient freezes the launch without starting the CLI or reading credentials.
func NewClient(ctx context.Context, options Options, host agentclient.HostCallbacks) (*Client, error) {
	if err := context.Cause(ctx); err != nil {
		return nil, err
	}
	if options.Executable == "" {
		options.Executable = "pi"
	}
	if options.PermissionMode == "" {
		options.PermissionMode = "default"
	}
	switch options.PermissionMode {
	case "default", "auto", "full-access":
	default:
		return nil, errors.New("unsupported Pi permission policy")
	}
	if options.MCPModulePath != "" && !filepath.IsAbs(options.MCPModulePath) {
		return nil, errors.New("Pi MCP bridge path must be absolute")
	}
	if options.PermissionMode == "auto" && options.MCPModulePath == "" {
		return nil, errors.New("Pi automatic approval requires the managed bridge")
	}
	options.PrefixArgs = slices.Clone(options.PrefixArgs)
	options.Environment = slices.Clone(options.Environment)
	if options.Environment == nil {
		options.Environment = os.Environ()
	}
	cwd, err := filepath.Abs(options.WorkingDirectory)
	if err != nil {
		return nil, err
	}
	options.WorkingDirectory = cwd
	process, err := agentclient.NewProcess(agentclient.ProcessSpec{Executable: options.Executable, Dir: cwd, Env: options.Environment})
	if err != nil {
		return nil, err
	}
	options.Executable = process.Executable()
	return &Client{options: options, host: host, openGate: make(chan struct{}, 1), sessions: make(map[acp.SessionId]*session), processes: make(map[*session]struct{}), ledger: make(map[acp.SessionId]*outcomeLedger)}, nil
}

var versionPattern = regexp.MustCompile(`^v?[0-9]+\.[0-9]+\.[0-9]+([-+][a-zA-Z0-9.+-]+)?$`)

func (c *Client) Initialize(ctx context.Context, request acp.InitializeRequest) (acp.InitializeResponse, error) {
	select {
	case c.openGate <- struct{}{}:
	case <-ctx.Done():
		return acp.InitializeResponse{}, context.Cause(ctx)
	}
	defer func() { <-c.openGate }()
	c.mu.Lock()
	already, closing := c.initialized, c.closing
	c.mu.Unlock()
	if closing {
		return acp.InitializeResponse{}, errors.New("Pi client is closed")
	}
	if !already {
		version, err := c.readVersion(ctx)
		if err != nil {
			return acp.InitializeResponse{}, err
		}
		c.mu.Lock()
		if c.closing {
			c.mu.Unlock()
			return acp.InitializeResponse{}, errors.New("Pi client is closed")
		}
		c.version = version
		c.initialized = true
		c.foregroundReceipts, _ = request.ClientCapabilities.Meta[acpmeta.NativeTurnReceiptsCapability].(bool)
		c.mu.Unlock()
	}
	version := c.version
	fork := acpmeta.VerifiedForkMode(version, "0.85.1", acpmeta.ForkLatest)
	meta := acpmeta.ForkMetadata(fork)
	meta["developerInstructions"] = acpmeta.DeveloperInstructionsCapability(fork != acpmeta.ForkUnsupported)
	meta[acpmeta.NativeTurnReceiptsCapability] = acpmeta.NativeTurnReceiptsMethod
	meta["modelCatalog"] = acpmeta.ModelCatalogMethod
	meta["compact"] = acpmeta.CompactMethod
	name := "Pi"
	return acp.InitializeResponse{
		ProtocolVersion: acp.ProtocolVersionNumber,
		Meta:            meta,
		AuthMethods:     c.authMethods(),
		AgentInfo:       &acp.Implementation{Name: "pi", Title: &name, Version: version, Meta: acpmeta.RuntimeVersionMetadata(version)},
		AgentCapabilities: acp.AgentCapabilities{
			LoadSession:        true,
			PromptCapabilities: acp.PromptCapabilities{Image: true, EmbeddedContext: true},
			McpCapabilities:    acp.McpCapabilities{Http: c.options.MCPModulePath != "", Sse: c.options.MCPModulePath != ""},
			SessionCapabilities: acp.SessionCapabilities{
				Close: &acp.SessionCloseCapabilities{}, List: &acp.SessionListCapabilities{},
				Resume: &acp.SessionResumeCapabilities{}, Fork: acpmeta.ForkCapability(fork),
				Delete:                &acp.SessionDeleteCapabilities{},
				AdditionalDirectories: &acp.SessionAdditionalDirectoriesCapabilities{},
			},
		},
	}, nil
}

// readVersion uses the same contained process owner and keeps failed cleanup
// registered for Close. A wrapper that omits version output can still speak RPC.
func (c *Client) readVersion(ctx context.Context) (string, error) {
	spec := c.processSpec(c.options.WorkingDirectory, nil, nil)
	spec.Args = append(slices.Clone(c.options.PrefixArgs), "--version")
	p, err := agentclient.NewProcess(spec)
	if err != nil {
		return "", err
	}
	owner := &session{client: c, rpc: newRPC(p)}
	c.mu.Lock()
	if c.closing {
		c.mu.Unlock()
		return "", errors.New("Pi client is closed")
	}
	c.processes[owner] = struct{}{}
	c.mu.Unlock()
	probe, stop := context.WithTimeout(ctx, 2*time.Second)
	defer stop()
	if err := p.Start(probe); err != nil {
		return "", errors.Join(err, c.retire(owner))
	}
	data := make(chan string, 1)
	go func() {
		raw, _ := io.ReadAll(io.LimitReader(p.Stdout(), 256))
		data <- strings.TrimSpace(string(raw))
	}()
	var version string
	select {
	case version = <-data:
		if exit, err := p.Wait(probe); err != nil || exit.Err != nil || !versionPattern.MatchString(version) {
			version = ""
		}
	case <-probe.Done():
	}
	cleanup := c.retire(owner)
	if err := context.Cause(ctx); err != nil {
		return "", errors.Join(err, cleanup)
	}
	if version == "" && c.options.Logger != nil {
		c.options.Logger.Debug("Pi native version probe did not return a version")
	}
	return version, cleanup
}

func (c *Client) authMethods() []acp.AuthMethod {
	env := make(map[string]any)
	if root := c.getenv("PI_CODING_AGENT_DIR"); root != "" {
		env["PI_CODING_AGENT_DIR"] = root
	}
	return []acp.AuthMethod{{Terminal: &acp.AuthMethodTerminalInline{
		Id: "pi_terminal_login", Name: "Configure Pi credentials", Args: []string{}, Env: env,
		Meta: map[string]any{"terminal-auth": map[string]any{"command": c.options.Executable, "args": []string{}, "label": "Launch Pi", "env": env}},
	}}}
}

func (*Client) Authenticate(context.Context, acp.AuthenticateRequest) (acp.AuthenticateResponse, error) {
	return acp.AuthenticateResponse{}, acp.NewMethodNotFound("authenticate")
}

func (*Client) Logout(context.Context, acp.LogoutRequest) (acp.LogoutResponse, error) {
	return acp.LogoutResponse{}, acp.NewMethodNotFound("logout")
}

func (c *Client) UserInputRequiresReady() bool { return c.options.MCPModulePath != "" }

func (c *Client) processSpec(cwd string, args, environment []string) agentclient.ProcessSpec {
	return agentclient.ProcessSpec{
		Executable: c.options.Executable, Dir: cwd,
		Args: append(append(slices.Clone(c.options.PrefixArgs), "--mode", "rpc"), args...),
		Env:  append(slices.Clone(c.options.Environment), environment...),
	}
}

func (c *Client) lockOpen(ctx context.Context) (func(), error) {
	select {
	case c.openGate <- struct{}{}:
	case <-ctx.Done():
		return nil, context.Cause(ctx)
	}
	unlock := func() { <-c.openGate }
	c.mu.Lock()
	ready := c.initialized && !c.closing
	c.mu.Unlock()
	if !ready {
		unlock()
		return nil, errors.New("Pi client is not ready")
	}
	return unlock, nil
}

func (c *Client) start(ctx context.Context, cwd string, args []string, resources resources, file string) (*session, error) {
	if err := validateRoots(cwd, resources.directories); err != nil {
		return nil, err
	}
	directory, extra, environment, err := c.prepareResources(resources)
	if err != nil {
		return nil, err
	}
	if file != "" {
		args = append(args, "--session", file)
	}
	p, err := agentclient.NewProcess(c.processSpec(cwd, append(args, extra...), environment))
	if err != nil {
		_ = os.RemoveAll(directory)
		return nil, err
	}
	s := &session{client: c, cwd: cwd, directory: directory, rpc: newRPC(p)}
	c.mu.Lock()
	if c.closing {
		c.mu.Unlock()
		_ = os.RemoveAll(directory)
		return nil, errors.New("Pi client is closed")
	}
	c.processes[s] = struct{}{}
	c.mu.Unlock()
	if err := p.Start(ctx); err != nil {
		return nil, errors.Join(err, c.retire(s))
	}
	go s.rpc.read()
	if slices.Contains(extra, "--extension") {
		select {
		case <-s.rpc.ready:
		case <-s.rpc.done:
			return nil, errors.Join(s.rpc.err(), c.retire(s))
		case <-ctx.Done():
			return nil, errors.Join(context.Cause(ctx), c.retire(s))
		}
	}
	return s, nil
}

func (c *Client) open(ctx context.Context, cwd string, id acp.SessionId, file string, resources resources) (*session, []acp.SessionConfigOption, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	s, err := c.start(ctx, cwd, nil, resources, file)
	if err != nil {
		return nil, nil, err
	}
	state, err := s.state(ctx)
	if err == nil {
		err = s.bindFile(ctx, state, file)
	}
	if err == nil && id != "" && !filepath.IsAbs(string(id)) && id != acp.SessionId(state.SessionID) {
		err = errors.New("Pi resumed a different native session ID")
	}
	if err != nil {
		return nil, nil, errors.Join(err, c.retire(s))
	}
	if id == "" {
		id = acp.SessionId(s.file)
	}
	options, err := s.configuration(ctx)
	if err != nil {
		return nil, nil, errors.Join(err, c.retire(s))
	}
	c.mu.Lock()
	duplicate := false
	for _, opened := range c.sessions {
		duplicate = duplicate || samePath(opened.file, s.file)
	}
	if c.closing || duplicate {
		c.mu.Unlock()
		return nil, nil, errors.Join(errors.New("Pi session is already open or closing"), c.retire(s))
	}
	s.id = id
	c.sessions[s.id] = s
	if c.ledger[s.id] == nil {
		c.ledger[s.id] = &outcomeLedger{changed: make(chan struct{})}
	}
	c.mu.Unlock()
	return s, options, nil
}

func (s *session) bindFile(ctx context.Context, state nativeState, requested string) error {
	file := state.SessionFile
	nativeID := state.SessionID
	if !s.client.containsHistory(s.cwd, file) || state.SessionID == "" || (requested != "" && !samePath(file, requested)) {
		return errors.New("Pi did not confirm the requested native session file")
	}
	if _, err := os.Stat(file); errors.Is(err, os.ErrNotExist) && requested == "" {
		// Pi defers writing until the first message. Its native switch initializes
		// a fresh file while retaining the already confirmed path.
		if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
			return errors.New("Pi session directory could not be created")
		}
		placeholder, err := os.OpenFile(file, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return errors.New("Pi session file could not be reserved")
		}
		if err := placeholder.Close(); err != nil {
			return err
		}
		var result struct {
			// Cancelled indicates that a native extension refused the switch.
			Cancelled bool
		}
		if err := s.rpc.call(ctx, "switch_session", map[string]any{"sessionPath": file}, &result); err != nil {
			return err
		}
		confirmed, err := s.state(ctx)
		if err != nil {
			return err
		}
		if result.Cancelled || !samePath(confirmed.SessionFile, file) || confirmed.SessionID == "" {
			return errors.New("Pi did not persist the new native session")
		}
		nativeID = confirmed.SessionID
	} else if err != nil {
		return errors.New("Pi native session file is unavailable")
	}
	item, err := sessionHeader(file)
	if err != nil || item.ID != nativeID || !samePath(item.Cwd, s.cwd) {
		return errors.New("Pi native session header did not match the opened session")
	}
	s.file = filepath.Clean(file)
	return nil
}

func (c *Client) NewSession(ctx context.Context, r acp.NewSessionRequest) (acp.NewSessionResponse, error) {
	unlock, err := c.lockOpen(ctx)
	if err != nil {
		return acp.NewSessionResponse{}, err
	}
	defer unlock()
	config, err := openResources(r.Meta, r.AdditionalDirectories, r.McpServers)
	if err != nil {
		return acp.NewSessionResponse{}, err
	}
	s, options, err := c.open(ctx, r.Cwd, "", "", config)
	if err != nil {
		return acp.NewSessionResponse{}, err
	}
	return acp.NewSessionResponse{SessionId: s.id, ConfigOptions: options}, nil
}

func (c *Client) resume(ctx context.Context, cwd string, id acp.SessionId, config resources) ([]acp.SessionConfigOption, error) {
	unlock, err := c.lockOpen(ctx)
	if err != nil {
		return nil, err
	}
	defer unlock()
	file, err := c.findSession(ctx, id, cwd)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	for _, opened := range c.sessions {
		if samePath(opened.file, file) {
			c.mu.Unlock()
			return nil, errors.New("Pi session is already open; close it before preparing it again")
		}
	}
	c.mu.Unlock()
	_, options, err := c.open(ctx, cwd, id, file, config)
	return options, err
}

func (c *Client) ResumeSession(ctx context.Context, r acp.ResumeSessionRequest) (acp.ResumeSessionResponse, error) {
	config, err := openResources(r.Meta, r.AdditionalDirectories, r.McpServers)
	if err != nil {
		return acp.ResumeSessionResponse{}, err
	}
	options, err := c.resume(ctx, r.Cwd, r.SessionId, config)
	return acp.ResumeSessionResponse{ConfigOptions: options}, err
}

func (c *Client) LoadSession(ctx context.Context, r acp.LoadSessionRequest) (acp.LoadSessionResponse, error) {
	config, err := openResources(r.Meta, r.AdditionalDirectories, r.McpServers)
	if err != nil {
		return acp.LoadSessionResponse{}, err
	}
	options, err := c.resume(ctx, r.Cwd, r.SessionId, config)
	if err != nil {
		return acp.LoadSessionResponse{}, err
	}
	s, err := c.session(r.SessionId)
	if err != nil {
		return acp.LoadSessionResponse{}, err
	}
	if err := s.replay(ctx); err != nil {
		return acp.LoadSessionResponse{}, errors.Join(err, c.retire(s))
	}
	return acp.LoadSessionResponse{ConfigOptions: options}, nil
}

func (c *Client) session(id acp.SessionId) (*session, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := c.sessions[id]
	if s == nil || c.closing {
		return nil, missingSession()
	}
	return s, nil
}

func (s *session) begin() (func(), error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing || s.rpc.err() != nil {
		return nil, errors.New("Pi session has closed")
	}
	if s.busy {
		return nil, errors.New("Pi session has an active operation")
	}
	s.busy = true
	return func() {
		s.mu.Lock()
		s.busy = false
		s.mu.Unlock()
	}, nil
}

func (c *Client) CloseSession(ctx context.Context, r acp.CloseSessionRequest) (acp.CloseSessionResponse, error) {
	c.mu.Lock()
	s := c.sessions[r.SessionId]
	c.mu.Unlock()
	if s == nil {
		return acp.CloseSessionResponse{}, nil
	}
	return acp.CloseSessionResponse{}, c.release(ctx, s)
}

func (c *Client) retire(s *session) error {
	ctx, cancel := context.WithTimeout(context.Background(), controlTimeout)
	defer cancel()
	return c.release(ctx, s)
}

func (c *Client) release(ctx context.Context, s *session) error {
	s.mu.Lock()
	s.closing = true
	if s.turn != nil {
		s.turn.cancel(context.Canceled)
	}
	s.mu.Unlock()
	if err := s.rpc.process.Close(ctx); err != nil {
		return err
	}
	if s.directory != "" {
		if err := os.RemoveAll(s.directory); err != nil {
			return errors.New("Pi resource cleanup failed")
		}
	}
	c.mu.Lock()
	delete(c.processes, s)
	if c.sessions[s.id] == s {
		delete(c.sessions, s.id)
	}
	if ledger := c.ledger[s.id]; ledger != nil {
		if ledger.producers == 0 && len(ledger.outcomes) == 0 {
			delete(c.ledger, s.id)
		}
		signalLedger(ledger)
	}
	c.mu.Unlock()
	return nil
}

func (c *Client) Close(ctx context.Context) error {
	c.mu.Lock()
	c.closing = true
	processes := make([]*session, 0, len(c.processes))
	for s := range c.processes {
		processes = append(processes, s)
	}
	c.mu.Unlock()
	var result error
	for _, s := range processes {
		result = errors.Join(result, c.release(ctx, s))
	}
	return result
}

func (c *Client) getenv(key string) string {
	for index := len(c.options.Environment) - 1; index >= 0; index-- {
		name, value, _ := strings.Cut(c.options.Environment[index], "=")
		if name == key {
			return value
		}
	}
	return ""
}

func validateRoots(cwd string, additional []string) error {
	for _, root := range append([]string{cwd}, additional...) {
		if !filepath.IsAbs(root) {
			return acp.NewInvalidParams(map[string]any{"message": "Pi workspace roots must be absolute"})
		}
		info, err := os.Stat(root)
		if err != nil || !info.IsDir() {
			return acp.NewInvalidParams(map[string]any{"message": "Pi workspace root is unavailable"})
		}
	}
	return nil
}

type classifiedError struct {
	// kind is the safe public error classification.
	kind string
	// phase names the operation where the error arose.
	phase string
}

func (e classifiedError) Error() string { return "Pi " + e.phase + ": " + e.kind }

func (e classifiedError) As(target any) bool {
	out, ok := target.(**acp.RequestError)
	if !ok {
		return false
	}
	data := map[string]any{"errorKind": e.kind, "phase": e.phase}
	switch e.kind {
	case "authentication":
		*out = acp.NewAuthRequired(data)
	case "session_missing":
		*out = &acp.RequestError{Code: -32002, Message: "Pi native session was not found", Data: data}
	default:
		*out = acp.NewInternalError(data)
	}
	return true
}

func classified(kind, phase string) error { return classifiedError{kind: kind, phase: phase} }

func missingSession() error { return classified("session_missing", "session_lookup") }

func samePath(a, b string) bool {
	if filepath.Clean(a) == filepath.Clean(b) {
		return true
	}
	left, leftErr := filepath.EvalSymlinks(a)
	right, rightErr := filepath.EvalSymlinks(b)
	return leftErr == nil && rightErr == nil && left == right
}

func pointer[T any](value T) *T { return &value }

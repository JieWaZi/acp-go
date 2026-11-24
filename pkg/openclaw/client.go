// Package openclaw connects a local Gateway session to the native OpenClaw ACP bridge.
package openclaw

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"

	acp "github.com/JieWaZi/acp-go-sdk"
	"github.com/JieWaZi/acp-go/pkg/acpclient"
	"github.com/JieWaZi/acp-go/pkg/acpmeta"
	"github.com/JieWaZi/acp-go/pkg/agentclient"
)

// Options freezes the OpenClaw launcher and Gateway selection.
type Options struct {
	// Executable selects the installed OpenClaw launcher.
	Executable string
	// PrefixArgs provides native global profile options or launcher arguments.
	PrefixArgs []string
	// WorkingDirectory is the local directory authorized for this client.
	WorkingDirectory string
	// Environment is the complete child environment. Nil snapshots the host environment.
	Environment []string
	// PermissionMode may preserve the Gateway's native approval policy.
	PermissionMode string
}

// Client owns the native bridge and the exact Gateway sessions it has bound.
type Client struct {
	// bridge owns the native ACP process after its local route is pinned.
	bridge atomic.Pointer[acpclient.Client]
	// gateway issues session control and management RPCs with the same frozen inputs.
	gateway gateway
	// cwd is the physical authorized root shared by bridge and Gateway.
	cwd string
	// host callbacks observe the authorized standard ACP session and prompt.
	host agentclient.HostCallbacks
	// pinDir contains the private ACP config and is released with the bridge.
	pinDir string
	// ctx and stop cancel Gateway operations when this connection closes.
	ctx  context.Context
	stop context.CancelFunc
	// op excludes session mutation and model writes during a reserved ACP prompt.
	op sync.Mutex
	// ready is set only after verifying the native lifecycle capabilities.
	ready bool
	// sessionMu synchronizes the session directory with native ACP notifications.
	sessionMu sync.Mutex
	// sessions stores Gateway-backed sessions actively bound in this bridge.
	sessions map[acp.SessionId]sessionState
	// unopened retains confirmed new Gateway rows whose ACP open failed.
	unopened map[binding]struct{}
}

var _ agentclient.Client = (*Client)(nil)

// NewClient freezes launch inputs. Initialize starts the bridge after host registration.
func NewClient(ctx context.Context, options Options, host agentclient.HostCallbacks) (*Client, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if options.PermissionMode != "" && options.PermissionMode != "default" {
		return nil, errors.New("OpenClaw uses the Gateway's native permission policy")
	}
	for _, arg := range options.PrefixArgs {
		flag, _, _ := strings.Cut(arg, "=")
		switch flag {
		case "--url", "--port", "--expect-url", "--session", "--session-label", "--reset-session", "--require-existing":
			return nil, errors.New("OpenClaw launcher cannot override Gateway or session binding")
		}
	}
	directory, err := filepath.Abs(options.WorkingDirectory)
	if err != nil {
		return nil, errors.New("cannot resolve OpenClaw working directory")
	}
	cwd, err := physicalDirectory(directory)
	if err != nil {
		return nil, err
	}
	if options.Environment == nil {
		options.Environment = os.Environ()
	}
	environment, err := localGatewayEnvironment(options.Environment)
	if err != nil {
		return nil, err
	}
	if options.Executable == "" {
		options.Executable = "openclaw"
	}
	spec := agentclient.ProcessSpec{Executable: options.Executable, Args: slices.Clone(options.PrefixArgs), Env: environment, Dir: cwd}
	launcher, err := agentclient.NewProcess(spec)
	if err != nil {
		return nil, err
	}
	spec.Executable = launcher.Executable()
	lifetime, stop := context.WithCancel(context.WithoutCancel(ctx))
	return &Client{gateway: gateway{process: spec, active: make(map[*agentclient.Process]struct{})}, cwd: cwd,
		host: host, ctx: lifetime, stop: stop, sessions: make(map[acp.SessionId]sessionState), unopened: make(map[binding]struct{})}, nil
}

func (c *Client) prepareBridge(ctx context.Context) (*acpclient.Client, error) {
	bridgeSpec, pinDir, err := c.gateway.bridgeProfile(ctx)
	if err != nil {
		return nil, err
	}
	bridgeSpec.Args = append(slices.Clone(bridgeSpec.Args), "acp", "--no-prefix-cwd")
	host := c.host
	if host.Observe != nil {
		host.Observe = c.observe
	}
	bridge, err := acpclient.NewClient(ctx, acpclient.Options{
		Process: bridgeSpec, RuntimeID: "openclaw", RequireVerifiedMCP: true,
		Protocol: acpclient.Protocol{
			ValidateInitialize: validateInitialize, CwdBoundProcess: true, BlockUntypedExtensions: true,
			Authentication:       acpclient.Authentication{Disabled: true},
			PromptUsage:          acpclient.UsageUnavailable,
			CapturePromptJournal: (usageJournal{root: usageRoot(c.gateway.process.Env, c.gateway.process.Args), gateway: c.gateway.url}).capture,
			NormalizeUsage:       func(*acp.SessionUsageUpdate) *acpmeta.UsageObservation { return nil },
		},
	}, host)
	if err != nil {
		_ = os.RemoveAll(pinDir)
		return nil, err
	}
	c.pinDir = pinDir
	c.bridge.Store(bridge)
	return bridge, nil
}

func (c *Client) observe(ctx context.Context, scope agentclient.Scope, note acp.SessionNotification) error {
	if note.Update.CurrentModeUpdate != nil {
		// OpenClaw's ACP modes represent thinking levels. The native thought
		// option reports the setting, and Gateway approval policy stays native.
		return nil
	}
	if update := note.Update.ConfigOptionUpdate; update != nil {
		projection := *update
		projection.ConfigOptions = c.updateOptions(scope.SessionID, update.ConfigOptions)
		note.Update.ConfigOptionUpdate = &projection
	}
	return c.host.Observe(ctx, scope, note)
}

func validateInitialize(response acp.InitializeResponse) error {
	caps := response.AgentCapabilities.SessionCapabilities
	if response.AgentInfo == nil || response.AgentInfo.Name != "openclaw-acp" || caps.Resume == nil || caps.Close == nil {
		return errors.New("OpenClaw ACP bridge must advertise native session resume and close")
	}
	return nil
}

// Initialize establishes the native ACP bridge without account, model, or version probes.
func (c *Client) Initialize(ctx context.Context, request acp.InitializeRequest) (acp.InitializeResponse, error) {
	if !c.op.TryLock() {
		return acp.InitializeResponse{}, acpclient.ErrBusy
	}
	defer c.op.Unlock()
	op, cancel := c.operationContext(ctx)
	defer cancel()
	bridge := c.bridge.Load()
	if bridge == nil {
		var err error
		bridge, err = c.prepareBridge(op)
		if err != nil {
			return acp.InitializeResponse{}, err
		}
	}
	result, err := bridge.Initialize(op, request)
	if err != nil {
		return acp.InitializeResponse{}, err
	}
	c.ready = true
	result.AgentCapabilities.LoadSession = false
	result.AgentCapabilities.McpCapabilities = acp.McpCapabilities{}
	caps := &result.AgentCapabilities.SessionCapabilities
	caps.List = nil
	caps.AdditionalDirectories = nil
	caps.Fork = nil
	if result.Meta == nil {
		result.Meta = make(map[string]any)
	}
	result.Meta["fork"] = acpmeta.ForkMetadata(acpmeta.ForkUnsupported)["fork"]
	return result, nil
}

func (c *Client) admit(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if c.ctx.Err() != nil {
		return acpclient.ErrRestartRequired
	}
	if !c.op.TryLock() {
		return acpclient.ErrBusy
	}
	if !c.ready {
		c.op.Unlock()
		return acp.NewInvalidParams("initialize OpenClaw before opening a Gateway session")
	}
	return nil
}

func (c *Client) operationContext(ctx context.Context) (context.Context, func()) {
	op, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(c.ctx, cancel)
	return op, func() { stop(); cancel() }
}

// Executable returns the same frozen launcher used by both bridge and Gateway calls.
func (c *Client) Executable() string { return c.gateway.process.Executable }

// Authenticate leaves Gateway credential management with the native OpenClaw profile.
func (c *Client) Authenticate(_ context.Context, _ acp.AuthenticateRequest) (acp.AuthenticateResponse, error) {
	return acp.AuthenticateResponse{}, acp.NewMethodNotFound(acp.AgentMethodAuthenticate)
}

// Logout leaves Gateway credential management with the native OpenClaw profile.
func (c *Client) Logout(_ context.Context, _ acp.LogoutRequest) (acp.LogoutResponse, error) {
	return acp.LogoutResponse{}, acp.NewMethodNotFound(acp.AgentMethodLogout)
}

// ListSessions does not publish unrelated Gateway or agent sessions.
func (c *Client) ListSessions(_ context.Context, _ acp.ListSessionsRequest) (acp.ListSessionsResponse, error) {
	return acp.ListSessionsResponse{}, acp.NewMethodNotFound(acp.AgentMethodSessionList)
}

// SetSessionMode rejects a per-chat permission override of the Gateway policy.
func (c *Client) SetSessionMode(_ context.Context, _ acp.SetSessionModeRequest) (acp.SetSessionModeResponse, error) {
	return acp.SetSessionModeResponse{}, acp.NewMethodNotFound(acp.AgentMethodSessionSetMode)
}

// ReservePrompt runs native ACP against the exact workspace owned by the binding.
func (c *Client) ReservePrompt(ctx context.Context, request acp.PromptRequest, options agentclient.PromptOptions) (func() agentclient.PromptResult, error) {
	if err := c.admit(ctx); err != nil {
		return nil, err
	}
	c.sessionMu.Lock()
	state, ok := c.sessions[request.SessionId]
	c.sessionMu.Unlock()
	if !ok {
		c.op.Unlock()
		return nil, missingSession()
	}
	if state.selectedModel != "" && state.model != state.selectedModel {
		c.op.Unlock()
		return nil, acp.NewInvalidParams("OpenClaw ACP has not refreshed controls for the selected model")
	}
	promptMeta := make(map[string]any, len(request.Meta)+1)
	for key, value := range request.Meta {
		promptMeta[key] = value
	}
	promptMeta["deliver"] = false
	request.Meta = promptMeta
	beforeSubmit := options.BeforeSubmit
	options.BeforeSubmit = func(ctx context.Context, input agentclient.PromptSubmission) error {
		op, cancel := c.operationContext(ctx)
		defer cancel()
		// Gateway administrators can move, reset, or dispatch a session after
		// the bridge opens it. Recheck the execution target before effectful work.
		row, err := c.describe(op, state.binding)
		if err != nil {
			return err
		}
		if state.selectedModel != "" && modelID(row.Provider, row.Model) != state.selectedModel {
			return acp.NewInvalidParams("OpenClaw Gateway has changed the confirmed session model")
		}
		if beforeSubmit != nil {
			return beforeSubmit(ctx, input)
		}
		return nil
	}
	run, err := c.bridge.Load().ReservePrompt(ctx, request, options)
	if err != nil {
		c.op.Unlock()
		return nil, err
	}
	var claimed atomic.Bool
	return func() agentclient.PromptResult {
		if !claimed.CompareAndSwap(false, true) {
			return run()
		}
		defer c.op.Unlock()
		return run()
	}, nil
}

// Cancel forwards cancellation to the bridge's current prompt owner.
func (c *Client) Cancel(notification acp.CancelNotification) error {
	if bridge := c.bridge.Load(); bridge != nil {
		return bridge.Cancel(notification)
	}
	return nil
}

// Close stops the bridge and cancels in-progress control calls.
func (c *Client) Close(ctx context.Context) error {
	c.stop()
	var err error
	bridge := c.bridge.Load()
	if bridge != nil {
		err = bridge.Close(ctx)
	}
	if !c.op.TryLock() {
		return errors.Join(err, acpclient.ErrBusy)
	}
	defer c.op.Unlock()
	if latest := c.bridge.Load(); latest != nil && latest != bridge {
		err = errors.Join(err, latest.Close(ctx))
	}
	for bound := range c.unopened {
		if ctx.Err() != nil {
			return errors.Join(err, ctx.Err())
		}
		err = errors.Join(err, c.discardUnopened(ctx, bound))
	}
	if len(c.unopened) != 0 {
		return err
	}
	err = errors.Join(err, c.gateway.close(ctx))
	if err == nil && c.pinDir != "" {
		if os.RemoveAll(c.pinDir) != nil {
			return errors.New("cannot remove the private OpenClaw ACP configuration")
		}
		c.pinDir = ""
	}
	return err
}

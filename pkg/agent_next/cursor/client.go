// Package cursor owns one Cursor CLI ACP process and its native sessions.
package cursor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/JieWaZi/acp-go/pkg/acpmeta"
	agentclient "github.com/JieWaZi/acp-go/pkg/agent_next"
	acp "github.com/JieWaZi/acp-go/sdk"
)

const (
	loginMethod         = "cursor_login"
	requestTimeout      = 30 * time.Second
	controlTimeout      = time.Second
	cancelGrace         = 10 * time.Second
	maxSessions         = 256
	maxObservation      = 128
	maxObservationBytes = 2 << 20
)

var (
	ErrBusy               = errors.New("Cursor is busy")
	ErrRestartRequired    = errors.New("Cursor connection requires a new client")
	errUnconfirmedSetting = errors.New("Cursor did not confirm the requested setting")
	errDeliveryLimit      = errors.New("Cursor observation budget exhausted")
)

// Options freezes the selected Cursor executable, credentials, and startup scope.
type Options struct {
	// Executable is the selected Cursor CLI path or command name.
	Executable string
	// PrefixArgs precede each Cursor subcommand.
	PrefixArgs []string
	// Environment is the complete launch environment, or nil to freeze the host environment.
	Environment []string
	// WorkingDirectory is the initial process directory.
	WorkingDirectory string
	// Logger is reserved for safe SDK diagnostics.
	Logger *slog.Logger
	// PermissionMode is the startup policy: default, auto, or full-access.
	PermissionMode string
	// RequireVerifiedMCP rejects servers when Cursor cannot verify their registration.
	RequireVerifiedMCP bool
	// Dashboard optionally enables quota reads for the explicitly bound native login.
	Dashboard *Dashboard
	// Credential identifies the exact login authorized for management reads.
	Credential CredentialSelection
}

// Client owns one process, one SDK connection, and one process-wide operation.
type Client struct {
	// mu serializes local admission and technical evidence without enclosing I/O.
	mu sync.Mutex
	// opts holds the immutable selected launch and account inputs.
	opts Options
	// proc owns the long-lived CLI, including partially initialized processes.
	proc *agentclient.Process
	// command retains a version/auth helper until its process tree is reaped.
	command *agentclient.Process
	// conn is published once the SDK owns the initialized transport.
	conn *acp.ClientSideConnection
	// initialized contains immutable native capabilities, separate from wire projection.
	initialized *acp.InitializeResponse
	// apiKey is the frozen credential used only for capability and safe diagnostic classification.
	apiKey string
	// ctx and stop cancel this owner independently of the caller's admission lifetime.
	ctx  context.Context
	stop context.CancelFunc
	// closed rejects new local operations and callbacks.
	closed bool
	// unusable prevents reuse after an ambiguous native mutation or lost authentication.
	unusable bool
	// attempted records that initialization cannot be replayed on this owner.
	attempted bool
	// busy admits one session operation or one foreground invocation at a time.
	busy bool
	// workers includes admitted operations, callbacks and ordered observer delivery.
	workers sync.WaitGroup
	// released closes once the owner's admitted Go work has exited.
	released chan struct{}
	// sessions stores confirmed generations by exact native session ID.
	sessions map[acp.SessionId]*session
	// opening is the one bounded provisional session opening in this process.
	opening *opening
	// turn is the reserved foreground invocation, including preparation and settlement.
	turn *turn
	// processModel is the last model the process confirmed, scoped to this transport.
	processModel acp.SessionConfigValueId
	// catalog caches an explicitly discovered native catalog for this credential.
	catalog *acpmeta.ModelCatalog
	// peer exists only on the public wire path.
	peer *acp.AgentSideConnection
	// reverseCalls and reverseBytes bound dispatched native host requests.
	reverseCalls int
	reverseBytes int
	// mcpConnections maps explicitly opened wire MCP channels.
	mcpConnections map[acp.UnstableMcpConnectionId]struct{}
	// clientCaps are the frozen capabilities actually advertised to Cursor.
	clientCaps acp.ClientCapabilities
	// host supplies the narrow direct application callbacks.
	host agentclient.HostCallbacks
	// observe is the bounded queue for ordered host delivery.
	observe chan observation
	// observeBytes includes the queued and currently delivered frame bytes.
	observeBytes int
}

var _ agentclient.Client = (*Client)(nil)
var _ acp.AgentLoader = (*Client)(nil)

// NewClient resolves and freezes options without starting any native process.
func NewClient(ctx context.Context, options Options, host agentclient.HostCallbacks) (*Client, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	discover := options.Executable == ""
	if discover {
		options.Executable = "cursor-agent"
	}
	if options.Environment == nil {
		options.Environment = os.Environ()
	}
	options.Environment = slices.Clone(options.Environment)
	options.PrefixArgs = slices.Clone(options.PrefixArgs)
	if options.Dashboard != nil {
		dashboard := *options.Dashboard
		if dashboard.Endpoint == "" {
			dashboard.Endpoint = envValue(options.Environment, "CURSOR_API_ENDPOINT")
		}
		if dashboard.HTTPClient != nil {
			client := *dashboard.HTTPClient
			dashboard.HTTPClient = &client
		}
		options.Dashboard = &dashboard
	}
	if options.WorkingDirectory == "" {
		dir, err := os.Getwd()
		if err != nil {
			return nil, errors.New("resolve Cursor working directory failed")
		}
		options.WorkingDirectory = dir
	}
	if !filepath.IsAbs(options.WorkingDirectory) {
		dir, err := filepath.Abs(options.WorkingDirectory)
		if err != nil {
			return nil, errors.New("resolve Cursor working directory failed")
		}
		options.WorkingDirectory = dir
	}
	apiKey := strings.TrimSpace(envValue(options.Environment, "CURSOR_API_KEY"))
	args := slices.Clone(options.PrefixArgs)
	switch options.PermissionMode {
	case "", "default":
	case "auto":
		args = append(args, "--auto-review")
	case "full-access":
		args = append(args, "--force")
	default:
		return nil, invalid("invalid Cursor startup permission mode")
	}
	spec := agentclient.ProcessSpec{
		Executable: options.Executable, Args: append(args, "acp"), Env: options.Environment, Dir: options.WorkingDirectory,
		Redact: func(record string) string {
			if apiKey != "" && strings.Contains(record, apiKey) {
				return ""
			}
			return agentclient.RedactProcessDiagnostic(record)
		},
	}
	proc, err := agentclient.NewProcess(spec)
	if err != nil && discover {
		spec.Executable = "agent"
		if alias, aliasErr := agentclient.NewProcess(spec); aliasErr == nil {
			if target, targetErr := filepath.EvalSymlinks(alias.Executable()); targetErr == nil &&
				target != alias.Executable() && cliName(target) == "cursor-agent" {
				proc, err = alias, nil
			}
		}
	}
	if err != nil {
		return nil, err
	}
	options.Executable = proc.Executable()
	target, err := filepath.EvalSymlinks(options.Executable)
	if err != nil || cliName(options.Executable) == "cursor" || cliName(target) == "cursor" {
		return nil, errors.New("selected executable is not a Cursor CLI launcher")
	}
	lifetime, stop := context.WithCancel(context.WithoutCancel(ctx))
	return &Client{opts: options, proc: proc, apiKey: apiKey, ctx: lifetime, stop: stop,
		sessions: make(map[acp.SessionId]*session), host: host,
		observe: make(chan observation, maxObservation), released: make(chan struct{})}, nil
}

func envValue(env []string, key string) string {
	for i := len(env) - 1; i >= 0; i-- {
		name, value, ok := strings.Cut(env[i], "=")
		if ok && (name == key || runtime.GOOS == "windows" && strings.EqualFold(name, key)) {
			return value
		}
	}
	return ""
}

func (c *Client) Initialize(ctx context.Context, request acp.InitializeRequest) (acp.InitializeResponse, error) {
	if err := ctx.Err(); err != nil {
		return acp.InitializeResponse{}, err
	}
	c.mu.Lock()
	if c.closed || c.attempted {
		c.mu.Unlock()
		return acp.InitializeResponse{}, invalid("Cursor initialization already attempted")
	}
	c.attempted = true
	c.busy = true
	c.workers.Add(1)
	if c.peer == nil {
		request.ClientCapabilities = acp.ClientCapabilities{}
	}
	if c.peer == nil && c.host.CreateElicitation != nil {
		request.ClientCapabilities.Elicitation = &acp.ElicitationCapabilities{Form: &acp.ElicitationFormCapabilities{}}
		if c.host.CompleteElicitation != nil {
			request.ClientCapabilities.Elicitation.Url = &acp.ElicitationUrlCapabilities{}
		}
	}
	c.clientCaps, _ = clone(request.ClientCapabilities)
	c.mu.Unlock()
	defer c.end()
	ctx, cancel := c.operationContext(ctx, requestTimeout)
	defer cancel()
	version, err := c.probeVersion(ctx)
	if err != nil {
		return acp.InitializeResponse{}, err
	}
	request.ClientCapabilities.Meta = copyMeta(request.ClientCapabilities.Meta)
	request.ClientCapabilities.Meta["parameterizedModelPicker"] = true
	if err := c.proc.Start(ctx); err != nil {
		return acp.InitializeResponse{}, err
	}
	in, out := c.proc.Stdin(), c.proc.Stdout()
	conn, err := acp.NewClientSideConnectionWithOptions((*reverseClient)(c), in, out, acp.ConnectionOptions{
		Context: c.ctx, NotificationQueueCapacity: 128, NotificationQueueBytes: 8 << 20,
		ReceiveNotification: c.receive, PrepareRequest: c.prepareNativeRequest,
		CloseTransport: func() error { return errors.Join(in.Close(), out.Close()) },
	})
	if err != nil {
		return acp.InitializeResponse{}, err
	}
	if c.opts.Logger != nil {
		conn.SetLogger(c.opts.Logger)
	} else {
		conn.SetLogger(slog.New(slog.NewTextHandler(io.Discard, nil)))
	}
	receipt := send[acp.InitializeResponse](conn, ctx, acp.AgentMethodInitialize, request)
	if err := receiptFailure(receipt, "initialize"); err != nil {
		return acp.InitializeResponse{}, err
	}
	native := receipt.Result
	if native.ProtocolVersion == 0 || native.ProtocolVersion != request.ProtocolVersion {
		return acp.InitializeResponse{}, errors.New("Cursor negotiated an unsupported ACP protocol version")
	}
	response, err := clone(native)
	if err != nil {
		return acp.InitializeResponse{}, err
	}
	if response.AgentInfo == nil {
		response.AgentInfo = &acp.Implementation{Name: "cursor", Version: version}
	}
	response.AgentInfo.Meta = copyMeta(response.AgentInfo.Meta)
	response.AgentInfo.Meta["runtime"] = acpmeta.RuntimeVersionMetadata(version)["runtime"]
	if c.apiKey != "" {
		response.AuthMethods = nil
	}
	// These standard capabilities do not imply a compatible optional local method.
	response.AgentCapabilities.SessionCapabilities.Delete = nil
	response.Meta = copyMeta(response.Meta)
	instructions := acpmeta.DeveloperInstructionsCapability(response.AgentCapabilities.SessionCapabilities.Fork != nil)
	instructions["delivery"] = "prompt_prefix"
	response.Meta["developerInstructions"] = instructions
	if c.peer == nil {
		response.AgentCapabilities.McpCapabilities.Acp = false
	}
	if response.AgentCapabilities.SessionCapabilities.Fork != nil {
		mode := acpmeta.ForkMode(response.Meta)
		if mode != acpmeta.ForkTurn {
			mode = acpmeta.ForkLatest
		}
		response.Meta = copyMeta(response.Meta)
		response.Meta["fork"] = acpmeta.ForkMetadata(mode)["fork"]
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return acp.InitializeResponse{}, ErrRestartRequired
	}
	c.conn, c.initialized = conn, &native
	c.workers.Add(1)
	go c.deliver()
	return response, nil
}

func (c *Client) begin(ctx context.Context) (*acp.ClientSideConnection, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.admissionLocked(ctx); err != nil {
		return nil, err
	}
	c.busy = true
	c.workers.Add(1)
	return c.conn, nil
}

func (c *Client) admissionLocked(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if c.closed || c.unusable {
		return ErrRestartRequired
	}
	if c.busy {
		return ErrBusy
	}
	if c.initialized == nil || c.conn == nil {
		return invalid("initialize Cursor first")
	}
	select {
	case <-c.conn.Done():
		return ErrRestartRequired
	default:
		return nil
	}
}

func (c *Client) end() {
	c.mu.Lock()
	c.busy = false
	c.mu.Unlock()
	c.workers.Done()
}

func (c *Client) operationContext(ctx context.Context, limit time.Duration) (context.Context, context.CancelFunc) {
	op, cancel := context.WithTimeout(ctx, limit)
	stop := context.AfterFunc(c.ctx, cancel)
	return op, func() { stop(); cancel() }
}

// Close cancels native and host work, reaps the same process and permits cleanup retry.
func (c *Client) Close(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	if !c.closed {
		c.closed = true
		c.stop()
		go func() { c.workers.Wait(); close(c.released) }()
	}
	command := c.command
	c.mu.Unlock()
	var commandErr error
	if command != nil {
		commandErr = c.closeCommand(ctx, command)
	}
	if err := errors.Join(c.proc.Close(ctx), commandErr); err != nil {
		return err
	}
	select {
	case <-c.released:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// BindWirePeer connects reverse capabilities only on the public ACP path.
func (c *Client) BindWirePeer(peer *acp.AgentSideConnection) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if peer == nil || c.peer != nil || c.attempted || c.closed {
		panic("Cursor requires one wire peer before initialization")
	}
	c.peer = peer
}

// ProvidesUserInput identifies Cursor's native question mechanism.
func (*Client) ProvidesUserInput() bool { return true }

func send[T any](conn *acp.ClientSideConnection, ctx context.Context, method string, request any) acp.RequestReceipt[T] {
	return acp.SendClientRequestWithReceipt[T](conn, ctx, method, request, boundedWrite(ctx, conn, nil))
}

// boundedWrite only closes a transport if the context expires after the write
// gate opens and before the local write result. The SDK owns response waiting.
func boundedWrite(ctx context.Context, conn *acp.ClientSideConnection, commit func() error) acp.RequestWriteHooks {
	var writing atomic.Bool
	var stop func() bool
	var finished chan struct{}
	return acp.RequestWriteHooks{
		Commit: func() error {
			if commit != nil {
				if err := commit(); err != nil {
					return err
				}
			}
			writing.Store(true)
			finished = make(chan struct{})
			stop = context.AfterFunc(ctx, func() {
				defer close(finished)
				if writing.CompareAndSwap(true, false) {
					conn.Close()
				}
			})
			return nil
		},
		Complete: func(acp.RequestWriteResult) {
			writing.Store(false)
			if !stop() {
				<-finished
			}
		},
	}
}

func receiptFailure[T any](r acp.RequestReceipt[T], method string) error {
	if r.PeerErr != nil {
		return safePeerError(r.PeerErr, method)
	}
	if r.ResultValid && r.Write.Err == nil && r.TransportErr == nil {
		return nil
	}
	return safeOperationError(method, errors.Join(r.TransportErr, r.Write.Err, r.DecodeErr, r.PrepareErr))
}

func safePeerError(err *acp.RequestError, method string) *acp.RequestError {
	switch err.Code {
	case -32000:
		return acp.NewAuthRequired(map[string]any{"method": method})
	case -32601:
		return acp.NewMethodNotFound(method)
	case -32602:
		return invalid("Cursor rejected the request parameters")
	case -32800:
		return acp.NewRequestCancelled(map[string]any{"method": method})
	default:
		return &acp.RequestError{Code: err.Code, Message: "Cursor request failed", Data: map[string]any{"method": method}}
	}
}

func safeOperationError(method string, cause error) error {
	for _, known := range []error{context.Canceled, context.DeadlineExceeded, ErrRestartRequired, ErrBusy, errSettingsChanged} {
		if errors.Is(cause, known) {
			return known
		}
	}
	return errors.New("Cursor " + method + " transport or response failed")
}

// explainExit uses only the shared process's complete, sanitized diagnostics.
func (c *Client) explainExit(method string, cause error) error {
	safe := safeOperationError(method, cause)
	if c.ctx.Err() != nil {
		return safe
	}
	ctx, cancel := context.WithTimeout(c.ctx, 2*time.Second)
	defer cancel()
	exit, err := c.proc.Wait(ctx)
	if err != nil {
		return safe
	}
	if c.opts.Logger != nil && exit.Diagnostic != "" {
		c.opts.Logger.Debug("Cursor CLI exited", "code", exit.Code, "diagnostic", exit.Diagnostic)
	}
	if exit.Signal != "" {
		return fmt.Errorf("Cursor CLI exited on signal %s", exit.Signal)
	}
	return fmt.Errorf("Cursor CLI exited with code %d", exit.Code)
}

func invalid(message string) *acp.RequestError {
	return acp.NewInvalidParams(map[string]any{"message": message})
}

func copyMeta(meta map[string]any) map[string]any {
	copy := make(map[string]any, len(meta)+1)
	for key, value := range meta {
		copy[key] = value
	}
	return copy
}

func clone[T any](v T) (T, error) {
	var result T
	b, err := json.Marshal(v)
	if err == nil {
		err = json.Unmarshal(b, &result)
	}
	return result, err
}

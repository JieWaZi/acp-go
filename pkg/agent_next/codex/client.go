// Package codex connects a host to the native Codex App Server.
package codex

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/JieWaZi/acp-go/pkg/acpmeta"
	agentclient "github.com/JieWaZi/acp-go/pkg/agent_next"
	"github.com/JieWaZi/acp-go/pkg/codex/protocol"
	acp "github.com/JieWaZi/acp-go/sdk"
)

const (
	requestTimeout = 30 * time.Second
	controlTimeout = 5 * time.Second
	maxFrameBytes  = 16 << 20
)

var (
	errUnavailable   = errors.New("Codex app server unavailable")
	errTurnChanged   = errors.New("Codex turn changed before native operation")
	errTurnBusy      = errors.New("Codex session already has an operation in progress")
	versionPattern   = regexp.MustCompile(`(?:^|[^0-9])([0-9]+\.[0-9]+\.[0-9]+(?:-[a-zA-Z0-9.-]+)?)`)
	diagnosticSecret = regexp.MustCompile(`(?i)(?:Bearer\s+[^\s,;]+|(?:sk|sess|token)[-_a-zA-Z0-9.=]{8,}|[A-Z0-9._%+-]+@[A-Z0-9.-]+\.[A-Z]{2,})`)
)

// Options freezes one app-server launch and its configuration overrides.
type Options struct {
	// Executable is an explicit Codex executable or a command on the frozen PATH.
	Executable string
	// PrefixArgs is prepended to the app-server command.
	PrefixArgs []string
	// Environment is the complete child environment, or nil to inherit it at construction.
	Environment []string
	// WorkingDirectory is the actual process working directory.
	WorkingDirectory string
	// Logger receives sanitized diagnostics.
	Logger *slog.Logger
	// ConfigOverrides contains native thread configuration overrides.
	ConfigOverrides map[string]json.RawMessage
	// DefaultModeRequestUserInput enables ordinary-mode questions when no setting overrides it.
	DefaultModeRequestUserInput bool
}

// Client owns one process and the sessions opened on that process.
type Client struct {
	// mu serializes process state, native receipts and session/turn ownership.
	mu sync.Mutex
	// process is retained through failed initialization and incomplete cleanup.
	process *agentclient.Process
	// options is the frozen launch and configuration.
	options Options
	// host is immutable after construction.
	host agentclient.HostCallbacks
	// life ends on transport failure or client shutdown.
	life context.Context
	// endLife publishes the first native failure or an orderly client close.
	endLife context.CancelCauseFunc
	// retireOnce schedules cleanup of the failed physical child once.
	retireOnce sync.Once
	// startDone publishes the initialize attempt, including failure.
	startDone chan struct{}
	// startErr records a failed initialize attempt.
	startErr error
	// ready allows session commands after native initialization.
	ready bool
	// closing prevents admission while native resources are being released.
	closing bool
	// readerDone closes when all native frames have been consumed.
	readerDone chan struct{}
	// readerStarted distinguishes pre-launch cleanup from a running reader.
	readerStarted bool
	// writeGate serializes complete native frames and their final ownership check.
	writeGate chan struct{}
	// nextID allocates this process's native request IDs.
	nextID uint64
	// pending owns unresolved native responses keyed by native request ID.
	pending map[string]*pendingCall
	// sessions is the live registry, with a distinct value per reopened generation.
	sessions map[acp.SessionId]*session
	// seenTurns fences late duplicate native turns across reopened generations.
	seenTurns map[turnIdentity]struct{}
	// recentTurns bounds duplicate-turn retention for this physical client.
	recentTurns []turnIdentity
	// nextRecentTurn replaces the oldest native turn once retention is full.
	nextRecentTurn int
	// foregroundReceipts retains direct Prompt outcomes only when negotiated.
	foregroundReceipts bool
	// receipts owns completed, unacknowledged outcomes by native session.
	receipts map[acp.SessionId][]*outcomeEntry
	// retainedOutcomes includes completed receipts and reservations.
	retainedOutcomes int
	// receiptChanged wakes readers on outcome or session lifecycle changes.
	receiptChanged chan struct{}
	// managing holds known native IDs during resume, fork, and archive.
	managing map[acp.SessionId]bool
	// login receives login completion for the one account operation in flight.
	login chan protocol.AccountLoginCompletedNotification
	// logoutDone captures the native account/updated barrier for the active logout.
	logoutDone chan struct{}
	// loggingOut excludes a concurrent login or logout.
	loggingOut bool
	// version is the observed CLI version.
	version string
	// forkMode is negotiated from the pinned protocol floor and observed version.
	forkMode string
	// wireBound marks external ACP capability negotiation.
	wireBound bool
	// fullTerminalOutput is negotiated for ACP clients that accept snapshots.
	fullTerminalOutput bool
	// formEnabled and urlEnabled freeze negotiated host interaction support.
	formEnabled, urlEnabled bool
	// interactionCount limits native server requests in flight.
	interactionCount int
	// urls correlates outstanding URL completions by native request ID.
	urls map[string]urlInteraction
	// interactions tracks admitted native requests and observations until handlers return.
	interactions sync.WaitGroup
	// drainOnce starts one cleanup waiter after callback admission is closed.
	drainOnce sync.Once
	// callbacksDone closes after all callbacks have returned.
	callbacksDone chan struct{}
	// skillRoots records the confirmed process-wide extra roots.
	skillRoots []string
	// skillUsers counts native consumers holding the current roots.
	skillUsers int
	// skillChanging excludes consumers while a root change is in flight.
	skillChanging bool
	// skillChanged wakes waiters after root changes and lease releases.
	skillChanged chan struct{}
	// probing keeps a management timeout from starting redundant health checks.
	probing bool
}

type rpcFrame struct {
	// ID distinguishes native requests/responses from notifications.
	ID json.RawMessage `json:"id,omitempty"`
	// Method is present on requests and notifications.
	Method string `json:"method,omitempty"`
	// Params carries the method-specific native input.
	Params json.RawMessage `json:"params,omitempty"`
	// Result is the native success payload.
	Result json.RawMessage `json:"result,omitempty"`
	// Error is the native failure payload.
	Error *acp.RequestError `json:"error,omitempty"`
}

type pendingCall struct {
	// done receives exactly one matched frame.
	done chan rpcFrame
	// capture updates native ownership synchronously in receive order.
	capture func(rpcFrame)
}

type frameWrite struct {
	// bytes reports how far the frame reached the native pipe.
	bytes int
	// size includes the NDJSON delimiter.
	size int
}

func (w frameWrite) submission() agentclient.Submission {
	switch {
	case w.bytes == 0:
		return agentclient.SubmissionNotSent
	case w.bytes == w.size:
		return agentclient.SubmissionSent
	default:
		return agentclient.SubmissionUnknown
	}
}

// NewClient validates and freezes options without starting the process.
func NewClient(ctx context.Context, options Options, host agentclient.HostCallbacks) (*Client, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if options.Executable == "" {
		options.Executable = "codex"
	}
	dir, err := filepath.Abs(options.WorkingDirectory)
	if err != nil {
		return nil, err
	}
	options.WorkingDirectory = dir
	options.PrefixArgs = slices.Clone(options.PrefixArgs)
	if options.Environment == nil {
		options.Environment = os.Environ()
	} else {
		options.Environment = slices.Clone(options.Environment)
	}
	config := make(map[string]json.RawMessage, len(options.ConfigOverrides))
	for key, value := range options.ConfigOverrides {
		if key == "" || !json.Valid(value) {
			return nil, fmt.Errorf("invalid Codex configuration override %q", key)
		}
		config[key] = bytes.Clone(value)
	}
	options.ConfigOverrides = config
	args := append(slices.Clone(options.PrefixArgs), "app-server", "--listen", "stdio://")
	process, err := agentclient.NewProcess(agentclient.ProcessSpec{
		Executable: options.Executable, Args: args, Dir: dir, Env: options.Environment,
		Redact: func(record string) string {
			record = safeDiagnostic(record)
			if options.Logger != nil && record != "" {
				options.Logger.Debug("Codex app server", "record", record)
			}
			return record
		},
	})
	if err != nil {
		return nil, err
	}
	life, fail := context.WithCancelCause(context.Background())
	return &Client{
		options: options, host: host, process: process, life: life, endLife: fail,
		writeGate: make(chan struct{}, 1), readerDone: make(chan struct{}), callbacksDone: make(chan struct{}), skillChanged: make(chan struct{}),
		pending: make(map[string]*pendingCall), sessions: make(map[acp.SessionId]*session), seenTurns: make(map[turnIdentity]struct{}), receipts: make(map[acp.SessionId][]*outcomeEntry), receiptChanged: make(chan struct{}),
	}, nil
}

// BindWirePeer marks public ACP capability negotiation without retaining a connection.
func (c *Client) BindWirePeer(_ *acp.AgentSideConnection) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.wireBound = true
}

func (c *Client) Initialize(ctx context.Context, request acp.InitializeRequest) (acp.InitializeResponse, error) {
	c.mu.Lock()
	start := c.startDone
	if c.closing || start == nil && c.life.Err() != nil {
		c.mu.Unlock()
		return acp.InitializeResponse{}, errUnavailable
	}
	if start == nil {
		start = make(chan struct{})
		c.startDone = start
		c.foregroundReceipts, _ = request.ClientCapabilities.Meta[acpmeta.NativeTurnReceiptsCapability].(bool)
		c.fullTerminalOutput, _ = request.ClientCapabilities.Meta["terminal_output"].(bool)
		elicitation := request.ClientCapabilities.Elicitation
		c.formEnabled = c.host.CreateElicitation != nil && (elicitation == nil && !c.wireBound || elicitation != nil && elicitation.Form != nil)
		c.urlEnabled = c.host.CreateElicitation != nil && (elicitation == nil && !c.wireBound || elicitation != nil && elicitation.Url != nil)
		c.mu.Unlock()
		err := c.initialize(ctx, request.ClientInfo)
		c.mu.Lock()
		c.startErr = err
		if err != nil {
			// The process belongs to this attempt until the handshake succeeds.
			// Publish the original failure even after retiring the transport.
			c.fail(err)
		}
		c.ready = err == nil && !c.closing && c.life.Err() == nil
		close(start)
		c.mu.Unlock()
	} else {
		c.mu.Unlock()
		select {
		case <-start:
		case <-ctx.Done():
			return acp.InitializeResponse{}, ctx.Err()
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.startErr != nil {
		return acp.InitializeResponse{}, c.startErr
	}
	if !c.ready || c.life.Err() != nil {
		return acp.InitializeResponse{}, errUnavailable
	}
	capability := acpmeta.ForkCapability(c.forkMode)
	return acp.InitializeResponse{
		ProtocolVersion: acp.ProtocolVersionNumber,
		AgentInfo:       &acp.Implementation{Name: "codex", Title: pointer("Codex"), Version: c.version, Meta: acpmeta.RuntimeVersionMetadata(c.version)},
		AuthMethods:     c.authMethods(),
		AgentCapabilities: acp.AgentCapabilities{
			LoadSession:        true,
			Auth:               acp.AgentAuthCapabilities{Logout: &acp.LogoutCapabilities{}},
			McpCapabilities:    acp.McpCapabilities{Http: true},
			PromptCapabilities: acp.PromptCapabilities{Image: true, EmbeddedContext: true},
			SessionCapabilities: acp.SessionCapabilities{
				Close: &acp.SessionCloseCapabilities{}, List: &acp.SessionListCapabilities{},
				Resume: &acp.SessionResumeCapabilities{}, Fork: capability,
				AdditionalDirectories: &acp.SessionAdditionalDirectoriesCapabilities{},
			},
		},
		Meta: map[string]any{
			"developerInstructions":              acpmeta.DeveloperInstructionsCapability(capability != nil),
			acpmeta.NativeTurnReceiptsCapability: acpmeta.NativeTurnReceiptsMethod,
			"fork":                               map[string]any{"mode": c.forkMode},
			"steering":                           map[string]bool{"supported": true},
			"native_management": map[string]any{"account_read": true, "account_usage": true, "compact": true,
				"codex_thread_archive": ThreadArchiveMethod, "codex_thread_unarchive": ThreadUnarchiveMethod, "codex_session_settings": SessionSettingsMethod,
				"schema_version": "0.148.0", "provider_support": "unknown"},
		},
	}, nil
}

func (c *Client) initialize(ctx context.Context, clientInfo *acp.Implementation) error {
	if err := c.process.Start(ctx); err != nil {
		return err
	}
	c.mu.Lock()
	c.readerStarted = true
	c.mu.Unlock()
	go c.readFrames()
	var reply struct {
		UserAgent string `json:"userAgent"`
		Version   string `json:"version"`
	}
	params := protocol.InitializeParams{
		ClientInfo:   protocol.ClientInfo{Name: "acp-go", Title: pointer("ACP Go"), Version: "unknown"},
		Capabilities: &protocol.InitializeCapabilities{ExperimentalAPI: pointer(true), RequestAttestation: pointer(false)},
	}
	if clientInfo != nil {
		params.ClientInfo = protocol.ClientInfo{Name: clientInfo.Name, Title: copyPointer(clientInfo.Title), Version: clientInfo.Version}
	}
	if c.formEnabled {
		params.Capabilities.Extensions = map[string]json.RawMessage{"openai/form": json.RawMessage("{}")}
	}
	if err := c.request(ctx, "initialize", params, &reply); err != nil {
		return err
	}
	if _, err := c.write(ctx, rpcFrame{Method: "initialized", Params: json.RawMessage("{}")}, nil); err != nil {
		return err
	}
	match := versionPattern.FindStringSubmatch(reply.Version + " " + reply.UserAgent)
	c.mu.Lock()
	if len(match) > 1 {
		c.version = match[1]
	}
	c.forkMode = acpmeta.VerifiedForkMode(c.version, "0.155.1", acpmeta.ForkTurn)
	c.mu.Unlock()
	return nil
}

func (c *Client) checkReadyLocked() error {
	if c.closing || c.life.Err() != nil || !c.ready {
		return errUnavailable
	}
	return nil
}

func (c *Client) request(ctx context.Context, method string, params, result any) error {
	_, err := c.call(ctx, method, params, result, nil, nil)
	if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil && method != protocol.MethodConfigRead {
		c.checkAnswering()
	}
	return err
}

func (c *Client) checkAnswering() {
	c.mu.Lock()
	if c.probing || c.checkReadyLocked() != nil {
		c.mu.Unlock()
		return
	}
	c.probing = true
	c.mu.Unlock()
	go func() {
		ctx, cancel := context.WithTimeout(c.life, requestTimeout)
		defer cancel()
		_, err := c.call(ctx, protocol.MethodConfigRead, map[string]bool{"includeLayers": false}, nil, nil, nil)
		c.mu.Lock()
		defer c.mu.Unlock()
		c.probing = false
		if errors.Is(err, context.DeadlineExceeded) {
			c.fail(errors.New("Codex app server stopped answering local requests"))
		}
	}()
}

func (c *Client) call(ctx context.Context, method string, params, result any, admit func() error, capture func(rpcFrame)) (frameWrite, error) {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	var payload json.RawMessage
	if params != nil {
		var err error
		payload, err = json.Marshal(params)
		if err != nil {
			return frameWrite{}, err
		}
	}
	c.mu.Lock()
	c.nextID++
	id := strconv.FormatUint(c.nextID, 10)
	pending := &pendingCall{done: make(chan rpcFrame, 1), capture: capture}
	c.pending[id] = pending
	c.mu.Unlock()
	defer func() { c.mu.Lock(); delete(c.pending, id); c.mu.Unlock() }()
	written, err := c.write(ctx, rpcFrame{ID: json.RawMessage(id), Method: method, Params: payload}, admit)
	if err != nil {
		return written, err
	}
	var frame rpcFrame
	select {
	case frame = <-pending.done:
	case <-c.life.Done():
		select {
		case frame = <-pending.done:
		default:
			return written, context.Cause(c.life)
		}
	case <-ctx.Done():
		select {
		case frame = <-pending.done:
		default:
			return written, context.Cause(ctx)
		}
	}
	if frame.Error != nil {
		return written, immutableNativeError(frame.Error)
	}
	if len(frame.Result) == 0 || bytes.Equal(frame.Result, []byte("null")) {
		return written, errors.New("Codex returned no native result")
	}
	if result != nil {
		return written, json.Unmarshal(frame.Result, result)
	}
	return written, nil
}

func (c *Client) write(ctx context.Context, frame rpcFrame, admit func() error) (frameWrite, error) {
	data, err := json.Marshal(frame)
	if err != nil {
		return frameWrite{}, err
	}
	data = append(data, '\n')
	if len(data) > maxFrameBytes {
		return frameWrite{}, errors.New("Codex outbound frame exceeds byte limit")
	}
	select {
	case c.writeGate <- struct{}{}:
		defer func() { <-c.writeGate }()
	case <-ctx.Done():
		return frameWrite{}, context.Cause(ctx)
	case <-c.life.Done():
		return frameWrite{}, context.Cause(c.life)
	}
	c.mu.Lock()
	err = ctx.Err()
	if err == nil {
		err = context.Cause(c.life)
	}
	if err == nil && admit != nil {
		err = admit()
	}
	c.mu.Unlock()
	if err != nil {
		return frameWrite{}, err
	}
	writer := c.process.Stdin()
	if writer == nil {
		return frameWrite{}, errUnavailable
	}
	// The owner check is the write linearization point. A subsequent cancel
	// treats this request as potentially submitted while the write finishes.
	if deadline, ok := writer.(interface{ SetWriteDeadline(time.Time) error }); ok {
		when := time.Now().Add(controlTimeout)
		if outer, ok := ctx.Deadline(); ok && outer.Before(when) {
			when = outer
		}
		_ = deadline.SetWriteDeadline(when)
		defer deadline.SetWriteDeadline(time.Time{})
	}
	n, err := writer.Write(data)
	if err == nil && n != len(data) {
		err = io.ErrShortWrite
	}
	if err != nil && n != len(data) {
		c.fail(fmt.Errorf("Codex native stream write: %w", err))
	}
	return frameWrite{bytes: n, size: len(data)}, err
}

func (c *Client) readFrames() {
	defer close(c.readerDone)
	reader := bufio.NewScanner(c.process.Stdout())
	reader.Buffer(make([]byte, 64<<10), maxFrameBytes)
	for reader.Scan() {
		var frame rpcFrame
		if err := json.Unmarshal(reader.Bytes(), &frame); err != nil {
			c.fail(errors.New("invalid Codex native frame"))
			return
		}
		if frame.Error != nil {
			message := safeDiagnostic(frame.Error.Message)
			if message == "" {
				message = "Codex rejected the native request"
			}
			frame.Error = &acp.RequestError{Code: frame.Error.Code, Message: message, Data: safeRPCData(frame.Error.Data)}
		}
		c.mu.Lock()
		switch {
		case frame.Method != "" && len(frame.ID) > 0:
			c.captureInteractionLocked(frame)
		case frame.Method != "":
			c.receiveLocked(frame.Method, frame.Params)
		case len(frame.ID) > 0:
			key := string(frame.ID)
			if pending := c.pending[key]; pending != nil {
				delete(c.pending, key)
				if pending.capture != nil {
					pending.capture(frame)
				}
				pending.done <- frame
			}
		}
		c.mu.Unlock()
	}
	err := reader.Err()
	if err == nil {
		waitCtx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
		exit, waitErr := c.process.Wait(waitCtx)
		cancel()
		err = io.EOF
		if waitErr == nil && exit.Err != nil {
			err = fmt.Errorf("Codex app server exited (%d, %s): %w", exit.Code, strings.TrimSpace(exit.Diagnostic), exit.Err)
		}
	}
	c.fail(err)
}

// Close cancels the original owners and waits for process and callback release.
func (c *Client) Close(ctx context.Context) error {
	c.mu.Lock()
	c.closing = true
	c.signalReceiptsLocked()
	start := c.startDone
	active := make([]*turn, 0, len(c.sessions))
	for _, s := range c.sessions {
		for draining := range s.draining {
			active = append(active, draining)
		}
		if s.active != nil {
			active = append(active, s.active)
			c.cancelTurnLocked(s.active)
		}
	}
	c.mu.Unlock()
	for _, t := range active {
		if err := waitDone(ctx, t.released); err != nil {
			return err
		}
	}
	c.endLife(errUnavailable)
	if err := c.process.Close(ctx); err != nil {
		return err
	}
	if start != nil {
		if err := waitDone(ctx, start); err != nil {
			return err
		}
	}
	c.mu.Lock()
	readerStarted := c.readerStarted
	c.mu.Unlock()
	if readerStarted {
		if err := waitDone(ctx, c.readerDone); err != nil {
			return err
		}
	}
	c.drainOnce.Do(func() { go func() { c.interactions.Wait(); close(c.callbacksDone) }() })
	return waitDone(ctx, c.callbacksDone)
}

func waitDone(ctx context.Context, done <-chan struct{}) error {
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

// fail withdraws the native transport and starts releasing the same child
// even if the caller has already received its final result.
func (c *Client) fail(err error) {
	c.endLife(err)
	c.retireOnce.Do(func() {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), controlTimeout)
			defer cancel()
			if err := c.process.Close(ctx); err != nil && c.options.Logger != nil {
				c.options.Logger.Warn("Codex app server cleanup requires retry", "error", err)
			}
		}()
	})
}

type nativeRequestError struct {
	// code and message are the retained, sanitized native RPC rejection.
	code    int
	message string
	// data stores an immutable JSON representation of allowed diagnostic facts.
	data string
}

func immutableNativeError(rpc *acp.RequestError) error {
	data, _ := json.Marshal(rpc.Data)
	return nativeRequestError{code: rpc.Code, message: rpc.Message, data: string(data)}
}

func (e nativeRequestError) Error() string { return e.message }

func (e nativeRequestError) As(target any) bool {
	rpc, ok := target.(**acp.RequestError)
	if !ok {
		return false
	}
	result := &acp.RequestError{Code: e.code, Message: e.message}
	_ = json.Unmarshal([]byte(e.data), &result.Data)
	*rpc = result
	return true
}

func safeRPCData(raw any) any {
	m, ok := raw.(map[string]any)
	if !ok {
		return nil
	}
	facts := make(map[string]any)
	if resource, ok := m["resource"].(string); ok {
		switch resource {
		case "thread", "workspace", "workspace-template", "rollout":
			facts["resource"] = resource
		}
	}
	if id, ok := m["threadId"].(string); ok && uuidPattern.MatchString(id) {
		facts["threadId"] = id
	}
	if len(facts) == 0 {
		return nil
	}
	return facts
}

func safeDiagnostic(record string) string {
	record = agentclient.RedactProcessDiagnostic(record)
	return diagnosticSecret.ReplaceAllString(record, "[redacted]")
}

func pointer[T any](v T) *T { return &v }

var _ agentclient.Client = (*Client)(nil)

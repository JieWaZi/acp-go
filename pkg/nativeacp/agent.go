// Package nativeacp connects installed stdio agents through the ACP SDK.
// NeuxBot modifications bind bounded transport and process shutdown to one owner; see sdk/PATCHES.md.
package nativeacp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"time"

	acp "github.com/JieWaZi/acp-go/sdk"
)

const (
	maxNativeDiagnosticBytes       = 4 << 10
	maxNativeDiagnosticRecordBytes = 64 << 10
)

const (
	nativeNotificationQueueCapacity = 256
	nativeNotificationQueueBytes    = 16 << 20
)

// Config describes an installed native process without installing or changing user configuration.
type Config struct {
	// PublicSessionID maps outgoing callback identities without changing native session ownership.
	PublicSessionID func(acp.SessionId) acp.SessionId
	// PromptFIFO selects queued admission; false rejects overlapping prompts using the same lifecycle.
	PromptFIFO bool
	// StrictCloseSession forwards native close even when support was not advertised.
	StrictCloseSession bool
	// Command is the installed executable path or command name.
	Command string
	// Args contains the complete native protocol launch arguments.
	Args []string
	// VersionArgs contains an optional version probe independent of protocol arguments.
	VersionArgs []string
	// RuntimeName identifies the runtime when native implementation metadata is absent.
	RuntimeName string
	// Environment is the complete child environment; nil snapshots the parent at construction.
	Environment []string
	// WorkingDirectory is the process directory; each session carries its own cwd.
	WorkingDirectory string
	// Logger receives diagnostics separately from protocol stdout.
	Logger *slog.Logger
	// CallbackAdapter handles private reverse requests; nil accepts only standard ACP callbacks.
	CallbackAdapter CallbackAdapter
	// PreparePrompt runs after admission with cancellable preparation ownership.
	PreparePrompt func(context.Context, acp.PromptRequest) error
	// RedactDiagnostic sanitizes whole stderr records before logging or retention.
	RedactDiagnostic func(string) string
	// FilterSessionUpdate runs before delivery without I/O; PromptCommitted is safe to read.
	// True forwards the update.
	FilterSessionUpdate func(acp.SessionNotification) bool
	// SessionAdapter handles private model or reasoning configuration; nil uses standard ACP.
	SessionAdapter SessionAdapter
	// EnrichConfigOptions is a pure catalog projection; it must not perform I/O or reenter Agent.
	EnrichConfigOptions func(SessionSnapshot) []acp.SessionConfigOption
	// PermissionAdapter handles private approval policies; nil delegates to the host.
	PermissionAdapter PermissionAdapter
}

// Agent owns native execution; the SDK owns transport, request correlation and notification order.
type Agent struct {
	// conn is the sole native JSON-RPC connection.
	conn *acp.Connection
	// command owns the external process.
	command *exec.Cmd
	// input closes the native process input stream.
	input io.WriteCloser
	// output unblocks the SDK reader during shutdown.
	output io.ReadCloser
	// cancel terminates the process lifetime.
	cancel context.CancelFunc
	// done closes after the process has been reaped.
	done chan struct{}
	// waitErr records the process exit status.
	waitErr error
	// waitMu protects waitErr.
	waitMu sync.Mutex
	// stderr retains only sanitized diagnostic records.
	stderr *diagnosticWriter
	// closed closes when explicit shutdown begins and releases pending callbacks.
	closed chan struct{}
	// versionOnce limits runtime probing to one initialization attempt.
	versionOnce sync.Once
	// version retains the observed version, or remains empty when unknown.
	version string
	// closeOnce makes explicit shutdown idempotent.
	closeOnce sync.Once
	// mutex protects host binding, native session state and prompt ownership.
	mutex sync.Mutex
	// host is the ACP server connection supplied by the caller.
	host *acp.AgentSideConnection
	// bound closes when the host is bound.
	bound chan struct{}
	// capabilities retains capabilities actually advertised by the native process.
	capabilities acp.AgentCapabilities
	// sessions maps native identities to the currently opened session generation.
	sessions map[acp.SessionId]*sessionOptions
	// turnOwners is the sole index of admitted operations, retained through ordered cancellation.
	turnOwners map[acp.SessionId]*promptWaiter
	// toolSessions retains the session identity observed in each tool notification.
	toolSessions map[acp.ToolCallId]acp.SessionId
	// config freezes protocol configuration for this native instance.
	config Config
	// toolDetails accumulates native tool evidence for permission review.
	toolDetails map[acp.ToolCallId]acp.ToolCallUpdate
	// toolRevisions distinguishes individual native tool evidence updates.
	toolRevisions map[acp.ToolCallId]*toolEvidenceRevision
	// toolChanged wakes approval requests waiting for preceding tool notifications.
	toolChanged chan struct{}
	// messageSequence assigns non-reusable identities to messages without native IDs.
	messageSequence uint64
}

var _ acp.Agent = (*Agent)(nil)
var _ acp.AgentLoader = (*Agent)(nil)

// NewAgent starts an installed program; its caller owns Close.
func NewAgent(ctx context.Context, config Config) (*Agent, error) {
	if ctx == nil || config.Logger == nil || strings.TrimSpace(config.Command) == "" {
		return nil, errors.New("native ACP requires context, command and logger")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	config.Args = slices.Clone(config.Args)
	config.VersionArgs = slices.Clone(config.VersionArgs)
	config.Environment = slices.Clone(config.Environment)
	resolved, err := ResolveCommand(config.Command, config.Environment)
	if err != nil {
		return nil, err
	}
	lifetime, cancel := context.WithCancel(context.WithoutCancel(ctx))
	config.Command = resolved
	command, err := CommandContext(lifetime, resolved, config.Args, config.Environment)
	if err != nil {
		cancel()
		return nil, err
	}
	config.Environment = append([]string{}, command.Env...)
	command.Dir = config.WorkingDirectory
	command.WaitDelay = 2 * time.Second
	stderr := &diagnosticWriter{logger: config.Logger, redact: config.RedactDiagnostic}
	command.Stderr = stderr
	input, err := command.StdinPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	output, err := command.StdoutPipe()
	if err != nil {
		cancel()
		_ = input.Close()
		return nil, err
	}
	if err := command.Start(); err != nil {
		cancel()
		_ = input.Close()
		_ = output.Close()
		return nil, fmt.Errorf("starting native ACP: %w", err)
	}
	agent := &Agent{
		command:       command,
		input:         input,
		output:        output,
		cancel:        cancel,
		done:          make(chan struct{}),
		stderr:        stderr,
		closed:        make(chan struct{}),
		bound:         make(chan struct{}),
		sessions:      make(map[acp.SessionId]*sessionOptions),
		turnOwners:    make(map[acp.SessionId]*promptWaiter),
		toolSessions:  make(map[acp.ToolCallId]acp.SessionId),
		config:        config,
		toolDetails:   make(map[acp.ToolCallId]acp.ToolCallUpdate),
		toolRevisions: make(map[acp.ToolCallId]*toolEvidenceRevision),
	}
	agent.conn, err = acp.NewConnectionWithOptions(agent.handle, input, output, acp.ConnectionOptions{
		Context:                   lifetime,
		NotificationQueueCapacity: nativeNotificationQueueCapacity,
		NotificationQueueBytes:    nativeNotificationQueueBytes,
		CloseTransport:            func() error { return errors.Join(input.Close(), output.Close()) },
	})
	if err != nil {
		cancel()
		_ = input.Close()
		_ = output.Close()
		_ = command.Wait()
		return nil, err
	}
	go func() {
		<-agent.conn.Done()
		agent.closePromptQueues()
		// Explicit shutdown drains stderr within its bounded exit window; disconnection terminates the process.
		select {
		case <-agent.closed:
		default:
			cancel()
		}
		waitErr := command.Wait()
		stderr.flush()
		agent.waitMu.Lock()
		agent.waitErr = waitErr
		agent.waitMu.Unlock()
		close(agent.done)
	}()
	return agent, nil
}

// diagnosticWriter only retains complete sanitized records. Raw partial lines never
// reach either logs or exit errors, including when the process writes in chunks.
type diagnosticWriter struct {
	// logger receives sanitized diagnostic lines.
	logger *slog.Logger
	// redact is a pure whole-line sanitizer; nil suppresses native content.
	redact func(string) string
	// mutex protects the partial line and safe tail.
	mutex sync.Mutex
	// pending buffers a bounded record until its delimiter arrives.
	pending []byte
	// depth tracks structured payloads across pretty-printed lines.
	depth int
	// opening holds a bracket until the following text establishes record syntax.
	opening byte
	// openingNewline records whether bracket classification crossed a line boundary.
	openingNewline bool
	// openingIndented distinguishes inspected object fields from following prose.
	openingIndented bool
	// inspect accepts native console object strings and indentation boundaries.
	inspect bool
	// inspectLine waits for the next line's first character before continuing a dump.
	inspectLine bool
	// quote tracks JSON and inspected object strings within a payload.
	quote byte
	// escaped records a string escape across pipe chunks.
	escaped bool
	// ansi strips terminal control sequences before record parsing and redaction.
	ansi byte
	// overflow discards the entire oversized line, including later chunks.
	overflow bool
	// tail retains at most 64 KiB of sanitized diagnostics.
	tail []byte
}

func (writer *diagnosticWriter) Write(data []byte) (int, error) {
	writer.mutex.Lock()
	defer writer.mutex.Unlock()
	for _, b := range data {
		if !writer.stripControl(b) {
			writer.recordByte(b)
		}
	}
	return len(data), nil
}

// recordByte classifies a record once, preserving scan state between chunks.
func (writer *diagnosticWriter) recordByte(b byte) {
	if writer.opening != 0 {
		switch b {
		case '\n':
			writer.openingNewline = true
			writer.openingIndented = false
			writer.appendByte(b)
			return
		case ' ', '\t', '\r':
			if writer.openingNewline && b != '\r' {
				writer.openingIndented = true
			}
			writer.appendByte(b)
			return
		}
		structured := b == '"' || b == '{' || b == '[' || b == '}' || b == ']'
		if writer.opening == '[' && (b == '-' || b >= '0' && b <= '9' || b == 't' || b == 'f' || b == 'n') {
			structured = true
		}
		inspect := writer.openingNewline && writer.openingIndented && !structured
		writer.opening = 0
		if structured || inspect {
			writer.depth = 1
			writer.inspect = inspect
		} else if writer.openingNewline {
			writer.line()
		}
		writer.openingNewline, writer.openingIndented = false, false
	}
	if writer.inspectLine {
		writer.inspectLine = false
		if b != ' ' && b != '\t' && b != '}' && b != ']' {
			// A malformed inspected payload cannot lend its field labels to an
			// unrelated following line; suppress the incomplete payload as a unit.
			writer.overflow = true
			writer.line()
		}
	}
	if writer.depth > 0 {
		if writer.quote != 0 {
			if writer.escaped {
				writer.escaped = false
			} else if b == '\\' {
				writer.escaped = true
			} else if b == writer.quote {
				writer.quote = 0
			}
		} else {
			switch b {
			case '"':
				writer.quote = b
			case '\'', '`':
				if writer.inspect {
					writer.quote = b
				}
			case '{', '[':
				writer.depth++
			case '}', ']':
				writer.depth--
			}
		}
		if writer.inspect && b == '\n' {
			writer.inspectLine = true
			writer.quote = 0
			writer.escaped = false
		}
	} else if b == '{' || b == '[' {
		writer.opening = b
	}
	if b == '\n' && writer.depth == 0 && writer.opening == 0 {
		writer.line()
	} else {
		writer.appendByte(b)
	}
}

func (writer *diagnosticWriter) appendByte(b byte) {
	if writer.overflow {
		return
	}
	if len(writer.pending) == maxNativeDiagnosticRecordBytes {
		writer.pending = nil
		writer.overflow = true
		return
	}
	writer.pending = append(writer.pending, b)
}

// stripControl consumes CSI, OSC and two-byte terminal escape sequences incrementally.
func (writer *diagnosticWriter) stripControl(b byte) bool {
	switch writer.ansi {
	case 1:
		switch b {
		case '[':
			writer.ansi = 2
		case ']':
			writer.ansi = 3
		default:
			writer.ansi = 0
		}
		return true
	case 2:
		if b >= 0x40 && b <= 0x7e {
			writer.ansi = 0
		}
		return true
	case 3:
		if b == 7 {
			writer.ansi = 0
		} else if b == 27 {
			writer.ansi = 4
		}
		return true
	case 4:
		if b == '\\' {
			writer.ansi = 0
		} else {
			writer.ansi = 3
		}
		return true
	}
	if b == 27 {
		writer.ansi = 1
		return true
	}
	return false
}

// line runs under mutex and never publishes any unredacted bytes.
func (writer *diagnosticWriter) line() {
	var safe string
	if writer.overflow {
		safe = "[oversized diagnostic suppressed]"
	} else if len(writer.pending) > 0 {
		if writer.redact == nil {
			safe = "[native diagnostic suppressed]"
		} else {
			safe = writer.redact(string(writer.pending))
		}
	}
	writer.pending, writer.overflow = nil, false
	writer.depth, writer.quote, writer.opening = 0, 0, 0
	writer.inspect, writer.inspectLine, writer.escaped = false, false, false
	if safe == "" {
		return
	}
	if len(safe) > maxNativeDiagnosticBytes {
		safe = safe[:maxNativeDiagnosticBytes]
	}
	writer.tail = append(writer.tail, safe...)
	writer.tail = append(writer.tail, '\n')
	const limit = 64 << 10
	if len(writer.tail) > limit {
		writer.tail = append([]byte(nil), writer.tail[len(writer.tail)-limit:]...)
	}
	if writer.logger != nil {
		writer.logger.Warn("native ACP stderr", "stderr", safe)
	}
}

// flush is called only after Wait has drained the stderr copier.
func (writer *diagnosticWriter) flush() {
	writer.mutex.Lock()
	defer writer.mutex.Unlock()
	if writer.depth != 0 || writer.quote != 0 {
		writer.overflow = true
	}
	writer.line()
}

func (writer *diagnosticWriter) String() string {
	writer.mutex.Lock()
	defer writer.mutex.Unlock()
	return string(writer.tail)
}

// SetAgentConnection binds the host once and releases startup callbacks.
func (agent *Agent) SetAgentConnection(connection *acp.AgentSideConnection) {
	agent.mutex.Lock()
	defer agent.mutex.Unlock()
	if agent.host == nil && connection != nil {
		agent.host = connection
		close(agent.bound)
	}
}

// Close interrupts owned transport, reaps the process within the caller deadline, and permits retries.
func (agent *Agent) Close(ctx context.Context) error {
	select {
	case <-agent.closed:
		select {
		case <-agent.done:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	default:
	}
	select {
	case <-agent.done:
		err := agent.exitError()
		agent.closeOnce.Do(func() { close(agent.closed); agent.conn.Close(); agent.closePromptQueues() })
		return err
	default:
	}
	agent.closeOnce.Do(func() {
		close(agent.closed)
		// Close the owned transport first; protocol shutdown could itself be blocked by backpressure.
		agent.conn.Close()
		agent.closePromptQueues()
	})
	timer := time.NewTimer(500 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-agent.done:
		return nil
	case <-timer.C:
	case <-ctx.Done():
	}
	agent.cancel()
	_ = agent.output.Close()
	select {
	case <-agent.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// exitError preserves the actual process exit with sanitized diagnostics.
func (agent *Agent) exitError() error {
	agent.waitMu.Lock()
	defer agent.waitMu.Unlock()
	if agent.waitErr == nil {
		return nil
	}
	if detail := strings.TrimSpace(agent.stderr.String()); detail != "" {
		return fmt.Errorf("native ACP process exited: %w: %s", agent.waitErr, detail)
	}
	return fmt.Errorf("native ACP process exited: %w", agent.waitErr)
}

// transportError appends safe exit details after a transport disconnect.
func (agent *Agent) transportError(err error) error {
	if err == nil {
		return nil
	}
	select {
	case <-agent.conn.Done():
		timer := time.NewTimer(200 * time.Millisecond)
		defer timer.Stop()
		select {
		case <-agent.done:
			return errors.Join(err, agent.exitError())
		case <-timer.C:
		}
	default:
	}
	return err
}

// sendNativeRequest preserves structured protocol errors and appends safe process diagnostics.
func sendNativeRequest[T any](agent *Agent, ctx context.Context, method string, request any) (T, error) {
	response, err := acp.SendRequest[T](agent.conn, ctx, method, request)
	return response, agent.transportError(err)
}

// Initialize retains actual native capabilities without inventing unsupported features.
func (agent *Agent) Initialize(ctx context.Context, request acp.InitializeRequest) (acp.InitializeResponse, error) {
	if agent.config.CallbackAdapter != nil {
		agent.config.CallbackAdapter.PrepareInitialize(&request)
	}
	response, err := sendNativeRequest[acp.InitializeResponse](agent, ctx, "initialize", request)
	if err == nil {
		agent.completeRuntimeVersion(ctx, &response)
		agent.mutex.Lock()
		agent.capabilities = response.AgentCapabilities
		agent.mutex.Unlock()
	}
	return response, err
}

// Prompt shares the same admission, preparation and native completion path.
func (agent *Agent) Prompt(ctx context.Context, request acp.PromptRequest) (acp.PromptResponse, error) {
	response, _, err := agent.PromptWithReceipt(ctx, request)
	return response, err
}

// ManagesSessionLifecycle prevents outer dispatch from abandoning native completion.
func (agent *Agent) ManagesSessionLifecycle() bool { return true }

// Cancel revokes interactions immediately and orders native cancellation after submission.
func (agent *Agent) Cancel(ctx context.Context, request acp.CancelNotification) error {
	return agent.cancelPromptOwner(ctx, request, nil)
}

// CloseSession seals admission and settles the native operation before closing its session.
func (agent *Agent) CloseSession(ctx context.Context, request acp.CloseSessionRequest) (acp.CloseSessionResponse, error) {
	release, err := agent.beginSessionTransition(request.SessionId, true, false)
	if err != nil {
		return acp.CloseSessionResponse{}, err
	}
	defer release(nil, false)
	agent.mutex.Lock()
	state := agent.sessions[request.SessionId]
	agent.mutex.Unlock()
	defer func() {
		agent.mutex.Lock()
		defer agent.mutex.Unlock()
		if agent.sessions[request.SessionId] == state {
			delete(agent.sessions, request.SessionId)
		}
	}()
	agent.mutex.Lock()
	supported := agent.capabilities.SessionCapabilities.Close != nil
	owner := agent.turnOwners[request.SessionId]
	agent.mutex.Unlock()
	if owner != nil {
		if err := agent.cancelPromptOwner(ctx, acp.CancelNotification{SessionId: request.SessionId}, owner); err != nil {
			return acp.CloseSessionResponse{}, err
		}
		select {
		case <-owner.done:
		case <-ctx.Done():
			agent.conn.Close()
			return acp.CloseSessionResponse{}, ctx.Err()
		case <-agent.conn.Done():
			return acp.CloseSessionResponse{}, agent.transportError(agent.conn.Err())
		}
	}
	if agent.config.SessionAdapter != nil {
		agent.config.SessionAdapter.ForgetSession(request.SessionId)
	}
	if supported || agent.config.StrictCloseSession {
		return sendNativeRequest[acp.CloseSessionResponse](agent, ctx, "session/close", request)
	}
	return acp.CloseSessionResponse{}, nil
}

// HandleExtensionMethod forwards extensions explicitly requested by the caller.
func (agent *Agent) HandleExtensionMethod(ctx context.Context, method string, params json.RawMessage) (any, error) {
	return sendNativeRequest[json.RawMessage](agent, ctx, method, params)
}

// handle delegates typed callbacks; framing, ordering and backpressure remain SDK responsibilities.
func (agent *Agent) handle(ctx context.Context, method string, params json.RawMessage) (any, *acp.RequestError) {
	select {
	case <-agent.bound:
	case <-agent.closed:
		return nil, acp.NewInternalError(nil)
	case <-ctx.Done():
		return nil, acp.NewInternalError(nil)
	}
	result, err := agent.dispatch(ctx, method, params)
	if err == nil {
		return result, nil
	}
	var requestErr *acp.RequestError
	if errors.As(err, &requestErr) {
		return nil, requestErr
	}
	return nil, acp.NewInternalError(map[string]any{"message": "native ACP client callback failed"})
}

// decodeCall uses SDK request types and preserves their validation errors.
func decodeCall[P any, R any](ctx context.Context, data json.RawMessage, call func(context.Context, P) (R, error)) (any, error) {
	var request P
	if err := json.Unmarshal(data, &request); err != nil {
		return nil, acp.NewInvalidParams(nil)
	}
	if validator, ok := any(&request).(interface {
		// Validate preserves SDK field and union validation.
		Validate() error
	}); ok {
		if err := validator.Validate(); err != nil {
			return nil, acp.NewInvalidParams(nil)
		}
	}
	return call(ctx, request)
}

// Done closes after the native process has been fully reaped.
func (agent *Agent) Done() <-chan struct{} { return agent.done }

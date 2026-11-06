package acp

import (
	"context"
	"io"
	"log/slog"
	"sync"
)

// SessionLifecycleOwner opts an Agent into owning session-scoped prompt admission and cancellation.
// The SDK still propagates cancellation of an individual request or its connection.
type SessionLifecycleOwner interface {
	// ManagesSessionLifecycle reports whether the agent owns the session lifecycle.
	ManagesSessionLifecycle() bool
}

// PromptPreparation prepares interactions under the admitted operation's context.
// The owner must run any cleanup before releasing the operation.
type PromptPreparation func(context.Context) (cleanup func(), err error)

// SessionAdmission lets the agent reserve the operation that each frame targets.
// Both hooks must return without I/O or waiting. The connection executes returned
// continuations once, even when their context has already been canceled.
type SessionAdmission struct {
	// Prompt reserves a prompt. On success it must return a continuation.
	Prompt func(context.Context, PromptRequest, PromptPreparation) (run func() (PromptResponse, error), err error)
	// Cancel captures and marks the current operation. A nil run with no error
	// means the notification was handled synchronously, including idle cancels.
	Cancel func(CancelNotification) (run func(context.Context) error, err error)
}

// AgentSideConnection represents the agent's view of a connection to a client.
type AgentSideConnection struct {
	conn  *Connection
	agent Agent

	mu             sync.Mutex
	sessionCancels map[string]context.CancelFunc
	// admission is the optional pair of ingress hooks, installed together.
	admission SessionAdmission
	// promptPreparation captures the optional interaction preparation at ingress.
	promptPreparation func(PromptRequest) PromptPreparation
}

// NewAgentSideConnection creates a new agent-side connection and starts
// reading immediately. Use NewAgentSideConnectionWithBinder when the agent
// installs admission hooks or needs the connection before its first request.
func NewAgentSideConnection(agent Agent, peerInput io.Writer, peerOutput io.Reader) *AgentSideConnection {
	return NewAgentSideConnectionWithBinder(agent, peerInput, peerOutput, nil)
}

// NewAgentSideConnectionWithBinder calls bind with the initialized connection
// before reading peer frames. The binder may install hooks and must not wait
// for peer responses.
func NewAgentSideConnectionWithBinder(agent Agent, peerInput io.Writer, peerOutput io.Reader, bind func(*AgentSideConnection)) *AgentSideConnection {
	asc := &AgentSideConnection{agent: agent, sessionCancels: make(map[string]context.CancelFunc)}
	asc.conn = newConnection(asc.handleWithExtensions, asc.prepareSessionInbound, peerInput, peerOutput, nil)
	if bind != nil {
		bind(asc)
	}
	asc.conn.start()
	return asc
}

// SetSessionAdmission installs both admission hooks once. It panics when
// either hook is missing or a binding already exists. Install hooks with
// NewAgentSideConnectionWithBinder to cover the first frame.
func (c *AgentSideConnection) SetSessionAdmission(admission SessionAdmission) {
	if admission.Prompt == nil || admission.Cancel == nil {
		panic("session admission requires prompt and cancel hooks")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.admission.Prompt != nil {
		panic("session admission is already bound")
	}
	c.admission = admission
}

// SessionAdmission returns the installed hooks for an agent wrapper whose
// direct methods must share the same operation owner as wire ingress.
func (c *AgentSideConnection) SessionAdmission() (SessionAdmission, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.admission, c.admission.Prompt != nil
}

// SetPromptPreparation installs an optional factory for prompt preparation.
// The factory captures local state without waiting or I/O. It panics when
// capture is nil or already installed.
func (c *AgentSideConnection) SetPromptPreparation(capture func(PromptRequest) PromptPreparation) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if capture == nil || c.promptPreparation != nil {
		panic("prompt preparation requires a single factory")
	}
	c.promptPreparation = capture
}

// Close aborts the logical connection and its in-flight operations. It is
// idempotent and does not wait for or close the caller's borrowed streams.
// The stream owner must unblock any physical Read or Write that is in progress.
func (c *AgentSideConnection) Close() { c.conn.Close() }

// Done closes when the peer disconnects or Close aborts the connection.
// It does not report whether a borrowed stream has finished reading.
func (c *AgentSideConnection) Done() <-chan struct{} { return c.conn.Done() }

// SetLogger directs connection diagnostics to the provided logger.
func (c *AgentSideConnection) SetLogger(l *slog.Logger) { c.conn.SetLogger(l) }

// Package acpserver binds an SDK Agent to the process stdio protocol boundary.
package acpserver

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	acp "github.com/JieWaZi/acp-go-sdk"
	"github.com/JieWaZi/acp-go/pkg/userinput"
)

const adapterCloseTimeout = 5 * time.Second

var (
	// ErrInvalidAgent reports a missing SDK Agent implementation.
	ErrInvalidAgent = errors.New("invalid agent")
	// ErrInvalidInput reports a missing ACP request input stream.
	ErrInvalidInput = errors.New("invalid protocol input")
	// ErrInvalidOutput reports a missing ACP response output stream.
	ErrInvalidOutput = errors.New("invalid protocol output")
)

// connectionBinder lets an agent install admission hooks and acquire the
// connection it uses to send updates and permission requests.
type connectionBinder interface {
	// SetAgentConnection binds the single agent-side connection created by the SDK.
	SetAgentConnection(connection *acp.AgentSideConnection)
}

// adapterCloser releases native resources independently of the server connection.
// Only adapters that own resources implement it; cleanup is not required by the protocol Agent interface.
type adapterCloser interface {
	// Close releases adapter-owned processes and connections within the supplied context.
	Close(ctx context.Context) error
}

// Server holds the dependencies needed to start an SDK AgentSideConnection.
type Server struct {
	// agent implements the SDK Agent protocol.
	agent acp.Agent
	// input reads line-delimited ACP JSON-RPC messages from the client.
	input io.Reader
	// output carries only ACP JSON-RPC messages to the client.
	output io.Writer
}

// New validates dependencies without starting the protocol connection.
func New(agent acp.Agent, input io.Reader, output io.Writer) (*Server, error) {
	if agent == nil {
		return nil, fmt.Errorf("creating acp server: %w", ErrInvalidAgent)
	}
	if input == nil {
		return nil, fmt.Errorf("creating acp server: %w", ErrInvalidInput)
	}
	if output == nil {
		return nil, fmt.Errorf("creating acp server: %w", ErrInvalidOutput)
	}
	return &Server{
		agent:  agent,
		input:  input,
		output: output,
	}, nil
}

// Serve binds the SDK connection before reading and waits for disconnect or
// cancellation. Owner cancellation aborts the logical connection; peer EOF
// preserves the SDK's received-notification drain. The caller owns the streams
// and must unblock any physical I/O still in progress after Serve returns.
func (s *Server) Serve(ctx context.Context) error {
	// The SDK names streams from the peer’s perspective. Agent output is peer input.
	connection := acp.NewAgentSideConnectionWithBinder(s.agent, s.output, s.input, func(connection *acp.AgentSideConnection) {
		if binder, ok := s.agent.(connectionBinder); ok {
			binder.SetAgentConnection(connection)
		}
	})

	select {
	case <-connection.Done():
	case <-ctx.Done():
	}
	if ctx.Err() != nil {
		connection.Close()
	}
	return s.closeAdapter(ctx)
}

// closeAdapter releases adapter resources with an independent, bounded context.
// Cleanup retains a short window to end child processes and connections after owner cancellation.
func (s *Server) closeAdapter(ctx context.Context) error {
	closer, ok := s.agent.(adapterCloser)
	if !ok {
		return nil
	}

	closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), adapterCloseTimeout)
	defer cancel()
	if err := closer.Close(closeCtx); err != nil {
		return fmt.Errorf("closing adapter: %w", err)
	}
	return nil
}

// NewWithUserInput adds managed questions at the single ACP boundary for hosts that need unified interactions.
func NewWithUserInput(agent acp.Agent, input io.Reader, output io.Writer) (*Server, error) {
	if agent == nil {
		return New(agent, input, output)
	}
	return New(userinput.Wrap(agent), input, output)
}

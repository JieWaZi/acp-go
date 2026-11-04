package qoder

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"sync"
	"time"

	agentclient "github.com/JieWaZi/acp-go/pkg/agent_next"
	"github.com/JieWaZi/acp-go/pkg/agent_next/acpclient"
)

type management struct {
	// options is the immutable regional command and credential selection.
	options Options
	// mu serializes admission and publication of the active process.
	mu sync.Mutex
	// closed rejects reads once cleanup starts.
	closed bool
	// active holds the admitted process until it is reaped.
	active *agentclient.Process
}

func (m *management) run(ctx context.Context, args []string, read func(context.Context, *agentclient.Process) error) (result error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	process, err := agentclient.NewProcess(agentclient.ProcessSpec{
		Executable: m.options.Executable, Args: append(slices.Clone(m.options.PrefixArgs), args...),
		Env: m.options.Environment, Dir: m.options.WorkingDirectory,
	})
	if err != nil {
		return err
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return acpclient.ErrRestartRequired
	}
	if m.active != nil {
		m.mu.Unlock()
		return acpclient.ErrBusy
	}
	m.active = process
	m.mu.Unlock()
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		result = errors.Join(result, m.closeProcess(cleanup, process))
	}()
	op, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := process.Start(op); err != nil {
		return err
	}
	stop := context.AfterFunc(op, func() {
		_ = process.Stdin().Close()
		_ = process.Stdout().Close()
	})
	defer stop()
	err = read(op, process)
	if op.Err() != nil {
		return op.Err()
	}
	m.mu.Lock()
	closed := m.closed
	m.mu.Unlock()
	if closed {
		return acpclient.ErrRestartRequired
	}
	return err
}

func (m *management) close(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	m.closed = true
	process := m.active
	m.mu.Unlock()
	if process == nil {
		return nil
	}
	return m.closeProcess(ctx, process)
}

func (m *management) closeProcess(ctx context.Context, process *agentclient.Process) error {
	if err := process.Close(ctx); err != nil {
		return err
	}
	m.mu.Lock()
	if m.active == process {
		m.active = nil
	}
	m.mu.Unlock()
	return nil
}

type controlAccount struct {
	// Account is absent until the CLI reports account evidence.
	Account *NativeIdentity `json:"account"`
}

type controlChannel struct {
	// scanner reads bounded native control and system frames.
	scanner *bufio.Scanner
	// encoder writes only SDK control requests without user input.
	encoder *json.Encoder
	// sequence creates distinct request IDs on this management process.
	sequence int
	// received bounds all bytes read, including ignored messages.
	received int
}

type controlResponse struct {
	// Type distinguishes SDK control replies from other CLI messages.
	Type string `json:"type"`
	// Response carries only the selected reply metadata and result.
	Response struct {
		// ID identifies the exact SDK request.
		ID string `json:"request_id"`
		// Subtype is success or error.
		Subtype string `json:"subtype"`
		// Body is retained only for the matching successful reply.
		Body json.RawMessage `json:"response"`
	} `json:"response"`
}

// control uses Qoder Agent SDK 1.0.58's stdio control envelope. The ACP
// transport has no account or quota read, and this channel sends no prompts.
func (m *management) control(ctx context.Context, read func(*controlChannel, controlAccount) error) error {
	args := []string{"--print", "--output-format", "stream-json", "--input-format", "stream-json",
		"--no-session-persistence", "--strict-mcp-config", "--setting-sources", ""}
	return m.run(ctx, args, func(_ context.Context, process *agentclient.Process) error {
		channel := controlChannel{
			scanner: bufio.NewScanner(process.Stdout()),
			encoder: json.NewEncoder(process.Stdin()),
		}
		channel.scanner.Buffer(make([]byte, 16<<10), 4<<20)
		var bootstrap controlAccount
		if err := channel.request("initialize", &bootstrap); err != nil {
			return err
		}
		return read(&channel, bootstrap)
	})
}

func (c *controlChannel) request(method string, result any) error {
	c.sequence++
	id := "acp-go-qoder-management-" + strconv.Itoa(c.sequence)
	if err := c.encoder.Encode(map[string]any{
		"type": "control_request", "request_id": id, "request": map[string]string{"type": method},
	}); err != nil {
		return errors.New("Qoder CLI control request write failed")
	}
	for c.scanner.Scan() {
		c.received += len(c.scanner.Bytes()) + 1
		if c.received > 4<<20 {
			return errors.New("Qoder CLI control output exceeds its limit")
		}
		var frame controlResponse
		if json.Unmarshal(c.scanner.Bytes(), &frame) != nil {
			return errors.New("Qoder CLI control output is invalid")
		}
		if frame.Type != "control_response" || frame.Response.ID != id {
			continue
		}
		if frame.Response.Subtype != "success" {
			return errors.New("Qoder CLI rejected " + method)
		}
		if len(frame.Response.Body) == 0 || string(frame.Response.Body) == "null" ||
			json.Unmarshal(frame.Response.Body, result) != nil {
			return errors.New("Qoder CLI control reply is invalid")
		}
		return nil
	}
	return errors.New("Qoder CLI control reply was not received")
}

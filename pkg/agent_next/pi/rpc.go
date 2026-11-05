package pi

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"sync"
	"sync/atomic"
	"time"

	agentclient "github.com/JieWaZi/acp-go/pkg/agent_next"
)

const (
	maxRecord      = 16 << 20
	maxQueued      = 256
	controlTimeout = 4 * time.Second
)

type record struct {
	Type    string          `json:"type"`
	ID      string          `json:"id"`
	Command string          `json:"command"`
	Success *bool           `json:"success"`
	Error   string          `json:"error"`
	Data    json.RawMessage `json:"data"`
}

type rpcRejection struct {
	command string
	detail  string
}

func (e *rpcRejection) Error() string { return "Pi rejected " + e.command }

type rpc struct {
	process     *agentclient.Process
	writeGate   chan struct{}
	dismissGate chan struct{}
	done        chan struct{}
	ready       chan struct{}
	sequence    atomic.Uint64

	mu      sync.Mutex
	failure error
	pending map[string]chan record
	active  *invocation
}

func newRPC(p *agentclient.Process) *rpc {
	return &rpc{process: p, writeGate: make(chan struct{}, 1), dismissGate: make(chan struct{}, 8), done: make(chan struct{}), ready: make(chan struct{}), pending: make(map[string]chan record)}
}

func (r *rpc) fail(err error) {
	r.mu.Lock()
	if r.failure == nil {
		r.failure = err
		close(r.done)
	}
	r.mu.Unlock()
}

func (r *rpc) err() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.failure
}

func (r *rpc) read() {
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), controlTimeout)
		defer cancel()
		_ = r.process.Close(ctx)
	}()
	scanner := bufio.NewScanner(r.process.Stdout())
	scanner.Buffer(make([]byte, 32<<10), maxRecord)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var rec record
		if json.Unmarshal(line, &rec) != nil || rec.Type == "" {
			r.fail(errors.New("Pi returned invalid RPC JSON"))
			return
		}
		if rec.Type == "response" {
			r.mu.Lock()
			if waiter := r.pending[rec.ID]; waiter != nil {
				delete(r.pending, rec.ID)
				waiter <- rec
			}
			r.mu.Unlock()
			continue
		}
		r.mu.Lock()
		active := r.active
		r.mu.Unlock()
		if rec.Type == "extension_error" && active == nil {
			r.fail(classified("extension_error", "session_open"))
			return
		}
		if rec.Type == "extension_ui_request" {
			var ui uiRequest
			if json.Unmarshal(line, &ui) == nil {
				if ui.Method == "setStatus" && ui.StatusKey == "acp-go.extension-ready" && ui.StatusText == "ready" {
					select {
					case <-r.ready:
					default:
						close(r.ready)
					}
				}
				if ui.ID != "" && ui.interactive() {
					// Dialogs may be needed before the prompt acknowledgment. They
					// have their own bounded delivery lane, outside the pipe reader.
					queued, withinBudget := false, true
					if active != nil {
						queued, withinBudget = active.enqueueUI(ui, len(line))
					}
					if !withinBudget {
						r.fail(errors.New("Pi extension dialog budget exhausted"))
						return
					}
					if !queued {
						select {
						case r.dismissGate <- struct{}{}:
							go r.dismiss(ui.ID)
						default:
							r.fail(errors.New("Pi idle extension dialog budget exhausted"))
							return
						}
					}
					continue
				}
			}
		}
		if active != nil && !active.enqueueEvent(line) {
			r.fail(errors.New("Pi RPC event budget exhausted"))
			return
		}
	}
	if scanner.Err() != nil {
		r.fail(errors.New("Pi RPC stdout failed or exceeded its record limit"))
	} else {
		r.fail(io.EOF)
	}
}

func (r *rpc) dismiss(id string) {
	defer func() { <-r.dismissGate }()
	ctx, cancel := context.WithTimeout(context.Background(), controlTimeout)
	defer cancel()
	_, _ = r.write(ctx, map[string]any{"type": "extension_ui_response", "id": id, "cancelled": true}, nil)
}

func (r *rpc) nextID() string { return fmt.Sprintf("pi-%d", r.sequence.Add(1)) }

func (r *rpc) call(ctx context.Context, command string, args map[string]any, dst any) error {
	_, err := r.request(ctx, r.nextID(), command, args, dst, nil)
	return err
}

func (r *rpc) request(ctx context.Context, id, command string, args map[string]any, dst any, beforeWrite func() bool) (agentclient.Submission, error) {
	payload := map[string]any{"id": id, "type": command}
	maps.Copy(payload, args)
	response := make(chan record, 1)
	r.mu.Lock()
	if r.failure != nil {
		err := r.failure
		r.mu.Unlock()
		return agentclient.SubmissionNotSent, err
	}
	r.pending[id] = response
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		delete(r.pending, id)
		r.mu.Unlock()
	}()
	sent, err := r.write(ctx, payload, beforeWrite)
	if err != nil {
		return sent, err
	}
	var rec record
	select {
	case rec = <-response:
	case <-ctx.Done():
		return sent, context.Cause(ctx)
	case <-r.done:
		// A final response can be read immediately before stdout EOF.
		select {
		case rec = <-response:
		default:
			return sent, r.err()
		}
	}
	if rec.Command != command || rec.Success == nil {
		err := errors.New("Pi returned an invalid RPC response")
		r.fail(err)
		return sent, err
	}
	if !*rec.Success {
		return sent, &rpcRejection{command: command, detail: rec.Error}
	}
	if dst != nil && len(rec.Data) > 0 && json.Unmarshal(rec.Data, dst) != nil {
		return sent, errors.New("Pi returned an invalid RPC result")
	}
	return sent, nil
}

func (r *rpc) write(ctx context.Context, payload map[string]any, beforeWrite func() bool) (agentclient.Submission, error) {
	encoded, err := json.Marshal(payload)
	if err != nil || len(encoded) >= maxRecord {
		return agentclient.SubmissionNotSent, errors.New("Pi RPC command exceeds its encoding budget")
	}
	select {
	case <-ctx.Done():
		return agentclient.SubmissionNotSent, context.Cause(ctx)
	case <-r.done:
		return agentclient.SubmissionNotSent, r.err()
	case r.writeGate <- struct{}{}:
	}
	defer func() { <-r.writeGate }()
	if err = r.err(); err != nil {
		return agentclient.SubmissionNotSent, err
	}
	if err = context.Cause(ctx); err != nil {
		return agentclient.SubmissionNotSent, err
	}
	if beforeWrite != nil && !beforeWrite() {
		return agentclient.SubmissionNotSent, context.Canceled
	}
	stdin := r.process.Stdin()
	if stdin == nil {
		return agentclient.SubmissionNotSent, io.ErrClosedPipe
	}
	// The Process stdin is an os.Pipe. Arm cancellation only while holding
	// the write gate so its deadline cannot interrupt a subsequent command.
	if pipe, ok := stdin.(interface{ SetWriteDeadline(time.Time) error }); ok {
		unarmed := make(chan struct{})
		stop := context.AfterFunc(ctx, func() {
			_ = pipe.SetWriteDeadline(time.Now())
			close(unarmed)
		})
		defer func() {
			if !stop() {
				<-unarmed
			}
			_ = pipe.SetWriteDeadline(time.Time{})
		}()
	}
	encoded = append(encoded, '\n')
	n, err := stdin.Write(encoded)
	if n == len(encoded) {
		return agentclient.SubmissionSent, err
	}
	r.fail(errors.New("Pi RPC stdin write failed"))
	if n > 0 {
		return agentclient.SubmissionUnknown, r.err()
	}
	return agentclient.SubmissionNotSent, r.err()
}

package claude

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"sync"
	"time"

	agentclient "github.com/JieWaZi/acp-go/pkg/agentclient"
	"github.com/JieWaZi/acp-go/pkg/claude/protocol"
)

const maxNativeFrame = 8 << 20

// wire owns the ordered reader, native control responses and the stdin gate.
type wire struct {
	// process remains owned until the session or management operation releases it.
	process *agentclient.Process
	// gate serializes writes, including their final guard and receipt.
	gate chan struct{}
	// done closes after the reader folds its final transport observation.
	done chan struct{}
	// mu protects pending native request IDs.
	mu      sync.Mutex
	pending map[string]*controlWaiter
	// limit bounds all native writes even when their parent has no deadline.
	limit time.Duration
	// onWriteFailure seals the owned stream when a frame write fails.
	onWriteFailure func(error)
}

type controlWaiter struct {
	// result carries the acknowledged native response, once.
	result chan protocol.ControlResponse
	// apply folds optional control evidence in native receive order.
	apply func(json.RawMessage)
}

func startWire(process *agentclient.Process, limit time.Duration) *wire {
	return &wire{process: process, gate: make(chan struct{}, 1), done: make(chan struct{}),
		pending: make(map[string]*controlWaiter), limit: limit}
}

func (w *wire) read(observe func(protocol.Message), disconnected func(error)) {
	defer close(w.done)
	scanner := bufio.NewScanner(w.process.Stdout())
	scanner.Buffer(make([]byte, 64<<10), maxNativeFrame)
	for scanner.Scan() {
		raw := bytes.TrimSpace(scanner.Bytes())
		if len(raw) == 0 {
			continue
		}
		m, err := protocol.DecodeMessage(raw)
		if err != nil {
			disconnected(errors.New("invalid Claude stream-json frame"))
			return
		}
		if response, ok := m.(*protocol.ControlResponseMessage); ok {
			w.receive(response.Response)
		} else {
			observe(m)
		}
	}
	if scanner.Err() != nil {
		disconnected(errors.New("Claude stream-json read failed"))
		return
	}
	disconnected(errTransport)
}

func (w *wire) receive(response protocol.ControlResponse) {
	w.mu.Lock()
	waiter := w.pending[response.RequestID]
	delete(w.pending, response.RequestID)
	w.mu.Unlock()
	if waiter == nil {
		return
	}
	if response.Subtype == "success" && waiter.apply != nil {
		waiter.apply(response.Response)
	}
	waiter.result <- response
}

func (w *wire) write(ctx context.Context, frame any, guard func() error, finish func(agentclient.Submission, error)) error {
	data, err := json.Marshal(frame)
	if err != nil {
		return err
	}
	if len(data) >= maxNativeFrame {
		return errors.New("Claude outgoing frame exceeds size limit")
	}
	ctx, cancel := context.WithTimeout(ctx, w.limit)
	defer cancel()
	if err := w.lock(ctx); err != nil {
		return err
	}
	defer w.unlock()
	file, ok := w.process.Stdin().(*os.File)
	if !ok {
		return errTransport
	}
	deadline, _ := ctx.Deadline()
	if err := file.SetWriteDeadline(deadline); err != nil {
		return errTransport
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// A cancellation after the guard can race the write. Only its byte count
	// determines whether the native input was definitely not sent.
	stopDone := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		_ = file.SetWriteDeadline(time.Now())
		close(stopDone)
	})
	defer func() {
		if !stop() {
			<-stopDone
		}
		_ = file.SetWriteDeadline(time.Time{})
	}()
	data = append(data, '\n')
	if guard != nil {
		if err := guard(); err != nil {
			return err
		}
	}
	n, writeErr := file.Write(data)
	evidence := agentclient.SubmissionNotSent
	switch {
	case n == len(data):
		evidence = agentclient.SubmissionSent
	case n > 0:
		evidence = agentclient.SubmissionUnknown
	}
	if writeErr != nil || n != len(data) {
		err = errTransport
	}
	if finish != nil {
		finish(evidence, err)
	}
	if err != nil && w.onWriteFailure != nil {
		w.onWriteFailure(err)
	}
	return err
}

func (w *wire) lock(ctx context.Context) error {
	select {
	case w.gate <- struct{}{}:
		if ctx.Err() != nil {
			w.unlock()
			return ctx.Err()
		}
		select {
		case <-w.done:
			w.unlock()
			return errTransport
		default:
			return nil
		}
	case <-ctx.Done():
		return ctx.Err()
	case <-w.done:
		return errTransport
	}
}

func (w *wire) unlock() { <-w.gate }

func (w *wire) call(ctx context.Context, request any, response any, guard func() error, apply func(json.RawMessage)) error {
	id, err := newUUID()
	if err != nil {
		return err
	}
	frame, err := protocol.NewControlRequest(id, request)
	if err != nil {
		return err
	}
	waiter := &controlWaiter{result: make(chan protocol.ControlResponse, 1), apply: apply}
	w.mu.Lock()
	w.pending[id] = waiter
	w.mu.Unlock()
	defer func() { w.mu.Lock(); delete(w.pending, id); w.mu.Unlock() }()
	if err := w.write(ctx, frame, guard, nil); err != nil {
		return err
	}
	readResponse := func(reply protocol.ControlResponse) error {
		if reply.Subtype != "success" {
			return errors.New("Claude rejected native control")
		}
		if response == nil {
			return nil
		}
		if len(reply.Response) == 0 {
			return io.ErrUnexpectedEOF
		}
		if err := json.Unmarshal(reply.Response, response); err != nil {
			return errors.New("invalid Claude native control response")
		}
		return nil
	}
	select {
	case reply := <-waiter.result:
		return readResponse(reply)
	case <-ctx.Done():
		return ctx.Err()
	case <-w.done:
		select {
		case reply := <-waiter.result:
			return readResponse(reply)
		default:
			return errTransport
		}
	}
}

package codex

import (
	"context"
	"errors"
	"sync"
	"time"

	acp "github.com/JieWaZi/acp-go/sdk"
)

const (
	maxQueuedSessionUpdates   = 64
	sessionUpdateDrainTimeout = 2 * time.Second
)

var errEventDeliveryBackpressure = errors.New("Codex ACP event delivery queue is full")

// eventDelivery owns the one ordered ACP notification writer for this Agent.
type eventDelivery struct {
	// ctx is the lifetime of the owning ACP Agent.
	ctx context.Context
	// queue bounds work even if the peer blocks a write indefinitely.
	queue chan func()
}

// startEventDelivery starts the sole delivery worker.
func startEventDelivery(ctx context.Context) *eventDelivery {
	d := &eventDelivery{ctx: ctx, queue: make(chan func(), maxQueuedSessionUpdates)}
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case job := <-d.queue:
				if ctx.Err() == nil {
					job()
				}
			}
		}
	}()
	return d
}

// enqueue never waits for the host while the native protocol reader holds a frame.
func (d *eventDelivery) enqueue(job func()) error {
	if err := d.ctx.Err(); err != nil {
		return err
	}
	select {
	case d.queue <- job:
		return nil
	default:
		return errEventDeliveryBackpressure
	}
}

// turnDelivery tracks delivery for a single native turn and exposes a terminal barrier.
type turnDelivery struct {
	// owner is the connection-wide writer used by all concurrent turns.
	owner *eventDelivery
	// updater writes the mapped ACP notification.
	updater sessionUpdater
	// ctx is cancelled when the invocation or connection is released.
	ctx context.Context
	// stillOwned fences queued updates from a retired thread generation.
	stillOwned func() bool
	// mu protects the first error and pending-job count.
	mu sync.Mutex
	// remaining includes jobs queued or currently writing.
	remaining int
	// drained closes once the current group of jobs reaches zero.
	drained chan struct{}
	// failure is the first error, if any, for this turn.
	failure error
}

// SessionUpdate queues an ordered update without waiting for a slow ACP peer.
func (d *turnDelivery) SessionUpdate(_ context.Context, update acp.SessionNotification) error {
	d.mu.Lock()
	if d.failure != nil {
		err := d.failure
		d.mu.Unlock()
		return err
	}
	if d.remaining == 0 {
		d.drained = make(chan struct{})
	}
	d.remaining++
	d.mu.Unlock()
	err := d.owner.enqueue(func() {
		d.mu.Lock()
		failure := d.failure
		d.mu.Unlock()
		if failure == nil && d.updater != nil {
			if !d.stillOwned() {
				failure = ErrSessionClosing
			} else {
				failure = d.updater.SessionUpdate(d.ctx, update)
			}
		}
		d.complete(failure)
	})
	if err != nil {
		d.complete(err)
	}
	return err
}

// complete records the first delivery error and signals the drain barrier.
func (d *turnDelivery) complete(err error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.failure == nil {
		d.failure = err
	}
	d.remaining--
	if d.remaining == 0 {
		close(d.drained)
	}
}

// wait finishes the ACP delivery boundary after the native reader processed the terminal.
func (d *turnDelivery) wait(ctx context.Context) error {
	d.mu.Lock()
	drained, failure := d.drained, d.failure
	d.mu.Unlock()
	if failure != nil || drained == nil {
		return failure
	}
	ctx, cancel := context.WithTimeout(ctx, sessionUpdateDrainTimeout)
	defer cancel()
	select {
	case <-drained:
		d.mu.Lock()
		defer d.mu.Unlock()
		return d.failure
	case <-ctx.Done():
		d.mu.Lock()
		defer d.mu.Unlock()
		if d.failure == nil {
			d.failure = ctx.Err()
		}
		return d.failure
	case <-d.owner.ctx.Done():
		return d.owner.ctx.Err()
	}
}

package claude

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"sync"
	"time"

	acp "github.com/JieWaZi/acp-go-sdk"
	"github.com/JieWaZi/acp-go/pkg/acpmeta"
	agentclient "github.com/JieWaZi/acp-go/pkg/agentclient"
)

const (
	maxOutcomes                         = 128
	maxPageCount                        = 16
	maxPageBytes                        = 512 << 10
	maxOutcomeBytes                     = 128 << 10
	receiptUsageObservationsUnavailable = "turn.usage.observations"
	maxTextBytes                        = 64 << 10
	maxObservations                     = 256
	maxObservationBytes                 = 2 << 20
	maxObservationFrame                 = 256 << 10
)

var errObservationBudget = errors.New("Claude observation capacity exhausted")

// outcomeLedger keeps canonical finalized results after the native generation
// closes. ACK only removes a result from this ledger, never from a direct waiter.
type outcomeLedger struct {
	mu        sync.Mutex
	changed   chan struct{}
	producing bool
	reserved  int
	outcomes  []storedOutcome
}

type storedOutcome struct {
	outcome agentclient.NativeOutcome
	bytes   int
}

func newOutcomeLedger() *outcomeLedger { return &outcomeLedger{changed: make(chan struct{})} }

func (l *outcomeLedger) signal() { close(l.changed); l.changed = make(chan struct{}) }

func (l *outcomeLedger) full() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.reserved+len(l.outcomes) >= maxOutcomes
}

func (l *outcomeLedger) reserve() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.reserved+len(l.outcomes) >= maxOutcomes {
		return false
	}
	l.reserved++
	return true
}

func (l *outcomeLedger) producer(alive bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.producing = alive
	l.signal()
}

func (l *outcomeLedger) empty() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return !l.producing && l.reserved == 0 && len(l.outcomes) == 0
}

func (l *outcomeLedger) complete(outcome agentclient.NativeOutcome, retain bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.reserved--
	if retain {
		l.outcomes = append(l.outcomes, storedOutcome{outcome: outcome, bytes: outcomeSize(outcome)})
	}
	l.signal()
}

func outcomeSize(o agentclient.NativeOutcome) int {
	// Measure the serialized data. Error values are deliberately represented by
	// safe classifications in the outer ACP projection.
	data, _ := json.Marshal(o)
	return len(data)
}

func boundOutcome(o *agentclient.NativeOutcome) {
	if o.FinalMessage != nil && len(o.FinalMessage.Text) > maxTextBytes {
		o.FinalMessage = nil
		o.Unavailable = append(o.Unavailable, acpmeta.ReceiptResultTextUnavailable)
	}
	if outcomeSize(*o) <= maxOutcomeBytes {
		return
	}
	// Drop overlapping evidence first; the primary accounting and result
	// answer take priority within the canonical retention budget.
	if u := o.Turn.Usage; u != nil && len(u.Observations) > 0 {
		u.Observations = nil
		o.Unavailable = append(o.Unavailable, receiptUsageObservationsUnavailable)
		if outcomeSize(*o) <= maxOutcomeBytes {
			return
		}
	}
	if o.FinalMessage != nil {
		o.FinalMessage = nil
		o.Unavailable = append(o.Unavailable, acpmeta.ReceiptResultTextUnavailable)
	}
	if outcomeSize(*o) <= maxOutcomeBytes {
		return
	}
	if u := o.Turn.Usage; u != nil {
		if u.Models != nil {
			u.Models = nil
			o.Unavailable = append(o.Unavailable, acpmeta.ReceiptModelUsageUnavailable)
		}
		if len(u.Model) > 512 {
			u.Model = ""
			o.Unavailable = append(o.Unavailable, acpmeta.ReceiptUsageModelUnavailable)
		}
		if u.Cost != nil && len(u.Cost.Amount) > 128 {
			u.Cost = nil
			o.Unavailable = append(o.Unavailable, acpmeta.ReceiptUsageCostUnavailable)
		}
	}
}

// ReadNativeOutcomes reads a bounded prefix and applies only exact, already
// finalized acknowledgments for the same native session.
func (c *Client) ReadNativeOutcomes(ctx context.Context, req acpmeta.NativeTurnReceiptsRequest, wait bool) (agentclient.NativeOutcomePage, error) {
	c.mu.Lock()
	id := acp.SessionId(req.SessionID)
	ledger := c.ledgers[id]
	c.mu.Unlock()
	if ledger == nil {
		return agentclient.NativeOutcomePage{}, nil
	}
	ack := make(map[string]struct{}, min(len(req.AckInvocationIDs), maxOutcomes))
	if len(req.AckInvocationIDs) > maxOutcomes {
		return agentclient.NativeOutcomePage{}, acp.NewInvalidParams(map[string]any{"message": "too many Claude receipt acknowledgments"})
	}
	for _, inv := range req.AckInvocationIDs {
		ack[inv] = struct{}{}
	}
	ledger.mu.Lock()
	if len(ack) > 0 {
		ledger.outcomes = slices.DeleteFunc(ledger.outcomes, func(o storedOutcome) bool { _, ok := ack[o.outcome.Turn.InvocationID]; return ok })
	}
	for wait && len(ledger.outcomes) == 0 && ledger.producing {
		changed := ledger.changed
		ledger.mu.Unlock()
		if err := await(ctx, changed); err != nil {
			return agentclient.NativeOutcomePage{}, err
		}
		ledger.mu.Lock()
	}
	var result agentclient.NativeOutcomePage
	var bytes int
	for _, entry := range ledger.outcomes {
		if len(result.Outcomes) == maxPageCount || bytes+entry.bytes > maxPageBytes {
			result.More = true
			break
		}
		result.Outcomes = append(result.Outcomes, entry.outcome.Snapshot())
		bytes += entry.bytes
	}
	empty := !ledger.producing && ledger.reserved == 0 && len(ledger.outcomes) == 0
	ledger.mu.Unlock()
	if empty {
		c.mu.Lock()
		if c.sessions[id] == nil && c.ledgers[id] == ledger && ledger.empty() {
			delete(c.ledgers, id)
		}
		c.mu.Unlock()
	}
	return result, nil
}

type delivery struct {
	inv          *invocation
	scope        agentclient.Scope
	notification acp.SessionNotification
	terminal     []*invocation
	bytes        int
}

func (s *session) enqueueLocked(inv *invocation, n acp.SessionNotification) {
	scope := agentclient.Scope{SessionID: s.id}
	if inv != nil {
		scope = inv.scope
	}
	s.enqueueScopedLocked(inv, scope, n)
}

func (s *session) enqueueScopedLocked(inv *invocation, scope agentclient.Scope, n acp.SessionNotification) {
	if s.client.host.Observe == nil {
		return
	}
	if inv != nil && inv.deliveryErr != nil {
		return
	}
	data, err := json.Marshal(n)
	size := len(data)
	if err != nil || s.observationCount >= maxObservations || size > maxObservationFrame || s.observationBytes+size > maxObservationBytes {
		if inv != nil && inv.deliveryErr == nil {
			inv.deliveryErr = errObservationBudget
		}
		return
	}
	s.deliveries = append(s.deliveries, delivery{inv: inv, scope: scope, notification: n, bytes: size})
	s.observationCount++
	s.observationBytes += size
	s.signalLocked()
}

func (s *session) sealLocked(inv *invocation) {
	if inv.terminal {
		return
	}
	s.freezeNativeLocked(inv)
	s.publishAfterDeliveryLocked(inv)
}

func (s *session) freezeNativeLocked(inv *invocation) {
	inv.terminal = true
	inv.sealedAt = time.Now()
	inv.cancel()
	close(inv.closed)
	if s.current == inv {
		s.current = nil
	}
	if inv.outcome.Turn.Usage == nil {
		inv.outcome.Turn.Usage = partialSnapshot(inv)
	}
	boundOutcome(&inv.outcome)
	if inv.outcome.Turn.Status != "" || inv.outcome.Turn.Usage != nil {
		meta := acpmeta.WithNativeTurn(nil, inv.outcome.Turn)
		if inv.outcome.Turn.Usage != nil {
			meta = acpmeta.WithTurnUsage(meta, *inv.outcome.Turn.Usage)
		}
		s.sendTurnLocked(inv, usageUpdate(inv.outcome.ContextUsage), meta)
	}
}

func (s *session) publishAfterDeliveryLocked(invocations ...*invocation) {
	// One read transition may retire a lost predecessor and observe the
	// current native terminal. Publish both before releasing either waiter.
	s.deliveries = append(s.deliveries, delivery{terminal: invocations})
	s.signalLocked()
}

func (s *session) deliver() {
	defer close(s.deliveryDone)
	var hung <-chan error
	for {
		s.mu.Lock()
		for len(s.deliveries) == 0 || (!s.open && !s.deliveryStop) {
			if s.deliveryStop && len(s.deliveries) == 0 {
				s.mu.Unlock()
				// Resource release waits for the actual callback even after
				// its timeout no longer delays native outcome publication.
				s.ledger.producer(false)
				if hung != nil {
					<-hung
				}
				return
			}
			wait := s.changed
			s.mu.Unlock()
			<-wait
			s.mu.Lock()
		}
		d := s.deliveries[0]
		s.deliveries[0] = delivery{}
		s.deliveries = s.deliveries[1:]
		if len(d.terminal) > 0 {
			for _, inv := range d.terminal {
				s.finalizeOutcomeLocked(inv)
			}
			for _, inv := range d.terminal {
				for _, in := range inv.inputs {
					s.finishInputLocked(in, nil)
				}
			}
			s.mu.Unlock()
			continue
		}
		s.observationCount--
		s.observationBytes -= d.bytes
		now := time.Now()
		deadline := s.observationDeadlineLocked(now)
		if d.inv != nil && !d.inv.sealedAt.IsZero() {
			deadline = minTime(deadline, d.inv.sealedAt.Add(s.client.options.ObservationTimeout))
		}
		expired := !deadline.After(now)
		if expired && d.inv != nil && d.inv.deliveryErr == nil {
			d.inv.deliveryErr = context.DeadlineExceeded
		}
		skip := expired || s.observerErr != nil || (d.inv != nil && d.inv.deliveryErr != nil)
		s.mu.Unlock()
		if skip {
			continue
		}
		ctx, cancel := context.WithDeadline(context.Background(), deadline)
		result := make(chan error, 1)
		go func() { result <- s.client.host.Observe(ctx, d.scope, d.notification) }()
		var err error
		select {
		case err = <-result:
		case <-ctx.Done():
			err = ctx.Err()
			hung = result
		}
		cancel()
		if err != nil {
			s.mu.Lock()
			if d.inv != nil && d.inv.deliveryErr == nil {
				d.inv.deliveryErr = err
			}
			if hung != nil {
				s.observerErr = err
			}
			s.mu.Unlock()
		}
	}
}

// A sealed terminal bounds the entire delivery prefix preceding its marker.
// Unrelated session notifications cannot each spend a fresh receipt budget.
func (s *session) observationDeadlineLocked(now time.Time) time.Time {
	limit := s.client.options.ObservationTimeout
	deadline := now.Add(limit)
	for _, queued := range s.deliveries {
		if len(queued.terminal) == 0 {
			continue
		}
		for _, inv := range queued.terminal {
			deadline = minTime(deadline, inv.sealedAt.Add(limit))
		}
		break
	}
	return deadline
}

func (s *session) finalizeOutcomeLocked(inv *invocation) {
	if s.observerErr != nil && inv.deliveryErr == nil {
		inv.deliveryErr = s.observerErr
	}
	inv.outcome.DeliveryError = inv.deliveryErr
	inv.finalized = true
	retain := inv.outcome.Turn.Origin != acpmeta.NativeTurnOriginHost || s.client.foregroundReceipts
	for _, in := range inv.inputs {
		retain = retain || in.cancelRequested || in.detached
	}
	s.ledger.complete(inv.outcome, retain)
}

func minTime(left, right time.Time) time.Time {
	if left.Before(right) {
		return left
	}
	return right
}

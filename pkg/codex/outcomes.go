package codex

import (
	"context"
	"slices"

	acp "github.com/JieWaZi/acp-go-sdk"
	"github.com/JieWaZi/acp-go/pkg/acpmeta"
	agentclient "github.com/JieWaZi/acp-go/pkg/agentclient"
)

const (
	maxRetainedOutcomes = 128
	maxOutcomePage      = 4
	maxReceiptText      = 64 << 10
	maxNativeIDBytes    = 512
)

type outcomeEntry struct {
	// outcome is filled once the terminal and input controls have settled.
	outcome *agentclient.NativeOutcome
}

func (c *Client) orderOutcomeLocked(t *turn) {
	if t.retainOutcome && t.outcomeEntry == nil {
		t.outcomeEntry = &outcomeEntry{}
		c.receipts[t.session.id] = append(c.receipts[t.session.id], t.outcomeEntry)
	}
}

func (c *Client) publishOutcomeLocked(t *turn, outcome *agentclient.NativeOutcome) {
	if t.retainOutcome {
		if outcome == nil {
			c.retainedOutcomes--
		} else {
			c.orderOutcomeLocked(t)
			snapshot := outcome.Snapshot()
			t.outcomeEntry.outcome = &snapshot
		}
	}
	c.signalReceiptsLocked()
}

// capOutcome bounds the canonical retained payload. A negotiated foreground
// result and its later receipt carry the same availability and evidence.
func capOutcome(outcome *agentclient.NativeOutcome) {
	if outcome.FinalMessage != nil && len(outcome.FinalMessage.Text) > maxReceiptText {
		outcome.FinalMessage = nil
		outcome.Unavailable = append(outcome.Unavailable, acpmeta.ReceiptResultTextUnavailable)
	}
	if usage := outcome.Turn.Usage; usage != nil && len(usage.Model) > maxNativeIDBytes {
		usage.Model = ""
		outcome.Unavailable = append(outcome.Unavailable, acpmeta.ReceiptUsageModelUnavailable)
	}
}

func (c *Client) signalReceiptsLocked() {
	close(c.receiptChanged)
	c.receiptChanged = make(chan struct{})
}

// ReadNativeOutcomes returns a stable prefix of this session's unacknowledged
// outcomes. ACK of a different session or an unsettled invocation has no effect.
func (c *Client) ReadNativeOutcomes(ctx context.Context, req acpmeta.NativeTurnReceiptsRequest, wait bool) (agentclient.NativeOutcomePage, error) {
	if req.SessionID == "" || len(req.AckInvocationIDs) > maxRetainedOutcomes {
		return agentclient.NativeOutcomePage{}, acp.NewInvalidParams("invalid Codex native receipt request")
	}
	if err := ctx.Err(); err != nil {
		return agentclient.NativeOutcomePage{}, err
	}
	id := acp.SessionId(req.SessionID)
	ack := make(map[string]bool, len(req.AckInvocationIDs))
	for _, inv := range req.AckInvocationIDs {
		ack[inv] = true
	}
	c.mu.Lock()
	entries := c.receipts[id]
	if len(ack) > 0 && len(entries) > 0 {
		next := slices.DeleteFunc(entries, func(e *outcomeEntry) bool { return e.outcome != nil && ack[e.outcome.Turn.InvocationID] })
		c.retainedOutcomes -= len(entries) - len(next)
		if len(next) == 0 {
			delete(c.receipts, id)
		} else {
			c.receipts[id] = next
		}
	}
	c.mu.Unlock()
	for {
		c.mu.Lock()
		entries = c.receipts[id]
		s := c.sessions[id]
		pending := s != nil && s.hasPendingOutcome()
		live := s != nil && !s.closing && !c.closing && c.life.Err() == nil
		var ready int
		for _, entry := range entries {
			if entry.outcome == nil {
				break
			}
			ready++
		}
		if ready > 0 || !wait || !pending && !live {
			result := agentclient.NativeOutcomePage{More: ready > maxOutcomePage}
			for _, entry := range entries[:min(maxOutcomePage, ready)] {
				result.Outcomes = append(result.Outcomes, entry.outcome.Snapshot())
			}
			c.mu.Unlock()
			return result, nil
		}
		changed := c.receiptChanged
		life := c.life.Done()
		if !live {
			life = nil
		}
		c.mu.Unlock()
		select {
		case <-changed:
		case <-life:
		case <-ctx.Done():
			return agentclient.NativeOutcomePage{}, ctx.Err()
		}
	}
}

var _ agentclient.NativeOutcomeReader = (*Client)(nil)

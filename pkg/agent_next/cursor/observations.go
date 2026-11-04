package cursor

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/JieWaZi/acp-go/pkg/acpmeta"
	agentclient "github.com/JieWaZi/acp-go/pkg/agent_next"
	acp "github.com/JieWaZi/acp-go/sdk"
)

const (
	deliveryTimeout        = 500 * time.Millisecond
	compactionContextRatio = 0.6
)

var cursorErrorClass = regexp.MustCompile(`^[a-zA-Z0-9_$]+$`)

// Cursor emits these complete provider-error chunks as assistant text. Keep
// normal assistant text that merely mentions the same error words.
func cursorNoise(text string) bool {
	detail, ok := strings.CutPrefix(strings.TrimSpace(text), "Error: ")
	if !ok {
		return false
	}
	if name, remainder, hasClass := strings.Cut(detail, ": "); hasClass && cursorErrorClass.MatchString(name) {
		detail = remainder
	}
	return detail == "WritableIterable is closed"
}

func cursorAuthLost(text string) bool {
	detail, ok := strings.CutPrefix(text, "\n\n")
	if !ok || strings.Contains(detail, "\n") {
		return false
	}
	return detail == "Please sign in to continue" || strings.HasPrefix(detail, "Error: [unauthenticated] Backend rejected authentication.")
}

type delivery struct {
	// ctx and cancel bound callbacks and stop abandoned observations.
	ctx    context.Context
	cancel context.CancelFunc
	// done signals the end of all admitted observations after sealing.
	done chan struct{}
	// pending counts queued and currently delivered observations.
	pending int
	// sealed prevents adding later frames to this invocation or replay.
	sealed bool
	// err retains the first failure or capacity loss independently of native facts.
	err error
}

type observation struct {
	// batch is nil for standalone session-level observations.
	batch *delivery
	// scope is fixed before handing the observation to another goroutine.
	scope agentclient.Scope
	// update is the typed ACP event.
	update acp.SessionNotification
	// action optionally delivers a captured reverse notification.
	action func(context.Context) error
	// size accounts for the native frame associated with the event.
	size int
}

type receivedUpdate struct {
	// sequence is the native SDK reader's receive order.
	sequence uint64
	// size is the bytes charged while the frame is retained.
	size int
	// id is the exact native target rather than the most recent active session.
	id acp.SessionId
	// config preserves absent fields in native config_option_update.
	config json.RawMessage
	// usage retains context and consumption field presence before SDK projection.
	usage *usageUpdate
	// note contains the standard observation, including the typed payload.
	note acp.SessionNotification
}

type usageUpdate struct {
	// Used and Size are independent context evidence.
	Used *int64 `json:"used"`
	Size *int64 `json:"size"`
	// Meta can carry explicitly scoped invocation counters.
	Meta map[string]any `json:"_meta"`
}

func newDelivery(parent context.Context) *delivery {
	ctx, cancel := context.WithCancel(parent)
	return &delivery{ctx: ctx, cancel: cancel, done: make(chan struct{})}
}

func (c *Client) receive(frame acp.ReceiveNotification) {
	if frame.Method != acp.ClientMethodSessionUpdate {
		c.receivePrivateNotification(frame)
		return
	}
	if len(frame.Params) > maxOpeningBytes {
		var oversized struct {
			// ID identifies the recipient without retaining the large update.
			ID acp.SessionId `json:"sessionId"`
		}
		if json.Unmarshal(frame.Params, &oversized) == nil && oversized.ID != "" {
			c.mu.Lock()
			if t := c.turn; t != nil && t.session.id == oversized.ID && t.writing && !t.terminal {
				c.failDeliveryLocked(t.delivery, errDeliveryLimit)
			}
			if p := c.opening; p != nil && p.started && p.session.id == oversized.ID {
				p.overflow = true
			}
			c.mu.Unlock()
		}
		return
	}
	var raw struct {
		// ID identifies the exact native session.
		ID acp.SessionId `json:"sessionId"`
		// Update retains fields whose absence matters to the normalized result.
		Update json.RawMessage `json:"update"`
	}
	if json.Unmarshal(frame.Params, &raw) != nil || raw.ID == "" {
		return
	}
	var discriminator struct {
		// Kind routes the bounded technical fold for recognized updates.
		Kind string `json:"sessionUpdate"`
		// ConfigOptions is the raw directory.
		ConfigOptions json.RawMessage `json:"configOptions"`
		// Status reports completion of Cursor's optional compaction notification.
		Status string `json:"status"`
	}
	if json.Unmarshal(raw.Update, &discriminator) != nil {
		return
	}
	if discriminator.Kind == "compaction_update" && discriminator.Status == "completed" {
		c.mu.Lock()
		if s := c.sessions[raw.ID]; s != nil && !c.closed {
			s.instructionEpoch++
			s.instructionPrompts = 0
		}
		c.mu.Unlock()
		return
	}
	update := receivedUpdate{sequence: frame.Sequence, size: len(frame.Params), id: raw.ID}
	if json.Unmarshal(frame.Params, &update.note) != nil {
		return
	}
	switch discriminator.Kind {
	case "config_option_update":
		update.config = discriminator.ConfigOptions
	case "usage_update":
		var usage usageUpdate
		if json.Unmarshal(raw.Update, &usage) == nil {
			update.usage = &usage
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	if p := c.opening; p != nil && p.started {
		if p.session.id == update.id {
			c.applyOpeningUpdate(p, update)
			return
		}
		if p.session.id == "" && c.sessions[update.id] == nil && update.id != p.source {
			if update.note.Update.ConfigOptionUpdate != nil || update.note.Update.CurrentModeUpdate != nil ||
				update.note.Update.AvailableCommandsUpdate != nil || update.note.Update.SessionInfoUpdate != nil {
				c.retainOpeningUpdate(p, update)
			}
			return
		}
	}
	s := c.sessions[update.id]
	if s == nil {
		return
	}
	if !c.applySessionUpdateLocked(s, &update) {
		return
	}
	if sessionUpdate(update.note.Update) {
		c.enqueueLocked(nil, agentclient.Scope{SessionID: s.id}, update.note, update.size)
		return
	}
	t := c.turn
	if t == nil || t.session != s || !t.writing || t.terminal {
		return
	}
	c.foldTurnUpdateLocked(t, update)
}

func (c *Client) applySessionUpdateLocked(s *session, update *receivedUpdate) bool {
	if update.note.Update.ConfigOptionUpdate != nil {
		if !present(update.config) || c.applyConfigurationLocked(s, update.config, nil, update.sequence) != nil {
			return false
		}
		config := *update.note.Update.ConfigOptionUpdate
		config.ConfigOptions = s.config.options
		update.note.Update.ConfigOptionUpdate = &config
	}
	if mode := update.note.Update.CurrentModeUpdate; mode != nil && s.modes != nil {
		modes := *s.modes
		modes.CurrentModeId = mode.CurrentModeId
		s.modes = &modes
	}
	return true
}

func sessionUpdate(u acp.SessionUpdate) bool {
	return u.ConfigOptionUpdate != nil || u.CurrentModeUpdate != nil || u.AvailableCommandsUpdate != nil || u.SessionInfoUpdate != nil
}

func (c *Client) retainOpeningUpdate(p *opening, update receivedUpdate) {
	if len(p.pending)+len(p.observations) >= maxOpeningFrames || p.bytes+update.size > maxOpeningBytes {
		p.overflow = true
		return
	}
	p.pending = append(p.pending, update)
	p.bytes += update.size
}

func (c *Client) applyOpeningUpdate(p *opening, update receivedUpdate) {
	if !c.applySessionUpdateLocked(p.session, &update) {
		return
	}
	if p.replay || sessionUpdate(update.note.Update) {
		if len(p.observations) >= maxOpeningFrames || p.observationBytes+update.size > maxOpeningBytes {
			p.overflow = true
			return
		}
		p.observations = append(p.observations, update)
		p.observationBytes += update.size
	}
}

func (c *Client) publishOpeningLocked(p *opening) {
	// The caller can immediately issue a setter when the first advertised
	// config observation runs. A replay keeps its admission until drained.
	configPublished := false
	for _, update := range p.observations {
		if update.note.Update.ConfigOptionUpdate != nil {
			if configPublished {
				continue
			}
			configPublished = true
			config := *update.note.Update.ConfigOptionUpdate
			config.ConfigOptions = p.session.config.options
			update.note.Update.ConfigOptionUpdate = &config
		}
		batch := p.delivery
		if !p.replay {
			batch = nil
		}
		c.enqueueLocked(batch, agentclient.Scope{SessionID: p.session.id}, update.note, update.size)
	}
}

func (c *Client) foldTurnUpdateLocked(t *turn, update receivedUpdate) {
	u := update.note.Update
	if u.AgentMessageChunk != nil && u.AgentMessageChunk.Content.Text != nil {
		text := u.AgentMessageChunk.Content.Text.Text
		if cursorNoise(text) {
			return
		}
		if cursorAuthLost(text) {
			t.authLost = true
			return
		}
	}
	if u.ToolCall != nil && !rememberTool(t, u.ToolCall.ToolCallId) ||
		u.ToolCallUpdate != nil && !rememberTool(t, u.ToolCallUpdate.ToolCallId) {
		return
	}
	if u.AgentMessageChunk != nil || u.AgentThoughtChunk != nil || u.ToolCall != nil || u.ToolCallUpdate != nil ||
		u.Plan != nil || u.PlanUpdate != nil || u.PlanRemoved != nil {
		t.associated, t.observedOutput = true, true
	}
	if update.usage != nil {
		if used := nonnegative(update.usage.Used); used != nil {
			if previous := t.session.contextUsed; previous != nil && float64(*used) < float64(*previous)*compactionContextRatio {
				t.session.instructionEpoch++
				t.session.instructionPrompts = 0
			}
			t.session.contextUsed = used
			t.contextUsage.UsedTokens = used
		}
		if size := nonnegative(update.usage.Size); size != nil && *size > 0 {
			t.contextUsage.WindowTokens = size
		}
		if usage, ok := acpmeta.ReadTurnUsage(update.usage.Meta); ok {
			t.usage = &usage
			t.associated = true
		}
	}
	c.enqueueLocked(t.delivery, t.scope, update.note, update.size)
}

func (c *Client) enqueueLocked(batch *delivery, scope agentclient.Scope, update acp.SessionNotification, size int) {
	if c.host.Observe == nil {
		return
	}
	c.enqueueObservationLocked(observation{batch: batch, scope: scope, update: update, size: size})
}

func (c *Client) enqueueActionLocked(batch *delivery, scope agentclient.Scope, size int, action func(context.Context) error) {
	c.enqueueObservationLocked(observation{batch: batch, scope: scope, size: size, action: action})
}

func (c *Client) enqueueObservationLocked(o observation) {
	batch, size := o.batch, o.size
	if batch != nil && (batch.err != nil || batch.sealed) {
		return
	}
	if c.observeBytes+size > maxObservationBytes || len(c.observe) >= cap(c.observe) {
		if batch != nil {
			c.failDeliveryLocked(batch, errDeliveryLimit)
		}
		return
	}
	if batch != nil {
		batch.pending++
	}
	c.observeBytes += size
	c.observe <- o
}

func (c *Client) sealDeliveryLocked(batch *delivery) {
	batch.sealed = true
	if batch.pending == 0 {
		close(batch.done)
	}
}

func (c *Client) failDeliveryLocked(batch *delivery, err error) {
	if batch.err == nil {
		batch.err = err
		batch.cancel()
	}
}

func (c *Client) deliver() {
	defer c.workers.Done()
	for {
		select {
		case <-c.ctx.Done():
			return
		case o := <-c.observe:
			ctx := c.ctx
			if o.batch != nil {
				ctx = o.batch.ctx
			}
			var err error
			if ctx.Err() == nil && o.action != nil {
				err = o.action(ctx)
			} else if ctx.Err() == nil {
				// Callers own their projection and may change it. No mutable
				// config value is shared with the native session directory.
				note, cloneErr := clone(o.update)
				if cloneErr != nil {
					err = errors.New("invalid Cursor observation")
				} else {
					err = c.host.Observe(ctx, o.scope, note)
				}
			}
			c.mu.Lock()
			c.observeBytes -= o.size
			if o.batch != nil {
				if err != nil {
					c.failDeliveryLocked(o.batch, err)
				}
				o.batch.pending--
				if o.batch.pending == 0 && o.batch.sealed {
					close(o.batch.done)
				}
			}
			c.mu.Unlock()
		}
	}
}

func (c *Client) waitDelivery(batch *delivery) error {
	wait, cancel := context.WithTimeout(c.ctx, deliveryTimeout)
	defer cancel()
	select {
	case <-batch.done:
	case <-batch.ctx.Done():
	case <-wait.Done():
		c.mu.Lock()
		c.failDeliveryLocked(batch, wait.Err())
		c.mu.Unlock()
	}
	c.mu.Lock()
	err := batch.err
	if err == nil {
		err = batch.ctx.Err()
	}
	c.mu.Unlock()
	return err
}

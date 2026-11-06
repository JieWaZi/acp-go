package pi

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"sync/atomic"
	"time"

	acp "github.com/JieWaZi/acp-go-sdk"
	"github.com/JieWaZi/acp-go/pkg/acpmeta"
	agentclient "github.com/JieWaZi/acp-go/pkg/agentclient"
	"github.com/JieWaZi/acp-go/pkg/autoreview"
)

type nativeCounters struct {
	// Input counts uncached input tokens from one assistant response.
	Input *int64 `json:"input"`
	// Output counts native output tokens from that response.
	Output *int64 `json:"output"`
	// CacheRead counts input served from cache.
	CacheRead *int64 `json:"cacheRead"`
	// CacheWrite counts input written to cache.
	CacheWrite *int64 `json:"cacheWrite"`
}

// tokenSubtotal counts observed values and preserves explicit zeros. Coverage
// belongs to the accumulator, so a missing value never erases a known subtotal.
type tokenSubtotal struct {
	// value sums the available counter values.
	value int64
	// observed counts responses that supplied this component.
	observed int
	// overflow marks a subtotal that exceeded the integer range.
	overflow bool
}

func (s *tokenSubtotal) add(value *int64) {
	if value == nil || *value < 0 || s.overflow {
		return
	}
	s.observed++
	if s.value > math.MaxInt64-*value {
		s.overflow = true
		return
	}
	s.value += *value
}

func (s tokenSubtotal) total() *int64 {
	if s.observed == 0 || s.overflow {
		return nil
	}
	return pointer(s.value)
}

func (s tokenSubtotal) partial(messages int) bool {
	return s.overflow || s.observed != 0 && s.observed != messages
}

type tokenTotals struct {
	// input accumulates uncached input.
	input tokenSubtotal
	// output accumulates native output.
	output tokenSubtotal
	// cacheRead accumulates input read from cache.
	cacheRead tokenSubtotal
	// cacheWrite accumulates input written to cache.
	cacheWrite tokenSubtotal
	// messages counts all assistant responses in this scope.
	messages int
}

func (t *tokenTotals) add(counters *nativeCounters) {
	t.messages++
	if counters == nil {
		return
	}
	t.input.add(counters.Input)
	t.output.add(counters.Output)
	t.cacheRead.add(counters.CacheRead)
	t.cacheWrite.add(counters.CacheWrite)
}

func (t tokenTotals) tokens() acpmeta.ModelUsage {
	return acpmeta.ModelUsage{
		InputTokens: t.input.total(), OutputTokens: t.output.total(),
		CacheReadTokens: t.cacheRead.total(), CacheWriteTokens: t.cacheWrite.total(),
	}
}

func (t tokenTotals) partial() bool {
	return t.input.partial(t.messages) || t.output.partial(t.messages) ||
		t.cacheRead.partial(t.messages) || t.cacheWrite.partial(t.messages)
}

type nativeMessage struct {
	// Role identifies the producer of the message.
	Role string `json:"role"`
	// Provider identifies the service that produced an assistant response.
	Provider string `json:"provider"`
	// Model identifies the exact model that produced the response.
	Model string `json:"model"`
	// StopReason is the provider's per-message completion reason.
	StopReason string `json:"stopReason"`
	// ErrorMessage supplies private text for failure classification.
	ErrorMessage string `json:"errorMessage"`
	// Content holds the complete native message body.
	Content json.RawMessage `json:"content"`
	// Usage reports counters for this response.
	Usage *nativeCounters `json:"usage"`
}

type nativeEvent struct {
	// Type selects the native event shape.
	Type string `json:"type"`
	// Aborted carries the native settlement cancellation fact when supplied.
	Aborted *bool `json:"aborted"`
	// Message is the typed message snapshot on lifecycle events.
	Message nativeMessage `json:"message"`
	// Assistant holds the native incremental text or thought update.
	Assistant struct {
		// Type identifies the native content change.
		Type string `json:"type"`
		// Delta is the incremental text emitted by that change.
		Delta string `json:"delta"`
	} `json:"assistantMessageEvent"`
	// ToolID correlates records for one native tool call.
	ToolID acp.ToolCallId `json:"toolCallId"`
	// ToolName is the provider's tool identifier.
	ToolName string `json:"toolName"`
	// Args contains the native tool parameters.
	Args json.RawMessage `json:"args"`
	// Result holds final tool output.
	Result struct {
		// Content contains the native output blocks.
		Content json.RawMessage
	} `json:"result"`
	// PartialResult holds an in-progress tool snapshot.
	PartialResult struct {
		// Content contains the native output blocks.
		Content json.RawMessage
	} `json:"partialResult"`
	// IsError marks the native tool result as failed.
	IsError bool `json:"isError"`
	// Success indicates the retry controller recovered.
	Success bool `json:"success"`
	// FinalError describes an exhausted retry for local classification.
	FinalError string `json:"finalError"`
	// StatusKey identifies the extension status channel.
	StatusKey string `json:"statusKey"`
	// StatusText contains the status channel's value.
	StatusText string `json:"statusText"`
}

type turnEvents struct {
	// session is the native owner emitting this turn's records.
	session *session
	// invocation supplies the frozen host scope and lifetime.
	invocation *invocation
	// outcome accumulates the canonical result before publication.
	outcome *agentclient.NativeOutcome
	// inputID is the admitted host input associated with a native start.
	inputID string
	// started gates execution events on an observed agent_start.
	started bool
	// messageID associates deltas and replacement text for the current response.
	messageID string
	// lastMessage preserves the latest complete assistant text.
	lastMessage *agentclient.FinalMessage
	// lastStop is the latest assistant response's stop reason.
	lastStop string
	// failure is the current model error, which a successful retry may clear.
	failure error
	// extensionError is an extension fault independent of model retries.
	extensionError error
	// counters aggregates measured components over the invocation.
	counters tokenTotals
	// modelCounters partitions the same counters by native model.
	modelCounters map[string]tokenTotals
	// attributionUnknown omits model decomposition when identities are incomplete.
	attributionUnknown bool
	// tools tracks native tool calls that are still in progress.
	tools map[acp.ToolCallId]bool

	// delivery queues bounded, ordered host observations.
	delivery chan observed
	// deliveryDone reports the observation worker's result.
	deliveryDone chan error
	// deliveryCancel stops the observation worker's callback context.
	deliveryCancel context.CancelFunc
	// deliveryBytes accounts for queued observation payloads.
	deliveryBytes atomic.Int64
	// deliveryFailure stores a local observation budget failure.
	deliveryFailure error
}

type observed struct {
	// notification is the ACP observation to deliver.
	notification acp.SessionNotification
	// bytes is the payload size charged to the delivery queue.
	bytes int64
}

func newTurnEvents(s *session, t *invocation, o *agentclient.NativeOutcome, inputID string) *turnEvents {
	e := &turnEvents{
		session: s, invocation: t, outcome: o, inputID: inputID,
		modelCounters: make(map[string]tokenTotals), tools: make(map[acp.ToolCallId]bool),
	}
	if s.client.host.Observe != nil {
		e.delivery = make(chan observed, 64)
		e.deliveryDone = make(chan error, 1)
		ctx, cancel := context.WithCancel(context.WithoutCancel(t.ctx))
		e.deliveryCancel = cancel
		go func() {
			for item := range e.delivery {
				err := s.client.host.Observe(ctx, t.scope, item.notification)
				e.deliveryBytes.Add(-item.bytes)
				if err != nil {
					e.deliveryDone <- err
					return
				}
			}
			e.deliveryDone <- nil
		}()
	}
	return e
}

func (e *turnEvents) observe(update acp.SessionUpdate, bytes int) {
	if e.delivery == nil || e.deliveryFailure != nil {
		return
	}
	size := int64(bytes)
	if e.deliveryBytes.Add(size) <= maxRecord {
		select {
		case e.delivery <- observed{notification: acp.SessionNotification{SessionId: e.session.id, Update: update}, bytes: size}:
			return
		default:
		}
	}
	e.deliveryBytes.Add(-size)
	e.deliveryFailure = errors.New("Pi observation delivery budget exhausted")
	e.invocation.cancel(e.deliveryFailure)
}

func (e *turnEvents) apply(line []byte) bool {
	var event nativeEvent
	if json.Unmarshal(line, &event) != nil {
		e.outcome.OperationError = errors.New("Pi returned an invalid execution event")
		return true
	}
	switch event.Type {
	case "agent_start":
		e.started = true
		e.outcome.Turn.Origin = acpmeta.NativeTurnOriginHost
		e.outcome.Turn.ConsumedInputIDs = []string{e.inputID}
	case "agent_settled":
		if !e.started {
			return false
		}
		e.settle(event)
		return true
	case "extension_error":
		e.extensionError = classified("extension_error", "prompt")
	case "auto_retry_end":
		if event.Success {
			e.failure = nil
		} else if event.FinalError != "" {
			e.failure = nativeFailure(event.FinalError)
		}
	default:
		if !e.started {
			return false
		}
		e.output(event, len(line))
	}
	return false
}

func (e *turnEvents) output(event nativeEvent, size int) {
	switch event.Type {
	case "message_start":
		if event.Message.Role == "assistant" {
			e.messageID = newID()
		}
	case "message_update":
		switch event.Assistant.Type {
		case "text_delta":
			e.outcome.ObservedOutput = true
			e.observe(e.messageUpdate(event.Assistant.Delta, nil), size)
		case "thinking_delta":
			e.outcome.ObservedOutput = true
			e.observe(acp.UpdateAgentThoughtText(event.Assistant.Delta), size)
		}
	case "message_end":
		msg := event.Message
		if msg.Role != "assistant" {
			return
		}
		e.recordUsage(msg)
		e.lastStop = msg.StopReason
		e.failure = nil
		if msg.StopReason == "error" {
			e.failure = nativeFailure(msg.ErrorMessage)
		}
		text := messageText(msg.Content)
		update := e.messageUpdate("", &text)
		e.observe(update, size)
		e.outcome.ObservedOutput = e.outcome.ObservedOutput || text != ""
		e.lastMessage = nil
		if text != "" {
			e.lastMessage = &agentclient.FinalMessage{ID: e.messageID, Text: text}
		}
		e.messageID = ""
	case "tool_execution_start":
		if event.ToolID == "" || e.tools[event.ToolID] {
			return
		}
		e.tools[event.ToolID] = true
		e.outcome.ObservedOutput = true
		e.observe(acp.StartToolCall(event.ToolID, event.ToolName, acp.WithStartStatus(acp.ToolCallStatusInProgress), acp.WithStartRawInput(event.Args)), size)
	case "tool_execution_update", "tool_execution_end":
		if !e.tools[event.ToolID] {
			return
		}
		status := acp.ToolCallStatusInProgress
		content := event.PartialResult.Content
		if event.Type == "tool_execution_end" {
			delete(e.tools, event.ToolID)
			status = acp.ToolCallStatusCompleted
			content = event.Result.Content
			if event.IsError {
				status = acp.ToolCallStatusFailed
			}
		}
		var blocks []acp.ToolCallContent
		for _, block := range messageContent(content) {
			blocks = append(blocks, acp.ToolContent(block))
		}
		e.observe(acp.UpdateToolCall(event.ToolID, acp.WithUpdateStatus(status), acp.WithUpdateContent(blocks)), size)
	case "extension_ui_request":
		if event.StatusKey != "acp-go.permission-review" {
			return
		}
		var report struct {
			// ToolID matches the native call evaluated by the reviewer.
			ToolID acp.ToolCallId `json:"toolCallId"`
			// Outcome is the reviewer decision.
			Outcome string
			// RiskLevel is the reviewer risk classification.
			RiskLevel string `json:"risk_level"`
			// Failed records an unavailable or unsuccessful review.
			Failed bool
		}
		if json.Unmarshal([]byte(event.StatusText), &report) == nil && report.ToolID != "" {
			update := acp.UpdateToolCall(report.ToolID)
			update.ToolCallUpdate.Meta = map[string]any{"acp-go/permission-review": autoreview.Metadata(autoreview.Decision{Outcome: report.Outcome, RiskLevel: report.RiskLevel}, report.Failed)}
			e.observe(update, size)
		}
	}
}

func (e *turnEvents) messageUpdate(delta string, replacement *string) acp.SessionUpdate {
	if e.messageID == "" {
		e.messageID = newID()
	}
	update := acp.UpdateAgentMessageText(delta)
	update.AgentMessageChunk.MessageId = pointer(e.messageID)
	update.AgentMessageChunk.Meta = acpmeta.WithMessageInfo(nil, acpmeta.MessageInfo{
		Phase: acpmeta.MessagePhaseUnknown, Source: acpmeta.MessageSourceNativeMessage,
		Complete: replacement != nil, ReplacementText: replacement,
	})
	return update
}

func (e *turnEvents) settle(event nativeEvent) {
	o := e.outcome
	if (event.Aborted != nil && *event.Aborted) || e.lastStop == "aborted" {
		o.Turn.Status = acpmeta.NativeTurnCancelled
		o.StopReason = pointer(acp.StopReasonCancelled)
		return
	}
	if e.extensionError != nil || e.failure != nil {
		o.Turn.Status = acpmeta.NativeTurnFailed
		o.NativeFailure = errors.Join(e.extensionError, e.failure)
		return
	}
	o.Turn.Status = acpmeta.NativeTurnCompleted
	o.StopReason = pointer(acp.StopReasonEndTurn)
	if e.lastStop == "length" {
		o.StopReason = pointer(acp.StopReasonMaxTokens)
	}
	if e.lastStop == "stop" || e.lastStop == "length" {
		o.FinalMessage = e.lastMessage
	}
	if o.FinalMessage != nil && len(o.FinalMessage.Text) > 256<<10 {
		o.FinalMessage = nil
		o.Unavailable = append(o.Unavailable, acpmeta.ReceiptResultTextUnavailable)
	}
}

func (e *turnEvents) finish() {
	o := e.outcome
	if o.Turn.Status != "" {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(e.invocation.ctx), time.Second)
		var stats struct {
			// Context reports the current model context estimate.
			Context struct {
				// Used is the native estimate, if available.
				Used *int64 `json:"tokens"`
				// Window is the native model context limit, if available.
				Window *int64 `json:"contextWindow"`
			} `json:"contextUsage"`
		}
		if e.session.rpc.call(ctx, "get_session_stats", nil, &stats) == nil {
			o.ContextUsage = agentclient.ContextUsage{UsedTokens: nonnegative(stats.Context.Used), WindowTokens: nonnegative(stats.Context.Window)}
		}
		cancel()
	}
	o.Turn.Usage = e.usage()
	if used, window := o.ContextUsage.UsedTokens, o.ContextUsage.WindowTokens; used != nil && window != nil && *window > 0 && *window <= math.MaxInt && *used <= math.MaxInt {
		var meta map[string]any
		if o.Turn.Usage != nil {
			meta = acpmeta.WithTurnUsage(nil, *o.Turn.Usage)
		}
		e.observe(acp.SessionUpdate{UsageUpdate: &acp.SessionUsageUpdate{Meta: meta, Used: int(*used), Size: int(*window)}}, 0)
	}
	if e.delivery != nil {
		close(e.delivery)
		deadline := time.NewTimer(controlTimeout)
		defer deadline.Stop()
		select {
		case err := <-e.deliveryDone:
			o.DeliveryError = errors.Join(e.deliveryFailure, err)
		case <-deadline.C:
			o.DeliveryError = errors.Join(e.deliveryFailure, errors.New("Pi observation delivery timed out"))
		}
		e.deliveryCancel()
	}
}

func messageText(raw json.RawMessage) string {
	var result strings.Builder
	for _, content := range messageContent(raw) {
		if content.Text != nil {
			result.WriteString(content.Text.Text)
		}
	}
	return result.String()
}

func messageContent(raw json.RawMessage) []acp.ContentBlock {
	var text string
	if json.Unmarshal(raw, &text) == nil && text != "" {
		return []acp.ContentBlock{acp.TextBlock(text)}
	}
	var blocks []acp.ContentBlock
	if json.Unmarshal(raw, &blocks) == nil {
		return blocks
	}
	// Pi includes thinking and tool-call blocks among ACP text and image blocks.
	var entries []json.RawMessage
	if json.Unmarshal(raw, &entries) != nil {
		return nil
	}
	for _, entry := range entries {
		var block acp.ContentBlock
		if json.Unmarshal(entry, &block) == nil && (block.Text != nil || block.Image != nil) {
			blocks = append(blocks, block)
		}
	}
	return blocks
}

func (e *turnEvents) recordUsage(msg nativeMessage) {
	e.counters.add(msg.Usage)
	if msg.Provider == "" || msg.Model == "" || len(msg.Provider)+len(msg.Model) > 512 {
		e.attributionUnknown = true
		return
	}
	key := msg.Provider + "/" + msg.Model
	totals, exists := e.modelCounters[key]
	if !exists && len(e.modelCounters) >= 64 {
		e.attributionUnknown = true
		return
	}
	totals.add(msg.Usage)
	e.modelCounters[key] = totals
}

func (e *turnEvents) usage() *acpmeta.TurnUsage {
	totals := e.counters.tokens()
	if totals == (acpmeta.ModelUsage{}) {
		return nil
	}
	u := &acpmeta.TurnUsage{
		InputTokens: totals.InputTokens, OutputTokens: totals.OutputTokens,
		CacheReadTokens: totals.CacheReadTokens, CacheWriteTokens: totals.CacheWriteTokens,
		Source: "pi.assistant_message.usage", Coverage: "assistant_messages",
		Complete:     e.outcome.Turn.Status != "" && !e.counters.partial(),
		ContextKnown: e.outcome.ContextUsage.UsedTokens != nil && e.outcome.ContextUsage.WindowTokens != nil,
	}
	if !e.attributionUnknown && len(e.modelCounters) > 0 {
		u.Models = make(map[string]acpmeta.ModelUsage, len(e.modelCounters))
		for model, counters := range e.modelCounters {
			u.Models[model] = counters.tokens()
			if len(e.modelCounters) == 1 {
				u.Model = model
			}
		}
	}
	return u
}

func nonnegative(counter *int64) *int64 {
	if counter == nil || *counter < 0 {
		return nil
	}
	return counter
}

package pi

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"sync/atomic"
	"time"

	"github.com/JieWaZi/acp-go/pkg/acpmeta"
	agentclient "github.com/JieWaZi/acp-go/pkg/agent_next"
	"github.com/JieWaZi/acp-go/pkg/autoreview"
	acp "github.com/JieWaZi/acp-go/sdk"
)

type nativeCounters struct {
	Input      *int64 `json:"input"`
	Output     *int64 `json:"output"`
	CacheRead  *int64 `json:"cacheRead"`
	CacheWrite *int64 `json:"cacheWrite"`
}

type tokenSubtotal struct {
	value    int64
	known    bool
	overflow bool
}

func (s *tokenSubtotal) add(value *int64) {
	if value == nil || *value < 0 || s.overflow {
		return
	}
	if s.value > math.MaxInt64-*value {
		s.overflow = true
		return
	}
	s.value += *value
	s.known = true
}

func (s tokenSubtotal) partial(total *int64) *int64 {
	if total != nil || !s.known || s.overflow {
		return nil
	}
	return pointer(s.value)
}

type nativeMessage struct {
	Role         string          `json:"role"`
	Provider     string          `json:"provider"`
	Model        string          `json:"model"`
	StopReason   string          `json:"stopReason"`
	ErrorMessage string          `json:"errorMessage"`
	Content      json.RawMessage `json:"content"`
	Usage        *nativeCounters `json:"usage"`
}

type nativeEvent struct {
	Type      string        `json:"type"`
	Aborted   *bool         `json:"aborted"`
	Message   nativeMessage `json:"message"`
	Assistant struct {
		Type  string `json:"type"`
		Delta string `json:"delta"`
	} `json:"assistantMessageEvent"`
	ToolID        acp.ToolCallId                    `json:"toolCallId"`
	ToolName      string                            `json:"toolName"`
	Args          json.RawMessage                   `json:"args"`
	Result        struct{ Content json.RawMessage } `json:"result"`
	PartialResult struct{ Content json.RawMessage } `json:"partialResult"`
	IsError       bool                              `json:"isError"`
	Success       bool                              `json:"success"`
	FinalError    string                            `json:"finalError"`
	StatusKey     string                            `json:"statusKey"`
	StatusText    string                            `json:"statusText"`
}

type turnEvents struct {
	session                                                    *session
	invocation                                                 *invocation
	outcome                                                    *agentclient.NativeOutcome
	inputID                                                    string
	started                                                    bool
	messageID                                                  string
	lastMessage                                                *agentclient.FinalMessage
	lastStop                                                   string
	failure                                                    error
	extensionError                                             error
	counters                                                   nativeCounters
	observedInput, observedOutput, observedRead, observedWrite tokenSubtotal
	modelCounters                                              map[string]nativeCounters
	modelCounts                                                map[string]int
	messages                                                   int
	usageSeen                                                  bool
	attributionUnknown                                         bool
	tools                                                      map[acp.ToolCallId]bool

	delivery        chan observed
	deliveryDone    chan error
	deliveryCancel  context.CancelFunc
	deliveryBytes   atomic.Int64
	deliveryFailure error
}

type observed struct {
	notification acp.SessionNotification
	bytes        int64
}

func newTurnEvents(s *session, t *invocation, o *agentclient.NativeOutcome, inputID string) *turnEvents {
	e := &turnEvents{
		session: s, invocation: t, outcome: o, inputID: inputID,
		modelCounters: make(map[string]nativeCounters), modelCounts: make(map[string]int), tools: make(map[acp.ToolCallId]bool),
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
		e.session.poison(e.outcome.OperationError)
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
			ToolID    acp.ToolCallId `json:"toolCallId"`
			Outcome   string
			RiskLevel string `json:"risk_level"`
			Failed    bool
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
			Context struct {
				Used   *int64 `json:"tokens"`
				Window *int64 `json:"contextWindow"`
			} `json:"contextUsage"`
		}
		if e.session.rpc.call(ctx, "get_session_stats", nil, &stats) == nil {
			o.ContextUsage = agentclient.ContextUsage{UsedTokens: nonnegative(stats.Context.Used), WindowTokens: nonnegative(stats.Context.Window)}
		}
		cancel()
	}
	if e.usageSeen {
		o.Turn.Usage = e.usage()
	}
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
	counters := nativeCounters{}
	if msg.Usage != nil {
		counters = *msg.Usage
		e.usageSeen = e.usageSeen || nonnegative(counters.Input) != nil || nonnegative(counters.Output) != nil || nonnegative(counters.CacheRead) != nil || nonnegative(counters.CacheWrite) != nil
	}
	e.observedInput.add(counters.Input)
	e.observedOutput.add(counters.Output)
	e.observedRead.add(counters.CacheRead)
	e.observedWrite.add(counters.CacheWrite)
	e.counters = mergeCounters(e.counters, counters, e.messages == 0)
	e.messages++
	if msg.Provider == "" || msg.Model == "" || len(msg.Provider)+len(msg.Model) > 512 {
		e.attributionUnknown = true
		return
	}
	key := msg.Provider + "/" + msg.Model
	if _, exists := e.modelCounts[key]; !exists && len(e.modelCounts) >= 64 {
		e.attributionUnknown = true
		return
	}
	e.modelCounters[key] = mergeCounters(e.modelCounters[key], counters, e.modelCounts[key] == 0)
	e.modelCounts[key]++
}

func (e *turnEvents) usage() *acpmeta.TurnUsage {
	u := &acpmeta.TurnUsage{
		InputTokens: e.counters.Input, OutputTokens: e.counters.Output,
		CacheReadTokens: e.counters.CacheRead, CacheWriteTokens: e.counters.CacheWrite,
		Source: "pi.assistant_message.usage", Coverage: "assistant_messages",
		Complete:     e.outcome.Turn.Status != "",
		ContextKnown: e.outcome.ContextUsage.UsedTokens != nil && e.outcome.ContextUsage.WindowTokens != nil,
	}
	if !e.attributionUnknown && len(e.modelCounters) > 0 {
		u.Models = make(map[string]acpmeta.ModelUsage, len(e.modelCounters))
		for model, usage := range e.modelCounters {
			u.Models[model] = acpmeta.ModelUsage{InputTokens: usage.Input, OutputTokens: usage.Output, CacheReadTokens: usage.CacheRead, CacheWriteTokens: usage.CacheWrite}
			if len(e.modelCounters) == 1 {
				u.Model = model
			}
		}
	}
	partial := acpmeta.ModelUsage{
		InputTokens: e.observedInput.partial(u.InputTokens), OutputTokens: e.observedOutput.partial(u.OutputTokens),
		CacheReadTokens: e.observedRead.partial(u.CacheReadTokens), CacheWriteTokens: e.observedWrite.partial(u.CacheWriteTokens),
	}
	if partial.InputTokens != nil || partial.OutputTokens != nil || partial.CacheReadTokens != nil || partial.CacheWriteTokens != nil {
		u.Observations = []acpmeta.UsageObservation{{Source: u.Source, Coverage: "known_message_components", Tokens: partial}}
	}
	return u
}

func mergeCounters(total, next nativeCounters, first bool) nativeCounters {
	return nativeCounters{
		Input: addCounter(total.Input, next.Input, first), Output: addCounter(total.Output, next.Output, first),
		CacheRead: addCounter(total.CacheRead, next.CacheRead, first), CacheWrite: addCounter(total.CacheWrite, next.CacheWrite, first),
	}
}

func addCounter(total, next *int64, first bool) *int64 {
	next = nonnegative(next)
	if first || next == nil {
		return next
	}
	if total == nil || *total > math.MaxInt64-*next {
		return nil
	}
	return pointer(*total + *next)
}

func nonnegative(counter *int64) *int64 {
	if counter == nil || *counter < 0 {
		return nil
	}
	return counter
}

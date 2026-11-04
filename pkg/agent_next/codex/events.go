package codex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"strings"

	"github.com/JieWaZi/acp-go/pkg/acpmeta"
	agentclient "github.com/JieWaZi/acp-go/pkg/agent_next"
	"github.com/JieWaZi/acp-go/pkg/codex/protocol"
	acp "github.com/JieWaZi/acp-go/sdk"
)

type turnEvent struct {
	// ThreadID names the subscribed native thread.
	ThreadID acp.SessionId `json:"threadId"`
	// TurnID names the native execution on ordinary item notifications.
	TurnID string `json:"turnId"`
	// Turn carries the execution identity and status on lifecycle notifications.
	Turn protocol.TurnElement `json:"turn"`
	// Item carries the native item on lifecycle notifications.
	Item protocol.ThreadItem `json:"item"`
	// Unprompted identifies a provider-origin start when the native server marks it.
	Unprompted bool `json:"unprompted"`
	// ItemID binds streamed fragments to the item lifecycle.
	ItemID string `json:"itemId"`
	// Delta is an incremental text or tool-output fragment.
	Delta string `json:"delta"`
}

func (c *Client) receiveLocked(method string, raw json.RawMessage) {
	switch method {
	case "thread/settings/updated":
		var event protocol.ThreadSettingsUpdatedNotification
		if json.Unmarshal(raw, &event) == nil {
			if s := c.sessions[acp.SessionId(event.ThreadID)]; s != nil {
				s.observe("thread_settings_updated", event.ThreadSettings.Model, event.ThreadSettings.Effort, event.ThreadSettings.ServiceTier)
				if t := s.active; t != nil && t.writing && !t.finishing && t.outcome.Turn.Status == "" &&
					t.model == "" && t.outcome.Turn.Usage == nil && value(t.request.Model) == event.ThreadSettings.Model {
					t.model = event.ThreadSettings.Model
				}
			}
		}
		return
	case protocol.MethodMCPServerStartupStatusUpdated:
		c.captureMCPStartupLocked(raw)
		return
	case "serverRequest/resolved":
		c.completeURLLocked(raw)
		return
	case "account/login/completed":
		c.captureLoginLocked(raw)
		return
	case "account/updated":
		if c.logoutDone != nil {
			close(c.logoutDone)
			c.logoutDone = nil
		}
		return
	}
	var event turnEvent
	if json.Unmarshal(raw, &event) != nil {
		return
	}
	s := c.sessions[event.ThreadID]
	if s == nil {
		return
	}
	t := s.active
	if method == protocol.MethodItemStarted || method == protocol.MethodItemCompleted {
		if event.Item.Type == protocol.UserMessage && event.TurnID != "" {
			for previous := range s.draining {
				if value(previous.outcome.Turn.NativeTurnID) == event.TurnID && previous.steerPending > 0 {
					recordSteeredItem(previous, event.Item)
					return
				}
			}
			if t != nil && t.steerPending > 0 && value(t.outcome.Turn.NativeTurnID) == event.TurnID {
				recordSteeredItem(t, event.Item)
			}
		}
	}
	if method == protocol.MethodTurnStarted && (t == nil || !t.writing || t.outcome.Turn.Status != "") {
		if started := c.startAutonomousLocked(s, event); started != nil {
			t = started
		}
	}
	if t == nil {
		if method == protocol.MethodThreadTokenUsageUpdated {
			s.usageCheckpoint = nil
		}
		return
	}
	if !t.writing || t.finishing || t.outcome.Turn.Status != "" {
		if method == protocol.MethodThreadTokenUsageUpdated {
			s.usageCheckpoint = nil
			if t.outcome.Turn.Status == "" && !t.finishing {
				t.breakUsageContinuity()
			}
		}
		return
	}
	id := event.TurnID
	if id == "" {
		id = event.Turn.ID
	}
	if id == "" {
		return
	}
	if t.outcome.Turn.NativeTurnID == nil {
		if c.seenTurnLocked(s.id, id) {
			if method == protocol.MethodThreadTokenUsageUpdated {
				t.breakUsageContinuity()
			}
			return
		}
		canConfirm := method == protocol.MethodItemStarted || method == protocol.MethodItemCompleted || method == protocol.MethodTurnStarted || method == protocol.MethodTurnCompleted
		if canConfirm && confirmsInput(t, event) || t.compact && method == protocol.MethodTurnStarted && !event.Unprompted {
			c.bindTurnLocked(t, id)
		} else {
			c.retainEarlyLocked(t, method, raw)
			return
		}
	}
	if value(t.outcome.Turn.NativeTurnID) != id || t.outcome.Turn.Status != "" {
		if method == protocol.MethodThreadTokenUsageUpdated && t.outcome.Turn.Status == "" {
			t.breakUsageContinuity()
		}
		return
	}
	switch method {
	case "turn/started":
		for _, item := range event.Turn.Items {
			c.receiveItemLocked(t, item, false)
		}
	case "turn/completed":
		c.captureTerminalLocked(t, event.Turn)
	case "thread/tokenUsage/updated":
		var update tokenUsageEvent
		if json.Unmarshal(raw, &update) == nil {
			c.captureUsageLocked(t, update.Usage)
		}
	case "model/rerouted":
		var reroute protocol.ModelReroutedNotification
		if json.Unmarshal(raw, &reroute) == nil && reroute.ToModel != "" {
			t.rerouted = t.rerouted || t.outcome.Turn.Usage != nil && t.model != reroute.ToModel
			t.model = reroute.ToModel
			if t.rerouted && t.outcome.Turn.Usage != nil {
				t.outcome.Turn.Usage.Model = ""
			}
		}
	case "item/started", "item/completed":
		c.receiveItemLocked(t, event.Item, method == "item/completed")
	case "item/agentMessage/delta":
		t.streamed[event.ItemID] += len(event.Delta)
		t.outcome.ObservedOutput = true
		c.queueLocked(t, agentMessage(event.ItemID, event.Delta, acpmeta.MessagePhaseUnknown, false), nil)
	case "item/reasoning/textDelta", "item/reasoning/summaryTextDelta":
		c.queueLocked(t, acp.UpdateAgentThoughtText(event.Delta), nil)
	case "item/commandExecution/outputDelta", "item/fileChange/outputDelta", "item/fileChange/patchUpdated":
		c.emitToolOutputLocked(t, event)
	case "item/mcpToolCall/progress":
		var progress protocol.MCPToolCallProgressNotification
		if json.Unmarshal(raw, &progress) == nil && progress.Message != "" {
			c.queueLocked(t, acp.UpdateToolCall(acp.ToolCallId(progress.ItemID), acp.WithUpdateContent([]acp.ToolCallContent{acp.ToolContent(acp.TextBlock(progress.Message))})), nil)
		}

	case "turn/plan/updated":
		var plan protocol.TurnPlanUpdatedNotification
		if json.Unmarshal(raw, &plan) == nil {
			c.queueLocked(t, planUpdate(plan.Plan), nil)
		}
	}
}

func messageMeta(id, phase string, complete bool) map[string]any {
	meta := map[string]any{"message_id": id}
	source := acpmeta.MessageSourceNativeMessage
	if phase != acpmeta.MessagePhaseUnknown {
		source = acpmeta.MessageSourceNativePhase
	}
	return acpmeta.WithMessageInfo(meta, acpmeta.MessageInfo{Phase: phase, Complete: complete, Source: source})
}

func (c *Client) receiveItemLocked(t *turn, item protocol.ThreadItem, complete bool) {
	if item.ID == "" {
		return
	}
	switch item.Type {
	case "userMessage":
		if id := value(item.ClientID); id != "" {
			if _, steered := t.steerInputs[id]; steered {
				consumeInput(t, id)
			}
		}
	case "agentMessage":
		if !complete || item.Text == nil {
			return
		}
		if _, sent := t.completedMessages[item.ID]; sent {
			return
		}
		t.completedMessages[item.ID] = struct{}{}
		phase := acpmeta.MessagePhaseUnknown
		switch value(item.Phase) {
		case "commentary", "partial_answer":
			phase = acpmeta.MessagePhaseCommentary
		case "final_answer":
			phase = acpmeta.MessagePhaseFinal
		}
		if phase == acpmeta.MessagePhaseFinal {
			t.outcome.FinalMessage = &agentclient.FinalMessage{ID: item.ID, Text: *item.Text}
		} else if phase == acpmeta.MessagePhaseUnknown {
			t.finalCandidate = &agentclient.FinalMessage{ID: item.ID, Text: *item.Text}
		}
		t.outcome.ObservedOutput = true
		offset := min(len(*item.Text), t.streamed[item.ID])
		t.streamed[item.ID] = len(*item.Text)
		c.queueLocked(t, agentMessage(item.ID, (*item.Text)[offset:], phase, true), nil)
	case "commandExecution", "fileChange", "mcpToolCall", "dynamicToolCall", "webSearch", "imageGeneration", "collabAgentToolCall":
		c.toolItemLocked(t, item, complete)
	}
}

func (c *Client) captureTerminalLocked(t *turn, native protocol.TurnElement) {
	if native.ID == "" || native.ID != value(t.outcome.Turn.NativeTurnID) || t.outcome.Turn.Status != "" || t.finishing {
		return
	}
	switch native.Status {
	case protocol.FluffyCompleted:
		t.outcome.Turn.Status = acpmeta.NativeTurnCompleted
		t.outcome.StopReason = pointer(acp.StopReasonEndTurn)
	case protocol.FluffyInterrupted:
		t.outcome.Turn.Status = acpmeta.NativeTurnCancelled
		t.outcome.StopReason = pointer(acp.StopReasonCancelled)
	case protocol.Failed:
		t.outcome.Turn.Status = acpmeta.NativeTurnFailed
		t.outcome.NativeFailure = errors.New("Codex reported a failed turn")
		if native.Error != nil && native.Error.Message != "" {
			t.outcome.NativeFailure = errors.New(safeDiagnostic(native.Error.Message))
		}
	default:
		return
	}
	for _, item := range native.Items {
		c.receiveItemLocked(t, item, true)
	}
	t.nativeStatus = string(native.Status)
	if t.consumed == nil && (t.previous == nil || !t.previous.disputed) {
		t.session.usageCheckpoint = nil
	}
	c.orderOutcomeLocked(t)
	t.outcome.Turn.TerminalEventID = pointer(native.ID)
	if usage := t.outcome.Turn.Usage; usage != nil {
		usage.Complete = !t.usageGap && native.Status == protocol.FluffyCompleted
	}
	if t.outcome.Turn.Status == acpmeta.NativeTurnCompleted {
		if c.forkMode == acpmeta.ForkTurn {
			t.outcome.ForkPosition = native.ID
		}
		if t.outcome.FinalMessage == nil {
			t.outcome.FinalMessage = t.finalCandidate
		}
	}
	close(t.terminal)
	t.stopNative(errTerminal)
}

type observation struct {
	// notification is the independent ACP observation.
	notification acp.SessionNotification
	// bytes is the retained size charged to this owner until delivery returns.
	bytes int
}

func (c *Client) queueLocked(t *turn, update acp.SessionUpdate, meta map[string]any) {
	if c.host.Observe == nil || t.finishing || t.outcome.DeliveryError != nil {
		return
	}
	notification := acp.SessionNotification{SessionId: t.session.id, Update: update, Meta: meta}
	encoded, err := json.Marshal(notification)
	if err != nil {
		t.outcome.DeliveryError = err
		return
	}
	if t.observationBytes+len(encoded) > maxEarlyBytes {
		t.outcome.DeliveryError = errObservationBudget
		return
	}
	select {
	case t.updates <- observation{notification: notification, bytes: len(encoded)}:
		t.observationBytes += len(encoded)
	default:
		t.outcome.DeliveryError = errObservationBudget
	}
}

func (c *Client) observeTurn(t *turn) {
	defer close(t.observerDone)
	scope := agentclient.Scope{SessionID: t.session.id, HostInvocationID: t.input.HostInvocationID, NativeInvocationID: t.outcome.Turn.InvocationID}
	for {
		select {
		case <-t.observeCtx.Done():
			c.mu.Lock()
			if t.observationBytes > 0 && t.outcome.DeliveryError == nil {
				t.outcome.DeliveryError = context.Cause(t.observeCtx)
			}
			c.mu.Unlock()
			return
		case event, ok := <-t.updates:
			if !ok {
				return
			}
			err := c.host.Observe(t.observeCtx, scope, event.notification)
			c.mu.Lock()
			t.observationBytes -= event.bytes
			if err != nil && t.outcome.DeliveryError == nil {
				t.outcome.DeliveryError = err
			}
			c.mu.Unlock()
			if err != nil {
				return
			}
		}
	}
}

type tokenUsageEvent struct {
	// Usage is the native cumulative and last-request evidence.
	Usage tokenSnapshot `json:"tokenUsage"`
}

type tokenSnapshot struct {
	// Total is the cumulative session counter.
	Total *tokenCounters `json:"total"`
	// Last is the most recent native request only.
	Last *tokenCounters `json:"last"`
	// Window is the native context limit for this observation.
	Window *int64 `json:"modelContextWindow"`
}

type tokenCounters struct {
	// Input includes cache reads and writes.
	Input *int64 `json:"inputTokens"`
	// Read is the cache-read subset of input.
	Read *int64 `json:"cachedInputTokens"`
	// Write is the cache-write subset when reported.
	Write *int64 `json:"cacheWriteInputTokens"`
	// Output includes reasoning.
	Output *int64 `json:"outputTokens"`
	// Reasoning is the separately reported output subset.
	Reasoning *int64 `json:"reasoningOutputTokens"`
	// Context is the provider's total tokens for the latest context.
	Context *int64 `json:"totalTokens"`
}

type tokenCheckpoint struct {
	// total is the highest accepted cumulative sample for this boundary.
	total *tokenCounters
	// disputed means a regression left an interval requiring last-request proof.
	disputed bool
}

func (t *turn) checkpoint(total *tokenCounters, disputed bool) {
	t.previous = &tokenCheckpoint{total: total, disputed: disputed}
	t.session.usageCheckpoint = t.previous
	if t.usageHigh == nil || *total.Context > *t.usageHigh {
		t.usageHigh = total.Context
	}
}

func (t *turn) breakUsageContinuity() {
	t.previous = nil
	t.markUsageGap()
	t.session.usageCheckpoint = nil
}

func (t *turn) markUsageGap() {
	t.usageGap = true
	if t.consumed != nil && t.outcome.Turn.Usage != nil {
		t.outcome.Turn.Usage.Coverage = t.usageCoverage()
	}
}

func (t *turn) usageCoverage() string {
	if !t.usageGap {
		return "observed_turn"
	}
	if t.usageOnlyLast {
		return "last_request"
	}
	return "observed_requests"
}

func (c *Client) captureUsageLocked(t *turn, snapshot tokenSnapshot) {
	last, total := snapshot.Last, snapshot.Total
	if last != nil {
		t.outcome.ContextUsage = agentclient.ContextUsage{UsedTokens: nonnegative(last.Context), WindowTokens: nonnegative(snapshot.Window)}
	}
	if total == nil || nonnegative(total.Context) == nil {
		t.breakUsageContinuity()
		c.publishLastRequestLocked(t, last)
		return
	}
	var delta tokenCounters
	switch {
	case t.previous != nil:
		before := t.previous.total
		if countersRegressed(total, before) {
			// A lower report is not a new counter epoch. Keep the high
			// water mark and avoid charging the disputed interval.
			t.markUsageGap()
			t.checkpoint(before, true)
			c.publishLastRequestLocked(t, last)
			return
		}
		var comparable bool
		delta, comparable = subtractCounters(total, before)
		if t.previous.disputed {
			if !comparable || value(delta.Context) <= 0 {
				return
			}
			if !matchesLastRequest(delta, last) {
				// This advance provides a new floor, but not a proven
				// turn delta. The next matching request can resume accounting.
				t.checkpoint(total, true)
				c.publishLastRequestLocked(t, last)
				return
			}
		}
		t.usageGap = t.usageGap || !comparable
	case last != nil:
		if t.usageHigh != nil && (*total.Context <= *t.usageHigh || *total.Context-*t.usageHigh < value(last.Context)) {
			return
		}
		delta = *last
		t.usageGap = true
	default:
		return
	}
	t.checkpoint(total, false)
	if t.consumed == nil {
		t.usageOnlyLast = matchesLastRequest(delta, last)
	} else if value(delta.Context) > 0 {
		t.usageOnlyLast = false
	}
	t.consumed = addCounters(t.consumed, delta)
	c.publishUsageLocked(t, t.consumed, t.usageCoverage())
}

func (c *Client) publishLastRequestLocked(t *turn, last *tokenCounters) {
	// Standalone request evidence is useful until an attributable sum exists.
	// It never participates in that sum or replaces existing evidence.
	if t.outcome.Turn.Usage == nil && last != nil {
		c.publishUsageLocked(t, last, "last_request")
	}
}

func matchesLastRequest(delta tokenCounters, last *tokenCounters) bool {
	if last == nil {
		return false
	}
	equal := func(a, b *int64) bool {
		return a == nil && b == nil || a != nil && b != nil && *a == *b
	}
	return equal(delta.Context, last.Context) && equal(delta.Input, last.Input) &&
		equal(delta.Read, last.Read) && equal(delta.Write, last.Write) &&
		equal(delta.Output, last.Output) && equal(delta.Reasoning, last.Reasoning)
}

func (c *Client) publishUsageLocked(t *turn, counters *tokenCounters, coverage string) {
	// The v0.163 native schema defaults absent cacheWriteInputTokens to
	// zero for input decomposition. Keep absence distinct in the receipt.

	var input *int64
	if counters.Input != nil && counters.Read != nil && *counters.Read <= *counters.Input &&
		value(counters.Write) <= *counters.Input-*counters.Read {
		input = pointer(*counters.Input - *counters.Read - value(counters.Write))
	}
	model := t.model
	if t.rerouted {
		model = ""
	}

	t.outcome.Turn.Usage = &acpmeta.TurnUsage{InputTokens: input, CacheReadTokens: nonnegative(counters.Read), CacheWriteTokens: nonnegative(counters.Write),
		OutputTokens: nonnegative(counters.Output), ReasoningTokens: nonnegative(counters.Reasoning), Model: model,
		Source: "codex/thread/tokenUsage/updated", Coverage: coverage, ContextKnown: t.outcome.ContextUsage.UsedTokens != nil && t.outcome.ContextUsage.WindowTokens != nil}
	update := &acp.SessionUsageUpdate{Meta: acpmeta.WithTurnUsage(nil, *t.outcome.Turn.Usage)}
	used, window := t.outcome.ContextUsage.UsedTokens, t.outcome.ContextUsage.WindowTokens
	if used != nil && window != nil && *used <= math.MaxInt && *window <= math.MaxInt {
		update.Used, update.Size = int(*used), int(*window)
	}
	c.queueLocked(t, acp.SessionUpdate{UsageUpdate: update}, nil)
}

func nonnegative(n *int64) *int64 {
	if n == nil || *n < 0 {
		return nil
	}
	return n
}

func countersRegressed(end, start *tokenCounters) bool {
	regressed := func(a, b *int64) bool { return nonnegative(a) != nil && nonnegative(b) != nil && *a < *b }
	return regressed(end.Context, start.Context) || regressed(end.Input, start.Input) || regressed(end.Read, start.Read) ||
		regressed(end.Write, start.Write) || regressed(end.Output, start.Output) || regressed(end.Reasoning, start.Reasoning)
}

func subtractCounters(end, start *tokenCounters) (tokenCounters, bool) {
	valid := true
	subtract := func(a, b *int64) *int64 {
		if a == nil && b == nil {
			return nil
		}
		if nonnegative(a) == nil || nonnegative(b) == nil || *a < *b {
			valid = false
			return nil
		}
		return pointer(*a - *b)
	}
	result := tokenCounters{Input: subtract(end.Input, start.Input), Read: subtract(end.Read, start.Read), Write: subtract(end.Write, start.Write),
		Output: subtract(end.Output, start.Output), Reasoning: subtract(end.Reasoning, start.Reasoning), Context: subtract(end.Context, start.Context)}
	return result, valid
}

func addCounters(previous *tokenCounters, delta tokenCounters) *tokenCounters {
	if previous == nil {
		return &tokenCounters{Input: nonnegative(delta.Input), Read: nonnegative(delta.Read), Write: nonnegative(delta.Write),
			Output: nonnegative(delta.Output), Reasoning: nonnegative(delta.Reasoning), Context: nonnegative(delta.Context)}
	}
	add := func(old, change *int64) *int64 {
		if nonnegative(change) == nil || value(old) > math.MaxInt64-*change {
			return old
		}
		return pointer(value(old) + *change)
	}
	return &tokenCounters{Input: add(previous.Input, delta.Input), Read: add(previous.Read, delta.Read), Write: add(previous.Write, delta.Write),
		Output: add(previous.Output, delta.Output), Reasoning: add(previous.Reasoning, delta.Reasoning), Context: add(previous.Context, delta.Context)}
}

func encodePrompt(blocks []acp.ContentBlock) ([]protocol.InputElement, error) {
	if len(blocks) == 0 {
		return nil, acp.NewInvalidParams("prompt is empty")
	}
	input := make([]protocol.InputElement, 0, len(blocks))
	text := func(s string) protocol.InputElement {
		return protocol.InputElement{Type: protocol.UserInputTypeText, Text: &s}
	}
	for _, block := range blocks {
		if err := block.Validate(); err != nil {
			return nil, acp.NewInvalidParams(err.Error())
		}
		switch {
		case block.Text != nil:
			input = append(input, text(block.Text.Text))
		case block.Image != nil:
			image := "data:" + block.Image.MimeType + ";base64," + block.Image.Data
			if address, err := url.Parse(value(block.Image.Uri)); err == nil && (address.Scheme == "https" || address.Scheme == "http") && address.Host != "" {
				image = address.String()
			}
			input = append(input, protocol.InputElement{Type: protocol.UserInputTypeImage, URL: &image})
		case block.ResourceLink != nil:
			input = append(input, text(fmt.Sprintf("[@%s](%s)", block.ResourceLink.Name, block.ResourceLink.Uri)))
		case block.Resource != nil && block.Resource.Resource.TextResourceContents != nil:
			r := block.Resource.Resource.TextResourceContents
			input = append(input, text(fmt.Sprintf("<context ref=%q>\n%s\n</context>", r.Uri, r.Text)))
		case block.Resource != nil && block.Resource.Resource.BlobResourceContents != nil:
			r := block.Resource.Resource.BlobResourceContents
			mime := value(r.MimeType)
			if strings.HasPrefix(mime, "image/") {
				image := "data:" + mime + ";base64," + r.Blob
				input = append(input, protocol.InputElement{Type: protocol.UserInputTypeImage, URL: &image})
			} else {
				input = append(input, text(fmt.Sprintf("<context ref=%q mimeType=%q encoding=\"base64\">\n%s\n</context>", r.Uri, mime, r.Blob)))
			}
		default:
			return nil, acp.NewInvalidParams("unsupported Codex prompt content")
		}
	}
	return input, nil
}

func planUpdate(steps []protocol.PlanElement) acp.SessionUpdate {
	entries := make([]acp.PlanEntry, 0, len(steps))
	for _, step := range steps {
		status := acp.PlanEntryStatusPending
		switch step.Status {
		case "inProgress":
			status = acp.PlanEntryStatusInProgress
		case "completed":
			status = acp.PlanEntryStatusCompleted
		}
		entries = append(entries, acp.PlanEntry{Content: step.Step, Priority: acp.PlanEntryPriorityMedium, Status: status})
	}
	return acp.UpdatePlan(entries...)
}

func (c *Client) toolItemLocked(t *turn, item protocol.ThreadItem, complete bool) {
	status := acp.ToolCallStatusInProgress
	if complete {
		status = acp.ToolCallStatusCompleted
		if value(item.Status) == "failed" || value(item.Status) == "declined" || item.Error != nil || item.Success != nil && !*item.Success || value(item.ExitCode) != 0 {
			status = acp.ToolCallStatusFailed
		}
	}
	previous, known := t.tools[item.ID]
	if known && (previous == acp.ToolCallStatusCompleted || previous == acp.ToolCallStatusFailed) {
		return
	}
	t.tools[item.ID] = status
	id := acp.ToolCallId(item.ID)
	kind, title := acp.ToolKindOther, string(item.Type)
	var content []acp.ToolCallContent
	var locations []acp.ToolCallLocation
	switch item.Type {
	case "commandExecution":
		kind, title = acp.ToolKindExecute, value(item.Command)
		if item.Cwd != nil {
			locations = append(locations, acp.ToolCallLocation{Path: *item.Cwd})
		}
	case "fileChange":
		kind, title = acp.ToolKindEdit, "Edit files"
		for _, change := range item.Changes {
			locations = append(locations, acp.ToolCallLocation{Path: change.Path})
			if change.Diff != "" {
				content = append(content, acp.ToolContent(acp.TextBlock(change.Diff)))
			}
		}
	case "webSearch":
		kind, title = acp.ToolKindSearch, value(item.Query)
	case "mcpToolCall", "dynamicToolCall", "collabAgentToolCall":
		title = value(item.Tool)
		if server := value(item.Server); server != "" {
			title = server + "/" + title
		}
	case "imageGeneration":
		title = "Generate image"
	}
	if title == "" {
		title = string(item.Type)
	}
	if item.AggregatedOutput != nil {
		content = append(content, acp.ToolContent(acp.TextBlock(*item.AggregatedOutput)))
	}
	if !known {
		c.queueLocked(t, acp.StartToolCall(id, title, acp.WithStartKind(kind), acp.WithStartStatus(status), acp.WithStartContent(content), acp.WithStartLocations(locations), acp.WithStartRawInput(item.Arguments), acp.WithStartRawOutput(item.Result)), nil)
		return
	}
	c.queueLocked(t, acp.UpdateToolCall(id, acp.WithUpdateStatus(status), acp.WithUpdateContent(content), acp.WithUpdateLocations(locations), acp.WithUpdateRawOutput(item.Result)), nil)
}

func confirmsInput(t *turn, event turnEvent) bool {
	if t.input.NativeInputID == nil {
		return false
	}
	matches := func(item protocol.ThreadItem) bool {
		return item.Type == protocol.UserMessage && value(item.ClientID) == *t.input.NativeInputID
	}
	if matches(event.Item) {
		return true
	}
	for _, item := range event.Turn.Items {
		if matches(item) {
			return true
		}
	}
	return false
}

func (c *Client) retainEarlyLocked(t *turn, method string, raw json.RawMessage) {
	switch method {
	case protocol.MethodTurnStarted, protocol.MethodTurnCompleted, protocol.MethodThreadTokenUsageUpdated, protocol.MethodModelRerouted:
		if t.earlyEvidenceCount >= maxPendingUpdates || t.earlyEvidenceBytes+len(raw) > maxFrameBytes {
			if method == protocol.MethodThreadTokenUsageUpdated {
				t.breakUsageContinuity()
			}
			if t.outcome.DeliveryError == nil {
				t.outcome.DeliveryError = errObservationBudget
			}
			return
		}
		t.earlyEvidenceCount++
		t.earlyEvidenceBytes += len(raw)
	default:
		if t.earlyBytes+len(raw) > maxEarlyBytes || len(t.early)-t.earlyEvidenceCount >= maxPendingUpdates {
			if t.outcome.DeliveryError == nil {
				t.outcome.DeliveryError = errObservationBudget
			}
			return
		}
		t.earlyBytes += len(raw)
	}
	t.early = append(t.early, earlyFrame{method: method, params: raw})
}

func agentMessage(id, text, phase string, complete bool) acp.SessionUpdate {
	update := acp.UpdateAgentMessageText(text)
	update.AgentMessageChunk.MessageId = pointer(id)
	update.AgentMessageChunk.Meta = messageMeta(id, phase, complete)
	return update
}

func (c *Client) emitToolOutputLocked(t *turn, event turnEvent) {
	if _, exists := t.tools[event.ItemID]; !exists || event.Delta == "" {
		return
	}
	key := "terminal_output_delta"
	if c.fullTerminalOutput {
		key = "terminal_output"
		previous := t.toolOutput[event.ItemID]
		if len(previous)+len(event.Delta) > maxEarlyBytes {
			return
		}
		event.Delta = previous + event.Delta
		t.toolOutput[event.ItemID] = event.Delta
	}
	update := acp.UpdateToolCall(acp.ToolCallId(event.ItemID), acp.WithUpdateContent([]acp.ToolCallContent{acp.ToolContent(acp.TextBlock(event.Delta))}))
	update.ToolCallUpdate.Meta = map[string]any{key: map[string]any{"terminal_id": event.ItemID, "data": event.Delta}}
	c.queueLocked(t, update, nil)
}

func (c *Client) captureMCPStartupLocked(raw json.RawMessage) {
	var status protocol.MCPServerStatusUpdatedNotification
	if json.Unmarshal(raw, &status) != nil || status.ThreadID == nil || status.Name == "" || c.host.Observe == nil ||
		status.Status != protocol.PurpleFailed && status.Status != protocol.Cancelled {
		return
	}
	s := c.sessions[acp.SessionId(*status.ThreadID)]
	if s == nil || s.closing || c.closing || c.interactionCount >= maxNativeInteractions {
		return
	}
	reported, expected := s.mcpServers[status.Name]
	if !expected || reported {
		return
	}
	s.mcpServers[status.Name] = true
	detail := safeDiagnostic(value(status.Error))
	if detail == "" {
		detail = "Codex could not start the MCP server"
	}
	update := acp.StartToolCall(acp.ToolCallId("mcp_startup."+url.QueryEscape(status.Name)), "mcp__"+status.Name+"__startup",
		acp.WithStartKind(acp.ToolKindOther), acp.WithStartStatus(acp.ToolCallStatusFailed),
		acp.WithStartContent([]acp.ToolCallContent{acp.ToolContent(acp.TextBlock(detail))}))
	c.interactionCount++
	c.interactions.Add(1)
	go func() {
		defer c.interactions.Done()
		defer func() { c.mu.Lock(); c.interactionCount--; c.mu.Unlock() }()
		ctx, cancel := context.WithTimeout(c.life, controlTimeout)
		defer cancel()
		_ = c.host.Observe(ctx, agentclient.Scope{SessionID: s.id}, acp.SessionNotification{SessionId: s.id, Update: update})
	}()
}

func recordSteeredItem(t *turn, item protocol.ThreadItem) {
	if id := value(item.ClientID); id != "" {
		if _, attempted := t.steerInputs[id]; attempted {
			consumeInput(t, id)
		}
	}
}

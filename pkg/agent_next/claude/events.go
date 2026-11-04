package claude

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/JieWaZi/acp-go/pkg/acpmeta"
	agentclient "github.com/JieWaZi/acp-go/pkg/agent_next"
	"github.com/JieWaZi/acp-go/pkg/agent_next/claude/protocol"
	acp "github.com/JieWaZi/acp-go/sdk"
)

func (s *session) receive(frame protocol.Message) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fault != nil {
		return
	}
	switch m := frame.(type) {
	case *protocol.SystemInitMessage:
		if m.SessionID != "" && m.SessionID != string(s.id) {
			s.faultAndStopLocked(errors.New("Claude reported a different session ID"))
			return
		}
		s.init = *m
		if s.open && s.settings.observeInit(*m) {
			s.announceSettingsLocked()
		}
	case *protocol.SystemMessage:
		if s.matches(m.SessionID) {
			s.systemLocked(m)
		}
	case *protocol.ResultMessage:
		if s.matches(m.SessionID) {
			s.resultLocked(m)
		}
	case *protocol.AssistantMessage:
		if s.matches(m.SessionID) {
			s.assistantLocked(m)
		}
	case *protocol.StreamEventMessage:
		if s.matches(m.SessionID) {
			s.streamLocked(m)
		}
	case *protocol.UserMessage:
		if s.matches(m.SessionID) {
			if in := s.sent[m.UUID]; in != nil && m.ParentToolUseID == nil {
				in.echoed = true
			}
			s.toolResultsLocked(m)
		}
	case *protocol.ToolProgressMessage:
		if s.matches(m.SessionID) {
			s.toolProgressLocked(m)
		}
	case *protocol.TaskMessage:
		if s.matches(m.SessionID) {
			s.taskUpdateLocked(m)
		}
	case *protocol.RateLimitMessage:
		if s.matches(m.SessionID) {
			s.rateLimitLocked(m)
		}
	case *protocol.ControlRequestMessage:
		s.interactionLocked(m)
	case *protocol.ControlCancelRequestMessage:
		if cancel := s.interactions[m.RequestID]; cancel != nil {
			cancel()
		}
	}
}

func (s *session) matches(id string) bool { return id == "" || id == string(s.id) }

func (s *session) systemLocked(m *protocol.SystemMessage) {
	switch m.Subtype {
	case "session_state_changed":
		if m.State == "running" || m.State == "requires_action" {
			// A state snapshot proves the native boundary, but it carries
			// no input ID and cannot prove which pending input it consumed.
			s.nativeInvocationLocked(protocol.TurnIdentity{}, false)
		} else if m.State == "idle" && s.current == nil {
			s.nativeQueued = 0
			s.signalLocked()
		}
	case "background_tasks_changed":
		if m.BackgroundTasks != nil {
			s.observeBackgroundLocked(*m.BackgroundTasks)
			s.sessionUpdateLocked(metaEvent(), acpmeta.WithNativeBackground(nil, acpmeta.NativeBackground{ActiveCount: acp.Ptr(int64(s.background))}))
			s.signalLocked()
		}
	case "commands_changed":
		s.initialization.Commands = slices.Clone(m.Commands)
		s.announceCommandsLocked()
	case "compact_boundary", "status":
		if s.current == nil && m.Subtype == "compact_boundary" && m.CompactMetadata != nil && m.CompactMetadata.Trigger == "manual" {
			s.nativeInvocationLocked(protocol.TurnIdentity{}, true)
		}
		if inv := s.current; inv != nil {
			if m.Subtype == "compact_boundary" && m.CompactMetadata != nil && m.CompactMetadata.Trigger == "manual" || m.CompactResult == "success" {
				inv.compactConfirmed = true
			}
		}
	case "api_retry":
		if inv := s.current; inv != nil {
			s.sendTurnLocked(inv, metaEvent(), map[string]any{"claude.retry": map[string]any{
				"attempt": m.RetryAttempt, "max_attempts": m.MaxRetries, "delay_ms": m.RetryDelayMS, "http_status": m.RetryHTTPStatus,
			}})
		}
	case "conversation_reset":
		s.counters = newQueryCounters(false)
	}
}

func (s *session) announceCommandsLocked() {
	commands := make([]acp.AvailableCommand, 0, len(s.initialization.Commands))
	terminal := s.init.TerminalSlashCommands
	for _, cmd := range s.initialization.Commands {
		name := strings.TrimPrefix(cmd.Name, "/")
		if name == "" || slices.Contains(terminal, name) || slices.Contains(terminal, "/"+name) || name == "exit" || name == "quit" {
			continue
		}
		command := acp.AvailableCommand{Name: name, Description: cmd.Description}
		switch hint := cmd.ArgumentHint.(type) {
		case string:
			if hint != "" {
				command.Input = &acp.AvailableCommandInput{Unstructured: &acp.UnstructuredCommandInput{Hint: hint}}
			}
		case []any:
			var parts []string
			for _, value := range hint {
				if text, ok := value.(string); ok {
					parts = append(parts, text)
				}
			}
			if len(parts) > 0 {
				command.Input = &acp.AvailableCommandInput{Unstructured: &acp.UnstructuredCommandInput{Hint: strings.Join(parts, " ")}}
			}
		}
		commands = append(commands, command)
	}
	s.sessionUpdateLocked(acp.SessionUpdate{AvailableCommandsUpdate: &acp.SessionAvailableCommandsUpdate{
		SessionUpdate: "available_commands_update", AvailableCommands: commands,
	}}, nil)
}

func (s *session) rateLimitLocked(m *protocol.RateLimitMessage) {
	info := m.Info
	if s.current != nil && info.Status == "rejected" {
		s.current.rejectedLimit = &info
	}
	meta := map[string]any{"claude.rate_limit": info}
	if s.current != nil {
		s.sendTurnLocked(s.current, metaEvent(), meta)
	} else {
		s.sessionUpdateLocked(metaEvent(), meta)
	}
}

func consumedIDs(identity protocol.TurnIdentity) []string {
	ids := identity.UserMessageUUIDs
	if len(ids) == 0 && identity.UserMessageUUID != "" {
		ids = []string{identity.UserMessageUUID}
	}
	out := make([]string, 0, min(len(ids), maxPendingInputs))
	for _, id := range ids {
		if !isUUID(id) || slices.Contains(out, id) {
			continue
		}
		out = append(out, id)
		if len(out) == maxPendingInputs {
			break
		}
	}
	return out
}

// nativeInvocationLocked resolves the native owner before recording consumed
// UUIDs. A bare running notification can nominate an opening input; the
// subsequent main-thread reply must independently prove its consumption.
func (s *session) nativeInvocationLocked(identity protocol.TurnIdentity, legacy bool) *invocation {
	ids := consumedIDs(identity)
	explicit := identity.UserMessageUUID != "" || len(identity.UserMessageUUIDs) > 0
	provider := identity.ResumeReason != "" || identity.Origin != nil && identity.Origin.Kind != "" && identity.Origin.Kind != "human"
	human := identity.Origin != nil && identity.Origin.Kind == "human"
	inv := s.current
	pending, related, allRetired := false, false, len(ids) > 0
	for _, id := range ids {
		if _, retired := s.retiredInputs[id]; retired {
			continue
		}
		allRetired = false
		if s.sent[id] != nil {
			pending = true
		}
		if inv != nil {
			_, seen := inv.seenInputs[id]
			related = related || seen
		}
	}
	if allRetired || inv != nil && explicit && !pending && !related && !provider && inv.outcome.Turn.Origin == acpmeta.NativeTurnOriginHost {
		return nil
	}
	opening := s.openingHostLocked()
	if !explicit && !provider && legacy && opening != nil && opening.echoed &&
		(inv == nil || inv.openingHost == opening && !inv.outcome.ObservedOutput) {
		ids = []string{opening.identity.InputID}
	}
	if inv == nil {
		origin := acpmeta.NativeTurnOriginUnknown
		if provider || len(s.sent) == 0 {
			origin = acpmeta.NativeTurnOriginProvider
		}
		inv = s.beginNativeLocked(origin)
		if inv == nil {
			return nil
		}
		inv.openingHost = opening
	}
	if inv.outcome.Turn.Origin == acpmeta.NativeTurnOriginUnknown {
		switch {
		case provider:
			inv.outcome.Turn.Origin = acpmeta.NativeTurnOriginProvider
		case inv.openingHost != nil && !inv.outcome.ObservedOutput &&
			slices.Contains(ids, inv.openingHost.identity.InputID):
			inv.outcome.Turn.Origin = acpmeta.NativeTurnOriginHost
		case human && (pending || related):
			// Result frames can name an input already consumed by this
			// invocation, after it has left the pending UUID registry.
			inv.outcome.Turn.Origin = acpmeta.NativeTurnOriginHost
		}
	}
	for _, id := range ids {
		s.consumeLocked(inv, id)
	}
	if inv.outcome.Turn.Origin == acpmeta.NativeTurnOriginHost && inv.scope.HostInvocationID == "" && len(inv.inputs) > 0 {
		inv.scope.HostInvocationID = inv.inputs[0].identity.HostInvocationID
	}
	for _, in := range inv.inputs {
		s.reconcileCancellationLocked(in)
	}
	return inv
}

func (s *session) openingHostLocked() *input {
	if len(s.sent) != 1 || s.nativeQueued != 0 {
		return nil
	}
	for _, in := range s.sent {
		if !in.mayFollowProvider && in.message.Priority == "" {
			return in
		}
	}
	return nil
}

func (s *session) beginNativeLocked(origin string) *invocation {
	if len(s.retiredInputs) >= maxRememberedIDs || !s.ledger.reserve() {
		s.faultAndStopLocked(errCapacity)
		return nil
	}
	id, err := newUUID()
	if err != nil {
		s.ledger.complete(agentclient.NativeOutcome{}, false)
		s.faultAndStopLocked(err)
		return nil
	}
	ctx, cancel := context.WithCancel(s.ctx)
	inv := &invocation{outcome: agentclient.NativeOutcome{
		Turn: acpmeta.NativeTurn{InvocationID: id, Origin: origin}, PossibleSideEffects: true,
	}, scope: agentclient.Scope{SessionID: s.id, NativeInvocationID: id}, seenInputs: make(map[string]struct{}),
		ctx: ctx, cancel: cancel, closed: make(chan struct{}), partialUsage: make(map[string]requestUsage),
	}
	s.current = inv
	s.epoch++
	s.signalLocked()
	return inv
}

func (s *session) consumeLocked(inv *invocation, id string) {
	if len(inv.seenInputs) >= maxPendingInputs {
		return
	}
	if _, seen := inv.seenInputs[id]; seen {
		return
	}
	if _, retired := s.retiredInputs[id]; retired {
		return
	}
	inv.seenInputs[id] = struct{}{}
	inv.outcome.Turn.ConsumedInputIDs = append(inv.outcome.Turn.ConsumedInputIDs, id)
	in := s.sent[id]
	if in == nil {
		return
	}
	delete(s.sent, id)
	in.inv = inv
	in.phase = consumed
	in.submission = agentclient.SubmissionSent
	in.admission = agentclient.AdmissionConfirmed
	inv.inputs = append(inv.inputs, in)
	inv.compact = inv.compact || in.compact
	s.signalLocked()
}

func (s *session) resultLocked(m *protocol.ResultMessage) {
	if startup := decodeOptional[struct {
		Reason string `json:"startup_failure_reason"`
	}](m.RawJSON()); startup != nil && startup.Reason != "" {
		var err error = errors.New("Claude native startup failed")
		if startup.Reason == "gateway_signin_required" {
			err = nativeFailure("authentication")
		}
		s.faultAndStopLocked(err)
		return
	}
	if m.ResultIndex != nil && (*m.ResultIndex < 0 || s.lastResultSeen && *m.ResultIndex <= s.lastResultIndex) {
		return
	}
	if m.ResultIndex == nil && m.UUID == "" {
		return
	}
	if m.UUID != "" && len(m.UUID) <= 512 {
		if _, seen := s.terminalIDs[m.UUID]; seen {
			return
		}
	}
	usage := reportedUsage(m.Usage)
	// An unnumbered, unconsumed, singular failure key may be an input
	// delivery error produced by the relay. It cannot retire the native turn.
	if nativeFailed(m) && m.ResultIndex == nil && m.ResumeReason == "" && m.UserMessageUUID != "" && len(m.UserMessageUUIDs) == 0 && !positiveUsage(usage) {
		confirmed := s.current != nil && slices.Contains(s.current.outcome.Turn.ConsumedInputIDs, m.UserMessageUUID)
		if !confirmed {
			if in := s.sent[m.UserMessageUUID]; in != nil {
				s.retiredInputs[m.UserMessageUUID] = struct{}{}
				in.admission = agentclient.AdmissionNotAccepted
				s.finishInputLocked(in, errors.New("Claude rejected native input delivery"))
			}
			return
		}
	}
	gap := m.ResultIndex != nil && (s.lastResultSeen && *m.ResultIndex-s.lastResultIndex > 1 || !s.lastResultSeen && *m.ResultIndex > 0)
	var inv, lost *invocation
	if gap {
		defer func() {
			if lost != nil {
				s.publishAfterDeliveryLocked(lost)
			}
		}()
		s.counters = newQueryCounters(true)
		if current := s.current; current != nil {
			related := slices.ContainsFunc(consumedIDs(m.TurnIdentity), func(id string) bool {
				_, known := current.seenInputs[id]
				return known
			})
			if !related {
				current.outcome.OperationError = errors.New("Claude native result sequence gap")
				s.freezeNativeLocked(current)
				lost = current
			}
		}
		// Sequence proves this terminal exists even when no UUID or origin
		// relates it to a waiter. Do not borrow either from the lost turn.
		if s.current == nil && len(consumedIDs(m.TurnIdentity)) == 0 && m.ResumeReason == "" &&
			(m.Origin == nil || m.Origin.Kind == "" || m.Origin.Kind == "human") {
			inv = s.beginNativeLocked(acpmeta.NativeTurnOriginUnknown)
		}
	}
	if inv == nil {
		inv = s.nativeInvocationLocked(m.TurnIdentity, !gap)
	}
	if inv == nil {
		return
	}
	if m.UUID != "" && len(m.UUID) <= 512 {
		if len(s.terminalIDs) >= maxRememberedIDs {
			s.faultAndStopLocked(errCapacity)
			return
		}
		s.terminalIDs[m.UUID] = struct{}{}
		inv.outcome.Turn.TerminalEventID = acp.Ptr(m.UUID)
	}
	if m.ResultIndex != nil {
		s.lastResultIndex = *m.ResultIndex
		s.lastResultSeen = true
	}
	if m.QueuedTurnCount != nil && *m.QueuedTurnCount >= 0 {
		s.nativeQueued = *m.QueuedTurnCount
	}
	if m.FastModeState != "" && s.settings.observeFast(m.FastModeState) {
		s.announceSettingsLocked()
	}
	if inv.outcome.ContextUsage.WindowTokens == nil && inv.model != "" && m.ModelUsage[inv.model].ContextWindow > 0 {
		inv.outcome.ContextUsage.WindowTokens = acp.Ptr(m.ModelUsage[inv.model].ContextWindow)
	}
	inv.outcome.Turn.Usage = s.counters.final(inv, m, usage)
	reason, failure := resultReason(m, inv)
	inv.outcome.StopReason = reason
	inv.outcome.NativeFailure = failure
	switch {
	case reason != nil && *reason == acp.StopReasonCancelled:
		inv.outcome.Turn.Status = acpmeta.NativeTurnCancelled
	case failure != nil:
		inv.outcome.Turn.Status = acpmeta.NativeTurnFailed
	default:
		inv.outcome.Turn.Status = acpmeta.NativeTurnCompleted
	}
	if inv.compact && failure == nil && inv.outcome.Turn.Status == acpmeta.NativeTurnCompleted && !inv.compactConfirmed {
		inv.outcome.OperationError = errors.New("Claude did not confirm manual compaction")
	}
	if text := firstString(m.Result, inv.completeText); failure == nil && m.Subtype == "success" && m.LocalCommand == "" && inv.outcome.Turn.Status != acpmeta.NativeTurnCancelled && text != "" {
		id := inv.lastMessage
		if id == "" {
			id = inv.outcome.Turn.InvocationID
		}
		inv.outcome.ObservedOutput = true
		inv.outcome.FinalMessage = &agentclient.FinalMessage{ID: id, Text: text}
		if inv.completeText == "" && inv.streamText == "" {
			s.textLocked(inv, id, text, false, true)
		}
	}
	if lost == nil {
		s.sealLocked(inv)
	} else {
		s.freezeNativeLocked(inv)
		s.publishAfterDeliveryLocked(lost, inv)
		lost = nil
	}
	s.refreshContextLocked()
}

func nativeFailed(m *protocol.ResultMessage) bool {
	return m.IsError || m.Subtype != "success"
}

func resultReason(m *protocol.ResultMessage, inv *invocation) (*acp.StopReason, error) {
	switch m.TerminalReason {
	case "aborted_streaming", "aborted_tools":
		return acp.Ptr(acp.StopReasonCancelled), nil
	case "max_turns":
		return acp.Ptr(acp.StopReasonMaxTurnRequests), nil
	}
	if m.Subtype == "error_max_turns" {
		return acp.Ptr(acp.StopReasonMaxTurnRequests), nil
	}
	if nativeFailed(m) {
		if inv.rejectedLimit != nil && inv.rejectedLimit.ErrorCode == "credits_required" {
			return nil, nativeFailure("billing_error")
		}
		if inv.rejectedLimit != nil || m.TerminalReason == "blocking_limit" || m.APIErrorStatus != nil && *m.APIErrorStatus == 429 {
			return nil, nativeFailure("rate_limit")
		}
		if m.APIErrorStatus != nil && (*m.APIErrorStatus == 401 || *m.APIErrorStatus == 403) {
			return nil, nativeFailure("authentication")
		}
		return nil, nativeFailure("native_execution_failed")
	}
	if m.StopReason != nil {
		switch *m.StopReason {
		case "max_tokens":
			return acp.Ptr(acp.StopReasonMaxTokens), nil
		case "refusal":
			return acp.Ptr(acp.StopReasonRefusal), nil
		}
	}
	return acp.Ptr(acp.StopReasonEndTurn), nil
}

func (s *session) assistantLocked(m *protocol.AssistantMessage) {
	if m.ParentToolUseID != nil || m.AgentID != "" {
		if owner := s.sidechainOwnerLocked(m.AgentID, m.ParentToolUseID); owner.scope.NativeInvocationID != "" {
			for _, block := range m.Message.Content {
				if block.Type == "tool_use" || block.Type == "server_tool_use" {
					s.startToolLocked(owner, block)
				}
			}
		}
		return
	}
	inv := s.nativeInvocationLocked(m.TurnIdentity, true)
	if inv == nil {
		return
	}
	inv.model = m.Message.Model
	if m.Message.Usage != nil {
		inv.recordUsage(m.Message.ID, m.Message.Model, reportedUsage(*m.Message.Usage))
	}
	s.applyContextLocked(inv, m)
	s.messageIdentity(inv, m.Message.ID)
	var complete, thinking strings.Builder
	for _, block := range m.Message.Content {
		switch block.Type {
		case "text":
			if m.ParentToolUseID == nil {
				complete.WriteString(block.Text)
			}
		case "thinking":
			if m.ParentToolUseID == nil {
				thinking.WriteString(block.Thinking)
			}
		case "tool_use", "server_tool_use":
			s.startToolLocked(toolOwner{scope: inv.scope}, block)
		}
	}
	if m.ParentToolUseID != nil {
		return
	}
	if complete.Len() > 0 && complete.String() != inv.completeText {
		if strings.HasPrefix(complete.String(), inv.streamText) {
			s.textLocked(inv, inv.lastMessage, strings.TrimPrefix(complete.String(), inv.streamText), false, true)
		}
		inv.completeText = boundedText(complete.String())
	}
	if thinking.Len() > 0 && strings.HasPrefix(thinking.String(), inv.streamThought) {
		s.textLocked(inv, inv.lastMessage, strings.TrimPrefix(thinking.String(), inv.streamThought), true, true)
		inv.streamThought = boundedText(thinking.String())
	}
}

func (s *session) streamLocked(m *protocol.StreamEventMessage) {
	if m.ParentToolUseID != nil || m.AgentID != "" {
		block := m.Event.ContentBlock
		if m.Event.Type == "content_block_start" && block != nil && (block.Type == "tool_use" || block.Type == "server_tool_use") {
			s.startToolLocked(s.sidechainOwnerLocked(m.AgentID, m.ParentToolUseID), *block)
		}
		return
	}
	inv := s.nativeInvocationLocked(m.TurnIdentity, true)
	if inv == nil {
		return
	}
	e := m.Event
	switch e.Type {
	case "message_start":
		if e.Message == nil || m.ParentToolUseID != nil {
			return
		}
		inv.model = e.Message.Model
		s.messageIdentity(inv, e.Message.ID)
		if e.Message.Usage != nil {
			inv.recordUsage(e.Message.ID, e.Message.Model, reportedUsage(*e.Message.Usage))
		}
	case "message_delta":
		if e.Usage == nil || m.ParentToolUseID != nil {
			return
		}
		inv.recordUsage(inv.nativeMessage, inv.model, acpmeta.TurnUsage{
			InputTokens: e.Usage.InputTokens, CacheReadTokens: e.Usage.CacheReadInputTokens,
			CacheWriteTokens: e.Usage.CacheCreationInputTokens, OutputTokens: nonnegative(e.Usage.OutputTokens),
		})
	case "content_block_start":
		if e.ContentBlock == nil {
			return
		}
		block := *e.ContentBlock
		if block.Type == "tool_use" || block.Type == "server_tool_use" {
			s.startToolLocked(toolOwner{scope: inv.scope}, block)
		}
		if block.Type == "text" && block.Text != "" && m.ParentToolUseID == nil {
			if inv.lastMessage == "" {
				s.messageIdentity(inv, "")
			}
			s.textLocked(inv, inv.lastMessage, block.Text, false, false)
			inv.streamText = boundedConcat(inv.streamText, block.Text)
		}
	case "content_block_delta":
		if e.Delta == nil || m.ParentToolUseID != nil {
			return
		}
		if inv.lastMessage == "" {
			s.messageIdentity(inv, "")
		}
		switch e.Delta.Type {
		case "text_delta":
			s.textLocked(inv, inv.lastMessage, e.Delta.Text, false, false)
			inv.streamText = boundedConcat(inv.streamText, e.Delta.Text)
		case "thinking_delta":
			s.textLocked(inv, inv.lastMessage, e.Delta.Thinking, true, false)
			inv.streamThought = boundedConcat(inv.streamThought, e.Delta.Thinking)
		}
	}
}

func (s *session) messageIdentity(inv *invocation, native string) {
	if inv.lastMessage != "" && (native == "" || native == inv.nativeMessage || inv.nativeMessage == "") {
		if inv.nativeMessage == "" {
			inv.nativeMessage = native
		}
		return
	}
	id, err := newUUID()
	if err != nil {
		id = inv.outcome.Turn.InvocationID
	}
	inv.lastMessage = id
	inv.nativeMessage = native
	inv.streamText = ""
	inv.streamThought = ""
	inv.completeText = ""
}

func (s *session) textLocked(inv *invocation, id, text string, thinking, complete bool) {
	if text == "" {
		return
	}
	inv.outcome.ObservedOutput = true
	kind := acpmeta.MessagePhaseUnknown
	if thinking {
		kind = acpmeta.MessagePhaseCommentary
	}
	info := acpmeta.MessageInfo{Phase: kind, Complete: complete, Source: acpmeta.MessageSourceNativeMessage}
	var update acp.SessionUpdate
	if thinking {
		update = acp.UpdateAgentThoughtText(text)
		update.AgentThoughtChunk.MessageId = &id
		update.AgentThoughtChunk.Meta = acpmeta.WithMessageInfo(nil, info)
	} else {
		update = acp.UpdateAgentMessageText(text)
		update.AgentMessageChunk.MessageId = &id
		update.AgentMessageChunk.Meta = acpmeta.WithMessageInfo(nil, info)
	}
	s.sendTurnLocked(inv, update, nil)
}

func boundedConcat(s, extra string) string {
	if len(s) > maxTextBytes || len(extra) > maxTextBytes-len(s) {
		return s
	}
	return s + extra
}

func boundedText(text string) string {
	if len(text) > maxTextBytes {
		return ""
	}
	return text
}

func (s *session) applyContextLocked(inv *invocation, m protocol.Message) {
	value := decodeOptional[struct {
		Context *struct {
			Used   *int64 `json:"total_tokens"`
			Window *int64 `json:"raw_max_tokens"`
		} `json:"context_usage"`
	}](m.RawJSON())
	if value == nil || value.Context == nil {
		return
	}
	if value.Context.Used != nil && *value.Context.Used >= 0 {
		inv.outcome.ContextUsage.UsedTokens = value.Context.Used
	}
	if value.Context.Window != nil && *value.Context.Window > 0 {
		inv.outcome.ContextUsage.WindowTokens = value.Context.Window
	}
}

// refreshContextLocked follows Claude's cheap summary control. It publishes
// session context only while the completed native epoch still owns it.
func (s *session) refreshContextLocked() {
	if !s.open || s.client.host.Observe == nil || s.closing || s.nativeQueued > 0 || len(s.sent) > 0 || s.contextReading {
		return
	}
	epoch := s.epoch
	s.contextReading = true
	s.taskLocked(func() {
		defer func() { s.mu.Lock(); s.contextReading = false; s.mu.Unlock() }()
		ctx, cancel := context.WithTimeout(s.ctx, 2*time.Second)
		defer cancel()
		request := struct {
			Subtype string `json:"subtype"`
			Detail  string `json:"detail"`
		}{Subtype: "get_context_usage", Detail: "summary"}
		check := func() error {
			if err := s.guardSessionLocked(); err != nil {
				return err
			}
			if s.epoch != epoch || s.current != nil || len(s.sent) > 0 {
				return errStale
			}
			return nil
		}
		_ = s.wire.call(ctx, request, nil, func() error {
			s.mu.Lock()
			defer s.mu.Unlock()
			return check()
		}, func(raw json.RawMessage) {
			value := decodeOptional[struct {
				Used   *int64 `json:"totalTokens"`
				Window *int64 `json:"rawMaxTokens"`
			}](raw)
			if value == nil {
				return
			}
			if value.Used != nil && *value.Used < 0 {
				value.Used = nil
			}
			if value.Window != nil && *value.Window <= 0 {
				value.Window = nil
			}
			if value.Used == nil && value.Window == nil {
				return
			}
			s.mu.Lock()
			defer s.mu.Unlock()
			if check() != nil {
				return
			}
			snapshot := agentclient.ContextUsage{UsedTokens: value.Used, WindowTokens: value.Window}
			s.sessionUpdateLocked(usageUpdate(snapshot), map[string]any{"claude.context_usage": snapshot})
		})
	})
}

func usageUpdate(ctx agentclient.ContextUsage) acp.SessionUpdate {
	if ctx.UsedTokens == nil || ctx.WindowTokens == nil {
		return metaEvent()
	}
	return acp.SessionUpdate{UsageUpdate: &acp.SessionUsageUpdate{SessionUpdate: "usage_update", Used: int(*ctx.UsedTokens), Size: int(*ctx.WindowTokens)}}
}

func promptContent(prompt []acp.ContentBlock) (json.RawMessage, error) {
	blocks := make([]protocol.ContentBlock, 0, len(prompt))
	for _, b := range prompt {
		switch {
		case b.Text != nil:
			if b.Text.Text != "" {
				blocks = append(blocks, protocol.ContentBlock{Type: "text", Text: b.Text.Text})
			}
		case b.Image != nil:
			switch b.Image.MimeType {
			case "image/jpeg", "image/png", "image/gif", "image/webp":
				if b.Image.Data == "" {
					return nil, errors.New("Claude image content is empty")
				}
				blocks = append(blocks, protocol.ContentBlock{Type: "image", Source: &protocol.ImageSource{Type: "base64", MediaType: b.Image.MimeType, Data: b.Image.Data}})
			default:
				return nil, errors.New("unsupported Claude image MIME type")
			}
		case b.ResourceLink != nil:
			blocks = append(blocks, protocol.ContentBlock{Type: "text", Text: b.ResourceLink.Name + " (" + b.ResourceLink.Uri + ")"})
		case b.Resource != nil && b.Resource.Resource.TextResourceContents != nil:
			resource := b.Resource.Resource.TextResourceContents
			blocks = append(blocks, protocol.ContentBlock{Type: "text", Text: resource.Uri + "\n" + resource.Text})
		default:
			return nil, errors.New("unsupported Claude prompt content")
		}
	}
	if len(blocks) == 0 {
		return nil, errors.New("Claude prompt is empty")
	}
	data, err := json.Marshal(blocks)
	if len(data) > maxNativeFrame-1024 {
		return nil, errors.New("Claude prompt exceeds native frame limit")
	}
	return data, err
}

// nativeFailure is immutable. Its ACP projection materializes a fresh public
// classification so callers cannot mutate a retained provider outcome.
type nativeFailure string

func (f nativeFailure) Error() string { return "Claude " + string(f) }

func (f nativeFailure) As(target any) bool {
	wire, ok := target.(**acp.RequestError)
	if !ok {
		return false
	}
	data := map[string]any{"errorKind": string(f)}
	if f == "authentication" {
		*wire = acp.NewAuthRequired(data)
	} else {
		*wire = acp.NewInternalError(data)
	}
	return true
}

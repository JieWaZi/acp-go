package claude

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	agentclient "github.com/JieWaZi/acp-go/pkg/agent_next"
	"github.com/JieWaZi/acp-go/pkg/agent_next/claude/protocol"
	acp "github.com/JieWaZi/acp-go/sdk"
)

const maxInteractionRequests = 32

type toolRecord struct {
	name      string
	completed bool
}

// ProvidesUserInput advertises the native AskUserQuestion path.
func (c *Client) ProvidesUserInput() bool { return true }

func (s *session) interactionLocked(m *protocol.ControlRequestMessage) {
	if m.RequestID == "" || len(m.RequestID) > 512 {
		return
	}
	if _, found := s.interactions[m.RequestID]; found {
		return
	}
	if len(s.interactions) >= maxInteractionRequests {
		s.faultAndStopLocked(errors.New("Claude interaction capacity exhausted"))
		return
	}
	inv := s.current
	lifetime := s.ctx
	if inv != nil {
		lifetime = inv.ctx
	}
	ctx, cancel := context.WithCancel(lifetime)
	s.interactions[m.RequestID] = cancel
	subtype := m.ControlSubtype()
	request := protocol.CanUseToolControlRequest{}
	hook := protocol.HookCallbackRequest{}
	var parseErr error
	switch subtype {
	case protocol.ControlCanUseTool:
		parseErr = m.DecodeRequest(&request)
	case "hook_callback":
		parseErr = m.DecodeRequest(&hook)
	}
	scopeOK := inv != nil && !inv.cancelRequested && !inv.terminal && s.open && !s.closing
	if _, stale := s.retiredTools[request.ToolUseID]; stale || len(s.retiredTools) >= maxRememberedIDs {
		scopeOK = false
	}
	if subtype == protocol.ControlCanUseTool && parseErr == nil && scopeOK {
		s.startToolLocked(inv, protocol.ContentBlock{Type: "tool_use", ID: request.ToolUseID, Name: request.ToolName, Input: request.Input}, nil)
	}
	scope := agentScope(inv)
	s.taskLocked(func() {
		defer cancel()
		defer func() { s.mu.Lock(); delete(s.interactions, m.RequestID); s.mu.Unlock() }()
		var result any
		var opErr error
		allow := false
		switch subtype {
		case "hook_callback":
			if parseErr != nil || s.launch.workspace == nil {
				opErr = errors.New("Claude workspace hook is unavailable")
				break
			}
			result = s.launch.workspace.hook(string(s.id), hook)
		case protocol.ControlCanUseTool:
			denied := denyTool(request.ToolUseID, "No verified live Claude execution owns this request.")
			if parseErr != nil || !scopeOK || request.ToolUseID == "" || request.ToolName == "" || len(request.Input) > maxObservationFrame {
				result = denied
				break
			}
			decision, err := s.permission(ctx, scope, request)
			if err != nil {
				s.mu.Lock()
				if !inv.terminal && inv.deliveryErr == nil {
					inv.deliveryErr = err
				}
				s.mu.Unlock()
			}
			result = decision
			allow = decision.Behavior == "allow"
		default:
			opErr = errors.New("unsupported Claude native control request")
		}
		send := func(value any, err error, guard func() error) error {
			var frame protocol.OutgoingControlResponse
			if err != nil {
				frame, err = protocol.NewControlError(m.RequestID, err.Error())
			} else {
				frame, err = protocol.NewControlSuccess(m.RequestID, value)
			}
			if err != nil {
				return err
			}
			return s.wire.write(s.ctx, frame, guard, nil)
		}
		var guard func() error
		if allow {
			guard = func() error {
				s.mu.Lock()
				defer s.mu.Unlock()
				if ctx.Err() != nil || s.current != inv || inv.terminal || inv.cancelRequested || !s.open || s.closing {
					return errStale
				}
				return nil
			}
		}
		err := send(result, opErr, guard)
		if errors.Is(err, errStale) {
			_ = send(denyTool(request.ToolUseID, "Claude tool approval expired."), nil, nil)
		}
	})
}

func agentScope(inv *invocation) agentclient.Scope {
	if inv == nil {
		return agentclient.Scope{}
	}
	return inv.scope
}

func denyTool(id, reason string) protocol.PermissionResult {
	return protocol.PermissionResult{Behavior: "deny", Message: reason, ToolUseID: id}
}

func (s *session) permission(ctx context.Context, scope agentclient.Scope, req protocol.CanUseToolControlRequest) (protocol.PermissionResult, error) {
	denied := denyTool(req.ToolUseID, "The Claude tool request was not approved.")
	if ctx.Err() != nil {
		return denied, nil
	}
	if s.client.options.Launch.Tools != nil && len(s.client.options.Launch.Tools) == 0 {
		return denied, nil
	}
	workspace := ""
	if p := s.launch.workspace; p != nil {
		workspace = p.decision(req.ToolName, req.Input)
		if workspace == "deny" {
			return denied, nil
		}
	}
	if req.ToolName == "AskUserQuestion" {
		payload, err := s.askQuestion(ctx, scope, req)
		if err != nil || payload == nil {
			return denied, err
		}
		return protocol.PermissionResult{Behavior: "allow", UpdatedInput: payload, ToolUseID: req.ToolUseID}, nil
	}
	if s.client.host.RequestPermission == nil {
		return denied, nil
	}
	options := []acp.PermissionOption{
		{OptionId: "allow_once", Name: "Allow once", Kind: acp.PermissionOptionKindAllowOnce},
		{OptionId: "reject_once", Name: "Reject", Kind: acp.PermissionOptionKindRejectOnce},
	}
	suggestions := decodeOptional[[]json.RawMessage](req.PermissionSuggestions)
	always := workspace != "ask" && !req.SuppressAlwaysAllowRule && suggestions != nil && len(*suggestions) > 0
	if always {
		options = append(options, acp.PermissionOption{OptionId: "allow_always", Name: "Always allow", Kind: acp.PermissionOptionKindAllowAlways})
	}
	title := firstString(req.Title, req.DisplayName, req.ToolName)
	kind := toolKind(req.ToolName)
	response, err := s.client.host.RequestPermission(ctx, scope, acp.RequestPermissionRequest{
		SessionId: s.id, Options: options, ToolCall: acp.ToolCallUpdate{
			ToolCallId: acp.ToolCallId(req.ToolUseID), Title: &title, Kind: &kind, Status: acp.Ptr(acp.ToolCallStatusPending),
			RawInput: jsonValue(req.Input), Locations: toolLocations(req.Input),
		},
	})
	if err != nil || response.Outcome.Selected == nil || response.Outcome.Cancelled != nil {
		return denied, err
	}
	result := protocol.PermissionResult{Behavior: "allow", UpdatedInput: req.Input, ToolUseID: req.ToolUseID}
	switch response.Outcome.Selected.OptionId {
	case "allow_once":
		return result, nil
	case "allow_always":
		if always {
			result.UpdatedPermissions = req.PermissionSuggestions
			return result, nil
		}
	}
	return denied, nil
}

type askQuestion struct {
	Question string `json:"question"`
	Header   string `json:"header"`
	Multi    bool   `json:"multiSelect"`
	Options  []struct {
		Label       string `json:"label"`
		Description string `json:"description"`
		Preview     string `json:"preview"`
	} `json:"options"`
}

func (s *session) askQuestion(ctx context.Context, scope agentclient.Scope, req protocol.CanUseToolControlRequest) (json.RawMessage, error) {
	if !s.client.forms || s.client.host.CreateElicitation == nil {
		return nil, nil
	}
	input := decodeOptional[struct {
		Questions []askQuestion `json:"questions"`
	}](req.Input)
	if input == nil || len(input.Questions) == 0 || len(input.Questions) > 4 {
		return nil, nil
	}
	properties := map[string]any{}
	for i, question := range input.Questions {
		if strings.TrimSpace(question.Question) == "" || len(question.Options) == 0 || len(question.Options) > 10 {
			return nil, nil
		}
		var options []map[string]any
		for _, opt := range question.Options {
			if opt.Label == "" {
				return nil, nil
			}
			row := map[string]any{"const": opt.Label, "title": opt.Label, "description": opt.Description}
			if opt.Preview != "" {
				row["_meta"] = map[string]any{"_claude/askUserQuestionOption": map[string]any{"preview": opt.Preview}}
			}
			options = append(options, row)
		}
		key := questionKey(i)
		field := map[string]any{"type": "string", "oneOf": options}
		if question.Multi {
			field = map[string]any{"type": "array", "items": map[string]any{"anyOf": options}}
		}
		field["title"] = firstString(question.Header, question.Question)
		if len(input.Questions) > 1 {
			field["description"] = question.Question
		}
		properties[key] = field
		properties[key+"_custom"] = map[string]any{"type": "string", "title": "Other", "_meta": map[string]any{
			"_askUserQuestionCustomAnswer": map[string]any{"questionId": key, "isCustomAnswer": true},
		}}
	}
	form := acp.NewUnstableCreateElicitationRequestForm(acp.UnstableElicitationSchema{Type: acp.UnstableElicitationSchemaTypeObject, Properties: properties})
	form.Form.Message = "Please answer the following questions."
	if len(input.Questions) == 1 {
		form.Form.Message = input.Questions[0].Question
	}
	form.Form.Meta = map[string]any{"claudeCode": map[string]any{"sessionId": s.id, "toolCallId": req.ToolUseID}}
	response, err := s.client.host.CreateElicitation(ctx, scope, form)
	if err != nil || response.Cancel != nil || response.Accept == nil && response.Decline == nil {
		return nil, err
	}
	answers := map[string]string{}
	if response.Accept != nil {
		for i, question := range input.Questions {
			key := questionKey(i)
			if custom, ok := response.Accept.Content[key+"_custom"].(string); ok && strings.TrimSpace(custom) != "" {
				answers[question.Question] = strings.TrimSpace(custom)
				continue
			}
			var values []string
			switch value := response.Accept.Content[key].(type) {
			case string:
				values = []string{value}
			case []string:
				values = value
			case []any:
				for _, v := range value {
					if text, ok := v.(string); ok {
						values = append(values, text)
					}
				}
			}
			var chosen []string
			for _, value := range values {
				for _, opt := range question.Options {
					if opt.Label == value {
						chosen = append(chosen, value)
						break
					}
				}
			}
			if len(chosen) > 0 {
				answers[question.Question] = strings.Join(chosen, ", ")
			}
		}
	}
	data := decodeOptional[map[string]any](req.Input)
	if data == nil {
		return nil, nil
	}
	(*data)["answers"] = answers
	return json.Marshal(*data)
}

func questionKey(i int) string { return fmt.Sprintf("question_%d", i+1) }

func (s *session) startToolLocked(inv *invocation, block protocol.ContentBlock, parent *string) {
	if block.ID == "" || len(block.ID) > 512 || block.Name == "" {
		return
	}
	if _, known := inv.tools[block.ID]; known {
		return
	}
	if len(inv.tools) >= maxUsageRows {
		inv.deliveryErr = errObservationBudget
		return
	}
	inv.tools[block.ID] = toolRecord{name: block.Name}
	meta := map[string]any{"claude.tool": block.Name}
	if parent != nil {
		meta["claude.parent_tool_use_id"] = *parent
	}
	s.sendTurnLocked(inv, acp.StartToolCall(acp.ToolCallId(block.ID), block.Name,
		acp.WithStartKind(toolKind(block.Name)), acp.WithStartStatus(acp.ToolCallStatusInProgress),
		acp.WithStartRawInput(jsonValue(block.Input)), acp.WithStartLocations(toolLocations(block.Input))), meta)
	if block.Name == "TodoWrite" {
		value := decodeOptional[struct {
			Todos []struct {
				Content string
				Status  string
			}
		}](block.Input)
		if value != nil {
			var rows []acp.PlanEntry
			for _, todo := range value.Todos {
				rows = append(rows, acp.PlanEntry{Content: todo.Content, Priority: acp.PlanEntryPriorityMedium, Status: planStatus(todo.Status)})
			}
			s.sendTurnLocked(inv, acp.UpdatePlan(rows...), nil)
		}
	}
}

func (s *session) toolResultsLocked(inv *invocation, m *protocol.UserMessage) {
	for _, block := range m.Message.Content {
		if block.Type != "tool_result" {
			continue
		}
		record, ok := inv.tools[block.ToolUseID]
		if !ok || record.completed {
			continue
		}
		record.completed = true
		inv.tools[block.ToolUseID] = record
		status := acp.ToolCallStatusCompleted
		if block.IsError {
			status = acp.ToolCallStatusFailed
		}
		var content []acp.ToolCallContent
		if text := decodeOptional[string](block.Content); text != nil {
			content = append(content, acp.ToolContent(acp.TextBlock(*text)))
		} else if parts := decodeOptional[[]protocol.ContentBlock](block.Content); parts != nil {
			for _, part := range *parts {
				if part.Type == "text" {
					content = append(content, acp.ToolContent(acp.TextBlock(part.Text)))
				}
			}
		}
		s.sendTurnLocked(inv, acp.UpdateToolCall(acp.ToolCallId(block.ToolUseID), acp.WithUpdateStatus(status),
			acp.WithUpdateContent(content), acp.WithUpdateRawOutput(jsonValue(firstRaw(m.ToolUseResult, block.Content)))), nil)
	}
}

func (s *session) taskUpdateLocked(inv *invocation, m *protocol.TaskMessage) {
	if m.SkipTranscript || m.TaskID == "" {
		return
	}
	id := acp.ToolCallId(m.TaskID)
	if m.ToolUseID != "" {
		id = acp.ToolCallId(m.ToolUseID)
	}
	status := acp.ToolCallStatusInProgress
	if m.Status == "completed" {
		status = acp.ToolCallStatusCompleted
	}
	if m.Status == "failed" || m.Status == "stopped" {
		status = acp.ToolCallStatusFailed
	}
	if _, exists := inv.tools[string(id)]; !exists {
		s.startToolLocked(inv, protocol.ContentBlock{ID: string(id), Name: firstString(m.Description, "Agent")}, nil)
	}
	s.sendTurnLocked(inv, acp.UpdateToolCall(id, acp.WithUpdateStatus(status),
		acp.WithUpdateContent([]acp.ToolCallContent{acp.ToolContent(acp.TextBlock(m.Summary))})), map[string]any{"claude.task_id": m.TaskID})
}

func firstRaw(values ...json.RawMessage) json.RawMessage {
	for _, value := range values {
		if len(value) > 0 {
			return value
		}
	}
	return nil
}

func jsonValue(raw json.RawMessage) any {
	if len(raw) > maxTextBytes {
		return nil
	}
	var value any
	_ = json.Unmarshal(raw, &value)
	return value
}

func toolKind(name string) acp.ToolKind {
	switch name {
	case "Read":
		return acp.ToolKindRead
	case "Edit", "MultiEdit", "Write", "NotebookEdit":
		return acp.ToolKindEdit
	case "Grep", "Glob":
		return acp.ToolKindSearch
	case "Bash":
		return acp.ToolKindExecute
	case "WebFetch", "WebSearch":
		return acp.ToolKindFetch
	case "TodoWrite", "Task", "Agent", "AskUserQuestion":
		return acp.ToolKindThink
	case "ExitPlanMode", "EnterPlanMode":
		return acp.ToolKindSwitchMode
	default:
		return acp.ToolKindOther
	}
}

func toolLocations(raw json.RawMessage) []acp.ToolCallLocation {
	payload := decodeOptional[struct {
		Path     string `json:"file_path"`
		Notebook string `json:"notebook_path"`
	}](raw)
	if payload == nil {
		return nil
	}
	if path := firstString(payload.Path, payload.Notebook); path != "" {
		return []acp.ToolCallLocation{{Path: path}}
	}
	return nil
}

func planStatus(status string) acp.PlanEntryStatus {
	switch status {
	case "completed":
		return acp.PlanEntryStatusCompleted
	case "in_progress":
		return acp.PlanEntryStatusInProgress
	default:
		return acp.PlanEntryStatusPending
	}
}

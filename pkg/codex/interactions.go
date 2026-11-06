package codex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"

	acp "github.com/JieWaZi/acp-go-sdk"
	agentclient "github.com/JieWaZi/acp-go/pkg/agentclient"
	"github.com/JieWaZi/acp-go/pkg/codex/protocol"
)

const maxNativeInteractions = 32

type urlInteraction struct {
	// thread is checked against the native resolution notification.
	thread acp.SessionId
	// scope belongs to the turn that originally produced the URL.
	scope agentclient.Scope
	// id is the host-facing URL elicitation identity.
	id acp.UnstableElicitationId
}

func (c *Client) captureInteractionLocked(frame rpcFrame) {
	var target struct {
		ThreadID acp.SessionId `json:"threadId"`
		TurnID   string        `json:"turnId"`
	}
	if json.Unmarshal(frame.Params, &target) != nil {
		return
	}
	if c.interactionCount >= maxNativeInteractions {
		c.fail(errors.New("Codex exceeded concurrent native interaction budget"))
		return
	}
	var t *turn
	if s := c.sessions[target.ThreadID]; s != nil && s.active != nil {
		owner := s.active
		if owner.writing && !owner.finishing && !owner.compact && !owner.stopped && owner.outcome.Turn.Status == "" && target.TurnID != "" {
			t = owner
			t.interactions.Add(1)
		}
	}
	c.interactionCount++
	c.interactions.Add(1)
	go func() {
		defer c.interactions.Done()
		defer func() { c.mu.Lock(); c.interactionCount--; c.mu.Unlock() }()
		if t != nil {
			defer t.interactions.Done()
		}
		c.answerInteraction(t, target.TurnID, frame)
	}()
}

func (c *Client) answerInteraction(t *turn, nativeID string, frame rpcFrame) {
	result, recognized := safeInteractionReply(frame.Method)
	allowed := false
	if t != nil {
		select {
		case <-t.ready:
		case <-t.prepare.Done():
		case <-t.native.Done():
		}
		c.mu.Lock()
		allowed = c.interactionCurrentLocked(t, nativeID)
		c.mu.Unlock()
	}
	if allowed {
		ctx, cancel := context.WithCancel(t.prepare)
		stop := context.AfterFunc(t.native, cancel)
		result = c.handleInteraction(ctx, t, frame, result)
		stop()
		cancel()
	}
	ctx, cancel := context.WithTimeout(c.life, controlTimeout)
	defer cancel()
	if !recognized {
		_, _ = c.write(ctx, rpcFrame{ID: frame.ID, Error: acp.NewMethodNotFound(frame.Method)}, nil)
		return
	}
	response, err := json.Marshal(result)
	if err != nil {
		return
	}
	admit := func() error {
		if allowed && !c.interactionCurrentLocked(t, nativeID) {
			return errTurnChanged
		}
		return nil
	}
	_, err = c.write(ctx, rpcFrame{ID: frame.ID, Result: response}, admit)
	if errors.Is(err, errTurnChanged) {
		safe, _ := safeInteractionReply(frame.Method)
		response, _ = json.Marshal(safe)
		_, _ = c.write(ctx, rpcFrame{ID: frame.ID, Result: response}, nil)
	}
}

func (c *Client) interactionCurrentLocked(t *turn, nativeID string) bool {
	return t != nil && c.activeLocked(t) && !t.stopped && !t.finishing && t.outcome.Turn.Status == "" && value(t.outcome.Turn.NativeTurnID) == nativeID
}

func safeInteractionReply(method string) (any, bool) {
	switch method {
	case protocol.MethodCommandExecutionRequestApproval, protocol.MethodFileChangeRequestApproval:
		return map[string]any{"decision": "cancel"}, true
	case protocol.MethodPermissionsRequestApproval:
		return protocol.PermissionsRequestApprovalResponse{Scope: pointer(protocol.Turn), StrictAutoReview: protocol.NewOptionalValue(true)}, true
	case protocol.MethodToolRequestUserInput:
		return protocol.ToolRequestUserInputResponse{Answers: map[string]protocol.ToolRequestUserInputAnswer{}}, true
	case protocol.MethodMCPServerElicitationRequest:
		return protocol.MCPServerElicitationRequestResponse{Action: protocol.MCPServerElicitationActionCancel}, true
	default:
		return nil, false
	}
}

func (c *Client) handleInteraction(ctx context.Context, t *turn, frame rpcFrame, fallback any) any {
	scope := agentclient.Scope{SessionID: t.session.id, HostInvocationID: t.input.HostInvocationID, NativeInvocationID: t.outcome.Turn.InvocationID}
	switch frame.Method {
	case protocol.MethodCommandExecutionRequestApproval, protocol.MethodFileChangeRequestApproval, protocol.MethodPermissionsRequestApproval:
		return c.approve(ctx, t, scope, frame, fallback)
	case protocol.MethodToolRequestUserInput:
		if c.host.CreateElicitation == nil || !c.formEnabled {
			return fallback
		}
		var native protocol.ToolRequestUserInputParams
		if json.Unmarshal(frame.Params, &native) != nil {
			return fallback
		}
		return c.requestUserInput(ctx, t, scope, native)
	case protocol.MethodMCPServerElicitationRequest:
		var native protocol.MCPServerElicitationRequestParams
		if json.Unmarshal(frame.Params, &native) != nil {
			return fallback
		}
		return c.mcpElicitation(ctx, t, scope, frame.ID, native)
	}
	return fallback
}

type approvalChoice struct {
	// option is a specific user-facing authorization.
	option acp.PermissionOption
	// reply is the exact native authorization selected by this option.
	reply any
}

func (c *Client) approve(ctx context.Context, t *turn, scope agentclient.Scope, frame rpcFrame, fallback any) any {
	if c.host.RequestPermission == nil {
		return fallback
	}
	choice := func(id, title string, kind acp.PermissionOptionKind, reply any) approvalChoice {
		return approvalChoice{option: acp.PermissionOption{OptionId: acp.PermissionOptionId(id), Name: title, Kind: kind}, reply: reply}
	}
	decision := func(value string) map[string]any { return map[string]any{"decision": value} }
	choices := []approvalChoice{
		choice("allow_once", "Allow once", acp.PermissionOptionKindAllowOnce, decision("accept")),
		choice("allow_always", "Allow for session", acp.PermissionOptionKindAllowAlways, decision("acceptForSession")),
	}
	var itemID, title string
	var native any
	kind := acp.ToolKindOther
	switch frame.Method {
	case protocol.MethodCommandExecutionRequestApproval:
		var p protocol.CommandExecutionRequestApprovalParams
		if json.Unmarshal(frame.Params, &p) != nil {
			return fallback
		}
		native, itemID, title, kind = p, p.ItemID, value(p.Command), acp.ToolKindExecute
		if len(p.ProposedExecpolicyAmendment) > 0 {
			amendment := map[string]any{"acceptWithExecpolicyAmendment": map[string]any{"execpolicy_amendment": p.ProposedExecpolicyAmendment}}
			label := "Allow and remember command pattern"
			if prefix := strings.Join(p.ProposedExecpolicyAmendment, " "); prefix != "" && !strings.ContainsAny(prefix, "\r\n") {
				label = "Allow commands starting with " + prefix
			}
			choices = append(choices, choice("accept_execpolicy_amendment", label, acp.PermissionOptionKindAllowAlways, map[string]any{"decision": amendment}))
		}
		for i, amendment := range p.ProposedNetworkPolicyAmendments {
			if amendment.Host == "" {
				continue
			}
			label, kind := "Allow access to "+amendment.Host, acp.PermissionOptionKindAllowAlways
			if amendment.Action != protocol.Allow {
				label, kind = "Block access to "+amendment.Host, acp.PermissionOptionKindRejectAlways
			}
			reply := map[string]any{"applyNetworkPolicyAmendment": map[string]any{"network_policy_amendment": amendment}}
			choices = append(choices, choice(fmt.Sprintf("apply_network_policy_amendment:%d", i), label, kind, map[string]any{"decision": reply}))
		}
		choices = append(choices, choice("reject_once", "Decline", acp.PermissionOptionKindRejectOnce, decision("decline")))
	case protocol.MethodFileChangeRequestApproval:
		var p protocol.FileChangeRequestApprovalParams
		if json.Unmarshal(frame.Params, &p) != nil {
			return fallback
		}
		native, itemID, title, kind = p, p.ItemID, "Edit files", acp.ToolKindEdit
		if root, ok := p.GrantRoot.Value(); ok {
			choices[1].option.Name = "Allow writes under " + root + " for session"
		}
		choices = append(choices, choice("reject_once", "Decline", acp.PermissionOptionKindRejectOnce, decision("decline")))
	case protocol.MethodPermissionsRequestApproval:
		var p protocol.PermissionsRequestApprovalParams
		if json.Unmarshal(frame.Params, &p) != nil {
			return fallback
		}
		native, itemID, title = p, p.ItemID, value(p.Reason)
		if title == "" {
			title = "Grant additional permissions"
		}
		grants := protocol.GrantedPermissionProfile{FileSystem: p.Permissions.FileSystem, Network: p.Permissions.Network}
		choices = []approvalChoice{
			choice("allow_permissions_turn", "Allow once", acp.PermissionOptionKindAllowOnce, protocol.PermissionsRequestApprovalResponse{Permissions: grants, Scope: pointer(protocol.Turn), StrictAutoReview: protocol.NewOptionalValue(false)}),
			choice("allow_permissions_session", "Allow for session", acp.PermissionOptionKindAllowAlways, protocol.PermissionsRequestApprovalResponse{Permissions: grants, Scope: pointer(protocol.Session), StrictAutoReview: protocol.NewOptionalValue(false)}),
			choice("reject_permissions", "Decline", acp.PermissionOptionKindRejectOnce, fallback),
		}
	}
	if itemID == "" {
		return fallback
	}
	options := make([]acp.PermissionOption, 0, len(choices))
	for _, choice := range choices {
		options = append(options, choice.option)
	}
	request := acp.RequestPermissionRequest{
		SessionId: t.session.id, Options: options, Meta: map[string]any{"codex": map[string]any{"params": native}},
		ToolCall: acp.ToolCallUpdate{ToolCallId: acp.ToolCallId(itemID), Title: pointer(title), Kind: &kind, Status: pointer(acp.ToolCallStatusPending), RawInput: native},
	}
	reply, err := c.host.RequestPermission(ctx, scope, request)
	c.recordInteractionError(t, err)
	if err != nil || reply.Outcome.Selected == nil {
		return fallback
	}
	for _, choice := range choices {
		if choice.option.OptionId == reply.Outcome.Selected.OptionId {
			return choice.reply
		}
	}
	return fallback
}

func (c *Client) requestUserInput(ctx context.Context, t *turn, scope agentclient.Scope, params protocol.ToolRequestUserInputParams) protocol.ToolRequestUserInputResponse {
	result := protocol.ToolRequestUserInputResponse{Answers: map[string]protocol.ToolRequestUserInputAnswer{}}
	fields := make(map[string]any, len(params.Questions))
	otherFields := make(map[string]string)
	for _, question := range params.Questions {
		if question.ID == "" {
			return result
		}
		field := map[string]any{"type": "string", "title": question.Header, "description": question.Question, "_meta": map[string]any{"codex": map[string]bool{"isSecret": value(question.IsSecret)}}}
		if len(question.Options) > 0 {
			choices := make([]map[string]string, 0, len(question.Options))
			for _, option := range question.Options {
				choices = append(choices, map[string]string{"const": option.Label, "title": option.Label, "description": option.Description})
			}
			field["oneOf"] = choices
			if value(question.IsOther) {
				otherFields[question.ID] = newID()
			}
		}
		fields[question.ID] = field
	}
	for question, id := range otherFields {
		fields[id] = map[string]any{"type": "string", "title": "Other", "_meta": map[string]any{"codex": map[string]any{"questionId": question, "isOtherAnswer": true}}}
	}
	request := acp.NewUnstableCreateElicitationRequestForm(acp.UnstableElicitationSchema{Type: acp.UnstableElicitationSchemaTypeObject, Properties: fields})
	request.Form.Message = "Input requested"
	request.Form.Meta = map[string]any{"codex": map[string]any{"sessionId": params.ThreadID, "toolCallId": params.ItemID}}
	response, err := c.host.CreateElicitation(ctx, scope, request)
	c.recordInteractionError(t, err)
	if err != nil || response.Accept == nil {
		return result
	}
	for _, question := range params.Questions {
		answer, ok := response.Accept.Content[question.ID].(string)
		if other := otherFields[question.ID]; other != "" {
			if text, provided := response.Accept.Content[other].(string); provided && text != "" {
				answer, ok = text, true
			}
		}
		if ok {
			result.Answers[question.ID] = protocol.ToolRequestUserInputAnswer{Answers: []string{answer}}
		}
	}
	return result
}

func (c *Client) mcpElicitation(ctx context.Context, t *turn, scope agentclient.Scope, rpcID json.RawMessage, p protocol.MCPServerElicitationRequestParams) protocol.MCPServerElicitationRequestResponse {
	result := protocol.MCPServerElicitationRequestResponse{Action: protocol.MCPServerElicitationActionCancel}
	meta := map[string]any{}
	_ = json.Unmarshal(p.Meta, &meta)
	if meta == nil {
		meta = map[string]any{}
	}
	meta["codex"] = map[string]any{"sessionId": p.ThreadID, "serverName": p.ServerName, "turnId": p.TurnID}
	var request acp.UnstableCreateElicitationRequest
	switch p.Mode {
	case protocol.URL:
		target, err := url.Parse(value(p.URL))
		if value(p.ElicitationID) == "" || err != nil || target.Scheme != "https" && target.Scheme != "http" || target.Hostname() == "" || target.User != nil {
			return result
		}
		if !c.urlEnabled {
			return c.mcpPermission(ctx, t, scope, p)
		}
		request = acp.NewUnstableCreateElicitationRequestUrl(acp.UnstableElicitationId(*p.ElicitationID), *p.URL)
		request.Url.Message, request.Url.Meta = p.Message, meta
	case protocol.Form, protocol.OpenaiForm:
		var schema acp.UnstableElicitationSchema
		if json.Unmarshal(p.RequestedSchema, &schema) != nil || schema.Type != acp.UnstableElicitationSchemaTypeObject {
			return result
		}
		if !c.formEnabled {
			return c.mcpPermission(ctx, t, scope, p)
		}
		request = acp.NewUnstableCreateElicitationRequestForm(schema)
		request.Form.Message, request.Form.Meta = p.Message, meta
	default:
		return result
	}
	response, err := c.host.CreateElicitation(ctx, scope, request)
	c.recordInteractionError(t, err)
	if err != nil {
		return result
	}
	if response.Decline != nil {
		result.Action = protocol.MCPServerElicitationActionDecline
	}
	if response.Accept != nil {
		result.Action = protocol.MCPServerElicitationActionAccept
		result.Content, _ = json.Marshal(response.Accept.Content)
		result.Meta, _ = json.Marshal(response.Accept.Meta)
		if request.Url != nil {
			c.mu.Lock()
			if c.interactionCurrentLocked(t, value(p.TurnID)) && len(c.urls) < maxNativeInteractions {
				if c.urls == nil {
					c.urls = make(map[string]urlInteraction)
				}
				c.urls[string(rpcID)] = urlInteraction{thread: t.session.id, scope: scope, id: request.Url.ElicitationId}
			} else {
				result.Action = protocol.MCPServerElicitationActionCancel
			}
			c.mu.Unlock()
		}
	}
	return result
}

func (c *Client) mcpPermission(ctx context.Context, t *turn, scope agentclient.Scope, p protocol.MCPServerElicitationRequestParams) protocol.MCPServerElicitationRequestResponse {
	result := protocol.MCPServerElicitationRequestResponse{Action: protocol.MCPServerElicitationActionCancel}
	if c.host.RequestPermission == nil {
		return result
	}
	id := value(p.ElicitationID)
	if id == "" {
		id = p.ServerName
	}
	request := acp.RequestPermissionRequest{SessionId: t.session.id,
		ToolCall: acp.ToolCallUpdate{ToolCallId: acp.ToolCallId("elicitation-" + id), Title: pointer("MCP " + p.ServerName + " requests input"),
			Kind: pointer(acp.ToolKindOther), Status: pointer(acp.ToolCallStatusPending), RawInput: p, Content: []acp.ToolCallContent{acp.ToolContent(acp.TextBlock(p.Message))}},
		Options: []acp.PermissionOption{
			{OptionId: "accept", Name: "Allow once", Kind: acp.PermissionOptionKindAllowOnce},
			{OptionId: "accept_session", Name: "Allow for session", Kind: acp.PermissionOptionKindAllowAlways},
			{OptionId: "accept_always", Name: "Always allow", Kind: acp.PermissionOptionKindAllowAlways},
			{OptionId: "decline", Name: "Decline", Kind: acp.PermissionOptionKindRejectOnce},
		},
	}
	reply, err := c.host.RequestPermission(ctx, scope, request)
	c.recordInteractionError(t, err)
	if err != nil || reply.Outcome.Selected == nil {
		return result
	}
	switch reply.Outcome.Selected.OptionId {
	case "accept":
		result.Action = protocol.MCPServerElicitationActionAccept
	case "accept_session", "accept_always":
		result.Action = protocol.MCPServerElicitationActionAccept
		result.Meta, _ = json.Marshal(map[string]string{"persist": strings.TrimPrefix(string(reply.Outcome.Selected.OptionId), "accept_")})
	case "decline":
		result.Action = protocol.MCPServerElicitationActionDecline
	}
	return result
}

func (c *Client) recordInteractionError(t *turn, err error) {
	if err == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if t.outcome.DeliveryError == nil {
		t.outcome.DeliveryError = err
	}
}

func (c *Client) completeURLLocked(raw json.RawMessage) {
	var event struct {
		RequestID json.RawMessage `json:"requestId"`
		ThreadID  acp.SessionId   `json:"threadId"`
	}
	if json.Unmarshal(raw, &event) != nil {
		return
	}
	pending, ok := c.urls[string(event.RequestID)]
	if !ok || pending.thread != event.ThreadID {
		return
	}
	delete(c.urls, string(event.RequestID))
	if c.host.CompleteElicitation != nil {
		c.interactions.Add(1)
		go func() {
			defer c.interactions.Done()
			ctx, cancel := context.WithTimeout(c.life, controlTimeout)
			defer cancel()
			_ = c.host.CompleteElicitation(ctx, pending.scope, acp.UnstableCompleteElicitationNotification{ElicitationId: pending.id})
		}()
	}
}

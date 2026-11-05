package pi

import (
	"context"
	"strconv"
	"strings"

	agentclient "github.com/JieWaZi/acp-go/pkg/agentclient"
	acp "github.com/JieWaZi/acp-go/sdk"
)

type uiRequest struct {
	// bytes is the record size charged against the pending event budget.
	bytes int
	// ID correlates the extension's dialog and reply.
	ID string `json:"id"`
	// Method selects the native dialog or status operation.
	Method string `json:"method"`
	// Title is the heading supplied by the extension.
	Title string `json:"title"`
	// Message gives the human-readable dialog body.
	Message string `json:"message"`
	// Options contains the exact values offered by a select dialog.
	Options []string `json:"options"`
	// Prefill is the starting value of an input dialog.
	Prefill string `json:"prefill"`
	// InitialText is the starting value of an editor dialog.
	InitialText string `json:"initialText"`
	// StatusKey identifies an extension status channel.
	StatusKey string `json:"statusKey"`
	// StatusText holds the value emitted on that channel.
	StatusText string `json:"statusText"`
}

func (u uiRequest) interactive() bool {
	switch u.Method {
	case "confirm", "select", "input", "editor":
		return true
	}
	return false
}

func (s *session) handleUI(ctx context.Context, scope agentclient.Scope, ui uiRequest) {
	reply := map[string]any{"type": "extension_ui_response", "id": ui.ID, "cancelled": true}
	if ctx.Err() == nil {
		switch {
		case ui.Method == "confirm" || ui.isMCPApproval():
			s.permission(ctx, scope, ui, reply)
		default:
			s.elicit(ctx, scope, ui, reply)
		}
	}
	if ctx.Err() != nil {
		delete(reply, "value")
		delete(reply, "confirmed")
		reply["cancelled"] = true
	}
	send, cancel := context.WithTimeout(context.Background(), controlTimeout)
	defer cancel()
	_, _ = s.rpc.write(send, reply, nil)
}

func (u uiRequest) isMCPApproval() bool {
	return u.Method == "select" && strings.HasPrefix(u.Title, "MCP: ") && len(u.Options) == 3 && u.Options[0] == "Allow once" && u.Options[1] == "Allow for session" && u.Options[2] == "Deny"
}

func (s *session) permission(ctx context.Context, scope agentclient.Scope, ui uiRequest, reply map[string]any) {
	host := s.client.host.RequestPermission
	if host == nil {
		return
	}
	options := []acp.PermissionOption{
		{OptionId: "yes", Name: "Allow", Kind: acp.PermissionOptionKindAllowOnce},
		{OptionId: "no", Name: "Deny", Kind: acp.PermissionOptionKindRejectOnce},
	}
	if ui.isMCPApproval() {
		options = []acp.PermissionOption{
			{OptionId: "0", Name: ui.Options[0], Kind: acp.PermissionOptionKindAllowOnce},
			{OptionId: "1", Name: ui.Options[1], Kind: acp.PermissionOptionKindAllowAlways},
			{OptionId: "2", Name: ui.Options[2], Kind: acp.PermissionOptionKindRejectOnce},
		}
	}
	result, err := host(ctx, scope, acp.RequestPermissionRequest{
		SessionId: s.id, Options: options, ToolCall: acp.ToolCallUpdate{
			ToolCallId: acp.ToolCallId("pi-ui-" + ui.ID), Title: pointer(ui.Title),
			Content: []acp.ToolCallContent{acp.ToolContent(acp.TextBlock(ui.Message))},
		},
	})
	if err != nil || result.Outcome.Selected == nil {
		return
	}
	for index, option := range options {
		if option.OptionId != result.Outcome.Selected.OptionId {
			continue
		}
		delete(reply, "cancelled")
		if ui.Method == "confirm" {
			reply["confirmed"] = option.OptionId == "yes"
		} else {
			reply["value"] = ui.Options[index]
		}
		return
	}
}

func (s *session) elicit(ctx context.Context, scope agentclient.Scope, ui uiRequest, reply map[string]any) {
	host := s.client.host.CreateElicitation
	if host == nil {
		return
	}
	property := map[string]any{"type": "string", "title": ui.Title}
	if ui.Prefill != "" {
		property["default"] = ui.Prefill
	}
	if ui.InitialText != "" {
		property["default"] = ui.InitialText
	}
	if ui.Method == "select" {
		values := make([]string, 0, len(ui.Options))
		for index := range ui.Options {
			values = append(values, strconv.Itoa(index))
		}
		property["enum"], property["enumNames"] = values, ui.Options
	}
	result, err := host(ctx, scope, acp.UnstableCreateElicitationRequest{Form: &acp.UnstableCreateElicitationForm{
		Mode: "form", Message: ui.Title, Meta: map[string]any{"sessionId": s.id},
		RequestedSchema: acp.UnstableElicitationSchema{Properties: map[string]any{"value": property}, Required: []string{"value"}},
	}})
	if err != nil || result.Accept == nil {
		return
	}
	value, ok := result.Accept.Content["value"].(string)
	if !ok {
		return
	}
	if ui.Method == "select" {
		index, err := strconv.Atoi(value)
		if err != nil || index < 0 || index >= len(ui.Options) {
			return
		}
		value = ui.Options[index]
	}
	delete(reply, "cancelled")
	reply["value"] = value
}

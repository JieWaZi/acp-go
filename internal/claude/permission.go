package claude

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"acp-go/agents/claude/protocol"
	acp "github.com/coder/acp-go-sdk"
)

const (
	// permissionAllowOnce 是仅允许当前工具调用的选择。
	permissionAllowOnce acp.PermissionOptionId = "allow_once"
	// permissionAllowAlways 是应用 CLI 建议规则并允许当前调用的选择。
	permissionAllowAlways acp.PermissionOptionId = "allow_always"
	// permissionRejectOnce 是拒绝当前工具调用的选择。
	permissionRejectOnce acp.PermissionOptionId = "reject_once"
)

// handleControlRequest 只处理权限询问，未知控制请求一律失败关闭。
func (s *claudeSession) handleControlRequest(ctx context.Context, message *protocol.ControlRequestMessage) (any, error) {
	if message.ControlSubtype() != protocol.ControlCanUseTool {
		return nil, fmt.Errorf("unsupported Claude control request %q", message.ControlSubtype())
	}
	var request protocol.CanUseToolControlRequest
	if err := message.DecodeRequest(&request); err != nil {
		return permissionDenied("invalid permission request", ""), nil
	}
	if request.ToolName == "" || request.ToolUseID == "" {
		return permissionDenied("invalid permission request", request.ToolUseID), nil
	}
	return s.requestToolPermission(ctx, request)
}

// requestToolPermission 发布工具卡片、请求客户端选择，并严格校验返回选项。
func (s *claudeSession) requestToolPermission(ctx context.Context, request protocol.CanUseToolControlRequest) (protocol.PermissionResult, error) {
	block := protocol.ContentBlock{Type: "tool_use", ID: request.ToolUseID, Name: request.ToolName, Input: request.Input}
	if err := s.startTool(ctx, block); err != nil && !errors.Is(err, ErrClaudeConnectionNotReady) {
		return permissionDenied("permission request could not be displayed", request.ToolUseID), nil
	}
	title, kind, locations := describeTool(request.ToolName, decodeJSONValue(request.Input))
	if request.Title != "" {
		title = request.Title
	}
	options := []acp.PermissionOption{
		{OptionId: permissionAllowOnce, Name: "Allow once", Kind: acp.PermissionOptionKindAllowOnce},
		{OptionId: permissionRejectOnce, Name: "Reject", Kind: acp.PermissionOptionKindRejectOnce},
	}
	canAlwaysAllow := !request.SuppressAlwaysAllowRule && hasPermissionSuggestions(request.PermissionSuggestions)
	if canAlwaysAllow {
		options = append(options, acp.PermissionOption{
			OptionId: permissionAllowAlways, Name: "Always allow", Kind: acp.PermissionOptionKindAllowAlways,
		})
	}
	status := acp.ToolCallStatusPending
	response, err := s.agent.requestPermission(ctx, acp.RequestPermissionRequest{
		SessionId: acp.SessionId(s.id), Options: options,
		ToolCall: acp.ToolCallUpdate{
			ToolCallId: acp.ToolCallId(request.ToolUseID), Title: &title, Kind: &kind,
			Locations: locations, RawInput: decodeJSONValue(request.Input), Status: &status,
		},
	})
	if err != nil {
		return permissionDenied("permission request failed", request.ToolUseID), nil
	}

	// 客户端返回后再次验证 Session 与活动 turn，关闭/取消期间的迟到允许必须转为拒绝。
	s.mu.Lock()
	closed := s.closed
	active := s.active
	s.mu.Unlock()
	current, currentOK := s.agent.sessions.get(s.id)
	if closed || active == nil || !currentOK || current != s {
		return permissionDenied("permission request is no longer active", request.ToolUseID), nil
	}
	active.mu.Lock()
	cancelled := active.cancelled
	active.mu.Unlock()
	select {
	case <-active.drained:
		return permissionDenied("permission request is no longer active", request.ToolUseID), nil
	default:
	}
	if cancelled {
		return permissionDenied("permission request cancelled", request.ToolUseID), nil
	}
	if response.Outcome.Cancelled != nil {
		return permissionDenied("permission request cancelled", request.ToolUseID), nil
	}
	if response.Outcome.Selected == nil {
		return permissionDenied("invalid permission response", request.ToolUseID), nil
	}
	switch response.Outcome.Selected.OptionId {
	case permissionAllowOnce:
		return protocol.PermissionResult{
			Behavior: "allow", UpdatedInput: cloneRaw(request.Input), ToolUseID: request.ToolUseID,
		}, nil
	case permissionAllowAlways:
		if !canAlwaysAllow {
			return permissionDenied("invalid permission option", request.ToolUseID), nil
		}
		return protocol.PermissionResult{
			Behavior: "allow", UpdatedInput: cloneRaw(request.Input),
			UpdatedPermissions: cloneRaw(request.PermissionSuggestions), ToolUseID: request.ToolUseID,
		}, nil
	case permissionRejectOnce:
		return permissionDenied("user denied permission", request.ToolUseID), nil
	default:
		return permissionDenied("unknown permission option", request.ToolUseID), nil
	}
}

// hasPermissionSuggestions 只接受至少包含一项的 JSON 数组，避免畸形建议生成永久授权选项。
func hasPermissionSuggestions(value json.RawMessage) bool {
	var suggestions []json.RawMessage
	return json.Unmarshal(value, &suggestions) == nil && len(suggestions) > 0
}

// permissionDenied 创建不扩大权限的稳定拒绝结果。
func permissionDenied(message, toolUseID string) protocol.PermissionResult {
	return protocol.PermissionResult{Behavior: "deny", Message: message, ToolUseID: toolUseID}
}

// cloneRaw 复制开放 JSON，避免控制回调结束后共享可变底层字节。
func cloneRaw(value json.RawMessage) json.RawMessage {
	return append(json.RawMessage(nil), value...)
}

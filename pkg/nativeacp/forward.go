package nativeacp

import (
	"context"
	"encoding/json"

	acp "github.com/coder/acp-go-sdk"
)

// Authenticate 原样转发标准 ACP 请求。
func (agent *Agent) Authenticate(ctx context.Context, request acp.AuthenticateRequest) (acp.AuthenticateResponse, error) {
	return acp.SendRequest[acp.AuthenticateResponse](agent.conn, ctx, "authenticate", request)
}

// Logout 原样转发标准 ACP 请求。
func (agent *Agent) Logout(ctx context.Context, request acp.LogoutRequest) (acp.LogoutResponse, error) {
	return acp.SendRequest[acp.LogoutResponse](agent.conn, ctx, "logout", request)
}

// ListSessions 原样转发标准 ACP 请求。
func (agent *Agent) ListSessions(ctx context.Context, request acp.ListSessionsRequest) (acp.ListSessionsResponse, error) {
	return acp.SendRequest[acp.ListSessionsResponse](agent.conn, ctx, "session/list", request)
}

// SetSessionMode 原样转发标准 ACP 请求。
func (agent *Agent) SetSessionMode(ctx context.Context, request acp.SetSessionModeRequest) (acp.SetSessionModeResponse, error) {
	return acp.SendRequest[acp.SetSessionModeResponse](agent.conn, ctx, "session/set_mode", request)
}

// dispatch 将上游的标准回调交给宿主 SDK，并处理已知扩展。
func (agent *Agent) dispatch(ctx context.Context, method string, params json.RawMessage) (any, error) {
	switch method {
	case "fs/read_text_file":
		return decodeCall(ctx, params, agent.host.ReadTextFile)
	case "fs/write_text_file":
		return decodeCall(ctx, params, agent.host.WriteTextFile)
	case "session/request_permission":
		return decodeCall(ctx, params, agent.requestPermission)
	case "terminal/create":
		return decodeCall(ctx, params, agent.host.CreateTerminal)
	case "terminal/kill":
		return decodeCall(ctx, params, agent.host.KillTerminal)
	case "terminal/output":
		return decodeCall(ctx, params, agent.host.TerminalOutput)
	case "terminal/release":
		return decodeCall(ctx, params, agent.host.ReleaseTerminal)
	case "terminal/wait_for_exit":
		return decodeCall(ctx, params, agent.host.WaitForTerminalExit)
	case "elicitation/create":
		return agent.elicitation(ctx, params)
	case "mcp/connect":
		return decodeCall(ctx, params, agent.host.UnstableConnectMcp)
	case "mcp/disconnect":
		return decodeCall(ctx, params, agent.host.UnstableDisconnectMcp)
	case "elicitation/complete":
		var request acp.UnstableCompleteElicitationNotification
		if err := json.Unmarshal(params, &request); err != nil {
			return nil, acp.NewInvalidParams(nil)
		}
		return nil, agent.host.UnstableCompleteElicitation(ctx, request)
	case "session/update":
		var request acp.SessionNotification
		if err := json.Unmarshal(params, &request); err != nil {
			return nil, acp.NewInvalidParams(nil)
		}
		agent.normalizeUpdate(&request)
		return nil, agent.host.SessionUpdate(ctx, request)
	case "cursor/ask_question", "cursor/create_plan":
		if agent.config.CursorExtensions {
			return agent.cursorInteraction(ctx, method, params)
		}
	case "cursor/update_todos", "cursor/task", "cursor/generate_image":
		if agent.config.CursorExtensions {
			return nil, agent.cursorNotification(ctx, method, params)
		}
	}
	if len(method) > 0 && method[0] == '_' {
		return agent.host.CallExtension(ctx, method, params)
	}
	return nil, acp.NewMethodNotFound(method)
}

// elicitation 将原生 ACP 顶层会话归属保存在宿主 SDK 支持的元数据中。
func (agent *Agent) elicitation(ctx context.Context, params json.RawMessage) (acp.UnstableCreateElicitationResponse, error) {
	var fields map[string]json.RawMessage
	if json.Unmarshal(params, &fields) != nil {
		return acp.UnstableCreateElicitationResponse{}, acp.NewInvalidParams(nil)
	}
	meta := map[string]any{}
	if raw := fields["_meta"]; len(raw) != 0 && json.Unmarshal(raw, &meta) != nil {
		return acp.UnstableCreateElicitationResponse{}, acp.NewInvalidParams(nil)
	}
	if meta == nil {
		meta = map[string]any{}
	}
	for _, key := range []string{"sessionId", "toolCallId"} {
		if value, exists := meta[key]; exists {
			if _, ok := value.(string); !ok {
				return acp.UnstableCreateElicitationResponse{}, acp.NewInvalidParams(nil)
			}
		}
		if raw := fields[key]; len(raw) != 0 {
			var value string
			if json.Unmarshal(raw, &value) != nil {
				return acp.UnstableCreateElicitationResponse{}, acp.NewInvalidParams(nil)
			}
			if previous, exists := meta[key]; exists && previous != value {
				return acp.UnstableCreateElicitationResponse{}, acp.NewInvalidParams(nil)
			}
			meta[key] = value
		}
	}
	sessionID, _ := meta["sessionId"].(string)
	toolID, _ := meta["toolCallId"].(string)
	sid := agent.interactionSession(toolID, sessionID)
	if sid == "" {
		return acp.NewUnstableCreateElicitationResponseCancel(), nil
	}
	meta["sessionId"] = sid
	fields["_meta"], _ = json.Marshal(meta)
	normalized, err := json.Marshal(fields)
	if err != nil {
		return acp.UnstableCreateElicitationResponse{}, err
	}
	var request acp.UnstableCreateElicitationRequest
	if err := json.Unmarshal(normalized, &request); err != nil {
		return acp.UnstableCreateElicitationResponse{}, acp.NewInvalidParams(nil)
	}
	return agent.host.UnstableCreateElicitation(ctx, request)
}

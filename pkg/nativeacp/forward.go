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
		return decodeCall(ctx, params, agent.host.RequestPermission)
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
		return decodeCall(ctx, params, agent.host.UnstableCreateElicitation)
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

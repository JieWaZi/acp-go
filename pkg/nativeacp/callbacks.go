package nativeacp

import (
	"context"
	"encoding/json"

	acp "github.com/JieWaZi/acp-go/sdk"
)

// CallbackBridge exposes only the host operations required by native callbacks.
type CallbackBridge interface {
	// ResolveInteractionSession resolves explicit or tool-bound session ownership.
	ResolveInteractionSession(toolCallID, sessionID string) acp.SessionId
	// UpdateSession publishes a standard host notification.
	UpdateSession(context.Context, acp.SessionNotification) error
	// RequestPermission requests standard host approval.
	RequestPermission(
		context.Context,
		acp.RequestPermissionRequest,
	) (acp.RequestPermissionResponse, error)
	// CreateElicitation requests structured user input from the host.
	CreateElicitation(
		context.Context,
		acp.UnstableCreateElicitationRequest,
	) (acp.UnstableCreateElicitationResponse, error)
}

// CallbackAdapter keeps private reverse protocols within their owning adapter.
type CallbackAdapter interface {
	// PrepareInitialize declares client capabilities needed by the private protocol.
	PrepareInitialize(*acp.InitializeRequest)
	// HandleCallback returns handled=false to continue standard dispatch.
	HandleCallback(
		context.Context,
		CallbackBridge,
		string,
		json.RawMessage,
	) (result any, handled bool, err error)
}

// SessionBridge exposes native requests needed by configuration adapters.
type SessionBridge interface {
	// SendRequest decodes the native JSON result into response.
	SendRequest(context.Context, string, any, any) error
}

// SessionSnapshot retains configuration facts returned by an opened native session.
type SessionSnapshot struct {
	// ConfigOptions contains the standard ACP configuration catalog.
	ConfigOptions []acp.SessionConfigOption
	// Modes contains the standard session mode catalog.
	Modes *acp.SessionModeState
	// Models retains legacy native model metadata.
	Models json.RawMessage
	// WorkingDirectory is the session cwd supplied by the host.
	WorkingDirectory string
}

// SessionAdapter keeps nonstandard configuration within its owning adapter.
type SessionAdapter interface {
	// NormalizeSession records native configuration and returns normalized host options.
	NormalizeSession(
		context.Context,
		SessionBridge,
		acp.SessionId,
		SessionSnapshot,
	) ([]acp.SessionConfigOption, error)
	// NormalizeUpdate records dynamic native configuration and returns normalized options.
	NormalizeUpdate(acp.SessionId, []acp.SessionConfigOption) []acp.SessionConfigOption
	// SetConfigOption returns handled=false to use standard configuration dispatch.
	SetConfigOption(
		context.Context,
		SessionBridge,
		acp.SetSessionConfigOptionRequest,
	) (response acp.SetSessionConfigOptionResponse, handled bool, err error)
	// ForgetSession discards private configuration when a session closes.
	ForgetSession(acp.SessionId)
}

// PermissionEvidence contains verifiable evidence for the current execution.
type PermissionEvidence struct {
	// Request is the native standard permission request.
	Request acp.RequestPermissionRequest
	// Detail accumulates prior native tool title, kind, input and content.
	Detail acp.ToolCallUpdate
	// Prompt contains the current operation's complete user input.
	Prompt []acp.ContentBlock
	// ToolOwner is the session identity observed for this tool.
	ToolOwner acp.SessionId
	// Active reports whether the request still belongs to an admitted execution.
	Active bool
	// unchanged rechecks exact turn, session and tool evidence identities under the owner lock.
	unchanged func() bool
}

// PermissionBridge exposes only host notification delivery for approval adapters.
type PermissionBridge interface {
	// UpdateSession publishes a standard host notification.
	UpdateSession(context.Context, acp.SessionNotification) error
}

// PermissionAdapter keeps private approval policy within its owning adapter.
type PermissionAdapter interface {
	// Review returns handled=false to continue standard host approval.
	Review(
		context.Context,
		PermissionBridge,
		PermissionEvidence,
	) (response acp.RequestPermissionResponse, handled bool, err error)
}

// ResolveInteractionSession implements CallbackBridge without exposing mutable owner state.
func (agent *Agent) ResolveInteractionSession(toolCallID, sessionID string) acp.SessionId {
	return agent.interactionSession(toolCallID, sessionID)
}

// ResolveInteractionContext freezes identity and context together, preventing stale callback rebinding.
func (agent *Agent) ResolveInteractionContext(toolCallID, sessionID string) (acp.SessionId, context.Context) {
	agent.mutex.Lock()
	defer agent.mutex.Unlock()
	sid := agent.interactionSessionLocked(toolCallID, sessionID)
	if sid == "" {
		return "", nil
	}
	return sid, agent.turnOwners[sid].context
}

// UpdateSession forwards through the bound host connection.
func (agent *Agent) UpdateSession(ctx context.Context, request acp.SessionNotification) error {
	request.SessionId = agent.publicSessionID(request.SessionId)
	return agent.host.SessionUpdate(ctx, request)
}

// RequestPermission forwards through the bound host connection.
func (agent *Agent) RequestPermission(
	ctx context.Context,
	request acp.RequestPermissionRequest,
) (acp.RequestPermissionResponse, error) {
	request.SessionId = agent.publicSessionID(request.SessionId)
	return agent.host.RequestPermission(ctx, request)
}

// CreateElicitation forwards through the bound host connection.
func (agent *Agent) CreateElicitation(
	ctx context.Context,
	request acp.UnstableCreateElicitationRequest,
) (acp.UnstableCreateElicitationResponse, error) {
	if agent.config.PublicSessionID == nil {
		return agent.host.UnstableCreateElicitation(ctx, request)
	}
	data, err := json.Marshal(request)
	if err != nil {
		return acp.UnstableCreateElicitationResponse{}, err
	}
	if err := json.Unmarshal(agent.publicCallback(data), &request); err != nil {
		return acp.UnstableCreateElicitationResponse{}, err
	}
	return agent.host.UnstableCreateElicitation(ctx, request)
}

// SendRequest implements SessionBridge while nativeacp retains protocol ownership.
func (agent *Agent) SendRequest(ctx context.Context, method string, request any, response any) error {
	raw, err := agent.CallNative(ctx, method, request)
	if err != nil {
		return err
	}
	if response == nil {
		return nil
	}
	return json.Unmarshal(raw, response)
}

// CallNative exposes a narrow native request with original SDK results and protocol errors.
func (agent *Agent) CallNative(ctx context.Context, method string, request any) (json.RawMessage, error) {
	return sendNativeRequest[json.RawMessage](agent, ctx, method, request)
}

// publicSessionID maps host-visible identity only when explicitly configured.
func (agent *Agent) publicSessionID(id acp.SessionId) acp.SessionId {
	if agent.config.PublicSessionID == nil {
		return id
	}
	return agent.config.PublicSessionID(id)
}

// publicCallback maps top-level and elicitation identities without rewriting tool inputs.
func (agent *Agent) publicCallback(data json.RawMessage) json.RawMessage {
	if agent.config.PublicSessionID == nil {
		return data
	}
	fields := map[string]json.RawMessage{}
	if json.Unmarshal(data, &fields) != nil {
		return data
	}
	if raw, ok := fields["sessionId"]; ok {
		var id acp.SessionId
		if json.Unmarshal(raw, &id) == nil {
			fields["sessionId"], _ = json.Marshal(agent.publicSessionID(id))
		}
	}
	if raw, ok := fields["_meta"]; ok {
		fields["_meta"] = agent.publicCallback(raw)
	}
	result, err := json.Marshal(fields)
	if err != nil {
		return data
	}
	return result
}

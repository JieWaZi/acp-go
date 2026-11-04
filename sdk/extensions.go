package acp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// ExtensionMethodHandler can be implemented by either an Agent or a Client.
//
// ACP extension methods are JSON-RPC methods whose names begin with "_".
// They provide a stable namespace for custom functionality that is not part
// of the core ACP spec.
//
// If the method is unrecognized, implementations should return NewMethodNotFound(method).
//
// See: https://agentclientprotocol.com/protocol/extensibility#extension-methods
type ExtensionMethodHandler interface {
	HandleExtensionMethod(ctx context.Context, method string, params json.RawMessage) (any, error)
}

func validateExtensionMethodName(method string) error {
	if method == "" {
		return fmt.Errorf("extension method name must be non-empty")
	}
	if !strings.HasPrefix(method, "_") {
		return fmt.Errorf("extension method name must start with '_' (got %q)", method)
	}
	return nil
}

func isExtensionMethodName(method string) bool {
	return strings.HasPrefix(method, "_")
}

func (a *AgentSideConnection) prepareSessionInbound(ctx context.Context, msg *anyMessage) inboundContinuation {
	if msg.Method != AgentMethodSessionPrompt && msg.Method != AgentMethodSessionCancel {
		return nil
	}
	a.mu.Lock()
	admission, capture := a.admission, a.promptPreparation
	a.mu.Unlock()
	if admission.Prompt == nil {
		return nil
	}
	if msg.JSONRPC != "2.0" {
		return completeInbound(NewInvalidRequest(nil))
	}
	switch msg.Method {
	case AgentMethodSessionPrompt:
		if msg.ID == nil {
			return completeInbound(NewInvalidRequest(nil))
		}
		var request PromptRequest
		if err := decodeSessionParams(msg.Params, &request, &request.SessionId); err != nil {
			return completeInbound(err)
		}
		var preparation PromptPreparation
		if capture != nil {
			preparation = capture(request)
		}
		run, err := admission.Prompt(ctx, request, preparation)
		if err != nil {
			return completeInbound(toReqErr(err))
		}
		if run == nil {
			return completeInbound(NewInternalError(nil))
		}
		return func(context.Context) (any, *RequestError) {
			response, err := run()
			return response, toReqErr(err)
		}
	case AgentMethodSessionCancel:
		if msg.ID != nil {
			return completeInbound(NewInvalidRequest(nil))
		}
		var notification CancelNotification
		if err := decodeSessionParams(msg.Params, &notification, &notification.SessionId); err != nil {
			return completeInbound(err)
		}
		run, err := admission.Cancel(notification)
		if err != nil || run == nil {
			return completeInbound(toReqErr(err))
		}
		return func(ctx context.Context) (any, *RequestError) {
			return nil, toReqErr(run(ctx))
		}
	}
	return nil
}

func decodeSessionParams(params json.RawMessage, value interface{ Validate() error }, sessionID *SessionId) *RequestError {
	if err := json.Unmarshal(params, value); err != nil {
		return NewInvalidParams(map[string]any{"error": err.Error()})
	}
	if *sessionID == "" {
		return NewInvalidParams(map[string]any{"error": "sessionId is required"})
	}
	if err := value.Validate(); err != nil {
		return NewInvalidParams(map[string]any{"error": err.Error()})
	}
	return nil
}

// A non-nil continuation preserves the existing response and logging path,
// including a bound cancel that was fully handled at ingress.
func completeInbound(err *RequestError) inboundContinuation {
	return func(context.Context) (any, *RequestError) { return nil, err }
}

func (a *AgentSideConnection) handleWithExtensions(ctx context.Context, method string, params json.RawMessage) (any, *RequestError) {
	if isExtensionMethodName(method) {
		h, ok := a.agent.(ExtensionMethodHandler)
		if !ok {
			return nil, NewMethodNotFound(method)
		}
		resp, err := h.HandleExtensionMethod(ctx, method, params)
		if err != nil {
			return nil, toReqErr(err)
		}
		return resp, nil
	}

	if owner, ok := a.agent.(SessionLifecycleOwner); ok && owner.ManagesSessionLifecycle() {
		switch method {
		case AgentMethodSessionPrompt:
			var request PromptRequest
			if err := json.Unmarshal(params, &request); err != nil {
				return nil, NewInvalidParams(map[string]any{"error": err.Error()})
			}
			if err := request.Validate(); err != nil {
				return nil, NewInvalidParams(map[string]any{"error": err.Error()})
			}
			response, err := a.agent.Prompt(ctx, request)
			return response, toReqErr(err)
		case AgentMethodSessionCancel:
			var notification CancelNotification
			if err := json.Unmarshal(params, &notification); err != nil {
				return nil, NewInvalidParams(map[string]any{"error": err.Error()})
			}
			if err := notification.Validate(); err != nil {
				return nil, NewInvalidParams(map[string]any{"error": err.Error()})
			}
			return nil, toReqErr(a.agent.Cancel(ctx, notification))
		}
	}

	return a.handle(ctx, method, params)
}

func (c *ClientSideConnection) handleWithExtensions(ctx context.Context, method string, params json.RawMessage) (any, *RequestError) {
	if isExtensionMethodName(method) {
		h, ok := c.client.(ExtensionMethodHandler)
		if !ok {
			return nil, NewMethodNotFound(method)
		}
		resp, err := h.HandleExtensionMethod(ctx, method, params)
		if err != nil {
			return nil, toReqErr(err)
		}
		return resp, nil
	}

	return c.handle(ctx, method, params)
}

// CallExtension sends an ACP extension-method request (method names starting with "_")
// from an agent to its client.
func (c *AgentSideConnection) CallExtension(ctx context.Context, method string, params any) (json.RawMessage, error) {
	if err := validateExtensionMethodName(method); err != nil {
		return nil, err
	}
	return SendRequest[json.RawMessage](c.conn, ctx, method, params)
}

// NotifyExtension sends an ACP extension-method notification (method names starting with "_")
// from an agent to its client.
func (c *AgentSideConnection) NotifyExtension(ctx context.Context, method string, params any) error {
	if err := validateExtensionMethodName(method); err != nil {
		return err
	}
	return c.conn.SendNotification(ctx, method, params)
}

// CallExtension sends an ACP extension-method request (method names starting with "_")
// from a client to its agent.
func (c *ClientSideConnection) CallExtension(ctx context.Context, method string, params any) (json.RawMessage, error) {
	if err := validateExtensionMethodName(method); err != nil {
		return nil, err
	}
	return SendRequest[json.RawMessage](c.conn, ctx, method, params)
}

// NotifyExtension sends an ACP extension-method notification (method names starting with "_")
// from a client to its agent.
func (c *ClientSideConnection) NotifyExtension(ctx context.Context, method string, params any) error {
	if err := validateExtensionMethodName(method); err != nil {
		return err
	}
	return c.conn.SendNotification(ctx, method, params)
}

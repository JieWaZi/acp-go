package agentclient

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"

	"github.com/JieWaZi/acp-go/pkg/acpmeta"
	acp "github.com/JieWaZi/acp-go/sdk"
)

// ACPAgent projects one Client onto a public ACP connection. It owns no sessions
// or turns; direct Go callers use the same client without this wire projection.
type ACPAgent struct {
	// Client is the sole native owner; ordinary SDK operations use it directly.
	Client
	// host is installed before the SDK starts reading or native initialization runs.
	host atomic.Pointer[acp.AgentSideConnection]
}

// NewACPAgent supplies wire callbacks while constructing the native owner. The
// factory must only validate and freeze options; Initialize starts its resources.
func NewACPAgent(create func(HostCallbacks) (Client, error)) (*ACPAgent, error) {
	if create == nil {
		return nil, errors.New("native client factory is required")
	}
	a := &ACPAgent{}
	callbacks := HostCallbacks{
		Observe: func(ctx context.Context, _ Scope, update acp.SessionNotification) error {
			host, err := a.connection()
			if err != nil {
				return err
			}
			return host.SessionUpdate(ctx, update)
		},
		RequestPermission: func(ctx context.Context, _ Scope, request acp.RequestPermissionRequest) (acp.RequestPermissionResponse, error) {
			host, err := a.connection()
			if err != nil {
				return acp.RequestPermissionResponse{}, err
			}
			return host.RequestPermission(ctx, request)
		},
		CreateElicitation: func(ctx context.Context, _ Scope, request acp.UnstableCreateElicitationRequest) (acp.UnstableCreateElicitationResponse, error) {
			host, err := a.connection()
			if err != nil {
				return acp.UnstableCreateElicitationResponse{}, err
			}
			return host.UnstableCreateElicitation(ctx, request)
		},
		CompleteElicitation: func(ctx context.Context, _ Scope, notification acp.UnstableCompleteElicitationNotification) error {
			host, err := a.connection()
			if err != nil {
				return err
			}
			return host.UnstableCompleteElicitation(ctx, notification)
		},
	}
	client, err := create(callbacks)
	if err != nil {
		return nil, err
	}
	if client == nil {
		return nil, errors.New("native client factory returned no client")
	}
	a.Client = client
	return a, nil
}

func (a *ACPAgent) connection() (*acp.AgentSideConnection, error) {
	host := a.host.Load()
	if host == nil {
		return nil, errors.New("ACP projection is not bound")
	}
	return host, nil
}

// SetAgentConnection binds callbacks and both ingress hooks before reading starts.
func (a *ACPAgent) SetAgentConnection(host *acp.AgentSideConnection) {
	if host == nil || !a.host.CompareAndSwap(nil, host) {
		panic("ACP projection requires one connection")
	}
	if peer, ok := a.Client.(interface {
		BindWirePeer(*acp.AgentSideConnection)
	}); ok {
		peer.BindWirePeer(host)
	}
	host.SetSessionAdmission(acp.SessionAdmission{
		Prompt: func(ctx context.Context, request acp.PromptRequest, prepare acp.PromptPreparation) (func() (acp.PromptResponse, error), error) {
			return a.reserve(ctx, request, prepare)
		},
		Cancel: func(notification acp.CancelNotification) (func(context.Context) error, error) {
			return nil, a.Client.Cancel(notification)
		},
	})
}

// ManagesSessionLifecycle keeps the SDK from creating another session cancel owner.
func (*ACPAgent) ManagesSessionLifecycle() bool { return true }

// Prompt uses the same native reservation as synchronous wire ingress.
func (a *ACPAgent) Prompt(ctx context.Context, request acp.PromptRequest) (acp.PromptResponse, error) {
	run, err := a.reserve(ctx, request, nil)
	if err != nil {
		return acp.PromptResponse{}, err
	}
	return run()
}

func (a *ACPAgent) reserve(ctx context.Context, request acp.PromptRequest, prepare acp.PromptPreparation) (func() (acp.PromptResponse, error), error) {
	run, err := a.Client.ReservePrompt(ctx, request, PromptOptions{Prepare: prepare})
	if err != nil {
		return nil, err
	}
	if run == nil {
		return nil, acp.NewInternalError(nil)
	}
	return func() (acp.PromptResponse, error) { return projectPromptResult(run()) }, nil
}

// Cancel handles intent synchronously; native control runs under the captured owner.
func (a *ACPAgent) Cancel(_ context.Context, notification acp.CancelNotification) error {
	return a.Client.Cancel(notification)
}

// LoadSession exposes the negotiated optional operation on the same native client.
func (a *ACPAgent) LoadSession(ctx context.Context, request acp.LoadSessionRequest) (acp.LoadSessionResponse, error) {
	if client, ok := a.Client.(acp.AgentLoader); ok {
		return client.LoadSession(ctx, request)
	}
	return acp.LoadSessionResponse{}, acp.NewMethodNotFound(acp.AgentMethodSessionLoad)
}

// UnstableForkSession preserves SDK fork metadata, including native reconciliation.
func (a *ACPAgent) UnstableForkSession(ctx context.Context, request acp.UnstableForkSessionRequest) (acp.UnstableForkSessionResponse, error) {
	if client, ok := a.Client.(interface {
		UnstableForkSession(context.Context, acp.UnstableForkSessionRequest) (acp.UnstableForkSessionResponse, error)
	}); ok {
		return client.UnstableForkSession(ctx, request)
	}
	return acp.UnstableForkSessionResponse{}, acp.NewMethodNotFound("session/fork")
}

// ProvidesUserInput prevents installing a second tool over a native input facility.
func (a *ACPAgent) ProvidesUserInput() bool {
	client, ok := a.Client.(interface{ ProvidesUserInput() bool })
	return ok && client.ProvidesUserInput()
}

// UserInputRequiresReady preserves the native tool-discovery gate when supported.
func (a *ACPAgent) UserInputRequiresReady() bool {
	client, ok := a.Client.(interface{ UserInputRequiresReady() bool })
	return ok && client.UserInputRequiresReady()
}

// HandleExtensionMethod decodes only shared wire operations. Provider-specific
// methods stay with the provider's typed operations and do not acquire new owners.
func (a *ACPAgent) HandleExtensionMethod(ctx context.Context, method string, params json.RawMessage) (any, error) {
	switch method {
	case "_session/steering":
		if client, ok := a.Client.(SteeringClient); ok {
			var request SteerRequest
			if json.Unmarshal(params, &request) != nil || request.SessionID == "" || request.Prompt == nil {
				return nil, acp.NewInvalidParams(nil)
			}
			return client.Steer(ctx, request)
		}
	case acpmeta.CompactMethod:
		if client, ok := a.Client.(CompactingClient); ok {
			var request acpmeta.CompactRequest
			if json.Unmarshal(params, &request) != nil || request.SessionID == "" {
				return nil, acp.NewInvalidParams(nil)
			}
			return client.Compact(ctx, request)
		}
	case acpmeta.NativeTurnReceiptsMethod:
		if client, ok := a.Client.(NativeOutcomeReader); ok {
			var request acpmeta.NativeTurnReceiptsRequest
			if json.Unmarshal(params, &request) != nil || request.SessionID == "" {
				return nil, acp.NewInvalidParams(nil)
			}
			page, err := client.ReadNativeOutcomes(ctx, request, false)
			if err != nil {
				return nil, err
			}
			response := acpmeta.NativeTurnReceiptsResponse{Receipts: make([]acpmeta.NativeTurnReceipt, 0, len(page.Outcomes)), More: page.More}
			for _, outcome := range page.Outcomes {
				response.Receipts = append(response.Receipts, projectNativeReceipt(outcome))
			}
			return response, nil
		}
	}
	if client, ok := a.Client.(acp.ExtensionMethodHandler); ok {
		return client.HandleExtensionMethod(ctx, method, params)
	}
	return nil, acp.NewMethodNotFound(method)
}

func projectPromptResult(result PromptResult) (acp.PromptResponse, error) {
	result = result.Snapshot()
	meta := map[string]any{
		"acp-go/prompt-receipt": map[string]any{
			"input_id":           result.Receipt.Input.InputID,
			"host_invocation_id": result.Receipt.Input.HostInvocationID,
			"submission":         result.Receipt.Submission,
			"admission":          result.Receipt.Admission,
		},
	}
	response := acp.PromptResponse{}
	failure := result.Receipt.OperationError
	if outcome := result.Outcome; outcome != nil {
		meta = acpmeta.WithNativeTurn(meta, outcome.Turn)
		if outcome.Turn.Usage != nil {
			meta = acpmeta.WithTurnUsage(meta, *outcome.Turn.Usage)
		}
		if outcome.StopReason != nil {
			response.StopReason = *outcome.StopReason
		}
		if outcome.DeliveryError != nil {
			meta["acp-go/delivery-error"] = true
		}
		if outcome.OperationError != nil {
			meta["acp-go/operation-error"] = true
		}
		if outcome.NativeFailure != nil {
			meta["acp-go/native-failure"] = true
		}
		if len(outcome.Unavailable) > 0 {
			meta["acp-go/unavailable"] = outcome.Unavailable
		}
		failure = errors.Join(failure, outcome.NativeFailure, outcome.OperationError, outcome.DeliveryError)
	}
	if result.Receipt.OperationError != nil {
		meta["acp-go/input-error"] = true
	}
	if result.Receipt.Admission == AdmissionConfirmed {
		response.UserMessageId = result.Receipt.Input.NativeInputID
	}
	response.Meta = meta
	if failure == nil && response.StopReason != "" {
		return response, nil
	}

	// Generic errors can contain provider payloads or secrets. Only classified
	// public RPC errors carry their message/data; other errors expose a category.
	wireError := acp.NewInternalError(meta)
	var classified *acp.RequestError
	if errors.As(failure, &classified) && classified != nil {
		wireError.Code, wireError.Message = classified.Code, classified.Message
		if classified.Data != nil {
			meta["cause"] = classified.Data
		}
	} else if errors.Is(failure, context.Canceled) {
		wireError = acp.NewRequestCancelled(meta)
	}
	return response, wireError
}

func projectNativeReceipt(outcome NativeOutcome) acpmeta.NativeTurnReceipt {
	outcome = outcome.Snapshot()
	receipt := acpmeta.NativeTurnReceipt{Turn: outcome.Turn, Unavailable: outcome.Unavailable}
	if outcome.StopReason != nil {
		receipt.StopReason = string(*outcome.StopReason)
	}
	if outcome.FinalMessage != nil {
		receipt.ResultText = outcome.FinalMessage.Text
	}
	if outcome.NativeFailure != nil || outcome.OperationError != nil || outcome.DeliveryError != nil {
		receipt.Failure = &acpmeta.NativeTurnFailure{Code: -32603, ObservedOutput: outcome.ObservedOutput, PossibleSideEffects: outcome.PossibleSideEffects}
	}
	return receipt
}

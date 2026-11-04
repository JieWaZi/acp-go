// Package agentclient defines the direct native-client boundary shared by hosts
// and the public ACP projection. Providers own execution; this package does not.
package agentclient

import (
	"context"
	"slices"

	"github.com/JieWaZi/acp-go/pkg/acpmeta"
	acp "github.com/JieWaZi/acp-go/sdk"
)

// Client owns native sessions and their process resources. Constructors freeze
// options only; Initialize starts resources after the host registers the client.
// Optional operations retain their negotiated SDK or provider-specific methods.
type Client interface {
	Initialize(context.Context, acp.InitializeRequest) (acp.InitializeResponse, error)
	Authenticate(context.Context, acp.AuthenticateRequest) (acp.AuthenticateResponse, error)
	Logout(context.Context, acp.LogoutRequest) (acp.LogoutResponse, error)
	ListSessions(context.Context, acp.ListSessionsRequest) (acp.ListSessionsResponse, error)
	NewSession(context.Context, acp.NewSessionRequest) (acp.NewSessionResponse, error)
	ResumeSession(context.Context, acp.ResumeSessionRequest) (acp.ResumeSessionResponse, error)
	CloseSession(context.Context, acp.CloseSessionRequest) (acp.CloseSessionResponse, error)
	SetSessionConfigOption(context.Context, acp.SetSessionConfigOptionRequest) (acp.SetSessionConfigOptionResponse, error)
	SetSessionMode(context.Context, acp.SetSessionModeRequest) (acp.SetSessionModeResponse, error)
	// ReservePrompt captures the actual owner synchronously, without I/O or waiting.
	// The caller must run the continuation exactly once, including after cancellation.
	ReservePrompt(context.Context, acp.PromptRequest, PromptOptions) (func() PromptResult, error)
	// Cancel captures its target and starts its bounded control task immediately.
	// A nil return acknowledges intent, not native interruption.
	Cancel(acp.CancelNotification) error
	// Close retains incomplete cleanup on this same client for a later retry.
	Close(context.Context) error
}

// Scope is established from native evidence before asynchronous host delivery.
type Scope struct {
	// SessionID identifies the confirmed native session.
	SessionID acp.SessionId
	// HostInvocationID is empty until a host input is demonstrably associated.
	HostInvocationID string
	// NativeInvocationID is the adapter's execution identity, absent for session events.
	NativeInvocationID string
}

// HostCallbacks contains only callbacks used by the built-in direct clients.
// Providers call these outside state locks and native-reader critical sections.
type HostCallbacks struct {
	// Observe receives ordered observations; failure cannot erase native evidence.
	Observe func(context.Context, Scope, acp.SessionNotification) error
	// RequestPermission resolves an approval belonging to the captured execution.
	RequestPermission func(context.Context, Scope, acp.RequestPermissionRequest) (acp.RequestPermissionResponse, error)
	// CreateElicitation handles a form or URL request with verified ownership.
	CreateElicitation func(context.Context, Scope, acp.UnstableCreateElicitationRequest) (acp.UnstableCreateElicitationResponse, error)
	// CompleteElicitation completes the original URL request, not the latest turn.
	CompleteElicitation func(context.Context, Scope, acp.UnstableCompleteElicitationNotification) error
}

// PromptOptions binds preparation and business admission to one reserved input.
type PromptOptions struct {
	// HostInvocationID is supplied by the host, or allocated for an ACP-only caller.
	HostInvocationID string
	// BeforeSubmit commits input identity and authorizes work before any prompt bytes.
	// It runs outside state locks and the write gate; failure guarantees no submission.
	BeforeSubmit func(context.Context, PromptSubmission) error
	// Prepare performs optional interaction setup; cleanup precedes owner release.
	Prepare acp.PromptPreparation
}

// PrepareSubmission runs host preparation and admission outside provider locks.
// The owner must call returned cleanup even when preparation returns an error.
func (o PromptOptions) PrepareSubmission(ctx context.Context, input PromptSubmission) (cleanup func(), err error) {
	if err = context.Cause(ctx); err != nil {
		return nil, err
	}
	if o.Prepare != nil {
		cleanup, err = o.Prepare(ctx)
	}
	if err == nil {
		err = context.Cause(ctx)
	}
	if err == nil && o.BeforeSubmit != nil {
		err = o.BeforeSubmit(ctx, input.Snapshot())
	}
	if err == nil {
		err = context.Cause(ctx)
	}
	return cleanup, err
}

// PromptSubmission is frozen before business admission and reused in the receipt.
type PromptSubmission struct {
	// SessionID is the opened target session.
	SessionID acp.SessionId
	// HostInvocationID identifies the host's business invocation.
	HostInvocationID string
	// InputID identifies the adapter-owned input independently of native support.
	InputID string
	// NativeInputID is selected before the gate and used unchanged on the wire when
	// supported. Its presence does not prove submission or native acceptance.
	NativeInputID *string
}

// Submission describes transport evidence, independently of native admission.
type Submission string

const (
	SubmissionNotSent Submission = "not_sent"
	SubmissionUnknown Submission = "unknown"
	SubmissionSent    Submission = "sent"
)

// Admission describes native acceptance; transport success alone is insufficient.
type Admission string

const (
	AdmissionNotAccepted Admission = "not_accepted"
	AdmissionUnknown     Admission = "unknown"
	AdmissionConfirmed   Admission = "confirmed"
)

// PromptReceipt describes one host input, which may be consumed by another turn.
type PromptReceipt struct {
	// Input is the same frozen identity supplied to BeforeSubmit.
	Input PromptSubmission
	// Submission records local write evidence.
	Submission Submission
	// Admission records only proven native acceptance or rejection.
	Admission Admission
	// OperationError concerns this input's preparation, submission or waiter.
	OperationError error
}

// FinalMessage contains provider-proven complete text, not an assumed stream tail.
type FinalMessage struct {
	// ID is the same identity used to reconcile streamed content.
	ID string
	// Text is the complete native message body.
	Text string
}

// ContextUsage is a context snapshot, never invocation consumption.
type ContextUsage struct {
	// UsedTokens is unknown when nil.
	UsedTokens *int64
	// WindowTokens can be unknown independently of used tokens.
	WindowTokens *int64
}

// NativeOutcome is a frozen execution fact shared by foreground and receipt reads.
// Native status is absent without a native terminal, even after local settlement.
// Errors must themselves be immutable and safe for their intended consumer.
type NativeOutcome struct {
	// Turn is the sole native identity, origin, consumption, status and usage record.
	Turn acpmeta.NativeTurn
	// StopReason exists only when supported by native terminal evidence.
	StopReason *acp.StopReason
	// ForkPosition is an opaque, verified native position for exact forking.
	ForkPosition string
	// NativeFailure is present only for a provider-reported failure.
	NativeFailure error
	// FinalMessage reconciles complete text when the provider supplies it.
	FinalMessage *FinalMessage
	// ContextUsage is independent of billable token consumption.
	ContextUsage ContextUsage
	// ObservedOutput indicates execution output was actually observed.
	ObservedOutput bool
	// PossibleSideEffects forbids assuming a failed wait is safe to replay.
	PossibleSideEffects bool
	// OperationError is an execution-scoped transport or waiting failure.
	OperationError error
	// DeliveryError is an observation or interaction delivery failure.
	DeliveryError error
	// Unavailable names fields omitted due to a declared retention budget.
	Unavailable []string
}

// PromptResult separates the host input's receipt from its associated execution.
type PromptResult struct {
	// Receipt describes this input independently of any native execution.
	Receipt PromptReceipt
	// Outcome is absent when no native execution can be associated.
	Outcome *NativeOutcome
}

// NativeOutcomeReader retains detached and autonomous results independently of
// Observe. Hosts ACK only after durable result and input-consumption evidence.
type NativeOutcomeReader interface {
	ReadNativeOutcomes(context.Context, acpmeta.NativeTurnReceiptsRequest, bool) (NativeOutcomePage, error)
}

// NativeOutcomePage starts at the oldest unacknowledged result in one session.
type NativeOutcomePage struct {
	// Outcomes are independent snapshots in completion order.
	Outcomes []NativeOutcome
	// More reports additional completed results beyond this bounded page.
	More bool
}

// SteerRequest carries additional input for supporting providers.
type SteerRequest struct {
	// SessionID identifies the session whose execution may receive the input.
	SessionID acp.SessionId `json:"sessionId"`
	// Prompt contains the additional input.
	Prompt []acp.ContentBlock `json:"prompt"`
	// PromptRequired requires a standard Prompt when idle, rather than starting work through steering.
	PromptRequired bool `json:"-"`
	// Target is the exact observed scope. A nonzero value requires every field
	// to match and disables idle fallback. The zero value preserves ACP policy.
	Target Scope `json:"-"`
	// Options admits this input independently of the target's opening input.
	Options PromptOptions `json:"-"`
}

// SteerResponse reports the native operation rather than an invented new turn.
type SteerResponse struct {
	// Outcome retains the provider's established public extension vocabulary.
	Outcome string `json:"outcome"`
	// Reason explains a provider's unsupported or rejected operation when supplied.
	Reason string `json:"reason,omitempty"`
	// Receipt is an independent snapshot once an input identity was allocated.
	// Legacy idle steering can acknowledge a local queue before bytes are sent.
	// It may accompany an error. NativeOutcome is the authority for consumption.
	Receipt *PromptReceipt `json:"-"`
}

// SteeringClient exposes steering only where the native protocol supports it.
type SteeringClient interface {
	Steer(context.Context, SteerRequest) (SteerResponse, error)
}

// CompactingClient exposes native compaction through the same session owner.
type CompactingClient interface {
	Compact(context.Context, acpmeta.CompactRequest) (acpmeta.CompactResponse, error)
}

// Snapshot isolates mutable result values from another consumer of the owner.
func (r PromptResult) Snapshot() PromptResult {
	r.Receipt.Input = r.Receipt.Input.Snapshot()
	if r.Outcome != nil {
		outcome := r.Outcome.Snapshot()
		r.Outcome = &outcome
	}
	return r
}

// Snapshot gives a gate or consumer its own optional native input identity.
func (s PromptSubmission) Snapshot() PromptSubmission {
	s.NativeInputID = copyValue(s.NativeInputID)
	return s
}

// Snapshot gives a foreground or receipt reader independent mutable evidence.
func (o NativeOutcome) Snapshot() NativeOutcome {
	o.Turn.NativeTurnID = copyValue(o.Turn.NativeTurnID)
	o.Turn.TerminalEventID = copyValue(o.Turn.TerminalEventID)
	o.Turn.ConsumedInputIDs = slices.Clone(o.Turn.ConsumedInputIDs)
	if o.Turn.Usage != nil {
		u := *o.Turn.Usage
		u.InputTokens = copyValue(u.InputTokens)
		u.CacheReadTokens = copyValue(u.CacheReadTokens)
		u.CacheWriteTokens = copyValue(u.CacheWriteTokens)
		u.OutputTokens = copyValue(u.OutputTokens)
		u.ReasoningTokens = copyValue(u.ReasoningTokens)
		u.Cost = copyValue(u.Cost)
		u.Models = copyUsageModels(u.Models)
		u.Observations = slices.Clone(u.Observations)
		for i := range u.Observations {
			u.Observations[i].Tokens = copyModelUsage(u.Observations[i].Tokens)
			u.Observations[i].Models = copyUsageModels(u.Observations[i].Models)
		}
		o.Turn.Usage = &u
	}
	o.StopReason = copyValue(o.StopReason)
	o.FinalMessage = copyValue(o.FinalMessage)
	o.ContextUsage.UsedTokens = copyValue(o.ContextUsage.UsedTokens)
	o.ContextUsage.WindowTokens = copyValue(o.ContextUsage.WindowTokens)
	o.Unavailable = slices.Clone(o.Unavailable)
	return o
}

func copyValue[T any](value *T) *T {
	if value == nil {
		return nil
	}
	result := *value
	return &result
}

func copyModelUsage(model acpmeta.ModelUsage) acpmeta.ModelUsage {
	model.InputTokens = copyValue(model.InputTokens)
	model.CacheReadTokens = copyValue(model.CacheReadTokens)
	model.CacheWriteTokens = copyValue(model.CacheWriteTokens)
	model.OutputTokens = copyValue(model.OutputTokens)
	model.ReasoningTokens = copyValue(model.ReasoningTokens)
	model.Cost = copyValue(model.Cost)
	return model
}

func copyUsageModels(models map[string]acpmeta.ModelUsage) map[string]acpmeta.ModelUsage {
	if models == nil {
		return nil
	}
	copy := make(map[string]acpmeta.ModelUsage, len(models))
	for id, model := range models {
		copy[id] = copyModelUsage(model)
	}
	return copy
}

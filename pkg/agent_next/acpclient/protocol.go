package acpclient

import (
	"context"
	"encoding/json"

	"github.com/JieWaZi/acp-go/pkg/acpmeta"
	acp "github.com/JieWaZi/acp-go/sdk"
)

// Protocol describes observed deviations at specific ACP boundaries.
type Protocol struct {
	// ModelScope is session-scoped unless the native process shares a model.
	ModelScope ModelScope
	// PromptUsage selects the native meaning of standard ACP usage.
	PromptUsage PromptUsageScope
	// NormalizeOption changes presentation while preserving native IDs and values.
	NormalizeOption func(acp.SessionConfigOption) acp.SessionConfigOption
	// ExecutionOption identifies additional native parameters frozen with each prompt.
	ExecutionOption func(acp.SessionConfigOptionCategory) bool
	// SessionMissing classifies a provider-specific error for the exact requested ID.
	SessionMissing func(*acp.RequestError, acp.SessionId) bool
	// ClassifyText classifies one complete native text chunk for the running version.
	ClassifyText func(version, text string) TextClass
	// Private decodes known reverse method data without performing I/O.
	Private PrivateProtocol
	// Catalog describes a provider-specific read-only directory request.
	Catalog *CatalogProbe
	// ParameterizedModelPicker advertises support for independent native model parameters.
	ParameterizedModelPicker bool
	// Authentication limits standard ACP authentication and supplies an optional logout command.
	Authentication Authentication
}

// ModelScope identifies where a native model set takes effect.
type ModelScope uint8

const (
	ModelPerSession ModelScope = iota
	ModelPerProcess
)

// PromptUsageScope describes a provider's standard ACP usage payload.
type PromptUsageScope uint8

const (
	UsageSessionCumulative PromptUsageScope = iota
	UsageInvocation
	UsageUnavailable
)

// TextClass describes a proven provider condition in a complete answer chunk.
type TextClass uint8

const (
	TextAnswer TextClass = iota
	TextNoise
	TextAuthenticationLost
)

// Authentication describes native login availability for a frozen credential.
type Authentication struct {
	// Disabled suppresses interactive authentication for an explicit credential.
	Disabled bool
	// Method limits standard authentication to one advertised method when specified.
	Method string
	// LogoutFallback performs the selected CLI's dedicated logout when ACP omits it.
	LogoutFallback func(context.Context) error
	// RetireOnLogout retires a process that can retain the previous credential.
	RetireOnLogout bool
}

// CatalogProbe describes one read-only native model discovery operation.
type CatalogProbe struct {
	// Method is the exact native catalog method.
	Method string
	// Decode returns only native catalog facts, independently of session selection.
	Decode func(json.RawMessage) (acpmeta.ModelCatalog, error)
}

// CallbackIdentity names the native session or tool that owns a reverse operation.
type CallbackIdentity struct {
	// SessionID is an explicit native session ID when present.
	SessionID acp.SessionId
	// ToolID refers to a known native tool call, including calls first named by an interaction.
	ToolID acp.ToolCallId
}

// Interaction describes a private request in terms of standard host capabilities.
type Interaction struct {
	// Identity must match the current native owner before dispatch.
	Identity CallbackIdentity
	// Permission contains an approval request for a native tool.
	Permission *acp.RequestPermissionRequest
	// Elicitation contains a form or URL request.
	Elicitation *acp.UnstableCreateElicitationRequest
	// Reply converts only host response data into the provider's wire response.
	Reply InteractionReplyCodec
}

// InteractionReplyCodec is a pure native response encoder for a decoded request.
type InteractionReplyCodec interface {
	EncodeReply(InteractionReply) (any, error)
}

// InteractionReply contains the authorized host answer. An empty reply is cancellation.
type InteractionReply struct {
	// Permission contains an approved or canceled tool request.
	Permission *acp.RequestPermissionResponse
	// Elicitation contains the accepted, declined, or canceled host form.
	Elicitation *acp.UnstableCreateElicitationResponse
}

// PlanItem associates a normalized entry with a provider's native merge key.
type PlanItem struct {
	// ID is the native item identity.
	ID string
	// Entry is nil if the provider entry does not have a displayable status.
	Entry *acp.PlanEntry
}

// Progress describes a private native update using standard ACP values.
type Progress struct {
	// Identity must match the current native owner before folding.
	Identity CallbackIdentity
	// Update is a normalized tool or progress update.
	Update *acp.SessionUpdate
	// Plan carries the ordered native entries to replace or merge.
	Plan *PlanProgress
}

// MaxPlanEntries bounds an observed native plan snapshot.
const MaxPlanEntries = 256

// PlanProgress contains either a replacement or a merge into the session's plan.
type PlanProgress struct {
	// Entries are keyed by provider-native identities.
	Entries []PlanItem
	// Merge preserves entries absent from this update.
	Merge bool
}

// PrivateProtocol is a stateless codec for a provider's exact reverse methods.
type PrivateProtocol interface {
	DecodeInteraction(string, json.RawMessage) (Interaction, error, bool)
	DecodeProgress(string, json.RawMessage) (Progress, error, bool)
}

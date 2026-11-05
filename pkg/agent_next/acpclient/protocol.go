package acpclient

import (
	"context"
	"encoding/json"
	"time"

	"github.com/JieWaZi/acp-go/pkg/acpmeta"
	acp "github.com/JieWaZi/acp-go/sdk"
)

// Protocol describes observed deviations at specific ACP boundaries.
type Protocol struct {
	// PrepareInitialize advertises a provider's documented client capability.
	PrepareInitialize func(*acp.InitializeRequest)
	// ValidateInitialize verifies the selected native protocol before publication.
	ValidateInitialize func(acp.InitializeResponse) error
	// PrepareSessionMeta adds frozen native launch policy to an owned metadata copy.
	PrepareSessionMeta func(map[string]any)
	// OpeningMode applies an advertised native mode before publishing a session.
	OpeningMode acp.SessionModeId
	// ForkAllowed narrows a native fork capability to verified versions.
	ForkAllowed func(acp.InitializeResponse) bool
	// CwdBoundProcess requires session cwd to match the launch directory.
	CwdBoundProcess bool
	// RequireLoadEvidence rejects an ID-less restore response without same-session history replay.
	RequireLoadEvidence bool
	// ValidateRestoreID rejects native selector aliases that cannot prove an exact session.
	ValidateRestoreID func(acp.SessionId) bool
	// PrepareRestore protects exact native history before a restore can write it.
	PrepareRestore func(context.Context, string, string, acp.SessionId) error
	// ModeOption selects an advertised native config setter for permission modes.
	ModeOption acp.SessionConfigId
	// BlockUntypedExtensions protects providers whose raw methods expose credentials or unscoped mutations.
	BlockUntypedExtensions bool
	// ReadPromptEvidence interprets provider-specific input, turn identity and failure fields.
	ReadPromptEvidence func(acp.StopReason, string, map[string]any) PromptEvidence
	// NormalizeUsage decodes auxiliary observations and corrects fields with different native meanings.
	NormalizeUsage func(*acp.SessionUsageUpdate) *acpmeta.UsageObservation
	// ModelScope is session-scoped unless the native process shares a model.
	ModelScope ModelScope
	// LegacyModel describes a version-specific session/set_model mutation contract.
	LegacyModel LegacyModelBehavior
	// PromptUsage selects the native meaning of standard ACP usage.
	PromptUsage PromptUsageScope
	// ReadPromptUsage decodes provider-owned accounting and classifies the standard counters.
	ReadPromptUsage func(string, *PromptUsageCounters, map[string]any) (*acpmeta.TurnUsage, PromptUsageScope)
	// CapturePromptJournal captures the read-only baseline of one native prompt.
	// Both capture and settlement run under invocation ownership outside ACP locks.
	CapturePromptJournal func(context.Context, PromptJournalScope) func(context.Context, *acp.StopReason) PromptJournalEvidence
	// NormalizeOption changes presentation while preserving native IDs and values.
	NormalizeOption func(acp.SessionConfigOption) acp.SessionConfigOption
	// ReadModelConfig reads model-scoped settings carried in native model rows.
	ReadModelConfig func(map[string]any) (acpmeta.ModelConfigCatalog, bool)
	// ReadOnlyModelCatalog disables discovery setters that can persist native defaults.
	ReadOnlyModelCatalog bool
	// DisposableModelCatalog permits probes without restoring a persistent default.
	DisposableModelCatalog bool
	// ExecutionOption identifies additional native parameters frozen with each prompt.
	ExecutionOption func(acp.SessionConfigOptionCategory) bool
	// SessionMissing classifies a provider-specific error for the exact requested ID.
	SessionMissing func(*acp.RequestError, acp.SessionId) bool
	// RequireExactMissing lets the profile distinguish resource and session failures.
	RequireExactMissing bool
	// RecoverRestore recognizes a native failure safe to check or retry once.
	RecoverRestore func(string, *acp.RequestError) RestoreRecovery
	// EmptyAnswerFailure corrects an end_turn that omitted the expected output.
	EmptyAnswerFailure func(reasoned bool) *acp.RequestError
	// DirectoryScopedMCP keeps one native MCP namespace per directory per process.
	DirectoryScopedMCP bool
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
	// StartupAuthentication selects an advertised noninteractive native method.
	StartupAuthentication func(acp.InitializeResponse) string
}

// PromptEvidence extends an ACP response with proven provider-specific facts.
type PromptEvidence struct {
	// NativeTurnID is the provider's stable turn identity, when present.
	NativeTurnID string
	// InputAccepted requires explicit native evidence that this input entered the conversation.
	InputAccepted bool
	// Failure describes a native prompt failure using safe structured fields.
	Failure *acp.RequestError
}

// PromptJournalScope identifies the native process and its exact bound workspace.
type PromptJournalScope struct {
	// Version is the selected CLI's reported runtime version.
	Version string
	// SessionID is the admitted native session generation's ID.
	SessionID acp.SessionId
	// Cwd is the workspace bound during the successful new/load/resume.
	Cwd string
}

// PromptJournalEvidence supplements a matched invocation using native persisted facts.
type PromptJournalEvidence struct {
	// NativeTurnID identifies a started native turn when the journal proves it.
	NativeTurnID string
	// UnverifiedTerminal marks a native response contradicted by missing journal evidence.
	UnverifiedTerminal bool
	// Failure describes a proven main-agent failure using safe fields.
	Failure *acp.RequestError
	// Usage contains independently scoped invocation counters.
	Usage *acpmeta.TurnUsage
}

// ModelScope identifies where a native model set takes effect.
type ModelScope uint8

const (
	ModelPerSession ModelScope = iota
	ModelPerProcess
)

// LegacyModelBehavior describes the evidence returned by a native legacy setter.
type LegacyModelBehavior struct {
	// ConfirmsValue identifies versions whose successful setter applies the selected value.
	ConfirmsValue func(string) bool
	// OpeningValueIsNoop identifies setters that still compare against the opening value.
	OpeningValueIsNoop bool
	// AppliedOnError identifies an exact persistence failure after the mutation.
	AppliedOnError func(*acp.RequestError) bool
}

// PromptUsageScope describes a provider's standard ACP usage payload.
type PromptUsageScope uint8

const (
	UsageSessionCumulative PromptUsageScope = iota
	UsageInvocation
	UsageUnavailable
	// UsageUnattributed retains native counters when their turn/session scope is unproven.
	UsageUnattributed
)

// TextClass describes a proven provider condition in a complete answer chunk.
type TextClass uint8

const (
	TextAnswer TextClass = iota
	TextNoise
	TextAuthenticationLost
)

// RestoreRecovery describes the safe next step after a recognized peer refusal.
type RestoreRecovery uint8

const (
	RestoreNoRecovery RestoreRecovery = iota
	RestoreRetryAfterStartup
	RestoreCheckListing
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
	// Timeout bounds this probe within the caller's deadline. Zero uses the default.
	Timeout time.Duration
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
	// Followup submits an optional native answer before replying to the reverse request.
	Followup *InteractionFollowup
}

// InteractionFollowup describes an answer request on the captured turn's connection.
type InteractionFollowup struct {
	// Method is the provider's exact answer method.
	Method string
	// Encode converts the host result to the native request without side effects.
	Encode func(InteractionReply) (any, error)
	// Validate requires positive native acknowledgement of the answer.
	Validate func(json.RawMessage) error
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

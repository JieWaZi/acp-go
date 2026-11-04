package acpmeta

import "encoding/json"

// AccountUsageMethod is the read-only ACP management extension for quota observations.
const AccountUsageMethod = "_account/usage"

// AccountUsage contains the quota buckets returned by one native read.
type AccountUsage struct {
	// Available is set only when the native provider reports quota availability.
	Available *bool `json:"available,omitempty"`
	// ResetCredits is the optional read-only observation of credits for resetting windows.
	ResetCredits *AccountUsageResetCredits `json:"reset_credits,omitempty"`
	// Buckets holds all observed buckets; an empty slice is a known empty result.
	Buckets []AccountUsageBucket `json:"buckets"`
}

// AccountUsageBucket describes one independently metered quota.
type AccountUsageBucket struct {
	// ID is the provider-issued bucket identity.
	ID string `json:"id,omitempty"`
	// Name is the optional provider display name.
	Name *string `json:"name,omitempty"`
	// Windows preserves each reported quota window.
	Windows []AccountUsageWindow `json:"windows"`
	// HasCredits is absent when the provider did not report credit status.
	HasCredits *bool `json:"has_credits,omitempty"`
	// Unlimited is absent when the provider did not report credit status.
	Unlimited *bool `json:"unlimited,omitempty"`
	// Balance is the provider-reported credit balance, never a computed amount.
	Balance *string `json:"balance,omitempty"`
	// ReachedType records the provider's reason a bucket has reached its limit.
	ReachedType *string `json:"reached_type,omitempty"`
	// SpendControlReached retains the native spend control state, including false.
	SpendControlReached *bool `json:"spend_control_reached,omitempty"`
	// Extra contains optional additional allowance observations and their unit.
	Extra *AccountUsageExtra `json:"extra,omitempty"`
}

// AccountUsageWindow describes one reported usage window.
type AccountUsageWindow struct {
	// Kind distinguishes the provider's primary and secondary windows.
	Kind string `json:"kind"`
	// UsedPercent is the reported percentage, including zero.
	UsedPercent *float64 `json:"used_percent,omitempty"`
	// RemainingPercent is reported independently of used percent.
	RemainingPercent *float64 `json:"remaining_percent,omitempty"`
	// DurationSeconds is absent when the provider did not report a duration.
	DurationSeconds *int64 `json:"duration_seconds,omitempty"`
	// ResetAtUnixSeconds is an optional absolute Unix timestamp in seconds.
	ResetAtUnixSeconds *int64 `json:"reset_at_unix_seconds,omitempty"`
}

// AccountUsageExtra holds the provider's optional additional allowance observation.
type AccountUsageExtra struct {
	// Enabled records the native enablement state.
	Enabled *bool `json:"enabled,omitempty"`
	// Used preserves the exact native numeric usage.
	Used *json.Number `json:"used,omitempty"`
	// Limit preserves the exact native numeric limit.
	Limit *json.Number `json:"limit,omitempty"`
	// Currency identifies the reported currency when the provider supplies it.
	Currency *string `json:"currency,omitempty"`
	// Unit names an explicitly reported unit. Absence does not imply minor currency units.
	Unit *string `json:"unit,omitempty"`
}

// AccountUsageResetCredits describes currently reported window reset credits.
type AccountUsageResetCredits struct {
	// AvailableCount includes zero when it is explicitly reported.
	AvailableCount *int64 `json:"available_count,omitempty"`
	// Details is nil when unavailable and empty when the provider returned no rows.
	Details []AccountUsageResetCredit `json:"details"`
}

// AccountUsageResetCredit contains one native read-only credit observation.
type AccountUsageResetCredit struct {
	// ID is the provider's opaque credit identity.
	ID string `json:"id"`
	// Status is the provider's credit status.
	Status string `json:"status,omitempty"`
	// ResetType identifies the kind of windows the credit resets.
	ResetType string `json:"reset_type,omitempty"`
	// Title is the optional provider title.
	Title *string `json:"title,omitempty"`
	// Description is the optional provider description.
	Description *string `json:"description,omitempty"`
	// GrantedAtUnixSeconds is the observed grant timestamp.
	GrantedAtUnixSeconds *int64 `json:"granted_at_unix_seconds,omitempty"`
	// ExpiresAtUnixSeconds is the reported expiry, when one was supplied.
	ExpiresAtUnixSeconds *int64 `json:"expires_at_unix_seconds,omitempty"`
}

// AccountReadMethod reads normalized authentication facts without starting a prompt.
const AccountReadMethod = "_account/read"

// AccountInfo describes the currently reported authentication state.
type AccountInfo struct {
	// RequiresAuthentication indicates that the provider requires its own authentication.
	RequiresAuthentication bool `json:"requires_authentication"`
	// Account is absent when the provider did not report a signed-in account.
	Account *AccountIdentity `json:"account,omitempty"`
}

// AccountIdentity describes a provider-reported account without inventing entitlements.
type AccountIdentity struct {
	// Kind identifies the authentication method using a normalized value.
	Kind string `json:"kind"`
	// Label is an optional account label supplied by the provider.
	Label *string `json:"label,omitempty"`
	// Plan is an optional provider-reported plan label.
	Plan *string `json:"plan,omitempty"`
}

package acpmeta

// AccountUsageMethod is the read-only ACP management extension for quota observations.
const AccountUsageMethod = "_account/usage"

// AccountUsage contains the quota buckets returned by one native read.
type AccountUsage struct {
	// Buckets holds all observed buckets; an empty slice is a known empty result.
	Buckets []AccountUsageBucket `json:"buckets"`
}

// AccountUsageBucket describes one independently metered quota.
type AccountUsageBucket struct {
	// ID is the provider-issued bucket identity.
	ID string `json:"id"`
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
}

// AccountUsageWindow describes one reported usage window.
type AccountUsageWindow struct {
	// Kind distinguishes the provider's primary and secondary windows.
	Kind string `json:"kind"`
	// UsedPercent is the reported percentage, including zero.
	UsedPercent *float64 `json:"used_percent,omitempty"`
	// DurationSeconds is absent when the provider did not report a duration.
	DurationSeconds *int64 `json:"duration_seconds,omitempty"`
	// ResetAtUnixSeconds is an optional absolute Unix timestamp in seconds.
	ResetAtUnixSeconds *int64 `json:"reset_at_unix_seconds,omitempty"`
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

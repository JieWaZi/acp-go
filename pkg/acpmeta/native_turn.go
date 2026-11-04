package acpmeta

import (
	"encoding/json"
	"slices"
)

const (
	nativeTurnKey       = "acp-go/native-turn"
	nativeBackgroundKey = "acp-go/native-background"

	// NativeTurnOriginHost identifies a native invocation initiated by an admitted host send.
	NativeTurnOriginHost = "host"
	// NativeTurnOriginProvider identifies a native invocation initiated by the provider.
	NativeTurnOriginProvider = "provider"
	// NativeTurnOriginUnknown means the adapter cannot establish the initiator.
	NativeTurnOriginUnknown = "unknown"

	// NativeTurnCompleted is a successful native terminal outcome.
	NativeTurnCompleted = "completed"
	// NativeTurnCancelled is an interrupted native terminal outcome.
	NativeTurnCancelled = "cancelled"
	// NativeTurnFailed is an unsuccessful native terminal outcome.
	NativeTurnFailed = "failed"
)

// NativeTurn relates an adapter invocation to native identities and a replaceable outcome.
type NativeTurn struct {
	// InvocationID identifies one adapter-observed invocation, independent of input IDs.
	InvocationID string `json:"invocation_id"`
	// NativeTurnID is present when the provider supplies a native turn identity.
	NativeTurnID *string `json:"native_turn_id,omitempty"`
	// TerminalEventID identifies the provider's terminal event, within its stated scope.
	TerminalEventID *string `json:"terminal_event_id,omitempty"`
	// Origin is the observed initiator, or unknown when evidence is insufficient.
	Origin string `json:"origin"`
	// ConsumedInputIDs contains confirmed inputs, in provider consumption order.
	ConsumedInputIDs []string `json:"consumed_input_ids,omitempty"`
	// Status is absent until an outcome is known.
	Status string `json:"status,omitempty"`
	// Usage is the observed consumption for this invocation.
	Usage *TurnUsage `json:"usage,omitempty"`
}

// NativeBackground reports native background activity independently of foreground completion.
type NativeBackground struct {
	// ActiveCount is nil for unknown and zero for a complete empty native snapshot.
	ActiveCount *int64 `json:"active_count,omitempty"`
}

// WithNativeTurn copies metadata and stores an independent invocation snapshot.
func WithNativeTurn(meta map[string]any, turn NativeTurn) map[string]any {
	copy := copyMetadata(meta)
	value := map[string]any{"invocation_id": turn.InvocationID, "origin": turn.Origin}
	if turn.NativeTurnID != nil {
		value["native_turn_id"] = *turn.NativeTurnID
	}
	if turn.TerminalEventID != nil {
		value["terminal_event_id"] = *turn.TerminalEventID
	}
	if len(turn.ConsumedInputIDs) > 0 {
		value["consumed_input_ids"] = slices.Clone(turn.ConsumedInputIDs)
	}
	if turn.Status != "" {
		value["status"] = turn.Status
	}
	if turn.Usage != nil {
		value["usage"] = WithTurnUsage(nil, *turn.Usage)[turnUsageKey]
	}
	copy[nativeTurnKey] = value
	return copy
}

// ReadNativeTurn accepts invocation metadata before or after an ACP JSON round trip.
func ReadNativeTurn(meta map[string]any) (NativeTurn, bool) {
	var result NativeTurn
	value, ok := meta[nativeTurnKey].(map[string]any)
	if !ok {
		return result, false
	}
	encoded, err := json.Marshal(value)
	if err != nil || json.Unmarshal(encoded, &result) != nil || result.InvocationID == "" {
		return NativeTurn{}, false
	}
	switch result.Origin {
	case NativeTurnOriginHost, NativeTurnOriginProvider, NativeTurnOriginUnknown:
	default:
		return NativeTurn{}, false
	}
	switch result.Status {
	case "", NativeTurnCompleted, NativeTurnCancelled, NativeTurnFailed:
	default:
		return NativeTurn{}, false
	}
	if value["usage"] != nil {
		usage, ok := ReadTurnUsage(map[string]any{turnUsageKey: value["usage"]})
		if !ok {
			return NativeTurn{}, false
		}
		result.Usage = &usage
	}
	return result, true
}

// WithNativeBackground copies metadata and records the current activity level.
func WithNativeBackground(meta map[string]any, background NativeBackground) map[string]any {
	copy := copyMetadata(meta)
	value := map[string]any{}
	if background.ActiveCount != nil {
		value["active_count"] = *background.ActiveCount
	}
	copy[nativeBackgroundKey] = value
	return copy
}

// ReadNativeBackground preserves both the unknown and the explicitly empty snapshot.
func ReadNativeBackground(meta map[string]any) (NativeBackground, bool) {
	value, ok := meta[nativeBackgroundKey].(map[string]any)
	if !ok {
		return NativeBackground{}, false
	}
	if raw := value["active_count"]; raw != nil {
		count, ok := usageInteger(raw)
		if !ok {
			return NativeBackground{}, false
		}
		return NativeBackground{ActiveCount: &count}, true
	}
	return NativeBackground{}, true
}

// copyMetadata copies the metadata envelope before replacing an owned namespace.
func copyMetadata(meta map[string]any) map[string]any {
	result := make(map[string]any, len(meta)+1)
	for key, value := range meta {
		result[key] = value
	}
	return result
}

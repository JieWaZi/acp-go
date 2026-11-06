// Package acpmeta 定义 ACP 标准结构尚未覆盖的通用元数据约定。
package acpmeta

import (
	"fmt"
	"strings"
)

const (
	// runtimeMetadataKey 是 Implementation._meta 内 Runtime 信息的命名空间。
	runtimeMetadataKey = "runtime"
	// runtimeVersionKey 是 Runtime 信息内被包装 CLI 的版本字段。
	runtimeVersionKey = "version"
)

// RuntimeVersionMetadata 构造包含被包装 Runtime CLI 版本的 ACP 元数据。
func RuntimeVersionMetadata(version string) map[string]any {
	version = strings.TrimSpace(version)
	if version == "" {
		return nil
	}
	return map[string]any{
		runtimeMetadataKey: map[string]any{
			runtimeVersionKey: version,
		},
	}
}

// RuntimeVersion 从 Implementation._meta 中读取被包装 Runtime CLI 的版本。
func RuntimeVersion(meta map[string]any) string {
	runtime, ok := meta[runtimeMetadataKey].(map[string]any)
	if !ok {
		return ""
	}
	version, ok := runtime[runtimeVersionKey].(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(version)
}

const developerInstructionsKey = "developerInstructions"

// DeveloperInstructionsMetadata carries an optional native instruction override.
// A nil pointer leaves instructions unchanged; an empty string clears them.
func DeveloperInstructionsMetadata(instructions *string) map[string]any {
	if instructions == nil {
		return nil
	}
	return map[string]any{developerInstructionsKey: *instructions}
}

// DeveloperInstructions parses the same contract declared by the adapter.
func DeveloperInstructions(meta map[string]any) (*string, error) {
	raw, exists := meta[developerInstructionsKey]
	if !exists {
		return nil, nil
	}
	value, ok := raw.(string)
	if !ok {
		return nil, fmt.Errorf("%s must be a string", developerInstructionsKey)
	}
	return &value, nil
}

// DeveloperInstructionsCapability declares native open operations accepting this metadata.
func DeveloperInstructionsCapability(forkSupported bool) map[string]any {
	return map[string]any{"metadataKey": developerInstructionsKey, "new": true, "resume": true, "load": true, "fork": forkSupported}
}

// CompactMethod starts a native compaction and returns its observed turn outcome.
const CompactMethod = "_session/compact"

// CompactRequest identifies the session whose native context should be compacted.
type CompactRequest struct {
	// SessionID identifies the existing native session.
	SessionID string `json:"session_id"`
}

// CompactResponse contains the native turn identity and terminal status.
type CompactResponse struct {
	// TurnID is the provider-issued turn identity, when reported.
	TurnID string `json:"turn_id,omitempty"`
	// Status is the native terminal status.
	Status string `json:"status"`
	// Meta carries invocation identity and other evidence observed during compaction.
	Meta map[string]any `json:"_meta,omitempty"`
}

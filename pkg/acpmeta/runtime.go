// Package acpmeta 定义 ACP 标准结构尚未覆盖的通用元数据约定。
package acpmeta

import "strings"

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

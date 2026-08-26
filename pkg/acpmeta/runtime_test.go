package acpmeta

import (
	"reflect"
	"testing"
)

// TestRuntimeVersionMetadataRoundTrip 验证 CLI 版本元数据可无损构造并读取。
func TestRuntimeVersionMetadataRoundTrip(t *testing.T) {
	t.Parallel()

	meta := RuntimeVersionMetadata(" 0.149.1 ")
	want := map[string]any{
		"runtime": map[string]any{"version": "0.149.1"},
	}
	if !reflect.DeepEqual(meta, want) {
		t.Fatalf("RuntimeVersionMetadata() = %#v，期望 %#v", meta, want)
	}
	if got := RuntimeVersion(meta); got != "0.149.1" {
		t.Fatalf("RuntimeVersion() = %q，期望 0.149.1", got)
	}
}

// TestRuntimeVersionRejectsMissingOrMalformedMetadata 验证缺失或畸形扩展不会被误认为版本。
func TestRuntimeVersionRejectsMissingOrMalformedMetadata(t *testing.T) {
	t.Parallel()

	tests := []struct {
		// name 描述当前无效元数据场景。
		name string
		// meta 是待解析的 ACP Implementation 元数据。
		meta map[string]any
	}{
		{name: "missing", meta: nil},
		{name: "runtime is not an object", meta: map[string]any{"runtime": "0.149.1"}},
		{name: "version is not a string", meta: map[string]any{"runtime": map[string]any{"version": 1491}}},
		{name: "blank version", meta: map[string]any{"runtime": map[string]any{"version": "  "}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := RuntimeVersion(test.meta); got != "" {
				t.Fatalf("RuntimeVersion() = %q，期望为空", got)
			}
		})
	}

	if meta := RuntimeVersionMetadata("  "); meta != nil {
		t.Fatalf("空版本元数据为 %#v，期望 nil", meta)
	}
}

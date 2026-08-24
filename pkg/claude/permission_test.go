package claude

import (
	"encoding/json"
	"testing"
)

// TestHasPermissionSuggestions 验证永久授权选项只接受非空 JSON 数组。
func TestHasPermissionSuggestions(t *testing.T) {
	tests := []struct {
		// name 是子测试名称。
		name string
		// value 是待校验的建议 JSON。
		value json.RawMessage
		// want 是期望校验结果。
		want bool
	}{
		{name: "有效数组", value: json.RawMessage(`[{"type":"addRules"}]`), want: true},
		{name: "空数组", value: json.RawMessage(`[]`)},
		{name: "null", value: json.RawMessage(`null`)},
		{name: "对象", value: json.RawMessage(`{"type":"addRules"}`)},
		{name: "空值", value: nil},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := hasPermissionSuggestions(test.value); got != test.want {
				t.Fatalf("hasPermissionSuggestions() = %v, want %v", got, test.want)
			}
		})
	}
}

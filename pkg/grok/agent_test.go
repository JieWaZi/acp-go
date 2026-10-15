package grok

import (
	"reflect"
	"testing"
)

// TestPermissionModesKeepNativeClassifierDistinct 验证 auto 不等于全权限，且不修改调用者元数据。
func TestPermissionModesKeepNativeClassifierDistinct(t *testing.T) {
	original := map[string]any{"trace": "keep", "autoMode": true, "yoloMode": true}
	for _, mode := range []string{"default", "auto", "full-access"} {
		actual := permissionMetadata(original, mode)
		if actual["trace"] != "keep" || actual["autoMode"] != (mode == "auto") || actual["yoloMode"] != (mode == "full-access") {
			t.Fatalf("%s: %#v", mode, actual)
		}
	}
	if !reflect.DeepEqual(original, map[string]any{"trace": "keep", "autoMode": true, "yoloMode": true}) {
		t.Fatal("caller metadata mutated")
	}
}

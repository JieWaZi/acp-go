package cursor

import (
	"github.com/JieWaZi/acp-go/pkg/nativeacp"
	"testing"
)

// TestExecutionModelArgument 验证官方模型标识与已确认参数原样进入恢复覆盖参数，禁止参数文本改变选择结构。
func TestExecutionModelArgument(t *testing.T) {
	selection := nativeacp.CursorModelSelection{ModelID: "composer-2.5"}
	if got, err := executionModelArgument(selection); err != nil || got != "composer-2.5" {
		t.Fatalf("model: %q %v", got, err)
	}
	selection = nativeacp.CursorModelSelection{ModelID: "claude-opus-4-8", Parameters: []nativeacp.CursorModelParameter{{ID: "effort", Value: "high"}, {ID: "fast", Value: "false"}}}
	if got, err := executionModelArgument(selection); err != nil || got != "claude-opus-4-8[effort=high,fast=false]" {
		t.Fatalf("parameters: %q %v", got, err)
	}
	selection.Parameters[0].Value = "high,fast=true"
	if _, err := executionModelArgument(selection); err == nil {
		t.Fatal("ambiguous parameter accepted")
	}
}

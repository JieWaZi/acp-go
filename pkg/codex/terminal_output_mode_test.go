package codex

import (
	"testing"

	acp "github.com/coder/acp-go-sdk"
)

// TestResolveTerminalOutputMode 验证 terminal output 能力判定表。
func TestResolveTerminalOutputMode(t *testing.T) {
	t.Parallel()

	tests := []struct {
		// name 描述客户端元数据形状。
		name string
		// meta 是 initialize ClientCapabilities._meta 的手工 fixture。
		meta map[string]any
		// want 是当前能力组合应选择的唯一输出键。
		want terminalOutputMode
	}{
		{name: "明确支持 full", meta: map[string]any{"terminal_output": true}, want: terminalOutputModeFull},
		{name: "明确关闭 full", meta: map[string]any{"terminal_output": false}, want: terminalOutputModeDelta},
		{name: "非布尔 full", meta: map[string]any{"terminal_output": "true"}, want: terminalOutputModeDelta},
		{name: "只声明 legacy", meta: map[string]any{"terminal_output_delta": true}, want: terminalOutputModeDelta},
		{name: "缺失元数据", meta: nil, want: terminalOutputModeDelta},
	}

	for _, fixture := range tests {
		t.Run(fixture.name, func(t *testing.T) {
			t.Parallel()
			got := resolveTerminalOutputMode(acp.ClientCapabilities{Meta: fixture.meta})
			if got != fixture.want {
				t.Fatalf("terminal output mode = %q，期望 %q", got, fixture.want)
			}
		})
	}
}

// TestCreateTerminalOutputMetaUsesExactlyOneNegotiatedKey 锁定两种模式的 wire 形状。
func TestCreateTerminalOutputMetaUsesExactlyOneNegotiatedKey(t *testing.T) {
	t.Parallel()

	assertMetaWire(
		t,
		createTerminalOutputMeta(terminalOutputModeFull, "terminal-1", "full\n"),
		`{"terminal_output":{"data":"full\n","terminal_id":"terminal-1"}}`,
	)
	assertMetaWire(
		t,
		createTerminalOutputMeta(terminalOutputModeDelta, "terminal-2", "delta\n"),
		`{"terminal_output_delta":{"data":"delta\n","terminal_id":"terminal-2"}}`,
	)
}

package nativeacp

import (
	"testing"

	acp "github.com/coder/acp-go-sdk"
)

// TestCursorExecutionSelection 保证官方模型参数原样传递，工作模式不会混入模型参数。
func TestCursorExecutionSelection(t *testing.T) {
	option := func(id, value, category string) acp.SessionConfigOption {
		return acp.SessionConfigOption{Select: &acp.SessionConfigOptionSelect{Id: acp.SessionConfigId(id), CurrentValue: acp.SessionConfigValueId(value), Category: acp.Ptr(acp.SessionConfigOptionCategory(category))}}
	}
	agent := &Agent{config: Config{CursorExtensions: true}, sessions: map[acp.SessionId]*sessionOptions{"s": {cursorNativeOptions: []acp.SessionConfigOption{option("mode", "agent", "mode"), option("model", "claude-sonnet-5", "model"), option("thinking", "true", "thought_level"), option("effort", "high", "thought_level"), option("fast", "false", "model_config")}}}}
	selection, err := agent.CursorExecutionModel("s")
	if err != nil {
		t.Fatal(err)
	}
	if selection.ModelID != "claude-sonnet-5" || len(selection.Parameters) != 3 || selection.Parameters[0].ID != "effort" || selection.Parameters[2].Value != "true" {
		t.Fatalf("selection=%+v", selection)
	}
	if _, err = agent.CursorExecutionModel("missing"); err == nil {
		t.Fatal("unknown session accepted")
	}
}

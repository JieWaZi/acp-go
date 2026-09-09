package cursor

import (
	"errors"
	acp "github.com/coder/acp-go-sdk"
	"testing"
)

// TestCursorApprovalHints 验证 Shell、MCP、删除工具共用快捷键检测，并避免向空闲输入框写入审批按键。
func TestCursorApprovalHints(t *testing.T) {
	for _, screen := range []string{"Run (once) (y)\nSkip & tell the agent", "Allow tool call (y/n)", "Delete (Y)\nKeep (n)"} {
		if !permissionScreen(screen) {
			t.Fatalf("missed approval: %s", screen)
		}
	}
	for _, screen := range []string{"Add a follow-up\nold result (y)", "Plan, search, build anything", "Tool pending without an accept key"} {
		if permissionScreen(screen) {
			t.Fatalf("unsafe approval screen: %s", screen)
		}
	}
}

// TestCursorPlanErrorMapping 验证官方套餐失败保留统一计费错误分类，未知错误不伪造分类。
func TestCursorPlanErrorMapping(t *testing.T) {
	var request *acp.RequestError
	err := cursorTurnError("Please upgrade your plan to use this model", "generation failed")
	if !errors.As(err, &request) {
		t.Fatalf("missing ACP error: %v", err)
	}
	data, ok := request.Data.(map[string]any)
	if !ok || data["errorKind"] != "billing_error" {
		t.Fatalf("wrong error: %+v", request)
	}
	if err := cursorTurnError("unknown failure", "generation failed"); err.Error() != "generation failed" {
		t.Fatalf("invented classification: %v", err)
	}
}

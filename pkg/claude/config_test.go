package claude

import (
	"testing"

	"github.com/JieWaZi/acp-go/pkg/claude/protocol"
	acp "github.com/coder/acp-go-sdk"
)

// TestPermissionModesGateBypassPermissions 验证危险模式只在进程安全门槛允许时公开。
func TestPermissionModesGateBypassPermissions(t *testing.T) {
	t.Parallel()

	if containsPermissionMode(permissionModes(false, false), "bypassPermissions") {
		t.Fatal("未授权进程公开了 bypassPermissions")
	}
	if !containsPermissionMode(permissionModes(false, true), "bypassPermissions") {
		t.Fatal("已授权进程未公开 bypassPermissions")
	}
}

// TestSessionConfigurationAdvertisesBypassPermissions 验证 Session mode/config 使用同一模式集合。
func TestSessionConfigurationAdvertisesBypassPermissions(t *testing.T) {
	t.Parallel()

	configuration := newSessionConfiguration(
		protocol.SystemInitMessage{PermissionMode: "default"},
		protocol.InitializeControlResponse{},
		true,
	)
	state := configuration.modeState()
	if state == nil || !containsSessionMode(state.AvailableModes, "bypassPermissions") {
		t.Fatalf("AvailableModes = %#v", state)
	}
	option := configuration.modeOption()
	if option.Select == nil || option.Select.Options.Ungrouped == nil {
		t.Fatalf("mode option = %#v", option)
	}
	found := false
	for _, item := range *option.Select.Options.Ungrouped {
		if item.Value == "bypassPermissions" {
			found = true
		}
	}
	if !found {
		t.Fatalf("mode option 未包含 bypassPermissions：%#v", option)
	}
}

// TestSessionConfigurationClampsUnavailableInitialMode 防止项目设置绕过进程安全门槛。
func TestSessionConfigurationClampsUnavailableInitialMode(t *testing.T) {
	t.Parallel()

	configuration := newSessionConfiguration(
		protocol.SystemInitMessage{PermissionMode: "bypassPermissions"},
		protocol.InitializeControlResponse{},
		false,
	)
	if configuration.mode != "default" {
		t.Fatalf("mode = %q", configuration.mode)
	}
}

// containsPermissionMode 判断内部模式定义中是否存在目标标识。
func containsPermissionMode(modes []permissionModeDefinition, id string) bool {
	for _, mode := range modes {
		if string(mode.ID) == id {
			return true
		}
	}
	return false
}

// containsSessionMode 判断 ACP 模式快照中是否存在目标标识。
func containsSessionMode(modes []acp.SessionMode, id string) bool {
	for _, mode := range modes {
		if string(mode.Id) == id {
			return true
		}
	}
	return false
}

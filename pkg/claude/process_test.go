package claude

import (
	"strings"
	"testing"
)

// TestClaudeProcessEnvUsesExplicitBase 验证 Session 环境基于调用方快照并覆盖 Adapter 固有变量。
func TestClaudeProcessEnvUsesExplicitBase(t *testing.T) {
	t.Parallel()

	base := []string{
		"HOME=/first",
		"NODE_OPTIONS=--inspect",
		"CLAUDE_CONFIG_DIR=/custom/config",
		"HOME=/current",
	}
	environment := claudeProcessEnv(base, map[string]string{"CUSTOM": "value"})
	values := environmentValues(environment)
	if values["HOME"] != "/current" ||
		values["CLAUDE_CONFIG_DIR"] != "/custom/config" ||
		values["CUSTOM"] != "value" {
		t.Fatalf("Session 环境为 %#v", values)
	}
	if _, exists := values["NODE_OPTIONS"]; exists {
		t.Fatal("Session 环境不应保留 NODE_OPTIONS")
	}
	if values["CLAUDE_CODE_ENTRYPOINT"] != "sdk-ts" ||
		values["CLAUDE_CODE_EMIT_SESSION_STATE_EVENTS"] != "1" {
		t.Fatalf("Adapter 固有环境为 %#v", values)
	}
	if got := claudeEnvironmentValue(base, "HOME"); got != "/current" {
		t.Fatalf("完整环境 HOME = %q", got)
	}
}

// environmentValues 把测试环境列表转换为便于断言的键值集合。
func environmentValues(environment []string) map[string]string {
	values := make(map[string]string, len(environment))
	for _, item := range environment {
		name, value, found := strings.Cut(item, "=")
		if found {
			values[name] = value
		}
	}
	return values
}

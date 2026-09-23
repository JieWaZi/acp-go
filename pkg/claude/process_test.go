package claude

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

// TestClaudeStderrKeepsRawCLIError 验证错误尾部与日志保留 CLI 原始内容。
func TestClaudeStderrKeepsRawCLIError(t *testing.T) {
	var output bytes.Buffer
	tail := &tailBuffer{limit: 1024}
	writer := &stderrWriter{tail: tail, logger: slog.New(slog.NewTextHandler(&output, nil))}
	const detail = "api_key=literal-secret\n"
	if _, err := writer.Write([]byte(detail)); err != nil {
		t.Fatal(err)
	}
	if tail.String() != detail || !strings.Contains(output.String(), detail[:len(detail)-1]) {
		t.Fatalf("CLI 原始错误被修改：tail=%q log=%q", tail.String(), output.String())
	}
}

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

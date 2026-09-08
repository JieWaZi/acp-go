package kimi

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JieWaZi/acp-go/pkg/nativeacp"
	"github.com/pelletier/go-toml/v2"
)

// TestKimiConfigIsolationAndPersistentSessions 验证隔离配置、权限和原生会话持久化。
func TestKimiConfigIsolationAndPersistentSessions(t *testing.T) {
	source, state, cwd := t.TempDir(), t.TempDir(), t.TempDir()
	original := "default_model = 'original'\ndefault_yolo = true\n[providers.test]\napi_key = 'private-value'\n"
	if err := os.WriteFile(filepath.Join(source, "config.toml"), []byte(original), 0600); err != nil {
		t.Fatal(err)
	}
	config := Config{Environment: []string{"KIMI_SHARE_DIR=" + source}, StateDirectory: state, WorkingDirectory: cwd, PermissionMode: "default"}
	directory, environment, err := isolatedEnvironment(config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(directory) })
	if nativeacp.EnvironmentValue(environment, "KIMI_SHARE_DIR") != directory {
		t.Fatal("CLI must use the isolated configuration")
	}
	data, err := os.ReadFile(filepath.Join(directory, "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	var values map[string]any
	if err := toml.Unmarshal(data, &values); err != nil {
		t.Fatal(err)
	}
	if values["default_yolo"] != false || values["default_model"] != "original" {
		t.Fatal("configuration or permission was lost")
	}
	// 模拟 Python CLI 的模型保存和原生会话写入。
	if err := os.WriteFile(filepath.Join(directory, "config.toml"), []byte("default_model='changed'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "sessions", "history"), []byte("retained"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(directory); err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile(filepath.Join(source, "config.toml"))
	if err != nil || string(data) != original {
		t.Fatal("model selection changed the real user configuration")
	}
	config.PermissionMode = "full-access"
	next, _, err := isolatedEnvironment(config)
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(next)
	data, err = os.ReadFile(filepath.Join(next, "sessions", "history"))
	if err != nil || string(data) != "retained" {
		t.Fatal("session history did not survive process replacement")
	}
	data, err = os.ReadFile(filepath.Join(next, "config.toml"))
	if err != nil || !strings.Contains(string(data), "default_yolo = true") {
		t.Fatal("full access did not reach the isolated CLI")
	}
}

// TestKimiRejectsInvalidConfigWithoutEchoingSecrets 验证无效配置不会导致崩溃或在错误中泄漏秘密。
func TestKimiRejectsInvalidConfigWithoutEchoingSecrets(t *testing.T) {
	for _, value := range []string{"null", `{"api_key":"private-value",}`} {
		source := t.TempDir()
		if err := os.WriteFile(filepath.Join(source, "config.json"), []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
		_, _, err := isolatedEnvironment(Config{Environment: []string{"KIMI_SHARE_DIR=" + source}, StateDirectory: t.TempDir(), WorkingDirectory: t.TempDir()})
		if err == nil || strings.Contains(err.Error(), "private-value") {
			t.Fatalf("expected safe configuration error: %v", err)
		}
	}
}

package gemini

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/JieWaZi/acp-go/pkg/nativeacp"
)

// TestThinkingDefaultOverlayPreservesSnapshot 验证默认配置副本保留用户设置而不引入思考覆盖。
func TestThinkingDefaultOverlayPreservesSnapshot(t *testing.T) {
	home := t.TempDir()
	if err := os.Mkdir(filepath.Join(home, ".gemini"), 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, ".gemini", "settings.json")
	original := []byte(`{"security":{"auth":{"selectedType":"gemini-api-key"}},"modelConfigs":{"customAliases":{"mine":{"modelConfig":{"model":"custom-model"}}},"customOverrides":[{"match":{"model":"gemini-3.5-flash"},"modelConfig":{"generateContentConfig":{"temperature":0.42,"thinkingConfig":{"thinkingBudget":4096}}}}]}}`)
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	environment := []string{"GEMINI_CLI_HOME=" + home, "GEMINI_API_KEY=private-key"}
	directory, actual, err := thinkingEnvironment(environment)
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(directory)
	overlay := filepath.Join(nativeacp.EnvironmentValue(actual, "GEMINI_CLI_HOME"), ".gemini", "settings.json")
	info, err := os.Stat(overlay)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("overlay permissions %v", info.Mode())
	}
	if nativeacp.EnvironmentValue(actual, "GEMINI_API_KEY") != "private-key" {
		t.Fatalf("preserved environment: %#v", actual)
	}
	data, err := os.ReadFile(path)
	if err != nil || !reflect.DeepEqual(data, original) {
		t.Fatal("original settings changed")
	}
	data, err = os.ReadFile(overlay)
	if err != nil {
		t.Fatal(err)
	}
	var values map[string]any
	if err := json.Unmarshal(data, &values); err != nil {
		t.Fatal(err)
	}
	configs := values["modelConfigs"].(map[string]any)
	if configs["customAliases"].(map[string]any)["mine"] == nil || values["security"] == nil {
		t.Fatal("original settings lost")
	}
	var originalValues map[string]any
	if err := json.Unmarshal(original, &originalValues); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(configs, originalValues["modelConfigs"]) {
		t.Fatalf("default model settings changed: %#v", configs)
	}
}

// TestThinkingCapabilitySnapshotDoesNotGuess 验证未知、auto 与不支持关闭思考的模型不会获得虚构配置。
func TestThinkingCapabilitySnapshotDoesNotGuess(t *testing.T) {
	for _, model := range []string{"auto", "auto-gemini-3", "gemini-3.5-flash", "gemini-3.5-flash-custom", "private-gemini-3.5-flash"} {
		if len(thinkingPresets(model)) != 0 {
			t.Fatalf("guessed capability for %s", model)
		}
	}
	for _, preset := range thinkingPresets("gemini-2.5-pro") {
		if preset.id == "off" || preset.thinking["thinkingLevel"] != nil {
			t.Fatalf("unsupported pro preset: %+v", preset)
		}
	}

}

// TestDefaultProfileIsReadOnlyAndStatePersists 验证默认用户 profile 不被写入，受管状态跨配置副本保留。
func TestDefaultProfileIsReadOnlyAndStatePersists(t *testing.T) {
	home := t.TempDir()
	profile := filepath.Join(home, ".gemini")
	if err := os.Mkdir(profile, 0700); err != nil {
		t.Fatal(err)
	}
	credentials := []byte(`{"access_token":"read-only-fixture"}`)
	if err := os.WriteFile(filepath.Join(profile, "oauth_creds.json"), credentials, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(profile, "settings.json"), []byte("{ // official JSON comments\n\"modelConfigs\":{\"customAliases\":{}}}"), 0600); err != nil {
		t.Fatal(err)
	}
	state := t.TempDir()
	environment := []string{"HOME=" + home}
	directory, _, err := thinkingEnvironmentForState(environment, state)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(profile, "tmp")); !os.IsNotExist(err) {
		t.Fatal("default user profile got persistent state writes")
	}
	data, err := os.ReadFile(filepath.Join(state, ".gemini", "oauth_creds.json"))
	if err != nil || !reflect.DeepEqual(data, credentials) {
		t.Fatalf("credential snapshot: %s %v", data, err)
	}
	history := filepath.Join(state, ".gemini", "tmp", "persistent-history")
	if err := os.WriteFile(history, []byte("owned history"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(directory); err != nil {
		t.Fatal(err)
	}
	second, _, err := thinkingEnvironmentForState(environment, state)
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(second)
	data, err = os.ReadFile(filepath.Join(second, ".gemini", "tmp", "persistent-history"))
	if err != nil || string(data) != "owned history" {
		t.Fatalf("lost native state: %s %v", data, err)
	}
	data, err = os.ReadFile(filepath.Join(profile, "oauth_creds.json"))
	if err != nil || !reflect.DeepEqual(data, credentials) {
		t.Fatal("original credentials overwritten")
	}
}

// TestCanonicalOverrideAndDefault 验证规范模型覆盖清空互斥字段，default 恢复完整原始设置。
func TestCanonicalOverrideAndDefault(t *testing.T) {
	home := t.TempDir()
	profile := filepath.Join(home, ".gemini")
	if err := os.Mkdir(profile, 0700); err != nil {
		t.Fatal(err)
	}
	original := []byte(`{"modelConfigs":{"customOverrides":[{"match":{"model":"gemini-3.8-flash"},"modelConfig":{"generateContentConfig":{"temperature":0.42,"thinkingConfig":{"thinkingBudget":4096}}}}]}}`)
	if err := os.WriteFile(filepath.Join(profile, "settings.json"), original, 0600); err != nil {
		t.Fatal(err)
	}
	state, err := newProfileState([]string{"GEMINI_CLI_HOME=" + home}, "", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, choice := range []string{"low", "default"} {
		directory, _, err := state.environment([]string{}, "gemini-3.8-flash", choice)
		if err != nil {
			t.Fatal(err)
		}
		defer os.RemoveAll(directory)
		data, err := os.ReadFile(filepath.Join(directory, ".gemini", "settings.json"))
		if err != nil {
			t.Fatal(err)
		}
		values := map[string]any{}
		if err := json.Unmarshal(data, &values); err != nil {
			t.Fatal(err)
		}
		overrides := values["modelConfigs"].(map[string]any)["customOverrides"].([]any)
		if choice == "default" {
			if len(overrides) != 1 {
				t.Fatal("default retained adapter overrides")
			}
			continue
		}
		if len(overrides) != 3 {
			t.Fatalf("canonical override count: %d", len(overrides))
		}
		for _, value := range overrides[1:] {
			if value.(map[string]any)["match"].(map[string]any)["model"] != "gemini-3.8-flash" {
				t.Fatal("override used an alias")
			}
		}
		reset := overrides[1].(map[string]any)["modelConfig"].(map[string]any)["generateContentConfig"].(map[string]any)
		selected := overrides[2].(map[string]any)["modelConfig"].(map[string]any)["generateContentConfig"].(map[string]any)["thinkingConfig"].(map[string]any)
		if reset["thinkingConfig"] != nil || selected["thinkingBudget"] != nil || selected["thinkingLevel"] != "LOW" {
			t.Fatal("mutually exclusive thinking configuration broken")
		}
	}
}

// TestCredentialRefreshSurvivesOverlayCleanup 验证新凭据与原生原子替换能共享持久状态且不会被旧进程回写。
func TestCredentialRefreshSurvivesOverlayCleanup(t *testing.T) {
	home := t.TempDir()
	stateDirectory := t.TempDir()
	state, err := newProfileState([]string{"HOME=" + home}, stateDirectory, "")
	if err != nil {
		t.Fatal(err)
	}
	first, _, err := state.environment([]string{}, "", "default")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(first)
	credential := filepath.Join(first, ".gemini", "oauth_creds.json")
	if err := os.WriteFile(credential, []byte("new credential"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := state.persistOverlay(first); err != nil {
		t.Fatal(err)
	}
	second, _, err := state.environment([]string{}, "", "default")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(second)
	replacement := filepath.Join(second, ".gemini", "credential.tmp")
	if err := os.WriteFile(replacement, []byte("refreshed credential"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, filepath.Join(second, ".gemini", "oauth_creds.json")); err != nil {
		t.Fatal(err)
	}
	if err := state.persistOverlay(second); err != nil {
		t.Fatal(err)
	}
	if err := state.persistOverlay(first); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(first); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(second); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(state.root, "oauth_creds.json"))
	if err != nil || string(data) != "refreshed credential" {
		t.Fatalf("lost refresh: %s %v", data, err)
	}
	if _, err := os.Stat(filepath.Join(home, ".gemini")); !os.IsNotExist(err) {
		t.Fatal("default profile was written")
	}
}

// TestEmptyEnvironmentAndPrivateState 验证显式空环境不会带入父环境并使用私有配置权限。
func TestEmptyEnvironmentAndPrivateState(t *testing.T) {
	t.Setenv("GEMINI_PARENT_SENTINEL", "secret")
	state, err := newProfileState([]string{"HOME=" + t.TempDir()}, t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	directory, environment, err := state.environment([]string{}, "", "default")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(directory)
	if len(environment) != 1 || nativeacp.EnvironmentValue(environment, "GEMINI_PARENT_SENTINEL") != "" {
		t.Fatalf("inherited explicit-empty environment: %v", environment)
	}
	info, err := os.Stat(filepath.Join(directory, ".gemini", "settings.json"))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("settings mode: %v %v", info, err)
	}
	info, err = os.Stat(state.root)
	if err != nil || info.Mode().Perm() != 0700 {
		t.Fatalf("state mode: %v %v", info, err)
	}
}

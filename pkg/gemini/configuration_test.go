package gemini

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/JieWaZi/acp-go/pkg/nativeacp"
)

// TestThinkingOverlayPreservesDefaultsAndCleansConflict 验证真实配置覆盖顺序与用户原配置保持不变。
func TestThinkingOverlayPreservesDefaultsAndCleansConflict(t *testing.T) {
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
	overrides := configs["customOverrides"].([]any)
	for index := 1; index < len(overrides); index += 2 {
		first := overrides[index].(map[string]any)
		second := overrides[index+1].(map[string]any)
		if !reflect.DeepEqual(first["match"], second["match"]) {
			t.Fatal("override order lost")
		}
		config := first["modelConfig"].(map[string]any)["generateContentConfig"].(map[string]any)
		if config["thinkingConfig"] != nil {
			t.Fatal("old budget/level not cleared")
		}
	}
}

// TestThinkingCapabilitySnapshotDoesNotGuess 验证未知、auto 与不支持关闭思考的模型不会获得虚构配置。
func TestThinkingCapabilitySnapshotDoesNotGuess(t *testing.T) {
	for _, model := range []string{"auto", "auto-gemini-3", "gemini-3.5-flash-custom", "private-gemini-3.5-flash"} {
		if len(thinkingPresets(model)) != 0 {
			t.Fatalf("guessed capability for %s", model)
		}
	}
	for _, preset := range thinkingPresets("gemini-2.5-pro") {
		if preset.id == "off" || preset.thinking["thinkingLevel"] != nil {
			t.Fatalf("unsupported pro preset: %+v", preset)
		}
	}
	for _, model := range supportedThinkingModels() {
		for _, preset := range thinkingPresets(model) {
			actual, current, ok := decodeThinkingAlias(thinkingAlias(model, preset.id))
			if !ok || actual != model || current != preset.id {
				t.Fatalf("alias restore: %s %s", actual, current)
			}
		}
	}
}

// TestOfficialResolverAndSDK 验证生成的每个预设经原始官方 resolver 和 SDK 序列化后的实际参数。
func TestOfficialResolverAndSDK(t *testing.T) {
	proof := os.Getenv("GEMINI_OFFICIAL_PROOF_DIRECTORY")
	if proof == "" {
		t.Skip("opt-in pinned official npm core/SDK verification; see UPSTREAM.md")
	}
	directory, environment, err := thinkingEnvironment([]string{"GEMINI_CLI_HOME=" + t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(directory)
	command := exec.Command("node", "testdata/verify-thinking.mjs", proof, filepath.Join(nativeacp.EnvironmentValue(environment, "GEMINI_CLI_HOME"), ".gemini", "settings.json"))
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("official resolver/SDK: %v\n%s", err, output)
	}
	t.Log(string(output))
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

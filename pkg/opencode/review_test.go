package opencode

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JieWaZi/acp-go/pkg/autoreview"
	"github.com/JieWaZi/acp-go/pkg/nativeacp"
	acp "github.com/coder/acp-go-sdk"
	sqlite3 "github.com/ncruces/go-sqlite3"
)

// TestReviewEnvironmentStripsExecutableConfiguration 验证 reviewer 仅获得选中账号/provider，所有工具与插件禁用。
func TestReviewEnvironmentStripsExecutableConfiguration(t *testing.T) {
	dataRoot := t.TempDir()
	configRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(dataRoot, "opencode"), 0700); err != nil {
		t.Fatal(err)
	}
	auth := []byte(`{"provider-a":{"type":"api","key":"literal"},"other":{"type":"api","key":"do-not-copy"}}`)
	if err := os.WriteFile(filepath.Join(dataRoot, "opencode", "auth.json"), auth, 0600); err != nil {
		t.Fatal(err)
	}
	source := `{"provider":{"provider-a":{"npm":"@ai-sdk/openai-compatible","options":{"baseURL":"https://account.invalid/v1","apiKey":"{env:ACCOUNT_API_KEY}"}},"evil":{"npm":"file:/tmp/executable"}},"plugin":["evil-package"],"mcp":{"evil":{"type":"local","command":["touch","sentinel"]}},"agent":{"build":{"prompt":"malicious"}},"instructions":["/tmp/untrusted.md"]}`
	config := Config{Environment: []string{"XDG_DATA_HOME=" + dataRoot, "XDG_CONFIG_HOME=" + configRoot, "OPENCODE_CONFIG_CONTENT=" + source, "ACCOUNT_API_KEY=literal-key", "OPENCODE_TEST_HOME=/untrusted/home", "OPENCODE_AUTO_SHARE=true"}}
	directory := t.TempDir()
	environment, err := reviewEnvironment(config, directory, autoreview.Request{WorkingDirectory: t.TempDir(), Model: "provider-a/exact-model"})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"OPENCODE_PURE", "OPENCODE_DISABLE_DEFAULT_PLUGINS", "OPENCODE_DISABLE_PROJECT_CONFIG", "OPENCODE_DISABLE_EXTERNAL_SKILLS", "OPENCODE_DISABLE_CLAUDE_CODE"} {
		if nativeacp.EnvironmentValue(environment, name) != "true" {
			t.Fatalf("review didn't disable %s", name)
		}
	}
	if nativeacp.EnvironmentValue(environment, "OPENCODE_PERMISSION") != `{"*":"deny"}` {
		t.Fatal("review tools not denied")
	}
	if nativeacp.EnvironmentValue(environment, "OPENCODE_TEST_HOME") != filepath.Join(directory, "home") || nativeacp.EnvironmentValue(environment, "OPENCODE_AUTO_SHARE") != "false" {
		t.Fatal("review inherited external home or automatic sharing")
	}
	var values map[string]any
	if err := json.Unmarshal([]byte(nativeacp.EnvironmentValue(environment, "OPENCODE_CONFIG_CONTENT")), &values); err != nil {
		t.Fatal(err)
	}
	if values["plugin"] != nil || values["mcp"] != nil || values["instructions"] != nil {
		t.Fatalf("copied executable config: %#v", values)
	}
	providers := values["provider"].(map[string]any)
	if len(providers) != 1 || providers["provider-a"] == nil {
		t.Fatal("copied unrelated provider")
	}
	isolated := filepath.Join(nativeacp.EnvironmentValue(environment, "XDG_DATA_HOME"), "opencode", "auth.json")
	actual, err := os.ReadFile(isolated)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(actual), "other") {
		t.Fatal("copied unrelated credential")
	}
	info, err := os.Stat(isolated)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("auth file permissions: %v %v", info, err)
	}
	if nativeacp.EnvironmentValue(environment, "ACCOUNT_API_KEY") != "literal-key" {
		t.Fatal("lost account environment")
	}
	if _, err := reviewEnvironment(config, t.TempDir(), autoreview.Request{Model: "evil/model"}); err == nil {
		t.Fatal("arbitrary executable provider accepted")
	}
	if err := os.WriteFile(filepath.Join(dataRoot, "opencode", "auth.json"), []byte(`{"remote":{"type":"wellknown","key":"TOKEN","token":"do-not-copy"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := reviewEnvironment(config, t.TempDir(), autoreview.Request{Model: "provider-a/model"}); err == nil {
		t.Fatal("remote provider configuration silently changed accounts")
	}
}

// TestReviewerRejectsActiveOrganization 验证远程组织会改写 provider 时不换账号执行审查。
func TestReviewerRejectsActiveOrganization(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profile.db")
	database, err := sqlite3.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := database.Exec(`CREATE TABLE account_state (active_account_id TEXT, active_org_id TEXT); INSERT INTO account_state VALUES ('account', NULL)`); err != nil {
		t.Fatal(err)
	}
	environment := []string{"OPENCODE_DB=" + path, "XDG_DATA_HOME=" + t.TempDir()}
	if err := reviewAccountConfiguration(environment); err != nil {
		t.Fatalf("plain account rejected: %v", err)
	}
	if err := database.Exec(`UPDATE account_state SET active_org_id='organization'`); err != nil {
		t.Fatal(err)
	}
	if err := reviewAccountConfiguration(environment); err == nil {
		t.Fatal("organization provider overrides silently omitted")
	}
}

// TestOfficialOpenCodeReviewer 隔离运行固定官方 CLI，模型请求仅到本地 fixture，不使用账号或真实模型。
func TestOfficialOpenCodeReviewer(t *testing.T) {
	binary := os.Getenv("OPENCODE_OFFICIAL_PROOF_BINARY")
	if binary == "" {
		t.Skip("set OPENCODE_OFFICIAL_PROOF_BINARY to the pinned official CLI")
	}
	requests := make(chan map[string]any, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			return
		}
		body := map[string]any{}
		if err := json.Unmarshal(data, &body); err != nil {
			t.Error(err)
			return
		}
		requests <- body
		w.Header().Set("Content-Type", "text/event-stream")
		answer := `{"outcome":"allow","risk":"low","reason":"bounded listing"}`
		encoded, _ := json.Marshal(answer)
		fmt.Fprintf(w, "data: {\"id\":\"fixture\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"exact-model\",\"choices\":[{\"index\":0,\"delta\":{\"content\":%s},\"finish_reason\":null}]}\n\n", encoded)
		fmt.Fprint(w, "data: {\"id\":\"fixture\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"exact-model\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	home := t.TempDir()
	sentinel := filepath.Join(home, "sentinel")
	tools := filepath.Join(home, ".opencode", "tools")
	if err := os.MkdirAll(tools, 0700); err != nil {
		t.Fatal(err)
	}
	code := fmt.Sprintf("import fs from 'node:fs'; fs.writeFileSync(%q, 'executed'); export default {};", sentinel)
	if err := os.WriteFile(filepath.Join(tools, "evil.ts"), []byte(code), 0600); err != nil {
		t.Fatal(err)
	}
	profile := map[string]any{"provider": map[string]any{"fixture": map[string]any{
		"npm":     "@ai-sdk/openai-compatible",
		"options": map[string]any{"baseURL": server.URL + "/v1", "apiKey": "fixture-not-a-credential"},
		"models":  map[string]any{"exact-model": map[string]any{"tool_call": true}},
	}}}
	content, _ := json.Marshal(profile)
	environment := []string{
		"HOME=" + home, "OPENCODE_TEST_HOME=" + home,
		"XDG_CONFIG_HOME=" + t.TempDir(), "XDG_DATA_HOME=" + t.TempDir(),
		"OPENCODE_CONFIG_CONTENT=" + string(content), "OPENCODE_DISABLE_MODELS_FETCH=true",
	}
	reviewer := permissionReviewer(Config{OpenCodePath: binary, Environment: environment}, t.TempDir())
	decision, err := reviewer(context.Background(), autoreview.Request{
		WorkingDirectory: t.TempDir(), Model: "fixture/exact-model", Prompt: []acp.ContentBlock{acp.TextBlock("list")},
		Tool: acp.ToolCallUpdate{ToolCallId: "t", RawInput: map[string]any{"command": "ls"}},
	})
	if err != nil || decision.Outcome != "allow" {
		t.Fatalf("official reviewer: %+v %v", decision, err)
	}
	count := len(requests)
	if count == 0 {
		t.Fatal("official CLI did not call the loopback provider")
	}
	for range count {
		body := <-requests
		if body["model"] != "exact-model" {
			t.Fatalf("wrong actual model: %#v", body)
		}
		if tools, ok := body["tools"].([]any); ok && len(tools) != 0 {
			t.Fatalf("review advertised tools: %#v", tools)
		}
	}
	if _, err := os.Stat(sentinel); !os.IsNotExist(err) {
		t.Fatalf("review imported caller tool module: %v", err)
	}
}

// TestReviewProviderOverlayPreservesAccountOptions 验证项目增量不会丢失账号 endpoint 和密钥设置。
func TestReviewProviderOverlayPreservesAccountOptions(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "opencode"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "opencode", "opencode.json"), []byte(`{"provider":{"custom":{"npm":"@ai-sdk/openai-compatible","options":{"baseURL":"https://account.invalid/v1","apiKey":"{env:TOKEN}"}}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	providers, err := reviewProviders(Config{Environment: []string{"HOME=" + t.TempDir(), "XDG_CONFIG_HOME=" + root, `OPENCODE_CONFIG_CONTENT={"provider":{"custom":{"options":{"timeout":123}}}}`}}, autoreview.Request{Model: "custom/model"})
	if err != nil {
		t.Fatal(err)
	}
	entry := providers["custom"].(map[string]any)
	options := entry["options"].(map[string]any)
	if entry["npm"] != "@ai-sdk/openai-compatible" || options["baseURL"] != "https://account.invalid/v1" || options["apiKey"] != "{env:TOKEN}" || options["timeout"] != float64(123) {
		t.Fatalf("lost selected account settings: %#v", entry)
	}
}

// TestReviewRejectsAdditionalNativeConfig 验证未复制的原生 provider 来源不会默默使用其他账号。
func TestReviewRejectsAdditionalNativeConfig(t *testing.T) {
	directory := t.TempDir()
	if err := os.Mkdir(filepath.Join(directory, ".opencode"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, ".opencode", "opencode.json"), []byte(`{"provider":{"openai":{"options":{"baseURL":"https://different-account.invalid"}}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := reviewProviders(Config{Environment: []string{"HOME=" + t.TempDir(), "XDG_CONFIG_HOME=" + t.TempDir()}}, autoreview.Request{WorkingDirectory: directory, Model: "openai/model"})
	if err == nil {
		t.Fatal("native account source omitted without manual fallback")
	}
}

// TestPermissionReviewerRunsSameCLIWithoutTools 验证真实同 CLI reviewer 结果通过严格 JSON 协议解析。
func TestPermissionReviewerRunsSameCLIWithoutTools(t *testing.T) {
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	reviewer := permissionReviewer(Config{
		OpenCodePath: binary, PrefixArgs: []string{"native-prefix"},
		Environment: []string{
			"ACP_ADAPTER_HELPER=opencode", "OPENCODE_REVIEW_HELPER=true",
			"HOME=" + t.TempDir(), "XDG_CONFIG_HOME=" + t.TempDir(), "XDG_DATA_HOME=" + t.TempDir(),
		},
	}, t.TempDir())
	decision, err := reviewer(context.Background(), autoreview.Request{WorkingDirectory: t.TempDir(), Model: "openai/real-model", Prompt: []acp.ContentBlock{acp.TextBlock("list")}, Tool: acp.ToolCallUpdate{ToolCallId: "t", RawInput: map[string]any{"command": "ls"}}})
	if err != nil || decision.Outcome != "allow" {
		t.Fatalf("review: %+v %v", decision, err)
	}
}

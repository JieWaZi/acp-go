package codex_test

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	agentclient "github.com/JieWaZi/acp-go/pkg/agentclient"
	"github.com/JieWaZi/acp-go/pkg/codex"
	acp "github.com/JieWaZi/acp-go/sdk"
	"github.com/pelletier/go-toml/v2"
)

func TestAccountStartupConfigurationFixture(t *testing.T) {
	if os.Getenv("CODEX_ACCOUNT_LAUNCH_FIXTURE") == "" {
		return
	}
	encoded, _ := json.Marshal(os.Args)
	_ = os.WriteFile(os.Getenv("CODEX_ACCOUNT_LAUNCH_FIXTURE"), encoded, 0600)
	scanner := bufio.NewScanner(os.Stdin)
	out := json.NewEncoder(os.Stdout)
	for scanner.Scan() {
		var request struct {
			// ID preserves the native request correlation.
			ID json.RawMessage
			// Method selects the native request fixture.
			Method string
		}
		if json.Unmarshal(scanner.Bytes(), &request) != nil {
			os.Exit(2)
		}
		if len(request.ID) == 0 {
			continue
		}
		body := map[string]any{}
		if request.Method == "initialize" {
			body["userAgent"] = "codex/0.163.0"
		}
		_ = out.Encode(map[string]any{"id": request.ID, "result": body})
	}
	os.Exit(0)
}

func TestStartupConfigurationEncodesNativeTOMLWithoutCredentialText(t *testing.T) {
	dir := t.TempDir()
	trace := filepath.Join(dir, "args.json")
	config := map[string]json.RawMessage{
		"model_provider":               json.RawMessage(`"neux_managed"`),
		"model_providers.neux_managed": json.RawMessage(`{"name":"Fixture","base_url":"https://fixture.invalid/v1","env_key":"NEUX_CODEX_ACCOUNT_API_KEY","wire_api":"responses","requires_openai_auth":false}`),
	}
	client, err := codex.NewClient(t.Context(), codex.Options{Executable: os.Args[0], PrefixArgs: []string{"-test.run=^TestAccountStartupConfigurationFixture$", "--"}, Environment: append(os.Environ(), "CODEX_ACCOUNT_LAUNCH_FIXTURE="+trace), WorkingDirectory: dir, StartupConfig: config}, agentclient.HostCallbacks{})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close(t.Context())
	// The constructor must snapshot the launch rather than borrow mutable input.
	config["model_provider"] = json.RawMessage(`"changed"`)
	if _, err := client.Initialize(t.Context(), acp.InitializeRequest{ProtocolVersion: 1}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(trace)
	if err != nil {
		t.Fatal(err)
	}
	var args []string
	if json.Unmarshal(raw, &args) != nil {
		t.Fatal("invalid launch trace")
	}
	var overrides []string
	for i, arg := range args {
		if arg == "-c" && i+1 < len(args) {
			overrides = append(overrides, args[i+1])
		}
	}
	var actual map[string]any
	if err := toml.Unmarshal([]byte(strings.Join(overrides, "\n")), &actual); err != nil {
		t.Fatal(err)
	}
	if actual["model_provider"] != "neux_managed" {
		t.Fatalf("wrong selected provider: %v", actual)
	}
	providers, ok := actual["model_providers"].(map[string]any)
	if !ok {
		t.Fatalf("wrong native table: %v", actual)
	}
	selected, ok := providers["neux_managed"].(map[string]any)
	if !ok || selected["base_url"] != "https://fixture.invalid/v1" || selected["env_key"] != "NEUX_CODEX_ACCOUNT_API_KEY" {
		t.Fatalf("wrong native provider: %v", providers)
	}
}

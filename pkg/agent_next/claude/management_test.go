package claude_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JieWaZi/acp-go/pkg/acpmeta"
	agentclient "github.com/JieWaZi/acp-go/pkg/agent_next"
	"github.com/JieWaZi/acp-go/pkg/agent_next/claude"
	acp "github.com/JieWaZi/acp-go/sdk"
)

func TestNativeSettingsAndAccountProbeWithoutPrompt(t *testing.T) {
	client, input, ctx := start(t, agentclient.HostCallbacks{})
	models, err := client.ReadModels(ctx)
	if err != nil || models.Options.Ungrouped == nil || len(*models.Options.Ungrouped) != 2 {
		t.Fatalf("models = %+v, %v", models, err)
	}
	account, err := client.ReadAccount(ctx)
	if err != nil || account.Account == nil || account.Account.Kind != "subscription" || account.Account.Plan == nil || *account.Account.Plan != "pro" {
		t.Fatalf("account = %+v, %v", account, err)
	}
	quota, err := client.ReadAccountUsage(ctx)
	if err != nil || quota.Available == nil || !*quota.Available || len(quota.Buckets) != 1 || len(quota.Buckets[0].Windows) != 1 || *quota.Buckets[0].Windows[0].UsedPercent != 0 {
		t.Fatalf("quota = %+v, %v", quota, err)
	}
	set := func(id, value string) []acp.SessionConfigOption {
		result, err := client.SetSessionConfigOption(ctx, acp.SetSessionConfigOptionRequest{ValueId: &acp.SetSessionConfigOptionValueId{
			SessionId: client.id, ConfigId: acp.SessionConfigId(id), Value: acp.SessionConfigValueId(value),
		}})
		if err != nil {
			t.Fatal(err)
		}
		return result.ConfigOptions
	}
	assert := func(options []acp.SessionConfigOption, id, value string) {
		t.Helper()
		for _, opt := range options {
			if row := opt.Select; row != nil && string(row.Id) == id {
				if string(row.CurrentValue) != value {
					t.Fatalf("%s = %+v, want %q", id, row, value)
				}
				return
			}
		}
		t.Fatalf("option %q missing", id)
	}
	assert(set("effort", "high"), "effort", "high")
	assert(set("fast", "on"), "fast", "on")
	assert(set("model", "opus"), "model", "opus")
	if _, err := client.SetSessionMode(ctx, acp.SetSessionModeRequest{SessionId: client.id, ModeId: "plan"}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Authenticate(ctx, acp.AuthenticateRequest{MethodId: "claudeai"}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Logout(ctx, acp.LogoutRequest{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(input); !os.IsNotExist(err) {
		t.Fatalf("management submitted a native prompt: %v", err)
	}
}

func TestLoadAndForkUseNativeTranscriptWithoutUsageReplay(t *testing.T) {
	text := make(chan string, 16)
	client, input, ctx := start(t, agentclient.HostCallbacks{Observe: func(_ context.Context, scope agentclient.Scope, n acp.SessionNotification) error {
		if scope.NativeInvocationID == "" {
			if chunk := n.Update.UserMessageChunk; chunk != nil && chunk.Content.Text != nil {
				text <- chunk.Content.Text.Text
			}
		}
		return nil
	}})
	dir := filepath.Dir(input)
	cwd, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	transcript := fixtureProject(dir, cwd)
	if err := os.MkdirAll(transcript, 0700); err != nil {
		t.Fatal(err)
	}
	saved := fmt.Sprintf("{\"type\":\"user\",\"sessionId\":%q,\"uuid\":\"a5be7014-fb83-4fee-92d8-391ab87393c3\",\"message\":{\"role\":\"user\",\"content\":\"saved question\"}}\n", client.id)
	if err := os.WriteFile(filepath.Join(transcript, string(client.id)+".jsonl"), []byte(saved), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := client.LoadSession(ctx, acp.LoadSessionRequest{SessionId: client.id, Cwd: cwd, Meta: acpmeta.DeveloperInstructionsMetadata(acp.Ptr("stable instruction"))}); err != nil {
		t.Fatal(err)
	}
	if got := receive(t, ctx, text); got != "saved question" {
		t.Fatal(got)
	}
	launch := string(marker(t, ctx, filepath.Join(dir, "launch")))
	if !strings.Contains(launch, "strict=true snapshot=true instructions=stable instruction mode=default") {
		t.Fatalf("native launch = %q", launch)
	}
	page, err := client.ReadNativeOutcomes(ctx, acpmeta.NativeTurnReceiptsRequest{SessionID: string(client.id)}, false)
	if err != nil || len(page.Outcomes) != 0 {
		t.Fatalf("history produced consumption: %+v, %v", page, err)
	}
	req := acp.UnstableForkSessionRequest{SessionId: client.id, Cwd: cwd, Meta: map[string]any{
		"sourceCwd": cwd, "forkReceiptDirectory": filepath.Join(dir, "fork-receipt"),
	}}
	branch, err := client.UnstableForkSession(ctx, req)
	if err != nil || branch.SessionId == "" || branch.SessionId == client.id {
		t.Fatalf("fork = %+v, %v", branch, err)
	}
	req.Meta["reconcileOnly"] = true
	recovered, err := client.UnstableForkSession(ctx, req)
	if err != nil || recovered.SessionId != branch.SessionId {
		t.Fatalf("reconciled = %+v, %v", recovered, err)
	}
	if _, err := os.Stat(input); !os.IsNotExist(err) {
		t.Fatalf("history operation submitted input: %v", err)
	}
}

func TestCompactUsesNativeManualBoundary(t *testing.T) {
	client, _, ctx := start(t, agentclient.HostCallbacks{})
	result, err := client.Compact(ctx, acpmeta.CompactRequest{SessionID: string(client.id)})
	if err != nil || result.Status != acpmeta.NativeTurnCompleted {
		t.Fatalf("compact = %+v, %v", result, err)
	}
}

func TestPluginLaunchUsesOnlyOneNativeDelivery(t *testing.T) {
	for _, tc := range []struct {
		name      string
		mode      string
		workspace bool
		admit     bool
	}{
		{"argv without an acknowledgement", "", false, true},
		{"initialize with an acknowledgement", "rereview-plugin-ack", true, true},
		{"initialize rejected by native peer", "rereview-plugin-reject", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plugin, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			client, dir, ctx := startClientMode(t, agentclient.HostCallbacks{}, tc.mode, func(o *claude.Options) {
				o.Launch.PluginDirectories = []string{plugin}
				o.Launch.WorkspaceOnly = tc.workspace
			})
			reply, err := client.NewSession(ctx, acp.NewSessionRequest{Cwd: dir})
			if tc.admit != (err == nil && reply.SessionId != "") {
				t.Fatalf("plugin launch = %+v, %v", reply, err)
			}
			want := fmt.Sprintf("await=false argv=[%s] payload=[]", plugin)
			if tc.workspace {
				want = fmt.Sprintf("await=true argv=[] payload=[%s]", plugin)
			}
			if got := string(marker(t, ctx, filepath.Join(dir, "plugin-delivery"))); got != want {
				t.Fatalf("delivery = %q, want %q", got, want)
			}
		})
	}
}

func TestLegacyModelAliasesPreserveNativeSettings(t *testing.T) {
	client, dir, ctx := startClientMode(t, agentclient.HostCallbacks{}, "legacy-model-aliases", func(o *claude.Options) {
		o.Environment = append(o.Environment, "ANTHROPIC_MODEL=")
	})
	catalog, err := client.ReadModels(ctx)
	if err != nil || catalog.DefaultID == nil || *catalog.DefaultID != "default" {
		t.Fatalf("legacy default = %+v, %v", catalog, err)
	}
	session, err := client.NewSession(ctx, acp.NewSessionRequest{Cwd: dir})
	if err != nil {
		t.Fatal(err)
	}
	checkModel := func(options []acp.SessionConfigOption, alias, wireID string, fast bool) *acp.SessionConfigOptionSelect {
		t.Helper()
		byID := make(map[acp.SessionConfigId]*acp.SessionConfigOptionSelect)
		for _, option := range options {
			if row := option.Select; row != nil {
				byID[row.Id] = row
			}
		}
		model := byID["model"]
		if model == nil || string(model.CurrentValue) != alias {
			t.Fatalf("selectable model = %+v, want %q", model, alias)
		}
		meta, _ := model.Meta["claude.configuration"].(map[string]any)
		if meta["observed"] != wireID {
			t.Fatalf("observed model = %+v, want %q", meta, wireID)
		}
		if byID["effort"] == nil || (byID["fast"] != nil) != fast {
			t.Fatalf("effort = %+v, fast = %+v", byID["effort"], byID["fast"])
		}
		return byID["fast"]
	}
	set := func(id, value string) []acp.SessionConfigOption {
		t.Helper()
		result, err := client.SetSessionConfigOption(ctx, acp.SetSessionConfigOptionRequest{ValueId: &acp.SetSessionConfigOptionValueId{
			SessionId: session.SessionId, ConfigId: acp.SessionConfigId(id), Value: acp.SessionConfigValueId(value),
		}})
		if err != nil {
			t.Fatal(err)
		}
		return result.ConfigOptions
	}
	fast := checkModel(session.ConfigOptions, "default", "claude-opus-4-8[1m]", true)
	if fast.Meta["claude.fast-mode-disabled-reason"] != "extra_usage_disabled" {
		t.Errorf("native Fast block = %+v", fast.Meta)
	}
	set("fast", "on")
	fast = checkModel(set("fast", "default"), "default", "claude-opus-4-8[1m]", true)
	if fast.CurrentValue != "default" {
		t.Fatalf("effective inherited Fast = %q", fast.CurrentValue)
	}
	checkModel(set("model", "sonnet"), "sonnet", "claude-sonnet-4-6", false)
	checkModel(set("effort", "max"), "sonnet", "claude-sonnet-4-6", false)
}

func TestReadAccountDistinguishesNativeEnvironmentTokenTypes(t *testing.T) {
	for _, tc := range []struct {
		variable string
		kind     string
	}{
		{"ANTHROPIC_AUTH_TOKEN", "api_key"},
		{"CLAUDE_CODE_OAUTH_TOKEN", "subscription"},
	} {
		t.Run(tc.variable, func(t *testing.T) {
			client, _, ctx := startClientMode(t, agentclient.HostCallbacks{}, "legacy-auth-token", func(o *claude.Options) {
				o.Environment = append(o.Environment, tc.variable+"=test")
			})
			account, err := client.ReadAccount(ctx)
			if err != nil || account.Account == nil || account.Account.Kind != tc.kind {
				t.Fatalf("account = %+v, %v, want %s", account, err, tc.kind)
			}
		})
	}
}

func TestClaudeLaunchWaitsForPendingMCPStatus(t *testing.T) {
	client, dir, ctx := startClientMode(t, agentclient.HostCallbacks{}, "mcp-pending", nil)
	opened, err := client.NewSession(ctx, acp.NewSessionRequest{
		Cwd: dir, McpServers: []acp.McpServer{{Stdio: &acp.McpServerStdio{Name: "local", Command: "fixture"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	launch, _ := opened.Meta["claude.launch"].(map[string]any)
	raw, err := json.Marshal(launch["mcp_servers"])
	if err != nil {
		t.Fatal(err)
	}
	var status []struct{ Name, Status string }
	if err := json.Unmarshal(raw, &status); err != nil || len(status) != 1 || status[0].Name != "local" || status[0].Status != "connected" {
		t.Fatalf("native status at admission = %s, %v", raw, err)
	}
}

func TestClaudeLoginStatusBindsNativeSubscriptionIdentity(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		// file is the native metadata path within the configured account root.
		file string
		// email represents the subject in the native metadata.
		email string
		// wantID is present only for matching metadata.
		wantID string
	}{
		{".claude.json", "local@example.test", "5e672c97-89c8-466f-8f0e-b2dc761c587b"},
		{".config.json", "local@example.test", "5e672c97-89c8-466f-8f0e-b2dc761c587b"},
		{".claude.json", "another@example.test", ""},
	} {
		t.Run(tc.file+"/"+tc.email, func(t *testing.T) {
			dir := t.TempDir()
			config := filepath.Join(dir, "config")
			if err := os.Mkdir(config, 0700); err != nil {
				t.Fatal(err)
			}
			body := fmt.Sprintf(`{"oauthAccount":{"accountUuid":"5e672c97-89c8-466f-8f0e-b2dc761c587b","organizationUuid":"c58fe389-3a47-4d19-aa06-104f4450dff8","emailAddress":%q}}`, tc.email)
			if err := os.WriteFile(filepath.Join(config, tc.file), []byte(body), 0600); err != nil {
				t.Fatal(err)
			}
			client, err := claude.NewClient(ctx, claude.Options{
				Executable: os.Args[0], PrefixArgs: []string{"-test.run=^TestClaudeFixture$", "--"},
				WorkingDirectory: dir, Environment: fixtureEnvironment(dir, "subscription-identity"),
			}, agentclient.HostCallbacks{})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = client.Close(ctx) })
			status, err := client.ReadLoginStatus(ctx)
			if err != nil || !status.Authenticated || status.Kind != "subscription" || status.AccountID != tc.wantID ||
				status.OrganizationID != "c58fe389-3a47-4d19-aa06-104f4450dff8" || status.ConfigDirectory != config {
				t.Fatalf("native account status = %+v, %v", status, err)
			}
		})
	}
}

func TestModelCatalogExposesNativeAutomaticPermissionSupport(t *testing.T) {
	client, _, ctx := startClientMode(t, agentclient.HostCallbacks{}, "legacy-model-aliases", nil)
	catalog, err := client.ReadModels(ctx)
	if err != nil || catalog.Options.Ungrouped == nil {
		t.Fatalf("catalog = %+v, %v", catalog, err)
	}
	for _, model := range *catalog.Options.Ungrouped {
		if auto := claude.ModelAutoModeSupported(model.Meta); auto != (model.Value == "sonnet") {
			t.Errorf("model %q auto = %t", model.Value, auto)
		}
	}
}

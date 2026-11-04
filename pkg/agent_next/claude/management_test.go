package claude_test

import (
	"context"
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
	if !strings.Contains(launch, "strict=true snapshot=true instructions=stable instruction") {
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

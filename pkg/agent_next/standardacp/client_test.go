package standardacp_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/JieWaZi/acp-go/pkg/acpmeta"
	agentclient "github.com/JieWaZi/acp-go/pkg/agent_next"
	"github.com/JieWaZi/acp-go/pkg/agent_next/acpclient"
	"github.com/JieWaZi/acp-go/pkg/agent_next/standardacp"
	acp "github.com/JieWaZi/acp-go/sdk"
)

func TestStandardNativeLifecycle(t *testing.T) {
	for _, provider := range []standardacp.Provider{standardacp.Kimi, standardacp.Gemini, standardacp.Grok, standardacp.OpenCode} {
		t.Run(string(provider), func(t *testing.T) {
			cwd, ready := t.TempDir(), make(chan struct{}, 1)
			client, ctx := newFixtureClient(t, provider, cwd, agentclient.HostCallbacks{
				Observe: func(_ context.Context, scope agentclient.Scope, _ acp.SessionNotification) error {
					if scope.HostInvocationID == "cancel" {
						select {
						case ready <- struct{}{}:
						default:
						}
					}
					return nil
				},
			})
			initialized, err := client.Initialize(ctx, acp.InitializeRequest{ProtocolVersion: 1})
			wantVersion := "1.2.3"
			if provider == standardacp.OpenCode {
				wantVersion = "1.18.34"
			} else if provider == standardacp.Gemini {
				wantVersion = "0.62.0"
			}
			if err != nil || initialized.AgentInfo == nil || acpmeta.RuntimeVersion(initialized.AgentInfo.Meta) != wantVersion {
				t.Fatalf("initialize: %#v %v", initialized, err)
			}
			catalog, err := client.ReadModels(ctx)
			if err != nil || catalog.Options.Ungrouped == nil || len(*catalog.Options.Ungrouped) != 2 {
				t.Fatalf("native catalog: %#v %v", catalog, err)
			}
			if provider == standardacp.Grok {
				options, ok := acpmeta.ReadModelConfigCatalog((*catalog.Options.Ungrouped)[1].Meta)
				if !ok || len(options.Options) != 1 || options.Options[0].Select.Id != "reasoning_effort" ||
					options.Options[0].Select.CurrentValue != "deliberate" {
					t.Fatalf("Grok model-specific reasoning: %#v", options)
				}
			}
			session, err := client.NewSession(ctx, acp.NewSessionRequest{Cwd: cwd})
			if err != nil || session.SessionId != "chat-session" {
				t.Fatalf("new: %#v %v", session, err)
			}
			set, err := client.SetSessionConfigOption(ctx, acp.SetSessionConfigOptionRequest{ValueId: &acp.SetSessionConfigOptionValueId{
				SessionId: session.SessionId, ConfigId: "model", Value: "fixture/alternate",
			}})
			if err != nil || len(set.ConfigOptions) != 1 || set.ConfigOptions[0].Select.CurrentValue != "fixture/alternate" {
				t.Fatalf("set model: %#v %v", set, err)
			}
			for _, id := range []string{"first", "second"} {
				result := reserve(t, ctx, client, session.SessionId, id)()
				if result.Outcome == nil || result.Outcome.StopReason == nil || *result.Outcome.StopReason != acp.StopReasonEndTurn ||
					result.Outcome.NativeFailure != nil || result.Receipt.OperationError != nil || !result.Outcome.ObservedOutput {
					t.Fatalf("%s: %#v", id, result)
				}
				usage := result.Outcome.Turn.Usage
				if provider == standardacp.OpenCode {
					if usage == nil || usage.InputTokens == nil || *usage.InputTokens != 16 || usage.OutputTokens == nil ||
						*usage.OutputTokens != 7 || usage.Complete || usage.Coverage != "observed_requests" {
						t.Fatalf("OpenCode last assistant request usage: %#v", usage)
					}
				} else if usage == nil || !slices.ContainsFunc(usage.Observations, func(o acpmeta.UsageObservation) bool { return o.Coverage == "unknown" }) {
					t.Fatalf("unattributed native usage: %#v", usage)
				}
				if provider == standardacp.Grok && (usage.InputTokens == nil || *usage.InputTokens != 10 || usage.OutputTokens == nil || *usage.OutputTokens != 7 || !usage.Complete) {
					t.Fatalf("Grok prompt ledger: %#v", usage)
				}
				if provider == standardacp.Gemini && (usage.InputTokens != nil || usage.OutputTokens == nil || *usage.OutputTokens != 6 || usage.Complete) {
					t.Fatalf("Gemini request usage: %#v", usage)
				}
			}
			run := reserve(t, ctx, client, session.SessionId, "cancel")
			done := make(chan agentclient.PromptResult, 1)
			go func() { done <- run() }()
			select {
			case <-ready:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			if err := client.Cancel(acp.CancelNotification{SessionId: session.SessionId}); err != nil {
				t.Fatal(err)
			}
			select {
			case result := <-done:
				if result.Outcome == nil || result.Outcome.StopReason == nil || *result.Outcome.StopReason != acp.StopReasonCancelled {
					t.Fatalf("cancel: %#v", result)
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			if provider == standardacp.Gemini {
				if _, err := client.SetSessionConfigOption(ctx, acp.SetSessionConfigOptionRequest{ValueId: &acp.SetSessionConfigOptionValueId{
					SessionId: session.SessionId, ConfigId: "model", Value: "fixture/default",
				}}); err != nil {
					t.Fatal(err)
				}
				restored := reserve(t, ctx, client, session.SessionId, "return-default")()
				if restored.Outcome == nil || restored.Outcome.OperationError != nil || restored.Outcome.NativeFailure != nil {
					t.Fatalf("legacy session could not switch back to its opening model: %#v", restored)
				}
				if _, err := client.SetSessionConfigOption(ctx, acp.SetSessionConfigOptionRequest{ValueId: &acp.SetSessionConfigOptionValueId{
					SessionId: session.SessionId, ConfigId: "model", Value: "fixture/alternate",
				}}); err != nil {
					t.Fatal(err)
				}
			}
			loaded, err := client.LoadSession(ctx, acp.LoadSessionRequest{SessionId: session.SessionId, Cwd: cwd})
			if err != nil || len(loaded.ConfigOptions) != 1 || loaded.ConfigOptions[0].Select.CurrentValue != "fixture/alternate" {
				t.Fatalf("load: %#v %v", loaded, err)
			}
		})
	}
}

func TestStandardACPWireProjectsGrokNativeUsage(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cwd := t.TempDir()
	adapter, err := agentclient.NewACPAgent(func(host agentclient.HostCallbacks) (agentclient.Client, error) {
		return standardacp.NewClient(ctx, standardacp.Options{
			Provider: standardacp.Grok, Executable: executable, PrefixArgs: []string{"-test.run=^TestStandardACPProcess$", "--"},
			WorkingDirectory: cwd, PermissionMode: "auto", Environment: append(os.Environ(), "STANDARD_ACP_FIXTURE=grok"),
		}, host)
	})
	if err != nil {
		t.Fatal(err)
	}
	defer adapter.Close(ctx)
	left, right := net.Pipe()
	defer left.Close()
	defer right.Close()
	acp.NewAgentSideConnectionWithBinder(adapter, left, left, adapter.SetAgentConnection)
	peer := acp.NewClientSideConnection(&wireObserver{}, right, right)
	if _, err := peer.Initialize(ctx, acp.InitializeRequest{ProtocolVersion: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := peer.CallExtension(ctx, acpmeta.ModelCatalogMethod, struct{}{}); err != nil {
		t.Fatal(err)
	}
	session, err := peer.NewSession(ctx, acp.NewSessionRequest{Cwd: cwd, McpServers: []acp.McpServer{}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := peer.SetSessionConfigOption(ctx, acp.SetSessionConfigOptionRequest{ValueId: &acp.SetSessionConfigOptionValueId{
		SessionId: session.SessionId, ConfigId: "model", Value: "fixture/alternate",
	}}); err != nil {
		t.Fatal(err)
	}
	result, err := peer.Prompt(ctx, acp.PromptRequest{SessionId: session.SessionId, Prompt: []acp.ContentBlock{acp.TextBlock("hello")}})
	turn, known := acpmeta.ReadNativeTurn(result.Meta)
	if err != nil || result.StopReason != acp.StopReasonEndTurn || !known || turn.Usage == nil ||
		turn.Usage.InputTokens == nil || *turn.Usage.InputTokens != 10 {
		t.Fatalf("wire native receipt: %#v %#v %v", result, turn, err)
	}
}

type wireObserver struct{ acp.Client }

func (*wireObserver) SessionUpdate(context.Context, acp.SessionNotification) error { return nil }

func TestStandardACPRestoreDistinguishesMissingResources(t *testing.T) {
	cwd := t.TempDir()
	client, ctx := newFixtureClient(t, standardacp.OpenCode, cwd, agentclient.HostCallbacks{})
	if _, err := client.Initialize(ctx, acp.InitializeRequest{ProtocolVersion: 1}); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		id          acp.SessionId
		wantMissing bool
	}{{"gone", true}, {"missing-resource", false}, {"wrong-session", false}} {
		_, err := client.LoadSession(ctx, acp.LoadSessionRequest{SessionId: test.id, Cwd: cwd})
		var native *acp.RequestError
		if !errors.As(err, &native) {
			t.Fatalf("%s: %v", test.id, err)
		}
		data, _ := native.Data.(map[string]any)
		if (data["classification"] == "session_missing") != test.wantMissing || (!test.wantMissing && native.Code == -32002) {
			t.Fatalf("%s: %#v", test.id, native)
		}
	}
}

func TestGeminiRestoreRequiresExactNativeSession(t *testing.T) {
	t.Setenv("STANDARD_ACP_GEMINI_UNVERIFIED_LOAD", "1")
	cwd := t.TempDir()
	client, ctx := newFixtureClient(t, standardacp.Gemini, cwd, agentclient.HostCallbacks{})
	if _, err := client.Initialize(ctx, acp.InitializeRequest{ProtocolVersion: 1}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []acp.SessionId{"latest", "1", " another-session "} {
		_, err := client.LoadSession(ctx, acp.LoadSessionRequest{SessionId: id, Cwd: cwd})
		var native *acp.RequestError
		if !errors.As(err, &native) || native.Code != -32602 {
			t.Fatalf("accepted ambiguous session alias %q: %v", id, err)
		}
	}
	_, err := client.LoadSession(ctx, acp.LoadSessionRequest{SessionId: "native-previous", Cwd: cwd})
	var native *acp.RequestError
	if !errors.As(err, &native) {
		t.Fatalf("unverified load: %v", err)
	}
	data, _ := native.Data.(map[string]any)
	if data["classification"] != "session_restore_unverified" {
		t.Fatalf("unverified load: %v", err)
	}
}

func TestGeminiDefaultedZeroQuotaRemainsUnknown(t *testing.T) {
	t.Setenv("STANDARD_ACP_GEMINI_ZERO_QUOTA", "1")
	cwd := t.TempDir()
	client, ctx := newFixtureClient(t, standardacp.Gemini, cwd, agentclient.HostCallbacks{})
	if _, err := client.Initialize(ctx, acp.InitializeRequest{ProtocolVersion: 1}); err != nil {
		t.Fatal(err)
	}
	session, err := client.NewSession(ctx, acp.NewSessionRequest{Cwd: cwd})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.SetSessionConfigOption(ctx, acp.SetSessionConfigOptionRequest{ValueId: &acp.SetSessionConfigOptionValueId{
		SessionId: session.SessionId, ConfigId: "model", Value: "fixture/alternate",
	}}); err != nil {
		t.Fatal(err)
	}
	result := reserve(t, ctx, client, session.SessionId, "defaulted-zero")()
	if result.Outcome == nil || result.Outcome.Turn.Usage == nil || result.Outcome.Turn.Usage.OutputTokens != nil {
		t.Fatalf("Gemini's defaulted zero must not become measured output: %#v", result.Outcome)
	}
}

func TestOpenCodeRestoreRecovery(t *testing.T) {
	for _, mode := range []string{"startup", "listed", "missing", "incomplete"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("STANDARD_ACP_OPENCODE_RESTORE", mode)
			cwd := t.TempDir()
			client, ctx := newFixtureClient(t, standardacp.OpenCode, cwd, agentclient.HostCallbacks{})
			if _, err := client.Initialize(ctx, acp.InitializeRequest{ProtocolVersion: 1}); err != nil {
				t.Fatal(err)
			}
			result, err := client.ResumeSession(ctx, acp.ResumeSessionRequest{SessionId: "recover", Cwd: cwd})
			if mode == "missing" {
				var native *acp.RequestError
				if !errors.As(err, &native) {
					t.Fatalf("missing error: %v", err)
				}
				data, _ := native.Data.(map[string]any)
				if data["classification"] != "session_missing" {
					t.Fatalf("missing proof: %v", err)
				}
			} else if err != nil || len(result.ConfigOptions) != 1 {
				t.Fatalf("restored session: %#v %v", result, err)
			}
		})
	}
}

func TestOpenCodeMCPDirectoryKeepsOneOwnerPerProcess(t *testing.T) {
	cwd := t.TempDir()
	server := func(token string) []acp.McpServer {
		var servers []acp.McpServer
		raw, _ := json.Marshal([]map[string]any{{"type": "http", "name": "managed", "url": "http://127.0.0.1:1/mcp",
			"headers": []map[string]string{{"name": "Authorization", "value": token}}}})
		if err := json.Unmarshal(raw, &servers); err != nil {
			t.Fatal(err)
		}
		return servers
	}
	first, firstCtx := newFixtureClient(t, standardacp.OpenCode, cwd, agentclient.HostCallbacks{})
	second, secondCtx := newFixtureClient(t, standardacp.OpenCode, cwd, agentclient.HostCallbacks{})
	for _, initialize := range []func() error{
		func() error {
			_, err := first.Initialize(firstCtx, acp.InitializeRequest{ProtocolVersion: 1})
			return err
		},
		func() error {
			_, err := second.Initialize(secondCtx, acp.InitializeRequest{ProtocolVersion: 1})
			return err
		},
	} {
		if err := initialize(); err != nil {
			t.Fatal(err)
		}
	}
	session, err := first.NewSession(firstCtx, acp.NewSessionRequest{Cwd: cwd, McpServers: server("first")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := second.NewSession(secondCtx, acp.NewSessionRequest{Cwd: cwd, McpServers: server("second")}); err != nil {
		t.Fatalf("process-private MCP namespace: %v", err)
	}
	assertConflict := func(servers []acp.McpServer) {
		t.Helper()
		_, err := first.NewSession(firstCtx, acp.NewSessionRequest{Cwd: cwd, McpServers: servers})
		var native *acp.RequestError
		if !errors.As(err, &native) || native.Code != -32602 {
			t.Fatalf("directory MCP namespace reused by another native session: %v", err)
		}
	}
	assertConflict(server("second"))
	if _, err := first.LoadSession(firstCtx, acp.LoadSessionRequest{SessionId: session.SessionId, Cwd: cwd, McpServers: server("first")}); err != nil {
		t.Fatalf("same session and unchanged MCP route: %v", err)
	}
	if _, err := first.CloseSession(firstCtx, acp.CloseSessionRequest{SessionId: session.SessionId}); err != nil {
		t.Fatal(err)
	}
	assertConflict(nil)
}

func TestOpenCodeEmptyAnswer(t *testing.T) {
	cwd := t.TempDir()
	client, ctx := newFixtureClient(t, standardacp.OpenCode, cwd, agentclient.HostCallbacks{})
	if _, err := client.Initialize(ctx, acp.InitializeRequest{ProtocolVersion: 1}); err != nil {
		t.Fatal(err)
	}
	session, err := client.NewSession(ctx, acp.NewSessionRequest{Cwd: cwd})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.SetSessionConfigOption(ctx, acp.SetSessionConfigOptionRequest{ValueId: &acp.SetSessionConfigOptionValueId{
		SessionId: session.SessionId, ConfigId: "model", Value: "fixture/alternate",
	}}); err != nil {
		t.Fatal(err)
	}
	for _, prompt := range []string{"empty-answer", "thought-only", "/compact"} {
		result := reserve(t, ctx, client, session.SessionId, prompt)()
		if result.Outcome == nil || result.Outcome.OperationError != nil ||
			(result.Outcome.NativeFailure == nil) != (prompt == "/compact") {
			t.Fatalf("%s: %#v", prompt, result)
		}
		if prompt != "/compact" && result.Outcome.Turn.Status != acpmeta.NativeTurnFailed {
			t.Fatalf("%s published a successful native turn: %#v", prompt, result.Outcome)
		}
	}
}

func TestKimiLegacyModelAppliedBeforeConfigWrite(t *testing.T) {
	for _, failure := range []string{"write", "ambiguous"} {
		t.Run(failure, func(t *testing.T) {
			t.Setenv("STANDARD_ACP_KIMI_LEGACY", failure)
			t.Setenv("KIMI_SHARE_DIR", t.TempDir())
			cwd := t.TempDir()
			client, ctx := newFixtureClient(t, standardacp.Kimi, cwd, agentclient.HostCallbacks{})
			if _, err := client.Initialize(ctx, acp.InitializeRequest{ProtocolVersion: 1}); err != nil {
				t.Fatal(err)
			}
			session, err := client.NewSession(ctx, acp.NewSessionRequest{Cwd: cwd})
			if err != nil {
				t.Fatal(err)
			}
			set := func(id string) (acp.SetSessionConfigOptionResponse, error) {
				return client.SetSessionConfigOption(ctx, acp.SetSessionConfigOptionRequest{ValueId: &acp.SetSessionConfigOptionValueId{
					SessionId: session.SessionId, ConfigId: "model", Value: acp.SessionConfigValueId(id),
				}})
			}
			response, err := set("fixture/alternate")
			if failure == "ambiguous" {
				if err == nil {
					t.Fatal("ambiguous config failure was accepted")
				}
				if _, err := set("fixture/default"); !errors.Is(err, acpclient.ErrRestartRequired) {
					t.Fatalf("uncertain session remained usable: %v", err)
				}
				return
			}
			if err != nil || response.Meta["acp-go/model-acknowledgement"] != "fixture/alternate" ||
				response.Meta["acp-go/model-persistence"] != "unconfirmed" || len(response.ConfigOptions) != 1 ||
				response.ConfigOptions[0].Select.CurrentValue != "fixture/alternate" {
				t.Fatalf("setter receipt: %#v %v", response, err)
			}
			result := reserve(t, ctx, client, session.SessionId, "alternate")()
			if result.Outcome == nil || result.Outcome.OperationError != nil || result.Outcome.NativeFailure != nil {
				t.Fatalf("selected model not usable: %#v", result)
			}
			if _, err := set("fixture/default"); err == nil {
				t.Fatal("Kimi acknowledged a no-op reversion to its opening model")
			}
		})
	}
}

func TestKimiNativeJournalAndOpeningMode(t *testing.T) {
	for _, mode := range []string{"failed", "unverified"} {
		t.Run(mode, func(t *testing.T) { checkKimiNativeJournal(t, mode) })
	}
}

func checkKimiNativeJournal(t *testing.T, journalMode string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cwd, home := t.TempDir(), t.TempDir()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	client, err := standardacp.NewClient(ctx, standardacp.Options{
		Provider: standardacp.Kimi, Executable: executable, PrefixArgs: []string{"-test.run=^TestStandardACPProcess$", "--"},
		WorkingDirectory: cwd, PermissionMode: "auto",
		Environment: append(os.Environ(), "STANDARD_ACP_FIXTURE=kimi", "STANDARD_ACP_KIMI_JOURNAL="+journalMode, "KIMI_CODE_HOME="+home),
	}, agentclient.HostCallbacks{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		closeCtx, stop := context.WithTimeout(context.Background(), 3*time.Second)
		defer stop()
		if err := client.Close(closeCtx); err != nil {
			t.Error(err)
		}
	}()
	if _, err := client.Initialize(ctx, acp.InitializeRequest{ProtocolVersion: 1}); err != nil {
		t.Fatal(err)
	}
	session, err := client.NewSession(ctx, acp.NewSessionRequest{Cwd: cwd})
	if err != nil || session.Modes == nil || session.Modes.CurrentModeId != "auto" {
		t.Fatalf("Kimi opening mode: %#v %v", session, err)
	}
	for _, id := range []string{"first", "second"} {
		result := reserve(t, ctx, client, session.SessionId, id)()
		if journalMode == "unverified" {
			if result.Outcome == nil || result.Outcome.Turn.Status != "" || result.Outcome.StopReason != nil ||
				result.Outcome.NativeFailure != nil || result.Outcome.OperationError == nil ||
				result.Receipt.Admission != agentclient.AdmissionConfirmed || result.Outcome.Turn.Usage == nil {
				t.Fatalf("unverified terminal: %#v", result)
			}
			break
		}
		if result.Outcome == nil || result.Outcome.Turn.Status != acpmeta.NativeTurnFailed ||
			result.Outcome.NativeFailure == nil || result.Outcome.OperationError != nil {
			t.Fatalf("%s native failure: %#v", id, result)
		}
		usage := result.Outcome.Turn.Usage
		if usage == nil || usage.InputTokens == nil || *usage.InputTokens != 7 || usage.OutputTokens == nil || *usage.OutputTokens != 5 {
			t.Fatalf("%s journal usage: %#v", id, usage)
		}
	}
}

func newFixtureClient(t *testing.T, provider standardacp.Provider, cwd string, host agentclient.HostCallbacks) (*acpclient.Client, context.Context) {
	t.Helper()
	ctx, stop := context.WithTimeout(context.Background(), 12*time.Second)
	t.Cleanup(stop)
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	environment := append(os.Environ(), "STANDARD_ACP_FIXTURE="+string(provider))
	mode := ""
	if provider == standardacp.Grok {
		mode = "auto"
	}
	client, err := standardacp.NewClient(ctx, standardacp.Options{
		Provider: provider, Executable: executable, PrefixArgs: []string{"-test.run=^TestStandardACPProcess$", "--"},
		Environment: environment, WorkingDirectory: cwd, PermissionMode: mode,
	}, host)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := client.Close(closeCtx); err != nil {
			t.Error(err)
		}
	})
	return client, ctx
}

func reserve(t *testing.T, ctx context.Context, client agentclient.Client, session acp.SessionId, id string) func() agentclient.PromptResult {
	t.Helper()
	run, err := client.ReservePrompt(ctx, acp.PromptRequest{SessionId: session, Prompt: []acp.ContentBlock{acp.TextBlock(id)}}, agentclient.PromptOptions{HostInvocationID: id})
	if err != nil {
		t.Fatal(err)
	}
	return run
}

func TestStandardACPProcess(t *testing.T) {
	provider := os.Getenv("STANDARD_ACP_FIXTURE")
	if provider == "" {
		return
	}
	journalMode := os.Getenv("STANDARD_ACP_KIMI_JOURNAL")
	restoreMode := os.Getenv("STANDARD_ACP_OPENCODE_RESTORE")
	journal := journalMode != ""
	kimiLegacy := os.Getenv("STANDARD_ACP_KIMI_LEGACY")
	version := "1.2.3"
	if provider == "gemini" {
		version = "0.62.0"
	}
	if provider == "opencode" {
		version = "1.18.34"
		if restoreMode == "startup" {
			version = "2.1.1"
		} else if restoreMode != "" {
			version = "1.19.3"
		}
	}
	if journal {
		version = "2.0.2"
	} else if kimiLegacy != "" {
		version = "1.52.0"
	}
	args := os.Args[slices.Index(os.Args, "--")+1:]
	if slices.Equal(args, []string{"--version"}) {
		_, _ = os.Stdout.WriteString(provider + " " + version + "\n")
		os.Exit(0)
	}
	wantArgs := map[string][]string{"kimi": {"acp"}, "gemini": {"--acp"}, "grok": {"--no-auto-update", "agent", "stdio"}, "opencode": {"acp"}}
	if !slices.Equal(args, wantArgs[provider]) {
		os.Exit(41)
	}
	name := map[string]string{"kimi": "Kimi Code CLI", "gemini": "gemini-cli", "opencode": "OpenCode"}[provider]
	type jsonObject = map[string]any
	out := json.NewEncoder(os.Stdout)
	s := bufio.NewScanner(os.Stdin)
	model := "fixture/default"
	var pending json.RawMessage
	nextSession := 0
	restoreCalls := 0
	mode := "default"
	var wire string
	authenticated := provider != "grok"
	options := func() []any {
		return []any{jsonObject{"id": "model", "name": "Model", "category": "model", "type": "select", "currentValue": model, "options": []any{
			jsonObject{"value": "fixture/default", "name": "Default"}, jsonObject{"value": "fixture/alternate", "name": "Alternate"},
		}}}
	}
	legacy := func() jsonObject {
		return jsonObject{"currentModelId": model, "availableModels": []any{
			jsonObject{"modelId": "fixture/default", "name": "Default"}, jsonObject{"modelId": "fixture/alternate", "name": "Alternate",
				"_meta": jsonObject{"supportsReasoningEffort": true, "reasoningEfforts": []any{
					jsonObject{"id": "deliberate", "value": "high", "label": "Deliberate", "default": true},
				}}},
		}}
	}
	usage := jsonObject{"inputTokens": 16, "outputTokens": 4, "totalTokens": 20}
	if provider == "opencode" {
		usage = jsonObject{"inputTokens": 16, "outputTokens": 4, "thoughtTokens": 3, "cachedReadTokens": 2, "totalTokens": 25}
	}
	for s.Scan() {
		var request struct {
			ID     json.RawMessage
			Method string
			Params jsonObject
		}
		if json.Unmarshal(s.Bytes(), &request) != nil {
			os.Exit(42)
		}
		response := jsonObject{"jsonrpc": "2.0", "id": request.ID}
		result := jsonObject{}
		switch request.Method {
		case "initialize":
			result = jsonObject{"protocolVersion": 1, "agentCapabilities": jsonObject{"loadSession": true,
				"sessionCapabilities": jsonObject{"close": jsonObject{}, "list": jsonObject{}, "resume": jsonObject{}},
				"mcpCapabilities":     jsonObject{"http": true}},
			}
			if provider == "grok" {
				result["_meta"] = jsonObject{"grokShell": true, "agentVersion": version, "defaultAuthMethodId": "xai.api_key"}
				result["authMethods"] = []any{jsonObject{"id": "xai.api_key", "name": "API key"}, jsonObject{"id": "grok.com", "name": "Interactive"}}
			} else {
				result["agentInfo"] = jsonObject{"name": name, "version": version}
			}
		case "authenticate":
			if provider != "grok" || request.Params["methodId"] != "xai.api_key" {
				os.Exit(48)
			}
			authenticated = true
		case "session/list":
			switch restoreMode {
			case "listed":
				if request.Params["cursor"] == nil {
					result["sessions"], result["nextCursor"] = []any{}, "second"
				} else {
					result["sessions"] = []any{jsonObject{"sessionId": "recover", "cwd": request.Params["cwd"]}}
				}
			case "missing":
				result["sessions"] = []any{}
			case "incomplete":
				result["nextCursor"] = "untrustworthy"
			default:
				os.Exit(54)
			}
		case "session/new", "session/load", "session/resume":
			if request.Method == "session/resume" && restoreMode != "" {
				restoreCalls++
				if restoreCalls == 1 {
					if restoreMode == "startup" {
						response["error"] = jsonObject{"code": -32603, "message": "Internal error: Internal service failure",
							"data": jsonObject{"errorName": "ServiceUnavailableError"}}
					} else {
						response["error"] = jsonObject{"code": -32603, "message": "Internal error: OpenCode service failure"}
					}
					_ = out.Encode(response)
					continue
				}
			}
			switch request.Params["sessionId"] {
			case "gone":
				response["error"] = jsonObject{"code": -32602, "message": "Invalid params: session not found: gone",
					"data": jsonObject{"sessionId": "gone"}}
				_ = out.Encode(response)
				continue
			case "missing-resource", "wrong-session":
				response["error"] = jsonObject{"code": -32002, "message": "Resource not found",
					"data": jsonObject{"uri": "fixture/unrelated"}}
				_ = out.Encode(response)
				continue
			}
			if !authenticated {
				os.Exit(49)
			}
			if provider == "grok" {
				meta, _ := request.Params["_meta"].(jsonObject)
				if request.Method == "session/load" || nextSession > 0 {
					if meta["autoMode"] != true || meta["yoloMode"] != false {
						os.Exit(43)
					}
				}
			}
			if cwd, _ := request.Params["cwd"].(string); !filepath.IsAbs(cwd) {
				os.Exit(44)
			}
			if request.Method == "session/new" {
				id := "catalog-session"
				if nextSession > 0 {
					id = "chat-session"
				}
				result["sessionId"] = id
				nextSession++
				if journal {
					home := os.Getenv("KIMI_CODE_HOME")
					dir := filepath.Join(home, "sessions", id)
					wire = filepath.Join(dir, "agents", "main", "wire.jsonl")
					index, _ := json.Marshal(jsonObject{"sessionId": id, "sessionDir": dir, "workDir": request.Params["cwd"]})
					if os.MkdirAll(filepath.Dir(wire), 0700) != nil ||
						os.WriteFile(filepath.Join(home, "session_index.jsonl"), append(index, '\n'), 0600) != nil ||
						os.WriteFile(wire, []byte(`{"type":"usage.record","usageScope":"turn","usage":{"inputOther":90,"output":90}}`+"\n"), 0600) != nil {
						os.Exit(50)
					}
					result["modes"] = jsonObject{"currentModeId": mode, "availableModes": []any{
						jsonObject{"id": "default", "name": "Default"}, jsonObject{"id": "auto", "name": "Auto"},
					}}
				}
			} else if os.Getenv("STANDARD_ACP_GEMINI_UNVERIFIED_LOAD") == "" {
				result["sessionId"] = request.Params["sessionId"]
			}
			if provider == "gemini" || kimiLegacy != "" {
				result["models"] = legacy()
			} else {
				result["configOptions"] = options()
				if provider == "grok" {
					result["models"] = legacy()
				}
			}
		case "session/set_mode":
			mode, _ = request.Params["modeId"].(string)
		case "session/set_model":
			model, _ = request.Params["modelId"].(string)
			if kimiLegacy != "" && model == "fixture/alternate" {
				path := filepath.Join(os.Getenv("KIMI_SHARE_DIR"), "config.toml")
				detail := "could not read config.toml"
				if kimiLegacy == "write" {
					detail = "[Errno 13] Permission denied: '" + path + "'"
				}
				response["error"] = jsonObject{"code": -32603, "message": "Internal error", "data": jsonObject{"details": detail}}
			}
		case "session/set_config_option":
			model, _ = request.Params["value"].(string)
			result["configOptions"] = options()
		case "session/close":
		case "session/prompt":
			if journal {
				if mode != "auto" {
					os.Exit(51)
				}
				f, err := os.OpenFile(wire, os.O_APPEND|os.O_WRONLY, 0600)
				if err != nil {
					os.Exit(52)
				}
				events := `{"type":"turn.prompt","agentId":"main","turnId":0}` + "\n" +
					`{"type":"usage.record","agentId":"main","model":"fixture/default","usageScope":"turn","usage":{"inputOther":7,"output":5}}` + "\n"
				if journalMode != "unverified" {
					events += `{"type":"turn.ended","agentId":"main","turnId":0,"reason":"failed","error":{"code":"provider.rate_limit"}}` + "\n"
				}
				_, err = f.WriteString(events)
				if err != nil || f.Close() != nil {
					os.Exit(53)
				}
			} else {
				expected := "fixture/alternate"
				if strings.Contains(s.Text(), `"text":"return-default"`) {
					expected = "fixture/default"
				}
				if model != expected {
					os.Exit(45)
				}
			}
			line := s.Text()
			if !strings.Contains(line, `"text":"empty-answer"`) && !strings.Contains(line, `"text":"/compact"`) {
				kind := "agent_message_chunk"
				if strings.Contains(line, `"text":"thought-only"`) {
					kind = "agent_thought_chunk"
				}
				_ = out.Encode(jsonObject{"jsonrpc": "2.0", "method": "session/update", "params": jsonObject{
					"sessionId": request.Params["sessionId"], "update": jsonObject{"sessionUpdate": kind,
						"content": jsonObject{"type": "text", "text": "fixture answer"}},
				}})
			}
			if strings.Contains(s.Text(), `"text":"cancel"`) {
				pending = slices.Clone(request.ID)
				continue
			}
			result = jsonObject{"stopReason": "end_turn", "usage": usage}
			if provider == "grok" {
				result["_meta"] = jsonObject{"usage": jsonObject{"inputTokens": 16, "outputTokens": 7, "cachedReadTokens": 4,
					"cacheCreationTokens": 2, "reasoningTokens": 3, "modelCalls": 1}}
			}
			if provider == "gemini" {
				input, output := 23, 6
				if os.Getenv("STANDARD_ACP_GEMINI_ZERO_QUOTA") != "" {
					input, output = 0, 0
				}
				result["_meta"] = jsonObject{"quota": jsonObject{"token_count": jsonObject{"input_tokens": input, "output_tokens": output}}}
			}
		case "session/cancel":
			if pending == nil {
				os.Exit(46)
			}
			response["id"], pending = pending, nil
			result = jsonObject{"stopReason": "cancelled"}
		default:
			response["error"] = jsonObject{"code": -32601, "message": "Method not found"}
		}
		if _, failed := response["error"]; !failed {
			response["result"] = result
		}
		if out.Encode(response) != nil {
			os.Exit(47)
		}
	}
	os.Exit(0)
}

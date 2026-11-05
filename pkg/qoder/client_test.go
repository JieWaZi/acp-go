package qoder_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JieWaZi/acp-go/pkg/acpmeta"
	agentclient "github.com/JieWaZi/acp-go/pkg/agentclient"
	"github.com/JieWaZi/acp-go/pkg/qoder"
	acp "github.com/JieWaZi/acp-go/sdk"
)

func TestNativeQoder(t *testing.T) {
	if os.Getenv("ACP_FAMILY_QODER") != "1" {
		return
	}
	command := os.Args[len(os.Args)-1]
	if len(os.Args) >= 3 && strings.Join(os.Args[len(os.Args)-3:], " ") == "status -o json" {
		fmt.Println(os.Getenv("ACP_FAMILY_QODER_STATUS"))
		os.Exit(0)
	}
	if strings.Contains(strings.Join(os.Args, " "), "--input-format stream-json") {
		runManagementFixture()
		os.Exit(0)
	}
	version := os.Getenv("ACP_FAMILY_QODER_USAGE_VERSION")
	if command == "--version" {
		if version == "" {
			version = "0.15.0"
		}
		fmt.Println("Qoder CLI " + version)
		os.Exit(0)
	}
	if command != "--acp" {
		os.Exit(2)
	}
	trace, err := os.OpenFile(os.Getenv("ACP_FAMILY_TRACE"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		os.Exit(3)
	}
	out := json.NewEncoder(os.Stdout)
	in := bufio.NewScanner(os.Stdin)
	options := func(id, thought string) []any {
		model := "standard"
		if id == "second" {
			model = "other"
		}
		return []any{
			map[string]any{"id": "reasoning_effort", "name": "Reasoning effort", "category": "model", "type": "select", "currentValue": thought, "options": []any{map[string]any{"value": "native-low", "name": "Low"}, map[string]any{"value": "native-high", "name": "High"}}},
			map[string]any{"id": "llm", "name": "Model", "category": "model", "type": "select", "currentValue": model, "options": []any{map[string]any{"value": "standard", "name": "Standard"}, map[string]any{"value": "other", "name": "Other"}}},
		}
	}
	var promptID json.RawMessage
	var promptSession string
	var opened int
	var prompts int
	reply := func(id json.RawMessage, result any) {
		_ = out.Encode(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
	}
	notify := func(id string, data map[string]any) {
		_ = out.Encode(map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": map[string]any{"sessionId": id, "update": data}})
	}
	config := func(id, thought string) {
		notify(id, map[string]any{"sessionUpdate": "config_option_update", "configOptions": options(id, thought)})
	}
	for in.Scan() {
		var frame struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params map[string]any  `json:"params"`
			Result json.RawMessage `json:"result"`
		}
		if json.Unmarshal(in.Bytes(), &frame) != nil {
			os.Exit(4)
		}
		fmt.Fprintln(trace, string(in.Bytes()))
		id, _ := frame.Params["sessionId"].(string)
		switch frame.Method {
		case "initialize":
			if expected := os.Getenv("ACP_FAMILY_QODER_PAT_ENV"); expected != "" && os.Getenv(expected) != "fixture-selected-token" {
				os.Exit(9)
			}
			result := map[string]any{"protocolVersion": 1, "authMethods": []any{map[string]any{"id": "qodercli-login", "name": "CLI login"}}, "agentCapabilities": map[string]any{"loadSession": true, "mcpCapabilities": map[string]any{"http": true, "sse": true}, "sessionCapabilities": map[string]any{"resume": map[string]any{}}}}
			if version != "" {
				result["agentInfo"] = map[string]string{"name": "qoder-cli", "version": version}
			}
			reply(frame.ID, result)
		case "session/new":
			opened++
			id = "first"
			if opened == 2 {
				id = "second"
			}
			config("foreign", "native-high")
			config(id, "native-low")
			reply(frame.ID, map[string]any{"sessionId": id, "configOptions": options(id, "native-low"), "modes": map[string]any{
				"currentModeId": "default", "availableModes": []any{map[string]any{"id": "default", "name": "Default"}, map[string]any{"id": "auto", "name": "Auto"}},
			}})
		case "session/resume", "session/load":
			if id == "missing" || id == "foreign-missing" || id == "bad-parameters" {
				code, missingID := "INVALID_SESSION_IDENTIFIER", id
				if id == "foreign-missing" {
					missingID = "different"
				}
				if id == "bad-parameters" {
					code = "INVALID_CWD"
				}
				_ = out.Encode(map[string]any{"jsonrpc": "2.0", "id": frame.ID, "error": map[string]any{
					"code": -32602, "message": "Invalid parameters", "data": map[string]any{"code": code, "sessionId": missingID, "projectRoot": "/private/project"},
				}})
				continue
			}
			config(id, "native-low")
			reply(frame.ID, map[string]any{"configOptions": options(id, "native-low")})
		case "session/set_config_option":
			if frame.Params["configId"] != "reasoning_effort" || frame.Params["value"] != "native-high" {
				os.Exit(5)
			}
			config(id, "native-high")
			reply(frame.ID, map[string]any{"configOptions": options(id, "native-high")})
		case "session/set_mode":
			if frame.Params["modeId"] != "auto" {
				os.Exit(10)
			}
			reply(frame.ID, map[string]any{})
		case "session/prompt":
			prompts++
			promptID = frame.ID
			if os.Getenv("ACP_FAMILY_QODER_DIAGNOSTIC") == "1" {
				fmt.Fprintln(os.Stderr, "safe startup evidence")
				fmt.Fprintln(os.Stderr, os.Getenv("QODER_PERSONAL_ACCESS_TOKEN"))
				os.Exit(12)
			}
			if os.Getenv("ACP_FAMILY_QODER_PERMISSION") == "1" {
				promptSession = id
				_ = out.Encode(map[string]any{"jsonrpc": "2.0", "id": "permission", "method": "session/request_permission", "params": map[string]any{
					"sessionId": id,
					"toolCall":  map[string]any{"toolCallId": "qoder-tool", "title": "Fixture tool"},
					"options":   []any{map[string]any{"optionId": "native-allow", "name": "Allow", "kind": "allow_once"}},
				}})
				continue
			}
			if variable := os.Getenv("ACP_FAMILY_QODER_USAGE_VARIABLE"); variable != "" {
				var input, output, rawOutput int
				if os.Getenv(variable) == "1" {
					switch prompts {
					case 1:
						input, output, rawOutput = 24, 7, 7
					case 2:
						input, output, rawOutput = 35, 11, 11
					case 4:
						input, output, rawOutput = 35, 11, 9
					}
				}
				quota := map[string]any{
					"token_count": map[string]int{"input_tokens": input, "output_tokens": output},
					"model_usage": []map[string]any{{"model": "qfmodel", "token_count": map[string]int{"input_tokens": input, "output_tokens": output}}},
				}
				reply(frame.ID, map[string]any{"stopReason": "end_turn",
					"usage": map[string]int{"inputTokens": input, "outputTokens": rawOutput, "totalTokens": input + rawOutput},
					"_meta": map[string]any{"quota": quota},
				})
				continue
			}
			notify("foreign", map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": "unrelated"}})
			notify(id, map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": "Error: RetriableError: WritableIterable is closed"}})
			if os.Getenv("ACP_FAMILY_WIRE_CANCEL") != "1" {
				reply(frame.ID, map[string]any{"stopReason": "end_turn", "usage": map[string]any{"inputTokens": prompts * 12, "cachedReadTokens": 0, "cachedWriteTokens": 0}})
			}
		case "session/cancel":
			reply(promptID, map[string]any{"stopReason": "cancelled", "usage": map[string]any{"inputTokens": 12, "cachedReadTokens": 0, "cachedWriteTokens": 0}})
		case "":
			var permission acp.RequestPermissionResponse
			if string(frame.ID) != `"permission"` || json.Unmarshal(frame.Result, &permission) != nil || permission.Outcome.Selected == nil || permission.Outcome.Selected.OptionId != "native-allow" {
				os.Exit(11)
			}
			notify(promptSession, map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": "Approved"}})
			reply(promptID, map[string]any{"stopReason": "end_turn"})
		default:
			os.Exit(6)
		}
	}
	os.Exit(0)
}

func runManagementFixture() {
	in := bufio.NewScanner(os.Stdin)
	out := json.NewEncoder(os.Stdout)
	trace, err := os.OpenFile(os.Getenv("ACP_FAMILY_TRACE"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		os.Exit(3)
	}
	defer trace.Close()
	for in.Scan() {
		var request struct {
			Type    string `json:"type"`
			ID      string `json:"request_id"`
			Request struct {
				Type string `json:"type"`
			} `json:"request"`
		}
		if json.Unmarshal(in.Bytes(), &request) != nil || request.Type != "control_request" {
			os.Exit(7)
		}
		fmt.Fprintln(trace, request.Request.Type)
		var result any
		switch request.Request.Type {
		case "initialize":
			result = map[string]any{"account": map[string]any{"name": "Bootstrap"}}
		case "account_info":
			result = map[string]any{"account": map[string]any{"userId": "native-id", "email": "account@example.test", "tokenSource": "selected", "subscriptionType": "Pro"}}
		case "get_usage_info":
			result = json.RawMessage(os.Getenv("ACP_FAMILY_QODER_QUOTA"))
		default:
			os.Exit(8)
		}
		_ = out.Encode(map[string]any{"type": "control_response", "response": map[string]any{"subtype": "success", "request_id": "foreign", "response": nil}})
		_ = out.Encode(map[string]any{"type": "control_response", "response": map[string]any{"subtype": "success", "request_id": request.ID, "response": result}})
	}
}

func TestQoderStatusReadsOnlyNativeAuthenticationFacts(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 7*time.Second)
	defer cancel()
	options, _ := fixtureOptions(t, false)
	options.Environment = append(options.Environment, `ACP_FAMILY_QODER_STATUS={"logged_in":true,"auth_source":"browser","account_type":"individual","login_method":"oauth","username":"local","email":"local@example.test","user_type":"Pro"}`)
	status, err := qoder.ReadLoginStatus(ctx, options)
	if err != nil || !status.Authenticated || status.Email != "local@example.test" || status.AuthSource != "browser" || status.LoginMethod != "oauth" || status.AccountType != "individual" || status.UserType != "Pro" {
		t.Fatalf("status = %+v, error = %v", status, err)
	}
	options.Environment = append(options.Environment, `ACP_FAMILY_QODER_STATUS={"logged_in":false,"email":"stale@example.test"}`)
	status, err = qoder.ReadLoginStatus(ctx, options)
	if err != nil || status.Authenticated || status.Email != "" {
		t.Fatalf("signed-out status = %+v, error = %v", status, err)
	}
	options.Environment = append(options.Environment, `ACP_FAMILY_QODER_STATUS={"email":"unverified@example.test"}`)
	if _, err := qoder.ReadLoginStatus(ctx, options); err == nil {
		t.Fatal("ambiguous status was accepted")
	}
}

func TestQoderStatusPreservesNativeServiceAccountIdentity(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	options, _ := fixtureOptions(t, false)
	for _, tc := range []struct {
		// raw is the CLI's native status output.
		raw string
		// principal is the expected native service account identifier.
		principal string
		// organization is the optional native service account organization.
		organization string
		// invalid indicates that the status claims an unusable service account identity.
		invalid bool
	}{
		{raw: `{"logged_in":true,"account_type":"service_account","principal_id":"principal-1","org_id":"org-1"}`, principal: "principal-1", organization: "org-1"},
		{raw: `{"logged_in":true,"account_type":"service_account","principal_id":"principal-2"}`, principal: "principal-2"},
		{raw: `{"logged_in":true,"account_type":"service_account"}`, invalid: true},
		{raw: `{"logged_in":true,"account_type":"service_account","principal_id":"principal-1\n"}`, invalid: true},
		{raw: `{"logged_in":true,"account_type":"service_account","principal_id":"principal-1","org_id":" org"}`, invalid: true},
		{raw: `{"logged_in":false,"account_type":"service_account","principal_id":"stale","org_id":"stale"}`},
	} {
		options.Environment = append(options.Environment, "ACP_FAMILY_QODER_STATUS="+tc.raw)
		status, err := qoder.ReadLoginStatus(ctx, options)
		if tc.invalid {
			if err == nil || status.Authenticated {
				t.Fatalf("accepted unusable status: %+v %v", status, err)
			}
			continue
		}
		if err != nil || status.PrincipalID != tc.principal || status.OrganizationID != tc.organization ||
			status.Authenticated != (tc.principal != "") || status.Authenticated && status.AccountType != "service_account" {
			t.Fatalf("incorrect native service account evidence: %+v %v", status, err)
		}
	}
}

func TestQoderCatalogKeepsOpeningReceiptWithoutModelWrites(t *testing.T) {
	for _, edition := range []string{"qoder", "qoder-cn"} {
		t.Run(edition, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			options, trace := fixtureOptions(t, false)
			options.RuntimeID = edition
			client, err := qoder.NewClient(ctx, options, agentclient.HostCallbacks{})
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close(ctx)
			if _, err = client.Initialize(ctx, acp.InitializeRequest{ProtocolVersion: 1}); err != nil {
				t.Fatal(err)
			}
			catalog, err := client.ReadModels(ctx)
			wire, readErr := os.ReadFile(trace)
			if readErr != nil || strings.Contains(string(wire), `"method":"session/set_`) {
				t.Fatalf("model discovery sent a setter: %v %s", readErr, wire)
			}
			if err != nil || catalog.DefaultID == nil || *catalog.DefaultID != "standard" || catalog.Options.Ungrouped == nil || len(*catalog.Options.Ungrouped) != 2 {
				t.Fatalf("native catalog: %+v %v", catalog, err)
			}
			for _, row := range *catalog.Options.Ungrouped {
				config, ok := acpmeta.ReadModelConfigCatalog(row.Meta)
				if !ok || (row.Value == "standard" && (len(config.Options) != 1 || config.Options[0].Select == nil || config.Options[0].Select.CurrentValue != "native-low" || len(config.UnknownConfigIDs) != 0)) ||
					(row.Value == "other" && (len(config.Options) != 0 || len(config.UnknownConfigIDs) == 0)) {
					t.Fatalf("incorrect model configuration for %q: %+v", row.Value, config)
				}
			}
		})
	}
}

func TestQoderPATSelectsTheRegionalNativeCredential(t *testing.T) {
	for runtime, key := range map[string]string{"qoder": "QODER_PERSONAL_ACCESS_TOKEN", "qoder-cn": "QODERCN_PERSONAL_ACCESS_TOKEN"} {
		t.Run(runtime, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 7*time.Second)
			defer cancel()
			options, trace := fixtureOptions(t, false)
			options.RuntimeID = runtime
			options.Environment = append(options.Environment, key+"=ambient-token", "ACP_FAMILY_QODER_PAT_ENV="+key)
			options.PersonalAccessToken = "fixture-selected-token"
			client, err := qoder.NewClient(ctx, options, agentclient.HostCallbacks{})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = client.Close(context.Background()) })
			initialized, err := client.Initialize(ctx, acp.InitializeRequest{ProtocolVersion: 1})
			if err != nil || len(initialized.AuthMethods) != 0 {
				t.Fatalf("PAT initialize auth = %v, error = %v", initialized.AuthMethods, err)
			}
			if _, err := client.Authenticate(ctx, acp.AuthenticateRequest{MethodId: "qodercli-login"}); err == nil {
				t.Fatal("PAT selection allowed CLI login")
			}
			wire, _ := os.ReadFile(trace)
			if strings.Contains(string(wire), "fixture-selected-token") || strings.Contains(string(wire), "authenticate") {
				t.Fatal("PAT or interactive login escaped the frozen launch")
			}
		})
	}
}

func TestQoderProcessDiagnosticSuppressesBareSelectedPAT(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 7*time.Second)
	defer cancel()
	options, _ := fixtureOptions(t, false)
	options.Environment = append(options.Environment, "ACP_FAMILY_QODER_DIAGNOSTIC=1")
	const secret = "fixture-unlabelled-credential"
	options.PersonalAccessToken = secret
	var output bytes.Buffer
	options.Logger = slog.New(slog.NewTextHandler(&output, &slog.HandlerOptions{Level: slog.LevelDebug}))
	client, err := qoder.NewClient(ctx, options, agentclient.HostCallbacks{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close(context.Background()) })
	if _, err := client.Initialize(ctx, acp.InitializeRequest{ProtocolVersion: 1}); err != nil {
		t.Fatal(err)
	}
	session, err := client.NewSession(ctx, acp.NewSessionRequest{Cwd: options.WorkingDirectory})
	if err != nil {
		t.Fatal(err)
	}
	run, err := client.ReservePrompt(ctx, acp.PromptRequest{SessionId: session.SessionId, Prompt: []acp.ContentBlock{acp.TextBlock("test")}}, agentclient.PromptOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if result := run(); result.Receipt.OperationError == nil && (result.Outcome == nil || result.Outcome.OperationError == nil) {
		t.Fatal("expected native process failure")
	}
	if strings.Contains(output.String(), secret) || !strings.Contains(output.String(), "safe startup evidence") {
		t.Fatal("bare PAT escaped the native process diagnostic guard or hid a safe record")
	}
}

func TestQoderManagementReadsSelectedNativeIdentityAndQuotaWithoutPrompt(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 7*time.Second)
	defer cancel()
	options, trace := fixtureOptions(t, false)
	options.ExpectedUserID = "native-id"
	options.Environment = append(options.Environment, `ACP_FAMILY_QODER_QUOTA={"usage":{"userId":"native-id","userQuota":{"total":12,"used":0,"remaining":12,"percentage":0,"unit":"Credits"}},"session":{"total_credits":99}}`)
	identity, err := qoder.ReadNativeIdentity(ctx, options)
	if err != nil || identity.UserID != "native-id" || identity.Email != "account@example.test" || identity.Plan != "Pro" || identity.TokenSource != "selected" {
		t.Fatalf("identity = %+v, error = %v", identity, err)
	}
	client, err := qoder.NewClient(ctx, options, agentclient.HostCallbacks{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close(context.Background()) })
	usage, err := client.ReadAccountUsage(ctx)
	if err != nil || usage.Available == nil || !*usage.Available || len(usage.Buckets) != 1 {
		t.Fatalf("quota = %+v, error = %v", usage, err)
	}
	bucket := usage.Buckets[0]
	if bucket.ID != "userQuota" || bucket.Extra == nil || bucket.Extra.Unit == nil || *bucket.Extra.Unit != "Credits" || bucket.Extra.Used == nil || string(*bucket.Extra.Used) != "0" || len(bucket.Windows) != 1 || bucket.Windows[0].UsedPercent == nil || *bucket.Windows[0].UsedPercent != 0 || bucket.Windows[0].ResetAtUnixSeconds != nil {
		t.Fatalf("quota bucket = %+v", bucket)
	}
	wire, _ := os.ReadFile(trace)
	if string(wire) != "initialize\naccount_info\ninitialize\naccount_info\nget_usage_info\n" {
		t.Fatalf("management wire = %q", wire)
	}
	options.ExpectedUserID = "different"
	if _, err := qoder.ReadNativeIdentity(ctx, options); err == nil {
		t.Fatal("accepted a different native account")
	}
}

func TestQoderQuotaDoesNotCrossNativeSubjectsOrUseSessionCredits(t *testing.T) {
	for _, tc := range []struct {
		name    string
		reply   string
		wantErr bool
	}{
		{"foreign", `{"usage":{"userId":"foreign","userQuota":{"remaining":10}}}`, true},
		{"session-only", `{"usage":null,"session":{"total_credits":99}}`, false},
		{"omitted", `{"session":{"total_credits":99}}`, true},
		{"empty-bucket", `{"usage":{"userId":"native-id","userQuota":{}}}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 7*time.Second)
			defer cancel()
			options, _ := fixtureOptions(t, false)
			options.ExpectedUserID = "native-id"
			options.Environment = append(options.Environment, "ACP_FAMILY_QODER_QUOTA="+tc.reply)
			client, err := qoder.NewClient(ctx, options, agentclient.HostCallbacks{})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = client.Close(context.Background()) })
			usage, err := client.ReadAccountUsage(ctx)
			if (err != nil) != tc.wantErr || len(usage.Buckets) != 0 || !tc.wantErr && (usage.Available == nil || *usage.Available) {
				t.Fatalf("quota = %+v, error = %v", usage, err)
			}
		})
	}
}

func TestQoderLoadOnlyClassifiesTheExactMissingSession(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 7*time.Second)
	defer cancel()
	options, _ := fixtureOptions(t, false)
	client, err := qoder.NewClient(ctx, options, agentclient.HostCallbacks{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close(context.Background()) })
	if _, err := client.Initialize(ctx, acp.InitializeRequest{ProtocolVersion: 1}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []acp.SessionId{"missing", "foreign-missing", "bad-parameters"} {
		_, err := client.LoadSession(ctx, acp.LoadSessionRequest{SessionId: id, Cwd: options.WorkingDirectory})
		var rejected *acp.RequestError
		if !errors.As(err, &rejected) || rejected.Code != -32602 {
			t.Fatalf("load %s returned %v", id, err)
		}
		data, _ := rejected.Data.(map[string]any)
		if (data["classification"] == "session_missing") != (id == "missing") || data["projectRoot"] != nil {
			t.Fatalf("load %s classification = %#v", id, data)
		}
	}
}

func fixtureOptions(t *testing.T, cancel bool) (qoder.Options, string) {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	trace := filepath.Join(dir, "wire")
	env := append(os.Environ(), "ACP_FAMILY_QODER=1", "ACP_FAMILY_TRACE="+trace)
	if cancel {
		env = append(env, "ACP_FAMILY_WIRE_CANCEL=1")
	}
	return qoder.Options{Executable: executable, PrefixArgs: []string{"-test.run=^TestNativeQoder$", "--"}, Environment: env, WorkingDirectory: dir}, trace
}

func checkOptions(t *testing.T, options []acp.SessionConfigOption, thought string) {
	t.Helper()
	if len(options) != 2 || options[0].Select == nil || options[1].Select == nil {
		t.Fatalf("directory = %#v", options)
	}
	reason, model := options[0].Select, options[1].Select
	if reason.Id != "reasoning_effort" || reason.CurrentValue != acp.SessionConfigValueId(thought) || reason.Category == nil || *reason.Category != acp.SessionConfigOptionCategoryThoughtLevel ||
		model.Id != "llm" || model.Category == nil || *model.Category != acp.SessionConfigOptionCategoryModel {
		t.Fatalf("reasoning = %#v model = %#v", reason, model)
	}
}

func TestQoderDirectNormalizesAllConfigurationEvidence(t *testing.T) {
	for _, runtime := range []string{"qoder", "qoder-cn"} {
		t.Run(runtime, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 7*time.Second)
			defer cancel()
			options, trace := fixtureOptions(t, false)
			options.RuntimeID = runtime
			observations := make(chan acp.SessionNotification, 32)
			client, err := qoder.NewClient(ctx, options, agentclient.HostCallbacks{Observe: func(_ context.Context, scope agentclient.Scope, note acp.SessionNotification) error {
				observations <- note
				return nil
			}})
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := client.Close(ctx); err != nil {
					t.Error(err)
				}
			}()
			initialized, err := client.Initialize(ctx, acp.InitializeRequest{ProtocolVersion: 1})
			if err != nil {
				t.Fatal(err)
			}
			if initialized.AgentInfo == nil || initialized.AgentInfo.Name != runtime || acpmeta.RuntimeVersion(initialized.AgentInfo.Meta) != "0.15.0" {
				t.Fatalf("regional version: %#v", initialized.AgentInfo)
			}
			first, err := client.NewSession(ctx, acp.NewSessionRequest{Cwd: options.WorkingDirectory})
			if err != nil {
				t.Fatal(err)
			}
			checkOptions(t, first.ConfigOptions, "native-low")
			if _, err := client.NewSession(ctx, acp.NewSessionRequest{Cwd: options.WorkingDirectory}); err != nil {
				t.Fatal(err)
			}
			loaded, err := client.LoadSession(ctx, acp.LoadSessionRequest{SessionId: first.SessionId, Cwd: options.WorkingDirectory})
			if err != nil {
				t.Fatal(err)
			}
			checkOptions(t, loaded.ConfigOptions, "native-low")
			resumed, err := client.ResumeSession(ctx, acp.ResumeSessionRequest{SessionId: first.SessionId, Cwd: options.WorkingDirectory})
			if err != nil {
				t.Fatal(err)
			}
			checkOptions(t, resumed.ConfigOptions, "native-low")
			if _, err := client.SetSessionConfigOption(ctx, acp.SetSessionConfigOptionRequest{ValueId: &acp.SetSessionConfigOptionValueId{SessionId: first.SessionId, ConfigId: "reasoning_effort", Value: "other"}}); err == nil {
				t.Fatal("model ID was accepted as a reasoning effort")
			}
			set, err := client.SetSessionConfigOption(ctx, acp.SetSessionConfigOptionRequest{ValueId: &acp.SetSessionConfigOptionValueId{SessionId: first.SessionId, ConfigId: "reasoning_effort", Value: "native-high"}})
			if err != nil {
				t.Fatal(err)
			}
			checkOptions(t, set.ConfigOptions, "native-high")
			run, err := client.ReservePrompt(ctx, acp.PromptRequest{SessionId: first.SessionId, Prompt: []acp.ContentBlock{acp.TextBlock("test")}}, agentclient.PromptOptions{})
			if err != nil {
				t.Fatal(err)
			}
			result := run()
			if result.Outcome == nil || result.Outcome.StopReason == nil || *result.Outcome.StopReason != acp.StopReasonEndTurn || !result.Outcome.ObservedOutput {
				t.Fatalf("outcome = %#v", result)
			}
			var dynamic, ordinary bool
			for !dynamic || !ordinary {
				select {
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				case event := <-observations:
					if event.SessionId == "foreign" {
						t.Fatalf("foreign update: %#v", event)
					}
					if entry := event.Update.ConfigOptionUpdate; event.SessionId == first.SessionId && entry != nil {
						thought := entry.ConfigOptions[0].Select.CurrentValue
						checkOptions(t, entry.ConfigOptions, string(thought))
						dynamic = dynamic || thought == "native-high"
					}
					if text := event.Update.AgentMessageChunk; text != nil {
						ordinary = text.Content.Text != nil && strings.Contains(text.Content.Text.Text, "WritableIterable")
					}
				}
			}
			wire, _ := os.ReadFile(trace)
			if strings.Count(string(wire), `"method":"session/set_config_option"`) != 1 || strings.Contains(string(wire), "cursor/") || strings.Contains(string(wire), "parameterizedModelPicker") ||
				!strings.Contains(string(wire), `"configId":"reasoning_effort","sessionId":"first","value":"native-high"`) {
				t.Fatalf("native settings trace = %s", wire)
			}
		})
	}
}

func TestQoderPublicACPUsesSameNativeCancelAndResult(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 7*time.Second)
	defer cancel()
	options, trace := fixtureOptions(t, true)
	var native agentclient.Client
	projected, err := agentclient.NewACPAgent(func(host agentclient.HostCallbacks) (agentclient.Client, error) {
		client, err := qoder.NewClient(ctx, options, host)
		native = client
		return client, err
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := native.Close(ctx); err != nil {
			t.Error(err)
		}
	}()
	in, send := io.Pipe()
	receive, out := io.Pipe()
	defer send.Close()
	defer receive.Close()
	defer in.Close()
	defer out.Close()
	stop := context.AfterFunc(ctx, func() { in.Close(); receive.Close() })
	defer stop()
	acp.NewAgentSideConnectionWithBinder(projected, out, in, projected.SetAgentConnection)
	encoder, scanner := json.NewEncoder(send), bufio.NewScanner(receive)
	write := func(id int, method string, params any) {
		t.Helper()
		frame := map[string]any{"jsonrpc": "2.0", "method": method, "params": params}
		if id > 0 {
			frame["id"] = id
		}
		if err := encoder.Encode(frame); err != nil {
			t.Fatal(err)
		}
	}
	wait := func(predicate func(map[string]json.RawMessage) bool) map[string]json.RawMessage {
		t.Helper()
		for scanner.Scan() {
			var frame map[string]json.RawMessage
			if err := json.Unmarshal(scanner.Bytes(), &frame); err != nil {
				t.Fatal(err)
			}
			if predicate(frame) {
				return frame
			}
		}
		t.Fatalf("no matching public frame: %v", scanner.Err())
		return nil
	}
	reply := func(id string) map[string]json.RawMessage {
		t.Helper()
		frame := wait(func(frame map[string]json.RawMessage) bool { return string(frame["id"]) == id })
		if frame["error"] != nil {
			t.Fatalf("public response error: %s", frame["error"])
		}
		return frame
	}
	write(1, "initialize", acp.InitializeRequest{ProtocolVersion: 1})
	reply("1")
	write(2, "session/new", acp.NewSessionRequest{Cwd: options.WorkingDirectory, McpServers: []acp.McpServer{}})
	var opened acp.NewSessionResponse
	if err := json.Unmarshal(reply("2")["result"], &opened); err != nil {
		t.Fatal(err)
	}
	checkOptions(t, opened.ConfigOptions, "native-low")
	write(3, "session/set_config_option", acp.SetSessionConfigOptionRequest{ValueId: &acp.SetSessionConfigOptionValueId{SessionId: opened.SessionId, ConfigId: "reasoning_effort", Value: "native-high"}})
	var configured acp.SetSessionConfigOptionResponse
	if err := json.Unmarshal(reply("3")["result"], &configured); err != nil {
		t.Fatal(err)
	}
	checkOptions(t, configured.ConfigOptions, "native-high")
	write(4, "session/prompt", acp.PromptRequest{SessionId: opened.SessionId, Prompt: []acp.ContentBlock{acp.TextBlock("test")}})
	wait(func(frame map[string]json.RawMessage) bool {
		if string(frame["method"]) != `"session/update"` {
			return false
		}
		var note acp.SessionNotification
		if json.Unmarshal(frame["params"], &note) != nil {
			return false
		}
		if note.SessionId == "foreign" {
			t.Fatalf("foreign public frame: %s", frame["params"])
		}
		return note.Update.AgentMessageChunk != nil
	})
	write(0, "session/cancel", acp.CancelNotification{SessionId: opened.SessionId})
	var result acp.PromptResponse
	if err := json.Unmarshal(reply("4")["result"], &result); err != nil {
		t.Fatal(err)
	}
	usage, ok := acpmeta.ReadTurnUsage(result.Meta)
	if result.StopReason != acp.StopReasonCancelled || !ok || usage.InputTokens != nil || usage.Complete ||
		len(usage.Observations) != 1 || usage.Observations[0].Coverage != "unknown" ||
		usage.Observations[0].Tokens.InputTokens != nil ||
		usage.Observations[0].InputTotalTokens == nil || *usage.Observations[0].InputTotalTokens != 12 {
		t.Fatalf("public native result = %#v, usage = %#v", result, usage)
	}
	wire, _ := os.ReadFile(trace)
	if strings.Count(string(wire), `"method":"session/cancel"`) != 1 || strings.Contains(string(wire), "cursor/") {
		t.Fatalf("native control trace = %s", wire)
	}
}

func TestQoderNativeModePermissionAndMCPUseTheSameSession(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 7*time.Second)
	defer cancel()
	options, trace := fixtureOptions(t, false)
	options.Environment = append(options.Environment, "ACP_FAMILY_QODER_PERMISSION=1")
	seen := make(chan agentclient.Scope, 1)
	client, err := qoder.NewClient(ctx, options, agentclient.HostCallbacks{
		RequestPermission: func(_ context.Context, scope agentclient.Scope, req acp.RequestPermissionRequest) (acp.RequestPermissionResponse, error) {
			if req.ToolCall.ToolCallId != "qoder-tool" {
				t.Errorf("permission tool = %q", req.ToolCall.ToolCallId)
			}
			seen <- scope
			return acp.RequestPermissionResponse{Outcome: acp.NewRequestPermissionOutcomeSelected("native-allow")}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close(context.Background()) })
	if _, err := client.Initialize(ctx, acp.InitializeRequest{ProtocolVersion: 1}); err != nil {
		t.Fatal(err)
	}
	servers := []acp.McpServer{{Http: &acp.McpServerHttpInline{Type: "http", Url: "https://example.test/mcp", Name: "project-tools", Headers: []acp.HttpHeader{}}}}
	session, err := client.NewSession(ctx, acp.NewSessionRequest{Cwd: options.WorkingDirectory, McpServers: servers, Meta: acpmeta.DeveloperInstructionsMetadata(acp.Ptr("fixture developer instructions"))})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.SetSessionMode(ctx, acp.SetSessionModeRequest{SessionId: session.SessionId, ModeId: "auto"}); err != nil {
		t.Fatal(err)
	}
	run, err := client.ReservePrompt(ctx, acp.PromptRequest{SessionId: session.SessionId, Prompt: []acp.ContentBlock{acp.TextBlock("question")}}, agentclient.PromptOptions{HostInvocationID: "host-invocation"})
	if err != nil {
		t.Fatal(err)
	}
	result := run()
	if result.Outcome == nil || result.Outcome.StopReason == nil || *result.Outcome.StopReason != acp.StopReasonEndTurn {
		t.Fatalf("outcome = %+v", result)
	}
	select {
	case scope := <-seen:
		if scope.SessionID != session.SessionId || scope.HostInvocationID != "host-invocation" || scope.NativeInvocationID == "" {
			t.Fatalf("permission scope = %+v", scope)
		}
	default:
		t.Fatal("permission callback was not received")
	}
	wire, _ := os.ReadFile(trace)
	for _, piece := range []string{`"name":"project-tools"`, `"modeId":"auto"`, `fixture developer instructions`, `"optionId":"native-allow"`} {
		if !strings.Contains(string(wire), piece) {
			t.Fatalf("native wire is missing %q: %s", piece, wire)
		}
	}
	if strings.Contains(string(wire), `"developerInstructions"`) {
		t.Fatalf("host-only instruction metadata reached the peer: %s", wire)
	}
}

func TestQoderPromptUsageExposesVerifiedInvocationTokens(t *testing.T) {
	for provider, variable := range map[string]string{"qoder": "QODER_EXPOSE_TOKEN_USAGE", "qoder-cn": "QODERCN_EXPOSE_TOKEN_USAGE"} {
		t.Run(provider, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			options, _ := fixtureOptions(t, false)
			options.RuntimeID = provider
			options.Environment = append(options.Environment, "ACP_FAMILY_QODER_USAGE_VERSION=1.1.53",
				"ACP_FAMILY_QODER_USAGE_VARIABLE="+variable, variable+"=0")
			client, err := qoder.NewClient(ctx, options, agentclient.HostCallbacks{})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = client.Close(context.Background()) })
			if _, err := client.Initialize(ctx, acp.InitializeRequest{ProtocolVersion: 1}); err != nil {
				t.Fatal(err)
			}
			session, err := client.NewSession(ctx, acp.NewSessionRequest{Cwd: options.WorkingDirectory})
			if err != nil {
				t.Fatal(err)
			}
			for i, want := range []int64{7, 11, 0, 0} {
				run, err := client.ReservePrompt(ctx, acp.PromptRequest{SessionId: session.SessionId, Prompt: []acp.ContentBlock{acp.TextBlock("fixture")}}, agentclient.PromptOptions{HostInvocationID: fmt.Sprint(i)})
				if err != nil {
					t.Fatal(err)
				}
				result := run()
				if result.Outcome == nil || result.Outcome.Turn.Usage == nil {
					t.Fatalf("usage receipt missing: %+v", result)
				}
				usage := result.Outcome.Turn.Usage
				if want == 0 {
					if usage.OutputTokens != nil || len(usage.Models) != 0 {
						t.Fatalf("zeroed or conflicting counters were attributed: %+v", usage)
					}
					continue
				}
				if usage.Complete || usage.InputTokens != nil || usage.CacheReadTokens != nil || usage.CacheWriteTokens != nil ||
					usage.ReasoningTokens != nil || usage.OutputTokens == nil || *usage.OutputTokens != want {
					t.Fatalf("incorrect measured invocation subtotal: %+v", usage)
				}
				model := usage.Models["qfmodel"]
				if model.OutputTokens == nil || *model.OutputTokens != want || model.InputTokens != nil {
					t.Fatalf("incorrect model attribution: %+v", usage.Models)
				}
				input := []int64{24, 35}[i]
				if len(usage.Observations) != 1 || usage.Observations[0].InputTotalTokens == nil ||
					*usage.Observations[0].InputTotalTokens != input || usage.Observations[0].TotalTokens == nil ||
					*usage.Observations[0].TotalTokens != input+want {
					t.Fatalf("native overlapping counters were lost: %+v", usage.Observations)
				}
			}
		})
	}
}

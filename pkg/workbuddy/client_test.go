package workbuddy_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JieWaZi/acp-go/pkg/acpmeta"
	agentclient "github.com/JieWaZi/acp-go/pkg/agentclient"
	"github.com/JieWaZi/acp-go/pkg/workbuddy"
	acp "github.com/JieWaZi/acp-go/sdk"
)

func TestWorkBuddyNativeQuestions(t *testing.T) {
	for _, name := range []string{"answer", "decline", "legacy", "cancel", "ack-failure", "approval", "stale"} {
		t.Run(name, func(t *testing.T) {
			ctx, stop := context.WithTimeout(context.Background(), 10*time.Second)
			defer stop()
			executable, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			cwd := t.TempDir()
			trace := filepath.Join(cwd, "trace.jsonl")
			form := make(chan agentclient.Scope, 2)
			approvals := make(chan agentclient.Scope, 2)
			client, err := workbuddy.NewClient(ctx, workbuddy.Options{
				Executable: executable, PrefixArgs: []string{"-test.run=^TestNativeWorkBuddy$", "--"},
				Environment:      append(os.Environ(), "WORKBUDDY_FIXTURE="+name, "WORKBUDDY_TRACE="+trace),
				WorkingDirectory: cwd,
			}, agentclient.HostCallbacks{
				CreateElicitation: func(ctx context.Context, scope agentclient.Scope, request acp.UnstableCreateElicitationRequest) (acp.UnstableCreateElicitationResponse, error) {
					form <- scope
					if name == "cancel" {
						<-ctx.Done()
					}
					if name == "decline" {
						return acp.NewUnstableCreateElicitationResponseDecline(), nil
					}
					if request.Form == nil || request.Form.Message != "Choose one" || request.Form.Meta["sessionId"] != "workbuddy-session" {
						return acp.UnstableCreateElicitationResponse{}, fmt.Errorf("unexpected elicitation: %#v", request)
					}
					answer := acp.NewUnstableCreateElicitationResponseAccept()
					answer.Accept.Content = map[string]any{"question_0_custom": "a custom answer"}
					return answer, nil
				},
				RequestPermission: func(_ context.Context, scope agentclient.Scope, request acp.RequestPermissionRequest) (acp.RequestPermissionResponse, error) {
					approvals <- scope
					return acp.RequestPermissionResponse{Outcome: acp.NewRequestPermissionOutcomeSelected(request.Options[0].OptionId)}, nil
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := client.Close(ctx); err != nil {
					t.Error(err)
				}
			}()
			initialized, err := client.Initialize(ctx, acp.InitializeRequest{ProtocolVersion: 1})
			if err != nil || initialized.AgentInfo == nil || acpmeta.RuntimeVersion(initialized.AgentInfo.Meta) != "2.151.0" {
				t.Fatalf("initialize: %#v %v", initialized, err)
			}
			session, err := client.NewSession(ctx, acp.NewSessionRequest{Cwd: cwd})
			if err != nil {
				t.Fatal(err)
			}
			run, err := client.ReservePrompt(ctx, acp.PromptRequest{SessionId: session.SessionId, Prompt: []acp.ContentBlock{acp.TextBlock("test question")}}, agentclient.PromptOptions{HostInvocationID: "host-turn"})
			if err != nil {
				t.Fatal(err)
			}
			done := make(chan agentclient.PromptResult, 1)
			go func() { done <- run() }()
			want := form
			if name == "approval" {
				want = approvals
			}
			select {
			case scope := <-want:
				if scope.SessionID != session.SessionId || scope.HostInvocationID != "host-turn" || scope.NativeInvocationID == "" {
					t.Fatalf("incorrect interaction scope: %#v", scope)
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			if name == "cancel" {
				if err := client.Cancel(acp.CancelNotification{SessionId: session.SessionId}); err != nil {
					t.Fatal(err)
				}
			}
			var result agentclient.PromptResult
			select {
			case result = <-done:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			if result.Outcome == nil || result.Outcome.StopReason == nil || (result.Outcome.DeliveryError != nil) != (name == "ack-failure") {
				t.Fatalf("incorrect outcome: %#v", result.Outcome)
			}
			if len(form) != 0 || len(approvals) != 0 {
				t.Fatal("a question and a generic approval both reached the host")
			}
			if name == "stale" {
				run, err := client.ReservePrompt(ctx, acp.PromptRequest{SessionId: session.SessionId, Prompt: []acp.ContentBlock{acp.TextBlock("successor")}}, agentclient.PromptOptions{HostInvocationID: "successor"})
				if err != nil {
					t.Fatal(err)
				}
				second := run()
				if second.Outcome == nil || second.Outcome.StopReason == nil || len(form) != 0 {
					t.Fatalf("stale question was delivered to the successor: %#v", second.Outcome)
				}
			}
			raw, err := os.ReadFile(trace)
			if err != nil {
				t.Fatal(err)
			}
			log := string(raw)
			if !strings.Contains(log, `"codebuddy.ai":{"question":true}`) {
				t.Fatalf("legacy form capability was not negotiated: %s", log)
			}
			needResolve := name == "answer" || name == "decline" || name == "ack-failure" || name == "stale"
			if strings.Contains(log, "_codebuddy.ai/resolveInterruption") != needResolve {
				t.Fatalf("unexpected native answer handoff: %s", log)
			}
			if name == "stale" && strings.Count(log, "_codebuddy.ai/resolveInterruption") != 1 ||
				name == "answer" && !strings.Contains(log, `"Choose one":"a custom answer"`) ||
				name == "decline" && !strings.Contains(log, `"decision":"deny"`) ||
				name == "legacy" && !strings.Contains(log, `"native-question":"a custom answer"`) {
				t.Fatalf("wrong native answer: %s", log)
			}
		})
	}
}

func TestWorkBuddyNativeEvidence(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cwd := t.TempDir()
	trace := filepath.Join(cwd, "trace.jsonl")
	client, err := workbuddy.NewClient(ctx, workbuddy.Options{
		Executable: executable, PrefixArgs: []string{"-test.run=^TestNativeWorkBuddy$", "--"},
		Environment:      append(os.Environ(), "WORKBUDDY_FIXTURE=evidence", "WORKBUDDY_TRACE="+trace),
		WorkingDirectory: cwd,
	}, agentclient.HostCallbacks{})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close(ctx)
	if _, err := client.Initialize(ctx, acp.InitializeRequest{ProtocolVersion: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.NewSession(ctx, acp.NewSessionRequest{Cwd: t.TempDir()}); err == nil {
		t.Fatal("CodeBuddy accepted a different cwd it would silently ignore")
	}
	_, err = client.LoadSession(ctx, acp.LoadSessionRequest{SessionId: "workbuddy-session", Cwd: cwd})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.SetSessionMode(ctx, acp.SetSessionModeRequest{SessionId: "workbuddy-session", ModeId: "auto"}); err != nil {
		t.Fatalf("confirmed native permission change: %v", err)
	}
	run, err := client.ReservePrompt(ctx, acp.PromptRequest{SessionId: "workbuddy-session", Prompt: []acp.ContentBlock{acp.TextBlock("test evidence")}}, agentclient.PromptOptions{})
	if err != nil {
		t.Fatal(err)
	}
	result := run()
	outcome := result.Outcome
	if outcome == nil || outcome.Turn.Status != acpmeta.NativeTurnFailed || outcome.NativeFailure == nil ||
		outcome.Turn.NativeTurnID == nil || *outcome.Turn.NativeTurnID != "0198a1b2c3d47e5f8a9b0c1d2e3f4a5b" ||
		result.Receipt.Admission != agentclient.AdmissionConfirmed ||
		outcome.ContextUsage.UsedTokens == nil || *outcome.ContextUsage.UsedTokens != 320 ||
		outcome.ContextUsage.WindowTokens == nil || *outcome.ContextUsage.WindowTokens != 100000 {
		t.Fatalf("native evidence was lost: %#v %#v", result.Receipt, outcome)
	}
	usage := outcome.Turn.Usage
	if usage == nil || usage.Complete || usage.InputTokens != nil || usage.OutputTokens != nil || usage.Cost != nil ||
		len(usage.Observations) != 1 || usage.Observations[0].Coverage != "latest_model_request" ||
		usage.Observations[0].Tokens.OutputTokens == nil || *usage.Observations[0].Tokens.OutputTokens != 17 {
		t.Fatalf("one model request was not retained as auxiliary usage: %#v", usage)
	}
	if _, err := client.LoadSession(ctx, acp.LoadSessionRequest{SessionId: "unknown-session", Cwd: cwd}); err == nil {
		t.Fatal("CodeBuddy returned a blank ACK for an unknown ID and was allowed to bind")
	}
}

type questionPeer struct {
	// Client rejects capabilities outside this test's form and observation.
	acp.Client
	// forms records native questions seen at the public ACP endpoint.
	forms chan acp.UnstableCreateElicitationRequest
}

func (*questionPeer) SessionUpdate(context.Context, acp.SessionNotification) error { return nil }

func (peer *questionPeer) UnstableCreateElicitation(_ context.Context, request acp.UnstableCreateElicitationRequest) (acp.UnstableCreateElicitationResponse, error) {
	peer.forms <- request
	answer := acp.NewUnstableCreateElicitationResponseAccept()
	answer.Accept.Content = map[string]any{"question_0": "A"}
	return answer, nil
}

func TestWorkBuddyWireQuestion(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cwd := t.TempDir()
	trace := filepath.Join(cwd, "trace.jsonl")
	agent, err := agentclient.NewACPAgent(func(host agentclient.HostCallbacks) (agentclient.Client, error) {
		return workbuddy.NewClient(ctx, workbuddy.Options{
			Executable: executable, PrefixArgs: []string{"-test.run=^TestNativeWorkBuddy$", "--"},
			Environment:      append(os.Environ(), "WORKBUDDY_FIXTURE=answer", "WORKBUDDY_TRACE="+trace),
			WorkingDirectory: cwd,
		}, host)
	})
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close(ctx)
	left, right := net.Pipe()
	defer left.Close()
	defer right.Close()
	peer := &questionPeer{forms: make(chan acp.UnstableCreateElicitationRequest, 1)}
	acp.NewAgentSideConnectionWithBinder(agent, left, left, agent.SetAgentConnection)
	conn := acp.NewClientSideConnection(peer, right, right)
	if _, err := conn.Initialize(ctx, acp.InitializeRequest{ProtocolVersion: 1, ClientCapabilities: acp.ClientCapabilities{
		Elicitation: &acp.ElicitationCapabilities{Form: &acp.ElicitationFormCapabilities{}},
	}}); err != nil {
		t.Fatal(err)
	}
	session, err := conn.NewSession(ctx, acp.NewSessionRequest{Cwd: cwd, McpServers: []acp.McpServer{}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = conn.CallExtension(ctx, "_codebuddy.ai/getUserInfo", struct{}{})
	var unsupported *acp.RequestError
	if !errors.As(err, &unsupported) || unsupported.Code != -32601 {
		t.Fatalf("untyped ACP extension must be rejected before native transport: %v", err)
	}
	response, err := conn.Prompt(ctx, acp.PromptRequest{SessionId: session.SessionId, Prompt: []acp.ContentBlock{acp.TextBlock("test")}})
	if err != nil || response.StopReason != acp.StopReasonEndTurn {
		t.Fatalf("wire outcome: %#v %v", response, err)
	}
	if len(peer.forms) != 1 || (<-peer.forms).Form.Message != "Choose one" {
		t.Fatal("public ACP peer did not see the native question")
	}
	raw, err := os.ReadFile(trace)
	if err != nil || !strings.Contains(string(raw), `"answers":{"Choose one":"A"}`) {
		t.Fatalf("public ACP answer was not sent to CodeBuddy: %v %s", err, raw)
	}
}

func TestWorkBuddyCatalogKeepsOpeningReceiptWithoutModelWrites(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cwd := t.TempDir()
	trace := filepath.Join(cwd, "trace.jsonl")
	client, err := workbuddy.NewClient(ctx, workbuddy.Options{
		Executable: executable, PrefixArgs: []string{"-test.run=^TestNativeWorkBuddy$", "--"},
		Environment:      append(os.Environ(), "WORKBUDDY_FIXTURE=catalog", "WORKBUDDY_TRACE="+trace),
		WorkingDirectory: cwd,
	}, agentclient.HostCallbacks{})
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
	if err != nil || catalog.DefaultID == nil || *catalog.DefaultID != "fast-model" || catalog.Options.Ungrouped == nil || len(*catalog.Options.Ungrouped) != 2 {
		t.Fatalf("native catalog: %+v %v", catalog, err)
	}
	if len(catalog.Modes) != 2 || catalog.Modes[0].Id != "default" || catalog.Modes[1].Id != "auto" {
		t.Fatalf("modern permission modes lost: %+v", catalog.Modes)
	}
	for _, row := range *catalog.Options.Ungrouped {
		config, ok := acpmeta.ReadModelConfigCatalog(row.Meta)
		if !ok || (row.Value == "fast-model" && (len(config.Options) != 2 || config.Options[0].Select == nil || config.Options[0].Select.CurrentValue != "low" || len(config.UnknownConfigIDs) != 0)) ||
			(row.Value == "DeepSeek-V4.1-Flash" && (len(config.Options) != 0 || len(config.UnknownConfigIDs) == 0)) {
			t.Fatalf("incorrect model configuration for %q: %+v", row.Value, config)
		}
	}
	opened, err := client.NewSession(ctx, acp.NewSessionRequest{Cwd: cwd})
	if err != nil || opened.Modes == nil || opened.Modes.CurrentModeId != "default" {
		t.Fatalf("native mode on open: %+v %v", opened.Modes, err)
	}
	if _, err := client.SetSessionMode(ctx, acp.SetSessionModeRequest{SessionId: opened.SessionId, ModeId: "auto"}); err != nil {
		t.Fatal(err)
	}
}

func TestWorkBuddyDisposableCatalogReadsEffortWithUnlistedOpeningModel(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	defer cancel()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cwd, home := t.TempDir(), t.TempDir()
	path := filepath.Join(home, "settings.json")
	if err := os.WriteFile(path, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	root, err := workbuddy.PrepareModelCatalog(home)
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	client, err := workbuddy.NewClient(ctx, workbuddy.Options{
		Executable: executable, PrefixArgs: []string{"-test.run=^TestNativeWorkBuddy$", "--"},
		Environment:      append(os.Environ(), "WORKBUDDY_FIXTURE=disposable-catalog", "WORKBUDDY_TRACE="+filepath.Join(cwd, "trace.jsonl"), "CODEBUDDY_CONFIG_DIR="+home),
		WorkingDirectory: cwd, CatalogConfigHome: root,
	}, agentclient.HostCallbacks{})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close(context.Background())
	if _, err := client.Initialize(ctx, acp.InitializeRequest{ProtocolVersion: 1}); err != nil {
		t.Fatal(err)
	}
	catalog, err := client.ReadModels(ctx)
	if err != nil || catalog.DefaultID != nil || catalog.Options.Ungrouped == nil {
		t.Fatalf("catalog = %+v, %v", catalog, err)
	}
	want := map[acp.SessionConfigValueId]acp.SessionConfigValueId{"fast-model": "low", "DeepSeek-V4.1-Flash": "max"}
	for _, row := range *catalog.Options.Ungrouped {
		config, ok := acpmeta.ReadModelConfigCatalog(row.Meta)
		if !ok || len(config.Options) != 1 || config.Options[0].Select == nil || config.Options[0].Select.CurrentValue != want[row.Value] {
			t.Fatalf("%s config = %+v", row.Value, config)
		}
		delete(want, row.Value)
	}
	if len(want) != 0 {
		t.Fatalf("missing models: %v", want)
	}
	source, sourceErr := os.ReadFile(path)
	copy, copyErr := os.ReadFile(filepath.Join(root, "settings.json"))
	if sourceErr != nil || copyErr != nil || string(source) != "original" || string(copy) != "DeepSeek-V4.1-Flash" {
		t.Fatalf("model write escaped the disposable scope: %v %v", sourceErr, copyErr)
	}
}

func TestNativeWorkBuddy(t *testing.T) {
	scenario := os.Getenv("WORKBUDDY_FIXTURE")
	if scenario == "" {
		return
	}
	if os.Args[len(os.Args)-1] == "--version" {
		fmt.Println("2.151.0")
		os.Exit(0)
	}
	trace, err := os.Create(os.Getenv("WORKBUDDY_TRACE"))
	if err != nil || os.Args[len(os.Args)-1] != "--acp" {
		os.Exit(2)
	}
	in := bufio.NewScanner(os.Stdin)
	out := json.NewEncoder(os.Stdout)
	var promptID json.RawMessage
	modeOption := func(current string) map[string]any {
		return map[string]any{"type": "select", "id": "mode", "category": "mode", "name": "Permission Mode",
			"currentValue": current, "options": []map[string]string{{"value": "default", "name": "Default"}, {"value": "auto", "name": "Auto"}}}
	}
	update := func(sessionID string, content map[string]any) {
		_ = out.Encode(map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": map[string]any{"sessionId": sessionID, "update": content}})
	}
	reply := func(id any, result any) {
		_ = out.Encode(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
	}
	request := func(id, method string, params any) {
		_ = out.Encode(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	}
	disposableOptions := func(model, effort string) []any {
		return []any{
			map[string]any{"type": "select", "id": "model", "category": "model", "name": "Model", "currentValue": model, "options": []map[string]string{{"value": "fast-model", "name": "Fast"}, {"value": "DeepSeek-V4.1-Flash", "name": "DeepSeek"}}},
			map[string]any{"type": "select", "id": "thought_level", "category": "thought_level", "name": "Thinking", "currentValue": effort, "options": []map[string]string{{"value": effort, "name": effort}}},
		}
	}
	for in.Scan() {
		var frame struct {
			// ID identifies the request or reply to the native reverse request.
			ID json.RawMessage `json:"id"`
			// Method distinguishes host requests from replies.
			Method string `json:"method"`
			// Params identifies the session selected in a load.
			Params map[string]any `json:"params"`
		}
		if json.Unmarshal(in.Bytes(), &frame) != nil {
			os.Exit(3)
		}
		fmt.Fprintln(trace, string(in.Bytes()))
		switch frame.Method {
		case "initialize":
			reply(frame.ID, map[string]any{"protocolVersion": 1, "agentCapabilities": map[string]any{"loadSession": true}})
		case "session/new":
			if scenario == "disposable-catalog" {
				reply(frame.ID, map[string]any{"sessionId": "probe", "configOptions": disposableOptions("unlisted-alias", "medium")})
				continue
			}
			if scenario == "catalog" {
				reply(frame.ID, map[string]any{"sessionId": "workbuddy-probe", "configOptions": []any{
					map[string]any{"type": "select", "id": "model", "category": "model", "name": "Model", "currentValue": "fast-model", "options": []map[string]string{{"value": "fast-model", "name": "Fast"}, {"value": "DeepSeek-V4.1-Flash", "name": "DeepSeek V4.1 Flash"}}},
					map[string]any{"type": "select", "id": "thought_level", "category": "thought_level", "name": "Reasoning", "currentValue": "low", "options": []map[string]string{{"value": "low", "name": "Low"}}},
					modeOption("default"),
				}})
				continue
			}
			reply(frame.ID, map[string]any{"sessionId": "workbuddy-session"})
		case "session/load":
			if frame.Params["sessionId"] == "workbuddy-session" {
				update("workbuddy-session", map[string]any{"sessionUpdate": "user_message_chunk", "content": map[string]any{"type": "text", "text": "recorded input"}})
				reply(frame.ID, map[string]any{
					"modes":         map[string]any{"currentModeId": "default", "availableModes": []map[string]string{{"id": "default", "name": "Default"}, {"id": "auto", "name": "Auto"}}},
					"configOptions": []any{modeOption("default")},
				})
				continue
			}
			reply(frame.ID, map[string]any{})
		case "session/set_config_option":
			if scenario == "disposable-catalog" {
				model, _ := frame.Params["value"].(string)
				effort := map[string]string{"fast-model": "low", "DeepSeek-V4.1-Flash": "max"}[model]
				if frame.Params["configId"] != "model" || effort == "" || os.WriteFile(filepath.Join(os.Getenv("CODEBUDDY_CONFIG_DIR"), "settings.json"), []byte(model), 0600) != nil {
					os.Exit(5)
				}
				reply(frame.ID, map[string]any{"configOptions": disposableOptions(model, effort)})
				continue
			}
			if frame.Params["configId"] != "mode" || frame.Params["value"] != "auto" {
				os.Exit(5)
			}
			reply(frame.ID, map[string]any{"configOptions": []any{modeOption("auto")}})
		case "session/prompt":
			promptID = frame.ID
			if scenario == "evidence" {
				update("workbuddy-session", map[string]any{
					"sessionUpdate": "usage_update", "used": 320, "size": 100000,
					"cost":  map[string]any{"amount": 2, "currency": ""},
					"_meta": map[string]any{"usage": map[string]any{"input_tokens": 320, "output_tokens": 17, "credit": 2}},
				})
				reply(frame.ID, map[string]any{"stopReason": "refusal", "userMessageId": "native-user-message", "_meta": map[string]any{
					"codebuddy.ai/conversationRequestId": "0198a1b2c3d47e5f8a9b0c1d2e3f4a5b",
					"codebuddy.ai/errorMessage":          "provider error with untrusted data",
				}})
				continue
			}
			questions := map[string]any{"questions": []any{map[string]any{"id": "native-question", "question": "Choose one", "options": []any{map[string]any{"label": "A"}}}}}
			if scenario == "legacy" {
				request("interaction", "_codebuddy.ai/question", map[string]any{"sessionId": "workbuddy-session", "toolCallId": "question", "inputType": "question", "schema": questions})
				continue
			}
			tool := "AskUserQuestion"
			if scenario == "approval" {
				tool = "Bash"
			}
			request("interaction", "session/request_permission", map[string]any{
				"sessionId": "workbuddy-session",
				"toolCall":  map[string]any{"toolCallId": "question", "_meta": map[string]string{"codebuddy.ai/toolName": tool}, "rawInput": questions},
				"options":   []any{map[string]string{"optionId": "allow", "kind": "allow_once", "name": "Allow"}},
			})
		case "_codebuddy.ai/resolveInterruption":
			reply(frame.ID, map[string]any{"resolved": scenario != "ack-failure", "queued": scenario == "ack-failure"})
		case "session/cancel":
			reply(promptID, map[string]any{"stopReason": "cancelled"})
		case "":
			if scenario != "cancel" && string(frame.ID) == `"interaction"` {
				reply(promptID, map[string]any{"stopReason": "end_turn"})
			}
		default:
			os.Exit(4)
		}
	}
	os.Exit(0)
}

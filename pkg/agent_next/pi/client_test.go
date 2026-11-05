package pi

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JieWaZi/acp-go/pkg/acpmeta"
	agentclient "github.com/JieWaZi/acp-go/pkg/agent_next"
	acp "github.com/JieWaZi/acp-go/sdk"
)

func fixtureClient(t *testing.T, host agentclient.HostCallbacks) (*Client, context.Context, acp.NewSessionResponse) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	t.Cleanup(cancel)
	dir := t.TempDir()
	client, err := NewClient(ctx, fixtureOptions(t, dir), host)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 4*time.Second)
		defer stop()
		if err := client.Close(cleanup); err != nil {
			t.Error(err)
		}
	})
	if _, err = client.Initialize(ctx, acp.InitializeRequest{ProtocolVersion: acp.ProtocolVersionNumber}); err != nil {
		t.Fatal(err)
	}
	session, err := client.NewSession(ctx, acp.NewSessionRequest{Cwd: dir, McpServers: []acp.McpServer{}})
	if err != nil {
		t.Fatal(err)
	}
	return client, ctx, session
}

func fixtureOptions(t *testing.T, dir string) Options {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return Options{
		Executable: executable, PrefixArgs: []string{"-test.run=^TestPiRPCFixture$", "--"},
		Environment:      append(os.Environ(), "ACP_PI_NEXT_FIXTURE=1", "PI_CODING_AGENT_DIR="+dir),
		WorkingDirectory: dir, PermissionMode: "full-access",
	}
}

type publicHost struct {
	// Client supplies unused SDK callbacks for the public wire fixture.
	acp.Client
}

func (publicHost) SessionUpdate(context.Context, acp.SessionNotification) error { return nil }

func TestPublicACPPreservesHandledAndTerminalUsage(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	dir := t.TempDir()
	agent, err := agentclient.NewACPAgent(func(host agentclient.HostCallbacks) (agentclient.Client, error) {
		return NewClient(ctx, fixtureOptions(t, dir), host)
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := agent.Close(ctx); err != nil {
			t.Error(err)
		}
	}()
	toAgent, fromClient := io.Pipe()
	toClient, fromAgent := io.Pipe()
	defer toAgent.Close()
	defer fromClient.Close()
	defer toClient.Close()
	defer fromAgent.Close()
	peer := acp.NewClientSideConnection(publicHost{}, fromClient, toClient)
	_ = acp.NewAgentSideConnectionWithBinder(agent, fromAgent, toAgent, agent.SetAgentConnection)
	capabilities, err := peer.Initialize(ctx, acp.InitializeRequest{ProtocolVersion: 1, ClientCapabilities: acp.ClientCapabilities{Meta: map[string]any{acpmeta.NativeTurnReceiptsCapability: true}}})
	if err != nil || capabilities.AgentInfo == nil || acpmeta.RuntimeVersion(capabilities.AgentInfo.Meta) != "0.85.1" || capabilities.Meta[acpmeta.NativeTurnReceiptsCapability] != acpmeta.NativeTurnReceiptsMethod {
		t.Fatalf("Pi native receipt negotiation: %+v %v", capabilities, err)
	}
	session, err := peer.NewSession(ctx, acp.NewSessionRequest{Cwd: dir, McpServers: []acp.McpServer{}})
	if err != nil {
		t.Fatal(err)
	}
	prompt := func(text string) acpmeta.NativeTurn {
		t.Helper()
		got, err := peer.Prompt(ctx, acp.PromptRequest{SessionId: session.SessionId, Prompt: []acp.ContentBlock{acp.TextBlock(text)}})
		turn, ok := acpmeta.ReadNativeTurn(got.Meta)
		if err != nil || !ok || got.StopReason != acp.StopReasonEndTurn || turn.Status != "completed" || len(turn.ConsumedInputIDs) != 1 || got.UserMessageId != nil {
			t.Fatalf("public prompt %q: %+v %v", text, got, err)
		}
		return turn
	}
	handled := prompt("/handled")
	if handled.Usage != nil || handled.NativeTurnID != nil {
		t.Fatalf("handled extension command invented model evidence: %+v", handled)
	}
	answered := prompt("second")
	if answered.Usage == nil || answered.Usage.InputTokens == nil || *answered.Usage.InputTokens != 2 {
		t.Fatalf("public usage: %+v", answered.Usage)
	}
	raw, err := peer.CallExtension(ctx, acpmeta.NativeTurnReceiptsMethod, acpmeta.NativeTurnReceiptsRequest{SessionID: string(session.SessionId), AckInvocationIDs: []string{handled.InvocationID}})
	var page acpmeta.NativeTurnReceiptsResponse
	if err != nil || json.Unmarshal(raw, &page) != nil || len(page.Receipts) != 1 || page.Receipts[0].Turn.InvocationID != answered.InvocationID {
		t.Fatalf("public receipt: %s %v", raw, err)
	}
	if _, err := peer.UnstableDeleteSession(ctx, acp.UnstableDeleteSessionRequest{SessionId: session.SessionId}); err != nil {
		t.Fatalf("public Pi history deletion: %v", err)
	}
	if _, err := os.Stat(string(session.SessionId)); !os.IsNotExist(err) {
		t.Fatalf("native history still exists: %v", err)
	}
}

func TestResumeAndClonePreserveTheNativeFile(t *testing.T) {
	client, ctx, session := fixtureClient(t, agentclient.HostCallbacks{})
	id := session.SessionId
	if !filepath.IsAbs(string(id)) {
		t.Fatal("missing exact native path")
	}
	cwd := client.options.WorkingDirectory
	if _, err := client.CloseSession(ctx, acp.CloseSessionRequest{SessionId: id}); err != nil {
		t.Fatal(err)
	}
	// A previously stored UUID remains resumable when its exact file and cwd match.
	if _, err := client.ResumeSession(ctx, acp.ResumeSessionRequest{SessionId: "native", Cwd: cwd}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.CloseSession(ctx, acp.CloseSessionRequest{SessionId: "native"}); err != nil {
		t.Fatal(err)
	}
	receipt := t.TempDir()
	request := acp.UnstableForkSessionRequest{SessionId: id, Cwd: cwd, Meta: map[string]any{"forkReceiptDirectory": receipt}}
	fork, err := client.UnstableForkSession(ctx, request)
	if err != nil || fork.SessionId == id || !filepath.IsAbs(string(fork.SessionId)) {
		t.Fatalf("fork: %+v %v", fork, err)
	}
	request.Meta["reconcileOnly"] = true
	got, err := client.UnstableForkSession(ctx, request)
	if err != nil || got.SessionId != fork.SessionId {
		t.Fatalf("fork receipt: %+v %v", got, err)
	}
	if _, err := os.Stat(string(id)); err != nil {
		t.Fatalf("source did not survive: %v", err)
	}
}

func TestManagedBridgeKeepsMCPValuesOutOfSource(t *testing.T) {
	ctx := context.Background()
	options := fixtureOptions(t, t.TempDir())
	options.MCPModulePath = filepath.Join(options.WorkingDirectory, "bridge.js")
	client, err := NewClient(ctx, options, agentclient.HostCallbacks{})
	if err != nil {
		t.Fatal(err)
	}
	secret := "fixture-bearer-do-not-embed"
	instructions := "developer instructions"
	dir, args, env, err := client.prepareResources(resources{instructions: &instructions, servers: []acp.McpServer{{Http: &acp.McpServerHttpInline{
		Name: "fixture", Url: "https://example.test/mcp", Headers: []acp.HttpHeader{{Name: "Authorization", Value: secret}},
	}}}})
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	source, err := os.ReadFile(filepath.Join(dir, "extension.ts"))
	if err != nil || strings.Contains(string(source), secret) || len(env) != 1 || !strings.Contains(env[0], secret) {
		t.Fatalf("incorrect bridge value placement: %v", err)
	}
	if len(args) < 2 || args[0] != "--append-system-prompt" {
		t.Fatalf("instruction arguments: %v", args)
	}
	got, err := os.ReadFile(args[1])
	if err != nil || string(got) != instructions {
		t.Fatalf("instruction file %q, %v", got, err)
	}
}

func TestPromptWaitsForSettlementAndReportsOnlyInvocationUsage(t *testing.T) {
	client, ctx, session := fixtureClient(t, agentclient.HostCallbacks{})
	for _, tc := range []struct {
		// prompt selects the native fixture branch.
		prompt string
		// text is the native terminal answer.
		text string
		// input is the measured uncached input for this invocation.
		input int64
	}{{"first", "second attempt", 9}, {"second", "done", 2}} {
		run, err := client.ReservePrompt(ctx, acp.PromptRequest{SessionId: session.SessionId, Prompt: []acp.ContentBlock{acp.TextBlock(tc.prompt)}}, agentclient.PromptOptions{})
		if err != nil {
			t.Fatal(err)
		}
		got := run()
		if got.Receipt.Admission != agentclient.AdmissionConfirmed || got.Outcome == nil || got.Outcome.Turn.Status != "completed" || got.Outcome.NativeFailure != nil || got.Outcome.FinalMessage == nil || got.Outcome.FinalMessage.Text != tc.text {
			t.Fatalf("prompt %q settled incorrectly: %+v / %+v", tc.prompt, got, got.Outcome)
		}
		usage := got.Outcome.Turn.Usage
		if usage == nil || usage.InputTokens == nil || *usage.InputTokens != tc.input || usage.CacheReadTokens != nil || !usage.Complete {
			t.Fatalf("prompt %q usage: %+v", tc.prompt, usage)
		}
	}
}

func TestRejectedAndCancelledPromptsKeepTheirNativeEvidence(t *testing.T) {
	started := make(chan struct{}, 1)
	client, ctx, session := fixtureClient(t, agentclient.HostCallbacks{Observe: func(_ context.Context, _ agentclient.Scope, _ acp.SessionNotification) error {
		select {
		case started <- struct{}{}:
		default:
		}
		return nil
	}})
	reserve := func(prompt string) func() agentclient.PromptResult {
		run, err := client.ReservePrompt(ctx, acp.PromptRequest{SessionId: session.SessionId, Prompt: []acp.ContentBlock{acp.TextBlock(prompt)}}, agentclient.PromptOptions{})
		if err != nil {
			t.Fatal(err)
		}
		return run
	}
	rejected := reserve("reject")()
	if rejected.Receipt.Admission != agentclient.AdmissionNotAccepted || rejected.Receipt.Submission != agentclient.SubmissionSent || rejected.Receipt.OperationError == nil || rejected.Outcome != nil {
		t.Fatalf("rejected prompt: %+v", rejected)
	}
	done := make(chan agentclient.PromptResult, 1)
	run := reserve("cancel")
	go func() { done <- run() }()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if err := client.Cancel(acp.CancelNotification{SessionId: session.SessionId}); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-done:
		if got.Outcome == nil || got.Outcome.Turn.Status != "cancelled" || got.Outcome.Turn.Usage != nil {
			t.Fatalf("cancelled prompt: %+v / %+v", got, got.Outcome)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	got := reserve("second")()
	if got.Outcome == nil || got.Outcome.FinalMessage == nil || got.Outcome.FinalMessage.Text != "done" {
		t.Fatalf("late events contaminated next prompt: %+v", got.Outcome)
	}
}

func TestNativeModelAndThinkingUseObservedMenus(t *testing.T) {
	client, ctx, session := fixtureClient(t, agentclient.HostCallbacks{})
	if len(session.ConfigOptions) != 2 || session.ConfigOptions[0].Select.CurrentValue != "test/alpha" {
		t.Fatalf("initial configuration: %+v", session.ConfigOptions)
	}
	set := func(id, value string) (acp.SetSessionConfigOptionResponse, error) {
		return client.SetSessionConfigOption(ctx, acp.SetSessionConfigOptionRequest{ValueId: &acp.SetSessionConfigOptionValueId{
			SessionId: session.SessionId, ConfigId: acp.SessionConfigId(id), Value: acp.SessionConfigValueId(value),
		}})
	}
	if _, err := set("reasoning", "max"); err == nil {
		t.Fatal("accepted unsupported thinking level")
	}
	got, err := set("model", "other/lab/beta")
	if err != nil || len(got.ConfigOptions) != 2 || got.ConfigOptions[0].Select.CurrentValue != "other/lab/beta" {
		t.Fatalf("set model: %+v %v", got, err)
	}
	got, err = set("reasoning", "max")
	if err != nil || got.ConfigOptions[1].Select.CurrentValue != "max" {
		t.Fatalf("set thinking: %+v %v", got, err)
	}
}

func TestProcessExitKeepsUnknownTerminalUsage(t *testing.T) {
	client, ctx, session := fixtureClient(t, agentclient.HostCallbacks{})
	run, err := client.ReservePrompt(ctx, acp.PromptRequest{SessionId: session.SessionId, Prompt: []acp.ContentBlock{acp.TextBlock("exit")}}, agentclient.PromptOptions{})
	if err != nil {
		t.Fatal(err)
	}
	got := run()
	if got.Receipt.Admission != agentclient.AdmissionConfirmed || got.Outcome == nil || got.Outcome.Turn.Status != "" || got.Outcome.Turn.Usage != nil || got.Outcome.OperationError == nil {
		t.Fatalf("native process exit: %+v / %+v", got, got.Outcome)
	}
	if _, err := client.CloseSession(ctx, acp.CloseSessionRequest{SessionId: session.SessionId}); err != nil && !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestReceiptReaderSurvivesIdleAndDrainsAfterClose(t *testing.T) {
	client, ctx, session := fixtureClient(t, agentclient.HostCallbacks{})
	request := acpmeta.NativeTurnReceiptsRequest{SessionID: string(session.SessionId)}
	idle, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
	defer cancel()
	if page, err := client.ReadNativeOutcomes(idle, request, true); !errors.Is(err, context.DeadlineExceeded) || len(page.Outcomes) != 0 {
		t.Fatalf("idle receipt reader returned before native owner closed: %+v %v", page, err)
	}
	if _, err := client.CloseSession(ctx, acp.CloseSessionRequest{SessionId: session.SessionId}); err != nil {
		t.Fatal(err)
	}
	if page, err := client.ReadNativeOutcomes(ctx, request, false); err != nil || len(page.Outcomes) != 0 {
		t.Fatalf("closed session's empty receipt drain: %+v %v", page, err)
	}
}

func TestUnknownUsageComponentPreservesObservedSubtotal(t *testing.T) {
	client, ctx, session := fixtureClient(t, agentclient.HostCallbacks{})
	run, err := client.ReservePrompt(ctx, acp.PromptRequest{SessionId: session.SessionId, Prompt: []acp.ContentBlock{acp.TextBlock("partial")}}, agentclient.PromptOptions{})
	if err != nil {
		t.Fatal(err)
	}
	got := run()
	if got.Outcome == nil {
		t.Fatalf("partial prompt: %+v", got)
	}
	usage := got.Outcome.Turn.Usage
	if usage == nil || usage.Complete || usage.Coverage != "assistant_messages" || len(usage.Observations) != 0 ||
		usage.CacheReadTokens == nil || *usage.CacheReadTokens != 5 || usage.CacheWriteTokens == nil || *usage.CacheWriteTokens != 0 ||
		usage.InputTokens == nil || *usage.InputTokens != 7 || usage.OutputTokens == nil || *usage.OutputTokens != 3 {
		t.Fatalf("partial primary usage: %+v", usage)
	}
	model := usage.Models["test/alpha"]
	if model.CacheReadTokens == nil || *model.CacheReadTokens != 5 || model.CacheWriteTokens == nil || *model.CacheWriteTokens != 0 {
		t.Fatalf("partial model usage: %+v", usage.Models)
	}
}

func TestContinuationCanOnlySubmitOnce(t *testing.T) {
	client, ctx, session := fixtureClient(t, agentclient.HostCallbacks{})
	reserve := func() func() agentclient.PromptResult {
		t.Helper()
		run, err := client.ReservePrompt(ctx, acp.PromptRequest{SessionId: session.SessionId, Prompt: []acp.ContentBlock{acp.TextBlock("second")}}, agentclient.PromptOptions{})
		if err != nil {
			t.Fatal(err)
		}
		return run
	}
	first := reserve()
	if got := first(); got.Outcome == nil {
		t.Fatalf("first prompt: %+v", got)
	}
	second := reserve()
	if got := first(); got.Receipt.Submission != agentclient.SubmissionNotSent || got.Receipt.OperationError == nil || got.Outcome != nil {
		t.Fatalf("duplicate continuation attempted to submit: %+v", got)
	}
	if _, err := client.ReservePrompt(ctx, acp.PromptRequest{SessionId: session.SessionId, Prompt: []acp.ContentBlock{acp.TextBlock("third")}}, agentclient.PromptOptions{}); err == nil {
		t.Fatal("duplicate continuation released another reservation")
	}
	if got := second(); got.Outcome == nil || got.Outcome.Turn.Status != "completed" {
		t.Fatalf("duplicate continuation disturbed current reservation: %+v", got)
	}
}

func TestTerminalEventsRemainAuthoritativeWhenACKIsLost(t *testing.T) {
	client, ctx, session := fixtureClient(t, agentclient.HostCallbacks{})
	run, err := client.ReservePrompt(ctx, acp.PromptRequest{SessionId: session.SessionId, Prompt: []acp.ContentBlock{acp.TextBlock("lost-ack")}}, agentclient.PromptOptions{})
	if err != nil {
		t.Fatal(err)
	}
	got := run()
	if got.Receipt.Admission != agentclient.AdmissionConfirmed || got.Receipt.OperationError != nil || got.Outcome == nil ||
		got.Outcome.Turn.Status != "completed" || got.Outcome.OperationError != nil ||
		got.Outcome.Turn.Usage == nil || got.Outcome.Turn.Usage.InputTokens == nil || *got.Outcome.Turn.Usage.InputTokens != 7 {
		t.Fatalf("terminal preceding absent ACK: %+v / %+v", got, got.Outcome)
	}
}

func TestLegacyCancelRetiresProcessWithoutInventingSettlement(t *testing.T) {
	observed := make(chan struct{}, 1)
	client, ctx, session := fixtureClient(t, agentclient.HostCallbacks{Observe: func(context.Context, agentclient.Scope, acp.SessionNotification) error {
		select {
		case observed <- struct{}{}:
		default:
		}
		return nil
	}})
	run, err := client.ReservePrompt(ctx, acp.PromptRequest{SessionId: session.SessionId, Prompt: []acp.ContentBlock{acp.TextBlock("legacy-cancel")}}, agentclient.PromptOptions{})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan agentclient.PromptResult, 1)
	go func() { done <- run() }()
	select {
	case <-observed:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if err := client.Cancel(acp.CancelNotification{SessionId: session.SessionId}); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-done:
		if got.Outcome == nil || got.Outcome.Turn.Status != "" || got.Outcome.OperationError == nil || got.Outcome.Turn.Usage != nil {
			t.Fatalf("unproven legacy cancellation: %+v / %+v", got, got.Outcome)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if _, err := client.ReservePrompt(ctx, acp.PromptRequest{SessionId: session.SessionId, Prompt: []acp.ContentBlock{acp.TextBlock("second")}}, agentclient.PromptOptions{}); err == nil {
		t.Fatal("closed legacy owner accepted another prompt")
	}
	if _, err := client.CloseSession(ctx, acp.CloseSessionRequest{SessionId: session.SessionId}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.ResumeSession(ctx, acp.ResumeSessionRequest{SessionId: session.SessionId, Cwd: client.options.WorkingDirectory}); err != nil {
		t.Fatal(err)
	}
}

func TestPiRPCFixture(t *testing.T) {
	if os.Getenv("ACP_PI_NEXT_FIXTURE") != "1" {
		return
	}
	if containsArg("--version") {
		fmt.Fprintln(os.Stdout, "0.85.1")
		os.Exit(0)
	}
	root := os.Getenv("PI_CODING_AGENT_DIR")
	sessionDir := filepath.Join(root, "sessions")
	_ = os.MkdirAll(sessionDir, 0700)
	sessionFile := filepath.Join(sessionDir, "native.jsonl")
	sessionID := "native"
	if existing := argValue("--session"); existing != "" {
		item, err := sessionHeader(existing)
		if err != nil {
			os.Exit(10)
		}
		sessionFile, sessionID = existing, item.ID
	}
	model := map[string]any{"provider": "test", "id": "alpha", "name": "Alpha"}
	beta := map[string]any{"provider": "other", "id": "lab/beta", "name": "Beta"}
	level := "off"
	if !containsArg("--no-session") && argValue("--session") == "" {
		cwd, _ := os.Getwd()
		header, _ := json.Marshal(map[string]any{"type": "session", "id": "native", "cwd": cwd})
		_ = os.WriteFile(sessionFile, append(header, '\n'), 0600)
	}
	out := json.NewEncoder(os.Stdout)
	event := func(typ string, fields map[string]any) {
		if fields == nil {
			fields = map[string]any{}
		}
		fields["type"] = typ
		if err := out.Encode(fields); err != nil {
			os.Exit(2)
		}
	}
	message := func(text, reason string, input int64) {
		event("message_end", map[string]any{"message": map[string]any{"role": "assistant", "provider": model["provider"], "model": model["id"], "stopReason": reason, "errorMessage": "temporary retry", "content": []any{map[string]any{"type": "text", "text": text}}, "usage": map[string]any{"input": input, "output": 1}}})
	}
	if containsArg("--extension") {
		event("extension_ui_request", map[string]any{"method": "setStatus", "statusKey": "acp-go.extension-ready", "statusText": "ready"})
	}
	cleared, legacy := false, false
	scan := bufio.NewScanner(os.Stdin)
	for scan.Scan() {
		var request map[string]any
		if json.Unmarshal(scan.Bytes(), &request) != nil {
			os.Exit(3)
		}
		typ := request["type"].(string)
		respond := func(success bool, data any) {
			event("response", map[string]any{"id": request["id"], "command": typ, "success": success, "data": data, "error": "fixture rejection"})
		}
		switch typ {
		case "get_state":
			respond(true, map[string]any{"sessionFile": sessionFile, "sessionId": sessionID, "model": model, "thinkingLevel": level})
		case "get_available_models":
			respond(true, map[string]any{"models": []any{map[string]any{"provider": "test", "id": "alpha", "name": "Alpha"}, beta}})
		case "get_available_thinking_levels":
			levels := []string{"off", "high"}
			if model["provider"] == "other" {
				levels = append(levels, "max")
			}
			respond(true, map[string]any{"levels": levels})
		case "set_model":
			if request["provider"] != "other" || request["modelId"] != "lab/beta" {
				respond(false, nil)
				continue
			}
			model, level = beta, "off"
			respond(true, model)
		case "set_thinking_level":
			level = request["level"].(string)
			respond(true, nil)
		case "get_session_stats":
			respond(true, map[string]any{"tokens": map[string]any{"input": 9999}, "contextUsage": map[string]any{"tokens": 90, "contextWindow": 200}})
		case "get_commands":
			respond(true, map[string]any{"commands": []any{}})
		case "clone":
			sessionFile, sessionID = filepath.Join(sessionDir, "clone.jsonl"), "clone"
			cwd, _ := os.Getwd()
			data, _ := json.Marshal(map[string]any{"type": "session", "id": sessionID, "cwd": cwd})
			if os.WriteFile(sessionFile, append(data, '\n'), 0600) != nil {
				os.Exit(11)
			}
			respond(true, map[string]any{"cancelled": false})
		case "prompt":
			prompt := request["message"].(string)
			if prompt == "/handled" {
				respond(true, map[string]any{"disposition": "handled"})
				continue
			}
			if prompt == "reject" {
				respond(false, nil)
				continue
			}
			if prompt == "lost-ack" {
				event("agent_start", nil)
				message("native terminal before ACK", "stop", 7)
				event("agent_settled", map[string]any{"aborted": false})
				os.Exit(18)
			}
			respond(true, map[string]any{"disposition": "started"})
			event("agent_start", nil)
			if prompt == "exit" {
				os.Exit(17)
			}
			if prompt == "cancel" || prompt == "legacy-cancel" {
				legacy = prompt == "legacy-cancel"
				event("message_update", map[string]any{"assistantMessageEvent": map[string]any{"type": "text_delta", "delta": "working"}})
				continue
			}
			if prompt == "partial" {
				event("message_end", map[string]any{"message": map[string]any{
					"role": "assistant", "provider": "test", "model": "alpha", "stopReason": "toolUse",
					"usage": map[string]any{"input": 1, "output": 1, "cacheRead": 3, "cacheWrite": 0},
				}})
				message("next tool", "toolUse", 2)
				event("message_end", map[string]any{"message": map[string]any{
					"role": "assistant", "provider": "test", "model": "alpha", "stopReason": "stop",
					"usage": map[string]any{"input": 4, "output": 1, "cacheRead": 2},
				}})
			} else if prompt == "first" {
				message("retry output", "error", 4)
				event("agent_end", map[string]any{"willRetry": true})
				event("agent_start", nil)
				message("second attempt", "stop", 5)
			} else {
				message("done", "stop", 2)
			}
			event("agent_end", map[string]any{"willRetry": false})
			event("agent_settled", map[string]any{"aborted": false})
		case "clear_queue":
			if legacy {
				respond(false, nil)
				continue
			}
			cleared = true
			respond(true, map[string]any{"steering": []string{}, "followUp": []string{}})
		case "abort":
			if !cleared {
				os.Exit(4)
			}
			event("agent_settled", map[string]any{"aborted": true})
			message("late", "stop", 500)
			respond(true, nil)
		default:
			fmt.Fprintln(os.Stderr, "unexpected Pi RPC fixture request", strings.TrimSpace(typ))
			respond(false, nil)
		}
	}
	os.Exit(0)
}

func containsArg(value string) bool {
	for _, arg := range os.Args {
		if arg == value {
			return true
		}
	}
	return false
}

func argValue(flag string) string {
	for i, arg := range os.Args {
		if arg == flag && i+1 < len(os.Args) {
			return os.Args[i+1]
		}
	}
	return ""
}

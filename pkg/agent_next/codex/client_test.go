package codex_test

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
	"github.com/JieWaZi/acp-go/pkg/agent_next/codex"
	acp "github.com/JieWaZi/acp-go/sdk"
)

func TestAppServerFixture(t *testing.T) {
	if os.Getenv("CODEX_NEXT_FIXTURE") == "" {
		return
	}
	mode := os.Getenv("CODEX_NEXT_FIXTURE")
	trace, err := os.OpenFile(os.Getenv("CODEX_NEXT_TRACE"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		os.Exit(2)
	}
	input := bufio.NewScanner(os.Stdin)
	out := json.NewEncoder(os.Stdout)
	turnNumber := 0
	nativeTurn := "turn-0"
	for input.Scan() {
		var call struct {
			ID     json.RawMessage
			Method string
			Params map[string]any
		}
		if json.Unmarshal(input.Bytes(), &call) != nil {
			os.Exit(2)
		}
		fmt.Fprintln(trace, string(input.Bytes()))
		emit := func(method string, params map[string]any) {
			_ = out.Encode(map[string]any{"method": method, "params": params})
		}
		ack := func(body any) {
			_ = out.Encode(map[string]any{"id": call.ID, "result": body})
		}
		turn := func(status string) map[string]any {
			return map[string]any{"threadId": "thread-1", "turn": map[string]any{"id": nativeTurn, "status": status}}
		}
		switch call.Method {
		case "initialize":
			ack(map[string]any{"userAgent": "codex/0.163.0"})
		case "initialized":
		case "account/read":
			if mode == "management" {
				ack(map[string]any{"account": map[string]any{"type": "chatgpt", "email": "fake@example.test", "planType": "plus"}, "requiresOpenaiAuth": true})
			} else {
				ack(map[string]any{"account": nil, "requiresOpenaiAuth": false})
			}
		case "account/rateLimits/read":
			ack(map[string]any{"rateLimitsByLimitId": map[string]any{"codex": map[string]any{"primary": map[string]any{"usedPercent": 5, "windowDurationMins": 300}}}})
		case "account/logout":
			emit("account/updated", map[string]any{})
			ack(map[string]any{})
		case "account/login/start":
			emit("account/login/completed", map[string]any{"success": true})
			ack(map[string]any{"type": "apiKey"})
		case "config/read":
			ack(map[string]any{"config": map[string]any{}})
		case "model/list":
			if mode == "management" {
				if call.Params["cursor"] == nil {
					ack(map[string]any{"data": []any{map[string]any{"id": "internal-a", "model": "actual-model", "displayName": "Model A", "isDefault": true, "defaultReasoningEffort": "medium", "supportedReasoningEfforts": []any{map[string]any{"reasoningEffort": "medium"}, map[string]any{"reasoningEffort": "high"}}, "serviceTiers": []any{map[string]any{"id": "priority-a", "name": "Fast"}}}}, "nextCursor": "second"})
				} else {
					ack(map[string]any{"data": []any{map[string]any{"id": "internal-b", "model": "legacy-model", "displayName": "Legacy Model", "additionalSpeedTiers": []string{"fast"}}}})
				}
			} else {
				ack(map[string]any{"data": []any{}, "nextCursor": nil})
			}
		case "thread/fork":
			ack(map[string]any{"thread": map[string]any{"id": "child-1"}, "model": "actual-model"})
		case "thread/turns/list":
			ack(map[string]any{"data": []any{map[string]any{"id": "history-turn"}}})
		case "thread/items/list":
			ack(map[string]any{"data": []any{map[string]any{"turnId": "history-turn", "item": map[string]any{"id": "history-user", "type": "userMessage", "content": []any{map[string]any{"type": "text", "text": "past input"}}}}}})
		case "thread/archive", "thread/unarchive":
			ack(map[string]any{})
		case "thread/start", "thread/resume":
			ack(map[string]any{"thread": map[string]any{"id": "thread-1"}, "model": "actual-model"})
		case "thread/unsubscribe":
			if mode == "retry_close" && turnNumber == 0 {
				turnNumber++
				_ = out.Encode(map[string]any{"id": call.ID, "error": map[string]any{"code": -32000, "message": "release failed"}})
				continue
			}
			ack(map[string]any{"status": "unsubscribed"})
		case "turn/start":
			turnNumber++
			nativeTurn = fmt.Sprintf("turn-%d", turnNumber)
			if mode == "approval" {
				ack(turn("inProgress"))
				_ = out.Encode(map[string]any{"id": "foreign-approval", "method": "item/commandExecution/requestApproval", "params": map[string]any{"threadId": "thread-1", "turnId": "foreign-turn", "itemId": "foreign", "command": "foreign command"}})
				_ = out.Encode(map[string]any{"id": "owned-approval", "method": "item/commandExecution/requestApproval", "params": map[string]any{"threadId": "thread-1", "turnId": nativeTurn, "itemId": "owned", "command": "owned command"}})
				continue
			}
			if mode == "unknown_exit" {
				emit("turn/completed", turn("completed"))
				os.Exit(0)
			}
			if mode == "known_usage_exit" {
				emit("item/started", map[string]any{"threadId": "thread-1", "turnId": nativeTurn, "item": map[string]any{"id": "user-1", "type": "userMessage", "clientId": call.Params["clientUserMessageId"]}})
				emit("thread/tokenUsage/updated", fixtureUsage("thread-1", nativeTurn, 19))
				os.Exit(0)
			}
			if mode == "no_ack" {
				emit("item/started", map[string]any{"threadId": "thread-1", "turnId": nativeTurn, "item": map[string]any{"id": "user-1", "type": "userMessage", "clientId": call.Params["clientUserMessageId"]}})
				emit("item/agentMessage/delta", map[string]any{"threadId": "thread-1", "turnId": nativeTurn, "itemId": "message-1", "delta": "waiting for interruption"})
				continue
			}
			emit("turn/completed", map[string]any{"threadId": "thread-1", "turn": map[string]any{"id": "foreign-turn", "status": "completed"}})
			emit("thread/tokenUsage/updated", fixtureUsage("foreign-thread", nativeTurn, 999))
			emit("thread/tokenUsage/updated", fixtureUsage("thread-1", nativeTurn, 19))
			if mode == "usage_gap" {
				emit("thread/tokenUsage/updated", fixtureUsage("thread-1", "foreign-turn", 999))
				last := map[string]any{"inputTokens": 9, "outputTokens": 7, "cachedInputTokens": 4, "reasoningOutputTokens": 2, "totalTokens": 16}
				total := map[string]any{"inputTokens": 1008, "outputTokens": 14, "cachedInputTokens": 8, "reasoningOutputTokens": 4, "totalTokens": 1022}
				update := map[string]any{"threadId": "thread-1", "turnId": nativeTurn, "tokenUsage": map[string]any{"last": last, "total": total}}
				emit("thread/tokenUsage/updated", update)
				emit("thread/tokenUsage/updated", update)
			}
			emit("item/completed", map[string]any{"threadId": "thread-1", "turnId": nativeTurn, "item": map[string]any{"id": "message-1", "type": "agentMessage", "text": "verified answer"}})
			if mode == "native_failed" {
				failed := turn("failed")
				failed["turn"].(map[string]any)["error"] = map[string]any{"message": "fixture native failure"}
				emit("turn/completed", failed)
			} else {
				emit("turn/completed", turn("completed"))
			}
			ack(turn("inProgress"))
		case "thread/compact/start":
			turnNumber++
			nativeTurn = fmt.Sprintf("turn-%d", turnNumber)
			emit("turn/started", turn("inProgress"))
			ack(map[string]any{})
			emit("turn/completed", turn("completed"))
		case "":
			if string(call.ID) == `"owned-approval"` {
				emit("turn/completed", turn("completed"))
			}
		case "turn/interrupt":
			ack(map[string]any{})
			emit("turn/completed", turn("interrupted"))
		default:
			_ = out.Encode(map[string]any{"id": call.ID, "error": map[string]any{"code": -32601, "message": "unsupported fixture method"}})
		}
	}
	_ = trace.Close()
	os.Exit(0)
}

func fixtureUsage(thread, turn string, count int) map[string]any {
	tokens := map[string]any{"inputTokens": count, "outputTokens": 7, "cachedInputTokens": 4, "reasoningOutputTokens": 2, "totalTokens": count + 7}
	return map[string]any{"threadId": thread, "turnId": turn, "tokenUsage": map[string]any{"last": tokens, "total": tokens, "modelContextWindow": 100}}
}

func newFixture(t *testing.T, mode string, host agentclient.HostCallbacks) (*codex.Client, context.Context, acp.SessionId, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	dir := t.TempDir()
	trace := filepath.Join(dir, "trace.ndjson")
	client, err := codex.NewClient(ctx, codex.Options{
		Executable:       os.Args[0],
		PrefixArgs:       []string{"-test.run=^TestAppServerFixture$", "--"},
		Environment:      append(os.Environ(), "CODEX_NEXT_FIXTURE="+mode, "CODEX_NEXT_TRACE="+trace),
		WorkingDirectory: dir,
	}, host)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(trace); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("constructor launched fixture: %v", err)
	}
	t.Cleanup(func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := client.Close(closeCtx); err != nil {
			t.Error(err)
		}
	})
	if _, err := client.Initialize(ctx, acp.InitializeRequest{ProtocolVersion: 1}); err != nil {
		t.Fatal(err)
	}
	session, err := client.NewSession(ctx, acp.NewSessionRequest{Cwd: dir, McpServers: []acp.McpServer{}})
	if err != nil {
		t.Fatal(err)
	}
	return client, ctx, session.SessionId, trace
}

func reserve(t *testing.T, c *codex.Client, ctx context.Context, session acp.SessionId, options agentclient.PromptOptions) func() agentclient.PromptResult {
	t.Helper()
	run, err := c.ReservePrompt(ctx, acp.PromptRequest{SessionId: session, Prompt: []acp.ContentBlock{acp.TextBlock("hello")}}, options)
	if err != nil {
		t.Fatal(err)
	}
	return run
}

func TestGateRejectionWritesNoPrompt(t *testing.T) {
	c, ctx, session, trace := newFixture(t, "normal", agentclient.HostCallbacks{})
	refused := errors.New("commit rejected")
	var admittedID string
	result := reserve(t, c, ctx, session, agentclient.PromptOptions{BeforeSubmit: func(_ context.Context, s agentclient.PromptSubmission) error {
		if s.NativeInputID == nil || *s.NativeInputID == "" {
			t.Fatal("native input identity was not frozen")
		}
		admittedID = *s.NativeInputID
		*s.NativeInputID = "mutated by gate"
		return refused
	}})()
	if result.Receipt.Submission != agentclient.SubmissionNotSent || result.Receipt.Admission != agentclient.AdmissionNotAccepted || !errors.Is(result.Receipt.OperationError, refused) || result.Outcome != nil {
		t.Fatalf("unexpected result %+v", result)
	}
	if result.Receipt.Input.NativeInputID == nil || *result.Receipt.Input.NativeInputID != admittedID {
		t.Fatal("gate changed the owner's input identity")
	}
	data, err := os.ReadFile(trace)
	if err != nil || strings.Contains(string(data), "turn/start") {
		t.Fatalf("prompt escaped rejection: %v %s", err, data)
	}
}

func TestNativeTerminalSurvivesObserveFailure(t *testing.T) {
	refused := errors.New("observer refused")
	c, ctx, session, _ := newFixture(t, "normal", agentclient.HostCallbacks{Observe: func(context.Context, agentclient.Scope, acp.SessionNotification) error { return refused }})
	result := reserve(t, c, ctx, session, agentclient.PromptOptions{})()
	o := result.Outcome
	if result.Receipt.Submission != agentclient.SubmissionSent || result.Receipt.Admission != agentclient.AdmissionConfirmed || o == nil || o.Turn.Status != acpmeta.NativeTurnCompleted || !errors.Is(o.DeliveryError, refused) {
		t.Fatalf("lost evidence: %+v outcome=%+v", result, o)
	}
	if o.Turn.Usage == nil || o.Turn.Usage.Model != "actual-model" || o.Turn.Usage.InputTokens == nil || *o.Turn.Usage.InputTokens != 15 {
		t.Fatalf("wrong usage: %+v", o.Turn.Usage)
	}
	if o.FinalMessage == nil || o.FinalMessage.Text != "verified answer" {
		t.Fatalf("wrong final message: %+v", o.FinalMessage)
	}
}

func TestCancelReservedOwnerWritesNoPrompt(t *testing.T) {
	c, ctx, session, trace := newFixture(t, "normal", agentclient.HostCallbacks{})
	run := reserve(t, c, ctx, session, agentclient.PromptOptions{})
	if err := c.Cancel(acp.CancelNotification{SessionId: session}); err != nil {
		t.Fatal(err)
	}
	result := run()
	if result.Receipt.Submission != agentclient.SubmissionNotSent || result.Outcome != nil {
		t.Fatalf("unexpected result %+v", result)
	}
	data, err := os.ReadFile(trace)
	if err != nil || strings.Contains(string(data), "turn/start") {
		t.Fatalf("prompt escaped cancellation: %v %s", err, data)
	}
}

func TestCancelAfterWriteBypassesBlockedObserveWithoutACK(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	c, ctx, session, trace := newFixture(t, "no_ack", agentclient.HostCallbacks{Observe: func(context.Context, agentclient.Scope, acp.SessionNotification) error {
		close(entered)
		<-release
		return nil
	}})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	run := reserve(t, c, ctx, session, agentclient.PromptOptions{})
	out := make(chan agentclient.PromptResult, 1)
	go func() { out <- run() }()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if err := c.Cancel(acp.CancelNotification{SessionId: session}); err != nil {
		t.Fatal(err)
	}
	waitTrace(t, ctx, trace, `"method":"turn/interrupt"`)
	if _, err := c.ReservePrompt(ctx, acp.PromptRequest{SessionId: session, Prompt: []acp.ContentBlock{acp.TextBlock("too early")}}, agentclient.PromptOptions{}); err == nil {
		t.Fatal("observation still owns the turn")
	}
	close(release)
	select {
	case result := <-out:
		if result.Receipt.Admission != agentclient.AdmissionConfirmed || result.Outcome == nil || result.Outcome.Turn.Status != acpmeta.NativeTurnCancelled {
			t.Fatalf("native cancellation was not retained: %+v %+v", result, result.Outcome)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}

func waitTrace(t *testing.T, ctx context.Context, path, needle string) {
	t.Helper()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), needle) {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("missing %s in %s", needle, data)
		case <-tick.C:
		}
	}
}

func TestCancelDuringPreparationKeepsOneOwner(t *testing.T) {
	entered := make(chan struct{})
	c, ctx, session, trace := newFixture(t, "normal", agentclient.HostCallbacks{})
	run := reserve(t, c, ctx, session, agentclient.PromptOptions{
		Prepare: func(ctx context.Context) (func(), error) {
			close(entered)
			<-ctx.Done()
			return nil, ctx.Err()
		},
		BeforeSubmit: func(context.Context, agentclient.PromptSubmission) error {
			t.Error("cancelled preparation entered business gate")
			return nil
		},
	})
	out := make(chan agentclient.PromptResult, 1)
	go func() { out <- run() }()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if _, err := c.Compact(ctx, acpmeta.CompactRequest{SessionID: string(session)}); err == nil {
		t.Fatal("compaction overtook preparing prompt")
	}
	if _, err := c.ResumeSession(ctx, acp.ResumeSessionRequest{SessionId: session, Cwd: filepath.Dir(trace)}); err == nil {
		t.Fatal("resume replaced preparing generation")
	}
	if err := c.Cancel(acp.CancelNotification{SessionId: session}); err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-out:
		if result.Receipt.Submission != agentclient.SubmissionNotSent || result.Outcome != nil {
			t.Fatalf("cancel submitted: %+v", result)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	data, err := os.ReadFile(trace)
	if err != nil || strings.Contains(string(data), "turn/start") || strings.Contains(string(data), "thread/compact/start") {
		t.Fatalf("busy operations reached native wire: %v %s", err, data)
	}
}

func TestCloseSessionRetainsExactGenerationForRetry(t *testing.T) {
	c, ctx, session, trace := newFixture(t, "retry_close", agentclient.HostCallbacks{})
	request := acp.CloseSessionRequest{SessionId: session}
	if _, err := c.CloseSession(ctx, request); err == nil {
		t.Fatal("failed unsubscribe was reported as released")
	}
	if _, err := c.ResumeSession(ctx, acp.ResumeSessionRequest{SessionId: session, Cwd: filepath.Dir(trace)}); err == nil {
		t.Fatal("resume replaced a closing generation")
	}
	if _, err := c.CloseSession(ctx, request); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ResumeSession(ctx, acp.ResumeSessionRequest{SessionId: session, Cwd: filepath.Dir(trace)}); err != nil {
		t.Fatal(err)
	}
	result := reserve(t, c, ctx, session, agentclient.PromptOptions{})()
	if result.Outcome == nil || result.Outcome.Turn.Status != acpmeta.NativeTurnCompleted {
		t.Fatalf("reopened generation failed: %+v", result)
	}
}

type fixtureWireClient struct{ acp.Client }

func (fixtureWireClient) SessionUpdate(context.Context, acp.SessionNotification) error { return nil }

func TestPublicACPUsesNativeOwner(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	dir := t.TempDir()
	trace := filepath.Join(dir, "trace.ndjson")
	agent, err := agentclient.NewACPAgent(func(host agentclient.HostCallbacks) (agentclient.Client, error) {
		return codex.NewClient(ctx, codex.Options{
			Executable: os.Args[0], PrefixArgs: []string{"-test.run=^TestAppServerFixture$", "--"},
			Environment: append(os.Environ(), "CODEX_NEXT_FIXTURE=normal", "CODEX_NEXT_TRACE="+trace), WorkingDirectory: dir,
		}, host)
	})
	if err != nil {
		t.Fatal(err)
	}
	toAgent, fromClient := io.Pipe()
	toClient, fromAgent := io.Pipe()
	t.Cleanup(func() {
		_ = toAgent.Close()
		_ = fromClient.Close()
		_ = toClient.Close()
		_ = fromAgent.Close()
		closeCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := agent.Close(closeCtx); err != nil {
			t.Error(err)
		}
	})
	acp.NewAgentSideConnectionWithBinder(agent, fromAgent, toAgent, agent.SetAgentConnection)
	peer := acp.NewClientSideConnection(fixtureWireClient{}, fromClient, toClient)
	if _, err := peer.Initialize(ctx, acp.InitializeRequest{ProtocolVersion: 1}); err != nil {
		t.Fatal(err)
	}
	s, err := peer.NewSession(ctx, acp.NewSessionRequest{Cwd: dir, McpServers: []acp.McpServer{}})
	if err != nil {
		t.Fatal(err)
	}
	reply, err := peer.Prompt(ctx, acp.PromptRequest{SessionId: s.SessionId, Prompt: []acp.ContentBlock{acp.TextBlock("hello")}})
	if err != nil {
		t.Fatal(err)
	}
	native, ok := acpmeta.ReadNativeTurn(reply.Meta)
	if !ok || native.Status != acpmeta.NativeTurnCompleted || native.Usage == nil || native.Usage.Model != "actual-model" {
		t.Fatalf("native result did not survive public ACP: %+v", reply)
	}
}

func TestForeignUsageBreaksCumulativeContinuity(t *testing.T) {
	c, ctx, session, _ := newFixture(t, "usage_gap", agentclient.HostCallbacks{})
	result := reserve(t, c, ctx, session, agentclient.PromptOptions{})()
	if result.Outcome == nil || result.Outcome.Turn.Usage == nil {
		t.Fatalf("missing native usage: %+v", result)
	}
	usage := result.Outcome.Turn.Usage
	if usage.InputTokens == nil || *usage.InputTokens != 20 || usage.OutputTokens == nil || *usage.OutputTokens != 14 || usage.Complete {
		t.Fatalf("foreign or repeated usage was charged: %+v", usage)
	}
}

func TestCompactClaimsNativeLifecycleWithoutPrompt(t *testing.T) {
	c, ctx, session, trace := newFixture(t, "normal", agentclient.HostCallbacks{})
	reply, err := c.Compact(ctx, acpmeta.CompactRequest{SessionID: string(session)})
	if err != nil || reply.TurnID != "turn-1" || reply.Status != "completed" {
		t.Fatalf("compaction did not capture its lifecycle: %+v %v", reply, err)
	}
	data, err := os.ReadFile(trace)
	if err != nil || strings.Contains(string(data), "turn/start") {
		t.Fatalf("compaction sent an ordinary prompt: %v %s", err, data)
	}
}

func TestCloseWaitsForReservedPreparationCleanup(t *testing.T) {
	c, ctx, session, _ := newFixture(t, "normal", agentclient.HostCallbacks{})
	preparing, cleaning, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	run := reserve(t, c, ctx, session, agentclient.PromptOptions{Prepare: func(ctx context.Context) (func(), error) {
		close(preparing)
		<-ctx.Done()
		return func() { close(cleaning); <-release }, ctx.Err()
	}})
	promptDone := make(chan agentclient.PromptResult, 1)
	go func() { promptDone <- run() }()
	select {
	case <-preparing:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	closeCtx, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
	defer cancel()
	closeDone := make(chan error, 1)
	go func() { closeDone <- c.Close(closeCtx) }()
	select {
	case <-cleaning:
	case err := <-closeDone:
		t.Fatalf("close returned before releasing preparation: %v", err)
	case <-closeCtx.Done():
		t.Fatal("close did not cancel preparation")
	}
	if err := <-closeDone; !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cleanup was still held: %v", err)
	}
	close(release)
	select {
	case result := <-promptDone:
		if result.Receipt.Submission != agentclient.SubmissionNotSent {
			t.Fatalf("close submitted a prompt: %+v", result)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if err := c.Close(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestFullWriteWithoutAcceptanceRetainsOnlyInputReceipt(t *testing.T) {
	c, ctx, session, _ := newFixture(t, "unknown_exit", agentclient.HostCallbacks{})
	result := reserve(t, c, ctx, session, agentclient.PromptOptions{})()
	if result.Receipt.Submission != agentclient.SubmissionSent || result.Receipt.Admission != agentclient.AdmissionUnknown || result.Receipt.OperationError == nil || result.Outcome != nil {
		t.Fatalf("unrelated terminal confirmed input: %+v %+v", result, result.Outcome)
	}
}

func TestConnectionLossPreservesBoundUsageWithoutNativeTerminal(t *testing.T) {
	c, ctx, session, _ := newFixture(t, "known_usage_exit", agentclient.HostCallbacks{})
	result := reserve(t, c, ctx, session, agentclient.PromptOptions{})()
	o := result.Outcome
	if o == nil || result.Receipt.Admission != agentclient.AdmissionConfirmed || o.Turn.Status != "" || o.Turn.Usage == nil || o.OperationError == nil || o.Turn.TerminalEventID != nil || o.StopReason != nil {
		t.Fatalf("lost usage or invented terminal: %+v %+v", result, o)
	}
}

func TestNativeFailureSurvivesObservationFailure(t *testing.T) {
	refused := errors.New("host observation failed")
	c, ctx, session, _ := newFixture(t, "native_failed", agentclient.HostCallbacks{Observe: func(context.Context, agentclient.Scope, acp.SessionNotification) error { return refused }})
	o := reserve(t, c, ctx, session, agentclient.PromptOptions{})().Outcome
	if o == nil || o.Turn.Status != acpmeta.NativeTurnFailed || o.NativeFailure == nil || !errors.Is(o.DeliveryError, refused) || o.Turn.Usage == nil {
		t.Fatalf("separate failure evidence was lost: %+v", o)
	}
}

func TestPartialNativeFrameDoesNotBecomeSentOrAccepted(t *testing.T) {
	c, ctx, session, _ := newFixture(t, "normal", agentclient.HostCallbacks{})
	run, err := c.ReservePrompt(ctx, acp.PromptRequest{SessionId: session, Prompt: []acp.ContentBlock{acp.TextBlock(strings.Repeat("z", 2<<20))}}, agentclient.PromptOptions{})
	if err != nil {
		t.Fatal(err)
	}
	result := run()
	if result.Receipt.Submission != agentclient.SubmissionUnknown || result.Receipt.Admission != agentclient.AdmissionUnknown || result.Outcome != nil || result.Receipt.OperationError == nil {
		t.Fatalf("partial native write was misclassified: %+v %+v", result, result.Outcome)
	}
}

func TestNativeManagementAndRecoveryUseTypedReceipts(t *testing.T) {
	var replayed string
	var replayScope agentclient.Scope
	c, ctx, session, trace := newFixture(t, "management", agentclient.HostCallbacks{Observe: func(_ context.Context, s agentclient.Scope, n acp.SessionNotification) error {
		if n.Update.UserMessageChunk != nil {
			replayed = n.Update.UserMessageChunk.Content.Text.Text
			replayScope = s
		}
		return nil
	}})
	models, err := c.ReadModels(ctx)
	if err != nil || models.DefaultID == nil || *models.DefaultID != "actual-model" || models.Options.Ungrouped == nil || len(*models.Options.Ungrouped) != 2 {
		t.Fatalf("model pagination lost identity: %+v %v", models, err)
	}
	for index, expected := range []string{"priority-a", "fast"} {
		model := (*models.Options.Ungrouped)[index]
		if tier, ok := codex.FastServiceTier(model.Meta); !ok || tier != expected {
			t.Fatalf("wrong native Fast ID for %s: %s", model.Value, tier)
		}
	}
	account, err := c.ReadAccount(ctx)
	if err != nil || account.Account == nil || account.Account.Kind != "account" {
		t.Fatalf("account read: %+v %v", account, err)
	}
	quota, err := c.ReadAccountUsage(ctx)
	if err != nil || len(quota.Buckets) != 1 || len(quota.Buckets[0].Windows) != 1 || quota.Buckets[0].Windows[0].DurationSeconds == nil || *quota.Buckets[0].Windows[0].DurationSeconds != 18000 {
		t.Fatalf("quota read: %+v %v", quota, err)
	}
	if _, err := c.Authenticate(ctx, acp.AuthenticateRequest{MethodId: "api-key", Meta: map[string]any{"api-key": map[string]any{"apiKey": "fixture-key"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Logout(ctx, acp.LogoutRequest{}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.SetSessionConfigOption(ctx, acp.SetSessionConfigOptionRequest{ValueId: &acp.SetSessionConfigOptionValueId{SessionId: session, ConfigId: "service_tier", Value: "priority-a"}}); err != nil {
		t.Fatal(err)
	}
	settings, err := c.ReadSessionSettings(ctx, session)
	if err != nil {
		t.Fatal(err)
	}
	tier, present := settings.Requested.ServiceTier.Value()
	if !present || tier != "priority-a" || settings.Observed.ServiceTier.IsPresent() {
		t.Fatalf("requested tier reported as effective: %+v", settings)
	}
	if _, err := c.CloseSession(ctx, acp.CloseSessionRequest{SessionId: session}); err != nil {
		t.Fatal(err)
	}
	cwd := filepath.Dir(trace)
	if _, err := c.LoadSession(ctx, acp.LoadSessionRequest{SessionId: session, Cwd: cwd}); err != nil {
		t.Fatal(err)
	}
	if replayed != "past input" || replayScope.SessionID != session || replayScope.NativeInvocationID != "" {
		t.Fatalf("history attributed as current: %q %+v", replayed, replayScope)
	}
	if _, err := c.CloseSession(ctx, acp.CloseSessionRequest{SessionId: session}); err != nil {
		t.Fatal(err)
	}
	fork := acp.UnstableForkSessionRequest{SessionId: session, Cwd: cwd, Meta: map[string]any{"forkReceiptDirectory": t.TempDir(), "forkPosition": "turn-4", "sourceCwd": cwd}}
	created, err := c.UnstableForkSession(ctx, fork)
	if err != nil || created.SessionId != "child-1" {
		t.Fatalf("fork: %+v %v", created, err)
	}
	fork.Meta["reconcileOnly"] = true
	recovered, err := c.UnstableForkSession(ctx, fork)
	if err != nil || recovered.SessionId != created.SessionId {
		t.Fatalf("reconcile: %+v %v", recovered, err)
	}
	data, err := os.ReadFile(trace)
	if err != nil || strings.Count(string(data), `"method":"thread/fork"`) != 1 || !strings.Contains(string(data), `"lastTurnId":"turn-4"`) {
		t.Fatalf("fork wrote incorrect request: %v %s", err, data)
	}
}

func TestApprovalRoutesOnlyToExactNativeTurn(t *testing.T) {
	requests := make(chan acp.RequestPermissionRequest, 2)
	c, ctx, session, trace := newFixture(t, "approval", agentclient.HostCallbacks{
		RequestPermission: func(_ context.Context, s agentclient.Scope, r acp.RequestPermissionRequest) (acp.RequestPermissionResponse, error) {
			if s.NativeInvocationID == "" || s.HostInvocationID == "" {
				t.Error("missing exact invocation scope")
			}
			requests <- r
			return acp.RequestPermissionResponse{Outcome: acp.RequestPermissionOutcome{Selected: &acp.RequestPermissionOutcomeSelected{OptionId: "allow_once", Outcome: "selected"}}}, nil
		},
	})
	result := reserve(t, c, ctx, session, agentclient.PromptOptions{})()
	if result.Outcome == nil || result.Outcome.Turn.Status != acpmeta.NativeTurnCompleted {
		t.Fatalf("approval turn did not finish: %+v", result)
	}
	if len(requests) != 1 {
		t.Fatalf("wanted one owned interaction, got %d", len(requests))
	}
	if r := <-requests; r.ToolCall.ToolCallId != "owned" {
		t.Fatalf("foreign approval escaped: %+v", r)
	}
	waitTrace(t, ctx, trace, `"id":"foreign-approval","result":{"decision":"cancel"}`)
	waitTrace(t, ctx, trace, `"id":"owned-approval","result":{"decision":"accept"}`)
}

func TestIdleSteeringAcknowledgesNativeAdmissionBeforeTerminal(t *testing.T) {
	c, ctx, session, trace := newFixture(t, "no_ack", agentclient.HostCallbacks{})
	steerCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	response, err := c.Steer(steerCtx, agentclient.SteerRequest{SessionID: session, Prompt: []acp.ContentBlock{acp.TextBlock("start")}})
	if err != nil || response.Outcome != "startedNewTurn" {
		t.Fatalf("idle steering waited for native completion: %+v %v", response, err)
	}
	data, readErr := os.ReadFile(trace)
	if readErr != nil || steerCtx.Err() != nil || strings.Contains(string(data), `"method":"turn/interrupt"`) {
		t.Fatalf("steering returned only after its waiter cancelled: %v %v", steerCtx.Err(), readErr)
	}
	if err := c.Cancel(acp.CancelNotification{SessionId: session}); err != nil {
		t.Fatal(err)
	}
	waitTrace(t, ctx, trace, `"method":"turn/interrupt"`)
}

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
	"strconv"
	"strings"
	"sync"
	"syscall"
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
	if mode == "malformed_live" || mode == "initialize_rejected_live" {
		_ = os.WriteFile(os.Getenv("CODEX_NEXT_TRACE")+".pid", []byte(strconv.Itoa(os.Getpid())), 0600)
	}
	turnNumber := 0
	archived := true
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
			if mode == "initialize_rejected_live" {
				_ = out.Encode(map[string]any{"id": call.ID, "error": map[string]any{"code": -32602, "message": "initialize rejected"}})
				continue
			}
			ack(map[string]any{"userAgent": "codex/0.163.0"})
		case "initialized":
		case "account/read":
			if mode == "management" {
				ack(map[string]any{"account": map[string]any{"type": "chatgpt", "email": "fake@example.test", "planType": "plus"}, "requiresOpenaiAuth": true})
			} else {
				ack(map[string]any{"account": nil, "requiresOpenaiAuth": false})
			}
		case "account/rateLimits/read":
			if mode == "autonomous" || mode == "autonomous_after_terminal" {
				nativeTurn = "native-autonomous"
				emit("turn/started", turn("inProgress"))
				emit("thread/tokenUsage/updated", fixtureUsage("thread-1", nativeTurn, 19))
				emit("item/completed", map[string]any{"threadId": "thread-1", "turnId": nativeTurn, "item": map[string]any{"id": "autonomous-answer", "type": "agentMessage", "phase": "final_answer", "text": "detached body"}})
				emit("turn/completed", turn("completed"))
				emit("turn/completed", turn("completed"))
			}
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
			archived = call.Method == "thread/archive"
			ack(map[string]any{})
		case "skills/extraRoots/set", "skills/list":
			ack(map[string]any{})
		case "thread/start", "thread/resume":
			if mode == "skills_during_gate" && filepath.Base(call.Params["cwd"].(string)) == "secondary-workspace" {
				ack(map[string]any{"thread": map[string]any{"id": "thread-2"}, "model": "actual-model"})
				continue
			}
			if mode == "archived_identity" && call.Method == "thread/resume" {
				const id = "a560d6d5-8080-4acd-b659-fad0b0c86cde"
				if archived {
					text := fmt.Sprintf("session %s is archived. Run `codex unarchive %s` to unarchive it first.", id, id)
					_ = out.Encode(map[string]any{"id": call.ID, "error": map[string]any{"code": -32600, "message": text}})
				} else {
					ack(map[string]any{"thread": map[string]any{"id": id}, "model": "actual-model"})
				}
				continue
			}
			if mode == "unrelated_archived_error" && call.Method == "thread/resume" {
				_ = out.Encode(map[string]any{"id": call.ID, "error": map[string]any{
					"code": -32000, "message": "failed to read archived workspace template", "data": map[string]any{"resource": "workspace-template"},
				}})
				continue
			}
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
			if mode == "malformed_live" {
				ack(turn("inProgress"))
				fmt.Fprintln(os.Stdout, "this is not a native frame")
				continue
			}
			if mode == "late_steer_ack" || mode == "steer_target_completes" {
				ack(turn("inProgress"))
				emit("item/agentMessage/delta", map[string]any{"threadId": "thread-1", "turnId": nativeTurn, "itemId": "message-1", "delta": "hold"})
				if mode == "steer_target_completes" {
					marker := os.Getenv("CODEX_NEXT_TRACE") + ".complete"
					for deadline := time.Now().Add(3 * time.Second); ; time.Sleep(5 * time.Millisecond) {
						if _, err := os.Stat(marker); err == nil {
							break
						}
						if time.Now().After(deadline) {
							os.Exit(3)
						}
					}
					emit("turn/completed", turn("completed"))
				}
				continue
			}
			if strings.HasPrefix(mode, "usage_dip") {
				ack(turn("inProgress"))
				report := func(input, read, output, lastInput int) {
					last := map[string]any{"inputTokens": lastInput, "cachedInputTokens": 0, "outputTokens": 1, "reasoningOutputTokens": 0, "totalTokens": lastInput + 1}
					total := map[string]any{"inputTokens": input, "cachedInputTokens": read, "outputTokens": output, "reasoningOutputTokens": 2, "totalTokens": input + output}
					if mode == "usage_dip_explicit_zero_last" || mode == "usage_dip_explicit_zero_both" {
						last["cacheWriteInputTokens"] = 0
					}
					if mode == "usage_dip_explicit_zero_both" {
						total["cacheWriteInputTokens"] = 0
					}
					if mode == "usage_dip_write_positive_absent" || mode == "usage_dip_write_positive_zero" {
						total["cacheWriteInputTokens"] = 2
						if input == 39 {
							last["cacheWriteInputTokens"] = 2
						}
					}
					emit("thread/tokenUsage/updated", map[string]any{"threadId": "thread-1", "turnId": nativeTurn, "tokenUsage": map[string]any{"last": last, "total": total, "modelContextWindow": 100}})
				}
				switch turnNumber {
				case 1:
					initial := fixtureUsage("thread-1", nativeTurn, 19)
					if mode == "usage_dip_write_positive_zero" {
						initial["tokenUsage"].(map[string]any)["total"].(map[string]any)["cacheWriteInputTokens"] = 0
					}
					emit("thread/tokenUsage/updated", initial)
				case 2:
					dip := func() { emit("thread/tokenUsage/updated", fixtureUsage("thread-1", nativeTurn, 9)) }
					if mode == "usage_dip_terminal" {
						report(39, 4, 8, 20)
						dip()
						break
					}
					dip()
					if mode == "usage_dip_mismatch" {
						report(39, 4, 8, 3)
					} else {
						report(39, 4, 8, 20)
					}
					report(41, 4, 9, 2)
					report(41, 4, 9, 2)
				case 3:
					report(43, 4, 10, 2)
				}
				emit("turn/completed", turn("completed"))
				continue
			}
			if mode == "usage_reset" {
				ack(turn("inProgress"))
				n := 19
				if turnNumber == 2 {
					n = 9
				}
				emit("thread/tokenUsage/updated", fixtureUsage("thread-1", nativeTurn, n))
				emit("turn/completed", turn("completed"))
				continue
			}
			if mode == "usage_tail" {
				ack(turn("inProgress"))
				emit("item/agentMessage/delta", map[string]any{"threadId": "thread-1", "turnId": nativeTurn, "itemId": "message-1", "delta": "hold"})
				emit("turn/completed", turn("completed"))
				emit("thread/tokenUsage/updated", fixtureUsage("thread-1", nativeTurn, 19))
				continue
			}
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
		case "turn/steer":
			emit("turn/completed", turn("completed"))
			ack(map[string]any{"turnId": nativeTurn})
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
	initialize := acp.InitializeRequest{ProtocolVersion: 1}
	if mode == "receipts" || mode == "autonomous_after_terminal" {
		initialize.ClientCapabilities.Meta = map[string]any{acpmeta.NativeTurnReceiptsCapability: true}
	}
	if _, err := client.Initialize(ctx, initialize); err != nil {
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

func TestReviewDetachedNativeOutcomeOnWire(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	dir := t.TempDir()
	trace := filepath.Join(dir, "trace.ndjson")
	a, err := agentclient.NewACPAgent(func(host agentclient.HostCallbacks) (agentclient.Client, error) {
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
		c, closeCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer closeCancel()
		if err := a.Close(c); err != nil {
			t.Error(err)
		}
	})
	acp.NewAgentSideConnectionWithBinder(a, fromAgent, toAgent, a.SetAgentConnection)
	peer := acp.NewClientSideConnection(fixtureWireClient{}, fromClient, toClient)
	if _, err = peer.Initialize(ctx, acp.InitializeRequest{ProtocolVersion: 1}); err != nil {
		t.Fatal(err)
	}
	session, err := peer.NewSession(ctx, acp.NewSessionRequest{Cwd: dir, McpServers: []acp.McpServer{}})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := peer.CallExtension(ctx, "_session/steering", agentclient.SteerRequest{
		SessionID: session.SessionId, Prompt: []acp.ContentBlock{acp.TextBlock("run detached")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != `{"outcome":"startedNewTurn"}` {
		t.Fatalf("steering not admitted: %s", raw)
	}
	t.Logf("steering ACK: %s", raw)
	for {
		raw, err := peer.CallExtension(ctx, acpmeta.NativeTurnReceiptsMethod, acpmeta.NativeTurnReceiptsRequest{SessionID: string(session.SessionId)})
		if err != nil {
			t.Fatalf("admitted detached execution has no public result: %v", err)
		}
		var page acpmeta.NativeTurnReceiptsResponse
		if err := json.Unmarshal(raw, &page); err != nil {
			t.Fatal(err)
		}
		if len(page.Receipts) > 0 {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("no receipt before deadline: %s", raw)
		case <-time.After(5 * time.Millisecond):
		}
	}
}

func TestReviewLateSteeringACKReportsConsumedInput(t *testing.T) {
	active, release := make(chan struct{}), make(chan struct{})
	c, ctx, session, trace := newFixture(t, "late_steer_ack", agentclient.HostCallbacks{
		Observe: func(ctx context.Context, _ agentclient.Scope, n acp.SessionNotification) error {
			if n.Update.AgentMessageChunk == nil {
				return nil
			}
			close(active)
			select {
			case <-release:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		},
	})
	run := reserve(t, c, ctx, session, agentclient.PromptOptions{})
	defer close(release)
	done := make(chan agentclient.PromptResult, 1)
	go func() { done <- run() }()
	select {
	case <-active:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	steer, err := c.Steer(ctx, agentclient.SteerRequest{
		SessionID: session, PromptRequired: true, Prompt: []acp.ContentBlock{acp.TextBlock("inject into current turn")},
	})
	if err != nil || steer.Outcome != "injected" {
		t.Fatalf("steering ACK: %+v %v", steer, err)
	}
	result := func() agentclient.PromptResult {
		select {
		case <-done:
			t.Fatal("observer released unexpectedly")
		default:
		}
		// Replace the deferred close above with releasing an observation through one buffered send.
		release <- struct{}{}
		return <-done
	}()
	b, err := os.ReadFile(trace)
	if err != nil {
		t.Fatal(err)
	}
	for _, frame := range strings.Split(string(b), "\n") {
		var r struct {
			Method string
			Params map[string]any
		}
		if json.Unmarshal([]byte(frame), &r) == nil && r.Method == "turn/steer" {
			id, _ := r.Params["clientUserMessageId"].(string)
			if result.Outcome == nil || id == "" {
				t.Fatalf("missing result/input: %+v %q", result, id)
			}
			for _, consumed := range result.Outcome.Turn.ConsumedInputIDs {
				if consumed == id {
					return
				}
			}
			t.Fatalf("native injected ACK for %s absent from terminal receipt %v", id, result.Outcome.Turn.ConsumedInputIDs)
		}
	}
	t.Fatal("fixture did not see turn/steer")
}

func TestReviewArchivedSubstringMutatesWrongRecovery(t *testing.T) {
	c, ctx, session, trace := newFixture(t, "unrelated_archived_error", agentclient.HostCallbacks{})
	if _, err := c.CloseSession(ctx, acp.CloseSessionRequest{SessionId: session}); err != nil {
		t.Fatal(err)
	}
	_, err := c.ResumeSession(ctx, acp.ResumeSessionRequest{SessionId: session, Cwd: filepath.Dir(trace)})
	if err == nil {
		t.Fatal("unexpected resume")
	}
	b, _ := os.ReadFile(trace)
	var rpc *acp.RequestError
	if !errors.As(err, &rpc) {
		t.Fatalf("missing RPC error: %v", err)
	}
	if strings.Contains(string(b), `"method":"thread/unarchive"`) || rpc.Data == nil {
		t.Fatalf("foreign archived error triggered unarchive=%t and returned data=%v: %v", strings.Contains(string(b), `"method":"thread/unarchive"`), rpc.Data, err)
	}
}

func TestReviewResetUsageLastRequestStillObservable(t *testing.T) {
	c, ctx, session, _ := newFixture(t, "usage_reset", agentclient.HostCallbacks{})
	first := reserve(t, c, ctx, session, agentclient.PromptOptions{})()
	second := reserve(t, c, ctx, session, agentclient.PromptOptions{})()
	if first.Outcome == nil || first.Outcome.Turn.Usage == nil {
		t.Fatal("fixture did not get first usage")
	}
	if second.Outcome == nil || second.Outcome.Turn.Usage == nil {
		t.Fatalf("missing second usage: %+v", second)
	}
	usage := second.Outcome.Turn.Usage
	if usage.InputTokens == nil || *usage.InputTokens != 5 || usage.OutputTokens == nil || *usage.OutputTokens != 7 {
		t.Fatalf("native last={input:9, cached:4, output:7} after total reset returned input=%v output=%v complete=%t",
			printableReviewCount(usage.InputTokens), printableReviewCount(usage.OutputTokens), usage.Complete)
	}
}

func printableReviewCount(n *int64) any {
	if n == nil {
		return nil
	}
	return *n
}

func TestReviewProcessSkillRootsCanChangeAfterGate(t *testing.T) {
	c, ctx, session, trace := newFixture(t, "skills_during_gate", agentclient.HostCallbacks{})
	root := filepath.Join(filepath.Dir(trace), "secondary-workspace")
	extra := t.TempDir()
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(extra, ".agents", "skills"), 0700); err != nil {
		t.Fatal(err)
	}
	result := reserve(t, c, ctx, session, agentclient.PromptOptions{
		BeforeSubmit: func(ctx context.Context, _ agentclient.PromptSubmission) error {
			_, err := c.NewSession(ctx, acp.NewSessionRequest{Cwd: root, AdditionalDirectories: []string{extra}, McpServers: []acp.McpServer{}})
			return err
		},
	})()
	if result.Outcome == nil {
		t.Fatalf("prompt did not execute: %+v", result)
	}
	b, err := os.ReadFile(trace)
	if err != nil {
		t.Fatal(err)
	}
	var extraRoots []string
	for _, frame := range strings.Split(string(b), "\n") {
		var r struct {
			Method string
			Params struct{ ExtraRoots []string }
		}
		if json.Unmarshal([]byte(frame), &r) != nil {
			continue
		}
		if r.Method == "skills/extraRoots/set" {
			extraRoots = r.Params.ExtraRoots
		}
		if r.Method == "turn/start" {
			if len(extraRoots) != 0 {
				t.Fatalf("primary workspace turn/start inherited the other session's process skill roots: %v", extraRoots)
			}
			return
		}
	}
	t.Fatal("fixture saw no turn/start")
}

func TestReviewMalformedNativeStreamLeavesProcessRunning(t *testing.T) {
	c, ctx, session, trace := newFixture(t, "malformed_live", agentclient.HostCallbacks{})
	outcome := reserve(t, c, ctx, session, agentclient.PromptOptions{})()
	if outcome.Outcome == nil || outcome.Outcome.OperationError == nil ||
		!strings.Contains(outcome.Outcome.OperationError.Error(), "invalid Codex native frame") {
		t.Fatalf("malformed stream was not reported: %+v", outcome)
	}
	raw, err := os.ReadFile(trace + ".pid")
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(string(raw))
	if err != nil {
		t.Fatal(err)
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return
	}
	defer p.Release()
	deadline := time.After(350 * time.Millisecond)
	for p.Signal(syscall.Signal(0)) == nil {
		select {
		case <-deadline:
			t.Fatal("the original live app-server still owns resources after the reader failed")
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func TestAutonomousReceiptRetainsIndependentOutcomeUntilScopedACK(t *testing.T) {
	observeErr := errors.New("detached observer failed")
	c, ctx, session, _ := newFixture(t, "autonomous", agentclient.HostCallbacks{Observe: func(_ context.Context, scope agentclient.Scope, _ acp.SessionNotification) error {
		if scope.HostInvocationID != "" || scope.NativeInvocationID == "" {
			t.Errorf("autonomous observation claimed host input: %+v", scope)
		}
		return observeErr
	}})
	request := acpmeta.NativeTurnReceiptsRequest{SessionID: string(session)}
	if _, err := c.ReadAccountUsage(ctx); err != nil {
		t.Fatal(err)
	}
	page, err := c.ReadNativeOutcomes(ctx, request, true)
	if err != nil || len(page.Outcomes) != 1 || page.More {
		t.Fatalf("autonomous receipt: %+v %v", page, err)
	}
	o := page.Outcomes[0]
	if o.Turn.Origin != acpmeta.NativeTurnOriginUnknown || o.Turn.Status != acpmeta.NativeTurnCompleted ||
		len(o.Turn.ConsumedInputIDs) != 0 || o.Turn.Usage == nil || !errors.Is(o.DeliveryError, observeErr) ||
		o.FinalMessage == nil || o.FinalMessage.Text != "detached body" {
		t.Fatalf("autonomous execution was lost or attributed to host: %+v", o)
	}
	*o.Turn.Usage.InputTokens = 999
	o.FinalMessage.Text = "mutated"
	if _, err := c.ReadNativeOutcomes(ctx, acpmeta.NativeTurnReceiptsRequest{
		SessionID: "different-thread", AckInvocationIDs: []string{o.Turn.InvocationID},
	}, false); err != nil {
		t.Fatal(err)
	}
	if _, err := c.CloseSession(ctx, acp.CloseSessionRequest{SessionId: session}); err != nil {
		t.Fatal(err)
	}
	page, err = c.ReadNativeOutcomes(ctx, request, false)
	if err != nil || len(page.Outcomes) != 1 || *page.Outcomes[0].Turn.Usage.InputTokens != 15 || page.Outcomes[0].FinalMessage.Text != "detached body" {
		t.Fatalf("receipt mutated or lost on close: %+v %v", page, err)
	}
	request.AckInvocationIDs = []string{o.Turn.InvocationID}
	for range 2 {
		page, err = c.ReadNativeOutcomes(ctx, request, true)
		if err != nil || len(page.Outcomes) != 0 {
			t.Fatalf("ACK was not idempotent: %+v %v", page, err)
		}
	}
}

func TestResumeRecoversOnlyNativeArchivedThreadIdentity(t *testing.T) {
	c, ctx, _, trace := newFixture(t, "archived_identity", agentclient.HostCallbacks{})
	const id = "a560d6d5-8080-4acd-b659-fad0b0c86cde"
	request := acp.ResumeSessionRequest{SessionId: "b560d6d5-8080-4acd-b659-fad0b0c86cde", Cwd: filepath.Dir(trace)}
	if _, err := c.ResumeSession(ctx, request); err == nil {
		t.Fatal("resume accepted evidence for a different archived thread")
	}
	request.SessionId = id
	if _, err := c.ResumeSession(ctx, request); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(trace)
	if err != nil || strings.Count(string(raw), `"method":"thread/unarchive"`) != 1 || !strings.Contains(string(raw), `"method":"thread/unarchive","params":{"threadId":"`+id+`"}`) {
		t.Fatalf("incorrect native archive transition: %v %s", err, raw)
	}
}

func TestAutonomousAfterTerminalSurvivesPreviousObserverDrain(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var first sync.Once
	c, ctx, session, _ := newFixture(t, "autonomous_after_terminal", agentclient.HostCallbacks{Observe: func(ctx context.Context, scope agentclient.Scope, _ acp.SessionNotification) error {
		if scope.HostInvocationID != "" {
			first.Do(func() { close(entered) })
			select {
			case <-release:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
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
	prompt := make(chan agentclient.PromptResult, 1)
	go func() { prompt <- run() }()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if _, err := c.ReadAccountUsage(ctx); err != nil {
		t.Fatal(err)
	}
	request := acpmeta.NativeTurnReceiptsRequest{SessionID: string(session)}
	if page, err := c.ReadNativeOutcomes(ctx, request, false); err != nil || len(page.Outcomes) != 0 {
		t.Fatalf("unsettled native prefix was skipped: %+v %v", page, err)
	}
	close(release)
	select {
	case result := <-prompt:
		if result.Outcome == nil || result.Outcome.Turn.Status != acpmeta.NativeTurnCompleted {
			t.Fatalf("first terminal lost: %+v", result)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	wait, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	page, err := c.ReadNativeOutcomes(wait, request, true)
	if err != nil || len(page.Outcomes) == 0 || page.Outcomes[0].Turn.NativeTurnID == nil ||
		*page.Outcomes[0].Turn.NativeTurnID != "turn-1" {
		t.Fatalf("first native terminal did not retain its receipt order: %+v %v", page, err)
	}
	request.AckInvocationIDs = []string{page.Outcomes[0].Turn.InvocationID}
	page, err = c.ReadNativeOutcomes(wait, request, true)
	if err != nil || len(page.Outcomes) != 1 || page.Outcomes[0].Turn.NativeTurnID == nil || *page.Outcomes[0].Turn.NativeTurnID != "native-autonomous" {
		t.Fatalf("detached continuation was lost behind previous observer: %+v %v", page, err)
	}
}

func TestForegroundReceiptPagesKeepCanonicalOrderAndExactACK(t *testing.T) {
	c, ctx, session, _ := newFixture(t, "receipts", agentclient.HostCallbacks{})
	var expected []string
	for range 6 {
		result := reserve(t, c, ctx, session, agentclient.PromptOptions{})()
		if result.Outcome == nil || result.Outcome.Turn.Status != acpmeta.NativeTurnCompleted {
			t.Fatalf("prompt lost terminal: %+v", result)
		}
		expected = append(expected, result.Outcome.Turn.InvocationID)
	}
	request := acpmeta.NativeTurnReceiptsRequest{SessionID: string(session)}
	page, err := c.ReadNativeOutcomes(ctx, request, false)
	if err != nil || !page.More || len(page.Outcomes) != 4 {
		t.Fatalf("first native page: %+v %v", page, err)
	}
	for i, o := range page.Outcomes {
		if o.Turn.InvocationID != expected[i] {
			t.Fatalf("wrong completion order: %+v", page)
		}
	}
	request.AckInvocationIDs = []string{"unknown", expected[0], expected[1], expected[2], expected[3]}
	page, err = c.ReadNativeOutcomes(ctx, request, false)
	if err != nil || page.More || len(page.Outcomes) != 2 {
		t.Fatalf("remaining native page: %+v %v", page, err)
	}
	for i, o := range page.Outcomes {
		if o.Turn.InvocationID != expected[4+i] {
			t.Fatalf("ACK removed another invocation: %+v", page)
		}
	}
}

func TestRereviewHandshakeRejectionRetiresLiveProcess(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	dir := t.TempDir()
	trace := filepath.Join(dir, "trace.ndjson")
	c, err := codex.NewClient(ctx, codex.Options{
		Executable: os.Args[0], PrefixArgs: []string{"-test.run=^TestAppServerFixture$", "--"},
		Environment:      append(os.Environ(), "CODEX_NEXT_FIXTURE=initialize_rejected_live", "CODEX_NEXT_TRACE="+trace),
		WorkingDirectory: dir,
	}, agentclient.HostCallbacks{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := c.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	for range 2 {
		_, err = c.Initialize(ctx, acp.InitializeRequest{ProtocolVersion: 1})
		var rpc *acp.RequestError
		if !errors.As(err, &rpc) || rpc.Code != -32602 || rpc.Message != "initialize rejected" {
			t.Fatalf("Initialize did not retain the native rejection: %v", err)
		}
		t.Logf("initialize error: %v", err)
	}
	frames, err := os.ReadFile(trace)
	if err != nil || strings.Count(string(frames), `"method":"initialize"`) != 1 {
		t.Fatalf("native initialization was not single-attempt: %s, %v", frames, err)
	}
	raw, err := os.ReadFile(trace + ".pid")
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(string(raw))
	if err != nil {
		t.Fatal(err)
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return
	}
	defer p.Release()
	deadline := time.After(350 * time.Millisecond)
	for p.Signal(syscall.Signal(0)) == nil {
		select {
		case <-deadline:
			t.Fatal("permanently unusable client left its live native process running")
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func TestRereviewUsageDipKeepsEstablishedHighWater(t *testing.T) {
	c, ctx, session, _ := newFixture(t, "usage_dip", agentclient.HostCallbacks{})
	first := reserve(t, c, ctx, session, agentclient.PromptOptions{})()
	if first.Outcome == nil || first.Outcome.Turn.Usage == nil {
		t.Fatal("fixture did not establish high water")
	}
	second := reserve(t, c, ctx, session, agentclient.PromptOptions{})()
	if second.Outcome == nil || second.Outcome.Turn.Usage == nil {
		t.Fatal("matching usage absent")
	}
	u := second.Outcome.Turn.Usage
	if u.InputTokens == nil || u.OutputTokens == nil {
		t.Fatalf("usage absent: %+v", u)
	}
	t.Logf("after valid cumulative 19→9→39→41→41, got input=%d output=%d coverage=%s complete=%t", *u.InputTokens, *u.OutputTokens, u.Coverage, u.Complete)
	if *u.InputTokens != 22 || *u.OutputTokens != 2 || u.Coverage != "observed_requests" || u.Complete {
		t.Fatalf("exact-turn monotonic observations above original high water discarded, duplicate fixture must not double count")
	}
}

func TestUsageRecoverySkipsUncorroboratedAdvance(t *testing.T) {
	c, ctx, session, _ := newFixture(t, "usage_dip_mismatch", agentclient.HostCallbacks{})
	_ = reserve(t, c, ctx, session, agentclient.PromptOptions{})()
	assert := func(wantInput, wantOutput int64, wantCoverage string, complete bool) {
		t.Helper()
		got := reserve(t, c, ctx, session, agentclient.PromptOptions{})()
		if got.Outcome == nil || got.Outcome.Turn.Usage == nil {
			t.Fatalf("missing usage: %+v", got)
		}
		u := got.Outcome.Turn.Usage
		if u.InputTokens == nil || *u.InputTokens != wantInput || u.OutputTokens == nil || *u.OutputTokens != wantOutput ||
			u.Coverage != wantCoverage || u.Complete != complete {
			t.Fatalf("usage = %+v (input=%v output=%v)", u, printableReviewCount(u.InputTokens), printableReviewCount(u.OutputTokens))
		}
	}
	assert(2, 1, "last_request", false)
	assert(2, 1, "observed_turn", true)
}

func TestUsageDipAfterAttributionDowngradesCoverage(t *testing.T) {
	c, ctx, session, _ := newFixture(t, "usage_dip_terminal", agentclient.HostCallbacks{})
	_ = reserve(t, c, ctx, session, agentclient.PromptOptions{})()
	got := reserve(t, c, ctx, session, agentclient.PromptOptions{})()
	if got.Outcome == nil || got.Outcome.Turn.Usage == nil {
		t.Fatalf("missing usage: %+v", got)
	}
	u := got.Outcome.Turn.Usage
	if u.InputTokens == nil || *u.InputTokens != 20 || u.OutputTokens == nil || *u.OutputTokens != 1 ||
		u.Coverage != "last_request" || u.Complete {
		t.Fatalf("usage after a late dip = %+v", u)
	}
}

func TestUsageDipOptionalZeroKeepsCorroboratedRequests(t *testing.T) {
	for _, mode := range []string{"usage_dip_explicit_zero_last", "usage_dip_explicit_zero_both"} {
		t.Run(mode, func(t *testing.T) {
			c, ctx, session, _ := newFixture(t, mode, agentclient.HostCallbacks{})
			_ = reserve(t, c, ctx, session, agentclient.PromptOptions{})()
			got := reserve(t, c, ctx, session, agentclient.PromptOptions{})()
			if got.Outcome == nil || got.Outcome.Turn.Usage == nil {
				t.Fatalf("missing final usage: %+v", got)
			}
			u := got.Outcome.Turn.Usage
			t.Logf("input=%v output=%v coverage=%s complete=%t", printableReviewCount(u.InputTokens), printableReviewCount(u.OutputTokens), u.Coverage, u.Complete)
			if u.InputTokens == nil || *u.InputTokens != 22 || u.OutputTokens == nil || *u.OutputTokens != 2 || u.Coverage != "observed_requests" || u.Complete {
				t.Fatalf("optional zero changed recovered request attribution: %+v", u)
			}
			if mode == "usage_dip_explicit_zero_last" {
				if u.CacheWriteTokens != nil {
					t.Fatalf("last-request proof introduced cache-write presence: %+v", u)
				}
			} else if u.CacheWriteTokens == nil || *u.CacheWriteTokens != 0 {
				t.Fatalf("explicit cumulative cache-write zero was not retained: %+v", u)
			}
		})
	}
}

func TestUsageDipOptionalCounterCanAdvanceFromDefault(t *testing.T) {
	for _, mode := range []string{"usage_dip_write_positive_zero", "usage_dip_write_positive_absent"} {
		t.Run(mode, func(t *testing.T) {
			c, ctx, session, _ := newFixture(t, mode, agentclient.HostCallbacks{})
			_ = reserve(t, c, ctx, session, agentclient.PromptOptions{})()
			result := reserve(t, c, ctx, session, agentclient.PromptOptions{})()
			if result.Outcome == nil || result.Outcome.Turn.Usage == nil {
				t.Fatalf("missing usage: %+v", result)
			}
			u := result.Outcome.Turn.Usage
			if u.InputTokens == nil || *u.InputTokens != 20 || u.OutputTokens == nil || *u.OutputTokens != 2 ||
				u.CacheWriteTokens == nil || *u.CacheWriteTokens != 2 || u.Coverage != "observed_requests" || u.Complete {
				t.Fatalf("expected 20/2/2 observed requests, got %v/%v/%v, coverage=%s complete=%t",
					printableReviewCount(u.InputTokens), printableReviewCount(u.OutputTokens), printableReviewCount(u.CacheWriteTokens), u.Coverage, u.Complete)
			}
		})
	}
}

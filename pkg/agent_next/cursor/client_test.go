package cursor_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/JieWaZi/acp-go/pkg/acpmeta"
	agentclient "github.com/JieWaZi/acp-go/pkg/agent_next"
	"github.com/JieWaZi/acp-go/pkg/agent_next/cursor"
	acp "github.com/JieWaZi/acp-go/sdk"
)

func TestCursorFixture(t *testing.T) {
	if os.Getenv("CURSOR_NEXT_FIXTURE") != "1" {
		return
	}
	scenario := os.Getenv("CURSOR_NEXT_SCENARIO")
	trace, err := os.OpenFile(os.Getenv("CURSOR_NEXT_TRACE"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		os.Exit(2)
	}
	encoder := json.NewEncoder(os.Stdout)
	scanner := bufio.NewScanner(os.Stdin)
	var promptID json.RawMessage
	var prompts, opens int
	var callbackWaiting bool
	activeModel := ""
	usage := func() map[string]any {
		return map[string]any{"inputTokens": prompts * 17, "cachedReadTokens": 0, "cachedWriteTokens": 0}
	}
	notify := func(id, update string, fields map[string]any) {
		fields["sessionUpdate"] = update
		_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": map[string]any{"sessionId": id, "update": fields}})
	}
	config := func(id, fastKey string, fast bool) {
		notify(id, "config_option_update", map[string]any{"configOptions": fixtureOptions(fastKey, fast)})
	}
	reply := func(id json.RawMessage, result any) {
		_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
	}
	if len(os.Args) > 0 && os.Args[len(os.Args)-1] == "--version" {
		_, _ = fmt.Fprintln(os.Stdout, "2026.10.22-27dcf5f")
		os.Exit(0)
	}
	if len(os.Args) >= 3 && strings.Join(os.Args[len(os.Args)-3:], " ") == "status --format json" {
		_, _ = fmt.Fprintln(trace, "status")
		switch scenario {
		case "status-signed-out":
			_ = encoder.Encode(map[string]any{"isAuthenticated": false})
		case "status-invalid":
			_, _ = fmt.Fprintln(os.Stdout, `{"accessToken":"fixture-secret"}`)
		default:
			_, _ = fmt.Fprintln(os.Stdout, `{"isAuthenticated":true,"userInfo":{"email":"cursor@example.invalid","userId":2147483201},"accessToken":"fixture-secret"}`)
		}
		os.Exit(0)
	}
	for scanner.Scan() {
		var msg struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params map[string]any  `json:"params"`
		}
		if json.Unmarshal(scanner.Bytes(), &msg) != nil {
			os.Exit(3)
		}
		_, _ = fmt.Fprintln(trace, string(scanner.Bytes()))
		if callbackWaiting && msg.Method == "" {
			if string(msg.ID) == "9000" {
				reply(promptID, map[string]any{"stopReason": "end_turn", "usage": usage()})
				callbackWaiting = false
			}
			continue
		}
		switch msg.Method {
		case "initialize":
			caps := map[string]any{"close": map[string]any{}, "resume": map[string]any{}, "list": map[string]any{}}
			if scenario == "fork" {
				caps["fork"] = map[string]any{}
			}
			reply(msg.ID, map[string]any{"protocolVersion": 1,
				"agentCapabilities": map[string]any{"loadSession": true, "sessionCapabilities": caps},
				"authMethods":       []any{map[string]any{"id": "cursor_login", "name": "Cursor sign-in"}},
			})
		case "authenticate":
			reply(msg.ID, map[string]any{})
		case "session/new":
			opens++
			if scenario == "process-model" || scenario == "family-failed-model" {
				id, model := "one", "model-a"
				if opens == 2 {
					id, model = "two", "model-b"
				}
				activeModel = model
				reply(msg.ID, map[string]any{"sessionId": id, "configOptions": modelOptions(model, opens == 1)})
				continue
			}
			config("foreign", "wrong", false)
			config("one", "native-fast", false)
			reply(msg.ID, map[string]any{"sessionId": "one"})
			if scenario == "opening-post" {
				config("one", "native-fast", true)
			}
		case "session/fork":
			config("one", "source-fast", false)
			config("forked", "child-fast", false)
			reply(msg.ID, map[string]any{"sessionId": "forked"})
		case "session/resume", "session/load":
			if msg.Method == "session/load" {
				notify("one", "agent_message_chunk", map[string]any{"content": map[string]any{"type": "text", "text": "history"}})
			}
			config("one", "native-fast", true)
			reply(msg.ID, map[string]any{})
		case "session/set_config_option":
			if scenario == "fork" && msg.Params["sessionId"] == "forked" && msg.Params["configId"] == "child-fast" {
				config("forked", "child-fast", true)
				reply(msg.ID, map[string]any{})
				continue
			}
			if scenario == "process-model" || scenario == "family-failed-model" {
				switch msg.Params["configId"] {
				case "native-model":
					activeModel = fmt.Sprint(msg.Params["value"])
					if scenario == "family-failed-model" && activeModel == "model-b" {
						_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": msg.ID, "error": map[string]any{"code": -32602, "message": "could not confirm model save"}})
						continue
					}
					reply(msg.ID, map[string]any{"configOptions": modelOptions(fmt.Sprint(msg.Params["value"]), false)})
				case "native-fast":
					reply(msg.ID, map[string]any{"configOptions": modelOptions("model-a", msg.Params["value"] == true)})
				}
				continue
			}
			if key, ok := msg.Params["configId"].(string); ok && (key == "native-fast" || key == "newer-fast") {
				config("one", key, msg.Params["value"] == true)
				reply(msg.ID, map[string]any{})
			} else {
				_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": msg.ID, "error": map[string]any{"code": -32602, "message": "unexpected setting"}})
			}
		case "cursor/list_available_models":
			models := []any{map[string]any{"value": "model-a", "name": "Model A", "configOptions": fixtureOptions("native-fast", false)[1:]}}
			switch scenario {
			case "catalog-slow":
				time.Sleep(3250 * time.Millisecond)
			case "catalog-unconfirmed":
				models = []any{}
			case "catalog-parameterized":
				models = []any{map[string]any{"value": "model-a", "name": "Model A", "configOptions": []any{
					map[string]any{"id": "effort", "name": "Effort", "category": "thought_level", "type": "select", "currentValue": "high",
						"options": []any{map[string]any{"value": "high", "name": "High"}}},
					map[string]any{"id": "fast", "name": "Fast", "category": "model_config", "type": "select", "currentValue": "false",
						"options": []any{map[string]any{"value": "false", "name": "Off"}, map[string]any{"value": "true", "name": "On"}}},
				}}}
			}
			reply(msg.ID, map[string]any{"models": models})
		case "session/close", "session/list":
			reply(msg.ID, map[string]any{"sessions": []any{}})
		case "session/prompt":
			prompts++
			promptID = msg.ID
			if code, ok := strings.CutPrefix(scenario, "reject-after-output:"); ok || scenario == "wire-reject-after-output" && prompts == 3 {
				if !ok {
					code = "-32602"
				}
				rejection, err := strconv.Atoi(code)
				if err != nil {
					os.Exit(3)
				}
				notify("one", "agent_message_chunk", map[string]any{"content": map[string]any{"type": "text", "text": "session observation"}})
				_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": msg.ID, "error": map[string]any{"code": rejection, "message": "prompt rejected"}})
				continue
			}
			switch scenario {
			case "terminal-only":
				reply(msg.ID, map[string]any{"stopReason": "end_turn"})
				continue
			case "reject-prompt":
				_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": msg.ID, "error": map[string]any{"code": -32000, "message": "prompt rejected"}})
				continue
			case "unproven-prompt":
				reply(msg.ID, map[string]any{"stopReason": "future_reason"})
				continue
			}
			if scenario == "interaction" {
				_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": 9000, "method": "cursor/ask_question", "params": map[string]any{
					"sessionId": "one", "toolCallId": "question", "title": "Choose", "questions": []any{
						map[string]any{"id": "q", "prompt": "Choose", "options": []any{map[string]any{"id": "choice", "label": "Choice"}}},
					},
				}})
				callbackWaiting = true
				continue
			}
			notify("foreign", "usage_update", map[string]any{"used": 99, "size": 999})
			notify("one", "usage_update", map[string]any{"used": 9})
			notify("one", "agent_message_chunk", map[string]any{"content": map[string]any{"type": "text", "text": "native answer"}})
			if scenario == "flood" {
				for i := 0; i < 300; i++ {
					notify("one", "agent_message_chunk", map[string]any{"content": map[string]any{"type": "text", "text": "more"}})
				}
			}
			if (scenario == "cancel" || scenario == "disconnect") && prompts == 1 {
				if scenario == "disconnect" {
					os.Exit(0)
				}
				continue
			}
			tokens := usage()
			if scenario == "family-failed-model" {
				reply(msg.ID, map[string]any{"stopReason": "end_turn", "_meta": acpmeta.WithTurnUsage(nil, acpmeta.TurnUsage{Model: activeModel, InputTokens: acp.Ptr(int64(1)), Coverage: "invocation", Source: "fixture.actual-model"})})
				continue
			}
			if scenario == "family-usage-dip" {
				values := []int{100, 80, 110, 116, 94, 98, 120, 126}
				tokens = map[string]any{"inputTokens": values[prompts-1], "outputTokens": values[prompts-1] / 2, "cachedReadTokens": 0, "cachedWriteTokens": 0}
			}
			if scenario == "family-usage-gap" {
				values := []map[string]any{
					{"inputTokens": 100, "outputTokens": 50, "cachedReadTokens": 2, "cachedWriteTokens": 1},
					{"inputTokens": 110, "outputTokens": 55, "cachedReadTokens": 3},
					{"inputTokens": 120, "outputTokens": 60, "cachedReadTokens": 4, "cachedWriteTokens": 2},
					nil,
					{"inputTokens": 140, "outputTokens": 70, "cachedReadTokens": 7, "cachedWriteTokens": 4},
					{"inputTokens": 150, "outputTokens": 75, "cachedReadTokens": 8, "cachedWriteTokens": 5},
				}
				tokens = values[prompts-1]
			}
			if scenario == "family-closure-cumulative-notification" {
				if prompts == 2 {
					notify("one", "usage_update", map[string]any{"used": 9, "size": 100, "_meta": acpmeta.WithTurnUsage(nil, acpmeta.TurnUsage{
						InputTokens: acp.Ptr(int64(110)), OutputTokens: acp.Ptr(int64(55)), Source: "fixture.session", Coverage: "session_cumulative", Complete: true,
					})})
				}
				reply(msg.ID, map[string]any{"stopReason": "end_turn", "usage": map[string]any{
					"inputTokens": 100 + (prompts-1)*10, "outputTokens": 50 + (prompts-1)*5, "cachedReadTokens": 0, "cachedWriteTokens": 0,
				}})
				continue
			}
			if scope, ok := strings.CutPrefix(scenario, "family-closure-known-"); ok {
				meta := acpmeta.WithTurnUsage(nil, acpmeta.TurnUsage{
					InputTokens: acp.Ptr(int64(8)), OutputTokens: acp.Ptr(int64(5)), Coverage: scope, Source: "fixture.known",
				})
				reply(msg.ID, map[string]any{"stopReason": "end_turn", "_meta": meta})
				continue
			}
			if scenario == "family-closure-no-standard" {
				report := func(source, scope string, input int64) acpmeta.TurnUsage {
					return acpmeta.TurnUsage{InputTokens: acp.Ptr(input), OutputTokens: acp.Ptr(input / 2),
						Coverage: scope, Source: source, Complete: true, ContextKnown: true}
				}
				if prompts == 2 {
					notify("one", "usage_update", map[string]any{"used": 9, "size": 100, "_meta": acpmeta.WithTurnUsage(nil, report("fixture.invocation", "invocation", 8))})
				}
				for _, observed := range []acpmeta.TurnUsage{
					report("fixture.future", "future_scope", 50),
					report("fixture.empty", "", 40),
					report("fixture.last", "last_request", 20),
					report("fixture.context", "context", 60),
				} {
					notify("one", "usage_update", map[string]any{"used": 9, "size": 100, "_meta": acpmeta.WithTurnUsage(map[string]any{"fixture.retained": true}, observed)})
				}
				final := report("fixture.future", "future_scope", 80)
				final.Observations = []acpmeta.UsageObservation{
					{Source: final.Source, Coverage: final.Coverage, Tokens: acpmeta.ModelUsage{InputTokens: acp.Ptr(int64(70))}},
				}
				reply(msg.ID, map[string]any{"stopReason": "end_turn", "_meta": acpmeta.WithTurnUsage(nil, final)})
				continue
			}
			if scenario == "family-scoped-usage" {
				response := map[string]any{"stopReason": "end_turn", "usage": map[string]any{
					"inputTokens": 100 + (prompts-1)*11, "outputTokens": 50 + (prompts-1)*6, "cachedReadTokens": 0, "cachedWriteTokens": 0,
				}}
				if prompts <= 2 {
					notify("one", "usage_update", map[string]any{"used": 9, "size": 100, "_meta": acpmeta.WithTurnUsage(nil, acpmeta.TurnUsage{
						InputTokens: acp.Ptr(int64(7)), OutputTokens: acp.Ptr(int64(4)), Source: "fixture.invocation", Coverage: "invocation", Complete: true,
					})})
				}
				if prompts == 2 {
					response["_meta"] = acpmeta.WithTurnUsage(nil, acpmeta.TurnUsage{
						InputTokens: acp.Ptr(int64(8)), OutputTokens: acp.Ptr(int64(5)), Source: "fixture.final", Coverage: "invocation", Complete: true,
					})
				}
				reply(msg.ID, response)
				continue
			}
			if scenario == "unknown-cache" {
				delete(tokens, "cachedWriteTokens")
			}
			reply(msg.ID, map[string]any{"stopReason": "end_turn", "usage": tokens})
			if scenario == "eof" {
				os.Exit(0)
			}
			if scenario == "late" {
				notify("one", "usage_update", map[string]any{"used": 77, "size": 777})
			}
		case "session/cancel":
			reply(promptID, map[string]any{"stopReason": "cancelled", "usage": usage()})
		}
	}
	os.Exit(0)
}

func modelOptions(model string, fast bool) []any {
	options := fixtureOptions("native-fast", fast)
	entry := options[0].(map[string]any)
	entry["currentValue"] = model
	entry["options"] = []any{map[string]any{"value": "model-a", "name": "Model A"}, map[string]any{"value": "model-b", "name": "Model B"}}
	return options
}

func fixtureOptions(fastKey string, fast bool) []any {
	return []any{
		map[string]any{"id": "native-model", "name": "Model", "type": "select", "category": "model", "options": []any{map[string]any{"value": "model-a", "name": "Model A"}}},
		map[string]any{"id": fastKey, "name": "Fast", "type": "boolean", "category": "model_config", "currentValue": fast},
	}
}

func candidate(t *testing.T, scenario string, callbacks agentclient.HostCallbacks, customize ...func(*cursor.Options)) (*cursor.Client, context.Context, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 7*time.Second)
	t.Cleanup(cancel)
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	trace := filepath.Join(dir, "wire")
	options := cursor.Options{
		Executable: binary, PrefixArgs: []string{"-test.run=^TestCursorFixture$", "--"},
		Environment:      append(os.Environ(), "CURSOR_NEXT_FIXTURE=1", "CURSOR_NEXT_SCENARIO="+scenario, "CURSOR_NEXT_TRACE="+trace),
		WorkingDirectory: dir,
	}
	for _, apply := range customize {
		apply(&options)
	}
	client, err := cursor.NewClient(ctx, options, callbacks)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		closeCtx, closeCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer closeCancel()
		if err := client.Close(closeCtx); err != nil {
			t.Error(err)
		}
	})
	if _, err := os.Stat(trace); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("constructor launched native program: %v", err)
	}
	if _, err := client.Initialize(ctx, acp.InitializeRequest{ProtocolVersion: 1}); err != nil {
		t.Fatal(err)
	}
	return client, ctx, trace
}

func openOne(t *testing.T, client *cursor.Client, ctx context.Context, trace string) acp.NewSessionResponse {
	t.Helper()
	opened, err := client.NewSession(ctx, acp.NewSessionRequest{Cwd: filepath.Dir(trace), McpServers: []acp.McpServer{}})
	if err != nil {
		t.Fatal(err)
	}
	return opened
}

func TestOpeningPublishesSetterReadyEvidence(t *testing.T) {
	var mu sync.Mutex
	var sessions []acp.SessionId
	var history bool
	client, ctx, trace := candidate(t, "", agentclient.HostCallbacks{Observe: func(_ context.Context, s agentclient.Scope, note acp.SessionNotification) error {
		mu.Lock()
		sessions = append(sessions, s.SessionID)
		if chunk := note.Update.AgentMessageChunk; chunk != nil && chunk.Content.Text != nil && chunk.Content.Text.Text == "history" {
			history = s.NativeInvocationID == "" && s.HostInvocationID == ""
		}
		mu.Unlock()
		return nil
	}})
	opened := openOne(t, client, ctx, trace)
	if opened.SessionId != "one" || len(opened.ConfigOptions) != 2 {
		t.Fatalf("opening = %#v", opened)
	}
	if opened.ConfigOptions[0].Select == nil || opened.ConfigOptions[0].Select.CurrentValue != "" {
		t.Fatalf("catalog invented a current model: %#v", opened.ConfigOptions[0])
	}
	request := acp.SetSessionConfigOptionRequest{Boolean: &acp.SetSessionConfigOptionBoolean{SessionId: opened.SessionId, ConfigId: "native-fast", Value: true}}
	got, err := client.SetSessionConfigOption(ctx, request)
	if err != nil || len(got.ConfigOptions) != 2 || got.ConfigOptions[1].Boolean == nil || !got.ConfigOptions[1].Boolean.CurrentValue {
		t.Fatalf("setter receipt = %#v, %v", got, err)
	}
	loaded, err := client.LoadSession(ctx, acp.LoadSessionRequest{SessionId: "one", Cwd: filepath.Dir(trace), McpServers: []acp.McpServer{}})
	if err != nil || len(loaded.ConfigOptions) != 2 {
		t.Fatalf("load = %#v %v", loaded, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if !history {
		t.Fatal("native Load history did not remain outside the foreground invocation")
	}
	for _, session := range sessions {
		if session != "one" {
			t.Fatalf("unexpected scope %q", session)
		}
	}
}

func promptRequest() acp.PromptRequest {
	return acp.PromptRequest{SessionId: "one", Prompt: []acp.ContentBlock{{Text: &acp.ContentBlockText{Text: "hello"}}}}
}

func reserve(t *testing.T, client *cursor.Client, ctx context.Context, opts agentclient.PromptOptions) func() agentclient.PromptResult {
	t.Helper()
	run, err := client.ReservePrompt(ctx, promptRequest(), opts)
	if err != nil {
		t.Fatal(err)
	}
	return run
}

func requireTerminal(t *testing.T, result agentclient.PromptResult, reason acp.StopReason) {
	t.Helper()
	requireStopReason(t, result, reason)
	usage := result.Outcome.Turn.Usage
	if usage == nil || usage.InputTokens == nil || *usage.InputTokens != 17 || usage.OutputTokens != nil {
		t.Fatalf("native usage = %#v", usage)
	}
}

func requireStopReason(t *testing.T, result agentclient.PromptResult, reason acp.StopReason) {
	t.Helper()
	o := result.Outcome
	if o == nil || o.StopReason == nil || *o.StopReason != reason || result.Receipt.Admission != agentclient.AdmissionConfirmed {
		t.Fatalf("native facts = %#v / %#v", result.Receipt, o)
	}
	if result.Receipt.Input.NativeInputID != nil {
		t.Fatalf("invented native message id: %#v", result.Receipt)
	}
}

func TestResponseFactsSurviveEOFAndObserverRejection(t *testing.T) {
	client, ctx, trace := candidate(t, "eof", agentclient.HostCallbacks{Observe: func(_ context.Context, scope agentclient.Scope, _ acp.SessionNotification) error {
		if scope.NativeInvocationID != "" {
			return errors.New("receiver rejected output")
		}
		return nil
	}})
	openOne(t, client, ctx, trace)
	result := reserve(t, client, ctx, agentclient.PromptOptions{})()
	requireTerminal(t, result, acp.StopReasonEndTurn)
	if result.Outcome.DeliveryError == nil {
		t.Fatalf("lost delivery error: %#v", result.Outcome)
	}
	if result.Outcome.ContextUsage.UsedTokens == nil || *result.Outcome.ContextUsage.UsedTokens != 9 || result.Outcome.ContextUsage.WindowTokens != nil {
		t.Fatalf("context evidence = %#v", result.Outcome.ContextUsage)
	}
}

func TestCancelBypassesBlockedObserveAndSettlesBeforeSuccessor(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	defer close(release)
	var once sync.Once
	client, ctx, trace := candidate(t, "cancel", agentclient.HostCallbacks{Observe: func(_ context.Context, scope agentclient.Scope, _ acp.SessionNotification) error {
		if scope.NativeInvocationID != "" {
			once.Do(func() { close(entered); <-release })
		}
		return nil
	}})
	openOne(t, client, ctx, trace)
	results := make(chan agentclient.PromptResult, 1)
	run := reserve(t, client, ctx, agentclient.PromptOptions{})
	go func() { results <- run() }()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if err := client.Cancel(acp.CancelNotification{SessionId: "one"}); err != nil {
		t.Fatal(err)
	}
	cancelDeadline := time.Now().Add(time.Second)
	for {
		raw, _ := os.ReadFile(trace)
		if strings.Contains(string(raw), `"method":"session/cancel"`) {
			break
		}
		if time.Now().After(cancelDeadline) {
			t.Fatal("control blocked behind observer")
		}
		time.Sleep(5 * time.Millisecond)
	}
	select {
	case result := <-results:
		requireTerminal(t, result, acp.StopReasonCancelled)
		if result.Outcome.DeliveryError == nil {
			t.Fatal("blocked delivery silently succeeded")
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	requireTerminal(t, reserve(t, client, ctx, agentclient.PromptOptions{})(), acp.StopReasonEndTurn)
	raw, _ := os.ReadFile(trace)
	if strings.Count(string(raw), `"method":"session/cancel"`) != 1 {
		t.Fatalf("stale control reached successor: %s", raw)
	}
}

func TestAdmissionRejectsBeforeNativeWrite(t *testing.T) {
	client, ctx, trace := candidate(t, "", agentclient.HostCallbacks{})
	openOne(t, client, ctx, trace)
	rejected := errors.New("business submission rejected")
	var input agentclient.PromptSubmission
	result := reserve(t, client, ctx, agentclient.PromptOptions{BeforeSubmit: func(_ context.Context, submission agentclient.PromptSubmission) error {
		input = submission
		return rejected
	}})()
	if !errors.Is(result.Receipt.OperationError, rejected) || result.Receipt.Submission != agentclient.SubmissionNotSent ||
		result.Receipt.Admission != agentclient.AdmissionNotAccepted || result.Outcome != nil || input != result.Receipt.Input || input.InputID == "" {
		t.Fatalf("gate evidence = %#v, input %#v", result, input)
	}
	raw, _ := os.ReadFile(trace)
	if strings.Contains(string(raw), "session/prompt") {
		t.Fatalf("gate rejection sent native bytes: %s", raw)
	}
	requireTerminal(t, reserve(t, client, ctx, agentclient.PromptOptions{})(), acp.StopReasonEndTurn)
}

func TestObservationBudgetCannotBlockNativeResult(t *testing.T) {
	client, ctx, trace := candidate(t, "flood", agentclient.HostCallbacks{Observe: func(ctx context.Context, scope agentclient.Scope, _ acp.SessionNotification) error {
		if scope.NativeInvocationID != "" {
			<-ctx.Done()
			return ctx.Err()
		}
		return nil
	}})
	openOne(t, client, ctx, trace)
	result := reserve(t, client, ctx, agentclient.PromptOptions{})()
	requireTerminal(t, result, acp.StopReasonEndTurn)
	if result.Outcome.DeliveryError == nil {
		t.Fatal("lost observation budget error")
	}
}

func TestDisconnectDoesNotInventTerminal(t *testing.T) {
	client, ctx, trace := candidate(t, "disconnect", agentclient.HostCallbacks{})
	openOne(t, client, ctx, trace)
	result := reserve(t, client, ctx, agentclient.PromptOptions{})()
	o := result.Outcome
	if o == nil || o.Turn.Status != "" || o.StopReason != nil || o.OperationError == nil || o.ContextUsage.UsedTokens == nil {
		t.Fatalf("disconnect evidence = %#v / %#v", result.Receipt, o)
	}
}

func TestStandardACPPromptConsumptionRequiresAssociation(t *testing.T) {
	for _, tc := range []struct {
		scenario string
		consumed bool
	}{
		{scenario: "terminal-only", consumed: true},
		{scenario: "disconnect", consumed: true},
		{scenario: "reject-prompt"},
		{scenario: "unproven-prompt"},
	} {
		t.Run(tc.scenario, func(t *testing.T) {
			client, ctx, trace := candidate(t, tc.scenario, agentclient.HostCallbacks{})
			openOne(t, client, ctx, trace)
			var input agentclient.PromptSubmission
			result := reserve(t, client, ctx, agentclient.PromptOptions{BeforeSubmit: func(_ context.Context, submission agentclient.PromptSubmission) error {
				input = submission
				return nil
			}})()
			if input.InputID == "" || input.NativeInputID != nil || result.Receipt.Input != input {
				t.Fatalf("adapter input identity = %+v, receipt = %+v", input, result.Receipt)
			}
			if tc.consumed {
				if result.Outcome == nil || len(result.Outcome.Turn.ConsumedInputIDs) != 1 || result.Outcome.Turn.ConsumedInputIDs[0] != input.InputID {
					t.Fatalf("exact adapter consumption missing: %+v", result.Outcome)
				}
			} else if result.Outcome != nil && len(result.Outcome.Turn.ConsumedInputIDs) != 0 {
				t.Fatalf("unproven consumption: %+v", result.Outcome)
			}
		})
	}
}

func TestStandardACPExplicitRejectionOutweighsObservation(t *testing.T) {
	for _, code := range []int{-32700, -32600, -32601, -32602, -32000, -32002} {
		t.Run(strconv.Itoa(code), func(t *testing.T) {
			client, ctx, trace := candidate(t, "reject-after-output:"+strconv.Itoa(code), agentclient.HostCallbacks{})
			openOne(t, client, ctx, trace)
			for range 2 {
				result := reserve(t, client, ctx, agentclient.PromptOptions{})()
				var rejection *acp.RequestError
				if result.Receipt.Admission != agentclient.AdmissionNotAccepted || result.Receipt.Submission != agentclient.SubmissionSent ||
					!errors.As(result.Receipt.OperationError, &rejection) || rejection.Code != code {
					t.Fatalf("prompt rejection = %+v", result.Receipt)
				}
				outcome := result.Outcome
				if outcome == nil || !outcome.ObservedOutput || len(outcome.Turn.ConsumedInputIDs) != 0 ||
					outcome.Turn.Origin != acpmeta.NativeTurnOriginUnknown || outcome.Turn.Status != "" ||
					outcome.NativeFailure != nil || outcome.OperationError != nil {
					t.Fatalf("session observation was lost or falsely associated: %+v", outcome)
				}
			}
		})
	}
}

func TestOpeningObservationCanUseNativeSetter(t *testing.T) {
	var client *cursor.Client
	result := make(chan error, 1)
	var called sync.Once
	client, ctx, trace := candidate(t, "opening-post", agentclient.HostCallbacks{Observe: func(ctx context.Context, s agentclient.Scope, update acp.SessionNotification) error {
		called.Do(func() {
			options := update.Update.ConfigOptionUpdate.ConfigOptions
			fast := options[1].Boolean
			_, err := client.SetSessionConfigOption(ctx, acp.SetSessionConfigOptionRequest{Boolean: &acp.SetSessionConfigOptionBoolean{
				SessionId: s.SessionID, ConfigId: fast.Id, Value: !fast.CurrentValue,
			}})
			result <- err
		})
		return nil
	}})
	openOne(t, client, ctx, trace)
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}

func TestCursorQuestionUsesCapturedScopeAndNativeChoiceIDs(t *testing.T) {
	var scope agentclient.Scope
	client, ctx, trace := candidate(t, "interaction", agentclient.HostCallbacks{CreateElicitation: func(_ context.Context, s agentclient.Scope, r acp.UnstableCreateElicitationRequest) (acp.UnstableCreateElicitationResponse, error) {
		scope = s
		if r.Form == nil || r.Form.Meta["sessionId"] != "one" || r.Form.Meta["toolCallId"] != "question" {
			return acp.UnstableCreateElicitationResponse{}, errors.New("missing native form evidence")
		}
		return acp.UnstableCreateElicitationResponse{Accept: &acp.UnstableCreateElicitationAccept{Action: "accept", Content: map[string]any{"q": "choice"}}}, nil
	}})
	openOne(t, client, ctx, trace)
	result := reserve(t, client, ctx, agentclient.PromptOptions{HostInvocationID: "invocation"})()
	requireTerminal(t, result, acp.StopReasonEndTurn)
	if scope.SessionID != "one" || scope.HostInvocationID != "invocation" || scope.NativeInvocationID != result.Outcome.Turn.InvocationID {
		t.Fatalf("callback scope = %#v", scope)
	}
	raw, _ := os.ReadFile(trace)
	if !strings.Contains(string(raw), `"selectedOptionIds":["choice"]`) {
		t.Fatalf("native form result missing: %s", raw)
	}
}

func TestTerminalReceiveExcludesLaterNativeUsage(t *testing.T) {
	client, ctx, trace := candidate(t, "late", agentclient.HostCallbacks{})
	openOne(t, client, ctx, trace)
	result := reserve(t, client, ctx, agentclient.PromptOptions{})()
	requireTerminal(t, result, acp.StopReasonEndTurn)
	u := result.Outcome.ContextUsage
	if u.UsedTokens == nil || *u.UsedTokens != 9 || u.WindowTokens != nil {
		t.Fatalf("late update altered frozen outcome: %#v", u)
	}
}

func TestModelDirectoryKeepsFastSeparateAndUnknownDefault(t *testing.T) {
	client, ctx, _ := candidate(t, "", agentclient.HostCallbacks{})
	models, err := client.ReadModels(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if models.DefaultID != nil || models.Options.Ungrouped == nil || len(*models.Options.Ungrouped) != 1 {
		t.Fatalf("catalog = %#v", models)
	}
	settings, ok := acpmeta.ModelConfigOptions((*models.Options.Ungrouped)[0].Meta)
	if !ok || len(settings) != 1 || settings[0].Boolean == nil || settings[0].Boolean.Id != "native-fast" {
		t.Fatalf("model settings = %#v / %v", settings, ok)
	}
}

func TestCursorCatalogRejectsEmptyUnconfirmedDirectory(t *testing.T) {
	client, ctx, _ := candidate(t, "catalog-unconfirmed", agentclient.HostCallbacks{})
	if _, err := client.ReadModels(ctx); err == nil {
		t.Fatal("empty Cursor directory was accepted as fresh discovery evidence")
	}
}

func TestCursorCatalogAllowsNativeColdDiscovery(t *testing.T) {
	client, ctx, _ := candidate(t, "catalog-slow", agentclient.HostCallbacks{})
	models, err := client.ReadModels(ctx)
	if err != nil || models.Options.Ungrouped == nil || len(*models.Options.Ungrouped) != 1 || (*models.Options.Ungrouped)[0].Value != "model-a" {
		t.Fatalf("cold model discovery = %+v, %v", models, err)
	}
}

func TestCursorCatalogHonorsCallerDeadline(t *testing.T) {
	client, ctx, trace := candidate(t, "catalog-slow", agentclient.HostCallbacks{})
	short, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	if _, err := client.ReadModels(short); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("model discovery ignored caller deadline: %v", err)
	}
	data, err := os.ReadFile(trace)
	if err != nil || !strings.Contains(string(data), `"method":"cursor/list_available_models"`) {
		t.Fatalf("caller deadline did not expire during native discovery: %v", err)
	}
}

func TestCursorCatalogPreservesNativeEffortAndFastSelectValues(t *testing.T) {
	client, ctx, _ := candidate(t, "catalog-parameterized", agentclient.HostCallbacks{})
	models, err := client.ReadModels(ctx)
	if err != nil {
		t.Fatal(err)
	}
	settings, ok := acpmeta.ModelConfigOptions((*models.Options.Ungrouped)[0].Meta)
	if !ok || len(settings) != 2 || settings[0].Select == nil || settings[1].Select == nil {
		t.Fatalf("missing native select options: %#v", settings)
	}
	effort, fast := settings[0].Select, settings[1].Select
	if effort.Id != "effort" || effort.CurrentValue != "high" || effort.Category == nil || *effort.Category != "thought_level" {
		t.Fatalf("effort lost native identity: %+v", effort)
	}
	if fast.Id != "fast" || fast.CurrentValue != "false" || fast.Category == nil || *fast.Category != "model_config" ||
		fast.Options.Ungrouped == nil || len(*fast.Options.Ungrouped) != 2 || (*fast.Options.Ungrouped)[1].Value != "true" {
		t.Fatalf("fast lost native select values: %+v", fast)
	}
}

func TestPublicACPAdjacentPromptCancelCapturesBeforePreparation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 7*time.Second)
	defer cancel()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	trace := filepath.Join(dir, "wire")
	var native *cursor.Client
	var rejectObservation atomic.Bool
	projected, err := agentclient.NewACPAgent(func(callbacks agentclient.HostCallbacks) (agentclient.Client, error) {
		sendObservation := callbacks.Observe
		callbacks.Observe = func(ctx context.Context, scope agentclient.Scope, update acp.SessionNotification) error {
			if rejectObservation.Load() && scope.NativeInvocationID != "" {
				return errors.New("host output rejected")
			}
			return sendObservation(ctx, scope, update)
		}
		native, err = cursor.NewClient(ctx, cursor.Options{Executable: binary,
			PrefixArgs:  []string{"-test.run=^TestCursorFixture$", "--"},
			Environment: append(os.Environ(), "CURSOR_NEXT_FIXTURE=1", "CURSOR_NEXT_TRACE="+trace, "CURSOR_NEXT_SCENARIO=wire-reject-after-output"), WorkingDirectory: dir,
		}, callbacks)
		return native, err
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		if err := native.Close(cleanup); err != nil {
			t.Error(err)
		}
	}()
	in, send := io.Pipe()
	receive, out := io.Pipe()
	defer in.Close()
	defer send.Close()
	defer receive.Close()
	defer out.Close()
	var preparations int
	acp.NewAgentSideConnectionWithBinder(projected, out, in, func(peer *acp.AgentSideConnection) {
		projected.SetAgentConnection(peer)
		peer.SetPromptPreparation(func(acp.PromptRequest) acp.PromptPreparation {
			preparations++
			if preparations > 1 {
				return nil
			}
			return func(ctx context.Context) (func(), error) { <-ctx.Done(); return nil, ctx.Err() }
		})
	})
	reader := bufio.NewScanner(receive)
	read := func(id string) map[string]json.RawMessage {
		t.Helper()
		for reader.Scan() {
			var frame map[string]json.RawMessage
			if err := json.Unmarshal(reader.Bytes(), &frame); err != nil {
				t.Fatal(err)
			}
			if string(frame["id"]) == id {
				return frame
			}
		}
		t.Fatalf("no public ACP response %s: %v", id, reader.Err())
		return nil
	}
	stopWire := context.AfterFunc(ctx, func() { _ = in.Close(); _ = receive.Close() })
	defer stopWire()
	enc := json.NewEncoder(send)
	request := func(id int, method string, params any) map[string]json.RawMessage {
		t.Helper()
		if err := enc.Encode(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}); err != nil {
			t.Fatal(err)
		}
		return read(fmt.Sprint(id))
	}
	if f := request(1, "initialize", map[string]any{"protocolVersion": 1}); f["error"] != nil {
		t.Fatalf("initialize: %s", f["error"])
	}
	if f := request(2, "session/new", map[string]any{"cwd": dir, "mcpServers": []any{}}); f["error"] != nil {
		t.Fatalf("new: %s", f["error"])
	}
	if err := enc.Encode(map[string]any{"jsonrpc": "2.0", "id": 3, "method": "session/prompt", "params": promptRequest()}); err != nil {
		t.Fatal(err)
	}
	if err := enc.Encode(map[string]any{"jsonrpc": "2.0", "method": "session/cancel", "params": map[string]any{"sessionId": "one"}}); err != nil {
		t.Fatal(err)
	}
	if f := read("3"); f["error"] == nil {
		t.Fatalf("cancel did not settle first input: %s", f["result"])
	}
	raw, _ := os.ReadFile(trace)
	if strings.Contains(string(raw), "session/prompt") || strings.Contains(string(raw), "session/cancel") {
		t.Fatalf("preparation cancellation sent native bytes: %s", raw)
	}
	f := request(4, "session/prompt", promptRequest())
	var completed acp.PromptResponse
	if err := json.Unmarshal(f["result"], &completed); err != nil || completed.StopReason != acp.StopReasonEndTurn {
		t.Fatalf("successor: %s / %s (%v)", f["result"], f["error"], err)
	}
	assertWireInputConsumption(t, completed.Meta)
	usage, ok := acpmeta.ReadTurnUsage(completed.Meta)
	if !ok || usage.InputTokens == nil || *usage.InputTokens != 17 || usage.OutputTokens != nil {
		t.Fatalf("wire usage = %#v", usage)
	}

	rejectObservation.Store(true)
	f = request(5, "session/prompt", promptRequest())
	var rejection acp.RequestError
	if err := json.Unmarshal(f["error"], &rejection); err != nil || f["result"] != nil {
		t.Fatalf("wire failure = %s / %s", f["error"], f["result"])
	}
	data, _ := rejection.Data.(map[string]any)
	assertWireInputConsumption(t, data)
	usage, ok = acpmeta.ReadTurnUsage(data)
	turn, nativeTurn := acpmeta.ReadNativeTurn(data)
	if !ok || usage.InputTokens == nil || *usage.InputTokens != 17 || !nativeTurn || turn.Status != acpmeta.NativeTurnCompleted ||
		data["acp-go/delivery-error"] != true || data["nativeStopReason"] != string(acp.StopReasonEndTurn) {
		t.Fatalf("wire failure lost terminal evidence: %#v", data)
	}

	rejectObservation.Store(false)
	f = request(6, "session/prompt", promptRequest())
	if err := json.Unmarshal(f["error"], &rejection); err != nil || f["result"] != nil || rejection.Code != -32602 {
		t.Fatalf("wire explicit rejection = %s / %s (%v)", f["error"], f["result"], err)
	}
	data, _ = rejection.Data.(map[string]any)
	receipt, _ := data["acp-go/prompt-receipt"].(map[string]any)
	turn, nativeTurn = acpmeta.ReadNativeTurn(data)
	if !nativeTurn || turn.Status != "" || turn.Origin != acpmeta.NativeTurnOriginUnknown || len(turn.ConsumedInputIDs) != 0 ||
		data["observedOutput"] != true || receipt["admission"] != "not_accepted" || receipt["submission"] != "sent" {
		t.Fatalf("wire rejection facts = %#v", data)
	}
}

func assertWireInputConsumption(t *testing.T, meta map[string]any) {
	t.Helper()
	receipt, _ := meta["acp-go/prompt-receipt"].(map[string]any)
	id, _ := receipt["input_id"].(string)
	turn, ok := acpmeta.ReadNativeTurn(meta)
	if id == "" || !ok || len(turn.ConsumedInputIDs) != 1 || turn.ConsumedInputIDs[0] != id {
		t.Fatalf("wire input consumption = %+v, receipt = %+v", turn, receipt)
	}
}

func TestCumulativeUsageUsesFreshAndRestoredBaselines(t *testing.T) {
	client, ctx, trace := candidate(t, "", agentclient.HostCallbacks{})
	openOne(t, client, ctx, trace)
	requireTerminal(t, reserve(t, client, ctx, agentclient.PromptOptions{})(), acp.StopReasonEndTurn)
	requireTerminal(t, reserve(t, client, ctx, agentclient.PromptOptions{})(), acp.StopReasonEndTurn)
	if _, err := client.ResumeSession(ctx, acp.ResumeSessionRequest{SessionId: "one", Cwd: filepath.Dir(trace)}); err != nil {
		t.Fatal(err)
	}
	baseline := reserve(t, client, ctx, agentclient.PromptOptions{})()
	if baseline.Outcome == nil || baseline.Outcome.Turn.Usage == nil || baseline.Outcome.Turn.Usage.InputTokens != nil || baseline.Outcome.Turn.Usage.Complete {
		t.Fatalf("restore booked earlier cumulative usage: %#v", baseline.Outcome)
	}
	observed := baseline.Outcome.Turn.Usage.Observations
	if len(observed) != 1 || observed[0].Coverage != "session_cumulative" || observed[0].Tokens.InputTokens == nil || *observed[0].Tokens.InputTokens != 51 {
		t.Fatalf("lost unbilled cumulative evidence: %#v", observed)
	}
	requireTerminal(t, reserve(t, client, ctx, agentclient.PromptOptions{})(), acp.StopReasonEndTurn)
}

func TestAbsentCacheCannotProveUncachedInput(t *testing.T) {
	client, ctx, trace := candidate(t, "unknown-cache", agentclient.HostCallbacks{})
	openOne(t, client, ctx, trace)
	result := reserve(t, client, ctx, agentclient.PromptOptions{})()
	if result.Outcome == nil || result.Outcome.Turn.Usage == nil {
		t.Fatalf("lost partially known usage: %#v", result)
	}
	usage := result.Outcome.Turn.Usage
	if usage.InputTokens != nil || usage.CacheReadTokens == nil || *usage.CacheReadTokens != 0 || usage.CacheWriteTokens != nil {
		t.Fatalf("invented uncached input or cache write: %#v", usage)
	}
}

func TestProcessModelRestoresFrozenFastAfterNativeReset(t *testing.T) {
	client, ctx, trace := candidate(t, "process-model", agentclient.HostCallbacks{})
	openOne(t, client, ctx, trace)
	other := openOne(t, client, ctx, trace)
	if other.SessionId != "two" {
		t.Fatalf("second session = %#v", other)
	}
	requireTerminal(t, reserve(t, client, ctx, agentclient.PromptOptions{})(), acp.StopReasonEndTurn)
	requireTerminal(t, reserve(t, client, ctx, agentclient.PromptOptions{})(), acp.StopReasonEndTurn)
	raw, _ := os.ReadFile(trace)
	lines := strings.Split(string(raw), "\n")
	var settings []string
	for _, line := range lines {
		if strings.Contains(line, `"method":"session/set_config_option"`) {
			settings = append(settings, line)
		}
	}
	if len(settings) != 2 || !strings.Contains(settings[0], `"configId":"native-model"`) || !strings.Contains(settings[1], `"configId":"native-fast"`) || !strings.Contains(settings[1], `"value":true`) {
		t.Fatalf("frozen model and Fast writes = %v", settings)
	}
}

func TestCloseRetriesWithoutReleasingBlockedObserver(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	client, ctx, trace := candidate(t, "", agentclient.HostCallbacks{Observe: func(_ context.Context, scope agentclient.Scope, _ acp.SessionNotification) error {
		if scope.NativeInvocationID != "" {
			select {
			case <-entered:
			default:
				close(entered)
			}
			<-release
		}
		return nil
	}})
	var once sync.Once
	defer once.Do(func() { close(release) })
	openOne(t, client, ctx, trace)
	result := make(chan agentclient.PromptResult, 1)
	run := reserve(t, client, ctx, agentclient.PromptOptions{})
	go func() { result <- run() }()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	short, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if err := client.Close(short); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("first close = %v", err)
	}
	if _, err := client.ReservePrompt(ctx, promptRequest(), agentclient.PromptOptions{}); !errors.Is(err, cursor.ErrRestartRequired) {
		t.Fatalf("closed owner reused: %v", err)
	}
	once.Do(func() { close(release) })
	if err := client.Close(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-result:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}

func TestCursorStartupRejectsOverridesOfFrozenLaunch(t *testing.T) {
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		args []string
		env  []string
	}{
		{name: "key", args: []string{"--api-key", "fixture-secret"}},
		{name: "token", args: []string{"--auth-token=fixture-secret"}},
		{name: "environment token", env: []string{"CURSOR_AUTH_TOKEN=fixture-secret"}},
		{name: "force", args: []string{"-f"}},
		{name: "force before print", args: []string{"-fp"}},
		{name: "force after print", args: []string{"-pf"}},
		{name: "combined insecure", args: []string{"-pk"}},
		{name: "force long", args: []string{"--yolo"}},
		{name: "auto review", args: []string{"--auto-review"}},
		{name: "sandbox", args: []string{"--sandbox=disabled"}},
		{name: "MCP approval", args: []string{"--approve-mcps"}},
		{name: "workspace trust", args: []string{"--trust"}},
		{name: "attached endpoint", args: []string{"-ehttps://example.invalid"}},
		{name: "attached header", args: []string{"-HAuthorization: fixture-secret"}},
		{name: "data root", args: []string{"--data-dir", "another-account"}},
		{name: "workspace root", args: []string{"--workspace", "different-root"}},
		{name: "additional directory", args: []string{"--add-dir=different-root"}},
		{name: "worktree", args: []string{"--worktree=other"}},
		{name: "short worktree", args: []string{"-wother"}},
		{name: "combined worktree", args: []string{"-pwother"}},
		{name: "worktree base", args: []string{"--worktree-base", "main"}},
		{name: "plugin directory", args: []string{"--plugin-dir", "different-root"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, err := cursor.NewClient(t.Context(), cursor.Options{
				Executable: binary, PrefixArgs: tc.args, Environment: tc.env,
			}, agentclient.HostCallbacks{})
			if err == nil {
				_ = client.Close(t.Context())
				t.Fatal("unfrozen launch override was admitted")
			}
			if strings.Contains(err.Error(), "fixture-secret") {
				t.Fatal("startup error exposed credential input")
			}
		})
	}
}

func TestCursorACPAutoReviewIsUnavailable(t *testing.T) {
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	client, err := cursor.NewClient(t.Context(), cursor.Options{
		Executable: binary, PermissionMode: "auto",
	}, agentclient.HostCallbacks{})
	if err == nil {
		_ = client.Close(t.Context())
		t.Fatal("Cursor ACP accepted an unsupported auto-review policy")
	}
	if !errors.Is(err, cursor.ErrPermissionModeUnsupported) {
		t.Fatalf("unexpected auto-review policy error: %v", err)
	}
}

func TestReadAccountUsesNativeLoginWithoutACPOrDashboard(t *testing.T) {
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name       string
		key        string
		signedIn   bool
		signedOut  bool
		wantErr    bool
		wantStatus bool
	}{
		{name: "status-signed-in", signedIn: true, wantStatus: true},
		{name: "status-signed-out", signedOut: true, wantStatus: true},
		{name: "status-invalid", wantErr: true, wantStatus: true},
		{name: "status-api-key", key: "test-key"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			dir := t.TempDir()
			trace := filepath.Join(dir, "wire")
			client, err := cursor.NewClient(ctx, cursor.Options{
				Executable: binary, PrefixArgs: []string{"-test.run=^TestCursorFixture$", "--"},
				Environment:      append(os.Environ(), "CURSOR_NEXT_FIXTURE=1", "CURSOR_NEXT_SCENARIO="+tc.name, "CURSOR_NEXT_TRACE="+trace, "CURSOR_API_KEY="+tc.key),
				WorkingDirectory: dir,
			}, agentclient.HostCallbacks{})
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := client.Close(ctx); err != nil {
					t.Error(err)
				}
			}()
			account, err := client.ReadAccount(ctx)
			if (err != nil) != tc.wantErr || err != nil && strings.Contains(err.Error(), "fixture-secret") {
				t.Fatalf("unexpected native status error: %v", err)
			}
			if tc.signedIn && (account.Account == nil || account.Account.Kind != "default_login" ||
				account.Account.Label == nil || *account.Account.Label != "cursor@example.invalid" ||
				account.Account.Plan != nil || account.RequiresAuthentication) {
				t.Fatalf("signed in account = %+v", account)
			}
			if tc.signedOut && (!account.RequiresAuthentication || account.Account != nil) {
				t.Fatalf("signed out account = %+v", account)
			}
			if tc.key != "" {
				if account.Account == nil || account.Account.Kind != "api_key" || account.RequiresAuthentication ||
					account.Account.Label != nil || account.Account.Plan != nil {
					t.Fatalf("API key selection = %+v", account)
				}
				if _, err := client.ReadLoginStatus(ctx); err == nil {
					t.Fatal("API key selection read the ambient native login")
				}
			}
			wantCommands := "status\n"
			if tc.signedIn {
				status, err := client.ReadLoginStatus(ctx)
				if err != nil || !status.Authenticated || status.UserID != "2147483201" {
					t.Fatalf("native login identity = %+v, %v", status, err)
				}
				wantCommands += "status\n"
			}
			commands, traceErr := os.ReadFile(trace)
			if tc.wantStatus && (traceErr != nil || string(commands) != wantCommands) ||
				!tc.wantStatus && !errors.Is(traceErr, os.ErrNotExist) {
				t.Fatalf("unexpected commands %q: %v", commands, traceErr)
			}
		})
	}
}

func TestDashboardReadsOnlyBoundDefaultCredential(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Authorization") != "Bearer selected" || r.Method != http.MethodPost {
			t.Error("dashboard request did not use the selected credential")
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		switch filepath.Base(r.URL.Path) {
		case "GetMe":
			_, _ = io.WriteString(w, `{"userId":2147483201}`)
		case "GetCurrentPeriodUsage":
			_, _ = io.WriteString(w, `{"billingCycleStart":"1000","billingCycleEnd":"3000","planUsage":{"autoPercentUsed":11,"apiPercentUsed":22,"totalPercentUsed":0}}`)
		case "GetPlanInfo":
			_, _ = io.WriteString(w, `{"isMerged":true}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	selection := cursor.CredentialSelection{Source: cursor.DefaultLogin, ID: "bound"}
	dashboard := cursor.Dashboard{Endpoint: server.URL, ExpectedUserID: "2147483201", Credential: func(_ context.Context, binding cursor.CredentialSelection) (cursor.DashboardCredential, error) {
		if binding != selection {
			t.Fatalf("unexpected quota binding: %#v", binding)
		}
		return cursor.DashboardCredential{Selection: binding, AccessToken: "selected"}, nil
	}}
	client, ctx, trace := candidate(t, "", agentclient.HostCallbacks{}, func(o *cursor.Options) {
		o.Dashboard, o.Credential = &dashboard, selection
	})
	got, err := client.ReadAccountUsage(ctx)
	if err != nil || len(got.Buckets) != 1 || len(got.Buckets[0].Windows) != 1 {
		t.Fatalf("merged quota = %#v, %v", got, err)
	}
	window := got.Buckets[0].Windows[0]
	if window.UsedPercent == nil || *window.UsedPercent != 0 || window.ResetAtUnixSeconds == nil || *window.ResetAtUnixSeconds != 3 ||
		window.DurationSeconds == nil || *window.DurationSeconds != 2 || calls.Load() != 3 {
		t.Fatalf("quota window = %#v, calls %d", window, calls.Load())
	}
	api, apiCtx, _ := candidate(t, "", agentclient.HostCallbacks{}, func(o *cursor.Options) {
		o.Environment = append(o.Environment, "CURSOR_API_KEY=explicit-key")
		o.Dashboard, o.Credential = &dashboard, selection
	})
	if _, err := api.ReadAccountUsage(apiCtx); err == nil || calls.Load() != 3 {
		t.Fatalf("API-key selection used a default login: %v, calls %d", err, calls.Load())
	}

	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cold, err := cursor.NewClient(ctx, cursor.Options{Executable: executable, Environment: []string{}, Dashboard: &dashboard, Credential: selection}, agentclient.HostCallbacks{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := cold.Close(ctx); err != nil {
			t.Error(err)
		}
	}()
	if _, err := cold.ReadAccountUsage(ctx); err != nil {
		t.Fatalf("quota without ACP startup: %v", err)
	}
	openOne(t, client, ctx, trace)
	run := reserve(t, client, ctx, agentclient.PromptOptions{})
	if _, err := client.ReadAccountUsage(ctx); err != nil {
		t.Errorf("quota while a prompt owns ACP: %v", err)
	}
	requireTerminal(t, run(), acp.StopReasonEndTurn)
}

func TestDashboardDefaultOriginIgnoresCLIEnvironment(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dialed := make(chan string, 1)
	transport := &http.Transport{DialContext: func(_ context.Context, _, address string) (net.Conn, error) {
		dialed <- address
		return nil, errors.New("test records the origin without network access")
	}}
	defer transport.CloseIdleConnections()
	selection := cursor.CredentialSelection{Source: cursor.DefaultLogin, ID: "bound"}
	client, err := cursor.NewClient(ctx, cursor.Options{
		Executable: executable, Environment: []string{"CURSOR_API_ENDPOINT=http://127.0.0.1:1"}, Credential: selection,
		Dashboard: &cursor.Dashboard{
			ExpectedUserID: "2147483201",
			HTTPClient:     &http.Client{Transport: transport},
			Credential: func(context.Context, cursor.CredentialSelection) (cursor.DashboardCredential, error) {
				return cursor.DashboardCredential{Selection: selection, AccessToken: "synthetic"}, nil
			},
		},
	}, agentclient.HostCallbacks{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close(ctx) }()
	_, _ = client.ReadAccountUsage(ctx)
	select {
	case got := <-dialed:
		if got != "api2.cursor.sh:443" {
			t.Fatalf("Dashboard destination = %q", got)
		}
	default:
		t.Fatal("Dashboard did not resolve its destination")
	}
}

func TestDashboardCloseWaitsForAdmittedCredentialRead(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	selection := cursor.CredentialSelection{Source: cursor.DefaultLogin, ID: "bound"}
	entered, cancelled, finish := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var release sync.Once
	var calls atomic.Int32
	client, err := cursor.NewClient(ctx, cursor.Options{
		Executable: executable, Environment: []string{}, Credential: selection,
		Dashboard: &cursor.Dashboard{
			ExpectedUserID: "2147483201",
			Endpoint:       "http://127.0.0.1:1",
			Credential: func(ctx context.Context, _ cursor.CredentialSelection) (cursor.DashboardCredential, error) {
				if calls.Add(1) == 1 {
					close(entered)
					<-ctx.Done()
					close(cancelled)
					<-finish
				}
				return cursor.DashboardCredential{}, ctx.Err()
			},
		},
	}, agentclient.HostCallbacks{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		release.Do(func() { close(finish) })
		_ = client.Close(ctx)
	}()
	read := make(chan error, 1)
	go func() {
		_, err := client.ReadAccountUsage(ctx)
		read <- err
	}()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("quota credential read did not start")
	}
	closeCtx, stop := context.WithTimeout(ctx, 100*time.Millisecond)
	defer stop()
	if err := client.Close(closeCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("close with live credential consumer = %v", err)
	}
	select {
	case <-cancelled:
	case <-ctx.Done():
		t.Fatal("close did not cancel the credential read")
	}
	if _, err := client.ReadAccountUsage(ctx); !errors.Is(err, cursor.ErrRestartRequired) || calls.Load() != 1 {
		t.Fatalf("closing owner admitted another credential read: %v, %d calls", err, calls.Load())
	}
	release.Do(func() { close(finish) })
	if err := client.Close(ctx); err != nil {
		t.Fatalf("retry after credential consumer exited: %v", err)
	}
	select {
	case err := <-read:
		if !errors.Is(err, cursor.ErrRestartRequired) {
			t.Fatalf("interrupted quota read = %v", err)
		}
	case <-ctx.Done():
		t.Fatal("closed owner did not return the admitted read")
	}
}

func TestNativeForkPublishesOnlyChildConfigurationAndReceipt(t *testing.T) {
	client, ctx, trace := candidate(t, "fork", agentclient.HostCallbacks{})
	openOne(t, client, ctx, trace)
	receipt := filepath.Join(t.TempDir(), "operation")
	request := acp.UnstableForkSessionRequest{SessionId: "one", Cwd: filepath.Dir(trace), Meta: map[string]any{"forkReceiptDirectory": receipt}}
	forked, err := client.UnstableForkSession(ctx, request)
	if err != nil || forked.SessionId != "forked" || len(forked.ConfigOptions) != 2 || forked.ConfigOptions[1].Boolean.Id != "child-fast" {
		t.Fatalf("fork binding = %#v, %v", forked, err)
	}
	_, err = client.SetSessionConfigOption(ctx, acp.SetSessionConfigOptionRequest{Boolean: &acp.SetSessionConfigOptionBoolean{
		SessionId: forked.SessionId, ConfigId: "child-fast", Value: true,
	}})
	if err != nil {
		t.Fatal(err)
	}
	request.Meta["reconcileOnly"] = true
	reconciled, err := client.UnstableForkSession(ctx, request)
	if err != nil || reconciled.SessionId != forked.SessionId {
		t.Fatalf("reconciled = %#v, %v", reconciled, err)
	}
	raw, _ := os.ReadFile(trace)
	if strings.Count(string(raw), `"method":"session/fork"`) != 1 || strings.Contains(string(raw), "forkReceiptDirectory") {
		t.Fatalf("duplicate native fork or leaked host receipt path: %s", raw)
	}
}

func TestNativeLoginRequiresNegotiatedDefaultLogin(t *testing.T) {
	client, ctx, trace := candidate(t, "", agentclient.HostCallbacks{})
	if _, err := client.Authenticate(ctx, acp.AuthenticateRequest{MethodId: "foreign"}); err == nil {
		t.Fatal("unadvertised login was accepted")
	}
	if _, err := client.Authenticate(ctx, acp.AuthenticateRequest{MethodId: "cursor_login"}); err != nil {
		t.Fatal(err)
	}
	wire, _ := os.ReadFile(trace)
	if strings.Count(string(wire), `"method":"authenticate"`) != 1 || !strings.Contains(string(wire), `"methodId":"cursor_login"`) {
		t.Fatalf("native authentication = %s", wire)
	}
}

func TestDeveloperInstructionsUseOnlyTheNativePromptPrefix(t *testing.T) {
	client, ctx, trace := candidate(t, "", agentclient.HostCallbacks{})
	const instructions = "bound profile instruction"
	if _, err := client.NewSession(ctx, acp.NewSessionRequest{Cwd: filepath.Dir(trace), Meta: acpmeta.DeveloperInstructionsMetadata(acp.Ptr(instructions))}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		requireTerminal(t, reserve(t, client, ctx, agentclient.PromptOptions{})(), acp.StopReasonEndTurn)
	}
	if _, err := client.ResumeSession(ctx, acp.ResumeSessionRequest{SessionId: "one", Cwd: filepath.Dir(trace)}); err != nil {
		t.Fatal(err)
	}
	result := reserve(t, client, ctx, agentclient.PromptOptions{})()
	if result.Outcome == nil || result.Outcome.StopReason == nil || *result.Outcome.StopReason != acp.StopReasonEndTurn {
		t.Fatalf("restore prompt = %#v", result)
	}
	wire, _ := os.ReadFile(trace)
	if strings.Count(string(wire), instructions) != 2 || strings.Contains(string(wire), "developerInstructions") {
		t.Fatalf("instructions were duplicated or sent as unnegotiated native metadata: %s", wire)
	}
}

func TestForkReceiptFailureDoesNotPublishChildAdmission(t *testing.T) {
	client, ctx, trace := candidate(t, "fork", agentclient.HostCallbacks{})
	openOne(t, client, ctx, trace)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "receipt.json"), []byte("invalid existing receipt"), 0600); err != nil {
		t.Fatal(err)
	}
	result, err := client.UnstableForkSession(ctx, acp.UnstableForkSessionRequest{SessionId: "one", Cwd: filepath.Dir(trace), Meta: map[string]any{"forkReceiptDirectory": dir}})
	if err == nil || result.SessionId != "forked" || len(result.ConfigOptions) != 0 {
		t.Fatalf("lost native child evidence: %#v, %v", result, err)
	}
	if run, err := client.ReservePrompt(ctx, acp.PromptRequest{SessionId: result.SessionId, Prompt: []acp.ContentBlock{acp.TextBlock("must not run")}}, agentclient.PromptOptions{}); err == nil || run != nil {
		t.Fatal("uncommitted native child became promptable")
	}
	if _, err := client.CloseSession(ctx, acp.CloseSessionRequest{SessionId: result.SessionId}); err != nil {
		t.Fatal(err)
	}
}

func TestFamilyReviewUsageDipCannotLowerHighWater(t *testing.T) {
	client, ctx, trace := candidate(t, "family-usage-dip", agentclient.HostCallbacks{})
	openOne(t, client, ctx, trace)
	for i, want := range []struct{ input, output, observed int64 }{
		{100, 50, 100}, {-1, -1, 80}, {-1, -1, 110}, {6, 3, 116},
		{-1, -1, 94}, {-1, -1, 98}, {-1, -1, 120}, {6, 3, 126},
	} {
		if i == 4 {
			if _, err := client.ResumeSession(ctx, acp.ResumeSessionRequest{SessionId: "one", Cwd: filepath.Dir(trace)}); err != nil {
				t.Fatal(err)
			}
		}
		result := reserve(t, client, ctx, agentclient.PromptOptions{})()
		requireStopReason(t, result, acp.StopReasonEndTurn)
		u := result.Outcome.Turn.Usage
		if u == nil || u.Complete != (want.input >= 0) || len(u.Observations) != 1 || u.Observations[0].Coverage != "session_cumulative" {
			t.Fatalf("turn %d: usage = %+v", i+1, u)
		}
		checkTokenCount(t, u.InputTokens, want.input)
		checkTokenCount(t, u.OutputTokens, want.output)
		checkTokenCount(t, u.Observations[0].Tokens.InputTokens, want.observed)
	}
}

func TestFamilyReviewUsageGapsRequireNewAdjacentBaseline(t *testing.T) {
	client, ctx, trace := candidate(t, "family-usage-gap", agentclient.HostCallbacks{})
	openOne(t, client, ctx, trace)
	for i, want := range []struct{ input, output, read, write int64 }{
		{97, 50, 2, 1}, {-1, 5, 1, -1}, {-1, 5, 1, -1},
		{-1, -1, -1, -1}, {-1, -1, -1, -1}, {8, 5, 1, 1},
	} {
		result := reserve(t, client, ctx, agentclient.PromptOptions{})()
		requireStopReason(t, result, acp.StopReasonEndTurn)
		u := result.Outcome.Turn.Usage
		if i == 3 {
			if u != nil {
				t.Fatalf("absent native sample produced usage: %+v", u)
			}
			continue
		}
		if u == nil || u.Complete != (want.input >= 0) {
			t.Fatalf("turn %d: usage = %+v", i+1, u)
		}
		checkTokenCount(t, u.InputTokens, want.input)
		checkTokenCount(t, u.OutputTokens, want.output)
		checkTokenCount(t, u.CacheReadTokens, want.read)
		checkTokenCount(t, u.CacheWriteTokens, want.write)
	}
}

func checkTokenCount(t *testing.T, got *int64, want int64) {
	t.Helper()
	if want < 0 && got == nil || want >= 0 && got != nil && *got == want {
		return
	}
	if got == nil {
		t.Fatalf("token count unknown, want %d", want)
	}
	t.Fatalf("token count %d, want %d (-1 is unknown)", *got, want)
}

func TestFamilyReviewExplicitUsageSurvivesRestoredCumulativeBaseline(t *testing.T) {
	client, ctx, trace := candidate(t, "family-scoped-usage", agentclient.HostCallbacks{})
	openOne(t, client, ctx, trace)
	if _, err := client.ResumeSession(ctx, acp.ResumeSessionRequest{SessionId: "one", Cwd: filepath.Dir(trace)}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []struct {
		source                  string
		input, output, observed int64
	}{
		{"fixture.invocation", 7, 4, 100},
		{"fixture.final", 8, 5, 111},
		{"acp/prompt", 11, 6, 122},
	} {
		result := reserve(t, client, ctx, agentclient.PromptOptions{})()
		requireStopReason(t, result, acp.StopReasonEndTurn)
		u := result.Outcome.Turn.Usage
		if u == nil || u.Source != want.source || u.Coverage != "invocation" || !u.Complete || len(u.Observations) != 1 || u.Observations[0].Coverage != "session_cumulative" {
			t.Fatalf("usage for %s: %+v", want.source, u)
		}
		checkTokenCount(t, u.InputTokens, want.input)
		checkTokenCount(t, u.OutputTokens, want.output)
		checkTokenCount(t, u.Observations[0].Tokens.InputTokens, want.observed)
	}
}

func TestFamilyReviewFailedModelSetterInvalidatesProcessEvidence(t *testing.T) {
	c, ctx, trace := candidate(t, "family-failed-model", agentclient.HostCallbacks{})
	openOne(t, c, ctx, trace)
	b := openOne(t, c, ctx, trace)
	first := reserve(t, c, ctx, agentclient.PromptOptions{})()
	if first.Outcome == nil || first.Outcome.Turn.Usage == nil || first.Outcome.Turn.Usage.Model != "model-a" {
		t.Fatalf("could not establish model-a: %+v", first)
	}
	_, err := c.SetSessionConfigOption(ctx, acp.SetSessionConfigOptionRequest{ValueId: &acp.SetSessionConfigOptionValueId{
		SessionId: b.SessionId, ConfigId: "native-model", Value: "model-b",
	}})
	if err == nil {
		t.Fatal("fixture did not reject model-b confirmation")
	}
	second := reserve(t, c, ctx, agentclient.PromptOptions{})()
	requireStopReason(t, second, acp.StopReasonEndTurn)
	if second.Outcome.Turn.Usage == nil || second.Outcome.Turn.Usage.Model != "model-a" {
		t.Fatalf("prompt intended model-a but native actually used %v", second.Outcome.Turn.Usage)
	}
	wire, err := os.ReadFile(trace)
	if err != nil {
		t.Fatal(err)
	}
	var sequence []string
	for _, line := range strings.Split(string(wire), "\n") {
		if strings.Contains(line, `"configId":"native-model"`) {
			if strings.Contains(line, `"value":"model-a"`) {
				sequence = append(sequence, "model-a")
			} else if strings.Contains(line, `"value":"model-b"`) {
				sequence = append(sequence, "model-b")
			}
		}
		if strings.Contains(line, `"method":"session/prompt"`) {
			sequence = append(sequence, "prompt")
		}
	}
	if strings.Join(sequence, ",") != "model-a,prompt,model-b,model-a,prompt" {
		t.Fatalf("model write order: %v", sequence)
	}
}

func TestFamilyClosureCumulativeNotificationCannotBecomeInvocation(t *testing.T) {
	c, ctx, trace := candidate(t, "family-closure-cumulative-notification", agentclient.HostCallbacks{})
	openOne(t, c, ctx, trace)
	requireStopReason(t, reserve(t, c, ctx, agentclient.PromptOptions{})(), acp.StopReasonEndTurn)
	out := reserve(t, c, ctx, agentclient.PromptOptions{})()
	requireStopReason(t, out, acp.StopReasonEndTurn)
	u := out.Outcome.Turn.Usage
	if u == nil || u.InputTokens == nil || u.OutputTokens == nil {
		t.Fatalf("missing independently attributable invocation: %+v", u)
	}
	t.Logf("primary source=%s scope=%s input=%d output=%d", u.Source, u.Coverage, *u.InputTokens, *u.OutputTokens)
	if u.Coverage != "invocation" || *u.InputTokens != 10 || *u.OutputTokens != 5 {
		t.Fatalf("cumulative notification became the second canonical invoice: %+v", u)
	}
	if len(u.Observations) != 2 {
		t.Fatalf("lost distinct cumulative observations: %+v", u.Observations)
	}
	for _, observed := range u.Observations {
		if observed.Coverage != "session_cumulative" {
			t.Fatalf("unexpected cumulative scope: %+v", observed)
		}
		checkTokenCount(t, observed.Tokens.InputTokens, 110)
		checkTokenCount(t, observed.Tokens.OutputTokens, 55)
	}
}

func TestFamilyClosureUnattributedUsageRemainsObservationWithoutStandardUsage(t *testing.T) {
	live := make(chan *acpmeta.TurnUsage, 16)
	client, ctx, trace := candidate(t, "family-closure-no-standard", agentclient.HostCallbacks{
		Observe: func(_ context.Context, _ agentclient.Scope, note acp.SessionNotification) error {
			update := note.Update.UsageUpdate
			if update != nil && update.Size == 100 {
				if usage, ok := acpmeta.ReadTurnUsage(update.Meta); ok {
					live <- &usage
				} else if update.Used == 9 && update.Meta["fixture.retained"] == true {
					live <- nil
				}
			}
			return nil
		},
	})
	openOne(t, client, ctx, trace)
	for i := range 2 {
		result := reserve(t, client, ctx, agentclient.PromptOptions{})()
		requireStopReason(t, result, acp.StopReasonEndTurn)
		u := result.Outcome.Turn.Usage
		if u == nil || u.Coverage != "invocation" || !u.ContextKnown || len(u.Observations) != 4 {
			t.Fatalf("turn %d: usage = %+v", i+1, u)
		}
		if i == 0 {
			checkTokenCount(t, u.InputTokens, -1)
			checkTokenCount(t, u.OutputTokens, -1)
			if u.Complete {
				t.Fatalf("observation-only report became complete: %+v", u)
			}
		} else {
			checkTokenCount(t, u.InputTokens, 8)
			checkTokenCount(t, u.OutputTokens, 4)
		}
		want := map[string]int64{"future_scope": 80, "unknown": 40, "last_request": 20, "context": 60}
		for _, obs := range u.Observations {
			count, ok := want[obs.Coverage]
			if !ok {
				t.Fatalf("unexpected duplicate or scope: %+v", obs)
			}
			delete(want, obs.Coverage)
			checkTokenCount(t, obs.Tokens.InputTokens, count)
		}
	}
	close(live)
	var contextOnly, scoped int
	for u := range live {
		if u == nil {
			contextOnly++
			continue
		}
		scoped++
		if u.Source != "fixture.invocation" || !u.ContextKnown {
			t.Fatalf("unexpected invocation projection: %+v", u)
		}
		checkTokenCount(t, u.InputTokens, 8)
	}
	if contextOnly != 8 || scoped != 1 {
		t.Fatalf("projected context-only=%d invocation=%d, want 8/1", contextOnly, scoped)
	}
}

func TestFamilyClosureKnownInvocationScopesArePreserved(t *testing.T) {
	for _, scope := range []string{"invocation", "native_turn", "main_loop", "observed_turn", "observed_requests", "query_pipeline"} {
		t.Run(scope, func(t *testing.T) {
			client, ctx, trace := candidate(t, "family-closure-known-"+scope, agentclient.HostCallbacks{})
			openOne(t, client, ctx, trace)
			result := reserve(t, client, ctx, agentclient.PromptOptions{})()
			requireStopReason(t, result, acp.StopReasonEndTurn)
			u := result.Outcome.Turn.Usage
			if u == nil || u.Source != "fixture.known" || u.Coverage != scope {
				t.Fatalf("known invocation scope lost: %+v", u)
			}
			checkTokenCount(t, u.InputTokens, 8)
			checkTokenCount(t, u.OutputTokens, 5)
		})
	}
}

func TestDashboardRejectsCredentialFromAnotherNativeAccount(t *testing.T) {
	for _, body := range []string{"{\"userId\":78}", "{\"userId\":0}", "{}"} {
		t.Run(body, func(t *testing.T) {
			var identityReads, quotaReads atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch filepath.Base(r.URL.Path) {
				case "GetMe":
					identityReads.Add(1)
					if r.Header.Get("Authorization") != "Bearer unbound-token" {
						t.Error("identity probe used a different token")
					}
					_, _ = io.WriteString(w, body)
				default:
					quotaReads.Add(1)
					_, _ = io.WriteString(w, "{\"planUsage\":{\"totalPercentUsed\":42}}")
				}
			}))
			defer server.Close()
			binary, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			selection := cursor.CredentialSelection{Source: cursor.DefaultLogin, ID: "host-binding"}
			client, err := cursor.NewClient(t.Context(), cursor.Options{
				Executable: binary, Environment: []string{}, Credential: selection,
				Dashboard: &cursor.Dashboard{
					Endpoint: server.URL, ExpectedUserID: "77",
					Credential: func(context.Context, cursor.CredentialSelection) (cursor.DashboardCredential, error) {
						return cursor.DashboardCredential{Selection: selection, AccessToken: "unbound-token"}, nil
					},
				},
			}, agentclient.HostCallbacks{})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = client.Close(t.Context()) }()
			usage, err := client.ReadAccountUsage(t.Context())
			if !errors.Is(err, cursor.ErrQuotaCredential) || len(usage.Buckets) != 0 || identityReads.Load() != 1 || quotaReads.Load() != 0 {
				t.Fatalf("unproven token read quota: error=%v, identity calls=%d, quota calls=%d", err, identityReads.Load(), quotaReads.Load())
			}
		})
	}
}

package claude_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/JieWaZi/acp-go/pkg/acpmeta"
	agentclient "github.com/JieWaZi/acp-go/pkg/agent_next"
	"github.com/JieWaZi/acp-go/pkg/agent_next/claude"
	acp "github.com/JieWaZi/acp-go/sdk"
)

type wireHost struct {
	acp.Client
	observe func(acp.SessionNotification)
	approve func(acp.RequestPermissionRequest)
}

func (h wireHost) SessionUpdate(_ context.Context, n acp.SessionNotification) error {
	if h.observe != nil {
		h.observe(n)
	}
	return nil
}

func (h wireHost) RequestPermission(_ context.Context, req acp.RequestPermissionRequest) (acp.RequestPermissionResponse, error) {
	if h.approve != nil {
		h.approve(req)
	}
	var response acp.RequestPermissionResponse
	_ = json.Unmarshal([]byte(`{"outcome":{"outcome":"selected","optionId":"allow_once"}}`), &response)
	return response, nil
}

func startACP(t *testing.T, mode string, observe func(acp.SessionNotification), prepare func(acp.PromptRequest) acp.PromptPreparation, reviewApprove ...func(acp.RequestPermissionRequest)) (*acp.ClientSideConnection, acp.SessionId, string, context.Context) {
	t.Helper()
	dir := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	agent, err := agentclient.NewACPAgent(func(host agentclient.HostCallbacks) (agentclient.Client, error) {
		return claude.NewClient(ctx, claude.Options{Executable: os.Args[0], PrefixArgs: []string{"-test.run=^TestClaudeFixture$", "--"},
			Environment: fixtureEnvironment(dir, mode), WorkingDirectory: dir, ControlTimeout: 1500 * time.Millisecond}, host)
	})
	if err != nil {
		t.Fatal(err)
	}
	toAgent, fromClient := io.Pipe()
	toClient, fromAgent := io.Pipe()
	host := wireHost{observe: observe}
	if len(reviewApprove) > 0 {
		host.approve = reviewApprove[0]
	}
	peer := acp.NewClientSideConnection(host, fromClient, toClient)
	_ = acp.NewAgentSideConnectionWithBinder(agent, fromAgent, toAgent, func(conn *acp.AgentSideConnection) {
		agent.SetAgentConnection(conn)
		if prepare != nil {
			conn.SetPromptPreparation(prepare)
		}
	})
	t.Cleanup(func() {
		_ = fromClient.Close()
		_ = fromAgent.Close()
		_ = toClient.Close()
		_ = toAgent.Close()
		if err := agent.Client.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	if _, err := peer.Initialize(ctx, acp.InitializeRequest{ProtocolVersion: 1, ClientCapabilities: acp.ClientCapabilities{Meta: map[string]any{
		acpmeta.NativeTurnReceiptsCapability: true,
	}}}); err != nil {
		t.Fatal(err)
	}
	opened, err := peer.NewSession(ctx, acp.NewSessionRequest{Cwd: dir, McpServers: []acp.McpServer{}})
	if err != nil {
		t.Fatal(err)
	}
	return peer, opened.SessionId, dir, ctx
}

func TestACPWirePreparationSteeringAndCancel(t *testing.T) {
	entered := make(chan struct{})
	cleaned := make(chan struct{})
	peer, id, dir, ctx := startACP(t, "", nil, func(acp.PromptRequest) acp.PromptPreparation {
		return func(owner context.Context) (func(), error) {
			close(entered)
			<-owner.Done()
			return func() { close(cleaned) }, owner.Err()
		}
	})
	done := make(chan error, 1)
	go func() {
		_, err := peer.Prompt(ctx, acp.PromptRequest{SessionId: id, Prompt: []acp.ContentBlock{acp.TextBlock("preparing")}})
		done <- err
	}()
	receive(t, ctx, entered)
	raw, err := peer.CallExtension(ctx, "_session/steering", map[string]any{
		"sessionId": id, "prompt": []acp.ContentBlock{acp.TextBlock("early")},
		"_meta": map[string]any{"steering": map[string]any{"idleBehavior": "promptRequired"}},
	})
	var reply agentclient.SteerResponse
	if err != nil || json.Unmarshal(raw, &reply) != nil || reply.Outcome != "promptRequired" {
		t.Errorf("steer reply = %s, %v", raw, err)
	}
	if err := peer.Cancel(ctx, acp.CancelNotification{SessionId: id}); err != nil {
		t.Fatal(err)
	}
	err = receive(t, ctx, done)
	var rpc *acp.RequestError
	if !errors.As(err, &rpc) || rpc.Code != acp.NewRequestCancelled(nil).Code {
		t.Errorf("prompt = %v", err)
	}
	receive(t, ctx, cleaned)
	if _, err := os.Stat(filepath.Join(dir, "input")); !os.IsNotExist(err) {
		t.Fatalf("early native submission: %v", err)
	}
}

func TestACPWireCapturedCancelDoesNotInterruptSuccessor(t *testing.T) {
	text := make(chan string, 16)
	peer, id, dir, ctx := startACP(t, "stale", func(n acp.SessionNotification) {
		if c := n.Update.AgentMessageChunk; c != nil && c.Content.Text != nil {
			text <- c.Content.Text.Text
		}
	}, nil)
	done := make(chan error, 1)
	go func() {
		_, err := peer.Prompt(ctx, acp.PromptRequest{SessionId: id, Prompt: []acp.ContentBlock{acp.TextBlock("hold A")}})
		done <- err
	}()
	if value := receive(t, ctx, text); value != "foreground A" {
		t.Fatal(value)
	}
	steered := make(chan error, 1)
	go func() {
		_, err := peer.CallExtension(ctx, "_session/steering", map[string]any{
			"sessionId": id, "prompt": []acp.ContentBlock{acp.TextBlock(strings.Repeat("s", 2<<20))},
		})
		steered <- err
	}()
	marker(t, ctx, filepath.Join(dir, "write-blocked"))
	if err := peer.Cancel(ctx, acp.CancelNotification{SessionId: id}); err != nil {
		t.Fatal(err)
	}
	// A later wire request proves the SDK reader ran the earlier cancel
	// admission, independently of the queued native write.
	if _, err := peer.CallExtension(ctx, acpmeta.NativeTurnReceiptsMethod, acpmeta.NativeTurnReceiptsRequest{SessionID: string(id)}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "finish-old"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := receive(t, ctx, done); err != nil {
		t.Fatal(err)
	}
	if value := receive(t, ctx, text); value != "provider successor" {
		t.Fatal(value)
	}
	if err := os.WriteFile(filepath.Join(dir, "resume-stdin"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := receive(t, ctx, steered); err != nil {
		t.Fatal(err)
	}
	marker(t, ctx, filepath.Join(dir, "recall"))
	time.Sleep(100 * time.Millisecond)
	if _, err := os.Stat(filepath.Join(dir, "interrupt")); !os.IsNotExist(err) {
		t.Fatalf("stale wire interrupt reached successor: %v", err)
	}
}

func TestACPWireFailureRetainsNumberedResultUsage(t *testing.T) {
	peer, id, _, ctx := startACP(t, "numbered-failure", nil, nil)
	_, err := peer.Prompt(ctx, acp.PromptRequest{SessionId: id, Prompt: []acp.ContentBlock{acp.TextBlock("fail natively")}})
	var rpc *acp.RequestError
	if !errors.As(err, &rpc) {
		t.Fatalf("prompt failure = %v", err)
	}
	meta, ok := rpc.Data.(map[string]any)
	if !ok {
		t.Fatalf("error data = %+v", rpc.Data)
	}
	turn, ok := acpmeta.ReadNativeTurn(meta)
	if !ok || turn.Status != acpmeta.NativeTurnFailed || turn.Usage == nil || turn.Usage.InputTokens == nil || *turn.Usage.InputTokens != 43 ||
		len(turn.ConsumedInputIDs) != 1 {
		t.Fatalf("native failure = %+v", turn)
	}
	raw, err := peer.CallExtension(ctx, acpmeta.NativeTurnReceiptsMethod, acpmeta.NativeTurnReceiptsRequest{SessionID: string(id)})
	var page acpmeta.NativeTurnReceiptsResponse
	if err != nil || json.Unmarshal(raw, &page) != nil || len(page.Receipts) != 1 ||
		page.Receipts[0].Turn.InvocationID != turn.InvocationID || page.Receipts[0].Failure == nil {
		t.Fatalf("receipt = %s, %v", raw, err)
	}
}

func TestReviewACPBackgroundPermissionUsesSuccessor(t *testing.T) {
	calls := make(chan acp.RequestPermissionRequest, 2)
	peer, id, dir, ctx := startACP(t, "review-stale-permission", nil, nil, func(r acp.RequestPermissionRequest) { calls <- r })
	req := acp.PromptRequest{SessionId: id, Prompt: []acp.ContentBlock{acp.TextBlock("first")}}
	if _, err := peer.Prompt(ctx, req); err != nil {
		t.Fatal(err)
	}
	req.Prompt = []acp.ContentBlock{acp.TextBlock("second")}
	if _, err := peer.Prompt(ctx, req); err != nil {
		t.Fatal(err)
	}
	answer, err := os.ReadFile(filepath.Join(dir, "interaction-background-A-tool"))
	if err != nil {
		t.Fatal(err)
	}
	select {
	case call := <-calls:
		t.Fatalf("background A reached successor permission port for %s, native control response: %s", call.ToolCall.ToolCallId, answer)
	default:
	}
	if strings.Contains(string(answer), "behavior:allow") {
		t.Fatalf("unowned tool allowed: %s", answer)
	}
}

func TestReviewACPNumberedAnonymousSuccessorDisappears(t *testing.T) {
	peer, id, _, ctx := startACP(t, "review-gap-anonymous", nil, nil)
	req := acp.PromptRequest{SessionId: id, Prompt: []acp.ContentBlock{acp.TextBlock("first")}}
	if _, err := peer.Prompt(ctx, req); err == nil {
		t.Fatal("expected loss of prior terminal")
	}
	raw, err := peer.CallExtension(ctx, acpmeta.NativeTurnReceiptsMethod, acpmeta.NativeTurnReceiptsRequest{SessionID: string(id)})
	var page acpmeta.NativeTurnReceiptsResponse
	if err != nil || json.Unmarshal(raw, &page) != nil || len(page.Receipts) != 2 {
		t.Fatalf("numbered successor absent from receipt page: %s err=%v", raw, err)
	}
	old, successor := page.Receipts[0], page.Receipts[1]
	if old.Turn.Status != "" || old.Turn.Usage == nil || old.Turn.Usage.InputTokens == nil || *old.Turn.Usage.InputTokens != 43 ||
		successor.Turn.Origin != acpmeta.NativeTurnOriginUnknown || successor.Turn.Status != acpmeta.NativeTurnCompleted ||
		len(successor.Turn.ConsumedInputIDs) != 0 || successor.Turn.Usage == nil || successor.Turn.Usage.InputTokens == nil ||
		*successor.Turn.Usage.InputTokens != 17 || successor.ResultText != "anonymous native completion" {
		t.Fatalf("lost and observed results were not separated: %s", raw)
	}
}

func TestReviewCancelledFoldStalls(t *testing.T) {
	c, path, ctx := startMode(t, agentclient.HostCallbacks{}, "review-cancel-fold", func(o *claude.Options) { o.ControlTimeout = 200 * time.Millisecond })
	request, cancel := context.WithCancel(ctx)
	defer cancel()
	run, err := c.ReservePrompt(request, c.prompt("fold"), agentclient.PromptOptions{HostInvocationID: "host-fold"})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan agentclient.PromptResult, 1)
	go func() { done <- run() }()
	id := string(marker(t, ctx, path))
	cancel()
	marker(t, ctx, filepath.Join(filepath.Dir(path), "folded"))
	wait, stop := context.WithTimeout(ctx, 750*time.Millisecond)
	defer stop()
	r := receive(t, wait, done)
	if !errors.Is(r.Receipt.OperationError, context.Canceled) || r.Receipt.Admission != agentclient.AdmissionConfirmed || r.Outcome != nil {
		t.Fatalf("cancelled input = %+v", r)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(path), "interrupt")); !os.IsNotExist(err) {
		t.Fatalf("host waiter interrupted provider: %v", err)
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(path), "finish-review-provider"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	page, err := c.ReadNativeOutcomes(ctx, acpmeta.NativeTurnReceiptsRequest{SessionID: string(c.id)}, true)
	if err != nil || len(page.Outcomes) != 1 {
		t.Fatalf("provider page = %+v, %v", page, err)
	}
	turn := page.Outcomes[0].Turn
	if turn.Origin != acpmeta.NativeTurnOriginProvider || turn.Status != acpmeta.NativeTurnCompleted ||
		len(turn.ConsumedInputIDs) != 1 || turn.ConsumedInputIDs[0] != id {
		t.Fatalf("provider outcome = %+v", turn)
	}
}

func TestReviewACPRepeatedAssistant(t *testing.T) {
	chunks := make(chan string, 8)
	peer, id, _, ctx := startACP(t, "review-repeated-assistant", func(n acp.SessionNotification) {
		if v := n.Update.AgentMessageChunk; v != nil && v.Content.Text != nil {
			chunks <- v.Content.Text.Text
		}
	}, nil)
	if _, err := peer.Prompt(ctx, acp.PromptRequest{SessionId: id, Prompt: []acp.ContentBlock{acp.TextBlock("test")}}); err != nil {
		t.Fatal(err)
	}
	var text string
	for len(chunks) > 0 {
		text += <-chunks
	}
	if text != "duplicate native message" {
		t.Fatalf("observed %q", text)
	}
}

func TestReviewFailureUnknownBaseline(t *testing.T) {
	c, _, ctx := startMode(t, agentclient.HostCallbacks{}, "review-zero-failure", nil)
	run, err := c.ReservePrompt(ctx, c.prompt("one"), agentclient.PromptOptions{})
	if err != nil {
		t.Fatal(err)
	}
	failed := run()
	run, err = c.ReservePrompt(ctx, c.prompt("two"), agentclient.PromptOptions{})
	if err != nil {
		t.Fatal(err)
	}
	out := run().Outcome
	if out == nil || out.Turn.Usage == nil {
		t.Fatalf("usage missing %+v", out)
	}
	u := out.Turn.Usage
	if u.Coverage != "main_loop" || u.InputTokens == nil || *u.InputTokens != 7 || u.Cost != nil {
		t.Fatalf("charges first turn's unreported counters to second, failed usage=%+v second coverage=%s input=%d cost=%+v", failed.Outcome.Turn.Usage, u.Coverage, *u.InputTokens, u.Cost)
	}
}

func TestReviewRunningBeforeUUIDDisablesHostCancel(t *testing.T) {
	hostScope := make(chan agentclient.Scope, 2)
	c, path, ctx := startMode(t, agentclient.HostCallbacks{Observe: func(_ context.Context, s agentclient.Scope, n acp.SessionNotification) error {
		if n.Update.AgentMessageChunk != nil {
			hostScope <- s
		}
		return nil
	}}, "review-running-before-uuid", func(o *claude.Options) { o.ControlTimeout = 200 * time.Millisecond })
	request, cancel := context.WithCancel(ctx)
	defer cancel()
	run, err := c.ReservePrompt(request, c.prompt("host"), agentclient.PromptOptions{HostInvocationID: "real-host"})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan agentclient.PromptResult, 1)
	go func() { done <- run() }()
	scope := receive(t, ctx, hostScope)
	if scope.HostInvocationID != "real-host" {
		t.Fatalf("association missing %+v", scope)
	}
	cancel()

	r := receive(t, ctx, done)
	if r.Outcome != nil && r.Outcome.Turn.Origin == "host" && r.Outcome.Turn.Status == "cancelled" {
		return
	}
	select {
	case <-time.After(500 * time.Millisecond):
		_, readErr := os.ReadFile(filepath.Join(filepath.Dir(path), "interrupt"))
		if err := c.Cancel(acp.CancelNotification{SessionId: c.id}); err != nil {
			t.Fatal(err)
		}
		page, err := c.ReadNativeOutcomes(ctx, acpmeta.NativeTurnReceiptsRequest{SessionID: string(c.id)}, true)
		if err != nil || len(page.Outcomes) != 1 {
			t.Fatalf("page=%+v err=%v", page, err)
		}
		t.Fatalf("host association present, request cancellation marker=%v, direct cancellation=%v, explicit human terminal persisted with origin=%s", readErr, r.Receipt.OperationError, page.Outcomes[0].Turn.Origin)
	}
}

func TestKnownForegroundSubagentPermissionUsesLaunchingOwner(t *testing.T) {
	calls := make(chan acp.RequestPermissionRequest, 1)
	peer, id, dir, ctx := startACP(t, "review-foreground-agent", nil, nil, func(r acp.RequestPermissionRequest) { calls <- r })
	if _, err := peer.Prompt(ctx, acp.PromptRequest{SessionId: id, Prompt: []acp.ContentBlock{acp.TextBlock("first")}}); err != nil {
		t.Fatal(err)
	}
	call := receive(t, ctx, calls)
	if call.ToolCall.ToolCallId != "known-child-tool" {
		t.Fatalf("tool = %+v", call.ToolCall)
	}
	answer := string(marker(t, ctx, filepath.Join(dir, "interaction-foreground-agent-tool")))
	if !strings.Contains(answer, "behavior:allow") {
		t.Fatalf("native reply = %q", answer)
	}
}

func TestRereviewNumberedRerunOfRetiredUUID(t *testing.T) {
	peer, id, dir, ctx := startACP(t, "rereview-retired-rerun", nil, nil)
	if _, err := peer.Prompt(ctx, acp.PromptRequest{SessionId: id, Prompt: []acp.ContentBlock{acp.TextBlock("original")}}); err != nil {
		t.Fatal(err)
	}
	marker(t, ctx, filepath.Join(dir, "emitted-rerun"))
	until := time.Now().Add(300 * time.Millisecond)
	var page acpmeta.NativeTurnReceiptsResponse
	for {
		raw, err := peer.CallExtension(ctx, acpmeta.NativeTurnReceiptsMethod, acpmeta.NativeTurnReceiptsRequest{SessionID: string(id)})
		if err != nil || json.Unmarshal(raw, &page) != nil {
			t.Fatalf("receipts=%s, %v", raw, err)
		}
		if len(page.Receipts) == 2 {
			if page.Receipts[1].Turn.Origin != "provider" || page.Receipts[1].Turn.Status != "completed" {
				t.Fatalf("second=%+v", page.Receipts[1])
			}
			return
		}
		if time.Now().After(until) {
			t.Fatalf("only %d receipt(s) after producer delivered numbered native rerun of exact retired UUID: %s", len(page.Receipts), raw)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
func TestRereviewPartialPipelineDoesNotHideKnownMainLoop(t *testing.T) {
	assertUsage := func(t *testing.T, u *acpmeta.TurnUsage) {
		t.Helper()
		if u == nil || u.Coverage != "main_loop" || u.InputTokens == nil || *u.InputTokens != 7 ||
			u.OutputTokens == nil || *u.OutputTokens != 4 || len(u.Observations) != 1 {
			t.Fatalf("main-loop accounting with scoped evidence = %+v", u)
		}
		o := u.Observations[0]
		row := o.Models["sonnet"]
		if o.Source != "claude.result.modelUsage" || o.Coverage != "query_pipeline" || o.Complete ||
			o.Tokens.InputTokens == nil || *o.Tokens.InputTokens != 7 || o.Tokens.OutputTokens != nil ||
			o.Tokens.CacheReadTokens != nil || o.Tokens.CacheWriteTokens != nil ||
			row.InputTokens == nil || *row.InputTokens != 7 || row.OutputTokens != nil {
			t.Fatalf("partial pipeline = %+v", o)
		}
	}
	t.Run("direct", func(t *testing.T) {
		c, _, ctx := startMode(t, agentclient.HostCallbacks{}, "rereview-partial-counter", nil)
		_ = receive(t, ctx, launchPrompt(t, c, ctx, "baseline"))
		second := receive(t, ctx, launchPrompt(t, c, ctx, "second"))
		if second.Outcome == nil {
			t.Fatalf("no outcome %+v", second)
		}
		assertUsage(t, second.Outcome.Turn.Usage)
	})
	t.Run("ACP prompt and retained receipt", func(t *testing.T) {
		peer, id, _, ctx := startACP(t, "rereview-partial-counter", nil, nil)
		prompt := func(text string) acp.PromptResponse {
			t.Helper()
			result, err := peer.Prompt(ctx, acp.PromptRequest{SessionId: id, Prompt: []acp.ContentBlock{acp.TextBlock(text)}})
			if err != nil {
				t.Fatal(err)
			}
			return result
		}
		_ = prompt("baseline")
		result := prompt("second")
		turn, ok := acpmeta.ReadNativeTurn(result.Meta)
		if !ok {
			t.Fatalf("native turn = %+v", result)
		}
		assertUsage(t, turn.Usage)
		raw, err := peer.CallExtension(ctx, acpmeta.NativeTurnReceiptsMethod, acpmeta.NativeTurnReceiptsRequest{SessionID: string(id)})
		var page acpmeta.NativeTurnReceiptsResponse
		if err != nil || json.Unmarshal(raw, &page) != nil || len(page.Receipts) != 2 || page.Receipts[1].Turn.InvocationID != turn.InvocationID {
			t.Fatalf("receipt page = %s, %v", raw, err)
		}
		assertUsage(t, page.Receipts[1].Turn.Usage)
	})
}

func TestPartialModelBoundaryPreservesOtherRows(t *testing.T) {
	c, _, ctx := startMode(t, agentclient.HostCallbacks{}, "rereview-model-gap", nil)
	_ = receive(t, ctx, launchPrompt(t, c, ctx, "baseline"))
	for _, input := range []string{"row disappears", "row returns"} {
		result := receive(t, ctx, launchPrompt(t, c, ctx, input))
		if result.Outcome == nil || result.Outcome.Turn.Usage == nil {
			t.Fatalf("%s outcome = %+v", input, result)
		}
		usage := result.Outcome.Turn.Usage
		if len(usage.Observations) != 1 {
			t.Fatalf("%s observation = %+v", input, usage)
		}
		o := usage.Observations[0]
		known := o.Models["sonnet"].InputTokens
		if o.Complete || o.Tokens.InputTokens != nil || known == nil || *known != 7 || o.Models["opus"].InputTokens != nil {
			t.Fatalf("%s observation = %+v", input, o)
		}
	}
}

func TestReceiptBudgetPreservesPrimaryUsageAndAnswer(t *testing.T) {
	peer, id, _, ctx := startACP(t, "rereview-observation-budget", nil, nil)
	if _, err := peer.Prompt(ctx, acp.PromptRequest{SessionId: id, Prompt: []acp.ContentBlock{acp.TextBlock("bounded receipt")}}); err != nil {
		t.Fatal(err)
	}
	raw, err := peer.CallExtension(ctx, acpmeta.NativeTurnReceiptsMethod, acpmeta.NativeTurnReceiptsRequest{SessionID: string(id)})
	var page acpmeta.NativeTurnReceiptsResponse
	if err != nil || json.Unmarshal(raw, &page) != nil || len(page.Receipts) != 1 {
		t.Fatalf("receipt page size = %d, %v", len(raw), err)
	}
	r := page.Receipts[0]
	u := r.Turn.Usage
	if u == nil || u.InputTokens == nil || *u.InputTokens != 7 || len(u.Observations) != 0 ||
		!slices.Contains(r.Unavailable, "turn.usage.observations") || r.ResultText != "known result" {
		t.Fatalf("bounded receipt = %+v", r)
	}
}

func TestRereviewCancelDoesNotInterruptToolOnlyProviderFold(t *testing.T) {
	events := make(chan agentclient.Scope, 2)
	host := agentclient.HostCallbacks{Observe: func(_ context.Context, scope agentclient.Scope, n acp.SessionNotification) error {
		if n.Update.AgentMessageChunk != nil {
			events <- scope
		}
		return nil
	}}
	c, path, ctx := startMode(t, host, "rereview-unstamped-provider", func(o *claude.Options) { o.ControlTimeout = 200 * time.Millisecond })
	request, cancel := context.WithCancel(ctx)
	defer cancel()
	result := launchPrompt(t, c, request, "fold")
	scope := receive(t, ctx, events)
	cancel()
	got := receive(t, ctx, result)
	if bytes, err := os.ReadFile(filepath.Join(filepath.Dir(path), "interrupt")); err == nil {
		t.Fatalf("cancel of folded H interrupted pre-existing tool-only P, interrupt=%s, scope=%+v receipt=%+v", bytes, scope, got.Receipt)
	}
}
func TestRereviewLateBackgroundSnapshotKeepsTaskOwner(t *testing.T) {
	tools := make(chan string, 8)
	peer, id, _, ctx := startACP(t, "rereview-late-background", func(n acp.SessionNotification) {
		if n.Update.ToolCall != nil {
			tools <- string(n.Update.ToolCall.ToolCallId)
		}
	}, nil)
	for _, text := range []string{"first", "second"} {
		if _, err := peer.Prompt(ctx, acp.PromptRequest{SessionId: id, Prompt: []acp.ContentBlock{acp.TextBlock(text)}}); err != nil {
			t.Fatal(err)
		}
	}
	for len(tools) > 0 {
		if <-tools == "late-background-tool" {
			return
		}
	}
	t.Fatal("native snapshot retained the same live background run, but no observation of its first tool reached ACP")
}
func TestRereviewGenericCompactStatusCannotConfirmManual(t *testing.T) {
	peer, id, _, ctx := startACP(t, "rereview-false-compact", nil, nil)
	raw, err := peer.CallExtension(ctx, "_session/compact", acpmeta.CompactRequest{SessionID: string(id)})
	if err == nil {
		t.Fatalf("automatic compact boundary and unqualified status accepted as manual compact: %s", raw)
	}
}

func TestRereviewWorkspaceRejectsContradictoryNativeCWD(t *testing.T) {
	client, dir, ctx := startClientMode(t, agentclient.HostCallbacks{}, "rereview-workspace-cwd", func(o *claude.Options) {
		o.Launch = claude.LaunchOptions{WorkspaceOnly: true, ExcludedWriteDirectories: []string{"blocked"}}
	})
	opened, err := client.NewSession(ctx, acp.NewSessionRequest{Cwd: dir})
	if err == nil || opened.SessionId != "" || !strings.Contains(err.Error(), "working directory") {
		t.Fatalf("workspace accepted contradictory native cwd: response=%+v, error=%v", opened, err)
	}
}

func TestBackgroundLevelCannotReviveEndedNativeRun(t *testing.T) {
	for _, tc := range []struct {
		mode     string
		observed bool
	}{
		{"rereview-ended-background", false},
		{"rereview-restarted-background", true},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			tools := make(chan struct{}, 1)
			peer, id, _, ctx := startACP(t, tc.mode, func(n acp.SessionNotification) {
				if n.Update.ToolCall != nil && n.Update.ToolCall.ToolCallId == "late-background-tool" {
					tools <- struct{}{}
				}
			}, nil)
			for _, value := range []string{"first", "second"} {
				if _, err := peer.Prompt(ctx, acp.PromptRequest{SessionId: id, Prompt: []acp.ContentBlock{acp.TextBlock(value)}}); err != nil {
					t.Fatal(err)
				}
			}
			if (len(tools) > 0) != tc.observed {
				t.Fatalf("tool observation = %v", len(tools) > 0)
			}
		})
	}
}

func TestACPWirePipelineUsageKeepsHighWaterUntilNativeReset(t *testing.T) {
	for _, tc := range []struct {
		// mode selects the native result and reset frames.
		mode string
		// input and output are the third result's attributable pipeline tokens.
		input, output int64
		// read and reasoning are the third result's cache-read and thinking deltas.
		read, reasoning int64
		// cost is the third result's attributable pipeline and model estimate.
		cost string
	}{
		{"pipeline-dip", 10, 5, 4, 2, "0.100000000000000000"},
		{"pipeline-token-dip", 10, 5, 4, 2, "0.100000000000000000"},
		{"pipeline-system-reset", 10, 5, 4, 2, "0.100000000000000000"},
		{"pipeline-reset", 30, 15, 6, 7, "0.300000000000000000"},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			peer, id, _, ctx := startACP(t, tc.mode, nil, nil)
			prompt := func(text string) *acpmeta.TurnUsage {
				t.Helper()
				response, err := peer.Prompt(ctx, acp.PromptRequest{SessionId: id, Prompt: []acp.ContentBlock{acp.TextBlock(text)}})
				if err != nil {
					t.Fatal(err)
				}
				turn, ok := acpmeta.ReadNativeTurn(response.Meta)
				if !ok || turn.Usage == nil {
					t.Fatalf("missing usage for %q: %+v", text, response)
				}
				return turn.Usage
			}
			checkPipeline := func(u *acpmeta.TurnUsage, input, output int64, cost string) {
				t.Helper()
				if u.Coverage != "query_pipeline" || !u.Complete || u.InputTokens == nil || *u.InputTokens != input ||
					u.OutputTokens == nil || *u.OutputTokens != output || u.Cost == nil || string(u.Cost.Amount) != cost {
					t.Errorf("pipeline usage = %+v, want %d/%d and %s", u, input, output, cost)
				}
				if model := u.Models["sonnet"]; model.Cost == nil || string(model.Cost.Amount) != cost {
					t.Errorf("model cost = %+v, want %s", model.Cost, cost)
				}
			}
			checkPipeline(prompt("establish baseline"), 100, 50, "1.000000000000000000")
			second := prompt("lower cumulative sample")
			if tc.mode == "pipeline-reset" {
				checkPipeline(second, 80, 40, "0.800000000000000000")
			} else {
				if second.Coverage != "main_loop" || second.InputTokens == nil || *second.InputTokens != 7 ||
					len(second.Observations) != 1 || second.Observations[0].Coverage != "query_pipeline" ||
					second.Observations[0].Complete || second.Observations[0].Tokens.InputTokens != nil || second.Observations[0].Tokens.OutputTokens != nil {
					t.Errorf("lower sample obscured scope or exposed unproven usage: %+v", second)
				}
				if tc.mode != "pipeline-token-dip" && second.Cost != nil {
					t.Errorf("lower cost was attributed to the turn: %+v", second.Cost)
				}
			}
			third := prompt("recover above high water")
			checkPipeline(third, tc.input, tc.output, tc.cost)
			if third.CacheReadTokens == nil || *third.CacheReadTokens != tc.read ||
				third.CacheWriteTokens == nil || *third.CacheWriteTokens != 2 ||
				third.ReasoningTokens == nil || *third.ReasoningTokens != tc.reasoning {
				t.Errorf("pipeline cache/thinking usage = %+v, want %d/2/%d", third, tc.read, tc.reasoning)
			}
		})
	}
}

func TestCounterBoundaryRetainsFloorWithoutAttributingGap(t *testing.T) {
	for _, tc := range []struct {
		mode      string
		resumeAt  int
		queryCost bool
	}{
		{"closure-missing-report", 4, false},
		{"closure-missing-components", 4, true},
		{"closure-missing-model", 4, true},
		{"closure-number-gap", 3, false},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			peer, id, _, ctx := startACP(t, tc.mode, nil, nil)
			for i := 0; i <= tc.resumeAt; i++ {
				u := readPromptUsage(t, peer, id, ctx)
				switch i {
				case tc.resumeAt - 1:
					if u.Coverage != "main_loop" || u.InputTokens == nil || *u.InputTokens != 7 ||
						u.OutputTokens == nil || *u.OutputTokens != 4 {
						t.Fatalf("uncertain interval replaced known main-loop usage: %+v", u)
					}
					for _, observation := range u.Observations {
						if row := observation.Models["sonnet"]; observation.Complete || row.InputTokens != nil || row.OutputTokens != nil || row.Cost != nil {
							t.Fatalf("uncertain interval appeared in pipeline evidence: %+v", observation)
						}
					}
					if tc.queryCost {
						if u.Cost == nil || string(u.Cost.Amount) != "0.100000000000000000" {
							t.Fatalf("independently adjacent query cost lost: %+v", u.Cost)
						}
					} else if u.Cost != nil {
						t.Fatalf("uncertain query cost was attributed: %+v", u.Cost)
					}
				case tc.resumeAt:
					row := u.Models["sonnet"]
					for _, observation := range u.Observations {
						if observation.Coverage == "query_pipeline" {
							row = observation.Models["sonnet"]
						}
					}
					if row.InputTokens == nil || *row.InputTokens != 10 || row.OutputTokens == nil || *row.OutputTokens != 5 ||
						row.Cost == nil || string(row.Cost.Amount) != "0.100000000000000000" ||
						u.Cost == nil || string(u.Cost.Amount) != "0.100000000000000000" {
						t.Fatalf("adjacent usage did not resume: usage=%+v model=%+v", u, row)
					}
				}
			}
		})
	}
}

func TestCounterBoundaryCannotReplayAnOlderReset(t *testing.T) {
	peer, id, _, ctx := startACP(t, "closure-late-reset-replay", nil, nil)
	for i, expected := range []struct {
		input, output int64
		cost          string
	}{
		{100, 50, "1.000000000000000000"},
		{80, 40, "0.800000000000000000"},
		{70, 35, "0.700000000000000000"},
		{20, 10, "0.200000000000000000"},
	} {
		u := readPromptUsage(t, peer, id, ctx)
		if u.Coverage != "query_pipeline" || !u.Complete || u.InputTokens == nil || *u.InputTokens != expected.input ||
			u.OutputTokens == nil || *u.OutputTokens != expected.output || u.Cost == nil || string(u.Cost.Amount) != expected.cost {
			t.Fatalf("turn %d usage = %+v", i, u)
		}
	}
}

func TestCounterBoundaryFailsClosedWhenResetHistoryIsFull(t *testing.T) {
	c, _, ctx := startMode(t, agentclient.HostCallbacks{}, "closure-reset-capacity", nil)
	initial := receive(t, ctx, launchPrompt(t, c, ctx, "baseline"))
	if initial.Outcome == nil || initial.Outcome.Turn.Status != acpmeta.NativeTurnCompleted {
		t.Fatalf("baseline outcome = %+v", initial)
	}
	over := receive(t, ctx, launchPrompt(t, c, ctx, "overflow"))
	if over.Outcome != nil || over.Receipt.OperationError == nil ||
		!strings.Contains(over.Receipt.OperationError.Error(), "conversation reset capacity exhausted") {
		t.Fatalf("reset capacity result = %+v", over)
	}
}

func readPromptUsage(t *testing.T, peer *acp.ClientSideConnection, id acp.SessionId, ctx context.Context) *acpmeta.TurnUsage {
	t.Helper()
	reply, err := peer.Prompt(ctx, acp.PromptRequest{SessionId: id, Prompt: []acp.ContentBlock{acp.TextBlock("query")}})
	if err != nil {
		t.Fatal(err)
	}
	turn, ok := acpmeta.ReadNativeTurn(reply.Meta)
	if !ok || turn.Usage == nil {
		t.Fatalf("missing usage: %+v", turn)
	}
	return turn.Usage
}

func TestUnpricedNativeClaudeUsageKeepsTokensWithoutGuessingCost(t *testing.T) {
	peer, id, _, ctx := startACP(t, "pricing-basis", nil, nil)
	for _, basis := range []string{"list", "managed", "managed", "unknown", "unknown", "re-established list", "list"} {
		u := readPromptUsage(t, peer, id, ctx)
		model := u.Models["sonnet"]
		if !u.Complete || u.InputTokens == nil || *u.InputTokens != 20 ||
			model.OutputTokens == nil || *model.OutputTokens != 20 {
			t.Fatalf("%s: token accounting = %+v, model = %+v", basis, u, model)
		}
		if basis != "list" {
			if u.Cost != nil || model.Cost != nil {
				t.Errorf("%s: unsupported pricing evidence entered the ledger, query=%+v model=%+v", basis, u.Cost, model.Cost)
			}
		} else if u.Cost == nil || model.Cost == nil || string(u.Cost.Amount) != "1.000000000000000000" || u.Cost.Amount != model.Cost.Amount {
			t.Errorf("%s: list estimate = %+v / %+v", basis, u.Cost, model.Cost)
		}
	}
}

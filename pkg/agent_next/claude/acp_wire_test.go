package claude_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
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

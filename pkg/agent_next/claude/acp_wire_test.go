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
}

func (h wireHost) SessionUpdate(_ context.Context, n acp.SessionNotification) error {
	if h.observe != nil {
		h.observe(n)
	}
	return nil
}

func startACP(t *testing.T, mode string, observe func(acp.SessionNotification), prepare func(acp.PromptRequest) acp.PromptPreparation) (*acp.ClientSideConnection, acp.SessionId, string, context.Context) {
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
	peer := acp.NewClientSideConnection(wireHost{observe: observe}, fromClient, toClient)
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

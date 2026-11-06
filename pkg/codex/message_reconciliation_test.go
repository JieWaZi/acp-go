package codex_test

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	acp "github.com/JieWaZi/acp-go-sdk"
	"github.com/JieWaZi/acp-go/pkg/acpmeta"
	agentclient "github.com/JieWaZi/acp-go/pkg/agentclient"
	"github.com/JieWaZi/acp-go/pkg/codex"
)

type reconciliationCase struct {
	// id identifies the same message throughout streaming and completion.
	id string
	// deltas are the incremental text already visible to the consumer.
	deltas []string
	// body and phase are the complete native item facts.
	body  string
	phase string
	// suffix is the expected legacy completion chunk.
	suffix string
	// replace requires the complete body to be sent as replacement evidence.
	replace bool
}

var reconciliationCases = []reconciliationCase{
	{id: "equal-prefix", deltas: []string{"案", "件"}, body: "案件です", phase: "commentary", suffix: "です"},
	{id: "same-length", deltas: []string{"stale"}, body: "fresh", phase: "commentary", replace: true},
	{id: "shorter", deltas: []string{"older answer"}, body: "new", phase: "commentary", replace: true},
	{id: "clear", deltas: []string{"discard"}, body: "", phase: "commentary", replace: true},
	{id: "identical", deltas: []string{"unchanged"}, body: "unchanged", phase: "commentary"},
	{id: "only-completion", body: "complete answer", phase: "final_answer", suffix: "complete answer"},
}

func TestDirectCompleteMessageReconcilesStreamedPrefix(t *testing.T) {
	var received []acp.SessionNotification
	c, ctx, session, _ := newFixture(t, "message_reconciliation", agentclient.HostCallbacks{
		Observe: func(_ context.Context, _ agentclient.Scope, n acp.SessionNotification) error {
			if n.Update.AgentMessageChunk != nil {
				received = append(received, n)
			}
			return nil
		},
	})
	result := reserve(t, c, ctx, session, agentclient.PromptOptions{})()
	if result.Outcome == nil || result.Outcome.DeliveryError != nil || result.Outcome.FinalMessage == nil ||
		result.Outcome.FinalMessage.Text != "complete answer" {
		t.Fatalf("native final outcome = %+v", result.Outcome)
	}
	assertReconciledMessages(t, received)
}

func TestPublicWireCompleteMessageReconcilesWithoutAppendingReplacement(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	dir := t.TempDir()
	a, err := agentclient.NewACPAgent(func(host agentclient.HostCallbacks) (agentclient.Client, error) {
		return codex.NewClient(ctx, codex.Options{
			Executable: os.Args[0], PrefixArgs: []string{"-test.run=^TestAppServerFixture$", "--"},
			Environment: append(os.Environ(), "CODEX_NEXT_FIXTURE=message_reconciliation", "CODEX_NEXT_TRACE="+filepath.Join(dir, "trace")), WorkingDirectory: dir,
		}, host)
	})
	if err != nil {
		t.Fatal(err)
	}
	toAgent, fromClient := io.Pipe()
	toClient, fromAgent := io.Pipe()
	defer func() {
		_ = toAgent.Close()
		_ = fromClient.Close()
		_ = toClient.Close()
		_ = fromAgent.Close()
		closeCtx, stop := context.WithTimeout(context.Background(), 3*time.Second)
		defer stop()
		if err := a.Close(closeCtx); err != nil {
			t.Error(err)
		}
	}()
	host := messageWireClient{updates: make(chan acp.SessionNotification, 32)}
	peer := acp.NewClientSideConnection(host, fromClient, toClient)
	acp.NewAgentSideConnectionWithBinder(a, fromAgent, toAgent, a.SetAgentConnection)
	if _, err = peer.Initialize(ctx, acp.InitializeRequest{ProtocolVersion: 1}); err != nil {
		t.Fatal(err)
	}
	session, err := peer.NewSession(ctx, acp.NewSessionRequest{Cwd: dir, McpServers: []acp.McpServer{}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := peer.Prompt(ctx, acp.PromptRequest{SessionId: session.SessionId, Prompt: []acp.ContentBlock{acp.TextBlock("messages")}}); err != nil {
		t.Fatal(err)
	}
	var received []acp.SessionNotification
	for _, test := range reconciliationCases {
		for range len(test.deltas) + 1 {
			select {
			case notification := <-host.updates:
				received = append(received, notification)
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
		}
	}
	assertReconciledMessages(t, received)
}

type messageWireClient struct {
	// Client supplies unused ACP callbacks.
	acp.Client
	// updates captures delivered agent message chunks in wire order.
	updates chan acp.SessionNotification
}

func (c messageWireClient) SessionUpdate(_ context.Context, n acp.SessionNotification) error {
	if n.Update.AgentMessageChunk != nil {
		c.updates <- n
	}
	return nil
}

func assertReconciledMessages(t *testing.T, received []acp.SessionNotification) {
	t.Helper()
	index := 0
	for _, test := range reconciliationCases {
		for i, delta := range append(append([]string{}, test.deltas...), test.suffix) {
			if index >= len(received) {
				t.Fatalf("missing update for %s at %d", test.id, i)
			}
			chunk := received[index].Update.AgentMessageChunk
			index++
			if chunk.MessageId == nil || *chunk.MessageId != test.id || chunk.Content.Text == nil || chunk.Content.Text.Text != delta {
				t.Fatalf("%s update %d has wrong identity or legacy delta: %+v", test.id, i, chunk)
			}
			info, ok := acpmeta.ReadMessageInfo(chunk.Meta)
			complete := i == len(test.deltas)
			if !ok || info.Complete != complete {
				t.Fatalf("%s completion evidence = %+v", test.id, info)
			}
			if !complete {
				if info.ReplacementText != nil {
					t.Fatalf("%s stream fragment replaced text", test.id)
				}
				continue
			}
			phase := acpmeta.MessagePhaseCommentary
			if test.phase == "final_answer" {
				phase = acpmeta.MessagePhaseFinal
			}
			if info.Phase != phase || info.Source != acpmeta.MessageSourceNativePhase || (info.ReplacementText != nil) != test.replace ||
				test.replace && *info.ReplacementText != test.body {
				t.Fatalf("%s authoritative message facts = %+v", test.id, info)
			}
		}
	}
	if index != len(received) {
		t.Fatalf("replayed completion or late delta was delivered: %d extra", len(received)-index)
	}
}

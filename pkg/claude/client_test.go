package claude_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	acp "github.com/JieWaZi/acp-go-sdk"
	"github.com/JieWaZi/acp-go/pkg/acpmeta"
	agentclient "github.com/JieWaZi/acp-go/pkg/agentclient"
	"github.com/JieWaZi/acp-go/pkg/claude"
)

type sessionClient struct {
	*claude.Client
	id acp.SessionId
}

func start(t *testing.T, host agentclient.HostCallbacks) (sessionClient, string, context.Context) {
	return startMode(t, host, "", nil)
}

func startMode(t *testing.T, host agentclient.HostCallbacks, mode string, configure func(*claude.Options)) (sessionClient, string, context.Context) {
	t.Helper()
	client, dir, ctx := startClientMode(t, host, mode, configure)
	opened, err := client.NewSession(ctx, acp.NewSessionRequest{Cwd: dir})
	if err != nil || opened.SessionId == "" {
		t.Fatalf("open = %#v, %v", opened, err)
	}
	return sessionClient{client, opened.SessionId}, filepath.Join(dir, "input"), ctx
}

func startClientMode(t *testing.T, host agentclient.HostCallbacks, mode string, configure func(*claude.Options)) (*claude.Client, string, context.Context) {
	t.Helper()
	dir := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	options := claude.Options{
		Executable: os.Args[0], PrefixArgs: []string{"-test.run=^TestClaudeFixture$", "--"},
		WorkingDirectory: dir,
		Environment:      fixtureEnvironment(dir, mode),
	}
	if configure != nil {
		configure(&options)
	}
	client, err := claude.NewClient(ctx, options, host)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := client.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	if _, err := client.Initialize(ctx, acp.InitializeRequest{ProtocolVersion: 1, ClientCapabilities: acp.ClientCapabilities{Meta: map[string]any{acpmeta.NativeTurnReceiptsCapability: true}, Elicitation: &acp.ElicitationCapabilities{Form: &acp.ElicitationFormCapabilities{}}}}); err != nil {
		t.Fatal(err)
	}
	return client, dir, ctx
}

func (client sessionClient) prompt(value string) acp.PromptRequest {
	return acp.PromptRequest{SessionId: client.id, Prompt: []acp.ContentBlock{acp.TextBlock(value)}}
}

func TestGateAndCancellationLeaveNativeInputEmpty(t *testing.T) {
	client, marker, ctx := start(t, agentclient.HostCallbacks{})
	ctx, cancel := context.WithCancel(ctx)
	wantErr := errors.New("journal refused")
	gate := func(_ context.Context, id agentclient.PromptSubmission) error {
		if id.InputID == "" || id.NativeInputID == nil || *id.NativeInputID != id.InputID || id.HostInvocationID != "host" {
			t.Errorf("identity = %+v", id)
		}
		return wantErr
	}
	run, err := client.ReservePrompt(ctx, client.prompt("one"), agentclient.PromptOptions{HostInvocationID: "host", BeforeSubmit: gate})
	if err != nil {
		t.Fatal(err)
	}
	result := run()
	if result.Receipt.Submission != agentclient.SubmissionNotSent || result.Receipt.Admission != agentclient.AdmissionNotAccepted || !errors.Is(result.Receipt.OperationError, wantErr) || result.Outcome != nil {
		t.Fatalf("result = %+v", result)
	}
	run, err = client.ReservePrompt(ctx, client.prompt("two"), agentclient.PromptOptions{BeforeSubmit: func(context.Context, agentclient.PromptSubmission) error { cancel(); return nil }})
	if err != nil {
		t.Fatal(err)
	}
	if r := run(); r.Receipt.Submission != agentclient.SubmissionNotSent || r.Outcome != nil {
		t.Fatalf("cancelled = %+v", r)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("native received input: %v", err)
	}
}

func TestSteerDuringPreparationNeverWrites(t *testing.T) {
	client, marker, ctx := start(t, agentclient.HostCallbacks{})
	preparing := make(chan struct{})
	run, err := client.ReservePrompt(ctx, client.prompt("one"), agentclient.PromptOptions{Prepare: func(owner context.Context) (func(), error) { close(preparing); <-owner.Done(); return nil, owner.Err() }})
	if err != nil {
		t.Fatal(err)
	}
	results := make(chan agentclient.PromptResult, 1)
	go func() { results <- run() }()
	select {
	case <-preparing:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	steer, err := client.Steer(ctx, agentclient.SteerRequest{SessionID: client.id, Prompt: client.prompt("early steer").Prompt, PromptRequired: true})
	if err != nil || steer.Outcome != "promptRequired" {
		t.Fatalf("steer = %+v, %v", steer, err)
	}
	if err := client.Cancel(acp.CancelNotification{SessionId: client.id}); err != nil {
		t.Fatal(err)
	}
	select {
	case r := <-results:
		if r.Receipt.Submission != agentclient.SubmissionNotSent {
			t.Fatalf("sent: %+v", r)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("native received input: %v", err)
	}
}

func TestObservationFailureKeepsCanonicalUsage(t *testing.T) {
	client, _, ctx := start(t, agentclient.HostCallbacks{Observe: func(_ context.Context, scope agentclient.Scope, _ acp.SessionNotification) error {
		if scope.NativeInvocationID != "" {
			return errors.New("observer offline")
		}
		return nil
	}})
	run, err := client.ReservePrompt(ctx, client.prompt("one"), agentclient.PromptOptions{})
	if err != nil {
		t.Fatal(err)
	}
	r := run()
	if r.Outcome == nil || r.Outcome.DeliveryError == nil || r.Outcome.Turn.Status != "completed" || r.Outcome.Turn.Usage == nil || r.Outcome.Turn.Usage.InputTokens == nil || *r.Outcome.Turn.Usage.InputTokens != 43 || r.Outcome.FinalMessage == nil || r.Outcome.FinalMessage.Text != "complete answer" {
		t.Fatalf("result = %+v, outcome = %+v", r, r.Outcome)
	}
	page, err := client.ReadNativeOutcomes(ctx, acpmeta.NativeTurnReceiptsRequest{SessionID: string(client.id)}, false)
	if err != nil || len(page.Outcomes) != 1 || page.Outcomes[0].Turn.InvocationID != r.Outcome.Turn.InvocationID || !strings.Contains(page.Outcomes[0].DeliveryError.Error(), "observer offline") {
		t.Fatalf("page = %+v, %v", page, err)
	}
}

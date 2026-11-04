package claude_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/JieWaZi/acp-go/pkg/acpmeta"
	agentclient "github.com/JieWaZi/acp-go/pkg/agent_next"
	acp "github.com/JieWaZi/acp-go/sdk"
)

func TestDirectSteerUsesNativeConsumptionEvidence(t *testing.T) {
	active := make(chan agentclient.Scope, 8)
	client, marker, ctx := startMode(t, agentclient.HostCallbacks{
		Observe: func(_ context.Context, scope agentclient.Scope, n acp.SessionNotification) error {
			if n.Update.AgentMessageChunk != nil {
				active <- scope
			}
			return nil
		},
	}, "steer-admission", nil)
	done := launchPrompt(t, client, ctx, "opening")
	target := receive(t, ctx, active)
	req := agentclient.SteerRequest{SessionID: client.id, Target: target, Prompt: client.prompt("priority").Prompt}
	denied := errors.New("partial preparation failed")
	var cleaned bool
	req.Options = agentclient.PromptOptions{HostInvocationID: "extra", Prepare: func(context.Context) (func(), error) {
		return func() { cleaned = true }, denied
	}}
	r, err := client.Steer(ctx, req)
	if !errors.Is(err, denied) || !cleaned || r.Receipt == nil || r.Receipt.Input.HostInvocationID != "extra" ||
		r.Receipt.Submission != agentclient.SubmissionNotSent || r.Receipt.Admission != agentclient.AdmissionNotAccepted {
		t.Fatalf("preparation failure = %+v, receipt = %+v, err = %v", r, r.Receipt, err)
	}
	req.Options.Prepare = nil
	canceled, cancel := context.WithCancel(ctx)
	defer cancel()
	req.Options.BeforeSubmit = func(context.Context, agentclient.PromptSubmission) error {
		cancel()
		return nil
	}
	r, err = client.Steer(canceled, req)
	if !errors.Is(err, context.Canceled) || r.Receipt == nil || r.Receipt.Submission != agentclient.SubmissionNotSent {
		t.Fatalf("cancelled gate = %+v, %v", r.Receipt, err)
	}
	var sent string
	req.Options.BeforeSubmit = func(_ context.Context, id agentclient.PromptSubmission) error {
		sent = id.InputID
		*id.NativeInputID = "mutated gate snapshot"
		return nil
	}
	r, err = client.Steer(ctx, req)
	if err != nil || r.Outcome != "injected" || r.Receipt == nil || r.Receipt.Input.InputID != sent ||
		*r.Receipt.Input.NativeInputID != sent || r.Receipt.Submission != agentclient.SubmissionSent || r.Receipt.Admission != agentclient.AdmissionUnknown {
		t.Fatalf("injected = %+v, receipt = %+v, err = %v", r, r.Receipt, err)
	}
	*r.Receipt.Input.NativeInputID = "mutated receipt"
	if err := os.WriteFile(filepath.Join(filepath.Dir(marker), "consume-steer"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	result := receive(t, ctx, done)
	if result.Outcome == nil || result.Outcome.Turn.InvocationID != target.NativeInvocationID ||
		!slices.Contains(result.Outcome.Turn.ConsumedInputIDs, sent) || r.Receipt.Admission != agentclient.AdmissionUnknown {
		t.Fatalf("canonical result = %+v, receipt = %+v", result.Outcome, r.Receipt)
	}
}

func TestDirectSteerRejectsTargetCompletedDuringGate(t *testing.T) {
	active := make(chan agentclient.Scope, 8)
	client, marker, ctx := startMode(t, agentclient.HostCallbacks{
		Observe: func(_ context.Context, scope agentclient.Scope, n acp.SessionNotification) error {
			if n.Update.AgentMessageChunk != nil {
				active <- scope
			}
			return nil
		},
	}, "steer-target-completes", nil)
	done := launchPrompt(t, client, ctx, "opening")
	target := receive(t, ctx, active)
	r, err := client.Steer(ctx, agentclient.SteerRequest{SessionID: client.id, Target: target, Prompt: client.prompt("too late").Prompt,
		Options: agentclient.PromptOptions{BeforeSubmit: func(owner context.Context, _ agentclient.PromptSubmission) error {
			if err := os.WriteFile(filepath.Join(filepath.Dir(marker), "finish-opening"), nil, 0600); err != nil {
				return err
			}
			<-owner.Done()
			return nil
		}},
	})
	if err == nil || r.Receipt == nil || r.Receipt.Submission != agentclient.SubmissionNotSent {
		t.Fatalf("stale gate = %+v, %v", r.Receipt, err)
	}
	result := receive(t, ctx, done)
	if result.Outcome == nil || result.Outcome.Turn.Status != "completed" || len(result.Outcome.Turn.ConsumedInputIDs) != 1 {
		t.Fatalf("opening result = %+v", result.Outcome)
	}
	data, err := os.ReadFile(marker)
	if err != nil || string(data) != result.Receipt.Input.InputID {
		t.Fatalf("stale input reached Claude: %s, %v", data, err)
	}
}

func TestDirectIdleSteerUsesPromptGate(t *testing.T) {
	client, marker, ctx := start(t, agentclient.HostCallbacks{})
	denied := errors.New("idle prompt denied")
	r, err := client.Steer(ctx, agentclient.SteerRequest{SessionID: client.id, Prompt: client.prompt("idle").Prompt,
		Options: agentclient.PromptOptions{HostInvocationID: "idle host", BeforeSubmit: func(_ context.Context, in agentclient.PromptSubmission) error {
			if in.HostInvocationID != "idle host" || in.NativeInputID == nil {
				t.Errorf("idle gate identity = %+v", in)
			}
			return denied
		}},
	})
	if !errors.Is(err, denied) || r.Receipt == nil || r.Receipt.Submission != agentclient.SubmissionNotSent {
		t.Fatalf("idle denial = %+v, %v", r.Receipt, err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("denied idle prompt reached Claude: %v", err)
	}
}

func TestACPWiredSteeringProjectsOnlyLegacyFields(t *testing.T) {
	active := make(chan struct{}, 1)
	peer, session, dir, ctx := startACP(t, "steer-admission", func(n acp.SessionNotification) {
		if n.Update.AgentMessageChunk != nil {
			select {
			case active <- struct{}{}:
			default:
			}
		}
	}, nil)
	done := make(chan acp.PromptResponse, 1)
	go func() {
		result, _ := peer.Prompt(ctx, acp.PromptRequest{SessionId: session, Prompt: []acp.ContentBlock{acp.TextBlock("opening")}})
		done <- result
	}()
	receive(t, ctx, active)
	raw, err := peer.CallExtension(ctx, "_session/steering", map[string]any{
		"sessionId": session, "prompt": []acp.ContentBlock{acp.TextBlock("extra")},
		"target": map[string]string{"sessionId": "foreign", "nativeInvocationId": "foreign"},
	})
	if err != nil || string(raw) != `{"outcome":"injected"}` {
		t.Fatalf("wire reply = %s, %v", raw, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "consume-steer"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	result := receive(t, ctx, done)
	turn, ok := acpmeta.ReadNativeTurn(result.Meta)
	if !ok || len(turn.ConsumedInputIDs) != 2 {
		t.Fatalf("wire canonical consumption = %+v", turn)
	}
}

package codex_test

import (
	"context"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"

	acp "github.com/JieWaZi/acp-go-sdk"
	agentclient "github.com/JieWaZi/acp-go/pkg/agentclient"
)

func TestDirectSteerAdmitsExactInputBeforeWrite(t *testing.T) {
	active := make(chan agentclient.Scope, 1)
	c, ctx, session, trace := newFixture(t, "late_steer_ack", agentclient.HostCallbacks{
		Observe: func(_ context.Context, scope agentclient.Scope, n acp.SessionNotification) error {
			if n.Update.AgentMessageChunk != nil {
				active <- scope
			}
			return nil
		},
	})
	run := reserve(t, c, ctx, session, agentclient.PromptOptions{HostInvocationID: "opening"})
	done := make(chan agentclient.PromptResult, 1)
	go func() { done <- run() }()
	var target agentclient.Scope
	select {
	case target = <-active:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	request := agentclient.SteerRequest{SessionID: session, Target: target, Prompt: []acp.ContentBlock{acp.TextBlock("steer")}}
	wrong := request
	wrong.Target.HostInvocationID = "another opening"
	if r, err := c.Steer(ctx, wrong); err == nil || r.Receipt != nil {
		t.Fatalf("foreign target: %+v, %v", r, err)
	}
	denied := errors.New("store denied")
	var gated agentclient.PromptSubmission
	var cleaned bool
	request.Options = agentclient.PromptOptions{
		HostInvocationID: "steered",
		Prepare: func(context.Context) (func(), error) {
			return func() { cleaned = true }, nil
		},
		BeforeSubmit: func(_ context.Context, id agentclient.PromptSubmission) error {
			gated = id.Snapshot()
			*id.NativeInputID = "caller mutation"
			return denied
		},
	}
	r, err := c.Steer(ctx, request)
	if !errors.Is(err, denied) || !cleaned || r.Receipt == nil || r.Receipt.Input.HostInvocationID != "steered" ||
		r.Receipt.Input.InputID == "" || *r.Receipt.Input.NativeInputID != gated.InputID ||
		r.Receipt.Submission != agentclient.SubmissionNotSent || r.Receipt.Admission != agentclient.AdmissionNotAccepted {
		t.Fatalf("denial = %+v, receipt = %+v, err = %v", r, r.Receipt, err)
	}
	data, err := os.ReadFile(trace)
	if err != nil || strings.Contains(string(data), `"method":"turn/steer"`) {
		t.Fatalf("denied input reached Codex: %s, %v", data, err)
	}
	canceled, cancel := context.WithCancel(ctx)
	defer cancel()
	request.Options.BeforeSubmit = func(context.Context, agentclient.PromptSubmission) error {
		cancel()
		return nil
	}
	r, err = c.Steer(canceled, request)
	if !errors.Is(err, context.Canceled) || r.Receipt == nil || r.Receipt.Submission != agentclient.SubmissionNotSent {
		t.Fatalf("cancelled gate = %+v, %v", r.Receipt, err)
	}
	var sent string
	request.Options.BeforeSubmit = func(_ context.Context, id agentclient.PromptSubmission) error {
		sent = id.InputID
		return nil
	}
	r, err = c.Steer(ctx, request)
	if err != nil || r.Outcome != "injected" || r.Receipt == nil || r.Receipt.Input.InputID != sent ||
		r.Receipt.Submission != agentclient.SubmissionSent || r.Receipt.Admission != agentclient.AdmissionConfirmed {
		t.Fatalf("injected = %+v, receipt = %+v, err = %v", r, r.Receipt, err)
	}
	waitTrace(t, ctx, trace, `"clientUserMessageId":"`+sent+`"`)
	*r.Receipt.Input.NativeInputID = "modified receipt"
	select {
	case result := <-done:
		if result.Outcome == nil || result.Outcome.Turn.InvocationID != target.NativeInvocationID ||
			!slices.Contains(result.Outcome.Turn.ConsumedInputIDs, sent) || slices.Contains(result.Outcome.Turn.ConsumedInputIDs, gated.InputID) {
			t.Fatalf("canonical consumption: %+v", result.Outcome)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if _, err := c.Steer(ctx, request); err == nil {
		t.Fatal("exact target unexpectedly started a successor")
	}
}

func TestDirectSteerRechecksTargetAfterGate(t *testing.T) {
	active := make(chan agentclient.Scope, 1)
	c, ctx, session, trace := newFixture(t, "steer_target_completes", agentclient.HostCallbacks{
		Observe: func(_ context.Context, scope agentclient.Scope, n acp.SessionNotification) error {
			if n.Update.AgentMessageChunk != nil {
				active <- scope
			}
			return nil
		},
	})
	run := reserve(t, c, ctx, session, agentclient.PromptOptions{HostInvocationID: "opening"})
	done := make(chan agentclient.PromptResult, 1)
	go func() { done <- run() }()
	var target agentclient.Scope
	select {
	case target = <-active:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	r, err := c.Steer(ctx, agentclient.SteerRequest{SessionID: session, Target: target, Prompt: []acp.ContentBlock{acp.TextBlock("too late")},
		Options: agentclient.PromptOptions{BeforeSubmit: func(owner context.Context, _ agentclient.PromptSubmission) error {
			if err := os.WriteFile(trace+".complete", nil, 0600); err != nil {
				return err
			}
			<-owner.Done()
			return nil
		}},
	})
	if err == nil || r.Receipt == nil || r.Receipt.Submission != agentclient.SubmissionNotSent {
		t.Fatalf("stale gate = %+v, %v", r.Receipt, err)
	}
	select {
	case result := <-done:
		if result.Outcome == nil || result.Outcome.Turn.Status != "completed" {
			t.Fatalf("opening turn = %+v", result.Outcome)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	data, err := os.ReadFile(trace)
	if err != nil || strings.Contains(string(data), `"method":"turn/steer"`) {
		t.Fatalf("stale input reached Codex: %s, %v", data, err)
	}
}

func TestDirectIdleSteerUsesPromptGate(t *testing.T) {
	c, ctx, session, trace := newFixture(t, "normal", agentclient.HostCallbacks{})
	denied := errors.New("idle prompt denied")
	r, err := c.Steer(ctx, agentclient.SteerRequest{SessionID: session, Prompt: []acp.ContentBlock{acp.TextBlock("idle")},
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
	data, err := os.ReadFile(trace)
	if err != nil || strings.Contains(string(data), `"method":"turn/start"`) {
		t.Fatalf("denied idle prompt reached Codex: %s, %v", data, err)
	}
}

func TestDirectSteerRetainsExactItemAcrossAcknowledgementAndTerminal(t *testing.T) {
	for _, phase := range []string{"active", "finishing", "draining"} {
		t.Run(phase, func(t *testing.T) {
			active := make(chan agentclient.Scope, 1)
			c, ctx, session, trace := newFixture(t, "steer_evidence_"+phase, agentclient.HostCallbacks{
				Observe: func(_ context.Context, scope agentclient.Scope, n acp.SessionNotification) error {
					if n.Update.AgentMessageChunk != nil {
						active <- scope
					}
					return nil
				},
			})
			run := reserve(t, c, ctx, session, agentclient.PromptOptions{HostInvocationID: "opening"})
			done := make(chan agentclient.PromptResult, 1)
			go func() { done <- run() }()
			var target agentclient.Scope
			select {
			case target = <-active:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			reply, err := c.Steer(ctx, agentclient.SteerRequest{SessionID: session, Target: target, Prompt: []acp.ContentBlock{acp.TextBlock("extra")}})
			admission := agentclient.AdmissionConfirmed
			if phase == "active" {
				admission = agentclient.AdmissionUnknown
			}
			if err == nil || reply.Receipt == nil || reply.Receipt.Admission != admission || reply.Receipt.Submission != agentclient.SubmissionSent {
				t.Fatalf("receipt with mismatched ACK = %+v, %v", reply.Receipt, err)
			}
			if phase == "active" {
				if err := os.WriteFile(trace+".after-ack", nil, 0600); err != nil {
					t.Fatal(err)
				}
			}
			select {
			case result := <-done:
				if result.Outcome == nil || result.Outcome.Turn.InvocationID != target.NativeInvocationID ||
					len(result.Outcome.Turn.ConsumedInputIDs) != 2 || !slices.Contains(result.Outcome.Turn.ConsumedInputIDs, reply.Receipt.Input.InputID) {
					t.Fatalf("canonical input ownership = %+v", result.Outcome)
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			if reply.Receipt.Admission != admission {
				t.Fatalf("late event mutated receipt = %+v", reply.Receipt)
			}
		})
	}
}

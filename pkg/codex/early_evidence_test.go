package codex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	acp "github.com/JieWaZi/acp-go-sdk"
	"github.com/JieWaZi/acp-go/pkg/acpmeta"
	"github.com/JieWaZi/acp-go/pkg/agentclient"
	"github.com/JieWaZi/acp-go/pkg/codex/protocol"
)

func newEarlyEvidenceOwner(t *testing.T) (*Client, *turn) {
	t.Helper()
	s := &session{id: "thread-1"}
	c := &Client{life: context.Background(), sessions: map[acp.SessionId]*session{s.id: s}, seenTurns: make(map[turnIdentity]struct{})}
	owner, err := c.installTurnLocked(context.Background(), s, false, false)
	if err != nil {
		t.Fatal(err)
	}
	owner.writing = true
	owner.admission = agentclient.AdmissionUnknown
	owner.model = "initial-model"
	t.Cleanup(func() {
		owner.stopPrepare(nil)
		owner.stopNative(nil)
		owner.stopObserve()
		c.interactions.Done()
	})
	return c, owner
}

func receiveEvidence(t *testing.T, c *Client, method string, params any) {
	t.Helper()
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	c.receiveLocked(method, raw)
}

func evidenceUsage(id string, total, last int64) map[string]any {
	counts := func(n int64) map[string]any {
		return map[string]any{"inputTokens": n, "outputTokens": n, "cachedInputTokens": 0, "reasoningOutputTokens": 0, "totalTokens": 2 * n}
	}
	return map[string]any{"threadId": "thread-1", "turnId": id, "tokenUsage": map[string]any{"total": counts(total), "last": counts(last), "modelContextWindow": 10000}}
}

func evidenceTerminal(id, status string) map[string]any {
	return map[string]any{"threadId": "thread-1", "turn": map[string]any{"id": id, "status": status}}
}

func TestEarlyTechnicalBacklogRetainsTerminal(t *testing.T) {
	c, owner := newEarlyEvidenceOwner(t)
	for i := int64(1); i <= 300; i++ {
		receiveEvidence(t, c, "thread/tokenUsage/updated", evidenceUsage("turn-1", i, 1))
	}
	receiveEvidence(t, c, "turn/completed", evidenceTerminal("turn-1", "completed"))
	c.bindTurnLocked(owner, "turn-1")
	if owner.outcome.Turn.Status != acpmeta.NativeTurnCompleted {
		t.Fatalf("received terminal was lost behind usage backlog: %+v", owner.outcome)
	}
	u := owner.outcome.Turn.Usage
	if u == nil || value(u.InputTokens) != 300 || value(u.OutputTokens) != 300 {
		t.Fatalf("matching pre-ACK usage was lost: %+v", u)
	}
}

func TestLifecycleIdentityRejectsMalformedEvidenceAcrossACK(t *testing.T) {
	for _, method := range []string{"turn/started", "turn/completed"} {
		for shape, innerID := range map[string]string{"conflicting_inner": "foreign-turn", "missing_inner": ""} {
			for _, early := range []bool{true, false} {
				for _, inputProof := range []bool{false, true} {
					for _, fullBacklog := range []bool{false, true} {
						name := fmt.Sprintf("%s/%s/preACK=%t/inputProof=%t/fullBacklog=%t", method, shape, early, inputProof, fullBacklog)
						t.Run(name, func(t *testing.T) {
							c, owner := newEarlyEvidenceOwner(t)
							owner.input.NativeInputID = pointer("exact-input")
							if !early {
								c.bindTurnLocked(owner, "turn-1")
							}
							receiveEvidence(t, c, "thread/tokenUsage/updated", evidenceUsage("turn-1", 1, 1))
							if fullBacklog {
								for i := 0; i <= maxPendingUpdates; i++ {
									receiveEvidence(t, c, "item/reasoning/textDelta", map[string]any{"threadId": "thread-1", "turnId": "turn-1", "delta": "thought"})
								}
							}
							status := "completed"
							if method == "turn/started" {
								status = "inProgress"
							}
							native := map[string]any{"status": status}
							if innerID != "" {
								native["id"] = innerID
							}
							if inputProof {
								native["items"] = []any{map[string]any{"id": "user", "type": "userMessage", "clientId": "exact-input"}}
							}
							receiveEvidence(t, c, method, map[string]any{"threadId": "thread-1", "turnId": "turn-1", "turn": native})
							if early {
								if owner.outcome.Turn.NativeTurnID != nil || owner.admission != agentclient.AdmissionUnknown {
									t.Fatalf("malformed native identity confirmed input: %+v", owner.outcome)
								}
								c.bindTurnLocked(owner, "turn-1")
							}
							if owner.outcome.Turn.Status != "" {
								t.Fatalf("malformed lifecycle identity completed the bound owner: %+v", owner.outcome)
							}
							// A rejected malformed event cannot seal the candidate or
							// prevent later exact native evidence from completing it.
							receiveEvidence(t, c, "turn/completed", evidenceTerminal("turn-1", "completed"))
							if owner.outcome.Turn.Status != acpmeta.NativeTurnCompleted || owner.outcome.Turn.Usage == nil || value(owner.outcome.Turn.Usage.InputTokens) != 1 {
								t.Fatalf("later accurate terminal or usage was lost: %+v", owner.outcome)
							}
						})
					}
				}
			}
		}
	}
}

func TestLifecycleIdentityAcceptsMatchingEnvelopeAcrossACK(t *testing.T) {
	for _, early := range []bool{true, false} {
		for _, inputProof := range []bool{true, false} {
			for _, fullBacklog := range []bool{true, false} {
				name := fmt.Sprintf("preACK=%t/inputProof=%t/fullBacklog=%t", early, inputProof, fullBacklog)
				t.Run(name, func(t *testing.T) {
					c, owner := newEarlyEvidenceOwner(t)
					owner.input.NativeInputID = pointer("exact-input")
					if !early {
						c.bindTurnLocked(owner, "turn-1")
					}
					receiveEvidence(t, c, "thread/tokenUsage/updated", evidenceUsage("turn-1", 1, 1))
					if fullBacklog {
						for i := 0; i <= maxPendingUpdates; i++ {
							receiveEvidence(t, c, "item/reasoning/textDelta", map[string]any{"threadId": "thread-1", "turnId": "turn-1", "delta": "thought"})
						}
					}
					finished := evidenceTerminal("turn-1", "completed")
					finished["turnId"] = "turn-1"
					if inputProof {
						finished["turn"].(map[string]any)["items"] = []any{map[string]any{"id": "user", "type": "userMessage", "clientId": "exact-input"}}
					}
					receiveEvidence(t, c, "turn/completed", finished)
					if early {
						c.bindTurnLocked(owner, "turn-1")
					}
					if owner.outcome.Turn.Status != acpmeta.NativeTurnCompleted || value(owner.outcome.Turn.NativeTurnID) != "turn-1" || owner.admission != agentclient.AdmissionConfirmed || owner.outcome.Turn.Usage == nil || value(owner.outcome.Turn.Usage.InputTokens) != 1 {
						t.Fatalf("matching native terminal was rejected across ACK: %+v", owner.outcome)
					}
				})
			}
		}
	}
}

func TestTechnicalEvidenceUsesSameRulesAcrossACK(t *testing.T) {
	for _, status := range []string{"completed", "failed", "interrupted"} {
		for _, early := range []bool{true, false} {
			t.Run(status+map[bool]string{true: "/before_ACK", false: "/after_ACK"}[early], func(t *testing.T) {
				c, owner := newEarlyEvidenceOwner(t)
				if !early {
					c.bindTurnLocked(owner, "turn-1")
				}
				for i := int64(1); i <= 600; i++ {
					receiveEvidence(t, c, "thread/tokenUsage/updated", evidenceUsage("turn-1", i, 1))
					receiveEvidence(t, c, "model/rerouted", map[string]any{"threadId": "thread-1", "turnId": "turn-1", "toModel": "switched-model"})
					receiveEvidence(t, c, "turn/started", evidenceTerminal("turn-1", "inProgress"))
				}
				completed := evidenceTerminal("turn-1", status)
				completed["turn"].(map[string]any)["error"] = map[string]any{"message": "native failure reason"}
				receiveEvidence(t, c, "turn/completed", completed)
				// Once native completion exists, neither a duplicate status nor a
				// late usage sample is allowed to change that turn's facts.
				receiveEvidence(t, c, "turn/completed", evidenceTerminal("turn-1", "failed"))
				receiveEvidence(t, c, "thread/tokenUsage/updated", evidenceUsage("turn-1", 9999, 1))
				if early {
					c.bindTurnLocked(owner, "turn-1")
				}
				want := acpmeta.NativeTurnCompleted
				if status == "failed" {
					want = acpmeta.NativeTurnFailed
				} else if status == "interrupted" {
					want = acpmeta.NativeTurnCancelled
				}
				u := owner.outcome.Turn.Usage
				if owner.outcome.Turn.Status != want || u == nil || value(u.InputTokens) != 600 || value(u.OutputTokens) != 600 || u.Model != "" {
					t.Fatalf("technical evidence changed across ACK: %+v usage=%+v", owner.outcome, u)
				}
				if early && !errors.Is(owner.outcome.DeliveryError, errObservationBudget) {
					t.Fatalf("discarded observations did not report delivery loss: %+v", owner.outcome)
				}
				if status == "failed" && (owner.outcome.NativeFailure == nil || owner.outcome.NativeFailure.Error() != "native failure reason") {
					t.Fatalf("native failure reason was lost: %+v", owner.outcome)
				}
				if owner.session.usageCheckpoint != nil {
					t.Fatal("post-terminal usage retained a cumulative checkpoint")
				}
			})
		}
	}
}

func TestEarlyUsagePreservesRegressionAndForeignBoundaries(t *testing.T) {
	for _, early := range []bool{true, false} {
		t.Run(map[bool]string{true: "before_ACK", false: "after_ACK"}[early], func(t *testing.T) {
			c, owner := newEarlyEvidenceOwner(t)
			if !early {
				c.bindTurnLocked(owner, "turn-1")
			}
			// The regression is disputed, its later matching advance is one
			// request, and the foreign sample invalidates the next cumulative delta.
			receiveEvidence(t, c, "thread/tokenUsage/updated", evidenceUsage("turn-1", 10, 1))
			receiveEvidence(t, c, "thread/tokenUsage/updated", evidenceUsage("turn-1", 5, 1))
			receiveEvidence(t, c, "thread/tokenUsage/updated", evidenceUsage("turn-1", 11, 1))
			receiveEvidence(t, c, "thread/tokenUsage/updated", evidenceUsage("foreign-turn", 999, 900))
			receiveEvidence(t, c, "model/rerouted", map[string]any{"threadId": "thread-1", "turnId": "foreign-turn", "toModel": "foreign-model"})
			receiveEvidence(t, c, "turn/completed", evidenceTerminal("foreign-turn", "failed"))
			receiveEvidence(t, c, "thread/tokenUsage/updated", evidenceUsage("turn-1", 1000, 1))
			receiveEvidence(t, c, "thread/tokenUsage/updated", evidenceUsage("turn-1", 1001, 1))
			receiveEvidence(t, c, "thread/tokenUsage/updated", evidenceUsage("turn-1", 1001, 1))
			receiveEvidence(t, c, "turn/completed", evidenceTerminal("turn-1", "completed"))
			if early {
				c.bindTurnLocked(owner, "turn-1")
			}
			u := owner.outcome.Turn.Usage
			if owner.outcome.Turn.Status != acpmeta.NativeTurnCompleted || u == nil || value(u.InputTokens) != 4 || value(u.OutputTokens) != 4 || u.Complete || u.Model != "initial-model" || u.Coverage != "observed_requests" {
				t.Fatalf("foreign or regressed counters entered turn accounting: %+v usage=%+v", owner.outcome, u)
			}
		})
	}
}

func TestEarlyUsagePreservesCumulativeBaselineAndModelSwitch(t *testing.T) {
	for _, early := range []bool{true, false} {
		t.Run(map[bool]string{true: "before_ACK", false: "after_ACK"}[early], func(t *testing.T) {
			c, owner := newEarlyEvidenceOwner(t)
			floor := &tokenCounters{Input: pointer(int64(10)), Output: pointer(int64(10)), Read: pointer(int64(0)), Reasoning: pointer(int64(0)), Context: pointer(int64(20))}
			owner.previous = &tokenCheckpoint{total: floor}
			owner.session.usageCheckpoint = owner.previous
			owner.usageHigh = floor.Context
			owner.usageGap = false
			if !early {
				c.bindTurnLocked(owner, "turn-1")
			}
			receiveEvidence(t, c, "model/rerouted", map[string]any{"threadId": "thread-1", "turnId": "turn-1", "toModel": "next-model"})
			for i := int64(11); i <= 13; i++ {
				receiveEvidence(t, c, "thread/tokenUsage/updated", evidenceUsage("turn-1", i, 1))
			}
			receiveEvidence(t, c, "turn/completed", evidenceTerminal("turn-1", "completed"))
			if early {
				c.bindTurnLocked(owner, "turn-1")
			}
			u := owner.outcome.Turn.Usage
			if u == nil || value(u.InputTokens) != 3 || value(u.OutputTokens) != 3 || !u.Complete || u.Coverage != "observed_turn" || u.Model != "next-model" || owner.session.usageCheckpoint == nil {
				t.Fatalf("baseline or model change was lost across correlation: %+v", u)
			}
		})
	}
}

func TestEarlyInputEvidenceBindsSameFoldedFactsAsACK(t *testing.T) {
	c, owner := newEarlyEvidenceOwner(t)
	owner.input.NativeInputID = pointer("exact-input")
	for i := int64(1); i <= 600; i++ {
		receiveEvidence(t, c, "thread/tokenUsage/updated", evidenceUsage("turn-1", i, 1))
	}
	receiveEvidence(t, c, "turn/completed", evidenceTerminal("foreign-turn", "failed"))
	finished := evidenceTerminal("turn-1", "completed")
	finished["turn"].(map[string]any)["items"] = []any{map[string]any{"id": "user", "type": "userMessage", "clientId": "exact-input"}}
	receiveEvidence(t, c, "turn/completed", finished)
	// A late acknowledgement cannot resurrect an already completed owner.
	c.bindTurnLocked(owner, "foreign-turn")
	c.captureTerminalLocked(owner, protocol.TurnElement{ID: "turn-1", Status: protocol.PurpleInProgress})
	if owner.admission != agentclient.AdmissionConfirmed || value(owner.outcome.Turn.NativeTurnID) != "turn-1" || owner.outcome.Turn.Status != acpmeta.NativeTurnCompleted || owner.outcome.Turn.Usage == nil || value(owner.outcome.Turn.Usage.InputTokens) != 600 {
		t.Fatalf("input correlation lost the folded turn facts: %+v", owner.outcome)
	}
}

func TestEarlyCandidateBudgetKeepsRetainedTerminal(t *testing.T) {
	c, owner := newEarlyEvidenceOwner(t)
	receiveEvidence(t, c, "thread/tokenUsage/updated", evidenceUsage("turn-1", 1, 1))
	for i := 0; i < 600; i++ {
		receiveEvidence(t, c, "thread/tokenUsage/updated", evidenceUsage(fmt.Sprintf("foreign-%d", i), 999, 999))
	}
	receiveEvidence(t, c, "turn/completed", evidenceTerminal("turn-1", "completed"))
	if len(owner.earlyTurns) > maxPendingUpdates || len(owner.early) > maxPendingUpdates || owner.earlyBytes > maxEarlyBytes {
		t.Fatal("unconfirmed retention exceeded its budget")
	}
	c.bindTurnLocked(owner, "turn-1")
	if owner.outcome.Turn.Status != acpmeta.NativeTurnCompleted || !errors.Is(owner.outcome.DeliveryError, errObservationBudget) || value(owner.outcome.Turn.Usage.InputTokens) != 1 {
		t.Fatalf("new candidate pressure erased an existing terminal: %+v", owner.outcome)
	}
}

func TestEarlyForeignStartCannotReplaceCompletedOwner(t *testing.T) {
	c, owner := newEarlyEvidenceOwner(t)
	receiveEvidence(t, c, "turn/completed", evidenceTerminal("turn-1", "completed"))
	receiveEvidence(t, c, "turn/started", evidenceTerminal("foreign-turn", "inProgress"))
	c.bindTurnLocked(owner, "turn-1")
	if owner.session.active != owner || owner.outcome.Turn.Status != acpmeta.NativeTurnCompleted {
		t.Fatalf("another candidate's start replaced the correlated owner: %+v", owner.outcome)
	}
}

func TestEarlyOversizedModelReportsOmittedFact(t *testing.T) {
	c, owner := newEarlyEvidenceOwner(t)
	receiveEvidence(t, c, "model/rerouted", map[string]any{"threadId": "thread-1", "turnId": "turn-1", "toModel": strings.Repeat("m", maxNativeIDBytes+1)})
	receiveEvidence(t, c, "thread/tokenUsage/updated", evidenceUsage("turn-1", 1, 1))
	receiveEvidence(t, c, "turn/completed", evidenceTerminal("turn-1", "completed"))
	c.bindTurnLocked(owner, "turn-1")
	if owner.outcome.Turn.Usage == nil || owner.outcome.Turn.Usage.Model != "" || !slices.Contains(owner.outcome.Unavailable, acpmeta.ReceiptUsageModelUnavailable) {
		t.Fatalf("bounded model omission was hidden: %+v", owner.outcome)
	}
}

func TestEarlyObservationBytesCannotEraseNativeStatus(t *testing.T) {
	c, owner := newEarlyEvidenceOwner(t)
	receiveEvidence(t, c, "thread/tokenUsage/updated", evidenceUsage("turn-1", 1, 1))
	finished := evidenceTerminal("turn-1", "completed")
	finished["turn"].(map[string]any)["items"] = []any{map[string]any{"id": "oversized-answer", "type": "agentMessage", "phase": "final_answer", "text": strings.Repeat("x", maxEarlyBytes+1)}}
	receiveEvidence(t, c, "turn/completed", finished)
	c.bindTurnLocked(owner, "turn-1")
	if owner.outcome.Turn.Status != acpmeta.NativeTurnCompleted || !errors.Is(owner.outcome.DeliveryError, errObservationBudget) || owner.outcome.OperationError != nil {
		t.Fatalf("payload budget changed known completion into operation failure: %+v", owner.outcome)
	}
}

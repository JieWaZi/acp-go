package nativeacp

import (
	"context"
	"errors"
	"testing"

	"github.com/JieWaZi/acp-go/pkg/autoreview"
	acp "github.com/coder/acp-go-sdk"
)

// reviewHost 收集审查审计通知。
type reviewHost struct {
	// updates 是实际发布的审计数量。
	updates []acp.SessionNotification
	// fail 模拟通知发送失败。
	fail bool
}

// TestReviewRejectsReplacedSession 验证审查完成后变更执行代或模型不会获得迟到授权。
func TestReviewRejectsReplacedSession(t *testing.T) {
	for _, replaceGeneration := range []bool{false, true} {
		state := SessionContext{generation: &sessionOptions{}, Model: "first", WorkingDirectory: "/work"}
		adapter := NewReviewPermissionAdapter(ReviewPermissionConfig{Mode: "auto", CurrentSession: func(acp.SessionId) (SessionContext, bool) { return state, true }, Reviewer: func(context.Context, autoreview.Request) (autoreview.Decision, error) {
			if replaceGeneration {
				state.generation = &sessionOptions{}
			} else {
				state.Model = "changed"
			}
			return autoreview.Decision{Outcome: "allow"}, nil
		}})
		evidence := PermissionEvidence{Request: acp.RequestPermissionRequest{SessionId: "s", ToolCall: acp.ToolCallUpdate{ToolCallId: "tool"}, Options: []acp.PermissionOption{{OptionId: "once", Kind: acp.PermissionOptionKindAllowOnce}}}, Detail: acp.ToolCallUpdate{RawInput: map[string]any{"command": "ls"}}, Prompt: []acp.ContentBlock{acp.TextBlock("list")}, ToolOwner: "s", Active: true}
		_, handled, err := adapter.Review(context.Background(), &reviewHost{}, evidence)
		if err != nil || handled {
			t.Fatalf("replaced session approved: generation=%v handled=%v err=%v", replaceGeneration, handled, err)
		}
	}
}

// UpdateSession 保存审计或模拟宿主断开。
func (h *reviewHost) UpdateSession(_ context.Context, r acp.SessionNotification) error {
	h.updates = append(h.updates, r)
	if h.fail {
		return errors.New("host gone")
	}
	return nil
}

// TestReviewPermissionFallsBackAndOnlyAllowsOnce 验证审查不能扩大为持久授权，失败和缺证据转人工。
func TestReviewPermissionFallsBackAndOnlyAllowsOnce(t *testing.T) {
	for _, outcome := range []string{"allow", "deny", "error", "missing", "detached", "audit-failure"} {
		t.Run(outcome, func(t *testing.T) {
			called := false
			adapter := NewReviewPermissionAdapter(ReviewPermissionConfig{Mode: "auto", CurrentSession: func(acp.SessionId) (SessionContext, bool) {
				return SessionContext{WorkingDirectory: "/work", Model: "actual-model"}, true
			}, Reviewer: func(_ context.Context, r autoreview.Request) (autoreview.Decision, error) {
				called = true
				if r.Model != "actual-model" || r.WorkingDirectory != "/work" || r.Tool.RawInput == nil || len(r.Prompt) != 1 {
					t.Fatalf("lost evidence: %+v", r)
				}
				if outcome == "error" {
					return autoreview.Decision{}, errors.New("unavailable")
				}
				return autoreview.Decision{Outcome: outcome}, nil
			}})
			evidence := PermissionEvidence{Request: acp.RequestPermissionRequest{SessionId: "s", ToolCall: acp.ToolCallUpdate{ToolCallId: "tool"}, Options: []acp.PermissionOption{{OptionId: "always", Kind: acp.PermissionOptionKindAllowAlways, Name: "Always"}, {OptionId: "once", Kind: acp.PermissionOptionKindAllowOnce, Name: "Once"}}}, Detail: acp.ToolCallUpdate{RawInput: map[string]any{"command": "ls"}}, Prompt: []acp.ContentBlock{acp.TextBlock("list files")}, ToolOwner: "s", Active: true}
			if outcome == "missing" {
				evidence.Detail.RawInput = nil
			}
			if outcome == "detached" {
				evidence.ToolOwner = "other"
			}
			host := &reviewHost{updates: []acp.SessionNotification{}, fail: outcome == "audit-failure"}
			response, handled, err := adapter.Review(context.Background(), host, evidence)
			if err != nil {
				t.Fatal(err)
			}
			if outcome == "allow" {
				if !handled || response.Outcome.Selected == nil || response.Outcome.Selected.OptionId != "once" {
					t.Fatalf("approval expanded: %+v %v", response, handled)
				}
			} else if outcome == "audit-failure" {
				if !handled || response.Outcome.Cancelled == nil {
					t.Fatalf("disconnected audit: %+v", response)
				}
			} else if handled {
				t.Fatalf("failed review approved: %+v", response)
			}
			if outcome == "missing" || outcome == "detached" {
				if called {
					t.Fatal("reviewer ran without complete owned evidence")
				}
			}
			if len(host.updates) != 1 {
				t.Fatalf("audit count %d", len(host.updates))
			}
		})
	}
}

// TestCurrentSessionSnapshotPreservesCwd 验证会话快照来自当前执行代，并在关闭后消失。
func TestCurrentSessionSnapshotPreservesCwd(t *testing.T) {
	agent, peer, _ := startLifecycleBridge(t, true, true)
	snapshot, ok := agent.CurrentSession("a")
	if !ok || snapshot.WorkingDirectory != "/fixture" {
		t.Fatalf("snapshot: %+v %v", snapshot, ok)
	}
	snapshot.WorkingDirectory = "mutated"
	actual, _ := agent.CurrentSession("a")
	if actual.WorkingDirectory != "/fixture" {
		t.Fatal("snapshot aliases state")
	}
	_, _ = peer.CloseSession(context.Background(), acp.CloseSessionRequest{SessionId: "a"})
	if _, ok := agent.CurrentSession("a"); ok {
		t.Fatal("closed session still returned")
	}
}

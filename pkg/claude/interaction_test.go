package claude_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	acp "github.com/JieWaZi/acp-go-sdk"
	"github.com/JieWaZi/acp-go/pkg/acpmeta"
	agentclient "github.com/JieWaZi/acp-go/pkg/agentclient"
	"github.com/JieWaZi/acp-go/pkg/claude"
)

func TestNativeToolPermissionUsesExactInvocation(t *testing.T) {
	scopeSeen := make(chan agentclient.Scope, 1)
	client, input, ctx := startMode(t, agentclient.HostCallbacks{
		RequestPermission: func(_ context.Context, scope agentclient.Scope, req acp.RequestPermissionRequest) (acp.RequestPermissionResponse, error) {
			scopeSeen <- scope
			if req.ToolCall.ToolCallId != "tool-1" || len(req.Options) != 2 {
				t.Errorf("permission request = %+v", req)
			}
			return acp.RequestPermissionResponse{Outcome: acp.RequestPermissionOutcome{
				Selected: &acp.RequestPermissionOutcomeSelected{OptionId: "allow_once"},
			}}, nil
		},
	}, "approval", nil)
	r := receive(t, ctx, launchPrompt(t, client, ctx, "approval"))
	scope := receive(t, ctx, scopeSeen)
	if r.Outcome == nil || r.Outcome.Turn.Status != acpmeta.NativeTurnCompleted || scope.NativeInvocationID != r.Outcome.Turn.InvocationID ||
		scope.HostInvocationID != r.Receipt.Input.HostInvocationID || scope.SessionID != client.id {
		t.Fatalf("scope = %+v, result = %+v", scope, r)
	}
	if got := string(marker(t, ctx, filepath.Join(filepath.Dir(input), "interaction-tool-request"))); !strings.Contains(got, "behavior:allow") {
		t.Fatal(got)
	}
}

func TestAskUserQuestionReturnsNativeAnswers(t *testing.T) {
	client, input, ctx := startMode(t, agentclient.HostCallbacks{
		CreateElicitation: func(_ context.Context, scope agentclient.Scope, req acp.UnstableCreateElicitationRequest) (acp.UnstableCreateElicitationResponse, error) {
			if scope.NativeInvocationID == "" || req.Form == nil || req.Form.Message != "Select a color" {
				t.Errorf("form = %+v, scope = %+v", req, scope)
			}
			return acp.UnstableCreateElicitationResponse{Accept: &acp.UnstableCreateElicitationAccept{
				Content: map[string]any{"question_1": "Green"},
			}}, nil
		},
	}, "ask", nil)
	r := receive(t, ctx, launchPrompt(t, client, ctx, "ask"))
	if r.Outcome == nil || r.Outcome.Turn.Status != acpmeta.NativeTurnCompleted {
		t.Fatalf("result = %+v", r)
	}
	got := string(marker(t, ctx, filepath.Join(filepath.Dir(input), "interaction-tool-request")))
	if !strings.Contains(got, "Select a color:Green") || !strings.Contains(got, "behavior:allow") {
		t.Fatal(got)
	}
}

func TestWorkspaceHookDeniesProtectedWritesAfterNativePolicyVerification(t *testing.T) {
	client, input, ctx := startMode(t, agentclient.HostCallbacks{}, "workspace", func(o *claude.Options) {
		o.Launch = claude.LaunchOptions{WorkspaceOnly: true, ExcludedWriteDirectories: []string{"protected"}}
	})
	r := receive(t, ctx, launchPrompt(t, client, ctx, "write protected file"))
	if r.Outcome == nil || r.Outcome.Turn.Status != acpmeta.NativeTurnCompleted {
		t.Fatalf("result = %+v", r)
	}
	got := string(marker(t, ctx, filepath.Join(filepath.Dir(input), "interaction-tool-request")))
	if !strings.Contains(got, "permissionDecision:deny") {
		t.Fatalf("native hook reply = %q", got)
	}
}

func TestBackgroundToolObservationKeepsLaunchingScope(t *testing.T) {
	observed := make(chan agentclient.Scope, 1)
	client, _, ctx := startMode(t, agentclient.HostCallbacks{
		Observe: func(_ context.Context, scope agentclient.Scope, n acp.SessionNotification) error {
			if tool := n.Update.ToolCall; tool != nil && tool.ToolCallId == "tool-first-seen-in-old-agent" {
				observed <- scope
			}
			return nil
		},
	}, "review-stale-permission", nil)
	first := receive(t, ctx, launchPrompt(t, client, ctx, "launch background"))
	second := receive(t, ctx, launchPrompt(t, client, ctx, "next foreground"))
	scope := receive(t, ctx, observed)
	if first.Outcome == nil || second.Outcome == nil || scope.NativeInvocationID != first.Outcome.Turn.InvocationID ||
		scope.HostInvocationID != first.Receipt.Input.HostInvocationID {
		t.Fatalf("late tool scope = %+v, old = %+v, new = %+v", scope, first.Outcome, second.Outcome)
	}
}

func TestWorkspaceApprovalRequiresMatchingCallDirectoryAndTarget(t *testing.T) {
	for _, tc := range []struct {
		mode string
		want string
	}{
		{"workspace-approval", "allow"},
		{"workspace-path-substitution", "deny"},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			calls := make(chan acp.RequestPermissionRequest, 1)
			client, path, ctx := startMode(t, agentclient.HostCallbacks{RequestPermission: func(_ context.Context, _ agentclient.Scope, req acp.RequestPermissionRequest) (acp.RequestPermissionResponse, error) {
				calls <- req
				return acp.RequestPermissionResponse{Outcome: acp.RequestPermissionOutcome{
					Selected: &acp.RequestPermissionOutcomeSelected{OptionId: "allow_once"},
				}}, nil
			}}, tc.mode, func(o *claude.Options) {
				o.Launch = claude.LaunchOptions{WorkspaceOnly: true, ExcludedWriteDirectories: []string{"protected"}}
			})
			r := receive(t, ctx, launchPrompt(t, client, ctx, "file approval"))
			if r.Outcome == nil || r.Outcome.Turn.Status != acpmeta.NativeTurnCompleted {
				t.Fatalf("result = %+v", r)
			}
			reply := string(marker(t, ctx, filepath.Join(filepath.Dir(path), "interaction-permission-followup")))
			if !strings.Contains(reply, "behavior:"+tc.want) || len(calls) != map[string]int{"allow": 1, "deny": 0}[tc.want] {
				t.Fatalf("native reply = %q, permission calls = %d", reply, len(calls))
			}
		})
	}
}

func TestWorkspaceHookRejectsCallDirectoryInExcludedRoot(t *testing.T) {
	client, path, ctx := startMode(t, agentclient.HostCallbacks{}, "rereview-hook-cwd", func(o *claude.Options) {
		o.Launch = claude.LaunchOptions{WorkspaceOnly: true, ExcludedWriteDirectories: []string{"blocked"}}
	})
	r := receive(t, ctx, launchPrompt(t, client, ctx, "write with changed cwd"))
	if r.Outcome == nil || r.Outcome.Turn.Status != acpmeta.NativeTurnCompleted {
		t.Fatalf("result = %+v", r)
	}
	reply := string(marker(t, ctx, filepath.Join(filepath.Dir(path), "interaction-wrong-cwd-hook")))
	if !strings.Contains(reply, "permissionDecision:deny") {
		t.Fatalf("native hook reply = %q", reply)
	}
}

func TestEmptyBuiltinToolsAllowOnlyExplicitMCPApproval(t *testing.T) {
	for _, mode := range []string{"mcp-only-local", "mcp-only-foreign", "mcp-only-builtin"} {
		t.Run(mode, func(t *testing.T) {
			calls := make(chan acp.RequestPermissionRequest, 1)
			client, dir, ctx := startClientMode(t, agentclient.HostCallbacks{
				RequestPermission: func(_ context.Context, _ agentclient.Scope, req acp.RequestPermissionRequest) (acp.RequestPermissionResponse, error) {
					calls <- req
					return acp.RequestPermissionResponse{Outcome: acp.RequestPermissionOutcome{
						Selected: &acp.RequestPermissionOutcomeSelected{OptionId: "allow_once"},
					}}, nil
				},
			}, mode, func(o *claude.Options) {
				o.Launch.Tools, o.Launch.SettingSources = []string{}, []string{}
			})
			opened, err := client.NewSession(ctx, acp.NewSessionRequest{Cwd: dir,
				McpServers: []acp.McpServer{{Stdio: &acp.McpServerStdio{Name: "local", Command: "fixture"}}},
			})
			if err != nil {
				t.Fatal(err)
			}
			r := receive(t, ctx, launchPrompt(t, sessionClient{client, opened.SessionId}, ctx, "MCP-only approval"))
			if r.Outcome == nil || r.Outcome.Turn.Status != acpmeta.NativeTurnCompleted {
				t.Fatalf("result = %+v", r)
			}
			reply := string(marker(t, ctx, filepath.Join(dir, "interaction-tool-request")))
			if mode == "mcp-only-local" {
				if len(calls) != 1 || !strings.Contains(reply, "behavior:allow") {
					t.Fatalf("configured tool did not reach its host approver: reply = %s, approvals = %d", reply, len(calls))
				}
				approval := <-calls
				if approval.ToolCall.Meta["claude/native-tool-name"] != "mcp__local__knowledge_audit_get" {
					t.Fatalf("missing exact tool name in approval: %+v", approval.ToolCall)
				}
			} else if len(calls) != 0 || !strings.Contains(reply, "behavior:deny") {
				t.Fatalf("tool outside explicit MCP scope was allowed: reply = %s, approvals = %d", reply, len(calls))
			}
		})
	}
}

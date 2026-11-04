package claude_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JieWaZi/acp-go/pkg/acpmeta"
	agentclient "github.com/JieWaZi/acp-go/pkg/agent_next"
	"github.com/JieWaZi/acp-go/pkg/agent_next/claude"
	acp "github.com/JieWaZi/acp-go/sdk"
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

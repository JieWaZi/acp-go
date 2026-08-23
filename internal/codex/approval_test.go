package codex

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"acp-go/agents/codex/protocol"

	acp "github.com/coder/acp-go-sdk"
)

// recordingPermissionRequester 记录 ACP permission 请求并返回测试指定结果。
type recordingPermissionRequester struct {
	// requests 保存客户端收到的请求。
	requests []acp.RequestPermissionRequest
	// response 是模拟客户端响应。
	response acp.RequestPermissionResponse
	// err 是模拟客户端错误。
	err error
	// after 在响应返回前执行，用于制造 generation 竞态。
	after func()
}

// panickingPermissionRequester 模拟客户端 permission 回调越过接口边界抛出 panic。
type panickingPermissionRequester struct{}

// RequestPermission 始终 panic，用于验证 approval handler 的 fail-closed 防线。
func (panickingPermissionRequester) RequestPermission(
	context.Context,
	acp.RequestPermissionRequest,
) (acp.RequestPermissionResponse, error) {
	panic("simulated callback failure")
}

// RequestPermission 实现 approval 组件需要的最小 ACP 连接接口。
func (r *recordingPermissionRequester) RequestPermission(
	_ context.Context,
	request acp.RequestPermissionRequest,
) (acp.RequestPermissionResponse, error) {
	r.requests = append(r.requests, request)
	if r.after != nil {
		r.after()
	}
	return r.response, r.err
}

// TestApprovalHandlerMapsThreeRequestKinds 验证三类请求的 option 和 app-server 响应映射。
func TestApprovalHandlerMapsThreeRequestKinds(t *testing.T) {
	t.Parallel()

	guard := &fixedGenerationGuard{current: true}
	requester := &recordingPermissionRequester{response: selectedPermission("allow_once")}
	handler := newTestApprovalHandler(requester, guard)
	command := handler.HandleCommand(context.Background(), protocol.CommandExecutionRequestApprovalParams{
		ThreadID: "thread-1", TurnID: "turn-1", ItemID: "command-1",
		Command: acp.Ptr("/bin/zsh -c 'npm install'"), Cwd: acp.Ptr("/work"),
	})
	if command.Decision == nil || command.Decision.Enum == nil || *command.Decision.Enum != protocol.Accept {
		t.Fatalf("command response = %#v，期望 accept", command)
	}
	request := requester.requests[0]
	if len(request.Options) != 3 || request.ToolCall.RawInput.(map[string]any)["command"] != "npm install" {
		t.Fatalf("command permission = %#v", request)
	}

	requester.response = selectedPermission("allow_always")
	file := handler.HandleFile(context.Background(), protocol.FileChangeRequestApprovalParams{
		ThreadID: "thread-1", TurnID: "turn-1", ItemID: "file-1",
		GrantRoot: protocol.NewOptionalValue("/work/generated"),
	})
	if file.Decision != protocol.AcceptForSession {
		t.Fatalf("file response = %#v，期望 acceptForSession", file)
	}
	if got := requester.requests[1].ToolCall.Kind; got == nil || *got != acp.ToolKindEdit {
		t.Fatalf("file tool kind = %v", got)
	}
	fileSessionOption := requester.requests[1].Options[1]
	if fileSessionOption.Name != "Allow Root for Session" || fileSessionOption.Meta["codex"].(map[string]any)["grantRoot"] != "/work/generated" {
		t.Fatalf("file session option = %#v", fileSessionOption)
	}

	requester.response = selectedPermission("allow_permissions_session")
	permissions := protocol.Permissions{
		Network: &protocol.NetworkClass{Enabled: acp.Ptr(true)},
		FileSystem: &protocol.FileSystemClass{
			Read: []string{"/work"}, Write: []string{"/work/tmp"},
		},
	}
	permission := handler.HandlePermissions(context.Background(), protocol.PermissionsRequestApprovalParams{
		ThreadID: "thread-1", TurnID: "turn-1", ItemID: "permissions-1", Cwd: "/work",
		Reason: acp.Ptr("Need extra access"), Permissions: permissions,
	})
	if permission.Scope == nil || *permission.Scope != protocol.Session || permission.Permissions.Network == nil {
		t.Fatalf("permissions response = %#v，期望 session 原样授权", permission)
	}
	if strict, ok := permission.StrictAutoReview.Value(); !ok || strict {
		t.Fatalf("strictAutoReview = %#v，期望显式 false", permission.StrictAutoReview)
	}
	if got := requester.requests[2].Options; len(got) != 3 || got[0].OptionId != "allow_permissions_session" || got[1].OptionId != "allow_permissions_turn" {
		t.Fatalf("permissions options = %#v", got)
	}
}

// TestApprovalHandlerSupportsCommandAmendments 验证 execpolicy 与 network amendment 选项精确回传。
func TestApprovalHandlerSupportsCommandAmendments(t *testing.T) {
	t.Parallel()

	guard := &fixedGenerationGuard{current: true}
	requester := &recordingPermissionRequester{response: selectedPermission("accept_execpolicy_amendment")}
	handler := newTestApprovalHandler(requester, guard)
	params := protocol.CommandExecutionRequestApprovalParams{
		ThreadID: "thread-1", TurnID: "turn-1", ItemID: "command-1",
		NetworkApprovalContext: &protocol.NetworkApprovalContext{
			Host: "registry.npmjs.org", Protocol: protocol.ProtocolEnum("https"),
		},
		ProposedExecpolicyAmendment: []string{"npm", "install"},
		ProposedNetworkPolicyAmendments: []protocol.NetworkPolicyAmendment{
			{Host: "registry.npmjs.org", Action: protocol.Allow},
			{Host: "blocked.example.test", Action: protocol.NetworkPolicyRuleActionDeny},
		},
	}
	response := handler.HandleCommand(context.Background(), params)
	decision := response.Decision.PolicyAmendmentCommandExecutionApprovalDecision
	if decision == nil || decision.AcceptWithExecpolicyAmendment == nil || len(decision.AcceptWithExecpolicyAmendment.ExecpolicyAmendment) != 2 {
		t.Fatalf("exec amendment response = %#v", response)
	}
	if got := requester.requests[0].Options; len(got) != 6 || got[1].Name != "Allow Host for Session" || got[1].Meta["permission"] == nil || got[2].OptionId != "accept_execpolicy_amendment" || got[2].Name != "Allow Commands Starting With `npm install`" || got[2].Meta["permission"] == nil || got[3].OptionId != "apply_network_policy_amendment:0" || got[3].Name != "Allow registry.npmjs.org in the Future" || got[3].Meta["permission"] == nil || got[4].OptionId != "apply_network_policy_amendment:1" || got[4].Kind != acp.PermissionOptionKindRejectAlways || got[5].OptionId != "reject_once" {
		t.Fatalf("command amendment options = %#v", got)
	}

	requester.response = selectedPermission("apply_network_policy_amendment:0")
	response = handler.HandleCommand(context.Background(), params)
	decision = response.Decision.PolicyAmendmentCommandExecutionApprovalDecision
	if decision == nil || decision.ApplyNetworkPolicyAmendment == nil || decision.ApplyNetworkPolicyAmendment.NetworkPolicyAmendment.Host != "registry.npmjs.org" {
		t.Fatalf("network amendment response = %#v", response)
	}
}

// TestApprovalHandlerFailsClosedForAllExceptionalPaths 验证取消、错误、非法 option、前后 stale 都拒绝。
func TestApprovalHandlerFailsClosedForAllExceptionalPaths(t *testing.T) {
	t.Parallel()

	commandParams := protocol.CommandExecutionRequestApprovalParams{
		ThreadID: "thread-1", TurnID: "turn-1", ItemID: "command-1",
	}
	for name, configure := range map[string]func(*recordingPermissionRequester, *fixedGenerationGuard){
		"cancelled": func(r *recordingPermissionRequester, _ *fixedGenerationGuard) {
			r.response = acp.RequestPermissionResponse{Outcome: acp.RequestPermissionOutcome{
				Cancelled: &acp.RequestPermissionOutcomeCancelled{Outcome: "cancelled"},
			}}
		},
		"request error": func(r *recordingPermissionRequester, _ *fixedGenerationGuard) {
			r.err = errors.New("client disconnected")
		},
		"unknown option": func(r *recordingPermissionRequester, _ *fixedGenerationGuard) {
			r.response = selectedPermission("unoffered-option")
		},
		"stale before": func(_ *recordingPermissionRequester, g *fixedGenerationGuard) {
			g.current = false
		},
		"stale after": func(r *recordingPermissionRequester, g *fixedGenerationGuard) {
			r.response = selectedPermission("allow_once")
			r.after = func() { g.current = false }
		},
	} {
		t.Run(name, func(t *testing.T) {
			guard := &fixedGenerationGuard{current: true}
			requester := &recordingPermissionRequester{}
			configure(requester, guard)
			response := newTestApprovalHandler(requester, guard).HandleCommand(context.Background(), commandParams)
			if response.Decision == nil || response.Decision.Enum == nil || *response.Decision.Enum != protocol.Cancel {
				t.Fatalf("fail-closed response = %#v，期望 cancel", response)
			}
		})
	}

	for name, requester := range map[string]permissionRequester{
		"missing handler": nil,
		"callback panic":  panickingPermissionRequester{},
	} {
		t.Run(name, func(t *testing.T) {
			response := newTestApprovalHandler(
				requester,
				&fixedGenerationGuard{current: true},
			).HandleCommand(context.Background(), commandParams)
			if response.Decision == nil || response.Decision.Enum == nil || *response.Decision.Enum != protocol.Cancel {
				t.Fatalf("fail-closed response = %#v，期望 cancel", response)
			}
		})
	}

	t.Run("mismatched identity", func(t *testing.T) {
		params := commandParams
		params.TurnID = "old-turn"
		requester := &recordingPermissionRequester{response: selectedPermission("allow_once")}
		response := newTestApprovalHandler(
			requester,
			&fixedGenerationGuard{current: true},
		).HandleCommand(context.Background(), params)
		if response.Decision == nil || response.Decision.Enum == nil || *response.Decision.Enum != protocol.Cancel {
			t.Fatalf("identity fail-closed response = %#v，期望 cancel", response)
		}
		if len(requester.requests) != 0 {
			t.Fatalf("旧 turn 发起了 %d 次 permission 请求", len(requester.requests))
		}
	})
}

// TestApprovalHandlerPermissionFailureIsStrictAndEmpty 验证 permissions 异常不会遗留任何授权。
func TestApprovalHandlerPermissionFailureIsStrictAndEmpty(t *testing.T) {
	t.Parallel()

	requester := &recordingPermissionRequester{response: selectedPermission("allow_once")}
	response := newTestApprovalHandler(requester, &fixedGenerationGuard{current: true}).HandlePermissions(
		context.Background(),
		protocol.PermissionsRequestApprovalParams{
			ThreadID: "thread-1", TurnID: "turn-1", ItemID: "permissions-1", Cwd: "/work",
		},
	)
	if response.Permissions.FileSystem != nil || response.Permissions.Network != nil || response.Scope == nil || *response.Scope != protocol.Turn {
		t.Fatalf("permissions fail-closed = %#v", response)
	}
	strict, ok := response.StrictAutoReview.Value()
	if !ok || !strict {
		t.Fatalf("strictAutoReview = %#v，期望显式 true", response.StrictAutoReview)
	}
}

// TestPermissionsOptionsPreserveDenyAndEntries 验证最新 typed permission profile 不被降级丢失。
func TestPermissionsOptionsPreserveDenyAndEntries(t *testing.T) {
	t.Parallel()

	path := "/private/file"
	params := protocol.PermissionsRequestApprovalParams{
		ThreadID: "thread-1", TurnID: "turn-1", ItemID: "permissions-entries", Cwd: "/work",
		Permissions: protocol.Permissions{
			Network: &protocol.NetworkClass{Enabled: acp.Ptr(false)},
			FileSystem: &protocol.FileSystemClass{Entries: []protocol.FileSystemEntry{{
				Access: protocol.AccessDeny,
				Path:   protocol.FileSystemPath{Type: protocol.Path, Path: &path},
			}}},
		},
	}
	request := permissionsPermissionRequest("session-1", params)
	permissionMeta := request.Options[0].Meta["permission"].(map[string]any)
	changes := permissionMeta["changes"].([]any)
	if len(changes) != 2 {
		t.Fatalf("permission changes = %#v，期望 network deny 与 filesystem deny", changes)
	}
	network := changes[0].(map[string]any)
	if network["type"] != "policy_rule" || network["ruleBehavior"] != "deny" {
		t.Fatalf("network deny change = %#v", network)
	}
	filesystem := changes[1].(map[string]any)
	if filesystem["type"] != "policy_rule" || filesystem["ruleBehavior"] != "deny" {
		t.Fatalf("filesystem deny change = %#v", filesystem)
	}
}

// selectedPermission 创建 ACP SDK selected outcome。
func selectedPermission(optionID acp.PermissionOptionId) acp.RequestPermissionResponse {
	return acp.RequestPermissionResponse{Outcome: acp.RequestPermissionOutcome{
		Selected: &acp.RequestPermissionOutcomeSelected{Outcome: "selected", OptionId: optionID},
	}}
}

// newTestApprovalHandler 创建绑定当前测试 generation 的审批处理器。
func newTestApprovalHandler(requester permissionRequester, guard generationGuard) *approvalHandler {
	return newApprovalHandler(requester, turnGeneration{
		SessionID: "session-1", ThreadID: "thread-1", TurnID: "turn-1", Generation: 7,
	}, guard, slog.Default())
}

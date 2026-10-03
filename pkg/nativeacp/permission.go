package nativeacp

import (
	"context"
	"time"

	acp "github.com/coder/acp-go-sdk"
)

// toolEvidenceRevision 以非零大小分配保证事件身份不被零大小指针规则合并。
type toolEvidenceRevision struct {
	// marker 只保证不同事件拥有独立地址，不保存工具或账号内容。
	marker byte
}

// requestPermission 收集标准执行证据，并让当前 CLI 自己决定是否需要补充审批策略。
func (agent *Agent) requestPermission(
	ctx context.Context,
	request acp.RequestPermissionRequest,
) (acp.RequestPermissionResponse, error) {
	agent.awaitToolEvidence(ctx, request)
	agent.mutex.Lock()
	prompt := agent.prompts[request.SessionId]
	detail := agent.toolDetails[request.ToolCall.ToolCallId]
	owner := agent.toolSessions[request.ToolCall.ToolCallId]
	turnCtx := agent.turnContexts[request.SessionId]
	active := agent.active[request.SessionId]
	turnOwner := agent.turnOwners[request.SessionId]
	revision := agent.toolRevisions[request.ToolCall.ToolCallId]
	session, sessionExists := agent.currentSessionLocked(request.SessionId)
	agent.mutex.Unlock()
	if turnCtx != nil {
		scoped, cancel := context.WithCancel(ctx)
		stop := context.AfterFunc(turnCtx, cancel)
		defer stop()
		defer cancel()
		ctx = scoped
	}
	if adapter := agent.config.PermissionAdapter; adapter != nil {
		evidence := PermissionEvidence{
			Request:   request,
			Detail:    detail,
			Prompt:    prompt,
			ToolOwner: owner,
			Active:    active,
			unchanged: func() bool {
				agent.mutex.Lock()
				defer agent.mutex.Unlock()
				current, exists := agent.currentSessionLocked(request.SessionId)
				return sessionExists && exists && current == session && turnCtx != nil && turnCtx.Err() == nil &&
					agent.active[request.SessionId] && agent.turnContexts[request.SessionId] == turnCtx &&
					agent.turnOwners[request.SessionId] == turnOwner &&
					agent.toolSessions[request.ToolCall.ToolCallId] == request.SessionId && revision != nil &&
					agent.toolRevisions[request.ToolCall.ToolCallId] == revision
			},
		}
		response, handled, err := adapter.Review(ctx, agent, evidence)
		if err != nil || handled {
			return response, err
		}
	}
	if ctx.Err() != nil {
		return acp.RequestPermissionResponse{Outcome: acp.NewRequestPermissionOutcomeCancelled()}, nil
	}
	return agent.RequestPermission(ctx, request)
}

// awaitToolEvidence 允许先到线上的工具通知完成入账；超时仍转人工，不凭缺失证据放行。
func (agent *Agent) awaitToolEvidence(ctx context.Context, request acp.RequestPermissionRequest) {
	if agent.config.PermissionAdapter == nil {
		return
	}
	wait, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
	defer cancel()
	for {
		agent.mutex.Lock()
		ready := !agent.active[request.SessionId] || agent.toolSessions[request.ToolCall.ToolCallId] == request.SessionId
		if agent.toolChanged == nil {
			agent.toolChanged = make(chan struct{})
		}
		changed := agent.toolChanged
		agent.mutex.Unlock()
		if ready {
			return
		}
		select {
		case <-changed:
		case <-wait.Done():
			return
		case <-agent.closed:
			return
		}
	}
}

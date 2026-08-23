package codex

import (
	"context"
	"fmt"

	"acp-go/agents/codex/protocol"
	acp "github.com/coder/acp-go-sdk"
)

// failClosedServerRequest 按生成协议变体返回审批拒绝安全值，绝不复制上游 DTO。
func failClosedServerRequest(request protocol.ServerRequest) (any, error) {
	switch request.(type) {
	case *protocol.CommandExecutionApprovalRequest:
		return failClosedCommandApproval(), nil
	case *protocol.FileChangeApprovalRequest:
		return failClosedFileApproval(), nil
	case *protocol.PermissionsApprovalRequest:
		return failClosedPermissionsApproval(), nil
	default:
		return nil, fmt.Errorf("unsupported Codex server request %T", request)
	}
}

// handleServerRequest 把 transport 解码的三类审批请求接到已验证 approvalHandler。
// 对应 upstream CodexAppServerClient approval handlers；连接或当前 turn 身份缺失时统一 fail closed。
func (a *Agent) handleServerRequest(ctx context.Context, request protocol.ServerRequest) (any, error) {
	requester := a.currentApprovalRequester()
	switch value := request.(type) {
	case *protocol.CommandExecutionApprovalRequest:
		generation, current := a.currentTurnGeneration(value.Params.ThreadID, value.Params.TurnID)
		if requester == nil || !current {
			return failClosedCommandApproval(), nil
		}
		return newApprovalHandler(requester, generation, a, a.logger).HandleCommand(ctx, value.Params), nil
	case *protocol.FileChangeApprovalRequest:
		generation, current := a.currentTurnGeneration(value.Params.ThreadID, value.Params.TurnID)
		if requester == nil || !current {
			return failClosedFileApproval(), nil
		}
		return newApprovalHandler(requester, generation, a, a.logger).HandleFile(ctx, value.Params), nil
	case *protocol.PermissionsApprovalRequest:
		generation, current := a.currentTurnGeneration(value.Params.ThreadID, value.Params.TurnID)
		if requester == nil || !current {
			return failClosedPermissionsApproval(), nil
		}
		return newApprovalHandler(requester, generation, a, a.logger).HandlePermissions(ctx, value.Params), nil
	default:
		return failClosedServerRequest(request)
	}
}

// currentApprovalRequester 返回 SDK connection 暴露的最小 permission 请求能力。
func (a *Agent) currentApprovalRequester() permissionRequester {
	a.connectionMu.RLock()
	defer a.connectionMu.RUnlock()
	return a.approvalRequester
}

// currentConnection 返回事件组件使用的 SDK connection 快照。
func (a *Agent) currentConnection() *acp.AgentSideConnection {
	a.connectionMu.RLock()
	defer a.connectionMu.RUnlock()
	return a.connection
}

// currentSessionUpdater 返回 event/history 当前可用的 SDK session/update 窄接口。
func (a *Agent) currentSessionUpdater() sessionUpdater {
	a.connectionMu.RLock()
	defer a.connectionMu.RUnlock()
	return a.sessionUpdater
}

// currentTurnGeneration 在 session、活动槽位、turn ID 与取消状态都匹配时构造不可变身份。
func (a *Agent) currentTurnGeneration(threadID, turnID string) (turnGeneration, bool) {
	state, ok := a.sessions.get(threadID)
	if !ok || !a.sessions.isCurrent(state) {
		return turnGeneration{}, false
	}
	state.mu.Lock()
	prompt := state.activePrompt
	state.mu.Unlock()
	if prompt == nil {
		return turnGeneration{}, false
	}
	activeTurnID, cancelled := prompt.currentTurn()
	if cancelled || activeTurnID == "" || activeTurnID != turnID {
		return turnGeneration{}, false
	}
	return turnGeneration{
		SessionID:  acp.SessionId(state.id),
		ThreadID:   state.id,
		TurnID:     activeTurnID,
		Generation: prompt.generation,
	}, true
}

// IsCurrent 实现 generationGuard，并在 permission 回调前后重新验证完整 runtime 身份。
func (a *Agent) IsCurrent(generation turnGeneration) bool {
	current, ok := a.currentTurnGeneration(generation.ThreadID, generation.TurnID)
	return ok && current == generation
}

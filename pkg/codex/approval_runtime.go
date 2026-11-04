package codex

import (
	"context"
	"fmt"

	"github.com/JieWaZi/acp-go/pkg/codex/protocol"
	acp "github.com/JieWaZi/acp-go/sdk"
)

// failClosedServerRequest 按生成协议变体返回审批拒绝安全值。
func failClosedServerRequest(request protocol.ServerRequest) (any, error) {
	switch request.(type) {
	case *protocol.CommandExecutionApprovalRequest:
		return failClosedCommandApproval(), nil
	case *protocol.FileChangeApprovalRequest:
		return failClosedFileApproval(), nil
	case *protocol.PermissionsApprovalRequest:
		return failClosedPermissionsApproval(), nil
	case *protocol.MCPServerElicitationRequest:
		return failClosedMCPServerElicitation(), nil
	case *protocol.ToolRequestUserInputRequest:
		return failClosedToolUserInput(), nil
	default:
		return nil, fmt.Errorf("unsupported Codex server request %T", request)
	}
}

// handleServerRequest dispatches only requests bound to a proven native turn.
func (a *Agent) handleServerRequest(ctx context.Context, request protocol.ServerRequest) (any, error) {
	requester := a.currentApprovalRequester()
	switch value := request.(type) {
	case *protocol.CommandExecutionApprovalRequest:
		generation, current := a.waitForTurnGeneration(ctx, value.Params.ThreadID, value.Params.TurnID)
		if requester == nil || !current {
			return failClosedCommandApproval(), nil
		}
		turnCtx, cancel, owned := a.turnElicitationContext(ctx, generation)
		if !owned {
			return failClosedCommandApproval(), nil
		}
		defer cancel()
		return newApprovalHandler(requester, generation, a, a.logger).HandleCommand(turnCtx, value.Params), nil
	case *protocol.FileChangeApprovalRequest:
		generation, current := a.waitForTurnGeneration(ctx, value.Params.ThreadID, value.Params.TurnID)
		if requester == nil || !current {
			return failClosedFileApproval(), nil
		}
		turnCtx, cancel, owned := a.turnElicitationContext(ctx, generation)
		if !owned {
			return failClosedFileApproval(), nil
		}
		defer cancel()
		return newApprovalHandler(requester, generation, a, a.logger).HandleFile(turnCtx, value.Params), nil
	case *protocol.PermissionsApprovalRequest:
		generation, current := a.waitForTurnGeneration(ctx, value.Params.ThreadID, value.Params.TurnID)
		if requester == nil || !current {
			return failClosedPermissionsApproval(), nil
		}
		turnCtx, cancel, owned := a.turnElicitationContext(ctx, generation)
		if !owned {
			return failClosedPermissionsApproval(), nil
		}
		defer cancel()
		return newApprovalHandler(requester, generation, a, a.logger).HandlePermissions(turnCtx, value.Params), nil
	case *protocol.MCPServerElicitationRequest:
		return a.handleMCPServerElicitation(ctx, value.Params), nil
	case *protocol.ToolRequestUserInputRequest:
		return a.handleToolUserInput(ctx, value.Params), nil
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

// waitForTurnGeneration parks an early server request outside the native reader.
// It can proceed after the ACK or verified user-message event binds the exact ID.
func (a *Agent) waitForTurnGeneration(ctx context.Context, threadID, turnID string) (turnGeneration, bool) {
	state, ok := a.sessions.get(threadID)
	if !ok {
		return turnGeneration{}, false
	}
	turn := sessionActiveTurn(state)
	if turn == nil || turn.kind == compactionTurn {
		return turnGeneration{}, false
	}
	if id := turn.confirmedTurnID(); id != "" && id != turnID {
		return turnGeneration{}, false
	}
	ctx, cancel := context.WithTimeout(ctx, appServerCleanupTimeout)
	defer cancel()
	select {
	case <-turn.turnStarted:
		generation, valid := a.currentTurnGeneration(threadID, turnID)
		return generation, valid && generation.Generation == turn.generation
	case <-turn.runCtx.Done():
	case <-ctx.Done():
	}
	return turnGeneration{}, false
}

// currentTurnGeneration 在 session、活动槽位、turn ID 与取消状态都匹配时构造不可变身份。
func (a *Agent) currentTurnGeneration(threadID, turnID string) (turnGeneration, bool) {
	state, ok := a.sessions.get(threadID)
	if !ok || !a.sessions.isCurrent(state) {
		return turnGeneration{}, false
	}
	state.mu.Lock()
	prompt := state.activeTurn
	state.mu.Unlock()
	if prompt == nil || prompt.kind == compactionTurn {
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

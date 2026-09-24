package claude

import (
	"context"

	"github.com/JieWaZi/acp-go/pkg/acpmeta"
	acp "github.com/coder/acp-go-sdk"
)

// UnstableForkSession 复用 Agent SDK 的原生离线分叉语义，再通过 CLI resume 打开独立身份。
func (a *Agent) UnstableForkSession(ctx context.Context, request acp.UnstableForkSessionRequest) (acp.UnstableForkSessionResponse, error) {
	if acpmeta.ReconcileOnly(request) {
		id, err := acpmeta.ReadForkReceipt(request)
		if err == nil {
			err = a.reconcileForkHistory(string(id), request.Cwd)
		}
		return acp.UnstableForkSessionResponse{SessionId: id}, err
	}

	if err := a.requireInitialized(); err != nil {
		return acp.UnstableForkSessionResponse{}, err
	}
	if acpmeta.ForkPosition(request.Meta) != "" {
		return acp.UnstableForkSessionResponse{}, acp.NewInvalidParams(map[string]any{"message": "historical fork position is not supported"})
	}
	if source, exists := a.sessions.get(string(request.SessionId)); exists {
		if !source.forkMu.TryLock() {
			return acp.UnstableForkSessionResponse{}, acp.NewInvalidParams(map[string]any{"message": "source session is active"})
		}
		defer source.forkMu.Unlock()
		source.mu.Lock()
		busy := source.active != nil || len(source.turns) > 0 || source.closed
		source.mu.Unlock()
		if busy {
			return acp.UnstableForkSessionResponse{}, acp.NewInvalidParams(map[string]any{"message": "source session is active"})
		}
	}
	if _, err := findHistoryPath(string(request.SessionId), a.environment); err != nil {
		return acp.UnstableForkSessionResponse{}, err
	}
	id, err := a.idGenerator()
	if err != nil {
		return acp.UnstableForkSessionResponse{}, err
	}
	if id == string(request.SessionId) {
		return acp.UnstableForkSessionResponse{}, acp.NewInternalError(map[string]any{"message": "fork identity collision"})
	}
	servers, err := acpmeta.ForkMCPServers(request.McpServers)
	if err != nil {
		return acp.UnstableForkSessionResponse{}, err
	}
	if err = acpmeta.BeginForkReceipt(request); err != nil {
		return acp.UnstableForkSessionResponse{}, err
	}
	if err = a.persistForkHistory(string(request.SessionId), id, request.Cwd); err != nil {
		return acp.UnstableForkSessionResponse{}, err
	}
	if err = acpmeta.WriteForkReceipt(request, acp.SessionId(id)); err != nil {
		return acp.UnstableForkSessionResponse{}, err
	}
	session, err := a.openSession(ctx, openSessionRequest{SessionID: id, Resume: true, CWD: request.Cwd, AdditionalDirectories: request.AdditionalDirectories, MCPServers: servers})
	if err != nil {
		return acp.UnstableForkSessionResponse{}, err
	}
	a.scheduleAvailableCommandsUpdate(session.id)
	if err = acpmeta.WriteForkReceipt(request, acp.SessionId(session.id)); err != nil {
		return acp.UnstableForkSessionResponse{}, err
	}
	return acpmeta.ForkResponse(request.SessionId, acp.NewSessionResponse{SessionId: acp.SessionId(session.id), ConfigOptions: session.configOptions(), Modes: session.modeState()})
}

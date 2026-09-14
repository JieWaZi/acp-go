package nativeacp

import (
	"context"

	acp "github.com/coder/acp-go-sdk"
)

// LoadSessionConfiguration 恢复配置而不向宿主回放历史，用于适配器驱动的标准 Resume。
func (agent *Agent) LoadSessionConfiguration(
	ctx context.Context,
	request acp.LoadSessionRequest,
) (acp.LoadSessionResponse, error) {
	agent.mutex.Lock()
	if agent.silentLoads == nil {
		agent.silentLoads = map[acp.SessionId]bool{}
	}
	agent.silentLoads[request.SessionId] = true
	agent.mutex.Unlock()
	defer func() {
		agent.mutex.Lock()
		delete(agent.silentLoads, request.SessionId)
		agent.mutex.Unlock()
	}()
	return agent.LoadSession(ctx, request)
}

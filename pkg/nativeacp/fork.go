package nativeacp

import (
	"context"

	"github.com/JieWaZi/acp-go/pkg/acpmeta"
	acp "github.com/coder/acp-go-sdk"
)

// UnstableForkSession 转发原生分叉并复用配置规范化，不把分叉命令交给 Prompt。
func (agent *Agent) UnstableForkSession(ctx context.Context, request acp.UnstableForkSessionRequest) (acp.UnstableForkSessionResponse, error) {
	if acpmeta.ReconcileOnly(request) {
		return acp.UnstableForkSessionResponse{}, acpmeta.ErrForkUnconfirmed
	}
	if acpmeta.ForkPosition(request.Meta) != "" {
		return acp.UnstableForkSessionResponse{}, acp.NewInvalidParams(map[string]any{"message": "native ACP does not advertise historical fork positions"})
	}
	response, err := sendNativeRequest[nativeSessionResponse](agent, ctx, "session/fork", request)
	if err != nil {
		return acp.UnstableForkSessionResponse{}, err
	}
	if _, err = acpmeta.ForkResponse(request.SessionId, response.NewSessionResponse); err != nil {
		return acp.UnstableForkSessionResponse{}, err
	}
	if err = agent.normalizeSession(ctx, &response, response.SessionId, request.Cwd); err != nil {
		return acp.UnstableForkSessionResponse{}, err
	}
	return acpmeta.ForkResponse(request.SessionId, response.NewSessionResponse)
}

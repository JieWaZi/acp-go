package grok

import (
	"context"
	acp "github.com/coder/acp-go-sdk"
	"maps"
)

// permissionMetadata 映射统一模式至官方会话元数据；native requirement gate 保持最高优先级。
func permissionMetadata(meta map[string]any, mode string) map[string]any {
	result := maps.Clone(meta)
	if result == nil {
		result = map[string]any{}
	}
	result["autoMode"] = mode == "auto"
	result["yoloMode"] = mode == "full-access"
	return result
}

// NewSession 使用原生分类器或审批模式创建会话，不把 auto 当作全权限。
func (agent *Agent) NewSession(ctx context.Context, request acp.NewSessionRequest) (acp.NewSessionResponse, error) {
	request.Meta = permissionMetadata(request.Meta, agent.permissionMode)
	return agent.Agent.NewSession(ctx, request)
}

// LoadSession 恢复时仍明确使用所选择的原生权限模式。
func (agent *Agent) LoadSession(ctx context.Context, request acp.LoadSessionRequest) (acp.LoadSessionResponse, error) {
	request.Meta = permissionMetadata(request.Meta, agent.permissionMode)
	return agent.Agent.LoadSession(ctx, request)
}

// ResumeSession 只使用原生无历史恢复方法，保持 CLI 自身声明和错误。
func (agent *Agent) ResumeSession(
	ctx context.Context,
	request acp.ResumeSessionRequest,
) (acp.ResumeSessionResponse, error) {
	request.Meta = permissionMetadata(request.Meta, agent.permissionMode)
	return agent.Agent.ResumeSession(ctx, request)
}

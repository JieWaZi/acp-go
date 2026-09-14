package nativeacp

import (
	"context"
	"encoding/json"

	acp "github.com/coder/acp-go-sdk"
)

// CallbackBridge 只暴露厂商回调接入 ACP 宿主所需的稳定能力。
type CallbackBridge interface {
	// ResolveInteractionSession 根据显式会话或工具归属解析唯一活跃会话。
	ResolveInteractionSession(toolCallID, sessionID string) acp.SessionId
	// UpdateSession 向宿主发布标准会话更新。
	UpdateSession(context.Context, acp.SessionNotification) error
	// RequestPermission 向宿主发起标准审批。
	RequestPermission(
		context.Context,
		acp.RequestPermissionRequest,
	) (acp.RequestPermissionResponse, error)
	// CreateElicitation 向宿主发起结构化交互问答。
	CreateElicitation(
		context.Context,
		acp.UnstableCreateElicitationRequest,
	) (acp.UnstableCreateElicitationResponse, error)
}

// CallbackAdapter 把某个 CLI 的私有反向请求封装在其自身适配器中。
type CallbackAdapter interface {
	// PrepareInitialize 为该 CLI 声明其私有协议需要的客户端能力。
	PrepareInitialize(*acp.InitializeRequest)
	// HandleCallback 处理该 CLI 的私有方法；handled=false 时继续标准分发。
	HandleCallback(
		context.Context,
		CallbackBridge,
		string,
		json.RawMessage,
	) (result any, handled bool, err error)
}

// SessionBridge 只暴露厂商会话配置适配所需的标准请求能力。
type SessionBridge interface {
	// SendRequest 调用外部 Agent 方法，并把 JSON 结果解码到 response。
	SendRequest(context.Context, string, any, any) error
}

// SessionSnapshot 汇集外部 Agent 创建或恢复会话时返回的配置事实。
type SessionSnapshot struct {
	// ConfigOptions 是标准 ACP 配置目录。
	ConfigOptions []acp.SessionConfigOption
	// Modes 是标准 ACP 会话模式目录。
	Modes *acp.SessionModeState
	// Models 是旧实现附带的厂商模型目录原文。
	Models json.RawMessage
	// WorkingDirectory 是该会话经过宿主传入的工作目录。
	WorkingDirectory string
}

// SessionAdapter 把某个 CLI 的非标准配置目录封装在其自身适配器中。
type SessionAdapter interface {
	// NormalizeSession 保存一个会话的厂商配置状态，并返回对宿主统一的选项。
	NormalizeSession(
		context.Context,
		SessionBridge,
		acp.SessionId,
		SessionSnapshot,
	) ([]acp.SessionConfigOption, error)
	// NormalizeUpdate 同步上游动态配置更新，并返回对宿主统一的选项。
	NormalizeUpdate(acp.SessionId, []acp.SessionConfigOption) []acp.SessionConfigOption
	// SetConfigOption 处理厂商配置写入；handled=false 时使用标准通用路径。
	SetConfigOption(
		context.Context,
		SessionBridge,
		acp.SetSessionConfigOptionRequest,
	) (response acp.SetSessionConfigOptionResponse, handled bool, err error)
	// ForgetSession 删除已关闭会话的厂商配置状态。
	ForgetSession(acp.SessionId)
}

// PermissionEvidence 汇集执行前审批可验证的当前轮证据。
type PermissionEvidence struct {
	// Request 是上游提交的标准 ACP 审批请求。
	Request acp.RequestPermissionRequest
	// Detail 是此前工具通知累计出的标题、类型、输入与内容。
	Detail acp.ToolCallUpdate
	// Prompt 是当前执行轮的完整用户输入。
	Prompt []acp.ContentBlock
	// ToolOwner 是该工具通知已经确认的所属会话。
	ToolOwner acp.SessionId
	// Active 表示请求所属执行轮仍然有效。
	Active bool
}

// PermissionBridge 只暴露厂商审批适配器发布审查结果所需的宿主能力。
type PermissionBridge interface {
	// UpdateSession 向宿主发布标准会话更新。
	UpdateSession(context.Context, acp.SessionNotification) error
}

// PermissionAdapter 把某个 CLI 的非标准审批策略封装在其自身目录中。
type PermissionAdapter interface {
	// Review 尝试处理审批；handled=false 时由标准宿主继续决定。
	Review(
		context.Context,
		PermissionBridge,
		PermissionEvidence,
	) (response acp.RequestPermissionResponse, handled bool, err error)
}

// ResolveInteractionSession 实现 CallbackBridge，不向适配器暴露 Agent 内部状态。
func (agent *Agent) ResolveInteractionSession(toolCallID, sessionID string) acp.SessionId {
	return agent.interactionSession(toolCallID, sessionID)
}

// UpdateSession 实现 CallbackBridge，统一使用已绑定的 ACP 宿主连接。
func (agent *Agent) UpdateSession(ctx context.Context, request acp.SessionNotification) error {
	return agent.host.SessionUpdate(ctx, request)
}

// RequestPermission 实现 CallbackBridge，统一使用已绑定的 ACP 宿主连接。
func (agent *Agent) RequestPermission(
	ctx context.Context,
	request acp.RequestPermissionRequest,
) (acp.RequestPermissionResponse, error) {
	return agent.host.RequestPermission(ctx, request)
}

// CreateElicitation 实现 CallbackBridge，统一使用已绑定的 ACP 宿主连接。
func (agent *Agent) CreateElicitation(
	ctx context.Context,
	request acp.UnstableCreateElicitationRequest,
) (acp.UnstableCreateElicitationResponse, error) {
	return agent.host.UnstableCreateElicitation(ctx, request)
}

// SendRequest 实现 SessionBridge，协议编解码仍由 nativeacp 统一拥有。
func (agent *Agent) SendRequest(ctx context.Context, method string, request any, response any) error {
	raw, err := acp.SendRequest[json.RawMessage](agent.conn, ctx, method, request)
	if err != nil {
		return err
	}
	if response == nil {
		return nil
	}
	return json.Unmarshal(raw, response)
}

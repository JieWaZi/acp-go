// Package codex 提供 Codex Adapter 的 ACP Agent 接入点。
package codex

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	acp "github.com/coder/acp-go-sdk"
)

const (
	agentName    = "codex"
	agentTitle   = "Codex"
	agentVersion = "development"
)

var (
	// ErrInvalidLogger 表示 Codex Adapter 缺少进程级诊断 logger。
	ErrInvalidLogger = errors.New("invalid codex logger")
	// ErrRuntimeUnavailable 表示 framework foundation 尚未接入 Codex app-server runtime。
	// 后续 runtime 子变更会用真实 thread/turn 实现替换返回此错误的必选操作。
	ErrRuntimeUnavailable = errors.New("codex runtime unavailable")
)

// Agent 是 Codex Adapter 的 ACP 协议入口。
// Foundation 阶段只提供诚实的 initialize 身份，其余业务由后续 Codex 子变更实现。
type Agent struct {
	// logger 是后续 app-server runtime、事件和异常路径共用的进程级诊断入口。
	logger *slog.Logger
}

var _ acp.Agent = (*Agent)(nil)

// NewAgent 使用显式 logger 创建一个不声明未实现能力的 Codex Agent。
func NewAgent(logger *slog.Logger) (*Agent, error) {
	if logger == nil {
		return nil, fmt.Errorf("creating codex agent: %w", ErrInvalidLogger)
	}
	return &Agent{logger: logger}, nil
}

// Authenticate 处理认证请求；foundation 阶段未声明认证能力。
func (a *Agent) Authenticate(context.Context, acp.AuthenticateRequest) (acp.AuthenticateResponse, error) {
	return acp.AuthenticateResponse{}, acp.NewMethodNotFound(acp.AgentMethodAuthenticate)
}

// Initialize 返回稳定协议版本和最小 Agent 身份，不提前声明后续 runtime 能力。
func (a *Agent) Initialize(context.Context, acp.InitializeRequest) (acp.InitializeResponse, error) {
	a.logger.Debug("received acp initialize request")
	title := agentTitle
	return acp.InitializeResponse{
		ProtocolVersion: acp.ProtocolVersionNumber,
		AgentInfo: &acp.Implementation{
			Name:    agentName,
			Title:   &title,
			Version: agentVersion,
		},
	}, nil
}

// Logout 处理退出认证请求；foundation 阶段未声明认证能力。
func (a *Agent) Logout(context.Context, acp.LogoutRequest) (acp.LogoutResponse, error) {
	return acp.LogoutResponse{}, acp.NewMethodNotFound(acp.AgentMethodLogout)
}

// Cancel 处理会话取消；没有 runtime 活动时该操作保持幂等。
func (a *Agent) Cancel(context.Context, acp.CancelNotification) error {
	return nil
}

// CloseSession 处理会话关闭；foundation 阶段未声明关闭能力。
func (a *Agent) CloseSession(context.Context, acp.CloseSessionRequest) (acp.CloseSessionResponse, error) {
	return acp.CloseSessionResponse{}, acp.NewMethodNotFound(acp.AgentMethodSessionClose)
}

// ListSessions 处理会话列表请求；foundation 阶段未声明列表能力。
func (a *Agent) ListSessions(context.Context, acp.ListSessionsRequest) (acp.ListSessionsResponse, error) {
	return acp.ListSessionsResponse{}, acp.NewMethodNotFound(acp.AgentMethodSessionList)
}

// NewSession 拒绝在 app-server runtime 接入前创建虚假会话。
func (a *Agent) NewSession(context.Context, acp.NewSessionRequest) (acp.NewSessionResponse, error) {
	return acp.NewSessionResponse{}, runtimeUnavailable(acp.AgentMethodSessionNew)
}

// Prompt 拒绝在 app-server runtime 接入前伪造 prompt 成功结果。
func (a *Agent) Prompt(context.Context, acp.PromptRequest) (acp.PromptResponse, error) {
	return acp.PromptResponse{}, runtimeUnavailable(acp.AgentMethodSessionPrompt)
}

// ResumeSession 处理会话恢复请求；foundation 阶段未声明恢复能力。
func (a *Agent) ResumeSession(context.Context, acp.ResumeSessionRequest) (acp.ResumeSessionResponse, error) {
	return acp.ResumeSessionResponse{}, acp.NewMethodNotFound(acp.AgentMethodSessionResume)
}

// SetSessionConfigOption 处理配置请求；foundation 阶段未声明会话配置能力。
func (a *Agent) SetSessionConfigOption(
	context.Context,
	acp.SetSessionConfigOptionRequest,
) (acp.SetSessionConfigOptionResponse, error) {
	return acp.SetSessionConfigOptionResponse{}, acp.NewMethodNotFound(acp.AgentMethodSessionSetConfigOption)
}

// SetSessionMode 处理模式请求；foundation 阶段未声明会话模式能力。
func (a *Agent) SetSessionMode(context.Context, acp.SetSessionModeRequest) (acp.SetSessionModeResponse, error) {
	return acp.SetSessionModeResponse{}, acp.NewMethodNotFound(acp.AgentMethodSessionSetMode)
}

// runtimeUnavailable 为必选协议操作保留可检查的根因，并附加具体方法上下文。
func runtimeUnavailable(method string) error {
	return fmt.Errorf("handling %s: %w", method, ErrRuntimeUnavailable)
}

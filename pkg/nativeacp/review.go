package nativeacp

import (
	"context"
	"errors"

	"github.com/JieWaZi/acp-go/pkg/autoreview"
	acp "github.com/coder/acp-go-sdk"
)

// SessionContext 是不含配置树或凭据的不可变会话快照。
type SessionContext struct {
	// generation 是不可访问的执行代身份，只用于阻止恢复后迟到授权。
	generation *sessionOptions
	// WorkingDirectory 是原生会话 cwd。
	WorkingDirectory string
	// Model 是宿主当前模型 ID。
	Model string
}

// ReviewPermissionConfig 保存 opt-in 风险审查器，不影响旧适配器。
type ReviewPermissionConfig struct {
	// Mode 选择 default、auto 或 full-access。
	Mode string
	// Reviewer 由供应商提供真正无工具的同模型执行器。
	Reviewer func(context.Context, autoreview.Request) (autoreview.Decision, error)
	// CurrentSession 返回当前执行代的 cwd 和模型。
	CurrentSession func(acp.SessionId) (SessionContext, bool)
}

// reviewPermissionAdapter 实现共享的审计与一次授权规则。
type reviewPermissionAdapter struct {
	// config 保存不可变审查接入配置。
	config ReviewPermissionConfig
}

// NewReviewPermissionAdapter 创建 opt-in 共享门禁。
func NewReviewPermissionAdapter(config ReviewPermissionConfig) PermissionAdapter {
	return &reviewPermissionAdapter{config: config}
}

// CurrentSession 返回脱离内部配置对象的值快照。
func (agent *Agent) CurrentSession(id acp.SessionId) (SessionContext, bool) {
	agent.mutex.Lock()
	defer agent.mutex.Unlock()
	state := agent.sessions[id]
	if state == nil {
		return SessionContext{}, false
	}
	snapshot := SessionContext{WorkingDirectory: state.workingDirectory, generation: state}
	for _, option := range state.options {
		if option.Select != nil && option.Select.Id == "model" {
			snapshot.Model = string(option.Select.CurrentValue)
			break
		}
	}
	return snapshot, true
}

// Review 按完整执行证据尝试审查。
func (adapter *reviewPermissionAdapter) Review(
	ctx context.Context,
	bridge PermissionBridge,
	evidence PermissionEvidence,
) (acp.RequestPermissionResponse, bool, error) {
	if adapter.config.Mode != "auto" {
		return acp.RequestPermissionResponse{}, false, nil
	}
	tool := evidence.Request.ToolCall
	if tool.RawInput == nil {
		tool.RawInput = evidence.Detail.RawInput
	}
	if tool.Kind == nil {
		tool.Kind = evidence.Detail.Kind
	}
	if tool.Title == nil {
		tool.Title = evidence.Detail.Title
	}
	decision := autoreview.Decision{}
	reviewErr := errors.New("insufficient permission review evidence")
	var session SessionContext
	exists := false
	if adapter.config.CurrentSession != nil {
		session, exists = adapter.config.CurrentSession(evidence.Request.SessionId)
	}
	complete := exists && evidence.Active && evidence.ToolOwner == evidence.Request.SessionId &&
		tool.RawInput != nil && len(evidence.Prompt) > 0
	if complete && adapter.config.Reviewer != nil {
		decision, reviewErr = adapter.config.Reviewer(ctx, autoreview.Request{
			WorkingDirectory: session.WorkingDirectory, Model: session.Model, Prompt: evidence.Prompt, Tool: tool,
		})
	}
	if ctx.Err() != nil {
		return acp.RequestPermissionResponse{Outcome: acp.NewRequestPermissionOutcomeCancelled()}, true, nil
	}
	update := acp.SessionNotification{
		SessionId: evidence.Request.SessionId,
		Update: acp.SessionUpdate{ToolCallUpdate: &acp.SessionToolCallUpdate{
			ToolCallId: tool.ToolCallId,
			Meta:       map[string]any{"acp-go/permission-review": autoreview.Metadata(decision, reviewErr != nil)},
		}},
	}
	if err := bridge.UpdateSession(ctx, update); err != nil {
		return acp.RequestPermissionResponse{Outcome: acp.NewRequestPermissionOutcomeCancelled()}, true, nil
	}
	if reviewErr == nil && decision.Outcome == "allow" && ctx.Err() == nil && evidence.Active {
		if current, exists := adapter.config.CurrentSession(evidence.Request.SessionId); exists && current == session {
			for _, option := range evidence.Request.Options {
				if option.Kind == acp.PermissionOptionKindAllowOnce {
					return acp.RequestPermissionResponse{Outcome: acp.NewRequestPermissionOutcomeSelected(option.OptionId)}, true, nil
				}
			}
		}
	}
	return acp.RequestPermissionResponse{}, false, nil
}

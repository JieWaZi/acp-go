package nativeacp

import (
	"context"
	"encoding/json"

	"github.com/JieWaZi/acp-go/pkg/autoreview"
	acp "github.com/coder/acp-go-sdk"
)

// requestPermission 在旧版上游的执行前门禁内补充自动审查，其余请求原样交给宿主。
func (agent *Agent) requestPermission(ctx context.Context, request acp.RequestPermissionRequest) (acp.RequestPermissionResponse, error) {
	agent.mutex.Lock()
	state := agent.sessions[request.SessionId]
	legacy := state != nil && state.legacyPermissions
	prompt := agent.prompts[request.SessionId]
	detail := agent.toolDetails[request.ToolCall.ToolCallId]
	owner := agent.toolSessions[request.ToolCall.ToolCallId]
	turnCtx := agent.turnContexts[request.SessionId]
	cwd, model := "", ""
	if state != nil {
		cwd = state.cwd
		for _, option := range state.options {
			if option.Select != nil && option.Select.Id == "model" {
				model = string(option.Select.CurrentValue)
			}
		}
	}
	agent.mutex.Unlock()
	if turnCtx != nil {
		scoped, cancel := context.WithCancel(ctx)
		stop := context.AfterFunc(turnCtx, cancel)
		defer stop()
		defer cancel()
		ctx = scoped
	}
	if reviewer := agent.config.LegacyPermissionReviewer; reviewer != nil && legacy && owner == request.SessionId {
		tool := request.ToolCall
		if tool.RawInput == nil {
			tool.RawInput = detail.RawInput
			// Python Kimi 把完整累计参数放在工具 content 中，而不是 rawInput。
			// 只接受可完整解码的 JSON 对象；截断或混合内容不能成为自动授权依据。
			if tool.RawInput == nil && len(detail.Content) == 1 && detail.Content[0].Content != nil && detail.Content[0].Content.Content.Text != nil {
				var input map[string]any
				if json.Unmarshal([]byte(detail.Content[0].Content.Content.Text.Text), &input) == nil && input != nil {
					tool.RawInput = input
				}
			}
		}
		if tool.Title == nil {
			tool.Title = detail.Title
		}
		if tool.Kind == nil {
			tool.Kind = detail.Kind
		}
		decision, err := reviewer(ctx, autoreview.Request{WorkingDirectory: cwd, Model: model, Prompt: prompt, Tool: tool})
		if err != nil {
			agent.config.Logger.Debug("permission review deferred to host", "error", err)
		}
		if ctx.Err() == nil {
			if updateErr := agent.host.SessionUpdate(ctx, acp.SessionNotification{SessionId: request.SessionId, Update: acp.SessionUpdate{ToolCallUpdate: &acp.SessionToolCallUpdate{ToolCallId: request.ToolCall.ToolCallId, Meta: map[string]any{"acp-go/permission-review": autoreview.Metadata(decision, err != nil)}}}}); updateErr != nil {
				return acp.RequestPermissionResponse{Outcome: acp.NewRequestPermissionOutcomeCancelled()}, nil
			}
		}
		if err == nil && decision.Outcome == "allow" && ctx.Err() == nil {
			agent.mutex.Lock()
			active := agent.active[request.SessionId]
			agent.mutex.Unlock()
			if active {
				for _, option := range request.Options {
					if option.Kind == acp.PermissionOptionKindAllowOnce {
						return acp.RequestPermissionResponse{Outcome: acp.NewRequestPermissionOutcomeSelected(option.OptionId)}, nil
					}
				}
			}
		}
	}
	if ctx.Err() != nil {
		return acp.RequestPermissionResponse{Outcome: acp.NewRequestPermissionOutcomeCancelled()}, nil
	}
	return agent.host.RequestPermission(ctx, request)
}

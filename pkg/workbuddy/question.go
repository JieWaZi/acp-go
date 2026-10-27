package workbuddy

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/JieWaZi/acp-go/pkg/nativeacp"
	"github.com/JieWaZi/acp-go/pkg/userinput"
	acp "github.com/JieWaZi/acp-go/sdk"
)

// questionAdapter 将公开的 CodeBuddy 问答请求转换为统一 ACP 表单。
type questionAdapter struct{}

// PrepareInitialize 仅在宿主支持表单时声明原生公开问答能力，不添加身份元数据。
func (questionAdapter) PrepareInitialize(request *acp.InitializeRequest) {
	if request.ClientCapabilities.Elicitation == nil || request.ClientCapabilities.Elicitation.Form == nil {
		return
	}
	meta := make(map[string]any, len(request.ClientCapabilities.Meta)+1)
	for key, value := range request.ClientCapabilities.Meta {
		meta[key] = value
	}
	capabilities := map[string]any{}
	if original, ok := meta["codebuddy.ai"].(map[string]any); ok {
		for key, value := range original {
			capabilities[key] = value
		}
	}
	capabilities["question"] = request.ClientCapabilities.Elicitation != nil && request.ClientCapabilities.Elicitation.Form != nil
	meta["codebuddy.ai"] = capabilities
	request.ClientCapabilities.Meta = meta
}

// questionRequester 把统一问答端口绑定到当前原生回调桥。
type questionRequester struct {
	// bridge 只提供已绑定宿主的标准交互入口。
	bridge nativeacp.CallbackBridge
}

// UnstableCreateElicitation 复用统一表单 Schema、选项校验和自由回答语义。
func (requester questionRequester) UnstableCreateElicitation(ctx context.Context, request acp.UnstableCreateElicitationRequest) (acp.UnstableCreateElicitationResponse, error) {
	return requester.bridge.CreateElicitation(ctx, request)
}

// HandleCallback 仅处理公开 question 方法，其他公共扩展继续原样转发。
func (questionAdapter) HandleCallback(ctx context.Context, bridge nativeacp.CallbackBridge, method string, data json.RawMessage) (any, bool, error) {
	if method != "_codebuddy.ai/question" {
		return nil, false, nil
	}
	var request struct {
		// SessionID 是提问所属的显式原生会话。
		SessionID string `json:"sessionId"`
		// ToolCallID 是产生提问的工具身份。
		ToolCallID string `json:"toolCallId"`
		// InputType 区分结构化问答与未知交互格式。
		InputType string `json:"inputType"`
		// Schema 保存问题、选项和稳定问题标识。
		Schema userinput.Questions `json:"schema"`
	}
	if len(data) > 65536 || json.Unmarshal(data, &request) != nil || request.InputType != "question" {
		return nil, true, acp.NewInvalidParams(nil)
	}
	for _, question := range request.Schema.Questions {
		if question.ID == "" {
			return nil, true, acp.NewInvalidParams(nil)
		}
	}
	cancelled := map[string]any{"outcome": "cancelled"}
	provider, ok := bridge.(interface {
		// ResolveInteractionContext 一次取得会话和所属执行，不能按会话重新查询下一轮。
		ResolveInteractionContext(string, string) (acp.SessionId, context.Context)
	})
	if !ok {
		return cancelled, true, nil
	}
	sid, turn := provider.ResolveInteractionContext(request.ToolCallID, request.SessionID)
	if sid == "" || ctx.Err() != nil || turn == nil || turn.Err() != nil {
		return cancelled, true, nil
	}
	ctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(turn, cancel)
	defer stop()
	defer cancel()
	result, err := userinput.Ask(ctx, questionRequester{bridge: bridge}, sid, request.Schema)
	if ctx.Err() != nil || turn.Err() != nil {
		return cancelled, true, nil
	}
	if err != nil {
		return nil, true, err
	}
	if result.Action != "accept" {
		return cancelled, true, nil
	}
	return map[string]any{"outcome": "submitted", "answers": result.Answers}, true, nil
}

// Review 将新版 CLI 审批中携带的原生问题转成表单；普通工具仍交由标准审批处理。
func (questionAdapter) Review(ctx context.Context, bridge nativeacp.PermissionBridge, evidence nativeacp.PermissionEvidence) (acp.RequestPermissionResponse, bool, error) {
	request := evidence.Request
	if request.ToolCall.Meta["codebuddy.ai/toolName"] != "AskUserQuestion" {
		return acp.RequestPermissionResponse{}, false, nil
	}
	cancelled := acp.RequestPermissionResponse{Outcome: acp.NewRequestPermissionOutcomeCancelled()}
	host, ok := bridge.(interface {
		nativeacp.CallbackBridge
		nativeacp.SessionBridge
		// ResolveInteractionContext 取得问题所属执行的取消信号。
		ResolveInteractionContext(string, string) (acp.SessionId, context.Context)
	})
	if !ok || !evidence.Active || evidence.ToolOwner != request.SessionId || ctx.Err() != nil {
		return cancelled, true, nil
	}
	sid, turn := host.ResolveInteractionContext(string(request.ToolCall.ToolCallId), string(request.SessionId))
	if sid != request.SessionId || turn == nil || turn.Err() != nil {
		return cancelled, true, nil
	}
	ctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(turn, cancel)
	defer stop()
	defer cancel()
	input := request.ToolCall.RawInput
	if input == nil {
		input = evidence.Detail.RawInput
	}
	encoded, err := json.Marshal(input)
	if err != nil {
		return cancelled, true, err
	}
	var questions userinput.Questions
	if len(encoded) > 65536 || json.Unmarshal(encoded, &questions) != nil {
		return cancelled, true, acp.NewInvalidParams(nil)
	}
	// 原生 AskService 按问题正文收答案，与旧 question 回调使用的 q_N 身份分开。
	for i := range questions.Questions {
		questions.Questions[i].ID = ""
	}
	result, err := userinput.Ask(ctx, questionRequester{bridge: host}, sid, questions)
	if err != nil {
		return cancelled, true, err
	}
	if ctx.Err() != nil || turn.Err() != nil {
		return cancelled, true, nil
	}
	decision := "deny"
	var answers map[string]any
	if result.Action == "accept" {
		decision, answers = "allow", result.Answers
	}
	var resolved struct {
		// Resolved 表示原生问题确已接收答案或拒答，不能把排队视为完成。
		Resolved bool `json:"resolved"`
	}
	err = host.SendRequest(ctx, "_codebuddy.ai/resolveInterruption", map[string]any{
		"sessionId": sid, "toolCallId": request.ToolCall.ToolCallId, "decision": decision, "answers": answers,
	}, &resolved)
	if err != nil {
		return cancelled, true, err
	}
	if !resolved.Resolved {
		return cancelled, true, errors.New("WorkBuddy question resolution was not accepted")
	}
	// resolveInterruption 已完成交互；取消原审批回执，避免再次放行或记住问答权限。
	return cancelled, true, nil
}

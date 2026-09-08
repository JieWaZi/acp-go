package nativeacp

import (
	"context"
	"encoding/json"

	acp "github.com/coder/acp-go-sdk"
)

// cursorInteractionRequest 保留 Cursor 文档定义的交互字段。
type cursorInteractionRequest struct {
	// SessionID 是部分实现附加的会话标识。
	SessionID string `json:"sessionId"`
	// ToolCallID 将交互关联到此前的工具更新。
	ToolCallID string `json:"toolCallId"`
	// Title 是问答标题。
	Title string `json:"title"`
	// Questions 是按原始顺序展示的问题。
	Questions []cursorQuestion `json:"questions"`
	// Name 是计划名称。
	Name string `json:"name"`
	// Plan 是需要用户明确批准的完整计划。
	Plan string `json:"plan"`
}

// cursorQuestion 表达一个单选或多选问题。
type cursorQuestion struct {
	// ID 是返回答案时的原始问题标识。
	ID string `json:"id"`
	// Prompt 是问题正文。
	Prompt string `json:"prompt"`
	// Options 是上游公开的选项。
	Options []cursorOption `json:"options"`
	// AllowMultiple 表示是否允许选择多个答案。
	AllowMultiple bool `json:"allowMultiple"`
}

// cursorOption 是问题选项的身份和展示标签。
type cursorOption struct {
	// ID 是回传给 Cursor 的选项标识。
	ID string `json:"id"`
	// Label 是用户可读的选项名称。
	Label string `json:"label"`
}

// cursorOutcome 构造 Cursor 文档规定的嵌套交互结果。
func cursorOutcome(value string) map[string]any {
	return map[string]any{"outcome": map[string]any{"outcome": value}}
}

// cursorInteraction 把非标准 Cursor 阻塞请求接到现有 ACP 表单与审批接口。
func (agent *Agent) cursorInteraction(ctx context.Context, method string, params json.RawMessage) (any, error) {
	var request cursorInteractionRequest
	if json.Unmarshal(params, &request) != nil {
		return nil, acp.NewInvalidParams(nil)
	}
	sid := agent.interactionSession(request.ToolCallID, request.SessionID)
	if sid == "" {
		return cursorOutcome("cancelled"), nil
	}
	if method == "cursor/create_plan" {
		title := cleanMessage(request.Name, "Review plan")
		response, err := agent.host.RequestPermission(ctx, acp.RequestPermissionRequest{SessionId: sid, ToolCall: acp.ToolCallUpdate{ToolCallId: acp.ToolCallId(request.ToolCallID), Title: &title, Content: []acp.ToolCallContent{acp.ToolContent(acp.TextBlock(request.Plan))}}, Options: []acp.PermissionOption{{OptionId: "accept", Name: "Accept plan", Kind: acp.PermissionOptionKindAllowOnce}, {OptionId: "reject", Name: "Reject plan", Kind: acp.PermissionOptionKindRejectOnce}}})
		if err != nil {
			return nil, err
		}
		if response.Outcome.Selected != nil {
			if response.Outcome.Selected.OptionId == "accept" {
				return cursorOutcome("accepted"), nil
			}
			return cursorOutcome("rejected"), nil
		}
		return cursorOutcome("cancelled"), nil
	}
	if len(request.Questions) == 0 {
		return nil, acp.NewInvalidParams(nil)
	}
	properties := make(map[string]any)
	required := make([]string, 0, len(request.Questions))
	for _, question := range request.Questions {
		if question.ID == "" || len(question.Options) == 0 {
			return nil, acp.NewInvalidParams(nil)
		}
		if _, exists := properties[question.ID]; exists {
			return nil, acp.NewInvalidParams(nil)
		}
		ids := make([]string, 0, len(question.Options))
		names := make([]string, 0, len(question.Options))
		for _, option := range question.Options {
			ids = append(ids, option.ID)
			names = append(names, option.Label)
		}
		property := map[string]any{"type": "string", "title": question.Prompt, "enum": ids, "enumNames": names}
		if question.AllowMultiple {
			property = map[string]any{"type": "array", "title": question.Prompt, "items": map[string]any{"type": "string", "enum": ids, "enumNames": names}, "minItems": 1, "uniqueItems": true}
		}
		properties[question.ID] = property
		required = append(required, question.ID)
	}
	response, err := agent.host.UnstableCreateElicitation(ctx, acp.UnstableCreateElicitationRequest{Form: &acp.UnstableCreateElicitationForm{Mode: "form", Message: cleanMessage(request.Title), Meta: map[string]any{"sessionId": sid}, RequestedSchema: acp.UnstableElicitationSchema{Properties: properties, Required: required}}})
	if err != nil {
		return nil, err
	}
	if response.Decline != nil {
		return cursorOutcome("skipped"), nil
	}
	if response.Accept == nil {
		return cursorOutcome("cancelled"), nil
	}
	answers := make([]map[string]any, 0, len(request.Questions))
	for _, question := range request.Questions {
		values := []string{}
		switch value := response.Accept.Content[question.ID].(type) {
		case string:
			values = append(values, value)
		case []any:
			for _, item := range value {
				if id, ok := item.(string); ok {
					values = append(values, id)
				}
			}
		case []string:
			values = value
		}
		if len(values) == 0 || (!question.AllowMultiple && len(values) != 1) {
			return cursorOutcome("cancelled"), nil
		}
		for _, id := range values {
			valid := false
			for _, option := range question.Options {
				if option.ID == id {
					valid = true
				}
			}
			if !valid {
				return cursorOutcome("cancelled"), nil
			}
		}
		answers = append(answers, map[string]any{"questionId": question.ID, "selectedOptionIds": values})
	}
	return map[string]any{"outcome": map[string]any{"outcome": "answered", "answers": answers}}, nil
}

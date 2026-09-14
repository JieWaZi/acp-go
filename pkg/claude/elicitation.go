package claude

import (
	"encoding/json"
	"fmt"
	"strings"

	acp "github.com/coder/acp-go-sdk"
)

const askUserQuestionCustomSuffix = "_custom"

// askUserQuestion 保存 Claude 内置 AskUserQuestion 的一个有效问题。
type askUserQuestion struct {
	// Question 是面向用户的完整问题。
	Question string `json:"question"`
	// Header 是表单字段的可选短标题。
	Header string `json:"header,omitempty"`
	// MultiSelect 表示允许用户选择多个选项。
	MultiSelect bool `json:"multiSelect,omitempty"`
	// Options 是至少包含一项的可选答案。
	Options []askUserQuestionOption `json:"options"`
}

// askUserQuestionOption 保存一个选项及其解释和预览。
type askUserQuestionOption struct {
	// Label 是写回 Claude 工具 input 的稳定答案值。
	Label string `json:"label"`
	// Description 是选项的补充说明。
	Description string `json:"description,omitempty"`
	// Preview 是客户端可选展示的代码或内容预览。
	Preview string `json:"preview,omitempty"`
}

// cloneClaudeElicitationCapabilities 只复制当前实现消费的 form 能力。
func cloneClaudeElicitationCapabilities(
	capabilities *acp.ElicitationCapabilities,
) *acp.ElicitationCapabilities {
	if capabilities == nil || capabilities.Form == nil {
		return nil
	}
	return &acp.ElicitationCapabilities{Form: &acp.ElicitationFormCapabilities{}}
}

// currentFormElicitation 返回协商后的 form 能力与当前连接请求器。
func (a *Agent) currentFormElicitation() (elicitationRequester, bool) {
	a.initializedMu.RLock()
	formSupported := a.elicitationCapabilities != nil &&
		a.elicitationCapabilities.Form != nil
	a.initializedMu.RUnlock()
	if !formSupported {
		return nil, false
	}
	a.connectionMu.RLock()
	requester := a.elicitationRequester
	a.connectionMu.RUnlock()
	return requester, requester != nil
}

// decodeAskUserQuestions 提取结构完整的问题；全部无效时才失败关闭。
func decodeAskUserQuestions(input json.RawMessage) ([]askUserQuestion, error) {
	var payload struct {
		// Questions 是 Claude 工具提交的问题数组。
		Questions []askUserQuestion `json:"questions"`
	}
	if err := json.Unmarshal(input, &payload); err != nil {
		return nil, fmt.Errorf("decoding AskUserQuestion input: %w", err)
	}
	questions := make([]askUserQuestion, 0, len(payload.Questions))
	for _, question := range payload.Questions {
		if !validAskUserQuestion(question) {
			continue
		}
		questions = append(questions, question)
	}
	if len(questions) == 0 {
		return nil, fmt.Errorf("AskUserQuestion input has no valid questions")
	}
	return questions, nil
}

// validAskUserQuestion 判断问题和全部选项是否可安全写入 ACP form。
func validAskUserQuestion(question askUserQuestion) bool {
	if strings.TrimSpace(question.Question) == "" || len(question.Options) == 0 {
		return false
	}
	for _, option := range question.Options {
		if strings.TrimSpace(option.Label) == "" {
			return false
		}
	}
	return true
}

// askUserQuestionElicitation 把 Claude 问题转换为 ACP JSON Schema form。
func askUserQuestionElicitation(
	questions []askUserQuestion,
	sessionID string,
	toolUseID string,
) acp.UnstableCreateElicitationRequest {
	properties := make(map[string]any, len(questions)*2)
	for index, question := range questions {
		options := make([]map[string]any, 0, len(question.Options))
		for _, option := range question.Options {
			entry := map[string]any{"const": option.Label, "title": option.Label}
			if option.Description != "" {
				entry["description"] = option.Description
			}
			if option.Preview != "" {
				entry["_meta"] = map[string]any{
					"_claude/askUserQuestionOption": map[string]any{"preview": option.Preview},
				}
			}
			options = append(options, entry)
		}
		field := map[string]any{"type": "string"}
		if question.MultiSelect {
			field = map[string]any{
				"type":  "array",
				"items": map[string]any{"anyOf": options},
			}
		} else {
			field["oneOf"] = options
		}
		if question.Header != "" {
			field["title"] = question.Header
		}
		if len(questions) > 1 {
			field["description"] = question.Question
		}
		key := askUserQuestionField(index)
		properties[key] = field
		properties[key+askUserQuestionCustomSuffix] = map[string]any{
			"type":        "string",
			"title":       "Other",
			"description": "Type your own answer instead of choosing an option above (optional).",
			"_meta": map[string]any{
				"_askUserQuestionCustomAnswer": map[string]any{
					"questionId":     key,
					"isCustomAnswer": true,
				},
			},
		}
	}
	message := "Please answer the following questions."
	if len(questions) == 1 {
		message = questions[0].Question
	}
	request := acp.NewUnstableCreateElicitationRequestForm(acp.UnstableElicitationSchema{
		Type:       acp.UnstableElicitationSchemaTypeObject,
		Properties: properties,
	})
	request.Form.Message = message
	request.Form.Meta = map[string]any{"claudeCode": map[string]any{
		"sessionId":  sessionID,
		"toolCallId": toolUseID,
	}}
	return request
}

// applyAskUserQuestionResponse 把表单内容写回 Claude 工具需要的 answers map。
func applyAskUserQuestionResponse(
	response acp.UnstableCreateElicitationResponse,
	input json.RawMessage,
	questions []askUserQuestion,
) (json.RawMessage, bool) {
	if response.Cancel != nil {
		return nil, false
	}
	if response.Accept == nil && response.Decline == nil {
		return nil, false
	}
	var payload map[string]any
	if json.Unmarshal(input, &payload) != nil {
		return nil, false
	}
	answers := make(map[string]string, len(questions))
	if response.Accept != nil {
		for index, question := range questions {
			key := askUserQuestionField(index)
			if custom, ok := response.Accept.Content[key+askUserQuestionCustomSuffix].(string); ok &&
				strings.TrimSpace(custom) != "" {
				answers[question.Question] = strings.TrimSpace(custom)
				continue
			}
			value := response.Accept.Content[key]
			switch typed := value.(type) {
			case string:
				if typed != "" {
					answers[question.Question] = typed
				}
			case []any:
				parts := make([]string, 0, len(typed))
				for _, item := range typed {
					if text, ok := item.(string); ok && text != "" {
						parts = append(parts, text)
					}
				}
				if len(parts) > 0 {
					answers[question.Question] = strings.Join(parts, ", ")
				}
			}
		}
	}
	payload["answers"] = answers
	updated, err := json.Marshal(payload)
	return updated, err == nil
}

// askUserQuestionField 返回按问题位置稳定生成的表单字段标识。
func askUserQuestionField(index int) string {
	return fmt.Sprintf("question_%d", index)
}

// ProvidesUserInput 表示 Claude 的原生 AskUserQuestion 在三个权限档位均走表单桥。
func (a *Agent) ProvidesUserInput() bool { return true }

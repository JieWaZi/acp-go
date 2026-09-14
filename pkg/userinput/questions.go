// Package userinput 为不同 CLI 补充与权限等级无关的 ACP 交互问答工具。
package userinput

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	acp "github.com/coder/acp-go-sdk"
)

// Question 是各 CLI 共用的单个用户问题。
type Question struct {
	// ID 是可选的稳定问题标识；省略时使用问题正文。
	ID string `json:"id,omitempty"`
	// Question 是展示给用户的完整问题。
	Question string `json:"question"`
	// Header 是可选的短标题。
	Header string `json:"header,omitempty"`
	// MultiSelect 表示允许多个选项。
	MultiSelect bool `json:"multiSelect,omitempty"`
	// Options 是候选答案；空值表示自由文本问题。
	Options []Option `json:"options,omitempty"`
}

// Option 保存一个候选答案及其解释。
type Option struct {
	// Label 是选项的展示文本和答案值。
	Label string `json:"label"`
	// Description 是选项的补充说明。
	Description string `json:"description,omitempty"`
}

// Questions 是 AskUserQuestion 的标准输入。
type Questions struct {
	// Questions 按展示顺序保存本次需要用户回答的问题。
	Questions []Question `json:"questions"`
}

// Result 明确区分用户回答、跳过和取消，避免以空对象伪装回答成功。
type Result struct {
	// Action 是 accept、decline 或 cancel。
	Action string `json:"action"`
	// Answers 以问题标识或正文关联实际用户答案。
	Answers map[string]any `json:"answers,omitempty"`
}

// Requester 是问答使用的最小宿主端口。
type Requester interface {
	// UnstableCreateElicitation 等待宿主提交实际用户回答。
	UnstableCreateElicitation(context.Context, acp.UnstableCreateElicitationRequest) (acp.UnstableCreateElicitationResponse, error)
}

// Ask 验证问题并通过标准 ACP 表单等待真实用户答案。
func Ask(ctx context.Context, host Requester, sid acp.SessionId, input Questions) (Result, error) {
	cancelled := Result{Action: "cancel"}
	if host == nil || sid == "" || ctx.Err() != nil {
		return cancelled, nil
	}
	data, err := json.Marshal(input)
	if err != nil || len(data) > 60000 || len(input.Questions) == 0 || len(input.Questions) > 8 {
		return cancelled, errors.New("invalid user questions")
	}
	properties := map[string]any{}
	identities := map[string]bool{}
	for i, question := range input.Questions {
		key := question.ID
		if key == "" {
			key = question.Question
		}
		if strings.TrimSpace(question.Question) == "" || len(question.Question) > 4096 || identities[key] || len(question.Options) > 64 {
			return cancelled, errors.New("invalid or duplicate question")
		}
		identities[key] = true
		field := map[string]any{"type": "string", "title": question.Header, "description": question.Question}
		if question.Header == "" {
			field["title"] = question.Question
		}
		options := []map[string]any{}
		labels := map[string]bool{}
		for _, option := range question.Options {
			if strings.TrimSpace(option.Label) == "" || labels[option.Label] {
				return cancelled, errors.New("invalid or duplicate question option")
			}
			labels[option.Label] = true
			options = append(options, map[string]any{"const": option.Label, "title": option.Label, "description": option.Description})
		}
		fieldKey := fmt.Sprintf("question_%d", i)
		if len(options) > 0 {
			if question.MultiSelect {
				field["type"] = "array"
				field["items"] = map[string]any{"anyOf": options}
				field["uniqueItems"] = true
			} else {
				field["oneOf"] = options
			}
			properties[fieldKey+"_custom"] = map[string]any{"type": "string", "title": "Other", "_meta": map[string]any{"_askUserQuestionCustomAnswer": map[string]any{"questionId": fieldKey, "isCustomAnswer": true}}}
		}
		properties[fieldKey] = field
	}
	request := acp.NewUnstableCreateElicitationRequestForm(acp.UnstableElicitationSchema{Type: acp.UnstableElicitationSchemaTypeObject, Properties: properties})
	request.Form.Message = "Please answer the following questions."
	if len(input.Questions) == 1 {
		request.Form.Message = input.Questions[0].Question
	}
	request.Form.Meta = map[string]any{"sessionId": sid}
	response, err := host.UnstableCreateElicitation(ctx, request)
	if err != nil {
		return cancelled, err
	}
	if ctx.Err() != nil {
		return cancelled, nil
	}
	if response.Decline != nil {
		return Result{Action: "decline"}, nil
	}
	if response.Accept == nil {
		return cancelled, nil
	}
	answers := map[string]any{}
	for i, question := range input.Questions {
		key := fmt.Sprintf("question_%d", i)
		value := response.Accept.Content[key]
		custom, _ := response.Accept.Content[key+"_custom"].(string)
		if strings.TrimSpace(custom) != "" && len(question.Options) > 0 {
			value = custom
		} else {
			valid := func(answer string) bool {
				if strings.TrimSpace(answer) == "" {
					return false
				}
				if len(question.Options) == 0 {
					return true
				}
				for _, opt := range question.Options {
					if answer == opt.Label {
						return true
					}
				}
				return false
			}
			if question.MultiSelect && len(question.Options) > 0 {
				encoded, _ := json.Marshal(value)
				var items []string
				if json.Unmarshal(encoded, &items) != nil || len(items) == 0 {
					return cancelled, nil
				}
				seen := map[string]bool{}
				for _, item := range items {
					if !valid(item) || seen[item] {
						return cancelled, nil
					}
					seen[item] = true
				}
				value = items
			} else {
				text, ok := value.(string)
				if !ok || !valid(text) {
					return cancelled, nil
				}
			}
		}
		identity := question.ID
		if identity == "" {
			identity = question.Question
		}
		answers[identity] = value
	}
	return Result{Action: "accept", Answers: answers}, nil
}

package cursor

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/JieWaZi/acp-go/pkg/userinput"
)

// cursorQuestion 保存原生问题的稳定身份与选择顺序。
type cursorQuestion struct {
	// ID 是回填原始答案的字段标识。
	ID string `json:"id"`
	// Prompt 是用户看到的问题。
	Prompt string `json:"prompt"`
	// Options 是终端中的固定选项，后面还有原生自由输入行。
	Options []cursorQuestionOption `json:"options"`
	// Multiple 表示可同时选择多个选项。
	Multiple bool `json:"allowMultiple"`
	// MultipleWire 是待处理 protobuf 检查点使用的 snake_case 字段。
	MultipleWire bool `json:"allow_multiple"`
}

// cursorQuestionOption 保存选项原生身份与终端标签。
type cursorQuestionOption struct {
	// ID 是宿主表单传回的原始值。
	ID string `json:"id"`
	// Label 是终端显示的选项。
	Label string `json:"label"`
}

// parseQuestions 校验原生问题身份并统一多选字段的当前检查点表示。
func parseQuestions(call pendingCall) ([]cursorQuestion, error) {
	data, err := json.Marshal(call.args["questions"])
	if err != nil {
		return nil, err
	}
	var questions []cursorQuestion
	if json.Unmarshal(data, &questions) != nil || len(questions) == 0 {
		return nil, errors.New("invalid Cursor questions")
	}
	seen := map[string]bool{}
	for index, q := range questions {
		if q.ID == "" || q.Prompt == "" || seen[q.ID] || len(q.Options) == 0 {
			return nil, errors.New("invalid Cursor question identity")
		}
		seen[q.ID] = true
		questions[index].Multiple = q.Multiple || q.MultipleWire
	}
	return questions, nil
}

// questionScreen 核对当前终端正在展示待回填的原生问题。
func questionScreen(screen string, call pendingCall) bool {
	questions, err := parseQuestions(call)
	if err != nil {
		return false
	}
	return strings.Contains(screen, questions[0].Prompt) && strings.Contains(screen, "Other")
}

// answerQuestions 复用统一问答协议，并逐步等待终端重绘后回填答案。
func (a *Agent) answerQuestions(ctx context.Context, s *interactiveSession, call pendingCall) error {
	questions, err := parseQuestions(call)
	if err != nil {
		return err
	}
	input := userinput.Questions{}
	for _, q := range questions {
		question := userinput.Question{ID: q.ID, Question: q.Prompt, MultiSelect: q.Multiple}
		for _, option := range q.Options {
			question.Options = append(question.Options, userinput.Option{Label: option.Label})
		}
		input.Questions = append(input.Questions, question)
	}
	a.mutex.Lock()
	host := a.host
	a.mutex.Unlock()
	result, err := userinput.Ask(ctx, host, s.id, input)
	if err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if !questionScreen(s.terminal.text(), call) {
		return errors.New("Cursor question changed before response")
	}
	if err = pendingUnchanged(ctx, s, call); err != nil {
		return err
	}
	if result.Action == "decline" {
		return s.terminal.write("\x1b")
	}
	if result.Action != "accept" {
		_ = s.terminal.write("\x1b")
		return context.Canceled
	}
	for _, q := range questions {
		values := []string{}
		switch value := result.Answers[q.ID].(type) {
		case string:
			values = []string{value}
		case []string:
			values = value
		case []any:
			for _, v := range value {
				str, ok := v.(string)
				if !ok {
					return errors.New("invalid Cursor answer value")
				}
				values = append(values, str)
			}
		}
		if len(values) == 0 || !q.Multiple && len(values) != 1 {
			return errors.New("invalid Cursor answer cardinality")
		}
		targets := map[int]string{}
		for _, value := range values {
			if strings.TrimSpace(value) == "" || strings.IndexFunc(value, func(r rune) bool { return r < 32 || r == 127 }) >= 0 {
				return errors.New("invalid Cursor answer text")
			}
			index := len(q.Options)
			custom := value
			for i, option := range q.Options {
				if option.Label == value {
					index = i
					custom = ""
					break
				}
			}
			if _, exists := targets[index]; exists {
				return errors.New("duplicate Cursor answer")
			}
			targets[index] = custom
		}
		step, cancel := context.WithTimeout(ctx, 5*time.Second)
		err = s.terminal.wait(step, func(screen string) bool {
			return strings.Contains(screen, q.Prompt) && strings.Contains(screen, "Other")
		})
		cancel()
		if err != nil {
			return err
		}
		indices := []int{}
		for index := range targets {
			indices = append(indices, index)
		}
		sort.Ints(indices)
		position := 0
		for _, index := range indices {
			for position < index {
				if err = questionKey(ctx, s.terminal, "\x1b[B"); err != nil {
					return err
				}
				position++
			}
			if custom := targets[index]; custom != "" {
				s.terminal.screen.Paste(custom)
				wait, cancel := context.WithTimeout(ctx, 5*time.Second)
				err = s.terminal.wait(wait, func(screen string) bool { return strings.Contains(screen, custom) })
				cancel()
				if err != nil {
					return err
				}
			} else if err = questionKey(ctx, s.terminal, " "); err != nil {
				return err
			}
		}
		if err = questionKey(ctx, s.terminal, "\r"); err != nil {
			return err
		}
	}
	return nil
}

// questionKey 等待终端重绘后才继续导航，避免把输入粘贴为单个按键。
func questionKey(ctx context.Context, terminal *cursorTerminal, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	before := terminal.text()
	if err := terminal.write(key); err != nil {
		return err
	}
	wait, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	return terminal.wait(wait, func(screen string) bool { return screen != before })
}

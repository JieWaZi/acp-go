package claude

import (
	"encoding/json"
	"testing"

	acp "github.com/coder/acp-go-sdk"
)

// TestAskUserQuestionElicitationRoundTrip 验证选项、自定义答案与多选结果转换。
func TestAskUserQuestionElicitationRoundTrip(t *testing.T) {
	t.Parallel()

	input := json.RawMessage(`{"questions":[{"question":"Continue?","header":"Decision","options":[{"label":"Yes","description":"Proceed"}]},{"question":"Targets?","multiSelect":true,"options":[{"label":"A"},{"label":"B"}]}]}`)
	questions, err := decodeAskUserQuestions(input)
	if err != nil {
		t.Fatalf("decodeAskUserQuestions 返回错误：%v", err)
	}
	request := askUserQuestionElicitation(questions, "session-1", "tool-1")
	if request.Form == nil || request.Form.RequestedSchema.Properties["question_0"] == nil ||
		request.Form.RequestedSchema.Properties["question_1_custom"] == nil {
		t.Fatalf("form request = %#v", request.Form)
	}

	response := acp.NewUnstableCreateElicitationResponseAccept()
	response.Accept.Content = map[string]any{
		"question_0_custom": "Use another path",
		"question_1":        []any{"A", "B"},
	}
	updated, ok := applyAskUserQuestionResponse(response, input, questions)
	if !ok {
		t.Fatal("applyAskUserQuestionResponse 返回取消")
	}
	var payload struct {
		// Answers 是 Claude AskUserQuestion 工具消费的答案映射。
		Answers map[string]string `json:"answers"`
	}
	if err = json.Unmarshal(updated, &payload); err != nil {
		t.Fatalf("解析 updated input 失败：%v", err)
	}
	if payload.Answers["Continue?"] != "Use another path" ||
		payload.Answers["Targets?"] != "A, B" {
		t.Fatalf("Answers = %#v", payload.Answers)
	}
}

// TestAskUserQuestionElicitationCancelFailsClosed 验证取消不会伪造工具答案。
func TestAskUserQuestionElicitationCancelFailsClosed(t *testing.T) {
	t.Parallel()

	questions, err := decodeAskUserQuestions(json.RawMessage(
		`{"questions":[{"question":"Continue?","options":[{"label":"Yes"}]}]}`,
	))
	if err != nil {
		t.Fatal(err)
	}
	response := acp.NewUnstableCreateElicitationResponseCancel()
	if _, ok := applyAskUserQuestionResponse(response, json.RawMessage(`{"questions":[]}`), questions); ok {
		t.Fatal("cancel response 被错误接受")
	}
	if _, ok := applyAskUserQuestionResponse(
		acp.UnstableCreateElicitationResponse{},
		json.RawMessage(`{"questions":[]}`),
		questions,
	); ok {
		t.Fatal("未知 response 被错误接受")
	}
}

// TestDecodeAskUserQuestionsKeepsValidEntries 验证混合输入不会丢弃仍可询问的问题。
func TestDecodeAskUserQuestionsKeepsValidEntries(t *testing.T) {
	t.Parallel()

	questions, err := decodeAskUserQuestions(json.RawMessage(
		`{"questions":[{"question":"Broken","options":[]},{"question":"Continue?","options":[{"label":"Yes"}]}]}`,
	))
	if err != nil {
		t.Fatalf("decodeAskUserQuestions 返回错误：%v", err)
	}
	if len(questions) != 1 || questions[0].Question != "Continue?" {
		t.Fatalf("questions = %#v", questions)
	}
}

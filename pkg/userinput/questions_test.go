package userinput

import (
	"context"
	"testing"

	acp "github.com/coder/acp-go-sdk"
)

// questionHost 模拟真实表单边界并保留实际路由与输入。
type questionHost struct {
	// callback 由场景决定用户回答或等待取消。
	callback func(context.Context, acp.UnstableCreateElicitationRequest) (acp.UnstableCreateElicitationResponse, error)
}

// UnstableCreateElicitation 把调用交给当前场景的用户模拟器。
func (h questionHost) UnstableCreateElicitation(ctx context.Context, r acp.UnstableCreateElicitationRequest) (acp.UnstableCreateElicitationResponse, error) {
	return h.callback(ctx, r)
}

// TestAnswersPreserveChoicesAndCustomInput 验证选择、自由回答、跳过和非法答案不会被混淆。
func TestAnswersPreserveChoicesAndCustomInput(t *testing.T) {
	for _, tc := range []struct {
		// name 是场景名称。
		name string
		// content 是模拟用户提交的表单答案。
		content map[string]any
		// decline 模拟用户跳过。
		decline bool
		// action 是预期的交互结果。
		action string
	}{
		{name: "choices", content: map[string]any{"question_0": "A", "question_1": []any{"B", "C"}}, action: "accept"},
		{name: "custom", content: map[string]any{"question_0_custom": "my answer", "question_1": []any{"C"}}, action: "accept"},
		{name: "invalid", content: map[string]any{"question_0": "unknown", "question_1": []any{"C"}}, action: "cancel"},
		{name: "missing", content: map[string]any{}, action: "cancel"},
		{name: "skip", decline: true, action: "decline"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			host := questionHost{callback: func(_ context.Context, r acp.UnstableCreateElicitationRequest) (acp.UnstableCreateElicitationResponse, error) {
				if r.Form.Meta["sessionId"] != acp.SessionId("owned-session") {
					t.Fatal("question routed to wrong session")
				}
				if tc.decline {
					return acp.NewUnstableCreateElicitationResponseDecline(), nil
				}
				r2 := acp.NewUnstableCreateElicitationResponseAccept()
				r2.Accept.Content = tc.content
				return r2, nil
			}}
			result, err := Ask(context.Background(), host, "owned-session", Questions{Questions: []Question{{ID: "one", Question: "First?", Options: []Option{{Label: "A"}}}, {ID: "two", Question: "Second?", MultiSelect: true, Options: []Option{{Label: "B"}, {Label: "C"}}}}})
			if err != nil || result.Action != tc.action {
				t.Fatalf("result=%#v, error=%v", result, err)
			}
			if tc.name == "custom" && result.Answers["one"] != "my answer" {
				t.Fatal("custom answer discarded")
			}
		})
	}
}

// TestLateAnswerCannotResolveCancelledQuestion 验证取消后的成功回包不构成用户回答。
func TestLateAnswerCannotResolveCancelledQuestion(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	host := questionHost{callback: func(context.Context, acp.UnstableCreateElicitationRequest) (acp.UnstableCreateElicitationResponse, error) {
		cancel()
		r := acp.NewUnstableCreateElicitationResponseAccept()
		r.Accept.Content = map[string]any{"question_0": "late"}
		return r, nil
	}}
	result, err := Ask(ctx, host, "session", Questions{Questions: []Question{{Question: "Answer?"}}})
	if err != nil || result.Action != "cancel" {
		t.Fatalf("late answer accepted: %#v %v", result, err)
	}
}

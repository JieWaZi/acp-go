package cursor

import (
	"encoding/json"
	"testing"
)

// TestPendingQuestionWireShape 验证真实待回答检查点和已提交消息的多选字段均不会退化为单选。
func TestPendingQuestionWireShape(t *testing.T) {
	for _, field := range []string{"allow_multiple", "allowMultiple"} {
		var args map[string]any
		if err := json.Unmarshal([]byte(`{"questions":[{"id":"colors","prompt":"选择颜色","`+field+`":true,"options":[{"id":"red","label":"红色"},{"id":"blue","label":"蓝色"}]}]}`), &args); err != nil {
			t.Fatal(err)
		}
		questions, err := parseQuestions(pendingCall{args: args})
		if err != nil || len(questions) != 1 || !questions[0].Multiple {
			t.Fatalf("%s lost multi-select: %+v %v", field, questions, err)
		}
	}
}

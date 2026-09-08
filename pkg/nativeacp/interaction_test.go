package nativeacp

import (
	"context"
	"encoding/json"
	"testing"

	acp "github.com/coder/acp-go-sdk"
)

// TestInteractionRejectsConflictingSession 验证未知身份和跨会话工具不能落到唯一活跃聊天。
func TestInteractionRejectsConflictingSession(t *testing.T) {
	agent := &Agent{active: map[acp.SessionId]bool{"active": true}, toolSessions: map[acp.ToolCallId]acp.SessionId{"old-tool": "old"}}
	if agent.interactionSession("", "unknown") != "" || agent.interactionSession("old-tool", "active") != "" || agent.interactionSession("old-tool", "") != "" {
		t.Fatal("foreign interaction routed to active session")
	}
	if agent.interactionSession("", "") != "active" {
		t.Fatal("unambiguous native callback was not routed")
	}
	for _, raw := range []string{`{"_meta":{"sessionId":123}}`, `{"_meta":{"toolCallId":[]}}`, `{"sessionId":"active","_meta":{"sessionId":"other"}}`} {
		if _, err := agent.elicitation(context.Background(), json.RawMessage(raw)); err == nil {
			t.Fatalf("invalid identity accepted: %s", raw)
		}
	}
}

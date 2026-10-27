package nativeacp

import (
	acp "github.com/JieWaZi/acp-go/sdk"
	"testing"
)

// TestNativeMessageSegmentsKeepIdentityAcrossChunks 验证七种原生 ACP 接入共用稳定身份且工具后不合并旧消息。
func TestNativeMessageSegmentsKeepIdentityAcrossChunks(t *testing.T) {
	for _, runtime := range []string{"opencode", "gemini", "grok", "workbuddy", "qoder", "qoder-cn", "kimi"} {
		t.Run(runtime, func(t *testing.T) {
			agent := &Agent{config: Config{RuntimeName: runtime}, sessions: map[acp.SessionId]*sessionOptions{"s": {}}, toolRevisions: map[acp.ToolCallId]*toolEvidenceRevision{}, toolSessions: map[acp.ToolCallId]acp.SessionId{}, toolDetails: map[acp.ToolCallId]acp.ToolCallUpdate{}}
			chunk := func(text string) *acp.SessionUpdateAgentMessageChunk {
				update := acp.SessionNotification{SessionId: "s", Update: acp.UpdateAgentMessageText(text)}
				agent.normalizeUpdate(&update)
				return update.Update.AgentMessageChunk
			}
			first, second := chunk("Checking"), chunk(" now")
			if first.MessageId == nil || second.MessageId == nil || *first.MessageId == "" || *first.MessageId != *second.MessageId {
				t.Fatalf("unstable chunks: %#v %#v", first, second)
			}
			tool := acp.SessionNotification{SessionId: "s", Update: acp.StartToolCall("t", "read")}
			agent.normalizeUpdate(&tool)
			last := chunk("Answer")
			if last.MessageId == nil || *last.MessageId == *first.MessageId {
				t.Fatalf("tool-separated body merged: %#v", last)
			}
			meta, ok := last.Meta["acp-go/message"].(map[string]any)
			if !ok || meta["phase"] != "unknown" || meta["complete"] != false {
				t.Fatalf("invented finality: %#v", last.Meta)
			}
			thought := acp.SessionNotification{SessionId: "s", Update: acp.UpdateAgentThoughtText("continue")}
			agent.normalizeUpdate(&thought)
			if after := chunk("Revised"); after.MessageId == nil || *after.MessageId == *last.MessageId {
				t.Fatalf("thought did not split message: %#v", after)
			}
			native := acp.SessionNotification{SessionId: "s", Update: acp.UpdateAgentMessageText("native")}
			native.Update.AgentMessageChunk.MessageId = acp.Ptr("native-message")
			native.Update.AgentMessageChunk.Meta = map[string]any{"acp-go/message": map[string]any{"phase": "commentary", "complete": true, "source": "native_phase"}, "vendor": "keep"}
			agent.normalizeUpdate(&native)
			if *native.Update.AgentMessageChunk.MessageId != "native-message" || native.Update.AgentMessageChunk.Meta["vendor"] != "keep" {
				t.Fatalf("native facts lost: %#v", native)
			}
		})
	}
}

// TestNativeKnownPhaseSurvivesUnknownContinuation 验证同一原生身份的未知增量不能覆盖已经确认的过程阶段。
func TestNativeKnownPhaseSurvivesUnknownContinuation(t *testing.T) {
	agent := &Agent{sessions: map[acp.SessionId]*sessionOptions{"s": {}}, toolRevisions: map[acp.ToolCallId]*toolEvidenceRevision{}, toolSessions: map[acp.ToolCallId]acp.SessionId{}, toolDetails: map[acp.ToolCallId]acp.ToolCallUpdate{}}
	for index, phase := range []string{"commentary", "unknown", "unknown"} {
		if index == 2 {
			tool := acp.SessionNotification{SessionId: "s", Update: acp.StartToolCall("t", "read")}
			agent.normalizeUpdate(&tool)
		}
		update := acp.SessionNotification{SessionId: "s", Update: acp.UpdateAgentMessageText("process")}
		update.Update.AgentMessageChunk.MessageId = acp.Ptr("native-id")
		update.Update.AgentMessageChunk.Meta = map[string]any{"acp-go/message": map[string]any{"phase": phase, "complete": false, "source": "native_phase"}}
		agent.normalizeUpdate(&update)
		facts := update.Update.AgentMessageChunk.Meta["acp-go/message"].(map[string]any)
		if facts["phase"] != "commentary" {
			t.Fatalf("known commentary overwritten at %d: %#v", index, facts)
		}
	}
}

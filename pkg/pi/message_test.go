package pi

import (
	"bytes"
	"encoding/json"
	"io"
	"testing"
	"time"

	acp "github.com/JieWaZi/acp-go/sdk"
)

// TestPiMessageLifecycleIdentity 验证文本增量共享本地消息身份，消息结束与全轮 settled 分离。
func TestPiMessageLifecycleIdentity(t *testing.T) {
	output := &bytes.Buffer{}
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()
	agent := &Agent{}
	agent.SetAgentConnection(acp.NewAgentSideConnection(agent, output, reader))
	events := make(chan rpcEvent, 9)
	done := make(chan struct{})
	turn := make(chan acp.StopReason, 1)
	session := &session{id: "s", process: &rpcProcess{events: events, done: done, eventChanged: make(chan struct{})}, turn: turn, eventsDone: make(chan struct{}), tools: map[string]string{}}
	for _, event := range []map[string]any{
		{"type": "message_start", "message": map[string]any{"role": "assistant"}},
		{"type": "message_update", "assistantMessageEvent": map[string]any{"type": "text_delta", "delta": "Check"}},
		{"type": "message_update", "assistantMessageEvent": map[string]any{"type": "text_delta", "delta": " now"}},
		{"type": "message_end", "message": map[string]any{"role": "assistant", "stopReason": "toolUse"}},
		{"type": "tool_execution_end", "toolCallId": "tool", "toolName": "read"},
		{"type": "message_start", "message": map[string]any{"role": "assistant"}},
		{"type": "message_update", "assistantMessageEvent": map[string]any{"type": "text_delta", "delta": "Answer"}},
		{"type": "message_end", "message": map[string]any{"role": "assistant", "stopReason": "stop"}},
		{"type": "agent_settled"},
	} {
		events <- rpcEvent{data: event}
	}
	go agent.events(session)
	select {
	case reason := <-turn:
		if reason != acp.StopReasonEndTurn {
			t.Fatal(reason)
		}
	case <-time.After(time.Second):
		t.Fatal("turn did not settle")
	}
	close(events)
	close(done)
	<-session.eventsDone
	decoder := json.NewDecoder(bytes.NewReader(output.Bytes()))
	var chunks []*acp.SessionUpdateAgentMessageChunk
	for decoder.More() {
		var wire struct {
			// Params 是从 ACP JSON-RPC 通知读取的标准消息。
			Params acp.SessionNotification `json:"params"`
		}
		if err := decoder.Decode(&wire); err != nil {
			t.Fatal(err)
		}
		if chunk := wire.Params.Update.AgentMessageChunk; chunk != nil {
			chunks = append(chunks, chunk)
		}
	}
	if len(chunks) != 5 {
		t.Fatalf("missing lifecycle chunks: %#v", chunks)
	}
	if chunks[0].MessageId == nil || chunks[1].MessageId == nil || *chunks[0].MessageId == "" || *chunks[0].MessageId != *chunks[1].MessageId || chunks[3].MessageId == nil || *chunks[0].MessageId == *chunks[3].MessageId {
		t.Fatalf("message identities merged: %#v", chunks)
	}
	for _, index := range []int{2, 4} {
		if facts, ok := chunks[index].Meta["acp-go/message"].(map[string]any); !ok || facts["complete"] != true || facts["phase"] != "unknown" {
			t.Fatalf("native boundary missing: %#v", chunks[index])
		}
	}
}

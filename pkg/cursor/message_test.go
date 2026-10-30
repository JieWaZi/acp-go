package cursor

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"testing"

	acp "github.com/JieWaZi/acp-go/sdk"
)

// TestCursorStoreMessageIdentityAndCompletion 验证检查点消息使用不可变身份，空消息也保留独立完成边界。
func TestCursorStoreMessageIdentityAndCompletion(t *testing.T) {
	output := &bytes.Buffer{}
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()
	agent := &Agent{}
	agent.host = acp.NewAgentSideConnection(agent, output, reader)
	session := &interactiveSession{id: "s"}
	projection := &turnProjection{tools: map[string]bool{}}
	batch := storeBatch{messages: []storeMessage{{id: "blob-1", role: "assistant", content: []storePart{{kind: "text", text: "Answer"}}}, {id: "blob-empty", role: "assistant"}}}
	if err := agent.project(context.Background(), session, projection, batch); err != nil {
		t.Fatal(err)
	}
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
	if len(chunks) != 3 {
		t.Fatalf("missing committed message boundaries: %#v", chunks)
	}
	for i, id := range []string{"blob-1", "blob-1", "blob-empty"} {
		if chunks[i].MessageId == nil || *chunks[i].MessageId != id {
			t.Fatalf("blob ID lost: %#v", chunks[i])
		}
	}
	if facts, ok := chunks[2].Meta["acp-go/message"].(map[string]any); !ok || facts["complete"] != true || facts["phase"] != "unknown" {
		t.Fatalf("empty message finality invented: %#v", chunks[2])
	}
}

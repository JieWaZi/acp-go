package claude

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	acp "github.com/coder/acp-go-sdk"
)

// TestPromptToUserMessage 验证文本、图片和嵌入资源保留关键字段。
func TestPromptToUserMessage(t *testing.T) {
	mime := "text/plain"
	message, err := promptToUserMessage("s1", "m1", []acp.ContentBlock{
		acp.TextBlock("hello"),
		acp.ImageBlock("BASE64", "image/png"),
		acp.ResourceBlock(acp.EmbeddedResourceResource{TextResourceContents: &acp.TextResourceContents{
			Uri: "file:///context.txt", MimeType: &mime, Text: "context",
		}}),
	}, "now")
	if err != nil {
		t.Fatal(err)
	}
	if message.UUID != "m1" || message.Priority != "now" || message.Origin.Kind != "user" {
		t.Fatalf("message = %#v", message)
	}
	var blocks []outboundContentBlock
	if err := json.Unmarshal(message.Message.Content, &blocks); err != nil {
		t.Fatal(err)
	}
	if len(blocks) != 3 || blocks[1].Source == nil || blocks[1].Source.MediaType != "image/png" ||
		blocks[2].Source == nil || blocks[2].Source.Data != "context" {
		t.Fatalf("blocks = %#v", blocks)
	}
}

// TestPromptToUserMessageRejectsUnsupportedAudio 验证 Audio 不会被静默丢弃。
func TestPromptToUserMessageRejectsUnsupportedAudio(t *testing.T) {
	_, err := promptToUserMessage("s", "m", []acp.ContentBlock{acp.AudioBlock("data", "audio/wav")}, "")
	if !errors.Is(err, ErrUnsupportedPromptContent) || !strings.Contains(err.Error(), "audio") {
		t.Fatalf("error = %v", err)
	}
}

// TestStripLocalCommandMarkers 验证 load 回放删除内部标签但保留真实用户文本。
func TestStripLocalCommandMarkers(t *testing.T) {
	input := "<command-name>/model</command-name><local-command-stdout>changed</local-command-stdout>hello"
	if got := stripLocalCommandMarkers(input); got != "hello" {
		t.Fatalf("stripLocalCommandMarkers() = %q", got)
	}
}

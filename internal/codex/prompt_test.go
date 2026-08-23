package codex

import (
	"testing"

	"acp-go/agents/codex/protocol"
	acp "github.com/coder/acp-go-sdk"
)

// TestBuildPromptInputMatchesUpstreamContentRules 验证 Text/Image/Resource 使用固定 buildPromptItems 格式。
func TestBuildPromptInputMatchesUpstreamContentRules(t *testing.T) {
	t.Parallel()
	mimeType := "image/png"
	blocks := []acp.ContentBlock{
		{Text: &acp.ContentBlockText{Type: "text", Text: "hello"}},
		{Image: &acp.ContentBlockImage{Type: "image", MimeType: "image/png", Data: "YWJj"}},
		{ResourceLink: &acp.ContentBlockResourceLink{Type: "resource_link", Name: "readme", Uri: "file:///tmp/README.md"}},
		{Resource: &acp.ContentBlockResource{Type: "resource", Resource: acp.EmbeddedResourceResource{
			TextResourceContents: &acp.TextResourceContents{Uri: "file:///tmp/context.txt", Text: "context"},
		}}},
		{Resource: &acp.ContentBlockResource{Type: "resource", Resource: acp.EmbeddedResourceResource{
			BlobResourceContents: &acp.BlobResourceContents{Uri: "file:///tmp/image.png", MimeType: &mimeType, Blob: "YWJj"},
		}}},
	}
	input, err := buildPromptInput(blocks)
	if err != nil {
		t.Fatalf("转换 prompt 失败: %v", err)
	}
	if len(input) != 5 {
		t.Fatalf("输入数量为 %d", len(input))
	}
	if input[0].Type != protocol.UserInputTypeText || input[0].Text == nil || *input[0].Text != "hello" {
		t.Fatalf("文本输入为 %#v", input[0])
	}
	if input[1].URL == nil || *input[1].URL != "data:image/png;base64,YWJj" {
		t.Fatalf("图片输入为 %#v", input[1])
	}
	if input[2].Text == nil || *input[2].Text != "[@readme](file:///tmp/README.md)" {
		t.Fatalf("资源链接输入为 %#v", input[2])
	}
	if input[3].Text == nil || *input[3].Text != "[@context.txt](file:///tmp/context.txt)\n<context ref=\"file:///tmp/context.txt\">\ncontext\n</context>" {
		t.Fatalf("文本资源输入为 %#v", input[3])
	}
	if input[4].URL == nil || *input[4].URL != "data:image/png;base64,YWJj" {
		t.Fatalf("图片资源输入为 %#v", input[4])
	}
}

// TestBuildPromptInputDropsAudioLikeUpstream 验证未声明的 audio 能力不会产生错误 wire 变体。
func TestBuildPromptInputDropsAudioLikeUpstream(t *testing.T) {
	t.Parallel()
	input, err := buildPromptInput([]acp.ContentBlock{{
		Audio: &acp.ContentBlockAudio{Type: "audio", MimeType: "audio/wav", Data: "YWJj"},
	}})
	if err != nil {
		t.Fatalf("过滤 audio 返回错误: %v", err)
	}
	if len(input) != 0 {
		t.Fatalf("audio 产生了输入: %#v", input)
	}
}

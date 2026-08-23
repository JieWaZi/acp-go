package codex

import (
	"reflect"
	"strings"
	"testing"

	"acp-go/agents/codex/protocol"

	acp "github.com/coder/acp-go-sdk"
)

// TestBuildPromptInputMatchesUpstreamAttachments 锁定 codex-acp 的文本、图片与资源映射。
func TestBuildPromptInputMatchesUpstreamAttachments(t *testing.T) {
	t.Parallel()

	imageURI := "https://example.com/image.png"
	imageMIME := "image/png"
	binaryMIME := "application/octet-stream"
	blocks := []acp.ContentBlock{
		acp.TextBlock("Hello"),
		{Image: &acp.ContentBlockImage{Type: "image", Data: "fallback", MimeType: imageMIME, Uri: &imageURI}},
		acp.ResourceLinkBlock("report.txt", "file:///tmp/report.txt"),
		acp.ResourceBlock(acp.EmbeddedResourceResource{TextResourceContents: &acp.TextResourceContents{
			Uri: "file:///tmp/notes.txt", Text: "Notes body",
		}}),
		acp.ResourceBlock(acp.EmbeddedResourceResource{BlobResourceContents: &acp.BlobResourceContents{
			Uri: "file:///tmp/pixel.png", MimeType: &imageMIME, Blob: "iVBORw0KGgo=",
		}}),
		acp.ResourceBlock(acp.EmbeddedResourceResource{BlobResourceContents: &acp.BlobResourceContents{
			Uri: "file:///tmp/archive.bin", MimeType: &binaryMIME, Blob: "AAEC",
		}}),
	}

	got, err := buildPromptInput(blocks)
	if err != nil {
		t.Fatalf("buildPromptInput 返回错误: %v", err)
	}
	want := []protocol.InputElement{
		{Type: protocol.UserInputTypeText, Text: acp.Ptr("Hello"), TextElements: []protocol.TextElementElement{}},
		{Type: protocol.UserInputTypeImage, URL: &imageURI},
		{Type: protocol.UserInputTypeText, Text: acp.Ptr("[@report.txt](file:///tmp/report.txt)"), TextElements: []protocol.TextElementElement{}},
		{Type: protocol.UserInputTypeText, Text: acp.Ptr("[@notes.txt](file:///tmp/notes.txt)\n<context ref=\"file:///tmp/notes.txt\">\nNotes body\n</context>"), TextElements: []protocol.TextElementElement{}},
		{Type: protocol.UserInputTypeImage, URL: acp.Ptr("data:image/png;base64,iVBORw0KGgo=")},
		{Type: protocol.UserInputTypeText, Text: acp.Ptr("[@archive.bin](file:///tmp/archive.bin)\n<context ref=\"file:///tmp/archive.bin\" mimeType=\"application/octet-stream\" encoding=\"base64\">\nAAEC\n</context>"), TextElements: []protocol.TextElementElement{}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("prompt input = %#v，期望 %#v", got, want)
	}
}

// TestBuildPromptInputUsesInlineImageForUnsupportedURI 验证本地或私有 URI 不会泄漏给 app-server。
func TestBuildPromptInputUsesInlineImageForUnsupportedURI(t *testing.T) {
	t.Parallel()

	uri := "file:///tmp/image.png"
	got, err := buildPromptInput([]acp.ContentBlock{{Image: &acp.ContentBlockImage{
		Type: "image", Data: "abc123", MimeType: "image/png", Uri: &uri,
	}}})
	if err != nil {
		t.Fatalf("buildPromptInput 返回错误: %v", err)
	}
	if len(got) != 1 || got[0].URL == nil || *got[0].URL != "data:image/png;base64,abc123" {
		t.Fatalf("图片 input = %#v，期望内联 data URL", got)
	}
}

// TestBuildPromptInputUsesInlineImageForMalformedHTTPURI 验证只有完整 HTTP URL 才直传。
func TestBuildPromptInputUsesInlineImageForMalformedHTTPURI(t *testing.T) {
	t.Parallel()

	uri := "http:"
	got, err := buildPromptInput([]acp.ContentBlock{{Image: &acp.ContentBlockImage{
		Type: "image", Data: "abc123", MimeType: "image/png", Uri: &uri,
	}}})
	if err != nil {
		t.Fatalf("buildPromptInput 返回错误: %v", err)
	}
	if len(got) != 1 || got[0].URL == nil || *got[0].URL != "data:image/png;base64,abc123" {
		t.Fatalf("畸形 HTTP 图片 input = %#v，期望内联 data URL", got)
	}
}

// TestBuildPromptInputRejectsAudio 验证 V1 不会静默丢弃 ACP audio 内容。
func TestBuildPromptInputRejectsAudio(t *testing.T) {
	t.Parallel()

	_, err := buildPromptInput([]acp.ContentBlock{acp.AudioBlock("AA==", "audio/wav")})
	if err == nil || !strings.Contains(err.Error(), "audio") {
		t.Fatalf("audio 错误 = %v，期望显式拒绝", err)
	}
}

// TestBuildPromptInputRejectsInvalidUnion 验证非法 ACP union 不会被默认为任意内容类型。
func TestBuildPromptInputRejectsInvalidUnion(t *testing.T) {
	t.Parallel()

	_, err := buildPromptInput([]acp.ContentBlock{{}})
	if err == nil {
		t.Fatal("空 ContentBlock 应返回错误")
	}
}

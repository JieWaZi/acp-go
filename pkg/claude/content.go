package claude

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/JieWaZi/acp-go/pkg/claude/protocol"
	acp "github.com/coder/acp-go-sdk"
)

var ErrUnsupportedPromptContent = errors.New("unsupported Claude prompt content")

// outboundContentBlock 表示写给 CLI 的用户内容块开放形状。
type outboundContentBlock struct {
	// Type 是内容块的 wire 判别值。
	Type string `json:"type"`
	// Text 是文本内容。
	Text string `json:"text,omitempty"`
	// Source 是图片或文档来源。
	Source *outboundContentSource `json:"source,omitempty"`
	// Title 是文档上下文的可选标题。
	Title string `json:"title,omitempty"`
	// Context 是文档上下文的可选说明。
	Context string `json:"context,omitempty"`
}

// outboundContentSource 保存 base64 或 URL 资源来源。
type outboundContentSource struct {
	// Type 区分 base64、url 与 text 来源。
	Type string `json:"type"`
	// MediaType 是 base64 内容的 MIME 类型。
	MediaType string `json:"media_type,omitempty"`
	// Data 是 base64 数据或纯文本正文。
	Data string `json:"data,omitempty"`
	// URL 是远程资源地址。
	URL string `json:"url,omitempty"`
}

// promptToUserMessage 严格转换 ACP prompt，并保留消息标识供 echo 关联。
func promptToUserMessage(sessionID, messageID string, blocks []acp.ContentBlock, priority string) (protocol.UserInputMessage, error) {
	if len(blocks) == 0 {
		return protocol.UserInputMessage{}, errors.New("converting Claude prompt: prompt is empty")
	}
	converted := make([]outboundContentBlock, 0, len(blocks))
	for index, block := range blocks {
		content, err := convertPromptBlock(block)
		if err != nil {
			return protocol.UserInputMessage{}, fmt.Errorf("converting Claude prompt block %d: %w", index, err)
		}
		converted = append(converted, content)
	}
	data, err := json.Marshal(converted)
	if err != nil {
		return protocol.UserInputMessage{}, fmt.Errorf("encoding Claude prompt: %w", err)
	}
	return protocol.UserInputMessage{
		Type: "user",
		Message: protocol.UserInputBody{
			Role: "user", Content: data,
		},
		ParentToolUseID: nil,
		SessionID:       sessionID,
		UUID:            messageID,
		Priority:        priority,
		Origin:          protocol.UserInputOrigin{Kind: "user"},
	}, nil
}

// convertPromptBlock 要求 ACP content union 恰有一个受支持变体。
func convertPromptBlock(block acp.ContentBlock) (outboundContentBlock, error) {
	variants := 0
	if block.Text != nil {
		variants++
	}
	if block.Image != nil {
		variants++
	}
	if block.Audio != nil {
		variants++
	}
	if block.ResourceLink != nil {
		variants++
	}
	if block.Resource != nil {
		variants++
	}
	if variants != 1 {
		return outboundContentBlock{}, fmt.Errorf("%w: expected exactly one content variant", ErrUnsupportedPromptContent)
	}

	switch {
	case block.Text != nil:
		return outboundContentBlock{Type: "text", Text: block.Text.Text}, nil
	case block.Image != nil:
		if strings.TrimSpace(block.Image.MimeType) == "" {
			return outboundContentBlock{}, fmt.Errorf("%w: image MIME type is empty", ErrUnsupportedPromptContent)
		}
		if block.Image.Data != "" {
			return outboundContentBlock{Type: "image", Source: &outboundContentSource{
				Type: "base64", MediaType: block.Image.MimeType, Data: block.Image.Data,
			}}, nil
		}
		if block.Image.Uri != nil && *block.Image.Uri != "" {
			return outboundContentBlock{Type: "image", Source: &outboundContentSource{
				Type: "url", URL: *block.Image.Uri,
			}}, nil
		}
		return outboundContentBlock{}, fmt.Errorf("%w: image data and URI are empty", ErrUnsupportedPromptContent)
	case block.ResourceLink != nil:
		link := block.ResourceLink
		text := fmt.Sprintf("<resource name=%q uri=%q", link.Name, link.Uri)
		if link.MimeType != nil && *link.MimeType != "" {
			text += fmt.Sprintf(" mime_type=%q", *link.MimeType)
		}
		text += "></resource>"
		return outboundContentBlock{Type: "text", Text: text}, nil
	case block.Resource != nil:
		return convertEmbeddedResource(block.Resource.Resource)
	default:
		return outboundContentBlock{}, fmt.Errorf("%w: audio content is not supported", ErrUnsupportedPromptContent)
	}
}

// convertEmbeddedResource 保留文本/二进制资源的 URI、MIME 与正文。
func convertEmbeddedResource(resource acp.EmbeddedResourceResource) (outboundContentBlock, error) {
	if resource.TextResourceContents != nil && resource.BlobResourceContents != nil {
		return outboundContentBlock{}, fmt.Errorf("%w: embedded resource has multiple variants", ErrUnsupportedPromptContent)
	}
	if text := resource.TextResourceContents; text != nil {
		mediaType := ""
		if text.MimeType != nil {
			mediaType = *text.MimeType
		}
		return outboundContentBlock{
			Type:   "document",
			Source: &outboundContentSource{Type: "text", MediaType: mediaType, Data: text.Text},
			Title:  text.Uri,
		}, nil
	}
	if blob := resource.BlobResourceContents; blob != nil {
		if blob.MimeType == nil || strings.TrimSpace(*blob.MimeType) == "" {
			return outboundContentBlock{}, fmt.Errorf("%w: embedded blob MIME type is empty", ErrUnsupportedPromptContent)
		}
		return outboundContentBlock{
			Type:   "document",
			Source: &outboundContentSource{Type: "base64", MediaType: *blob.MimeType, Data: blob.Blob},
			Title:  blob.Uri,
		}, nil
	}
	return outboundContentBlock{}, fmt.Errorf("%w: embedded resource is empty", ErrUnsupportedPromptContent)
}

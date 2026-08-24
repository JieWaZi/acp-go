package codex

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/JieWaZi/acp-go/pkg/codex/protocol"

	acp "github.com/coder/acp-go-sdk"
)

// buildPromptInput 将 ACP 内容块等价映射为 codex app-server 的 turn input。
func buildPromptInput(blocks []acp.ContentBlock) ([]protocol.InputElement, error) {
	input := make([]protocol.InputElement, 0, len(blocks))
	for index := range blocks {
		block := &blocks[index]
		if err := block.Validate(); err != nil {
			return nil, fmt.Errorf("invalid prompt content at index %d: %w", index, err)
		}

		element, err := buildPromptInputElement(*block)
		if err != nil {
			return nil, fmt.Errorf("building prompt content at index %d: %w", index, err)
		}
		input = append(input, element)
	}
	return input, nil
}

// buildPromptInputElement 按 ACP union 的唯一变体选择对应 app-server 输入类型。
func buildPromptInputElement(block acp.ContentBlock) (protocol.InputElement, error) {
	switch {
	case block.Text != nil:
		return textInput(block.Text.Text), nil
	case block.Image != nil:
		return imageInput(*block.Image), nil
	case block.ResourceLink != nil:
		return textInput(formatResourceLink(block.ResourceLink.Name, block.ResourceLink.Uri)), nil
	case block.Resource != nil:
		return embeddedResourceInput(block.Resource.Resource)
	case block.Audio != nil:
		// Audio 未声明为受支持输入，必须显式失败，避免用户内容被静默丢弃。
		return protocol.InputElement{}, fmt.Errorf("audio prompt content is not supported")
	default:
		return protocol.InputElement{}, fmt.Errorf("unsupported ACP content union")
	}
}

// textInput 构造 app-server 需要的文本 input，并保留必需的空 text_elements 数组。
func textInput(text string) protocol.InputElement {
	return protocol.InputElement{
		Type:         protocol.UserInputTypeText,
		Text:         &text,
		TextElements: []protocol.TextElementElement{},
	}
}

// imageInput 优先复用可由 app-server 访问的 URL，否则回退到 ACP 内联图片数据。
func imageInput(block acp.ContentBlockImage) protocol.InputElement {
	imageURL := ""
	if block.Uri != nil && isSupportedImageURL(*block.Uri) {
		imageURL = *block.Uri
	} else {
		imageURL = fmt.Sprintf("data:%s;base64,%s", block.MimeType, block.Data)
	}
	return protocol.InputElement{Type: protocol.UserInputTypeImage, URL: &imageURL}
}

// embeddedResourceInput 将文本、图片或二进制资源转换为 app-server 输入。
func embeddedResourceInput(resource acp.EmbeddedResourceResource) (protocol.InputElement, error) {
	if (resource.TextResourceContents == nil) == (resource.BlobResourceContents == nil) {
		return protocol.InputElement{}, fmt.Errorf("embedded resource must contain exactly one variant")
	}
	switch {
	case resource.TextResourceContents != nil:
		textResource := resource.TextResourceContents
		link := formatResourceLink("", textResource.Uri)
		context := fmt.Sprintf("<context ref=%q>\n%s\n</context>", textResource.Uri, textResource.Text)
		return textInput(link + "\n" + context), nil
	case resource.BlobResourceContents != nil:
		blobResource := resource.BlobResourceContents
		if blobResource.MimeType != nil && strings.HasPrefix(*blobResource.MimeType, "image/") {
			imageURL := fmt.Sprintf("data:%s;base64,%s", *blobResource.MimeType, blobResource.Blob)
			return protocol.InputElement{Type: protocol.UserInputTypeImage, URL: &imageURL}, nil
		}
		mimeType := "application/octet-stream"
		if blobResource.MimeType != nil {
			mimeType = *blobResource.MimeType
		}
		link := formatResourceLink("", blobResource.Uri)
		context := fmt.Sprintf(
			"<context ref=%q mimeType=%q encoding=\"base64\">\n%s\n</context>",
			blobResource.Uri,
			mimeType,
			blobResource.Blob,
		)
		return textInput(link + "\n" + context), nil
	default:
		return protocol.InputElement{}, fmt.Errorf("unsupported embedded resource union")
	}
}

// formatResourceLink 复用 codex-acp 的资源链接格式，file URI 缺少名称时取末级文件名。
func formatResourceLink(name, uri string) string {
	if name != "" {
		return fmt.Sprintf("[@%s](%s)", name, uri)
	}
	if strings.HasPrefix(uri, "file://") {
		path := strings.TrimPrefix(uri, "file://")
		if separator := strings.LastIndex(path, "/"); separator >= 0 {
			path = path[separator+1:]
		}
		return fmt.Sprintf("[@%s](%s)", path, uri)
	}
	return uri
}

// isSupportedImageURL 仅允许 HTTP、HTTPS 与 data URL。
func isSupportedImageURL(raw string) bool {
	parsed, err := url.Parse(raw)
	if err != nil {
		return false
	}
	switch parsed.Scheme {
	case "http", "https":
		return parsed.Host != ""
	case "data":
		return parsed.Opaque != ""
	default:
		return false
	}
}

package codex

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"path"
	"strings"
	"sync"

	"acp-go/agents/codex/protocol"
	acp "github.com/coder/acp-go-sdk"
)

var (
	// ErrInvalidPromptContent 表示 ACP ContentBlock union 没有合法 discriminator 变体。
	ErrInvalidPromptContent = errors.New("invalid ACP prompt content")
)

// activePrompt 保存一个 session 唯一 pending/active turn 的取消身份。
// 字段对应 upstream ActivePrompt 与 PendingTurnStart，合并后仍保持 turn-start 前后语义。
type activePrompt struct {
	// mu 保护 turnID、cancelRequested 和 finished。
	mu sync.Mutex
	// generation 是 prompt 启动时的 session generation。
	generation uint64
	// runCtx 只约束当前 RunTurn；普通 ACP 取消保留它以观察迟到 start，session close 则显式终止它。
	runCtx context.Context
	// runCancel 在 session/Adapter close 时解除 pending turn/start 或 completion waiter。
	runCancel context.CancelFunc
	// turnID 在 turn/start 响应前为空，迟到响应仍会填入以便 interrupt。
	turnID string
	// cancelRequested 表示 ACP cancel、请求 context 或 close 已到达。
	cancelRequested bool
	// finished 表示前台 Prompt 已决定结果；迟到 start 必须视为 stale。
	finished bool
	// cancelSignal 在首次取消时关闭，解除 turn-start 前的前台等待。
	cancelSignal chan struct{}
	// cancelOnce 保证 cancelSignal 只关闭一次。
	cancelOnce sync.Once
	// interruptOnce 保证同一 turn 最多发送一次 turn/interrupt。
	interruptOnce sync.Once
	// interruptDone 在 interrupt 请求结束时关闭；尚未触发时保持开放。
	interruptDone chan struct{}
	// turnStarted 在 turn/start 返回身份后关闭，steering 可等待 pending start 而不制造 rival turn。
	turnStarted chan struct{}
	// turnStartedOnce 保证迟到回调或异常重复回调不会重复关闭。
	turnStartedOnce sync.Once
	// foregroundDone 在 ACP Prompt 或 steering 启动调用完成其前台返回后关闭。
	foregroundDone chan struct{}
	// foregroundDoneOnce 保证前台返回信号只关闭一次。
	foregroundDoneOnce sync.Once
	// backgroundDone 在 RunTurn 完整结束、迟到身份已处理后关闭。
	backgroundDone chan struct{}
	// backgroundDoneOnce 保证清理路径幂等。
	backgroundDoneOnce sync.Once
}

// newActivePrompt 创建尚未取消、尚无 turn ID 的 prompt 身份与独立 RunTurn 生命周期。
func newActivePrompt(parent context.Context, generation uint64) *activePrompt {
	runCtx, runCancel := context.WithCancel(parent)
	return &activePrompt{
		generation:     generation,
		runCtx:         runCtx,
		runCancel:      runCancel,
		cancelSignal:   make(chan struct{}),
		interruptDone:  make(chan struct{}),
		turnStarted:    make(chan struct{}),
		foregroundDone: make(chan struct{}),
		backgroundDone: make(chan struct{}),
	}
}

// markForegroundDone 通知 close 路径前台调用已执行完 finally 清理。
func (p *activePrompt) markForegroundDone() {
	p.foregroundDoneOnce.Do(func() { close(p.foregroundDone) })
}

// cancelRun 终止底层 pending turn/start 或 completion waiter；普通请求取消不会调用它，以保留迟到观察者。
func (p *activePrompt) cancelRun() {
	p.runCancel()
}

// requestCancel 幂等记录取消，并返回当前已知 turn ID。
func (p *activePrompt) requestCancel() string {
	p.mu.Lock()
	p.cancelRequested = true
	turnID := p.turnID
	p.mu.Unlock()
	p.cancelOnce.Do(func() { close(p.cancelSignal) })
	return turnID
}

// setTurn 记录 turn/start 结果，并返回它是否已经属于取消/结束后的迟到 turn。
func (p *activePrompt) setTurn(turnID string) bool {
	p.mu.Lock()
	p.turnID = turnID
	late := p.cancelRequested || p.finished
	p.mu.Unlock()
	p.turnStartedOnce.Do(func() { close(p.turnStarted) })
	return late
}

// markBackgroundDone 通知 steering/close 当前 prompt 已完全释放 session 槽位。
func (p *activePrompt) markBackgroundDone() {
	p.backgroundDoneOnce.Do(func() { close(p.backgroundDone) })
}

// currentTurn 返回当前 turn ID 与取消状态快照。
func (p *activePrompt) currentTurn() (string, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.turnID, p.cancelRequested
}

// markForegroundFinished 标记 ACP Prompt 已返回；后台只负责 late interrupt/completion 清理。
func (p *activePrompt) markForegroundFinished() {
	p.mu.Lock()
	p.finished = true
	p.mu.Unlock()
}

// buildPromptInput 等价移植 CodexAcpClient.buildPromptItems 的 Text/Image/Resource 规则。
func buildPromptInput(blocks []acp.ContentBlock) ([]protocol.InputElement, error) {
	input := make([]protocol.InputElement, 0, len(blocks))
	for index := range blocks {
		block := blocks[index]
		switch {
		case block.Text != nil:
			text := block.Text.Text
			input = append(input, protocol.InputElement{
				Type: protocol.UserInputTypeText, Text: &text, TextElements: []protocol.TextElementElement{},
			})
		case block.Image != nil:
			imageURL := supportedImageURL(block.Image)
			input = append(input, protocol.InputElement{Type: protocol.UserInputTypeImage, URL: &imageURL})
		case block.ResourceLink != nil:
			text := formatResourceLink(block.ResourceLink.Name, block.ResourceLink.Uri)
			input = append(input, protocol.InputElement{
				Type: protocol.UserInputTypeText, Text: &text, TextElements: []protocol.TextElementElement{},
			})
		case block.Resource != nil:
			element, err := embeddedResourceInput(block.Resource.Resource)
			if err != nil {
				return nil, fmt.Errorf("converting prompt block %d: %w", index, err)
			}
			input = append(input, element)
		case block.Audio != nil:
			// 固定 upstream V1 buildPromptItems 明确过滤 audio，且能力不会声明 audio。
			continue
		default:
			return nil, fmt.Errorf("converting prompt block %d: %w", index, ErrInvalidPromptContent)
		}
	}
	return input, nil
}

// supportedImageURL 保留 http/https/data URI，否则使用内嵌 base64 data URI。
func supportedImageURL(image *acp.ContentBlockImage) string {
	if image.Uri != nil {
		parsed, err := url.Parse(*image.Uri)
		if err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https" || parsed.Scheme == "data") {
			return *image.Uri
		}
	}
	return "data:" + image.MimeType + ";base64," + image.Data
}

// embeddedResourceInput 把 text/blob resource 转成固定上游的带来源 context 或 image data URI。
func embeddedResourceInput(resource acp.EmbeddedResourceResource) (protocol.InputElement, error) {
	if resource.TextResourceContents != nil {
		value := resource.TextResourceContents
		link := formatResourceLink("", value.Uri)
		text := fmt.Sprintf("%s\n<context ref=\"%s\">\n%s\n</context>", link, value.Uri, value.Text)
		return protocol.InputElement{
			Type: protocol.UserInputTypeText, Text: &text, TextElements: []protocol.TextElementElement{},
		}, nil
	}
	if resource.BlobResourceContents != nil {
		value := resource.BlobResourceContents
		mimeType := "application/octet-stream"
		if value.MimeType != nil {
			mimeType = *value.MimeType
		}
		if strings.HasPrefix(mimeType, "image/") {
			imageURL := "data:" + mimeType + ";base64," + value.Blob
			return protocol.InputElement{Type: protocol.UserInputTypeImage, URL: &imageURL}, nil
		}
		link := formatResourceLink("", value.Uri)
		text := fmt.Sprintf(
			"%s\n<context ref=\"%s\" mimeType=\"%s\" encoding=\"base64\">\n%s\n</context>",
			link,
			value.Uri,
			mimeType,
			value.Blob,
		)
		return protocol.InputElement{
			Type: protocol.UserInputTypeText, Text: &text, TextElements: []protocol.TextElementElement{},
		}, nil
	}
	return protocol.InputElement{}, ErrInvalidPromptContent
}

// formatResourceLink 等价移植 upstream formatUriAsLink 的显示规则。
func formatResourceLink(name, uri string) string {
	if name != "" {
		return "[@" + name + "](" + uri + ")"
	}
	if strings.HasPrefix(uri, "file://") {
		fileName := path.Base(strings.TrimPrefix(uri, "file://"))
		return "[@" + fileName + "](" + uri + ")"
	}
	return uri
}

package codex

import (
	"context"
	"fmt"
	"strings"

	"acp-go/agents/codex/protocol"

	acp "github.com/coder/acp-go-sdk"
)

// replayThreadHistory 按 upstream streamThreadHistory 顺序重放 turn/item。
// 已有 eventHandler/toolMapper 负责完成项映射；这里只补历史 userMessage 的反向内容转换。
func (a *Agent) replayThreadHistory(ctx context.Context, state *sessionState, thread protocol.Thread) error {
	if len(thread.Turns) == 0 {
		return nil
	}
	updater := a.currentSessionUpdater()
	if updater == nil {
		return ErrConnectionNotReady
	}
	handler := newEventHandler(updater, acp.SessionId(state.id), a.logger, state.terminalOutputMode)
	seenUserMessages := make(map[string]struct{})
	for _, turn := range thread.Turns {
		for _, item := range turn.Items {
			if !a.sessions.isCurrent(state) {
				return ErrSessionClosing
			}
			var err error
			if item.Type == protocol.UserMessage {
				if _, seen := seenUserMessages[item.ID]; seen {
					continue
				}
				seenUserMessages[item.ID] = struct{}{}
				err = a.replayUserMessage(ctx, state, handler, item)
			} else {
				err = handler.handleHistoryItem(ctx, protocol.ItemCompletedNotification{
					ThreadID: thread.ID,
					TurnID:   turn.ID,
					Item:     item,
				})
			}
			if err != nil {
				return err
			}
			// 外部 SDK callback 返回后再次校验，避免后续历史继续写入已关闭 generation。
			if !a.sessions.isCurrent(state) {
				return ErrSessionClosing
			}
		}
	}
	return nil
}

// replayUserMessage 把一个历史 userMessage 的内容按原顺序发送为 ACP chunks。
// 每个外部 SDK callback 前后都重新检查 session generation，防止 close 后继续回放多块消息。
func (a *Agent) replayUserMessage(
	ctx context.Context,
	state *sessionState,
	handler *eventHandler,
	item protocol.ThreadItem,
) error {
	for _, content := range item.Content {
		blocks := historyContentBlocks(content)
		for _, block := range blocks {
			if !a.sessions.isCurrent(state) {
				return ErrSessionClosing
			}
			update := acp.UpdateUserMessage(block)
			update.UserMessageChunk.MessageId = &item.ID
			if err := handler.emit(ctx, update); err != nil {
				return err
			}
			if !a.sessions.isCurrent(state) {
				return ErrSessionClosing
			}
		}
	}
	return nil
}

// historyContentBlocks 等价裁剪 upstream userInputToContentBlocks 的 V1 分支。
func historyContentBlocks(content protocol.ContentElement) []acp.ContentBlock {
	if content.String != nil && *content.String != "" {
		return []acp.ContentBlock{acp.TextBlock(*content.String)}
	}
	if content.UserInput == nil {
		return []acp.ContentBlock{}
	}
	input := content.UserInput
	switch input.Type {
	case protocol.UserInputTypeText:
		if input.Text == nil || *input.Text == "" {
			return []acp.ContentBlock{}
		}
		return []acp.ContentBlock{acp.TextBlock(*input.Text)}
	case protocol.UserInputTypeImage:
		if input.URL == nil || *input.URL == "" {
			return []acp.ContentBlock{}
		}
		return []acp.ContentBlock{acp.TextBlock(formatResourceLink("image", *input.URL))}
	case protocol.LocalImage:
		if input.Path == nil || *input.Path == "" {
			return []acp.ContentBlock{}
		}
		uri := *input.Path
		if !strings.HasPrefix(uri, "file://") {
			uri = "file://" + uri
		}
		return []acp.ContentBlock{acp.TextBlock(formatResourceLink("", uri))}
	case protocol.Skill:
		if input.Name == nil || input.Path == nil {
			return []acp.ContentBlock{}
		}
		return []acp.ContentBlock{acp.TextBlock(fmt.Sprintf("skill:%s (%s)", *input.Name, *input.Path))}
	default:
		return []acp.ContentBlock{}
	}
}

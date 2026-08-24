package claude

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/JieWaZi/acp-go/pkg/claude/protocol"
	acp "github.com/coder/acp-go-sdk"
)

const (
	// maxHistoryLineBytes 限制单条本地历史记录大小。
	maxHistoryLineBytes = 8 << 20
	// maxHistoryFileBytes 限制单次 load 读取的 transcript 总量。
	maxHistoryFileBytes = 64 << 20
)

var (
	// ErrClaudeHistoryNotFound 表示恢复成功但本机找不到可回放 transcript。
	ErrClaudeHistoryNotFound = errors.New("Claude session history not found")
	// ErrInvalidClaudeHistorySessionID 表示 Session ID 不能安全用作 transcript 文件名。
	ErrInvalidClaudeHistorySessionID = errors.New("invalid Claude history session id")
	// localCommandMarkerPattern 删除客户端本地命令的内部标记段。
	localCommandMarkerPattern = regexp.MustCompile(`(?s)</?(?:command-name|command-message|command-args)>.*?</(?:command-name|command-message|command-args)>|<(?:local-command-stdout|local-command-stderr)>.*?</(?:local-command-stdout|local-command-stderr)>`)
	// historySessionIDPattern 只允许不会改变目录或 Glob 语义的文件名字符。
	historySessionIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,255}$`)
)

// historyEntry 是本地 transcript 中可表达消息的窄视图。
type historyEntry struct {
	// Type 区分 user、assistant 与内部记录。
	Type string `json:"type"`
	// UUID 是消息标识。
	UUID string `json:"uuid,omitempty"`
	// ParentToolUseID 标识子工具或子任务消息。
	ParentToolUseID *string `json:"parent_tool_use_id,omitempty"`
	// Message 保存角色与开放 content。
	Message historyMessage `json:"message"`
}

// historyMessage 保存 transcript 消息主体。
type historyMessage struct {
	// ID 是模型消息标识。
	ID string `json:"id,omitempty"`
	// Role 是 user 或 assistant。
	Role string `json:"role"`
	// Content 保留字符串或 content block 数组。
	Content json.RawMessage `json:"content"`
}

// replaySessionHistory 查找本地 transcript，并按原顺序发送可表达更新。
func (a *Agent) replaySessionHistory(ctx context.Context, session *claudeSession) error {
	path, err := findHistoryPath(session.id)
	if err != nil {
		return fmt.Errorf("loading Claude session %q: %w", session.id, err)
	}
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("opening Claude history: %w", err)
	}
	defer file.Close()
	if info, statErr := file.Stat(); statErr != nil {
		return fmt.Errorf("checking Claude history: %w", statErr)
	} else if info.Size() > maxHistoryFileBytes {
		return fmt.Errorf("checking Claude history: transcript exceeds %d bytes", maxHistoryFileBytes)
	}

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64<<10), maxHistoryLineBytes)
	for scanner.Scan() {
		var entry historyEntry
		if json.Unmarshal(scanner.Bytes(), &entry) != nil {
			continue
		}
		if entry.ParentToolUseID != nil || (entry.Type != "user" && entry.Type != "assistant") {
			continue
		}
		if err := session.replayEntry(ctx, entry); err != nil {
			return err
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("reading Claude history: %w", err)
	}
	return nil
}

// findHistoryPath 在配置目录的 project transcript 中按 Session ID 精确查找。
func findHistoryPath(sessionID string) (string, error) {
	if !historySessionIDPattern.MatchString(sessionID) {
		return "", ErrInvalidClaudeHistorySessionID
	}
	configDirectory := os.Getenv("CLAUDE_CONFIG_DIR")
	if configDirectory == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		configDirectory = filepath.Join(home, ".claude")
	}
	matches, err := filepath.Glob(filepath.Join(configDirectory, "projects", "*", sessionID+".jsonl"))
	if err != nil {
		return "", err
	}
	if len(matches) == 0 {
		return "", ErrClaudeHistoryNotFound
	}
	// 多个 project 意外包含同一 ID 时使用排序首项，避免文件系统遍历顺序造成不确定回放。
	sort.Strings(matches)
	return matches[0], nil
}

// replayEntry 把一条 user/assistant 历史消息转换为 ACP 更新。
func (s *claudeSession) replayEntry(ctx context.Context, entry historyEntry) error {
	blocks, err := historyContentBlocks(entry.Message.Content)
	if err != nil {
		return nil
	}
	messageID := entry.UUID
	for _, block := range blocks {
		if entry.Message.Role == "user" {
			switch block.Type {
			case "text":
				text := stripLocalCommandMarkers(block.Text)
				if text != "" {
					update := acp.UpdateUserMessageText(text)
					if update.UserMessageChunk != nil && messageID != "" {
						update.UserMessageChunk.MessageId = &messageID
					}
					if err := s.agent.sendUpdate(ctx, s.id, update); err != nil {
						return err
					}
				}
			case "image":
				if block.Source != nil && block.Source.Type == "base64" && block.Source.Data != "" && block.Source.MediaType != "" {
					update := acp.UpdateUserMessage(acp.ImageBlock(block.Source.Data, block.Source.MediaType))
					if update.UserMessageChunk != nil && messageID != "" {
						update.UserMessageChunk.MessageId = &messageID
					}
					if err := s.agent.sendUpdate(ctx, s.id, update); err != nil {
						return err
					}
				}
			case "tool_result":
				if err := s.completeTool(ctx, block); err != nil {
					return err
				}
			}
			continue
		}
		switch block.Type {
		case "text":
			if err := s.sendAgentText(ctx, messageID, block.Text, false); err != nil {
				return err
			}
		case "thinking":
			if err := s.sendAgentText(ctx, messageID, block.Thinking, true); err != nil {
				return err
			}
		case "tool_use":
			if err := s.startTool(ctx, block); err != nil {
				return err
			}
		}
	}
	return nil
}

// historyContentBlocks 兼容 transcript 中的字符串与 content block 数组。
func historyContentBlocks(raw json.RawMessage) ([]protocol.ContentBlock, error) {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return []protocol.ContentBlock{{Type: "text", Text: text}}, nil
	}
	var blocks []protocol.ContentBlock
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return nil, err
	}
	return blocks, nil
}

// stripLocalCommandMarkers 删除内部命令标签，但保留同一消息中的真实用户文本。
func stripLocalCommandMarkers(value string) string {
	for {
		cleaned := localCommandMarkerPattern.ReplaceAllString(value, "")
		if cleaned == value {
			return strings.TrimSpace(cleaned)
		}
		value = cleaned
	}
}

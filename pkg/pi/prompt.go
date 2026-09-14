package pi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	acp "github.com/coder/acp-go-sdk"
)

// Prompt 对照 pi-acp 串行执行提示，等待整轮 settled 而非中间 agent_end。
func (a *Agent) Prompt(ctx context.Context, request acp.PromptRequest) (acp.PromptResponse, error) {
	s, err := a.get(request.SessionId)
	if err != nil {
		return acp.PromptResponse{}, err
	}
	s.mutex.Lock()
	generation := s.generation
	s.mutex.Unlock()
	if err := s.operation.Lock(ctx); err != nil {
		return acp.PromptResponse{}, err
	}
	defer s.operation.Unlock()
	if err = ctx.Err(); err != nil {
		return acp.PromptResponse{}, err
	}
	s.mutex.Lock()
	cancelled := generation != s.generation
	s.mutex.Unlock()
	if cancelled {
		return acp.PromptResponse{StopReason: acp.StopReasonCancelled}, nil
	}
	message, images, err := promptContent(request.Prompt)
	if err != nil {
		return acp.PromptResponse{}, err
	}

	turnCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	turn := make(chan acp.StopReason, 1)
	s.mutex.Lock()
	if generation != s.generation {
		s.mutex.Unlock()
		return acp.PromptResponse{StopReason: acp.StopReasonCancelled}, nil
	}
	s.turn = turn
	s.turnContext = turnCtx
	s.cancelTurn = cancel
	s.cancelled = false
	s.failure = nil
	s.usage = nil
	s.mutex.Unlock()
	defer func() {
		s.mutex.Lock()
		if s.turn == turn {
			s.turn = nil
		}
		s.mutex.Unlock()
	}()
	if handled, err := a.slash(ctx, s, message, len(images) > 0); handled {
		return acp.PromptResponse{StopReason: acp.StopReasonEndTurn}, err
	}
	s.mutex.Lock()
	call, err := s.process.begin(turnCtx, "prompt", map[string]any{"message": message, "images": images})
	s.mutex.Unlock()
	if err == nil {
		err = call.wait(turnCtx, nil)
	}
	if err != nil {
		a.abortTurn(s, turn)
		return acp.PromptResponse{}, err
	}
	select {
	case reason := <-turn:
		s.mutex.Lock()
		failure, usage := s.failure, s.usage
		s.mutex.Unlock()
		return acp.PromptResponse{StopReason: reason, Usage: usage}, failure
	case <-ctx.Done():
		a.abortTurn(s, turn)
		return acp.PromptResponse{}, ctx.Err()
	}
}

// promptContent 移植 pi-acp 的文本、图片、链接及嵌入上下文转换。
func promptContent(blocks []acp.ContentBlock) (string, []any, error) {
	values, err := convert[[]map[string]any](blocks)
	if err != nil {
		return "", nil, err
	}
	var message strings.Builder
	images := []any{}
	for _, block := range values {
		switch block["type"] {
		case "text":
			message.WriteString(text(block["text"]))
		case "image":
			images = append(images, map[string]any{"type": "image", "data": block["data"], "mimeType": block["mimeType"]})
		case "resource_link":
			fmt.Fprintf(&message, "\n[Context] %s", text(block["uri"]))
		case "resource":
			resource := object(block["resource"])
			uri := text(resource["uri"])
			switch {
			case resource["text"] != nil:
				mimeType := text(resource["mimeType"])
				if mimeType == "" {
					mimeType = "text/plain"
				}
				fmt.Fprintf(&message, "\n[Embedded Context] %s (%s)\n%s", uri, mimeType, text(resource["text"]))
			case resource["blob"] != nil:
				mimeType := text(resource["mimeType"])
				if mimeType == "" {
					mimeType = "application/octet-stream"
				}
				decoded, decodeErr := base64.StdEncoding.DecodeString(text(resource["blob"]))
				if decodeErr != nil {
					return "", nil, acp.NewInvalidParams(map[string]any{"message": "invalid embedded resource blob"})
				}
				fmt.Fprintf(&message, "\n[Embedded Context] %s (%s, %d bytes)", uri, mimeType, len(decoded))
			default:
				fmt.Fprintf(&message, "\n[Embedded Context] %s", uri)
			}
		case "audio":
			return "", nil, acp.NewInvalidParams(map[string]any{"message": "Pi does not support audio"})
		default:
			return "", nil, acp.NewInvalidParams(nil)
		}
	}
	return message.String(), images, nil
}

// slash 在 Go 中实现 pi-acp 的内置命令，其余技能与模板交给 Pi 原生解析。
func (a *Agent) slash(ctx context.Context, s *session, message string, hasImages bool) (bool, error) {
	message = strings.TrimSpace(message)
	if hasImages || !strings.HasPrefix(message, "/") {
		return false, nil
	}
	name, arg, _ := strings.Cut(message, " ")
	name = strings.TrimPrefix(name, "/")
	arg = strings.TrimSpace(arg)
	command := ""
	params := map[string]any{}
	var result map[string]any
	switch name {
	case "compact":
		command = "compact"
		params["customInstructions"] = arg
	case "session":
		command = "get_session_stats"
	case "name":
		if arg == "" {
			return true, acp.NewInvalidParams(nil)
		}
		command = "set_session_name"
		params["name"] = arg
	case "steering", "follow-up":
		if arg == "" {
			command = "get_state"
		} else {
			if arg != "all" && arg != "one-at-a-time" {
				return true, acp.NewInvalidParams(nil)
			}
			command = "set_steering_mode"
			if name == "follow-up" {
				command = "set_follow_up_mode"
			}
			params["mode"] = arg
		}
	case "autocompact":
		if err := s.process.call(ctx, "get_state", nil, &result); err != nil {
			return true, err
		}
		enabled, _ := result["autoCompactionEnabled"].(bool)
		switch arg {
		case "on":
			enabled = true
		case "off":
			enabled = false
		case "", "toggle":
			enabled = !enabled
		default:
			return true, acp.NewInvalidParams(nil)
		}
		command = "set_auto_compaction"
		params["enabled"] = enabled
	case "export":
		if _, err := os.Stat(s.file); err != nil {
			return true, a.emitAgentMessage(ctx, s, map[string]any{
				"type": "text",
				"text": "Nothing to export yet.",
			})
		}
		command = "export_html"
		params["outputPath"] = filepath.Join(s.cwd, "pi-session-"+string(s.id)+".html")
	case "changelog":
		executable, err := filepath.EvalSymlinks(a.config.PiPath)
		if err != nil {
			return true, err
		}
		file := filepath.Join(filepath.Dir(filepath.Dir(executable)), "CHANGELOG.md")
		data, err := os.ReadFile(file)
		if err != nil {
			return true, err
		}
		content := []rune(string(data))
		return true, a.emitAgentMessage(ctx, s, map[string]any{
			"type": "text",
			"text": string(content[:min(20000, len(content))]),
		})
	default:
		return false, nil
	}
	if err := s.process.call(ctx, command, params, &result); err != nil {
		return true, err
	}
	if name == "name" {
		update := map[string]any{
			"sessionUpdate": "session_info_update",
			"title":         arg,
			"updatedAt":     time.Now().UTC().Format(time.RFC3339),
		}
		if err := a.emit(ctx, s, update); err != nil {
			return true, err
		}
	}
	if name == "export" {
		path := text(result["path"])
		if path == "" {
			path = text(params["outputPath"])
		}
		uri := (&url.URL{Scheme: "file", Path: path}).String()
		return true, a.emitAgentMessage(ctx, s, map[string]any{
			"type":     "resource_link",
			"uri":      uri,
			"name":     filepath.Base(path),
			"mimeType": "text/html",
		})
	}
	encoded, _ := json.Marshal(result)
	switch name {
	case "name":
		encoded = []byte("Session name set to: " + arg)
	case "compact":
		encoded = []byte(fmt.Sprintf(
			"Compaction completed. Tokens before: %v\n%s",
			result["tokensBefore"],
			text(result["summary"]),
		))
	case "steering", "follow-up":
		value := arg
		if value == "" {
			field := "steeringMode"
			if name == "follow-up" {
				field = "followUpMode"
			}
			value = text(result[field])
		}
		encoded = []byte(name + " mode: " + value)
	case "autocompact":
		encoded = []byte(fmt.Sprintf("Automatic compaction: %v", params["enabled"]))
	default:
		if result == nil {
			encoded = []byte("Pi /" + name + " complete")
		}
	}
	return true, a.emitAgentMessage(ctx, s, map[string]any{
		"type": "text",
		"text": string(encoded),
	})
}

// emitAgentMessage 发布一个标准 Agent 消息内容块。
func (a *Agent) emitAgentMessage(ctx context.Context, s *session, content map[string]any) error {
	return a.emit(ctx, s, map[string]any{
		"sessionUpdate": "agent_message_chunk",
		"content":       content,
	})
}

// abortTurn 等待被取消的回合清空事件，避免上一轮终态误结束下一轮。
func (a *Agent) abortTurn(s *session, turn <-chan acp.StopReason) {
	_ = a.Cancel(context.Background(), acp.CancelNotification{SessionId: s.id})
	select {
	case <-turn:
	case <-time.After(3 * time.Second):
		cleanup, stop := context.WithTimeout(context.Background(), 3*time.Second)
		defer stop()
		_, _ = a.CloseSession(cleanup, acp.CloseSessionRequest{SessionId: s.id})
	}
}

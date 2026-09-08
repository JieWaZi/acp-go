package pi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/JieWaZi/acp-go/pkg/autoreview"
	acp "github.com/coder/acp-go-sdk"
)

// emit 通过 SDK 发布标准通知，保留顺序并将错误传给回合终态。
func (a *Agent) emit(ctx context.Context, s *session, update map[string]any) error {
	a.mutex.Lock()
	host := a.host
	a.mutex.Unlock()
	if host == nil {
		return nil
	}
	notification, err := convert[acp.SessionNotification](map[string]any{"sessionId": s.id, "update": update})
	if err != nil {
		return err
	}
	return host.SessionUpdate(ctx, notification)
}

// events 对照 pi-acp 的事件映射；只在 agent_settled 后完成整个 ACP 回合。
func (a *Agent) events(s *session) {
	eventCtx, eventCancel := context.WithCancel(context.Background())
	defer func() {
		eventCancel()
		s.mutex.Lock()
		if s.cancelTurn != nil {
			s.cancelTurn()
		}
		s.mutex.Unlock()
		s.callbacks.Wait()
		close(s.eventsDone)
	}()
	for event := range s.process.events {
		s.mutex.Lock()
		ctx := s.turnContext
		if ctx == nil {
			ctx = eventCtx
		}
		s.mutex.Unlock()
		kind := text(event["type"])
		if kind == "extension_ui_request" {
			if event["method"] == "setStatus" && event["statusKey"] == "acp-go.permission-review" {
				var record map[string]any
				if json.Unmarshal([]byte(text(event["statusText"])), &record) == nil && text(record["toolCallId"]) != "" {
					metadata := autoreview.Metadata(autoreview.Decision{Outcome: text(record["outcome"]), RiskLevel: text(record["risk_level"])}, record["failed"] == true)
					if err := a.emit(ctx, s, map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": record["toolCallId"], "_meta": map[string]any{"acp-go/permission-review": metadata}}); err != nil {
						s.mutex.Lock()
						s.failure = err
						s.mutex.Unlock()
					}
				}
				continue
			}
			s.callbacks.Add(1)
			go func(event map[string]any) { defer s.callbacks.Done(); a.extensionUI(ctx, s, event) }(event)
			continue
		}
		if kind == "agent_settled" {
			s.mutex.Lock()
			turn := s.turn
			reason := acp.StopReasonEndTurn
			if s.cancelled {
				reason = acp.StopReasonCancelled
			}
			s.turn = nil
			s.mutex.Unlock()
			if turn != nil {
				turn <- reason
			}
			continue
		}
		var update map[string]any
		switch kind {
		case "message_update":
			message := object(event["assistantMessageEvent"])
			typ := text(message["type"])
			if typ == "text_delta" || typ == "thinking_delta" {
				name := "agent_message_chunk"
				if typ == "thinking_delta" {
					name = "agent_thought_chunk"
				}
				update = map[string]any{"sessionUpdate": name, "content": map[string]any{"type": "text", "text": text(message["delta"])}}
			} else if strings.HasPrefix(typ, "toolcall_") {
				tool := object(message["toolCall"])
				if tool == nil {
					parts := list(object(message["partial"])["content"])
					index, _ := message["contentIndex"].(float64)
					if index >= 0 && int(index) < len(parts) {
						tool = object(parts[int(index)])
					}
				}
				if id := text(tool["id"]); id != "" {
					update = s.toolUpdate(id, text(tool["name"]), "pending", tool["arguments"], nil, false)
				}
			}
		case "message_end":
			message := object(event["message"])
			if message["role"] == "assistant" {
				if reason := text(message["stopReason"]); reason == "error" {
					s.mutex.Lock()
					s.failure = errors.New("Pi model request failed: " + text(message["errorMessage"]))
					s.mutex.Unlock()
				}
			}
		case "tool_execution_start":
			s.snapshotFile(event)
			update = s.toolUpdate(text(event["toolCallId"]), text(event["toolName"]), "in_progress", event["args"], nil, false)
		case "tool_execution_update":
			update = s.toolUpdate(text(event["toolCallId"]), text(event["toolName"]), "in_progress", nil, event["partialResult"], false)
		case "tool_execution_end":
			failed, _ := event["isError"].(bool)
			status := "completed"
			if failed {
				status = "failed"
			}
			update = s.toolUpdate(text(event["toolCallId"]), text(event["toolName"]), status, nil, event["result"], true)
		case "auto_retry_start", "auto_retry_end", "auto_compaction_start", "auto_compaction_end", "extension_error":
			messages := map[string]string{"auto_retry_start": fmt.Sprintf("Retrying model request (attempt %v).", event["attempt"]), "auto_retry_end": "Retry finished.", "auto_compaction_start": "Compacting session context…", "auto_compaction_end": "Session compaction finished.", "extension_error": "Pi extension error: " + text(event["error"])}
			if kind == "auto_retry_end" && event["success"] == true {
				s.mutex.Lock()
				s.failure = nil
				s.mutex.Unlock()
			}
			update = map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": messages[kind]}}
		}
		if update != nil {
			if err := a.emit(context.WithoutCancel(ctx), s, update); err != nil {
				s.mutex.Lock()
				s.failure = err
				s.mutex.Unlock()
			}
		}
	}
	s.mutex.Lock()
	turn := s.turn
	s.turn = nil
	if s.failure == nil {
		s.failure = errors.New("Pi process exited during prompt")
	}
	s.mutex.Unlock()
	if turn != nil {
		turn <- acp.StopReasonCancelled
	}
}

// toolUpdate 维护工具身份和状态，并映射原生内容与工作目录路径。
func (s *session) toolUpdate(id, name, status string, input, output any, final bool) map[string]any {
	if id == "" {
		return nil
	}
	kind := "tool_call"
	previous := s.tools[id]
	if previous != "" {
		kind = "tool_call_update"
		if status == "pending" {
			status = previous
		}
	}
	s.tools[id] = status
	result := map[string]any{"sessionUpdate": kind, "toolCallId": id, "status": status}
	if name != "" {
		result["title"] = name
		toolKind := "other"
		switch name {
		case "bash":
			toolKind = "execute"
		case "read":
			toolKind = "read"
		case "write", "edit":
			toolKind = "edit"
		case "grep", "find", "ls":
			toolKind = "search"
		}
		result["kind"] = toolKind
	}
	if input != nil {
		result["rawInput"] = input
		args := object(input)
		path := text(args["path"])
		if path != "" {
			if !filepath.IsAbs(path) {
				path = filepath.Join(s.cwd, path)
			}
			result["locations"] = []any{map[string]any{"path": path}}
		}
	}
	if output != nil {
		result["content"] = toolContents(object(output)["content"])
		if final {
			result["rawOutput"] = output
		}
	}
	s.decorateTool(result, id, name, status, input, output, final)
	if final {
		delete(s.tools, id)
		delete(s.snapshots, id)
		delete(s.bashOutput, id)
	}
	return result
}

// contentBlocks 对照 upstream 回放文本和图片，不将工具私有类型传给 ACP。
func contentBlocks(content any) []any {
	if value, ok := content.(string); ok {
		return []any{map[string]any{"type": "text", "text": value}}
	}
	out := []any{}
	for _, item := range list(content) {
		block := object(item)
		switch block["type"] {
		case "text":
			out = append(out, map[string]any{"type": "text", "text": text(block["text"])})
		case "image":
			out = append(out, map[string]any{"type": "image", "mimeType": block["mimeType"], "data": block["data"]})
		}
	}
	return out
}

// toolContents 将 Pi 内容包装为标准 ACP 工具内容。
func toolContents(content any) []any {
	out := []any{}
	for _, block := range contentBlocks(content) {
		out = append(out, map[string]any{"type": "content", "content": block})
	}
	return out
}

// messageText 只拼接 Pi 文本内容。
func messageText(content any) string {
	var b strings.Builder
	for _, item := range contentBlocks(content) {
		block := object(item)
		if block["type"] == "text" {
			b.WriteString(text(block["text"]))
		}
	}
	return b.String()
}

// extensionUI 对照 pi-acp 的 select/confirm 桥，并用 ACP 表单承接输入和编辑。
func (a *Agent) extensionUI(ctx context.Context, s *session, event map[string]any) {
	id := text(event["id"])
	if id == "" {
		return
	}
	response := map[string]any{"type": "extension_ui_response", "id": id, "cancelled": true}
	defer func() { _ = s.process.write(response) }()
	if timeout, ok := event["timeout"].(float64); ok && timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(timeout)*time.Millisecond)
		defer cancel()
	}
	a.mutex.Lock()
	host := a.host
	formUI := a.formUI
	a.mutex.Unlock()
	if host == nil {
		return
	}
	method := text(event["method"])
	switch method {
	case "confirm", "select":
		if method == "select" && !mcpApprovalSelection(event) {
			if !formUI {
				return
			}
			choices := []string{}
			for _, value := range list(event["options"]) {
				choices = append(choices, text(value))
			}
			if len(choices) == 0 {
				return
			}
			result, err := host.UnstableCreateElicitation(ctx, acp.UnstableCreateElicitationRequest{Form: &acp.UnstableCreateElicitationForm{Mode: "form", Message: text(event["title"]), Meta: map[string]any{"sessionId": s.id}, RequestedSchema: acp.UnstableElicitationSchema{Type: acp.UnstableElicitationSchemaTypeObject, Properties: map[string]any{"value": map[string]any{"type": "string", "enum": choices}}, Required: []string{"value"}}}})
			if err == nil && ctx.Err() == nil && result.Accept != nil {
				for _, value := range choices {
					if result.Accept.Content["value"] == value {
						delete(response, "cancelled")
						response["value"] = value
						return
					}
				}
			}
			return
		}
		options := []acp.PermissionOption{}
		if method == "confirm" {
			options = []acp.PermissionOption{{OptionId: "yes", Name: "Yes", Kind: acp.PermissionOptionKindAllowOnce}, {OptionId: "no", Name: "No", Kind: acp.PermissionOptionKindRejectOnce}}
		} else {
			for i, name := range list(event["options"]) {
				kind := acp.PermissionOptionKindAllowOnce
				if i == 1 {
					kind = acp.PermissionOptionKindAllowAlways
				}
				if i == 2 {
					kind = acp.PermissionOptionKindRejectOnce
				}
				options = append(options, acp.PermissionOption{OptionId: acp.PermissionOptionId("choice-" + strconv.Itoa(i)), Name: text(name), Kind: kind})
			}
		}
		if len(options) == 0 {
			return
		}
		title := text(event["title"])
		if title == "" {
			title = "Pi " + method
		}
		result, err := host.RequestPermission(ctx, acp.RequestPermissionRequest{SessionId: s.id, ToolCall: acp.ToolCallUpdate{ToolCallId: acp.ToolCallId("pi-ui-" + id), Title: &title, Content: []acp.ToolCallContent{acp.ToolContent(acp.TextBlock(text(event["message"])))}}, Options: options})
		if err != nil || ctx.Err() != nil || result.Outcome.Selected == nil {
			return
		}
		for i, option := range options {
			if option.OptionId == result.Outcome.Selected.OptionId {
				delete(response, "cancelled")
				if method == "confirm" {
					response["confirmed"] = option.OptionId == "yes"
				} else {
					response["value"] = event["options"].([]any)[i]
				}
				return
			}
		}
	case "input", "editor":
		if !formUI {
			return
		}
		property := map[string]any{"type": "string", "title": text(event["title"])}
		if value := text(event["prefill"]); value != "" {
			property["default"] = value
		}
		if value := text(event["initialText"]); value != "" {
			property["default"] = value
		}
		result, err := host.UnstableCreateElicitation(ctx, acp.UnstableCreateElicitationRequest{Form: &acp.UnstableCreateElicitationForm{Mode: "form", Message: text(event["title"]), Meta: map[string]any{"sessionId": s.id}, RequestedSchema: acp.UnstableElicitationSchema{Properties: map[string]any{"value": property}, Required: []string{"value"}}}})
		if err == nil && ctx.Err() == nil && result.Accept != nil {
			if value, ok := result.Accept.Content["value"].(string); ok {
				delete(response, "cancelled")
				response["value"] = value
			}
		}
	case "notify":
		_ = a.emit(ctx, s, map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": text(event["message"])}})
	}
}

// mcpApprovalSelection 识别固定上游工厂的审批选项；普通扩展选择属于问答而非授权。
func mcpApprovalSelection(event map[string]any) bool {
	options := list(event["options"])
	return strings.HasPrefix(text(event["title"]), "MCP: ") && len(options) == 3 && options[0] == "Allow once" && options[1] == "Allow for session" && options[2] == "Deny"
}

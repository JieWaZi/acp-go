package nativeacp

import (
	"context"
	"encoding/json"

	acp "github.com/coder/acp-go-sdk"
)

// cursorTodo 保存 Cursor 计划条目的身份、内容和原始状态。
type cursorTodo struct {
	// ID 是 merge 时匹配已有条目的稳定标识。
	ID string `json:"id"`
	// Content 是用户可见的工作说明。
	Content string `json:"content"`
	// Status 是 Cursor 提供的状态。
	Status string `json:"status"`
}

// cursorNotificationRequest 是三个通知共用的标准文档字段集合。
type cursorNotificationRequest struct {
	// SessionID 是上游可选的精确会话归属。
	SessionID string `json:"sessionId"`
	// ToolCallID 关联标准工具消息。
	ToolCallID string `json:"toolCallId"`
	// Todos 是需要更新的计划条目。
	Todos []cursorTodo `json:"todos"`
	// Merge 指定合并或整体替换计划。
	Merge bool `json:"merge"`
	// Description 是子任务或图片说明。
	Description string `json:"description"`
	// FilePath 是图片通知提供的目标路径，不表示文件一定已生成。
	FilePath string `json:"filePath"`
}

// cursorNotification 将可展示通知投影为 ACP Plan 或工具内容，不伪造工具终态。
func (agent *Agent) cursorNotification(ctx context.Context, method string, params json.RawMessage) error {
	var request cursorNotificationRequest
	if json.Unmarshal(params, &request) != nil {
		return acp.NewInvalidParams(nil)
	}
	sid := agent.interactionSession(request.ToolCallID, request.SessionID)
	if sid == "" {
		return nil
	}
	if method == "cursor/update_todos" {
		agent.mutex.Lock()
		state := agent.sessions[sid]
		if state == nil {
			agent.mutex.Unlock()
			return nil
		}
		if !request.Merge {
			state.todos = nil
		}
		for _, todo := range request.Todos {
			if todo.ID == "" {
				continue
			}
			found := false
			for i := range state.todos {
				if state.todos[i].ID == todo.ID {
					state.todos[i] = todo
					found = true
					break
				}
			}
			if !found {
				state.todos = append(state.todos, todo)
			}
		}
		entries := make([]acp.PlanEntry, 0, len(state.todos))
		for _, todo := range state.todos {
			status := acp.PlanEntryStatus(todo.Status)
			content := todo.Content
			if todo.Status == "cancelled" {
				status = acp.PlanEntryStatusCompleted
				content = "[Cancelled] " + content
			}
			if status != acp.PlanEntryStatusPending && status != acp.PlanEntryStatusInProgress && status != acp.PlanEntryStatusCompleted {
				continue
			}
			entries = append(entries, acp.PlanEntry{Content: content, Priority: acp.PlanEntryPriorityMedium, Status: status})
		}
		agent.mutex.Unlock()
		return agent.host.SessionUpdate(ctx, acp.SessionNotification{SessionId: sid, Update: acp.UpdatePlan(entries...)})
	}
	if request.ToolCallID == "" {
		return nil
	}
	update := acp.ToolCallUpdate{ToolCallId: acp.ToolCallId(request.ToolCallID), Content: []acp.ToolCallContent{acp.ToolContent(acp.TextBlock(request.Description))}}
	if request.FilePath != "" {
		update.Locations = []acp.ToolCallLocation{{Path: request.FilePath}}
	}
	return agent.host.SessionUpdate(ctx, acp.SessionNotification{SessionId: sid, Update: acp.UpdateToolCall(update.ToolCallId, acp.WithUpdateContent(update.Content), acp.WithUpdateLocations(update.Locations))})
}

package cursor

import (
	"context"
	"encoding/json"
	"strings"
	"sync"

	"github.com/JieWaZi/acp-go/pkg/nativeacp"
	acp "github.com/coder/acp-go-sdk"
)

// cursorCallbacks 拥有 Cursor 私有反向请求及其计划合并状态。
type cursorCallbacks struct {
	// mutex 保护多个会话并发更新的计划状态。
	mutex sync.Mutex
	// todos 按会话保存 Cursor 计划条目的权威顺序。
	todos map[acp.SessionId][]cursorTodo
}

// cursorInteractionRequest 保留 Cursor 文档定义的交互字段。
type cursorInteractionRequest struct {
	// SessionID 是部分实现附加的会话标识。
	SessionID string `json:"sessionId"`
	// ToolCallID 将交互关联到此前的工具更新。
	ToolCallID string `json:"toolCallId"`
	// Title 是问答标题。
	Title string `json:"title"`
	// Questions 是按原始顺序展示的问题。
	Questions []cursorCallbackQuestion `json:"questions"`
	// Name 是计划名称。
	Name string `json:"name"`
	// Plan 是需要用户明确批准的完整计划。
	Plan string `json:"plan"`
}

// cursorCallbackQuestion 表达一个私有回调中的单选或多选问题。
type cursorCallbackQuestion struct {
	// ID 是返回答案时的原始问题标识。
	ID string `json:"id"`
	// Prompt 是问题正文。
	Prompt string `json:"prompt"`
	// Options 是上游公开的选项。
	Options []cursorCallbackOption `json:"options"`
	// AllowMultiple 表示是否允许选择多个答案。
	AllowMultiple bool `json:"allowMultiple"`
}

// cursorCallbackOption 是私有回调问题选项的身份和展示标签。
type cursorCallbackOption struct {
	// ID 是回传给 Cursor 的选项标识。
	ID string `json:"id"`
	// Label 是用户可读的选项名称。
	Label string `json:"label"`
}

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

// newCursorCallbacks 创建不共享会话状态的 Cursor 私有协议适配器。
func newCursorCallbacks() *cursorCallbacks {
	return &cursorCallbacks{todos: make(map[acp.SessionId][]cursorTodo)}
}

// NewCallbackAdapter 创建 Cursor 私有反向请求适配器，供标准 stdio 传输组合使用。
func NewCallbackAdapter() nativeacp.CallbackAdapter {
	return newCursorCallbacks()
}

// PrepareInitialize 声明 Cursor 参数化模型目录需要的客户端能力。
func (*cursorCallbacks) PrepareInitialize(request *acp.InitializeRequest) {
	meta := make(map[string]any, len(request.ClientCapabilities.Meta)+1)
	for key, value := range request.ClientCapabilities.Meta {
		meta[key] = value
	}
	meta["parameterizedModelPicker"] = true
	request.ClientCapabilities.Meta = meta
}

// HandleCallback 只识别 Cursor 私有方法，其余方法交回 nativeacp 标准分发。
func (adapter *cursorCallbacks) HandleCallback(
	ctx context.Context,
	bridge nativeacp.CallbackBridge,
	method string,
	params json.RawMessage,
) (any, bool, error) {
	switch method {
	case "cursor/ask_question", "cursor/create_plan":
		result, err := adapter.interaction(ctx, bridge, method, params)
		return result, true, err
	case "cursor/update_todos", "cursor/task", "cursor/generate_image":
		return nil, true, adapter.notification(ctx, bridge, method, params)
	default:
		return nil, false, nil
	}
}

// interaction 把 Cursor 阻塞请求接到现有 ACP 表单与审批接口。
func (*cursorCallbacks) interaction(
	ctx context.Context,
	bridge nativeacp.CallbackBridge,
	method string,
	params json.RawMessage,
) (any, error) {
	var request cursorInteractionRequest
	if json.Unmarshal(params, &request) != nil {
		return nil, acp.NewInvalidParams(nil)
	}
	sessionID := bridge.ResolveInteractionSession(request.ToolCallID, request.SessionID)
	if sessionID == "" {
		return cursorOutcome("cancelled"), nil
	}
	if method == "cursor/create_plan" {
		title := cursorMessage(request.Name, "Review plan")
		response, err := bridge.RequestPermission(ctx, acp.RequestPermissionRequest{
			SessionId: sessionID,
			ToolCall: acp.ToolCallUpdate{
				ToolCallId: acp.ToolCallId(request.ToolCallID),
				Title:      &title,
				Content:    []acp.ToolCallContent{acp.ToolContent(acp.TextBlock(request.Plan))},
			},
			Options: []acp.PermissionOption{
				{OptionId: "accept", Name: "Accept plan", Kind: acp.PermissionOptionKindAllowOnce},
				{OptionId: "reject", Name: "Reject plan", Kind: acp.PermissionOptionKindRejectOnce},
			},
		})
		if err != nil {
			return nil, err
		}
		if response.Outcome.Selected == nil {
			return cursorOutcome("cancelled"), nil
		}
		if response.Outcome.Selected.OptionId == "accept" {
			return cursorOutcome("accepted"), nil
		}
		return cursorOutcome("rejected"), nil
	}
	if len(request.Questions) == 0 {
		return nil, acp.NewInvalidParams(nil)
	}
	properties := make(map[string]any, len(request.Questions))
	required := make([]string, 0, len(request.Questions))
	for _, question := range request.Questions {
		if question.ID == "" || len(question.Options) == 0 {
			return nil, acp.NewInvalidParams(nil)
		}
		if _, exists := properties[question.ID]; exists {
			return nil, acp.NewInvalidParams(nil)
		}
		ids := make([]string, 0, len(question.Options))
		names := make([]string, 0, len(question.Options))
		for _, option := range question.Options {
			ids = append(ids, option.ID)
			names = append(names, option.Label)
		}
		property := map[string]any{
			"type":      "string",
			"title":     question.Prompt,
			"enum":      ids,
			"enumNames": names,
		}
		if question.AllowMultiple {
			property = map[string]any{
				"type":  "array",
				"title": question.Prompt,
				"items": map[string]any{
					"type":      "string",
					"enum":      ids,
					"enumNames": names,
				},
				"minItems":    1,
				"uniqueItems": true,
			}
		}
		properties[question.ID] = property
		required = append(required, question.ID)
	}
	response, err := bridge.CreateElicitation(ctx, acp.UnstableCreateElicitationRequest{
		Form: &acp.UnstableCreateElicitationForm{
			Mode:            "form",
			Message:         cursorMessage(request.Title),
			Meta:            map[string]any{"sessionId": sessionID},
			RequestedSchema: acp.UnstableElicitationSchema{Properties: properties, Required: required},
		},
	})
	if err != nil {
		return nil, err
	}
	if response.Decline != nil {
		return cursorOutcome("skipped"), nil
	}
	if response.Accept == nil {
		return cursorOutcome("cancelled"), nil
	}
	answers := make([]map[string]any, 0, len(request.Questions))
	for _, question := range request.Questions {
		values := cursorAnswerValues(response.Accept.Content[question.ID])
		invalidCount := len(values) == 0 || (!question.AllowMultiple && len(values) != 1)
		if invalidCount || !cursorAnswersValid(question.Options, values) {
			return cursorOutcome("cancelled"), nil
		}
		answers = append(answers, map[string]any{"questionId": question.ID, "selectedOptionIds": values})
	}
	return map[string]any{"outcome": map[string]any{"outcome": "answered", "answers": answers}}, nil
}

// notification 将 Cursor 可展示通知投影为 ACP Plan 或工具内容，不伪造工具终态。
func (adapter *cursorCallbacks) notification(
	ctx context.Context,
	bridge nativeacp.CallbackBridge,
	method string,
	params json.RawMessage,
) error {
	var request cursorNotificationRequest
	if json.Unmarshal(params, &request) != nil {
		return acp.NewInvalidParams(nil)
	}
	sessionID := bridge.ResolveInteractionSession(request.ToolCallID, request.SessionID)
	if sessionID == "" {
		return nil
	}
	if method == "cursor/update_todos" {
		entries := adapter.planEntries(sessionID, request)
		return bridge.UpdateSession(ctx, acp.SessionNotification{
			SessionId: sessionID,
			Update:    acp.UpdatePlan(entries...),
		})
	}
	if request.ToolCallID == "" {
		return nil
	}
	update := acp.ToolCallUpdate{
		ToolCallId: acp.ToolCallId(request.ToolCallID),
		Content:    []acp.ToolCallContent{acp.ToolContent(acp.TextBlock(request.Description))},
	}
	if request.FilePath != "" {
		update.Locations = []acp.ToolCallLocation{{Path: request.FilePath}}
	}
	return bridge.UpdateSession(ctx, acp.SessionNotification{
		SessionId: sessionID,
		Update: acp.UpdateToolCall(
			update.ToolCallId,
			acp.WithUpdateContent(update.Content),
			acp.WithUpdateLocations(update.Locations),
		),
	})
}

// planEntries 合并 Cursor todo 并生成标准计划快照。
func (adapter *cursorCallbacks) planEntries(
	sessionID acp.SessionId,
	request cursorNotificationRequest,
) []acp.PlanEntry {
	adapter.mutex.Lock()
	defer adapter.mutex.Unlock()
	if !request.Merge {
		adapter.todos[sessionID] = nil
	}
	for _, todo := range request.Todos {
		if todo.ID == "" {
			continue
		}
		found := false
		for index := range adapter.todos[sessionID] {
			if adapter.todos[sessionID][index].ID == todo.ID {
				adapter.todos[sessionID][index] = todo
				found = true
				break
			}
		}
		if !found {
			adapter.todos[sessionID] = append(adapter.todos[sessionID], todo)
		}
	}
	entries := make([]acp.PlanEntry, 0, len(adapter.todos[sessionID]))
	for _, todo := range adapter.todos[sessionID] {
		status := acp.PlanEntryStatus(todo.Status)
		content := todo.Content
		if todo.Status == "cancelled" {
			status = acp.PlanEntryStatusCompleted
			content = "[Cancelled] " + content
		}
		validStatus := status == acp.PlanEntryStatusPending ||
			status == acp.PlanEntryStatusInProgress ||
			status == acp.PlanEntryStatusCompleted
		if validStatus {
			entries = append(entries, acp.PlanEntry{
				Content:  content,
				Priority: acp.PlanEntryPriorityMedium,
				Status:   status,
			})
		}
	}
	return entries
}

// cursorOutcome 构造 Cursor 文档规定的嵌套交互结果。
func cursorOutcome(value string) map[string]any {
	return map[string]any{"outcome": map[string]any{"outcome": value}}
}

// cursorMessage 返回首个非空文本，避免空标题破坏宿主展示。
func cursorMessage(parts ...string) string {
	for _, part := range parts {
		if value := strings.TrimSpace(part); value != "" {
			return value
		}
	}
	return "Interaction"
}

// cursorAnswerValues 把单选和多选响应统一成有序字符串列表。
func cursorAnswerValues(value any) []string {
	values := []string{}
	switch typed := value.(type) {
	case string:
		values = append(values, typed)
	case []any:
		for _, item := range typed {
			if id, ok := item.(string); ok {
				values = append(values, id)
			}
		}
	case []string:
		values = append(values, typed...)
	}
	return values
}

// cursorAnswersValid 验证所有回传标识都来自当前问题的官方选项。
func cursorAnswersValid(options []cursorCallbackOption, values []string) bool {
	for _, value := range values {
		valid := false
		for _, option := range options {
			if option.ID == value {
				valid = true
				break
			}
		}
		if !valid {
			return false
		}
	}
	return true
}

package nativeacp

import (
	"context"
	"encoding/json"
	"strings"

	acp "github.com/coder/acp-go-sdk"
)

// sessionOptions 将宿主统一配置标识映射到上游声明的实际标识。
type sessionOptions struct {
	// ids 按 model/reasoning 等规范化标识保存原生配置标识。
	ids map[acp.SessionConfigId]acp.SessionConfigId
	// options 是最后一次会话返回的配置目录。
	options []acp.SessionConfigOption
	// legacyModel 表示上游仍使用 models 与 session/set_model。
	legacyModel bool
	// legacyPermissions 标识只声明 default、需要适配器补充自动审查的上游。
	legacyPermissions bool
	// cwd 是会话实际工作目录，供风险审查使用。
	cwd string
	// todos 保存 Cursor merge 通知需要的有序计划状态。
	todos []cursorTodo
}

// nativeSessionResponse 兼容当前仍提供 models 的外部 ACP 实现。
type nativeSessionResponse struct {
	// NewSessionResponse 包含 SDK 定义的公共会话字段。
	acp.NewSessionResponse
	// Models 保存尚未迁移到 configOptions 的原生模型目录。
	Models *nativeModels `json:"models,omitempty"`
}

// nativeModels 是外部 ACP 的旧模型选择协议，不涉及 Ally 持久化兼容。
type nativeModels struct {
	// CurrentModelID 是上游当前选择的模型标识。
	CurrentModelID string `json:"currentModelId"`
	// AvailableModels 是上游公开的模型列表。
	AvailableModels []nativeModel `json:"availableModels"`
}

// nativeModel 保存外部 Agent 提供的原始模型身份。
type nativeModel struct {
	// ModelID 是切换模型时必须原样回传的标识。
	ModelID string `json:"modelId"`
	// Name 是模型显示名。
	Name string `json:"name"`
}

// NewSession 创建原生会话，并统一模型与思考配置的入口。
func (agent *Agent) NewSession(ctx context.Context, request acp.NewSessionRequest) (acp.NewSessionResponse, error) {
	response, err := acp.SendRequest[nativeSessionResponse](agent.conn, ctx, "session/new", request)
	if err != nil {
		return acp.NewSessionResponse{}, err
	}
	agent.normalizeSession(&response, response.SessionId, request.Cwd)
	return response.NewSessionResponse, nil
}

// LoadSession 恢复上游历史，保留其真实模型与模式目录。
func (agent *Agent) LoadSession(ctx context.Context, request acp.LoadSessionRequest) (acp.LoadSessionResponse, error) {
	response, err := acp.SendRequest[nativeSessionResponse](agent.conn, ctx, "session/load", request)
	if err != nil {
		return acp.LoadSessionResponse{}, err
	}
	agent.normalizeSession(&response, request.SessionId, request.Cwd)
	return acp.LoadSessionResponse{Meta: response.Meta, ConfigOptions: response.ConfigOptions, Modes: response.Modes}, nil
}

// ResumeSession 仅调用上游明确声明支持的无历史恢复方法。
func (agent *Agent) ResumeSession(ctx context.Context, request acp.ResumeSessionRequest) (acp.ResumeSessionResponse, error) {
	response, err := acp.SendRequest[nativeSessionResponse](agent.conn, ctx, "session/resume", request)
	if err != nil {
		return acp.ResumeSessionResponse{}, err
	}
	agent.normalizeSession(&response, request.SessionId, request.Cwd)
	return acp.ResumeSessionResponse{Meta: response.Meta, ConfigOptions: response.ConfigOptions, Modes: response.Modes}, nil
}

// normalizeSession 为一个会话保存原生配置标识和旧版模型目录。
func (agent *Agent) normalizeSession(response *nativeSessionResponse, id acp.SessionId, cwd string) {
	agent.mutex.Lock()
	defer agent.mutex.Unlock()
	state := &sessionOptions{cwd: cwd, ids: make(map[acp.SessionConfigId]acp.SessionConfigId)}
	response.ConfigOptions = normalizeOptions(response.ConfigOptions, state.ids)
	if response.Models != nil && !hasConfig(response.ConfigOptions, "model") {
		values := make(acp.SessionConfigSelectOptionsUngrouped, 0, len(response.Models.AvailableModels))
		for _, model := range response.Models.AvailableModels {
			values = append(values, acp.SessionConfigSelectOption{Value: acp.SessionConfigValueId(model.ModelID), Name: model.Name})
		}
		category := acp.SessionConfigOptionCategoryModel
		response.ConfigOptions = append(response.ConfigOptions, acp.SessionConfigOption{Select: &acp.SessionConfigOptionSelect{Id: "model", Name: "Model", Category: &category, CurrentValue: acp.SessionConfigValueId(response.Models.CurrentModelID), Options: acp.SessionConfigSelectOptions{Ungrouped: &values}}})
		state.ids["model"] = "model"
		state.legacyModel = true
	}
	state.legacyPermissions = response.Modes != nil && len(response.Modes.AvailableModes) == 1 && response.Modes.AvailableModes[0].Id == "default"
	state.options = response.ConfigOptions
	agent.sessions[id] = state
}

// hasConfig 判断配置目录是否已经提供指定选择项。
func hasConfig(options []acp.SessionConfigOption, id acp.SessionConfigId) bool {
	for _, option := range options {
		if option.Select != nil && option.Select.Id == id {
			return true
		}
	}
	return false
}

// normalizeOptions 仅统一能力类别明确的配置标识，不改变模型或选项值。
func normalizeOptions(options []acp.SessionConfigOption, ids map[acp.SessionConfigId]acp.SessionConfigId) []acp.SessionConfigOption {
	result := make([]acp.SessionConfigOption, 0, len(options))
	for _, option := range options {
		if option.Select != nil {
			copy := *option.Select
			original := copy.Id
			category := ""
			if copy.Category != nil {
				category = string(*copy.Category)
			}
			switch {
			case category == "model" || original == "model":
				copy.Id = "model"
			case category == "thought_level" || original == "thinking" || original == "thought_level" || original == "reasoning_effort":
				copy.Id = "reasoning"
			}
			ids[copy.Id] = original
			option.Select = &copy
		}
		result = append(result, option)
	}
	return result
}

// SetSessionConfigOption 将规范化标识还原为原生标识，模型值始终保持原样。
func (agent *Agent) SetSessionConfigOption(ctx context.Context, request acp.SetSessionConfigOptionRequest) (acp.SetSessionConfigOptionResponse, error) {
	if request.ValueId == nil {
		return acp.SendRequest[acp.SetSessionConfigOptionResponse](agent.conn, ctx, "session/set_config_option", request)
	}
	value := *request.ValueId
	agent.mutex.Lock()
	state := agent.sessions[value.SessionId]
	if state == nil {
		agent.mutex.Unlock()
		return acp.SetSessionConfigOptionResponse{}, acp.NewInvalidParams(nil)
	}
	nativeID, ok := state.ids[value.ConfigId]
	legacy := state.legacyModel && value.ConfigId == "model"
	agent.mutex.Unlock()
	if !ok {
		return acp.SetSessionConfigOptionResponse{}, acp.NewInvalidParams(map[string]any{"message": "configuration not advertised by agent"})
	}
	if legacy {
		_, err := acp.SendRequest[json.RawMessage](agent.conn, ctx, "session/set_model", map[string]any{"sessionId": value.SessionId, "modelId": value.Value})
		if err != nil {
			return acp.SetSessionConfigOptionResponse{}, err
		}
		agent.mutex.Lock()
		defer agent.mutex.Unlock()
		for index, option := range state.options {
			if option.Select != nil && option.Select.Id == "model" {
				copy := *option.Select
				copy.CurrentValue = value.Value
				state.options[index] = acp.SessionConfigOption{Select: &copy}
			}
		}
		return acp.SetSessionConfigOptionResponse{ConfigOptions: append([]acp.SessionConfigOption(nil), state.options...)}, nil
	}
	value.ConfigId = nativeID
	request.ValueId = &value
	response, err := acp.SendRequest[acp.SetSessionConfigOptionResponse](agent.conn, ctx, "session/set_config_option", request)
	if err != nil {
		return response, err
	}
	agent.mutex.Lock()
	defer agent.mutex.Unlock()
	response.ConfigOptions = normalizeOptions(response.ConfigOptions, state.ids)
	state.options = response.ConfigOptions
	return response, nil
}

// normalizeUpdate 关联工具所属会话并同步动态配置目录。
func (agent *Agent) normalizeUpdate(request *acp.SessionNotification) {
	agent.mutex.Lock()
	defer agent.mutex.Unlock()
	if update := request.Update.ToolCall; update != nil {
		agent.toolSessions[update.ToolCallId] = request.SessionId
		agent.toolDetails[update.ToolCallId] = acp.ToolCallUpdate{ToolCallId: update.ToolCallId, Title: acp.Ptr(update.Title), Kind: acp.Ptr(update.Kind), RawInput: update.RawInput, Content: update.Content}
	}
	if update := request.Update.ToolCallUpdate; update != nil {
		agent.toolSessions[update.ToolCallId] = request.SessionId
		previous := agent.toolDetails[update.ToolCallId]
		if update.RawInput != nil {
			previous.RawInput = update.RawInput
		}
		if update.Title != nil {
			previous.Title = update.Title
		}
		if update.Kind != nil {
			previous.Kind = update.Kind
		}
		if update.Content != nil {
			previous.Content = update.Content
		}
		previous.ToolCallId = update.ToolCallId
		agent.toolDetails[update.ToolCallId] = previous
	}
	if update := request.Update.ConfigOptionUpdate; update != nil {
		if state := agent.sessions[request.SessionId]; state != nil {
			update.ConfigOptions = normalizeOptions(update.ConfigOptions, state.ids)
			state.options = update.ConfigOptions
		}
	}
}

// interactionSession 只在会话归属唯一时路由缺少会话标识的请求。
func (agent *Agent) interactionSession(toolID string, explicit string) acp.SessionId {
	agent.mutex.Lock()
	defer agent.mutex.Unlock()
	if explicit != "" {
		if agent.active[acp.SessionId(explicit)] {
			if owner := agent.toolSessions[acp.ToolCallId(toolID)]; owner != "" && owner != acp.SessionId(explicit) {
				return ""
			}
			return acp.SessionId(explicit)
		}
		return ""
	}
	if sid := agent.toolSessions[acp.ToolCallId(toolID)]; sid != "" {
		if agent.active[sid] {
			return sid
		}
		return ""
	}
	if len(agent.active) == 1 {
		for sid := range agent.active {
			return sid
		}
	}
	return ""
}

// cleanMessage 选择第一个非空文本作为交互说明。
func cleanMessage(parts ...string) string {
	for _, part := range parts {
		if strings.TrimSpace(part) != "" {
			return part
		}
	}
	return "Input requested"
}

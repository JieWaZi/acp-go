package nativeacp

import (
	"context"
	"encoding/json"
	"strings"
	"sync"

	acp "github.com/coder/acp-go-sdk"
)

// sessionOptions 将宿主统一配置标识映射到上游声明的实际标识。
type sessionOptions struct {
	// configMutex 串行化一次配置操作中的多项原生参数更新。
	configMutex sync.Mutex
	// ids 按 model/reasoning 等规范化标识保存原生配置标识。
	ids map[acp.SessionConfigId]acp.SessionConfigId
	// options 是最后一次会话返回的配置目录。
	options []acp.SessionConfigOption
}

// nativeSessionResponse 兼容当前仍提供 models 的外部 ACP 实现。
type nativeSessionResponse struct {
	// NewSessionResponse 包含 SDK 定义的公共会话字段。
	acp.NewSessionResponse
	// Models 保存尚未迁移到 configOptions 的厂商模型目录原文。
	Models json.RawMessage `json:"models,omitempty"`
}

// NewSession 创建原生会话，并统一模型与思考配置的入口。
func (agent *Agent) NewSession(ctx context.Context, request acp.NewSessionRequest) (acp.NewSessionResponse, error) {
	if request.McpServers == nil {
		request.McpServers = []acp.McpServer{}
	}
	response, err := sendNativeRequest[nativeSessionResponse](agent, ctx, "session/new", request)
	if err != nil {
		return acp.NewSessionResponse{}, err
	}
	if err = agent.normalizeSession(ctx, &response, response.SessionId, request.Cwd); err != nil {
		return acp.NewSessionResponse{}, err
	}
	return response.NewSessionResponse, nil
}

// LoadSession 恢复上游历史，保留其真实模型与模式目录。
func (agent *Agent) LoadSession(ctx context.Context, request acp.LoadSessionRequest) (acp.LoadSessionResponse, error) {
	if request.McpServers == nil {
		request.McpServers = []acp.McpServer{}
	}
	response, err := sendNativeRequest[nativeSessionResponse](agent, ctx, "session/load", request)
	if err != nil {
		return acp.LoadSessionResponse{}, err
	}
	if err = agent.normalizeSession(ctx, &response, request.SessionId, request.Cwd); err != nil {
		return acp.LoadSessionResponse{}, err
	}
	return acp.LoadSessionResponse{Meta: response.Meta, ConfigOptions: response.ConfigOptions, Modes: response.Modes}, nil
}

// ResumeSession 仅调用上游明确声明支持的无历史恢复方法。
func (agent *Agent) ResumeSession(
	ctx context.Context,
	request acp.ResumeSessionRequest,
) (acp.ResumeSessionResponse, error) {
	response, err := sendNativeRequest[nativeSessionResponse](agent, ctx, "session/resume", request)
	if err != nil {
		return acp.ResumeSessionResponse{}, err
	}
	if err = agent.normalizeSession(ctx, &response, request.SessionId, request.Cwd); err != nil {
		return acp.ResumeSessionResponse{}, err
	}
	return acp.ResumeSessionResponse{Meta: response.Meta, ConfigOptions: response.ConfigOptions, Modes: response.Modes}, nil
}

// normalizeSession 为一个会话保存原生配置标识和旧版模型目录。
func (agent *Agent) normalizeSession(
	ctx context.Context,
	response *nativeSessionResponse,
	id acp.SessionId,
	cwd string,
) error {
	state := &sessionOptions{ids: make(map[acp.SessionConfigId]acp.SessionConfigId)}
	if agent.config.SessionAdapter != nil {
		snapshot := SessionSnapshot{
			ConfigOptions:    response.ConfigOptions,
			Modes:            response.Modes,
			Models:           response.Models,
			WorkingDirectory: cwd,
		}
		options, err := agent.config.SessionAdapter.NormalizeSession(ctx, agent, id, snapshot)
		if err != nil {
			return err
		}
		response.ConfigOptions = options
	} else {
		response.ConfigOptions = NormalizeOptions(response.ConfigOptions, state.ids)
	}
	agent.mutex.Lock()
	defer agent.mutex.Unlock()
	state.options = response.ConfigOptions
	agent.sessions[id] = state
	return nil
}

// NormalizeOptions 仅统一能力类别明确的配置标识，不改变模型或选项值。
func NormalizeOptions(
	options []acp.SessionConfigOption,
	ids map[acp.SessionConfigId]acp.SessionConfigId,
) []acp.SessionConfigOption {
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
			case category == "thought_level" ||
				original == "thinking" ||
				original == "thought_level" ||
				original == "reasoning_effort":
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
func (agent *Agent) SetSessionConfigOption(
	ctx context.Context,
	request acp.SetSessionConfigOptionRequest,
) (acp.SetSessionConfigOptionResponse, error) {
	if request.ValueId == nil {
		return sendNativeRequest[acp.SetSessionConfigOptionResponse](agent, ctx, "session/set_config_option", request)
	}
	value := *request.ValueId
	agent.mutex.Lock()
	state := agent.sessions[value.SessionId]
	if state == nil {
		agent.mutex.Unlock()
		return acp.SetSessionConfigOptionResponse{}, acp.NewInvalidParams(nil)
	}
	agent.mutex.Unlock()
	state.configMutex.Lock()
	defer state.configMutex.Unlock()
	if agent.config.SessionAdapter != nil {
		response, handled, err := agent.config.SessionAdapter.SetConfigOption(ctx, agent, request)
		if handled {
			if err == nil {
				agent.mutex.Lock()
				state.options = append([]acp.SessionConfigOption(nil), response.ConfigOptions...)
				agent.mutex.Unlock()
			}
			return response, err
		}
	}
	agent.mutex.Lock()
	nativeID, ok := state.ids[value.ConfigId]
	agent.mutex.Unlock()
	if !ok {
		return acp.SetSessionConfigOptionResponse{}, acp.NewInvalidParams(map[string]any{
			"message": "configuration not advertised by agent",
		})
	}
	value.ConfigId = nativeID
	request.ValueId = &value
	response, err := sendNativeRequest[acp.SetSessionConfigOptionResponse](agent, ctx, "session/set_config_option", request)
	if err != nil {
		return response, err
	}
	agent.mutex.Lock()
	defer agent.mutex.Unlock()
	response.ConfigOptions = NormalizeOptions(response.ConfigOptions, state.ids)
	state.options = response.ConfigOptions
	return response, nil
}

// normalizeUpdate 关联工具所属会话并同步动态配置目录。
func (agent *Agent) normalizeUpdate(request *acp.SessionNotification) {
	agent.mutex.Lock()
	defer agent.mutex.Unlock()
	if update := request.Update.ToolCall; update != nil {
		agent.toolSessions[update.ToolCallId] = request.SessionId
		agent.toolDetails[update.ToolCallId] = acp.ToolCallUpdate{
			ToolCallId: update.ToolCallId,
			Title:      acp.Ptr(update.Title),
			Kind:       acp.Ptr(update.Kind),
			RawInput:   update.RawInput,
			Content:    update.Content,
		}
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
			if agent.config.SessionAdapter != nil {
				update.ConfigOptions = agent.config.SessionAdapter.NormalizeUpdate(request.SessionId, update.ConfigOptions)
			} else {
				update.ConfigOptions = NormalizeOptions(update.ConfigOptions, state.ids)
			}
			state.options = update.ConfigOptions
		}
	}
	if request.Update.ToolCall != nil || request.Update.ToolCallUpdate != nil {
		if agent.toolChanged != nil {
			close(agent.toolChanged)
		}
		agent.toolChanged = make(chan struct{})
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

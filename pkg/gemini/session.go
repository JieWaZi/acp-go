package gemini

import (
	"context"
	"encoding/json"
	"strings"
	"sync"

	"github.com/JieWaZi/acp-go/pkg/nativeacp"
	acp "github.com/coder/acp-go-sdk"
)

// legacyModels 保存官方 ACP 尚未迁移的 models 目录。
type legacyModels struct {
	// CurrentModelID 是配置模型或本包生成的 alias。
	CurrentModelID string `json:"currentModelId"`
	// AvailableModels 是原生发现的实际模型。
	AvailableModels []struct {
		// ModelID 是原生精确模型标识。
		ModelID string `json:"modelId"`
		// Name 是原生展示名称。
		Name string `json:"name"`
		// Description 是原生模型说明。
		Description *string `json:"description,omitempty"`
	} `json:"availableModels"`
}

// geminiSession 保存当前模型与受支持的思考配置。
type geminiSession struct {
	// models 是原生 catalog 和当前模型快照。
	models legacyModels
	// reasoning 保存当前显式档位；default 保留用户基础配置。
	reasoning string
	// cwd 是审批审查所需的原生会话目录。
	cwd string
}

// sessionAdapter 将标准配置调用映射到真实 legacy set_model。
type sessionAdapter struct {
	// mutex 保护多个会话的配置快照。
	mutex sync.Mutex
	// sessions 只保存原生成功创建或恢复的会话。
	sessions map[acp.SessionId]*geminiSession
}

// newSessionAdapter 创建原生配置的兼容映射。
func newSessionAdapter() *sessionAdapter {
	return &sessionAdapter{sessions: map[acp.SessionId]*geminiSession{}}
}

// NormalizeSession 保留原生 catalog，反向解码已恢复的本包别名。
func (a *sessionAdapter) NormalizeSession(
	_ context.Context,
	_ nativeacp.SessionBridge,
	id acp.SessionId,
	snapshot nativeacp.SessionSnapshot,
) ([]acp.SessionConfigOption, error) {
	var models legacyModels
	if len(snapshot.Models) > 0 {
		if err := json.Unmarshal(snapshot.Models, &models); err != nil {
			return nil, err
		}
	}
	model, reasoning, _ := decodeThinkingAlias(models.CurrentModelID)
	models.CurrentModelID = model
	state := &geminiSession{models: models, reasoning: reasoning, cwd: snapshot.WorkingDirectory}
	a.mutex.Lock()
	defer a.mutex.Unlock()
	a.sessions[id] = state
	return state.options(), nil
}

// options 当前仅显示原生发现的模型；思考设置等待真实 continuation 实现。
func (s *geminiSession) options() []acp.SessionConfigOption {
	values := acp.SessionConfigSelectOptionsUngrouped{}

	for _, model := range s.models.AvailableModels {
		canonical, _, generated := decodeThinkingAlias(model.ModelID)
		if generated {
			continue
		}
		values = append(values, acp.SessionConfigSelectOption{
			Value: acp.SessionConfigValueId(canonical), Name: model.Name, Description: model.Description,
		})

	}
	options := []acp.SessionConfigOption{}
	if s.models.CurrentModelID == "" && len(values) == 0 {
		return options
	}
	options = append(options, selectOption(
		"model",
		"Model",
		s.models.CurrentModelID,
		values,
	))

	// 当前固定 CLI 在工具 continuation 后丢失 alias 配置；在完整实现前不公开思考菜单。
	return options
}

// selectOption 构造 ACP 标准下拉目录，模型值保持原生 canonical ID。
func selectOption(id, name, current string, values acp.SessionConfigSelectOptionsUngrouped) acp.SessionConfigOption {
	category := acp.SessionConfigOptionCategoryModel
	if id == "reasoning" {
		category = acp.SessionConfigOptionCategoryThoughtLevel
	}
	return acp.SessionConfigOption{Select: &acp.SessionConfigOptionSelect{
		Id: acp.SessionConfigId(id), Name: name, Type: "select", Category: &category,
		CurrentValue: acp.SessionConfigValueId(current), Options: acp.SessionConfigSelectOptions{Ungrouped: &values},
	}}
}

// NormalizeUpdate 保留真正标准配置更新，不推测未知 native 通知。
func (a *sessionAdapter) NormalizeUpdate(_ acp.SessionId, options []acp.SessionConfigOption) []acp.SessionConfigOption {
	return nativeacp.NormalizeOptions(options, map[acp.SessionConfigId]acp.SessionConfigId{})
}

// SetConfigOption 通过原生 set_model 切换 canonical model；当前不接受未公开的思考设置。
func (a *sessionAdapter) SetConfigOption(
	ctx context.Context,
	bridge nativeacp.SessionBridge,
	request acp.SetSessionConfigOptionRequest,
) (acp.SetSessionConfigOptionResponse, bool, error) {
	if request.ValueId == nil {
		return acp.SetSessionConfigOptionResponse{}, true, acp.NewInvalidParams(nil)
	}
	value := request.ValueId
	a.mutex.Lock()
	state := a.sessions[value.SessionId]
	if state == nil {
		a.mutex.Unlock()
		return acp.SetSessionConfigOptionResponse{}, true, acp.NewInvalidParams(nil)
	}
	next := *state
	options := state.options()
	valid := false
	for _, option := range options {
		if option.Select.Id == value.ConfigId {
			for _, candidate := range *option.Select.Options.Ungrouped {
				if candidate.Value == value.Value {
					valid = true
				}
			}
		}
	}
	a.mutex.Unlock()
	if value.ConfigId == "model" && string(value.Value) != "" {
		_, _, reserved := decodeThinkingAlias(string(value.Value))
		valid = !reserved && !strings.HasPrefix(string(value.Value), "acp-go-thinking/")
	}
	if !valid {
		return acp.SetSessionConfigOptionResponse{}, true, acp.NewInvalidParams(nil)
	}
	model := next.models.CurrentModelID
	switch value.ConfigId {
	case "model":
		model = string(value.Value)
		next.models.CurrentModelID = model
		next.reasoning = "default"
	case "reasoning":
		next.reasoning = string(value.Value)
		if next.reasoning != "default" {
			model = thinkingAlias(model, next.reasoning)
		}
	default:
		return acp.SetSessionConfigOptionResponse{}, true, acp.NewInvalidParams(nil)
	}
	if err := bridge.SendRequest(
		ctx,
		"session/set_model",
		map[string]any{"sessionId": value.SessionId, "modelId": model},
		nil,
	); err != nil {
		return acp.SetSessionConfigOptionResponse{}, true, err
	}
	a.mutex.Lock()
	defer a.mutex.Unlock()
	if a.sessions[value.SessionId] != state {
		return acp.SetSessionConfigOptionResponse{}, true, acp.NewInvalidParams(nil)
	}
	a.sessions[value.SessionId] = &next
	return acp.SetSessionConfigOptionResponse{ConfigOptions: next.options()}, true, nil
}

// ForgetSession 移除关闭会话的本地映射。
func (a *sessionAdapter) ForgetSession(id acp.SessionId) {
	a.mutex.Lock()
	defer a.mutex.Unlock()
	delete(a.sessions, id)
}

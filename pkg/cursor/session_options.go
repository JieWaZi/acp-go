package cursor

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/JieWaZi/acp-go/pkg/acpmeta"
	"github.com/JieWaZi/acp-go/pkg/nativeacp"
	acp "github.com/coder/acp-go-sdk"
)

// CursorModelSelection 保存官方配置文件可直接接受的模型选择。
type CursorModelSelection struct {
	// ModelID 是官方模型目录中的参数式模型标识。
	ModelID string `json:"modelId"`
	// Parameters 是已经得到原生回执的实际参数。
	Parameters []CursorModelParameter `json:"parameters"`
}

// CursorModelParameter 保留厂商参数身份与原始值。
type CursorModelParameter struct {
	// ID 是官方参数名称。
	ID string `json:"id"`
	// Value 是官方目录中的参数值。
	Value string `json:"value"`
}

// cursorModelCatalog 保留官方 cursor/list_available_models 返回的模型专属参数。
type cursorModelCatalog struct {
	// Models 是官方返回的可选模型集合，不包含由名称推断的模型。
	Models []cursorModelConfiguration `json:"models"`
}

// cursorModelConfiguration 是 Cursor 公开目录中的单模型配置。
type cursorModelConfiguration struct {
	// Value 是参数式模型的原生标识。
	Value string `json:"value"`
	// ConfigOptions 是该模型的开关、思考强度等有效选项。
	ConfigOptions []acp.SessionConfigOption `json:"configOptions"`
}

// cursorSessionOptions 保存单个 Cursor 会话的原生和统一配置视图。
type cursorSessionOptions struct {
	// nativeOptions 保存原生思考开关与强度，避免组合后丢失参数。
	nativeOptions []acp.SessionConfigOption
	// ids 将统一配置标识映射回当前原生标识。
	ids map[acp.SessionConfigId]acp.SessionConfigId
	// options 是最后一次返回给宿主的统一配置目录。
	options []acp.SessionConfigOption
}

// cursorSessionAdapter 拥有 Cursor 参数化模型目录和会话配置状态。
type cursorSessionAdapter struct {
	// mutex 保护模型目录和各会话配置状态。
	mutex sync.Mutex
	// models 缓存官方模型参数目录；nil 表示尚未成功读取。
	models map[string][]acp.SessionConfigOption
	// sessions 按 ACP 会话标识保存 Cursor 配置状态。
	sessions map[acp.SessionId]*cursorSessionOptions
}

// newCursorSessionAdapter 创建不共享会话状态的 Cursor 配置适配器。
func newCursorSessionAdapter() *cursorSessionAdapter {
	return &cursorSessionAdapter{sessions: make(map[acp.SessionId]*cursorSessionOptions)}
}

// NewSessionAdapter 创建 Cursor 参数化模型适配器，供标准 stdio 传输组合使用。
func NewSessionAdapter() nativeacp.SessionAdapter {
	return newCursorSessionAdapter()
}

// NormalizeSession 读取一次官方参数目录并保存当前会话的原生配置。
func (adapter *cursorSessionAdapter) NormalizeSession(
	ctx context.Context,
	bridge nativeacp.SessionBridge,
	sessionID acp.SessionId,
	snapshot nativeacp.SessionSnapshot,
) ([]acp.SessionConfigOption, error) {
	adapter.loadModels(ctx, bridge)
	adapter.mutex.Lock()
	defer adapter.mutex.Unlock()
	state := &cursorSessionOptions{}
	result := adapter.normalizeOptions(snapshot.ConfigOptions, state)
	state.options = result
	adapter.sessions[sessionID] = state
	return result, nil
}

// NormalizeUpdate 同步动态配置目录；未知会话保持标准配置规范化。
func (adapter *cursorSessionAdapter) NormalizeUpdate(
	sessionID acp.SessionId,
	options []acp.SessionConfigOption,
) []acp.SessionConfigOption {
	adapter.mutex.Lock()
	defer adapter.mutex.Unlock()
	state := adapter.sessions[sessionID]
	if state == nil {
		state = &cursorSessionOptions{}
		adapter.sessions[sessionID] = state
	}
	result := adapter.normalizeOptions(options, state)
	state.options = result
	return result
}

// SetConfigOption 应用 Cursor 原生配置，并把组合思考选择拆回官方参数。
func (adapter *cursorSessionAdapter) SetConfigOption(
	ctx context.Context,
	bridge nativeacp.SessionBridge,
	request acp.SetSessionConfigOptionRequest,
) (acp.SetSessionConfigOptionResponse, bool, error) {
	if request.ValueId == nil {
		return acp.SetSessionConfigOptionResponse{}, false, nil
	}
	value := *request.ValueId
	adapter.mutex.Lock()
	state := adapter.sessions[value.SessionId]
	if state == nil {
		adapter.mutex.Unlock()
		return acp.SetSessionConfigOptionResponse{}, true, acp.NewInvalidParams(nil)
	}
	for _, option := range state.options {
		if option.Select != nil && option.Select.Id == value.ConfigId && option.Select.CurrentValue == value.Value {
			response := acp.SetSessionConfigOptionResponse{
				ConfigOptions: append([]acp.SessionConfigOption(nil), state.options...),
			}
			adapter.mutex.Unlock()
			return response, true, nil
		}
	}
	composite := value.ConfigId == "reasoning" && len(thoughtSelects(state.nativeOptions)) > 1
	nativeID, ok := state.ids[value.ConfigId]
	adapter.mutex.Unlock()
	if composite {
		response, err := adapter.setThinking(ctx, bridge, state, value)
		return response, true, err
	}
	if !ok {
		return acp.SetSessionConfigOptionResponse{}, true, acp.NewInvalidParams(map[string]any{
			"message": "configuration not advertised by Cursor",
		})
	}
	value.ConfigId = nativeID
	request.ValueId = &value
	var response acp.SetSessionConfigOptionResponse
	if err := bridge.SendRequest(ctx, "session/set_config_option", request, &response); err != nil {
		return response, true, err
	}
	adapter.mutex.Lock()
	response.ConfigOptions = adapter.normalizeOptions(response.ConfigOptions, state)
	state.options = response.ConfigOptions
	adapter.mutex.Unlock()
	return response, true, nil
}

// ForgetSession 删除关闭会话的参数状态，模型目录仍可供同一进程复用。
func (adapter *cursorSessionAdapter) ForgetSession(sessionID acp.SessionId) {
	adapter.mutex.Lock()
	delete(adapter.sessions, sessionID)
	adapter.mutex.Unlock()
}

// ExecutionModel 返回已获原生回执的完整模型参数，供交互执行使用。
func (adapter *cursorSessionAdapter) ExecutionModel(sessionID acp.SessionId) (CursorModelSelection, error) {
	adapter.mutex.Lock()
	defer adapter.mutex.Unlock()
	state := adapter.sessions[sessionID]
	if state == nil {
		return CursorModelSelection{}, errors.New("Cursor session configuration unavailable")
	}
	model := ""
	parameters := []CursorModelParameter{}
	for _, option := range state.nativeOptions {
		if option.Select == nil {
			continue
		}
		selected := option.Select
		if selected.Id == "model" || selected.Category != nil && *selected.Category == acp.SessionConfigOptionCategoryModel {
			model = string(selected.CurrentValue)
			continue
		}
		unsupportedCategory := selected.Category != nil &&
			*selected.Category != acp.SessionConfigOptionCategoryThoughtLevel &&
			string(*selected.Category) != "model_config"
		if selected.CurrentValue == "" || selected.Category == nil || unsupportedCategory {
			continue
		}
		parameters = append(parameters, CursorModelParameter{ID: string(selected.Id), Value: string(selected.CurrentValue)})
	}
	if model == "" {
		return CursorModelSelection{}, errors.New("Cursor did not confirm a current model")
	}
	sort.Slice(parameters, func(i, j int) bool { return parameters[i].ID < parameters[j].ID })
	return CursorModelSelection{ModelID: model, Parameters: parameters}, nil
}

// loadModels 一次读取官方参数目录，不切换默认模型，也不执行提示。
func (adapter *cursorSessionAdapter) loadModels(ctx context.Context, bridge nativeacp.SessionBridge) {
	adapter.mutex.Lock()
	loaded := adapter.models != nil
	adapter.mutex.Unlock()
	if loaded {
		return
	}
	loadCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var catalog cursorModelCatalog
	if err := bridge.SendRequest(loadCtx, "cursor/list_available_models", map[string]any{}, &catalog); err != nil {
		return
	}
	models := make(map[string][]acp.SessionConfigOption, len(catalog.Models))
	for _, model := range catalog.Models {
		if model.Value != "" {
			models[model.Value] = model.ConfigOptions
		}
	}
	adapter.mutex.Lock()
	adapter.models = models
	adapter.mutex.Unlock()
}

// normalizeOptions 组合 Cursor 思考参数并附加各模型自己的配置目录。
func (adapter *cursorSessionAdapter) normalizeOptions(
	options []acp.SessionConfigOption,
	state *cursorSessionOptions,
) []acp.SessionConfigOption {
	state.nativeOptions = append([]acp.SessionConfigOption(nil), options...)
	state.ids = make(map[acp.SessionConfigId]acp.SessionConfigId)
	result := nativeacp.NormalizeOptions(cursorThoughtOptions(options), state.ids)
	for _, option := range result {
		if option.Select == nil || option.Select.Id != "model" {
			continue
		}
		mapOptions := func(values []acp.SessionConfigSelectOption) {
			for index, value := range values {
				if params, ok := adapter.models[string(value.Value)]; ok {
					modelOptions := nativeacp.NormalizeOptions(
						cursorThoughtOptions(params),
						map[acp.SessionConfigId]acp.SessionConfigId{},
					)
					values[index].Meta = acpmeta.WithModelConfigOptions(value.Meta, modelOptions)
				}
			}
		}
		if option.Select.Options.Ungrouped != nil {
			values := append(acp.SessionConfigSelectOptionsUngrouped{}, (*option.Select.Options.Ungrouped)...)
			mapOptions(values)
			option.Select.Options.Ungrouped = &values
		}
		if option.Select.Options.Grouped != nil {
			groups := append(acp.SessionConfigSelectOptionsGrouped{}, (*option.Select.Options.Grouped)...)
			for index := range groups {
				groups[index].Options = append([]acp.SessionConfigSelectOption{}, groups[index].Options...)
				mapOptions(groups[index].Options)
			}
			option.Select.Options.Grouped = &groups
		}
	}
	return result
}

// setThinking 在统一单选入口内逐项应用官方参数，并校验每个原生回执。
func (adapter *cursorSessionAdapter) setThinking(
	ctx context.Context,
	bridge nativeacp.SessionBridge,
	state *cursorSessionOptions,
	value acp.SetSessionConfigOptionValueId,
) (acp.SetSessionConfigOptionResponse, error) {
	adapter.mutex.Lock()
	thoughts := thoughtSelects(state.nativeOptions)
	adapter.mutex.Unlock()
	choices, _ := cursorThoughtChoices(thoughts)
	valid := false
	for _, choice := range choices {
		if choice.Value == value.Value {
			valid = true
			break
		}
	}
	if !valid {
		return acp.SetSessionConfigOptionResponse{}, acp.NewInvalidParams(nil)
	}
	var params map[string]string
	if json.Unmarshal([]byte(value.Value), &params) != nil {
		return acp.SetSessionConfigOptionResponse{}, acp.NewInvalidParams(nil)
	}
	ordered := orderedThoughts(thoughts)
	var response acp.SetSessionConfigOptionResponse
	for _, option := range ordered {
		selected, ok := params[string(option.Id)]
		if !ok {
			continue
		}
		nativeValue := value
		nativeValue.ConfigId = option.Id
		nativeValue.Value = acp.SessionConfigValueId(selected)
		request := acp.SetSessionConfigOptionRequest{ValueId: &nativeValue}
		if err := bridge.SendRequest(ctx, "session/set_config_option", request, &response); err != nil {
			return response, err
		}
		confirmed := false
		for _, result := range response.ConfigOptions {
			if result.Select != nil &&
				result.Select.Id == option.Id &&
				result.Select.CurrentValue == nativeValue.Value {
				confirmed = true
				break
			}
		}
		adapter.mutex.Lock()
		response.ConfigOptions = adapter.normalizeOptions(response.ConfigOptions, state)
		state.options = response.ConfigOptions
		adapter.mutex.Unlock()
		if !confirmed {
			return acp.SetSessionConfigOptionResponse{}, acp.NewInvalidParams(map[string]any{
				"message": "Cursor did not confirm thinking selection",
			})
		}
	}
	return response, nil
}

// thoughtSelects 只使用 Cursor 声明为 thought_level 的选择项。
func thoughtSelects(options []acp.SessionConfigOption) []*acp.SessionConfigOptionSelect {
	result := []*acp.SessionConfigOptionSelect{}
	for _, option := range options {
		if option.Select != nil && option.Select.Category != nil &&
			*option.Select.Category == acp.SessionConfigOptionCategoryThoughtLevel {
			result = append(result, option.Select)
		}
	}
	return result
}

// cursorThoughtOptions 把思考开关与强度组合成一个宿主选择。
func cursorThoughtOptions(options []acp.SessionConfigOption) []acp.SessionConfigOption {
	thoughts := thoughtSelects(options)
	if len(thoughts) < 2 {
		return options
	}
	choices, current := cursorThoughtChoices(thoughts)
	if len(choices) == 0 {
		return options
	}
	result := make([]acp.SessionConfigOption, 0, len(options))
	for _, option := range options {
		if option.Select == nil || option.Select.Category == nil ||
			*option.Select.Category != acp.SessionConfigOptionCategoryThoughtLevel {
			result = append(result, option)
		}
	}
	category := acp.SessionConfigOptionCategoryThoughtLevel
	reasoning := acp.SessionConfigOption{
		Select: &acp.SessionConfigOptionSelect{
			Id:           "reasoning",
			Name:         "Thinking",
			Category:     &category,
			CurrentValue: current,
			Options:      acp.SessionConfigSelectOptions{Ungrouped: &choices},
		},
	}
	return append(result, reasoning)
}

// cursorThoughtChoices 枚举官方参数组合；关闭思考时不重复无效强度。
func cursorThoughtChoices(
	thoughts []*acp.SessionConfigOptionSelect,
) (acp.SessionConfigSelectOptionsUngrouped, acp.SessionConfigValueId) {
	thoughts = orderedThoughts(thoughts)
	choices := acp.SessionConfigSelectOptionsUngrouped{}
	currentParams := make(map[string]string, len(thoughts))
	for _, option := range thoughts {
		currentParams[string(option.Id)] = string(option.CurrentValue)
	}
	var visit func(int, map[string]string, []string)
	visit = func(index int, params map[string]string, names []string) {
		if len(choices) >= 128 {
			return
		}
		if index == len(thoughts) {
			data, _ := json.Marshal(params)
			choices = append(choices, acp.SessionConfigSelectOption{
				Value: acp.SessionConfigValueId(data),
				Name:  strings.Join(names, " · "),
			})
			return
		}
		option := thoughts[index]
		for _, value := range selectValues(option.Options) {
			next := make(map[string]string, len(params)+1)
			for key, current := range params {
				next[key] = current
			}
			next[string(option.Id)] = string(value.Value)
			if option.Id == "thinking" && value.Value == "false" {
				data, _ := json.Marshal(next)
				choices = append(choices, acp.SessionConfigSelectOption{Value: acp.SessionConfigValueId(data), Name: value.Name})
				continue
			}
			labels := append([]string(nil), names...)
			if option.Id != "thinking" || value.Value != "true" {
				labels = append(labels, value.Name)
			}
			visit(index+1, next, labels)
		}
	}
	visit(0, map[string]string{}, nil)
	var current acp.SessionConfigValueId
	for _, choice := range choices {
		var params map[string]string
		_ = json.Unmarshal([]byte(choice.Value), &params)
		matches := true
		for key, value := range params {
			if currentParams[key] != value {
				matches = false
				break
			}
		}
		if matches {
			current = choice.Value
			break
		}
	}
	return choices, current
}

// orderedThoughts 把 thinking 开关移到首位，避免关闭时仍设置强度。
func orderedThoughts(thoughts []*acp.SessionConfigOptionSelect) []*acp.SessionConfigOptionSelect {
	ordered := append([]*acp.SessionConfigOptionSelect(nil), thoughts...)
	for index, option := range ordered {
		if option.Id == "thinking" {
			ordered[0], ordered[index] = ordered[index], ordered[0]
			break
		}
	}
	return ordered
}

// selectValues 展开官方选择项的分组，不改变原始顺序或标识。
func selectValues(options acp.SessionConfigSelectOptions) []acp.SessionConfigSelectOption {
	if options.Ungrouped != nil {
		return *options.Ungrouped
	}
	result := []acp.SessionConfigSelectOption{}
	if options.Grouped != nil {
		for _, group := range *options.Grouped {
			result = append(result, group.Options...)
		}
	}
	return result
}

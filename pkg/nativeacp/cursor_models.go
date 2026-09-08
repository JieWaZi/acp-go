package nativeacp

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/JieWaZi/acp-go/pkg/acpmeta"
	acp "github.com/coder/acp-go-sdk"
)

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

// loadCursorModels 一次读取官方参数目录，不切换默认模型，也不执行提示。
func (agent *Agent) loadCursorModels(ctx context.Context) {
	if !agent.config.CursorExtensions {
		return
	}
	agent.mutex.Lock()
	loaded := agent.cursorModels != nil
	agent.mutex.Unlock()
	if loaded {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	catalog, err := acp.SendRequest[cursorModelCatalog](agent.conn, ctx, "cursor/list_available_models", map[string]any{})
	if err != nil {
		return
	}
	models := map[string][]acp.SessionConfigOption{}
	for _, model := range catalog.Models {
		if model.Value != "" {
			models[model.Value] = model.ConfigOptions
		}
	}
	agent.mutex.Lock()
	agent.cursorModels = models
	agent.mutex.Unlock()
}

// normalizeSessionOptions 由调用方持有 mutex，更新原生配置与当前模型的规范化映射。
func (agent *Agent) normalizeSessionOptions(options []acp.SessionConfigOption, state *sessionOptions) []acp.SessionConfigOption {
	state.ids = make(map[acp.SessionConfigId]acp.SessionConfigId)
	if agent.config.CursorExtensions {
		state.cursorNativeOptions = append([]acp.SessionConfigOption(nil), options...)
		options = cursorThoughtOptions(options)
	}
	result := normalizeOptions(options, state.ids)
	if !agent.config.CursorExtensions {
		return result
	}
	for _, option := range result {
		if option.Select == nil || option.Select.Id != "model" {
			continue
		}
		mapOptions := func(values []acp.SessionConfigSelectOption) {
			for index, value := range values {
				if params, ok := agent.cursorModels[string(value.Value)]; ok {
					values[index].Meta = acpmeta.WithModelConfigOptions(value.Meta, normalizeOptions(cursorThoughtOptions(params), map[acp.SessionConfigId]acp.SessionConfigId{}))
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

// thoughtSelects 只使用 Cursor 已声明为 thought_level 的选择项。
func thoughtSelects(options []acp.SessionConfigOption) []*acp.SessionConfigOptionSelect {
	result := []*acp.SessionConfigOptionSelect{}
	for _, option := range options {
		if option.Select != nil && option.Select.Category != nil && *option.Select.Category == acp.SessionConfigOptionCategoryThoughtLevel {
			result = append(result, option.Select)
		}
	}
	return result
}

// cursorThoughtOptions 把原生思考开关与强度组合成一个宿主选择，保留所有真实值而不套用固定等级。
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
		if option.Select != nil && option.Select.Category != nil && *option.Select.Category == acp.SessionConfigOptionCategoryThoughtLevel {
			continue
		}
		result = append(result, option)
	}
	category := acp.SessionConfigOptionCategoryThoughtLevel
	result = append(result, acp.SessionConfigOption{Select: &acp.SessionConfigOptionSelect{Id: "reasoning", Name: "Thinking", Category: &category, CurrentValue: current, Options: acp.SessionConfigSelectOptions{Ungrouped: &choices}}})
	return result
}

// cursorThoughtChoices 枚举当前官方参数组合；关闭思考时不重复列出无效的强度组合。
func cursorThoughtChoices(thoughts []*acp.SessionConfigOptionSelect) (acp.SessionConfigSelectOptionsUngrouped, acp.SessionConfigValueId) {
	choices := acp.SessionConfigSelectOptionsUngrouped{}
	currentParams := map[string]string{}
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
			choices = append(choices, acp.SessionConfigSelectOption{Value: acp.SessionConfigValueId(data), Name: strings.Join(names, " · ")})
			return
		}
		option := thoughts[index]
		for _, value := range selectValues(option.Options) {
			next := make(map[string]string, len(params)+1)
			for key, v := range params {
				next[key] = v
			}
			next[string(option.Id)] = string(value.Value)
			if option.Id == "thinking" && value.Value == "false" {
				data, _ := json.Marshal(next)
				choices = append(choices, acp.SessionConfigSelectOption{Value: acp.SessionConfigValueId(data), Name: value.Name})
				continue
			}
			labels := append([]string{}, names...)
			if !(option.Id == "thinking" && value.Value == "true") {
				labels = append(labels, value.Name)
			}
			visit(index+1, next, labels)
		}
	}
	// 官方目录不要求 thinking 排在 effort 前面；关闭分支必须先于其他参数。
	ordered := append([]*acp.SessionConfigOptionSelect{}, thoughts...)
	for index, option := range ordered {
		if option.Id == "thinking" {
			ordered[0], ordered[index] = ordered[index], ordered[0]
			break
		}
	}
	thoughts = ordered
	visit(0, map[string]string{}, nil)
	var current acp.SessionConfigValueId
	for _, choice := range choices {
		var params map[string]string
		_ = json.Unmarshal([]byte(choice.Value), &params)
		matches := true
		for key, value := range params {
			if currentParams[key] != value {
				matches = false
			}
		}
		if matches {
			current = choice.Value
			break
		}
	}
	return choices, current
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

// setCursorThinking 在标准单选入口内逐项应用官方参数，并校验每个原生回执。
func (agent *Agent) setCursorThinking(ctx context.Context, state *sessionOptions, value acp.SetSessionConfigOptionValueId) (acp.SetSessionConfigOptionResponse, error) {
	agent.mutex.Lock()
	thoughts := thoughtSelects(state.cursorNativeOptions)
	agent.mutex.Unlock()
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
	// 开关先于强度，避免原生实现拒绝对已关闭的思考设置强度。
	ordered := append([]*acp.SessionConfigOptionSelect{}, thoughts...)
	for index, option := range ordered {
		if option.Id == "thinking" {
			ordered[0], ordered[index] = ordered[index], ordered[0]
			break
		}
	}
	var response acp.SetSessionConfigOptionResponse
	for _, option := range ordered {
		selected, ok := params[string(option.Id)]
		if !ok {
			continue
		}
		native := value
		native.ConfigId = option.Id
		native.Value = acp.SessionConfigValueId(selected)
		var err error
		response, err = acp.SendRequest[acp.SetSessionConfigOptionResponse](agent.conn, ctx, "session/set_config_option", acp.SetSessionConfigOptionRequest{ValueId: &native})
		if err != nil {
			return response, err
		}
		confirmed := false
		for _, result := range response.ConfigOptions {
			if result.Select != nil && result.Select.Id == option.Id && result.Select.CurrentValue == native.Value {
				confirmed = true
			}
		}
		agent.mutex.Lock()
		response.ConfigOptions = agent.normalizeSessionOptions(response.ConfigOptions, state)
		state.options = response.ConfigOptions
		agent.mutex.Unlock()
		if !confirmed {
			return acp.SetSessionConfigOptionResponse{}, acp.NewInvalidParams(map[string]any{"message": "Cursor did not confirm thinking selection"})
		}
	}
	return response, nil
}

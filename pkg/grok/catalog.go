package grok

import (
	"encoding/json"

	"github.com/JieWaZi/acp-go/pkg/acpmeta"
	"github.com/JieWaZi/acp-go/pkg/nativeacp"
	acp "github.com/coder/acp-go-sdk"
)

// legacyModelCatalog 只读取原生响应实际提供的模型元数据，不新增模型。
type legacyModelCatalog struct {
	// AvailableModels 是官方旧版目录。
	AvailableModels []legacyCatalogModel `json:"availableModels"`
}

// legacyCatalogModel 关联实际模型标识与供应方元数据。
type legacyCatalogModel struct {
	// ModelID 是原生模型标识。
	ModelID string `json:"modelId"`
	// Meta 保存支持标记和逐模型菜单原文。
	Meta map[string]json.RawMessage `json:"_meta"`
}

// catalogEffort 保留固定上游序列化的输入标识、规范值、文案与默认标记。
type catalogEffort struct {
	// ID 是 setter 接收的输入标识，可能与 Value 不同。
	ID string `json:"id"`
	// Value 是上游规范推理强度，仅用于校验来源。
	Value string `json:"value"`
	// Label 是供应方显示名称。
	Label *string `json:"label"`
	// Description 是供应方可选说明。
	Description *string `json:"description"`
	// Default 是供应方明确提供的默认标记。
	Default *bool `json:"default"`
}

// enrichModelCatalog 纯投影匹配的实际 model 选项；当前原生配置始终优先，不共享其他模型的会话值。
func enrichModelCatalog(snapshot nativeacp.SessionSnapshot) []acp.SessionConfigOption {
	catalog := legacyModelCatalog{}
	_ = json.Unmarshal(snapshot.Models, &catalog)
	menus := map[string][]acp.SessionConfigOption{}
	for _, model := range catalog.AvailableModels {
		if menu, known := modelReasoningMenu(model.Meta); known {
			menus[model.ModelID] = menu
		}
	}
	current := []acp.SessionConfigOption{}
	for _, option := range snapshot.ConfigOptions {
		if option.Select != nil && option.Select.Id == "reasoning" {
			current = append(current, option)
		}
	}
	result := append([]acp.SessionConfigOption{}, snapshot.ConfigOptions...)
	for index, option := range result {
		if option.Select == nil || option.Select.Id != "model" {
			continue
		}
		copy := *option.Select
		enrich := func(values []acp.SessionConfigSelectOption) []acp.SessionConfigSelectOption {
			cloned := append([]acp.SessionConfigSelectOption{}, values...)
			for index, value := range cloned {
				menu, known := menus[string(value.Value)]
				if value.Value == copy.CurrentValue {
					menu, known = current, true
				}
				if known {
					cloned[index].Meta = acpmeta.WithModelConfigOptions(value.Meta, menu)
				}
			}
			return cloned
		}
		if values := copy.Options.Ungrouped; values != nil {
			cloned := acp.SessionConfigSelectOptionsUngrouped(enrich(*values))
			copy.Options.Ungrouped = &cloned
		}
		if groups := copy.Options.Grouped; groups != nil {
			cloned := append(acp.SessionConfigSelectOptionsGrouped{}, (*groups)...)
			for index, group := range cloned {
				cloned[index].Options = enrich(group.Options)
			}
			copy.Options.Grouped = &cloned
		}
		result[index].Select = &copy
	}
	return result
}

// modelReasoningMenu 按固定上游支持标记与菜单序列化投影；畸形菜单不触发 fallback。
func modelReasoningMenu(meta map[string]json.RawMessage) ([]acp.SessionConfigOption, bool) {
	var supports *bool
	flag, supplied := meta["supportsReasoningEffort"]
	if !supplied {
		return nil, false
	}
	if err := json.Unmarshal(flag, &supports); err != nil || supports == nil {
		return nil, false
	}
	if !*supports {
		return []acp.SessionConfigOption{}, true
	}
	efforts := []catalogEffort{}
	if raw, supplied := meta["reasoningEfforts"]; supplied {
		if json.Unmarshal(raw, &efforts) != nil || efforts == nil {
			return nil, false
		}
	}
	if len(efforts) == 0 {
		efforts = legacyCatalogEfforts()
	}
	choices := acp.SessionConfigSelectOptionsUngrouped{}
	for _, effort := range efforts {
		missingPresentation := effort.ID == "" || effort.Label == nil
		invalidDefault := effort.Default == nil
		invalidValue := !knownEffort(effort.Value)
		if missingPresentation || invalidDefault || invalidValue {
			return nil, false
		}
		choices = append(choices, acp.SessionConfigSelectOption{
			Value: acp.SessionConfigValueId(effort.ID), Name: *effort.Label, Description: effort.Description,
			Meta: map[string]any{"default": *effort.Default},
		})
	}
	selected := choices[0].Value
	for _, choice := range choices {
		if choice.Meta["default"] == true {
			selected = choice.Value
			break
		}
	}
	category := acp.SessionConfigOptionCategory("thought_level")
	return []acp.SessionConfigOption{{Select: &acp.SessionConfigOptionSelect{
		Id: "reasoning", Name: "Reasoning Effort", Category: &category, Type: "select", CurrentValue: selected,
		Options: acp.SessionConfigSelectOptions{Ungrouped: &choices},
	}}}, true
}

// knownEffort 只接受固定上游 ReasoningEffort 枚举，不根据模型名称推断值。
func knownEffort(value string) bool {
	switch value {
	case "none", "minimal", "low", "medium", "high", "xhigh", "max":
		return true
	}
	return false
}

// legacyCatalogEfforts 精确复现 session_config.rs 的五项 fallback；仅在明确支持且菜单缺失或为空时使用。
func legacyCatalogEfforts() []catalogEffort {
	result := []catalogEffort{}
	labels := map[string]string{
		"minimal": "Minimal", "low": "Low", "medium": "Medium", "high": "High", "xhigh": "X-High",
	}
	for _, value := range []string{"minimal", "low", "medium", "high", "xhigh"} {
		label := labels[value]
		result = append(result, catalogEffort{ID: value, Value: value, Label: &label, Default: acp.Ptr(false)})
	}
	return result
}

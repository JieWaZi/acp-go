// Package qoder 将 Qoder 与 Qoder CN 的原生 ACP 配置差异限制在其适配层。
package qoder

import (
	"context"
	"slices"

	"github.com/JieWaZi/acp-go/pkg/nativeacp"
	acp "github.com/JieWaZi/acp-go/sdk"
)

// sessionAdapter 保留原生配置身份，只归一化 Qoder 的非标准类别。
type sessionAdapter struct{}

// NewSessionAdapter 创建无额外会话状态的 Qoder 配置适配器。
func NewSessionAdapter() nativeacp.SessionAdapter {
	return sessionAdapter{}
}

// NormalizeSession 归一化新建、加载和恢复会话返回的类别，保留原生身份与取值。
func (sessionAdapter) NormalizeSession(_ context.Context, _ nativeacp.SessionBridge, _ acp.SessionId, snapshot nativeacp.SessionSnapshot) ([]acp.SessionConfigOption, error) {
	return normalizeOptions(snapshot.ConfigOptions), nil
}

// NormalizeUpdate 对会话运行中的动态目录应用同一类别修正。
func (sessionAdapter) NormalizeUpdate(_ acp.SessionId, options []acp.SessionConfigOption) []acp.SessionConfigOption {
	return normalizeOptions(options)
}

// SetConfigOption 使用原生身份写入配置并返回当前目录。
func (sessionAdapter) SetConfigOption(ctx context.Context, bridge nativeacp.SessionBridge, request acp.SetSessionConfigOptionRequest) (acp.SetSessionConfigOptionResponse, bool, error) {
	var response acp.SetSessionConfigOptionResponse
	err := bridge.SendRequest(ctx, "session/set_config_option", request, &response)
	if err == nil {
		response.ConfigOptions = normalizeOptions(response.ConfigOptions)
	}
	return response, true, err
}

// ForgetSession 无需清理额外状态，原生 Agent 拥有会话生命周期。
func (sessionAdapter) ForgetSession(acp.SessionId) {}

// normalizeOptions 修正 Qoder 把推理档位标为模型的已知错误，不修改 CLI 的原始响应对象。
func normalizeOptions(options []acp.SessionConfigOption) []acp.SessionConfigOption {
	result := slices.Clone(options)
	for i, option := range result {
		if option.Select == nil || option.Select.Id != "reasoning_effort" || option.Select.Category == nil || *option.Select.Category != acp.SessionConfigOptionCategoryModel {
			continue
		}
		selection := *option.Select
		category := acp.SessionConfigOptionCategoryThoughtLevel
		selection.Category = &category
		result[i].Select = &selection
	}
	return result
}

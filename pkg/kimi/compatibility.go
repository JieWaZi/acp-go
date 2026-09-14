package kimi

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"

	"github.com/JieWaZi/acp-go/pkg/autoreview"
	"github.com/JieWaZi/acp-go/pkg/nativeacp"
	acp "github.com/coder/acp-go-sdk"
)

// legacyModels 是 Python Kimi 仍返回的旧模型选择协议。
type legacyModels struct {
	// CurrentModelID 是当前选择的上游模型标识。
	CurrentModelID string `json:"currentModelId"`
	// AvailableModels 是上游公开的模型列表。
	AvailableModels []legacyModel `json:"availableModels"`
}

// legacyModel 保存 Python Kimi 提供的原始模型身份。
type legacyModel struct {
	// ModelID 是切换模型时必须原样回传的标识。
	ModelID string `json:"modelId"`
	// Name 是模型显示名。
	Name string `json:"name"`
}

// compatibilitySession 保存单个 Kimi 会话的旧协议状态。
type compatibilitySession struct {
	// configMutex 串行化同一会话的配置写入。
	configMutex sync.Mutex
	// ids 将统一配置标识映射回上游原生标识。
	ids map[acp.SessionConfigId]acp.SessionConfigId
	// options 是最后一次返回给宿主的统一配置目录。
	options []acp.SessionConfigOption
	// legacyModel 表示模型必须通过 session/set_model 切换。
	legacyModel bool
	// legacyPermissions 表示上游只提供 default 模式，需要补充自动审查。
	legacyPermissions bool
	// workingDirectory 是执行风险审查使用的真实会话目录。
	workingDirectory string
}

// compatibilityAdapter 封装 Python Kimi 的旧模型与审批协议。
type compatibilityAdapter struct {
	// mutex 保护各会话的配置快照。
	mutex sync.Mutex
	// sessions 按 ACP 会话标识保存旧协议状态。
	sessions map[acp.SessionId]*compatibilitySession
	// reviewer 是 Kimi 自身的无工具自动审查器；nil 表示完全交给宿主。
	reviewer func(context.Context, autoreview.Request) (autoreview.Decision, error)
	// logger 记录审查失败并说明已经回退宿主。
	logger *slog.Logger
}

// newCompatibilityAdapter 创建不共享状态的 Kimi 兼容适配器。
func newCompatibilityAdapter(
	reviewer func(context.Context, autoreview.Request) (autoreview.Decision, error),
	logger *slog.Logger,
) *compatibilityAdapter {
	return &compatibilityAdapter{
		sessions: make(map[acp.SessionId]*compatibilitySession),
		reviewer: reviewer,
		logger:   logger,
	}
}

// NewCompatibilityAdapters 创建 Kimi 旧协议适配器，供标准 stdio 传输和对照测试组合使用。
func NewCompatibilityAdapters(
	reviewer func(context.Context, autoreview.Request) (autoreview.Decision, error),
	logger *slog.Logger,
) (nativeacp.SessionAdapter, nativeacp.PermissionAdapter) {
	adapter := newCompatibilityAdapter(reviewer, logger)
	if reviewer == nil {
		return adapter, nil
	}
	return adapter, adapter
}

// NormalizeSession 把 Python Kimi 的 models 目录投影为标准配置，并保存审批所需状态。
func (adapter *compatibilityAdapter) NormalizeSession(
	_ context.Context,
	_ nativeacp.SessionBridge,
	sessionID acp.SessionId,
	snapshot nativeacp.SessionSnapshot,
) ([]acp.SessionConfigOption, error) {
	state := &compatibilitySession{
		ids:              make(map[acp.SessionConfigId]acp.SessionConfigId),
		workingDirectory: snapshot.WorkingDirectory,
	}
	options := nativeacp.NormalizeOptions(snapshot.ConfigOptions, state.ids)
	if len(snapshot.Models) > 0 && !hasKimiConfig(options, "model") {
		var models legacyModels
		if err := json.Unmarshal(snapshot.Models, &models); err != nil {
			return nil, err
		}
		values := make(acp.SessionConfigSelectOptionsUngrouped, 0, len(models.AvailableModels))
		for _, model := range models.AvailableModels {
			values = append(values, acp.SessionConfigSelectOption{
				Value: acp.SessionConfigValueId(model.ModelID),
				Name:  model.Name,
			})
		}
		category := acp.SessionConfigOptionCategoryModel
		options = append(options, acp.SessionConfigOption{
			Select: &acp.SessionConfigOptionSelect{
				Id:           "model",
				Name:         "Model",
				Category:     &category,
				CurrentValue: acp.SessionConfigValueId(models.CurrentModelID),
				Options:      acp.SessionConfigSelectOptions{Ungrouped: &values},
			},
		})
		state.ids["model"] = "model"
		state.legacyModel = true
	}
	state.legacyPermissions = snapshot.Modes != nil &&
		len(snapshot.Modes.AvailableModes) == 1 &&
		snapshot.Modes.AvailableModes[0].Id == "default"
	state.options = options
	adapter.mutex.Lock()
	adapter.sessions[sessionID] = state
	adapter.mutex.Unlock()
	return options, nil
}

// NormalizeUpdate 规范化 Kimi 动态配置目录并刷新当前模型。
func (adapter *compatibilityAdapter) NormalizeUpdate(
	sessionID acp.SessionId,
	options []acp.SessionConfigOption,
) []acp.SessionConfigOption {
	adapter.mutex.Lock()
	defer adapter.mutex.Unlock()
	state := adapter.sessions[sessionID]
	if state == nil {
		state = &compatibilitySession{ids: make(map[acp.SessionConfigId]acp.SessionConfigId)}
		adapter.sessions[sessionID] = state
	}
	result := nativeacp.NormalizeOptions(options, state.ids)
	state.options = result
	return result
}

// SetConfigOption 处理 Kimi 旧模型切换，并对标准配置保留原生标识。
func (adapter *compatibilityAdapter) SetConfigOption(
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
	adapter.mutex.Unlock()
	if state == nil {
		return acp.SetSessionConfigOptionResponse{}, true, acp.NewInvalidParams(nil)
	}
	state.configMutex.Lock()
	defer state.configMutex.Unlock()
	adapter.mutex.Lock()
	nativeID, advertised := state.ids[value.ConfigId]
	legacy := state.legacyModel && value.ConfigId == "model"
	adapter.mutex.Unlock()
	if !advertised {
		return acp.SetSessionConfigOptionResponse{}, true, acp.NewInvalidParams(map[string]any{
			"message": "configuration not advertised by Kimi",
		})
	}
	if legacy {
		params := map[string]any{"sessionId": value.SessionId, "modelId": value.Value}
		if err := bridge.SendRequest(ctx, "session/set_model", params, nil); err != nil {
			return acp.SetSessionConfigOptionResponse{}, true, err
		}
		adapter.mutex.Lock()
		setCurrentKimiValue(state.options, "model", value.Value)
		response := acp.SetSessionConfigOptionResponse{
			ConfigOptions: append([]acp.SessionConfigOption(nil), state.options...),
		}
		adapter.mutex.Unlock()
		return response, true, nil
	}
	value.ConfigId = nativeID
	request.ValueId = &value
	var response acp.SetSessionConfigOptionResponse
	if err := bridge.SendRequest(ctx, "session/set_config_option", request, &response); err != nil {
		return response, true, err
	}
	adapter.mutex.Lock()
	response.ConfigOptions = nativeacp.NormalizeOptions(response.ConfigOptions, state.ids)
	state.options = response.ConfigOptions
	adapter.mutex.Unlock()
	return response, true, nil
}

// ForgetSession 删除关闭会话的旧协议状态。
func (adapter *compatibilityAdapter) ForgetSession(sessionID acp.SessionId) {
	adapter.mutex.Lock()
	delete(adapter.sessions, sessionID)
	adapter.mutex.Unlock()
}

// Review 仅为 Python Kimi 的单一 default 模式补充无工具自动审查。
func (adapter *compatibilityAdapter) Review(
	ctx context.Context,
	bridge nativeacp.PermissionBridge,
	evidence nativeacp.PermissionEvidence,
) (acp.RequestPermissionResponse, bool, error) {
	adapter.mutex.Lock()
	state := adapter.sessions[evidence.Request.SessionId]
	unsupported := state == nil ||
		!state.legacyPermissions ||
		adapter.reviewer == nil ||
		evidence.ToolOwner != evidence.Request.SessionId
	if unsupported {
		adapter.mutex.Unlock()
		return acp.RequestPermissionResponse{}, false, nil
	}
	workingDirectory := state.workingDirectory
	model := currentKimiModel(state.options)
	adapter.mutex.Unlock()
	tool := evidence.Request.ToolCall
	if tool.RawInput == nil {
		tool.RawInput = evidence.Detail.RawInput
		// Python Kimi 把完整累计参数放在工具 content 中；只有完整 JSON 对象可成为授权依据。
		content := evidence.Detail.Content
		if tool.RawInput == nil && len(content) == 1 &&
			content[0].Content != nil && content[0].Content.Content.Text != nil {
			var input map[string]any
			text := content[0].Content.Content.Text.Text
			if json.Unmarshal([]byte(text), &input) == nil && input != nil {
				tool.RawInput = input
			}
		}
	}
	if tool.Title == nil {
		tool.Title = evidence.Detail.Title
	}
	if tool.Kind == nil {
		tool.Kind = evidence.Detail.Kind
	}
	decision, reviewErr := adapter.reviewer(ctx, autoreview.Request{
		WorkingDirectory: workingDirectory,
		Model:            model,
		Prompt:           evidence.Prompt,
		Tool:             tool,
	})
	if reviewErr != nil && adapter.logger != nil {
		adapter.logger.Debug("permission review deferred to host", "error", reviewErr)
	}
	if ctx.Err() == nil {
		update := acp.SessionNotification{
			SessionId: evidence.Request.SessionId,
			Update: acp.SessionUpdate{
				ToolCallUpdate: &acp.SessionToolCallUpdate{
					ToolCallId: evidence.Request.ToolCall.ToolCallId,
					Meta: map[string]any{
						"acp-go/permission-review": autoreview.Metadata(decision, reviewErr != nil),
					},
				},
			},
		}
		if err := bridge.UpdateSession(ctx, update); err != nil {
			return acp.RequestPermissionResponse{Outcome: acp.NewRequestPermissionOutcomeCancelled()}, true, nil
		}
	}
	if reviewErr == nil && decision.Outcome == "allow" && ctx.Err() == nil && evidence.Active {
		for _, option := range evidence.Request.Options {
			if option.Kind == acp.PermissionOptionKindAllowOnce {
				return acp.RequestPermissionResponse{Outcome: acp.NewRequestPermissionOutcomeSelected(option.OptionId)}, true, nil
			}
		}
	}
	return acp.RequestPermissionResponse{}, false, nil
}

// hasKimiConfig 判断配置目录是否已经公开指定标识。
func hasKimiConfig(options []acp.SessionConfigOption, id acp.SessionConfigId) bool {
	for _, option := range options {
		if option.Select != nil && option.Select.Id == id {
			return true
		}
	}
	return false
}

// currentKimiModel 返回风险审查使用的当前模型标识。
func currentKimiModel(options []acp.SessionConfigOption) string {
	for _, option := range options {
		if option.Select != nil && option.Select.Id == "model" {
			return string(option.Select.CurrentValue)
		}
	}
	return ""
}

// setCurrentKimiValue 更新旧模型切换后的宿主配置快照。
func setCurrentKimiValue(
	options []acp.SessionConfigOption,
	id acp.SessionConfigId,
	value acp.SessionConfigValueId,
) {
	for index, option := range options {
		if option.Select != nil && option.Select.Id == id {
			copy := *option.Select
			copy.CurrentValue = value
			option.Select = &copy
			options[index] = option
		}
	}
}

package claude

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/JieWaZi/acp-go/pkg/claude/protocol"
	acp "github.com/coder/acp-go-sdk"
)

const (
	// modeConfigID 是权限模式配置标识。
	modeConfigID acp.SessionConfigId = "mode"
	// modelConfigID 是模型配置标识。
	modelConfigID acp.SessionConfigId = "model"
	// effortConfigID 是推理强度配置标识。
	effortConfigID acp.SessionConfigId = "effort"
	// fastConfigID 是快速模式配置标识。
	fastConfigID acp.SessionConfigId = "fast"
)

// permissionModeDefinition 保存一个可选择权限模式的展示信息。
type permissionModeDefinition struct {
	// ID 是 CLI 和 ACP 共用的稳定模式标识。
	ID acp.SessionModeId
	// Name 是客户端展示名称。
	Name string
	// Description 是模式安全边界说明。
	Description string
}

// sessionConfiguration 保存 CLI 报告且运行时可更新的 Session 配置。
type sessionConfiguration struct {
	// models 是初始化时可选模型目录。
	models []protocol.ModelInfo
	// model 是当前模型标识。
	model string
	// effort 是当前推理强度；空值表示未显式选择。
	effort string
	// fast 表示快速模式用户开关。
	fast bool
	// mode 是当前权限模式。
	mode acp.SessionModeId
}

// permissionModes 返回 V1 实际允许设置的非危险权限模式。
func permissionModes(auto bool) []permissionModeDefinition {
	modes := []permissionModeDefinition{
		{ID: "default", Name: "Default", Description: "Ask before sensitive operations."},
		{ID: "acceptEdits", Name: "Accept edits", Description: "Allow file edits while prompting for other sensitive operations."},
		{ID: "plan", Name: "Plan", Description: "Inspect and plan without changing files."},
		{ID: "dontAsk", Name: "Don't ask", Description: "Reject operations that require a permission prompt."},
	}
	if auto {
		modes = append(modes, permissionModeDefinition{
			ID: "auto", Name: "Auto", Description: "Let the active model choose safe automatic actions.",
		})
	}
	return modes
}

// newSessionConfiguration 从两条初始化通道建立配置真值。
func newSessionConfiguration(init protocol.SystemInitMessage, response protocol.InitializeControlResponse) sessionConfiguration {
	mode := acp.SessionModeId(init.PermissionMode)
	if mode == "" {
		mode = "default"
	}
	return sessionConfiguration{
		models: append([]protocol.ModelInfo(nil), response.Models...),
		model:  init.Model,
		fast:   init.FastModeState == "on" || init.FastModeState == "cooldown",
		mode:   mode,
	}
}

// configOptions 返回当前 Session 配置的 ACP 快照。
func (s *claudeSession) configOptions() []acp.SessionConfigOption {
	s.mu.Lock()
	configuration := s.configuration
	s.mu.Unlock()
	return configuration.options()
}

// modeState 返回当前模式和实际允许模式。
func (s *claudeSession) modeState() *acp.SessionModeState {
	s.mu.Lock()
	configuration := s.configuration
	s.mu.Unlock()
	return configuration.modeState()
}

// options 生成 mode、model、effort 与 fast 的稳定有序快照。
func (c sessionConfiguration) options() []acp.SessionConfigOption {
	options := []acp.SessionConfigOption{c.modeOption()}
	if len(c.models) > 0 && c.model != "" {
		options = append(options, c.modelOption())
		if model, ok := c.currentModel(); ok && model.SupportsEffort && len(model.SupportedEffortLevels) > 0 {
			options = append(options, c.effortOption(model))
		}
		if model, ok := c.currentModel(); ok && model.SupportsFastMode {
			options = append(options, c.fastOption())
		}
	}
	return options
}

// modeState 生成 ACP 模式列表。
func (c sessionConfiguration) modeState() *acp.SessionModeState {
	available := make([]acp.SessionMode, 0, len(permissionModes(c.currentModelSupportsAuto())))
	for _, mode := range permissionModes(c.currentModelSupportsAuto()) {
		description := mode.Description
		available = append(available, acp.SessionMode{
			Id: mode.ID, Name: mode.Name, Description: &description,
		})
	}
	return &acp.SessionModeState{AvailableModes: available, CurrentModeId: c.mode}
}

// modeOption 把权限模式表示为 ACP select 配置。
func (c sessionConfiguration) modeOption() acp.SessionConfigOption {
	category := acp.SessionConfigOptionCategoryMode
	values := make(acp.SessionConfigSelectOptionsUngrouped, 0)
	for _, mode := range permissionModes(c.currentModelSupportsAuto()) {
		description := mode.Description
		values = append(values, acp.SessionConfigSelectOption{
			Value: acp.SessionConfigValueId(mode.ID), Name: mode.Name, Description: &description,
		})
	}
	description := "Tool permission behavior for this session"
	return acp.SessionConfigOption{Select: &acp.SessionConfigOptionSelect{
		Id: modeConfigID, Name: "Mode", Description: &description, Category: &category,
		CurrentValue: acp.SessionConfigValueId(c.mode),
		Options:      acp.SessionConfigSelectOptions{Ungrouped: &values}, Type: "select",
	}}
}

// modelOption 把初始化模型目录表示为 ACP select 配置。
func (c sessionConfiguration) modelOption() acp.SessionConfigOption {
	category := acp.SessionConfigOptionCategoryModel
	values := make(acp.SessionConfigSelectOptionsUngrouped, 0, len(c.models)+1)
	if _, ok := c.modelByID(c.model); !ok {
		values = append(values, acp.SessionConfigSelectOption{
			Value: acp.SessionConfigValueId(c.model), Name: c.model,
		})
	}
	for _, model := range c.models {
		description := model.Description
		name := model.DisplayName
		if name == "" {
			name = model.Value
		}
		values = append(values, acp.SessionConfigSelectOption{
			Value: acp.SessionConfigValueId(model.Value), Name: name, Description: &description,
		})
	}
	description := "Model used for this session"
	return acp.SessionConfigOption{Select: &acp.SessionConfigOptionSelect{
		Id: modelConfigID, Name: "Model", Description: &description, Category: &category,
		CurrentValue: acp.SessionConfigValueId(c.model),
		Options:      acp.SessionConfigSelectOptions{Ungrouped: &values}, Type: "select",
	}}
}

// effortOption 把当前模型的推理强度表示为 ACP select 配置。
func (c sessionConfiguration) effortOption(model protocol.ModelInfo) acp.SessionConfigOption {
	category := acp.SessionConfigOptionCategoryThoughtLevel
	values := make(acp.SessionConfigSelectOptionsUngrouped, 0, len(model.SupportedEffortLevels))
	current := c.effort
	if current == "" {
		current = defaultEffort(model.SupportedEffortLevels)
	}
	for _, effort := range model.SupportedEffortLevels {
		values = append(values, acp.SessionConfigSelectOption{
			Value: acp.SessionConfigValueId(effort), Name: titleCase(effort),
		})
	}
	description := "Amount of reasoning used by the selected model"
	return acp.SessionConfigOption{Select: &acp.SessionConfigOptionSelect{
		Id: effortConfigID, Name: "Effort", Description: &description, Category: &category,
		CurrentValue: acp.SessionConfigValueId(current),
		Options:      acp.SessionConfigSelectOptions{Ungrouped: &values}, Type: "select",
	}}
}

// fastOption 把快速模式表示为兼容所有客户端的 select 配置。
func (c sessionConfiguration) fastOption() acp.SessionConfigOption {
	category := acp.SessionConfigOptionCategoryModel
	values := acp.SessionConfigSelectOptionsUngrouped{
		{Value: "on", Name: "On"},
		{Value: "off", Name: "Off"},
	}
	current := acp.SessionConfigValueId("off")
	if c.fast {
		current = "on"
	}
	description := "Faster responses on supported models"
	return acp.SessionConfigOption{Select: &acp.SessionConfigOptionSelect{
		Id: fastConfigID, Name: "Fast mode", Description: &description, Category: &category,
		CurrentValue: current, Options: acp.SessionConfigSelectOptions{Ungrouped: &values}, Type: "select",
	}}
}

// setConfigOption 在 Session 内串行调用 CLI，成功后才提交本地状态。
func (s *claudeSession) setConfigOption(ctx context.Context, request acp.SetSessionConfigOptionRequest) error {
	s.configMu.Lock()
	defer s.configMu.Unlock()
	s.mu.Lock()
	if s.closed {
		err := s.fatalErr
		s.mu.Unlock()
		if err == nil {
			err = ErrClaudeSessionClosed
		}
		return err
	}
	configuration := s.configuration
	s.mu.Unlock()

	if request.Boolean != nil {
		if request.Boolean.ConfigId != fastConfigID {
			return fmt.Errorf("setting Claude config: boolean value is invalid for %q", request.Boolean.ConfigId)
		}
		return s.applyFast(ctx, configuration, request.Boolean.Value)
	}
	if request.ValueId == nil {
		return errors.New("setting Claude config: request has no value")
	}
	id := request.ValueId.ConfigId
	value := string(request.ValueId.Value)
	switch id {
	case modeConfigID:
		return s.applyMode(ctx, value)
	case modelConfigID:
		return s.applyModel(ctx, configuration, value)
	case effortConfigID:
		return s.applyEffort(ctx, configuration, value)
	case fastConfigID:
		if value != "on" && value != "off" {
			return fmt.Errorf("setting Claude fast mode: invalid value %q", value)
		}
		return s.applyFast(ctx, configuration, value == "on")
	default:
		return fmt.Errorf("setting Claude config: unknown option %q", id)
	}
}

// setMode 校验目标模式，通过 control request 生效后再更新快照。
func (s *claudeSession) setMode(ctx context.Context, value string) error {
	s.configMu.Lock()
	defer s.configMu.Unlock()
	return s.applyMode(ctx, value)
}

// applyMode 在已取得配置串行锁后更新权限模式。
func (s *claudeSession) applyMode(ctx context.Context, value string) error {
	s.mu.Lock()
	configuration := s.configuration
	s.mu.Unlock()
	valid := false
	for _, mode := range permissionModes(configuration.currentModelSupportsAuto()) {
		if string(mode.ID) == value {
			valid = true
			break
		}
	}
	if !valid {
		return fmt.Errorf("setting Claude mode: invalid value %q", value)
	}
	if err := s.transport.Call(ctx, protocol.SetPermissionModeControlRequest{
		Subtype: protocol.ControlSetPermissionMode, Mode: value,
	}, nil); err != nil {
		return fmt.Errorf("setting Claude mode: %w", err)
	}
	s.mu.Lock()
	s.configuration.mode = acp.SessionModeId(value)
	s.mu.Unlock()
	return nil
}

// applyModel 校验模型，CLI 接受后更新模型并清理不再适用的 effort/fast 状态。
func (s *claudeSession) applyModel(ctx context.Context, configuration sessionConfiguration, value string) error {
	model, ok := configuration.modelByID(value)
	if !ok {
		return fmt.Errorf("setting Claude model: unknown model %q", value)
	}
	if configuration.mode == "auto" && !model.SupportsAutoMode {
		// 先收紧权限再切换到不支持 auto 的模型，任何中途失败都不会扩大权限。
		if err := s.transport.Call(ctx, protocol.SetPermissionModeControlRequest{
			Subtype: protocol.ControlSetPermissionMode, Mode: "default",
		}, nil); err != nil {
			return fmt.Errorf("clamping Claude mode before model switch: %w", err)
		}
		s.mu.Lock()
		s.configuration.mode = "default"
		s.mu.Unlock()
	}
	if err := s.transport.Call(ctx, protocol.SetModelControlRequest{
		Subtype: protocol.ControlSetModel, Model: &value,
	}, nil); err != nil {
		return fmt.Errorf("setting Claude model: %w", err)
	}
	s.mu.Lock()
	s.configuration.model = value
	// 模型切换后由 CLI 选择该模型默认 effort，本地清空显式覆盖并按目录展示默认值。
	s.configuration.effort = ""
	if !model.SupportsFastMode {
		s.configuration.fast = false
	}
	s.mu.Unlock()
	return nil
}

// applyEffort 仅允许当前模型声明的推理强度。
func (s *claudeSession) applyEffort(ctx context.Context, configuration sessionConfiguration, value string) error {
	model, ok := configuration.currentModel()
	if !ok || !model.SupportsEffort || !contains(model.SupportedEffortLevels, value) {
		return fmt.Errorf("setting Claude effort: unsupported value %q", value)
	}
	if err := s.transport.Call(ctx, protocol.ApplyFlagSettingsControlRequest{
		Subtype: protocol.ControlApplyFlagSettings, Settings: map[string]any{"effortLevel": value},
	}, nil); err != nil {
		return fmt.Errorf("setting Claude effort: %w", err)
	}
	s.mu.Lock()
	s.configuration.effort = value
	s.mu.Unlock()
	return nil
}

// applyFast 只在当前模型支持时发送快速模式设置。
func (s *claudeSession) applyFast(ctx context.Context, configuration sessionConfiguration, enabled bool) error {
	model, ok := configuration.currentModel()
	if !ok || !model.SupportsFastMode {
		return errors.New("setting Claude fast mode: current model does not support fast mode")
	}
	if err := s.transport.Call(ctx, protocol.ApplyFlagSettingsControlRequest{
		Subtype: protocol.ControlApplyFlagSettings, Settings: map[string]any{"fastMode": enabled},
	}, nil); err != nil {
		return fmt.Errorf("setting Claude fast mode: %w", err)
	}
	s.mu.Lock()
	s.configuration.fast = enabled
	s.mu.Unlock()
	return nil
}

// currentModel 返回当前模型目录项。
func (c sessionConfiguration) currentModel() (protocol.ModelInfo, bool) {
	return c.modelByID(c.model)
}

// modelByID 按 value 或 resolvedModel 精确查找模型。
func (c sessionConfiguration) modelByID(id string) (protocol.ModelInfo, bool) {
	for _, model := range c.models {
		if model.Value == id || model.ResolvedModel == id {
			return model, true
		}
	}
	return protocol.ModelInfo{}, false
}

// currentModelSupportsAuto 返回当前模型是否允许 auto 权限模式。
func (c sessionConfiguration) currentModelSupportsAuto() bool {
	model, ok := c.currentModel()
	return ok && model.SupportsAutoMode
}

// defaultEffort 在可用值中优先选择 medium，否则选择首项。
func defaultEffort(values []string) string {
	if contains(values, "medium") {
		return "medium"
	}
	if len(values) > 0 {
		return values[0]
	}
	return ""
}

// contains 判断字符串列表是否包含目标值。
func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

// titleCase 把配置值首字母转为大写展示名。
func titleCase(value string) string {
	if value == "" {
		return value
	}
	return strings.ToUpper(value[:1]) + value[1:]
}

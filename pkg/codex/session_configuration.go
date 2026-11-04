package codex

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/JieWaZi/acp-go/pkg/acpmeta"
	"github.com/JieWaZi/acp-go/pkg/codex/protocol"

	acp "github.com/JieWaZi/acp-go/sdk"
)

const (
	// modeConfigID 是 codex-acp 对外公开的模式配置标识。
	modeConfigID acp.SessionConfigId = "mode"
	// modelConfigID 是 codex-acp 对外公开的模型配置标识。
	modelConfigID acp.SessionConfigId = "model"
	// reasoningEffortConfigID 是 codex-acp 对外公开的推理强度配置标识。
	reasoningEffortConfigID acp.SessionConfigId = "reasoning_effort"
	serviceTierConfigID     acp.SessionConfigId = "service_tier"
)

// agentModeDefinition 聚合一个用户可选模式对应的 ACP 展示信息与 Codex 安全策略。
type agentModeDefinition struct {
	// ID 是 ACP 配置和 session mode 共同使用的稳定标识。
	ID acp.SessionModeId
	// Name 是客户端展示的模式名称。
	Name string
	// Description 是客户端展示的安全能力说明。
	Description string
	// ApprovalPolicy 是传给 app-server 的审批策略。
	ApprovalPolicy protocol.ApprovalPolicy
	// SandboxPolicy 是传给 app-server 的细粒度沙箱策略。
	SandboxPolicy protocol.SandboxPolicy
	// SandboxMode 是恢复 thread 时使用的粗粒度沙箱枚举。
	SandboxMode protocol.SandboxEnum
}

// modelSelection 保存当前模型与推理强度，供 turn/start 消费方读取。
type modelSelection struct {
	// Model 是不包含 effort 后缀的 app-server 模型标识。
	Model string
	// Effort 是当前模型选定的推理强度。
	Effort string
	// Mode 是当前审批与沙箱模式标识。
	Mode acp.SessionModeId
	// ServiceTier is the requested native override, independent of observed state.
	ServiceTier protocol.OptionalNullable[string]
	// ModelRequested distinguishes explicit selection from native observation.
	ModelRequested bool
	// EffortRequested distinguishes explicit selection from native observation.
	EffortRequested bool
}

// sessionConfiguration 只维护单个 session 的配置选择，不持有 runtime 生命周期状态。
type sessionConfiguration struct {
	// models 是 app-server 返回的模型目录快照。
	models []protocol.ModelListResponseDatum
	// current 是当前模型、effort 与模式选择。
	current modelSelection
	// effortExplicit records an explicit selection that must not be silently replaced.
	effortExplicit bool
	// observedServiceTier is the latest explicit native thread response, not an ACK.
	observedServiceTier protocol.OptionalNullable[string]
}

// agentModes 返回顺序稳定的三个 V1 权限模式。
func agentModes() []agentModeDefinition {
	approvalOnRequest := protocol.OnRequest
	approvalNever := protocol.Never
	networkDisabled := false
	excludeTmp := false
	return []agentModeDefinition{
		{
			ID:             "read-only",
			Name:           "Read-only",
			Description:    "Requires approval to edit files and run commands.",
			ApprovalPolicy: protocol.ApprovalPolicy{Enum: &approvalOnRequest},
			SandboxPolicy: protocol.SandboxPolicy{
				Type:          protocol.SandboxPolicyTypeReadOnly,
				NetworkAccess: &protocol.NetworkAccessUnion{Bool: &networkDisabled},
			},
			SandboxMode: protocol.ReadOnly,
		},
		{
			ID:             "agent",
			Name:           "Agent",
			Description:    "Read and edit files, and run commands.",
			ApprovalPolicy: protocol.ApprovalPolicy{Enum: &approvalOnRequest},
			SandboxPolicy: protocol.SandboxPolicy{
				Type:                protocol.SandboxPolicyTypeWorkspaceWrite,
				WritableRoots:       []string{},
				NetworkAccess:       &protocol.NetworkAccessUnion{Bool: &networkDisabled},
				ExcludeTmpdirEnvVar: &excludeTmp,
				ExcludeSlashTmp:     &excludeTmp,
			},
			SandboxMode: protocol.WorkspaceWrite,
		},
		{
			ID:             "agent-full-access",
			Name:           "Agent (full access)",
			Description:    "Codex can edit files outside this workspace and run commands with network access. Exercise caution when using.",
			ApprovalPolicy: protocol.ApprovalPolicy{Enum: &approvalNever},
			SandboxPolicy:  protocol.SandboxPolicy{Type: protocol.SandboxPolicyTypeDangerFullAccess},
			SandboxMode:    protocol.DangerFullAccess,
		},
	}
}

// findAgentMode 按稳定 ID 查找模式；未知值不会回退到宽松模式。
func findAgentMode(id acp.SessionModeId) (agentModeDefinition, bool) {
	for _, mode := range agentModes() {
		if mode.ID == id {
			return mode, true
		}
	}
	return agentModeDefinition{}, false
}

// newSessionConfiguration 从 app-server 模型目录和实际当前值创建配置组件。
func newSessionConfiguration(
	models []protocol.ModelListResponseDatum,
	currentModel string,
	currentEffort string,
	currentMode acp.SessionModeId,
) (*sessionConfiguration, error) {
	if currentModel == "" {
		return nil, fmt.Errorf("current model is empty")
	}
	if _, ok := findAgentMode(currentMode); !ok {
		return nil, fmt.Errorf("unknown agent mode %q", currentMode)
	}
	return &sessionConfiguration{
		models: append([]protocol.ModelListResponseDatum(nil), models...),
		current: modelSelection{
			Model:  currentModel,
			Effort: currentEffort,
			Mode:   currentMode,
		},
	}, nil
}

// Selection 返回当前选择的值副本，避免消费方获得内部可变状态。
func (c *sessionConfiguration) Selection() modelSelection {
	return c.current
}

// ModeDefinition 返回当前模式对应的 app-server 安全策略。
func (c *sessionConfiguration) ModeDefinition() agentModeDefinition {
	mode, _ := findAgentMode(c.current.Mode)
	return mode
}

// ModeState 直接使用 ACP SDK DTO 描述可选模式和当前模式。
func (c *sessionConfiguration) ModeState() acp.SessionModeState {
	available := make([]acp.SessionMode, 0, len(agentModes()))
	for _, mode := range agentModes() {
		description := mode.Description
		available = append(available, acp.SessionMode{
			Id: mode.ID, Name: mode.Name, Description: &description,
		})
	}
	return acp.SessionModeState{AvailableModes: available, CurrentModeId: c.current.Mode}
}

// Options 按稳定顺序构造 mode、model 与可用的 reasoning_effort ACP 配置。
func (c *sessionConfiguration) Options() []acp.SessionConfigOption {
	options := []acp.SessionConfigOption{c.modeOption(), c.modelOption()}
	if model, ok := c.findModel(c.current.Model); ok {
		if len(model.SupportedReasoningEfforts) > 0 {
			options = append(options, c.effortOption(model))
		}
		if tier, known := c.tierOption(model); known {
			options = append(options, tier)
		}
	}
	return options
}

// Select 校验并应用一个 ACP 配置选择；未知值保持原状态并返回错误。
func (c *sessionConfiguration) Select(id acp.SessionConfigId, value string) error {
	switch id {
	case modeConfigID:
		modeID := acp.SessionModeId(value)
		if _, ok := findAgentMode(modeID); !ok {
			return fmt.Errorf("unknown agent mode %q", value)
		}
		c.current.Mode = modeID
		return nil
	case modelConfigID:
		return c.selectModel(value)
	case reasoningEffortConfigID:
		return c.selectEffort(value)
	case serviceTierConfigID:
		return c.selectTier(value)
	default:
		return fmt.Errorf("unknown session config %q", id)
	}
}

// selectModel 应用模型选择，并保留有效 effort；无效时使用模型默认值。
func (c *sessionConfiguration) selectModel(id string) error {
	model, ok := c.findModel(id)
	if !ok {
		if id == c.current.Model {
			c.current.ModelRequested = true
			return nil
		}
		return fmt.Errorf("unknown model %q", id)
	}
	effort := c.current.Effort
	if _, ok := findSupportedEffort(model.SupportedReasoningEfforts, effort); !ok {
		if c.effortExplicit {
			return fmt.Errorf("model %q does not support selected reasoning effort %q", id, effort)
		}
		effort = model.DefaultReasoningEffort
	}
	if tier, present := c.current.ServiceTier.Value(); present && !supportsTier(model, tier) {
		return fmt.Errorf("model %q does not support selected service tier %q", id, tier)
	}
	c.current.Model = id
	c.current.ModelRequested = true
	c.current.Effort = effort
	return nil
}

// selectEffort 只允许当前已编目模型声明支持的 reasoning effort。
func (c *sessionConfiguration) selectEffort(effort string) error {
	model, ok := c.findModel(c.current.Model)
	if !ok {
		return fmt.Errorf("model %q has no advertised reasoning efforts", c.current.Model)
	}
	selected, ok := findSupportedEffort(model.SupportedReasoningEfforts, effort)
	if !ok {
		return fmt.Errorf("model %q does not support reasoning effort %q", c.current.Model, effort)
	}
	c.effortExplicit = true
	c.current.EffortRequested = true
	c.current.Effort = selected.ReasoningEffort
	return nil
}

// findModel 在 app-server 模型目录中按模型 ID 查找条目。
func (c *sessionConfiguration) findModel(id string) (protocol.ModelListResponseDatum, bool) {
	for _, model := range c.models {
		if model.ID == id {
			return model, true
		}
	}
	return protocol.ModelListResponseDatum{}, false
}

// findSupportedEffort 在一个模型声明的可选 effort 中做精确匹配。
func findSupportedEffort(
	options []protocol.SupportedReasoningEffortElement,
	effort string,
) (protocol.SupportedReasoningEffortElement, bool) {
	for _, option := range options {
		if option.ReasoningEffort == effort {
			return option, true
		}
	}
	return protocol.SupportedReasoningEffortElement{}, false
}

// modeOption 将当前模式和全部模式直接表示为 ACP select 配置。
func (c *sessionConfiguration) modeOption() acp.SessionConfigOption {
	category := acp.SessionConfigOptionCategoryMode
	options := make(acp.SessionConfigSelectOptionsUngrouped, 0, len(agentModes()))
	for _, mode := range agentModes() {
		description := mode.Description
		options = append(options, acp.SessionConfigSelectOption{
			Value: acp.SessionConfigValueId(mode.ID), Name: mode.Name, Description: &description,
		})
	}
	description := "Approval and sandboxing preset for the session"
	return acp.SessionConfigOption{Select: &acp.SessionConfigOptionSelect{
		Id: modeConfigID, Name: "Mode", Description: &description, Category: &category,
		CurrentValue: acp.SessionConfigValueId(c.current.Mode),
		Options:      acp.SessionConfigSelectOptions{Ungrouped: &options}, Type: "select",
	}}
}

// modelOption 将 app-server 模型目录直接转换为 ACP model select 配置。
func (c *sessionConfiguration) modelOption() acp.SessionConfigOption {
	category := acp.SessionConfigOptionCategoryModel
	options := make(acp.SessionConfigSelectOptionsUngrouped, 0, len(c.models)+1)
	if _, ok := c.findModel(c.current.Model); !ok {
		options = append(options, acp.SessionConfigSelectOption{
			Value: acp.SessionConfigValueId(c.current.Model), Name: c.current.Model,
		})
	}
	for _, model := range c.models {
		description := model.Description
		var catalog acpmeta.ModelConfigCatalog
		switch {
		case model.SupportedReasoningEfforts == nil:
			catalog.UnknownConfigIDs = append(catalog.UnknownConfigIDs, reasoningEffortConfigID)
		case len(model.SupportedReasoningEfforts) > 0:
			catalog.Options = append(catalog.Options, c.effortOption(model))
		}
		if tier, known := c.tierOption(model); !known {
			catalog.UnknownConfigIDs = append(catalog.UnknownConfigIDs, serviceTierConfigID)
		} else if len(nativeTiers(model)) > 0 {
			catalog.Options = append(catalog.Options, tier)
		}
		meta := acpmeta.WithModelConfigCatalog(nil, catalog)
		options = append(options, acp.SessionConfigSelectOption{
			Value: acp.SessionConfigValueId(model.ID), Name: model.DisplayName, Description: &description, Meta: meta,
		})
	}
	description := "Model Codex uses for the session"
	return acp.SessionConfigOption{Select: &acp.SessionConfigOptionSelect{
		Id: modelConfigID, Name: "Model", Description: &description, Category: &category,
		CurrentValue: acp.SessionConfigValueId(c.current.Model),
		Options:      acp.SessionConfigSelectOptions{Ungrouped: &options}, Type: "select",
	}}
}

// effortOption 将当前模型声明的 reasoning effort 转换为 ACP thought_level 配置。
func (c *sessionConfiguration) effortOption(model protocol.ModelListResponseDatum) acp.SessionConfigOption {
	category := acp.SessionConfigOptionCategoryThoughtLevel
	options := make(acp.SessionConfigSelectOptionsUngrouped, 0, len(model.SupportedReasoningEfforts))
	for _, effort := range model.SupportedReasoningEfforts {
		description := effort.Description
		options = append(options, acp.SessionConfigSelectOption{
			Value:       acp.SessionConfigValueId(effort.ReasoningEffort),
			Name:        capitalize(effort.ReasoningEffort),
			Description: &description,
		})
	}
	description := "How much reasoning effort the model should use"
	current := c.current.Effort
	if model.ID != c.current.Model {
		current = model.DefaultReasoningEffort
	}
	return acp.SessionConfigOption{Select: &acp.SessionConfigOptionSelect{
		Id: reasoningEffortConfigID, Name: "Reasoning effort", Description: &description,
		Category: &category, CurrentValue: acp.SessionConfigValueId(current),
		Options: acp.SessionConfigSelectOptions{Ungrouped: &options}, Type: "select",
	}}
}

// capitalize 将 effort 展示名的首字母转换为大写。
func capitalize(value string) string {
	if value == "" {
		return value
	}
	return strings.ToUpper(value[:1]) + value[1:]
}

// nativeTiers reads the modern catalog, falling back to the legacy IDs only
// when the modern field was absent. An explicit empty modern list is authoritative.
func nativeTiers(model protocol.ModelListResponseDatum) []protocol.ServiceTierElement {
	if model.ServiceTiers != nil {
		return model.ServiceTiers
	}
	tiers := make([]protocol.ServiceTierElement, 0, len(model.AdditionalSpeedTiers))
	for _, id := range model.AdditionalSpeedTiers {
		name := id
		if id == "fast" {
			name = "Fast"
		}
		tiers = append(tiers, protocol.ServiceTierElement{ID: id, Name: name})
	}
	return tiers
}

// supportsTier checks exact native IDs in the current model catalog.
func supportsTier(model protocol.ModelListResponseDatum, id string) bool {
	for _, tier := range nativeTiers(model) {
		if tier.ID == id && id != "" && id != "off" && id != "inherit" {
			return true
		}
	}
	return false
}

// selectTier validates and records an explicit native override.
func (c *sessionConfiguration) selectTier(value string) error {
	switch value {
	case "inherit":
		c.current.ServiceTier = protocol.OptionalNullable[string]{}
		return nil
	case "off":
		c.current.ServiceTier = protocol.NewOptionalNull[string]()
		return nil
	}
	model, ok := c.findModel(c.current.Model)
	if !ok || !supportsTier(model, value) {
		return fmt.Errorf("model %q does not support service tier %q", c.current.Model, value)
	}
	c.current.ServiceTier = protocol.NewOptionalValue(value)
	return nil
}

// tierOption describes the model tier catalog when its presence is known.
func (c *sessionConfiguration) tierOption(model protocol.ModelListResponseDatum) (acp.SessionConfigOption, bool) {
	if model.ServiceTiers == nil && model.AdditionalSpeedTiers == nil {
		return acp.SessionConfigOption{}, false
	}
	options := acp.SessionConfigSelectOptionsUngrouped{{Value: "inherit", Name: "Inherit"}, {Value: "off", Name: "Off"}}
	seen := map[string]bool{}
	for _, tier := range nativeTiers(model) {
		if tier.ID != "" && tier.ID != "off" && tier.ID != "inherit" && !seen[tier.ID] {
			options = append(options, acp.SessionConfigSelectOption{Value: acp.SessionConfigValueId(tier.ID), Name: tier.Name})
			seen[tier.ID] = true
		}
	}
	current := "inherit"
	if model.ID == c.current.Model && c.current.ServiceTier.IsNull() {
		current = "off"
	} else if value, ok := c.current.ServiceTier.Value(); model.ID == c.current.Model && ok && supportsTier(model, value) {
		current = value
	}
	var meta map[string]any
	if model.ID == c.current.Model && c.observedServiceTier.IsPresent() {
		meta = map[string]any{"observed_service_tier": nil}
		if value, ok := c.observedServiceTier.Value(); ok {
			meta["observed_service_tier"] = value
		}
	}
	return acp.SessionConfigOption{Select: &acp.SessionConfigOptionSelect{Meta: meta, Id: serviceTierConfigID, Name: "Service tier", CurrentValue: acp.SessionConfigValueId(current), Options: acp.SessionConfigSelectOptions{Ungrouped: &options}, Type: "select"}}, true
}

// configurationForSession 复用 model/list 与 sessionConfiguration 组装真实 session 配置。
// app-server 响应缺失 schema-required model 时显式失败，避免向 ACP 客户端静默降级能力。
func (a *Agent) configurationForSession(
	ctx context.Context,
	model string,
	effort *string,
) (*sessionConfiguration, error) {
	if model == "" {
		return nil, errors.New("Codex session response did not include a model")
	}
	models, err := a.client.ListModels(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing Codex models: %w", err)
	}
	currentEffort := ""
	if effort != nil {
		currentEffort = *effort
	}
	configuration, err := newSessionConfiguration(models, model, currentEffort, "agent")
	if err != nil {
		return nil, fmt.Errorf("building session configuration: %w", err)
	}
	return configuration, nil
}

// sessionConfigurationResponse 在 session 锁内生成 ACP mode/config 快照。
func sessionConfigurationResponse(
	state *sessionState,
) (*acp.SessionModeState, []acp.SessionConfigOption) {
	if state == nil {
		return nil, nil
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.configuration == nil {
		return nil, nil
	}
	modes := state.configuration.ModeState()
	return &modes, state.configuration.Options()
}

// turnParamsForSession 在 session 锁内快照 cwd 与当前配置，供普通 Prompt 和 steering fallback 共用。
// 返回值不持有 session 内部指针，后续配置更新不会改写已发送的 turn/start。
func turnParamsForSession(
	state *sessionState,
	input []protocol.InputElement,
	messageID *string,
) protocol.TurnStartParams {
	state.mu.Lock()
	defer state.mu.Unlock()
	cwd := state.cwd
	params := protocol.TurnStartParams{
		ThreadID:            state.id,
		Input:               input,
		ClientUserMessageID: messageID,
		Cwd:                 &cwd,
	}
	if state.configuration == nil {
		return params
	}
	selection := state.configuration.Selection()
	mode := state.configuration.ModeDefinition()
	if selection.ModelRequested {
		params.Model = &selection.Model
	}
	params.ServiceTier = selection.ServiceTier
	if selection.EffortRequested && selection.Effort != "" {
		params.Effort = &selection.Effort
	}
	params.ApprovalPolicy = &mode.ApprovalPolicy
	sandbox := sandboxPolicyWithAdditionalDirectories(
		mode.SandboxPolicy,
		state.additionalDirectories,
	)
	params.SandboxPolicy = &sandbox
	return params
}

// SetSessionConfigOption 校验 SDK union，并把选择写入精确当前 session 的现有配置组件。
func (a *Agent) SetSessionConfigOption(
	_ context.Context,
	request acp.SetSessionConfigOptionRequest,
) (acp.SetSessionConfigOptionResponse, error) {
	if request.ValueId == nil || request.Boolean != nil {
		return acp.SetSessionConfigOptionResponse{}, acp.NewInvalidParams(map[string]any{
			"error": "session config requires a string value",
		})
	}
	value := request.ValueId
	var options []acp.SessionConfigOption
	err := a.sessions.withCurrent(string(value.SessionId), func(state *sessionState) error {
		if state.configuration == nil {
			return acp.NewInvalidParams(map[string]any{
				"error": "session configuration is unavailable",
			})
		}
		if selectErr := state.configuration.Select(value.ConfigId, string(value.Value)); selectErr != nil {
			return acp.NewInvalidParams(map[string]any{"error": selectErr.Error()})
		}
		options = state.configuration.Options()
		return nil
	})
	if errors.Is(err, ErrSessionNotFound) {
		return acp.SetSessionConfigOptionResponse{}, codexSessionNotFoundError(string(value.SessionId))
	}
	if err != nil {
		return acp.SetSessionConfigOptionResponse{}, err
	}
	return acp.SetSessionConfigOptionResponse{ConfigOptions: options}, nil
}

// SetSessionMode 复用 mode 配置选择；未知模式绝不回退到更宽松权限。
func (a *Agent) SetSessionMode(
	_ context.Context,
	request acp.SetSessionModeRequest,
) (acp.SetSessionModeResponse, error) {
	err := a.sessions.withCurrent(string(request.SessionId), func(state *sessionState) error {
		if state.configuration == nil {
			return acp.NewInvalidParams(map[string]any{
				"error": "session configuration is unavailable",
			})
		}
		if selectErr := state.configuration.Select(modeConfigID, string(request.ModeId)); selectErr != nil {
			return acp.NewInvalidParams(map[string]any{"error": selectErr.Error()})
		}
		return nil
	})
	if errors.Is(err, ErrSessionNotFound) {
		return acp.SetSessionModeResponse{}, codexSessionNotFoundError(string(request.SessionId))
	}
	if err != nil {
		return acp.SetSessionModeResponse{}, err
	}
	return acp.SetSessionModeResponse{}, nil
}

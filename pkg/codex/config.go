package codex

import (
	"fmt"
	"strings"

	"github.com/JieWaZi/acp-go/pkg/codex/protocol"

	acp "github.com/coder/acp-go-sdk"
)

const (
	// modeConfigID 是 codex-acp 对外公开的模式配置标识。
	modeConfigID acp.SessionConfigId = "mode"
	// modelConfigID 是 codex-acp 对外公开的模型配置标识。
	modelConfigID acp.SessionConfigId = "model"
	// reasoningEffortConfigID 是 codex-acp 对外公开的推理强度配置标识。
	reasoningEffortConfigID acp.SessionConfigId = "reasoning_effort"
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
}

// sessionConfiguration 只维护单个 session 的配置选择，不持有 runtime 生命周期状态。
type sessionConfiguration struct {
	// models 是 app-server 返回的模型目录快照。
	models []protocol.ModelListResponseDatum
	// current 是当前模型、effort 与模式选择。
	current modelSelection
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
	// app-server 可省略 reasoningEffort，此时按模型目录的默认值补齐。
	modelFound := false
	for _, model := range models {
		if model.ID != currentModel {
			continue
		}
		modelFound = true
		if currentEffort == "" {
			currentEffort = model.DefaultReasoningEffort
		}
		break
	}
	// 未编目的自定义模型保留原 ID，并在缺失 effort 时回退到 medium。
	if !modelFound && currentEffort == "" {
		currentEffort = "medium"
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
		options = append(options, c.effortOption(model))
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
	default:
		return fmt.Errorf("unknown session config %q", id)
	}
}

// selectModel 应用模型选择，并保留有效 effort；无效时使用模型默认值。
func (c *sessionConfiguration) selectModel(id string) error {
	model, ok := c.findModel(id)
	if !ok {
		if id == c.current.Model {
			return nil
		}
		return fmt.Errorf("unknown model %q", id)
	}
	effort := c.current.Effort
	if _, ok := findSupportedEffort(model.SupportedReasoningEfforts, effort); !ok {
		effort = model.DefaultReasoningEffort
	}
	c.current.Model = id
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
		options = append(options, acp.SessionConfigSelectOption{
			Value: acp.SessionConfigValueId(model.ID), Name: model.DisplayName, Description: &description,
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
	return acp.SessionConfigOption{Select: &acp.SessionConfigOptionSelect{
		Id: reasoningEffortConfigID, Name: "Reasoning effort", Description: &description,
		Category: &category, CurrentValue: acp.SessionConfigValueId(c.current.Effort),
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

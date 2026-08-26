// Package protocol 定义 Claude Code stream-json/control V1 所需的窄类型协议边界。
// 未知字段和未纳入消息保留原始 JSON，避免 Claude CLI 增量字段导致整个会话解码失败。
package protocol

import (
	"encoding/json"
	"errors"
	"fmt"
)

// ErrInvalidMessage 表示 Claude wire 消息缺少必需的顶层判别字段或结构无效。
var ErrInvalidMessage = errors.New("invalid Claude protocol message")

// 顶层消息类型常量集中维护 stream-json 消息与 control envelope 的判别值。
const (
	TypeSystem               = "system"
	TypeAssistant            = "assistant"
	TypeUser                 = "user"
	TypeResult               = "result"
	TypeStreamEvent          = "stream_event"
	TypeToolProgress         = "tool_progress"
	TypeControlRequest       = "control_request"
	TypeControlResponse      = "control_response"
	TypeControlCancelRequest = "control_cancel_request"
	TypeKeepAlive            = "keep_alive"
)

// Control subtype 常量只覆盖 Claude ACP V1 实际调用或处理的能力。
const (
	ControlInitialize           = "initialize"
	ControlCanUseTool           = "can_use_tool"
	ControlInterrupt            = "interrupt"
	ControlSetPermissionMode    = "set_permission_mode"
	ControlSetModel             = "set_model"
	ControlSetMaxThinkingTokens = "set_max_thinking_tokens"
	ControlApplyFlagSettings    = "apply_flag_settings"
)

// Message 是解码后的封闭顶层消息集合。
type Message interface {
	// MessageType 返回 wire 顶层 type。
	MessageType() string
	// RawJSON 返回未丢字段的原始消息副本。
	RawJSON() json.RawMessage
	// isMessage 防止包外伪造未经过判别解码的消息。
	isMessage()
}

// messageBase 保存所有已解码消息共享的顶层元数据。
type messageBase struct {
	// typeName 保存顶层消息判别值。
	typeName string
	// raw 保存未丢字段的完整 wire 消息。
	raw json.RawMessage
}

// MessageType 返回消息顶层 type。
func (m messageBase) MessageType() string { return m.typeName }

// RawJSON 返回原始消息的独立副本，调用方可以安全保留。
func (m messageBase) RawJSON() json.RawMessage { return append(json.RawMessage(nil), m.raw...) }

// isMessage 封闭消息变体。
func (m messageBase) isMessage() {}

// ContentBlock 是 V1 mapper 消费的 Claude/Anthropic content union 窄视图。
// Raw 字段保留工具和未来 block 的完整载荷。
type ContentBlock struct {
	// Type 是 content union 的判别值。
	Type string `json:"type"`
	// Text 保存文本或文本增量。
	Text string `json:"text,omitempty"`
	// Thinking 保存思考正文或增量。
	Thinking string `json:"thinking,omitempty"`
	// ID 是工具调用或内容块的稳定标识。
	ID string `json:"id,omitempty"`
	// Name 是工具名称。
	Name string `json:"name,omitempty"`
	// Input 保留工具输入的原始 JSON。
	Input json.RawMessage `json:"input,omitempty"`
	// PartialJSON 是工具输入流的 JSON 字符串增量。
	PartialJSON string `json:"partial_json,omitempty"`
	// ToolUseID 关联工具结果与原始调用。
	ToolUseID string `json:"tool_use_id,omitempty"`
	// Content 保留工具结果等开放内容。
	Content json.RawMessage `json:"content,omitempty"`
	// IsError 表示工具结果是否失败。
	IsError bool `json:"is_error,omitempty"`
	// Source 保存图片来源。
	Source *ImageSource `json:"source,omitempty"`
	// Raw 保存完整 content union 载荷。
	Raw json.RawMessage `json:"-"`
}

// UnmarshalJSON 解码已知 content 字段并保留原始 union 载荷。
func (b *ContentBlock) UnmarshalJSON(data []byte) error {
	type alias ContentBlock
	var decoded alias
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*b = ContentBlock(decoded)
	b.Raw = append(json.RawMessage(nil), data...)
	return nil
}

// ImageSource 表示 Claude image block 的 base64 或 URL 来源。
type ImageSource struct {
	// Type 区分 base64 与 URL 图片。
	Type string `json:"type"`
	// Data 保存 base64 图片内容。
	Data string `json:"data,omitempty"`
	// MediaType 保存图片 MIME 类型。
	MediaType string `json:"media_type,omitempty"`
	// URL 保存远程图片地址。
	URL string `json:"url,omitempty"`
}

// AnthropicMessage 是 assistant/user wrapper 中的消息主体窄视图。
type AnthropicMessage struct {
	// ID 是消息分组使用的稳定标识。
	ID string `json:"id,omitempty"`
	// Role 区分 user、assistant 与 system 消息。
	Role string `json:"role"`
	// Model 是生成当前消息的模型。
	Model string `json:"model,omitempty"`
	// Content 保存有序内容块。
	Content []ContentBlock `json:"content"`
	// StopReason 保存可选的模型停止原因。
	StopReason *string `json:"stop_reason,omitempty"`
	// Usage 是当前 assistant 消息的累计上下文用量快照。
	Usage *Usage `json:"usage,omitempty"`
}

// SystemInitMessage 是 CLI 启动后发出的 system/init 消息。
type SystemInitMessage struct {
	// messageBase 提供顶层类型与原始 JSON。
	messageBase
	// Subtype 固定为 init。
	Subtype string `json:"subtype"`
	// SessionID 是 Claude 持久会话标识。
	SessionID string `json:"session_id"`
	// UUID 是当前 wire 消息标识。
	UUID string `json:"uuid"`
	// ClaudeCodeVersion 是子进程报告的 CLI 版本。
	ClaudeCodeVersion string `json:"claude_code_version"`
	// CWD 是 CLI 实际工作目录。
	CWD string `json:"cwd"`
	// Model 是当前生效模型。
	Model string `json:"model"`
	// PermissionMode 是当前工具权限模式。
	PermissionMode string `json:"permissionMode"`
	// Tools 是当前会话可用工具名。
	Tools []string `json:"tools"`
	// TerminalSlashCommands 是只适合 CLI 终端交互、不应出现在 ACP 菜单中的命令名。
	TerminalSlashCommands []string `json:"terminal_slash_commands,omitempty"`
	// MCPServers 是 CLI 报告的 MCP 状态。
	MCPServers []MCPServerStatus `json:"mcp_servers"`
	// Capabilities 是 CLI 支持的开放能力标识。
	Capabilities []string `json:"capabilities,omitempty"`
	// FastModeState 是当前快速模式状态。
	FastModeState string `json:"fast_mode_state,omitempty"`
	// FastModeDisabledReason 说明快速模式不可用原因。
	FastModeDisabledReason string `json:"fast_mode_disabled_reason,omitempty"`
}

// MCPServerStatus 是 system/init 中 Claude CLI 报告的 MCP 连接状态。
type MCPServerStatus struct {
	// Name 是 MCP server 名称。
	Name string `json:"name"`
	// Status 是 CLI 报告的连接状态。
	Status string `json:"status"`
}

// SystemMessage 保存 V1 未专门建模的 system subtype。
type SystemMessage struct {
	// messageBase 提供顶层类型与原始 JSON。
	messageBase
	// Subtype 是 system 消息判别值。
	Subtype string `json:"subtype"`
	// SessionID 关联当前会话。
	SessionID string `json:"session_id,omitempty"`
	// UUID 是当前 wire 消息标识。
	UUID string `json:"uuid,omitempty"`
	// Status 保留开放状态值。
	Status json.RawMessage `json:"status,omitempty"`
	// State 是 Session 状态变化后的新状态。
	State string `json:"state,omitempty"`
	// Commands 是 commands_changed 携带的完整权威命令列表。
	Commands []SlashCommand `json:"commands,omitempty"`
	// Data 保存完整 system 载荷供兼容处理。
	Data json.RawMessage `json:"-"`
}

// TaskMessage 合并 Task/Todo plan 所需的四类 system task 消息字段。
type TaskMessage struct {
	// messageBase 提供顶层类型与原始 JSON。
	messageBase
	// Subtype 区分任务创建、进度、更新和通知。
	Subtype string `json:"subtype"`
	// SessionID 关联当前会话。
	SessionID string `json:"session_id"`
	// UUID 是当前 wire 消息标识。
	UUID string `json:"uuid"`
	// TaskID 是任务稳定标识。
	TaskID string `json:"task_id"`
	// ToolUseID 关联创建任务的工具调用。
	ToolUseID string `json:"tool_use_id,omitempty"`
	// Description 是任务说明。
	Description string `json:"description,omitempty"`
	// Status 是任务当前状态。
	Status string `json:"status,omitempty"`
	// Summary 是任务完成摘要。
	Summary string `json:"summary,omitempty"`
	// Patch 保留增量变更字段。
	Patch json.RawMessage `json:"patch,omitempty"`
	// SkipTranscript 表示任务不应显示在主记录中。
	SkipTranscript bool `json:"skip_transcript,omitempty"`
}

// AssistantMessage 是完成或组装后的 assistant 消息。
type AssistantMessage struct {
	// messageBase 提供顶层类型与原始 JSON。
	messageBase
	// Message 保存 assistant 消息主体。
	Message AnthropicMessage `json:"message"`
	// ParentToolUseID 标识消息所属的父工具调用。
	ParentToolUseID *string `json:"parent_tool_use_id"`
	// Error 保存可选的消息级错误类别。
	Error string `json:"error,omitempty"`
	// UUID 是当前 wire 消息标识。
	UUID string `json:"uuid"`
	// SessionID 关联当前会话。
	SessionID string `json:"session_id"`
}

// UserMessage 是输入 echo 或工具结果消息。
type UserMessage struct {
	// messageBase 提供顶层类型与原始 JSON。
	messageBase
	// Message 保存用户消息或工具结果主体。
	Message AnthropicMessage `json:"message"`
	// ParentToolUseID 标识消息所属的父工具调用。
	ParentToolUseID *string `json:"parent_tool_use_id"`
	// ToolUseResult 保留结构化工具输出。
	ToolUseResult json.RawMessage `json:"tool_use_result,omitempty"`
	// Priority 控制 steering 输入调度优先级。
	Priority string `json:"priority,omitempty"`
	// UUID 用于匹配 prompt echo。
	UUID string `json:"uuid,omitempty"`
	// SessionID 关联当前会话。
	SessionID string `json:"session_id,omitempty"`
}

// ResultMessage 是一个 Claude query turn 的终止结果。
type ResultMessage struct {
	// messageBase 提供顶层类型与原始 JSON。
	messageBase
	// Subtype 区分成功与各类执行错误。
	Subtype string `json:"subtype"`
	// SessionID 关联当前会话。
	SessionID string `json:"session_id"`
	// UUID 是当前 wire 消息标识。
	UUID string `json:"uuid"`
	// IsError 表示 turn 是否失败。
	IsError bool `json:"is_error"`
	// StopReason 是模型或运行时停止原因。
	StopReason *string `json:"stop_reason"`
	// Result 保存成功结果文本。
	Result string `json:"result,omitempty"`
	// Errors 保存执行错误列表。
	Errors []string `json:"errors,omitempty"`
	// Usage 是主循环本轮用量。
	Usage Usage `json:"usage"`
	// ModelUsage 是按模型累计的用量。
	ModelUsage map[string]ModelUsage `json:"modelUsage,omitempty"`
	// PermissionDenials 保存本轮权限拒绝记录。
	PermissionDenials []PermissionDenial `json:"permission_denials,omitempty"`
}

// Usage 是 result.usage 中 V1 使用的 token 统计子集。
type Usage struct {
	// InputTokens 是普通输入 token 数。
	InputTokens int64 `json:"input_tokens"`
	// OutputTokens 是输出 token 数。
	OutputTokens int64 `json:"output_tokens"`
	// CacheCreationInputTokens 是写入缓存的输入 token 数。
	CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
	// CacheReadInputTokens 是从缓存读取的输入 token 数。
	CacheReadInputTokens int64 `json:"cache_read_input_tokens"`
}

// UsageDelta 是 message_delta 中允许省略累计字段的上下文用量增量。
type UsageDelta struct {
	// InputTokens 是可选的累计普通输入 token 数。
	InputTokens *int64 `json:"input_tokens,omitempty"`
	// OutputTokens 是 Anthropic message_delta 保证提供的累计输出 token 数。
	OutputTokens int64 `json:"output_tokens"`
	// CacheCreationInputTokens 是可选的累计缓存写入 token 数。
	CacheCreationInputTokens *int64 `json:"cache_creation_input_tokens,omitempty"`
	// CacheReadInputTokens 是可选的累计缓存读取 token 数。
	CacheReadInputTokens *int64 `json:"cache_read_input_tokens,omitempty"`
}

// ModelUsage 是 result.modelUsage 中单模型的累计用量。
type ModelUsage struct {
	// InputTokens 是该模型普通输入 token 数。
	InputTokens int64 `json:"inputTokens"`
	// OutputTokens 是该模型输出 token 数。
	OutputTokens int64 `json:"outputTokens"`
	// CacheReadInputTokens 是该模型缓存读取 token 数。
	CacheReadInputTokens int64 `json:"cacheReadInputTokens"`
	// CacheCreationInputTokens 是该模型缓存写入 token 数。
	CacheCreationInputTokens int64 `json:"cacheCreationInputTokens"`
	// ContextWindow 是该模型上下文窗口。
	ContextWindow int64 `json:"contextWindow"`
	// MaxOutputTokens 是该模型最大输出 token 数。
	MaxOutputTokens int64 `json:"maxOutputTokens"`
}

// PermissionDenial 是 result 中权限拒绝的权威记录。
type PermissionDenial struct {
	// ToolName 是被拒绝的工具名。
	ToolName string `json:"tool_name"`
	// ToolUseID 是被拒绝的工具调用标识。
	ToolUseID string `json:"tool_use_id"`
	// ToolInput 保留被拒绝的原始输入。
	ToolInput json.RawMessage `json:"tool_input"`
}

// StreamEventMessage 包装 Anthropic streaming event。
type StreamEventMessage struct {
	// messageBase 提供顶层类型与原始 JSON。
	messageBase
	// Event 保存 Anthropic streaming event。
	Event StreamEvent `json:"event"`
	// ParentToolUseID 标识事件所属的父工具调用。
	ParentToolUseID *string `json:"parent_tool_use_id"`
	// UUID 是当前 wire 消息标识。
	UUID string `json:"uuid"`
	// SessionID 关联当前会话。
	SessionID string `json:"session_id"`
}

// StreamEvent 是 V1 内容去重与 tool streaming 所需的 Anthropic event 窄视图。
type StreamEvent struct {
	// Type 是 streaming event 判别值。
	Type string `json:"type"`
	// Index 是内容块位置。
	Index int `json:"index,omitempty"`
	// Message 保存 message_start 的消息骨架。
	Message *AnthropicMessage `json:"message,omitempty"`
	// ContentBlock 保存 content_block_start 的初始块。
	ContentBlock *ContentBlock `json:"content_block,omitempty"`
	// Delta 保存内容增量。
	Delta *ContentBlock `json:"delta,omitempty"`
	// Usage 保存 message_delta 的可空累计用量字段。
	Usage *UsageDelta `json:"usage,omitempty"`
}

// ToolProgressMessage 是 Claude 独立 tool progress 消息。
type ToolProgressMessage struct {
	// messageBase 提供顶层类型与原始 JSON。
	messageBase
	// ToolUseID 关联已发布工具调用。
	ToolUseID string `json:"tool_use_id"`
	// ToolName 是正在执行的工具名。
	ToolName string `json:"tool_name"`
	// ParentToolUseID 标识父工具调用。
	ParentToolUseID *string `json:"parent_tool_use_id"`
	// ElapsedTimeSeconds 是已执行秒数。
	ElapsedTimeSeconds float64 `json:"elapsed_time_seconds"`
	// UUID 是当前 wire 消息标识。
	UUID string `json:"uuid"`
	// SessionID 关联当前会话。
	SessionID string `json:"session_id"`
}

// ControlRequestMessage 是 CLI 发给宿主，或宿主发给 CLI 的 control 请求 envelope。
type ControlRequestMessage struct {
	// messageBase 提供顶层类型与原始 JSON。
	messageBase
	// RequestID 关联后续 control response。
	RequestID string `json:"request_id"`
	// Request 保留 subtype 对应的开放请求载荷。
	Request json.RawMessage `json:"request"`
}

// ControlSubtype 返回嵌套 request 的 subtype；无效 request 返回空字符串。
func (m *ControlRequestMessage) ControlSubtype() string {
	var header struct {
		// Subtype 是嵌套 control request 判别值。
		Subtype string `json:"subtype"`
	}
	if json.Unmarshal(m.Request, &header) != nil {
		return ""
	}
	return header.Subtype
}

// DecodeRequest 把 control request payload 解码到调用方提供的窄类型。
func (m *ControlRequestMessage) DecodeRequest(target any) error {
	if target == nil {
		return fmt.Errorf("%w: nil control request target", ErrInvalidMessage)
	}
	return json.Unmarshal(m.Request, target)
}

// ControlResponseMessage 包装 CLI control response 的内层 response。
type ControlResponseMessage struct {
	// messageBase 提供顶层类型与原始 JSON。
	messageBase
	// Response 是嵌套的成功或失败结果。
	Response ControlResponse `json:"response"`
}

// ControlResponse 使用 subtype 与 request_id 关联请求，Response 保留各 subtype 的结果。
type ControlResponse struct {
	// Subtype 区分 success 与 error。
	Subtype string `json:"subtype"`
	// RequestID 关联原始 control request。
	RequestID string `json:"request_id"`
	// Response 保留成功响应载荷。
	Response json.RawMessage `json:"response,omitempty"`
	// Error 保存失败文本。
	Error string `json:"error,omitempty"`
}

// ControlCancelRequestMessage 表示 control request 的显式取消。
type ControlCancelRequestMessage struct {
	// messageBase 提供顶层类型与原始 JSON。
	messageBase
	// RequestID 指定要取消的 control request。
	RequestID string `json:"request_id"`
}

// KeepAliveMessage 表示无需业务处理的保活帧。
type KeepAliveMessage struct {
	// messageBase 提供顶层类型与原始 JSON。
	messageBase
}

// UnknownMessage 保存未来新增的顶层消息类型。
type UnknownMessage struct {
	// messageBase 提供未知类型与原始 JSON。
	messageBase
}

// DecodeMessage 先判别顶层 type/subtype，再只解码 V1 需要的窄类型。
func DecodeMessage(data []byte) (Message, error) {
	var header struct {
		// Type 是顶层消息判别值。
		Type string `json:"type"`
		// Subtype 是 system/control 等消息的次级判别值。
		Subtype string `json:"subtype"`
	}
	if err := json.Unmarshal(data, &header); err != nil {
		return nil, fmt.Errorf("%w: decoding header: %v", ErrInvalidMessage, err)
	}
	if header.Type == "" {
		return nil, fmt.Errorf("%w: missing type", ErrInvalidMessage)
	}

	raw := append(json.RawMessage(nil), data...)
	base := messageBase{typeName: header.Type, raw: raw}
	var target Message
	switch header.Type {
	case TypeSystem:
		switch header.Subtype {
		case "init":
			target = &SystemInitMessage{messageBase: base}
		case "task_started", "task_progress", "task_updated", "task_notification":
			target = &TaskMessage{messageBase: base}
		default:
			target = &SystemMessage{messageBase: base, Data: raw}
		}
	case TypeAssistant:
		target = &AssistantMessage{messageBase: base}
	case TypeUser:
		target = &UserMessage{messageBase: base}
	case TypeResult:
		target = &ResultMessage{messageBase: base}
	case TypeStreamEvent:
		target = &StreamEventMessage{messageBase: base}
	case TypeToolProgress:
		target = &ToolProgressMessage{messageBase: base}
	case TypeControlRequest:
		target = &ControlRequestMessage{messageBase: base}
	case TypeControlResponse:
		target = &ControlResponseMessage{messageBase: base}
	case TypeControlCancelRequest:
		target = &ControlCancelRequestMessage{messageBase: base}
	case TypeKeepAlive:
		target = &KeepAliveMessage{messageBase: base}
	default:
		return &UnknownMessage{messageBase: base}, nil
	}
	if err := json.Unmarshal(data, target); err != nil {
		return nil, fmt.Errorf("%w: decoding %s: %v", ErrInvalidMessage, header.Type, err)
	}
	return target, nil
}

// InitializeControlRequest 是宿主启动 Session 后发送的最小 initialize 请求。
type InitializeControlRequest struct {
	// Subtype 固定为 initialize。
	Subtype string `json:"subtype"`
}

// InitializeControlResponse 保存 Session 配置选择需要的初始化结果。
type InitializeControlResponse struct {
	// Commands 是初始化时 Claude SDK 报告的完整 Slash Command 列表。
	Commands []SlashCommand `json:"commands"`
	// Models 是当前账号与设置允许选择的模型。
	Models []ModelInfo `json:"models"`
	// OutputStyle 是当前输出风格。
	OutputStyle string `json:"output_style,omitempty"`
	// AvailableOutputStyles 是可选择的输出风格。
	AvailableOutputStyles []string `json:"available_output_styles,omitempty"`
	// FastModeState 是当前快速模式状态。
	FastModeState string `json:"fast_mode_state,omitempty"`
	// FastModeDisabledReason 说明快速模式不可用原因。
	FastModeDisabledReason string `json:"fast_mode_disabled_reason,omitempty"`
}

// SlashCommand 描述 Claude SDK 公开的一条可调用 Skill 或内置命令。
type SlashCommand struct {
	// Name 是不带前导斜杠的命令名。
	Name string `json:"name"`
	// Description 是命令的人类可读说明。
	Description string `json:"description"`
	// ArgumentHint 是参数占位提示；兼容对字符串数组的防御性处理。
	ArgumentHint any `json:"argumentHint,omitempty"`
	// Aliases 是解析到同一命令的备用名称。
	Aliases []string `json:"aliases,omitempty"`
}

// ModelInfo 描述 CLI 当前允许选择的一个模型。
type ModelInfo struct {
	// Value 是设置模型时使用的标识。
	Value string `json:"value"`
	// ResolvedModel 是别名解析后的模型标识。
	ResolvedModel string `json:"resolvedModel,omitempty"`
	// DisplayName 是客户端展示名称。
	DisplayName string `json:"displayName"`
	// Description 是模型能力说明。
	Description string `json:"description"`
	// SupportsEffort 表示模型是否支持 effort 配置。
	SupportsEffort bool `json:"supportsEffort,omitempty"`
	// SupportedEffortLevels 是模型允许的 effort 值。
	SupportedEffortLevels []string `json:"supportedEffortLevels,omitempty"`
	// SupportsFastMode 表示模型是否支持快速模式。
	SupportsFastMode bool `json:"supportsFastMode,omitempty"`
	// SupportsAutoMode 表示模型是否支持自动权限模式。
	SupportsAutoMode bool `json:"supportsAutoMode,omitempty"`
}

// InterruptControlRequest 中 cancel_queued 要求支持该 capability 的 CLI 同时清空排队消息。
type InterruptControlRequest struct {
	// Subtype 固定为 interrupt。
	Subtype string `json:"subtype"`
	// CancelQueued 要求同时清除尚未执行的消息。
	CancelQueued bool `json:"cancel_queued,omitempty"`
}

// SetPermissionModeControlRequest 更新后续工具权限模式。
type SetPermissionModeControlRequest struct {
	// Subtype 固定为 set_permission_mode。
	Subtype string `json:"subtype"`
	// Mode 是目标权限模式。
	Mode string `json:"mode"`
}

// SetModelControlRequest 更新后续 turn 使用的模型；nil 表示恢复默认。
type SetModelControlRequest struct {
	// Subtype 固定为 set_model。
	Subtype string `json:"subtype"`
	// Model 是目标模型，nil 表示恢复默认。
	Model *string `json:"model,omitempty"`
}

// SetMaxThinkingTokensControlRequest 更新 thinking token 与展示方式。
type SetMaxThinkingTokensControlRequest struct {
	// Subtype 固定为 set_max_thinking_tokens。
	Subtype string `json:"subtype"`
	// MaxThinkingTokens 是新的 thinking token 上限。
	MaxThinkingTokens *int64 `json:"max_thinking_tokens,omitempty"`
	// ThinkingDisplay 是新的思考展示模式。
	ThinkingDisplay *string `json:"thinking_display,omitempty"`
}

// ApplyFlagSettingsControlRequest 合并 effort/fast 等运行时 flag settings。
type ApplyFlagSettingsControlRequest struct {
	// Subtype 固定为 apply_flag_settings。
	Subtype string `json:"subtype"`
	// Settings 是要合并的 flag setting。
	Settings map[string]any `json:"settings"`
}

// CanUseToolControlRequest 是 CLI 发给宿主的工具权限询问。
type CanUseToolControlRequest struct {
	// Subtype 固定为 can_use_tool。
	Subtype string `json:"subtype"`
	// ToolName 是申请执行的工具名。
	ToolName string `json:"tool_name"`
	// Input 保存工具输入。
	Input json.RawMessage `json:"input"`
	// PermissionSuggestions 保存 CLI 建议的持久权限更新。
	PermissionSuggestions json.RawMessage `json:"permission_suggestions,omitempty"`
	// BlockedPath 是触发权限检查的路径。
	BlockedPath string `json:"blocked_path,omitempty"`
	// DecisionReason 是面向用户的申请原因。
	DecisionReason string `json:"decision_reason,omitempty"`
	// DecisionReasonType 是申请原因类别。
	DecisionReasonType string `json:"decision_reason_type,omitempty"`
	// SuppressAlwaysAllowRule 禁止展示持久允许选项。
	SuppressAlwaysAllowRule bool `json:"suppress_always_allow_rule,omitempty"`
	// Title 是权限卡片标题。
	Title string `json:"title,omitempty"`
	// DisplayName 是工具展示名。
	DisplayName string `json:"display_name,omitempty"`
	// ToolUseID 是权限申请关联的工具调用标识。
	ToolUseID string `json:"tool_use_id"`
	// Description 是权限申请补充说明。
	Description string `json:"description,omitempty"`
}

// PermissionResult 是发回 CLI 的 can_use_tool 决定。
type PermissionResult struct {
	// Behavior 是 allow 或 deny。
	Behavior string `json:"behavior"`
	// Message 是拒绝时返回给模型的说明。
	Message string `json:"message,omitempty"`
	// Interrupt 表示拒绝后是否同时中断 turn。
	Interrupt bool `json:"interrupt,omitempty"`
	// UpdatedInput 是允许前修正后的工具输入。
	UpdatedInput json.RawMessage `json:"updatedInput,omitempty"`
	// UpdatedPermissions 是客户端确认的权限更新。
	UpdatedPermissions json.RawMessage `json:"updatedPermissions,omitempty"`
	// ToolUseID 回显关联工具调用标识。
	ToolUseID string `json:"toolUseID,omitempty"`
}

// OutgoingControlRequest 是宿主发给 CLI 的 control_request wire envelope。
type OutgoingControlRequest struct {
	// Type 固定为 control_request。
	Type string `json:"type"`
	// RequestID 关联后续响应。
	RequestID string `json:"request_id"`
	// Request 是具体 control payload。
	Request any `json:"request"`
}

// NewControlRequest 创建一个固定顶层 type 的 control request。
func NewControlRequest(requestID string, request any) (OutgoingControlRequest, error) {
	if requestID == "" || request == nil {
		return OutgoingControlRequest{}, fmt.Errorf("%w: control request requires id and payload", ErrInvalidMessage)
	}
	return OutgoingControlRequest{Type: TypeControlRequest, RequestID: requestID, Request: request}, nil
}

// OutgoingControlCancelRequest 是宿主取消尚未完成 control request 的 wire envelope。
type OutgoingControlCancelRequest struct {
	// Type 固定为 control_cancel_request。
	Type string `json:"type"`
	// RequestID 指定待取消请求。
	RequestID string `json:"request_id"`
}

// NewControlCancelRequest 创建 control_cancel_request。
func NewControlCancelRequest(requestID string) (OutgoingControlCancelRequest, error) {
	if requestID == "" {
		return OutgoingControlCancelRequest{}, fmt.Errorf("%w: control cancel requires id", ErrInvalidMessage)
	}
	return OutgoingControlCancelRequest{Type: TypeControlCancelRequest, RequestID: requestID}, nil
}

// OutgoingControlResponse 是宿主响应 CLI control request 的 wire envelope。
type OutgoingControlResponse struct {
	// Type 固定为 control_response。
	Type string `json:"type"`
	// Response 保存成功或失败结果。
	Response ControlResponse `json:"response"`
}

// NewControlSuccess 创建成功 control response，payload 为具体 subtype 的响应对象。
func NewControlSuccess(requestID string, payload any) (OutgoingControlResponse, error) {
	if requestID == "" {
		return OutgoingControlResponse{}, fmt.Errorf("%w: control response requires id", ErrInvalidMessage)
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return OutgoingControlResponse{}, fmt.Errorf("encoding control response: %w", err)
	}
	if payload == nil {
		raw = nil
	}
	return OutgoingControlResponse{
		Type:     TypeControlResponse,
		Response: ControlResponse{Subtype: "success", RequestID: requestID, Response: raw},
	}, nil
}

// NewControlError 创建失败 control response；错误文本不能为空。
func NewControlError(requestID, message string) (OutgoingControlResponse, error) {
	if requestID == "" || message == "" {
		return OutgoingControlResponse{}, fmt.Errorf("%w: control error requires id and message", ErrInvalidMessage)
	}
	return OutgoingControlResponse{
		Type:     TypeControlResponse,
		Response: ControlResponse{Subtype: "error", RequestID: requestID, Error: message},
	}, nil
}

// UserInputMessage 是写给 Claude CLI 的 streaming user 消息。
type UserInputMessage struct {
	// Type 固定为 user。
	Type string `json:"type"`
	// Message 保存用户输入主体。
	Message UserInputBody `json:"message"`
	// ParentToolUseID 对普通输入为 nil。
	ParentToolUseID *string `json:"parent_tool_use_id"`
	// SessionID 关联目标会话。
	SessionID string `json:"session_id"`
	// UUID 关联 echo 与 turn waiter。
	UUID string `json:"uuid"`
	// Priority 控制 steering 输入优先级。
	Priority string `json:"priority,omitempty"`
	// Origin 声明输入来源。
	Origin UserInputOrigin `json:"origin"`
}

// UserInputBody 是 Claude user message 主体。
type UserInputBody struct {
	// Role 固定为 user。
	Role string `json:"role"`
	// Content 保存有序输入内容块。
	Content json.RawMessage `json:"content"`
}

// UserInputOrigin 明确声明 ACP 输入来自真实用户。
type UserInputOrigin struct {
	// Kind 表示输入来源类别。
	Kind string `json:"kind"`
}

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
	TypeConversationReset    = "conversation_reset"
	TypeAssistant            = "assistant"
	TypeUser                 = "user"
	TypeResult               = "result"
	TypeStreamEvent          = "stream_event"
	TypeToolProgress         = "tool_progress"
	TypeRateLimit            = "rate_limit_event"
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

// UnmarshalJSON accepts the native shorthand text or an Anthropic block array.
func (m *AnthropicMessage) UnmarshalJSON(data []byte) error {
	type fields AnthropicMessage
	wire := struct {
		*fields
		Body json.RawMessage `json:"content"`
	}{fields: (*fields)(m)}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	if len(wire.Body) == 0 {
		return nil
	}
	var text string
	if wire.Body[0] == '"' && json.Unmarshal(wire.Body, &text) == nil {
		m.Content = []ContentBlock{{Type: "text", Text: text}}
		return nil
	}
	return json.Unmarshal(wire.Body, &m.Content)
}

// SystemInitMessage is the native system/init message.
type SystemInitMessage struct {
	// Effort is the applied effort when the producer publishes it, including explicit null.
	Effort json.RawMessage `json:"effort,omitempty"`
	// messageBase preserves the top-level discriminator and raw JSON.
	messageBase
	// Subtype is init.
	Subtype string `json:"subtype"`
	// SessionID identifies the persisted Claude session.
	SessionID string `json:"session_id"`
	// UUID identifies the native event.
	UUID string `json:"uuid"`
	// ClaudeCodeVersion is the CLI version reported by the process.
	ClaudeCodeVersion string `json:"claude_code_version"`
	// CWD is the actual native working directory.
	CWD string `json:"cwd"`
	// Model is the model actually in use.
	Model string `json:"model"`
	// PermissionMode is the current native tool permission mode.
	PermissionMode string `json:"permissionMode"`
	// Tools lists the tools available to this session.
	Tools []string `json:"tools"`
	// TerminalSlashCommands identifies commands available only in the native terminal.
	TerminalSlashCommands []string `json:"terminal_slash_commands,omitempty"`
	// MCPServers contains native MCP connection statuses.
	MCPServers []MCPServerStatus `json:"mcp_servers"`
	// Capabilities lists native capability identifiers.
	Capabilities []string `json:"capabilities,omitempty"`
	// FastModeState reports the native Fast service state.
	FastModeState string `json:"fast_mode_state,omitempty"`
	// FastModeDisabledReason explains why the Fast service is unavailable.
	FastModeDisabledReason string `json:"fast_mode_disabled_reason,omitempty"`
}

// MCPServerStatus 是 system/init 中 Claude CLI 报告的 MCP 连接状态。
type MCPServerStatus struct {
	// Name 是 MCP server 名称。
	Name string `json:"name"`
	// Status 是 CLI 报告的连接状态。
	Status string `json:"status"`
	// Source is the native scope or plugin origin of this MCP definition.
	Source string `json:"source,omitempty"`
}

// ConversationResetMessage identifies a fresh conversation in the native session stream.
type ConversationResetMessage struct {
	// messageBase preserves the top-level discriminator and raw JSON.
	messageBase
	// SessionID identifies the native session carrying the reset.
	SessionID string `json:"session_id"`
	// UUID identifies the reset event.
	UUID string `json:"uuid"`
	// NewConversationID identifies the new conversation within that session.
	NewConversationID string `json:"new_conversation_id"`
}

// SystemMessage carries native system events without dedicated message types.
type SystemMessage struct {
	// RetractedMessages identifies frames removed by native refusal fallback.
	RetractedMessages []string `json:"retracted_message_uuids,omitempty"`
	// FallbackScope distinguishes main-session fallback from sidechain fallback.
	FallbackScope string `json:"scope,omitempty"`
	// TurnIdentity carries input UUID evidence on native thinking progress.
	TurnIdentity
	// BackgroundTasks replaces the prior native snapshot when present.
	BackgroundTasks *[]BackgroundTask `json:"tasks,omitempty"`
	// CompactResult is an explicit success or failure from a compaction status frame.
	CompactResult string `json:"compact_result,omitempty"`
	// RetryAttempt counts this attempt of a native API retry, when reported.
	RetryAttempt *int `json:"attempt,omitempty"`
	// MaxRetries is the CLI's stated retry limit.
	MaxRetries *int `json:"max_retries,omitempty"`
	// RetryDelayMS is the native delay before the next attempt.
	RetryDelayMS *int64 `json:"retry_delay_ms,omitempty"`
	// RetryHTTPStatus is absent when no HTTP response was observed.
	RetryHTTPStatus *int `json:"error_status,omitempty"`
	// NoResponse describes an attempt that did not receive response headers.
	NoResponse *RetryNoResponse `json:"no_response,omitempty"`
	// CompactMetadata is the native boundary. Its counts cover transcript messages only.
	CompactMetadata *CompactMetadata `json:"compact_metadata,omitempty"`
	// messageBase preserves the top-level discriminator and raw JSON.
	messageBase
	// Subtype discriminates the native system event.
	Subtype string `json:"subtype"`
	// SessionID identifies the current native session.
	SessionID string `json:"session_id,omitempty"`
	// UUID identifies the native event.
	UUID string `json:"uuid,omitempty"`
	// Status preserves the provider-specific state value.
	Status json.RawMessage `json:"status,omitempty"`
	// State reports the session lifecycle state.
	State string `json:"state,omitempty"`
	// Commands contains the complete command list from commands_changed.
	Commands []SlashCommand `json:"commands,omitempty"`
	// Data holds the original event for compatibility.
	Data json.RawMessage `json:"-"`
}

// TaskMessage 合并 Task/Todo plan 所需的四类 system task 消息字段。
type TaskMessage struct {
	// RunID distinguishes resumed runs of the same task.
	RunID string `json:"run_id,omitempty"`
	// ParentTaskID identifies a tracked subagent that launched this task.
	ParentTaskID string `json:"parent_task_id,omitempty"`
	// Ambient marks housekeeping excluded from user-visible activity.
	Ambient bool `json:"ambient,omitempty"`
	// messageBase preserves the top-level discriminator and raw JSON.
	messageBase
	// Subtype 区分任务创建、进度、更新和通知。
	Subtype string `json:"subtype"`
	// SessionID identifies the current native session.
	SessionID string `json:"session_id"`
	// UUID identifies the native event.
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
	// Supersedes identifies earlier native frames retracted by a fallback.
	Supersedes []string `json:"supersedes,omitempty"`
	// Aborted reports that the native stream stopped mid-block.
	Aborted bool `json:"aborted,omitempty"`
	// AgentID identifies the native subagent task when one owns this frame.
	AgentID string `json:"agent_id,omitempty"`
	// TurnIdentity carries native input correlation on this frame.
	TurnIdentity
	// messageBase preserves the top-level discriminator and raw JSON.
	messageBase
	// Message 保存 assistant 消息主体。
	Message AnthropicMessage `json:"message"`
	// ParentToolUseID 标识消息所属的父工具调用。
	ParentToolUseID *string `json:"parent_tool_use_id"`
	// Error 保存可选的消息级错误类别。
	Error string `json:"error,omitempty"`
	// UUID identifies the native event.
	UUID string `json:"uuid"`
	// SessionID identifies the current native session.
	SessionID string `json:"session_id"`
}

// UserMessage 是输入 echo 或工具结果消息。
type UserMessage struct {
	// messageBase preserves the top-level discriminator and raw JSON.
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
	// SessionID identifies the current native session.
	SessionID string `json:"session_id,omitempty"`
	// IsReplay identifies the native stream-json acknowledgement of stdin input.
	IsReplay bool `json:"isReplay,omitempty"`
	// Origin identifies the source of the replayed user input when supplied.
	Origin *MessageOrigin `json:"origin,omitempty"`
}

// ResultMessage is the native query invocation outcome.
type ResultMessage struct {
	// TurnIdentity carries native input correlation on this frame.
	TurnIdentity
	// TerminalReason carries the native stop cause independently of the result subtype.
	TerminalReason string `json:"terminal_reason,omitempty"`
	// APIErrorStatus is the HTTP status when the CLI identifies an API error.
	APIErrorStatus *int `json:"api_error_status,omitempty"`
	// LocalCommand identifies a CLI slash command that completed locally.
	LocalCommand string `json:"local_command,omitempty"`
	// ResultIndex orders results within one process, never across resumes.
	ResultIndex *int64 `json:"result_index,omitempty"`
	// TotalCostUSD is the query-pipeline cumulative estimate.
	TotalCostUSD *json.Number `json:"total_cost_usd,omitempty"`
	// FastModeState is the native service observation at completion.
	FastModeState string `json:"fast_mode_state,omitempty"`
	// FastModeDisabledReason is the native account or service restriction.
	FastModeDisabledReason string `json:"fast_mode_disabled_reason,omitempty"`
	// QueuedTurnCount is the count of pending native user sends, when reported.
	QueuedTurnCount *int `json:"queued_turn_count,omitempty"`
	// messageBase preserves the top-level discriminator and raw JSON.
	messageBase
	// Subtype distinguishes native success and execution errors.
	Subtype string `json:"subtype"`
	// SessionID identifies the current native session.
	SessionID string `json:"session_id"`
	// UUID identifies the native event.
	UUID string `json:"uuid"`
	// IsError indicates a native failure.
	IsError bool `json:"is_error"`
	// StopReason reports the model or runtime stop reason.
	StopReason *string `json:"stop_reason"`
	// Result contains the native result text.
	Result string `json:"result,omitempty"`
	// Errors contains native execution diagnostics.
	Errors []string `json:"errors,omitempty"`
	// Usage reports this invocation’s main-loop consumption.
	Usage Usage `json:"usage"`
	// ModelUsage reports cumulative pipeline usage by model.
	ModelUsage map[string]ModelUsage `json:"modelUsage,omitempty"`
	// PermissionDenials contains native permission denials.
	PermissionDenials []PermissionDenial `json:"permission_denials,omitempty"`
}

// Usage 是 result.usage 中 V1 使用的 token 统计子集。
type Usage struct {
	// KnownFields 保存原始 JSON 中明确出现的 token 字段；nil 表示程序内构造。
	KnownFields map[string]bool `json:"-"`
	// InputTokens 是普通输入 token 数。
	InputTokens int64 `json:"input_tokens"`
	// OutputTokens 是输出 token 数。
	OutputTokens int64 `json:"output_tokens"`
	// CacheCreationInputTokens 是写入缓存的输入 token 数。
	CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
	// CacheReadInputTokens 是从缓存读取的输入 token 数。
	CacheReadInputTokens int64 `json:"cache_read_input_tokens"`
}

// UnmarshalJSON 保留字段是否实际出现，显式零与缺失不能混为一谈。
func (u *Usage) UnmarshalJSON(data []byte) error {
	type plain Usage
	var value plain
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	value.KnownFields = make(map[string]bool)
	for _, key := range []string{"input_tokens", "output_tokens", "cache_creation_input_tokens", "cache_read_input_tokens"} {
		if raw, ok := fields[key]; ok && string(raw) != "null" {
			value.KnownFields[key] = true
		}
	}
	*u = Usage(value)
	return nil
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
	// CompleteCounters records presence of the four query-pipeline token components.
	CompleteCounters bool `json:"-"`
	// KnownCounters preserves which individual native token components exist.
	KnownCounters map[string]bool `json:"-"`
	// ThinkingTokens is an optional subset of output and may be partial across versions.
	ThinkingTokens *int64 `json:"thinkingTokens,omitempty"`
	// CostUSD is the native cumulative model estimate.
	CostUSD *json.Number `json:"costUSD,omitempty"`
	// CostBasis identifies the native pricing source when reported.
	CostBasis string `json:"costBasis,omitempty"`
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

// UnmarshalJSON verifies core token presence before a row becomes a counter baseline.
func (m *ModelUsage) UnmarshalJSON(data []byte) error {
	type modelUsageFields ModelUsage
	wire := struct {
		// modelUsageFields decodes the remaining native fields.
		*modelUsageFields
		// Input preserves uncached input presence.
		Input *int64 `json:"inputTokens"`
		// Output preserves output presence.
		Output *int64 `json:"outputTokens"`
		// Read preserves cache-read presence.
		Read *int64 `json:"cacheReadInputTokens"`
		// Write preserves cache-write presence.
		Write *int64 `json:"cacheCreationInputTokens"`
	}{modelUsageFields: (*modelUsageFields)(m)}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	m.CompleteCounters = wire.Input != nil && wire.Output != nil && wire.Read != nil && wire.Write != nil
	m.KnownCounters = make(map[string]bool, 4)
	for name, field := range map[string]struct {
		raw   *int64
		value *int64
	}{
		"input": {wire.Input, &m.InputTokens}, "output": {wire.Output, &m.OutputTokens},
		"read": {wire.Read, &m.CacheReadInputTokens}, "write": {wire.Write, &m.CacheCreationInputTokens},
	} {
		if field.raw != nil {
			*field.value = *field.raw
			m.KnownCounters[name] = true
		}
	}
	return nil
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
	// AgentID identifies the native subagent task when one owns this frame.
	AgentID string `json:"agent_id,omitempty"`
	// TurnIdentity carries native input correlation on this frame.
	TurnIdentity
	// messageBase preserves the top-level discriminator and raw JSON.
	messageBase
	// Event 保存 Anthropic streaming event。
	Event StreamEvent `json:"event"`
	// ParentToolUseID 标识事件所属的父工具调用。
	ParentToolUseID *string `json:"parent_tool_use_id"`
	// UUID identifies the native event.
	UUID string `json:"uuid"`
	// SessionID identifies the current native session.
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
	// TaskID identifies the native task associated with this progress frame.
	TaskID string `json:"task_id,omitempty"`
	// messageBase preserves the top-level discriminator and raw JSON.
	messageBase
	// ToolUseID 关联已发布工具调用。
	ToolUseID string `json:"tool_use_id"`
	// ToolName 是正在执行的工具名。
	ToolName string `json:"tool_name"`
	// ParentToolUseID 标识父工具调用。
	ParentToolUseID *string `json:"parent_tool_use_id"`
	// ElapsedTimeSeconds 是已执行秒数。
	ElapsedTimeSeconds float64 `json:"elapsed_time_seconds"`
	// UUID identifies the native event.
	UUID string `json:"uuid"`
	// SessionID identifies the current native session.
	SessionID string `json:"session_id"`
}

// ControlRequestMessage 是 CLI 发给宿主，或宿主发给 CLI 的 control 请求 envelope。
type ControlRequestMessage struct {
	// messageBase preserves the top-level discriminator and raw JSON.
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
	// messageBase preserves the top-level discriminator and raw JSON.
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
	// messageBase preserves the top-level discriminator and raw JSON.
	messageBase
	// RequestID 指定要取消的 control request。
	RequestID string `json:"request_id"`
}

// KeepAliveMessage 表示无需业务处理的保活帧。
type KeepAliveMessage struct {
	// messageBase preserves the top-level discriminator and raw JSON.
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
	case TypeConversationReset:
		target = &ConversationResetMessage{messageBase: base}
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
	case TypeRateLimit:
		target = &RateLimitMessage{messageBase: base}
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

// InitializeControlRequest configures the native control channel at startup.
type InitializeControlRequest struct {
	// AppendSystemPrompt modifies the native Claude Code preset, preserving explicit empty.
	AppendSystemPrompt *string `json:"appendSystemPrompt,omitempty"`
	// Plugins provides local plugin roots during startup initialize.
	Plugins []LocalPlugin `json:"plugins,omitempty"`
	// Hooks registers startup SDK callbacks by native hook event.
	Hooks map[string][]HookCallbackMatcher `json:"hooks,omitempty"`
	// SystemPromptSnapshot requests fresh rendering on native implementations that honor it.
	SystemPromptSnapshot *bool `json:"systemPromptSnapshot,omitempty"`
	// Subtype is initialize.
	Subtype string `json:"subtype"`
}

// InitializeControlResponse contains startup capabilities and configuration choices.
type InitializeControlResponse struct {
	// HooksApplied confirms that the CLI registered the declared callbacks.
	HooksApplied *bool `json:"hooks_applied,omitempty"`
	// PluginsApplied confirms the declared plugin set when the CLI can verify it.
	PluginsApplied *bool `json:"plugins_applied,omitempty"`
	// Commands contains the slash commands reported at startup.
	Commands []SlashCommand `json:"commands"`
	// Models lists the native model choices.
	Models []ModelInfo `json:"models"`
	// OutputStyle reports the current output style.
	OutputStyle string `json:"output_style,omitempty"`
	// AvailableOutputStyles lists selectable output styles.
	AvailableOutputStyles []string `json:"available_output_styles,omitempty"`
	// FastModeState reports the native Fast service state.
	FastModeState string `json:"fast_mode_state,omitempty"`
	// FastModeDisabledReason explains why the Fast service is unavailable.
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
	// Builtin distinguishes Claude Code commands from identically named skills.
	Builtin bool `json:"builtin,omitempty"`
}

// ModelInfo describes one selectable native model.
type ModelInfo struct {
	// Value is the identifier sent to the native model setter.
	Value string `json:"value"`
	// ResolvedModel is the model ID after native alias resolution.
	ResolvedModel string `json:"resolvedModel,omitempty"`
	// DisplayName is the human-readable model name.
	DisplayName string `json:"displayName"`
	// Description describes the model capabilities.
	Description string `json:"description"`
	// SupportsEffort indicates whether the model accepts effort configuration.
	SupportsEffort bool `json:"supportsEffort,omitempty"`
	// EffortSupportReported distinguishes a false capability from an absent capability.
	EffortSupportReported bool `json:"-"`
	// SupportedEffortLevels lists the allowed effort values.
	SupportedEffortLevels []string `json:"supportedEffortLevels,omitempty"`
	// SupportsFastMode indicates whether the model accepts Fast mode.
	SupportsFastMode bool `json:"supportsFastMode,omitempty"`
	// FastSupportReported distinguishes a false capability from an absent capability.
	FastSupportReported bool `json:"-"`
	// SupportsAutoMode indicates whether the model supports automatic permissions.
	SupportsAutoMode bool `json:"supportsAutoMode,omitempty"`
}

// UnmarshalJSON preserves optional capability presence alongside the typed values.
func (m *ModelInfo) UnmarshalJSON(data []byte) error {
	type modelFields ModelInfo
	wire := struct {
		// modelFields decodes the remaining model fields.
		*modelFields
		// Effort preserves presence of the effort capability.
		Effort *bool `json:"supportsEffort"`
		// Fast preserves presence of the fast capability.
		Fast *bool `json:"supportsFastMode"`
	}{modelFields: (*modelFields)(m)}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	m.EffortSupportReported, m.FastSupportReported = wire.Effort != nil, wire.Fast != nil
	m.SupportsEffort = wire.Effort != nil && *wire.Effort
	m.SupportsFastMode = wire.Fast != nil && *wire.Fast
	return nil
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
	// AgentID identifies the native subagent task when one owns this frame.
	AgentID string `json:"agent_id,omitempty"`
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

// GetUsageControlRequest asks the CLI for structured plan usage without a prompt.
type GetUsageControlRequest struct {
	// Subtype is get_usage.
	Subtype string `json:"subtype"`
	// SkipBehaviors suppresses the local transcript scan.
	SkipBehaviors bool `json:"skip_behaviors"`
}

// AccountUsageResponse selects the plan facts and excludes current-session consumption.
type AccountUsageResponse struct {
	// Available is absent on older or incompatible responses.
	Available *bool `json:"rate_limits_available"`
	// RateLimits preserves names and presence until the adapter classifies each row.
	RateLimits map[string]json.RawMessage `json:"rate_limits"`
}

// QuotaWindow describes one native limit window.
type QuotaWindow struct {
	// Name is supplied for model-scoped windows.
	Name string `json:"display_name"`
	// Utilization is the used percentage, including a reported zero.
	Utilization *float64 `json:"utilization"`
	// ResetAt is an optional ISO 8601 timestamp.
	ResetAt *string `json:"resets_at"`
}

// ExtraUsage is the overage spending observation, separate from plan windows.
type ExtraUsage struct {
	// Enabled reports whether extra usage is enabled.
	Enabled *bool `json:"is_enabled"`
	// Used is the native numeric credits usage; the declaration does not specify a unit.
	Used *json.Number `json:"used_credits"`
	// Limit is the native monthly allowance; the declaration does not specify a unit.
	Limit *json.Number `json:"monthly_limit"`
	// Currency is supplied by Claude when known.
	Currency *string `json:"currency,omitempty"`
	// Utilization is the used percentage of this separate allowance.
	Utilization *float64 `json:"utilization"`
}

// TurnIdentity is stamped only on native frames with observed input correlation.
type TurnIdentity struct {
	// UserMessageUUID is the last native consumed input ID named on this frame.
	UserMessageUUID string `json:"user_message_uuid,omitempty"`
	// UserMessageUUIDs enumerates all consumed inputs known at this frame.
	UserMessageUUIDs []string `json:"user_message_uuids,omitempty"`
	// ResumeReason marks a provider-initiated continuation of a previous invocation.
	ResumeReason string `json:"resume_reason,omitempty"`
	// Origin holds the narrow native provenance discriminator.
	Origin *MessageOrigin `json:"origin,omitempty"`
}

// MessageOrigin identifies the kind of source without exposing its payload.
type MessageOrigin struct {
	// Kind is the native origin discriminator.
	Kind string `json:"kind"`
}

// BackgroundTask is one physical entry in the complete native task snapshot.
type BackgroundTask struct {
	// RunID distinguishes resumed runs of the same task.
	RunID string `json:"run_id,omitempty"`
	// ParentTaskID identifies a tracked subagent that launched this task.
	ParentTaskID string `json:"parent_task_id,omitempty"`
	// Ambient marks housekeeping excluded from user-visible activity.
	Ambient bool `json:"ambient,omitempty"`
	// TaskID identifies the native task, including ambient watcher tasks.
	TaskID string `json:"task_id"`
}

// LocalPlugin describes an actual local Claude plugin directory.
type LocalPlugin struct {
	// Type is local for a plugin launched through an on-disk directory.
	Type string `json:"type"`
	// Path is the directory prepared by the host.
	Path string `json:"path"`
	// SkipMCPDiscovery prevents the plugin from starting unmanaged MCP servers.
	SkipMCPDiscovery bool `json:"skipMcpDiscovery,omitempty"`
}

// MCPStatusRequest reads the current native MCP connection statuses.
type MCPStatusRequest struct {
	// Subtype is mcp_status.
	Subtype string `json:"subtype"`
}

// MCPStatusResponse selects the safe fields from native status.
type MCPStatusResponse struct {
	// Servers are the connections the CLI reported, including failed or pending entries.
	Servers []MCPServerStatus `json:"mcpServers"`
}

// CompactMetadata describes a native compaction boundary.
type CompactMetadata struct {
	// Trigger distinguishes manual requests from automatic compaction.
	Trigger string `json:"trigger"`
}

// RetryNoResponse is the native first-byte timeout observation.
type RetryNoResponse struct {
	// WaitedMS is the time spent waiting for the failed attempt.
	WaitedMS int64 `json:"waited_ms"`
	// RetryWaitMS is the wait limit the CLI selected for its retry.
	RetryWaitMS int64 `json:"retry_wait_ms"`
}

// RateLimitMessage is a live plan-limit observation, not a turn outcome.
type RateLimitMessage struct {
	// messageBase carries the envelope type and bytes.
	messageBase
	// SessionID associates this update with the native session.
	SessionID string `json:"session_id"`
	// Info contains only fields needed to observe policy and explicit errors.
	Info RateLimitInfo `json:"rate_limit_info"`
}

// RateLimitInfo describes an observed plan or overage limit.
type RateLimitInfo struct {
	// Status identifies the native plan state.
	Status string `json:"status"`
	// ResetAt is the native reset time, when supplied.
	ResetAt *int64 `json:"resetsAt,omitempty"`
	// RateLimitType identifies the affected window.
	RateLimitType string `json:"rateLimitType,omitempty"`
	// Utilization is the native used fraction.
	Utilization *float64 `json:"utilization,omitempty"`
	// OverageStatus is separate from the subscription plan.
	OverageStatus string `json:"overageStatus,omitempty"`
	// ErrorCode is set when Claude requires credits.
	ErrorCode string `json:"errorCode,omitempty"`
}

// HookCallbackMatcher selects the native tool names and the host callback.
type HookCallbackMatcher struct {
	// Matcher is Claude's native tool-name expression.
	Matcher string `json:"matcher"`
	// CallbackIDs contains the host-owned callback IDs.
	CallbackIDs []string `json:"hookCallbackIds"`
}

// HookCallbackRequest invokes a callback registered during initialize.
type HookCallbackRequest struct {
	// CallbackID identifies one host callback.
	CallbackID string `json:"callback_id"`
	// Input is the native hook event.
	Input json.RawMessage `json:"input"`
}

// PreToolUseInput selects the native evidence needed for a write decision.
type PreToolUseInput struct {
	// CWD is the native working directory at this tool call.
	CWD string `json:"cwd"`
	// ToolUseID identifies the native tool that can later request approval.
	ToolUseID string `json:"tool_use_id"`
	// Event must be PreToolUse.
	Event string `json:"hook_event_name"`
	// SessionID binds the request to this native generation.
	SessionID string `json:"session_id"`
	// Tool is the native file tool name.
	Tool string `json:"tool_name"`
	// Input contains the native tool arguments.
	Input json.RawMessage `json:"tool_input"`
}

// HookCallbackResult returns a native decision or leaves in-scope work to standard permissions.
type HookCallbackResult struct {
	// Output is present only when the hook intervenes.
	Output *HookSpecificOutput `json:"hookSpecificOutput,omitempty"`
}

// HookSpecificOutput is the synchronous PreToolUse permission decision.
type HookSpecificOutput struct {
	// Event identifies the native hook output variant.
	Event string `json:"hookEventName"`
	// Decision is ask or deny for a restricted write.
	Decision string `json:"permissionDecision"`
	// Reason gives the policy reason without exposing file content.
	Reason string `json:"permissionDecisionReason"`
}

// GetSettingsControlRequest reads effective native settings without submitting a prompt.
type GetSettingsControlRequest struct {
	// Subtype is get_settings.
	Subtype string `json:"subtype"`
}

// SettingsControlResponse selects effective policy and applied configuration fields.
type SettingsControlResponse struct {
	// Effective contains the merged policy observed by the CLI.
	Effective *EffectiveSettings `json:"effective"`
	// Applied is the native effective invocation configuration.
	Applied map[string]json.RawMessage `json:"applied"`
}

// EffectiveSettings contains the narrow fields required to verify launch policy.
type EffectiveSettings struct {
	// Model is the merged model setting before native alias resolution.
	Model string `json:"model"`
	// Environment selects model overrides from the native settings environment.
	Environment struct {
		// Model overrides the settings model through ANTHROPIC_MODEL.
		Model string `json:"ANTHROPIC_MODEL"`
	} `json:"env"`
	// FastMode is the merged preference, separate from the observed service state.
	FastMode *bool `json:"fastMode"`
	// AllowManagedHooksOnly confirms the restricted hook sources.
	AllowManagedHooksOnly *bool `json:"allowManagedHooksOnly"`
	// Sandbox reports native Bash restrictions.
	Sandbox *EffectiveSandbox `json:"sandbox"`
	// Permissions exposes allow rules which may widen the sandbox.
	Permissions EffectivePermissions `json:"permissions"`
}

// EffectivePermissions selects permission rules relevant to sandbox writes.
type EffectivePermissions struct {
	// Allow contains native permission selectors.
	Allow []string `json:"allow"`
}

// EffectiveSandbox contains the native Bash policy independently from tool hooks.
type EffectiveSandbox struct {
	// Enabled confirms that the CLI must sandbox Bash.
	Enabled *bool `json:"enabled"`
	// FailIfUnavailable rejects runtime startup when sandboxing cannot work.
	FailIfUnavailable *bool `json:"failIfUnavailable"`
	// AllowUnsandboxed permits a native escape only when explicitly true.
	AllowUnsandboxed *bool `json:"allowUnsandboxedCommands"`
	// ExcludedCommands lists unsandboxed command exceptions.
	ExcludedCommands []string `json:"excludedCommands"`
	// Filesystem reports the effective write policy.
	Filesystem *EffectiveSandboxFilesystem `json:"filesystem"`
}

// EffectiveSandboxFilesystem is the native effective path policy.
type EffectiveSandboxFilesystem struct {
	// Disabled bypasses filesystem enforcement.
	Disabled bool `json:"disabled"`
	// AllowWrite lists writable roots.
	AllowWrite []string `json:"allowWrite"`
	// DenyWrite lists protected paths.
	DenyWrite []string `json:"denyWrite"`
}

package codex

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"strings"

	"github.com/JieWaZi/acp-go/pkg/codex/protocol"

	acp "github.com/coder/acp-go-sdk"
)

// sessionUpdater 是事件组件消费的最小 ACP 连接能力。
type sessionUpdater interface {
	// SessionUpdate 向客户端发送一个 SDK 原生 session/update 通知。
	SessionUpdate(context.Context, acp.SessionNotification) error
}

// turnUsage 保存当前 generation 最近一次 token usage，不跨 Turn 复用。
type turnUsage struct {
	// LastTokens 是最近一次请求自身消耗的 token 数。
	LastTokens int64
	// TotalTokens 是 thread 累计消耗的 token 数。
	TotalTokens int64
	// ContextWindow 是模型上下文窗口大小；未知时为零。
	ContextWindow int64
	// InputTokens 是扣除缓存读取后的本轮输入 token 数。
	InputTokens int64
	// CacheReadTokens 是本轮命中的缓存输入 token 数。
	CacheReadTokens int64
	// CacheWriteTokens 是本轮写入缓存的 token 数；nil 表示上游未报告。
	CacheWriteTokens *int64
	// OutputTokens 是包含 reasoning 在内的本轮输出 token 数。
	OutputTokens int64
	// ThoughtTokens 是本轮 reasoning 输出 token 数。
	ThoughtTokens int64
}

// PromptUsage 把已校验的 Codex 本轮统计转换为 ACP PromptResponse Usage。
func (u turnUsage) PromptUsage() *acp.Usage {
	cacheRead := int(u.CacheReadTokens)
	thought := int(u.ThoughtTokens)
	usage := &acp.Usage{
		CachedReadTokens: &cacheRead,
		InputTokens:      int(u.InputTokens),
		OutputTokens:     int(u.OutputTokens),
		ThoughtTokens:    &thought,
		TotalTokens:      int(u.LastTokens),
	}
	if u.CacheWriteTokens != nil {
		cacheWrite := int(*u.CacheWriteTokens)
		usage.CachedWriteTokens = &cacheWrite
	}
	return usage
}

// eventHandler 保存一个 turn generation 内去重所需的局部事件状态。
type eventHandler struct {
	// updater 是 SDK 原生 session/update 发送边界。
	updater sessionUpdater
	// sessionID 是所有发出通知绑定的 ACP session 标识。
	sessionID acp.SessionId
	// logger 只记录未知方法或 item 的安全身份摘要。
	logger *slog.Logger
	// tools 负责 Command、File 与 MCP 的纯 DTO 映射。
	tools toolMapper
	// terminalOutputMode 是 session 安装时保存的客户端能力快照。
	terminalOutputMode terminalOutputMode
	// messagePhases 保存 item/started 宣告的消息阶段。
	messagePhases map[string]protocol.PhaseEnum
	// streamedMessages 标记已通过 delta 发出的 agent message，完成项不再重复。
	streamedMessages map[string]struct{}
	// streamedReasoning 标记已通过任一 reasoning delta 发出的 reasoning item。
	streamedReasoning map[string]struct{}
	// emittedImageViews 标记 started 阶段已经发出的单次图片查看工具卡片。
	emittedImageViews map[string]struct{}
	// completedItems 标记已成功处理的完成项，避免 app-server 重放导致重复通知。
	completedItems map[string]struct{}
	// terminalCommands 标记 started 阶段实际展示了 ACP terminal 的命令。
	terminalCommands map[string]struct{}
	// terminalCommandOutputs 标记 terminal 命令已经发送过非空输出增量。
	terminalCommandOutputs map[string]struct{}
	// usage 保存当前 turn 最新 usage 快照。
	usage *turnUsage
}

// newEventHandler 创建只绑定一个 session/generation 的事件处理器。
func newEventHandler(
	updater sessionUpdater,
	sessionID acp.SessionId,
	logger *slog.Logger,
	terminalMode terminalOutputMode,
) *eventHandler {
	if logger == nil {
		logger = slog.Default()
	}
	return &eventHandler{
		updater:                updater,
		sessionID:              sessionID,
		logger:                 logger,
		terminalOutputMode:     terminalMode,
		messagePhases:          make(map[string]protocol.PhaseEnum),
		streamedMessages:       make(map[string]struct{}),
		streamedReasoning:      make(map[string]struct{}),
		emittedImageViews:      make(map[string]struct{}),
		completedItems:         make(map[string]struct{}),
		terminalCommands:       make(map[string]struct{}),
		terminalCommandOutputs: make(map[string]struct{}),
	}
}

// Usage 返回当前 turn 最近一次 token usage 的副本。
func (h *eventHandler) Usage() (turnUsage, bool) {
	if h.usage == nil {
		return turnUsage{}, false
	}
	return *h.usage, true
}

// handleItemStarted 记录 agent message phase；其他 item 交由 tool mapper 处理。
func (h *eventHandler) handleItemStarted(ctx context.Context, params protocol.ItemStartedNotification) error {
	if params.Item.Type == protocol.AgentMessage && params.Item.Phase != nil {
		h.messagePhases[params.Item.ID] = *params.Item.Phase
	}
	if params.Item.Type == protocol.CommandExecution {
		if commandExecutionUsesTerminalOutput(params.Item) {
			h.terminalCommands[params.Item.ID] = struct{}{}
		} else {
			delete(h.terminalCommands, params.Item.ID)
			delete(h.terminalCommandOutputs, params.Item.ID)
		}
	}
	update, err := h.tools.mapStarted(params.Item)
	if err != nil {
		return err
	}
	if update != nil {
		if err := h.emit(ctx, *update); err != nil {
			return err
		}
		if params.Item.Type == protocol.ImageView {
			h.emittedImageViews[params.Item.ID] = struct{}{}
		}
		return nil
	}
	switch params.Item.Type {
	case protocol.AgentMessage, protocol.Reasoning, protocol.UserMessage,
		protocol.HookPrompt, protocol.Sleep:
		return nil
	default:
		h.logger.Info(
			"Ignoring unknown Codex started item",
			"item_type", string(params.Item.Type),
			"item_id", params.Item.ID,
		)
	}
	return nil
}

// handleCommandOutputDelta 记录 terminal 输出状态并按 session 协商键发送增量。
func (h *eventHandler) handleCommandOutputDelta(
	ctx context.Context,
	params protocol.CommandExecutionOutputDeltaNotification,
) error {
	if _, terminal := h.terminalCommands[params.ItemID]; terminal && params.Delta != "" {
		h.terminalCommandOutputs[params.ItemID] = struct{}{}
	}
	return h.emit(ctx, mapCommandOutputDelta(params, h.commandOutputMode(params.ItemID)))
}

// handleTerminalInteraction 将 stdin 回显视为 terminal 命令的非空输出。
func (h *eventHandler) handleTerminalInteraction(
	ctx context.Context,
	params protocol.TerminalInteractionNotification,
) error {
	if _, terminal := h.terminalCommands[params.ItemID]; terminal {
		h.terminalCommandOutputs[params.ItemID] = struct{}{}
	}
	return h.emit(ctx, mapTerminalInteraction(params, h.commandOutputMode(params.ItemID)))
}

// handleAgentMessageDelta 将 agent 文本 delta 映射为带 messageId 和 phase 的 ACP chunk。
func (h *eventHandler) handleAgentMessageDelta(
	ctx context.Context,
	params protocol.AgentMessageDeltaNotification,
) error {
	h.streamedMessages[params.ItemID] = struct{}{}
	phase := h.messagePhases[params.ItemID]
	return h.emitAgentMessage(ctx, params.ItemID, params.Delta, phase)
}

// handleReasoningDelta 将 reasoning 文本映射为稳定 ACP agent_thought_chunk。
func (h *eventHandler) handleReasoningDelta(ctx context.Context, itemID, delta string) error {
	h.streamedReasoning[itemID] = struct{}{}
	update := acp.SessionUpdate{AgentThoughtChunk: &acp.SessionUpdateAgentThoughtChunk{
		MessageId: &itemID,
		Content:   acp.TextBlock(delta),
	}}
	return h.emit(ctx, update)
}

// handleItemCompleted 依据生成协议的 item discriminator 选择完成项映射。
func (h *eventHandler) handleItemCompleted(ctx context.Context, params protocol.ItemCompletedNotification) error {
	item := params.Item
	if _, completed := h.completedItems[item.ID]; completed {
		return nil
	}

	var err error
	switch item.Type {
	case protocol.AgentMessage:
		err = h.handleCompletedAgentMessage(ctx, item)
	case protocol.Reasoning:
		err = h.handleCompletedReasoning(ctx, item)
	case protocol.ThreadItemType("plan"):
		// 当前生成类型未提供 plan 常量，但历史完成项仍可能返回该 discriminator。
		err = h.handleCompletedPlan(ctx, item)
	case protocol.CommandExecution, protocol.FileChange, protocol.MCPToolCall, protocol.WebSearch:
		var update *acp.SessionUpdate
		update, err = h.tools.mapCompleted(item)
		if err == nil && update != nil {
			if item.Type == protocol.CommandExecution {
				h.decorateCommandCompletion(item, update)
			}
			err = h.emit(ctx, *update)
		}
	case protocol.ImageView:
		if _, emitted := h.emittedImageViews[item.ID]; emitted {
			delete(h.emittedImageViews, item.ID)
			break
		}
		update := mapImageView(item)
		err = h.emit(ctx, update)
	case protocol.UserMessage, protocol.HookPrompt, protocol.Sleep:
		// 用户输入、hook prompt 与 sleep 不展示为 ACP 工具。
	default:
		h.logger.Info("Ignoring unknown Codex item", "item_type", string(item.Type), "item_id", item.ID)
	}
	// 只在成功后记录完成态；客户端发送失败时允许调用方重试同一事件。
	if err == nil {
		h.completedItems[item.ID] = struct{}{}
		delete(h.terminalCommands, item.ID)
		delete(h.terminalCommandOutputs, item.ID)
	}
	return err
}

// handleHistoryItem 把历史 Item 转换为 V1 工具更新。
// command 先发完整 tool_call 再发终态 update；file/MCP 只发一条 completed tool_call。
func (h *eventHandler) handleHistoryItem(
	ctx context.Context,
	params protocol.ItemCompletedNotification,
) error {
	item := params.Item
	if _, completed := h.completedItems[item.ID]; completed {
		return nil
	}
	switch item.Type {
	case protocol.CommandExecution:
		if err := h.handleItemStarted(ctx, protocol.ItemStartedNotification{
			ThreadID: params.ThreadID,
			TurnID:   params.TurnID,
			Item:     item,
		}); err != nil {
			return err
		}
		return h.handleItemCompleted(ctx, params)
	case protocol.FileChange:
		update, err := h.tools.mapStarted(item)
		if err != nil {
			return err
		}
		if update != nil {
			if err = h.emit(ctx, *update); err != nil {
				return err
			}
		}
		h.completedItems[item.ID] = struct{}{}
		return nil
	case protocol.MCPToolCall:
		update, err := mapMCPHistory(item)
		if err != nil {
			return err
		}
		if err = h.emit(ctx, update); err != nil {
			return err
		}
		h.completedItems[item.ID] = struct{}{}
		return nil
	case protocol.WebSearch:
		if err := h.emit(ctx, mapWebSearchHistory(item)); err != nil {
			return err
		}
		h.completedItems[item.ID] = struct{}{}
		return nil
	case protocol.ImageView:
		if err := h.emit(ctx, mapImageView(item)); err != nil {
			return err
		}
		h.completedItems[item.ID] = struct{}{}
		return nil
	default:
		return h.handleItemCompleted(ctx, params)
	}
}

// decorateCommandCompletion 为 terminal 命令补齐 exit，并仅在缺少 delta 时回退聚合输出。
func (h *eventHandler) decorateCommandCompletion(item protocol.ThreadItem, update *acp.SessionUpdate) {
	if _, terminal := h.terminalCommands[item.ID]; !terminal || update.ToolCallUpdate == nil {
		return
	}
	_, hadOutput := h.terminalCommandOutputs[item.ID]
	update.ToolCallUpdate.Meta = terminalCompletionMeta(
		h.terminalOutputMode,
		item.ID,
		stringValue(item.AggregatedOutput),
		item.ExitCode,
		hadOutput,
	)
}

// commandOutputMode 返回当前 Session 协商的命令输出模式。
// 已解析为 read/search 的命令没有 terminal content，即使现代客户端也继续使用 delta 键。
func (h *eventHandler) commandOutputMode(itemID string) terminalOutputMode {
	if h.terminalOutputMode == terminalOutputModeFull {
		if _, terminal := h.terminalCommands[itemID]; terminal {
			return terminalOutputModeFull
		}
	}
	return terminalOutputModeDelta
}

// handleCompletedAgentMessage 在未流式发送 delta 时补发完整消息，否则仅完成去重状态。
func (h *eventHandler) handleCompletedAgentMessage(ctx context.Context, item protocol.ThreadItem) error {
	defer delete(h.messagePhases, item.ID)
	if _, streamed := h.streamedMessages[item.ID]; streamed {
		delete(h.streamedMessages, item.ID)
		return nil
	}
	if item.Text == nil || *item.Text == "" {
		return nil
	}
	phase := protocol.FinalAnswer
	if item.Phase != nil {
		phase = *item.Phase
	} else if startedPhase, ok := h.messagePhases[item.ID]; ok {
		phase = startedPhase
	}
	return h.emitAgentMessage(ctx, item.ID, *item.Text, phase)
}

// handleCompletedReasoning 在没有 delta 时按 summary 优先、content 兜底输出完整 reasoning。
func (h *eventHandler) handleCompletedReasoning(ctx context.Context, item protocol.ThreadItem) error {
	if _, streamed := h.streamedReasoning[item.ID]; streamed {
		delete(h.streamedReasoning, item.ID)
		return nil
	}
	parts := append([]string(nil), item.Summary...)
	if len(parts) == 0 {
		for _, content := range item.Content {
			switch {
			case content.String != nil:
				parts = append(parts, *content.String)
			case content.UserInput != nil && content.UserInput.Text != nil:
				parts = append(parts, *content.UserInput.Text)
			}
		}
	}
	if len(parts) == 0 {
		return nil
	}
	return h.handleReasoningDelta(ctx, item.ID, strings.Join(parts, "\n\n"))
}

// handleCompletedPlan 将没有稳定 checklist 事件的历史计划文本降级为最终 agent 消息。
func (h *eventHandler) handleCompletedPlan(ctx context.Context, item protocol.ThreadItem) error {
	if item.Text == nil || *item.Text == "" {
		return nil
	}
	return h.emitAgentMessage(ctx, item.ID, *item.Text, protocol.FinalAnswer)
}

// handlePlanUpdated 使用 ACP SDK PlanEntry/UpdatePlan 映射稳定 turn/plan/updated。
func (h *eventHandler) handlePlanUpdated(ctx context.Context, params protocol.TurnPlanUpdatedNotification) error {
	entries := make([]acp.PlanEntry, 0, len(params.Plan))
	for _, step := range params.Plan {
		status, err := mapPlanStatus(step.Status)
		if err != nil {
			return err
		}
		entries = append(entries, acp.PlanEntry{
			Content: step.Step, Priority: acp.PlanEntryPriorityMedium, Status: status,
		})
	}
	return h.emit(ctx, acp.UpdatePlan(entries...))
}

// mapPlanStatus 将 app-server 驼峰状态映射为 ACP 下划线状态。
func mapPlanStatus(status protocol.PlanStatus) (acp.PlanEntryStatus, error) {
	switch status {
	case protocol.Pending:
		return acp.PlanEntryStatusPending, nil
	case protocol.FluffyInProgress:
		return acp.PlanEntryStatusInProgress, nil
	case protocol.TentacledCompleted:
		return acp.PlanEntryStatusCompleted, nil
	default:
		return "", fmt.Errorf("unknown plan status %q", status)
	}
}

// handleTokenUsage 保存最新快照，并在上下文窗口可用时发送 ACP usage_update。
func (h *eventHandler) handleTokenUsage(
	ctx context.Context,
	params protocol.ThreadTokenUsageUpdatedNotification,
) error {
	usage, err := turnUsageFromTokenUsage(params.TokenUsage)
	if err != nil {
		return err
	}
	h.usage = &usage
	if usage.ContextWindow <= 0 {
		return nil
	}
	update := acp.SessionUpdate{UsageUpdate: &acp.SessionUsageUpdate{
		Used: int(usage.LastTokens),
		Size: int(usage.ContextWindow),
	}}
	return h.emit(ctx, update)
}

// turnUsageFromTokenUsage 校验 app-server 数值并保留 PromptResponse 所需的完整本轮明细。
func turnUsageFromTokenUsage(tokenUsage protocol.TokenUsage) (turnUsage, error) {
	last := tokenUsage.Last
	values := []int64{
		last.TotalTokens,
		last.InputTokens,
		last.CachedInputTokens,
		last.OutputTokens,
		last.ReasoningOutputTokens,
		tokenUsage.Total.TotalTokens,
	}
	if tokenUsage.ModelContextWindow != nil {
		values = append(values, *tokenUsage.ModelContextWindow)
	}
	if last.CacheWriteInputTokens != nil {
		values = append(values, *last.CacheWriteInputTokens)
	}
	for _, value := range values {
		if value < 0 || value > math.MaxInt {
			return turnUsage{}, fmt.Errorf("token usage exceeds ACP integer range")
		}
	}
	if last.InputTokens < last.CachedInputTokens {
		return turnUsage{}, fmt.Errorf("cached input tokens exceed total input tokens")
	}
	var cacheWrite *int64
	if last.CacheWriteInputTokens != nil {
		value := *last.CacheWriteInputTokens
		cacheWrite = &value
	}
	return turnUsage{
		LastTokens:       last.TotalTokens,
		TotalTokens:      tokenUsage.Total.TotalTokens,
		ContextWindow:    int64Value(tokenUsage.ModelContextWindow),
		InputTokens:      last.InputTokens - last.CachedInputTokens,
		CacheReadTokens:  last.CachedInputTokens,
		CacheWriteTokens: cacheWrite,
		OutputTokens:     last.OutputTokens,
		ThoughtTokens:    last.ReasoningOutputTokens,
	}, nil
}

// int64Value 将可选 int64 转为快照值，nil 表示未知并返回零。
func int64Value(value *int64) int64 {
	if value == nil {
		return 0
	}
	return *value
}

// emitAgentMessage 直接构造 SDK agent_message_chunk，以保留 messageId 与 Codex phase 元数据。
func (h *eventHandler) emitAgentMessage(
	ctx context.Context,
	itemID string,
	text string,
	phase protocol.PhaseEnum,
) error {
	meta := map[string]any(nil)
	if phase != "" {
		meta = map[string]any{"codex": map[string]any{"phase": string(phase)}}
	}
	update := acp.SessionUpdate{AgentMessageChunk: &acp.SessionUpdateAgentMessageChunk{
		Meta: meta, MessageId: &itemID, Content: acp.TextBlock(text),
	}}
	return h.emit(ctx, update)
}

// emit 在唯一边界包装 ACP session 标识并传播 SDK 发送错误。
func (h *eventHandler) emit(ctx context.Context, update acp.SessionUpdate) error {
	if err := h.updater.SessionUpdate(ctx, acp.SessionNotification{
		SessionId: h.sessionID,
		Update:    update,
	}); err != nil {
		return fmt.Errorf("sending ACP session update: %w", err)
	}
	return nil
}

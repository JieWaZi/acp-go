package codex

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/JieWaZi/acp-go/pkg/codex/protocol"

	acp "github.com/coder/acp-go-sdk"
)

// turnGeneration 是 runtime 授予一个 handler 的不可变 turn 身份。
type turnGeneration struct {
	// SessionID 是面向 ACP 客户端的 session 标识。
	SessionID acp.SessionId
	// ThreadID 是 Codex app-server thread 标识。
	ThreadID string
	// TurnID 是 Codex app-server turn 标识。
	TurnID string
	// Generation 是 runtime 在新 turn/cancel/restore 时递增的租约编号。
	Generation uint64
}

// generationGuard 由 runtime 实现，事件组件不保存第二份 generation 真相。
type generationGuard interface {
	// IsCurrent 判断一个不可变 generation 身份当前是否仍可写入客户端。
	IsCurrent(turnGeneration) bool
}

// eventRouter 负责强类型协议解码、generation 过滤和事件 handler 分派。
type eventRouter struct {
	// generation 是该路由器被创建时绑定的 runtime generation。
	generation turnGeneration
	// guard 在每个通知处理前读取 runtime 当前 generation。
	guard generationGuard
	// handler 负责当前 generation 内的状态化映射与去重。
	handler *eventHandler
	// logger 仅记录安全摘要，禁止输出原始 JSON payload。
	logger *slog.Logger
}

// newEventRouter 使用消费方小接口和显式 generation 注入创建路由器。
func newEventRouter(
	updater sessionUpdater,
	generation turnGeneration,
	guard generationGuard,
	logger *slog.Logger,
	terminalMode terminalOutputMode,
) *eventRouter {
	if logger == nil {
		logger = slog.Default()
	}
	return &eventRouter{
		generation: generation,
		guard:      guard,
		handler:    newEventHandler(updater, generation.SessionID, logger, terminalMode),
		logger:     logger,
	}
}

// HandleJSON 使用 protocol 包的 method 常量和 typed envelope 解码后再分派通知。
func (r *eventRouter) HandleJSON(ctx context.Context, raw []byte) error {
	notification, err := protocol.DecodeServerNotification(raw)
	if err != nil {
		return fmt.Errorf("decoding Codex server notification: %w", err)
	}
	return r.Handle(ctx, notification, len(raw))
}

// Handle 对强类型通知执行 generation/thread/turn 校验后分派。
func (r *eventRouter) Handle(ctx context.Context, notification protocol.ServerNotification, rawSize int) error {
	if unknown, ok := notification.(*protocol.UnknownServerNotification); ok {
		if ignoredCodexNotification(unknown.Method()) {
			r.logger.Debug("Ignoring Codex notification", "method", unknown.Method())
			return nil
		}
		threadID, turnID := unknownNotificationIdentity(unknown.Params)
		attributes := []any{
			"method", unknown.Method(),
			"session_id", string(r.generation.SessionID),
			"payload_bytes", len(unknown.Params),
		}
		if threadID != "" {
			attributes = append(attributes, "thread_id", threadID)
		}
		if turnID != "" {
			attributes = append(attributes, "turn_id", turnID)
		}
		r.logger.Info("Ignoring unknown Codex notification", attributes...)
		return nil
	}
	threadID, turnID, scoped := notificationScope(notification)
	if scoped && (threadID != r.generation.ThreadID || turnID != r.generation.TurnID) {
		r.logger.Debug("Ignoring Codex notification from another turn", "method", notification.Method())
		return nil
	}
	if r.guard == nil || !r.guard.IsCurrent(r.generation) {
		r.logger.Debug("Ignoring Codex notification from stale generation", "method", notification.Method())
		return nil
	}

	switch event := notification.(type) {
	case *protocol.ItemStartedEnvelope:
		return r.handler.handleItemStarted(ctx, event.Params)
	case *protocol.ItemCompletedEnvelope:
		return r.handler.handleItemCompleted(ctx, event.Params)
	case *protocol.AgentMessageDeltaEnvelope:
		return r.handler.handleAgentMessageDelta(ctx, event.Params)
	case *protocol.ReasoningSummaryTextDeltaEnvelope:
		return r.handler.handleReasoningDelta(ctx, event.Params.ItemID, event.Params.Delta)
	case *protocol.ReasoningSummaryPartAddedEnvelope:
		return r.handler.handleReasoningDelta(ctx, event.Params.ItemID, "\n\n")
	case *protocol.ReasoningTextDeltaEnvelope:
		return r.handler.handleReasoningDelta(ctx, event.Params.ItemID, event.Params.Delta)
	case *protocol.TurnPlanUpdatedEnvelope:
		return r.handler.handlePlanUpdated(ctx, event.Params)
	case *protocol.ThreadTokenUsageUpdatedEnvelope:
		return r.handler.handleTokenUsage(ctx, event.Params)
	case *protocol.CommandExecutionOutputDeltaEnvelope:
		return r.handler.handleCommandOutputDelta(ctx, event.Params)
	case *protocol.TerminalInteractionEnvelope:
		return r.handler.handleTerminalInteraction(ctx, event.Params)
	case *protocol.MCPToolCallProgressEnvelope:
		return r.handler.emit(ctx, mapMCPProgress(event.Params))
	case *protocol.FileChangePatchUpdatedEnvelope:
		return r.handler.emit(ctx, mapFilePatchUpdated(event.Params))
	default:
		r.logger.Debug(
			"Ignoring Codex notification not mapped by V1",
			"method", notification.Method(),
			"payload_bytes", rawSize,
		)
		return nil
	}
}

// ignoredCodexNotification 标识 upstream 明确忽略且不应作为未知能力告警的方法。
func ignoredCodexNotification(method string) bool {
	return method == "hook/started" || method == "hook/completed"
}

// unknownNotificationIdentity 只从未知 params 中提取安全 thread/turn 字符串，不记录其他字段。
func unknownNotificationIdentity(params json.RawMessage) (threadID string, turnID string) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(params, &fields); err != nil {
		return "", ""
	}
	return rawIdentityString(fields["threadId"]), rawIdentityString(fields["turnId"])
}

// rawIdentityString 仅接受 JSON string identity，其他形状一律按不可用处理。
func rawIdentityString(raw json.RawMessage) string {
	var value string
	if len(raw) == 0 || json.Unmarshal(raw, &value) != nil {
		return ""
	}
	return value
}

// Usage 返回该路由器绑定 turn 的最新 usage 快照。
func (r *eventRouter) Usage() (turnUsage, bool) {
	return r.handler.Usage()
}

// notificationScope 从每个 typed payload 中读取 thread/turn 身份，避免解析通用 map。
func notificationScope(notification protocol.ServerNotification) (string, string, bool) {
	switch event := notification.(type) {
	case *protocol.ErrorEnvelope:
		return event.Params.ThreadID, event.Params.TurnID, true
	case *protocol.ItemStartedEnvelope:
		return event.Params.ThreadID, event.Params.TurnID, true
	case *protocol.ItemCompletedEnvelope:
		return event.Params.ThreadID, event.Params.TurnID, true
	case *protocol.AgentMessageDeltaEnvelope:
		return event.Params.ThreadID, event.Params.TurnID, true
	case *protocol.ReasoningSummaryTextDeltaEnvelope:
		return event.Params.ThreadID, event.Params.TurnID, true
	case *protocol.ReasoningSummaryPartAddedEnvelope:
		return event.Params.ThreadID, event.Params.TurnID, true
	case *protocol.ReasoningTextDeltaEnvelope:
		return event.Params.ThreadID, event.Params.TurnID, true
	case *protocol.TurnPlanUpdatedEnvelope:
		return event.Params.ThreadID, event.Params.TurnID, true
	case *protocol.ThreadTokenUsageUpdatedEnvelope:
		return event.Params.ThreadID, event.Params.TurnID, true
	case *protocol.CommandExecutionOutputDeltaEnvelope:
		return event.Params.ThreadID, event.Params.TurnID, true
	case *protocol.TerminalInteractionEnvelope:
		return event.Params.ThreadID, event.Params.TurnID, true
	case *protocol.MCPToolCallProgressEnvelope:
		return event.Params.ThreadID, event.Params.TurnID, true
	case *protocol.FileChangePatchUpdatedEnvelope:
		return event.Params.ThreadID, event.Params.TurnID, true
	default:
		return "", "", false
	}
}

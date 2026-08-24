package codex

import (
	"context"
	"fmt"
	"net/url"

	"github.com/JieWaZi/acp-go/pkg/codex/protocol"
	acp "github.com/coder/acp-go-sdk"
)

// mcpStatusSnapshot 把状态与全局单调水位绑定，防止新会话消费旧同名 server 终态。
type mcpStatusSnapshot struct {
	// notification 是 app-server 最近一次状态通知。
	notification protocol.MCPServerStatusUpdatedNotification
	// version 是该通知在当前 app-server 连接上的到达序号。
	version uint64
}

// handleMCPStartupStatus 保存早到状态，并把失败/取消转换为用户可见的 ACP tool_call。
func (a *Agent) handleMCPStartupStatus(ctx context.Context, status protocol.MCPServerStatusUpdatedNotification) {
	a.mcpMu.Lock()
	a.mcpStatusVersion++
	snapshot := mcpStatusSnapshot{notification: status, version: a.mcpStatusVersion}
	a.mcpStatuses[status.Name] = snapshot
	a.mcpMu.Unlock()

	if status.ThreadID != nil {
		if state, ok := a.sessions.get(*status.ThreadID); ok {
			a.publishMCPStartupFailure(ctx, state, snapshot)
		}
		return
	}
	for _, state := range a.sessions.snapshot() {
		a.publishMCPStartupFailure(ctx, state, snapshot)
	}
}

// currentMCPStatusVersion 返回 thread open 前使用的启动状态水位。
func (a *Agent) currentMCPStatusVersion() uint64 {
	a.mcpMu.Lock()
	defer a.mcpMu.Unlock()
	return a.mcpStatusVersion
}

// publishKnownMCPStartupFailures 安装请求 server 集合，并补发 thread/start 响应前到达的终态。
func (a *Agent) publishKnownMCPStartupFailures(state *sessionState, names []string, afterVersion uint64) {
	if state == nil || len(names) == 0 {
		return
	}
	state.mu.Lock()
	state.mcpServers = make(map[string]struct{}, len(names))
	state.mcpStartupReported = make(map[string]protocol.MCPServerStatusUpdatedNotificationStatus)
	state.mcpStartupAfterVersion = afterVersion
	for _, name := range names {
		state.mcpServers[name] = struct{}{}
	}
	state.mu.Unlock()

	a.mcpMu.Lock()
	statuses := make([]mcpStatusSnapshot, 0, len(names))
	for _, name := range names {
		if status, ok := a.mcpStatuses[name]; ok && status.version > afterVersion {
			statuses = append(statuses, status)
		}
	}
	a.mcpMu.Unlock()
	for _, status := range statuses {
		a.publishMCPStartupFailure(a.runtimeCtx, state, status)
	}
}

// publishMCPStartupFailure 对当前 session 的单个失败终态做去重并发送 ACP tool_call。
func (a *Agent) publishMCPStartupFailure(
	ctx context.Context,
	state *sessionState,
	snapshot mcpStatusSnapshot,
) {
	status := snapshot.notification
	if !a.sessions.isCurrent(state) || (status.Status != protocol.PurpleFailed && status.Status != protocol.Cancelled) {
		return
	}
	if status.ThreadID != nil && *status.ThreadID != state.id {
		return
	}
	state.mu.Lock()
	_, requested := state.mcpServers[status.Name]
	currentStartup := snapshot.version > state.mcpStartupAfterVersion
	_, alreadyReported := state.mcpStartupReported[status.Name]
	if requested && currentStartup && !alreadyReported {
		state.mcpStartupReported[status.Name] = status.Status
	}
	state.mu.Unlock()
	if !requested || !currentStartup || alreadyReported {
		return
	}

	message := fmt.Sprintf("[acp-go forwarded startup error] MCP server `%s` startup was cancelled.", status.Name)
	if status.Status == protocol.PurpleFailed {
		detail := "unknown MCP startup error"
		if status.Error != nil && *status.Error != "" {
			detail = *status.Error
		}
		message = fmt.Sprintf("[acp-go forwarded startup error] MCP server `%s` failed to start: %s", status.Name, detail)
	}
	update := acp.StartToolCall(
		acp.ToolCallId("mcp_startup."+url.QueryEscape(status.Name)),
		"mcp__"+status.Name+"__startup",
		acp.WithStartKind(acp.ToolKindOther),
		acp.WithStartStatus(acp.ToolCallStatusFailed),
		acp.WithStartContent([]acp.ToolCallContent{acp.ToolContent(acp.TextBlock(message))}),
	)
	if updater := a.currentSessionUpdater(); updater != nil {
		if err := updater.SessionUpdate(ctx, acp.SessionNotification{SessionId: acp.SessionId(state.id), Update: update}); err != nil {
			a.logger.Debug("发布 MCP 启动失败状态失败", "session_id", state.id, "server", status.Name, "error", err)
		}
	}
}

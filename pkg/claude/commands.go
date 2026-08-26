package claude

import (
	"context"
	"strings"
	"time"

	"github.com/JieWaZi/acp-go/pkg/claude/protocol"
	acp "github.com/coder/acp-go-sdk"
)

const (
	// availableCommandsUpdateTimeout 限制返回 Session 后异步发布命令菜单的等待时间。
	availableCommandsUpdateTimeout = 5 * time.Second
)

// availableSlashCommands 复用 ACP SDK 类型映射 Claude SDK 的完整命令列表。
func availableSlashCommands(
	commands []protocol.SlashCommand,
	terminalCommands []string,
) []acp.AvailableCommand {
	terminal := make(map[string]struct{}, len(terminalCommands))
	for _, name := range terminalCommands {
		terminal[name] = struct{}{}
	}
	available := make([]acp.AvailableCommand, 0, len(commands))
	for _, command := range commands {
		if _, hidden := terminal[command.Name]; hidden {
			continue
		}
		name := command.Name
		if strings.HasSuffix(name, " (MCP)") {
			name = "mcp:" + strings.TrimSuffix(name, " (MCP)")
		}
		if isUnsupportedSlashCommand(name) {
			continue
		}
		availableCommand := acp.AvailableCommand{
			Name: name, Description: command.Description,
		}
		if hint := slashCommandArgumentHint(command.ArgumentHint); hint != "" {
			availableCommand.Input = &acp.AvailableCommandInput{
				Unstructured: &acp.UnstructuredCommandInput{Hint: hint},
			}
		}
		available = append(available, availableCommand)
	}
	return available
}

// isUnsupportedSlashCommand 判断命令是否依赖 Claude 本地终端或不适合 ACP 菜单。
func isUnsupportedSlashCommand(name string) bool {
	switch name {
	case "clear", "cost", "keybindings-help", "login", "logout", "output-style:new", "release-notes", "todos":
		return true
	default:
		return false
	}
}

// slashCommandArgumentHint 兼容固定对字符串和字符串数组的处理。
func slashCommandArgumentHint(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case []string:
		return strings.Join(typed, " ")
	case []any:
		parts := make([]string, 0, len(typed))
		for _, item := range typed {
			if part, ok := item.(string); ok {
				parts = append(parts, part)
			}
		}
		return strings.Join(parts, " ")
	default:
		return ""
	}
}

// availableCommands 返回当前 SDK 命令与终端过滤条件的原子快照。
func (s *claudeSession) availableCommands() []acp.AvailableCommand {
	s.mu.Lock()
	commands := append([]protocol.SlashCommand(nil), s.initialization.Commands...)
	terminal := append([]string(nil), s.systemInit.TerminalSlashCommands...)
	s.mu.Unlock()
	return availableSlashCommands(commands, terminal)
}

// sendAvailableCommandsUpdate 发送 ACP 标准的完整命令替换通知。
func (s *claudeSession) sendAvailableCommandsUpdate(ctx context.Context) error {
	return s.agent.sendUpdate(ctx, s.id, acp.SessionUpdate{
		AvailableCommandsUpdate: &acp.SessionAvailableCommandsUpdate{
			AvailableCommands: s.availableCommands(),
			SessionUpdate:     "available_commands_update",
		},
	})
}

// updateAvailableCommands 保存 commands_changed 的权威列表并立即通知客户端。
func (s *claudeSession) updateAvailableCommands(
	ctx context.Context,
	commands []protocol.SlashCommand,
) error {
	s.mu.Lock()
	s.initialization.Commands = append([]protocol.SlashCommand(nil), commands...)
	s.mu.Unlock()
	return s.sendAvailableCommandsUpdate(ctx)
}

// scheduleAvailableCommandsUpdate 按当前 Session 身份异步发布菜单。
func (a *Agent) scheduleAvailableCommandsUpdate(sessionID string) {
	time.AfterFunc(0, func() {
		session, ok := a.sessions.get(sessionID)
		if !ok {
			return
		}
		base := a.runtimeCtx
		if base == nil {
			base = context.Background()
		}
		ctx, cancel := context.WithTimeout(base, availableCommandsUpdateTimeout)
		defer cancel()
		if err := session.sendAvailableCommandsUpdate(ctx); err != nil {
			a.logger.Warn("Failed to publish Claude slash commands", "session_id", session.id, "error", err)
		}
	})
}

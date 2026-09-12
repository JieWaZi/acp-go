package cursor

import (
	"path/filepath"
	"strings"
)

// permissionPanel 仅检查当前审批区域，排除正文和此前工具中相同的命令或路径。
func permissionPanel(screen string) string {
	lines := strings.Split(screen, "\n")
	start := 0
	for index, line := range lines {
		trimmed := strings.TrimSpace(line)
		if len([]rune(trimmed)) >= 12 && strings.Trim(trimmed, "─━") == "" {
			start = index + 1
		}
	}
	return strings.Join(strings.Fields(strings.Join(lines[start:], "\n")), " ")
}

// permissionMatches 将官方当前审批类型和可见操作内容与原生检查点对应，不能把普通读取当成 Shell 审批。
func permissionMatches(screen string, call pendingCall) bool {
	if !permissionScreen(screen) {
		return false
	}
	panel := permissionPanel(screen)
	text := func(key string) string { return strings.Join(strings.Fields(stringValue(call.args[key])), " ") }
	contains := func(value string) bool { return value != "" && strings.Contains(panel, value) }
	switch strings.ToLower(call.name) {
	case "shell", "bash":
		command := text("command")
		return strings.Contains(panel, "Run this command") && command != "" && strings.Contains(panel, "$ "+command+" in ")
	case "calldynamictool", "mcp":
		return strings.Contains(panel, "Run this MCP tool?") && text("namespace") != "" && text("toolName") != "" && contains(text("namespace")+": "+text("toolName"))
	case "write", "strreplace", "edit":
		path := text("path")
		if path == "" {
			path = text("file_path")
		}
		return strings.Contains(panel, "Write to this file?") && path != "" && (contains(path) || contains(filepath.Base(path)))
	case "delete":
		path := text("path")
		return strings.Contains(panel, "Delete this file?") && (contains(path) || path != "" && contains(filepath.Base(path)))
	case "websearch":
		return strings.Contains(panel, "Allow this web search?") && (contains(text("search_term")) || contains(text("query")))
	case "webfetch":
		return strings.Contains(panel, "Allow this web fetch?") && contains(text("url"))
	default:
		return strings.HasPrefix(strings.ToLower(call.name), "mcp_") && strings.Contains(panel, "Run this MCP tool?")
	}
}

// visiblePermission 只在可见审批与一个待处理调用唯一对应时返回其身份，重复命令或同时多个 MCP 不猜测归属。
func visiblePermission(screen string, pending []pendingCall) (pendingCall, bool) {
	var selected pendingCall
	for _, call := range pending {
		if !permissionMatches(screen, call) {
			continue
		}
		if selected.id != "" {
			return pendingCall{}, false
		}
		selected = call
	}
	return selected, selected.id != ""
}

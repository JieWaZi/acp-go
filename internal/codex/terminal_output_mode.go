package codex

import acp "github.com/coder/acp-go-sdk"

// terminalOutputMode 表示客户端协商的 ACP 终端输出扩展键。
// 该类型薄移植 fixed upstream TerminalOutputMode.ts，不引入第二套输出协议。
type terminalOutputMode string

const (
	// terminalOutputModeFull 表示客户端明确支持 terminal_output 快照键。
	terminalOutputModeFull terminalOutputMode = "terminal_output"
	// terminalOutputModeDelta 表示默认兼容 terminal_output_delta 增量键。
	terminalOutputModeDelta terminalOutputMode = "terminal_output_delta"
)

// resolveTerminalOutputMode 按 upstream 只接受 `_meta.terminal_output == true`。
// 缺失、false 或非布尔值全部保持 legacy delta，避免向旧客户端发送未知键。
func resolveTerminalOutputMode(capabilities acp.ClientCapabilities) terminalOutputMode {
	terminalOutput, ok := capabilities.Meta["terminal_output"].(bool)
	if ok && terminalOutput {
		return terminalOutputModeFull
	}
	return terminalOutputModeDelta
}

// createTerminalOutputMeta 生成与协商模式精确对应的单个 terminal output 扩展键。
func createTerminalOutputMeta(
	mode terminalOutputMode,
	terminalID string,
	data string,
) map[string]any {
	if mode != terminalOutputModeFull {
		mode = terminalOutputModeDelta
	}
	return map[string]any{
		string(mode): map[string]any{
			"data":        data,
			"terminal_id": terminalID,
		},
	}
}

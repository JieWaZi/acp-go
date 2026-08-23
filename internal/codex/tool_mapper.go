package codex

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"acp-go/agents/codex/protocol"

	acp "github.com/coder/acp-go-sdk"
)

// shellPrefixPattern 对应 upstream CommandUtils.stripShellPrefix 的 shell 前缀规则。
var shellPrefixPattern = regexp.MustCompile(`^(/bin/)?(bash|zsh|sh)[[:space:]]+(-[lc]+[[:space:]]+)?`)

// toolMapper 将生成协议的 typed ThreadItem 映射为 ACP SDK tool call 更新。
type toolMapper struct{}

// mapStarted 根据 ThreadItem discriminator 创建 tool_call；非 V1 工具返回 nil。
func (toolMapper) mapStarted(item protocol.ThreadItem) (*acp.SessionUpdate, error) {
	switch item.Type {
	case protocol.CommandExecution:
		update, err := mapCommandStarted(item)
		return &update, err
	case protocol.FileChange:
		update, err := mapFileStarted(item)
		return &update, err
	case protocol.MCPToolCall:
		update, err := mapMCPStarted(item)
		return &update, err
	default:
		return nil, nil
	}
}

// mapCompleted 根据 ThreadItem discriminator 创建 tool_call_update；非 V1 工具返回 nil。
func (toolMapper) mapCompleted(item protocol.ThreadItem) (*acp.SessionUpdate, error) {
	switch item.Type {
	case protocol.CommandExecution:
		update, err := mapCommandCompleted(item)
		return &update, err
	case protocol.FileChange:
		status, err := mapToolStatus(item.Status)
		if err != nil {
			return nil, err
		}
		update := acp.UpdateToolCall(acp.ToolCallId(item.ID), acp.WithUpdateStatus(status))
		return &update, nil
	case protocol.MCPToolCall:
		update, err := mapMCPCompleted(item)
		return &update, err
	default:
		return nil, nil
	}
}

// mapCommandStarted 复用 SDK StartToolCall，并保留 upstream 的解析命令与终端两条分支。
func mapCommandStarted(item protocol.ThreadItem) (acp.SessionUpdate, error) {
	status, err := mapToolStatus(item.Status)
	if err != nil {
		return acp.SessionUpdate{}, err
	}
	if len(item.CommandActions) == 1 {
		return mapCommandAction(item.ID, status, item.Cwd, item.CommandActions[0]), nil
	}
	command := stringValue(item.Command)
	cwd := stringValue(item.Cwd)
	update := acp.StartToolCall(
		acp.ToolCallId(item.ID),
		stripShellPrefix(command),
		acp.WithStartKind(acp.ToolKindExecute),
		acp.WithStartStatus(status),
		acp.WithStartContent([]acp.ToolCallContent{acp.ToolTerminalRef(item.ID)}),
		acp.WithStartRawInput(map[string]any{"command": command, "cwd": cwd}),
	)
	update.ToolCall.Meta = terminalInfoMeta(item.ID, cwd)
	return update, nil
}

// mapCommandAction 等价移植 upstream 对 read/search/listFiles/unknown action 的展示语义。
func mapCommandAction(
	id string,
	status acp.ToolCallStatus,
	cwd *string,
	action protocol.CommandAction,
) acp.SessionUpdate {
	switch action.Type {
	case protocol.CommandActionTypeRead:
		path := stringValue(action.Path)
		return acp.StartToolCall(
			acp.ToolCallId(id),
			fmt.Sprintf("Read file '%s'", path),
			acp.WithStartStatus(status),
			acp.WithStartKind(acp.ToolKindRead),
			acp.WithStartLocations([]acp.ToolCallLocation{{Path: path}}),
		)
	case protocol.CommandActionTypeSearch:
		return acp.StartToolCall(
			acp.ToolCallId(id),
			searchTitle(action.Query, action.Path),
			acp.WithStartStatus(status),
			acp.WithStartKind(acp.ToolKindSearch),
		)
	case protocol.ListFiles:
		title := "List files"
		if action.Path != nil && *action.Path != "" {
			title = fmt.Sprintf("List files in '%s'", *action.Path)
		}
		return acp.StartToolCall(
			acp.ToolCallId(id), title,
			acp.WithStartStatus(status), acp.WithStartKind(acp.ToolKindRead),
		)
	default:
		command := action.Command
		cwdValue := stringValue(cwd)
		update := acp.StartToolCall(
			acp.ToolCallId(id), stripShellPrefix(command),
			acp.WithStartStatus(status),
			acp.WithStartKind(acp.ToolKindExecute),
			acp.WithStartContent([]acp.ToolCallContent{acp.ToolTerminalRef(id)}),
			acp.WithStartRawInput(map[string]any{"command": command, "cwd": cwdValue}),
		)
		update.ToolCall.Meta = terminalInfoMeta(id, cwdValue)
		return update
	}
}

// mapCommandCompleted 映射命令最终状态和聚合输出，不在此重复 start 内容。
func mapCommandCompleted(item protocol.ThreadItem) (acp.SessionUpdate, error) {
	status, err := mapToolStatus(item.Status)
	if err != nil {
		return acp.SessionUpdate{}, err
	}
	rawOutput := map[string]any{
		"formatted_output": stringValue(item.AggregatedOutput),
		"exit_code":        item.ExitCode,
	}
	// upstream JSON 对可选 exitCode 输出标量或 null；保留 int64 标量便于 SDK 直接编码。
	if item.ExitCode != nil {
		rawOutput["exit_code"] = *item.ExitCode
	}
	return acp.UpdateToolCall(
		acp.ToolCallId(item.ID),
		acp.WithUpdateStatus(status),
		acp.WithUpdateRawOutput(rawOutput),
	), nil
}

// mapCommandOutputDelta 使用 upstream terminal_output_delta 元数据流式传递原始增量。
func mapCommandOutputDelta(params protocol.CommandExecutionOutputDeltaNotification) acp.SessionUpdate {
	update := acp.UpdateToolCall(acp.ToolCallId(params.ItemID))
	update.ToolCallUpdate.Meta = terminalOutputMeta(params.ItemID, params.Delta)
	return update
}

// mapTerminalInteraction 将 stdin 作为带换行的终端输出增量回显。
func mapTerminalInteraction(params protocol.TerminalInteractionNotification) acp.SessionUpdate {
	update := acp.UpdateToolCall(acp.ToolCallId(params.ItemID))
	update.ToolCallUpdate.Meta = terminalOutputMeta(params.ItemID, "\n"+params.Stdin+"\n")
	return update
}

// mapMCPStarted 映射 MCP 标题、typed raw input 与 MCP 身份元数据。
func mapMCPStarted(item protocol.ThreadItem) (acp.SessionUpdate, error) {
	status, err := mapToolStatus(item.Status)
	if err != nil {
		return acp.SessionUpdate{}, err
	}
	server := stringValue(item.Server)
	tool := stringValue(item.Tool)
	update := acp.StartToolCall(
		acp.ToolCallId(item.ID),
		fmt.Sprintf("mcp.%s.%s", server, tool),
		acp.WithStartKind(acp.ToolKindExecute),
		acp.WithStartStatus(status),
		acp.WithStartRawInput(mcpRawInput(server, tool, item.Arguments)),
	)
	update.ToolCall.Meta = map[string]any{"is_mcp_tool_call": true}
	return update, nil
}

// mapMCPCompleted 映射 MCP 最终状态以及可选 result/error 原始输出。
func mapMCPCompleted(item protocol.ThreadItem) (acp.SessionUpdate, error) {
	status, err := mapToolStatus(item.Status)
	if err != nil {
		return acp.SessionUpdate{}, err
	}
	server := stringValue(item.Server)
	tool := stringValue(item.Tool)
	options := []acp.ToolCallUpdateOpt{
		acp.WithUpdateStatus(status),
		acp.WithUpdateRawInput(mcpRawInput(server, tool, item.Arguments)),
	}
	if item.Result != nil || item.Error != nil {
		options = append(options, acp.WithUpdateRawOutput(map[string]any{
			"result": item.Result,
			"error":  item.Error,
		}))
	}
	return acp.UpdateToolCall(acp.ToolCallId(item.ID), options...), nil
}

// mapMCPProgress 使用 upstream mcp_output_delta 元数据逐条保留重复进度消息。
func mapMCPProgress(params protocol.MCPToolCallProgressNotification) acp.SessionUpdate {
	update := acp.UpdateToolCall(acp.ToolCallId(params.ItemID))
	update.ToolCallUpdate.Meta = map[string]any{
		"mcp_output_delta": map[string]any{"data": params.Message},
	}
	return update
}

// mapFileStarted 对 add/delete 使用 SDK diff DTO；update/move 不解析 patch，仅保留 typed raw changes。
func mapFileStarted(item protocol.ThreadItem) (acp.SessionUpdate, error) {
	status, err := mapToolStatus(item.Status)
	if err != nil {
		return acp.SessionUpdate{}, err
	}
	content := make([]acp.ToolCallContent, 0, len(item.Changes))
	var rawChanges []protocol.ChangeElement
	for _, change := range item.Changes {
		switch change.Kind.Type {
		case protocol.Add:
			diff := acp.ToolDiffContent(change.Path, change.Diff)
			diff.Diff.Meta = map[string]any{"kind": "add"}
			content = append(content, diff)
		case protocol.Delete:
			diff := acp.ToolDiffContent(change.Path, "", change.Diff)
			diff.Diff.Meta = map[string]any{"kind": "delete"}
			content = append(content, diff)
		case protocol.Update:
			// Go V1 没有 upstream npm diff 的等价依赖，禁止自行实现 patch parser。
			rawChanges = append(rawChanges, change)
		default:
			rawChanges = append(rawChanges, change)
		}
	}
	options := []acp.ToolCallStartOpt{
		acp.WithStartKind(acp.ToolKindEdit),
		acp.WithStartStatus(status),
		acp.WithStartContent(content),
	}
	if len(rawChanges) > 0 {
		options = append(options, acp.WithStartRawInput(map[string]any{"changes": rawChanges}))
	}
	return acp.StartToolCall(acp.ToolCallId(item.ID), "Editing files", options...), nil
}

// mapFilePatchUpdated 保留 app-server 的 typed raw patch 更新，不尝试生成不可靠 rich diff。
func mapFilePatchUpdated(params protocol.FileChangePatchUpdatedNotification) acp.SessionUpdate {
	return acp.UpdateToolCall(
		acp.ToolCallId(params.ItemID),
		acp.WithUpdateRawInput(map[string]any{"changes": params.Changes}),
	)
}

// mapToolStatus 将 app-server item 状态映射为 ACP SDK 状态枚举。
func mapToolStatus(status *string) (acp.ToolCallStatus, error) {
	if status == nil {
		return acp.ToolCallStatusPending, nil
	}
	switch *status {
	case "inProgress":
		return acp.ToolCallStatusInProgress, nil
	case "completed":
		return acp.ToolCallStatusCompleted, nil
	case "failed", "declined":
		return acp.ToolCallStatusFailed, nil
	default:
		return "", fmt.Errorf("unknown Codex tool status %q", *status)
	}
}

// mcpRawInput 保留生成协议 typed arguments 的原始 JSON，避免重新定义 MCP DTO。
func mcpRawInput(server, tool string, arguments json.RawMessage) map[string]any {
	return map[string]any{"server": server, "tool": tool, "arguments": arguments}
}

// terminalInfoMeta 生成 upstream terminal_info 扩展元数据。
func terminalInfoMeta(terminalID, cwd string) map[string]any {
	return map[string]any{
		"terminal_info": map[string]any{"cwd": cwd, "terminal_id": terminalID},
	}
}

// terminalOutputMeta 生成 upstream terminal_output_delta 扩展元数据。
func terminalOutputMeta(terminalID, data string) map[string]any {
	return map[string]any{
		"terminal_output_delta": map[string]any{"data": data, "terminal_id": terminalID},
	}
}

// searchTitle 等价移植 upstream 对 query/path 四种组合的标题规则。
func searchTitle(query, path *string) string {
	queryValue := stringValue(query)
	pathValue := stringValue(path)
	switch {
	case queryValue != "" && pathValue != "":
		return fmt.Sprintf("Search for '%s' in %s", queryValue, pathValue)
	case queryValue != "":
		return fmt.Sprintf("Search for '%s'", queryValue)
	case pathValue != "":
		return fmt.Sprintf("Search in '%s'", pathValue)
	default:
		return "Search"
	}
}

// stripShellPrefix 等价移植 upstream CommandUtils，去除常见 shell 包装和成对单引号。
func stripShellPrefix(command string) string {
	withoutShell := shellPrefixPattern.ReplaceAllString(command, "")
	if strings.HasPrefix(withoutShell, "'") && strings.HasSuffix(withoutShell, "'") {
		return withoutShell[1 : len(withoutShell)-1]
	}
	return withoutShell
}

// stringValue 将可选 string 安全转为 upstream 使用的空字符串默认值。
func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

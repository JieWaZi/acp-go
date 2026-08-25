package codex

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/JieWaZi/acp-go/pkg/codex/protocol"

	acp "github.com/coder/acp-go-sdk"
)

// shellPrefixPattern 匹配常见 shell 包装前缀。
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
	case protocol.WebSearch:
		update, err := mapWebSearchStarted(item)
		return &update, err
	case protocol.ImageView:
		update := mapImageView(item)
		return &update, nil
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
	case protocol.WebSearch:
		update, err := mapWebSearchCompleted(item)
		return &update, err
	default:
		return nil, nil
	}
}

// mapCommandStarted 使用 SDK StartToolCall，并区分已解析命令与终端命令。
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

// mapCommandAction 把 read、search、listFiles 与未知动作转换为工具展示信息。
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

// commandExecutionUsesTerminalOutput 判断命令是否使用 ACP terminal 输出分支。
func commandExecutionUsesTerminalOutput(item protocol.ThreadItem) bool {
	if len(item.CommandActions) != 1 {
		return true
	}
	return item.CommandActions[0].Type == protocol.CommandActionTypeUnknown
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
	// 可选 exitCode 可能是标量或 null；保留 int64 标量便于 SDK 直接编码。
	if item.ExitCode != nil {
		rawOutput["exit_code"] = *item.ExitCode
	}
	return acp.UpdateToolCall(
		acp.ToolCallId(item.ID),
		acp.WithUpdateStatus(status),
		acp.WithUpdateRawOutput(rawOutput),
	), nil
}

// mapCommandOutputDelta 使用协商的 terminal output 元数据流式传递原始增量。
func mapCommandOutputDelta(
	params protocol.CommandExecutionOutputDeltaNotification,
	mode terminalOutputMode,
) acp.SessionUpdate {
	update := acp.UpdateToolCall(acp.ToolCallId(params.ItemID))
	update.ToolCallUpdate.Meta = createTerminalOutputMeta(mode, params.ItemID, params.Delta)
	return update
}

// mapTerminalInteraction 将 stdin 作为带换行的终端输出增量回显。
func mapTerminalInteraction(
	params protocol.TerminalInteractionNotification,
	mode terminalOutputMode,
) acp.SessionUpdate {
	update := acp.UpdateToolCall(acp.ToolCallId(params.ItemID))
	update.ToolCallUpdate.Meta = createTerminalOutputMeta(mode, params.ItemID, "\n"+params.Stdin+"\n")
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

// mapMCPHistory 在单条 completed tool_call 中同时保留 MCP 输入与输出。
func mapMCPHistory(item protocol.ThreadItem) (acp.SessionUpdate, error) {
	status, err := mapToolStatus(item.Status)
	if err != nil {
		return acp.SessionUpdate{}, err
	}
	server := stringValue(item.Server)
	tool := stringValue(item.Tool)
	options := []acp.ToolCallStartOpt{
		acp.WithStartKind(acp.ToolKindExecute),
		acp.WithStartStatus(status),
		acp.WithStartRawInput(mcpRawInput(server, tool, item.Arguments)),
	}
	if item.Result != nil || item.Error != nil {
		options = append(options, acp.WithStartRawOutput(map[string]any{
			"result": item.Result,
			"error":  item.Error,
		}))
	}
	update := acp.StartToolCall(acp.ToolCallId(item.ID), fmt.Sprintf("mcp.%s.%s", server, tool), options...)
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

// mapWebSearchStarted 将 Codex 网页操作转换为 ACP search ToolCall。
func mapWebSearchStarted(item protocol.ThreadItem) (acp.SessionUpdate, error) {
	return acp.StartToolCall(
		acp.ToolCallId(item.ID),
		webSearchTitle(item),
		acp.WithStartKind(acp.ToolKindSearch),
		acp.WithStartStatus(acp.ToolCallStatusInProgress),
		acp.WithStartRawInput(webSearchRawInput(item)),
	), nil
}

// mapWebSearchCompleted 保留网页操作终态、标题和输入。
func mapWebSearchCompleted(item protocol.ThreadItem) (acp.SessionUpdate, error) {
	return acp.UpdateToolCall(
		acp.ToolCallId(item.ID),
		acp.WithUpdateStatus(acp.ToolCallStatusCompleted),
		acp.WithUpdateTitle(webSearchTitle(item)),
		acp.WithUpdateRawInput(webSearchRawInput(item)),
	), nil
}

// mapWebSearchHistory 把历史网页操作恢复为单条 completed ToolCall。
func mapWebSearchHistory(item protocol.ThreadItem) acp.SessionUpdate {
	return acp.StartToolCall(
		acp.ToolCallId(item.ID),
		webSearchTitle(item),
		acp.WithStartKind(acp.ToolKindSearch),
		acp.WithStartStatus(acp.ToolCallStatusCompleted),
		acp.WithStartRawInput(map[string]any{
			"query":  item.Query,
			"action": item.Action,
		}),
	)
}

// mapImageView 将本地图片查看转换为单条 completed ACP read ToolCall。
func mapImageView(item protocol.ThreadItem) acp.SessionUpdate {
	path := stringValue(item.Path)
	options := []acp.ToolCallStartOpt{
		acp.WithStartKind(acp.ToolKindRead),
		acp.WithStartStatus(acp.ToolCallStatusCompleted),
		acp.WithStartRawInput(map[string]any{"path": path}),
	}
	if path != "" {
		options = append(
			options,
			acp.WithStartContent([]acp.ToolCallContent{
				acp.ToolContent(acp.ResourceLinkBlock(path, path)),
			}),
			acp.WithStartLocations([]acp.ToolCallLocation{{Path: path}}),
		)
	}
	return acp.StartToolCall(
		acp.ToolCallId(item.ID),
		imageViewTitle(path),
		options...,
	)
}

// webSearchRawInput 保存 ACP Client 展示网页操作所需的稳定字段。
func webSearchRawInput(item protocol.ThreadItem) map[string]any {
	return map[string]any{
		"type":   string(item.Type),
		"id":     item.ID,
		"query":  item.Query,
		"action": item.Action,
	}
}

// webSearchTitle 根据动作生成不包含结果正文的可读标题。
func webSearchTitle(item protocol.ThreadItem) string {
	if item.Action == nil {
		if query := stringValue(item.Query); query != "" {
			return fmt.Sprintf("Web search: %s", query)
		}
		return "Web search"
	}
	action := item.Action
	switch action.Type {
	case protocol.WebSearchActionTypeSearch:
		query := stringValue(action.Query)
		if query == "" && len(action.Queries) > 0 {
			queries := make([]string, 0, len(action.Queries))
			for _, candidate := range action.Queries {
				if candidate != "" {
					queries = append(queries, candidate)
				}
			}
			query = strings.Join(queries, ", ")
		}
		if query == "" {
			query = stringValue(item.Query)
		}
		if query != "" {
			return fmt.Sprintf("Web search: %s", query)
		}
	case protocol.OpenPage:
		if rawURL := stringValue(action.URL); rawURL != "" {
			return fmt.Sprintf("Open page: %s", rawURL)
		}
		return "Open page"
	case protocol.FindInPage:
		pattern := stringValue(action.Pattern)
		rawURL := stringValue(action.URL)
		return strings.TrimSpace(fmt.Sprintf(
			"Find in page%s%s",
			formatOptionalQuoted(" for ", pattern),
			formatOptional(" in ", rawURL),
		))
	}
	return "Web search"
}

// formatOptional 只在 value 非空时拼接前缀。
func formatOptional(prefix, value string) string {
	if value == "" {
		return ""
	}
	return prefix + value
}

// formatOptionalQuoted 只在 value 非空时拼接带单引号的前缀。
func formatOptionalQuoted(prefix, value string) string {
	if value == "" {
		return ""
	}
	return prefix + "'" + value + "'"
}

// imageViewTitle 返回图片路径可用时的 ACP 工具标题。
func imageViewTitle(path string) string {
	if path == "" {
		return "View Image"
	}
	return fmt.Sprintf("View Image %s", path)
}

// mapMCPProgress 使用 mcp_output_delta 元数据逐条保留重复进度消息。
func mapMCPProgress(params protocol.MCPToolCallProgressNotification) acp.SessionUpdate {
	update := acp.UpdateToolCall(acp.ToolCallId(params.ItemID))
	update.ToolCallUpdate.Meta = map[string]any{
		"mcp_output_delta": map[string]any{"data": params.Message},
	}
	return update
}

// mapFileStarted 参考 Codex ACP upstream，把可验证的文件变更转换为标准 ACP diff。
func mapFileStarted(item protocol.ThreadItem) (acp.SessionUpdate, error) {
	status, err := mapToolStatus(item.Status)
	if err != nil {
		return acp.SessionUpdate{}, err
	}
	content := make([]acp.ToolCallContent, 0, len(item.Changes))
	var rawChanges []protocol.ChangeElement
	for _, change := range item.Changes {
		if diff, ok := createFileDiffContent(change); ok {
			content = append(content, diff)
		} else {
			// 无法验证的补丁只保留原始 typed change，不能伪造 oldText/newText。
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

// terminalInfoMeta 生成 terminal_info 扩展元数据。
func terminalInfoMeta(terminalID, cwd string) map[string]any {
	return map[string]any{
		"terminal_info": map[string]any{"cwd": cwd, "terminal_id": terminalID},
	}
}

// terminalCompletionMeta 生成命令完成时的输出回退和退出元数据。
func terminalCompletionMeta(
	mode terminalOutputMode,
	terminalID string,
	aggregatedOutput string,
	exitCode *int64,
	hadOutput bool,
) map[string]any {
	var exitCodeValue any
	if exitCode != nil {
		exitCodeValue = *exitCode
	}
	meta := map[string]any{
		"terminal_exit": map[string]any{
			"exit_code":   exitCodeValue,
			"signal":      nil,
			"terminal_id": terminalID,
		},
	}
	if !hadOutput && aggregatedOutput != "" {
		for key, value := range createTerminalOutputMeta(mode, terminalID, aggregatedOutput) {
			meta[key] = value
		}
	}
	return meta
}

// searchTitle 根据 query 与 path 的组合生成稳定标题。
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

// stripShellPrefix 去除常见 shell 包装和成对单引号。
func stripShellPrefix(command string) string {
	withoutShell := shellPrefixPattern.ReplaceAllString(command, "")
	if strings.HasPrefix(withoutShell, "'") && strings.HasSuffix(withoutShell, "'") {
		return withoutShell[1 : len(withoutShell)-1]
	}
	return withoutShell
}

// stringValue 将可选 string 安全转换为空字符串默认值。
func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

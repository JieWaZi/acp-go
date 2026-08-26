package claude

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"

	acp "github.com/coder/acp-go-sdk"
)

// toolInfo 保存 Claude 工具映射后的标准 ACP 展示信息。
type toolInfo struct {
	// Title 是描述当前工具动作的人类可读标题。
	Title string
	// Kind 是 ACP 标准工具类别。
	Kind acp.ToolKind
	// Content 是结构化文本、diff 等 ACP 工具内容。
	Content []acp.ToolCallContent
	// Locations 是工具读取或修改的文件位置。
	Locations []acp.ToolCallLocation
	// RawInput 是需要替代原始 Claude 输入的标准化 ACP 输入。
	RawInput any
	// Meta 是 ACP 扩展元数据，不携带产品层展示文案。
	Meta map[string]any
}

// diffToolResponse 保存 Claude PostToolUse 或 tool_use_result 的结构化文件补丁。
type diffToolResponse struct {
	// FilePath 是补丁对应的完整文件路径。
	FilePath string `json:"filePath"`
	// StructuredPatch 是 Claude 已解析的一个或多个变更区间。
	StructuredPatch []diffToolResponseHunk `json:"structuredPatch"`
}

// diffToolResponseHunk 保存 Claude 结构化补丁中的单个 hunk。
type diffToolResponseHunk struct {
	// OldStart 是旧文件 hunk 的 1-based 起始行。
	OldStart int `json:"oldStart"`
	// OldLines 是旧文件 hunk 的行数。
	OldLines int `json:"oldLines"`
	// NewStart 是新文件 hunk 的 1-based 起始行。
	NewStart int `json:"newStart"`
	// NewLines 是新文件 hunk 的行数。
	NewLines int `json:"newLines"`
	// Lines 是包含 unified diff 前缀的 hunk 行。
	Lines []string `json:"lines"`
}

// toolInfoFromToolUse 映射标准工具语义，并附加 Claude 元数据。
func toolInfoFromToolUse(name string, input any, cwd string, home string) toolInfo {
	info := mappedToolInfoFromToolUse(name, input)
	if info.Meta == nil {
		info.Meta = make(map[string]any)
	}
	info.Meta["claudeCode"] = claudeCodeMetaFromToolUse(name, input, cwd, home)
	return info
}

// mappedToolInfoFromToolUse 映射内置工具的标准 ACP 展示语义。
func mappedToolInfoFromToolUse(name string, input any) toolInfo {
	if server, tool, ok := claudeMCPToolIdentity(name); ok {
		return toolInfo{
			Title: fmt.Sprintf("mcp.%s.%s", server, tool),
			Kind:  acp.ToolKindExecute,
			RawInput: map[string]any{
				"server":    server,
				"tool":      tool,
				"arguments": input,
			},
			Meta: map[string]any{"is_mcp_tool_call": true},
		}
	}
	object, _ := input.(map[string]any)
	switch name {
	case "Agent", "Task":
		title := objectString(object, "description")
		if title == "" {
			title = "Task"
		}
		return toolInfo{
			Title:   title,
			Kind:    acp.ToolKindThink,
			Content: optionalTextContent(objectString(object, "prompt")),
		}
	case "Bash":
		title := objectString(object, "command")
		if title == "" {
			title = "Terminal"
		}
		return toolInfo{
			Title:   title,
			Kind:    acp.ToolKindExecute,
			Content: optionalTextContent(objectString(object, "description")),
		}
	case "Read":
		path := objectString(object, "file_path")
		displayPath := path
		if displayPath == "" {
			displayPath = "File"
		}
		offset := objectPositiveInt(object, "offset")
		limit := objectPositiveInt(object, "limit")
		rangeLabel := ""
		if limit > 0 {
			if offset == 0 {
				offset = 1
			}
			rangeLabel = fmt.Sprintf(" (%d - %d)", offset, offset+limit-1)
		} else if offset > 0 {
			rangeLabel = fmt.Sprintf(" (from line %d)", offset)
		}
		return toolInfo{
			Title:     "Read " + displayPath + rangeLabel,
			Kind:      acp.ToolKindRead,
			Locations: fileLocations(path, offset),
		}
	case "Write":
		path := objectString(object, "file_path")
		content := objectString(object, "content")
		info := toolInfo{Title: "Preparing file…", Kind: acp.ToolKindEdit, Locations: fileLocations(path, 0)}
		if path != "" {
			info.Title = "Write " + path
			info.Content = []acp.ToolCallContent{acp.ToolDiffContent(path, content)}
		} else {
			info.Content = optionalTextContent(content)
		}
		return info
	case "Edit":
		path := objectString(object, "file_path")
		title := "Edit"
		if path != "" {
			title += " " + path
		}
		info := toolInfo{Title: title, Kind: acp.ToolKindEdit, Locations: fileLocations(path, 0)}
		oldText := objectString(object, "old_string")
		newText := objectString(object, "new_string")
		if path != "" && (oldText != "" || newText != "") {
			info.Content = []acp.ToolCallContent{acp.ToolDiffContent(path, newText, oldText)}
		}
		return info
	case "NotebookEdit":
		path := objectString(object, "notebook_path")
		title := "Edit notebook"
		if path != "" {
			title += " " + path
		}
		return toolInfo{Title: title, Kind: acp.ToolKindEdit, Locations: fileLocations(path, 0)}
	case "Glob":
		path := objectString(object, "path")
		pattern := objectString(object, "pattern")
		title := "Find"
		if path != "" {
			title += " `" + path + "`"
		}
		if pattern != "" {
			title += " `" + pattern + "`"
		}
		return toolInfo{Title: title, Kind: acp.ToolKindSearch, Locations: fileLocations(path, 0)}
	case "Grep", "Search":
		return toolInfo{Title: grepTitle(object), Kind: acp.ToolKindSearch}
	case "WebFetch":
		rawURL := objectString(object, "url")
		title := "Fetch"
		if rawURL != "" {
			title += " " + rawURL
		}
		return toolInfo{
			Title:   title,
			Kind:    acp.ToolKindFetch,
			Content: optionalTextContent(objectString(object, "prompt")),
		}
	case "WebSearch":
		return toolInfo{Title: claudeWebSearchTitle(object), Kind: acp.ToolKindFetch}
	case "TodoWrite":
		return toolInfo{Title: todoWriteTitle(object), Kind: acp.ToolKindThink}
	case "ReportFindings", "TaskCreate", "TaskUpdate", "TaskList", "TaskGet":
		return toolInfo{Title: taskToolTitle(name, object), Kind: acp.ToolKindThink}
	case "EnterPlanMode":
		return toolInfo{Title: "Enter plan mode", Kind: acp.ToolKindSwitchMode}
	case "ExitPlanMode":
		return toolInfo{
			Title:   "Ready to code?",
			Kind:    acp.ToolKindSwitchMode,
			Content: optionalTextContent(objectString(object, "plan")),
		}
	case "Skill":
		skill := objectString(object, "skill")
		title := "Load skill"
		if skill != "" {
			title += ": " + skill
		}
		return toolInfo{Title: title, Kind: acp.ToolKindOther}
	case "AskUserQuestion":
		questions := questionTexts(object["questions"])
		title := "Asking for your input"
		if len(questions) == 1 {
			title = questions[0]
		}
		return toolInfo{Title: title, Kind: acp.ToolKindOther, Content: textContents(questions)}
	default:
		title := name
		if title == "" {
			title = "Unknown Tool"
		}
		return toolInfo{Title: title, Kind: acp.ToolKindOther}
	}
}

// claudeCodeMetaFromToolUse Claude Code 工具扩展元数据。
func claudeCodeMetaFromToolUse(name string, input any, cwd string, home string) map[string]any {
	meta := map[string]any{"toolName": name}
	object, _ := input.(map[string]any)
	if name == "Bash" {
		if title := objectString(object, "description"); title != "" {
			meta["title"] = title
		}
	}
	if name == "Agent" || name == "Task" {
		meta["subagent"] = true
	}
	if name == "Skill" {
		if skill := objectString(object, "skill"); skill != "" {
			meta["skill"] = skill
			if skillPath := resolveClaudeSkillPath(skill, cwd, home); skillPath != "" {
				meta["skillPath"] = skillPath
			}
		}
	}
	return meta
}

// resolveClaudeSkillPath 按项目、目录作用域、插件和用户目录布局查找 SKILL.md。
func resolveClaudeSkillPath(skillName string, cwd string, home string) string {
	if cwd == "" {
		return ""
	}
	scope, name := "", skillName
	if separator := strings.IndexByte(skillName, ':'); separator >= 0 {
		scope, name = skillName[:separator], skillName[separator+1:]
	}
	if name == "" {
		return ""
	}
	candidates := make([]string, 0, 7)
	addCandidates := func(base string) {
		for _, container := range [...]string{".claude/skills", ".agents/skills"} {
			candidates = append(candidates, filepath.Join(base, container, name, "SKILL.md"))
		}
	}
	if scope != "" {
		addCandidates(filepath.Join(cwd, scope))
		candidates = append(candidates, filepath.Join(cwd, ".claude/plugins", scope, "skills", name, "SKILL.md"))
	}
	addCandidates(cwd)
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	if home != "" {
		addCandidates(home)
	}
	for _, candidate := range candidates {
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	return ""
}

// claudeUserHomeDirectory 返回与 Claude 子进程配置一致的用户目录。
func claudeUserHomeDirectory(environment []string) string {
	if home := claudeEnvironmentValue(environment, "HOME"); home != "" {
		return home
	}
	home, _ := os.UserHomeDir()
	return home
}

// claudeMCPToolIdentity 解析 Claude 使用的 mcp__server__tool 标识。
func claudeMCPToolIdentity(name string) (string, string, bool) {
	parts := strings.Split(name, "__")
	if len(parts) < 3 || parts[0] != "mcp" || parts[1] == "" {
		return "", "", false
	}
	tool := strings.Join(parts[2:], "__")
	if tool == "" {
		return "", "", false
	}
	return parts[1], tool, true
}

// objectString 读取 map 中的非空字符串字段。
func objectString(object map[string]any, key string) string {
	value, _ := object[key].(string)
	return value
}

// objectPositiveInt 读取 JSON number 编码的正整数。
func objectPositiveInt(object map[string]any, key string) int {
	switch value := object[key].(type) {
	case float64:
		if value > 0 && value <= float64(math.MaxInt) && value == float64(int(value)) {
			return int(value)
		}
	case int:
		if value > 0 {
			return value
		}
	case int64:
		if value > 0 && value <= int64(math.MaxInt) {
			return int(value)
		}
	}
	return 0
}

// fileLocations 创建规范化文件位置；空路径不生成位置。
func fileLocations(path string, line int) []acp.ToolCallLocation {
	if path == "" {
		return nil
	}
	location := acp.ToolCallLocation{Path: filepath.Clean(path)}
	if line > 0 {
		location.Line = acp.Ptr(line)
	}
	return []acp.ToolCallLocation{location}
}

// optionalTextContent 把非空文本转换为 ACP tool content。
func optionalTextContent(value string) []acp.ToolCallContent {
	if value == "" {
		return nil
	}
	return []acp.ToolCallContent{acp.ToolContent(acp.TextBlock(value))}
}

// textContents 按顺序转换多段 ACP 文本内容。
func textContents(values []string) []acp.ToolCallContent {
	content := make([]acp.ToolCallContent, 0, len(values))
	for _, value := range values {
		content = append(content, acp.ToolContent(acp.TextBlock(value)))
	}
	return content
}

// toolDiffUpdateFromResult 把 structuredPatch 转成标准 ACP diff。
func toolDiffUpdateFromResult(
	raw json.RawMessage,
) ([]acp.ToolCallContent, []acp.ToolCallLocation) {
	if len(raw) == 0 {
		return nil, nil
	}
	var response diffToolResponse
	if err := json.Unmarshal(raw, &response); err != nil ||
		response.FilePath == "" || len(response.StructuredPatch) == 0 {
		return nil, nil
	}
	content := make([]acp.ToolCallContent, 0, len(response.StructuredPatch))
	locations := make([]acp.ToolCallLocation, 0, len(response.StructuredPatch))
	for _, hunk := range response.StructuredPatch {
		oldLines := make([]string, 0, len(hunk.Lines))
		newLines := make([]string, 0, len(hunk.Lines))
		valid := true
		for _, line := range hunk.Lines {
			if line == "" {
				valid = false
				break
			}
			switch line[0] {
			case '-':
				oldLines = append(oldLines, line[1:])
			case '+':
				newLines = append(newLines, line[1:])
			case ' ':
				oldLines = append(oldLines, line[1:])
				newLines = append(newLines, line[1:])
			default:
				valid = false
			}
			if !valid {
				break
			}
		}
		if !valid || (len(oldLines) == 0 && len(newLines) == 0) {
			continue
		}
		oldText := strings.Join(oldLines, "\n")
		newText := strings.Join(newLines, "\n")
		if oldText == "" {
			content = append(content, acp.ToolDiffContent(response.FilePath, newText))
		} else {
			content = append(content, acp.ToolDiffContent(response.FilePath, newText, oldText))
		}
		location := acp.ToolCallLocation{Path: response.FilePath}
		if hunk.NewStart > 0 {
			location.Line = acp.Ptr(hunk.NewStart)
		}
		locations = append(locations, location)
	}
	return content, locations
}

// stringSlice 读取开放 JSON 中的字符串数组。
func stringSlice(value any) []string {
	items, ok := value.([]any)
	if !ok {
		if values, typed := value.([]string); typed {
			return append([]string(nil), values...)
		}
		return nil
	}
	result := make([]string, 0, len(items))
	for _, item := range items {
		if text, ok := item.(string); ok && text != "" {
			result = append(result, text)
		}
	}
	return result
}

// grepTitle 生成接近 Claude CLI 的搜索命令标题。
func grepTitle(object map[string]any) string {
	title := "grep"
	if flag, _ := object["-i"].(bool); flag {
		title += " -i"
	}
	if pattern := objectString(object, "pattern"); pattern != "" {
		title += " \"" + pattern + "\""
	}
	if path := objectString(object, "path"); path != "" {
		title += " " + path
	}
	return title
}

// claudeWebSearchTitle 保留查询与域名过滤信息。
func claudeWebSearchTitle(object map[string]any) string {
	query := objectString(object, "query")
	title := "Web search"
	if query != "" {
		title = "\"" + query + "\""
	}
	if allowed := stringSlice(object["allowed_domains"]); len(allowed) > 0 {
		title += " (allowed: " + strings.Join(allowed, ", ") + ")"
	}
	if blocked := stringSlice(object["blocked_domains"]); len(blocked) > 0 {
		title += " (blocked: " + strings.Join(blocked, ", ") + ")"
	}
	return title
}

// todoWriteTitle 汇总 TODO 内容；坏结构使用稳定兜底标题。
func todoWriteTitle(object map[string]any) string {
	items, _ := object["todos"].([]any)
	labels := make([]string, 0, len(items))
	for _, item := range items {
		if todo, ok := item.(map[string]any); ok {
			if content := objectString(todo, "content"); content != "" {
				labels = append(labels, content)
			}
		}
	}
	if len(labels) == 0 {
		return "Update TODOs"
	}
	return "Update TODOs: " + strings.Join(labels, ", ")
}

// taskToolTitle 生成任务管理工具的稳定 ACP 标题。
func taskToolTitle(name string, object map[string]any) string {
	switch name {
	case "TaskCreate":
		if subject := objectString(object, "subject"); subject != "" {
			return "Create task: " + subject
		}
		return "Create task"
	case "TaskUpdate":
		if subject := objectString(object, "subject"); subject != "" {
			return "Update task: " + subject
		}
		return "Update task"
	case "TaskList":
		return "List tasks"
	case "TaskGet":
		return "Get task"
	default:
		return "Report findings"
	}
}

// questionTexts 提取 AskUserQuestion 的可展示问题。
func questionTexts(value any) []string {
	items, _ := value.([]any)
	questions := make([]string, 0, len(items))
	for _, item := range items {
		if question, ok := item.(map[string]any); ok {
			if text := objectString(question, "question"); text != "" {
				questions = append(questions, text)
			}
		}
	}
	return questions
}

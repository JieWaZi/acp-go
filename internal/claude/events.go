package claude

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"sort"
	"strings"

	"acp-go/agents/claude/protocol"
	acp "github.com/coder/acp-go-sdk"
)

// toolState 保存已发布工具调用的最小生命周期状态。
type toolState struct {
	// ID 是 ACP ToolCallID。
	ID string
	// Name 是 CLI 工具名称。
	Name string
	// Input 是工具原始输入。
	Input any
	// Completed 阻止迟到进度重新打开工具调用。
	Completed bool
}

// taskState 保存计划更新需要的单个任务快照。
type taskState struct {
	// ID 是 Session 内任务标识。
	ID string
	// Description 是任务说明。
	Description string
	// Status 是 CLI 任务状态。
	Status string
}

// handleMessage 按 transport 到达顺序处理一条 CLI 消息。
func (s *claudeSession) handleMessage(ctx context.Context, message protocol.Message) {
	var err error
	switch typed := message.(type) {
	case *protocol.SystemInitMessage:
		first := false
		s.initOnce.Do(func() {
			first = true
			s.initMessages <- typed
		})
		if !first {
			// 重复 init 用于配置状态刷新，不得阻塞唯一 stdout reader。
			s.syncSystemInit(*typed)
		}
	case *protocol.StreamEventMessage:
		err = s.handleStreamEvent(ctx, typed)
	case *protocol.AssistantMessage:
		err = s.handleAssistant(ctx, typed)
	case *protocol.UserMessage:
		err = s.handleUser(ctx, typed)
	case *protocol.ResultMessage:
		err = s.handleResult(ctx, typed)
	case *protocol.ToolProgressMessage:
		err = s.handleToolProgress(ctx, typed)
	case *protocol.TaskMessage:
		err = s.handleTask(ctx, typed)
	case *protocol.SystemMessage:
		err = s.handleSystem(ctx, typed)
	case *protocol.UnknownMessage:
		s.agent.logger.Debug("忽略未知 Claude 消息", "type", typed.MessageType(), "bytes", len(typed.RawJSON()))
	}
	if err != nil && !errors.Is(err, ErrClaudeConnectionNotReady) {
		s.agent.logger.Warn("处理 Claude 消息失败", "session_id", s.id, "type", message.MessageType(), "error", err)
	}
}

// syncSystemInit 更新 CLI 可自主变化的模型、权限和快速模式状态。
func (s *claudeSession) syncSystemInit(init protocol.SystemInitMessage) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if init.SessionID != s.id || s.closed {
		return
	}
	s.systemInit = init
	if init.Model != "" {
		s.configuration.model = init.Model
	}
	if init.PermissionMode != "" {
		s.configuration.mode = acp.SessionModeId(init.PermissionMode)
	}
	if init.FastModeState != "" {
		s.configuration.fast = init.FastModeState == "on" || init.FastModeState == "cooldown"
	}
}

// handleStreamEvent 映射文本、思考、工具开始和 usage 增量。
func (s *claudeSession) handleStreamEvent(ctx context.Context, message *protocol.StreamEventMessage) error {
	turn := s.activeTurn()
	if turn == nil {
		return nil
	}
	event := message.Event
	switch event.Type {
	case "content_block_start":
		if event.ContentBlock != nil && event.ContentBlock.Type == "tool_use" {
			return s.startTool(ctx, *event.ContentBlock)
		}
	case "content_block_delta":
		if event.Delta == nil {
			return nil
		}
		if event.Delta.Text != "" {
			turn.mu.Lock()
			turn.streamedText[event.Index] += event.Delta.Text
			turn.mu.Unlock()
			return s.sendAgentText(ctx, message.UUID, event.Delta.Text, false)
		}
		if event.Delta.Thinking != "" {
			turn.mu.Lock()
			turn.streamedThinking[event.Index] += event.Delta.Thinking
			turn.mu.Unlock()
			return s.sendAgentText(ctx, message.UUID, event.Delta.Thinking, true)
		}
	case "message_delta":
		if event.Usage != nil {
			return s.sendUsageUpdate(ctx, *event.Usage, 0)
		}
	}
	return nil
}

// handleAssistant 发送未被流式 delta 覆盖的尾部，并发布工具开始。
func (s *claudeSession) handleAssistant(ctx context.Context, message *protocol.AssistantMessage) error {
	turn := s.activeTurn()
	if turn == nil || (message.SessionID != "" && message.SessionID != s.id) {
		return nil
	}
	for index, block := range message.Message.Content {
		switch block.Type {
		case "text":
			turn.mu.Lock()
			streamed := turn.streamedText[index]
			turn.mu.Unlock()
			if remainder := unstreamedRemainder(block.Text, streamed); remainder != "" {
				if err := s.sendAgentText(ctx, message.UUID, remainder, false); err != nil {
					return err
				}
			}
		case "thinking":
			turn.mu.Lock()
			streamed := turn.streamedThinking[index]
			turn.mu.Unlock()
			if remainder := unstreamedRemainder(block.Thinking, streamed); remainder != "" {
				if err := s.sendAgentText(ctx, message.UUID, remainder, true); err != nil {
					return err
				}
			}
		case "tool_use":
			if err := s.startTool(ctx, block); err != nil {
				return err
			}
		}
	}
	return nil
}

// handleUser 匹配 prompt/steering echo，并完成工具结果。
func (s *claudeSession) handleUser(ctx context.Context, message *protocol.UserMessage) error {
	turn := s.activeTurn()
	if turn == nil || (message.SessionID != "" && message.SessionID != s.id) {
		return nil
	}
	// 工具结果拥有独立 UUID，必须先按 tool_use_id 处理，不能误当成未知 prompt echo 丢弃。
	for _, block := range message.Message.Content {
		if block.Type == "tool_result" {
			if err := s.completeTool(ctx, block); err != nil {
				return err
			}
		}
	}
	if message.UUID != "" && message.UUID != turn.id {
		turn.mu.Lock()
		_, steering := turn.steeringIDs[message.UUID]
		if steering {
			delete(turn.steeringIDs, message.UUID)
		}
		turn.mu.Unlock()
		if !steering {
			return nil
		}
	}
	return nil
}

// handleResult 生成 PromptResponse；steering turn 延迟到 idle 再完成。
func (s *claudeSession) handleResult(ctx context.Context, message *protocol.ResultMessage) error {
	turn := s.activeTurn()
	if turn == nil || (message.SessionID != "" && message.SessionID != s.id) {
		return nil
	}
	response, err := promptResponseFromResult(message)
	turn.mu.Lock()
	turn.gotResult = true
	cancelled := turn.cancelled
	steered := turn.steered
	if steered && err == nil {
		copy := response
		turn.steeredResult = &copy
	}
	turn.mu.Unlock()
	if cancelled {
		turn.drain()
		return nil
	}
	if err != nil {
		turn.settle(acp.PromptResponse{}, err)
		turn.drain()
		return nil
	}
	updateErr := s.sendUsageUpdate(ctx, message.Usage, contextWindow(message.ModelUsage))
	if steered {
		return updateErr
	}
	turn.settle(response, nil)
	turn.drain()
	return updateErr
}

// handleSystem 使用 idle 作为缺失 result 的终止保护，并完成 steering 周期。
func (s *claudeSession) handleSystem(_ context.Context, message *protocol.SystemMessage) error {
	if message.Subtype != "session_state_changed" || message.State != "idle" {
		return nil
	}
	turn := s.activeTurn()
	if turn == nil {
		return nil
	}
	turn.mu.Lock()
	gotResult := turn.gotResult
	cancelled := turn.cancelled
	steered := turn.steered
	steeredResult := turn.steeredResult
	turn.mu.Unlock()
	switch {
	case cancelled:
		turn.settle(acp.PromptResponse{StopReason: acp.StopReasonCancelled}, nil)
	case steered && steeredResult != nil:
		turn.settle(*steeredResult, nil)
	case !gotResult:
		turn.settle(acp.PromptResponse{}, ErrClaudeTurnEndedWithoutResult)
	}
	turn.drain()
	return nil
}

// handleToolProgress 只更新已发布且未完成的工具调用。
func (s *claudeSession) handleToolProgress(ctx context.Context, message *protocol.ToolProgressMessage) error {
	s.mu.Lock()
	tool := s.tools[message.ToolUseID]
	if tool == nil || tool.Completed {
		s.mu.Unlock()
		return nil
	}
	s.mu.Unlock()
	return s.agent.sendUpdate(ctx, s.id, acp.UpdateToolCall(
		acp.ToolCallId(message.ToolUseID),
		acp.WithUpdateRawOutput(map[string]any{"elapsedTimeSeconds": message.ElapsedTimeSeconds}),
	))
}

// handleTask 更新 Session 任务快照并发布完整 ACP plan。
func (s *claudeSession) handleTask(ctx context.Context, message *protocol.TaskMessage) error {
	if message.TaskID == "" {
		return nil
	}
	s.mu.Lock()
	task := s.tasks[message.TaskID]
	task.ID = message.TaskID
	if message.Description != "" {
		task.Description = message.Description
	}
	if message.Summary != "" && task.Description == "" {
		task.Description = message.Summary
	}
	if message.Status != "" {
		task.Status = message.Status
	}
	if message.Subtype == "task_started" && task.Status == "" {
		task.Status = "in_progress"
	}
	if message.Subtype == "task_notification" && task.Status == "" {
		task.Status = "completed"
	}
	s.tasks[message.TaskID] = task
	tasks := make([]taskState, 0, len(s.tasks))
	for _, item := range s.tasks {
		tasks = append(tasks, item)
	}
	s.mu.Unlock()
	sort.Slice(tasks, func(i, j int) bool { return tasks[i].ID < tasks[j].ID })
	entries := make([]acp.PlanEntry, 0, len(tasks))
	for _, item := range tasks {
		entries = append(entries, acp.PlanEntry{
			Content: item.Description, Priority: acp.PlanEntryPriorityMedium, Status: planStatus(item.Status),
		})
	}
	return s.agent.sendUpdate(ctx, s.id, acp.UpdatePlan(entries...))
}

// activeTurn 返回当前活动 turn 快照。
func (s *claudeSession) activeTurn() *claudeTurn {
	s.mu.Lock()
	turn := s.active
	s.mu.Unlock()
	return turn
}

// sendAgentText 发送一段普通文本或思考文本。
func (s *claudeSession) sendAgentText(ctx context.Context, messageID, text string, thinking bool) error {
	if text == "" {
		return nil
	}
	update := acp.UpdateAgentMessageText(text)
	if thinking {
		update = acp.UpdateAgentThoughtText(text)
	}
	if messageID != "" {
		if update.AgentMessageChunk != nil {
			update.AgentMessageChunk.MessageId = &messageID
		}
		if update.AgentThoughtChunk != nil {
			update.AgentThoughtChunk.MessageId = &messageID
		}
	}
	return s.agent.sendUpdate(ctx, s.id, update)
}

// sendUsageUpdate 发布当前消息可确定的 token 与上下文窗口。
func (s *claudeSession) sendUsageUpdate(ctx context.Context, usage protocol.Usage, window int64) error {
	used := usage.InputTokens + usage.CacheCreationInputTokens + usage.CacheReadInputTokens
	if used == 0 || window <= 0 {
		return nil
	}
	return s.agent.sendUpdate(ctx, s.id, acp.SessionUpdate{UsageUpdate: &acp.SessionUsageUpdate{
		Used: boundedInt(used), Size: boundedInt(window),
	}})
}

// startTool 幂等发布工具开始；重复来源只刷新缓存而不重复创建。
func (s *claudeSession) startTool(ctx context.Context, block protocol.ContentBlock) error {
	if block.ID == "" {
		return nil
	}
	input := decodeJSONValue(block.Input)
	s.mu.Lock()
	if existing := s.tools[block.ID]; existing != nil {
		if existing.Input == nil && input != nil {
			existing.Input = input
		}
		s.mu.Unlock()
		return nil
	}
	tool := &toolState{ID: block.ID, Name: block.Name, Input: input}
	s.tools[block.ID] = tool
	s.mu.Unlock()
	title, kind, locations := describeTool(block.Name, input)
	return s.agent.sendUpdate(ctx, s.id, acp.StartToolCall(
		acp.ToolCallId(block.ID), title,
		acp.WithStartKind(kind), acp.WithStartStatus(acp.ToolCallStatusInProgress),
		acp.WithStartRawInput(input), acp.WithStartLocations(locations),
	))
}

// completeTool 终止一个已知工具；未知结果先创建 generic 调用再完成。
func (s *claudeSession) completeTool(ctx context.Context, block protocol.ContentBlock) error {
	if block.ToolUseID == "" {
		return nil
	}
	s.mu.Lock()
	tool := s.tools[block.ToolUseID]
	created := tool == nil
	if tool == nil {
		tool = &toolState{ID: block.ToolUseID, Name: "Tool"}
		s.tools[block.ToolUseID] = tool
	}
	if tool.Completed {
		s.mu.Unlock()
		return nil
	}
	tool.Completed = true
	s.mu.Unlock()
	if created {
		// 缺失开始事件时先补一张 generic 工具卡片，保证客户端不会只收到悬空的完成更新。
		if err := s.agent.sendUpdate(ctx, s.id, acp.StartToolCall(
			acp.ToolCallId(block.ToolUseID), "Tool",
			acp.WithStartKind(acp.ToolKindOther), acp.WithStartStatus(acp.ToolCallStatusInProgress),
		)); err != nil {
			return err
		}
	}
	status := acp.ToolCallStatusCompleted
	if block.IsError {
		status = acp.ToolCallStatusFailed
	}
	content, rawOutput := toolResultContent(block)
	return s.agent.sendUpdate(ctx, s.id, acp.UpdateToolCall(
		acp.ToolCallId(block.ToolUseID), acp.WithUpdateStatus(status),
		acp.WithUpdateContent(content), acp.WithUpdateRawOutput(rawOutput),
	))
}

// promptResponseFromResult 转换 stop reason 与本轮 token 用量。
func promptResponseFromResult(message *protocol.ResultMessage) (acp.PromptResponse, error) {
	stopReason := acp.StopReasonEndTurn
	if message.StopReason != nil {
		switch *message.StopReason {
		case "max_tokens":
			stopReason = acp.StopReasonMaxTokens
		case "refusal":
			stopReason = acp.StopReasonRefusal
		}
	}
	switch message.Subtype {
	case "error_max_turns":
		stopReason = acp.StopReasonMaxTurnRequests
	case "error_during_execution", "error_max_budget_usd", "error_max_structured_output_retries":
		detail := strings.Join(message.Errors, "; ")
		if detail == "" {
			detail = message.Subtype
		}
		return acp.PromptResponse{}, fmt.Errorf("Claude turn failed: %s", detail)
	}
	usage := acp.Usage{
		InputTokens:       boundedInt(message.Usage.InputTokens),
		OutputTokens:      boundedInt(message.Usage.OutputTokens),
		CachedReadTokens:  intPointer(message.Usage.CacheReadInputTokens),
		CachedWriteTokens: intPointer(message.Usage.CacheCreationInputTokens),
	}
	usage.TotalTokens = usage.InputTokens + usage.OutputTokens
	if usage.CachedReadTokens != nil {
		usage.TotalTokens += *usage.CachedReadTokens
	}
	if usage.CachedWriteTokens != nil {
		usage.TotalTokens += *usage.CachedWriteTokens
	}
	return acp.PromptResponse{StopReason: stopReason, Usage: &usage}, nil
}

// unstreamedRemainder 去掉组装消息中已经实时发送的前缀。
func unstreamedRemainder(assembled, streamed string) string {
	if streamed == "" {
		return assembled
	}
	if strings.HasPrefix(assembled, streamed) {
		return strings.TrimPrefix(assembled, streamed)
	}
	if strings.HasPrefix(streamed, assembled) {
		return ""
	}
	return assembled
}

// contextWindow 返回 model usage 中最大的上下文窗口。
func contextWindow(models map[string]protocol.ModelUsage) int64 {
	var result int64
	for _, model := range models {
		if model.ContextWindow > result {
			result = model.ContextWindow
		}
	}
	return result
}

// boundedInt 把 wire int64 安全收窄到当前平台 int。
func boundedInt(value int64) int {
	if value <= 0 {
		return 0
	}
	if value > int64(math.MaxInt) {
		return math.MaxInt
	}
	return int(value)
}

// intPointer 把正 token 数转换为可选 ACP 字段。
func intPointer(value int64) *int {
	if value <= 0 {
		return nil
	}
	converted := boundedInt(value)
	return &converted
}

// decodeJSONValue 把开放 JSON 转成可序列化值，坏值保留安全占位符。
func decodeJSONValue(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return map[string]any{"unparsed": true}
	}
	return value
}

// describeTool 返回常见工具的标题、kind 与文件位置。
func describeTool(name string, input any) (string, acp.ToolKind, []acp.ToolCallLocation) {
	kind := acp.ToolKindOther
	switch name {
	case "Read":
		kind = acp.ToolKindRead
	case "Edit", "Write", "NotebookEdit":
		kind = acp.ToolKindEdit
	case "Bash", "Task", "TodoWrite":
		kind = acp.ToolKindExecute
	case "Grep", "Glob", "Search":
		kind = acp.ToolKindSearch
	case "WebFetch", "WebSearch":
		kind = acp.ToolKindFetch
	case "EnterPlanMode", "ExitPlanMode":
		kind = acp.ToolKindSwitchMode
	}
	title := name
	if title == "" {
		title = "Tool"
	}
	locations := []acp.ToolCallLocation{}
	if object, ok := input.(map[string]any); ok {
		for _, key := range []string{"file_path", "path", "notebook_path"} {
			if path, ok := object[key].(string); ok && path != "" {
				locations = append(locations, acp.ToolCallLocation{Path: filepath.Clean(path)})
				break
			}
		}
		if command, ok := object["command"].(string); ok && command != "" && name == "Bash" {
			title = command
		}
	}
	return title, kind, locations
}

// toolResultContent 把文本和图片结果转换为 ACP tool content。
func toolResultContent(block protocol.ContentBlock) ([]acp.ToolCallContent, any) {
	rawOutput := decodeJSONValue(block.Content)
	content := []acp.ToolCallContent{}
	var text string
	if json.Unmarshal(block.Content, &text) == nil {
		content = append(content, acp.ToolContent(acp.TextBlock(text)))
		return content, rawOutput
	}
	var blocks []protocol.ContentBlock
	if json.Unmarshal(block.Content, &blocks) == nil {
		for _, item := range blocks {
			switch item.Type {
			case "text":
				content = append(content, acp.ToolContent(acp.TextBlock(item.Text)))
			case "image":
				if item.Source != nil && item.Source.Type == "base64" {
					content = append(content, acp.ToolContent(acp.ImageBlock(item.Source.Data, item.Source.MediaType)))
				}
			}
		}
	}
	return content, rawOutput
}

// planStatus 把开放任务状态收敛到 ACP 三态。
func planStatus(status string) acp.PlanEntryStatus {
	switch status {
	case "completed", "done", "success":
		return acp.PlanEntryStatusCompleted
	case "in_progress", "running", "active":
		return acp.PlanEntryStatusInProgress
	default:
		return acp.PlanEntryStatusPending
	}
}

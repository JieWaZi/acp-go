package claude

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"slices"
	"sort"
	"strings"

	"github.com/JieWaZi/acp-go/pkg/claude/protocol"
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
		// 首条 init 也在 prompt 消费期校准状态。
		err = s.syncSystemInit(ctx, *typed)
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
		s.agent.logger.Debug("Ignoring unknown Claude message", "type", typed.MessageType(), "bytes", len(typed.RawJSON()))
	}
	if err != nil && !errors.Is(err, ErrClaudeConnectionNotReady) {
		s.agent.logger.Warn("Failed to handle Claude message", "session_id", s.id, "type", message.MessageType(), "error", err)
	}
}

// syncSystemInit 更新 CLI 可自主变化的配置，并在终端命令集合变化时重新发布菜单。
func (s *claudeSession) syncSystemInit(ctx context.Context, init protocol.SystemInitMessage) error {
	s.mu.Lock()
	if init.SessionID != s.id || s.closed {
		s.mu.Unlock()
		return nil
	}
	terminalCommandsChanged := init.TerminalSlashCommands != nil &&
		!slices.Equal(init.TerminalSlashCommands, s.systemInit.TerminalSlashCommands)
	if init.TerminalSlashCommands == nil {
		init.TerminalSlashCommands = s.systemInit.TerminalSlashCommands
	}
	s.systemInit = init
	if init.Model != "" {
		modelChanged := s.configuration.model != init.Model
		s.configuration.model = init.Model
		if modelChanged {
			s.seedContextWindowLocked(init.Model)
		}
	}
	if init.PermissionMode != "" {
		s.configuration.mode = acp.SessionModeId(init.PermissionMode)
	}
	if init.FastModeState != "" {
		s.configuration.fast = init.FastModeState == "on" || init.FastModeState == "cooldown"
	}
	s.mu.Unlock()
	if terminalCommandsChanged {
		return s.sendAvailableCommandsUpdate(ctx)
	}
	return nil
}

// handleStreamEvent 映射文本、思考、工具开始和 usage 增量。
func (s *claudeSession) handleStreamEvent(ctx context.Context, message *protocol.StreamEventMessage) error {
	turn := s.activeTurn()
	if turn == nil {
		return nil
	}
	event := message.Event
	switch event.Type {
	case "message_start":
		turn.mu.Lock()
		delete(turn.streamedBlocks, streamedBlockOwner(message.ParentToolUseID))
		turn.mu.Unlock()
		if message.ParentToolUseID != nil || event.Message == nil {
			return nil
		}
		s.useAssistantModel(event.Message.Model)
		if event.Message.Usage != nil {
			recordContextUsage(turn, *event.Message.Usage, event.Message.Model)
			return s.sendUsageUpdate(ctx, turn, false)
		}
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
			recordStreamedBlock(
				turn.streamedBlocks,
				streamedBlockOwner(message.ParentToolUseID),
				event.Index,
				"text",
				event.Delta.Text,
			)
			turn.mu.Unlock()
			return s.sendAgentText(ctx, message.UUID, event.Delta.Text, false)
		}
		if event.Delta.Thinking != "" {
			turn.mu.Lock()
			recordStreamedBlock(
				turn.streamedBlocks,
				streamedBlockOwner(message.ParentToolUseID),
				event.Index,
				"thinking",
				event.Delta.Thinking,
			)
			turn.mu.Unlock()
			return s.sendAgentText(ctx, message.UUID, event.Delta.Thinking, true)
		}
	case "message_delta":
		if message.ParentToolUseID == nil && event.Usage != nil {
			recordContextUsageDelta(turn, *event.Usage)
			return s.sendUsageUpdate(ctx, turn, false)
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
	if message.Error != "" {
		var details []string
		for _, block := range message.Message.Content {
			if block.Type == "text" && strings.TrimSpace(block.Text) != "" {
				details = append(details, block.Text)
			}
		}
		turn.mu.Lock()
		if message.ParentToolUseID == nil {
			turn.assistantError = message.Error
			turn.assistantErrorMessage = strings.Join(details, "\n")
		}
		delete(turn.streamedBlocks, streamedBlockOwner(message.ParentToolUseID))
		turn.mu.Unlock()
		// 错误正文不交付；顶层错误由 result 结算，子代理错误由后续 user.tool_result 结算对应工具。
		return nil
	}
	if message.ParentToolUseID == nil {
		s.useAssistantModel(message.Message.Model)
		if message.Message.Usage != nil {
			recordContextUsage(turn, *message.Message.Usage, message.Message.Model)
		}
	}
	owner := streamedBlockOwner(message.ParentToolUseID)
	turn.mu.Lock()
	streamed := append([]streamedContentBlock(nil), turn.streamedBlocks[owner]...)
	delete(turn.streamedBlocks, owner)
	turn.mu.Unlock()
	streamPosition := 0
	for _, block := range message.Message.Content {
		switch block.Type {
		case "text":
			remainder, consumed := streamedBlockRemainder(block.Text, "text", streamed, streamPosition)
			if consumed {
				streamPosition++
			}
			if remainder != "" {
				if err := s.sendAgentText(ctx, message.UUID, remainder, false); err != nil {
					return err
				}
			}
		case "thinking":
			remainder, consumed := streamedBlockRemainder(block.Thinking, "thinking", streamed, streamPosition)
			if consumed {
				streamPosition++
			}
			if remainder != "" {
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

// streamedBlockOwner 把顶层与不同子代理消息映射到互不污染的去重槽位。
func streamedBlockOwner(parentToolUseID *string) string {
	if parentToolUseID == nil {
		return ""
	}
	return *parentToolUseID
}

// recordStreamedBlock 按规则合并同一原始 index/type 的连续增量。
func recordStreamedBlock(
	blocks map[string][]streamedContentBlock,
	owner string,
	index int,
	kind string,
	text string,
) {
	if text == "" {
		return
	}
	owned := blocks[owner]
	if len(owned) > 0 && owned[len(owned)-1].index == index && owned[len(owned)-1].kind == kind {
		owned[len(owned)-1].text += text
		blocks[owner] = owned
		return
	}
	blocks[owner] = append(owned, streamedContentBlock{index: index, kind: kind, text: text})
}

// streamedBlockRemainder 按聚合消息的文档顺序匹配流式块，并只返回未发送尾部。
func streamedBlockRemainder(
	assembled string,
	kind string,
	streamed []streamedContentBlock,
	position int,
) (string, bool) {
	if assembled == "" {
		return "", false
	}
	if position >= len(streamed) || streamed[position].kind != kind || streamed[position].text == "" {
		return assembled, false
	}
	if !strings.HasPrefix(assembled, streamed[position].text) {
		return assembled, false
	}
	return strings.TrimPrefix(assembled, streamed[position].text), true
}

// handleUser 匹配 prompt/steering echo，并完成工具结果。
func (s *claudeSession) handleUser(ctx context.Context, message *protocol.UserMessage) error {
	turn := s.activeTurn()
	if turn == nil || (message.SessionID != "" && message.SessionID != s.id) {
		return nil
	}
	// 工具结果拥有独立 UUID，必须先按 tool_use_id 处理，不能误当成未知 prompt echo 丢弃。
	toolResultCount := 0
	for _, block := range message.Message.Content {
		if block.Type == "tool_result" {
			toolResultCount++
		}
	}
	for _, block := range message.Message.Content {
		if block.Type == "tool_result" {
			var toolUseResult json.RawMessage
			// Claude 的 message-level tool_use_result 不带 tool_use_id，只能归属到唯一结果块。
			if toolResultCount == 1 {
				toolUseResult = message.ToolUseResult
			}
			if err := s.completeTool(ctx, block, toolUseResult); err != nil {
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
	turn.mu.Lock()
	assistantError := turn.assistantError
	assistantErrorMessage := turn.assistantErrorMessage
	turn.mu.Unlock()
	if assistantError != "" && assistantErrorMessage != "" && (!message.IsError || strings.TrimSpace(message.Result) == "") {
		copy := *message
		copy.Result = assistantErrorMessage
		message = &copy
	}
	response, err := promptResponseFromResult(message, assistantError)
	turn.mu.Lock()
	turn.gotResult = true
	cancelled := turn.cancelled
	steered := turn.steered
	if steered && err == nil {
		copy := response
		turn.steeredResult = &copy
	}
	turn.mu.Unlock()
	if !steered {
		// Claude 的每个非 steering result 后都会发送无 Turn ID 的 idle；先记账，避免它误结算下一轮。
		s.mu.Lock()
		s.owedTrailingIdles++
		s.mu.Unlock()
	}
	if cancelled {
		turn.drain()
		return nil
	}
	if err != nil {
		turn.settle(acp.PromptResponse{}, err)
		turn.drain()
		return nil
	}
	s.updateContextWindowFromResult(turn, message.ModelUsage)
	updateErr := s.sendUsageUpdate(ctx, turn, true)
	if steered {
		return updateErr
	}
	turn.settle(response, nil)
	turn.drain()
	return updateErr
}

// handleSystem 同步动态命令，并使用 idle 作为缺失 result 的终止保护。
func (s *claudeSession) handleSystem(ctx context.Context, message *protocol.SystemMessage) error {
	if message.Subtype == "commands_changed" {
		return s.updateAvailableCommands(ctx, message.Commands)
	}
	if message.Subtype != "session_state_changed" || message.State != "idle" {
		return nil
	}
	s.mu.Lock()
	if s.owedTrailingIdles > 0 {
		s.owedTrailingIdles--
		s.mu.Unlock()
		return nil
	}
	s.mu.Unlock()
	turn := s.activeTurn()
	if turn == nil {
		return nil
	}
	turn.mu.Lock()
	gotResult := turn.gotResult
	cancelled := turn.cancelled
	steered := turn.steered
	steeredResult := turn.steeredResult
	assistantError := turn.assistantError
	assistantErrorMessage := turn.assistantErrorMessage
	turn.mu.Unlock()
	switch {
	case cancelled:
		turn.settle(acp.PromptResponse{StopReason: acp.StopReasonCancelled}, nil)
	case assistantError != "":
		turn.settle(acp.PromptResponse{}, claudeResultRequestError(&protocol.ResultMessage{Result: assistantErrorMessage}, assistantError))
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
	info := toolInfoFromToolUse(block.Name, input, s.cwd, claudeUserHomeDirectory(s.agent.environment))
	rawInput := input
	if info.RawInput != nil {
		rawInput = info.RawInput
	}
	update := acp.StartToolCall(
		acp.ToolCallId(block.ID), info.Title,
		acp.WithStartKind(info.Kind), acp.WithStartStatus(acp.ToolCallStatusInProgress),
		acp.WithStartRawInput(rawInput), acp.WithStartContent(info.Content),
		acp.WithStartLocations(info.Locations),
	)
	update.ToolCall.Meta = info.Meta
	return s.agent.sendUpdate(ctx, s.id, update)
}

// completeTool 终止一个已知工具；未知结果先创建 generic 调用再完成。
func (s *claudeSession) completeTool(
	ctx context.Context,
	block protocol.ContentBlock,
	toolUseResult json.RawMessage,
) error {
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
	options := []acp.ToolCallUpdateOpt{
		acp.WithUpdateStatus(status),
		acp.WithUpdateRawOutput(rawOutput),
	}
	if !block.IsError && (tool.Name == "Edit" || tool.Name == "Write") {
		// 由 PostToolUse structuredPatch 修正乐观 diff；CLI 直接携带同形结果时等价处理。
		diffContent, locations := toolDiffUpdateFromResult(toolUseResult)
		if len(diffContent) > 0 {
			options = append(
				options,
				acp.WithUpdateContent(diffContent),
				acp.WithUpdateLocations(locations),
			)
		}
	} else {
		options = append(options, acp.WithUpdateContent(content))
	}
	return s.agent.sendUpdate(
		ctx,
		s.id,
		acp.UpdateToolCall(acp.ToolCallId(block.ToolUseID), options...),
	)
}

// promptResponseFromResult 转换 stop reason 与本轮 token 用量。
func promptResponseFromResult(
	message *protocol.ResultMessage,
	assistantError string,
) (acp.PromptResponse, error) {
	if message.IsError || assistantError != "" {
		return acp.PromptResponse{}, claudeResultRequestError(message, assistantError)
	}
	stopReason := acp.StopReasonEndTurn
	if message.StopReason != nil {
		switch *message.StopReason {
		case "max_tokens":
			stopReason = acp.StopReasonMaxTokens
		case "refusal":
			stopReason = acp.StopReasonRefusal
		}
	}
	if stopReason != acp.StopReasonMaxTokens && stopReason != acp.StopReasonRefusal {
		if message.Subtype == "success" && strings.HasPrefix(strings.TrimSpace(message.Result), "Not logged in") && strings.Contains(message.Result, "Please run /login") {
			return acp.PromptResponse{}, acp.NewAuthRequired(claudeErrorKindData("authentication_failed"))
		}
		switch message.Subtype {
		case "error_max_turns", "error_max_budget_usd", "error_max_structured_output_retries":
			stopReason = acp.StopReasonMaxTurnRequests
		}
	}
	usage := acp.Usage{
		InputTokens:       boundedInt(message.Usage.InputTokens),
		OutputTokens:      boundedInt(message.Usage.OutputTokens),
		CachedReadTokens:  intPointer(message.Usage.CacheReadInputTokens),
		CachedWriteTokens: intPointer(message.Usage.CacheCreationInputTokens),
	}
	usage.TotalTokens = boundedInt(totalClaudeUsage(message.Usage))
	return acp.PromptResponse{StopReason: stopReason, Usage: &usage}, nil
}

// claudeResultRequestError 把 is_error 结果转换为 ACP InternalError。
func claudeResultRequestError(
	message *protocol.ResultMessage,
	assistantError string,
) *acp.RequestError {
	detail := strings.TrimSpace(message.Result)
	if detail == "" {
		detail = strings.Join(message.Errors, "; ")
	}
	if detail == "" {
		detail = assistantError
	}
	if detail == "" {
		detail = message.Subtype
	}
	return &acp.RequestError{
		Code:    -32603,
		Message: detail,
		Data:    claudeErrorKindData(assistantError),
	}
}

// claudeErrorKindData 复用开放 errorKind 扩展，不引入产品失败码。
func claudeErrorKindData(errorKind string) any {
	if errorKind == "" {
		return nil
	}
	return map[string]any{"errorKind": errorKind}
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

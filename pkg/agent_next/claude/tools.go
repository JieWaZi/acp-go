package claude

import (
	"encoding/json"

	agentclient "github.com/JieWaZi/acp-go/pkg/agent_next"
	"github.com/JieWaZi/acp-go/pkg/agent_next/claude/protocol"
	acp "github.com/JieWaZi/acp-go/sdk"
)

// toolOwner is the native lineage of a tool, frozen independently of which
// foreground invocation is currently active.
type toolOwner struct {
	scope  agentclient.Scope
	agent  string
	parent string
}

type toolRecord struct {
	owner     toolOwner
	name      string
	completed bool
}

type taskRecord struct {
	scope      agentclient.Scope
	run        string
	tool       string
	live       bool
	background bool
	ambient    bool
}

func (s *session) nativeTaskLocked(id string) *taskRecord {
	if id == "" || len(id) > 512 {
		return nil
	}
	task := s.nativeTasks[id]
	if task != nil {
		return task
	}
	if len(s.nativeTasks) >= maxRememberedIDs {
		s.taskOwnershipFull = true
		return nil
	}
	task = &taskRecord{live: true}
	s.nativeTasks[id] = task
	return task
}

func (s *session) sidechainOwnerLocked(agent string, parent *string) toolOwner {
	var owner toolOwner
	if parent != nil {
		owner.parent = *parent
		if tool := s.tools[*parent]; tool != nil {
			owner.scope = tool.owner.scope
		}
	}
	if agent == "" {
		return owner
	}
	owner.agent = agent
	task := s.nativeTasks[agent]
	// An assistant's parent tool and agent ID can precede task_started.
	if task == nil && owner.scope.NativeInvocationID != "" {
		task = s.nativeTaskLocked(agent)
	}
	if task == nil || !task.live {
		return toolOwner{}
	}
	if task.scope.NativeInvocationID == "" {
		task.scope = owner.scope
	}
	if owner.scope.NativeInvocationID != "" && owner.scope.NativeInvocationID != task.scope.NativeInvocationID {
		return toolOwner{}
	}
	owner.scope = task.scope
	return owner
}

func (s *session) permissionToolLocked(inv *invocation, req protocol.CanUseToolControlRequest) *toolRecord {
	if inv == nil || inv.terminal || inv.cancelRequested || !s.open || s.closing ||
		req.ToolUseID == "" || req.ToolName == "" || len(req.Input) > maxObservationFrame {
		return nil
	}
	tool := s.tools[req.ToolUseID]
	if tool != nil {
		if tool.name != req.ToolName || req.AgentID != "" && req.AgentID != tool.owner.agent || !s.liveToolLocked(inv, tool) {
			return nil
		}
		return tool
	}
	owner := toolOwner{scope: inv.scope}
	if req.AgentID != "" {
		owner = s.sidechainOwnerLocked(req.AgentID, nil)
		if owner.scope.NativeInvocationID != inv.scope.NativeInvocationID {
			return nil
		}
	} else {
		// Claude can request approval before its tool block is observed.
		// Untagged early requests may bind only to a proven foreground owner
		// without a competing tracked task. Older producers omit agent_id.
		if inv.scope.HostInvocationID == "" || s.background > 0 || s.taskOwnershipFull {
			return nil
		}
		for _, task := range s.nativeTasks {
			if task.live {
				return nil
			}
		}
	}
	return s.startToolLocked(owner, protocol.ContentBlock{Type: "tool_use", ID: req.ToolUseID, Name: req.ToolName, Input: req.Input})
}

func (s *session) liveToolLocked(inv *invocation, tool *toolRecord) bool {
	if inv == nil || tool == nil || inv != s.current || inv.terminal || inv.cancelRequested || tool.completed ||
		!s.open || s.closing || tool.owner.scope.NativeInvocationID != inv.scope.NativeInvocationID {
		return false
	}
	if tool.owner.agent != "" {
		task := s.nativeTasks[tool.owner.agent]
		return task != nil && task.live && task.scope.NativeInvocationID == tool.owner.scope.NativeInvocationID
	}
	return true
}

func (s *session) observeBackgroundLocked(tasks []protocol.BackgroundTask) {
	present := make(map[string]bool, min(len(tasks), maxRememberedIDs))
	s.background = 0
	for _, reported := range tasks {
		if !reported.Ambient {
			s.background++
		}
		task := s.nativeTaskLocked(reported.TaskID)
		if task == nil {
			continue
		}
		present[reported.TaskID] = true
		if !task.advance(reported.RunID) {
			continue
		}
		task.background = true
		task.ambient = reported.Ambient
		s.linkTaskLocked(task, "", reported.ParentTaskID)
	}
	for id, task := range s.nativeTasks {
		if task.background && !present[id] {
			task.live = false
			task.background = false
		}
	}
}

func (t *taskRecord) advance(run string) bool {
	if run == "" {
		return true
	}
	if t.run != "" && run < t.run {
		return false
	}
	if run != t.run {
		t.run = run
		t.live = true
	}
	return true
}

func (s *session) linkTaskLocked(task *taskRecord, toolID, parentID string) {
	if toolID != "" && task.tool == "" {
		task.tool = toolID
	}
	if task.scope.NativeInvocationID != "" {
		return
	}
	if tool := s.tools[task.tool]; tool != nil {
		task.scope = tool.owner.scope
	}
	if task.scope.NativeInvocationID == "" {
		if parent := s.nativeTasks[parentID]; parent != nil {
			task.scope = parent.scope
		}
	}
}

func (s *session) sendToolLocked(owner toolOwner, update acp.SessionUpdate, meta map[string]any) {
	if owner.scope.NativeInvocationID == "" {
		return
	}
	if inv := s.current; inv != nil && inv.scope.NativeInvocationID == owner.scope.NativeInvocationID {
		s.sendTurnLocked(inv, update, meta)
		return
	}
	s.enqueueScopedLocked(nil, owner.scope, acp.SessionNotification{SessionId: s.id, Update: update, Meta: meta})
}

func (s *session) startToolLocked(owner toolOwner, block protocol.ContentBlock) *toolRecord {
	if owner.scope.NativeInvocationID == "" || block.ID == "" || len(block.ID) > 512 || block.Name == "" {
		return nil
	}
	if existing := s.tools[block.ID]; existing != nil {
		return nil
	}
	if len(s.tools) >= maxRememberedIDs {
		return nil
	}
	record := &toolRecord{owner: owner, name: block.Name}
	s.tools[block.ID] = record
	meta := map[string]any{"claude.tool": block.Name}
	if owner.parent != "" {
		meta["claude.parent_tool_use_id"] = owner.parent
	}
	if owner.agent != "" {
		meta["claude.task_id"] = owner.agent
	}
	s.sendToolLocked(owner, acp.StartToolCall(acp.ToolCallId(block.ID), block.Name,
		acp.WithStartKind(toolKind(block.Name)), acp.WithStartStatus(acp.ToolCallStatusInProgress),
		acp.WithStartRawInput(jsonValue(block.Input)), acp.WithStartLocations(toolLocations(block.Input))), meta)
	if block.Name == "TodoWrite" && owner.parent == "" && owner.agent == "" {
		value := decodeOptional[struct {
			Todos []struct {
				Content string
				Status  string
			}
		}](block.Input)
		if value != nil {
			var rows []acp.PlanEntry
			for _, todo := range value.Todos {
				rows = append(rows, acp.PlanEntry{Content: todo.Content, Priority: acp.PlanEntryPriorityMedium, Status: planStatus(todo.Status)})
			}
			s.sendToolLocked(owner, acp.UpdatePlan(rows...), nil)
		}
	}
	return record
}

func (s *session) toolResultsLocked(m *protocol.UserMessage) {
	for _, block := range m.Message.Content {
		if block.Type != "tool_result" {
			continue
		}
		record := s.tools[block.ToolUseID]
		if record == nil || record.completed || m.ParentToolUseID != nil && record.owner.parent != *m.ParentToolUseID {
			continue
		}
		record.completed = true
		status := acp.ToolCallStatusCompleted
		if block.IsError {
			status = acp.ToolCallStatusFailed
		}
		var content []acp.ToolCallContent
		if text := decodeOptional[string](block.Content); text != nil {
			content = append(content, acp.ToolContent(acp.TextBlock(*text)))
		} else if parts := decodeOptional[[]protocol.ContentBlock](block.Content); parts != nil {
			for _, part := range *parts {
				if part.Type == "text" {
					content = append(content, acp.ToolContent(acp.TextBlock(part.Text)))
				}
			}
		}
		s.sendToolLocked(record.owner, acp.UpdateToolCall(acp.ToolCallId(block.ToolUseID), acp.WithUpdateStatus(status),
			acp.WithUpdateContent(content), acp.WithUpdateRawOutput(jsonValue(firstRaw(m.ToolUseResult, block.Content)))), nil)
	}
}

func (s *session) toolProgressLocked(m *protocol.ToolProgressMessage) {
	record := s.tools[m.ToolUseID]
	if record == nil || record.completed || m.ParentToolUseID != nil && record.owner.parent != *m.ParentToolUseID ||
		m.TaskID != "" && record.owner.agent != "" && record.owner.agent != m.TaskID {
		return
	}
	s.sendToolLocked(record.owner, acp.UpdateToolCall(acp.ToolCallId(m.ToolUseID),
		acp.WithUpdateStatus(acp.ToolCallStatusInProgress)), nil)
}

func (s *session) taskUpdateLocked(m *protocol.TaskMessage) {
	task := s.nativeTaskLocked(m.TaskID)
	if task == nil || !task.advance(m.RunID) {
		return
	}
	task.ambient = task.ambient || m.Ambient || m.SkipTranscript
	task.background = task.background || m.IsBackgrounded
	s.linkTaskLocked(task, m.ToolUseID, m.ParentTaskID)
	state := m.Status
	if m.Subtype == "task_updated" {
		var patch struct {
			Status         string
			IsBackgrounded bool `json:"is_backgrounded"`
		}
		if json.Unmarshal(m.Patch, &patch) == nil {
			if patch.Status != "" {
				state = patch.Status
			}
			task.background = task.background || patch.IsBackgrounded
		}
	}
	status := acp.ToolCallStatusInProgress
	switch state {
	case "completed":
		status = acp.ToolCallStatusCompleted
		task.live = false
	case "failed", "stopped", "killed":
		status = acp.ToolCallStatusFailed
		task.live = false
	}
	if task.ambient || task.tool == "" {
		return
	}
	update := acp.UpdateToolCall(acp.ToolCallId(task.tool), acp.WithUpdateStatus(status))
	if m.Summary != "" {
		update.ToolCallUpdate.Content = []acp.ToolCallContent{acp.ToolContent(acp.TextBlock(m.Summary))}
	}
	s.sendToolLocked(toolOwner{scope: task.scope, agent: m.TaskID}, update, map[string]any{"claude.task_id": m.TaskID})
}

func (s *session) retireForegroundTasksLocked(inv *invocation) {
	for _, task := range s.nativeTasks {
		if task.scope.NativeInvocationID == inv.scope.NativeInvocationID && !task.background {
			task.live = false
		}
	}
}

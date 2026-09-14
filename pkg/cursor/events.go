package cursor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"time"

	acp "github.com/coder/acp-go-sdk"
)

// turnProjection 保存一轮已发出的工具与 Hook 身份，不跨轮累计计费。
type turnProjection struct {
	// tools 防止待审批工具提交后重复产生开始事件。
	tools map[string]bool
	// handled 防止一条仍驻留在检查点中的审批被再次请求。
	handled map[string]bool
	// generations 防止官方重复 stop 对同一生成重复计费。
	generations map[string]bool
	// usage 是本轮已确认的用量，nil 表示上游未提供。
	usage *acp.Usage
	// ended 保存官方结束状态，空表示尚未结束。
	ended string
}

// notify 通过当前宿主连接发出统一会话事件。
func (a *Agent) notify(ctx context.Context, s *interactiveSession, update acp.SessionUpdate) error {
	a.mutex.Lock()
	host := a.host
	a.mutex.Unlock()
	if host == nil {
		return errors.New("Cursor host connection unavailable")
	}
	return host.SessionUpdate(ctx, acp.SessionNotification{SessionId: s.id, Update: update})
}

// toolKind 将原生工具类别映射到 ACP 展示语义。
func toolKind(name string) acp.ToolKind {
	switch strings.ToLower(name) {
	case "shell", "bash":
		return acp.ToolKindExecute
	case "read", "readfile":
		return acp.ToolKindRead
	case "write", "strreplace", "edit", "delete":
		return acp.ToolKindEdit
	case "glob", "grep", "rg", "websearch":
		return acp.ToolKindSearch
	default:
		return acp.ToolKindOther
	}
}

// startTool 以原生工具身份去重开始通知并保留执行参数。
func (a *Agent) startTool(ctx context.Context, s *interactiveSession, p *turnProjection, id, name string, args map[string]any, status acp.ToolCallStatus) error {
	if p.tools[id] {
		if status == acp.ToolCallStatusInProgress {
			return a.notify(ctx, s, acp.UpdateToolCall(acp.ToolCallId(id), acp.WithUpdateStatus(status), acp.WithUpdateRawInput(args)))
		}
		return nil
	}
	p.tools[id] = true
	return a.notify(ctx, s, acp.StartToolCall(acp.ToolCallId(id), name, acp.WithStartKind(toolKind(name)), acp.WithStartStatus(status), acp.WithStartRawInput(args)))
}

// project 把新增原生消息映射为有序文本、思考和工具事件。
func (a *Agent) project(ctx context.Context, s *interactiveSession, p *turnProjection, batch storeBatch) error {
	for _, message := range batch.messages {
		for _, part := range message.content {
			var update acp.SessionUpdate
			switch part.kind {
			case "text":
				if message.role != "assistant" || part.text == "" {
					continue
				}
				update = acp.UpdateAgentMessageText(part.text)
			case "reasoning":
				if message.role != "assistant" || part.text == "" {
					continue
				}
				update = acp.UpdateAgentThoughtText(part.text)
			case "tool-call":
				if err := a.startTool(ctx, s, p, part.id, part.name, part.args, acp.ToolCallStatusInProgress); err != nil {
					return err
				}
				continue
			case "tool-result":
				if !p.tools[part.id] {
					if err := a.startTool(ctx, s, p, part.id, part.name, nil, acp.ToolCallStatusInProgress); err != nil {
						return err
					}
				}
				status := acp.ToolCallStatusCompleted
				text, ok := part.result.(string)
				if !ok {
					data, _ := json.Marshal(part.result)
					text = string(data)
				}
				if strings.HasPrefix(text, "Rejected:") || strings.HasPrefix(text, "Error:") {
					status = acp.ToolCallStatusFailed
				}
				update = acp.UpdateToolCall(acp.ToolCallId(part.id), acp.WithUpdateStatus(status), acp.WithUpdateRawOutput(part.result), acp.WithUpdateContent([]acp.ToolCallContent{acp.ToolContent(acp.TextBlock(text))}))
			default:
				continue
			}
			if err := a.notify(ctx, s, update); err != nil {
				return err
			}
		}
	}
	return nil
}

// runTurn 合并检查点与官方 Hook，驱动审批并返回本轮真实结束与用量。
func (a *Agent) runTurn(ctx context.Context, s *interactiveSession) (acp.PromptResponse, error) {
	p := &turnProjection{tools: map[string]bool{}, handled: map[string]bool{}, generations: map[string]bool{}}
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	var idleSince time.Time
	var unknownApprovalSince time.Time
	for {
		batch, err := s.store.read(ctx)
		if err != nil {
			return acp.PromptResponse{Usage: p.usage}, err
		}
		if err = a.project(ctx, s, p, batch); err != nil {
			return acp.PromptResponse{Usage: p.usage}, err
		}
		events, err := readHooks(ctx, filepath.Join(s.directory, "events"))
		if err != nil {
			return acp.PromptResponse{Usage: p.usage}, err
		}
		for _, event := range events {
			if event.Session != string(s.id) {
				continue
			}
			if event.Name == "preCompact" && event.ContextWindow > 0 && event.ContextTokens >= 0 {
				if err = a.notify(ctx, s, acp.SessionUpdate{UsageUpdate: &acp.SessionUsageUpdate{Used: event.ContextTokens, Size: event.ContextWindow}}); err != nil {
					return acp.PromptResponse{Usage: p.usage}, err
				}
			}
			if event.Name != "stop" || event.Generation == "" || p.generations[event.Generation] {
				continue
			}
			p.generations[event.Generation] = true
			p.ended = event.Status
			if usage := event.usage(); usage != nil {
				if p.usage == nil {
					p.usage = usage
				} else {
					p.usage.InputTokens += usage.InputTokens
					p.usage.OutputTokens += usage.OutputTokens
					p.usage.TotalTokens += usage.TotalTokens
					*p.usage.CachedReadTokens += *usage.CachedReadTokens
					*p.usage.CachedWriteTokens += *usage.CachedWriteTokens
				}
			}
		}
		if s.terminal.outcome != nil {
			if err := s.terminal.outcome.failure(); err != nil {
				return acp.PromptResponse{Usage: p.usage}, err
			}
		}
		if p.ended != "" && terminalReady(s.terminal.text()) && len(batch.pending) == 0 {
			batch, err = s.store.read(ctx)
			if err == nil {
				err = a.project(ctx, s, p, batch)
			}
			if err != nil {
				return acp.PromptResponse{Usage: p.usage}, err
			}
			a.refreshContextUsage(ctx, s)
			response := acp.PromptResponse{StopReason: acp.StopReasonEndTurn, Usage: p.usage}
			switch p.ended {
			case "completed":
				return response, nil
			case "aborted", "cancelled":
				response.StopReason = acp.StopReasonCancelled
				return response, nil
			default:
				return response, cursorTurnError(s.terminal.text(), fmt.Sprintf("Cursor generation ended with status %s", p.ended))
			}
		}
		screen := s.terminal.text()
		if permissionScreen(screen) {
			if _, ok := visiblePermission(screen, batch.pending); ok {
				unknownApprovalSince = time.Time{}
			} else {
				if unknownApprovalSince.IsZero() {
					unknownApprovalSince = time.Now()
				}
				if time.Since(unknownApprovalSince) > 2*time.Second {
					return acp.PromptResponse{Usage: p.usage}, errors.New("Cursor approval cannot be uniquely matched to a pending operation")
				}
			}
		} else {
			unknownApprovalSince = time.Time{}
		}
		for _, pending := range batch.pending {
			if p.handled[pending.id] {
				continue
			}
			screen := s.terminal.text()
			question := strings.EqualFold(pending.name, "AskQuestion")
			if question {
				if !questionScreen(screen, pending) {
					continue
				}
			} else {
				visible, ok := visiblePermission(screen, batch.pending)
				if !ok || visible.id != pending.id {
					continue
				}
			}
			if err = a.startTool(ctx, s, p, pending.id, pending.name, pending.args, acp.ToolCallStatusPending); err != nil {
				return acp.PromptResponse{Usage: p.usage}, err
			}
			if question {
				err = a.answerQuestions(ctx, s, pending)
			} else {
				err = a.approve(ctx, s, pending)
			}
			if err != nil {
				return acp.PromptResponse{Usage: p.usage}, err
			}
			p.handled[pending.id] = true
			break
		}
		if terminalReady(s.terminal.text()) {
			if idleSince.IsZero() {
				idleSince = time.Now()
			}
			if time.Since(idleSince) > 10*time.Second {
				return acp.PromptResponse{Usage: p.usage}, cursorTurnError(s.terminal.text(), "Cursor became idle without a completion hook")
			}
		} else {
			idleSince = time.Time{}
		}
		select {
		case <-ctx.Done():
			return acp.PromptResponse{Usage: p.usage}, ctx.Err()
		case <-s.terminal.done:
			return acp.PromptResponse{Usage: p.usage}, errors.New("Cursor interactive process exited before completion")
		case <-ticker.C:
		}
	}
}

// permissionKeyHint 匹配官方审批提示的允许快捷键；工具身份由检查点另行校验。
var permissionKeyHint = regexp.MustCompile(`(?i)\(\s*y\s*(?:/[^)\n]*)?\)`)

// permissionScreen 只在非空闲屏幕具有官方允许键提示时回填审批。
func permissionScreen(screen string) bool {
	return permissionKeyHint.MatchString(screen) && !terminalReady(screen)
}

// approve 请求宿主裁决并核对原生工具身份后回填允许或拒绝。
func (a *Agent) approve(ctx context.Context, s *interactiveSession, call pendingCall) error {
	title := call.name
	kind := toolKind(call.name)
	a.mutex.Lock()
	host := a.host
	a.mutex.Unlock()
	if host == nil {
		return errors.New("Cursor host connection unavailable")
	}
	response, err := host.RequestPermission(ctx, acp.RequestPermissionRequest{SessionId: s.id, ToolCall: acp.ToolCallUpdate{ToolCallId: acp.ToolCallId(call.id), Title: &title, Kind: &kind, RawInput: call.args}, Options: []acp.PermissionOption{{OptionId: "allow", Name: "Allow once", Kind: acp.PermissionOptionKindAllowOnce}, {OptionId: "reject", Name: "Reject", Kind: acp.PermissionOptionKindRejectOnce}}})
	if err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if !permissionMatches(s.terminal.text(), call) {
		return errors.New("Cursor approval prompt changed before response")
	}
	if err = pendingUnchanged(ctx, s, call); err != nil {
		return err
	}
	if response.Outcome.Selected != nil && response.Outcome.Selected.OptionId == "allow" {
		return s.terminal.write("y")
	}
	if err = s.terminal.write("\x1b"); err != nil {
		return err
	}
	transition, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err = s.terminal.wait(transition, func(screen string) bool {
		return strings.Contains(screen, "empty to skip") || !permissionMatches(screen, call)
	}); err != nil {
		return err
	}
	if strings.Contains(s.terminal.text(), "empty to skip") {
		return s.terminal.write("\r")
	}
	return nil
}

// pendingUnchanged 在回填前独立核对当前原生检查点，不消耗主投影的增量游标。
func pendingUnchanged(ctx context.Context, s *interactiveSession, call pendingCall) error {
	batch, err := newStoreCursor(s.store.path).read(ctx)
	if err != nil {
		return err
	}
	for _, pending := range batch.pending {
		if pending.id == call.id && pending.name == call.name && reflect.DeepEqual(pending.args, call.args) {
			if strings.EqualFold(call.name, "AskQuestion") {
				return nil
			}
			visible, ok := visiblePermission(s.terminal.text(), batch.pending)
			if ok && visible.id == call.id {
				return nil
			}
		}
	}
	return errors.New("Cursor interaction is no longer uniquely pending")
}

// cursorTurnError 保留官方已明确显示的套餐失败分类，供宿主在同一聊天切换模型重试。
func cursorTurnError(screen, fallback string) error {
	lower := strings.ToLower(screen)
	if strings.Contains(lower, "upgrade your plan") || strings.Contains(lower, "upgrade plan") {
		return acp.NewInternalError(map[string]any{"errorKind": "billing_error", "message": "Cursor requires a plan upgrade for the selected model"})
	}
	return errors.New(fallback)
}

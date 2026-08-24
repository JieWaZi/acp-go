package claude

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JieWaZi/acp-go/pkg/claude/protocol"
	acp "github.com/coder/acp-go-sdk"
)

const fakeClaudeProcessEnv = "ACP_GO_TEST_FAKE_CLAUDE_PROCESS"

// TestMain 在子进程标记存在时运行 fake CLI，否则执行正常测试集合。
func TestMain(m *testing.M) {
	if os.Getenv(fakeClaudeProcessEnv) != "" {
		os.Exit(runFakeClaudeProcess(os.Args[1:], os.Stdin, os.Stdout))
	}
	os.Exit(m.Run())
}

// recordingClaudeClient 记录 Session 更新，并以固定选择响应权限请求。
type recordingClaudeClient struct {
	// mu 保护 updates 与 permissions。
	mu sync.Mutex
	// updates 保存 ACP session/update 顺序。
	updates []acp.SessionNotification
	// permissions 保存收到的权限请求。
	permissions []acp.RequestPermissionRequest
	// elicitations 保存收到的结构化用户输入请求。
	elicitations []acp.UnstableCreateElicitationRequest
}

// TestClaudeAgentReturnsResourceNotFoundForMissingSession 锁住 Session 缺失的 ACP 标准错误码。
func TestClaudeAgentReturnsResourceNotFoundForMissingSession(t *testing.T) {
	t.Parallel()

	agent := &Agent{sessions: newClaudeSessionStore(), initialized: true}
	_, err := agent.Prompt(context.Background(), acp.PromptRequest{
		SessionId: "missing-session",
		Prompt:    []acp.ContentBlock{acp.TextBlock("continue")},
	})
	var requestErr *acp.RequestError
	if !errors.As(err, &requestErr) || requestErr.Code != acpResourceNotFoundCode {
		t.Fatalf("missing Session error = %v", err)
	}
}

// SessionUpdate 记录一条 ACP 更新。
func (c *recordingClaudeClient) SessionUpdate(_ context.Context, notification acp.SessionNotification) error {
	c.mu.Lock()
	c.updates = append(c.updates, notification)
	c.mu.Unlock()
	return nil
}

// RequestPermission 记录请求并选择 allow_once。
func (c *recordingClaudeClient) RequestPermission(_ context.Context, request acp.RequestPermissionRequest) (acp.RequestPermissionResponse, error) {
	c.mu.Lock()
	c.permissions = append(c.permissions, request)
	c.mu.Unlock()
	return acp.RequestPermissionResponse{Outcome: acp.RequestPermissionOutcome{
		Selected: &acp.RequestPermissionOutcomeSelected{Outcome: "selected", OptionId: permissionAllowOnce},
	}}, nil
}

// UnstableCreateElicitation 记录 form 请求并选择第一个问题的 Yes 选项。
func (c *recordingClaudeClient) UnstableCreateElicitation(
	_ context.Context,
	request acp.UnstableCreateElicitationRequest,
) (acp.UnstableCreateElicitationResponse, error) {
	c.mu.Lock()
	c.elicitations = append(c.elicitations, request)
	c.mu.Unlock()
	response := acp.NewUnstableCreateElicitationResponseAccept()
	response.Accept.Content = map[string]any{"question_0": "Yes"}
	return response, nil
}

// snapshot 返回不共享底层数组的记录快照。
func (c *recordingClaudeClient) snapshot() ([]acp.SessionNotification, []acp.RequestPermissionRequest) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]acp.SessionNotification(nil), c.updates...), append([]acp.RequestPermissionRequest(nil), c.permissions...)
}

// TestClaudeAgentSessionPromptPermissionConfigAndCancel 覆盖真实进程边界上的核心 V1 生命周期。
func TestClaudeAgentSessionPromptPermissionConfigAndCancel(t *testing.T) {
	t.Setenv(fakeClaudeProcessEnv, "1")
	executablePath, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	agent, err := NewAgent(ctx, Config{Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), ClaudePath: executablePath})
	if err != nil {
		t.Fatal(err)
	}
	agent.allowBypassPermissions = true
	t.Cleanup(func() { _ = agent.Close(context.Background()) })
	client := &recordingClaudeClient{}
	agent.connectionMu.Lock()
	agent.updater = client
	agent.permissionRequester = client
	agent.elicitationRequester = client
	agent.connectionMu.Unlock()

	initialized, err := agent.Initialize(ctx, acp.InitializeRequest{
		ClientCapabilities: acp.ClientCapabilities{
			Elicitation: &acp.ElicitationCapabilities{Form: &acp.ElicitationFormCapabilities{}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if initialized.AgentInfo == nil || initialized.AgentInfo.Name != "claude" || len(initialized.AuthMethods) != 0 ||
		!initialized.AgentCapabilities.LoadSession || initialized.AgentCapabilities.Auth.Logout != nil {
		t.Fatalf("Initialize() = %#v", initialized)
	}
	created, err := agent.NewSession(ctx, acp.NewSessionRequest{Cwd: t.TempDir(), McpServers: []acp.McpServer{}})
	if err != nil {
		t.Fatal(err)
	}
	if created.SessionId == "" || len(created.ConfigOptions) != 4 || created.Modes == nil {
		t.Fatalf("NewSession() = %#v", created)
	}

	response, err := agent.Prompt(ctx, acp.PromptRequest{
		SessionId: created.SessionId, Prompt: []acp.ContentBlock{acp.TextBlock("hello")},
	})
	if err != nil || response.StopReason != acp.StopReasonEndTurn || response.Usage == nil || response.Usage.OutputTokens != 5 {
		t.Fatalf("Prompt() = %#v, %v", response, err)
	}
	updates, _ := client.snapshot()
	assertRuntimeUpdates(t, updates)

	steeredPrompt := make(chan acp.PromptResponse, 1)
	steeredError := make(chan error, 1)
	go func() {
		result, promptError := agent.Prompt(ctx, acp.PromptRequest{
			SessionId: created.SessionId, Prompt: []acp.ContentBlock{acp.TextBlock("wait-steer")},
		})
		steeredPrompt <- result
		steeredError <- promptError
	}()
	waitForActiveTurn(t, agent, string(created.SessionId))
	steeringResult, err := agent.HandleExtensionMethod(ctx, "_session/steering", json.RawMessage(fmt.Sprintf(
		`{"sessionId":%q,"prompt":[{"type":"text","text":"follow up"}]}`,
		created.SessionId,
	)))
	if err != nil {
		t.Fatalf("steering = %v", err)
	}
	if outcome, ok := steeringResult.(map[string]any); !ok || outcome["outcome"] != "injected" {
		t.Fatalf("steering result = %#v", steeringResult)
	}
	if result := <-steeredPrompt; result.StopReason != acp.StopReasonEndTurn {
		t.Fatalf("steered prompt = %#v", result)
	}
	if err := <-steeredError; err != nil {
		t.Fatalf("steered prompt error = %v", err)
	}

	permissionResponse, err := agent.Prompt(ctx, acp.PromptRequest{
		SessionId: created.SessionId, Prompt: []acp.ContentBlock{acp.TextBlock("permission")},
	})
	if err != nil || permissionResponse.StopReason != acp.StopReasonEndTurn {
		t.Fatalf("permission Prompt() = %#v, %v", permissionResponse, err)
	}
	_, permissions := client.snapshot()
	if len(permissions) != 1 || permissions[0].ToolCall.ToolCallId != "permission-tool" {
		t.Fatalf("permissions = %#v", permissions)
	}
	questionResponse, err := agent.Prompt(ctx, acp.PromptRequest{
		SessionId: created.SessionId,
		Prompt:    []acp.ContentBlock{acp.TextBlock("question")},
	})
	if err != nil || questionResponse.StopReason != acp.StopReasonEndTurn {
		t.Fatalf("question Prompt() = %#v, %v", questionResponse, err)
	}
	client.mu.Lock()
	if len(client.elicitations) != 1 || client.elicitations[0].Form == nil {
		t.Fatalf("elicitations = %#v", client.elicitations)
	}
	client.mu.Unlock()

	if _, err := agent.SetSessionConfigOption(ctx, acp.SetSessionConfigOptionRequest{ValueId: &acp.SetSessionConfigOptionValueId{
		SessionId: created.SessionId, ConfigId: modelConfigID, Value: "opus",
	}}); err != nil {
		t.Fatalf("SetSessionConfigOption(model) = %v", err)
	}
	if _, err := agent.SetSessionMode(ctx, acp.SetSessionModeRequest{SessionId: created.SessionId, ModeId: "plan"}); err != nil {
		t.Fatalf("SetSessionMode() = %v", err)
	}
	if _, err := agent.SetSessionMode(ctx, acp.SetSessionModeRequest{
		SessionId: created.SessionId,
		ModeId:    "bypassPermissions",
	}); err != nil {
		t.Fatalf("SetSessionMode(bypassPermissions) = %v", err)
	}

	promptDone := make(chan acp.PromptResponse, 1)
	promptErr := make(chan error, 1)
	go func() {
		result, promptError := agent.Prompt(ctx, acp.PromptRequest{
			SessionId: created.SessionId, Prompt: []acp.ContentBlock{acp.TextBlock("wait")},
		})
		promptDone <- result
		promptErr <- promptError
	}()
	waitForActiveTurn(t, agent, string(created.SessionId))
	if err := agent.Cancel(ctx, acp.CancelNotification{SessionId: created.SessionId}); err != nil {
		t.Fatal(err)
	}
	if result := <-promptDone; result.StopReason != acp.StopReasonCancelled {
		t.Fatalf("cancel result = %#v", result)
	}
	if err := <-promptErr; err != nil {
		t.Fatalf("cancel prompt error = %v", err)
	}
	if _, err := agent.CloseSession(ctx, acp.CloseSessionRequest{SessionId: created.SessionId}); err != nil {
		t.Fatal(err)
	}
}

// TestClaudeAgentCancelUnresponsiveInterrupt 验证 CLI 不响应 interrupt 时取消仍会有界返回并关闭 Session。
func TestClaudeAgentCancelUnresponsiveInterrupt(t *testing.T) {
	t.Setenv(fakeClaudeProcessEnv, "1")
	executablePath, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	agent, err := NewAgent(context.Background(), Config{
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), ClaudePath: executablePath,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = agent.Close(context.Background()) })
	agent.idGenerator = func() (string, error) { return "ignore-interrupt-session", nil }
	agent.connectionMu.Lock()
	agent.updater = &recordingClaudeClient{}
	agent.connectionMu.Unlock()
	if _, err := agent.Initialize(context.Background(), acp.InitializeRequest{}); err != nil {
		t.Fatal(err)
	}
	created, err := agent.NewSession(context.Background(), acp.NewSessionRequest{Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	promptResult := make(chan acp.PromptResponse, 1)
	promptError := make(chan error, 1)
	go func() {
		response, promptErr := agent.Prompt(context.Background(), acp.PromptRequest{
			SessionId: created.SessionId, Prompt: []acp.ContentBlock{acp.TextBlock("wait-no-interrupt")},
		})
		promptResult <- response
		promptError <- promptErr
	}()
	waitForActiveTurn(t, agent, string(created.SessionId))

	cancelCtx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	err = agent.Cancel(cancelCtx, acp.CancelNotification{SessionId: created.SessionId})
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Cancel() error = %v", err)
	}
	if response := <-promptResult; response.StopReason != acp.StopReasonCancelled {
		t.Fatalf("Prompt() = %#v", response)
	}
	if err := <-promptError; err != nil {
		t.Fatalf("Prompt() error = %v", err)
	}
	session, ok := agent.sessions.get(string(created.SessionId))
	if !ok || session.isOpen() {
		t.Fatalf("Session 应在 interrupt 超时后关闭：exists=%v open=%v", ok, ok && session.isOpen())
	}
}

// assertRuntimeUpdates 验证文本去重、工具、计划和 usage 都通过事件 mapper。
func assertRuntimeUpdates(t *testing.T, updates []acp.SessionNotification) {
	t.Helper()
	var textCount, toolStartCount, toolCompleteCount, planCount, usageCount int
	for _, notification := range updates {
		switch {
		case notification.Update.AgentMessageChunk != nil:
			textCount++
			if notification.Update.AgentMessageChunk.Content.Text == nil || notification.Update.AgentMessageChunk.Content.Text.Text != "answer" {
				t.Fatalf("message update = %#v", notification.Update.AgentMessageChunk)
			}
		case notification.Update.ToolCall != nil:
			toolStartCount++
		case notification.Update.ToolCallUpdate != nil && notification.Update.ToolCallUpdate.Status != nil:
			toolCompleteCount++
		case notification.Update.Plan != nil:
			planCount++
		case notification.Update.UsageUpdate != nil:
			usageCount++
		}
	}
	if textCount != 1 || toolStartCount != 1 || toolCompleteCount != 1 || planCount != 1 || usageCount != 1 {
		t.Fatalf("updates 统计 text=%d start=%d complete=%d plan=%d usage=%d：%#v", textCount, toolStartCount, toolCompleteCount, planCount, usageCount, updates)
	}
}

// waitForActiveTurn 等待 worker 安装活动 turn，不依赖固定 sleep。
func waitForActiveTurn(t *testing.T, agent *Agent, sessionID string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if session, ok := agent.sessions.get(sessionID); ok && session.activeTurn() != nil {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("等待活动 turn 超时")
}

// runFakeClaudeProcess 实现测试所需的最小 stream-json/control CLI。
func runFakeClaudeProcess(args []string, input io.Reader, output io.Writer) int {
	if len(args) == 1 && args[0] == "--version" {
		_, _ = fmt.Fprintln(output, "2.1.232")
		return 0
	}
	sessionID := fakeSessionID(args)
	encoder := json.NewEncoder(output)
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 64<<10), 8<<20)
	permissionPending := false
	pendingToolID := ""
	ignoreInterrupt := strings.Contains(sessionID, "ignore-interrupt")
	for scanner.Scan() {
		var header struct {
			// Type 是输入消息判别值。
			Type string `json:"type"`
			// RequestID 是 control request 标识。
			RequestID string `json:"request_id"`
			// Request 保存 control payload。
			Request json.RawMessage `json:"request"`
		}
		if json.Unmarshal(scanner.Bytes(), &header) != nil {
			continue
		}
		switch header.Type {
		case protocol.TypeControlRequest:
			var control struct {
				// Subtype 是 control payload 判别值。
				Subtype string `json:"subtype"`
			}
			_ = json.Unmarshal(header.Request, &control)
			if control.Subtype == protocol.ControlInterrupt && ignoreInterrupt {
				continue
			}
			response := map[string]any{}
			if control.Subtype == protocol.ControlInitialize {
				response = map[string]any{
					"models": []map[string]any{{
						"value": "sonnet", "displayName": "Sonnet", "description": "Balanced",
						"supportsEffort": true, "supportedEffortLevels": []string{"low", "medium", "high"},
						"supportsFastMode": true, "supportsAutoMode": true,
					}, {"value": "opus", "displayName": "Opus", "description": "Deep"}},
					"fast_mode_state": "off",
				}
			}
			_ = encoder.Encode(map[string]any{"type": "control_response", "response": map[string]any{
				"subtype": "success", "request_id": header.RequestID, "response": response,
			}})
			if control.Subtype == protocol.ControlInitialize {
				_ = encoder.Encode(map[string]any{
					"type": "system", "subtype": "init", "session_id": sessionID, "uuid": "init-1",
					"claude_code_version": "2.1.232", "cwd": "/tmp", "model": "sonnet",
					"permissionMode": "default", "tools": []string{"Read", "Bash"}, "mcp_servers": []any{},
				})
			}
			if control.Subtype == protocol.ControlInterrupt && !ignoreInterrupt {
				emitFakeResult(encoder, sessionID, "cancel-result")
			}
		case protocol.TypeUser:
			var message protocol.UserInputMessage
			if json.Unmarshal(scanner.Bytes(), &message) != nil {
				continue
			}
			_ = encoder.Encode(map[string]any{
				"type": "user", "message": message.Message, "parent_tool_use_id": nil,
				"uuid": message.UUID, "session_id": sessionID,
			})
			if strings.Contains(string(message.Message.Content), "wait") && message.Priority == "" {
				continue
			}
			if strings.Contains(string(message.Message.Content), "permission") {
				permissionPending = true
				pendingToolID = "permission-tool"
				_ = encoder.Encode(map[string]any{
					"type": "assistant",
					"message": map[string]any{
						"id": "permission-message", "role": "assistant", "content": []map[string]any{{
							"type": "tool_use", "id": "permission-tool", "name": "Read", "input": map[string]any{"file_path": "/tmp/a"},
						}},
					},
					"parent_tool_use_id": nil, "uuid": "permission-assistant", "session_id": sessionID,
				})
				_ = encoder.Encode(map[string]any{
					"type": "control_request", "request_id": "permission-request", "request": map[string]any{
						"subtype": "can_use_tool", "tool_name": "Read", "input": map[string]any{"file_path": "/tmp/a"},
						"tool_use_id": "permission-tool", "permission_suggestions": []map[string]any{{"type": "addRules"}},
					},
				})
				continue
			}
			if strings.Contains(string(message.Message.Content), "question") {
				permissionPending = true
				pendingToolID = "question-tool"
				_ = encoder.Encode(map[string]any{
					"type": "control_request", "request_id": "question-request", "request": map[string]any{
						"subtype": "can_use_tool", "tool_name": "AskUserQuestion",
						"input": map[string]any{"questions": []map[string]any{{
							"question": "Continue?", "header": "Decision",
							"options": []map[string]any{{"label": "Yes", "description": "Continue"}},
						}}},
						"tool_use_id": "question-tool",
					},
				})
				continue
			}
			emitFakeTurn(encoder, sessionID, message.UUID)
		case protocol.TypeControlResponse:
			if permissionPending {
				permissionPending = false
				_ = encoder.Encode(map[string]any{
					"type": "user",
					"message": map[string]any{
						"role": "user", "content": []map[string]any{{
							"type": "tool_result", "tool_use_id": pendingToolID, "content": "allowed",
						}},
					},
					"parent_tool_use_id": nil, "uuid": "permission-result", "session_id": sessionID,
				})
				emitFakeResult(encoder, sessionID, "permission-finished")
				pendingToolID = ""
			}
		}
	}
	if err := scanner.Err(); err != nil && !errors.Is(err, os.ErrClosed) {
		return 1
	}
	return 0
}

// fakeSessionID 从创建或恢复参数中读取 Session ID。
func fakeSessionID(args []string) string {
	for _, argument := range args {
		if value, ok := strings.CutPrefix(argument, "--session-id="); ok {
			return value
		}
		if value, ok := strings.CutPrefix(argument, "--resume="); ok {
			return value
		}
	}
	return "missing-session"
}

// emitFakeTurn 输出包含文本去重、工具、任务与 usage 的完整 turn。
func emitFakeTurn(encoder *json.Encoder, sessionID, messageID string) {
	_ = encoder.Encode(map[string]any{
		"type": "stream_event", "event": map[string]any{
			"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "text_delta", "text": "answer"},
		}, "parent_tool_use_id": nil, "uuid": "assistant-stream", "session_id": sessionID,
	})
	_ = encoder.Encode(map[string]any{
		"type": "assistant", "message": map[string]any{
			"id": "assistant-message", "role": "assistant", "content": []map[string]any{
				{"type": "text", "text": "answer"},
				{"type": "tool_use", "id": "tool-1", "name": "Read", "input": map[string]any{"file_path": "/tmp/a"}},
			},
		},
		"parent_tool_use_id": nil, "uuid": "assistant-finished", "session_id": sessionID,
	})
	_ = encoder.Encode(map[string]any{
		"type": "tool_progress", "tool_use_id": "tool-1", "tool_name": "Read", "parent_tool_use_id": nil,
		"elapsed_time_seconds": 0.2, "uuid": "progress", "session_id": sessionID,
	})
	_ = encoder.Encode(map[string]any{
		"type": "user", "message": map[string]any{
			"role": "user", "content": []map[string]any{{
				"type": "tool_result", "tool_use_id": "tool-1", "content": "file text",
			}},
		},
		"parent_tool_use_id": nil, "uuid": "tool-result", "session_id": sessionID,
	})
	_ = encoder.Encode(map[string]any{
		"type": "system", "subtype": "task_started", "task_id": "task-1", "description": "Inspect file",
		"status": "in_progress", "uuid": "task", "session_id": sessionID,
	})
	emitFakeResult(encoder, sessionID, messageID+"-result")
}

// emitFakeResult 输出成功 result 与 idle 边界。
func emitFakeResult(encoder *json.Encoder, sessionID, uuid string) {
	_ = encoder.Encode(map[string]any{
		"type": "result", "subtype": "success", "session_id": sessionID, "uuid": uuid, "is_error": false,
		"stop_reason": "end_turn", "result": "answer",
		"usage":      map[string]any{"input_tokens": 10, "output_tokens": 5, "cache_read_input_tokens": 2, "cache_creation_input_tokens": 1},
		"modelUsage": map[string]any{"sonnet": map[string]any{"inputTokens": 10, "outputTokens": 5, "contextWindow": 200000}},
	})
	_ = encoder.Encode(map[string]any{
		"type": "system", "subtype": "session_state_changed", "state": "idle", "session_id": sessionID, "uuid": uuid + "-idle",
	})
}

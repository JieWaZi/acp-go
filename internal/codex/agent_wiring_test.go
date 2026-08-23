package codex

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"acp-go/agents/codex/protocol"

	acp "github.com/coder/acp-go-sdk"
)

// recordingHistoryUpdater 记录真实 eventHandler 产生的 ACP history updates。
type recordingHistoryUpdater struct {
	// notifications 保留收到的更新顺序。
	notifications []acp.SessionNotification
	// err 使外部 SDK callback 可确定性失败。
	err error
}

// barrierHistoryUpdater 在第一条历史通知内提供可控 generation 切换屏障。
type barrierHistoryUpdater struct {
	// first 在第一条通知已进入 callback 时关闭。
	first chan struct{}
	// release 允许第一条 callback 返回。
	release chan struct{}
	// mu 保护 count。
	mu sync.Mutex
	// count 记录 stale 切换前后实际发送的通知数。
	count int
}

// SessionUpdate 在首条 callback 暂停，使测试可在两个 content block 之间关闭 session。
func (u *barrierHistoryUpdater) SessionUpdate(context.Context, acp.SessionNotification) error {
	u.mu.Lock()
	u.count++
	count := u.count
	u.mu.Unlock()
	if count == 1 {
		close(u.first)
		<-u.release
	}
	return nil
}

// notificationCount 返回当前通知数快照。
func (u *barrierHistoryUpdater) notificationCount() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.count
}

// SessionUpdate 记录通知或返回预置错误。
func (u *recordingHistoryUpdater) SessionUpdate(
	_ context.Context,
	notification acp.SessionNotification,
) error {
	if u.err != nil {
		return u.err
	}
	u.notifications = append(u.notifications, notification)
	return nil
}

// decodeTestNotification 从手工 wire fixture 创建生成协议的强类型通知。
func decodeTestNotification(t *testing.T, method string, params any) protocol.ServerNotification {
	t.Helper()
	wire, err := json.Marshal(map[string]any{"method": method, "params": params})
	if err != nil {
		t.Fatalf("编码通知 fixture 失败: %v", err)
	}
	notification, err := protocol.DecodeServerNotification(wire)
	if err != nil {
		t.Fatalf("解码通知 fixture 失败: %v", err)
	}
	return notification
}

// TestAgentAuthenticationWiresTypedAppServerFlow 验证生产 Agent 复用 authenticator 与 typed client。
// 若 Authenticate/Logout 仍是占位，或在请求之后才订阅完成通知，本测试应失败。
func TestAgentAuthenticationWiresTypedAppServerFlow(t *testing.T) {
	t.Parallel()

	rpc := newFakeAppServerRPC()
	runtimeCtx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	client := newAppServerClient(runtimeCtx, rpc)
	agent := newAgentWithClient(
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		runtimeCtx,
		cancel,
		client,
	)
	rpc.handleCall = func(_ context.Context, request protocol.ClientRequest, result any) error {
		switch request.Method() {
		case protocol.MethodInitialize:
			return nil
		case protocol.MethodAccountLoginStart:
			result.(*protocol.LoginAccountResponse).Type = protocol.TypeAPIKey
			client.HandleNotification(context.Background(), decodeTestNotification(
				t,
				protocol.MethodAccountLoginCompleted,
				protocol.AccountLoginCompletedNotification{Success: true},
			))
			return nil
		case protocol.MethodAccountLogout:
			client.HandleNotification(context.Background(), decodeTestNotification(
				t,
				protocol.MethodAccountUpdated,
				map[string]any{"account": nil},
			))
			return nil
		default:
			return errors.New("unexpected call: " + request.Method())
		}
	}
	initializeTestAgent(t, agent)

	if _, err := agent.Authenticate(context.Background(), acp.AuthenticateRequest{
		MethodId: "api-key",
		Meta: map[string]any{
			"api-key": map[string]any{"apiKey": "TEST_ONLY_TOKEN"},
		},
	}); err != nil {
		t.Fatalf("Authenticate 返回错误: %v", err)
	}
	if _, err := agent.Logout(context.Background(), acp.LogoutRequest{}); err != nil {
		t.Fatalf("Logout 返回错误: %v", err)
	}
	if got := rpc.calls; len(got) != 3 || got[1] != protocol.MethodAccountLoginStart ||
		got[2] != protocol.MethodAccountLogout {
		t.Fatalf("认证调用顺序为 %v", got)
	}
}

// TestAgentSessionConfigurationFlowsIntoTurnStart 验证已存在的配置组件接入 session 与 prompt。
// 若 set 操作只改响应、不进入后续 turn/start 的 typed 参数，本测试应失败。
func TestAgentSessionConfigurationFlowsIntoTurnStart(t *testing.T) {
	t.Parallel()

	rpc := newFakeAppServerRPC()
	runtimeCtx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	client := newAppServerClient(runtimeCtx, rpc)
	agent := newAgentWithClient(
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		runtimeCtx,
		cancel,
		client,
	)
	agent.markConnectionReady()
	var turnParams protocol.TurnStartParams
	rpc.handleCall = func(_ context.Context, request protocol.ClientRequest, result any) error {
		switch request.Method() {
		case protocol.MethodInitialize:
			return nil
		case protocol.MethodThreadStart:
			response := result.(*protocol.ThreadStartResponse)
			response.Thread.ID = "configured-thread"
			response.Model = "fast-model"
			response.ReasoningEffort = acp.Ptr("medium")
			return nil
		case protocol.MethodModelList:
			result.(*protocol.ModelListResponse).Data = testModels()
			return nil
		case protocol.MethodThreadUnsubscribe:
			return nil
		case protocol.MethodTurnStart:
			wire, err := json.Marshal(request)
			if err != nil {
				return err
			}
			var envelope struct {
				// Params 是固定 turn/start 的 typed 参数。
				Params protocol.TurnStartParams `json:"params"`
			}
			if err = json.Unmarshal(wire, &envelope); err != nil {
				return err
			}
			turnParams = envelope.Params
			result.(*protocol.TurnStartResponse).Turn = protocol.TurnElement{
				ID: "configured-turn", Items: []protocol.ThreadItem{}, Status: protocol.PurpleInProgress,
			}
			client.HandleNotification(context.Background(), decodeTestNotification(
				t,
				protocol.MethodTurnCompleted,
				protocol.TurnCompletedNotification{
					ThreadID: "configured-thread",
					Turn: protocol.TurnElement{
						ID: "configured-turn", Items: []protocol.ThreadItem{}, Status: protocol.FluffyCompleted,
					},
				},
			))
			return nil
		default:
			return errors.New("unexpected call: " + request.Method())
		}
	}
	initializeTestAgent(t, agent)

	created, err := agent.NewSession(context.Background(), acp.NewSessionRequest{
		Cwd: "/workspace", McpServers: []acp.McpServer{},
	})
	if err != nil {
		t.Fatalf("NewSession 返回错误: %v", err)
	}
	if created.Modes == nil || created.Modes.CurrentModeId != "agent" || len(created.ConfigOptions) != 3 {
		t.Fatalf("session 初始配置为 modes=%#v options=%#v", created.Modes, created.ConfigOptions)
	}
	if _, err = agent.SetSessionMode(context.Background(), acp.SetSessionModeRequest{
		SessionId: created.SessionId, ModeId: "agent-full-access",
	}); err != nil {
		t.Fatalf("SetSessionMode 返回错误: %v", err)
	}
	if _, err = agent.SetSessionMode(context.Background(), acp.SetSessionModeRequest{
		SessionId: created.SessionId, ModeId: "unknown-mode",
	}); err == nil {
		t.Fatal("未知 mode 应被拒绝")
	} else if requestErr := (*acp.RequestError)(nil); !errors.As(err, &requestErr) || requestErr.Code != -32602 {
		t.Fatalf("未知 mode 错误为 %T %v，期望 InvalidParams", err, err)
	}
	updated, err := agent.SetSessionConfigOption(context.Background(), acp.SetSessionConfigOptionRequest{
		ValueId: &acp.SetSessionConfigOptionValueId{
			SessionId: created.SessionId, ConfigId: modelConfigID, Value: "slow-model",
		},
	})
	if err != nil {
		t.Fatalf("SetSessionConfigOption 返回错误: %v", err)
	}
	if len(updated.ConfigOptions) != 3 || selectOption(t, updated.ConfigOptions, modelConfigID).CurrentValue != "slow-model" {
		t.Fatalf("更新后配置为 %#v", updated.ConfigOptions)
	}

	response, err := agent.Prompt(context.Background(), acp.PromptRequest{
		SessionId: created.SessionId,
		Prompt:    []acp.ContentBlock{{Text: &acp.ContentBlockText{Type: "text", Text: "hello"}}},
	})
	if err != nil || response.StopReason != acp.StopReasonEndTurn {
		t.Fatalf("Prompt 响应为 %#v, %v", response, err)
	}
	if turnParams.Model == nil || *turnParams.Model != "slow-model" ||
		turnParams.Effort == nil || *turnParams.Effort != "medium" ||
		turnParams.ApprovalPolicy == nil || turnParams.ApprovalPolicy.Enum == nil ||
		*turnParams.ApprovalPolicy.Enum != protocol.Never ||
		turnParams.SandboxPolicy == nil || turnParams.SandboxPolicy.Type != protocol.SandboxPolicyTypeDangerFullAccess {
		t.Fatalf("turn/start 配置参数为 %#v", turnParams)
	}
	if _, err = agent.CloseSession(context.Background(), acp.CloseSessionRequest{SessionId: created.SessionId}); err != nil {
		t.Fatalf("CloseSession 返回错误: %v", err)
	}
	if _, err = agent.SetSessionMode(context.Background(), acp.SetSessionModeRequest{
		SessionId: created.SessionId, ModeId: "agent",
	}); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("已关闭 session 配置错误为 %v，期望 ErrSessionNotFound", err)
	}
}

// TestAgentRejectsMissingRequiredSessionModel 验证破损 app-server 响应不会让配置能力静默降级。
func TestAgentRejectsMissingRequiredSessionModel(t *testing.T) {
	t.Parallel()

	rpc := newFakeAppServerRPC()
	rpc.handleCall = func(_ context.Context, request protocol.ClientRequest, result any) error {
		switch request.Method() {
		case protocol.MethodInitialize, protocol.MethodThreadUnsubscribe:
			return nil
		case protocol.MethodThreadStart:
			result.(*protocol.ThreadStartResponse).Thread.ID = "missing-model-thread"
			return nil
		default:
			return errors.New("unexpected call: " + request.Method())
		}
	}
	runtimeCtx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	agent := newAgentWithClient(
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		runtimeCtx,
		cancel,
		newAppServerClient(runtimeCtx, rpc),
	)
	initializeTestAgent(t, agent)

	_, err := agent.NewSession(context.Background(), acp.NewSessionRequest{
		Cwd: "/workspace", McpServers: []acp.McpServer{},
	})
	if err == nil || !strings.Contains(err.Error(), "did not include a model") {
		t.Fatalf("缺失 model 错误为 %v", err)
	}
	if got := rpc.calls; len(got) != 3 || got[1] != protocol.MethodThreadStart ||
		got[2] != protocol.MethodThreadUnsubscribe {
		t.Fatalf("缺失 model 调用顺序为 %v", got)
	}
}

// TestAgentFailureCleanupDetachesFromCancelledRequest 验证已获得的远端 thread 使用独立有界 context 释放。
func TestAgentFailureCleanupDetachesFromCancelledRequest(t *testing.T) {
	t.Parallel()

	rpc := newFakeAppServerRPC()
	requestCtx, cancelRequest := context.WithCancel(context.Background())
	cleanupContext := make(chan error, 1)
	rpc.handleCall = func(ctx context.Context, request protocol.ClientRequest, result any) error {
		switch request.Method() {
		case protocol.MethodInitialize:
			return nil
		case protocol.MethodThreadStart:
			response := result.(*protocol.ThreadStartResponse)
			response.Thread.ID = "cancelled-config-thread"
			response.Model = "fast-model"
			return nil
		case protocol.MethodModelList:
			cancelRequest()
			return requestCtx.Err()
		case protocol.MethodThreadUnsubscribe:
			cleanupContext <- ctx.Err()
			return nil
		default:
			return errors.New("unexpected call: " + request.Method())
		}
	}
	runtimeCtx, cancelRuntime := context.WithCancel(context.Background())
	t.Cleanup(cancelRuntime)
	agent := newAgentWithClient(
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		runtimeCtx,
		cancelRuntime,
		newAppServerClient(runtimeCtx, rpc),
	)
	initializeTestAgent(t, agent)

	if _, err := agent.NewSession(requestCtx, acp.NewSessionRequest{
		Cwd: "/workspace", McpServers: []acp.McpServer{},
	}); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消配置错误为 %v，期望 context.Canceled", err)
	}
	if cleanupErr := <-cleanupContext; cleanupErr != nil {
		t.Fatalf("unsubscribe 继承了已取消请求 context: %v", cleanupErr)
	}
}

// TestAgentLoadReplaysHistoryThroughExistingMappers 验证 load 复用 eventHandler/toolMapper 并去重完成项。
func TestAgentLoadReplaysHistoryThroughExistingMappers(t *testing.T) {
	t.Parallel()

	rpc := newFakeAppServerRPC()
	text := "hello from history"
	answer := "history answer"
	completedStatus := "completed"
	command := "/bin/sh -lc ls"
	cwd := "/workspace"
	output := "README.md\n"
	exitCode := int64(0)
	mcpServer := "github"
	mcpTool := "search"
	rpc.handleCall = func(_ context.Context, request protocol.ClientRequest, result any) error {
		switch request.Method() {
		case protocol.MethodInitialize:
			return nil
		case protocol.MethodThreadResume:
			result.(*protocol.ThreadResumeResponse).Thread.ID = "history-thread"
			return nil
		case protocol.MethodThreadRead:
			result.(*protocol.ThreadReadResponse).Thread = protocol.Thread{
				ID: "history-thread",
				Turns: []protocol.TurnElement{{
					ID: "history-turn",
					Items: []protocol.ThreadItem{
						{
							ID: "user-1", Type: protocol.UserMessage,
							Content: []protocol.ContentElement{{UserInput: &protocol.UserInput{
								Type: protocol.UserInputTypeText, Text: &text,
							}}},
						},
						{ID: "agent-1", Type: protocol.AgentMessage, Text: &answer},
						{ID: "agent-1", Type: protocol.AgentMessage, Text: &answer},
						{
							ID: "command-1", Type: protocol.CommandExecution, Status: &completedStatus,
							Command: &command, Cwd: &cwd, AggregatedOutput: &output, ExitCode: &exitCode,
						},
						{
							ID: "file-1", Type: protocol.FileChange, Status: &completedStatus,
							Changes: []protocol.ChangeElement{{
								Path: "/workspace/README.md", Diff: "hello\n",
								Kind: protocol.PatchChangeKind{Type: protocol.Add},
							}},
						},
						{
							ID: "mcp-1", Type: protocol.MCPToolCall, Status: &completedStatus,
							Server: &mcpServer, Tool: &mcpTool, Arguments: json.RawMessage(`{"query":"codex"}`),
						},
					},
				}},
			}
			return nil
		default:
			return errors.New("unexpected call: " + request.Method())
		}
	}
	agent := newRuntimeTestAgent(t, rpc)
	if _, err := agent.Initialize(context.Background(), acp.InitializeRequest{
		ProtocolVersion: acp.ProtocolVersionNumber,
		ClientCapabilities: acp.ClientCapabilities{
			Meta: map[string]any{"terminal_output": true},
		},
	}); err != nil {
		t.Fatalf("以 terminal_output 能力重新初始化 Agent 失败: %v", err)
	}
	updater := &recordingHistoryUpdater{}
	agent.connectionMu.Lock()
	agent.sessionUpdater = updater
	agent.connectionMu.Unlock()

	if _, err := agent.LoadSession(context.Background(), acp.LoadSessionRequest{
		SessionId: "history-thread", Cwd: "/workspace", McpServers: []acp.McpServer{},
	}); err != nil {
		t.Fatalf("LoadSession 返回错误: %v", err)
	}
	if len(updater.notifications) != 6 {
		t.Fatalf("历史更新数为 %d，期望去重并补齐工具后 6", len(updater.notifications))
	}
	user := updater.notifications[0].Update.UserMessageChunk
	if user == nil || user.Content.Text == nil || user.Content.Text.Text != text {
		t.Fatalf("用户历史更新为 %#v", updater.notifications[0])
	}
	agentMessage := updater.notifications[1].Update.AgentMessageChunk
	if agentMessage == nil || agentMessage.Content.Text == nil || agentMessage.Content.Text.Text != answer {
		t.Fatalf("Agent 历史更新为 %#v", updater.notifications[1])
	}
	commandStart := updater.notifications[2].Update.ToolCall
	commandComplete := updater.notifications[3].Update.ToolCallUpdate
	if commandStart == nil || commandStart.Title != "ls" || commandStart.Status != acp.ToolCallStatusCompleted ||
		commandComplete == nil || commandComplete.Status == nil ||
		*commandComplete.Status != acp.ToolCallStatusCompleted || commandComplete.Meta == nil {
		t.Fatalf("命令历史更新为 %#v / %#v", updater.notifications[2], updater.notifications[3])
	}
	assertMetaWire(t, commandComplete.Meta, `{"terminal_exit":{"exit_code":0,"signal":null,"terminal_id":"command-1"},"terminal_output":{"data":"README.md\n","terminal_id":"command-1"}}`)
	fileStart := updater.notifications[4].Update.ToolCall
	if fileStart == nil || fileStart.Title != "Editing files" || fileStart.Kind != acp.ToolKindEdit ||
		fileStart.Status != acp.ToolCallStatusCompleted || len(fileStart.Content) != 1 {
		t.Fatalf("文件历史更新为 %#v", updater.notifications[4])
	}
	mcpStart := updater.notifications[5].Update.ToolCall
	if mcpStart == nil || mcpStart.Title != "mcp.github.search" ||
		mcpStart.Status != acp.ToolCallStatusCompleted || mcpStart.RawInput == nil {
		t.Fatalf("MCP 历史更新为 %#v", updater.notifications[5])
	}
}

// TestAgentLoadHistoryFailureDoesNotLeaveInstalledSession 验证外层 session/update 失败会清理订阅状态。
// 若失败 load 仍能被后续 prompt 当成有效 session，本测试应失败。
func TestAgentLoadHistoryFailureDoesNotLeaveInstalledSession(t *testing.T) {
	t.Parallel()

	rpc := newFakeAppServerRPC()
	text := "history"
	rpc.handleCall = func(_ context.Context, request protocol.ClientRequest, result any) error {
		switch request.Method() {
		case protocol.MethodInitialize, protocol.MethodThreadUnsubscribe:
			return nil
		case protocol.MethodThreadResume:
			result.(*protocol.ThreadResumeResponse).Thread.ID = "failed-history-thread"
			return nil
		case protocol.MethodThreadRead:
			result.(*protocol.ThreadReadResponse).Thread = protocol.Thread{
				ID: "failed-history-thread",
				Turns: []protocol.TurnElement{{
					ID: "turn-1",
					Items: []protocol.ThreadItem{{
						ID: "user-1", Type: protocol.UserMessage,
						Content: []protocol.ContentElement{{UserInput: &protocol.UserInput{
							Type: protocol.UserInputTypeText, Text: &text,
						}}},
					}},
				}},
			}
			return nil
		default:
			return errors.New("unexpected call: " + request.Method())
		}
	}
	agent := newRuntimeTestAgent(t, rpc)
	agent.connectionMu.Lock()
	agent.sessionUpdater = &recordingHistoryUpdater{err: errors.New("client disconnected")}
	agent.connectionMu.Unlock()

	if _, err := agent.LoadSession(context.Background(), acp.LoadSessionRequest{
		SessionId: "failed-history-thread", Cwd: "/workspace", McpServers: []acp.McpServer{},
	}); err == nil {
		t.Fatal("history callback 失败时 LoadSession 应返回错误")
	}
	if _, installed := agent.sessions.get("failed-history-thread"); installed {
		t.Fatal("history callback 失败后仍留下已安装 session")
	}
	if got := rpc.calls; len(got) != 5 || got[3] != protocol.MethodModelList ||
		got[4] != protocol.MethodThreadUnsubscribe {
		t.Fatalf("失败 load 调用顺序为 %v，期望最终 unsubscribe", got)
	}
}

// TestAgentLoadStopsBetweenUserHistoryBlocksAfterClose 验证每次外部 callback 前后都检查 generation。
func TestAgentLoadStopsBetweenUserHistoryBlocksAfterClose(t *testing.T) {
	t.Parallel()

	rpc := newFakeAppServerRPC()
	first := "first"
	second := "second"
	rpc.handleCall = func(_ context.Context, request protocol.ClientRequest, result any) error {
		switch request.Method() {
		case protocol.MethodInitialize, protocol.MethodThreadUnsubscribe:
			return nil
		case protocol.MethodThreadResume:
			result.(*protocol.ThreadResumeResponse).Thread.ID = "multi-block-thread"
			return nil
		case protocol.MethodThreadRead:
			result.(*protocol.ThreadReadResponse).Thread = protocol.Thread{
				ID: "multi-block-thread",
				Turns: []protocol.TurnElement{{
					ID: "turn-1",
					Items: []protocol.ThreadItem{{
						ID: "user-1", Type: protocol.UserMessage,
						Content: []protocol.ContentElement{
							{UserInput: &protocol.UserInput{Type: protocol.UserInputTypeText, Text: &first}},
							{UserInput: &protocol.UserInput{Type: protocol.UserInputTypeText, Text: &second}},
						},
					}},
				}},
			}
			return nil
		default:
			return errors.New("unexpected call: " + request.Method())
		}
	}
	agent := newRuntimeTestAgent(t, rpc)
	updater := &barrierHistoryUpdater{first: make(chan struct{}), release: make(chan struct{})}
	agent.connectionMu.Lock()
	agent.sessionUpdater = updater
	agent.connectionMu.Unlock()
	loadResult := make(chan error, 1)
	go func() {
		_, err := agent.LoadSession(context.Background(), acp.LoadSessionRequest{
			SessionId: "multi-block-thread", Cwd: "/workspace", McpServers: []acp.McpServer{},
		})
		loadResult <- err
	}()
	<-updater.first
	if _, err := agent.CloseSession(context.Background(), acp.CloseSessionRequest{SessionId: "multi-block-thread"}); err != nil {
		t.Fatalf("关闭 history session 失败: %v", err)
	}
	close(updater.release)
	if err := <-loadResult; !errors.Is(err, ErrSessionClosing) {
		t.Fatalf("stale history load 错误为 %v，期望 ErrSessionClosing", err)
	}
	if got := updater.notificationCount(); got != 1 {
		t.Fatalf("session close 后仍发送 %d 条 history block，期望 1", got)
	}
}

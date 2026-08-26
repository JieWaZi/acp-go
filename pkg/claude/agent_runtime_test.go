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
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JieWaZi/acp-go/pkg/claude/protocol"
	acp "github.com/coder/acp-go-sdk"
)

const (
	fakeClaudeProcessEnv          = "ACP_GO_TEST_FAKE_CLAUDE_PROCESS"
	fakeClaudeLaunchConfigGateEnv = "ACP_GO_TEST_REQUIRE_CLAUDE_LAUNCH_CONFIG"
	fakeClaudeCustomEnvironment   = "ACP_GO_TEST_CLAUDE_CUSTOM_ENVIRONMENT"
	fakeClaudePrefixArgument      = "--acp-test-prefix"
)

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
	t.Setenv(fakeClaudeLaunchConfigGateEnv, "1")
	t.Setenv(fakeClaudeCustomEnvironment, "")
	executablePath, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	environment := append(os.Environ(), fakeClaudeCustomEnvironment+"=enabled")
	agent, err := NewAgent(ctx, Config{
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		ClaudePath:  executablePath,
		PrefixArgs:  []string{fakeClaudePrefixArgument, "team"},
		Environment: environment,
	})
	if err != nil {
		t.Fatal(err)
	}
	if agent.executable.Version != "2.1.232" {
		t.Fatalf("版本探测结果为 %q", agent.executable.Version)
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
	workspace := t.TempDir()
	created, err := agent.NewSession(ctx, acp.NewSessionRequest{Cwd: workspace, McpServers: []acp.McpServer{}})
	if err != nil {
		t.Fatal(err)
	}
	if created.SessionId == "" || len(created.ConfigOptions) != 4 || created.Modes == nil {
		t.Fatalf("NewSession() = %#v", created)
	}
	initialCommands := waitForAvailableCommandUpdates(t, client, 1)
	if len(initialCommands) != 3 || initialCommands[0].Name != "diagnosing-bugs" ||
		initialCommands[1].Name != "doctor" || initialCommands[2].Name != "mcp:github" {
		t.Fatalf("initial available commands = %#v", initialCommands)
	}

	commandResponse, err := agent.Prompt(ctx, acp.PromptRequest{
		SessionId: created.SessionId, Prompt: []acp.ContentBlock{acp.TextBlock("commands-change")},
	})
	if err != nil || commandResponse.StopReason != acp.StopReasonEndTurn {
		t.Fatalf("commands Prompt() = %#v, %v", commandResponse, err)
	}
	// 首轮 system/init 先过滤终端命令，commands_changed 随后发布动态目录。
	dynamicCommands := waitForAvailableCommandUpdates(t, client, 3)
	if len(dynamicCommands) != 1 || dynamicCommands[0].Name != "new-skill" {
		t.Fatalf("dynamic available commands = %#v", dynamicCommands)
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
	steeredMessageID := "wait-steer-message"
	go func() {
		result, promptError := agent.Prompt(ctx, acp.PromptRequest{
			SessionId: created.SessionId, MessageId: &steeredMessageID,
			Prompt: []acp.ContentBlock{acp.TextBlock("wait-steer")},
		})
		steeredPrompt <- result
		steeredError <- promptError
	}()
	waitForActiveTurn(t, agent, string(created.SessionId), steeredMessageID)
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
	cancelMessageID := "wait-cancel-message"
	go func() {
		result, promptError := agent.Prompt(ctx, acp.PromptRequest{
			SessionId: created.SessionId, MessageId: &cancelMessageID,
			Prompt: []acp.ContentBlock{acp.TextBlock("wait")},
		})
		promptDone <- result
		promptErr <- promptError
	}()
	waitForActiveTurn(t, agent, string(created.SessionId), cancelMessageID)
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

// TestClaudeAgentUsageSeparatesContextSnapshotFromTurnTotals 锁住 upstream 的两类 Usage 语义。
func TestClaudeAgentUsageSeparatesContextSnapshotFromTurnTotals(t *testing.T) {
	t.Setenv(fakeClaudeProcessEnv, "1")
	executablePath, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	agent, err := NewAgent(ctx, Config{
		Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		ClaudePath: executablePath,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = agent.Close(context.Background()) })
	client := &recordingClaudeClient{}
	agent.connectionMu.Lock()
	agent.updater = client
	agent.connectionMu.Unlock()
	if _, err = agent.Initialize(ctx, acp.InitializeRequest{}); err != nil {
		t.Fatal(err)
	}
	created, err := agent.NewSession(ctx, acp.NewSessionRequest{
		Cwd:        t.TempDir(),
		McpServers: []acp.McpServer{},
	})
	if err != nil {
		t.Fatal(err)
	}

	response, err := agent.Prompt(ctx, acp.PromptRequest{
		SessionId: created.SessionId,
		Prompt:    []acp.ContentBlock{acp.TextBlock("usage-cumulative")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.Usage == nil || response.Usage.InputTokens != 10 ||
		response.Usage.OutputTokens != 5 || response.Usage.TotalTokens != 18 {
		t.Fatalf("PromptResponse Usage = %#v", response.Usage)
	}

	updates, _ := client.snapshot()
	usageUpdates := make([]acp.SessionUsageUpdate, 0, 3)
	for _, notification := range updates {
		if notification.Update.UsageUpdate != nil {
			usageUpdates = append(usageUpdates, *notification.Update.UsageUpdate)
		}
	}
	if len(usageUpdates) != 3 {
		t.Fatalf("Usage updates = %#v", usageUpdates)
	}
	if usageUpdates[0].Used != 30_184 ||
		usageUpdates[0].Size != boundedInt(extendedClaudeContextWindow) {
		t.Fatalf("message_start Usage = %#v", usageUpdates[0])
	}
	if usageUpdates[1].Used != 30_190 ||
		usageUpdates[1].Size != boundedInt(extendedClaudeContextWindow) {
		t.Fatalf("message_delta Usage = %#v", usageUpdates[1])
	}
	if usageUpdates[2].Used != 30_190 ||
		usageUpdates[2].Size != boundedInt(extendedClaudeContextWindow) {
		t.Fatalf("result Usage = %#v", usageUpdates[2])
	}

	session, ok := agent.sessions.get(string(created.SessionId))
	if !ok {
		t.Fatal("Claude Session 不存在")
	}
	session.mu.Lock()
	window := session.contextWindowSize
	authoritative := session.contextWindowAuthoritative
	session.mu.Unlock()
	if window != extendedClaudeContextWindow || !authoritative {
		t.Fatalf("context window = %d authoritative=%v", window, authoritative)
	}
	if cached, ok := agent.cachedContextWindow("claude-opus-4-6-1m"); !ok || cached != extendedClaudeContextWindow {
		t.Fatalf("cached context window = %d, %v", cached, ok)
	}
}

// TestClaudeAgentPublishesCommandsAfterResumeAndLoad 锁定两类恢复入口的初始命令菜单。
func TestClaudeAgentPublishesCommandsAfterResumeAndLoad(t *testing.T) {
	t.Setenv(fakeClaudeProcessEnv, "1")
	configDirectory := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", configDirectory)
	projectDirectory := filepath.Join(configDirectory, "projects", "-workspace")
	if err := os.MkdirAll(projectDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectDirectory, "load-command-session.jsonl"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	executablePath, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	agent, err := NewAgent(ctx, Config{
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		ClaudePath:  executablePath,
		Environment: os.Environ(),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = agent.Close(context.Background()) })
	client := &recordingClaudeClient{}
	agent.connectionMu.Lock()
	agent.updater = client
	agent.connectionMu.Unlock()
	if _, err := agent.Initialize(ctx, acp.InitializeRequest{}); err != nil {
		t.Fatal(err)
	}
	workspace := t.TempDir()
	resumeID := acp.SessionId("resume-command-session")
	if _, err := agent.ResumeSession(ctx, acp.ResumeSessionRequest{
		SessionId: resumeID, Cwd: workspace, McpServers: []acp.McpServer{},
	}); err != nil {
		t.Fatal(err)
	}
	if commands := waitForAvailableCommandUpdates(t, client, 1); len(commands) != 3 {
		t.Fatalf("resume commands = %#v", commands)
	}
	if _, err := agent.CloseSession(ctx, acp.CloseSessionRequest{SessionId: resumeID}); err != nil {
		t.Fatal(err)
	}
	if _, err := agent.LoadSession(ctx, acp.LoadSessionRequest{
		SessionId: "load-command-session", Cwd: workspace, McpServers: []acp.McpServer{},
	}); err != nil {
		t.Fatal(err)
	}
	if commands := waitForAvailableCommandUpdates(t, client, 2); len(commands) != 3 {
		t.Fatalf("load commands = %#v", commands)
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
	unresponsiveMessageID := "wait-unresponsive-message"
	go func() {
		response, promptErr := agent.Prompt(context.Background(), acp.PromptRequest{
			SessionId: created.SessionId, MessageId: &unresponsiveMessageID,
			Prompt: []acp.ContentBlock{acp.TextBlock("wait-no-interrupt")},
		})
		promptResult <- response
		promptError <- promptErr
	}()
	waitForActiveTurn(t, agent, string(created.SessionId), unresponsiveMessageID)

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

// TestClaudeAgentCancelConcurrentCloseIsIdempotent 验证关闭 Session 不会把已完成取消误报为协议错误。
func TestClaudeAgentCancelConcurrentCloseIsIdempotent(t *testing.T) {
	t.Setenv(fakeClaudeProcessEnv, "1")
	executablePath, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	agent, err := NewAgent(ctx, Config{
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), ClaudePath: executablePath,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = agent.Close(context.Background()) })
	agent.idGenerator = func() (string, error) { return "delay-interrupt-session", nil }
	agent.connectionMu.Lock()
	agent.updater = &recordingClaudeClient{}
	agent.connectionMu.Unlock()
	if _, err := agent.Initialize(ctx, acp.InitializeRequest{}); err != nil {
		t.Fatal(err)
	}
	created, err := agent.NewSession(ctx, acp.NewSessionRequest{Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	session, ok := agent.sessions.get(string(created.SessionId))
	if !ok {
		t.Fatal("Claude Session 未安装")
	}

	promptResult := make(chan acp.PromptResponse, 1)
	promptError := make(chan error, 1)
	messageID := "wait-close-race-message"
	go func() {
		response, promptErr := agent.Prompt(ctx, acp.PromptRequest{
			SessionId: created.SessionId, MessageId: &messageID,
			Prompt: []acp.ContentBlock{acp.TextBlock("wait-close-race")},
		})
		promptResult <- response
		promptError <- promptErr
	}()
	waitForActiveTurn(t, agent, string(created.SessionId), messageID)
	cancelError := make(chan error, 1)
	go func() {
		cancelError <- agent.Cancel(ctx, acp.CancelNotification{SessionId: created.SessionId})
	}()
	waitForPendingClaudeControlCall(t, session)
	if _, err := agent.CloseSession(ctx, acp.CloseSessionRequest{SessionId: created.SessionId}); err != nil {
		t.Fatal(err)
	}
	if err := <-cancelError; err != nil {
		t.Fatalf("Cancel() error = %v", err)
	}
	if response := <-promptResult; response.StopReason != acp.StopReasonCancelled {
		t.Fatalf("Prompt() = %#v", response)
	}
	if err := <-promptError; err != nil {
		t.Fatalf("Prompt() error = %v", err)
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
func waitForActiveTurn(t *testing.T, agent *Agent, sessionID string, turnID string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if session, ok := agent.sessions.get(sessionID); ok {
			if active := session.activeTurn(); active != nil && active.id == turnID {
				return
			}
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("等待活动 turn 超时")
}

// waitForPendingClaudeControlCall 等待 cancel interrupt 已写入并开始等待响应。
func waitForPendingClaudeControlCall(t *testing.T, session *claudeSession) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		session.transport.pendingMu.Lock()
		pending := len(session.transport.pending)
		session.transport.pendingMu.Unlock()
		if pending > 0 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("等待 Claude control request 超时")
}

// waitForAvailableCommandUpdates 等待指定数量的完整命令更新并返回最后一份列表。
func waitForAvailableCommandUpdates(
	t *testing.T,
	client *recordingClaudeClient,
	want int,
) []acp.AvailableCommand {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		client.mu.Lock()
		count := 0
		var commands []acp.AvailableCommand
		for _, notification := range client.updates {
			if update := notification.Update.AvailableCommandsUpdate; update != nil {
				count++
				commands = append([]acp.AvailableCommand(nil), update.AvailableCommands...)
			}
		}
		client.mu.Unlock()
		if count >= want {
			return commands
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("等待 %d 条 available_commands_update 超时", want)
	return nil
}

// runFakeClaudeProcess 实现测试所需的最小 stream-json/control CLI。
func runFakeClaudeProcess(args []string, input io.Reader, output io.Writer) int {
	if os.Getenv(fakeClaudeLaunchConfigGateEnv) != "" {
		if os.Getenv(fakeClaudeCustomEnvironment) != "enabled" {
			return 9
		}
		if len(args) < 2 || args[0] != fakeClaudePrefixArgument || args[1] != "team" {
			return 8
		}
		args = args[2:]
	}
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
	delayInterrupt := strings.Contains(sessionID, "delay-interrupt")
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
			if control.Subtype == protocol.ControlInterrupt && delayInterrupt {
				time.Sleep(time.Second)
			}
			response := map[string]any{}
			if control.Subtype == protocol.ControlInitialize {
				response = map[string]any{
					"commands": []map[string]any{
						{"name": "diagnosing-bugs", "description": "Diagnose hard bugs", "argumentHint": "<symptom>"},
						{"name": "doctor", "description": "Terminal diagnostics"},
						{"name": "clear", "description": "Clear terminal state"},
						{"name": "github (MCP)", "description": "Use GitHub MCP"},
					},
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
			if control.Subtype == protocol.ControlInterrupt && !ignoreInterrupt {
				emitFakeResult(encoder, sessionID, "cancel-result")
			}
		case protocol.TypeUser:
			var message protocol.UserInputMessage
			if json.Unmarshal(scanner.Bytes(), &message) != nil {
				continue
			}
			_ = encoder.Encode(map[string]any{
				"type": "system", "subtype": "init", "session_id": sessionID, "uuid": "init-1",
				"claude_code_version": "2.1.232", "cwd": "/tmp", "model": "sonnet",
				"permissionMode": "default", "tools": []string{"Read", "Bash"}, "mcp_servers": []any{},
				"terminal_slash_commands": []string{"doctor"},
			})
			_ = encoder.Encode(map[string]any{
				"type": "user", "message": message.Message, "parent_tool_use_id": nil,
				"uuid": message.UUID, "session_id": sessionID,
			})
			if strings.Contains(string(message.Message.Content), "commands-change") {
				_ = encoder.Encode(map[string]any{
					"type": "system", "subtype": "commands_changed", "session_id": sessionID, "uuid": "commands-2",
					"commands": []map[string]any{
						{"name": "new-skill", "description": "Newly discovered skill"},
						{"name": "doctor", "description": "Terminal diagnostics"},
					},
				})
				emitFakeResult(encoder, sessionID, "commands-result")
				continue
			}
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
			if strings.Contains(string(message.Message.Content), "usage-cumulative") {
				emitFakeUsageTurn(encoder, sessionID, message.UUID)
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
			"id": "assistant-message", "role": "assistant", "model": "sonnet",
			"usage": map[string]any{
				"input_tokens": 10, "output_tokens": 5,
				"cache_read_input_tokens": 2, "cache_creation_input_tokens": 1,
			},
			"content": []map[string]any{
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

// emitFakeUsageTurn 输出 message_delta 累计快照与不同的 result Turn Usage。
func emitFakeUsageTurn(encoder *json.Encoder, sessionID, messageID string) {
	model := "claude-opus-4-6-1m"
	_ = encoder.Encode(map[string]any{
		"type": "stream_event",
		"event": map[string]any{
			"type": "message_start",
			"message": map[string]any{
				"id": "usage-message", "role": "assistant", "model": model, "content": []any{},
				"usage": map[string]any{
					"input_tokens": 29_953, "output_tokens": 1,
					"cache_read_input_tokens": 200, "cache_creation_input_tokens": 30,
				},
			},
		},
		"parent_tool_use_id": nil, "uuid": "usage-start", "session_id": sessionID,
	})
	_ = encoder.Encode(map[string]any{
		"type": "stream_event",
		"event": map[string]any{
			"type":  "message_delta",
			"usage": map[string]any{"output_tokens": 7},
		},
		"parent_tool_use_id": nil, "uuid": "usage-delta", "session_id": sessionID,
	})
	_ = encoder.Encode(map[string]any{
		"type": "result", "subtype": "success", "session_id": sessionID,
		"uuid": messageID + "-result", "is_error": false, "stop_reason": "end_turn",
		"result": "answer",
		"usage": map[string]any{
			"input_tokens": 10, "output_tokens": 5,
			"cache_read_input_tokens": 2, "cache_creation_input_tokens": 1,
		},
		"modelUsage": map[string]any{
			model: map[string]any{
				"inputTokens": 10, "outputTokens": 5,
				"cacheReadInputTokens": 2, "cacheCreationInputTokens": 1,
				"contextWindow": extendedClaudeContextWindow,
			},
		},
	})
	_ = encoder.Encode(map[string]any{
		"type": "system", "subtype": "session_state_changed", "state": "idle",
		"session_id": sessionID, "uuid": messageID + "-idle",
	})
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

package workbuddy

import (
	"context"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JieWaZi/acp-go/pkg/acpserver"
	"github.com/JieWaZi/acp-go/pkg/nativeacp"
	acp "github.com/JieWaZi/acp-go/sdk"
)

// TestInstalledWorkBuddyProtocol 仅在显式开启时探测已安装 CLI；不发送 Prompt、不调用模型或登录。
func TestInstalledWorkBuddyProtocol(t *testing.T) {
	if os.Getenv("WORKBUDDY_REAL_ACP") != "1" {
		t.Skip("set WORKBUDDY_REAL_ACP=1 for installed CLI protocol checks without model calls")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	environment := nativeacp.WithEnvironment(os.Environ(), "CODEBUDDY_CONFIG_DIR", t.TempDir())
	cwd := t.TempDir()
	config := Config{WorkBuddyPath: os.Getenv("WORKBUDDY_PATH"), Environment: environment, WorkingDirectory: cwd, PermissionMode: "auto", Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	agent, err := NewAgent(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close(context.Background())
	// 未声明宿主文件、终端或表单能力，因此不会触发 UI 或工具操作。
	peer, _ := connectFixture(t, agent, "accept")
	response, err := peer.Initialize(ctx, acp.InitializeRequest{ProtocolVersion: 1})
	if err != nil || !response.AgentCapabilities.LoadSession || response.AgentInfo == nil {
		t.Fatalf("installed initialize: %+v %v", response, err)
	}
	session, err := peer.NewSession(ctx, acp.NewSessionRequest{Cwd: cwd, McpServers: []acp.McpServer{}})
	if err != nil || session.SessionId == "" || session.Modes == nil || session.Modes.CurrentModeId != "auto" {
		t.Fatalf("installed new: %+v %v", session, err)
	}
	if err := agent.Close(ctx); err != nil {
		t.Fatal(err)
	}
	restored, err := NewAgent(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close(context.Background())
	restoredPeer, _ := connectFixture(t, restored, "accept")
	if _, err := restoredPeer.Initialize(ctx, acp.InitializeRequest{ProtocolVersion: 1}); err != nil {
		t.Fatal(err)
	}
	loaded, err := restoredPeer.LoadSession(ctx, acp.LoadSessionRequest{SessionId: session.SessionId, Cwd: cwd, McpServers: []acp.McpServer{}})
	if err != nil || loaded.Modes == nil || loaded.Modes.CurrentModeId != "auto" {
		t.Fatalf("installed load: %+v %v", loaded, err)
	}
	t.Logf("installed CLI initialized, created and cold-loaded a session with auto permissions; config options=%d; no model prompt sent", len(loaded.ConfigOptions))
}

// installedQuestionHost 为隔离验收会话自动提交固定测试答案，不操作用户的业务聊天。
type installedQuestionHost struct {
	// Client 对测试未提供的宿主能力保持显式失败。
	acp.Client
	// mutex 保护 SDK 回调与验收断言共享的观察结果。
	mutex sync.Mutex
	// message 保存本轮模型在工具返回后输出的答案。
	message string
	// forms 记录确实经过宿主表单的提问次数。
	forms int
	// approvals 记录被误路由为普通权限审批的次数。
	approvals int
}

// SessionUpdate 消费测试会话通知，不访问运行中的界面。
func (host *installedQuestionHost) SessionUpdate(_ context.Context, request acp.SessionNotification) error {
	host.mutex.Lock()
	defer host.mutex.Unlock()
	if chunk := request.Update.AgentMessageChunk; chunk != nil && chunk.Content.Text != nil {
		host.message += chunk.Content.Text.Text
	}
	return nil
}

// RequestPermission 拒绝测试中不应出现的普通工具授权。
func (host *installedQuestionHost) RequestPermission(context.Context, acp.RequestPermissionRequest) (acp.RequestPermissionResponse, error) {
	host.mutex.Lock()
	defer host.mutex.Unlock()
	host.approvals++
	return acp.RequestPermissionResponse{Outcome: acp.NewRequestPermissionOutcomeCancelled()}, nil
}

// UnstableCreateElicitation 为每个测试表单提供固定自由回答。
func (host *installedQuestionHost) UnstableCreateElicitation(_ context.Context, request acp.UnstableCreateElicitationRequest) (acp.UnstableCreateElicitationResponse, error) {
	host.mutex.Lock()
	defer host.mutex.Unlock()
	host.forms++
	response := acp.NewUnstableCreateElicitationResponseAccept()
	response.Accept.Content = map[string]any{"question_0_custom": "NEUXBOT_QUESTION_OK", "question_0": "NEUXBOT_QUESTION_OK"}
	return response, nil
}

// TestInstalledWorkBuddyQuestion 显式开启后调用实际模型，在隔离临时目录验收连续两轮原生提问与答复。
func TestInstalledWorkBuddyQuestion(t *testing.T) {
	if os.Getenv("WORKBUDDY_REAL_QUESTION") != "1" {
		t.Skip("set WORKBUDDY_REAL_QUESTION=1 to run isolated real-model question acceptance")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	cwd := t.TempDir()
	agent, err := NewAgent(ctx, Config{WorkBuddyPath: os.Getenv("WORKBUDDY_PATH"), WorkingDirectory: cwd, PermissionMode: "full-access", Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close(context.Background())
	serverInput, clientOutput := io.Pipe()
	clientInput, serverOutput := io.Pipe()
	defer serverInput.Close()
	defer clientOutput.Close()
	defer clientInput.Close()
	defer serverOutput.Close()
	server, err := acpserver.NewWithUserInput(agent, serverInput, serverOutput)
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = server.Serve(ctx) }()
	host := &installedQuestionHost{}
	peer := acp.NewClientSideConnection(host, clientOutput, clientInput)
	initializeFixture(t, peer)
	session, err := peer.NewSession(ctx, acp.NewSessionRequest{Cwd: cwd, McpServers: []acp.McpServer{}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := peer.SetSessionConfigOption(ctx, acp.SetSessionConfigOptionRequest{ValueId: &acp.SetSessionConfigOptionValueId{SessionId: session.SessionId, ConfigId: "model", Value: "deepseek-v4.1-flash"}}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		host.mutex.Lock()
		host.message = ""
		host.mutex.Unlock()
		result, err := peer.Prompt(ctx, acp.PromptRequest{SessionId: session.SessionId, Prompt: []acp.ContentBlock{acp.TextBlock("这是原生问答集成测试。本轮必须调用一次内置 AskUserQuestion 工具，询问‘测试选择？’，提供‘选项A’和‘选项B’。等待工具真实返回用户回答后，只回复该答案。不要使用文件、命令、MCP 或其他工具。即使前轮已经问过，本轮也必须重新调用 AskUserQuestion。")}})
		if err != nil || result.StopReason != acp.StopReasonEndTurn {
			t.Fatalf("round %d failed: result=%+v error=%v", i+1, result, err)
		}
		host.mutex.Lock()
		forms, approvals, message := host.forms, host.approvals, host.message
		host.mutex.Unlock()
		if forms != i+1 || approvals != 0 || !strings.Contains(message, "NEUXBOT_QUESTION_OK") {
			t.Fatalf("round %d did not use real answer form: forms=%d approvals=%d", i+1, forms, approvals)
		}
		t.Logf("round %d completed through native question form with no generic approval", i+1)
	}
}

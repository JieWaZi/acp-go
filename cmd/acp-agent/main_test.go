package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	acp "github.com/coder/acp-go-sdk"
)

// recordingACPClient 是 production composition E2E 使用的最小 SDK Client。
type recordingACPClient struct {
	// mu 保护 SDK 通知 goroutine 写入的 updates。
	mu sync.Mutex
	// updates 按 ACP session/update 到达顺序保存。
	updates []acp.SessionNotification
}

// ReadTextFile 实现 SDK Client；本 E2E 不声明文件读取能力。
func (*recordingACPClient) ReadTextFile(
	context.Context,
	acp.ReadTextFileRequest,
) (acp.ReadTextFileResponse, error) {
	return acp.ReadTextFileResponse{}, errors.New("测试客户端不支持读文件")
}

// WriteTextFile 实现 SDK Client；本 E2E 不声明文件写入能力。
func (*recordingACPClient) WriteTextFile(
	context.Context,
	acp.WriteTextFileRequest,
) (acp.WriteTextFileResponse, error) {
	return acp.WriteTextFileResponse{}, errors.New("测试客户端不支持写文件")
}

// RequestPermission 实现 SDK Client；本 E2E 的 fake turn 不发起审批。
func (*recordingACPClient) RequestPermission(
	context.Context,
	acp.RequestPermissionRequest,
) (acp.RequestPermissionResponse, error) {
	return acp.RequestPermissionResponse{}, errors.New("测试 fake turn 不应请求审批")
}

// SessionUpdate 记录 production event router 发送的 ACP 更新。
func (c *recordingACPClient) SessionUpdate(
	_ context.Context,
	notification acp.SessionNotification,
) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.updates = append(c.updates, notification)
	return nil
}

// CreateTerminal 实现 SDK Client；本 E2E 不声明 terminal 能力。
func (*recordingACPClient) CreateTerminal(
	context.Context,
	acp.CreateTerminalRequest,
) (acp.CreateTerminalResponse, error) {
	return acp.CreateTerminalResponse{}, errors.New("测试客户端不支持 terminal")
}

// KillTerminal 实现 SDK Client；本 E2E 不声明 terminal 能力。
func (*recordingACPClient) KillTerminal(
	context.Context,
	acp.KillTerminalRequest,
) (acp.KillTerminalResponse, error) {
	return acp.KillTerminalResponse{}, errors.New("测试客户端不支持 terminal")
}

// TerminalOutput 实现 SDK Client；本 E2E 不声明 terminal 能力。
func (*recordingACPClient) TerminalOutput(
	context.Context,
	acp.TerminalOutputRequest,
) (acp.TerminalOutputResponse, error) {
	return acp.TerminalOutputResponse{}, errors.New("测试客户端不支持 terminal")
}

// ReleaseTerminal 实现 SDK Client；本 E2E 不声明 terminal 能力。
func (*recordingACPClient) ReleaseTerminal(
	context.Context,
	acp.ReleaseTerminalRequest,
) (acp.ReleaseTerminalResponse, error) {
	return acp.ReleaseTerminalResponse{}, errors.New("测试客户端不支持 terminal")
}

// WaitForTerminalExit 实现 SDK Client；本 E2E 不声明 terminal 能力。
func (*recordingACPClient) WaitForTerminalExit(
	context.Context,
	acp.WaitForTerminalExitRequest,
) (acp.WaitForTerminalExitResponse, error) {
	return acp.WaitForTerminalExitResponse{}, errors.New("测试客户端不支持 terminal")
}

// snapshotUpdates 返回一份不与 SDK 通知 goroutine 共享底层数组的快照。
func (c *recordingACPClient) snapshotUpdates() []acp.SessionNotification {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]acp.SessionNotification(nil), c.updates...)
}

// TestRunStartsDefaultAndExplicitCodex 验证默认选择与显式 codex 都进入真实 SDK stdio 服务。
// 若默认值改变、显式选择走不同实现或 composition root 未启动 SDK，本测试应失败。
func TestRunStartsDefaultAndExplicitCodex(t *testing.T) {
	tests := []struct {
		// name 描述 Adapter 参数形式。
		name string
		// args 是传给组合根的完整命令行参数。
		args []string
	}{
		{name: "默认 Codex", args: []string{}},
		{name: "显式 Codex", args: []string{"--adapter", "codex"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("CODEX_PATH", writeFakeCodex(t))
			response := runInitializeExchange(t, tt.args)
			if response.ProtocolVersion != acp.ProtocolVersionNumber {
				t.Fatalf("协议版本为 %d，期望 %d", response.ProtocolVersion, acp.ProtocolVersionNumber)
			}
			if response.AgentInfo == nil || response.AgentInfo.Name != "codex" {
				t.Fatalf("Agent 信息为 %#v，期望 codex", response.AgentInfo)
			}
		})
	}
}

// TestRunProductionCompositionSessionFlow 验证生产组合根经 SDK stdio 驱动真实 fake app-server 子进程。
// 若认证、session 配置或 event router 仅在单元测试被接入，本测试应失败。
func TestRunProductionCompositionSessionFlow(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fake Codex 仅在 Unix 运行；Windows 由交叉构建覆盖")
	}
	t.Setenv("CODEX_PATH", writeFakeCodex(t))

	serverInput, clientOutput := io.Pipe()
	clientInput, serverOutput := io.Pipe()
	t.Cleanup(func() {
		closeTestPipe(t, serverInput)
		closeTestPipe(t, clientOutput)
		closeTestPipe(t, clientInput)
		closeTestPipe(t, serverOutput)
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var diagnostics bytes.Buffer
	exitResult := make(chan int, 1)
	go func() {
		exitResult <- run(ctx, nil, processIO{
			input: serverInput, output: serverOutput, diagnostics: &diagnostics,
		})
	}()

	client := &recordingACPClient{}
	connection := acp.NewClientSideConnection(client, clientOutput, clientInput)
	initialized, err := connection.Initialize(ctx, acp.InitializeRequest{
		ProtocolVersion:    acp.ProtocolVersionNumber,
		ClientCapabilities: acp.ClientCapabilities{},
	})
	if err != nil {
		t.Fatalf("production initialize 失败: %v，诊断: %s", err, diagnostics.String())
	}
	if initialized.AgentCapabilities.Auth.Logout == nil ||
		len(initialized.AuthMethods) != 2 {
		t.Fatalf("认证能力为 %#v，methods=%#v", initialized.AgentCapabilities.Auth, initialized.AuthMethods)
	}

	if _, err = connection.Authenticate(ctx, acp.AuthenticateRequest{
		MethodId: "api-key",
		Meta: map[string]any{
			"api-key": map[string]any{"apiKey": "TEST_ONLY_E2E_TOKEN"},
		},
	}); err != nil {
		t.Fatalf("production Authenticate 失败: %v", err)
	}
	created, err := connection.NewSession(ctx, acp.NewSessionRequest{
		Cwd: t.TempDir(), McpServers: []acp.McpServer{},
	})
	if err != nil {
		t.Fatalf("production NewSession 失败: %v", err)
	}
	if created.SessionId != "e2e-thread" || created.Modes == nil || len(created.ConfigOptions) != 3 {
		t.Fatalf("新 session 响应为 %#v", created)
	}
	if _, err = connection.SetSessionMode(ctx, acp.SetSessionModeRequest{
		SessionId: created.SessionId, ModeId: "agent-full-access",
	}); err != nil {
		t.Fatalf("production SetSessionMode 失败: %v", err)
	}
	if _, err = connection.SetSessionConfigOption(ctx, acp.SetSessionConfigOptionRequest{
		ValueId: &acp.SetSessionConfigOptionValueId{
			SessionId: created.SessionId, ConfigId: "model", Value: "slow-model",
		},
	}); err != nil {
		t.Fatalf("production SetSessionConfigOption 失败: %v", err)
	}
	prompt, err := connection.Prompt(ctx, acp.PromptRequest{
		SessionId: created.SessionId,
		Prompt:    []acp.ContentBlock{{Text: &acp.ContentBlockText{Type: "text", Text: "hello e2e"}}},
	})
	if err != nil || prompt.StopReason != acp.StopReasonEndTurn {
		t.Fatalf("production Prompt 响应为 %#v, %v", prompt, err)
	}
	updates := client.snapshotUpdates()
	if len(updates) != 1 || updates[0].Update.AgentMessageChunk == nil ||
		updates[0].Update.AgentMessageChunk.Content.Text == nil ||
		updates[0].Update.AgentMessageChunk.Content.Text.Text != "fake answer" {
		t.Fatalf("production session updates 为 %#v", updates)
	}
	if _, err = connection.Logout(ctx, acp.LogoutRequest{}); err != nil {
		t.Fatalf("production Logout 失败: %v", err)
	}
	if _, err = connection.CloseSession(ctx, acp.CloseSessionRequest{SessionId: created.SessionId}); err != nil {
		t.Fatalf("production CloseSession 失败: %v", err)
	}

	if err = clientOutput.Close(); err != nil {
		t.Fatalf("关闭 SDK 客户端输出失败: %v", err)
	}
	select {
	case exitCode := <-exitResult:
		if exitCode != 0 {
			t.Fatalf("production composition 退出码为 %d，诊断: %s", exitCode, diagnostics.String())
		}
	case <-ctx.Done():
		t.Fatalf("等待 production composition 退出失败: %v，诊断: %s", ctx.Err(), diagnostics.String())
	}
}

// TestRunRejectsInvalidCodexPathBeforeProtocolOutput 验证显式 CODEX_PATH 启动失败不会回退或污染 stdout。
func TestRunRejectsInvalidCodexPathBeforeProtocolOutput(t *testing.T) {
	t.Setenv("CODEX_PATH", filepath.Join(t.TempDir(), "missing-codex"))
	var protocolOutput bytes.Buffer
	var diagnostics bytes.Buffer
	exitCode := run(context.Background(), nil, processIO{
		input: bytes.NewReader(nil), output: &protocolOutput, diagnostics: &diagnostics,
	})
	if exitCode == 0 {
		t.Fatal("无效 CODEX_PATH 返回成功")
	}
	if protocolOutput.Len() != 0 {
		t.Fatalf("启动失败污染 stdout: %q", protocolOutput.String())
	}
	if !strings.Contains(diagnostics.String(), "missing-codex") {
		t.Fatalf("启动诊断为 %q", diagnostics.String())
	}
}

// TestRunRejectsUnknownAdapterBeforeProtocolOutput 验证未知 Adapter 只产生 stderr 诊断。
// 若选择器静默回退或 SDK 已向 stdout 写出协议帧，本测试应失败。
func TestRunRejectsUnknownAdapterBeforeProtocolOutput(t *testing.T) {
	t.Parallel()

	var protocolOutput bytes.Buffer
	var diagnostics bytes.Buffer
	exitCode := run(
		context.Background(),
		[]string{"--adapter", "unknown"},
		processIO{
			input:       bytes.NewReader(nil),
			output:      &protocolOutput,
			diagnostics: &diagnostics,
		},
	)

	if exitCode == 0 {
		t.Fatal("未知 Adapter 返回了成功退出码")
	}
	if protocolOutput.Len() != 0 {
		t.Fatalf("未知 Adapter 向 stdout 写入了 %q", protocolOutput.String())
	}
	if !strings.Contains(diagnostics.String(), `adapter "unknown"`) ||
		!strings.Contains(diagnostics.String(), "adapter not found") {
		t.Fatalf("诊断信息 %q 缺少 Adapter 名称或根因", diagnostics.String())
	}
}

// TestRunRejectsUnexpectedArguments 验证多余位置参数在协议启动前被拒绝。
// 若拼写错误的启动参数被静默忽略，本测试应失败。
func TestRunRejectsUnexpectedArguments(t *testing.T) {
	t.Parallel()

	var protocolOutput bytes.Buffer
	var diagnostics bytes.Buffer
	exitCode := run(
		context.Background(),
		[]string{"unexpected"},
		processIO{
			input:       bytes.NewReader(nil),
			output:      &protocolOutput,
			diagnostics: &diagnostics,
		},
	)

	if exitCode == 0 {
		t.Fatal("多余参数返回了成功退出码")
	}
	if protocolOutput.Len() != 0 {
		t.Fatalf("多余参数向 stdout 写入了 %q", protocolOutput.String())
	}
	if !strings.Contains(diagnostics.String(), "unexpected arguments") {
		t.Fatalf("诊断信息 %q 缺少多余参数根因", diagnostics.String())
	}
}

// runInitializeExchange 启动命令、发送 initialize，并在收到响应后关闭客户端输入。
func runInitializeExchange(t *testing.T, args []string) acp.InitializeResponse {
	t.Helper()

	serverInput, clientOutput := io.Pipe()
	clientInput, serverOutput := io.Pipe()
	t.Cleanup(func() {
		closeTestPipe(t, serverInput)
		closeTestPipe(t, clientOutput)
		closeTestPipe(t, clientInput)
		closeTestPipe(t, serverOutput)
	})

	var diagnostics bytes.Buffer
	exitResult := make(chan int, 1)
	go func() {
		exitResult <- run(
			context.Background(),
			args,
			processIO{
				input:       serverInput,
				output:      serverOutput,
				diagnostics: &diagnostics,
			},
		)
	}()

	request := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":1,"clientCapabilities":{}}}` + "\n"
	if _, err := io.WriteString(clientOutput, request); err != nil {
		t.Fatalf("写入 initialize 请求失败: %v", err)
	}

	responseLine, err := bufio.NewReader(clientInput).ReadBytes('\n')
	if err != nil {
		t.Fatalf("读取 initialize 响应失败: %v", err)
	}
	var envelope map[string]json.RawMessage
	if err = json.Unmarshal(responseLine, &envelope); err != nil {
		t.Fatalf("响应不是合法 JSON: %v", err)
	}
	if len(envelope["error"]) != 0 {
		t.Fatalf("initialize 返回协议错误: %s", envelope["error"])
	}

	var response acp.InitializeResponse
	if err = json.Unmarshal(envelope["result"], &response); err != nil {
		t.Fatalf("解析 initialize result 失败: %v", err)
	}

	if err = clientOutput.Close(); err != nil {
		t.Fatalf("关闭客户端输出失败: %v", err)
	}
	select {
	case exitCode := <-exitResult:
		if exitCode != 0 {
			t.Fatalf("命令退出码为 %d，诊断: %s", exitCode, diagnostics.String())
		}
	case <-time.After(time.Second):
		t.Fatal("等待命令退出超时")
	}

	return response
}

// closeTestPipe 关闭测试管道，并把真实清理失败记录到当前测试。
func closeTestPipe(t *testing.T, closer io.Closer) {
	t.Helper()
	if err := closer.Close(); err != nil && !errors.Is(err, io.ErrClosedPipe) {
		t.Errorf("关闭测试管道失败: %v", err)
	}
}

// writeFakeCodex 创建支持 V1 关键链路的可控本地 fake app-server。
func writeFakeCodex(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell fake Codex 仅用于 Unix composition 测试；Windows 由交叉构建覆盖")
	}
	path := filepath.Join(t.TempDir(), "codex")
	script := `#!/bin/sh
if [ "$1" = "--version" ]; then
  echo "codex-cli 0.148.0"
  exit 0
fi
if [ "$1" != "app-server" ]; then
  exit 2
fi
while IFS= read -r line; do
  id=$(printf '%s' "$line" | sed -n 's/.*"id":\([0-9][0-9]*\).*/\1/p')
  case "$line" in
    *'"method":"initialize"'*)
      printf '{"id":%s,"result":{"codexHome":"/tmp/codex-home","platformFamily":"unix","platformOs":"test","userAgent":"fake"}}\n' "$id"
      ;;
    *'"method":"account/login/start"'*)
      printf '{"id":%s,"result":{"type":"apiKey"}}\n' "$id"
      printf '%s\n' '{"method":"account/login/completed","params":{"success":true}}'
      ;;
    *'"method":"thread/start"'*)
      printf '{"id":%s,"result":{"approvalPolicy":"on-request","approvalsReviewer":"user","cwd":"/workspace","model":"fast-model","modelProvider":"openai","reasoningEffort":"medium","sandbox":{"type":"workspaceWrite"},"thread":{"cliVersion":"0.148.0","createdAt":1,"cwd":"/workspace","ephemeral":false,"id":"e2e-thread","modelProvider":"openai","preview":"","sessionId":"e2e-session","source":{},"status":{"type":"idle"},"turns":[],"updatedAt":1}}}\n' "$id"
      ;;
    *'"method":"model/list"'*)
      printf '{"id":%s,"result":{"data":[{"defaultReasoningEffort":"medium","description":"Fast","displayName":"Fast model","hidden":false,"id":"fast-model","isDefault":true,"model":"fast-model","supportedReasoningEfforts":[{"reasoningEffort":"medium","description":"Balanced"}]},{"defaultReasoningEffort":"low","description":"Slow","displayName":"Slow model","hidden":false,"id":"slow-model","isDefault":false,"model":"slow-model","supportedReasoningEfforts":[{"reasoningEffort":"low","description":"Fast"},{"reasoningEffort":"medium","description":"Balanced"}]}]}}\n' "$id"
      ;;
    *'"method":"turn/start"'*)
      case "$line" in
        *'"model":"slow-model"'*'"approvalPolicy":"never"'*|*'"approvalPolicy":"never"'*'"model":"slow-model"'*)
          printf '{"id":%s,"result":{"turn":{"id":"e2e-turn","items":[],"status":"inProgress"}}}\n' "$id"
          printf '%s\n' '{"method":"item/agentMessage/delta","params":{"delta":"fake answer","itemId":"e2e-message","threadId":"e2e-thread","turnId":"e2e-turn"}}'
          printf '%s\n' '{"method":"turn/completed","params":{"threadId":"e2e-thread","turn":{"id":"e2e-turn","items":[],"status":"completed"}}}'
          ;;
        *)
          printf '{"id":%s,"error":{"code":-32602,"message":"turn/start missing selected configuration"}}\n' "$id"
          ;;
      esac
      ;;
    *'"method":"account/logout"'*)
      printf '{"id":%s,"result":{}}\n' "$id"
      printf '%s\n' '{"method":"account/updated","params":{"account":null}}'
      ;;
    *'"method":"thread/unsubscribe"'*)
      printf '{"id":%s,"result":{"status":"unsubscribed"}}\n' "$id"
      ;;
  esac
done
`
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatalf("写 fake Codex 失败: %v", err)
	}
	return path
}

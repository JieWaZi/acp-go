package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	codexprotocol "github.com/JieWaZi/acp-go/pkg/codex/protocol"
	acp "github.com/coder/acp-go-sdk"
)

// fakeCodexProcessEnv 标记测试二进制子进程应进入 fake Codex 模式。
const fakeCodexProcessEnv = "ACP_GO_TEST_FAKE_CODEX_PROCESS"

// TestMain 在子进程标记存在时直接运行 fake Codex，避免 shell/sed 进程竞争污染并行全量测试。
func TestMain(m *testing.M) {
	switch {
	case os.Getenv(fakeCodexProcessEnv) != "":
		os.Exit(runFakeCodexProcess(os.Args[1:], os.Stdin, os.Stdout))
	}
	os.Exit(m.Run())
}

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
	return acp.ReadTextFileResponse{}, errors.New("test client does not support reading files")
}

// WriteTextFile 实现 SDK Client；本 E2E 不声明文件写入能力。
func (*recordingACPClient) WriteTextFile(
	context.Context,
	acp.WriteTextFileRequest,
) (acp.WriteTextFileResponse, error) {
	return acp.WriteTextFileResponse{}, errors.New("test client does not support writing files")
}

// RequestPermission 实现 SDK Client；本 E2E 的 fake turn 不发起审批。
func (*recordingACPClient) RequestPermission(
	context.Context,
	acp.RequestPermissionRequest,
) (acp.RequestPermissionResponse, error) {
	return acp.RequestPermissionResponse{}, errors.New("test fake turn must not request permission")
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
	return acp.CreateTerminalResponse{}, errors.New("test client does not support terminal operations")
}

// KillTerminal 实现 SDK Client；本 E2E 不声明 terminal 能力。
func (*recordingACPClient) KillTerminal(
	context.Context,
	acp.KillTerminalRequest,
) (acp.KillTerminalResponse, error) {
	return acp.KillTerminalResponse{}, errors.New("test client does not support terminal operations")
}

// TerminalOutput 实现 SDK Client；本 E2E 不声明 terminal 能力。
func (*recordingACPClient) TerminalOutput(
	context.Context,
	acp.TerminalOutputRequest,
) (acp.TerminalOutputResponse, error) {
	return acp.TerminalOutputResponse{}, errors.New("test client does not support terminal operations")
}

// ReleaseTerminal 实现 SDK Client；本 E2E 不声明 terminal 能力。
func (*recordingACPClient) ReleaseTerminal(
	context.Context,
	acp.ReleaseTerminalRequest,
) (acp.ReleaseTerminalResponse, error) {
	return acp.ReleaseTerminalResponse{}, errors.New("test client does not support terminal operations")
}

// WaitForTerminalExit 实现 SDK Client；本 E2E 不声明 terminal 能力。
func (*recordingACPClient) WaitForTerminalExit(
	context.Context,
	acp.WaitForTerminalExitRequest,
) (acp.WaitForTerminalExitResponse, error) {
	return acp.WaitForTerminalExitResponse{}, errors.New("test client does not support terminal operations")
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
			// 未选择 Claude 时不应探测或启动该 Adapter。
			t.Setenv("CLAUDE_CODE_EXECUTABLE", filepath.Join(t.TempDir(), "missing-claude"))
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

// TestRunRejectsInvalidClaudePathBeforeProtocolOutput 验证显式选择 Claude 时路径错误只写诊断流。
func TestRunRejectsInvalidClaudePathBeforeProtocolOutput(t *testing.T) {
	t.Setenv("CLAUDE_CODE_EXECUTABLE", filepath.Join(t.TempDir(), "missing-claude"))
	var protocolOutput bytes.Buffer
	var diagnostics bytes.Buffer
	exitCode := run(context.Background(), []string{"--adapter", "claude"}, processIO{
		input: bytes.NewReader(nil), output: &protocolOutput, diagnostics: &diagnostics,
	})
	if exitCode == 0 {
		t.Fatal("无效 CLAUDE_CODE_EXECUTABLE 返回成功")
	}
	if protocolOutput.Len() != 0 {
		t.Fatalf("启动失败污染 stdout: %q", protocolOutput.String())
	}
	if !strings.Contains(diagnostics.String(), "missing-claude") {
		t.Fatalf("启动诊断为 %q", diagnostics.String())
	}
}

// TestRunProductionCompositionSessionFlow 验证生产组合根经 SDK stdio 驱动真实 fake app-server 子进程。
// 若认证、session 配置或 event router 仅在单元测试被接入，本测试应失败。
func TestRunProductionCompositionSessionFlow(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("production process fixture 仅在 Unix 运行；Windows 由交叉构建覆盖")
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
	exitResult := runTestAgentWithOwnedPipes(
		ctx,
		nil,
		processIO{input: serverInput, output: serverOutput, diagnostics: &diagnostics},
		serverInput,
		serverOutput,
	)

	client := &recordingACPClient{}
	connection := acp.NewClientSideConnection(client, clientOutput, clientInput)
	initialized, err := connection.Initialize(ctx, acp.InitializeRequest{
		ProtocolVersion:    acp.ProtocolVersionNumber,
		ClientCapabilities: acp.ClientCapabilities{},
	})
	if err != nil {
		t.Fatalf("production initialize 失败: %v，诊断: %s", err, diagnostics.String())
	}
	wantAuthMethods := 2
	if os.Getenv("NO_BROWSER") != "" {
		wantAuthMethods = 1
	}
	if initialized.AgentCapabilities.Auth.Logout == nil ||
		len(initialized.AuthMethods) != wantAuthMethods {
		t.Fatalf("认证能力为 %#v，methods=%#v", initialized.AgentCapabilities.Auth, initialized.AuthMethods)
	}
	steering, ok := initialized.Meta["steering"].(map[string]any)
	if !ok || len(initialized.Meta) != 2 || steering["supported"] != true {
		t.Fatalf("initialize meta 为 %#v，期望 steering.supported=true 及版本限定的 fork 能力", initialized.Meta)
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
	assertProductionV1Updates(t, updates)
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

// assertProductionV1Updates 验证完整 stdio 组合链路保留 V1 核心消息、思考、计划、用量和工具生命周期。
func assertProductionV1Updates(t *testing.T, updates []acp.SessionNotification) {
	t.Helper()
	if len(updates) != 8 {
		t.Fatalf("production session update 数为 %d，期望 8：%#v", len(updates), updates)
	}

	wantMessageDeltas := []string{"fake ", "answer"}
	for index, want := range wantMessageDeltas {
		message := updates[index].Update.AgentMessageChunk
		if message == nil || message.Content.Text == nil || message.Content.Text.Text != want {
			t.Fatalf("agent message delta[%d] 为 %#v，期望 %q", index, message, want)
		}
	}
	thought := updates[2].Update.AgentThoughtChunk
	if thought == nil || thought.Content.Text == nil || thought.Content.Text.Text != "fake thought" {
		t.Fatalf("agent thought 为 %#v", thought)
	}
	plan := updates[3].Update.Plan
	if plan == nil || len(plan.Entries) != 2 || plan.Entries[0].Status != acp.PlanEntryStatusCompleted ||
		plan.Entries[1].Status != acp.PlanEntryStatusInProgress {
		t.Fatalf("plan update 为 %#v", plan)
	}
	usage := updates[4].Update.UsageUpdate
	if usage == nil || usage.Used != 25 || usage.Size != 128000 {
		t.Fatalf("usage update 为 %#v", usage)
	}
	toolStart := updates[5].Update.ToolCall
	if toolStart == nil || toolStart.ToolCallId != "e2e-command" || toolStart.Kind != acp.ToolKindExecute ||
		toolStart.Status != acp.ToolCallStatusInProgress {
		t.Fatalf("tool call start 为 %#v", toolStart)
	}
	toolDelta := updates[6].Update.ToolCallUpdate
	if toolDelta == nil || toolDelta.ToolCallId != toolStart.ToolCallId ||
		toolDelta.Meta["terminal_output_delta"] == nil {
		t.Fatalf("tool call delta 为 %#v", toolDelta)
	}
	toolCompleted := updates[7].Update.ToolCallUpdate
	if toolCompleted == nil || toolCompleted.ToolCallId != toolStart.ToolCallId ||
		toolCompleted.Status == nil || *toolCompleted.Status != acp.ToolCallStatusCompleted {
		t.Fatalf("tool call completion 为 %#v", toolCompleted)
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

// TestProductionFixtureUnblocksInitializeWhenServerExitsEarly 验证测试 pipe owner 传播启动失败。
// 若 server 提前退出后没有关闭 reader，SDK 的 io.Pipe.Write 会忽略请求 context 并卡住本测试。
func TestProductionFixtureUnblocksInitializeWhenServerExitsEarly(t *testing.T) {
	t.Setenv("CODEX_PATH", filepath.Join(t.TempDir(), "missing-codex"))

	serverInput, clientOutput := io.Pipe()
	clientInput, serverOutput := io.Pipe()
	t.Cleanup(func() {
		closeTestPipe(t, serverInput)
		closeTestPipe(t, clientOutput)
		closeTestPipe(t, clientInput)
		closeTestPipe(t, serverOutput)
	})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var diagnostics bytes.Buffer
	exitResult := runTestAgentWithOwnedPipes(
		ctx,
		nil,
		processIO{input: serverInput, output: serverOutput, diagnostics: &diagnostics},
		serverInput,
		serverOutput,
	)

	connection := acp.NewClientSideConnection(&recordingACPClient{}, clientOutput, clientInput)
	initializeResult := make(chan error, 1)
	go func() {
		_, err := connection.Initialize(ctx, acp.InitializeRequest{
			ProtocolVersion: acp.ProtocolVersionNumber,
		})
		initializeResult <- err
	}()
	select {
	case err := <-initializeResult:
		if err == nil {
			t.Fatal("server 提前退出后 initialize 返回成功")
		}
	case <-ctx.Done():
		t.Fatalf("server 提前退出未解除 initialize write: %v", ctx.Err())
	}
	select {
	case exitCode := <-exitResult:
		if exitCode == 0 {
			t.Fatalf("缺失 Codex 返回成功，诊断: %s", diagnostics.String())
		}
	case <-ctx.Done():
		t.Fatalf("等待缺失 Codex 退出失败: %v，诊断: %s", ctx.Err(), diagnostics.String())
	}
}

// runInitializeExchange 启动命令、发送 initialize，并在收到响应后关闭客户端输入。
func runInitializeExchange(t *testing.T, args []string) acp.InitializeResponse {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	serverInput, clientOutput := io.Pipe()
	clientInput, serverOutput := io.Pipe()
	t.Cleanup(func() {
		closeTestPipe(t, serverInput)
		closeTestPipe(t, clientOutput)
		closeTestPipe(t, clientInput)
		closeTestPipe(t, serverOutput)
	})

	var diagnostics bytes.Buffer
	exitResult := runTestAgentWithOwnedPipes(
		ctx,
		args,
		processIO{
			input:       serverInput,
			output:      serverOutput,
			diagnostics: &diagnostics,
		},
		serverInput,
		serverOutput,
	)

	request := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":1,"clientCapabilities":{}}}` + "\n"
	writeResult := make(chan error, 1)
	go func() {
		_, writeErr := io.WriteString(clientOutput, request)
		writeResult <- writeErr
	}()
	select {
	case err := <-writeResult:
		if err != nil {
			t.Fatalf("写入 initialize 请求失败: %v，诊断: %s", err, diagnostics.String())
		}
	case exitCode := <-exitResult:
		t.Fatalf("initialize 写入前命令已退出，退出码为 %d，诊断: %s", exitCode, diagnostics.String())
	case <-ctx.Done():
		t.Fatalf("等待 initialize 写入失败: %v，诊断: %s", ctx.Err(), diagnostics.String())
	}

	type readResult struct {
		// line 是 fake Agent 返回的一条完整 ACP 帧。
		line []byte
		// err 是 pipe 读取失败。
		err error
	}
	readResults := make(chan readResult, 1)
	go func() {
		line, readErr := bufio.NewReader(clientInput).ReadBytes('\n')
		readResults <- readResult{line: line, err: readErr}
	}()
	var responseLine []byte
	select {
	case result := <-readResults:
		if result.err != nil {
			t.Fatalf("读取 initialize 响应失败: %v，诊断: %s", result.err, diagnostics.String())
		}
		responseLine = result.line
	case exitCode := <-exitResult:
		t.Fatalf("initialize 响应前命令已退出，退出码为 %d，诊断: %s", exitCode, diagnostics.String())
	case <-ctx.Done():
		t.Fatalf("等待 initialize 响应失败: %v，诊断: %s", ctx.Err(), diagnostics.String())
	}

	var err error
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
	case <-ctx.Done():
		// 复用覆盖整个 exchange 的有界 deadline；race 插桩下正常进程回收可超过一秒。
		t.Fatalf("等待命令退出失败: %v，诊断: %s", ctx.Err(), diagnostics.String())
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

// runTestAgentWithOwnedPipes 启动组合根，并在退出时关闭测试拥有的 pipe 端。
// 真实进程不关闭 os.Stdin/stdout；该职责只属于使用 io.Pipe 的测试夹具。
func runTestAgentWithOwnedPipes(
	ctx context.Context,
	args []string,
	streams processIO,
	owned ...io.Closer,
) <-chan int {
	exitResult := make(chan int, 1)
	go func() {
		exitCode := run(ctx, args, streams)
		for _, closer := range owned {
			_ = closer.Close()
		}
		exitResult <- exitCode
	}()
	return exitResult
}

// writeFakeCodex 返回当前测试二进制，并用环境标记让其子进程进入 fake Codex 模式。
func writeFakeCodex(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("production process fixture 仅用于 Unix composition 测试；Windows 由交叉构建覆盖")
	}
	t.Setenv(fakeCodexProcessEnv, "1")
	path, err := os.Executable()
	if err != nil {
		t.Fatalf("定位测试二进制失败: %v", err)
	}
	return path
}

// runFakeCodexProcess 在测试子进程中实现固定 NDJSON fixture，不派生额外 shell 工具进程。
func runFakeCodexProcess(args []string, input io.Reader, output io.Writer) int {
	if len(args) == 1 && args[0] == "--version" {
		if _, err := fmt.Fprintln(output, "codex-cli 0.148.0"); err != nil {
			return 3
		}
		return 0
	}
	if len(args) != 1 || args[0] != "app-server" {
		return 2
	}

	// fake 按 NDJSON 帧解码并复用生成 method 常量，避免 shell 文本匹配漂移。
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 4096), 1024*1024)
	encoder := json.NewEncoder(output)
	for scanner.Scan() {
		var request map[string]json.RawMessage
		if err := json.Unmarshal(scanner.Bytes(), &request); err != nil {
			return 4
		}
		var method string
		if err := json.Unmarshal(request["method"], &method); err != nil {
			return 4
		}
		if err := handleFakeCodexRequest(encoder, request["id"], method, request["params"]); err != nil {
			return 5
		}
	}
	if err := scanner.Err(); err != nil {
		return 6
	}
	return 0
}

// handleFakeCodexRequest 对 V1 production composition 使用的方法返回固定协议形状。
func handleFakeCodexRequest(
	encoder *json.Encoder,
	id json.RawMessage,
	method string,
	params json.RawMessage,
) error {
	// 只实现生产组合触达的方法；未知 request 返回显式协议错误，不能静默放行。
	switch method {
	case codexprotocol.MethodInitialized:
		return nil
	case codexprotocol.MethodInitialize:
		return writeFakeResult(encoder, id, `{"codexHome":"/tmp/codex-home","platformFamily":"unix","platformOs":"test","userAgent":"fake"}`)
	case codexprotocol.MethodAccountLoginStart:
		if err := writeFakeResult(encoder, id, `{"type":"apiKey"}`); err != nil {
			return err
		}
		return writeFakeNotification(encoder, codexprotocol.MethodAccountLoginCompleted, `{"success":true}`)
	case codexprotocol.MethodAccountRead:
		return writeFakeResult(encoder, id, `{"account":{"type":"apiKey"},"requiresOpenaiAuth":true}`)
	case codexprotocol.MethodThreadStart:
		return writeFakeResult(encoder, id, `{"approvalPolicy":"on-request","approvalsReviewer":"user","cwd":"/workspace","model":"fast-model","modelProvider":"openai","reasoningEffort":"medium","sandbox":{"type":"workspaceWrite"},"thread":{"cliVersion":"0.148.0","createdAt":1,"cwd":"/workspace","ephemeral":false,"id":"e2e-thread","modelProvider":"openai","preview":"","sessionId":"e2e-session","source":{},"status":{"type":"idle"},"turns":[],"updatedAt":1}}`)
	case codexprotocol.MethodModelList:
		return writeFakeResult(encoder, id, `{"data":[{"defaultReasoningEffort":"medium","description":"Fast","displayName":"Fast model","hidden":false,"id":"fast-model","isDefault":true,"model":"fast-model","supportedReasoningEfforts":[{"reasoningEffort":"medium","description":"Balanced"}]},{"defaultReasoningEffort":"low","description":"Slow","displayName":"Slow model","hidden":false,"id":"slow-model","isDefault":false,"model":"slow-model","supportedReasoningEfforts":[{"reasoningEffort":"low","description":"Fast"},{"reasoningEffort":"medium","description":"Balanced"}]}]}`)
	case codexprotocol.MethodTurnStart:
		var turnParams map[string]json.RawMessage
		if err := json.Unmarshal(params, &turnParams); err != nil {
			return err
		}
		var model string
		var approvalPolicy string
		if err := json.Unmarshal(turnParams["model"], &model); err != nil {
			return err
		}
		if err := json.Unmarshal(turnParams["approvalPolicy"], &approvalPolicy); err != nil {
			return err
		}
		if model != "slow-model" || approvalPolicy != "never" {
			return writeFakeError(encoder, id, -32602, "turn/start missing selected configuration")
		}
		if err := writeFakeResult(encoder, id, `{"turn":{"id":"e2e-turn","items":[],"status":"inProgress"}}`); err != nil {
			return err
		}
		fixtures := []struct {
			// method 是 fake app-server 发出的稳定通知方法。
			method string
			// params 是与生成协议类型一致的原始 JSON 参数。
			params string
		}{
			{codexprotocol.MethodAgentMessageDelta, `{"delta":"fake ","itemId":"e2e-message","threadId":"e2e-thread","turnId":"e2e-turn"}`},
			{codexprotocol.MethodAgentMessageDelta, `{"delta":"answer","itemId":"e2e-message","threadId":"e2e-thread","turnId":"e2e-turn"}`},
			{codexprotocol.MethodReasoningSummaryTextDelta, `{"delta":"fake thought","itemId":"e2e-reasoning","summaryIndex":0,"threadId":"e2e-thread","turnId":"e2e-turn"}`},
			{codexprotocol.MethodTurnPlanUpdated, `{"explanation":"fake plan","plan":[{"status":"completed","step":"map"},{"status":"inProgress","step":"verify"}],"threadId":"e2e-thread","turnId":"e2e-turn"}`},
			{codexprotocol.MethodThreadTokenUsageUpdated, `{"threadId":"e2e-thread","turnId":"e2e-turn","tokenUsage":{"last":{"cachedInputTokens":0,"inputTokens":10,"outputTokens":10,"reasoningOutputTokens":5,"totalTokens":25},"modelContextWindow":128000,"total":{"cachedInputTokens":0,"inputTokens":10,"outputTokens":10,"reasoningOutputTokens":5,"totalTokens":25}}}`},
			{codexprotocol.MethodItemStarted, `{"item":{"command":"printf fake-tool","commandActions":[],"cwd":"/workspace","id":"e2e-command","status":"inProgress","type":"commandExecution"},"startedAtMs":1,"threadId":"e2e-thread","turnId":"e2e-turn"}`},
			{codexprotocol.MethodCommandExecutionOutputDelta, `{"delta":"fake-tool","itemId":"e2e-command","threadId":"e2e-thread","turnId":"e2e-turn"}`},
			{codexprotocol.MethodItemCompleted, `{"completedAtMs":2,"item":{"aggregatedOutput":"fake-tool","command":"printf fake-tool","commandActions":[],"cwd":"/workspace","exitCode":0,"id":"e2e-command","status":"completed","type":"commandExecution"},"threadId":"e2e-thread","turnId":"e2e-turn"}`},
		}
		for _, fixture := range fixtures {
			if err := writeFakeNotification(encoder, fixture.method, fixture.params); err != nil {
				return err
			}
		}
		return writeFakeNotification(encoder, codexprotocol.MethodTurnCompleted, `{"threadId":"e2e-thread","turn":{"id":"e2e-turn","items":[],"status":"completed"}}`)
	case codexprotocol.MethodAccountLogout:
		if err := writeFakeResult(encoder, id, `{}`); err != nil {
			return err
		}
		return writeFakeNotification(encoder, codexprotocol.MethodAccountUpdated, `{"account":null}`)
	case codexprotocol.MethodThreadUnsubscribe:
		return writeFakeResult(encoder, id, `{"status":"unsubscribed"}`)
	default:
		if len(id) == 0 {
			return nil
		}
		return writeFakeError(encoder, id, -32601, "unexpected fake app-server method")
	}
}

// writeFakeResult 写入保留请求 ID 的 JSON-RPC result envelope。
func writeFakeResult(encoder *json.Encoder, id json.RawMessage, result string) error {
	return encoder.Encode(map[string]any{
		"id":     id,
		"result": json.RawMessage(result),
	})
}

// writeFakeNotification 写入 app-server 无 jsonrpc 字段的 NDJSON notification。
func writeFakeNotification(encoder *json.Encoder, method string, params string) error {
	return encoder.Encode(map[string]any{
		"method": method,
		"params": json.RawMessage(params),
	})
}

// writeFakeError 写入测试夹具的稳定 JSON-RPC error envelope。
func writeFakeError(encoder *json.Encoder, id json.RawMessage, code int, message string) error {
	return encoder.Encode(map[string]any{
		"id": id,
		"error": map[string]any{
			"code": code, "message": message,
		},
	})
}

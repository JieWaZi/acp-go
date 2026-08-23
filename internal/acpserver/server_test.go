package acpserver

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"
	"time"

	acp "github.com/coder/acp-go-sdk"
)

// testAgent 是协议边界测试使用的最小 SDK Agent，并记录 connection 注入结果。
type testAgent struct {
	// connectionReady 在 Server 注入 SDK connection 后交付该实例，避免测试依赖调度时序。
	connectionReady chan *acp.AgentSideConnection
	// closed 记录 Adapter 关闭时观察到的上下文错误。
	closed chan error
	// closeErr 是 Adapter 关闭时向协议服务返回的可控错误。
	closeErr error
}

var _ acp.Agent = (*testAgent)(nil)

// newTestAgent 创建一个带有单元素通知通道的测试 Agent。
func newTestAgent() *testAgent {
	return &testAgent{
		connectionReady: make(chan *acp.AgentSideConnection, 1),
		closed:          make(chan error, 1),
	}
}

// SetAgentConnection 接收 Server 创建的 SDK connection。
func (a *testAgent) SetAgentConnection(connection *acp.AgentSideConnection) {
	a.connectionReady <- connection
}

// Close 记录协议服务释放 Adapter 资源时使用的上下文状态。
func (a *testAgent) Close(ctx context.Context) error {
	a.closed <- ctx.Err()
	return a.closeErr
}

// Authenticate 为测试 Agent 实现 acp.Agent；框架测试不声明认证能力。
func (a *testAgent) Authenticate(context.Context, acp.AuthenticateRequest) (acp.AuthenticateResponse, error) {
	return acp.AuthenticateResponse{}, acp.NewMethodNotFound(acp.AgentMethodAuthenticate)
}

// Initialize 返回可由测试独立校验的最小稳定 ACP 握手结果。
func (a *testAgent) Initialize(context.Context, acp.InitializeRequest) (acp.InitializeResponse, error) {
	return acp.InitializeResponse{
		ProtocolVersion: acp.ProtocolVersionNumber,
		AgentInfo: &acp.Implementation{
			Name:    "test-agent",
			Version: "test",
		},
	}, nil
}

// Logout 为测试 Agent 实现 acp.Agent；框架测试不声明退出认证能力。
func (a *testAgent) Logout(context.Context, acp.LogoutRequest) (acp.LogoutResponse, error) {
	return acp.LogoutResponse{}, acp.NewMethodNotFound(acp.AgentMethodLogout)
}

// Cancel 为测试 Agent 实现 acp.Agent；没有活动任务时取消是幂等操作。
func (a *testAgent) Cancel(context.Context, acp.CancelNotification) error {
	return nil
}

// CloseSession 为测试 Agent 实现 acp.Agent；框架测试不声明会话关闭能力。
func (a *testAgent) CloseSession(context.Context, acp.CloseSessionRequest) (acp.CloseSessionResponse, error) {
	return acp.CloseSessionResponse{}, acp.NewMethodNotFound(acp.AgentMethodSessionClose)
}

// ListSessions 为测试 Agent 实现 acp.Agent；框架测试不声明会话列表能力。
func (a *testAgent) ListSessions(context.Context, acp.ListSessionsRequest) (acp.ListSessionsResponse, error) {
	return acp.ListSessionsResponse{}, acp.NewMethodNotFound(acp.AgentMethodSessionList)
}

// NewSession 为测试 Agent 实现 acp.Agent；本测试只覆盖 initialize 边界。
func (a *testAgent) NewSession(context.Context, acp.NewSessionRequest) (acp.NewSessionResponse, error) {
	return acp.NewSessionResponse{}, acp.NewMethodNotFound(acp.AgentMethodSessionNew)
}

// Prompt 为测试 Agent 实现 acp.Agent；本测试不启动 prompt。
func (a *testAgent) Prompt(context.Context, acp.PromptRequest) (acp.PromptResponse, error) {
	return acp.PromptResponse{}, acp.NewMethodNotFound(acp.AgentMethodSessionPrompt)
}

// ResumeSession 为测试 Agent 实现 acp.Agent；框架测试不声明恢复能力。
func (a *testAgent) ResumeSession(context.Context, acp.ResumeSessionRequest) (acp.ResumeSessionResponse, error) {
	return acp.ResumeSessionResponse{}, acp.NewMethodNotFound(acp.AgentMethodSessionResume)
}

// SetSessionConfigOption 为测试 Agent 实现 acp.Agent；框架测试不声明配置能力。
func (a *testAgent) SetSessionConfigOption(
	context.Context,
	acp.SetSessionConfigOptionRequest,
) (acp.SetSessionConfigOptionResponse, error) {
	return acp.SetSessionConfigOptionResponse{}, acp.NewMethodNotFound(acp.AgentMethodSessionSetConfigOption)
}

// SetSessionMode 为测试 Agent 实现 acp.Agent；框架测试不声明模式能力。
func (a *testAgent) SetSessionMode(context.Context, acp.SetSessionModeRequest) (acp.SetSessionModeResponse, error) {
	return acp.SetSessionModeResponse{}, acp.NewMethodNotFound(acp.AgentMethodSessionSetMode)
}

// TestServerUsesSDKConnection 验证 Server 直接用 SDK 处理 NDJSON initialize 并注入同一 connection。
// 若改为自有 JSON-RPC、接反 stdin/stdout 或漏掉 connection 注入，本测试应失败。
func TestServerUsesSDKConnection(t *testing.T) {
	t.Parallel()

	serverInput, clientOutput := io.Pipe()
	clientInput, serverOutput := io.Pipe()
	t.Cleanup(func() {
		closeTestPipe(t, serverInput)
		closeTestPipe(t, clientOutput)
		closeTestPipe(t, clientInput)
		closeTestPipe(t, serverOutput)
	})

	agent := newTestAgent()
	server, err := New(agent, serverInput, serverOutput)
	if err != nil {
		t.Fatalf("创建协议 Server 失败: %v", err)
	}

	serveResult := make(chan error, 1)
	go func() {
		serveResult <- server.Serve(context.Background())
	}()

	select {
	case connection := <-agent.connectionReady:
		if connection == nil {
			t.Fatal("注入的 SDK connection 不能为空")
		}
	case <-time.After(time.Second):
		t.Fatal("等待 SDK connection 注入超时")
	}

	request := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":1,"clientCapabilities":{}}}` + "\n"
	if _, err = io.WriteString(clientOutput, request); err != nil {
		t.Fatalf("写入 initialize 请求失败: %v", err)
	}

	responseLine, err := bufio.NewReader(clientInput).ReadBytes('\n')
	if err != nil {
		t.Fatalf("读取 initialize 响应失败: %v", err)
	}

	var response map[string]json.RawMessage
	if err = json.Unmarshal(responseLine, &response); err != nil {
		t.Fatalf("响应不是合法 JSON: %v", err)
	}
	if string(response["jsonrpc"]) != `"2.0"` {
		t.Fatalf("jsonrpc 为 %s，期望 2.0", response["jsonrpc"])
	}
	if string(response["id"]) != "1" {
		t.Fatalf("响应 id 为 %s，期望 1", response["id"])
	}

	var result acp.InitializeResponse
	if err = json.Unmarshal(response["result"], &result); err != nil {
		t.Fatalf("解析 initialize result 失败: %v", err)
	}
	if result.ProtocolVersion != acp.ProtocolVersionNumber {
		t.Fatalf("协议版本为 %d，期望 %d", result.ProtocolVersion, acp.ProtocolVersionNumber)
	}
	if result.AgentInfo == nil || result.AgentInfo.Name != "test-agent" {
		t.Fatalf("Agent 信息为 %#v，期望 test-agent", result.AgentInfo)
	}

	if err = clientOutput.Close(); err != nil {
		t.Fatalf("关闭客户端输出失败: %v", err)
	}
	select {
	case err = <-serveResult:
		if err != nil {
			t.Fatalf("协议 Server 返回错误: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("等待协议 Server 退出超时")
	}

	select {
	case closeContextErr := <-agent.closed:
		if closeContextErr != nil {
			t.Fatalf("peer 断开时关闭上下文错误为 %v", closeContextErr)
		}
	case <-time.After(time.Second):
		t.Fatal("等待 Adapter 关闭超时")
	}
}

// TestNewRejectsMissingDependencies 验证协议边界在启动 goroutine 前拒绝缺失依赖。
// 若 nil Agent 或 I/O 延迟到 SDK 内部才失败，本测试应失败。
func TestNewRejectsMissingDependencies(t *testing.T) {
	t.Parallel()

	agent := newTestAgent()
	tests := []struct {
		// name 描述当前缺失的构造依赖。
		name string
		// agent 是交给构造函数的 SDK Agent。
		agent acp.Agent
		// input 是交给构造函数的协议输入。
		input io.Reader
		// output 是交给构造函数的协议输出。
		output io.Writer
	}{
		{name: "缺少 Agent", agent: nil, input: &emptyReader{}, output: io.Discard},
		{name: "缺少输入", agent: agent, input: nil, output: io.Discard},
		{name: "缺少输出", agent: agent, input: &emptyReader{}, output: nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if _, err := New(tt.agent, tt.input, tt.output); err == nil {
				t.Fatal("缺失依赖时创建 Server 未返回错误")
			}
		})
	}
}

// TestServerTreatsContextCancellationAsGraceful 验证进程上下文取消是正常关停而非启动失败。
// 若 Serve 把信号取消传播为 CLI 错误并产生误导诊断，本测试应失败。
func TestServerTreatsContextCancellationAsGraceful(t *testing.T) {
	t.Parallel()

	serverInput, clientOutput := io.Pipe()
	t.Cleanup(func() {
		closeTestPipe(t, serverInput)
		closeTestPipe(t, clientOutput)
	})

	agent := newTestAgent()
	server, err := New(agent, serverInput, io.Discard)
	if err != nil {
		t.Fatalf("创建协议 Server 失败: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	serveResult := make(chan error, 1)
	go func() {
		serveResult <- server.Serve(ctx)
	}()

	select {
	case <-agent.connectionReady:
	case <-time.After(time.Second):
		t.Fatal("等待 SDK connection 注入超时")
	}

	cancel()
	select {
	case err = <-serveResult:
		if err != nil {
			t.Fatalf("上下文取消应正常退出，实际返回: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("等待协议 Server 取消超时")
	}

	select {
	case closeContextErr := <-agent.closed:
		if closeContextErr != nil {
			t.Fatalf("进程取消后关闭上下文错误为 %v，期望独立清理上下文", closeContextErr)
		}
	case <-time.After(time.Second):
		t.Fatal("等待 Adapter 关闭超时")
	}
}

// TestServerReturnsAdapterCloseFailure 验证资源清理失败会保留根因并返回组合根。
// 若协议服务吞掉 Adapter Close 错误并报告正常退出，本测试应失败。
func TestServerReturnsAdapterCloseFailure(t *testing.T) {
	t.Parallel()

	serverInput, clientOutput := io.Pipe()
	t.Cleanup(func() {
		closeTestPipe(t, serverInput)
		closeTestPipe(t, clientOutput)
	})

	closeErr := errors.New("close runtime")
	agent := newTestAgent()
	agent.closeErr = closeErr
	server, err := New(agent, serverInput, io.Discard)
	if err != nil {
		t.Fatalf("创建协议 Server 失败: %v", err)
	}

	serveResult := make(chan error, 1)
	go func() {
		serveResult <- server.Serve(context.Background())
	}()

	select {
	case <-agent.connectionReady:
	case <-time.After(time.Second):
		t.Fatal("等待 SDK connection 注入超时")
	}
	if err = clientOutput.Close(); err != nil {
		t.Fatalf("关闭客户端输出失败: %v", err)
	}

	select {
	case err = <-serveResult:
		if !errors.Is(err, closeErr) {
			t.Fatalf("关闭错误为 %v，期望保留 %v", err, closeErr)
		}
	case <-time.After(time.Second):
		t.Fatal("等待协议 Server 退出超时")
	}
}

// emptyReader 是不产生数据的非 nil Reader，用于隔离构造参数校验分支。
type emptyReader struct{}

// Read 实现 io.Reader，并立即报告输入结束。
func (*emptyReader) Read([]byte) (int, error) {
	return 0, io.EOF
}

// closeTestPipe 关闭测试管道，并忽略已经由测试主路径关闭的幂等错误。
func closeTestPipe(t *testing.T, closer io.Closer) {
	t.Helper()
	if err := closer.Close(); err != nil && !errors.Is(err, io.ErrClosedPipe) {
		t.Errorf("关闭测试管道失败: %v", err)
	}
}

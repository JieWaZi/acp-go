package acpserver

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	acp "github.com/JieWaZi/acp-go-sdk"
)

// testAgent supplies the protocol methods and records the bound SDK connection.
type testAgent struct {
	// connectionReady publishes the bound connection without relying on scheduling delays.
	connectionReady chan *acp.AgentSideConnection
	// closed reports the context error observed during adapter cleanup.
	closed chan error
	// closeErr is the cleanup failure returned to the server.
	closeErr error
}

var _ acp.Agent = (*testAgent)(nil)

// newTestAgent creates buffered channels for connection binding and cleanup observations.
func newTestAgent() *testAgent {
	return &testAgent{
		connectionReady: make(chan *acp.AgentSideConnection, 1),
		closed:          make(chan error, 1),
	}
}

// SetAgentConnection receives the SDK connection created by the server.
func (a *testAgent) SetAgentConnection(connection *acp.AgentSideConnection) {
	a.connectionReady <- connection
}

// Close records the context supplied by server cleanup.
func (a *testAgent) Close(ctx context.Context) error {
	a.closed <- ctx.Err()
	return a.closeErr
}

// Authenticate rejects authentication, which this fixture does not support.
func (a *testAgent) Authenticate(context.Context, acp.AuthenticateRequest) (acp.AuthenticateResponse, error) {
	return acp.AuthenticateResponse{}, acp.NewMethodNotFound(acp.AgentMethodAuthenticate)
}

// Initialize returns a minimal handshake that the protocol test can verify independently.
func (a *testAgent) Initialize(context.Context, acp.InitializeRequest) (acp.InitializeResponse, error) {
	return acp.InitializeResponse{
		ProtocolVersion: acp.ProtocolVersionNumber,
		AgentInfo: &acp.Implementation{
			Name:    "test-agent",
			Version: "test",
		},
	}, nil
}

// Logout rejects sign-out, which this fixture does not support.
func (a *testAgent) Logout(context.Context, acp.LogoutRequest) (acp.LogoutResponse, error) {
	return acp.LogoutResponse{}, acp.NewMethodNotFound(acp.AgentMethodLogout)
}

// Cancel is an idle no-op for this fixture.
func (a *testAgent) Cancel(context.Context, acp.CancelNotification) error {
	return nil
}

// CloseSession rejects session cleanup, which this fixture does not support.
func (a *testAgent) CloseSession(context.Context, acp.CloseSessionRequest) (acp.CloseSessionResponse, error) {
	return acp.CloseSessionResponse{}, acp.NewMethodNotFound(acp.AgentMethodSessionClose)
}

// ListSessions rejects session listing, which this fixture does not support.
func (a *testAgent) ListSessions(context.Context, acp.ListSessionsRequest) (acp.ListSessionsResponse, error) {
	return acp.ListSessionsResponse{}, acp.NewMethodNotFound(acp.AgentMethodSessionList)
}

// NewSession rejects session creation; this fixture exercises initialization only.
func (a *testAgent) NewSession(context.Context, acp.NewSessionRequest) (acp.NewSessionResponse, error) {
	return acp.NewSessionResponse{}, acp.NewMethodNotFound(acp.AgentMethodSessionNew)
}

// Prompt rejects execution; this fixture never starts native work.
func (a *testAgent) Prompt(context.Context, acp.PromptRequest) (acp.PromptResponse, error) {
	return acp.PromptResponse{}, acp.NewMethodNotFound(acp.AgentMethodSessionPrompt)
}

// ResumeSession rejects session recovery, which this fixture does not support.
func (a *testAgent) ResumeSession(context.Context, acp.ResumeSessionRequest) (acp.ResumeSessionResponse, error) {
	return acp.ResumeSessionResponse{}, acp.NewMethodNotFound(acp.AgentMethodSessionResume)
}

// SetSessionConfigOption rejects configuration changes, which this fixture does not support.
func (a *testAgent) SetSessionConfigOption(
	context.Context,
	acp.SetSessionConfigOptionRequest,
) (acp.SetSessionConfigOptionResponse, error) {
	return acp.SetSessionConfigOptionResponse{}, acp.NewMethodNotFound(acp.AgentMethodSessionSetConfigOption)
}

// SetSessionMode rejects mode changes, which this fixture does not support.
func (a *testAgent) SetSessionMode(context.Context, acp.SetSessionModeRequest) (acp.SetSessionModeResponse, error) {
	return acp.SetSessionModeResponse{}, acp.NewMethodNotFound(acp.AgentMethodSessionSetMode)
}

// TestServerUsesSDKConnection checks SDK NDJSON initialization and binding of the same connection.
// Replacing SDK dispatch, reversing the streams, or omitting the binding must fail this test.
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

// TestNewRejectsMissingDependencies rejects incomplete construction before any goroutine starts.
// Deferring nil dependency checks to SDK startup must fail this test.
func TestNewRejectsMissingDependencies(t *testing.T) {
	t.Parallel()

	agent := newTestAgent()
	tests := []struct {
		// name identifies the missing constructor dependency.
		name string
		// agent is the SDK implementation passed to the constructor.
		agent acp.Agent
		// input is the constructor's protocol reader.
		input io.Reader
		// output is the constructor's protocol writer.
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

// TestServerTreatsContextCancellationAsGraceful checks normal shutdown on owner cancellation.
// Reporting signal cancellation as a CLI failure must fail this test.
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

	var connection *acp.AgentSideConnection
	select {
	case connection = <-agent.connectionReady:
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
	case <-connection.Done():
	default:
		t.Fatal("Serve returned with its ACP connection still open")
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

// TestServerCancellationRetainsCloseFailure closes the protocol owner even
// when releasing the independent native client fails.
func TestServerCancellationRetainsCloseFailure(t *testing.T) {
	input, writer := io.Pipe()
	t.Cleanup(func() { _ = input.Close(); _ = writer.Close() })
	agent := newTestAgent()
	agent.closeErr = errors.New("native client close failed")
	server, err := New(agent, struct {
		// Reader borrows input without exposing a transport Close method.
		io.Reader
	}{input}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	finished := make(chan error, 1)
	go func() { finished <- server.Serve(ctx) }()
	connection := <-agent.connectionReady
	cancel()
	select {
	case err := <-finished:
		if !errors.Is(err, agent.closeErr) {
			t.Fatalf("Serve lost the native close error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Serve waited for a borrowed reader to finish")
	}
	select {
	case <-connection.Done():
	default:
		t.Fatal("native close failure retained an open protocol connection")
	}
}

// connectionOnlyAgent exposes no native resource closer, so protocol cleanup
// must not depend on the optional adapterCloser contract.
type connectionOnlyAgent struct {
	// Agent supplies protocol methods without exposing the embedded test closer.
	acp.Agent
	// ready publishes the logical connection acquired by the server.
	ready chan *acp.AgentSideConnection
}

func (a *connectionOnlyAgent) SetAgentConnection(connection *acp.AgentSideConnection) {
	a.ready <- connection
}

func TestServerCancellationClosesConnectionWithoutAdapterCloser(t *testing.T) {
	input, writer := io.Pipe()
	t.Cleanup(func() { _ = input.Close(); _ = writer.Close() })
	agent := &connectionOnlyAgent{Agent: newTestAgent(), ready: make(chan *acp.AgentSideConnection, 1)}
	server, err := New(agent, struct {
		// Reader borrows input without exposing a transport Close method.
		io.Reader
	}{input}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	finished := make(chan error, 1)
	go func() { finished <- server.Serve(ctx) }()
	connection := <-agent.ready
	cancel()
	select {
	case err := <-finished:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Serve waited for a borrowed reader to finish")
	}
	select {
	case <-connection.Done():
	default:
		t.Fatal("protocol cleanup depended on a native client closer")
	}
}

// drainingNotificationAgent holds the first received notification so the
// service can observe EOF while the SDK still owns a queued second one.
type drainingNotificationAgent struct {
	// testAgent supplies the independent native closer and protocol methods.
	*testAgent
	// entered signals that the first notification is being delivered.
	entered chan struct{}
	// release allows that delivery to complete after Serve observes EOF.
	release chan struct{}
	// delivered records the context seen by the queued notification.
	delivered chan error
}

func (a *drainingNotificationAgent) HandleExtensionMethod(ctx context.Context, method string, _ json.RawMessage) (any, error) {
	if method == "_hold" {
		close(a.entered)
		select {
		case <-ctx.Done():
		case <-a.release:
		}
	} else {
		a.delivered <- ctx.Err()
	}
	return nil, nil
}

func TestServerPeerEOFDrainsReceivedNotifications(t *testing.T) {
	agent := &drainingNotificationAgent{testAgent: newTestAgent(), entered: make(chan struct{}), release: make(chan struct{}, 1), delivered: make(chan error, 1)}
	defer close(agent.release)
	frames := `{"jsonrpc":"2.0","method":"_hold"}` + "\n" + `{"jsonrpc":"2.0","method":"_queued"}` + "\n"
	server, err := New(agent, strings.NewReader(frames), io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	finished := make(chan error, 1)
	go func() { finished <- server.Serve(context.Background()) }()
	select {
	case <-agent.entered:
	case <-time.After(time.Second):
		t.Fatal("first notification was not delivered")
	}
	select {
	case err := <-finished:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Serve did not observe peer EOF")
	}
	agent.release <- struct{}{}
	select {
	case err := <-agent.delivered:
		if err != nil {
			t.Fatalf("peer EOF aborted a received notification: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("received notification was discarded after peer EOF")
	}
}

// TestServerReturnsAdapterCloseFailure preserves the cleanup cause at the composition root.
// Swallowing an adapter cleanup failure must fail this test.
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

// emptyReader supplies a non-nil reader for constructor validation without producing data.
type emptyReader struct{}

// Read reports EOF immediately.
func (*emptyReader) Read([]byte) (int, error) {
	return 0, io.EOF
}

// closeTestPipe closes a test pipe and tolerates a prior close in the test body.
func closeTestPipe(t *testing.T, closer io.Closer) {
	t.Helper()
	if err := closer.Close(); err != nil && !errors.Is(err, io.ErrClosedPipe) {
		t.Errorf("关闭测试管道失败: %v", err)
	}
}

// bindingAgent gives the test control over when connection binding completes.
type bindingAgent struct {
	// testAgent supplies the other ACP test methods.
	*testAgent
	// finishBinding unblocks connection binding.
	finishBinding chan struct{}
}

func (a *bindingAgent) SetAgentConnection(connection *acp.AgentSideConnection) {
	a.testAgent.SetAgentConnection(connection)
	<-a.finishBinding
}

// ingressProbe records the SDK’s first read.
type ingressProbe struct {
	// reading closes when the first read starts.
	reading chan struct{}
}

func (r ingressProbe) Read([]byte) (int, error) {
	close(r.reading)
	return 0, io.EOF
}

func TestServerBindsBeforeReadingHostFrames(t *testing.T) {
	agent := &bindingAgent{testAgent: newTestAgent(), finishBinding: make(chan struct{})}
	reading := make(chan struct{})
	server, err := New(agent, ingressProbe{reading}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	finished := make(chan error, 1)
	go func() { finished <- server.Serve(context.Background()) }()
	defer func() {
		close(agent.finishBinding)
		select {
		case err := <-finished:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(time.Second):
			t.Error("注册完成后 Server 未退出")
		}
	}()
	select {
	case <-agent.connectionReady:
	case <-time.After(time.Second):
		t.Fatal("Server 未开始连接注册")
	}
	select {
	case <-reading:
		t.Fatal("连接注册结束前读取了宿主输入")
	case <-time.After(50 * time.Millisecond):
	}
}

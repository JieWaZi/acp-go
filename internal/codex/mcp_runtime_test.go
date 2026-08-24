package codex

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"acp-go/agents/codex/protocol"
	acp "github.com/coder/acp-go-sdk"
)

// recordingElicitationRequester 记录 ACP Elicitation 并返回预置响应。
type recordingElicitationRequester struct {
	// requests 保存收到的表单或 URL 请求。
	requests []acp.UnstableCreateElicitationRequest
	// completed 保存收到的 URL 完成通知。
	completed []acp.UnstableCompleteElicitationNotification
	// response 是 create 请求的预置响应。
	response acp.UnstableCreateElicitationResponse
	// err 是 create 请求的预置错误。
	err error
}

// UnstableCreateElicitation 记录请求并返回预置响应。
func (r *recordingElicitationRequester) UnstableCreateElicitation(
	_ context.Context,
	request acp.UnstableCreateElicitationRequest,
) (acp.UnstableCreateElicitationResponse, error) {
	r.requests = append(r.requests, request)
	return r.response, r.err
}

// UnstableCompleteElicitation 记录 URL 交互完成通知。
func (r *recordingElicitationRequester) UnstableCompleteElicitation(
	_ context.Context,
	notification acp.UnstableCompleteElicitationNotification,
) error {
	r.completed = append(r.completed, notification)
	return nil
}

// TestAgentSessionMCPConfigMatchesCodexACP 验证 stdio/HTTP 映射、名称清洗与同名配置过滤。
func TestAgentSessionMCPConfigMatchesCodexACP(t *testing.T) {
	rpc := newFakeAppServerRPC()
	var startConfig map[string]json.RawMessage
	rpc.handleCall = func(_ context.Context, request protocol.ClientRequest, result any) error {
		switch request.Method() {
		case protocol.MethodInitialize:
			return nil
		case protocol.MethodConfigRead:
			result.(*protocol.ConfigReadResponse).Config = json.RawMessage(`{"mcp_servers":{"existing_server":{"command":"user-command"}}}`)
			return nil
		case protocol.MethodThreadStart:
			startConfig = request.(protocol.ThreadStartRequest).Params.Config
			response := result.(*protocol.ThreadStartResponse)
			response.Thread.ID = "thread-mcp-config"
			return nil
		default:
			return errors.New("unexpected call: " + request.Method())
		}
	}
	agent := newRuntimeTestAgent(t, rpc)

	_, err := agent.NewSession(context.Background(), acp.NewSessionRequest{
		Cwd: "/tmp",
		McpServers: []acp.McpServer{
			{Stdio: &acp.McpServerStdio{Name: "existing server", Command: "ignored", Args: []string{}, Env: []acp.EnvVariable{}}},
			{Http: &acp.McpServerHttpInline{
				Name: "remote server", Type: "http", Url: "https://mcp.example.test",
				Headers: []acp.HttpHeader{{Name: "Authorization", Value: "Bearer token"}},
			}},
		},
	})
	if err != nil {
		t.Fatalf("创建带 MCP 的 session 失败: %v", err)
	}
	var configured map[string]map[string]any
	if err = json.Unmarshal(startConfig["mcp_servers"], &configured); err != nil {
		t.Fatalf("解析 thread/start MCP 配置失败: %v", err)
	}
	if _, exists := configured["existing_server"]; exists {
		t.Fatal("ACP MCP 配置覆盖了同名用户配置")
	}
	remote := configured["remote_server"]
	if remote["url"] != "https://mcp.example.test" || remote["http_headers"].(map[string]any)["Authorization"] != "Bearer token" {
		t.Fatalf("HTTP MCP 配置为 %#v", remote)
	}
}

// TestCodexMCPServerConfigRejectsUnsupportedTransports 验证未声明的 SSE/ACP transport 显式失败。
func TestCodexMCPServerConfigRejectsUnsupportedTransports(t *testing.T) {
	tests := []acp.McpServer{
		{Sse: &acp.McpServerSseInline{Name: "sse", Type: "sse"}},
		{Acp: &acp.McpServerAcpInline{Name: "acp", Type: "acp"}},
	}
	for _, server := range tests {
		if _, _, err := codexMCPServerConfig(server); err == nil {
			t.Fatalf("未拒绝不支持的 MCP transport: %#v", server)
		}
	}
}

// TestToolRequestUserInputUsesACPForm 验证 request_user_input 的选项、自定义答案和结果回填。
func TestToolRequestUserInputUsesACPForm(t *testing.T) {
	requester := &recordingElicitationRequester{response: acp.UnstableCreateElicitationResponse{
		Accept: &acp.UnstableCreateElicitationAccept{Action: "accept", Content: map[string]any{
			"language": "Go", "language__other": "Zig",
		}},
	}}
	agent, state := newElicitationTestAgent(t, requester, true, true)
	prompt := newActivePrompt(agent.runtimeCtx, 7)
	prompt.setTurn("turn-input")
	state.mu.Lock()
	state.activePrompt = prompt
	state.mu.Unlock()
	request, err := protocol.DecodeServerRequest([]byte(`{
		"id":"input-1","method":"item/tool/requestUserInput","params":{
			"isBlocking":true,"itemId":"item-input","threadId":"thread-elicit","turnId":"turn-input",
			"questions":[{"header":"Language","id":"language","question":"Choose a language","isOther":true,
			"options":[{"label":"Go","description":"Use Go"}]}]
		}}`))
	if err != nil {
		t.Fatalf("解码 request_user_input 失败: %v", err)
	}
	value, err := agent.handleServerRequest(context.Background(), request)
	if err != nil {
		t.Fatalf("处理 request_user_input 失败: %v", err)
	}
	response := value.(protocol.ToolRequestUserInputResponse)
	if !reflect.DeepEqual(response.Answers["language"].Answers, []string{"Zig"}) {
		t.Fatalf("request_user_input 答案为 %#v", response.Answers)
	}
	if len(requester.requests) != 1 || requester.requests[0].Form == nil {
		t.Fatalf("ACP form 请求为 %#v", requester.requests)
	}
	properties := requester.requests[0].Form.RequestedSchema.Properties
	if properties["language__other"] == nil || len(requester.requests[0].Form.RequestedSchema.Required) != 0 {
		t.Fatalf("自定义答案 schema 为 %#v", requester.requests[0].Form.RequestedSchema)
	}
}

// TestMCPServerElicitationUsesACPAndCompletesURL 验证 MCP URL accept 与 resolved 完成通知闭环。
func TestMCPServerElicitationUsesACPAndCompletesURL(t *testing.T) {
	requester := &recordingElicitationRequester{response: acp.UnstableCreateElicitationResponse{
		Accept: &acp.UnstableCreateElicitationAccept{Action: "accept", Content: map[string]any{}},
	}}
	agent, _ := newElicitationTestAgent(t, requester, true, true)
	request, err := protocol.DecodeServerRequest([]byte(`{
		"id":"mcp-elicit-1","method":"mcpServer/elicitation/request","params":{
			"serverName":"remote","threadId":"thread-elicit","mode":"url","message":"Authorize remote MCP",
			"elicitationId":"url-1","url":"https://mcp.example.test/auth"
		}}`))
	if err != nil {
		t.Fatalf("解码 MCP elicitation 失败: %v", err)
	}
	value, err := agent.handleServerRequest(context.Background(), request)
	if err != nil {
		t.Fatalf("处理 MCP elicitation 失败: %v", err)
	}
	if value.(protocol.MCPServerElicitationRequestResponse).Action != protocol.MCPServerElicitationActionAccept {
		t.Fatalf("MCP elicitation 响应为 %#v", value)
	}
	resolved, err := protocol.DecodeServerNotification([]byte(`{
		"method":"serverRequest/resolved","params":{"requestId":"mcp-elicit-1","threadId":"thread-elicit"}
	}`))
	if err != nil {
		t.Fatalf("解码 serverRequest/resolved 失败: %v", err)
	}
	agent.handleNotification(context.Background(), resolved)
	if len(requester.completed) != 1 || requester.completed[0].ElicitationId != "url-1" {
		t.Fatalf("URL elicitation 完成通知为 %#v", requester.completed)
	}
}

// TestMCPServerElicitationFallsBackToPermission 验证客户端未声明 form 时仍可允许或拒绝 MCP 请求。
func TestMCPServerElicitationFallsBackToPermission(t *testing.T) {
	agent, _ := newElicitationTestAgent(t, &recordingElicitationRequester{}, false, false)
	permission := &recordingPermissionRequester{response: selectedPermission("accept")}
	agent.connectionMu.Lock()
	agent.approvalRequester = permission
	agent.connectionMu.Unlock()
	request, err := protocol.DecodeServerRequest([]byte(`{
		"id":"mcp-elicit-form","method":"mcpServer/elicitation/request","params":{
			"serverName":"local","threadId":"thread-elicit","mode":"form","message":"Choose scope",
			"requestedSchema":{"type":"object","properties":{}}
		}}`))
	if err != nil {
		t.Fatalf("解码 MCP form elicitation 失败: %v", err)
	}
	value, err := agent.handleServerRequest(context.Background(), request)
	if err != nil {
		t.Fatalf("处理 MCP permission fallback 失败: %v", err)
	}
	if value.(protocol.MCPServerElicitationRequestResponse).Action != protocol.MCPServerElicitationActionAccept ||
		len(permission.requests) != 1 {
		t.Fatalf("MCP permission fallback 结果为 %#v，请求为 %#v", value, permission.requests)
	}
}

// TestMCPStartupStatusIgnoresOlderSameNameFailure 验证版本水位隔离旧会话的同名失败状态。
func TestMCPStartupStatusIgnoresOlderSameNameFailure(t *testing.T) {
	agent, state := newElicitationTestAgent(t, &recordingElicitationRequester{}, false, false)
	updater := &recordingSessionUpdater{}
	agent.connectionMu.Lock()
	agent.sessionUpdater = updater
	agent.connectionMu.Unlock()
	oldError := "old failure"
	agent.handleMCPStartupStatus(context.Background(), protocol.MCPServerStatusUpdatedNotification{
		Name: "shared", Status: protocol.PurpleFailed, Error: &oldError,
	})
	afterVersion := agent.currentMCPStatusVersion()
	agent.publishKnownMCPStartupFailures(state, []string{"shared"}, afterVersion)
	if len(updater.notifications) != 0 {
		t.Fatalf("新会话错误消费了旧 MCP 状态: %#v", updater.notifications)
	}
	newError := "new failure"
	agent.handleMCPStartupStatus(context.Background(), protocol.MCPServerStatusUpdatedNotification{
		Name: "shared", Status: protocol.PurpleFailed, Error: &newError,
	})
	if len(updater.notifications) != 1 || updater.notifications[0].Update.ToolCall == nil ||
		updater.notifications[0].Update.ToolCall.Status != acp.ToolCallStatusFailed {
		t.Fatalf("当前 MCP 启动失败通知为 %#v", updater.notifications)
	}
}

// newElicitationTestAgent 创建已安装 session 且声明 Elicitation 能力的最小 Agent。
func newElicitationTestAgent(
	t *testing.T,
	requester elicitationRequester,
	form bool,
	urlMode bool,
) (*Agent, *sessionState) {
	t.Helper()
	agent := newTestAgent(t)
	agent.initializeMu.Lock()
	agent.initialized = true
	agent.elicitationCapabilities = &acp.ElicitationCapabilities{}
	if form {
		agent.elicitationCapabilities.Form = &acp.ElicitationFormCapabilities{}
	}
	if urlMode {
		agent.elicitationCapabilities.Url = &acp.ElicitationUrlCapabilities{}
	}
	agent.initializeMu.Unlock()
	agent.connectionMu.Lock()
	agent.elicitationRequester = requester
	agent.connectionMu.Unlock()
	generation, err := agent.sessions.beginOpen("thread-elicit")
	if err != nil {
		t.Fatalf("建立 elicitation session 失败: %v", err)
	}
	state, installed := agent.sessions.install("thread-elicit", "/tmp", generation, nil, terminalOutputModeDelta)
	if !installed {
		t.Fatal("安装 elicitation session 失败")
	}
	return agent, state
}

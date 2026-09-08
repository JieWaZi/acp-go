package nativeacp_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JieWaZi/acp-go/pkg/acpserver"
	"github.com/JieWaZi/acp-go/pkg/nativeacp"
	acp "github.com/coder/acp-go-sdk"
)

// hostClient 只实现本测试实际宣告或使用的宿主能力。
type hostClient struct {
	// Client 保留未宣告能力的接口，不允许测试意外调用。
	acp.Client
	// mutex 保护异步通知与审批记录。
	mutex sync.Mutex
	// updates 保存收到的流式消息。
	updates []string
	// approvals 记录权限请求次数。
	approvals int
	// rejectInteractions 模拟用户拒绝计划和跳过问题。
	rejectInteractions bool
	// questions 记录 Cursor 转换后的提问次数。
	questions int
	// plans 保存 Cursor 通知转换后的完整计划，用于检查 merge 语义。
	plans [][]acp.PlanEntry
	// toolUpdates 记录图片和子任务内容投影。
	toolUpdates int
}

// SessionUpdate 收集宿主实际收到的有序文本。
func (client *hostClient) SessionUpdate(_ context.Context, request acp.SessionNotification) error {
	client.mutex.Lock()
	defer client.mutex.Unlock()
	if request.Update.AgentMessageChunk != nil {
		client.updates = append(client.updates, request.Update.AgentMessageChunk.Content.Text.Text)
	}
	if request.Update.Plan != nil {
		client.plans = append(client.plans, request.Update.Plan.Entries)
	}
	if request.Update.ToolCallUpdate != nil {
		client.toolUpdates++
	}
	return nil
}

// RequestPermission 模拟用户选择一次性批准。
func (client *hostClient) RequestPermission(_ context.Context, request acp.RequestPermissionRequest) (acp.RequestPermissionResponse, error) {
	client.mutex.Lock()
	client.approvals++
	client.mutex.Unlock()
	if request.ToolCall.ToolCallId == "plan" {
		if request.ToolCall.Title == nil || *request.ToolCall.Title != "Plan" || len(request.ToolCall.Content) != 1 || request.ToolCall.Content[0].Content == nil || !strings.Contains(request.ToolCall.Content[0].Content.Content.Text.Text, "Exact plan body") {
			return acp.RequestPermissionResponse{}, acp.NewInvalidParams(nil)
		}
		if client.rejectInteractions {
			return acp.RequestPermissionResponse{Outcome: acp.NewRequestPermissionOutcomeSelected("reject")}, nil
		}
	}
	return acp.RequestPermissionResponse{Outcome: acp.NewRequestPermissionOutcomeSelected(request.Options[0].OptionId)}, nil
}

// UnstableCreateElicitation 校验会话关联并返回问题选项。
func (client *hostClient) UnstableCreateElicitation(_ context.Context, request acp.UnstableCreateElicitationRequest) (acp.UnstableCreateElicitationResponse, error) {
	client.mutex.Lock()
	client.questions++
	client.mutex.Unlock()
	if request.Form == nil || request.Form.Meta["sessionId"] == nil {
		return acp.NewUnstableCreateElicitationResponseCancel(), nil
	}
	if client.rejectInteractions {
		return acp.NewUnstableCreateElicitationResponseDecline(), nil
	}
	response := acp.NewUnstableCreateElicitationResponseAccept()
	response.Accept.Content = map[string]any{"choice": "a"}
	return response, nil
}

// UnstableCompleteElicitation 接收无需进一步处理的完成通知。
func (*hostClient) UnstableCompleteElicitation(context.Context, acp.UnstableCompleteElicitationNotification) error {
	return nil
}

// UnstableConnectMcp 明确拒绝测试未声明的 MCP 能力。
func (*hostClient) UnstableConnectMcp(context.Context, acp.UnstableConnectMcpRequest) (acp.UnstableConnectMcpResponse, error) {
	return acp.UnstableConnectMcpResponse{}, acp.NewMethodNotFound("mcp/connect")
}

// UnstableDisconnectMcp 明确拒绝测试未声明的 MCP 能力。
func (*hostClient) UnstableDisconnectMcp(context.Context, acp.UnstableDisconnectMcpRequest) (acp.UnstableDisconnectMcpResponse, error) {
	return acp.UnstableDisconnectMcpResponse{}, acp.NewMethodNotFound("mcp/disconnect")
}

// startAgent 通过 acpserver 和双向管道连接实际子进程。
func startAgent(t *testing.T, variant string) (*nativeacp.Agent, *acp.ClientSideConnection, *hostClient) {
	t.Helper()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	agent, err := nativeacp.NewAgent(context.Background(), nativeacp.Config{Command: binary, Args: []string{"-test.run=^TestACPProcess$"}, Environment: append(os.Environ(), "NATIVE_ACP_TEST="+variant), Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), CursorExtensions: true})
	if err != nil {
		t.Fatal(err)
	}
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	server, err := acpserver.New(agent, inR, outW)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- server.Serve(ctx) }()
	host := &hostClient{rejectInteractions: variant == "reject"}
	connection := acp.NewClientSideConnection(host, inW, outR)
	t.Cleanup(func() {
		cancel()
		_ = inR.Close()
		_ = inW.Close()
		_ = outR.Close()
		_ = outW.Close()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(5 * time.Second):
			t.Error("server did not close")
		}
	})
	return agent, connection, host
}

// TestNativeModelSelectionAndCallbacks 验证新版和旧版模型协议以及反向交互。
func TestNativeModelSelectionAndCallbacks(t *testing.T) {
	for _, variant := range []string{"config", "legacy", "reject"} {
		t.Run(variant, func(t *testing.T) {
			_, connection, host := startAgent(t, variant)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			initialized, err := connection.Initialize(ctx, acp.InitializeRequest{ProtocolVersion: 1})
			if err != nil || initialized.AgentInfo.Name != "fixture" {
				t.Fatalf("initialize: %+v %v", initialized, err)
			}
			session, err := connection.NewSession(ctx, acp.NewSessionRequest{Cwd: t.TempDir(), McpServers: []acp.McpServer{}})
			if err != nil {
				t.Fatal(err)
			}
			if len(session.ConfigOptions) == 0 || session.ConfigOptions[0].Select.Id != "model" {
				t.Fatalf("options: %+v", session.ConfigOptions)
			}
			updated, err := connection.SetSessionConfigOption(ctx, acp.SetSessionConfigOptionRequest{ValueId: &acp.SetSessionConfigOptionValueId{SessionId: session.SessionId, ConfigId: "model", Value: "provider/model-b"}})
			if err != nil || updated.ConfigOptions[0].Select.CurrentValue != "provider/model-b" {
				t.Fatalf("model: %+v %v", updated, err)
			}
			result, err := connection.Prompt(ctx, acp.PromptRequest{SessionId: session.SessionId, Prompt: []acp.ContentBlock{acp.TextBlock("callbacks")}})
			if err != nil || result.StopReason != acp.StopReasonEndTurn {
				t.Fatalf("prompt: %+v %v", result, err)
			}
			host.mutex.Lock()
			defer host.mutex.Unlock()
			if len(host.updates) != 2 || host.updates[0] != "first" || host.updates[1] != "last" || host.approvals != 2 || host.questions != 1 {
				t.Fatalf("callbacks: %+v %d %d", host.updates, host.approvals, host.questions)
			}
			if len(host.plans) != 2 || len(host.plans[1]) != 2 || host.plans[1][0].Status != acp.PlanEntryStatusCompleted || host.toolUpdates != 2 {
				t.Fatal("Cursor notification content or merge state was lost")
			}
		})
	}
}

// TestNativeCancellationAndClose 验证取消可解除阻塞且进程关闭幂等。
func TestNativeCancellationAndClose(t *testing.T) {
	agent, connection, _ := startAgent(t, "config")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := connection.Initialize(ctx, acp.InitializeRequest{ProtocolVersion: 1}); err != nil {
		t.Fatal(err)
	}
	session, err := connection.NewSession(ctx, acp.NewSessionRequest{Cwd: t.TempDir(), McpServers: []acp.McpServer{}})
	if err != nil {
		t.Fatal(err)
	}
	promptCtx, cancelPrompt := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancelPrompt()
	_, err = connection.Prompt(promptCtx, acp.PromptRequest{SessionId: session.SessionId, Prompt: []acp.ContentBlock{acp.TextBlock("wait")}})
	if err == nil || !errors.Is(promptCtx.Err(), context.DeadlineExceeded) {
		t.Fatalf("cancel error: %v", err)
	}
	for range 2 {
		if err := agent.Close(ctx); err != nil {
			t.Fatal(err)
		}
	}
}

// TestNativeStartFailure 验证未安装程序时明确报告启动失败。
func TestNativeStartFailure(t *testing.T) {
	_, err := nativeacp.NewAgent(context.Background(), nativeacp.Config{Command: "/nonexistent/native-acp", Logger: slog.Default()})
	if err == nil {
		t.Fatal("missing binary accepted")
	}
}

// TestACPProcess 在隔离子进程内运行 SDK 驱动的协议对照 Agent。
func TestACPProcess(t *testing.T) {
	variant := os.Getenv("NATIVE_ACP_TEST")
	if variant == "" {
		return
	}
	ready := make(chan struct{})
	var connection *acp.Connection
	options := func(model string) []map[string]any {
		return []map[string]any{{"id": "native_model", "name": "Model", "type": "select", "category": "model", "currentValue": model, "options": []map[string]string{{"value": "provider/model-a", "name": "A"}, {"value": "provider/model-b", "name": "B"}}}, {"id": "thinking", "name": "Thinking", "type": "select", "category": "thought_level", "currentValue": "low", "options": []map[string]string{{"value": "low", "name": "Low"}, {"value": "high", "name": "High"}}}}
	}
	connection = acp.NewConnection(func(ctx context.Context, method string, data json.RawMessage) (any, *acp.RequestError) {
		<-ready
		var request map[string]any
		_ = json.Unmarshal(data, &request)
		switch method {
		case "initialize":
			return map[string]any{"protocolVersion": 1, "agentCapabilities": map[string]any{"loadSession": true, "mcpCapabilities": map[string]any{"http": true, "sse": true}}, "authMethods": []any{}, "agentInfo": map[string]string{"name": "fixture", "version": "1"}}, nil
		case "session/new", "session/load":
			if variant == "mcp" {
				var input acp.NewSessionRequest
				_ = json.Unmarshal(data, &input)
				token := "first-token"
				if method == "session/load" {
					token = "updated-token"
				}
				if len(input.McpServers) != 3 || input.McpServers[0].Stdio == nil || input.McpServers[0].Stdio.Env[0].Value != "!literal ${VAR}" || input.McpServers[1].Http == nil || input.McpServers[1].Http.Headers[0].Value != token || input.McpServers[2].Sse == nil {
					return nil, acp.NewInvalidParams(nil)
				}
			}
			response := map[string]any{"sessionId": "session", "modes": map[string]any{"currentModeId": "default", "availableModes": []map[string]string{{"id": "default", "name": "Default"}}}}
			if variant == "legacy" {
				response["models"] = map[string]any{"currentModelId": "provider/model-a", "availableModels": []map[string]string{{"modelId": "provider/model-a", "name": "A"}, {"modelId": "provider/model-b", "name": "B"}}}
			} else {
				response["configOptions"] = options("provider/model-a")
			}
			return response, nil
		case "session/set_config_option":
			if request["configId"] != "native_model" {
				return nil, acp.NewInvalidParams(nil)
			}
			return map[string]any{"configOptions": options(request["value"].(string))}, nil
		case "session/set_model":
			if request["modelId"] != "provider/model-b" {
				return nil, acp.NewInvalidParams(nil)
			}
			return map[string]any{}, nil
		case "session/prompt":
			var prompt acp.PromptRequest
			_ = json.Unmarshal(data, &prompt)
			if prompt.Prompt[0].Text.Text == "wait" {
				<-ctx.Done()
				return nil, acp.NewInternalError(nil)
			}
			for _, value := range []string{"first", "last"} {
				_ = connection.SendNotification(ctx, "session/update", map[string]any{"sessionId": "session", "update": map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": value}}})
			}
			_, err := acp.SendRequest[json.RawMessage](connection, ctx, "session/request_permission", map[string]any{"sessionId": "session", "toolCall": map[string]any{"toolCallId": "tool", "title": "Approve"}, "options": []map[string]any{{"optionId": "allow", "name": "Allow", "kind": "allow_once"}}})
			if err != nil {
				return nil, acp.NewInternalError(nil)
			}
			response, err := acp.SendRequest[map[string]any](connection, ctx, "cursor/ask_question", map[string]any{"toolCallId": "question", "questions": []map[string]any{{"id": "choice", "prompt": "Choose", "options": []map[string]string{{"id": "a", "label": "A"}}}}})
			expectedAnswer, expectedPlan := "answered", "accepted"
			if variant == "reject" {
				expectedAnswer, expectedPlan = "skipped", "rejected"
			}
			if err != nil || response["outcome"].(map[string]any)["outcome"] != expectedAnswer {
				return nil, acp.NewInternalError(nil)
			}
			_ = connection.SendNotification(ctx, "cursor/update_todos", map[string]any{"toolCallId": "todos", "todos": []map[string]string{{"id": "1", "content": "First", "status": "pending"}}})
			_ = connection.SendNotification(ctx, "cursor/update_todos", map[string]any{"toolCallId": "todos", "merge": true, "todos": []map[string]string{{"id": "1", "content": "First", "status": "completed"}, {"id": "2", "content": "Second", "status": "in_progress"}}})
			_ = connection.SendNotification(ctx, "cursor/task", map[string]any{"toolCallId": "task", "description": "Explore code"})
			_ = connection.SendNotification(ctx, "cursor/generate_image", map[string]any{"toolCallId": "image", "description": "Image", "filePath": "/tmp/image.png"})
			plan, err := acp.SendRequest[map[string]any](connection, ctx, "cursor/create_plan", map[string]any{"toolCallId": "plan", "name": "Plan", "plan": strings.Repeat("Exact plan body\n", 800)})
			if err != nil || plan["outcome"].(map[string]any)["outcome"] != expectedPlan {
				return nil, acp.NewInternalError(nil)
			}
			return map[string]any{"stopReason": "end_turn"}, nil
		case "session/cancel":
			return nil, nil
		default:
			return nil, acp.NewMethodNotFound(method)
		}
	}, os.Stdout, os.Stdin)
	close(ready)
	<-connection.Done()
	os.Exit(0)
}

// TestNativeMCPPreservesTransportAndFreshHeaders 验证三种 MCP 传输与恢复时更新的请求头完整透传。
func TestNativeMCPPreservesTransportAndFreshHeaders(t *testing.T) {
	_, connection, _ := startAgent(t, "mcp")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := connection.Initialize(ctx, acp.InitializeRequest{ProtocolVersion: 1}); err != nil {
		t.Fatal(err)
	}
	servers := []acp.McpServer{
		{Stdio: &acp.McpServerStdio{Name: "stdio", Command: "fixture", Args: []string{}, Env: []acp.EnvVariable{{Name: "TOKEN", Value: "!literal ${VAR}"}}}},
		{Http: &acp.McpServerHttpInline{Name: "http", Url: "http://127.0.0.1/mcp", Headers: []acp.HttpHeader{{Name: "Authorization", Value: "first-token"}}}},
		{Sse: &acp.McpServerSseInline{Name: "sse", Url: "http://127.0.0.1/sse", Headers: []acp.HttpHeader{}}},
	}
	cwd := t.TempDir()
	session, err := connection.NewSession(ctx, acp.NewSessionRequest{Cwd: cwd, McpServers: servers})
	if err != nil {
		t.Fatal(err)
	}
	servers[1].Http.Headers[0].Value = "updated-token"
	if _, err := connection.LoadSession(ctx, acp.LoadSessionRequest{SessionId: session.SessionId, Cwd: cwd, McpServers: servers}); err != nil {
		t.Fatal(err)
	}
}

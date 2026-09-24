package nativeacp_test

import (
	"bytes"
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

	"github.com/JieWaZi/acp-go/pkg/acpmeta"
	"github.com/JieWaZi/acp-go/pkg/acpserver"
	"github.com/JieWaZi/acp-go/pkg/autoreview"
	"github.com/JieWaZi/acp-go/pkg/cursor"
	"github.com/JieWaZi/acp-go/pkg/kimi"
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
func startAgent(t *testing.T, variant string, reviewers ...func(context.Context, autoreview.Request) (autoreview.Decision, error)) (*nativeacp.Agent, *acp.ClientSideConnection, *hostClient) {
	t.Helper()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	config := nativeacp.Config{Command: binary, Args: []string{"-test.run=^TestACPProcess$"}, Environment: append(os.Environ(), "NATIVE_ACP_TEST="+variant), Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), CallbackAdapter: cursor.NewCallbackAdapter()}
	if variant == "legacy" {
		var reviewer func(context.Context, autoreview.Request) (autoreview.Decision, error)
		if len(reviewers) > 0 {
			reviewer = reviewers[0]
		}
		config.SessionAdapter, config.PermissionAdapter = kimi.NewCompatibilityAdapters(reviewer, config.Logger)
	} else {
		config.SessionAdapter = cursor.NewSessionAdapter()
	}
	var agent *nativeacp.Agent
	if variant == "version" {
		var wrapped *cursor.Agent
		wrapped, err = cursor.NewAgent(context.Background(), cursor.Config{CursorPath: binary, PrefixArgs: []string{"-test.run=^TestACPProcess$", "--"}, Environment: config.Environment, Logger: config.Logger})
		if err == nil {
			agent = wrapped.Agent
		}
	} else {
		agent, err = nativeacp.NewAgent(context.Background(), config)
	}
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

// TestNativeStderrIsVisibleRaw 验证原生 ACP 失败诊断保留 CLI 原始内容。
func TestNativeStderrIsVisibleRaw(t *testing.T) {
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	agent, err := nativeacp.NewAgent(context.Background(), nativeacp.Config{
		Command:     binary,
		Args:        []string{"-test.run=^TestACPProcess$"},
		Environment: append(os.Environ(), "NATIVE_ACP_TEST=stderr"),
		Logger:      slog.New(slog.NewTextHandler(&output, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := agent.Close(ctx); err != nil {
		t.Fatal(err)
	}
	diagnostic := output.String()
	if !strings.Contains(diagnostic, "fixture failure") {
		t.Fatalf("stderr 诊断不可见：%q", diagnostic)
	}
	if !strings.Contains(diagnostic, "api_key=secret-value") {
		t.Fatalf("stderr 原始诊断丢失：%q", diagnostic)
	}
}

// TestNativeRequestReportsRawCLIExit 验证请求失败时附加原始 CLI 退出诊断。
func TestNativeRequestReportsRawCLIExit(t *testing.T) {
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	agent, err := nativeacp.NewAgent(context.Background(), nativeacp.Config{
		Command:     binary,
		Args:        []string{"-test.run=^TestACPProcess$"},
		Environment: append(os.Environ(), "NATIVE_ACP_TEST=stderr"),
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, err = agent.Initialize(ctx, acp.InitializeRequest{})
	if err == nil || !strings.Contains(err.Error(), "api_key=secret-value") {
		t.Fatalf("native CLI raw error missing: %v", err)
	}
	_ = agent.Close(ctx)
}

// TestACPProcess 在隔离子进程内运行 SDK 驱动的协议对照 Agent。
func TestACPProcess(t *testing.T) {
	variant := os.Getenv("NATIVE_ACP_TEST")
	if variant == "" {
		return
	}
	if variant == "version" && os.Args[len(os.Args)-1] == "--version" {
		fmt.Println("2026.09.02-c22c1a3")
		os.Exit(0)
	}
	if variant == "parameters" {
		runCursorParameterProcess()
		os.Exit(0)
	}
	if variant == "stderr" {
		fmt.Fprintln(os.Stderr, "fixture failure api_key=secret-value")
		os.Exit(9)
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
			if variant == "version" {
				return map[string]any{"protocolVersion": 1, "agentCapabilities": map[string]any{}}, nil
			}
			return map[string]any{"protocolVersion": 1, "agentCapabilities": map[string]any{"loadSession": true, "mcpCapabilities": map[string]any{"http": true, "sse": true}}, "authMethods": []any{}, "agentInfo": map[string]string{"name": "fixture", "version": "1"}}, nil
		case "session/fork":
			if request["sessionId"] != "session" || request["cwd"] != "/fork-target" {
				return nil, acp.NewInvalidParams(nil)
			}
			return map[string]any{"sessionId": "native-child", "configOptions": options("provider/model-a")}, nil
		case "session/new", "session/load", "session/resume":
			if variant == "empty-mcp" && method != "session/resume" {
				if _, ok := request["mcpServers"].([]any); !ok {
					return nil, acp.NewInvalidParams(map[string]any{"message": "mcpServers must be an array"})
				}
			}
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
			if prompt.Prompt[0].Text.Text == "review-operation" {
				_ = connection.SendNotification(ctx, "session/update", map[string]any{"sessionId": "session", "update": map[string]any{"sessionUpdate": "tool_call", "toolCallId": "reviewed-tool", "title": "Write file", "kind": "edit", "status": "pending", "rawInput": map[string]any{"path": "test.txt", "content": "owned"}}})
				response, err := acp.SendRequest[acp.RequestPermissionResponse](connection, ctx, "session/request_permission", acp.RequestPermissionRequest{SessionId: "session", ToolCall: acp.ToolCallUpdate{ToolCallId: "reviewed-tool"}, Options: []acp.PermissionOption{{OptionId: "once", Name: "Allow once", Kind: acp.PermissionOptionKindAllowOnce}, {OptionId: "always", Name: "Allow session", Kind: acp.PermissionOptionKindAllowAlways}}})
				if err != nil || response.Outcome.Selected == nil || response.Outcome.Selected.OptionId != "once" {
					return nil, acp.NewInvalidParams(nil)
				}
				return map[string]any{"stopReason": "end_turn"}, nil
			}
			if prompt.Prompt[0].Text.Text == "kimi-question" {
				response, err := acp.SendRequest[map[string]any](connection, ctx, "elicitation/create", map[string]any{"sessionId": "session", "toolCallId": "kimi-question", "mode": "form", "message": "Kimi question", "requestedSchema": map[string]any{"type": "object", "properties": map[string]any{"choice": map[string]any{"type": "string"}}, "required": []string{"choice"}}})
				if err != nil || response["action"] != "accept" {
					return nil, acp.NewInvalidParams(map[string]any{"message": "question did not reach the host"})
				}
				return map[string]any{"stopReason": "end_turn"}, nil
			}
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

// TestNativeKimiQuestionRoutesSession 验证 Kimi 顶层会话身份经过 SDK 转发仍能抵达宿主输入端口。
func TestNativeKimiQuestionRoutesSession(t *testing.T) {
	_, connection, host := startAgent(t, "config")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := connection.Initialize(ctx, acp.InitializeRequest{ProtocolVersion: 1}); err != nil {
		t.Fatal(err)
	}
	session, err := connection.NewSession(ctx, acp.NewSessionRequest{Cwd: t.TempDir(), McpServers: []acp.McpServer{}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := connection.Prompt(ctx, acp.PromptRequest{SessionId: session.SessionId, Prompt: []acp.ContentBlock{acp.TextBlock("kimi-question")}}); err != nil {
		t.Fatal(err)
	}
	host.mutex.Lock()
	defer host.mutex.Unlock()
	if host.questions != 1 {
		t.Fatal("question not delivered")
	}
}

// TestLegacyAutoReviewFallsBackToHost 验证真实 ACP 回调携带完整证据，且审查失败不会放行。
func TestLegacyAutoReviewFallsBackToHost(t *testing.T) {
	for _, outcome := range []string{"allow", "deny", "error"} {
		t.Run(outcome, func(t *testing.T) {
			reviewed := false
			reviewer := func(_ context.Context, r autoreview.Request) (autoreview.Decision, error) {
				reviewed = true
				if r.WorkingDirectory == "" || len(r.Prompt) != 1 || r.Prompt[0].Text.Text != "review-operation" || r.Tool.RawInput == nil || r.Model != "provider/model-a" {
					t.Errorf("incomplete evidence: %#v", r)
				}
				if outcome == "error" {
					return autoreview.Decision{}, errors.New("classifier unavailable")
				}
				return autoreview.Decision{Outcome: outcome}, nil
			}
			_, connection, host := startAgent(t, "legacy", reviewer)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if _, err := connection.Initialize(ctx, acp.InitializeRequest{ProtocolVersion: 1}); err != nil {
				t.Fatal(err)
			}
			session, err := connection.NewSession(ctx, acp.NewSessionRequest{Cwd: t.TempDir(), McpServers: []acp.McpServer{}})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := connection.Prompt(ctx, acp.PromptRequest{SessionId: session.SessionId, Prompt: []acp.ContentBlock{acp.TextBlock("review-operation")}}); err != nil {
				t.Fatal(err)
			}
			host.mutex.Lock()
			defer host.mutex.Unlock()
			expected := 1
			if outcome == "allow" {
				expected = 0
			}
			if !reviewed || host.approvals != expected {
				t.Fatalf("reviewed=%v approvals=%d", reviewed, host.approvals)
			}
		})
	}
}

// TestCursorVersionWithoutAgentInfo 验证真实 Cursor 握手缺少信息时仍向宿主返回 CLI 版本。
func TestCursorVersionWithoutAgentInfo(t *testing.T) {
	_, client, _ := startAgent(t, "version")
	response, err := client.Initialize(context.Background(), acp.InitializeRequest{ProtocolVersion: 1})
	if err != nil {
		t.Fatal(err)
	}
	if response.AgentInfo == nil || acpmeta.RuntimeVersion(response.AgentInfo.Meta) != "2026.09.02-c22c1a3" {
		t.Fatalf("Cursor CLI version missing: %+v", response.AgentInfo)
	}
}

// TestNativeEmptyMCPLists 验证未配置 MCP 时，创建和两种恢复请求仍发送上游要求的空数组。
func TestNativeEmptyMCPLists(t *testing.T) {
	agent, _, _ := startAgent(t, "empty-mcp")
	ctx := context.Background()
	cwd := t.TempDir()
	created, err := agent.NewSession(ctx, acp.NewSessionRequest{Cwd: cwd})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = agent.LoadSession(ctx, acp.LoadSessionRequest{SessionId: created.SessionId, Cwd: cwd}); err != nil {
		t.Fatal(err)
	}
	if _, err = agent.ResumeSession(ctx, acp.ResumeSessionRequest{SessionId: created.SessionId, Cwd: cwd}); err != nil {
		t.Fatal(err)
	}
}

// TestNativeForkUsesDedicatedProtocol 验证 Kimi 复用的标准 session/fork 原样传递身份和 cwd。
func TestNativeForkUsesDedicatedProtocol(t *testing.T) {
	_, connection, _ := startAgent(t, "fork")
	if _, err := connection.Initialize(context.Background(), acp.InitializeRequest{ProtocolVersion: 1}); err != nil {
		t.Fatal(err)
	}
	fork, err := connection.UnstableForkSession(context.Background(), acp.UnstableForkSessionRequest{SessionId: "session", Cwd: "/fork-target"})
	if err != nil || fork.SessionId != "native-child" {
		t.Fatalf("%+v %v", fork, err)
	}
	if _, err := connection.UnstableForkSession(context.Background(), acp.UnstableForkSessionRequest{SessionId: "session", Cwd: "/fork-target", Meta: map[string]any{"forkPosition": "older"}}); err == nil {
		t.Fatal("unverified historical position accepted")
	}
}

// TestNativeForkReconcileDoesNotCreate 验证对账请求不会再次执行原生分叉。
func TestNativeForkReconcileDoesNotCreate(t *testing.T) {
	agent := &nativeacp.Agent{}
	_, err := agent.UnstableForkSession(context.Background(), acp.UnstableForkSessionRequest{SessionId: "session", Cwd: t.TempDir(), Meta: map[string]any{"reconcileOnly": true}})
	if !errors.Is(err, acpmeta.ErrForkUnconfirmed) {
		t.Fatalf("reconcile must not create another native fork: %v", err)
	}
}

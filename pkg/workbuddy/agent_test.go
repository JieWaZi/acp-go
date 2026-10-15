package workbuddy

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JieWaZi/acp-go/pkg/acpmeta"
	"github.com/JieWaZi/acp-go/pkg/acpserver"
	acp "github.com/coder/acp-go-sdk"
)

// fixtureHost 记录经过两层标准 ACP 连接交付的事件与问答。
type fixtureHost struct {
	// Client 对未声明的宿主能力保持显式失败。
	acp.Client
	// mutex 保护协议读协程写入的观察结果。
	mutex sync.Mutex
	// updates 保存未经重写的会话通知。
	updates []acp.SessionNotification
	// questionAction 决定测试宿主接纳、拒答或等待取消。
	questionAction string
	// questions 在表单真正进入宿主时通知测试。
	questions chan struct{}
}

// SessionUpdate 保存原始扩展元数据，供协议断言使用。
func (host *fixtureHost) SessionUpdate(_ context.Context, request acp.SessionNotification) error {
	host.mutex.Lock()
	host.updates = append(host.updates, request)
	host.mutex.Unlock()
	return nil
}

// RequestPermission 原样回传上游的一次授权选项。
func (*fixtureHost) RequestPermission(_ context.Context, request acp.RequestPermissionRequest) (acp.RequestPermissionResponse, error) {
	return acp.RequestPermissionResponse{Outcome: acp.NewRequestPermissionOutcomeSelected(request.Options[0].OptionId)}, nil
}

// UnstableCreateElicitation 校验选项、自定义答案和会话关联后返回真实输入。
func (host *fixtureHost) UnstableCreateElicitation(ctx context.Context, request acp.UnstableCreateElicitationRequest) (acp.UnstableCreateElicitationResponse, error) {
	if request.Form == nil || request.Form.Meta["sessionId"] != "workbuddy-session" {
		return acp.NewUnstableCreateElicitationResponseCancel(), nil
	}
	select {
	case host.questions <- struct{}{}:
	default:
	}
	if host.questionAction == "wait" {
		<-ctx.Done()
		return acp.NewUnstableCreateElicitationResponseCancel(), nil
	}
	if host.questionAction == "decline" {
		return acp.NewUnstableCreateElicitationResponseDecline(), nil
	}
	response := acp.NewUnstableCreateElicitationResponseAccept()
	response.Accept.Content = map[string]any{"question_0": "A", "question_1": []string{"B", "C"}, "question_2": "free text"}
	return response, nil
}

// startFixture 以当前测试可执行文件启动真实 ACP 子进程，不读取用户配置或请求模型。
func startFixture(t *testing.T, mode, questionAction string) (*Agent, *acp.ClientSideConnection, *fixtureHost) {
	t.Helper()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	agent, err := NewAgent(context.Background(), Config{
		WorkBuddyPath: binary, PrefixArgs: []string{"-test.run=^TestWorkBuddyProcess$", "--"},
		Environment: []string{"WORKBUDDY_FIXTURE=1"}, WorkingDirectory: t.TempDir(),
		PermissionMode: mode, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	peer, host := connectFixture(t, agent, questionAction)
	return agent, peer, host
}

// connectFixture 为已构造的适配器建立真实的宿主 SDK 双向连接。
func connectFixture(t *testing.T, agent *Agent, questionAction string) (*acp.ClientSideConnection, *fixtureHost) {
	t.Helper()
	host := &fixtureHost{questionAction: questionAction, questions: make(chan struct{}, 1)}
	serverInput, clientOutput := io.Pipe()
	clientInput, serverOutput := io.Pipe()
	server, err := acpserver.NewWithUserInput(agent, serverInput, serverOutput)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = server.Serve(ctx); close(done) }()
	peer := acp.NewClientSideConnection(host, clientOutput, clientInput)
	t.Cleanup(func() {
		cancel()
		_ = clientOutput.Close()
		_ = clientInput.Close()
		_ = serverInput.Close()
		_ = serverOutput.Close()
		closeCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		_ = agent.Close(closeCtx)
		select {
		case <-done:
		case <-closeCtx.Done():
			t.Error("ACP server did not stop")
		}
	})
	return peer, host
}

// initializeFixture 保证原生问答能力仅由标准表单能力派生。
func initializeFixture(t *testing.T, peer *acp.ClientSideConnection) acp.InitializeResponse {
	t.Helper()
	request := acp.InitializeRequest{ProtocolVersion: 1}
	request.ClientCapabilities.Elicitation = &acp.ElicitationCapabilities{Form: &acp.ElicitationFormCapabilities{}}
	response, err := peer.Initialize(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

// TestWorkBuddyStandardContract 覆盖握手、模型、布尔配置、权限、MCP、连续聊天、用量和错误原文。
func TestWorkBuddyStandardContract(t *testing.T) {
	for _, mode := range []string{"default", "auto", "full-access"} {
		t.Run(mode, func(t *testing.T) {
			agent, peer, host := startFixture(t, mode, "accept")
			response := initializeFixture(t, peer)
			if !response.AgentCapabilities.LoadSession || !response.AgentCapabilities.McpCapabilities.Sse || acpmeta.RuntimeVersion(response.AgentInfo.Meta) != "2.151.0" || len(response.AuthMethods) != 1 {
				t.Fatalf("native capabilities or CLI version lost: %+v", response)
			}
			auth, err := peer.Authenticate(context.Background(), acp.AuthenticateRequest{MethodId: "external"})
			if err != nil || auth.Meta["codebuddy.ai/userinfo"] == nil {
				t.Fatalf("authentication metadata lost: %+v %v", auth, err)
			}
			servers := []acp.McpServer{{Http: &acp.McpServerHttpInline{Name: "stable", Url: "http://127.0.0.1:9/mcp", Headers: []acp.HttpHeader{{Name: "Authorization", Value: "fixture"}}}}}
			session, err := peer.NewSession(context.Background(), acp.NewSessionRequest{Cwd: t.TempDir(), McpServers: servers})
			if err != nil || len(session.ConfigOptions) != 3 {
				t.Fatalf("new session: %+v %v", session, err)
			}
			wantMode := map[string]acp.SessionModeId{"default": "default", "auto": "auto", "full-access": "bypassPermissions"}[mode]
			if session.Modes == nil || session.Modes.CurrentModeId != wantMode {
				t.Fatalf("permission acknowledgment did not reflect current policy: %+v", session.Modes)
			}
			config, err := peer.SetSessionConfigOption(context.Background(), acp.SetSessionConfigOptionRequest{ValueId: &acp.SetSessionConfigOptionValueId{SessionId: session.SessionId, ConfigId: "reasoning", Value: "high"}})
			if err != nil || config.ConfigOptions[1].Select.Id != "reasoning" || config.ConfigOptions[1].Select.CurrentValue != "high" {
				t.Fatalf("reasoning mapping: %+v %v", config, err)
			}
			for _, text := range []string{"first", "second", "question", "permission"} {
				result, err := peer.Prompt(context.Background(), acp.PromptRequest{SessionId: session.SessionId, Prompt: []acp.ContentBlock{acp.TextBlock(text)}})
				if err != nil || result.StopReason != acp.StopReasonEndTurn || result.Usage == nil || result.Usage.InputTokens != 7 {
					t.Fatalf("prompt %s: %+v %v", text, result, err)
				}
			}
			select {
			case <-host.questions:
			default:
				t.Fatal("native question bypassed the host")
			}
			_, err = peer.Prompt(context.Background(), acp.PromptRequest{SessionId: session.SessionId, Prompt: []acp.ContentBlock{acp.TextBlock("error")}})
			var nativeError *acp.RequestError
			if !errors.As(err, &nativeError) || nativeError.Code != -32042 || nativeError.Message != "provider quota exceeded" {
				t.Fatalf("native error changed: %v", err)
			}
			if _, err := agent.HandleExtensionMethod(context.Background(), "_session/steering", nil); err == nil {
				t.Fatal("invented steering support")
			}
			host.mutex.Lock()
			defer host.mutex.Unlock()
			found := false
			for _, update := range host.updates {
				if chunk := update.Update.AgentMessageChunk; chunk != nil && chunk.Content.Text.Text == "second" {
					found = chunk.Meta["codebuddy.ai/responseModelId"] == "native/model"
				}
			}
			if !found {
				t.Fatal("second turn or public metadata lost")
			}
		})
	}
}

// TestWorkBuddyRestoreAndCancel 覆盖冷恢复、恢复权限、拒答、待回答取消与未声明方法。
func TestWorkBuddyRestoreAndCancel(t *testing.T) {
	for _, action := range []string{"decline", "wait"} {
		t.Run(action, func(t *testing.T) {
			_, peer, host := startFixture(t, "auto", action)
			initializeFixture(t, peer)
			_, err := peer.LoadSession(context.Background(), acp.LoadSessionRequest{SessionId: "workbuddy-session", Cwd: t.TempDir(), McpServers: []acp.McpServer{}})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			done := make(chan error, 1)
			go func() {
				_, err := peer.Prompt(ctx, acp.PromptRequest{SessionId: "workbuddy-session", Prompt: []acp.ContentBlock{acp.TextBlock("question")}})
				done <- err
			}()
			select {
			case <-host.questions:
			case <-ctx.Done():
				t.Fatal("question never reached host")
			}
			if action == "wait" {
				if err := peer.Cancel(ctx, acp.CancelNotification{SessionId: "workbuddy-session"}); err != nil {
					t.Fatal(err)
				}
			}
			select {
			case <-done:
			case <-ctx.Done():
				t.Fatal("pending question survived cancellation")
			}
			if _, err := peer.ResumeSession(ctx, acp.ResumeSessionRequest{SessionId: "workbuddy-session", Cwd: t.TempDir()}); err == nil {
				t.Fatal("undeclared native resume unexpectedly succeeded")
			}
		})
	}
}

// TestWorkBuddyConfigurationGuards 验证显式失效路径、取消上下文与零厂商元数据的基础握手。
func TestWorkBuddyConfigurationGuards(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	for _, config := range []Config{{Logger: logger, PermissionMode: "invented"}, {Logger: logger, WorkBuddyPath: filepath.Join(t.TempDir(), "missing")}, {}} {
		if agent, err := NewAgent(context.Background(), config); err == nil {
			_ = agent.Close(context.Background())
			t.Fatal("invalid configuration accepted")
		}
	}
	request := acp.InitializeRequest{ProtocolVersion: 1}
	questionAdapter{}.PrepareInitialize(&request)
	if request.ClientCapabilities.Meta != nil {
		t.Fatal("base ACP handshake fabricated metadata")
	}
	_, peer, _ := startFixture(t, "", "accept")
	if _, err := peer.Initialize(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	managed, managedPeer, _ := startFixture(t, "auto", "accept")
	initializeFixture(t, managedPeer)
	session, err := managedPeer.NewSession(context.Background(), acp.NewSessionRequest{Cwd: t.TempDir(), McpServers: []acp.McpServer{}})
	if err != nil {
		t.Fatal(err)
	}
	if err := managed.applyMode(context.Background(), session.SessionId, nil); err == nil {
		t.Fatal("missing permission catalog was accepted")
	}
	if _, err := managedPeer.Prompt(context.Background(), acp.PromptRequest{SessionId: session.SessionId, Prompt: []acp.ContentBlock{acp.TextBlock("must not run")}}); err == nil {
		t.Fatal("failed permission admission left an executable session")
	}
}

// TestWorkBuddyProcess 是实际子进程中的标准 ACP fixture，测试范围不访问任何模型服务。
func TestWorkBuddyProcess(t *testing.T) {
	if os.Getenv("WORKBUDDY_FIXTURE") != "1" {
		return
	}
	if os.Args[len(os.Args)-1] == "--version" {
		_, _ = os.Stdout.WriteString("2.151.0\n")
		os.Exit(0)
	}
	if !strings.Contains(strings.Join(os.Args, " "), "--acp") {
		os.Exit(2)
	}
	ready := make(chan struct{})
	var connection *acp.Connection
	var mutex sync.Mutex
	reasoning := "enabled"
	options := func() []map[string]any {
		return []map[string]any{
			{"type": "select", "id": "model", "name": "Model", "category": "model", "currentValue": "native/model", "options": []map[string]string{{"value": "native/model", "name": "Native"}}},
			{"type": "select", "id": "thought_level", "name": "Thinking", "category": "thought_level", "currentValue": reasoning, "options": []map[string]string{{"value": "enabled", "name": "On"}, {"value": "high", "name": "High"}}},
			{"type": "boolean", "id": "multitask", "name": "Multitask", "currentValue": false},
		}
	}
	connection = acp.NewConnection(func(ctx context.Context, method string, data json.RawMessage) (any, *acp.RequestError) {
		<-ready
		var request map[string]any
		_ = json.Unmarshal(data, &request)
		meta, _ := request["_meta"].(map[string]any)
		for key := range meta {
			if strings.Contains(key, "sessionAdmission") || strings.Contains(key, "Credential") {
				return nil, acp.NewInvalidParams(nil)
			}
		}
		switch method {
		case "initialize":
			return map[string]any{"protocolVersion": 1, "agentCapabilities": map[string]any{"loadSession": true, "mcpCapabilities": map[string]bool{"http": true, "sse": true}, "promptCapabilities": map[string]bool{"image": true}}, "authMethods": []map[string]string{{"id": "external", "name": "Login"}}}, nil
		case "authenticate":
			return map[string]any{"_meta": map[string]any{"codebuddy.ai/userinfo": map[string]string{"userName": "fixture"}}}, nil
		case "session/new", "session/load":
			mutex.Lock()
			defer mutex.Unlock()
			servers, _ := request["mcpServers"].([]any)
			for _, value := range servers {
				server, _ := value.(map[string]any)
				if server["name"] == "acp_go_user_input" {
					return nil, acp.NewInvalidParams(map[string]any{"message": "duplicate question MCP"})
				}
			}
			return map[string]any{"sessionId": "workbuddy-session", "configOptions": options(), "modes": map[string]any{"currentModeId": "default", "availableModes": []map[string]string{{"id": "default", "name": "Default"}, {"id": "auto", "name": "Auto"}, {"id": "bypassPermissions", "name": "Bypass"}}}}, nil
		case "session/set_mode":
			return map[string]any{}, nil
		case "session/set_config_option":
			mutex.Lock()
			defer mutex.Unlock()
			if request["configId"] != "thought_level" {
				return nil, acp.NewInvalidParams(nil)
			}
			reasoning, _ = request["value"].(string)
			return map[string]any{"configOptions": options()}, nil
		case "session/prompt":
			var prompt acp.PromptRequest
			if json.Unmarshal(data, &prompt) != nil || len(prompt.Prompt) != 1 || prompt.Prompt[0].Text == nil {
				return nil, acp.NewInvalidParams(nil)
			}
			text := prompt.Prompt[0].Text.Text
			if text == "error" {
				return nil, &acp.RequestError{Code: -32042, Message: "provider quota exceeded"}
			}
			if text == "question" {
				question := json.RawMessage(`{"sessionId":"workbuddy-session","toolCallId":"question-tool","inputType":"question","schema":{"questions":[{"id":"q_0","question":"Choose one","options":[{"label":"A"}]},{"id":"q_1","question":"Choose several","multiSelect":true,"options":[{"label":"B"},{"label":"C"}]},{"id":"q_2","question":"Explain"}]}}`)
				answer, err := acp.SendRequest[map[string]any](connection, ctx, "_codebuddy.ai/question", question)
				if err != nil {
					return nil, acp.NewInternalError(nil)
				}
				if answer["outcome"] == "submitted" && !reflect.DeepEqual(answer["answers"], map[string]any{"q_0": "A", "q_1": []any{"B", "C"}, "q_2": "free text"}) {
					return nil, acp.NewInvalidParams(nil)
				}
			}
			if text == "permission" {
				permission := acp.RequestPermissionRequest{SessionId: prompt.SessionId, ToolCall: acp.ToolCallUpdate{ToolCallId: "tool"}, Options: []acp.PermissionOption{{OptionId: "once", Name: "Once", Kind: acp.PermissionOptionKindAllowOnce}}}
				answer, err := acp.SendRequest[acp.RequestPermissionResponse](connection, ctx, "session/request_permission", permission)
				if err != nil || answer.Outcome.Selected == nil || answer.Outcome.Selected.OptionId != "once" {
					return nil, acp.NewInternalError(nil)
				}
			}
			_ = connection.SendNotification(ctx, "session/update", acp.SessionNotification{SessionId: prompt.SessionId, Update: acp.SessionUpdate{AgentMessageChunk: &acp.SessionUpdateAgentMessageChunk{Content: acp.TextBlock(text), Meta: map[string]any{"codebuddy.ai/responseModelId": "native/model"}}}})
			return map[string]any{"stopReason": "end_turn", "usage": map[string]any{"inputTokens": 7, "outputTokens": 3, "totalTokens": 10}}, nil
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

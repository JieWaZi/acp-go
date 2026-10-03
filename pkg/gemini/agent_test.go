package gemini

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/JieWaZi/acp-go/pkg/acpmeta"
	acp "github.com/coder/acp-go-sdk"
)

// TestMain 提供只访问临时目录的原生 ACP 子进程夹具。
func TestMain(m *testing.M) {
	if os.Getenv("GEMINI_SESSION_FIXTURE") == "1" {
		os.Exit(sessionFixture())
	}
	os.Exit(m.Run())
}

// sessionFixture 模拟官方启动设置缓存、空历史过滤与同分钟文件碰撞。
func sessionFixture() int {
	if os.Args[len(os.Args)-1] == "--version" {
		fmt.Println("fixture 0.62.0")
		return 0
	}
	profile := filepath.Join(os.Getenv("GEMINI_CLI_HOME"), ".gemini")
	data, _ := os.ReadFile(filepath.Join(profile, "settings.json"))
	settings := map[string]any{}
	_ = json.Unmarshal(data, &settings)
	for index, arg := range os.Args {
		if arg == "--prompt" && index+1 < len(os.Args) {
			tools, _ := settings["tools"].(map[string]any)
			core, _ := tools["core"].([]any)
			if tools == nil || len(core) != 0 {
				return 21
			}
			if os.Getenv("GEMINI_REVIEW_REFRESH") == "1" {
				_ = os.WriteFile(filepath.Join(profile, "oauth_creds.json"), []byte("reviewer refreshed"), 0600)
			}
			model := os.Args[len(os.Args)-1]
			outcome := os.Getenv("GEMINI_REVIEW_OUTCOME")
			if outcome == "" {
				outcome = "allow"
			}
			_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"response": fmt.Sprintf(`{"outcome":%q,"risk_level":"low"}`, outcome), "stats": map[string]any{"models": map[string]any{model: map[string]any{}}}})
			return 0
		}
	}
	model := "gemini-3.8-flash"
	if value, ok := settings["model"].(map[string]any); ok {
		if name, ok := value["name"].(string); ok {
			model = name
		}
	}
	sid := fmt.Sprintf("session-%d", os.Getpid())
	history := ""
	record := ""
	encoder := json.NewEncoder(os.Stdout)
	scanner := bufio.NewScanner(os.Stdin)
	var pending json.RawMessage
	var permissionPending json.RawMessage
	authenticated := false
	for scanner.Scan() {
		var request struct {
			// ID 是调用关联键。
			ID json.RawMessage `json:"id"`
			// Method 是原生方法。
			Method string `json:"method"`
			// Params 是原始参数。
			Params map[string]any `json:"params"`
			// Result 是宿主回调回应。
			Result map[string]any `json:"result"`
		}
		if json.Unmarshal(scanner.Bytes(), &request) != nil {
			return 2
		}
		if request.Method == "" && permissionPending != nil {
			_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": permissionPending, "result": map[string]any{"stopReason": "end_turn", "_meta": map[string]any{"permission": request.Result}}})
			permissionPending = nil
			continue
		}
		params := request.Params
		result := map[string]any{}
		var failure any
		switch request.Method {
		case "initialize":
			result = map[string]any{"protocolVersion": 1, "agentCapabilities": map[string]any{"loadSession": true}, "agentInfo": map[string]any{"name": "gemini", "version": "fixture"}, "authMethods": []any{}}
		case "authenticate":
			authenticated = true
		case "session/new", "session/load":
			if request.Method == "session/load" && os.Getenv("GEMINI_STALL_LOAD") != "" {
				path := os.Getenv("GEMINI_STALL_LOAD")
				if _, err := os.Stat(path); err == nil {
					_ = os.WriteFile(path+".started", []byte("started"), 0600)
					time.Sleep(10 * time.Second)
				}
			}
			if os.Getenv("GEMINI_REQUIRE_AUTH") == "1" && !authenticated {
				failure = map[string]any{"code": -32000, "message": "authentication required"}
				break
			}
			if request.Method == "session/load" {
				sid, _ = params["sessionId"].(string)
			}
			dir := filepath.Join(profile, "tmp", "project", "chats")
			_ = os.MkdirAll(dir, 0700)
			record = filepath.Join(dir, "session-current-"+sid+".jsonl")
			metadata := fmt.Sprintf("{\"sessionId\":%q,\"projectHash\":\"fixture\",\"lastUpdated\":\"2026-10-03T00:00:00Z\"}\n{\"$set\":{\"messages\":[]}}\n", sid)
			file, _ := os.OpenFile(record, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
			_, _ = file.WriteString(metadata)
			_ = file.Close()
			if request.Method == "session/load" {
				history = ""
				entries, _ := os.ReadDir(dir)
				for _, entry := range entries {
					body, _ := os.ReadFile(filepath.Join(dir, entry.Name()))
					if strings.Contains(string(body), fmt.Sprintf("\"sessionId\":%q", sid)) && !strings.HasSuffix(string(body), "{\"$set\":{\"messages\":[]}}\n") {
						history = string(body)
						record = filepath.Join(dir, entry.Name())
					}
				}
				if history == "" {
					failure = map[string]any{"code": -32602, "message": "No previous sessions found"}
				}
			}
			result = map[string]any{"sessionId": sid, "models": map[string]any{"currentModelId": model, "availableModels": []any{map[string]any{"modelId": "gemini-3.8-flash", "name": "Gemini"}, map[string]any{"modelId": "unverified-model", "name": "Unknown"}}}}
		case "session/set_model":
			model, _ = params["modelId"].(string)
			if model == "fail-model" {
				failure = map[string]any{"code": -32602, "message": "model rejected"}
			}
		case "session/set_mode":
			result["modeId"] = params["modeId"]
		case "_capture":
			result = map[string]any{"pid": os.Getpid(), "sessionId": sid, "model": model, "settings": settings, "history": history, "profile": profile, "args": os.Args[1:], "parent": os.Getenv("PARENT_SECRET")}
		case "session/prompt":
			blocks, _ := params["prompt"].([]any)
			text := blocks[0].(map[string]any)["text"].(string)
			if text == "permission" {
				permissionPending = request.ID
				_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": map[string]any{"sessionId": sid, "update": map[string]any{"sessionUpdate": "tool_call", "toolCallId": "tool", "title": "Read", "kind": "read", "status": "pending", "rawInput": map[string]any{"file": "fixture"}}}})
				_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": "approval", "method": "session/request_permission", "params": map[string]any{"sessionId": sid, "toolCall": map[string]any{"toolCallId": "tool", "rawInput": map[string]any{"file": "fixture"}}, "options": []any{map[string]any{"optionId": "always", "name": "Always", "kind": "allow_always"}, map[string]any{"optionId": "once", "name": "Once", "kind": "allow_once"}, map[string]any{"optionId": "reject", "name": "Reject", "kind": "reject_once"}}}})
				continue
			}
			if text == "crash" {
				return 19
			}
			if text == "wait" {
				pending = request.ID
				continue
			}
			history += text
			line, _ := json.Marshal(map[string]any{"id": fmt.Sprintf("msg-%d", time.Now().UnixNano()), "type": "user", "content": text})
			file, _ := os.OpenFile(record, os.O_APPEND|os.O_WRONLY, 0600)
			_, _ = file.Write(append(line, '\n'))
			_ = file.Close()
			_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": map[string]any{"sessionId": sid, "update": map[string]any{"sessionUpdate": "agent_thought_chunk", "content": map[string]any{"type": "text", "text": text}}}})
			if text == "partial-wait" {
				pending = request.ID
				continue
			}
			if text == "partial-error" {
				failure = map[string]any{"code": -32000, "message": "failed after recording user input"}
			}
			result = map[string]any{"stopReason": "end_turn", "_meta": map[string]any{"quota": map[string]any{"token_count": map[string]any{"input_tokens": 10, "output_tokens": 5}}}}
		case "session/cancel":
			if pending != nil {
				_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": pending, "result": map[string]any{"stopReason": "cancelled"}})
				pending = nil
			}
			continue
		default:
			failure = map[string]any{"code": -32601, "message": "Method not found"}
		}
		response := map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result}
		if failure != nil {
			delete(response, "result")
			response["error"] = failure
		}
		if err := encoder.Encode(response); err != nil {
			return 3
		}
	}
	return 0
}

// sessionClient 收集真实 SDK 转发的公共会话更新。
type sessionClient struct {
	// Client 保留未使用的接口方法。
	acp.Client
	// updates 接收映射后的通知。
	updates chan acp.SessionNotification
	// permissions 统计真正宿主审批次数。
	permissions atomic.Int32
}

// SessionUpdate 保存可验证的通知。
func (client *sessionClient) SessionUpdate(_ context.Context, update acp.SessionNotification) error {
	client.updates <- update
	return nil
}

// startSessionAgent 启动独立进程与真实 SDK 宿主。
func startSessionAgent(t *testing.T, config Config) (*Agent, *sessionClient) {
	t.Helper()
	binary, _ := os.Executable()
	config.GeminiPath = binary
	config.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	config.Environment = append(config.Environment, "GEMINI_SESSION_FIXTURE=1")
	if config.WorkingDirectory == "" {
		config.WorkingDirectory = t.TempDir()
	}
	agent, err := NewAgent(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	input, output := io.Pipe()
	hostInput, hostOutput := io.Pipe()
	connection := acp.NewAgentSideConnection(agent, output, hostInput)
	agent.SetAgentConnection(connection)
	client := &sessionClient{updates: make(chan acp.SessionNotification, 100)}
	_ = acp.NewClientSideConnection(client, hostOutput, input)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = agent.Close(ctx)
		_ = input.Close()
		_ = output.Close()
		_ = hostInput.Close()
		_ = hostOutput.Close()
	})
	if _, err := agent.Initialize(context.Background(), acp.InitializeRequest{ProtocolVersion: 1}); err != nil {
		t.Fatal(err)
	}
	return agent, client
}

// newFixtureSession 创建空的公共会话。
func newFixtureSession(t *testing.T, agent *Agent) acp.NewSessionResponse {
	t.Helper()
	response, err := agent.NewSession(context.Background(), acp.NewSessionRequest{Cwd: t.TempDir(), McpServers: []acp.McpServer{}})
	if err != nil {
		t.Fatal(err)
	}
	return response
}

// captureSession 读取指定会话的真实子进程状态。
func captureSession(t *testing.T, agent *Agent, id acp.SessionId) map[string]any {
	t.Helper()
	raw, err := agent.CallNative(context.Background(), "_capture", map[string]any{"sessionId": id})
	if err != nil {
		t.Fatal(err)
	}
	result := map[string]any{}
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

// configureSession 使用公开标准配置入口。
func configureSession(t *testing.T, agent *Agent, id acp.SessionId, key, value string) acp.SetSessionConfigOptionResponse {
	t.Helper()
	response, err := agent.SetSessionConfigOption(context.Background(), acp.SetSessionConfigOptionRequest{ValueId: &acp.SetSessionConfigOptionValueId{SessionId: id, ConfigId: acp.SessionConfigId(key), Value: acp.SessionConfigValueId(value)}})
	if err != nil {
		t.Fatal(err)
	}
	return response
}

// TestSessionChildrenAndThinkingReplacement 验证隔离、模型元数据、空会话稳定身份及失败原子性。
func TestSessionChildrenAndThinkingReplacement(t *testing.T) {
	agent, client := startSessionAgent(t, Config{Environment: []string{"GEMINI_CLI_HOME=" + t.TempDir()}})
	first, second := newFixtureSession(t, agent), newFixtureSession(t, agent)
	before, other := captureSession(t, agent, first.SessionId), captureSession(t, agent, second.SessionId)
	if before["pid"] == other["pid"] {
		t.Fatal("public sessions share one native process")
	}
	options, known := acpmeta.ModelConfigOptions((*first.ConfigOptions[0].Select.Options.Ungrouped)[0].Meta)
	if !known || len(options) != 1 {
		t.Fatal("actual catalog model lacks exact thinking metadata")
	}
	changed := configureSession(t, agent, first.SessionId, "reasoning", "low")
	if len(changed.ConfigOptions) != 2 || changed.ConfigOptions[1].Select.CurrentValue != "low" {
		t.Fatalf("missing reasoning: %+v", changed)
	}
	after := captureSession(t, agent, first.SessionId)
	if before["pid"] == after["pid"] || after["pid"] == other["pid"] {
		t.Fatal("configuration did not replace dedicated child")
	}
	if captureSession(t, agent, second.SessionId)["pid"] != other["pid"] {
		t.Fatal("other session was replaced")
	}
	_, err := agent.Prompt(context.Background(), acp.PromptRequest{SessionId: first.SessionId, Prompt: []acp.ContentBlock{acp.TextBlock("hello")}})
	if err != nil {
		t.Fatal(err)
	}
	if update := <-client.updates; update.SessionId != first.SessionId {
		t.Fatalf("callback uses native identity: %+v", update)
	}
	_, err = agent.SetSessionConfigOption(context.Background(), acp.SetSessionConfigOptionRequest{ValueId: &acp.SetSessionConfigOptionValueId{SessionId: first.SessionId, ConfigId: "model", Value: "fail-model"}})
	if err == nil {
		t.Fatal("failed replacement succeeded")
	}
	if captureSession(t, agent, first.SessionId)["pid"] != after["pid"] {
		t.Fatal("failed replacement destroyed original child")
	}
}

// TestHistoryReplacementAndRestart 验证同分钟原始历史恢复及跨适配器启动的稳定身份。
func TestHistoryReplacementAndRestart(t *testing.T) {
	config := Config{Environment: []string{"GEMINI_CLI_HOME=" + t.TempDir()}}
	agent, client := startSessionAgent(t, config)
	session := newFixtureSession(t, agent)
	configureSession(t, agent, session.SessionId, "reasoning", "low")
	for _, text := range []string{"first turn", "second turn"} {
		if _, err := agent.Prompt(context.Background(), acp.PromptRequest{SessionId: session.SessionId, Prompt: []acp.ContentBlock{acp.TextBlock(text)}}); err != nil {
			t.Fatal(err)
		}
		<-client.updates
		configureSession(t, agent, session.SessionId, "reasoning", "medium")
	}
	capture := captureSession(t, agent, session.SessionId)
	if !strings.Contains(capture["history"].(string), "first turn") || !strings.Contains(capture["history"].(string), "second turn") {
		t.Fatalf("lost genuine history: %v", capture["history"])
	}
	if err := agent.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	next, _ := startSessionAgent(t, config)
	if _, err := next.ResumeSession(context.Background(), acp.ResumeSessionRequest{SessionId: session.SessionId, Cwd: t.TempDir(), McpServers: []acp.McpServer{}}); err != nil {
		t.Fatal(err)
	}
	restored := captureSession(t, next, session.SessionId)
	if !strings.Contains(restored["history"].(string), "second turn") {
		t.Fatal("restart lost persisted mapping/history")
	}
}

// TestBusyConfigurationAndClose 验证忙碌修改不取消执行，关闭释放所有等待请求。
func TestBusyConfigurationAndClose(t *testing.T) {
	agent, _ := startSessionAgent(t, Config{Environment: []string{"GEMINI_CLI_HOME=" + t.TempDir()}})
	session := newFixtureSession(t, agent)
	var group sync.WaitGroup
	for range 3 {
		group.Add(1)
		go func() {
			defer group.Done()
			_, _ = agent.Prompt(context.Background(), acp.PromptRequest{SessionId: session.SessionId, Prompt: []acp.ContentBlock{acp.TextBlock("wait")}})
		}()
	}
	time.Sleep(100 * time.Millisecond)
	_, err := agent.SetSessionConfigOption(context.Background(), acp.SetSessionConfigOptionRequest{ValueId: &acp.SetSessionConfigOptionValueId{SessionId: session.SessionId, ConfigId: "model", Value: "gemini-3.8-flash"}})
	if err == nil {
		t.Fatal("busy setter accepted")
	}
	if _, err := agent.CloseSession(context.Background(), acp.CloseSessionRequest{SessionId: session.SessionId}); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { group.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("close leaked queued prompts")
	}
}

// TestCrashReleasesOnlyOwnedOverlay 验证崩溃及时回收临时配置，其他会话仍可执行。
func TestCrashReleasesOnlyOwnedOverlay(t *testing.T) {
	agent, client := startSessionAgent(t, Config{Environment: []string{"GEMINI_CLI_HOME=" + t.TempDir()}})
	first, second := newFixtureSession(t, agent), newFixtureSession(t, agent)
	crashed := captureSession(t, agent, first.SessionId)
	other := captureSession(t, agent, second.SessionId)
	if _, err := agent.Prompt(context.Background(), acp.PromptRequest{SessionId: first.SessionId, Prompt: []acp.ContentBlock{acp.TextBlock("crash")}}); err == nil {
		t.Fatal("crash returned success")
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(crashed["profile"].(string)); os.IsNotExist(err) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := os.Stat(crashed["profile"].(string)); !os.IsNotExist(err) {
		t.Fatal("crashed child overlay retained")
	}
	if _, err := os.Stat(other["profile"].(string)); err != nil {
		t.Fatal("live session overlay removed")
	}
	if _, err := agent.Prompt(context.Background(), acp.PromptRequest{SessionId: second.SessionId, Prompt: []acp.ContentBlock{acp.TextBlock("still alive")}}); err != nil {
		t.Fatal(err)
	}
	if update := <-client.updates; update.SessionId != second.SessionId {
		t.Fatal("wrong live session")
	}
}

// RequestPermission 记录实际人工回退并明确拒绝一次。
func (client *sessionClient) RequestPermission(_ context.Context, request acp.RequestPermissionRequest) (acp.RequestPermissionResponse, error) {
	client.permissions.Add(1)
	return acp.RequestPermissionResponse{Outcome: acp.NewRequestPermissionOutcomeSelected("reject")}, nil
}

// TestAutoReviewAndManualFallback 验证官方无工具入口、一次批准、审计和严格失败回退。
func TestAutoReviewAndManualFallback(t *testing.T) {
	for _, outcome := range []string{"allow", "deny", "invalid"} {
		t.Run(outcome, func(t *testing.T) {
			agent, client := startSessionAgent(t, Config{PermissionMode: "auto", Environment: []string{"GEMINI_CLI_HOME=" + t.TempDir(), "GEMINI_REVIEW_OUTCOME=" + outcome}})
			session := newFixtureSession(t, agent)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			response, err := agent.Prompt(ctx, acp.PromptRequest{SessionId: session.SessionId, Prompt: []acp.ContentBlock{acp.TextBlock("permission")}})
			if err != nil {
				t.Fatal(err)
			}
			permission := response.Meta["permission"].(map[string]any)
			selected := permission["outcome"].(map[string]any)["optionId"]
			expected := "reject"
			if outcome == "allow" {
				expected = "once"
			}
			if selected != expected {
				t.Fatalf("selected %v want %s", selected, expected)
			}
			if (client.permissions.Load() == 0) != (outcome == "allow") {
				t.Fatal("manual fallback count wrong")
			}
			audited := false
			for len(client.updates) > 0 {
				update := <-client.updates
				if update.SessionId != session.SessionId {
					t.Fatal("review callback leaked native ID")
				}
				if update.Update.ToolCallUpdate != nil && update.Update.ToolCallUpdate.Meta["acp-go/permission-review"] != nil {
					audited = true
				}
			}
			if !audited {
				t.Fatal("review missing audit")
			}
		})
	}
}

// TestExplicitRestrictedReviewFallsBack 验证宿主明确禁用信任时不能用私有目录重新批准。
func TestExplicitRestrictedReviewFallsBack(t *testing.T) {
	for _, policy := range []string{"GEMINI_CLI_TRUST_WORKSPACE=false", "GEMINI_RESTRICTED_MODE=true"} {
		t.Run(policy, func(t *testing.T) {
			agent, client := startSessionAgent(t, Config{PermissionMode: "auto", Environment: []string{"GEMINI_CLI_HOME=" + t.TempDir(), policy}})
			session := newFixtureSession(t, agent)
			response, err := agent.Prompt(context.Background(), acp.PromptRequest{SessionId: session.SessionId, Prompt: []acp.ContentBlock{acp.TextBlock("permission")}})
			if err != nil {
				t.Fatal(err)
			}
			if response.Meta["permission"].(map[string]any)["outcome"].(map[string]any)["optionId"] != "reject" || client.permissions.Load() != 1 {
				t.Fatal("explicit restricted policy still auto-approved")
			}
		})
	}
}

// TestHigherPriorityThinkingRejectsWithoutSwap 验证工作区模型覆盖导致明确错误，原执行代保持可用。
func TestHigherPriorityThinkingRejectsWithoutSwap(t *testing.T) {
	agent, _ := startSessionAgent(t, Config{Environment: []string{"GEMINI_CLI_HOME=" + t.TempDir()}})
	work := t.TempDir()
	if err := os.Mkdir(filepath.Join(work, ".gemini"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, ".gemini", "settings.json"), []byte(`{"modelConfigs":{"customOverrides":[]}}`), 0600); err != nil {
		t.Fatal(err)
	}
	session, err := agent.NewSession(context.Background(), acp.NewSessionRequest{Cwd: work, McpServers: []acp.McpServer{}})
	if err != nil {
		t.Fatal(err)
	}
	before := captureSession(t, agent, session.SessionId)
	_, err = agent.SetSessionConfigOption(context.Background(), acp.SetSessionConfigOptionRequest{ValueId: &acp.SetSessionConfigOptionValueId{SessionId: session.SessionId, ConfigId: "reasoning", Value: "low"}})
	if err == nil || !strings.Contains(err.Error(), "higher-priority") {
		t.Fatalf("ineffective thinking selection accepted: %v", err)
	}
	if captureSession(t, agent, session.SessionId)["pid"] != before["pid"] {
		t.Fatal("rejected override replaced live child")
	}
}

// TestCancelFIFOAndContext 验证取消排队代后，新轮次仍可执行。
func TestCancelFIFOAndContext(t *testing.T) {
	agent, client := startSessionAgent(t, Config{Environment: []string{"GEMINI_CLI_HOME=" + t.TempDir()}})
	session := newFixtureSession(t, agent)
	results := make(chan error, 3)
	for range 3 {
		go func() {
			_, err := agent.Prompt(context.Background(), acp.PromptRequest{SessionId: session.SessionId, Prompt: []acp.ContentBlock{acp.TextBlock("wait")}})
			results <- err
		}()
	}
	owned, err := agent.findSession(session.SessionId)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		owned.mutex.Lock()
		pending := owned.pending
		owned.mutex.Unlock()
		if pending == 3 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if err := agent.Cancel(context.Background(), acp.CancelNotification{SessionId: session.SessionId}); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		select {
		case <-results:
		case <-time.After(3 * time.Second):
			t.Fatal("cancel left queued prompt blocked")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := agent.Prompt(ctx, acp.PromptRequest{SessionId: session.SessionId, Prompt: []acp.ContentBlock{acp.TextBlock("canceled")}}); err == nil {
		t.Fatal("canceled context succeeded")
	}
	if _, err := agent.Prompt(context.Background(), acp.PromptRequest{SessionId: session.SessionId, Prompt: []acp.ContentBlock{acp.TextBlock("next generation")}}); err != nil {
		t.Fatal(err)
	}
	if update := <-client.updates; update.SessionId != session.SessionId {
		t.Fatal("new generation callback identity changed")
	}
}

// TestExplicitAuthenticationReachesSessionChildren 验证标准认证参数会应用到后来创建的独立进程。
func TestExplicitAuthenticationReachesSessionChildren(t *testing.T) {
	agent, _ := startSessionAgent(t, Config{Environment: []string{"GEMINI_CLI_HOME=" + t.TempDir(), "GEMINI_REQUIRE_AUTH=1"}})
	if _, err := agent.Authenticate(context.Background(), acp.AuthenticateRequest{MethodId: "fixture"}); err != nil {
		t.Fatal(err)
	}
	session := newFixtureSession(t, agent)
	configureSession(t, agent, session.SessionId, "reasoning", "low")
}

// TestCloseDuringReplacementReapsPreparedChild 验证关闭与初始化竞争时不会泄漏待提交进程。
func TestCloseDuringReplacementReapsPreparedChild(t *testing.T) {
	for _, whole := range []bool{false, true} {
		t.Run(fmt.Sprint(whole), func(t *testing.T) {
			stall := filepath.Join(t.TempDir(), "stall")
			agent, client := startSessionAgent(t, Config{Environment: []string{"GEMINI_CLI_HOME=" + t.TempDir(), "GEMINI_STALL_LOAD=" + stall}})
			session := newFixtureSession(t, agent)
			if _, err := agent.Prompt(context.Background(), acp.PromptRequest{SessionId: session.SessionId, Prompt: []acp.ContentBlock{acp.TextBlock("persistent history")}}); err != nil {
				t.Fatal(err)
			}
			<-client.updates
			if err := os.WriteFile(stall, []byte("stall"), 0600); err != nil {
				t.Fatal(err)
			}
			changed := make(chan error, 1)
			go func() {
				_, err := agent.SetSessionConfigOption(context.Background(), acp.SetSessionConfigOptionRequest{ValueId: &acp.SetSessionConfigOptionValueId{SessionId: session.SessionId, ConfigId: "reasoning", Value: "low"}})
				changed <- err
			}()
			deadline := time.Now().Add(3 * time.Second)
			for time.Now().Before(deadline) {
				if _, err := os.Stat(stall + ".started"); err == nil {
					break
				}
				time.Sleep(time.Millisecond)
			}
			if _, err := os.Stat(stall + ".started"); err != nil {
				t.Fatal("replacement did not reach native load")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			if whole {
				if err := agent.Close(ctx); err != nil {
					t.Fatal(err)
				}
			} else {
				if _, err := agent.CloseSession(ctx, acp.CloseSessionRequest{SessionId: session.SessionId}); err != nil {
					t.Fatal(err)
				}
			}
			select {
			case err := <-changed:
				if err == nil {
					t.Fatal("closed replacement committed")
				}
			case <-ctx.Done():
				t.Fatal("replacement remained blocked")
			}
			agent.mutex.Lock()
			children := len(agent.children)
			agent.mutex.Unlock()
			expected := 1
			if whole {
				expected = 0
			}
			if children != expected {
				t.Fatalf("child leak: %d want %d", children, expected)
			}
		})
	}
}

// TestReviewerCredentialRefreshPersists 验证临时无工具审查进程的认证刷新也不会随清理丢失。
func TestReviewerCredentialRefreshPersists(t *testing.T) {
	home := t.TempDir()
	if err := os.Mkdir(filepath.Join(home, ".gemini"), 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, ".gemini", "oauth_creds.json")
	if err := os.WriteFile(path, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	agent, _ := startSessionAgent(t, Config{PermissionMode: "auto", Environment: []string{"GEMINI_CLI_HOME=" + home, "GEMINI_REVIEW_REFRESH=1"}})
	session := newFixtureSession(t, agent)
	if _, err := agent.Prompt(context.Background(), acp.PromptRequest{SessionId: session.SessionId, Prompt: []acp.ContentBlock{acp.TextBlock("permission")}}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "reviewer refreshed" {
		t.Fatalf("reviewer refresh lost: %s %v", data, err)
	}
}

// TestFullAccessAndImplementedCapabilities 验证 full-access 使用原生 yolo，并声明真正拥有的关闭与恢复。
func TestFullAccessAndImplementedCapabilities(t *testing.T) {
	agent, _ := startSessionAgent(t, Config{PermissionMode: "full-access", Environment: []string{"GEMINI_CLI_HOME=" + t.TempDir()}})
	response, err := agent.Initialize(context.Background(), acp.InitializeRequest{ProtocolVersion: 1})
	if err != nil {
		t.Fatal(err)
	}
	if response.AgentCapabilities.SessionCapabilities.Close == nil || response.AgentCapabilities.SessionCapabilities.Resume == nil {
		t.Fatal("owned lifecycle capability absent")
	}
	session := newFixtureSession(t, agent)
	capture := captureSession(t, agent, session.SessionId)
	args := capture["args"].([]any)
	if len(args) < 2 || args[len(args)-2] != "--approval-mode" || args[len(args)-1] != "yolo" {
		t.Fatalf("wrong full-access args: %v", args)
	}
}

// TestCanceledCloseStillReapsBeforeCleanup 验证调用方取消关闭等待时也不会先删除仍被进程使用的配置。
func TestCanceledCloseStillReapsBeforeCleanup(t *testing.T) {
	agent, _ := startSessionAgent(t, Config{Environment: []string{"GEMINI_CLI_HOME=" + t.TempDir()}})
	session := newFixtureSession(t, agent)
	owned, err := agent.findSession(session.SessionId)
	if err != nil {
		t.Fatal(err)
	}
	child := owned.child
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _ = agent.CloseSession(ctx, acp.CloseSessionRequest{SessionId: session.SessionId})
	select {
	case <-child.agent.Done():
	default:
		t.Fatal("close cleaned configuration before process reap")
	}
	if _, err := os.Stat(child.directory); !os.IsNotExist(err) {
		t.Fatalf("owned configuration retained: %v", err)
	}
}

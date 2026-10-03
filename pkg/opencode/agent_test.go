package opencode_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/JieWaZi/acp-go/internal/buildinfo"
	"github.com/JieWaZi/acp-go/pkg/acpmeta"
	"github.com/JieWaZi/acp-go/pkg/gemini"
	"github.com/JieWaZi/acp-go/pkg/grok"
	"github.com/JieWaZi/acp-go/pkg/nativeacp"
	"github.com/JieWaZi/acp-go/pkg/opencode"
	acp "github.com/coder/acp-go-sdk"
)

// TestMain 在子进程内运行无账号的原生协议夹具。
func TestMain(m *testing.M) {
	if os.Getenv("ACP_ADAPTER_HELPER") != "" {
		os.Exit(runFixture())
	}
	os.Exit(m.Run())
}

// adapterAgent 描述集成测试需要的原生连接能力。
type adapterAgent interface {
	// Agent 保留标准 ACP 方法。
	acp.Agent
	// SetAgentConnection 注入真实 SDK 宿主连接。
	SetAgentConnection(*acp.AgentSideConnection)
	// CallNative 保留原始扩展调用。
	CallNative(context.Context, string, any) (json.RawMessage, error)
	// Close 回收原生进程。
	Close(context.Context) error
}

// fixtureClient 保存宿主收到的有序思考流。
type fixtureClient struct {
	// Client 保留测试未声明的能力。
	acp.Client
	// updates 是收到的通知队列。
	updates chan acp.SessionNotification
	// permissions 是回退宿主审批的次数。
	permissions atomic.Int32
}

// SessionUpdate 把真实 SDK 通知交给测试。
func (c *fixtureClient) SessionUpdate(_ context.Context, r acp.SessionNotification) error {
	c.updates <- r
	return nil
}

// RequestPermission 在未被自动审查批准时记录真正宿主回退。
func (c *fixtureClient) RequestPermission(_ context.Context, r acp.RequestPermissionRequest) (acp.RequestPermissionResponse, error) {
	c.permissions.Add(1)
	return acp.RequestPermissionResponse{Outcome: acp.NewRequestPermissionOutcomeSelected("reject")}, nil
}

// startAdapter 启动指定公开 Adapter 并连接真正的 SDK。
func startAdapter(t *testing.T, name string, modes ...string) (adapterAgent, *acp.ClientSideConnection, *fixtureClient) {
	t.Helper()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	args := []string{"native-prefix"}
	environment := []string{"ACP_ADAPTER_HELPER=" + name, "INSTANCE_TOKEN=literal-key", "XDG_DATA_HOME=/account/data", "XDG_CONFIG_HOME=/account/config", "XDG_CACHE_HOME=/account/cache", "XDG_STATE_HOME=/account/state", "GEMINI_CLI_HOME=" + t.TempDir(), "GROK_HOME=/account/grok"}
	mode := ""
	if len(modes) > 0 {
		mode = modes[0]
		environment = append(environment, "OPENCODE_REVIEW_HELPER=true")
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	var agent adapterAgent
	switch name {
	case "opencode":
		agent, err = opencode.NewAgent(context.Background(), opencode.Config{OpenCodePath: binary, PrefixArgs: args, Environment: environment, Logger: logger, PermissionMode: mode})
	case "gemini":
		agent, err = gemini.NewAgent(context.Background(), gemini.Config{GeminiPath: binary, PrefixArgs: args, Environment: environment, Logger: logger})
	case "grok":
		agent, err = grok.NewAgent(context.Background(), grok.Config{GrokPath: binary, PrefixArgs: args, Environment: environment, Logger: logger})
	}
	if err != nil {
		t.Fatal(err)
	}
	args[0] = "mutated"
	environment[0] = "mutated"
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	outer := acp.NewAgentSideConnection(agent, outW, inR)
	outer.SetLogger(logger)
	agent.SetAgentConnection(outer)
	host := &fixtureClient{updates: make(chan acp.SessionNotification, 32)}
	peer := acp.NewClientSideConnection(host, inW, outR)
	peer.SetLogger(logger)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := agent.Close(ctx); err != nil {
			t.Error(err)
		}
		_ = inR.Close()
		_ = inW.Close()
		_ = outR.Close()
		_ = outW.Close()
	})
	return agent, peer, host
}

// TestPublicAdaptersPreserveNativeConfiguration 覆盖三个公开入口、模型/思考发现、更新与独立环境。
func TestPublicAdaptersPreserveNativeConfiguration(t *testing.T) {
	t.Setenv("PARENT_SECRET", "do-not-inherit")
	for _, name := range []string{"opencode", "gemini", "grok"} {
		t.Run(name, func(t *testing.T) {
			agent, peer, host := startAdapter(t, name)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			init, err := peer.Initialize(ctx, acp.InitializeRequest{ProtocolVersion: 1})
			if err != nil {
				t.Fatal(err)
			}
			if init.AgentInfo.Version != buildinfo.Current() || acpmeta.RuntimeVersion(init.AgentInfo.Meta) != "fixture-cli 1.2.3" {
				t.Fatalf("adapter/runtime versions: %+v", init.AgentInfo)
			}
			raw, err := agent.CallNative(ctx, "_capture", nil)
			if err != nil {
				t.Fatal(err)
			}
			var capture map[string]any
			if err := json.Unmarshal(raw, &capture); err != nil {
				t.Fatal(err)
			}
			suffix := []any{"acp"}
			if name == "gemini" {
				suffix = []any{"--acp"}
			}
			if name == "grok" {
				suffix = []any{"agent", "stdio"}
			}
			expected := append([]any{"native-prefix"}, suffix...)
			if !reflect.DeepEqual(capture["args"], expected) || capture["parent"] != "" || capture["token"] != "literal-key" {
				t.Fatalf("process snapshot: %s", raw)
			}
			for _, key := range []string{"XDG_DATA_HOME", "XDG_CONFIG_HOME", "XDG_CACHE_HOME", "XDG_STATE_HOME", "GEMINI_CLI_HOME", "GROK_HOME"} {
				if capture[key] == "" {
					t.Fatalf("lost isolation %s", key)
				}
			}
			session, err := peer.NewSession(ctx, acp.NewSessionRequest{Cwd: t.TempDir(), McpServers: []acp.McpServer{}})
			if err != nil {
				t.Fatal(err)
			}
			if len(session.ConfigOptions) == 0 || session.ConfigOptions[0].Select.Id != "model" || name != "gemini" && (len(session.ConfigOptions) < 2 || session.ConfigOptions[1].Select.Id != "reasoning") {
				t.Fatalf("normalized options: %+v", session.ConfigOptions)
			}

			if name != "gemini" {
				selection := "high"
				response, err := peer.SetSessionConfigOption(ctx, acp.SetSessionConfigOptionRequest{ValueId: &acp.SetSessionConfigOptionValueId{SessionId: session.SessionId, ConfigId: "reasoning", Value: acp.SessionConfigValueId(selection)}})
				if err != nil || response.ConfigOptions[1].Select.CurrentValue != "high" {
					t.Fatalf("reasoning write: %+v %v", response, err)
				}
			}
			if _, err := peer.Prompt(ctx, acp.PromptRequest{SessionId: session.SessionId, Prompt: []acp.ContentBlock{acp.TextBlock("hello")}}); err != nil {
				t.Fatal(err)
			}
			select {
			case update := <-host.updates:
				if update.Update.AgentThoughtChunk == nil || update.Update.AgentThoughtChunk.Content.Text.Text != "native thought" {
					t.Fatalf("thought: %+v", update)
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			if name == "gemini" {
				_, err := peer.CloseSession(ctx, acp.CloseSessionRequest{SessionId: session.SessionId})
				if err != nil {
					t.Fatalf("owned Gemini child close: %v", err)
				}
			}
		})
	}
}

// runFixture 模拟固定上游的协议形状，不发起网络或模型请求。
func runFixture() int {
	args := os.Args[1:]
	if len(args) > 0 && args[len(args)-1] == "--version" {
		fmt.Println("fixture-cli 1.2.3")
		return 0
	}
	name := os.Getenv("ACP_ADAPTER_HELPER")
	if os.Getenv("OPENCODE_REVIEW_HELPER") == "true" && len(args) > 1 && args[1] == "run" {
		expected := []string{"native-prefix", "run", "--format", "json", "--agent", "acp-go-reviewer", "--model", "openai/real-model"}
		if !reflect.DeepEqual(args, expected) || os.Getenv("OPENCODE_PERMISSION") != `{"*":"deny"}` || os.Getenv("OPENCODE_PURE") != "true" {
			return 8
		}
		var evidence map[string]any
		if json.NewDecoder(os.Stdin).Decode(&evidence) != nil || evidence["model"] != "openai/real-model" {
			return 9
		}
		fmt.Println(`{"type":"text","part":{"text":"{\"outcome\":\"allow\",\"risk_level\":\"low\"}"}}`)
		return 0
	}
	scanner := bufio.NewScanner(os.Stdin)
	encoder := json.NewEncoder(os.Stdout)
	model := "gemini-3.5-flash"
	if name == "opencode" {
		model = "openai/real-model"
	}
	var pendingPrompt json.RawMessage
	var lastInterjection any
	reasoning := "low"
	// options 构造原生选项，保留供应商的实际思考标识。
	options := func() []any {
		id := "effort"
		if name == "grok" {
			id = "reasoning_effort"
		}
		return []any{map[string]any{"id": "model", "name": "Model", "type": "select", "category": "model", "currentValue": model, "options": []any{map[string]string{"value": model, "name": model}}}, map[string]any{"id": id, "name": "Effort", "type": "select", "category": "thought_level", "currentValue": reasoning, "options": []any{map[string]string{"value": "low", "name": "Low"}, map[string]string{"value": "high", "name": "High"}}}}
	}
	for scanner.Scan() {
		var request map[string]json.RawMessage
		if json.Unmarshal(scanner.Bytes(), &request) != nil {
			return 2
		}
		if request["method"] == nil && pendingPrompt != nil {
			var permission map[string]any
			_ = json.Unmarshal(request["result"], &permission)
			selected := ""
			if outcome, ok := permission["outcome"].(map[string]any); ok {
				selected, _ = outcome["optionId"].(string)
			}
			_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": pendingPrompt, "result": map[string]any{"stopReason": "end_turn", "_meta": map[string]any{"selected": selected}}})
			pendingPrompt = nil
			continue
		}
		var method string
		_ = json.Unmarshal(request["method"], &method)
		var params map[string]any
		_ = json.Unmarshal(request["params"], &params)
		var result any = map[string]any{}
		switch method {
		case "initialize":
			result = map[string]any{"protocolVersion": 1, "agentCapabilities": map[string]any{"loadSession": true}, "agentInfo": map[string]any{"name": name, "version": "upstream-info-version"}, "authMethods": []any{}}
		case "_capture":
			capture := map[string]any{"args": args, "parent": os.Getenv("PARENT_SECRET"), "token": os.Getenv("INSTANCE_TOKEN"), "model": model, "interjection": lastInterjection, "settingsPath": os.Getenv("GEMINI_CLI_HOME") + "/.gemini/settings.json"}
			for _, key := range []string{"XDG_DATA_HOME", "XDG_CONFIG_HOME", "XDG_CACHE_HOME", "XDG_STATE_HOME", "GEMINI_CLI_HOME", "GROK_HOME"} {
				capture[key] = os.Getenv(key)
			}
			result = capture
		case "_x.ai/interject":
			lastInterjection = params
			result = map[string]any{"status": "queued"}
		case "session/new", "session/load":
			result = map[string]any{"sessionId": "native-session", "configOptions": options()}
			if name == "gemini" {
				result = map[string]any{"sessionId": "native-session", "models": map[string]any{"currentModelId": model, "availableModels": []any{map[string]string{"modelId": "gemini-3.5-flash", "name": "Gemini 3.5 Flash"}, map[string]string{"modelId": "unverified-model", "name": "Unknown"}}}}
			}
		case "session/set_config_option":
			reasoning, _ = params["value"].(string)
			result = map[string]any{"configOptions": options()}
		case "session/set_model":
			model, _ = params["modelId"].(string)
		case "session/prompt":
			if blocks, ok := params["prompt"].([]any); ok && len(blocks) > 0 && blocks[0].(map[string]any)["text"] == "permission" {
				pendingPrompt = request["id"]
				_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": map[string]any{"sessionId": "native-session", "update": map[string]any{"sessionUpdate": "tool_call", "toolCallId": "approved-tool", "title": "List", "kind": "execute", "status": "in_progress", "rawInput": map[string]any{"command": "ls"}}}})
				_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": "permission", "method": "session/request_permission", "params": map[string]any{"sessionId": "native-session", "toolCall": map[string]any{"toolCallId": "approved-tool", "rawInput": map[string]any{"command": "ls"}}, "options": []any{map[string]any{"optionId": "always", "name": "Always", "kind": "allow_always"}, map[string]any{"optionId": "once", "name": "Once", "kind": "allow_once"}, map[string]any{"optionId": "reject", "name": "Reject", "kind": "reject_once"}}}})
				continue
			}
			_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": map[string]any{"sessionId": "native-session", "update": map[string]any{"sessionUpdate": "agent_thought_chunk", "content": map[string]string{"type": "text", "text": "native thought"}}}})
			result = map[string]any{"stopReason": "end_turn", "_meta": map[string]any{"quota": map[string]any{"token_count": map[string]int{"input_tokens": 10, "output_tokens": 5}}}}
		case "session/close":
			if name == "gemini" {
				_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": request["id"], "error": map[string]any{"code": -32601, "message": "Method not found"}})
				continue
			}
		case "session/cancel":
			continue
		default:
			if strings.HasPrefix(method, "_") {
				result = params
			} else {
				_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": request["id"], "error": map[string]any{"code": -32601, "message": "Method not found"}})
				continue
			}
		}
		if err := encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": request["id"], "result": result}); err != nil {
			return 3
		}
	}
	return 0
}

// TestNativeBridgeIsSDKConnection 验证公开适配器仍通过唯一的公共 SDK 传输。
func TestNativeBridgeIsSDKConnection(t *testing.T) { var _ acp.Agent = (*nativeacp.Agent)(nil) }

// TestGeminiCustomModelsResumeAndQuota 验证未知模型保留原生接受语义、恢复别名与本轮用量。
func TestGeminiCustomModelsResumeAndQuota(t *testing.T) {
	agent, peer, host := startAdapter(t, "gemini")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := peer.Initialize(ctx, acp.InitializeRequest{ProtocolVersion: 1}); err != nil {
		t.Fatal(err)
	}
	session, err := peer.NewSession(ctx, acp.NewSessionRequest{Cwd: t.TempDir(), McpServers: []acp.McpServer{}})
	if err != nil {
		t.Fatal(err)
	}
	// setOption 从真正外层连接更改配置。
	setOption := func(id, value string) acp.SetSessionConfigOptionResponse {
		t.Helper()
		response, err := peer.SetSessionConfigOption(ctx, acp.SetSessionConfigOptionRequest{ValueId: &acp.SetSessionConfigOptionValueId{SessionId: session.SessionId, ConfigId: acp.SessionConfigId(id), Value: acp.SessionConfigValueId(value)}})
		if err != nil {
			t.Fatal(err)
		}
		return response
	}
	setOption("model", "gemini-3.5-flash")
	loader := agent.(*gemini.Agent)
	restored, err := loader.ResumeSession(ctx, acp.ResumeSessionRequest{SessionId: session.SessionId, Cwd: "/project", McpServers: []acp.McpServer{}})
	if err != nil {
		t.Fatal(err)
	}
	if len(restored.ConfigOptions) != 1 || restored.ConfigOptions[0].Select.CurrentValue != "gemini-3.5-flash" {
		t.Fatalf("native model resume: %+v", restored.ConfigOptions)
	}
	select {
	case update := <-host.updates:
		t.Fatalf("resume replayed history: %+v", update)
	default:
	}
	response := setOption("model", "account-exact-custom-model")
	if len(response.ConfigOptions) != 1 || response.ConfigOptions[0].Select.CurrentValue != "account-exact-custom-model" {
		t.Fatalf("custom model guessed thinking: %+v", response.ConfigOptions)
	}
	prompt, err := peer.Prompt(ctx, acp.PromptRequest{SessionId: session.SessionId, Prompt: []acp.ContentBlock{acp.TextBlock("hello")}})
	if err != nil {
		t.Fatal(err)
	}
	if prompt.Usage == nil || prompt.Usage.InputTokens != 10 || prompt.Usage.OutputTokens != 5 || prompt.Usage.TotalTokens != 15 {
		t.Fatalf("native turn usage: %+v", prompt)
	}
}

// TestGrokSteeringAcknowledgesQueueOnly 验证统一插话保留原生 queued 语义。
func TestGrokSteeringAcknowledgesQueueOnly(t *testing.T) {
	agent, peer, _ := startAdapter(t, "grok")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := peer.Initialize(ctx, acp.InitializeRequest{ProtocolVersion: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := peer.NewSession(ctx, acp.NewSessionRequest{Cwd: "/project", McpServers: []acp.McpServer{}}); err != nil {
		t.Fatal(err)
	}
	raw, err := peer.CallExtension(ctx, "_session/steering", json.RawMessage(`{"sessionId":"native-session","prompt":[{"type":"text","text":"adjust"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	var result map[string]any
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	if result["outcome"] != "queued" {
		t.Fatalf("queued falsely reported consumed: %s", raw)
	}
	capture, err := agent.CallNative(ctx, "_capture", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(capture), `"text":"adjust"`) {
		t.Fatalf("interject not forwarded: %s", capture)
	}
}

// TestOpenCodeAutoApprovalUsesNativeCallbackAndAudit 验证公开 auto 工厂连通原生审批、无工具 reviewer 与 allow_once 回执。
func TestOpenCodeAutoApprovalUsesNativeCallbackAndAudit(t *testing.T) {
	_, peer, host := startAdapter(t, "opencode", "auto")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := peer.Initialize(ctx, acp.InitializeRequest{ProtocolVersion: 1}); err != nil {
		t.Fatal(err)
	}
	session, err := peer.NewSession(ctx, acp.NewSessionRequest{Cwd: t.TempDir(), McpServers: []acp.McpServer{}})
	if err != nil {
		t.Fatal(err)
	}
	response, err := peer.Prompt(ctx, acp.PromptRequest{SessionId: session.SessionId, Prompt: []acp.ContentBlock{acp.TextBlock("permission")}})
	if err != nil {
		t.Fatal(err)
	}
	if response.Meta["selected"] != "once" || host.permissions.Load() != 0 {
		t.Fatalf("auto native callback: %+v, host calls=%d", response, host.permissions.Load())
	}
	audit := false
	for !audit {
		select {
		case update := <-host.updates:
			if tool := update.Update.ToolCallUpdate; tool != nil {
				if meta, ok := tool.Meta["acp-go/permission-review"].(map[string]any); ok && meta["outcome"] == "allow" {
					audit = true
				}
			}
		case <-ctx.Done():
			t.Fatal("review audit missing")
		}
	}
}

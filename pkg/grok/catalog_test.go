package grok

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/JieWaZi/acp-go/pkg/acpmeta"
	"github.com/JieWaZi/acp-go/pkg/acpserver"
	acp "github.com/coder/acp-go-sdk"
)

// catalogHost 保存宿主实际收到的配置通知。
type catalogHost struct {
	// Client 禁止调用未声明的宿主能力。
	acp.Client
	// updates 接收异步目录更新。
	updates chan []acp.SessionConfigOption
}

// SessionUpdate 接收真正 SDK 转发的动态目录。
func (host *catalogHost) SessionUpdate(_ context.Context, request acp.SessionNotification) error {
	if update := request.Update.ConfigOptionUpdate; update != nil {
		host.updates <- update.ConfigOptions
	}
	return nil
}

// startCatalogAgent 启动无账号 fake CLI 和真实宿主 SDK。
func startCatalogAgent(t *testing.T) (*acp.ClientSideConnection, *catalogHost) {
	t.Helper()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	agent, err := NewAgent(context.Background(), Config{
		GrokPath: binary, PrefixArgs: []string{"-test.run=^TestGrokCatalogProcess$", "--"},
		Environment: []string{"GROK_CATALOG_PROCESS=1"}, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	input, writer := io.Pipe()
	output, reader := io.Pipe()
	server, err := acpserver.New(agent, input, reader)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- server.Serve(ctx) }()
	host := &catalogHost{updates: make(chan []acp.SessionConfigOption, 16)}
	peer := acp.NewClientSideConnection(host, writer, output)
	t.Cleanup(func() {
		cancel()
		_ = writer.Close()
		_ = input.Close()
		_ = reader.Close()
		_ = output.Close()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(5 * time.Second):
			t.Error("server did not close")
		}
	})
	if _, err := peer.Initialize(context.Background(), acp.InitializeRequest{ProtocolVersion: 1}); err != nil {
		t.Fatal(err)
	}
	return peer, host
}

// catalogSelect 查找宿主可见的统一配置项。
func catalogSelect(options []acp.SessionConfigOption, id acp.SessionConfigId) *acp.SessionConfigOptionSelect {
	for _, option := range options {
		if option.Select != nil && option.Select.Id == id {
			return option.Select
		}
	}
	return nil
}

// modelMenu 读取模型选项的权威参数目录，并检查未知与空目录的区别。
func modelMenu(t *testing.T, options []acp.SessionConfigOption, id string, known bool) []acp.SessionConfigOption {
	t.Helper()
	model := catalogSelect(options, "model")
	if model == nil {
		t.Fatal("model select missing")
	}
	if len(*model.Options.Ungrouped) != 9 {
		t.Fatalf("legacy-only model was synthesized: %+v", model)
	}
	for _, value := range *model.Options.Ungrouped {
		if string(value.Value) != id {
			continue
		}
		menu, ok := acpmeta.ModelConfigOptions(value.Meta)
		if value.Meta["keep"] != true {
			t.Fatalf("native model metadata lost: %+v", value)
		}
		if ok != known {
			t.Fatalf("model %s known=%v want %v: %#v", id, ok, known, value.Meta)
		}
		return menu
	}
	t.Fatalf("model %s missing", id)
	return nil
}

// assertCatalog 捕获把当前模型的菜单复制给其他模型或丢弃原生元数据的回归。
func assertCatalog(t *testing.T, options []acp.SessionConfigOption, suffix string) {
	t.Helper()
	current := catalogSelect(modelMenu(t, options, "a", true), "reasoning")
	if current == nil || current.CurrentValue != "native-unlisted" || (*current.Options.Ungrouped)[0].Value != "actual" {
		t.Fatalf("current native config lost: %+v", current)
	}
	if (*current.Options.Ungrouped)[0].Meta["native"] != true {
		t.Fatalf("current option metadata overridden: %+v", current)
	}
	other := catalogSelect(modelMenu(t, options, "b", true), "reasoning")
	if other == nil || string(other.CurrentValue) != "deep-"+suffix {
		t.Fatalf("source default lost: %+v", other)
	}
	choice := (*other.Options.Ungrouped)[0]
	if string(choice.Value) != "deep-"+suffix || choice.Name != "Deep source" || choice.Description == nil || *choice.Description != "source description" || choice.Meta["default"] != true {
		t.Fatalf("source option changed: %+v", choice)
	}
	if len(modelMenu(t, options, "disabled", true)) != 0 {
		t.Fatal("disabled effort fabricated")
	}
	modelMenu(t, options, "unknown", false)
	modelMenu(t, options, "malformed", false)
	modelMenu(t, options, "unknown-value", false)
	for _, id := range []string{"fallback", "empty"} {
		reasoning := catalogSelect(modelMenu(t, options, id, true), "reasoning")
		values := *reasoning.Options.Ungrouped
		if reasoning.CurrentValue != "minimal" || len(values) != 5 || values[0].Value != "minimal" || values[4].Value != "xhigh" || values[4].Name != "X-High" {
			t.Fatalf("pinned fallback changed: %+v", reasoning)
		}
		for _, value := range values {
			if value.Meta["default"] != false {
				t.Fatalf("fallback default invented: %+v", value)
			}
		}
	}
	explicit := catalogSelect(modelMenu(t, options, "explicit", true), "reasoning")
	if values := *explicit.Options.Ungrouped; len(values) != 2 || values[0].Value != "off" || values[1].Value != "ceiling" {
		t.Fatalf("explicit none/max filtered: %+v", explicit)
	}
}

// TestGrokCatalogNewLoadResumeAndReplacement 验证三种入口保留不同菜单，重载替换旧会话目录。
func TestGrokCatalogNewLoadResumeAndReplacement(t *testing.T) {
	peer, _ := startCatalogAgent(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	first, err := peer.NewSession(ctx, acp.NewSessionRequest{Cwd: "/one", McpServers: []acp.McpServer{}})
	if err != nil {
		t.Fatal(err)
	}
	assertCatalog(t, first.ConfigOptions, "one")
	loaded, err := peer.LoadSession(ctx, acp.LoadSessionRequest{SessionId: "loaded", Cwd: "/two", McpServers: []acp.McpServer{}})
	if err != nil {
		t.Fatal(err)
	}
	assertCatalog(t, loaded.ConfigOptions, "two")
	resumed, err := peer.ResumeSession(ctx, acp.ResumeSessionRequest{SessionId: "resumed", Cwd: "/three", McpServers: []acp.McpServer{}})
	if err != nil {
		t.Fatal(err)
	}
	assertCatalog(t, resumed.ConfigOptions, "three")
	replaced, err := peer.LoadSession(ctx, acp.LoadSessionRequest{SessionId: first.SessionId, Cwd: "/replacement", McpServers: []acp.McpServer{}})
	if err != nil {
		t.Fatal(err)
	}
	assertCatalog(t, replaced.ConfigOptions, "replacement")
	refreshed, err := peer.SetSessionConfigOption(ctx, acp.SetSessionConfigOptionRequest{ValueId: &acp.SetSessionConfigOptionValueId{SessionId: "loaded", ConfigId: "reasoning", Value: "actual"}})
	if err != nil {
		t.Fatal(err)
	}
	other := catalogSelect(modelMenu(t, refreshed.ConfigOptions, "b", true), "reasoning")
	if other.CurrentValue != "deep-two" {
		t.Fatalf("sessions leaked catalog: %+v", other)
	}
}

// TestGrokCatalogSettersUpdatesAndErrors 验证原生 setter 标识、最新目录、异步更新和错误不丢失。
func TestGrokCatalogSettersUpdatesAndErrors(t *testing.T) {
	peer, host := startCatalogAgent(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	session, err := peer.NewSession(ctx, acp.NewSessionRequest{Cwd: "/one", McpServers: []acp.McpServer{}})
	if err != nil {
		t.Fatal(err)
	}
	set := func(id acp.SessionConfigId, value acp.SessionConfigValueId) (acp.SetSessionConfigOptionResponse, error) {
		return peer.SetSessionConfigOption(ctx, acp.SetSessionConfigOptionRequest{ValueId: &acp.SetSessionConfigOptionValueId{SessionId: session.SessionId, ConfigId: id, Value: value}})
	}
	switched, err := set("model", "b")
	if err != nil {
		t.Fatal(err)
	}
	reasoning := catalogSelect(modelMenu(t, switched.ConfigOptions, "b", true), "reasoning")
	if reasoning.CurrentValue != "deep-one" {
		t.Fatalf("setter current menu stale: %+v", reasoning)
	}
	selected, err := set("reasoning", "deep-one")
	if err != nil || catalogSelect(selected.ConfigOptions, "reasoning").CurrentValue != "deep-one" {
		t.Fatalf("custom input id not transmitted: %+v %v", selected, err)
	}
	if _, err = peer.SetSessionMode(ctx, acp.SetSessionModeRequest{SessionId: session.SessionId, ModeId: "native-mode"}); err != nil {
		t.Fatal(err)
	}
	if _, err = peer.CallExtension(ctx, "_emit", map[string]any{"sessionId": session.SessionId}); err != nil {
		t.Fatal(err)
	}
	select {
	case options := <-host.updates:
		reasoning = catalogSelect(modelMenu(t, options, "b", true), "reasoning")
		if reasoning.CurrentValue != "updated-unlisted" || (*reasoning.Options.Ungrouped)[0].Value != "live" {
			t.Fatalf("update overwritten with stale catalog: %+v", reasoning)
		}
		modelMenu(t, options, "unknown", false)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	restored, err := set("model", "a")
	if err != nil {
		t.Fatal(err)
	}
	assertCatalog(t, restored.ConfigOptions, "one")
	disabled, err := set("model", "disabled")
	if err != nil || catalogSelect(disabled.ConfigOptions, "reasoning") != nil ||
		len(modelMenu(t, disabled.ConfigOptions, "disabled", true)) != 0 {
		t.Fatalf("native absent reasoning overridden: %+v %v", disabled, err)
	}
	if _, err = set("model", "b"); err != nil {
		t.Fatal(err)
	}
	if _, err = peer.CallExtension(ctx, "_emit", map[string]any{
		"sessionId": session.SessionId, "noReasoning": true,
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case options := <-host.updates:
		if catalogSelect(options, "reasoning") != nil || len(modelMenu(t, options, "b", true)) != 0 {
			t.Fatalf("async absent reasoning overridden: %+v", options)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	_, err = set("model", "error")
	var rpcError *acp.RequestError
	if !errors.As(err, &rpcError) || rpcError.Code != -32042 || rpcError.Message != "native rejection" || rpcError.Data.(map[string]any)["reason"] != "source" {
		t.Fatalf("native error changed: %v", err)
	}
}

// TestGrokCatalogProcess 提供冻结原生响应，无网络、账号或真实模型。
func TestGrokCatalogProcess(t *testing.T) {
	if os.Getenv("GROK_CATALOG_PROCESS") != "1" {
		return
	}
	for _, arg := range os.Args {
		if arg == "--version" {
			_, _ = os.Stdout.WriteString("fixture-grok\n")
			os.Exit(0)
		}
	}
	sessions := map[string]string{}
	models := map[string]string{}
	selectOption := func(id, category, current string, values []map[string]any) map[string]any {
		return map[string]any{"id": id, "name": id, "category": category, "type": "select", "currentValue": current, "options": values}
	}
	options := func(id string, updated bool) []any {
		entries := []map[string]any{}
		for _, name := range []string{"a", "b", "disabled", "unknown", "malformed", "unknown-value", "fallback", "empty", "explicit"} {
			entries = append(entries, map[string]any{"value": name, "name": name, "_meta": map[string]any{"keep": true}})
		}
		value, label, current := "actual", "Native actual", "native-unlisted"
		if models[id] == "b" {
			value, label, current = "deep-"+sessions[id], "Deep source", "deep-"+sessions[id]
		}
		if updated {
			value, label, current = "live", "Live native", "updated-unlisted"
		}
		result := []any{selectOption("model", "model", models[id], entries)}
		if models[id] == "disabled" {
			return result
		}
		return append(result, selectOption("reasoning_effort", "thought_level", current,
			[]map[string]any{{"value": value, "name": label, "_meta": map[string]any{"native": true}}}))
	}
	metadata := func(suffix string) map[string]any {
		available := []any{}
		menus := map[string]any{
			"a":        map[string]any{"supportsReasoningEffort": true, "reasoningEfforts": []any{map[string]any{"id": "old", "value": "low", "label": "Old base", "default": true}}},
			"b":        map[string]any{"supportsReasoningEffort": true, "reasoningEfforts": []any{map[string]any{"id": "deep-" + suffix, "value": "high", "label": "Deep source", "description": "source description", "default": true}}},
			"disabled": map[string]any{"supportsReasoningEffort": false}, "unknown": map[string]any{},
			"malformed":     map[string]any{"supportsReasoningEffort": true, "reasoningEfforts": map[string]any{"value": "high"}},
			"unknown-value": map[string]any{"supportsReasoningEffort": true, "reasoningEfforts": []any{map[string]any{"id": "alien", "value": "alien", "label": "Alien", "default": false}}},
			"fallback":      map[string]any{"supportsReasoningEffort": true}, "empty": map[string]any{"supportsReasoningEffort": true, "reasoningEfforts": []any{}},
			"explicit":       map[string]any{"supportsReasoningEffort": true, "reasoningEfforts": []any{map[string]any{"id": "off", "value": "none", "label": "Off", "default": false}, map[string]any{"id": "ceiling", "value": "max", "label": "Ceiling", "default": true}}},
			"not-selectable": map[string]any{"supportsReasoningEffort": true},
		}
		for name, meta := range menus {
			available = append(available, map[string]any{"modelId": name, "name": name, "_meta": meta})
		}
		return map[string]any{"currentModelId": "a", "availableModels": available}
	}
	var connection *acp.Connection
	connection = acp.NewConnection(func(ctx context.Context, method string, data json.RawMessage) (any, *acp.RequestError) {
		request := map[string]any{}
		_ = json.Unmarshal(data, &request)
		id, _ := request["sessionId"].(string)
		switch method {
		case "initialize":
			return map[string]any{"protocolVersion": 1, "agentCapabilities": map[string]any{"loadSession": true, "sessionCapabilities": map[string]any{"resume": map[string]any{}}}}, nil
		case "session/new", "session/load", "session/resume":
			if id == "" {
				id = "new"
			}
			sessions[id] = strings.TrimPrefix(request["cwd"].(string), "/")
			models[id] = "a"
			return map[string]any{"sessionId": id, "models": metadata(sessions[id]), "configOptions": options(id, false)}, nil
		case "session/set_config_option":
			value, _ := request["value"].(string)
			if value == "error" {
				return nil, &acp.RequestError{Code: -32042, Message: "native rejection", Data: map[string]any{"reason": "source"}}
			}
			switch request["configId"] {
			case "model":
				models[id] = value
			case "reasoning_effort":
				if models[id] == "b" && value != "deep-"+sessions[id] {
					return nil, acp.NewInvalidParams(nil)
				}
			default:
				return nil, acp.NewInvalidParams(nil)
			}
			return map[string]any{"configOptions": options(id, false)}, nil
		case "session/set_mode":
			if request["modeId"] != "native-mode" {
				return nil, acp.NewInvalidParams(nil)
			}
			return map[string]any{}, nil
		case "_emit":
			updated := options(id, true)
			if request["noReasoning"] == true {
				updated = updated[:1]
			}
			_ = connection.SendNotification(ctx, "session/update", map[string]any{
				"sessionId": id,
				"update":    map[string]any{"sessionUpdate": "config_option_update", "configOptions": updated},
			})
			return map[string]any{}, nil
		}
		return nil, acp.NewMethodNotFound(method)
	}, os.Stdout, os.Stdin)
	<-connection.Done()
	os.Exit(0)
}

// TestGrokCatalogMalformedDoesNotInvent 捕获 null 支持标记被误判为确认不支持或畸形菜单被 fallback 掩盖。
func TestGrokCatalogMalformedDoesNotInvent(t *testing.T) {
	for _, payload := range []string{
		`{"supportsReasoningEffort":null}`,
		`{"supportsReasoningEffort":"true"}`,
		`{"supportsReasoningEffort":true,"reasoningEfforts":null}`,
		`{"supportsReasoningEffort":true,"reasoningEfforts":[{"id":"deep","value":"high","label":"Deep"}]}`,
		`{"supportsReasoningEffort":true,"reasoningEfforts":["high"]}`,
		`{"reasoningEfforts":[{"id":"deep","value":"high","label":"Deep","default":true}]}`,
	} {
		t.Run(payload, func(t *testing.T) {
			metadata := map[string]json.RawMessage{}
			if err := json.Unmarshal([]byte(payload), &metadata); err != nil {
				t.Fatal(err)
			}
			if _, known := modelReasoningMenu(metadata); known {
				t.Fatalf("invalid metadata invented a catalog: %s", payload)
			}
		})
	}
}

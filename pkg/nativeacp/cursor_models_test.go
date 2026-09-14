package nativeacp_test

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	acp "github.com/coder/acp-go-sdk"
)

// TestCursorModelDependentThinking 验证握手启用参数目录，模型切换后只有该模型的有效思考选项。
func TestCursorModelDependentThinking(t *testing.T) {
	_, client, _ := startAgent(t, "parameters")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := client.Initialize(ctx, acp.InitializeRequest{ProtocolVersion: 1}); err != nil {
		t.Fatal(err)
	}
	session, err := client.NewSession(ctx, acp.NewSessionRequest{Cwd: t.TempDir(), McpServers: []acp.McpServer{}})
	if err != nil {
		t.Fatal(err)
	}
	reasoning := selectParameter(session.ConfigOptions, "reasoning")
	if reasoning == nil {
		t.Fatalf("Cursor thinking missing: %+v", session.ConfigOptions)
	}
	options := *reasoning.Options.Ungrouped
	if len(options) != 3 || options[0].Name != "Off" || options[2].Name != "High" {
		t.Fatalf("thinking and effort lost: %+v", options)
	}
	model := selectParameter(session.ConfigOptions, "model")
	for _, option := range *model.Options.Ungrouped {
		if _, ok := option.Meta["acp-go/model-config-options"]; !ok {
			t.Fatalf("model %s has no parameter catalog", option.Value)
		}
	}
	// 同一表单先关闭思考，再选择高强度，必须把原生开关也开启。
	for _, choice := range []acp.SessionConfigValueId{options[0].Value, options[2].Value} {
		result, err := client.SetSessionConfigOption(ctx, acp.SetSessionConfigOptionRequest{ValueId: &acp.SetSessionConfigOptionValueId{SessionId: session.SessionId, ConfigId: "reasoning", Value: choice}})
		if err != nil || selectParameter(result.ConfigOptions, "reasoning").CurrentValue != choice {
			t.Fatalf("thinking not applied: %+v %v", result, err)
		}
	}
	result, err := client.SetSessionConfigOption(ctx, acp.SetSessionConfigOptionRequest{ValueId: &acp.SetSessionConfigOptionValueId{SessionId: session.SessionId, ConfigId: "model", Value: "gpt"}})
	if err != nil {
		t.Fatal(err)
	}
	reasoning = selectParameter(result.ConfigOptions, "reasoning")
	if reasoning == nil || len(*reasoning.Options.Ungrouped) != 2 || (*reasoning.Options.Ungrouped)[0].Value != "low" {
		t.Fatalf("GPT got another model's thinking: %+v", reasoning)
	}
	result, err = client.SetSessionConfigOption(ctx, acp.SetSessionConfigOptionRequest{ValueId: &acp.SetSessionConfigOptionValueId{SessionId: session.SessionId, ConfigId: "reasoning", Value: "high"}})
	if err != nil || selectParameter(result.ConfigOptions, "reasoning").CurrentValue != "high" {
		t.Fatalf("native reasoning ID lost: %+v %v", result, err)
	}
	result, err = client.SetSessionConfigOption(ctx, acp.SetSessionConfigOptionRequest{ValueId: &acp.SetSessionConfigOptionValueId{SessionId: session.SessionId, ConfigId: "model", Value: "composer"}})
	if err != nil || selectParameter(result.ConfigOptions, "reasoning") != nil {
		t.Fatalf("Composer fabricated thinking: %+v %v", result, err)
	}
	if _, err = client.SetSessionConfigOption(ctx, acp.SetSessionConfigOptionRequest{ValueId: &acp.SetSessionConfigOptionValueId{SessionId: session.SessionId, ConfigId: "reasoning", Value: "high"}}); err == nil {
		t.Fatal("stale thinking accepted for unsupported model")
	}
}

// selectParameter 获取测试宿主实际收到的配置项。
func selectParameter(options []acp.SessionConfigOption, id acp.SessionConfigId) *acp.SessionConfigOptionSelect {
	for _, option := range options {
		if option.Select != nil && option.Select.Id == id {
			return option.Select
		}
	}
	return nil
}

// runCursorParameterProcess 对照官方参数式握手、目录扩展与原生配置回执。
func runCursorParameterProcess() {
	enabled := false
	model, thinking, effort := "claude", "true", "medium"
	gptEffort := "low"
	selectOption := func(id, category, current string, values ...string) map[string]any {
		options := []any{}
		for _, value := range values {
			name := map[string]string{"false": "Off", "true": "On", "medium": "Medium", "high": "High", "low": "Low"}[value]
			if name == "" {
				name = value
			}
			options = append(options, map[string]any{"value": value, "name": name})
		}
		return map[string]any{"id": id, "name": id, "category": category, "type": "select", "currentValue": current, "options": options}
	}
	params := func(name string) []any {
		if name == "claude" {
			return []any{selectOption("thinking", "thought_level", thinking, "false", "true"), selectOption("effort", "thought_level", effort, "medium", "high")}
		}
		if name == "gpt" {
			return []any{selectOption("reasoning_effort", "thought_level", gptEffort, "low", "high")}
		}
		return []any{}
	}
	options := func() []any {
		if !enabled {
			return []any{selectOption("model", "model", "claude[effort=medium]", "claude[effort=medium]")}
		}
		return append([]any{selectOption("model", "model", model, "claude", "gpt", "composer")}, params(model)...)
	}
	connection := acp.NewConnection(func(ctx context.Context, method string, data json.RawMessage) (any, *acp.RequestError) {
		var request map[string]any
		_ = json.Unmarshal(data, &request)
		switch method {
		case "initialize":
			var value acp.InitializeRequest
			_ = json.Unmarshal(data, &value)
			enabled = value.ClientCapabilities.Meta["parameterizedModelPicker"] == true
			return map[string]any{"protocolVersion": 1, "agentCapabilities": map[string]any{}}, nil
		case "cursor/list_available_models":
			values := []any{}
			for _, name := range []string{"claude", "gpt", "composer"} {
				values = append(values, map[string]any{"value": name, "name": name, "configOptions": params(name)})
			}
			return map[string]any{"models": values}, nil
		case "session/new":
			return map[string]any{"sessionId": "parameters", "configOptions": options()}, nil
		case "session/set_config_option":
			id, value := request["configId"], request["value"].(string)
			switch id {
			case "model":
				model = value
			case "thinking":
				if model != "claude" {
					return nil, acp.NewInvalidParams(nil)
				}
				thinking = value
			case "effort":
				if model != "claude" || thinking != "true" {
					return nil, acp.NewInvalidParams(nil)
				}
				effort = value
			case "reasoning_effort":
				gptEffort = value
				if model != "gpt" {
					return nil, acp.NewInvalidParams(nil)
				}
			default:
				return nil, acp.NewInvalidParams(nil)
			}
			return map[string]any{"configOptions": options()}, nil
		}
		return nil, acp.NewMethodNotFound(method)
	}, os.Stdout, os.Stdin)
	<-connection.Done()
}

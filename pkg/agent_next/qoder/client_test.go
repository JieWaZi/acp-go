package qoder_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JieWaZi/acp-go/pkg/acpmeta"
	agentclient "github.com/JieWaZi/acp-go/pkg/agent_next"
	"github.com/JieWaZi/acp-go/pkg/agent_next/qoder"
	acp "github.com/JieWaZi/acp-go/sdk"
)

func TestNativeQoder(t *testing.T) {
	if os.Getenv("ACP_FAMILY_QODER") != "1" {
		return
	}
	command := os.Args[len(os.Args)-1]
	if command == "--version" {
		fmt.Println("Qoder CLI 0.15.0")
		os.Exit(0)
	}
	if command != "--acp" {
		os.Exit(2)
	}
	trace, err := os.OpenFile(os.Getenv("ACP_FAMILY_TRACE"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		os.Exit(3)
	}
	out := json.NewEncoder(os.Stdout)
	in := bufio.NewScanner(os.Stdin)
	options := func(id, thought string) []any {
		model := "standard"
		if id == "second" {
			model = "other"
		}
		return []any{
			map[string]any{"id": "reasoning_effort", "name": "Reasoning effort", "category": "model", "type": "select", "currentValue": thought, "options": []any{map[string]any{"value": "native-low", "name": "Low"}, map[string]any{"value": "native-high", "name": "High"}}},
			map[string]any{"id": "llm", "name": "Model", "category": "model", "type": "select", "currentValue": model, "options": []any{map[string]any{"value": "standard", "name": "Standard"}, map[string]any{"value": "other", "name": "Other"}}},
		}
	}
	var promptID json.RawMessage
	var opened int
	var prompts int
	reply := func(id json.RawMessage, result any) {
		_ = out.Encode(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
	}
	notify := func(id string, data map[string]any) {
		_ = out.Encode(map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": map[string]any{"sessionId": id, "update": data}})
	}
	config := func(id, thought string) {
		notify(id, map[string]any{"sessionUpdate": "config_option_update", "configOptions": options(id, thought)})
	}
	for in.Scan() {
		var frame struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params map[string]any  `json:"params"`
		}
		if json.Unmarshal(in.Bytes(), &frame) != nil {
			os.Exit(4)
		}
		fmt.Fprintln(trace, string(in.Bytes()))
		id, _ := frame.Params["sessionId"].(string)
		switch frame.Method {
		case "initialize":
			reply(frame.ID, map[string]any{"protocolVersion": 1, "agentCapabilities": map[string]any{"loadSession": true, "sessionCapabilities": map[string]any{"resume": map[string]any{}}}})
		case "session/new":
			opened++
			id = "first"
			if opened == 2 {
				id = "second"
			}
			config("foreign", "native-high")
			config(id, "native-low")
			reply(frame.ID, map[string]any{"sessionId": id, "configOptions": options(id, "native-low")})
		case "session/resume", "session/load":
			config(id, "native-low")
			reply(frame.ID, map[string]any{"configOptions": options(id, "native-low")})
		case "session/set_config_option":
			if frame.Params["configId"] != "reasoning_effort" || frame.Params["value"] != "native-high" {
				os.Exit(5)
			}
			config(id, "native-high")
			reply(frame.ID, map[string]any{"configOptions": options(id, "native-high")})
		case "session/prompt":
			prompts++
			promptID = frame.ID
			notify("foreign", map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": "unrelated"}})
			notify(id, map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": "Error: RetriableError: WritableIterable is closed"}})
			if os.Getenv("ACP_FAMILY_WIRE_CANCEL") != "1" {
				reply(frame.ID, map[string]any{"stopReason": "end_turn", "usage": map[string]any{"inputTokens": prompts * 12, "cachedReadTokens": 0, "cachedWriteTokens": 0}})
			}
		case "session/cancel":
			reply(promptID, map[string]any{"stopReason": "cancelled", "usage": map[string]any{"inputTokens": 12, "cachedReadTokens": 0, "cachedWriteTokens": 0}})
		default:
			os.Exit(6)
		}
	}
	os.Exit(0)
}

func fixtureOptions(t *testing.T, cancel bool) (qoder.Options, string) {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	trace := filepath.Join(dir, "wire")
	env := append(os.Environ(), "ACP_FAMILY_QODER=1", "ACP_FAMILY_TRACE="+trace)
	if cancel {
		env = append(env, "ACP_FAMILY_WIRE_CANCEL=1")
	}
	return qoder.Options{Executable: executable, PrefixArgs: []string{"-test.run=^TestNativeQoder$", "--"}, Environment: env, WorkingDirectory: dir}, trace
}

func checkOptions(t *testing.T, options []acp.SessionConfigOption, thought string) {
	t.Helper()
	if len(options) != 2 || options[0].Select == nil || options[1].Select == nil {
		t.Fatalf("directory = %#v", options)
	}
	reason, model := options[0].Select, options[1].Select
	if reason.Id != "reasoning_effort" || reason.CurrentValue != acp.SessionConfigValueId(thought) || reason.Category == nil || *reason.Category != acp.SessionConfigOptionCategoryThoughtLevel ||
		model.Id != "llm" || model.Category == nil || *model.Category != acp.SessionConfigOptionCategoryModel {
		t.Fatalf("reasoning = %#v model = %#v", reason, model)
	}
}

func TestQoderDirectNormalizesAllConfigurationEvidence(t *testing.T) {
	for _, runtime := range []string{"qoder", "qoder-cn"} {
		t.Run(runtime, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 7*time.Second)
			defer cancel()
			options, trace := fixtureOptions(t, false)
			options.RuntimeID = runtime
			observations := make(chan acp.SessionNotification, 32)
			client, err := qoder.NewClient(ctx, options, agentclient.HostCallbacks{Observe: func(_ context.Context, scope agentclient.Scope, note acp.SessionNotification) error {
				observations <- note
				return nil
			}})
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := client.Close(ctx); err != nil {
					t.Error(err)
				}
			}()
			initialized, err := client.Initialize(ctx, acp.InitializeRequest{ProtocolVersion: 1})
			if err != nil {
				t.Fatal(err)
			}
			if initialized.AgentInfo == nil || initialized.AgentInfo.Name != runtime || acpmeta.RuntimeVersion(initialized.AgentInfo.Meta) != "0.15.0" {
				t.Fatalf("regional version: %#v", initialized.AgentInfo)
			}
			first, err := client.NewSession(ctx, acp.NewSessionRequest{Cwd: options.WorkingDirectory})
			if err != nil {
				t.Fatal(err)
			}
			checkOptions(t, first.ConfigOptions, "native-low")
			if _, err := client.NewSession(ctx, acp.NewSessionRequest{Cwd: options.WorkingDirectory}); err != nil {
				t.Fatal(err)
			}
			loaded, err := client.LoadSession(ctx, acp.LoadSessionRequest{SessionId: first.SessionId, Cwd: options.WorkingDirectory})
			if err != nil {
				t.Fatal(err)
			}
			checkOptions(t, loaded.ConfigOptions, "native-low")
			resumed, err := client.ResumeSession(ctx, acp.ResumeSessionRequest{SessionId: first.SessionId, Cwd: options.WorkingDirectory})
			if err != nil {
				t.Fatal(err)
			}
			checkOptions(t, resumed.ConfigOptions, "native-low")
			set, err := client.SetSessionConfigOption(ctx, acp.SetSessionConfigOptionRequest{ValueId: &acp.SetSessionConfigOptionValueId{SessionId: first.SessionId, ConfigId: "reasoning_effort", Value: "native-high"}})
			if err != nil {
				t.Fatal(err)
			}
			checkOptions(t, set.ConfigOptions, "native-high")
			run, err := client.ReservePrompt(ctx, acp.PromptRequest{SessionId: first.SessionId, Prompt: []acp.ContentBlock{acp.TextBlock("test")}}, agentclient.PromptOptions{})
			if err != nil {
				t.Fatal(err)
			}
			result := run()
			if result.Outcome == nil || result.Outcome.StopReason == nil || *result.Outcome.StopReason != acp.StopReasonEndTurn || !result.Outcome.ObservedOutput {
				t.Fatalf("outcome = %#v", result)
			}
			var dynamic, ordinary bool
			for !dynamic || !ordinary {
				select {
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				case event := <-observations:
					if event.SessionId == "foreign" {
						t.Fatalf("foreign update: %#v", event)
					}
					if entry := event.Update.ConfigOptionUpdate; event.SessionId == first.SessionId && entry != nil {
						thought := entry.ConfigOptions[0].Select.CurrentValue
						checkOptions(t, entry.ConfigOptions, string(thought))
						dynamic = dynamic || thought == "native-high"
					}
					if text := event.Update.AgentMessageChunk; text != nil {
						ordinary = text.Content.Text != nil && strings.Contains(text.Content.Text.Text, "WritableIterable")
					}
				}
			}
			wire, _ := os.ReadFile(trace)
			if strings.Count(string(wire), `"method":"session/set_config_option"`) != 1 || strings.Contains(string(wire), "cursor/") || strings.Contains(string(wire), "parameterizedModelPicker") ||
				!strings.Contains(string(wire), `"configId":"reasoning_effort","sessionId":"first","value":"native-high"`) {
				t.Fatalf("native settings trace = %s", wire)
			}
		})
	}
}

func TestQoderPublicACPUsesSameNativeCancelAndResult(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 7*time.Second)
	defer cancel()
	options, trace := fixtureOptions(t, true)
	var native agentclient.Client
	projected, err := agentclient.NewACPAgent(func(host agentclient.HostCallbacks) (agentclient.Client, error) {
		client, err := qoder.NewClient(ctx, options, host)
		native = client
		return client, err
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := native.Close(ctx); err != nil {
			t.Error(err)
		}
	}()
	in, send := io.Pipe()
	receive, out := io.Pipe()
	defer send.Close()
	defer receive.Close()
	defer in.Close()
	defer out.Close()
	stop := context.AfterFunc(ctx, func() { in.Close(); receive.Close() })
	defer stop()
	acp.NewAgentSideConnectionWithBinder(projected, out, in, projected.SetAgentConnection)
	encoder, scanner := json.NewEncoder(send), bufio.NewScanner(receive)
	write := func(id int, method string, params any) {
		t.Helper()
		frame := map[string]any{"jsonrpc": "2.0", "method": method, "params": params}
		if id > 0 {
			frame["id"] = id
		}
		if err := encoder.Encode(frame); err != nil {
			t.Fatal(err)
		}
	}
	wait := func(predicate func(map[string]json.RawMessage) bool) map[string]json.RawMessage {
		t.Helper()
		for scanner.Scan() {
			var frame map[string]json.RawMessage
			if err := json.Unmarshal(scanner.Bytes(), &frame); err != nil {
				t.Fatal(err)
			}
			if predicate(frame) {
				return frame
			}
		}
		t.Fatalf("no matching public frame: %v", scanner.Err())
		return nil
	}
	reply := func(id string) map[string]json.RawMessage {
		t.Helper()
		frame := wait(func(frame map[string]json.RawMessage) bool { return string(frame["id"]) == id })
		if frame["error"] != nil {
			t.Fatalf("public response error: %s", frame["error"])
		}
		return frame
	}
	write(1, "initialize", acp.InitializeRequest{ProtocolVersion: 1})
	reply("1")
	write(2, "session/new", acp.NewSessionRequest{Cwd: options.WorkingDirectory, McpServers: []acp.McpServer{}})
	var opened acp.NewSessionResponse
	if err := json.Unmarshal(reply("2")["result"], &opened); err != nil {
		t.Fatal(err)
	}
	checkOptions(t, opened.ConfigOptions, "native-low")
	write(3, "session/set_config_option", acp.SetSessionConfigOptionRequest{ValueId: &acp.SetSessionConfigOptionValueId{SessionId: opened.SessionId, ConfigId: "reasoning_effort", Value: "native-high"}})
	var configured acp.SetSessionConfigOptionResponse
	if err := json.Unmarshal(reply("3")["result"], &configured); err != nil {
		t.Fatal(err)
	}
	checkOptions(t, configured.ConfigOptions, "native-high")
	write(4, "session/prompt", acp.PromptRequest{SessionId: opened.SessionId, Prompt: []acp.ContentBlock{acp.TextBlock("test")}})
	wait(func(frame map[string]json.RawMessage) bool {
		if string(frame["method"]) != `"session/update"` {
			return false
		}
		var note acp.SessionNotification
		if json.Unmarshal(frame["params"], &note) != nil {
			return false
		}
		if note.SessionId == "foreign" {
			t.Fatalf("foreign public frame: %s", frame["params"])
		}
		return note.Update.AgentMessageChunk != nil
	})
	write(0, "session/cancel", acp.CancelNotification{SessionId: opened.SessionId})
	var result acp.PromptResponse
	if err := json.Unmarshal(reply("4")["result"], &result); err != nil {
		t.Fatal(err)
	}
	usage, ok := acpmeta.ReadTurnUsage(result.Meta)
	if result.StopReason != acp.StopReasonCancelled || !ok || usage.InputTokens == nil || *usage.InputTokens != 12 {
		t.Fatalf("public native result = %#v, usage = %#v", result, usage)
	}
	wire, _ := os.ReadFile(trace)
	if strings.Count(string(wire), `"method":"session/cancel"`) != 1 || strings.Contains(string(wire), "cursor/") {
		t.Fatalf("native control trace = %s", wire)
	}
}

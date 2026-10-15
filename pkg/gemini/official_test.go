package gemini

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JieWaZi/acp-go/pkg/autoreview"
	acp "github.com/coder/acp-go-sdk"
)

// TestOfficialSessionThinkingAndHistory 使用固定官方 CLI 验证真实工具续轮与生产替换实现。
func TestOfficialSessionThinkingAndHistory(t *testing.T) {
	cli := os.Getenv("GEMINI_OFFICIAL_CLI")
	if cli == "" {
		t.Skip("opt-in official 0.62.0 loopback proof")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal(err)
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(root, "work")
	home := filepath.Join(root, "home")
	if err := os.MkdirAll(filepath.Join(home, ".gemini"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(work, 0700); err != nil {
		t.Fatal(err)
	}
	fixture := filepath.Join(work, "fixture.txt")
	if err := os.WriteFile(fixture, []byte("genuine tool fixture data"), 0600); err != nil {
		t.Fatal(err)
	}
	settings := `{"security":{"auth":{"selectedType":"gemini-api-key"}},"model":{"name":"gemini-3.8-flash"},"tools":{"core":["read_file"]},"hooksConfig":{"enabled":false},"admin":{"mcp":{"enabled":false},"extensions":{"enabled":false},"skills":{"enabled":false}},"telemetry":{"enabled":false},"modelConfigs":{"customOverrides":[{"match":{"model":"gemini-3.8-flash"},"modelConfig":{"generateContentConfig":{"temperature":0.42,"thinkingConfig":{"includeThoughts":true,"thinkingLevel":"HIGH"}}}}]}}`
	if err := os.WriteFile(filepath.Join(home, ".gemini", "settings.json"), []byte(settings), 0600); err != nil {
		t.Fatal(err)
	}
	var mutex sync.Mutex
	requests := []map[string]any{}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		if request.Method == http.MethodGet {
			_ = json.NewEncoder(writer).Encode(map[string]any{"models": []any{map[string]any{"name": "models/gemini-3.8-flash", "supportedGenerationMethods": []string{"generateContent"}}}})
			return
		}
		body := map[string]any{}
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Error(err)
			writer.WriteHeader(400)
			return
		}
		if strings.Contains(request.URL.Path, "countTokens") {
			_ = json.NewEncoder(writer).Encode(map[string]any{"totalTokens": 10})
			return
		}
		mutex.Lock()
		body["fixturePath"] = request.URL.Path
		requests = append(requests, body)
		index := len(requests)
		mutex.Unlock()
		parts := []any{map[string]any{"text": "Fixture answer"}}
		if index == 1 {
			parts = []any{map[string]any{"functionCall": map[string]any{"id": "fixture-call", "name": "read_file", "args": map[string]any{"file_path": fixture}}, "thoughtSignature": "Zml4dHVyZQ=="}}
		}
		result := map[string]any{"candidates": []any{map[string]any{"content": map[string]any{"role": "model", "parts": parts}, "finishReason": "STOP", "index": 0}}, "usageMetadata": map[string]any{"promptTokenCount": 10, "candidatesTokenCount": 5, "totalTokenCount": 15}}
		if request.URL.Query().Get("alt") == "sse" {
			writer.Header().Set("Content-Type", "text/event-stream")
			data, _ := json.Marshal(result)
			_, _ = fmt.Fprintf(writer, "data: %s\n\n", data)
			return
		}
		_ = json.NewEncoder(writer).Encode(result)
	}))
	defer server.Close()
	environment := []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "GEMINI_CLI_HOME=" + home, "GEMINI_API_KEY=fixture-only", "GOOGLE_GEMINI_BASE_URL=" + server.URL, "GEMINI_CLI_TRUST_WORKSPACE=true", "CI=true", "NO_PROXY=127.0.0.1,localhost"}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	agent, err := NewAgent(context.Background(), Config{GeminiPath: node, PrefixArgs: []string{cli}, Environment: environment, WorkingDirectory: work, Logger: logger})
	if err != nil {
		t.Fatal(err)
	}
	input, output := io.Pipe()
	hostInput, hostOutput := io.Pipe()
	connection := acp.NewAgentSideConnection(agent, output, hostInput)
	agent.SetAgentConnection(connection)
	client := &sessionClient{updates: make(chan acp.SessionNotification, 256)}
	_ = acp.NewClientSideConnection(client, hostOutput, input)
	defer func() {
		_ = agent.Close(context.Background())
		_ = input.Close()
		_ = output.Close()
		_ = hostInput.Close()
		_ = hostOutput.Close()
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Second)
	defer cancel()
	if _, err := agent.Initialize(ctx, acp.InitializeRequest{ProtocolVersion: 1}); err != nil {
		t.Fatal(err)
	}
	session, err := agent.NewSession(ctx, acp.NewSessionRequest{Cwd: work, McpServers: []acp.McpServer{}})
	if err != nil {
		t.Fatal(err)
	}
	set := func(value string) {
		t.Helper()
		if _, err := agent.SetSessionConfigOption(ctx, acp.SetSessionConfigOptionRequest{ValueId: &acp.SetSessionConfigOptionValueId{SessionId: session.SessionId, ConfigId: "reasoning", Value: acp.SessionConfigValueId(value)}}); err != nil {
			t.Fatal(err)
		}
	}
	prompt := func(text string) {
		t.Helper()
		if _, err := agent.Prompt(ctx, acp.PromptRequest{SessionId: session.SessionId, Prompt: []acp.ContentBlock{acp.TextBlock(text)}}); err != nil {
			t.Fatal(err)
		}
	}
	set("low")
	prompt("Read the fixture file and answer.")
	set("medium")
	prompt("Continue using the previous fixture result.")
	set("default")
	prompt("Continue again with default settings.")
	mutex.Lock()
	defer mutex.Unlock()
	if len(requests) != 4 {
		t.Fatalf("expected real tool continuation plus two restores; requests=%d", len(requests))
	}
	for index, body := range requests {
		expected := "LOW"
		if index == 2 {
			expected = "MEDIUM"
		}
		if index == 3 {
			expected = "HIGH"
		}
		generation := body["generationConfig"].(map[string]any)
		thinking := generation["thinkingConfig"].(map[string]any)
		if thinking["thinkingLevel"] != expected || thinking["thinkingBudget"] != nil || generation["temperature"] != 0.42 {
			t.Fatalf("request %d lost canonical config: %+v", index, generation)
		}
		if !strings.Contains(body["fixturePath"].(string), "gemini-3.8-flash:") {
			t.Fatalf("unexpected model: %v", body["fixturePath"])
		}
		text, _ := json.Marshal(body)
		if index > 0 && !strings.Contains(string(text), "genuine tool fixture data") {
			t.Fatalf("missing real tool response in request %d", index)
		}
		if index > 1 && !strings.Contains(string(text), "Read the fixture file and answer.") {
			t.Fatalf("history missing in replacement request %d", index)
		}
	}
	started, completed := false, false
	for len(client.updates) > 0 {
		update := <-client.updates
		if update.SessionId != session.SessionId {
			t.Fatal("native callback leaked identity")
		}
		if update.Update.ToolCall != nil {
			started = true
		}
		if update.Update.ToolCallUpdate != nil && update.Update.ToolCallUpdate.Status != nil && *update.Update.ToolCallUpdate.Status == acp.ToolCallStatusCompleted {
			completed = true
		}
	}
	if !started || !completed {
		t.Fatalf("real tool events absent: started=%v completed=%v", started, completed)
	}
	t.Log("PASS official 0.62: actual read_file + continuation LOW; same-minute production restore MEDIUM; original HIGH/temperature restored; all callback IDs stable")
}

// TestOfficialReviewerNoTools 验证生产审查器的实际官方请求没有工具且统计确认当前模型。
func TestOfficialReviewerNoTools(t *testing.T) {
	cli := os.Getenv("GEMINI_OFFICIAL_CLI")
	if cli == "" {
		t.Skip("opt-in official 0.62.0 loopback proof")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal(err)
	}
	var mutex sync.Mutex
	requests := []map[string]any{}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		if request.Method == http.MethodGet {
			_ = json.NewEncoder(writer).Encode(map[string]any{"models": []any{map[string]any{"name": "models/gemini-3.8-flash", "supportedGenerationMethods": []string{"generateContent"}}}})
			return
		}
		body := map[string]any{}
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		if strings.Contains(request.URL.Path, "countTokens") {
			_ = json.NewEncoder(writer).Encode(map[string]any{"totalTokens": 10})
			return
		}
		mutex.Lock()
		body["fixturePath"] = request.URL.Path
		requests = append(requests, body)
		mutex.Unlock()
		result := map[string]any{"candidates": []any{map[string]any{"content": map[string]any{"role": "model", "parts": []any{map[string]any{"text": `{"outcome":"allow","risk_level":"low","rationale":"fixture"}`}}}, "finishReason": "STOP", "index": 0}}, "usageMetadata": map[string]any{"promptTokenCount": 10, "candidatesTokenCount": 5, "totalTokenCount": 15}}
		if request.URL.Query().Get("alt") == "sse" {
			writer.Header().Set("Content-Type", "text/event-stream")
			data, _ := json.Marshal(result)
			_, _ = fmt.Fprintf(writer, "data: %s\n\n", data)
			return
		}
		_ = json.NewEncoder(writer).Encode(result)
	}))
	defer server.Close()
	home := t.TempDir()
	if err := os.Mkdir(filepath.Join(home, ".gemini"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".gemini", "settings.json"), []byte(`{"security":{"auth":{"selectedType":"gemini-api-key"}},"tools":{"core":["run_shell_command"]},"hooksConfig":{"enabled":true}}`), 0600); err != nil {
		t.Fatal(err)
	}
	environment := []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "GEMINI_CLI_HOME=" + home, "GEMINI_API_KEY=fixture-only", "GOOGLE_GEMINI_BASE_URL=" + server.URL, "CI=true", "NO_PROXY=127.0.0.1,localhost"}
	profile, err := newProfileState(environment, "", "")
	if err != nil {
		t.Fatal(err)
	}
	agent := &Agent{config: Config{GeminiPath: node, PrefixArgs: []string{cli}, Environment: environment}, profile: profile}
	decision, err := agent.permissionReviewer(context.Background(), autoreview.Request{Model: "gemini-3.8-flash", WorkingDirectory: home, Prompt: []acp.ContentBlock{acp.TextBlock("Read fixture")}, Tool: acp.ToolCallUpdate{ToolCallId: "fixture", RawInput: map[string]any{"file": "fixture"}}})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Outcome != "allow" {
		t.Fatalf("unexpected fixture decision: %+v", decision)
	}
	mutex.Lock()
	defer mutex.Unlock()
	if len(requests) == 0 {
		t.Fatal("reviewer never generated")
	}
	for _, body := range requests {
		if !strings.Contains(body["fixturePath"].(string), "gemini-3.8-flash:") {
			t.Fatal("review model changed")
		}
		tools, _ := body["tools"].([]any)
		for _, tool := range tools {
			fields := tool.(map[string]any)
			for key, value := range fields {
				if key != "functionDeclarations" {
					t.Fatalf("advertised tool %s", key)
				}
				if declarations, ok := value.([]any); ok && len(declarations) > 0 {
					t.Fatal("advertised function declarations")
				}
			}
		}
	}
	t.Log("PASS actual official production reviewer: exact canonical3.8 and zero advertised tools on every generation; strict allow result")
}

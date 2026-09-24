package pi

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	acp "github.com/coder/acp-go-sdk"
)

// TestMCPConfigSnapshotPreservesLiteralsAndReplacesCredentials 验证 MCP 字面量、凭据替换和私有快照。
func TestMCPConfigSnapshotPreservesLiteralsAndReplacesCredentials(t *testing.T) {
	directory := t.TempDir()
	extension := filepath.Join(directory, "extension.ts")
	prepare := func(servers []acp.McpServer) error {
		return writeExtension(extension, "/installed package/index.ts", "default", servers)
	}
	secret := "!do-not-run ${HOME} {env:HOME} __MANUAL__ __CONFIG__"
	servers := []acp.McpServer{
		{Http: &acp.McpServerHttpInline{Name: "remote", Url: "http://127.0.0.1/mcp", Headers: []acp.HttpHeader{{Name: "Authorization", Value: secret}}}},
		{Stdio: &acp.McpServerStdio{Name: "local", Command: "node", Args: []string{"literal ${HOME}"}, Env: []acp.EnvVariable{{Name: "TOKEN", Value: secret}}}},
		{Sse: &acp.McpServerSseInline{Name: "events", Url: "http://127.0.0.1/sse"}},
	}
	if err := prepare(servers); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(extension)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, expected := range []string{secret, `"lifecycle":"eager"`, `"scriptMode":false`, `"literalEnv":true`, `"httpTransport":"streamable-http"`, `"httpTransport":"sse"`, `const permissionMode = "default";`} {
		if !strings.Contains(text, expected) {
			t.Fatalf("snapshot lost %q", expected)
		}
	}
	info, _ := os.Stat(extension)
	if info.Mode().Perm() != 0600 {
		t.Fatal("credential file must be private")
	}
	if err := prepare(nil); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(extension)
	if strings.Contains(string(data), secret) {
		t.Fatal("disabled connector credentials survived into the next session")
	}
	if err := prepare(append(servers, servers[0])); err == nil {
		t.Fatal("duplicate MCP identities accepted")
	}
}

// TestPiDiscoveryRequiresHostBridge 验证 Pi 适配器要求宿主提供可读取的绝对模块路径。
func TestPiDiscoveryRequiresHostBridge(t *testing.T) {
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	config := Config{PiPath: binary, Environment: []string{"PATH=/no-pi-acp"}, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if _, err := NewAgent(context.Background(), config); err == nil || !strings.Contains(err.Error(), "MCPModulePath") {
		t.Fatalf("missing bridge must fail clearly: %v", err)
	}
	config.MCPModulePath = "relative/bridge.mjs"
	if _, err := NewAgent(context.Background(), config); err == nil || !strings.Contains(err.Error(), "absolute") {
		t.Fatalf("relative bridge must fail clearly: %v", err)
	}
	config.MCPModulePath = filepath.Join(t.TempDir(), "missing.mjs")
	if _, err := NewAgent(context.Background(), config); err == nil || !strings.Contains(err.Error(), "unavailable") {
		t.Fatalf("missing bridge file must fail clearly: %v", err)
	}
	config.MCPModulePath = filepath.Join(t.TempDir(), "bridge.mjs")
	if err := os.WriteFile(config.MCPModulePath, []byte("export const createMcpAdapter = () => {};"), 0600); err != nil {
		t.Fatal(err)
	}
	agent, err := NewAgent(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	if agent.modulePath != config.MCPModulePath {
		t.Fatal("host bridge path was not retained")
	}
	if err := agent.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(config.MCPModulePath); err != nil {
		t.Fatalf("adapter removed host-owned bridge: %v", err)
	}
}

// TestManagedUserInputUsesStableManualTool 验证受管问答只暴露稳定工具，不触发随机直连工具重启提示。
func TestManagedUserInputUsesStableManualTool(t *testing.T) {
	extension := filepath.Join(t.TempDir(), "extension.ts")
	server := acp.McpServer{Http: &acp.McpServerHttpInline{
		Meta: map[string]any{"acp-go/user-input": true},
		Name: "acp_go_user_input",
		Url:  "http://127.0.0.1/mcp",
	}}
	if err := writeExtension(extension, "/installed package/index.ts", "full-access", []acp.McpServer{server}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(extension)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if strings.Contains(text, `const questionServers = ["acp_go_user_input"];`) {
		t.Fatal("forged user input metadata bypassed the managed endpoint boundary")
	}
	if strings.Contains(text, `"requestTimeoutMs":2147483647`) {
		t.Fatal("forged MCP received the managed tool timeout")
	}
}

// TestPiNotificationDoesNotBecomeAssistantMessage 验证扩展 UI 通知不会污染助手正文。
func TestPiNotificationDoesNotBecomeAssistantMessage(t *testing.T) {
	var output bytes.Buffer
	agent := &Agent{config: Config{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}}
	agent.SetAgentConnection(acp.NewAgentSideConnection(agent, &output, strings.NewReader("")))
	response, err := os.CreateTemp(t.TempDir(), "extension-response-")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Close()
	session := &session{id: "fixture", process: &rpcProcess{input: response}}
	agent.extensionUI(context.Background(), session, map[string]any{
		"id":         "notification-1",
		"method":     "notify",
		"message":    "MCP: direct tools will be available after restart",
		"notifyType": "info",
	})
	if strings.Contains(output.String(), "session/update") || strings.Contains(output.String(), "available after restart") {
		t.Fatalf("extension notification leaked into assistant output: %s", output.String())
	}
}

package pi

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	acp "github.com/coder/acp-go-sdk"
)

// TestMCPConfigSnapshotPreservesLiteralsAndReplacesCredentials 验证 MCP 字面量、凭据替换和私有快照。
func TestMCPConfigSnapshotPreservesLiteralsAndReplacesCredentials(t *testing.T) {
	directory := t.TempDir()
	agent := &Agent{directory: directory, modulePath: "/installed package/index.ts", permissionMode: "default"}
	secret := "!do-not-run ${HOME} {env:HOME} __MANUAL__ __CONFIG__"
	servers := []acp.McpServer{
		{Http: &acp.McpServerHttpInline{Name: "remote", Url: "http://127.0.0.1/mcp", Headers: []acp.HttpHeader{{Name: "Authorization", Value: secret}}}},
		{Stdio: &acp.McpServerStdio{Name: "local", Command: "node", Args: []string{"literal ${HOME}"}, Env: []acp.EnvVariable{{Name: "TOKEN", Value: secret}}}},
		{Sse: &acp.McpServerSseInline{Name: "events", Url: "http://127.0.0.1/sse"}},
	}
	if err := agent.prepareExtension(servers); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(agent.extensionPath())
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, expected := range []string{secret, `"literalEnv":true`, `"httpTransport":"streamable-http"`, `"httpTransport":"sse"`, "const manual = true;"} {
		if !strings.Contains(text, expected) {
			t.Fatalf("snapshot lost %q", expected)
		}
	}
	info, _ := os.Stat(agent.extensionPath())
	if info.Mode().Perm() != 0600 {
		t.Fatal("credential file must be private")
	}
	if err := agent.prepareExtension(nil); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(agent.extensionPath())
	if strings.Contains(string(data), secret) {
		t.Fatal("disabled connector credentials survived into the next session")
	}
	if err := agent.prepareExtension(append(servers, servers[0])); err == nil {
		t.Fatal("duplicate MCP identities accepted")
	}
}

// TestDependencyDiscoveryUsesAdapterInstallation 验证从适配器安装位置发现已安装依赖。
func TestDependencyDiscoveryUsesAdapterInstallation(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(root, "node_modules", ".bin")
	module := filepath.Join(root, "node_modules", "pi-mcp-adapter", "index.ts")
	if err := os.MkdirAll(bin, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(module), 0700); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Join(bin, "pi"), filepath.Join(bin, "pi-acp"), module} {
		if err := os.WriteFile(path, []byte("fixture"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	piPath, modulePath, err := resolveDependencies(filepath.Join(bin, "pi-acp"), Config{Environment: []string{}})
	expectedModule, _ := filepath.EvalSymlinks(module)
	if err != nil || piPath != filepath.Join(bin, "pi") || modulePath != expectedModule {
		t.Fatalf("discovery: %s %s %v", piPath, modulePath, err)
	}
}

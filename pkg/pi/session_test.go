package pi

import (
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
	for _, expected := range []string{secret, `"literalEnv":true`, `"httpTransport":"streamable-http"`, `"httpTransport":"sse"`, `const permissionMode = "default";`} {
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

// TestPiDiscoveryNeedsOnlyPi 验证只安装 Pi 即可构造适配器，MCP 工厂来自 Go 内置资源。
func TestPiDiscoveryNeedsOnlyPi(t *testing.T) {
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	agent, err := NewAgent(context.Background(), Config{PiPath: binary, Environment: []string{"PATH=/no-pi-acp"}, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	module := agent.modulePath
	if data, err := os.ReadFile(module); err != nil || len(data) < 1000 {
		t.Fatalf("missing embedded module: %v", err)
	}
	if err := agent.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(module); !os.IsNotExist(err) {
		t.Fatal("extension not cleaned up")
	}
}

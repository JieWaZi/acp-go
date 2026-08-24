package claude

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	acp "github.com/coder/acp-go-sdk"
)

// TestPrepareLaunchOptionsAndArgs 验证目录、MCP 与创建参数稳定映射。
func TestPrepareLaunchOptionsAndArgs(t *testing.T) {
	cwd := t.TempDir()
	additional := filepath.Join(cwd, "additional")
	if err := os.Mkdir(additional, 0o755); err != nil {
		t.Fatal(err)
	}
	options, err := prepareLaunchOptions(cwd, "session-1", false, true, []string{"additional", additional}, []acp.McpServer{
		{Stdio: &acp.McpServerStdio{
			Name: "local", Command: "server", Args: []string{"--stdio"},
			Env: []acp.EnvVariable{{Name: "TOKEN", Value: "secret-value"}},
		}},
		{Http: &acp.McpServerHttpInline{
			Name: "remote", Url: "https://mcp.example.test", Headers: []acp.HttpHeader{{Name: "Authorization", Value: "secret-header"}},
		}},
	})
	if err != nil {
		t.Fatalf("prepareLaunchOptions() error = %v", err)
	}
	if !reflect.DeepEqual(options.AdditionalDirectories, []string{additional}) {
		t.Fatalf("AdditionalDirectories = %#v", options.AdditionalDirectories)
	}
	var mcp mcpConfigEnvelope
	if err := json.Unmarshal([]byte(options.MCPConfig), &mcp); err != nil || len(mcp.MCPServers) != 2 {
		t.Fatalf("MCPConfig = %q, err=%v", options.MCPConfig, err)
	}
	args := claudeLaunchArgs(options)
	joined := strings.Join(args, "\n")
	for _, required := range []string{
		"--output-format\nstream-json", "--input-format\nstream-json", "--verbose",
		"--permission-prompt-tool\nstdio", "--include-partial-messages", "--replay-user-messages",
		"--session-id=session-1", "--allow-dangerously-skip-permissions", "--add-dir\n" + additional,
	} {
		if !strings.Contains(joined, required) {
			t.Fatalf("args 缺少 %q：%#v", required, args)
		}
	}
}

// TestPrepareLaunchOptionsRejectsInvalidInput 验证坏路径、重复 MCP 名称和不支持传输在启动前失败。
func TestPrepareLaunchOptionsRejectsInvalidInput(t *testing.T) {
	cwd := t.TempDir()
	tests := []struct {
		// name 是子测试名称。
		name string
		// cwd 是待校验工作目录。
		cwd string
		// directories 是待校验附加目录。
		directories []string
		// servers 是待编码 MCP 配置。
		servers []acp.McpServer
		// want 是期望的错误类别。
		want error
	}{
		{name: "相对 cwd", cwd: "relative", want: ErrInvalidWorkingDirectory},
		{name: "空附加目录", cwd: cwd, directories: []string{""}, want: ErrInvalidAdditionalDirectory},
		{name: "空 MCP union", cwd: cwd, servers: []acp.McpServer{{}}, want: ErrInvalidMCPServer},
		{name: "ACP MCP", cwd: cwd, servers: []acp.McpServer{{Acp: &acp.McpServerAcpInline{Name: "x", Id: "id"}}}, want: ErrInvalidMCPServer},
		{name: "非法远程 MCP URL", cwd: cwd, servers: []acp.McpServer{{Http: &acp.McpServerHttpInline{Name: "x", Url: "file:///tmp/mcp"}}}, want: ErrInvalidMCPServer},
		{name: "远程 MCP header 重名", cwd: cwd, servers: []acp.McpServer{{Sse: &acp.McpServerSseInline{Name: "x", Url: "https://mcp.example.test", Headers: []acp.HttpHeader{{Name: "Authorization", Value: "one"}, {Name: "authorization", Value: "two"}}}}}, want: ErrInvalidMCPServer},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := prepareLaunchOptions(test.cwd, "s", false, false, test.directories, test.servers)
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
		})
	}
}

// TestClaudeLaunchArgsResume 验证恢复只使用 resume 身份，不同时传 session-id。
func TestClaudeLaunchArgsResume(t *testing.T) {
	args := strings.Join(claudeLaunchArgs(launchOptions{SessionID: "existing", Resume: true}), " ")
	if !strings.Contains(args, "--resume=existing") || strings.Contains(args, "--session-id") {
		t.Fatalf("resume args = %q", args)
	}
}

// TestClaudeLaunchArgsBypassPermissions 验证 bypass 只在显式允许后进入危险模式。
func TestClaudeLaunchArgsBypassPermissions(t *testing.T) {
	args := strings.Join(claudeLaunchArgs(launchOptions{
		SessionID:                       "session-bypass",
		PermissionMode:                  "bypassPermissions",
		AllowDangerouslySkipPermissions: true,
	}), " ")
	if !strings.Contains(args, "--allow-dangerously-skip-permissions") ||
		!strings.Contains(args, "--permission-mode bypassPermissions") {
		t.Fatalf("bypass args = %q", args)
	}
}

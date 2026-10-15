package workbuddy

import (
	"context"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/JieWaZi/acp-go/pkg/nativeacp"
	acp "github.com/coder/acp-go-sdk"
)

// TestInstalledWorkBuddyProtocol 仅在显式开启时探测已安装 CLI；不发送 Prompt、不调用模型或登录。
func TestInstalledWorkBuddyProtocol(t *testing.T) {
	if os.Getenv("WORKBUDDY_REAL_ACP") != "1" {
		t.Skip("set WORKBUDDY_REAL_ACP=1 for installed CLI protocol checks without model calls")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	environment := nativeacp.WithEnvironment(os.Environ(), "CODEBUDDY_CONFIG_DIR", t.TempDir())
	cwd := t.TempDir()
	config := Config{WorkBuddyPath: os.Getenv("WORKBUDDY_PATH"), Environment: environment, WorkingDirectory: cwd, PermissionMode: "auto", Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	agent, err := NewAgent(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close(context.Background())
	// 未声明宿主文件、终端或表单能力，因此不会触发 UI 或工具操作。
	peer, _ := connectFixture(t, agent, "accept")
	response, err := peer.Initialize(ctx, acp.InitializeRequest{ProtocolVersion: 1})
	if err != nil || !response.AgentCapabilities.LoadSession || response.AgentInfo == nil {
		t.Fatalf("installed initialize: %+v %v", response, err)
	}
	session, err := peer.NewSession(ctx, acp.NewSessionRequest{Cwd: cwd, McpServers: []acp.McpServer{}})
	if err != nil || session.SessionId == "" || session.Modes == nil || session.Modes.CurrentModeId != "auto" {
		t.Fatalf("installed new: %+v %v", session, err)
	}
	if err := agent.Close(ctx); err != nil {
		t.Fatal(err)
	}
	restored, err := NewAgent(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close(context.Background())
	restoredPeer, _ := connectFixture(t, restored, "accept")
	if _, err := restoredPeer.Initialize(ctx, acp.InitializeRequest{ProtocolVersion: 1}); err != nil {
		t.Fatal(err)
	}
	loaded, err := restoredPeer.LoadSession(ctx, acp.LoadSessionRequest{SessionId: session.SessionId, Cwd: cwd, McpServers: []acp.McpServer{}})
	if err != nil || loaded.Modes == nil || loaded.Modes.CurrentModeId != "auto" {
		t.Fatalf("installed load: %+v %v", loaded, err)
	}
	t.Logf("installed CLI initialized, created and cold-loaded a session with auto permissions; config options=%d; no model prompt sent", len(loaded.ConfigOptions))
}

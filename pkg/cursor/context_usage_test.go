package cursor

import (
	"context"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	acp "github.com/coder/acp-go-sdk"
)

// TestParseContextUsage 验证模型名不会干扰页头解析，并覆盖官方 Token 缩写。
func TestParseContextUsage(t *testing.T) {
	for _, test := range []struct {
		// name 标识当前输出格式样例。
		name string
		// screen 是去除终端控制序列后的官方页面。
		screen string
		// used 是期望的当前上下文用量。
		used int
		// size 是期望的上下文窗口容量。
		size int
	}{
		{
			name:   "thousands",
			screen: "Context  composer-2.5  87.4K / 200K  43.7%\nCurrent context usage by category.",
			used:   87_400,
			size:   200_000,
		},
		{
			name:   "millions",
			screen: "Context  claude-opus-4-8[context=1m]  1M / 1M  100.0%\nCurrent context usage by category.",
			used:   1_000_000,
			size:   1_000_000,
		},
		{
			name:   "small",
			screen: "Context  auto  950 / 128K  0.7%\nCurrent context usage by category.",
			used:   950,
			size:   128_000,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			usage, err := parseContextUsage(test.screen)
			if err != nil {
				t.Fatal(err)
			}
			if usage.Used != test.used || usage.Size != test.size {
				t.Fatalf("usage=%+v", usage)
			}
		})
	}
}

// TestParseContextUsageRejectsInvalidOutput 验证缺失窗口或越界用量不会形成伪造事件。
func TestParseContextUsageRejectsInvalidOutput(t *testing.T) {
	for _, screen := range []string{
		"No context usage breakdown to show yet.",
		"Context  auto  201K / 200K  100.0%\nCurrent context usage by category.",
		"Context  auto  unknown / 200K  10.0%\nCurrent context usage by category.",
	} {
		if usage, err := parseContextUsage(screen); err == nil || usage != nil {
			t.Fatalf("invalid output accepted: %+v", usage)
		}
	}
}

// TestCursorRealContextUsage 显式启用时，不调用模型，验证官方 `/context` 的打开和关闭协议。
func TestCursorRealContextUsage(t *testing.T) {
	if os.Getenv("ALLY_CURSOR_REAL_PROBE") != "1" {
		t.Skip("explicit real Cursor probe required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	agent, err := NewAgent(ctx, Config{
		StateDirectory: t.TempDir(),
		Environment:    os.Environ(),
		Logger:         slog.Default(),
	})
	if err != nil {
		t.Fatal(err)
	}
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()
	agent.SetAgentConnection(acp.NewAgentSideConnection(agent, io.Discard, reader))
	defer agent.Close(context.Background())
	if _, err = agent.Initialize(ctx, acp.InitializeRequest{ProtocolVersion: 1}); err != nil {
		t.Fatal(err)
	}
	response, err := agent.NewSession(ctx, acp.NewSessionRequest{Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	session, err := agent.session(response.SessionId)
	if err != nil {
		t.Fatal(err)
	}
	if err = agent.start(ctx, session); err != nil {
		t.Fatal(err)
	}
	usage, err := session.terminal.contextUsage(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if usage != nil && (usage.Used < 0 || usage.Size <= 0 || usage.Used > usage.Size) {
		t.Fatalf("usage=%+v", usage)
	}
	if !terminalReady(session.terminal.text()) {
		t.Fatal("Cursor input did not recover after /context")
	}
}

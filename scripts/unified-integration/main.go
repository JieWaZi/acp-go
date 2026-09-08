// Command unified-integration 只为离线真实 CLI 回归创建显式配置的协议进程。
package main

import (
	"context"
	"log/slog"
	"os"

	"github.com/JieWaZi/acp-go/pkg/acpserver"
	"github.com/JieWaZi/acp-go/pkg/cursor"
	"github.com/JieWaZi/acp-go/pkg/kimi"
	"github.com/JieWaZi/acp-go/pkg/pi"
	acp "github.com/coder/acp-go-sdk"
)

// main 从测试专属环境读取路径和权限，模型凭据由隔离 fixture 提供。
func main() {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	ctx := context.Background()
	cwd, err := os.Getwd()
	if err != nil {
		panic(err)
	}
	var agent acp.Agent
	switch os.Getenv("ACP_TEST_CLI") {
	case "kimi":
		agent, err = kimi.NewAgent(ctx, kimi.Config{KimiPath: os.Getenv("KIMI_PATH"), Logger: logger, WorkingDirectory: cwd, PermissionMode: os.Getenv("ACP_TEST_PERMISSION"), StateDirectory: os.Getenv("ACP_TEST_STATE")})
	case "cursor":
		agent, err = cursor.NewAgent(ctx, cursor.Config{CursorPath: os.Getenv("CURSOR_PATH"), Logger: logger, WorkingDirectory: cwd, PermissionMode: os.Getenv("ACP_TEST_PERMISSION")})
	case "pi":
		agent, err = pi.NewAgent(ctx, pi.Config{PiPath: os.Getenv("PI_PATH"), Logger: logger, WorkingDirectory: cwd, PermissionMode: os.Getenv("ACP_TEST_PERMISSION")})
	default:
		panic("only offline Kimi, Pi and Cursor fixtures are supported")
	}
	if err != nil {
		panic(err)
	}
	server, err := acpserver.NewWithUserInput(agent, os.Stdin, os.Stdout)
	if err != nil {
		panic(err)
	}
	if err := server.Serve(ctx); err != nil {
		panic(err)
	}
}

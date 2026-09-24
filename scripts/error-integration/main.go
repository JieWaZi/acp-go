// Command error-integration 使用真实 CLI 和隔离配置验证故障协议。
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/JieWaZi/acp-go/pkg/acpserver"
	"github.com/JieWaZi/acp-go/pkg/claude"
	"github.com/JieWaZi/acp-go/pkg/codex"
	"github.com/JieWaZi/acp-go/pkg/cursor"
	"github.com/JieWaZi/acp-go/pkg/kimi"
	"github.com/JieWaZi/acp-go/pkg/pi"
	acp "github.com/coder/acp-go-sdk"
)

// main 组装五种真实适配器，并在退出时清理本次测试拥有的子进程。
func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	cwd, err := os.Getwd()
	if err != nil {
		panic(err)
	}
	path := os.Getenv("ACP_ERROR_CLI_PATH")
	var agent acp.Agent
	switch os.Getenv("ACP_ERROR_CLI") {
	case "codex":
		agent, err = codex.NewAgent(ctx, codex.Config{Logger: logger, CodexPath: path})
	case "claude":
		agent, err = claude.NewAgent(ctx, claude.Config{Logger: logger, ClaudePath: path, PrefixArgs: []string{"--bare"}})
	case "cursor":
		agent, err = cursor.NewAgent(ctx, cursor.Config{Logger: logger, CursorPath: path, WorkingDirectory: cwd, StateDirectory: os.Getenv("ACP_ERROR_STATE")})
	case "kimi":
		agent, err = kimi.NewAgent(ctx, kimi.Config{Logger: logger, KimiPath: path, WorkingDirectory: cwd, StateDirectory: os.Getenv("ACP_ERROR_STATE")})
	case "pi":
		agent, err = pi.NewAgent(ctx, pi.Config{Logger: logger, PiPath: path, WorkingDirectory: cwd})
	default:
		panic("unknown test CLI")
	}
	if err != nil {
		panic(err)
	}
	defer func() {
		if closer, ok := agent.(interface {
			// Close 释放适配器及其 CLI 子进程。
			Close(context.Context) error
		}); ok {
			closeCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_ = closer.Close(closeCtx)
		}
	}()
	server, err := acpserver.NewWithUserInput(agent, os.Stdin, os.Stdout)
	if err != nil {
		panic(err)
	}
	if err := server.Serve(ctx); err != nil {
		panic(err)
	}
}

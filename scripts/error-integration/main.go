// Command error-integration projects isolated native clients for wire error fixtures.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/JieWaZi/acp-go/pkg/acpserver"
	"github.com/JieWaZi/acp-go/pkg/agentclient"
	"github.com/JieWaZi/acp-go/pkg/claude"
	"github.com/JieWaZi/acp-go/pkg/codex"
	"github.com/JieWaZi/acp-go/pkg/cursor"
	"github.com/JieWaZi/acp-go/pkg/pi"
	"github.com/JieWaZi/acp-go/pkg/standardacp"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	cwd, err := os.Getwd()
	if err != nil {
		panic(err)
	}
	path := os.Getenv("ACP_ERROR_CLI_PATH")
	agent, err := agentclient.NewACPAgent(func(host agentclient.HostCallbacks) (agentclient.Client, error) {
		switch os.Getenv("ACP_ERROR_CLI") {
		case "codex":
			return codex.NewClient(ctx, codex.Options{Logger: logger, Executable: path, WorkingDirectory: cwd}, host)
		case "claude":
			return claude.NewClient(ctx, claude.Options{Logger: logger, Executable: path, WorkingDirectory: cwd, PrefixArgs: []string{"--bare"}}, host)
		case "cursor":
			return cursor.NewClient(ctx, cursor.Options{Logger: logger, Executable: path, WorkingDirectory: cwd}, host)
		case "kimi":
			return standardacp.NewClient(ctx, standardacp.Options{Provider: standardacp.Kimi, Logger: logger, Executable: path, WorkingDirectory: cwd}, host)
		case "pi":
			return pi.NewClient(ctx, pi.Options{Logger: logger, Executable: path, WorkingDirectory: cwd}, host)
		default:
			panic("unknown test CLI")
		}
	})
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

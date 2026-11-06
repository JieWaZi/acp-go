// Command unified-integration runs an isolated native client behind the ACP wire fixture.
package main

import (
	"context"
	"log/slog"
	"os"

	"github.com/JieWaZi/acp-go/pkg/acpserver"
	"github.com/JieWaZi/acp-go/pkg/agentclient"
	"github.com/JieWaZi/acp-go/pkg/cursor"
	"github.com/JieWaZi/acp-go/pkg/pi"
	"github.com/JieWaZi/acp-go/pkg/standardacp"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	ctx := context.Background()
	cwd, err := os.Getwd()
	if err != nil {
		panic(err)
	}
	permission := os.Getenv("ACP_TEST_PERMISSION")
	agent, err := agentclient.NewACPAgent(func(host agentclient.HostCallbacks) (agentclient.Client, error) {
		switch os.Getenv("ACP_TEST_CLI") {
		case "kimi":
			return standardacp.NewClient(ctx, standardacp.Options{
				Provider: standardacp.Kimi, Executable: os.Getenv("KIMI_PATH"), Logger: logger,
				WorkingDirectory: cwd, PermissionMode: permission,
			}, host)
		case "cursor":
			return cursor.NewClient(ctx, cursor.Options{
				Executable: os.Getenv("CURSOR_PATH"), Logger: logger,
				WorkingDirectory: cwd, PermissionMode: permission,
			}, host)
		case "pi":
			return pi.NewClient(ctx, pi.Options{
				Executable: os.Getenv("PI_PATH"), Logger: logger, WorkingDirectory: cwd,
				PermissionMode: permission, MCPModulePath: os.Getenv("PI_MCP_MODULE_PATH"),
			}, host)
		default:
			panic("only offline Kimi, Pi and Cursor fixtures are supported")
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

// Command acp-agent 通过 stdio 提供可选择 Adapter 的 ACP Agent 服务。
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/JieWaZi/acp-go/pkg/acpserver"
	"github.com/JieWaZi/acp-go/pkg/agentclient"
	"github.com/JieWaZi/acp-go/pkg/claude"
	"github.com/JieWaZi/acp-go/pkg/codex"
	"github.com/JieWaZi/acp-go/pkg/cursor"
	"github.com/JieWaZi/acp-go/pkg/pi"
	"github.com/JieWaZi/acp-go/pkg/qoder"
	"github.com/JieWaZi/acp-go/pkg/standardacp"
	"github.com/JieWaZi/acp-go/pkg/workbuddy"

	acp "github.com/JieWaZi/acp-go-sdk"
)

const (
	codexAdapterName  = "codex"
	claudeAdapterName = "claude"
)

// processIO 隔离协议流与诊断流，防止日志污染 stdout 上的 ACP 帧。
type processIO struct {
	// input 是读取客户端 ACP 请求的 stdin。
	input io.Reader
	// output 是只写 ACP 响应和通知的 stdout。
	output io.Writer
	// diagnostics 是写启动错误与 SDK 日志的 stderr。
	diagnostics io.Writer
}

// commandOptions 保存命令行解析后的启动选项。
type commandOptions struct {
	// adapter 是用户选择的 Adapter 名称；省略时保持 Codex 默认值。
	adapter string
}

// main 建立进程信号上下文，并把操作系统 stdio 显式交给组合根。
func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)

	exitCode := run(
		ctx,
		os.Args[1:],
		processIO{
			input:       os.Stdin,
			output:      os.Stdout,
			diagnostics: os.Stderr,
		},
	)
	// os.Exit 不执行 defer，因此先显式释放 signal.NotifyContext 注册的资源。
	cancel()
	os.Exit(exitCode)
}

// run 执行命令并在唯一的进程边界输出一次错误诊断。
func run(ctx context.Context, args []string, streams processIO) int {
	if err := runAgent(ctx, args, streams); err != nil {
		if _, writeErr := fmt.Fprintf(streams.diagnostics, "acp-agent: %v\n", err); writeErr != nil {
			return 1
		}
		return 1
	}
	return 0
}

// runAgent 解析选项、选择 Adapter，并用 acp-go-sdk 启动 stdio 服务。
func runAgent(ctx context.Context, args []string, streams processIO) error {
	options, err := parseOptions(args)
	if err != nil {
		return fmt.Errorf("parsing options: %w", err)
	}

	logger := slog.New(slog.NewTextHandler(streams.diagnostics, &slog.HandlerOptions{
		Level: slog.LevelWarn,
	}))
	registry, err := newRegistry(logger)
	if err != nil {
		return fmt.Errorf("building adapter registry: %w", err)
	}
	selection, err := registry.Select(ctx, options.adapter)
	if err != nil {
		// Adapter 选择必须先于 SDK connection，保证未知名称不会向 stdout 写协议内容。
		return fmt.Errorf("selecting startup adapter: %w", err)
	}

	server, err := acpserver.NewWithUserInput(selection.Agent, streams.input, streams.output)
	if err != nil {
		return fmt.Errorf("creating protocol server: %w", err)
	}
	if err = server.Serve(ctx); err != nil {
		return fmt.Errorf("serving protocol: %w", err)
	}
	return nil
}

// parseOptions 解析受支持的命令行参数，并拒绝未消费的位置参数。
func parseOptions(args []string) (commandOptions, error) {
	flags := flag.NewFlagSet("acp-agent", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	adapter := flags.String("adapter", codexAdapterName, "ACP Adapter name")
	if err := flags.Parse(args); err != nil {
		return commandOptions{}, err
	}
	if flags.NArg() != 0 {
		return commandOptions{}, fmt.Errorf("unexpected arguments: %s", strings.Join(flags.Args(), " "))
	}

	return commandOptions{adapter: *adapter}, nil
}

// newRegistry selects one native owner and projects it onto the public ACP connection.
func newRegistry(logger *slog.Logger) (*acpserver.Registry, error) {
	registry, err := acpserver.NewRegistry(codexAdapterName)
	if err != nil {
		return nil, err
	}

	standard := func(provider standardacp.Provider) nativeFactory {
		return func(ctx context.Context, executable string, host agentclient.HostCallbacks) (agentclient.Client, error) {
			return standardacp.NewClient(ctx, standardacp.Options{Provider: provider, Executable: executable, Logger: logger}, host)
		}
	}
	registrations := []acpserver.Registration{
		nativeRegistration(codexAdapterName, "CODEX_PATH", func(ctx context.Context, executable string, host agentclient.HostCallbacks) (agentclient.Client, error) {
			return codex.NewClient(ctx, codex.Options{Executable: executable, Logger: logger}, host)
		}),
		nativeRegistration(claudeAdapterName, "CLAUDE_CODE_EXECUTABLE", func(ctx context.Context, executable string, host agentclient.HostCallbacks) (agentclient.Client, error) {
			return claude.NewClient(ctx, claude.Options{Executable: executable, Logger: logger}, host)
		}),
		nativeRegistration("cursor", "CURSOR_PATH", func(ctx context.Context, executable string, host agentclient.HostCallbacks) (agentclient.Client, error) {
			return cursor.NewClient(ctx, cursor.Options{Executable: executable, Logger: logger}, host)
		}),
		nativeRegistration("pi", "PI_PATH", func(ctx context.Context, executable string, host agentclient.HostCallbacks) (agentclient.Client, error) {
			return pi.NewClient(ctx, pi.Options{Executable: executable, Logger: logger, MCPModulePath: os.Getenv("PI_MCP_MODULE_PATH")}, host)
		}),
		nativeRegistration("workbuddy", "WORKBUDDY_PATH", func(ctx context.Context, executable string, host agentclient.HostCallbacks) (agentclient.Client, error) {
			return workbuddy.NewClient(ctx, workbuddy.Options{Executable: executable, Logger: logger}, host)
		}),
		nativeRegistration("qoder", "QODER_PATH", func(ctx context.Context, executable string, host agentclient.HostCallbacks) (agentclient.Client, error) {
			return qoder.NewClient(ctx, qoder.Options{RuntimeID: "qoder", Executable: executable, Logger: logger}, host)
		}),
		nativeRegistration("qoder-cn", "QODER_CN_PATH", func(ctx context.Context, executable string, host agentclient.HostCallbacks) (agentclient.Client, error) {
			return qoder.NewClient(ctx, qoder.Options{RuntimeID: "qoder-cn", Executable: executable, Logger: logger}, host)
		}),
		nativeRegistration("kimi", "KIMI_PATH", standard(standardacp.Kimi)),
		nativeRegistration("opencode", "OPENCODE_PATH", standard(standardacp.OpenCode)),
		nativeRegistration("gemini", "GEMINI_PATH", standard(standardacp.Gemini)),
		nativeRegistration("grok", "GROK_PATH", standard(standardacp.Grok)),
	}
	for _, registration := range registrations {
		if err := registry.Register(registration); err != nil {
			return nil, err
		}
	}
	return registry, nil
}

type nativeFactory func(context.Context, string, agentclient.HostCallbacks) (agentclient.Client, error)

func nativeRegistration(name, executableEnv string, create nativeFactory) acpserver.Registration {
	return acpserver.Registration{Name: name, Factory: func(ctx context.Context) (acp.Agent, error) {
		executable := os.Getenv(executableEnv)
		agent, err := agentclient.NewACPAgent(func(host agentclient.HostCallbacks) (agentclient.Client, error) {
			return create(ctx, executable, host)
		})
		if err != nil && executable != "" {
			return nil, fmt.Errorf("%s=%q: %w", executableEnv, executable, err)
		}
		if err != nil {
			return nil, err
		}
		return agent, nil
	}}
}

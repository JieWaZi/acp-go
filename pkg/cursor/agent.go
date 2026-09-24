// Package cursor 在 Go 内统一官方 ACP 配置、交互终端与会话事件。
package cursor

import (
	"context"
	"errors"
	"log/slog"
	"path/filepath"

	"github.com/JieWaZi/acp-go/pkg/nativeacp"
)

// Config 保存 cursor 的受控启动配置。
type Config struct {
	// StateDirectoryForWorkspace 为分叉目标选择持久状态目录；为空时沿用本 Adapter 的根目录。
	StateDirectoryForWorkspace func(string) string
	// StateDirectory 保存受管 Cursor 会话；空值使用用户缓存目录。
	StateDirectory string
	// CursorPath 是已安装程序的路径；空值使用默认命令。
	CursorPath string
	// PrefixArgs 是协议子命令前的参数。
	PrefixArgs []string
	// Environment 是完整进程环境；nil 时继承宿主。
	Environment []string
	// WorkingDirectory 是外部进程的启动目录。
	WorkingDirectory string
	// Logger 接收进程诊断。
	Logger *slog.Logger
	// PermissionMode 选择原生审批策略：default、auto 或 full-access。
	PermissionMode string
}

// NewAgent 启动 cursor 的现成 ACP 实现。
func NewAgent(ctx context.Context, config Config) (*Agent, error) {
	command := config.CursorPath
	if command == "" {
		command = "cursor-agent"
	}
	if config.CursorPath == "" {
		if _, err := nativeacp.ResolveCommand(command, config.Environment); err != nil {
			command = "agent"
		}
	}
	resolved, err := nativeacp.ResolveCommand(command, config.Environment)
	if err != nil {
		return nil, err
	}
	command, err = filepath.EvalSymlinks(resolved)
	if err != nil {
		return nil, err
	}
	args := append([]string(nil), config.PrefixArgs...)
	switch config.PermissionMode {
	case "", "default":
	case "auto":
		args = append(args, "--auto-review")
	case "full-access":
		args = append(args, "--force")
	default:
		return nil, errors.New("unsupported Cursor permission mode")
	}
	args = append(args, "acp")
	options := newCursorSessionAdapter()
	nativeConfig := nativeacp.Config{
		Command:          command,
		Args:             args,
		Environment:      config.Environment,
		WorkingDirectory: config.WorkingDirectory,
		Logger:           config.Logger,
		CallbackAdapter:  NewCallbackAdapter(),
		SessionAdapter:   options,
		RuntimeName:      "cursor",
		VersionArgs:      append(append([]string{}, config.PrefixArgs...), "--version"),
	}
	return newCursorAgent(ctx, config, nativeConfig, options)
}

// Package pi 复用已安装的 pi ACP 入口，不实现厂商私有协议。
package pi

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"

	"github.com/JieWaZi/acp-go/pkg/nativeacp"
	acp "github.com/coder/acp-go-sdk"
)

// AdapterPackage 记录调查选定的开源适配器版本；本包不会自动安装它。
const AdapterPackage = "pi-acp@0.0.33"

// Config 保存 pi 的受控启动配置。
type Config struct {
	// AdapterPath 是已安装程序的路径；空值使用默认命令。
	AdapterPath string
	// PrefixArgs 是协议子命令前的参数。
	PrefixArgs []string
	// Environment 是完整进程环境；nil 时继承宿主。
	Environment []string
	// WorkingDirectory 是外部进程的启动目录。
	WorkingDirectory string
	// Logger 接收进程诊断。
	Logger *slog.Logger
	// PiPath 是 Pi 可执行文件；为空时从适配器同目录或 PATH 查找。
	PiPath string
	// MCPModulePath 是已安装 pi-mcp-adapter 的 index.ts；为空时查找同一 npm 安装。
	MCPModulePath string
	// PermissionMode 选择 default 逐次审批或 full-access 原生完全访问。
	PermissionMode string
}

// Agent 只拥有 Pi 扩展配置；所有 ACP 和 Pi RPC 仍由现成实现负责。
type Agent struct {
	// Agent 是连接开源 pi-acp 的现有 SDK 桥。
	*nativeacp.Agent
	// mutex 串行化启动，保证每个 Pi 子进程读取完整且独立的配置快照。
	mutex sync.Mutex
	// directory 保存仅该适配器拥有的临时启动文件。
	directory string
	// modulePath 是实际安装的 MCP extension 工厂入口。
	modulePath string
	// permissionMode 是本次进程固定的执行权限。
	permissionMode string
	// snapshots 保存每个原生会话的完整扩展快照，供上游隐式恢复时重新挂载。
	snapshots map[acp.SessionId][]byte
}

// NewAgent 启动 pi 的现成 ACP 实现。
func NewAgent(ctx context.Context, config Config) (*Agent, error) {
	command := config.AdapterPath
	if command == "" {
		command = "pi-acp"
	}
	command, err := nativeacp.ResolveCommand(command, config.Environment)
	if err != nil {
		return nil, err
	}
	piPath, modulePath, err := resolveDependencies(command, config)
	if err != nil {
		return nil, err
	}
	mode := config.PermissionMode
	if mode == "" {
		mode = "default"
	}
	if mode != "default" && mode != "full-access" {
		return nil, fmt.Errorf("unsupported Pi permission mode: %s", mode)
	}
	directory, err := os.MkdirTemp("", "acp-go-pi-")
	if err != nil {
		return nil, err
	}
	launcher, err := writeLauncher(directory, piPath)
	if err != nil {
		_ = os.RemoveAll(directory)
		return nil, err
	}
	environment := nativeacp.WithEnvironment(config.Environment, "PI_ACP_PI_COMMAND", launcher)
	args := append([]string(nil), config.PrefixArgs...)
	upstream, err := nativeacp.NewAgent(ctx, nativeacp.Config{Command: command, Args: args, Environment: environment, WorkingDirectory: config.WorkingDirectory, Logger: config.Logger})
	if err != nil {
		_ = os.RemoveAll(directory)
		return nil, err
	}
	return &Agent{Agent: upstream, directory: directory, modulePath: modulePath, permissionMode: mode, snapshots: make(map[acp.SessionId][]byte)}, nil
}

// Close 先回收上游进程树，再删除包含本次凭据的临时文件。
func (agent *Agent) Close(ctx context.Context) error {
	if err := agent.Agent.Close(ctx); err != nil {
		return err
	}
	agent.mutex.Lock()
	defer agent.mutex.Unlock()
	return os.RemoveAll(agent.directory)
}

// extensionPath 返回 Pi 显式加载的受管扩展路径。
func (agent *Agent) extensionPath() string { return filepath.Join(agent.directory, "extension.ts") }

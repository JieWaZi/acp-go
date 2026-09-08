// Package kimi 复用已安装的 kimi ACP 入口，不实现厂商私有协议。
package kimi

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"

	"github.com/JieWaZi/acp-go/pkg/nativeacp"
	acp "github.com/coder/acp-go-sdk"
)

// Config 保存 kimi 的受控启动配置。
type Config struct {
	// KimiPath 是已安装程序的路径；空值使用默认命令。
	KimiPath string
	// PrefixArgs 是协议子命令前的参数。
	PrefixArgs []string
	// Environment 是完整进程环境；nil 时继承宿主。
	Environment []string
	// WorkingDirectory 是外部进程的启动目录。
	WorkingDirectory string
	// Logger 接收进程诊断。
	Logger *slog.Logger
	// StateDirectory 保存本适配器拥有的 Python Kimi 会话和刷新凭据；空值使用用户缓存目录。
	StateDirectory string
	// PermissionMode 为 Python 实现生成隔离的 default_yolo；TypeScript 使用标准会话模式。
	PermissionMode string
}

// Agent 复用原生连接，并拥有 Python Kimi 的隔离配置快照。
type Agent struct {
	// Agent 是复用 SDK 的原生 ACP 连接。
	*nativeacp.Agent
	// directory 是可删除的本次配置目录，不包含持久会话的真实文件。
	directory string
	// host 是用于发布补充用量通知的标准宿主连接。
	host atomic.Pointer[acp.AgentSideConnection]
	// usagePrompts 阻止同一会话并发提示读取到彼此的统计。
	usagePrompts sync.Map
	// wirePaths 将会话标识映射到受管目录中的官方 wire.jsonl。
	wirePaths sync.Map
	// pythonACP 标识原生问答会被丢弃、必须等待受管工具就绪的 Python 实现。
	pythonACP atomic.Bool
}

// NewAgent 启动 kimi 的现成 ACP 实现。
func NewAgent(ctx context.Context, config Config) (*Agent, error) {
	if ctx == nil || config.Logger == nil {
		return nil, errors.New("Kimi requires context and logger")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if config.PermissionMode != "" && config.PermissionMode != "default" && config.PermissionMode != "auto" && config.PermissionMode != "full-access" {
		return nil, errors.New("unsupported Kimi permission mode")
	}
	cwd, err := filepath.Abs(config.WorkingDirectory)
	if err != nil {
		return nil, err
	}
	config.WorkingDirectory = cwd
	command := config.KimiPath
	if command == "" {
		command = "kimi"
	}
	resolved, err := nativeacp.ResolveCommand(command, config.Environment)
	if err != nil {
		return nil, err
	}
	command = resolved
	config.KimiPath = resolved
	args := append([]string(nil), config.PrefixArgs...)
	args = append(args, "acp")
	directory, environment, err := isolatedEnvironment(config)
	if err != nil {
		return nil, err
	}
	nativeConfig := nativeacp.Config{Command: command, Args: args, Environment: environment, WorkingDirectory: config.WorkingDirectory, Logger: config.Logger}
	if config.PermissionMode == "auto" {
		nativeConfig.LegacyPermissionReviewer, err = permissionReviewer(config, directory, environment)
		if err != nil {
			_ = os.RemoveAll(directory)
			return nil, err
		}
	}
	upstream, err := nativeacp.NewAgent(ctx, nativeConfig)
	if err != nil {
		_ = os.RemoveAll(directory)
		return nil, err
	}
	return &Agent{Agent: upstream, directory: directory}, nil
}

// Close 在原生进程结束后移除本次模型配置，持久会话保留供 load 使用。
func (agent *Agent) Close(ctx context.Context) error {
	if err := agent.Agent.Close(ctx); err != nil {
		return err
	}
	return os.RemoveAll(agent.directory)
}

// Initialize 保留原生握手，并识别需要强制问答工具就绪检查的实现。
func (agent *Agent) Initialize(ctx context.Context, r acp.InitializeRequest) (acp.InitializeResponse, error) {
	response, err := agent.Agent.Initialize(ctx, r)
	if err == nil && response.AgentInfo != nil {
		agent.pythonACP.Store(response.AgentInfo.Name == "Kimi Code CLI")
	}
	return response, err
}

// UserInputRequiresReady 防止 Python ACP 在受管工具加载失败时回到静默空答案。
func (agent *Agent) UserInputRequiresReady() bool { return agent.pythonACP.Load() }

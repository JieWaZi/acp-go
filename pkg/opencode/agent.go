// Package opencode 连接官方原生 ACP CLI。
package opencode

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"slices"

	"github.com/JieWaZi/acp-go/internal/buildinfo"
	"github.com/JieWaZi/acp-go/pkg/nativeacp"
	acp "github.com/coder/acp-go-sdk"
)

// Config 保存受控进程配置。
type Config struct {
	// OpenCodePath 是原生 CLI 可执行文件路径。
	OpenCodePath string
	// PrefixArgs 是协议参数前的受控参数。
	PrefixArgs []string
	// Environment 是完整环境；nil 继承宿主。
	Environment []string
	// WorkingDirectory 是进程启动目录。
	WorkingDirectory string
	// Logger 接收诊断。
	Logger *slog.Logger
	// PermissionMode 是 default、auto 或 full-access。
	PermissionMode string
}

// Agent 复用原生 ACP SDK 连接。
type Agent struct {
	// Agent 拥有原生进程和标准协议。
	*nativeacp.Agent
	// directory 拥有私有自动审批审查工作区。
	directory string
}

// NewAgent 启动官方 CLI。
func NewAgent(ctx context.Context, config Config) (*Agent, error) {
	if ctx == nil || config.Logger == nil {
		return nil, errors.New("OpenCode requires context and logger")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	valid := config.PermissionMode == "" || config.PermissionMode == "default" ||
		config.PermissionMode == "auto" || config.PermissionMode == "full-access"
	if !valid {
		return nil, errors.New("unsupported OpenCode permission mode")
	}
	command := config.OpenCodePath
	if command == "" {
		command = "opencode"
	}
	resolved, err := nativeacp.ResolveCommand(command, config.Environment)
	if err != nil {
		return nil, err
	}
	config.OpenCodePath = resolved
	config.PrefixArgs = slices.Clone(config.PrefixArgs)
	config.Environment = slices.Clone(config.Environment)
	environment := config.Environment
	if config.PermissionMode == "full-access" {
		environment = nativeacp.WithEnvironment(environment, "OPENCODE_PERMISSION", `{"*":"allow"}`)
	}
	if config.PermissionMode == "auto" {
		environment = nativeacp.WithEnvironment(environment, "OPENCODE_PERMISSION", `{"*":"ask"}`)
	}
	args := append([]string{}, config.PrefixArgs...)
	args = append(args, "acp")
	directory := ""
	var adapter nativeacp.PermissionAdapter
	var upstream *nativeacp.Agent
	if config.PermissionMode == "auto" {
		directory, err = os.MkdirTemp("", "acp-go-opencode-review-")
		if err != nil {
			return nil, err
		}
		adapter = nativeacp.NewReviewPermissionAdapter(nativeacp.ReviewPermissionConfig{
			Mode: "auto", Reviewer: permissionReviewer(config, directory),
			CurrentSession: func(id acp.SessionId) (nativeacp.SessionContext, bool) {
				return upstream.CurrentSession(id)
			},
		})
	}
	upstream, err = nativeacp.NewAgent(ctx, nativeacp.Config{
		Command: resolved, Args: args, Environment: environment,
		WorkingDirectory: config.WorkingDirectory, Logger: config.Logger,
		RuntimeName: "opencode", VersionArgs: append(append([]string{}, config.PrefixArgs...), "--version"),
		PromptFIFO: true, StrictCloseSession: true, PermissionAdapter: adapter,
	})
	if err != nil {
		if directory != "" {
			_ = os.RemoveAll(directory)
		}
		return nil, err
	}
	return &Agent{Agent: upstream, directory: directory}, nil
}

// Initialize 区分适配器版本与真实 CLI 版本，并保留原生能力。
func (agent *Agent) Initialize(ctx context.Context, request acp.InitializeRequest) (acp.InitializeResponse, error) {
	response, err := agent.Agent.Initialize(ctx, request)
	if err == nil && response.AgentInfo != nil {
		response.AgentInfo.Version = buildinfo.Current()
		if response.Meta == nil {
			response.Meta = map[string]any{}
		}
		response.Meta["steering"] = map[string]any{"supported": false}
	}
	return response, err
}

// Close 在原生进程结束后删除所有本次审查临时文件。
func (agent *Agent) Close(ctx context.Context) error {
	err := agent.Agent.Close(ctx)
	if agent.directory != "" {
		err = errors.Join(err, os.RemoveAll(agent.directory))
	}
	return err
}

// HandleExtensionMethod 对缺少真实立即确认的 steering 返回明确不支持，其余原生扩展保留。
func (agent *Agent) HandleExtensionMethod(ctx context.Context, method string, params json.RawMessage) (any, error) {
	if method == "_session/steering" {
		return nil, acp.NewMethodNotFound(method)
	}
	return agent.Agent.HandleExtensionMethod(ctx, method, params)
}

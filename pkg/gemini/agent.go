// Package gemini 连接官方原生 ACP CLI。
package gemini

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"

	"github.com/JieWaZi/acp-go/internal/buildinfo"
	"github.com/JieWaZi/acp-go/pkg/nativeacp"
	acp "github.com/coder/acp-go-sdk"
)

// Config 保存受控进程配置。
type Config struct {
	// StateDirectory 保存适配器拥有的持久状态；空值使用按原始 profile 区分的缓存目录。
	StateDirectory string
	// GeminiPath 是原生 CLI 可执行文件路径。
	GeminiPath string
	// PrefixArgs 是协议参数前的受控参数。
	PrefixArgs []string
	// Environment 是完整环境；nil 继承宿主。
	Environment []string
	// WorkingDirectory 是进程启动目录。
	WorkingDirectory string
	// Logger 接收诊断。
	Logger *slog.Logger
	// PermissionMode 当前仅支持 default；其他模式明确返回错误。
	PermissionMode string
}

// Agent 复用原生 ACP SDK 连接。
type Agent struct {
	// Agent 拥有原生进程和标准协议。
	*nativeacp.Agent
	// directory 是本进程拥有的临时用户配置。
	directory string
}

// NewAgent 启动官方 CLI。
func NewAgent(ctx context.Context, config Config) (*Agent, error) {
	if ctx == nil || config.Logger == nil {
		return nil, errors.New("Gemini requires context and logger")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if config.PermissionMode != "" && config.PermissionMode != "default" {
		return nil, errors.New("permission mode not implemented")
	}
	command := config.GeminiPath
	if command == "" {
		command = "gemini"
	}
	resolved, err := nativeacp.ResolveCommand(command, config.Environment)
	if err != nil {
		return nil, err
	}
	command = resolved
	directory, environment, err := thinkingEnvironmentForState(config.Environment, config.StateDirectory)
	if err != nil {
		return nil, err
	}
	args := append([]string{}, config.PrefixArgs...)
	args = append(args, "--acp")
	upstream, err := nativeacp.NewAgent(ctx, nativeacp.Config{
		Command: command, Args: args, Environment: environment,
		WorkingDirectory: config.WorkingDirectory, Logger: config.Logger,
		SessionAdapter: newSessionAdapter(), RuntimeName: "gemini",
		VersionArgs: append(append([]string{}, config.PrefixArgs...), "--version"),
		PromptFIFO:  true, StrictCloseSession: true,
	})
	if err != nil {
		_ = os.RemoveAll(directory)
		return nil, err
	}
	return &Agent{Agent: upstream, directory: directory}, nil
}

// Initialize 保留真实能力，并公开本包实现的无历史回放恢复能力。
func (agent *Agent) Initialize(ctx context.Context, request acp.InitializeRequest) (acp.InitializeResponse, error) {
	response, err := agent.Agent.Initialize(ctx, request)
	if err == nil {
		if response.Meta == nil {
			response.Meta = map[string]any{}
		}
		response.Meta["steering"] = map[string]any{"supported": false}
		if response.AgentInfo != nil {
			response.AgentInfo.Version = buildinfo.Current()
		}
		if response.AgentCapabilities.LoadSession {
			response.AgentCapabilities.SessionCapabilities.Resume = &acp.SessionResumeCapabilities{}
		}
	}
	return response, err
}

// ResumeSession 用官方 load 恢复状态，并屏蔽其历史回放。
func (agent *Agent) ResumeSession(
	ctx context.Context,
	request acp.ResumeSessionRequest,
) (acp.ResumeSessionResponse, error) {
	response, err := agent.LoadSessionConfiguration(ctx, acp.LoadSessionRequest{
		SessionId: request.SessionId, Cwd: request.Cwd, McpServers: request.McpServers, Meta: request.Meta,
	})
	return acp.ResumeSessionResponse{
		Meta: response.Meta, Modes: response.Modes, ConfigOptions: response.ConfigOptions,
	}, err
}

// Close 先回收原生进程，再删除私有配置副本。
func (agent *Agent) Close(ctx context.Context) error {
	return errors.Join(agent.Agent.Close(ctx), os.RemoveAll(agent.directory))
}

// Prompt 保留思考流，并将官方 quota 本轮累计值投影为标准 usage；不构造上下文占用率。
func (agent *Agent) Prompt(ctx context.Context, request acp.PromptRequest) (acp.PromptResponse, error) {
	response, err := agent.Agent.Prompt(ctx, request)
	if err != nil || response.Usage != nil {
		return response, err
	}
	data, marshalErr := json.Marshal(response.Meta["quota"])
	if marshalErr != nil {
		return response, err
	}
	var quota struct {
		// TokenCount 是官方本轮模型调用累计值。
		TokenCount *struct {
			// InputTokens 是本轮输入 token。
			InputTokens int `json:"input_tokens"`
			// OutputTokens 是本轮输出 token。
			OutputTokens int `json:"output_tokens"`
		} `json:"token_count"`
	}
	if json.Unmarshal(data, &quota) == nil && quota.TokenCount != nil {
		input, output := quota.TokenCount.InputTokens, quota.TokenCount.OutputTokens
		if input >= 0 && output >= 0 {
			response.Usage = &acp.Usage{InputTokens: input, OutputTokens: output, TotalTokens: input + output}
		}
	}
	return response, err
}

// Package workbuddy 通过 CodeBuddy Code 的标准 ACP 入口接入 WorkBuddy 执行能力。
package workbuddy

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/JieWaZi/acp-go/internal/buildinfo"
	"github.com/JieWaZi/acp-go/pkg/nativeacp"
	acp "github.com/JieWaZi/acp-go/sdk"
)

// Config 保存标准 ACP 进程的启动事实，不包含 WorkBuddy 内部准入材料。
type Config struct {
	// WorkBuddyPath 是 CodeBuddy Code 的可执行路径；空值依次发现 codebuddy、cbc。
	WorkBuddyPath string
	// PrefixArgs 是固定 ACP 参数前的受控参数。
	PrefixArgs []string
	// Environment 是完整进程环境；nil 继承宿主，空切片不继承。
	Environment []string
	// WorkingDirectory 是原生进程的启动目录。
	WorkingDirectory string
	// Logger 接收独立于协议 stdout 的诊断。
	Logger *slog.Logger
	// PermissionMode 是 default、auto 或 full-access；空值保留原生默认配置。
	PermissionMode string
}

// Agent 复用原生 ACP 的传输、FIFO、取消、配置与进程生命周期。
type Agent struct {
	// Agent 拥有标准 ACP 连接与 CLI 子进程。
	*nativeacp.Agent
	// mode 是每次新建或恢复后必须重新应用的受管权限。
	mode acp.SessionModeId
}

// NewAgent 只启动已安装的标准登录入口，不调用桌面端私有 sidecar。
func NewAgent(ctx context.Context, config Config) (*Agent, error) {
	if ctx == nil || config.Logger == nil {
		return nil, errors.New("WorkBuddy requires context and logger")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	mode := acp.SessionModeId("")
	switch config.PermissionMode {
	case "":
	case "default", "auto":
		mode = acp.SessionModeId(config.PermissionMode)
	case "full-access":
		mode = "bypassPermissions"
	default:
		return nil, errors.New("unsupported WorkBuddy permission mode")
	}
	command := config.WorkBuddyPath
	if command == "" {
		command = "codebuddy"
		if _, err := nativeacp.ResolveCommand(command, config.Environment); err != nil {
			command = "cbc"
		}
	}
	args := append([]string{}, config.PrefixArgs...)
	args = append(args, "--acp")
	if mode != "" {
		args = append(args, "--permission-mode", string(mode))
	}
	upstream, err := nativeacp.NewAgent(ctx, nativeacp.Config{
		Command: command, Args: args, Environment: config.Environment,
		WorkingDirectory: config.WorkingDirectory, Logger: config.Logger,
		RuntimeName: "workbuddy", VersionArgs: append(append([]string{}, config.PrefixArgs...), "--version"),
		PromptFIFO: true, CallbackAdapter: questionAdapter{},
	})
	if err != nil {
		return nil, err
	}
	return &Agent{Agent: upstream, mode: mode}, nil
}

// Initialize 保留实际握手能力，区分适配器构建版本与已安装的 CLI 版本。
func (agent *Agent) Initialize(ctx context.Context, request acp.InitializeRequest) (acp.InitializeResponse, error) {
	response, err := agent.Agent.Initialize(ctx, request)
	if err == nil {
		if response.AgentInfo != nil {
			response.AgentInfo.Version = buildinfo.Current()
		}
		if response.Meta == nil {
			response.Meta = map[string]any{}
		}
		response.Meta["steering"] = map[string]any{"supported": false}
	}
	return response, err
}

// NewSession 在标准建会话后校验并应用本次宿主权限。
func (agent *Agent) NewSession(ctx context.Context, request acp.NewSessionRequest) (acp.NewSessionResponse, error) {
	response, err := agent.Agent.NewSession(ctx, request)
	if err == nil {
		err = agent.applyMode(ctx, response.SessionId, response.Modes)
		if err == nil {
			agent.updateMode(response.Modes, response.ConfigOptions)
		}
	}
	return response, err
}

// LoadSession 恢复原生历史，并防止历史权限覆盖当前执行策略。
func (agent *Agent) LoadSession(ctx context.Context, request acp.LoadSessionRequest) (acp.LoadSessionResponse, error) {
	response, err := agent.Agent.LoadSession(ctx, request)
	if err == nil {
		err = agent.applyMode(ctx, request.SessionId, response.Modes)
		if err == nil {
			agent.updateMode(response.Modes, response.ConfigOptions)
		}
	}
	return response, err
}

// ResumeSession 只转发真实声明的原生恢复，随后重新应用宿主权限。
func (agent *Agent) ResumeSession(ctx context.Context, request acp.ResumeSessionRequest) (acp.ResumeSessionResponse, error) {
	response, err := agent.Agent.ResumeSession(ctx, request)
	if err == nil {
		err = agent.applyMode(ctx, request.SessionId, response.Modes)
		if err == nil {
			agent.updateMode(response.Modes, response.ConfigOptions)
		}
	}
	return response, err
}

// updateMode 让新建或恢复回执反映已由上游确认的当前权限。
func (agent *Agent) updateMode(modes *acp.SessionModeState, options []acp.SessionConfigOption) {
	if agent.mode == "" {
		return
	}
	if modes != nil {
		modes.CurrentModeId = agent.mode
	}
	for _, option := range options {
		if option.Select != nil && option.Select.Category != nil && *option.Select.Category == acp.SessionConfigOptionCategoryMode {
			option.Select.CurrentValue = acp.SessionConfigValueId(agent.mode)
		}
	}
}

// applyMode 要求原生目录明确声明所选权限，不能把缺失的模式视为已经生效。
func (agent *Agent) applyMode(ctx context.Context, sid acp.SessionId, modes *acp.SessionModeState) (err error) {
	if agent.mode == "" {
		return nil
	}
	defer func() {
		if err != nil {
			closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
			defer cancel()
			_, _ = agent.Agent.CloseSession(closeCtx, acp.CloseSessionRequest{SessionId: sid})
		}
	}()
	if modes != nil {
		for _, mode := range modes.AvailableModes {
			if mode.Id == agent.mode {
				_, err = agent.Agent.SetSessionMode(ctx, acp.SetSessionModeRequest{SessionId: sid, ModeId: agent.mode})
				return err
			}
		}
	}
	return acp.NewInvalidParams(map[string]any{"message": "permission mode not advertised by WorkBuddy"})
}

// ProvidesUserInput 声明原生提问已桥接到宿主，不再注入第二份问答 MCP。
func (*Agent) ProvidesUserInput() bool { return true }

// HandleExtensionMethod 保留厂商公共扩展；没有确认语义的插话明确返回不支持。
func (agent *Agent) HandleExtensionMethod(ctx context.Context, method string, params json.RawMessage) (any, error) {
	if method == "_session/steering" {
		return nil, acp.NewMethodNotFound(method)
	}
	return agent.Agent.HandleExtensionMethod(ctx, method, params)
}

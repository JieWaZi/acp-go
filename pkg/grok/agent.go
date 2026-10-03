// Package grok 连接官方原生 ACP CLI。
package grok

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"

	"github.com/JieWaZi/acp-go/internal/buildinfo"
	"github.com/JieWaZi/acp-go/pkg/nativeacp"
	acp "github.com/coder/acp-go-sdk"
)

// Config 保存受控进程配置。
type Config struct {
	// GrokPath 是原生 CLI 可执行文件路径。
	GrokPath string
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
	// permissionMode 保存所选的官方会话权限策略。
	permissionMode string
}

// NewAgent 启动官方 CLI。
func NewAgent(ctx context.Context, config Config) (*Agent, error) {
	command := config.GrokPath
	if command == "" {
		command = "grok"
	}
	if config.PermissionMode != "" && config.PermissionMode != "default" &&
		config.PermissionMode != "auto" && config.PermissionMode != "full-access" {
		return nil, errors.New("unsupported Grok permission mode")
	}
	args := append([]string{}, config.PrefixArgs...)
	args = append(args, "agent", "stdio")
	upstream, err := nativeacp.NewAgent(ctx, nativeacp.Config{
		Command: command, Args: args, Environment: config.Environment,
		WorkingDirectory: config.WorkingDirectory, Logger: config.Logger,
		RuntimeName: "grok", VersionArgs: append(append([]string{}, config.PrefixArgs...), "--version"),
		PromptFIFO: true, StrictCloseSession: true,
	})
	if err != nil {
		return nil, err
	}
	return &Agent{Agent: upstream, permissionMode: config.PermissionMode}, nil
}

// Initialize 区分适配器版本与真实 CLI 版本，并保留原生能力。
func (agent *Agent) Initialize(ctx context.Context, request acp.InitializeRequest) (acp.InitializeResponse, error) {
	response, err := agent.Agent.Initialize(ctx, request)
	if err == nil && response.AgentInfo != nil {
		response.AgentInfo.Version = buildinfo.Current()
		if response.Meta == nil {
			response.Meta = map[string]any{}
		}
		response.Meta["steering"] = map[string]any{"supported": true, "mode": "queued"}
	}
	return response, err
}

// HandleExtensionMethod 将统一插话转交官方 safe-point 缓冲，queued 仅表示接收。
func (agent *Agent) HandleExtensionMethod(ctx context.Context, method string, params json.RawMessage) (any, error) {
	if method != "_session/steering" {
		return agent.Agent.HandleExtensionMethod(ctx, method, params)
	}
	var request struct {
		// SessionID 指向原生会话。
		SessionID string `json:"sessionId"`
		// Prompt 是标准 ACP 文本和图片块。
		Prompt []acp.ContentBlock `json:"prompt"`
	}
	if json.Unmarshal(params, &request) != nil || request.SessionID == "" || len(request.Prompt) == 0 {
		return nil, acp.NewInvalidParams(nil)
	}
	text := strings.Builder{}
	for _, block := range request.Prompt {
		if err := block.Validate(); err != nil {
			return nil, acp.NewInvalidParams(nil)
		}
		if block.Text != nil {
			text.WriteString(block.Text.Text)
			continue
		}
		if block.Image == nil {
			return nil, acp.NewInvalidParams(nil)
		}
	}
	raw, err := agent.CallNative(ctx, "_x.ai/interject", map[string]any{
		"sessionId": request.SessionID, "text": text.String(), "content": request.Prompt,
	})
	if err != nil {
		return nil, err
	}
	var response struct {
		// Status 是上游的接收确认。
		Status string `json:"status"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		return nil, err
	}
	if response.Status != "queued" {
		return nil, errors.New("unexpected Grok interject acknowledgment")
	}
	return map[string]any{"outcome": "queued"}, nil
}

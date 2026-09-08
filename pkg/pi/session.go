package pi

import (
	"context"
	_ "embed"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	acp "github.com/coder/acp-go-sdk"
)

// extensionTemplate 仅组合上游公开 MCP factory 与官方 tool_call 审批 hook。
//
//go:embed extension.ts
var extensionTemplate string

// Initialize 仅在已成功解析 MCP 扩展依赖后声明其真实传输能力。
func (agent *Agent) Initialize(ctx context.Context, request acp.InitializeRequest) (acp.InitializeResponse, error) {
	response, err := agent.Agent.Initialize(ctx, request)
	if err == nil {
		response.AgentCapabilities.McpCapabilities = acp.McpCapabilities{Http: true, Sse: true}
	}
	return response, err
}

// NewSession 在 pi-acp 启动该会话的 Pi 进程前生成扩展快照。
func (agent *Agent) NewSession(ctx context.Context, request acp.NewSessionRequest) (acp.NewSessionResponse, error) {
	agent.mutex.Lock()
	defer agent.mutex.Unlock()
	if err := agent.prepareExtension(request.McpServers); err != nil {
		return acp.NewSessionResponse{}, err
	}
	response, err := agent.Agent.NewSession(ctx, request)
	if err == nil {
		err = agent.rememberSnapshot(response.SessionId)
	}
	return response, err
}

// LoadSession 保留 Pi 历史，并为恢复后的新子进程挂载最新 MCP 配置。
func (agent *Agent) LoadSession(ctx context.Context, request acp.LoadSessionRequest) (acp.LoadSessionResponse, error) {
	agent.mutex.Lock()
	defer agent.mutex.Unlock()
	if err := agent.prepareExtension(request.McpServers); err != nil {
		return acp.LoadSessionResponse{}, err
	}
	response, err := agent.Agent.LoadSession(ctx, request)
	if err == nil {
		err = agent.rememberSnapshot(request.SessionId)
	}
	return response, err
}

// rememberSnapshot 在上游完成加载后记录实际使用的不可变扩展内容。
func (agent *Agent) rememberSnapshot(id acp.SessionId) error {
	data, err := os.ReadFile(agent.extensionPath())
	if err != nil {
		return err
	}
	agent.snapshots[id] = data
	return nil
}

// restoreSnapshot 为 pi-acp 的隐式会话恢复入口挂载正确配置。
func (agent *Agent) restoreSnapshot(id acp.SessionId) error {
	data, ok := agent.snapshots[id]
	if !ok {
		return acp.NewInvalidParams(map[string]any{"message": "load the Pi session before using it"})
	}
	return os.WriteFile(agent.extensionPath(), data, 0600)
}

// Prompt 串行复用上游单活跃会话模型，防止隐式恢复读取其他会话的凭据。
func (agent *Agent) Prompt(ctx context.Context, request acp.PromptRequest) (acp.PromptResponse, error) {
	agent.mutex.Lock()
	defer agent.mutex.Unlock()
	if err := agent.restoreSnapshot(request.SessionId); err != nil {
		return acp.PromptResponse{}, err
	}
	return agent.Agent.Prompt(ctx, request)
}

// SetSessionMode 为上游可能隐式恢复的思考模式切换挂载原会话快照。
func (agent *Agent) SetSessionMode(ctx context.Context, request acp.SetSessionModeRequest) (acp.SetSessionModeResponse, error) {
	agent.mutex.Lock()
	defer agent.mutex.Unlock()
	if err := agent.restoreSnapshot(request.SessionId); err != nil {
		return acp.SetSessionModeResponse{}, err
	}
	return agent.Agent.SetSessionMode(ctx, request)
}

// SetSessionConfigOption 在模型或推理选择触发隐式恢复前恢复正确配置。
func (agent *Agent) SetSessionConfigOption(ctx context.Context, request acp.SetSessionConfigOptionRequest) (acp.SetSessionConfigOptionResponse, error) {
	agent.mutex.Lock()
	defer agent.mutex.Unlock()
	if request.ValueId == nil {
		return acp.SetSessionConfigOptionResponse{}, acp.NewInvalidParams(nil)
	}
	if err := agent.restoreSnapshot(request.ValueId.SessionId); err != nil {
		return acp.SetSessionConfigOptionResponse{}, err
	}
	return agent.Agent.SetSessionConfigOption(ctx, request)
}

// CloseSession 释放内存中的凭据快照，历史仍由 Pi 自身拥有。
func (agent *Agent) CloseSession(ctx context.Context, request acp.CloseSessionRequest) (acp.CloseSessionResponse, error) {
	agent.mutex.Lock()
	defer agent.mutex.Unlock()
	delete(agent.snapshots, request.SessionId)
	return agent.Agent.CloseSession(ctx, request)
}

// prepareExtension 把标准 ACP 服务转换为现有 MCP 库接受的配置形状。
func (agent *Agent) prepareExtension(servers []acp.McpServer) error {
	entries := make(map[string]any, len(servers))
	for _, server := range servers {
		var name string
		entry := map[string]any{"auth": false, "oauth": false}
		switch {
		case server.Stdio != nil:
			value := server.Stdio
			name = value.Name
			env := map[string]string{}
			for _, variable := range value.Env {
				env[variable.Name] = variable.Value
			}
			entry["command"], entry["args"], entry["env"], entry["literalEnv"] = value.Command, value.Args, env, true
		case server.Http != nil:
			value := server.Http
			name = value.Name
			entry["url"], entry["httpTransport"], entry["headers"] = value.Url, "streamable-http", headerMap(value.Headers)
		case server.Sse != nil:
			value := server.Sse
			name = value.Name
			entry["url"], entry["httpTransport"], entry["headers"] = value.Url, "sse", headerMap(value.Headers)
		default:
			return acp.NewInvalidParams(nil)
		}
		if name == "" {
			return acp.NewInvalidParams(nil)
		}
		if _, exists := entries[name]; exists {
			return acp.NewInvalidParams(nil)
		}
		entries[name] = entry
	}
	config := map[string]any{"mcpServers": entries, "settings": map[string]any{"autoAuth": false, "directTools": false, "approveTools": agent.permissionMode != "full-access"}}
	encoded, err := json.Marshal(config)
	if err != nil {
		return err
	}
	module, _ := json.Marshal(filepath.ToSlash(agent.modulePath))
	source := strings.NewReplacer("__MCP_MODULE__", string(module), "__CONFIG__", string(encoded), "__MANUAL__", map[bool]string{true: "true", false: "false"}[agent.permissionMode != "full-access"]).Replace(extensionTemplate)
	return os.WriteFile(agent.extensionPath(), []byte(source), 0600)
}

// headerMap 保留 ACP 请求头的原始值，扩展负责避免上游表达式解释。
func headerMap(headers []acp.HttpHeader) map[string]string {
	result := make(map[string]string, len(headers))
	for _, header := range headers {
		result[header.Name] = header.Value
	}
	return result
}

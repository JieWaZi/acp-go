package codex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode"

	"github.com/JieWaZi/acp-go/pkg/codex/protocol"
	"github.com/JieWaZi/acp-go/pkg/userinput"
	acp "github.com/coder/acp-go-sdk"
)

const disableMCPConfigFilteringEnv = "DISABLE_MCP_CONFIG_FILTERING"

// defaultModeRequestUserInputFeature 是 Codex 普通协作模式下结构化提问的功能开关。
const defaultModeRequestUserInputFeature = "default_mode_request_user_input"

// sessionConfig 合并工作范围、调用方提问默认策略与 ACP server，并返回清洗后的 MCP 请求名称。
// 同名用户或项目配置默认保留，避免 Codex 深合并不同 transport 字段。
func (a *Agent) sessionConfig(
	ctx context.Context,
	workspace codexWorkspace,
	servers []acp.McpServer,
) (map[string]json.RawMessage, []string, error) {
	config, err := codexWorkspaceConfig(workspace)
	if err != nil {
		return nil, nil, err
	}
	existing := map[string]struct{}{}
	filterMCP := len(servers) > 0 && a.getenv(disableMCPConfigFilteringEnv) != "true"
	if a.defaultModeRequestUserInput || filterMCP {
		includeLayers := true
		cwd := workspace.CWD
		response, err := a.client.ConfigRead(ctx, protocol.ConfigReadParams{Cwd: &cwd, IncludeLayers: &includeLayers})
		if err != nil {
			return nil, nil, fmt.Errorf("reading Codex session config: %w", err)
		}
		if a.defaultModeRequestUserInput {
			if err := applyDefaultModeRequestUserInput(config, response.Config); err != nil {
				return nil, nil, err
			}
		}
		if filterMCP {
			existing = configuredMCPServerNames(response)
		}
	}

	requestedNames := make([]string, 0, len(servers))
	seenNames := make(map[string]struct{}, len(servers))
	configured := make(map[string]json.RawMessage, len(servers))
	for _, server := range servers {
		name, config, err := codexMCPServerConfig(server)
		if err != nil {
			return nil, nil, err
		}
		if _, seen := seenNames[name]; !seen {
			requestedNames = append(requestedNames, name)
			seenNames[name] = struct{}{}
		}
		if _, conflict := existing[name]; conflict {
			continue
		}
		configured[name] = config
	}
	if len(configured) == 0 {
		return config, requestedNames, nil
	}

	rawServers, err := json.Marshal(configured)
	if err != nil {
		return nil, nil, fmt.Errorf("encoding Codex MCP config: %w", err)
	}
	config["mcp_servers"] = rawServers
	return config, requestedNames, nil
}

// applyDefaultModeRequestUserInput 只在上游有效配置未声明 feature 时补调用方默认值。
// config/read 序列化的是 ConfigToml，features 不注入运行时默认布尔值；按键存在性保留显式 false。
// 使用点路径覆盖单项，避免替换其他 features；读取失败时绝不猜测用户未配置。
func applyDefaultModeRequestUserInput(config map[string]json.RawMessage, effective json.RawMessage) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(effective, &fields); err != nil {
		return fmt.Errorf("decoding Codex effective config: %w", err)
	}
	if fields == nil {
		return errors.New("decoding Codex effective config: expected object")
	}
	features := map[string]json.RawMessage{}
	if raw, ok := fields["features"]; ok {
		if err := json.Unmarshal(raw, &features); err != nil {
			return fmt.Errorf("decoding Codex features config: %w", err)
		}
	}
	if _, configured := features[defaultModeRequestUserInputFeature]; !configured {
		config["features."+defaultModeRequestUserInputFeature] = json.RawMessage(`true`)
	}
	return nil
}

// codexMCPServerConfig 实现 codex-acp 的 transport 映射；stdio 是 ACP 必选能力，HTTP 显式声明支持。
func codexMCPServerConfig(server acp.McpServer) (string, json.RawMessage, error) {
	variants := 0
	if server.Stdio != nil {
		variants++
	}
	if server.Http != nil {
		variants++
	}
	if server.Sse != nil {
		variants++
	}
	if server.Acp != nil {
		variants++
	}
	if variants != 1 {
		return "", nil, fmt.Errorf("MCP server must contain exactly one transport, got %d", variants)
	}

	var name string
	var value any
	switch {
	case server.Stdio != nil:
		name = sanitizeMCPServerName(server.Stdio.Name)
		if server.Stdio.Command == "" {
			return "", nil, fmt.Errorf("MCP stdio server %q command cannot be empty", name)
		}
		env := make(map[string]string, len(server.Stdio.Env))
		for _, entry := range server.Stdio.Env {
			env[entry.Name] = entry.Value
		}
		value = struct {
			// Command 是启动 MCP server 的可执行文件。
			Command string `json:"command"`
			// Args 是原序传递的命令参数。
			Args []string `json:"args"`
			// Env 是按名称合并后的环境变量。
			Env map[string]string `json:"env"`
		}{Command: server.Stdio.Command, Args: append([]string{}, server.Stdio.Args...), Env: env}
	case server.Http != nil:
		name = sanitizeMCPServerName(server.Http.Name)
		if server.Http.Url == "" {
			return "", nil, fmt.Errorf("MCP HTTP server %q URL cannot be empty", name)
		}
		headers := make(map[string]string, len(server.Http.Headers))
		for _, header := range server.Http.Headers {
			headers[header.Name] = header.Value
		}
		value = struct {
			// URL 是 streamable HTTP endpoint。
			URL string `json:"url"`
			// HTTPHeaders 是按名称合并后的请求头。
			HTTPHeaders map[string]string `json:"http_headers"`
		}{URL: server.Http.Url, HTTPHeaders: headers}
	case server.Sse != nil:
		return "", nil, fmt.Errorf("MCP server %q uses unsupported SSE transport", server.Sse.Name)
	default:
		return "", nil, fmt.Errorf("MCP server %q uses unsupported ACP transport", server.Acp.Name)
	}
	if name == "" {
		return "", nil, fmt.Errorf("MCP server name cannot be empty")
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return "", nil, fmt.Errorf("encoding MCP server %q: %w", name, err)
	}
	if userinput.IsServer(server) {
		var fields map[string]any
		if err := json.Unmarshal(raw, &fields); err != nil {
			return "", nil, err
		}
		fields["tool_timeout_sec"] = 2147483
		raw, err = json.Marshal(fields)
	}
	return name, raw, err
}

// sanitizeMCPServerName 与 codex-acp 一致，把 Unicode 空白替换为下划线。
func sanitizeMCPServerName(name string) string {
	return strings.Map(func(value rune) rune {
		if unicode.IsSpace(value) {
			return '_'
		}
		return value
	}, name)
}

// configuredMCPServerNames 同时检查 effective config 与各配置层中的 mcp_servers 键。
func configuredMCPServerNames(response protocol.ConfigReadResponse) map[string]struct{} {
	result := make(map[string]struct{})
	collectMCPServerNames(response.Config, result)
	for _, layer := range response.Layers {
		collectMCPServerNames(layer.Config, result)
	}
	return result
}

// collectMCPServerNames 从单个有效配置或配置层中收集 MCP server key。
func collectMCPServerNames(raw json.RawMessage, result map[string]struct{}) {
	var config map[string]json.RawMessage
	if len(raw) == 0 || json.Unmarshal(raw, &config) != nil {
		return
	}
	var servers map[string]json.RawMessage
	if json.Unmarshal(config["mcp_servers"], &servers) != nil {
		return
	}
	for name := range servers {
		result[name] = struct{}{}
	}
}

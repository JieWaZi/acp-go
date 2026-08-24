package codex

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"unicode"

	"acp-go/agents/codex/protocol"
	acp "github.com/coder/acp-go-sdk"
)

const disableMCPConfigFilteringEnv = "DISABLE_MCP_CONFIG_FILTERING"

// sessionMCPConfig 把 ACP stdio/http server 转为 Codex thread config，并返回清洗后的请求名称。
// 同名用户或项目配置默认保留，避免 Codex 深合并不同 transport 字段。
func (a *Agent) sessionMCPConfig(
	ctx context.Context,
	cwd string,
	servers []acp.McpServer,
) (map[string]json.RawMessage, []string, error) {
	if len(servers) == 0 {
		return nil, nil, nil
	}

	existing := map[string]struct{}{}
	if os.Getenv(disableMCPConfigFilteringEnv) != "true" {
		includeLayers := true
		response, err := a.client.ConfigRead(ctx, protocol.ConfigReadParams{Cwd: &cwd, IncludeLayers: &includeLayers})
		if err != nil {
			return nil, nil, fmt.Errorf("reading Codex config for MCP conflicts: %w", err)
		}
		existing = configuredMCPServerNames(response)
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
		return nil, requestedNames, nil
	}

	rawServers, err := json.Marshal(configured)
	if err != nil {
		return nil, nil, fmt.Errorf("encoding Codex MCP config: %w", err)
	}
	return map[string]json.RawMessage{"mcp_servers": rawServers}, requestedNames, nil
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
	return name, raw, nil
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

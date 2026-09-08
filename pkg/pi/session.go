package pi

import (
	_ "embed"
	"encoding/json"
	acp "github.com/coder/acp-go-sdk"
	"os"
	"path/filepath"
	"strings"
)

// extensionTemplate 组合现有 MCP 工厂与官方审批 hook。
//
//go:embed extension.ts
var extensionTemplate string

// prepareExtension 把标准 ACP 服务转换为现有 MCP 库接受的配置形状。
func writeExtension(path, modulePath, permissionMode string, servers []acp.McpServer) error {
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
	config := map[string]any{"mcpServers": entries, "settings": map[string]any{"autoAuth": false, "directTools": false, "approveTools": permissionMode != "full-access"}}
	encoded, err := json.Marshal(config)
	if err != nil {
		return err
	}
	module, _ := json.Marshal(filepath.ToSlash(modulePath))
	source := strings.NewReplacer("__MCP_MODULE__", string(module), "__CONFIG__", string(encoded), "__MANUAL__", map[bool]string{true: "true", false: "false"}[permissionMode != "full-access"]).Replace(extensionTemplate)
	return os.WriteFile(path, []byte(source), 0600)
}

// headerMap 保留 ACP 请求头的原始值，扩展负责避免上游表达式解释。
func headerMap(headers []acp.HttpHeader) map[string]string {
	result := make(map[string]string, len(headers))
	for _, header := range headers {
		result[header.Name] = header.Value
	}
	return result
}

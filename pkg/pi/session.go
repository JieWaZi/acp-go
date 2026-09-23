package pi

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/JieWaZi/acp-go/pkg/userinput"
	acp "github.com/coder/acp-go-sdk"
)

// extensionTemplate 组合现有 MCP 工厂与官方审批 hook。
//
//go:embed extension.ts
var extensionTemplate string

// prepareExtension 把标准 ACP 服务转换为现有 MCP 库接受的配置形状。
func writeExtension(path, modulePath, permissionMode string, servers []acp.McpServer) error {
	entries := make(map[string]any, len(servers))
	questions := []string{}
	for _, server := range servers {
		var name string
		// ACP 注入的服务必须在首轮工具检索前完成连接，不能依赖 Pi 的本地元数据缓存。
		entry := map[string]any{"auth": false, "oauth": false, "lifecycle": "eager"}
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
		if userinput.IsServer(server) {
			entry["requestTimeoutMs"] = 2147483647
			entry["approveTools"] = false
			questions = append(questions, name)
		}
		entries[name] = entry
	}
	config := map[string]any{"mcpServers": entries, "settings": map[string]any{"autoAuth": false, "directTools": false, "scriptMode": false, "approveTools": permissionMode == "default"}}
	encoded, err := json.Marshal(config)
	if err != nil {
		return err
	}
	module, _ := json.Marshal(filepath.ToSlash(modulePath))
	questionTools, _ := json.Marshal(questions)
	source := strings.NewReplacer("__QUESTION_TOOLS__", string(questionTools), "__MCP_MODULE__", string(module), "__CONFIG__", string(encoded), "__PERMISSION_MODE__", fmt.Sprintf("%q", permissionMode)).Replace(extensionTemplate)
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

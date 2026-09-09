package cursor

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/JieWaZi/acp-go/pkg/nativeacp"
	acp "github.com/coder/acp-go-sdk"
)

// hookTransport 只负责传输白名单事件；会话、用量与结束语义由 Go 解释。
const hookTransport = `const fs = require('node:fs');
const path = require('node:path');
const crypto = require('node:crypto');
let data = '';
process.stdin.setEncoding('utf8');
process.stdin.on('data', chunk => { data += chunk; if (data.length > 16777216) process.exit(1); });
process.stdin.on('end', () => {
  try {
    const event = JSON.parse(data), result = {};
    if (process.argv[3] && event.conversation_id !== process.argv[3]) { process.stdout.write('{}'); return; }
    for (const key of ['hook_event_name','conversation_id','generation_id','status','input_tokens','output_tokens','cache_read_tokens','cache_write_tokens','context_tokens','context_window_size']) {
      if (Object.hasOwn(event,key)) result[key] = event[key];
    }
    const destination = path.join(process.argv[2], crypto.randomUUID()+'.json');
    fs.writeFileSync(destination+'.tmp', JSON.stringify(result), {mode:0o600});
    fs.renameSync(destination+'.tmp', destination);
    process.stdout.write('{}');
  } catch { process.exitCode = 1; }
});
`

// hookEvent 保留官方每代生成的结束用量，不保存邮件、凭据或完整提示。
type hookEvent struct {
	// Name 是官方 Hook 事件名称。
	Name string `json:"hook_event_name"`
	// Session 是精确的原生会话标识。
	Session string `json:"conversation_id"`
	// Generation 用于同一轮内去重，恢复时不复用旧轮累计值。
	Generation string `json:"generation_id"`
	// Status 是官方结束状态。
	Status string `json:"status"`
	// Input 包含缓存读取和写入的输入总量；nil 表示未提供。
	Input *int `json:"input_tokens"`
	// Output 是生成输出量；nil 表示未提供。
	Output *int `json:"output_tokens"`
	// CacheRead 是读取缓存的输入量。
	CacheRead int `json:"cache_read_tokens"`
	// CacheWrite 是写入缓存的输入量。
	CacheWrite int `json:"cache_write_tokens"`
	// ContextTokens 是压缩前官方上下文快照。
	ContextTokens int `json:"context_tokens"`
	// ContextWindow 是该快照对应的上下文容量。
	ContextWindow int `json:"context_window_size"`
}

// usage 校验官方用量并拆分非缓存输入，缺失值不伪造为零。
func (event hookEvent) usage() *acp.Usage {
	if event.Input == nil || event.Output == nil || *event.Input < 0 || *event.Output < 0 || event.CacheRead < 0 || event.CacheWrite < 0 || event.CacheRead > *event.Input || event.CacheWrite > *event.Input-event.CacheRead {
		return nil
	}
	return &acp.Usage{InputTokens: max(0, *event.Input-event.CacheRead-event.CacheWrite), OutputTokens: *event.Output, CachedReadTokens: acp.Ptr(event.CacheRead), CachedWriteTokens: acp.Ptr(event.CacheWrite), TotalTokens: *event.Input + *event.Output}
}

// createPlugin 生成私有 MCP 插件和 Hook 脚本；项目 Hook 的临时登记由独立租约负责。
func createPlugin(directory, command string, environment []string, servers []acp.McpServer, sid acp.SessionId) (string, error) {
	plugin := filepath.Join(directory, "plugin")
	events := filepath.Join(directory, "events")
	for _, path := range []string{filepath.Join(plugin, ".cursor-plugin"), events} {
		if err := os.MkdirAll(path, 0700); err != nil {
			return "", err
		}
	}
	resolved, err := nativeacp.ResolveCommand(command, environment)
	if err != nil {
		return "", err
	}
	resolved, err = filepath.EvalSymlinks(resolved)
	if err != nil {
		return "", err
	}
	node := filepath.Join(filepath.Dir(resolved), "node")
	if stat, statErr := os.Stat(node); statErr != nil || stat.IsDir() {
		node, err = nativeacp.ResolveCommand("node", environment)
		if err != nil {
			return "", errors.New("Cursor bundled Node runtime unavailable")
		}
	}
	script := filepath.Join(directory, "hook.cjs")
	if err = os.WriteFile(script, []byte(hookTransport), 0600); err != nil {
		return "", err
	}
	quote := func(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'" }
	hook := map[string]any{"command": quote(node) + " " + quote(script) + " " + quote(events) + " " + quote(string(sid))}
	hooks := map[string]any{"version": 1, "hooks": map[string]any{"stop": []any{hook}, "preCompact": []any{hook}}}
	mcp := map[string]any{}
	for _, server := range servers {
		switch {
		case server.Stdio != nil:
			value := server.Stdio
			env := map[string]string{}
			for _, item := range value.Env {
				env[item.Name] = item.Value
			}
			mcp[value.Name] = map[string]any{"command": value.Command, "args": value.Args, "env": env}
		case server.Http != nil:
			value := server.Http
			headers := map[string]string{}
			for _, item := range value.Headers {
				headers[item.Name] = item.Value
			}
			mcp[value.Name] = map[string]any{"url": value.Url, "headers": headers}
		case server.Sse != nil:
			return "", errors.New("Cursor interactive MCP requires stdio or HTTP transport")
		default:
			return "", errors.New("invalid MCP server")
		}
	}
	for path, value := range map[string]any{filepath.Join(plugin, ".cursor-plugin/plugin.json"): map[string]any{"name": "acp-go-cursor-session", "version": "1.0.0", "mcpServers": "./mcp.json"}, filepath.Join(directory, "hooks.json"): hooks, filepath.Join(plugin, "mcp.json"): map[string]any{"mcpServers": mcp}} {
		data, marshalErr := json.Marshal(value)
		if marshalErr != nil {
			return "", marshalErr
		}
		if err = os.WriteFile(path, data, 0600); err != nil {
			return "", err
		}
	}
	return plugin, nil
}

// readHooks 消费已经原子提交的 Hook 文件；删除旧轮事件使恢复不会重复计费。
func readHooks(ctx context.Context, directory string) ([]hookEvent, error) {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return nil, err
	}
	events := []hookEvent{}
	for _, entry := range entries {
		if err = ctx.Err(); err != nil {
			return nil, err
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(directory, entry.Name())
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil, readErr
		}
		var event hookEvent
		if json.Unmarshal(data, &event) != nil {
			return nil, errors.New("invalid Cursor hook event")
		}
		if err = os.Remove(path); err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	return events, nil
}

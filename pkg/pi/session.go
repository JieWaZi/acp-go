package pi

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/JieWaZi/acp-go/pkg/userinput"
	acp "github.com/coder/acp-go-sdk"
)

// extensionTemplate 在会话目录生成 Pi 官方加载器使用的临时 TypeScript 扩展。
const extensionTemplate = `// Composes pi-mcp-adapter@2.32.1 with Pi's official tool_call hook.
import { createMcpAdapter, classifyAction, createReviewSubject, completeSimple } from __MCP_MODULE__;

const config = __CONFIG__;
const permissionMode = __PERMISSION_MODE__;
const questionServers = __QUESTION_TOOLS__;
let literalIndex = 0;
function literal(value: string): string {
  const key = "ACP_GO_PI_LITERAL_" + process.pid + "_" + literalIndex++;
  process.env[key] = value;
  // This is the library's final interpolation pass: inserted values are not re-parsed.
  return "{env:" + key + "}";
}
for (const server of Object.values(config.mcpServers) as any[]) {
  if (server.headers) {
    for (const key of Object.keys(server.headers)) server.headers[key] = literal(server.headers[key]);
  }
  if (server.args) server.args = server.args.map(literal);
}

export default async function (pi: any) {
  let mcpTool: any;
  await createMcpAdapter({ config })({
    ...pi,
    registerTool(tool: any) {
      if (tool.name === "mcp") mcpTool = tool;
      pi.registerTool(tool);
    },
  });
  if (questionServers.length) {
    if (!mcpTool) throw new Error("Managed user input requires the MCP tool factory");
    // Register before Pi's first model request; upstream lazy discovery may otherwise
    // expose a new direct MCP tool only on the following turn.
    pi.registerTool({
      name: "AskUserQuestion", label: "Ask user", description: "Ask the user a question and wait for their answer. Available in every permission mode.",
      parameters: {type:"object",properties:{questions:{type:"array",minItems:1,maxItems:8,items:{type:"object",properties:{question:{type:"string"},header:{type:"string"},multiSelect:{type:"boolean"},options:{type:"array",items:{type:"object",properties:{label:{type:"string"},description:{type:"string"}},required:["label"]}}},required:["question"]}}},required:["questions"]},
      execute(id: string, args: any, signal: AbortSignal, update: any, ctx: any) {
        return mcpTool.execute(id, {server: questionServers[0], tool: "AskUserQuestion", args}, signal, update, ctx);
      },
    });
  }
  // Forward Pi's documented context snapshot; conversion stays in the Go adapter.
  const reportContext = (_event: any, ctx: any) => {
    const usage = ctx.getContextUsage();
    if (usage && Number.isFinite(usage.tokens) && usage.tokens >= 0 && usage.contextWindow > 0) {
      ctx.ui.setStatus("acp-go.context-usage", JSON.stringify(usage));
    }
  };
  pi.on("turn_end", reportContext);
  pi.on("session_start", reportContext);
  pi.on("session_compact", reportContext);
  // Pi may answer get_state even when an extension import failed. Signal only
  // after the bridge and all session hooks have registered successfully.
  pi.on("session_start", (_event: any, ctx: any) => {
    ctx.ui.setStatus("acp-go.extension-ready", "ready");
  });
  if (permissionMode === "full-access") return;
  // MCP tools use the library's own approval gate, including direct and resource tools.
  // Other tools use Pi's documented pre-execution hook, following the upstream gate example.
  pi.on("tool_call", async (event: any, ctx: any) => {
    if (questionServers.length && (event.toolName === "AskUserQuestion" || questionServers.some(name => event.toolName === name + "_AskUserQuestion"))) return;
    // Default MCP approvals stay in the upstream adapter, immediately before calls.
    if (permissionMode === "default" && ["mcp", "mcpScript"].includes(event.toolName)) return;
    if (permissionMode === "auto") {
      try {
        // Classify the exact action, including workspace writes and MCP payloads.
        // External readOnlyHint values and cached earlier decisions cannot bypass this check.
        const decision = await classifyAction(ctx, {
          enabled: true, mode: "fallback", classifierModel: null,
          classifierTimeoutSeconds: 90, approvalTimeoutSeconds: 0,
          maxConsecutiveDenials: 3, safeCommandAllowlist: [], allow: [], deny: [],
          environment: "The user selected automatic approvals. User questions always require actual user input.",
          audit: false,
        }, createReviewSubject(event.toolName, event.input, ctx.cwd), completeSimple);
        ctx.ui.setStatus("acp-go.permission-review", JSON.stringify({toolCallId:event.toolCallId,outcome:decision.outcome,risk_level:decision.risk_level}));
        if (decision.outcome === "allow") return;
      } catch {
        // Missing credentials, malformed output and timeouts fall back to the same host approval.
        ctx.ui.setStatus("acp-go.permission-review", JSON.stringify({toolCallId:event.toolCallId,failed:true}));
      }
    } else if (["read", "grep", "find", "ls"].includes(event.toolName)) {
      // Resolve links so a workspace-relative path cannot silently read outside it.
      try {
        const { realpathSync } = await import("node:fs");
        const { resolve, relative, isAbsolute } = await import("node:path");
        const root = realpathSync(ctx.cwd);
        const target = realpathSync(resolve(root, event.input?.path ?? "."));
        const rel = relative(root, target);
        if (!isAbsolute(rel) && rel !== ".." && !rel.startsWith("../")) return;
      } catch { /* unresolved paths require confirmation */ }
    }
    if (!ctx.hasUI) return { block: true, reason: "Approval UI unavailable" };
    const allowed = await ctx.ui.confirm(
      "Allow " + event.toolName + "?",
      JSON.stringify(event.input),
    );
    if (!allowed) return { block: true, reason: "Rejected by user" };
  });
}`

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

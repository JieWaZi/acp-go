package pi

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/JieWaZi/acp-go/pkg/acpmeta"
	"github.com/JieWaZi/acp-go/pkg/userinput"
	acp "github.com/JieWaZi/acp-go/sdk"
)

type resources struct {
	probe        bool
	instructions *string
	directories  []string
	servers      []acp.McpServer
}

func openResources(meta map[string]any, directories []string, servers []acp.McpServer) (resources, error) {
	instructions, err := acpmeta.DeveloperInstructions(meta)
	if err != nil {
		return resources{}, acp.NewInvalidParams(nil)
	}
	return resources{instructions: instructions, directories: slices.Clone(directories), servers: servers}, nil
}

const bridgeEnv = "ACP_GO_PI_SESSION_BRIDGE"

func (c *Client) prepareResources(input resources) (string, []string, []string, error) {
	if input.probe {
		return "", nil, nil, nil
	}
	if len(input.servers) > 0 && c.options.MCPModulePath == "" {
		return "", nil, nil, classified("unsupported", "mcp")
	}
	extension := c.options.MCPModulePath != "" || c.options.PermissionMode != "full-access"
	if !extension && (input.instructions == nil || *input.instructions == "") {
		return "", nil, nil, nil
	}
	dir, err := os.MkdirTemp("", "acp-go-pi-*")
	if err != nil {
		return "", nil, nil, errors.New("Pi resource directory could not be created")
	}
	var args, env []string
	err = func() error {
		if input.instructions != nil && *input.instructions != "" {
			file := filepath.Join(dir, "instructions.md")
			if err := os.WriteFile(file, []byte(*input.instructions), 0600); err != nil {
				return errors.New("Pi instructions could not be prepared")
			}
			args = append(args, "--append-system-prompt", file)
		}
		if !extension {
			return nil
		}
		source := "const bridge = null;\n"
		if c.options.MCPModulePath != "" {
			config, err := bridgeConfiguration(input.servers, c.options.PermissionMode)
			if err != nil {
				return err
			}
			env = append(env, bridgeEnv+"="+config)
			module, _ := json.Marshal(filepath.ToSlash(c.options.MCPModulePath))
			source = "import * as bridge from " + string(module) + ";\n"
		}
		source += "const permissionMode = " + string(mustJSON(c.options.PermissionMode)) + ";\n" + extensionSource
		file := filepath.Join(dir, "extension.ts")
		if err := os.WriteFile(file, []byte(source), 0600); err != nil {
			return errors.New("Pi bridge extension could not be prepared")
		}
		args = append(args, "--no-extensions", "--extension", file)
		return nil
	}()
	if err != nil {
		_ = os.RemoveAll(dir)
		return "", nil, nil, err
	}
	return dir, args, env, nil
}

func mustJSON(value string) []byte {
	raw, _ := json.Marshal(value)
	return raw
}

func bridgeConfiguration(servers []acp.McpServer, mode string) (string, error) {
	entries := make(map[string]any, len(servers))
	var questions []string
	for _, server := range servers {
		var name string
		entry := map[string]any{"auth": false, "oauth": false, "lifecycle": "eager"}
		switch {
		case server.Stdio != nil:
			s := server.Stdio
			name = s.Name
			env := make(map[string]string, len(s.Env))
			for _, variable := range s.Env {
				env[variable.Name] = variable.Value
			}
			entry["command"], entry["args"], entry["env"], entry["literalEnv"] = s.Command, s.Args, env, true
		case server.Http != nil:
			s := server.Http
			name = s.Name
			entry["url"], entry["httpTransport"], entry["headers"] = s.Url, "streamable-http", httpHeaders(s.Headers)
		case server.Sse != nil:
			s := server.Sse
			name = s.Name
			entry["url"], entry["httpTransport"], entry["headers"] = s.Url, "sse", httpHeaders(s.Headers)
		default:
			return "", acp.NewInvalidParams(nil)
		}
		if _, exists := entries[name]; exists || strings.TrimSpace(name) == "" || name == "__proto__" || name == "constructor" || name == "prototype" {
			return "", acp.NewInvalidParams(map[string]any{"message": "Pi MCP server name is empty, duplicate or reserved"})
		}
		if userinput.IsServer(server) {
			entry["requestTimeoutMs"], entry["approveTools"] = 2147483647, false
			questions = append(questions, name)
		}
		entries[name] = entry
	}
	config, err := json.Marshal(map[string]any{
		"config":    map[string]any{"mcpServers": entries, "settings": map[string]any{"autoAuth": false, "directTools": false, "scriptMode": false, "approveTools": mode == "default"}},
		"questions": questions,
	})
	if err != nil || len(config) > 100<<10 {
		return "", classified("unsupported", "mcp")
	}
	return string(config), nil
}

func httpHeaders(headers []acp.HttpHeader) map[string]string {
	result := make(map[string]string, len(headers))
	for _, header := range headers {
		result[header.Name] = header.Value
	}
	return result
}

// The extension composes the installed bridge with Pi's documented tool_call
// and session_start hooks. MCP values never enter the generated source.
const extensionSource = `
// Pi loads extensions again when switching into a newly persisted session.
const payloadSlot = Symbol.for("acp-go.pi.session-bridge");
globalThis[payloadSlot] ??= process.env.ACP_GO_PI_SESSION_BRIDGE;
const payload = globalThis[payloadSlot];
delete process.env.ACP_GO_PI_SESSION_BRIDGE;
let nextLiteral = 0;
function literal(value: string) {
  const key = "ACP_GO_PI_LITERAL_" + process.pid + "_" + nextLiteral++;
  process.env[key] = value;
  // The adapter's final substitution pass will not expand the value again.
  return "{env:" + key + "}";
}

export default async function (pi: any) {
  const { config, questions } = payload ? JSON.parse(payload) : { config: null, questions: [] };
  if (bridge && config) {
    for (const server of Object.values(config.mcpServers) as any[]) {
      if (server.headers) {
        for (const key of Object.keys(server.headers)) server.headers[key] = literal(server.headers[key]);
      }
      if (server.args) server.args = server.args.map(literal);
    }
    let mcpTool: any;
    await bridge.createMcpAdapter({ config })({
      ...pi,
      registerTool(tool: any) {
        if (tool.name === "mcp") mcpTool = tool;
        pi.registerTool(tool);
      },
    });
    if (questions.length) {
      if (!mcpTool) throw new Error("Managed user input requires the MCP tool");
      pi.registerTool({
        name: "AskUserQuestion", label: "Ask user", description: "Ask the user a question and wait for their answer.",
        parameters: {type:"object",properties:{questions:{type:"array",minItems:1,maxItems:8,items:{type:"object",properties:{question:{type:"string"},header:{type:"string"},multiSelect:{type:"boolean"},options:{type:"array",items:{type:"object",properties:{label:{type:"string"},description:{type:"string"}},required:["label"]}}},required:["questions"]}}},required:["questions"]},
        execute(id: string, args: any, signal: AbortSignal, update: any, ctx: any) {
          return mcpTool.execute(id, {server: questions[0], tool: "AskUserQuestion", args}, signal, update, ctx);
        },
      });
    }
  }
  pi.on("session_start", (_event: any, ctx: any) => {
    ctx.ui.setStatus("acp-go.extension-ready", "ready");
  });
  if (permissionMode === "full-access") return;
  pi.on("tool_call", async (event: any, ctx: any) => {
    if (questions.length && (event.toolName === "AskUserQuestion" || questions.some((name: string) => event.toolName === name + "_AskUserQuestion"))) return;
    if (permissionMode === "default" && bridge && ["mcp", "mcpScript"].includes(event.toolName)) return;
    if (permissionMode === "auto" && bridge) {
      try {
        const decision = await bridge.classifyAction(ctx, {
          enabled: true, mode: "fallback", classifierModel: null,
          classifierTimeoutSeconds: 90, approvalTimeoutSeconds: 0,
          maxConsecutiveDenials: 3, safeCommandAllowlist: [], allow: [], deny: [],
          environment: "User questions always require actual user input.", audit: false,
        }, bridge.createReviewSubject(event.toolName, event.input, ctx.cwd), bridge.completeSimple);
        ctx.ui.setStatus("acp-go.permission-review", JSON.stringify({toolCallId:event.toolCallId,outcome:decision.outcome,risk_level:decision.risk_level}));
        if (decision.outcome === "allow") return;
      } catch {
        ctx.ui.setStatus("acp-go.permission-review", JSON.stringify({toolCallId:event.toolCallId,failed:true}));
      }
    } else if (["read", "grep", "find", "ls"].includes(event.toolName)) {
      try {
        const { realpathSync } = await import("node:fs");
        const { resolve, relative, isAbsolute } = await import("node:path");
        const root = realpathSync(ctx.cwd);
        const target = realpathSync(resolve(root, event.input?.path ?? "."));
        const rel = relative(root, target);
        if (!isAbsolute(rel) && rel !== ".." && !rel.startsWith("../") && !rel.startsWith("..\\")) return;
      } catch { /* Unresolved paths require permission. */ }
    }
    if (!ctx.hasUI || !await ctx.ui.confirm("Allow " + event.toolName + "?", JSON.stringify(event.input))) {
      return { block: true, reason: "Permission denied" };
    }
  });
}
`

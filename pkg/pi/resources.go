package pi

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	acp "github.com/JieWaZi/acp-go-sdk"
	"github.com/JieWaZi/acp-go/pkg/acpmeta"
	"github.com/JieWaZi/acp-go/pkg/userinput"
)

type resources struct {
	// probe suppresses session-only resources for a management launch.
	probe bool
	// instructions is the optional native system prompt addition.
	instructions *string
	// roots freezes the canonical cwd followed by distinct additional directories.
	roots []workspaceRoot
	// servers contains the MCP descriptors frozen for this session.
	servers []acp.McpServer
}

// workspaceRoot binds one admitted directory to its filesystem identity.
type workspaceRoot struct {
	// path is the canonical absolute path delivered to the native extension.
	path string
	// identity rejects replacement between admission and resource preparation.
	identity os.FileInfo
	// device is the admitted Node-compatible device ID as an exact decimal string.
	device string
	// inode is the admitted Node-compatible file ID; empty means unverified.
	inode string
}

func openResources(cwd string, meta map[string]any, directories []string, servers []acp.McpServer) (resources, error) {
	instructions, err := acpmeta.DeveloperInstructions(meta)
	if err != nil {
		return resources{}, acp.NewInvalidParams(nil)
	}
	roots, err := freezeWorkspaceRoots(cwd, directories)
	if err != nil {
		return resources{}, err
	}
	return resources{instructions: instructions, roots: roots, servers: servers}, nil
}

func freezeWorkspaceRoots(cwd string, additional []string) ([]workspaceRoot, error) {
	var roots []workspaceRoot
	for _, path := range append([]string{cwd}, additional...) {
		if !filepath.IsAbs(path) {
			return nil, acp.NewInvalidParams(map[string]any{"message": "Pi workspace roots must be absolute"})
		}
		canonical, err := filepath.EvalSymlinks(path)
		if err != nil {
			return nil, acp.NewInvalidParams(map[string]any{"message": "Pi workspace root is unavailable"})
		}
		info, device, inode, err := snapshotWorkspaceRoot(canonical)
		if err != nil || !info.IsDir() {
			return nil, acp.NewInvalidParams(map[string]any{"message": "Pi workspace root is unavailable"})
		}
		duplicate := false
		for _, root := range roots {
			duplicate = duplicate || os.SameFile(root.identity, info)
		}
		if !duplicate {
			roots = append(roots, workspaceRoot{path: canonical, identity: info, device: device, inode: inode})
		}
	}
	return roots, nil
}

const bridgeEnv = "ACP_GO_PI_SESSION_BRIDGE"

func (c *Client) prepareResources(cwd string, input resources) (string, []string, []string, error) {
	if input.probe {
		_, err := freezeWorkspaceRoots(cwd, nil)
		return "", nil, nil, err
	}
	info, err := os.Stat(cwd)
	if err != nil || len(input.roots) == 0 || !os.SameFile(info, input.roots[0].identity) {
		return "", nil, nil, acp.NewInvalidParams(map[string]any{"message": "Pi workspace cwd changed after admission"})
	}
	paths := make([]string, len(input.roots))
	proofs := make([]map[string]string, len(input.roots))
	for i, root := range input.roots {
		canonical, err := filepath.EvalSymlinks(root.path)
		if err != nil || !samePath(canonical, root.path) {
			return "", nil, nil, acp.NewInvalidParams(map[string]any{"message": "Pi workspace root changed after admission"})
		}
		current, err := os.Stat(canonical)
		if err != nil || !os.SameFile(current, root.identity) {
			return "", nil, nil, acp.NewInvalidParams(map[string]any{"message": "Pi workspace root changed after admission"})
		}
		paths[i] = root.path
		proofs[i] = map[string]string{"path": root.path, "device": root.device, "inode": root.inode}
	}
	if len(input.servers) > 0 && c.options.MCPModulePath == "" {
		return "", nil, nil, classified("unsupported", "mcp")
	}
	instructions := ""
	if input.instructions != nil {
		instructions = *input.instructions
	}
	if len(paths) > 1 {
		additional, _ := json.Marshal(paths[1:])
		instructions += "\n\nAdditional workspace directories (paths):\n" + string(additional)
	}
	extension := c.options.MCPModulePath != "" || c.options.PermissionMode != "full-access"
	if !extension && instructions == "" {
		return "", nil, nil, nil
	}
	dir, err := os.MkdirTemp("", "acp-go-pi-*")
	if err != nil {
		return "", nil, nil, errors.New("Pi resource directory could not be created")
	}
	var args, env []string
	err = func() error {
		if instructions != "" {
			file := filepath.Join(dir, "instructions.md")
			if err := os.WriteFile(file, []byte(instructions), 0600); err != nil {
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
		workspaceRoots, _ := json.Marshal(proofs)
		source += "const permissionMode = " + string(mustJSON(c.options.PermissionMode)) + ";\n" +
			"const workspaceRoots = " + string(workspaceRoots) + ";\n" + extensionSource
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
	questions := []string{}
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
const workspacePaths = workspaceRoots.map((root: any) => root.path);
let nextLiteral = 0;
function literal(value: string) {
  const key = "ACP_GO_PI_LITERAL_" + process.pid + "_" + nextLiteral++;
  process.env[key] = value;
  // The adapter's final substitution pass will not expand the value again.
  return "{env:" + key + "}";
}

export default async function (pi: any) {
  const { realpathSync, statSync } = await import("node:fs");
  const { resolve, relative, isAbsolute } = await import("node:path");
  const within = (root: string, target: string) => {
    const rel = relative(root, target);
    return !isAbsolute(rel) && rel !== ".." && !rel.startsWith("../") && !rel.startsWith("..\\");
  };
  // A Pi session switch reloads this extension in the same process. It must
  // retain the original bindings, never acquire a replacement or union roots.
  const workspaceSlot = Symbol.for("acp-go.pi.workspace-roots");
  const scopeKey = JSON.stringify(workspaceRoots);
  let workspace = globalThis[workspaceSlot];
  if (workspace && workspace.key !== scopeKey) throw new Error("Pi workspace roots changed in a live process");
  if (!workspace) {
    const decimal = (value: any) => typeof value === "string" && /^(0|[1-9][0-9]*)$/.test(value) ? BigInt(value) : null;
    const roots = workspaceRoots.map((root: any) => {
      // The first consumer receives Go's admitted identity. A new stat can
      // verify it, but must never promote a replacement into an authorized root.
      return Object.freeze({path: root.path, dev: decimal(root.device), ino: decimal(root.inode)});
    });
    workspace = Object.freeze({key: scopeKey, roots: Object.freeze(roots)});
    globalThis[workspaceSlot] = workspace;
  }
  const bound = (root: any) => {
    try {
      if (root.dev === null || root.ino === null || root.ino === 0n) return false;
      const canonical = realpathSync(root.path);
      const stat = statSync(canonical, {bigint: true});
      return relative(root.path, canonical) === "" && stat.isDirectory() && stat.dev === root.dev && stat.ino === root.ino;
    } catch { return false; }
  };
  const authorizedRead = (event: any, ctx: any) => {
    try {
      const root = workspace.roots[0];
      if (!bound(root) || relative(root.path, realpathSync(ctx.cwd)) !== "") return false;
      const path = event.input?.path ?? ".";
      // Native tools normalize aliases beyond node:path.resolve. Unproven
      // aliases retain human approval rather than trusting a different target.
      if (typeof path !== "string" || path.startsWith("@") || path.startsWith("~") ||
          /^file:\/\//.test(path) || /[\u00A0\u2000-\u200A\u202F\u205F\u3000]/.test(path) ||
          process.platform === "win32" && /^\/(?:mnt\/|cygdrive\/)?[a-z](?:\/|$)/i.test(path)) return false;
      // Missing targets, including proposed creations, do not inherit a
      // read grant merely because their nearest existing ancestor is trusted.
      const target = realpathSync(resolve(root.path, path));
      return workspace.roots.some((candidate: any) => bound(candidate) && within(candidate.path, target));
    } catch { return false; }
  };
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
          environment: "User questions always require actual user input. Frozen workspace roots: " + JSON.stringify(workspacePaths) +
            ". Currently bound roots: " + JSON.stringify(workspace.roots.filter(bound).map((root: any) => root.path)), audit: false,
        }, bridge.createReviewSubject(event.toolName, event.input, ctx.cwd), bridge.completeSimple);
        ctx.ui.setStatus("acp-go.permission-review", JSON.stringify({toolCallId:event.toolCallId,outcome:decision.outcome,risk_level:decision.risk_level}));
        if (decision.outcome === "allow") return;
      } catch {
        ctx.ui.setStatus("acp-go.permission-review", JSON.stringify({toolCallId:event.toolCallId,failed:true}));
      }
    } else if (["read", "grep", "find", "ls"].includes(event.toolName)) {
      if (authorizedRead(event, ctx)) return;
    }
    if (!ctx.hasUI || !await ctx.ui.confirm("Allow " + event.toolName + "?", JSON.stringify(event.input))) {
      return { block: true, reason: "Permission denied" };
    }
  });
}
`

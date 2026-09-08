// Composes pi-mcp-adapter@2.32.1 with Pi's official tool_call hook.
// References and licenses: UPSTREAM.md. No ACP or Pi RPC implementation here.
import { createMcpAdapter, classifyAction, createReviewSubject, completeSimple } from __MCP_MODULE__;

const config = __CONFIG__;
const permissionMode = __PERMISSION_MODE__;
const questionServers = __QUESTION_TOOLS__;
let literalIndex = 0;
function literal(value: string): string {
  const key = `ACP_GO_PI_LITERAL_${process.pid}_${literalIndex++}`;
  process.env[key] = value;
  // This is the library's final interpolation pass: inserted values are not re-parsed.
  return `{env:${key}}`;
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
  if (permissionMode === "full-access") return;
  // MCP tools use the library's own approval gate, including direct and resource tools.
  // Other tools use Pi's documented pre-execution hook, following the upstream gate example.
  pi.on("tool_call", async (event: any, ctx: any) => {
    if (questionServers.length && (event.toolName === "AskUserQuestion" || questionServers.some(name => event.toolName === `${name}_AskUserQuestion`))) return;
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
      `Allow ${event.toolName}?`,
      JSON.stringify(event.input),
    );
    if (!allowed) return { block: true, reason: "Rejected by user" };
  });
}

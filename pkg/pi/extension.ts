// Composes pi-mcp-adapter@2.32.1 with Pi's official tool_call hook.
// References and licenses: UPSTREAM.md. No ACP or Pi RPC implementation here.
import { createMcpAdapter } from __MCP_MODULE__;

const config = __CONFIG__;
const manual = __MANUAL__;
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
  await createMcpAdapter({ config })(pi);
  if (!manual) return;
  // MCP tools use the library's own approval gate, including direct and resource tools.
  // Other tools use Pi's documented pre-execution hook, following the upstream gate example.
  pi.on("tool_call", async (event: any, ctx: any) => {
    if (["read", "grep", "find", "ls", "mcp", "mcpScript"].includes(event.toolName)) return;
    if (!ctx.hasUI) return { block: true, reason: "Approval UI unavailable" };
    const allowed = await ctx.ui.confirm(
      `Allow ${event.toolName}?`,
      JSON.stringify(event.input),
    );
    if (!allowed) return { block: true, reason: "Rejected by user" };
  });
}

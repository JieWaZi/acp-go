import { spawn } from "node:child_process";
import { createInterface } from "node:readline";
import { createServer } from "node:http";
import { createRequire } from "node:module";
import {
  readFileSync,
  writeFileSync,
  appendFileSync,
  mkdirSync,
  existsSync,
  rmSync,
} from "node:fs";
import assert from "node:assert/strict";
import { mkdtempSync } from "node:fs";
import { tmpdir } from "node:os";
import { resolve, join } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";
// Runs only loopback model/MCP fixtures; never uses a personal provider or credentials.
if (!process.argv[2] || !process.argv[3])
  throw new Error(
    "Usage: node scripts/pi-integration/run.mjs <npm-install-directory> <acp-agent-binary>",
  );
const dependencies = resolve(process.argv[2]);
const binary = resolve(process.argv[3]);
const requireDependency = createRequire(join(dependencies, "package.json"));
const { Server } = requireDependency(
  "@modelcontextprotocol/sdk/server/index.js",
);
const { SSEServerTransport } = requireDependency(
  "@modelcontextprotocol/sdk/server/sse.js",
);
const { ListToolsRequestSchema, CallToolRequestSchema } = requireDependency(
  "@modelcontextprotocol/sdk/types.js",
);
const sseConnections = new Map();
const fixtures = fileURLToPath(new URL(".", import.meta.url));
const root = mkdtempSync(join(tmpdir(), "acp-go-pi-test-"));
mkdirSync(root + "/home");
console.log("Integration artifacts: " + root);
const configDir = root + "/go-config",
  workspace = root + "/go-workspace";
mkdirSync(configDir, { recursive: true });
mkdirSync(workspace, { recursive: true });
const log = root + "/go-chain-events.jsonl";
writeFileSync(log, "");
const append = (x) =>
  appendFileSync(log, JSON.stringify({ ...x, time: Date.now() }) + "\n");
const special =
  '!literal ${NEVER_REPLACE} $env:NEVER_REPLACE {env:NEVER_REPLACE} "quote" `tick` \\ backslash';
const modelRequests = [],
  permissionRequests = [],
  events = [];
const server = createServer(async (req, res) => {
  try {
    if (req.url.startsWith("/sse/")) {
      const url = new URL(req.url, "http://127.0.0.1");
      const tag = url.pathname.split("/")[2];
      append({ event: "sse", tag, header: req.headers["x-owned-special"] });
      if (req.headers["x-owned-special"] !== special) {
        res.writeHead(401).end();
        return;
      }
      if (req.method === "GET") {
        const transport = new SSEServerTransport(`/sse/${tag}/messages`, res);
        const mcp = new Server(
          { name: "local-sse-" + tag, version: "1" },
          { capabilities: { tools: {} } },
        );
        mcp.setRequestHandler(ListToolsRequestSchema, async () => ({
          tools: [
            {
              name: "record",
              description: "Local SSE fixture",
              inputSchema: {
                type: "object",
                properties: { marker: { type: "string" } },
                required: ["marker"],
              },
            },
          ],
        }));
        mcp.setRequestHandler(CallToolRequestSchema, async (request) => {
          append({
            event: "tool_called",
            transport: "sse",
            tag,
            marker: request.params.arguments.marker,
          });
          return { content: [{ type: "text", text: "recorded " + tag }] };
        });
        sseConnections.set(transport.sessionId, { transport, mcp });
        res.on("close", () => {
          sseConnections.delete(transport.sessionId);
          void mcp.close();
        });
        await mcp.connect(transport);
      } else {
        const entry = sseConnections.get(url.searchParams.get("sessionId"));
        if (!entry) {
          res.writeHead(404).end();
          return;
        }
        await entry.transport.handlePostMessage(req, res);
      }
      return;
    }
    let raw = "";
    for await (const chunk of req) raw += chunk;
    const body = raw ? JSON.parse(raw) : {};
    if (req.url.startsWith("/mcp/")) {
      const tag = req.url.slice("/mcp/".length);
      append({
        event: "http",
        tag,
        method: req.method,
        header: req.headers["x-owned-special"],
        rpc: body.method,
      });
      if (req.headers["x-owned-special"] !== special) {
        res.writeHead(401, { "www-authenticate": "Bearer" }).end();
        return;
      }
      if (req.method !== "POST") {
        res.writeHead(405).end();
        return;
      }
      if (body.id === undefined) {
        res.writeHead(202).end();
        return;
      }
      let result = {};
      if (body.method === "initialize")
        result = {
          protocolVersion: body.params.protocolVersion,
          capabilities: { tools: {} },
          serverInfo: { name: "local-http-" + tag, version: "1" },
        };
      if (body.method === "tools/list")
        result = {
          tools: [
            {
              name: "record",
              description: "Local fixture side effect only",
              inputSchema: {
                type: "object",
                properties: { marker: { type: "string" } },
                required: ["marker"],
              },
            },
          ],
        };
      if (body.method === "tools/call") {
        append({
          event: "tool_called",
          transport: "http",
          tag,
          marker: body.params.arguments.marker,
        });
        result = { content: [{ type: "text", text: "recorded " + tag }] };
      }
      res
        .writeHead(200, { "content-type": "application/json" })
        .end(JSON.stringify({ jsonrpc: "2.0", id: body.id, result }));
      return;
    }
    if (req.url !== "/v1/chat/completions") {
      res.writeHead(404).end();
      return;
    }
    const index = body.messages.findLastIndex(
      (message) => message.role === "user",
    );
    const content = body.messages[index]?.content;
    const text =
      typeof content === "string"
        ? content
        : content
            ?.filter((x) => x.type === "text")
            .map((x) => x.text)
            .join("\n");
    const marker = text?.match(/GOFIX:(\{[^\n]+\})/)?.[1];
    if (!marker) throw new Error("Fixture marker absent");
    const task = JSON.parse(marker),
      alreadyCalled = body.messages
        .slice(index + 1)
        .some((message) => message.role === "tool");
    modelRequests.push({
      marker: task.marker,
      alreadyCalled,
      tools: (body.tools ?? []).map((x) => x.function?.name),
    });
    const tool = task.kind === "bash" ? "bash" : "mcp";
    const args =
      tool === "bash"
        ? {
            command: `printf 'fixture-approved' > '${workspace}/${task.marker}.txt'`,
          }
        : {
            server: task.transport,
            tool: "record",
            args: { marker: task.marker },
          };
    const delta = alreadyCalled
      ? { role: "assistant", content: "local fixture complete" }
      : {
          role: "assistant",
          tool_calls: [
            {
              index: 0,
              id: "call_" + task.marker,
              type: "function",
              function: { name: tool, arguments: JSON.stringify(args) },
            },
          ],
        };
    res.writeHead(200, { "content-type": "text/event-stream" });
    res.write(
      "data: " +
        JSON.stringify({
          id: "local-probe",
          object: "chat.completion.chunk",
          created: 0,
          model: "local-model",
          choices: [{ index: 0, delta, finish_reason: null }],
        }) +
        "\n\n",
    );
    res.write(
      "data: " +
        JSON.stringify({
          id: "local-probe",
          object: "chat.completion.chunk",
          created: 0,
          model: "local-model",
          choices: [
            {
              index: 0,
              delta: {},
              finish_reason: alreadyCalled ? "stop" : "tool_calls",
            },
          ],
        }) +
        "\n\n",
    );
    res.end("data: [DONE]\n\n");
  } catch (error) {
    append({ event: "fixture_error", message: String(error) });
    res
      .writeHead(500)
      .end(JSON.stringify({ error: { message: String(error) } }));
  }
});
await new Promise((resolve) => server.listen(0, "127.0.0.1", resolve));
const port = server.address().port;
writeFileSync(
  configDir + "/models.json",
  JSON.stringify({
    providers: {
      "ally-local-fixture": {
        baseUrl: `http://127.0.0.1:${port}/v1`,
        api: "openai-completions",
        apiKey: "dummy-local-only",
        models: [{ id: "local-model", name: "Local fixture" }],
      },
    },
  }),
);
writeFileSync(
  configDir + "/settings.json",
  JSON.stringify({
    defaultProvider: "ally-local-fixture",
    defaultModel: "local-model",
    quietStartup: true,
  }),
);
const mcpServers = (tag) => [
  {
    type: "sse",
    name: "sse",
    url: `http://127.0.0.1:${port}/sse/${tag}`,
    headers: [{ name: "X-Owned-Special", value: special }],
  },
  {
    name: "stdio",
    command: process.execPath,
    args: [fixtures + "/mcp-fixture.mjs", tag, log, special, "line1\nline2"],
    env: [{ name: "OWNED_SPECIAL", value: special }],
  },
  {
    type: "http",
    name: "http",
    url: `http://127.0.0.1:${port}/mcp/${tag}`,
    headers: [{ name: "X-Owned-Special", value: special }],
  },
];
const env = {
  PATH: process.env.PATH,
  TMPDIR: configDir,
  PI_CODING_AGENT_DIR: configDir,
  PI_ACP_PATH: dependencies + "/node_modules/.bin/pi-acp",
  PI_ACP_PI_COMMAND: dependencies + "/node_modules/.bin/pi",
  PI_MCP_ADAPTER_PATH: dependencies + "/node_modules/pi-mcp-adapter/index.ts",
  XDG_CONFIG_HOME: configDir,
  NO_COLOR: "1",
  PI_SKIP_VERSION_CHECK: "1",
  HOME: root + "/home",
  USERPROFILE: root + "/home",
  ACP_GO_TEST_HOME: root + "/home",
  NODE_OPTIONS:
    "--import=" + pathToFileURL(fixtures + "/home-isolation.mjs").href,
};
const child = spawn(binary, ["--adapter", "pi"], {
  cwd: workspace,
  env,
  stdio: ["pipe", "pipe", "pipe"],
});
const pending = new Map();
let id = 0,
  currentTask;
child.stderr.on("data", (data) =>
  append({ event: "stderr", text: data.toString() }),
);
createInterface({ input: child.stdout }).on("line", (line) => {
  try {
    const value = JSON.parse(line);
    events.push(value);
    if (!value.method && pending.has(value.id)) {
      pending.get(value.id)(value);
      pending.delete(value.id);
    }
    if (value.method === "session/request_permission") {
      const options = value.params.options;
      if (currentTask.decision === "cancel") {
        permissionRequests.push({
          marker: currentTask.marker,
          decision: "cancel",
          options,
        });
        child.stdin.write(
          JSON.stringify({
            jsonrpc: "2.0",
            method: "session/cancel",
            params: { sessionId: value.params.sessionId },
          }) + "\n",
        );
        child.stdin.write(
          JSON.stringify({
            jsonrpc: "2.0",
            id: value.id,
            result: { outcome: { outcome: "cancelled" } },
          }) + "\n",
        );
        return;
      }
      const deny = currentTask.decision === "deny";
      const choice = options.find((option) =>
        deny
          ? /^(Deny|No)$/i.test(option.name)
          : /^(Allow once|Yes)$/i.test(option.name),
      );
      if (!choice)
        throw new Error(
          "Cannot identify fixture permission choice: " +
            JSON.stringify(options),
        );
      permissionRequests.push({
        marker: currentTask.marker,
        decision: currentTask.decision,
        choice,
        options,
      });
      child.stdin.write(
        JSON.stringify({
          jsonrpc: "2.0",
          id: value.id,
          result: {
            outcome: { outcome: "selected", optionId: choice.optionId },
          },
        }) + "\n",
      );
    }
  } catch (error) {
    append({ event: "protocol_error", message: String(error), line });
  }
});
async function rpc(method, params) {
  const n = ++id;
  const result = await new Promise((resolve, reject) => {
    const timer = setTimeout(
      () => reject(new Error("timeout " + method)),
      45000,
    );
    pending.set(n, (value) => {
      clearTimeout(timer);
      resolve(value);
    });
    child.stdin.write(
      JSON.stringify({ jsonrpc: "2.0", id: n, method, params }) + "\n",
    );
  });
  append({
    event: "rpc_result",
    method,
    id: n,
    error: result.error,
    sessionId: result.result?.sessionId,
  });
  if (result.error)
    throw Object.assign(new Error(JSON.stringify(result.error)), {
      code: result.error.code,
    });
  return result.result;
}
async function prompt(
  sessionId,
  marker,
  transport = "stdio",
  decision = "allow",
  kind = "mcp",
) {
  currentTask = { marker, transport, decision, kind };
  return rpc("session/prompt", {
    sessionId,
    prompt: [{ type: "text", text: "GOFIX:" + JSON.stringify(currentTask) }],
  });
}
try {
  const initialized = await rpc("initialize", {
    protocolVersion: 1,
    clientCapabilities: {},
    clientInfo: { name: "go-chain-local-fixture", version: "1" },
  });
  const a = await rpc("session/new", {
    cwd: workspace,
    mcpServers: mcpServers("A"),
  });
  await prompt(a.sessionId, "A_FIRST");
  const b = await rpc("session/new", {
    cwd: workspace,
    mcpServers: mcpServers("B"),
  });
  await prompt(b.sessionId, "B_FIRST");
  await prompt(a.sessionId, "A_RESTORED");
  await prompt(a.sessionId, "HTTP_ALLOWED", "http");
  await prompt(a.sessionId, "HTTP_DENIED", "http", "deny");
  await prompt(a.sessionId, "SSE_ALLOWED", "sse");
  await prompt(a.sessionId, "SSE_DENIED", "sse", "deny");
  for (const marker of ["BASH_DENIED", "BASH_ALLOWED"])
    rmSync(workspace + "/" + marker + ".txt", { force: true });
  await prompt(a.sessionId, "BASH_DENIED", "stdio", "deny", "bash");
  await prompt(a.sessionId, "BASH_ALLOWED", "stdio", "allow", "bash");
  await prompt(a.sessionId, "BASH_CANCELLED", "stdio", "cancel", "bash").then(
    (result) => assert.equal(result.stopReason, "cancelled"),
    (error) => assert.equal(error.code, -32800),
  );
  assert.equal(existsSync(workspace + "/BASH_CANCELLED.txt"), false);
  await rpc("session/load", {
    sessionId: a.sessionId,
    cwd: workspace,
    mcpServers: mcpServers("A_UPDATED"),
  });
  await prompt(a.sessionId, "A_UPDATED");
  const records = readFileSync(log, "utf8")
      .trim()
      .split("\n")
      .filter(Boolean)
      .map(JSON.parse),
    calls = records.filter((x) => x.event === "tool_called");
  const actual = Object.fromEntries(calls.map((x) => [x.marker, x.tag]));
  assert.deepEqual(actual, {
    A_FIRST: "A",
    B_FIRST: "B",
    A_RESTORED: "A",
    HTTP_ALLOWED: "A",
    SSE_ALLOWED: "A",
    A_UPDATED: "A_UPDATED",
  });
  assert.equal(existsSync(workspace + "/BASH_DENIED.txt"), false);
  assert.equal(
    readFileSync(workspace + "/BASH_ALLOWED.txt", "utf8"),
    "fixture-approved",
  );
  assert.equal(permissionRequests.length, 11);
  const starts = records.filter((x) => x.event === "stdio_start");
  assert.ok(starts.length >= 4);
  assert.ok(
    starts.every(
      (x) =>
        x.args[2] === special &&
        x.args[3] === "line1\nline2" &&
        x.env === special,
    ),
  );
  const headers = records.filter(
    (x) => ["http", "sse"].includes(x.event) && x.header !== undefined,
  );
  assert.ok(headers.length > 0);
  assert.ok(headers.every((x) => x.header === special));
  const summary = {
    success: true,
    initialized,
    actual,
    permissionRequests,
    bashDeniedDidNotExecute: true,
    approvalCancellationDidNotExecute: true,
    bashAllowedExecuted: true,
    specialArgsEnvHeadersPreserved: true,
    localModelRequests: modelRequests.length,
  };
  writeFileSync(
    root + "/go-chain-summary.json",
    JSON.stringify(summary, null, 2),
  );
  console.log(JSON.stringify(summary, null, 2));
} finally {
  child.kill("SIGTERM");
  await new Promise((resolve) => {
    child.once("exit", resolve);
    setTimeout(() => {
      child.kill("SIGKILL");
      resolve();
    }, 5000).unref();
  });
  server.closeAllConnections();
  server.close();
  writeFileSync(root + "/go-chain-acp.json", JSON.stringify(events, null, 2));
  writeFileSync(
    root + "/go-chain-model-requests.json",
    JSON.stringify(modelRequests, null, 2),
  );
}

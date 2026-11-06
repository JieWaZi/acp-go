import { createInterface } from "node:readline";
import { appendFileSync } from "node:fs";
const [tag, log, special] = process.argv.slice(2);
const record = (value) => appendFileSync(log, JSON.stringify(value) + "\n");
record({
  event: "stdio_start",
  tag,
  args: process.argv.slice(2),
  env: process.env.OWNED_SPECIAL,
});
createInterface({ input: process.stdin }).on("line", (line) => {
  const request = JSON.parse(line);
  if (request.id === undefined) return;
  let result = {};
  if (request.method === "initialize")
    result = {
      protocolVersion: request.params.protocolVersion,
      capabilities: { tools: {} },
      serverInfo: { name: "go-fixture-" + tag, version: "1" },
    };
  if (request.method === "tools/list")
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
  if (request.method === "tools/call") {
    record({
      event: "tool_called",
      transport: "stdio",
      tag,
      marker: request.params.arguments.marker,
    });
    result = { content: [{ type: "text", text: "recorded " + tag }] };
  }
  process.stdout.write(
    JSON.stringify({ jsonrpc: "2.0", id: request.id, result }) + "\n",
  );
});

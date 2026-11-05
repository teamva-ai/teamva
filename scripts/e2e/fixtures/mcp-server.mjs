// Offline MCP fixture. No provider, model, filesystem write or network calls.
import { createInterface } from "node:readline";
const lines = createInterface({ input: process.stdin, terminal: false });
lines.on("line", (line) => {
  let request;
  try { request = JSON.parse(line); } catch { return; }
  if (request.id === undefined) return;
  let result;
  switch (request.method) {
    case "initialize":
      result = {
        protocolVersion: "2025-03-26",
        capabilities: { tools: {}, resources: {}, prompts: {} },
        serverInfo: { name: "teamva-offline-fixture", version: "1.0.0" }
      };
      break;
    case "ping": result = {}; break;
    case "tools/list":
      result = { tools: [{ name: "fixture_echo", description: "Offline fixture tool", inputSchema: { type: "object", properties: { text: { type: "string" } } } }] };
      break;
    case "resources/list":
      result = { resources: [{ name: "Fixture resource", uri: "fixture://readme", mimeType: "text/plain" }] };
      break;
    case "resources/templates/list": result = { resourceTemplates: [] }; break;
    case "prompts/list":
      result = { prompts: [{ name: "fixture_prompt", description: "Offline fixture prompt" }] };
      break;
    case "tools/call": result = { content: [{ type: "text", text: "Offline fixture only" }] }; break;
    default:
      process.stdout.write(`${JSON.stringify({ jsonrpc: "2.0", id: request.id, error: { code: -32601, message: "Method not found" } })}\n`);
      return;
  }
  process.stdout.write(`${JSON.stringify({ jsonrpc: "2.0", id: request.id, result })}\n`);
});
lines.on("close", () => process.exit(0));

// Deterministic offline MCP fixture: no filesystem/network/auth operations.
import { createInterface } from "node:readline";
const lines = createInterface({ input: process.stdin, terminal: false });
const unsupportedForm = process.argv[2] === "unsupported-form";
const pending = new Map();
const reply = (id, result) => process.stdout.write(JSON.stringify({ jsonrpc: "2.0", id, result }) + "\n");
lines.on("line", line => {
  let request;
  try { request = JSON.parse(line); } catch { return; }
  if (request.id === undefined) return;
  if (pending.has(request.id)) {
    const toolRequest = pending.get(request.id);
    pending.delete(request.id);
    const declined = request.result?.action === "decline" || request.result?.action === "cancel" || request.error;
    reply(toolRequest, { isError: true, content: [{ type: "text", text: declined ? "UNSUPPORTED_FORM_DECLINED" : "UNSAFE_FORM_ACCEPTED" }] });
    return;
  }
  let result;
  switch (request.method) {
    case "initialize": result = { protocolVersion: "2025-03-26", capabilities: { tools: {} }, serverInfo: { name: "codex-approval-fixture", version: "1" } }; break;
    case "ping": result = {}; break;
    case "tools/list": result = { tools: [{ name: "qa_echo", description: "Return the deterministic offline receipt", inputSchema: { type: "object", properties: { text: { type: "string" } }, required: ["text"] } }] }; break;
    case "tools/call":
      if (unsupportedForm) {
        const id = "fixture-input-form";
        pending.set(id, request.id);
        process.stdout.write(JSON.stringify({ jsonrpc: "2.0", id, method: "elicitation/create", params: {
          mode: "form", message: "Offline fixture requires an input field, not binary approval",
          requestedSchema: { type: "object", properties: { answer: { type: "string" } }, required: ["answer"] }
        } }) + "\n");
        return;
      }
      result = { content: [{ type: "text", text: "CODEX_MCP_NATIVE_RECEIPT:" + String(request.params?.arguments?.text ?? "") }] }; break;
    default: result = {};
  }
  reply(request.id, result);
});
lines.on("close", () => process.exit(0));

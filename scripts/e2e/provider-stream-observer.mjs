// Test-only transparent stdio observer. Never writes Provider payloads or
// credentials: records method names and locations of three public test markers.
import { spawn } from "node:child_process";
import { createWriteStream } from "node:fs";
import { StringDecoder } from "node:string_decoder";

const target = process.env.TEAMVA_STREAM_NATIVE_BINARY;
const destination = process.env.TEAMVA_STREAM_NATIVE_METRICS;
if (!target || !destination) throw new Error("observer requires an isolated binary and metrics path");
const compactLimit = Number(process.env.TEAMVA_STREAM_CODEX_COMPACT_LIMIT || 0);
if (!Number.isInteger(compactLimit) || compactLimit < 0) throw new Error("invalid test compaction limit");
const args = compactLimit ? ["-c", `model_auto_compact_token_limit=${compactLimit}`, ...process.argv.slice(2)] : process.argv.slice(2);
const child = spawn(target, args, { stdio: ["pipe", "pipe", "inherit"] });
const log = createWriteStream(destination, { flags: "a", mode: 0o600 });
process.stdin.pipe(child.stdin);
child.stdin.on("error", () => {});
let buffer = "";
const decoder = new StringDecoder("utf8");
child.stdout.on("data", (chunk) => {
  process.stdout.write(chunk);
  buffer += decoder.write(chunk);
  if (buffer.length > 16 * 1024 * 1024) buffer = "";
  let split;
  while ((split = buffer.indexOf("\n")) >= 0) {
    const line = buffer.slice(0, split);
    buffer = buffer.slice(split + 1);
    try {
      const message = JSON.parse(line);
      const markers = [];
      const visit = (value, path = "", depth = 0) => {
        if (depth > 12) return;
        if (typeof value === "string") {
          for (const marker of ["STREAM_TOOL_A", "STREAM_TOOL_B", "STREAM_EXPECTED_FAILURE"]) {
            if (value.includes(marker)) markers.push({ field: path, marker });
          }
        } else if (value && typeof value === "object") {
          for (const [key, nested] of Object.entries(value)) visit(nested, `${path}.${key}`, depth + 1);
        }
      };
      visit(message);
      log.write(JSON.stringify({ method: message.method ?? "rpc.response", item_id: message.params?.itemId ?? message.params?.item?.id, markers }) + "\n");
    } catch { /* --version/probe output is not JSON RPC. */ }
  }
});
for (const signal of ["SIGTERM", "SIGINT"]) process.on(signal, () => child.kill(signal));
child.on("error", () => { process.exitCode = 1; log.end(); });
child.on("close", (code) => {
  // Do not force-exit while relayed stdout still has buffered bytes.
  process.exitCode = code ?? 1;
  process.stdin.unpipe(child.stdin);
  process.stdin.destroy();
  log.end();
});

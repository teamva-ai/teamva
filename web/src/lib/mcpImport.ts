// Independently implemented parser. Accept popular snippets without dropping
// unknown OAuth fields, silently fixing invalid fields, or executing anything.
import { buildMcpInput, isMcpObject, MCP_NAME_PATTERN, McpValidationError, mcpDraft, type McpConfig } from "./mcp";
export interface McpImportItem { name: string; config: McpConfig }
function rejectDuplicateKeys(text: string) {
  const stack: Array<{ object: boolean; key: boolean; keys: Set<string> }> = [];
  for (const match of text.matchAll(/"(?:\\.|[^"\\])*"|[{}\[\]:,]/g)) {
    const token = match[0];
    const top = stack.at(-1);
    if (token.startsWith('"') && top?.object && top.key) {
      const key = JSON.parse(token) as string;
      if (top.keys.has(key)) throw new McpValidationError("duplicateKey");
      top.keys.add(key); top.key = false;
    } else if (token === "{") stack.push({ object: true, key: true, keys: new Set() });
    else if (token === "[") stack.push({ object: false, key: false, keys: new Set() });
    else if (token === "}" || token === "]") stack.pop();
    else if (token === "," && top?.object) top.key = true;
  }
}
function invalid(): never { throw new McpValidationError("invalidImport"); }
function strings(value: unknown): value is string[] { return Array.isArray(value) && value.every((item) => typeof item === "string"); }
function normalize(value: unknown): McpConfig {
  if (!isMcpObject(value)) return invalid();
  const raw = structuredClone(value);
  const allowed = ["name", "type", "command", "args", "cwd", "environment", "env", "url", "headers", "oauth", "enabled", "disabled", "protocol", "codemode", "timeout"];
  if (Object.keys(raw).some((key) => !allowed.includes(key))) invalid();
  if (raw.protocol !== undefined && !["legacy", "auto", "2026-07-28"].includes(String(raw.protocol))) invalid();
  let type: "local" | "remote";
  if (["local", "stdio"].includes(String(raw.type)) || (raw.type === undefined && raw.command !== undefined)) type = "local";
  else if (["remote", "http", "sse"].includes(String(raw.type)) || (raw.type === undefined && raw.url !== undefined)) type = "remote";
  else return invalid();
  const config = { ...raw, type } as McpConfig;
  if (type === "local") {
    const command = typeof raw.command === "string" ? [raw.command] : strings(raw.command) ? raw.command : invalid();
    if (raw.args !== undefined && !strings(raw.args)) invalid();
    config.command = [...command, ...((raw.args ?? []) as string[])];
  } else if (typeof raw.url !== "string") invalid();
  for (const [target, source] of [["environment", raw.environment ?? raw.env], ["headers", raw.headers]] as const) {
    if (source === undefined) continue;
    if (!isMcpObject(source) || Object.values(source).some((entry) => typeof entry !== "string" || entry === "__TEAMVA_KEEP_SECRET__")) invalid();
    config[target] = source as Record<string, string>;
  }
  if (raw.disabled !== undefined && typeof raw.disabled !== "boolean") invalid();
  if (raw.enabled !== undefined && typeof raw.enabled !== "boolean") invalid();
  if (raw.codemode !== undefined && typeof raw.codemode !== "boolean") invalid();
  if (raw.cwd !== undefined && typeof raw.cwd !== "string") invalid();
  config.disabled = raw.disabled === undefined ? raw.enabled === false : raw.disabled === true;
  if (raw.timeout !== undefined) {
    if (typeof raw.timeout === "number") config.timeout = { execution: raw.timeout };
    else if (!isMcpObject(raw.timeout)) invalid();
    else if (Object.entries(raw.timeout).some(([key, value]) => !["startup", "catalog", "execution"].includes(key) || typeof value !== "number" || !Number.isInteger(value) || value < 1 || value > 3600000)) invalid();
  }
  if (raw.oauth !== undefined && raw.oauth !== false) {
    if (!isMcpObject(raw.oauth)) invalid();
    const oauth = { ...raw.oauth };
    for (const [old, next] of Object.entries({ clientId: "client_id", clientSecret: "client_secret", callbackPort: "callback_port", redirectUri: "redirect_uri", authServerMetadataUrl: "auth_server_metadata_url" })) {
      if (oauth[old] !== undefined && oauth[next] === undefined) oauth[next] = oauth[old];
      delete oauth[old];
    }
    for (const key of ["client_id", "client_secret", "scope", "redirect_uri", "auth_server_metadata_url"]) {
      if (oauth[key] !== undefined && (typeof oauth[key] !== "string" || oauth[key] === "__TEAMVA_KEEP_SECRET__")) invalid();
    }
    config.oauth = oauth;
  }
  delete config.args; delete config.env; delete config.enabled; delete config.name;
  // The same validation as the editor; no partial imports on malformed entries.
  return buildMcpInput(mcpDraft(config, "imported"), 0).config;
}
export function parseMcpImport(text: string): McpImportItem[] {
  let parsed: unknown;
  try { parsed = JSON.parse(text); } catch { return invalid(); }
  rejectDuplicateKeys(text);
  if (!isMcpObject(parsed)) return invalid();
  if (parsed.command !== undefined || parsed.url !== undefined) {
    const name = typeof parsed.name === "string" ? parsed.name : "imported-server";
    if (!MCP_NAME_PATTERN.test(name)) invalid();
    return [{ name, config: normalize(parsed) }];
  }
  let servers: unknown = parsed;
  if (parsed.mcpServers !== undefined) servers = parsed.mcpServers;
  else if (isMcpObject(parsed.mcp)) servers = parsed.mcp.servers ?? parsed.mcp;
  else if (parsed.servers !== undefined) servers = parsed.servers; // VS Code mcp.json
  if (!isMcpObject(servers) || !Object.keys(servers).length) return invalid();
  return Object.entries(servers).map(([name, config]) => {
    if (!MCP_NAME_PATTERN.test(name)) return invalid();
    return { name, config: normalize(config) };
  });
}

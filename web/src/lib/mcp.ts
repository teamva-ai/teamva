/** Native OpenCode V2 MCP configuration. This module never persists credentials. */
export const MCP_KEEP_SECRET = "__TEAMVA_KEEP_SECRET__";
export type McpScope = "user" | "project";
export interface McpConfig extends Record<string, unknown> {
  type: "local" | "remote";
  command?: string[];
  cwd?: string;
  url?: string;
  environment?: Record<string, string>;
  headers?: Record<string, string>;
  oauth?: false | Record<string, unknown>;
  disabled?: boolean;
  protocol?: "legacy" | "auto" | "2026-07-28";
  codemode?: boolean;
  timeout?: { startup?: number; catalog?: number; execution?: number };
}
export interface McpView {
  name: string; scope: McpScope; project_id?: string; version: number; config: McpConfig;
  status: string; error?: string; shadowed?: boolean;
  tools?: { name: string; description?: string }[];
  resources?: { name: string; uri: string; description?: string }[];
  prompts?: { name: string; description?: string }[];
}
export interface McpInput { name: string; scope: McpScope; project_id?: string; version: number; config: McpConfig }
export interface McpPair { key: string; value: string; keepKey?: string }
export interface McpDraft {
  name: string; scope: McpScope; type: "local" | "remote"; command: string; cwd: string; url: string;
  environment: McpPair[]; headers: McpPair[]; oauthEnabled: boolean; oauth: Record<string, unknown>;
  clientSecret: string; keepClientSecret: boolean; protocol: string; codemode: string;
  startup: string; catalog: string; execution: string; disabled: boolean; base: McpConfig;
}
export class McpValidationError extends Error {
  constructor(public key: string) { super(key); }
}
export const MCP_NAME_PATTERN = /^[a-z0-9](?:[a-z0-9_-]{0,126}[a-z0-9])?$/;
const secretKeyPattern = /(secret|password|passwd|token|api[-_]?key|authorization|credential|signature|access[-_]?key)/i;
const publicString = (value: string) => !/[\x00\r\n]/.test(value) && !value.includes(MCP_KEEP_SECRET);
function fail(key: string): never { throw new McpValidationError(key); }
export function isMcpObject(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}
export function isMcpHttpURL(value: string): boolean {
  try {
    const url = new URL(value);
    if (!publicString(value) || !["http:", "https:"].includes(url.protocol) || !url.hostname || url.username || url.password || url.hash) return false;
    if (/%(?![a-f\d]{2})/i.test(url.search) || url.search.includes(";")) return false;
    return !Array.from(url.searchParams.keys()).some((key) => secretKeyPattern.test(key) || /^(key|auth)$/i.test(key));
  } catch { return false; }
}
export function mcpDraft(config: McpConfig = { type: "local", command: [] }, name = "", scope: McpScope = "user"): McpDraft {
  const pairs = (map?: Record<string, string>): McpPair[] => Object.entries(map ?? {}).map(([key, value]) =>
    ({ key, value: value === MCP_KEEP_SECRET ? "" : value, ...(value === MCP_KEEP_SECRET ? { keepKey: key } : {}) }));
  const oauth = config.oauth && typeof config.oauth === "object" ? { ...config.oauth } : {};
  const secret = oauth.client_secret;
  delete oauth.client_secret;
  return {
    name, scope, type: config.type, command: (config.command ?? []).join("\n"), cwd: config.cwd ?? "", url: config.url ?? "",
    environment: pairs(config.environment), headers: pairs(config.headers), oauthEnabled: config.oauth !== false,
    oauth, clientSecret: typeof secret === "string" && secret !== MCP_KEEP_SECRET ? secret : "",
    keepClientSecret: secret === MCP_KEEP_SECRET, protocol: config.protocol ?? "legacy",
    codemode: config.codemode === undefined ? "default" : config.codemode ? "on" : "off",
    startup: String(config.timeout?.startup ?? ""), catalog: String(config.timeout?.catalog ?? ""), execution: String(config.timeout?.execution ?? ""),
    disabled: config.disabled === true, base: JSON.parse(JSON.stringify(config)) as McpConfig
  };
}
/** Rebase only unchanged secret inputs after a save; preserve all later edits. */
export function rebaseMcpSecrets(live: McpDraft, submitted: McpDraft, config: McpConfig): McpDraft {
  const next = JSON.parse(JSON.stringify(live)) as McpDraft;
  for (const category of ["environment", "headers"] as const) {
    for (const row of next[category]) {
      const sent = submitted[category].find((item) => item.key === row.key);
      if (sent && row.value === sent.value && row.keepKey === sent.keepKey && config[category]?.[row.key] === MCP_KEEP_SECRET) {
        row.value = ""; row.keepKey = row.key;
      }
    }
  }
  if (next.clientSecret === submitted.clientSecret && next.keepClientSecret === submitted.keepClientSecret && config.oauth && config.oauth.client_secret === MCP_KEEP_SECRET) {
    next.clientSecret = ""; next.keepClientSecret = true;
  }
  return next;
}
function pairsToRecord(pairs: McpPair[], existing: boolean, category: "environment" | "headers"): Record<string, string> {
  const result: Record<string, string> = Object.create(null);
  if (pairs.length > 256) fail("invalidPair");
  for (const row of pairs) {
    const key = row.key.trim();
    if (!key) fail("invalidPair");
    if (Object.hasOwn(result, key)) fail("duplicateKey");
    if (row.value === MCP_KEEP_SECRET) fail("invalidSecret");
    if (new TextEncoder().encode(row.value).length > 65536 || row.value.includes("\0")) fail("invalidPair");
    if (category === "environment" ? !/^[A-Za-z_][A-Za-z0-9_]*$/.test(key) : !/^[!#$%&'*+\-.^_`|~0-9A-Za-z]+$/.test(key) || /^(host|content-length|transfer-encoding|connection|cookie|set-cookie)$/i.test(key) || /[\r\n]/.test(row.value)) fail("invalidPair");
    result[key] = !row.value && row.keepKey === key && existing ? MCP_KEEP_SECRET : row.value;
  }
  return result;
}
export function mcpPositiveInteger(value: unknown, port = false): number | undefined {
  if (value === "" || value === undefined) return undefined;
  if ((typeof value !== "number" && typeof value !== "string") || !/^\d+$/.test(String(value))) fail("invalidNumber");
  const number = Number(value);
  if (!Number.isSafeInteger(number) || number <= 0 || number > (port ? 65535 : 3600000)) fail("invalidNumber");
  return number;
}
export function buildMcpInput(draft: McpDraft, version: number, projectId?: string): McpInput {
  if (!MCP_NAME_PATTERN.test(draft.name)) fail("invalidName");
  if (draft.scope === "project" && !projectId) fail("needProject");
  const config: McpConfig = { ...draft.base, type: draft.type, disabled: draft.disabled };
  delete config.command; delete config.cwd; delete config.url; delete config.headers; delete config.oauth;
  config.environment = pairsToRecord(draft.environment, version > 0, "environment");
  if (draft.type === "local") {
    config.command = draft.command.split("\n");
    if (!config.command.length || config.command.length > 256 || config.command.some((part) => !part.trim() || !publicString(part) || new TextEncoder().encode(part).length > 8192 || ((part.startsWith("-") || part.includes("=")) && secretKeyPattern.test(part.split("=", 1)[0])) || (part.includes("://") && !isMcpHttpURL(part)))) fail("invalidCommand");
    if (draft.cwd.trim()) {
      if (!draft.cwd.trim().startsWith("/") || !publicString(draft.cwd)) fail("invalidCwd");
      config.cwd = draft.cwd.trim();
    }
  } else {
    if (!isMcpHttpURL(draft.url)) fail("invalidURL");
    config.url = draft.url.trim();
    config.headers = pairsToRecord(draft.headers, version > 0, "headers");
    if (!draft.oauthEnabled) config.oauth = false;
    else {
      const oauth = { ...draft.oauth };
      for (const key of ["client_id", "scope", "redirect_uri", "auth_server_metadata_url"]) {
        if (oauth[key] === "") delete oauth[key];
      }
      if (oauth.auth_server_metadata_url && (typeof oauth.auth_server_metadata_url !== "string" || !isMcpHttpURL(oauth.auth_server_metadata_url))) fail("invalidURL");
      if (oauth.redirect_uri) {
        if (typeof oauth.redirect_uri !== "string" || !isMcpHttpURL(oauth.redirect_uri)) fail("invalidRedirect");
        const redirect = new URL(oauth.redirect_uri);
        if (redirect.protocol !== "http:" || redirect.hostname !== "127.0.0.1" || redirect.search) fail("invalidRedirect");
      }
      for (const key of ["client_id", "scope"]) if (oauth[key] !== undefined && (typeof oauth[key] !== "string" || !publicString(oauth[key]))) fail("invalidOAuth");
      const port = mcpPositiveInteger(oauth.callback_port, true);
      if (port === undefined) delete oauth.callback_port; else oauth.callback_port = port;
      if (!publicString(draft.clientSecret)) fail("invalidSecret");
      if (draft.clientSecret) oauth.client_secret = draft.clientSecret;
      else if (draft.keepClientSecret && version > 0) oauth.client_secret = MCP_KEEP_SECRET;
      else delete oauth.client_secret;
      config.oauth = oauth;
    }
  }
  if (!["legacy", "auto", "2026-07-28"].includes(draft.protocol)) fail("invalidProtocol");
  delete config.protocol;
  if (draft.protocol !== "legacy") config.protocol = draft.protocol as McpConfig["protocol"];
  delete config.codemode;
  if (draft.codemode !== "default") config.codemode = draft.codemode === "on";
  const timeout = {
    ...(draft.type === "local" ? { startup: mcpPositiveInteger(draft.startup) } : {}),
    catalog: mcpPositiveInteger(draft.catalog), execution: mcpPositiveInteger(draft.execution)
  };
  config.timeout = Object.fromEntries(Object.entries(timeout).filter(([, value]) => value !== undefined));
  if (!Object.keys(config.timeout).length) delete config.timeout;
  return { name: draft.name, scope: draft.scope, ...(draft.scope === "project" ? { project_id: projectId } : {}), version, config };
}

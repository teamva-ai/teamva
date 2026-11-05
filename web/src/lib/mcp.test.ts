import { describe, expect, it } from "vitest";
import { buildMcpInput, MCP_KEEP_SECRET, mcpDraft, rebaseMcpSecrets, type McpConfig } from "./mcp";
import { parseMcpImport } from "./mcpImport";

describe("MCP editor secret roundtrips", () => {
  const config: McpConfig = { type: "remote", url: "https://mcp.test", environment: { TOKEN: MCP_KEEP_SECRET }, headers: { Authorization: MCP_KEEP_SECRET }, oauth: { client_secret: MCP_KEEP_SECRET, custom_extension: { audience: "private" }, callback_port: 49151 } };
  it("never displays secret placeholders and retains unknown OAuth fields", () => {
    const draft = mcpDraft(config, "docs");
    expect(draft.environment[0].value).toBe("");
    expect(draft.headers[0].value).toBe("");
    expect(draft.clientSecret).toBe("");
    expect(draft.keepClientSecret).toBe(true);
    const result = buildMcpInput(draft, 4);
    expect(result.config).toMatchObject(config);
    expect(result.version).toBe(4);
  });
  it("removes a secret only on explicit row or OAuth secret deletion", () => {
    const draft = mcpDraft(config, "docs");
    draft.environment = [];
    draft.headers[0].value = "replacement";
    draft.keepClientSecret = false;
    const result = buildMcpInput(draft, 2).config;
    expect(result.environment).toEqual({});
    expect(result.headers).toEqual({ Authorization: "replacement" });
    expect(result.oauth).not.toHaveProperty("client_secret");
    expect(result.oauth).toHaveProperty("custom_extension", { audience: "private" });
  });
  it("rejects manually entered placeholders and duplicate/empty keys", () => {
    const draft = mcpDraft({ type: "local", command: ["node"] }, "test");
    draft.environment = [{ key: "TOKEN", value: MCP_KEEP_SECRET }];
    expect(() => buildMcpInput(draft, 0)).toThrow("invalidSecret");
    draft.environment = [{ key: "TOKEN", value: "a" }, { key: "TOKEN", value: "b" }];
    expect(() => buildMcpInput(draft, 1)).toThrow("duplicateKey");
    draft.environment = [{ key: "", value: "a" }];
    expect(() => buildMcpInput(draft, 1)).toThrow("invalidPair");
  });
  it("rebases saved secrets to masked placeholders without losing later edits", () => {
    const submitted = mcpDraft({ ...config, headers: { Authorization: "new-value" } }, "docs");
    const live = structuredClone(submitted);
    live.url = "https://later.test";
    live.environment.push({ key: "LATER", value: "later-value" });
    const result = rebaseMcpSecrets(live, submitted, config);
    expect(result.url).toBe("https://later.test");
    expect(result.environment[1].value).toBe("later-value");
    expect(result.headers[0]).toEqual({ key: "Authorization", value: "", keepKey: "Authorization" });
    expect(buildMcpInput(result, 7).config.headers?.Authorization).toBe(MCP_KEEP_SECRET);
    live.headers[0].value = "even-newer";
    expect(rebaseMcpSecrets(live, submitted, config).headers[0].value).toBe("even-newer");
  });
  it.each(["0", "-1", "1.2", "Infinity", "1e3", " 100 "])("rejects invalid milliseconds %s", (value) => {
    const draft = mcpDraft({ type: "local", command: ["node"] }, "test");
    draft.execution = value;
    expect(() => buildMcpInput(draft, 0)).toThrow("invalidNumber");
  });
  it("writes V2 timeouts, cwd, CodeMode and protocol without shell argument splitting", () => {
    const draft = mcpDraft({ type: "local", command: ["/path with space/mcp", "two words"], cwd: "/work" }, "test", "project");
    draft.startup = "1000"; draft.catalog = "2000"; draft.execution = "3000";
    draft.codemode = "off"; draft.protocol = "2026-07-28";
    expect(buildMcpInput(draft, 0, "project-test")).toMatchObject({ project_id: "project-test", scope: "project", config: { command: ["/path with space/mcp", "two words"], cwd: "/work", timeout: { startup: 1000, catalog: 2000, execution: 3000 }, codemode: false, protocol: "2026-07-28" } });
  });
  it.each(["UPPER", "with.dot", "-prefix", "suffix_", "x".repeat(129)])("rejects server names the backend disallows: %s", (name) => {
    expect(() => buildMcpInput(mcpDraft({ type: "local", command: ["node"] }, name), 0)).toThrow("invalidName");
  });
  it.each(["https://user:password@mcp.test", "https://mcp.test?access_token=secret", "https://mcp.test?api-key=secret", "https://mcp.test?auth=secret", "https://mcp.test#fragment"])("rejects unsafe configuration URL %s", (url) => {
    expect(() => buildMcpInput(mcpDraft({ type: "remote", url }, "docs"), 0)).toThrow("invalidURL");
  });
});

describe("MCP JSON import", () => {
  const local = { command: "npx", args: ["-y", "package", "two words"], env: { TOKEN: "value" } };
  it.each([
    { mcpServers: { first: local } },
    { mcp: { first: { ...local, enabled: false } } },
    { mcp: { servers: { first: { ...local, type: "local" } } } },
    { servers: { first: { ...local, type: "stdio" } }, inputs: [] },
    { first: local }
  ])("imports V1/V2, Claude, VSCode and named map %j", (input) => {
    expect(parseMcpImport(JSON.stringify(input))[0]).toMatchObject({ name: "first", config: { type: "local", command: ["npx", "-y", "package", "two words"], environment: { TOKEN: "value" } } });
  });
  it("imports bare remote config and retains unknown OAuth options", () => {
    const imported = parseMcpImport(JSON.stringify({ type: "remote", url: "https://mcp.test", timeout: 2000, oauth: { clientId: "client", clientSecret: "secret", callbackPort: 6000, future: { yes: true } } }))[0];
    expect(imported.config).toMatchObject({ timeout: { execution: 2000 }, oauth: { client_id: "client", client_secret: "secret", callback_port: 6000, future: { yes: true } } });
    expect(imported.config.oauth).not.toHaveProperty("clientSecret");
  });
  it("returns all servers for explicit selection", () => {
    expect(parseMcpImport(JSON.stringify({ mcpServers: { local, remote: { type: "http", url: "https://mcp.test", oauth: false } } })).map((item) => item.name)).toEqual(["local", "remote"]);
  });
  it.each([
    "", "[]", "null", "{}", '{"command": 5}',
    '{"type":"remote","url":"file:///private"}',
    '{"command":"node","env":{"TOKEN":123}}',
    '{"command":"node","args":"bad"}',
    '{"command":"node","timeout":-1}',
    '{"command":"node","timeout":{"startup":1.5}}',
    '{"command":"node","timeout":{"execution":null}}',
    '{"command":"node","timeout":{"catalog":3600001}}',
    '{"command":"node","timeout":{"execution":"2000"}}',
    '{"command":"node","timeout":{"unknown":2000}}',
    '{"command":"node","env":{"bad-name":"value"}}',
    '{"type":"remote","url":"https://mcp.test","headers":{"Cookie":"a=b"}}',
    '{"type":"remote","url":"https://mcp.test?token=secret"}',
    '{"type":"remote","url":"https://user:secret@mcp.test"}',
    '{"type":"remote","url":"https://mcp.test","oauth":{"redirectUri":"https://public.test/callback"}}',
    '{"type":"remote","url":"https://test","oauth":{"callback_port":65536}}',
    '{"type":"remote","url":"https://test","oauth":{"client_secret":"__TEAMVA_KEEP_SECRET__"}}',
    '{"command":"node","enabled":"yes"}',
    '{"command":"node","protocol":"unknown"}'
  ])("rejects invalid configuration without partial import %s", (text) => {
    expect(() => parseMcpImport(text)).toThrow();
  });
  it("rejects duplicate decoded JSON names rather than silently overwriting", () => {
    expect(() => parseMcpImport('{"mcpServers":{"same":{"command":"node"},"s\\u0061me":{"command":"other"}}}')).toThrow("duplicateKey");
    expect(() => parseMcpImport('{"command":"node","env":{"A":"one","A":"two"}}')).toThrow("duplicateKey");
  });
});

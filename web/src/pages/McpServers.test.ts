import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/svelte";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import ConsolePageTestHost from "../test/ConsolePageTestHost.svelte";
import { queryClient } from "../lib/query";
import { remoteAuth } from "../lib/remoteAuth.svelte";
import { navigate } from "../lib/router.svelte";
import { t } from "../lib/i18n.svelte";
import { MCP_KEEP_SECRET, type McpInput, type McpView } from "../lib/mcp";
import McpServers from "./McpServers.svelte";
import LocalSkills from "./LocalSkills.svelte";

function json(data: unknown): Response { return new Response(JSON.stringify({ data }), { headers: { "Content-Type": "application/json" } }); }
function server(config: Partial<McpView["config"]> = {}): McpView {
  return { name: "docs", scope: "user", version: 3, status: "disconnected", config: { type: "local", command: ["node", "server.js"], ...config } };
}
function redact(view: McpView): McpView {
  const result = structuredClone(view);
  for (const key of ["environment", "headers"] as const) if (result.config[key]) {
    result.config[key] = Object.fromEntries(Object.keys(result.config[key]!).map((name) => [name, MCP_KEEP_SECRET]));
  }
  if (result.config.oauth && result.config.oauth.client_secret) result.config.oauth.client_secret = MCP_KEEP_SECRET;
  return result;
}
function backend(initial: McpView[] = []) {
  let items = initial;
  const calls: Array<{ path: string; method: string; body?: McpInput & Record<string, unknown> }> = [];
  const fetch = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const path = String(input); const method = init?.method ?? "GET";
    const body = init?.body ? JSON.parse(String(init.body)) : undefined;
    calls.push({ path, method, body });
    if (path.startsWith("/v1/mcp/servers") && method === "GET") return json({ items });
    if (path === "/v1/mcp/servers" && method === "POST") {
      const saved = redact({ ...body, status: "disconnected", version: body.version + 1 });
      items = [...items.filter((item) => item.name !== saved.name || item.scope !== saved.scope), saved];
      return json(saved);
    }
    if (method === "DELETE") { items = []; return json({ deleted: true }); }
    if (path.endsWith("/auth")) { items = items.map((item) => ({ ...item, status: "needs_auth" })); return json({ authorization_url: "https://auth.test/authorize", status: "needs_auth" }); }
    if (path.endsWith("/connect") || path.endsWith("/disconnect") || path.endsWith("/revoke")) {
      items = items.map((item) => ({ ...item, status: path.endsWith("/connect") ? "connected" : "disconnected", tools: [{ name: "lookup", description: "Read-only lookup" }], prompts: [{ name: "summary" }], resources: [{ name: "reference", uri: "mcp://docs/reference" }] }));
      return json(items[0]);
    }
    return json({ items: [] });
  });
  vi.stubGlobal("fetch", fetch);
  return { calls, fetch, get items() { return items; }, set items(value) { items = value; } };
}
const posts = (mock: ReturnType<typeof backend>) => mock.calls.filter((call) => call.path === "/v1/mcp/servers" && call.method === "POST");
beforeEach(() => { remoteAuth.active = false; navigate("/skills?tab=mcp", { replace: true }); vi.spyOn(window, "confirm").mockReturnValue(true); });
afterEach(() => { cleanup(); queryClient.clear(); remoteAuth.active = false; vi.unstubAllGlobals(); vi.restoreAllMocks(); vi.useRealTimers(); navigate("/workbench", { replace: true }); });

describe("MCP real HTTP contract", () => {
  it("loads project context and never auto-connects or constantly polls disconnected servers", async () => {
    const mock = backend([server()]);
    render(ConsolePageTestHost, { page: McpServers });
    expect(await screen.findByRole("article", { name: /docs/ })).toBeInTheDocument();
    expect(mock.calls).toEqual([{ path: "/v1/mcp/servers?project_id=project-test", method: "GET", body: undefined }]);
    vi.useFakeTimers();
    await vi.advanceTimersByTimeAsync(6000);
    expect(mock.calls).toHaveLength(1);
  });
  it("does not execute or save a new server until explicit create, then masks secret updates", async () => {
    const mock = backend();
    render(ConsolePageTestHost, { page: McpServers });
    await fireEvent.click(screen.getByRole("button", { name: t("mcp.add") }));
    await fireEvent.input(screen.getByLabelText(t("mcp.name")), { target: { value: "new-server" } });
    await fireEvent.input(screen.getByLabelText(t("mcp.command")), { target: { value: "node\nwith space.js" } });
    await fireEvent.click(screen.getByRole("button", { name: t("mcp.addPair") }));
    await fireEvent.input(screen.getByLabelText(`${t("mcp.key")} 1`), { target: { value: "TOKEN" } });
    await fireEvent.input(screen.getByLabelText(`${t("mcp.value")} 1`), { target: { value: "private-value" } });
    vi.useFakeTimers(); await vi.advanceTimersByTimeAsync(1500); vi.useRealTimers();
    expect(posts(mock)).toHaveLength(0);
    await fireEvent.click(screen.getByRole("button", { name: t("mcp.create") }));
    await waitFor(() => expect(posts(mock)).toHaveLength(1));
    expect(posts(mock)[0].body).toMatchObject({ name: "new-server", scope: "user", version: 0, config: { type: "local", command: ["node", "with space.js"], environment: { TOKEN: "private-value" } } });
    await waitFor(() => expect(screen.getByLabelText(`${t("mcp.value")} 1`)).toHaveValue(""));
    expect(screen.getByLabelText(`${t("mcp.value")} 1`)).toHaveAttribute("placeholder", t("mcp.secretSet"));
    await fireEvent.input(screen.getByLabelText(t("mcp.cwd")), { target: { value: "/new-work" } });
    await waitFor(() => expect(posts(mock)).toHaveLength(2));
    expect(posts(mock)[1].body?.config.environment).toEqual({ TOKEN: MCP_KEEP_SECRET });
    expect(mock.calls.some((call) => /\/connect$/.test(call.path))).toBe(false);
    expect(localStorage.getItem("private-value")).toBeNull();
  });
  it("uses sentinels on unchanged environment/headers/OAuth and preserves unknown OAuth", async () => {
    const mock = backend([server({ type: "remote", command: undefined, url: "https://mcp.test", environment: { TOKEN: MCP_KEEP_SECRET }, headers: { Authorization: MCP_KEEP_SECRET }, oauth: { client_secret: MCP_KEEP_SECRET, future: { audience: "docs" } } })]);
    render(ConsolePageTestHost, { page: McpServers });
    await fireEvent.click(await screen.findByRole("button", { name: t("common.edit") }));
    const dialog = screen.getByRole("dialog");
    for (const password of dialog.querySelectorAll('input[type="password"]')) expect(password).toHaveValue("");
    await fireEvent.input(screen.getByLabelText(t("mcp.catalog")), { target: { value: "3000" } });
    await waitFor(() => expect(posts(mock)).toHaveLength(1));
    expect(posts(mock)[0].body).toMatchObject({ version: 3, config: { environment: { TOKEN: MCP_KEEP_SECRET }, headers: { Authorization: MCP_KEEP_SECRET }, oauth: { client_secret: MCP_KEEP_SECRET, future: { audience: "docs" } }, timeout: { catalog: 3000 } } });
    await fireEvent.click(screen.getByRole("button", { name: t("mcp.deleteSecret") }));
    await waitFor(() => expect(posts(mock)).toHaveLength(2));
    expect(posts(mock)[1].body?.config.oauth).not.toHaveProperty("client_secret");
  });
  it("serializes autosaves and sends later edits with the returned version", async () => {
    let resolve!: (response: Response) => void;
    const pending = new Promise<Response>((done) => { resolve = done; });
    const mock = backend([server()]);
    const regular = mock.fetch.getMockImplementation()!;
    let saves = 0;
    const submitted: McpInput[] = [];
    mock.fetch.mockImplementation(async (input, init) => {
      if (String(input) === "/v1/mcp/servers" && init?.method === "POST") {
        const body = JSON.parse(String(init.body)) as McpInput; submitted.push(body);
        if (++saves === 1) return pending;
      }
      return regular(input, init);
    });
    render(ConsolePageTestHost, { page: McpServers });
    await fireEvent.click(await screen.findByRole("button", { name: t("common.edit") }));
    await fireEvent.input(screen.getByLabelText(t("mcp.cwd")), { target: { value: "/first" } });
    await waitFor(() => expect(submitted).toHaveLength(1));
    await fireEvent.input(screen.getByLabelText(t("mcp.cwd")), { target: { value: "/later" } });
    await fireEvent.input(screen.getByLabelText(t("mcp.execution")), { target: { value: "1234" } });
    expect(submitted).toHaveLength(1);
    resolve(json({ ...server(), config: submitted[0].config, version: 4 }));
    await waitFor(() => expect(submitted).toHaveLength(2));
    expect(submitted[1]).toMatchObject({ version: 4, config: { cwd: "/later", timeout: { execution: 1234 } } });
    expect(screen.getByLabelText(t("mcp.cwd"))).toHaveValue("/later");
  });
  it("preserves a conflicting draft and never blindly retries its version", async () => {
    const mock = backend([server()]);
    const regular = mock.fetch.getMockImplementation()!;
    let attempts = 0;
    mock.fetch.mockImplementation(async (input, init) => {
      if (init?.method === "POST") { attempts++; return new Response(JSON.stringify({ error: { code: "version_conflict" } }), { status: 409 }); }
      return regular(input, init);
    });
    render(ConsolePageTestHost, { page: McpServers });
    await fireEvent.click(await screen.findByRole("button", { name: t("common.edit") }));
    await fireEvent.input(screen.getByLabelText(t("mcp.cwd")), { target: { value: "/my-draft" } });
    expect(await screen.findByRole("alert")).toHaveTextContent(t("mcp.conflict"));
    await fireEvent.input(screen.getByLabelText(t("mcp.cwd")), { target: { value: "/even-later" } });
    vi.useFakeTimers(); await vi.advanceTimersByTimeAsync(4000); vi.useRealTimers();
    expect(attempts).toBe(1);
    expect(screen.getByLabelText(t("mcp.cwd"))).toHaveValue("/even-later");
    expect(screen.getByRole("button", { name: t("common.save") })).toBeDisabled();
  });
  it("does not autosave invalid timeout, preserves HTTP errors without exposing their raw contents", async () => {
    const mock = backend([server()]);
    const regular = mock.fetch.getMockImplementation()!;
    mock.fetch.mockImplementation(async (input, init) => init?.method === "POST" ? new Response(JSON.stringify({ error: { code: "internal", message: "private-provider-diagnostic" } }), { status: 500 }) : regular(input, init));
    render(ConsolePageTestHost, { page: McpServers });
    await fireEvent.click(await screen.findByRole("button", { name: t("common.edit") }));
    await fireEvent.input(screen.getByLabelText(t("mcp.execution")), { target: { value: "-1" } });
    expect(screen.getByRole("alert")).toHaveTextContent(t("mcp.invalidNumber"));
    expect(screen.getByRole("button", { name: t("common.save") })).toBeDisabled();
    await fireEvent.input(screen.getByLabelText(t("mcp.execution")), { target: { value: "1000" } });
    expect(await screen.findByText(t("mcp.failed"))).toBeInTheDocument();
    expect(screen.queryByText("private-provider-diagnostic")).not.toBeInTheDocument();
    expect(screen.getByLabelText(t("mcp.execution"))).toHaveValue("1000");
  });
  it("performs real connect/disconnect/auth/revoke and shows catalog without call buttons", async () => {
    const mock = backend([server({ type: "remote", command: undefined, url: "https://mcp.test", oauth: {} })]);
    const open = vi.spyOn(window, "open").mockReturnValue(null);
    render(ConsolePageTestHost, { page: McpServers });
    await fireEvent.click(await screen.findByRole("button", { name: t("mcp.connect") }));
    await screen.findByRole("button", { name: t("mcp.disconnect") });
    await waitFor(() => expect(screen.getByRole("button", { name: t("mcp.disconnect") })).toBeEnabled());
    expect(mock.calls.find((call) => call.path.endsWith("/connect"))?.body).toEqual({ scope: "user", version: 3 });
    expect(screen.getByText("lookup")).toBeInTheDocument();
    expect(screen.getByText("summary")).toBeInTheDocument();
    expect(screen.getByText("mcp://docs/reference")).toBeInTheDocument();
    await fireEvent.click(screen.getByRole("button", { name: t("mcp.disconnect") }));
    await screen.findByRole("button", { name: t("mcp.connect") });
    await waitFor(() => expect(screen.getByRole("button", { name: t("mcp.auth") })).toBeEnabled());
    await fireEvent.click(screen.getByRole("button", { name: t("mcp.auth") }));
    await screen.findByRole("button", { name: t("mcp.authReady") });
    expect(open).not.toHaveBeenCalled();
    await fireEvent.click(screen.getByRole("button", { name: t("mcp.authReady") }));
    expect(open).toHaveBeenCalledWith("https://auth.test/authorize", "_blank", "noopener,noreferrer");
    await waitFor(() => expect(screen.getByRole("button", { name: t("mcp.revoke") })).toBeEnabled());
    await fireEvent.click(screen.getByRole("button", { name: t("mcp.revoke") }));
    await waitFor(() => expect(screen.queryByRole("button", { name: t("mcp.authReady") })).not.toBeInTheDocument());
    expect(mock.calls.some((call) => call.path.endsWith("/revoke"))).toBe(true);
    expect(mock.calls.some((call) => /\/tools\/.*call/.test(call.path))).toBe(false);
  });
  it("imports selected servers only and rejects duplicates before sending POST", async () => {
    const mock = backend([server()]);
    render(ConsolePageTestHost, { page: McpServers });
    await screen.findByRole("article", { name: /docs/ });
    await fireEvent.click(screen.getByRole("button", { name: t("mcp.import") }));
    await fireEvent.input(screen.getByLabelText(t("mcp.paste")), { target: { value: JSON.stringify({ mcpServers: { docs: { command: "node" }, second: { url: "https://mcp.test", oauth: { future: "retained" } } } }) } });
    await fireEvent.click(screen.getByRole("button", { name: t("mcp.parse") }));
    await fireEvent.click(screen.getByRole("button", { name: t("mcp.importSelected") }));
    expect(screen.getByRole("alert")).toHaveTextContent(t("mcp.duplicate"));
    expect(posts(mock)).toHaveLength(0);
    await fireEvent.click(screen.getByRole("checkbox", { name: /docs/ }));
    await fireEvent.click(screen.getByRole("button", { name: t("mcp.importSelected") }));
    await waitFor(() => expect(posts(mock)).toHaveLength(1));
    expect(posts(mock)[0].body).toMatchObject({ name: "second", version: 0, config: { type: "remote", oauth: { future: "retained" } } });
  });
  it("remote mode shows local-only notice and does not fetch configuration or allow writes", async () => {
    const mock = backend([server()]); remoteAuth.active = true;
    render(ConsolePageTestHost, { page: McpServers });
    expect(screen.getByText(t("mcp.localOnly"))).toBeInTheDocument();
    expect(screen.queryByRole("button")).not.toBeInTheDocument();
    expect(mock.calls).toHaveLength(0);
  });
  it("reloads manager error status after connect returns a generic HTTP failure", async () => {
    const mock = backend([server()]);
    const regular = mock.fetch.getMockImplementation()!;
    mock.fetch.mockImplementation(async (input, init) => {
      if (String(input).endsWith("/connect")) {
        mock.items = [{ ...server(), status: "error", error: "MCP connection failed" }];
        return new Response(JSON.stringify({ error: { code: "mcp_unavailable", message: "private-subprocess-details" } }), { status: 503 });
      }
      return regular(input, init);
    });
    render(ConsolePageTestHost, { page: McpServers });
    await fireEvent.click(await screen.findByRole("button", { name: t("mcp.connect") }));
    expect(await screen.findByText(t("mcp.status.error"))).toBeInTheDocument();
    expect(await screen.findByRole("alert")).toHaveTextContent(t("mcp.failed"));
    expect(mock.calls.filter((call) => call.method === "GET")).toHaveLength(2);
    expect(screen.queryByText("private-subprocess-details")).not.toBeInTheDocument();
  });
  it("polls only a pending authorization/connection and stops after it completes", async () => {
    const mock = backend([{ ...server(), status: "connecting" }]);
    render(ConsolePageTestHost, { page: McpServers });
    await screen.findByText(t("mcp.status.connecting"));
    await waitFor(() => expect(screen.getByRole("button", { name: t("mcp.refresh") })).toBeEnabled());
    mock.items = [{ ...server(), status: "connected" }];
    await waitFor(() => expect(mock.calls.filter((call) => call.method === "GET")).toHaveLength(2), { timeout: 3000 });
    await screen.findByText(t("mcp.status.connected"));
    vi.useFakeTimers();
    await vi.advanceTimersByTimeAsync(8000);
    expect(mock.calls.filter((call) => call.method === "GET")).toHaveLength(2);
  });
  it("retains the authorization URL through auth_pending polling, then stops and removes it on connected", async () => {
    const mock = backend([server({ type: "remote", command: undefined, url: "https://mcp.test", oauth: {} })]);
    const regular = mock.fetch.getMockImplementation()!;
    mock.fetch.mockImplementation(async (input, init) => {
      if (String(input).endsWith("/auth") && init?.method === "POST") {
        mock.items = mock.items.map((item) => ({ ...item, status: "auth_pending" }));
        return json({ authorization_url: "https://auth.test/authorize?state=opaque", status: "auth_pending" });
      }
      return regular(input, init);
    });
    const open = vi.spyOn(window, "open").mockReturnValue(null);
    render(ConsolePageTestHost, { page: McpServers });
    await fireEvent.click(await screen.findByRole("button", { name: t("mcp.auth") }));
    expect(await screen.findByText(t("mcp.status.auth_pending"))).toBeInTheDocument();
    expect(screen.getByRole("button", { name: t("mcp.authReady") })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: t("mcp.connect") })).toBeDisabled();
    expect(open).not.toHaveBeenCalled();
    // Initial GET + the immediate post-auth GET + the first scheduled GET.
    await waitFor(() => expect(mock.calls.filter((call) => call.method === "GET")).toHaveLength(3), { timeout: 3000 });
    expect(screen.getByText(t("mcp.status.auth_pending"))).toBeInTheDocument();
    expect(screen.getByRole("button", { name: t("mcp.authReady") })).toBeInTheDocument();
    await fireEvent.click(screen.getByRole("button", { name: t("mcp.authReady") }));
    expect(open).toHaveBeenCalledWith("https://auth.test/authorize?state=opaque", "_blank", "noopener,noreferrer");
    mock.items = mock.items.map((item) => ({ ...item, status: "connected" }));
    await waitFor(() => expect(mock.calls.filter((call) => call.method === "GET")).toHaveLength(4), { timeout: 3000 });
    await waitFor(() => expect(screen.queryByRole("button", { name: t("mcp.authReady") })).not.toBeInTheDocument());
    expect(screen.getByText(t("mcp.status.connected"))).toBeInTheDocument();
    expect(screen.queryByText(t("mcp.authPending"))).not.toBeInTheDocument();
    vi.useFakeTimers();
    await vi.advanceTimersByTimeAsync(10000);
    expect(mock.calls.filter((call) => call.method === "GET")).toHaveLength(4);
  }, 10000);
  it.each([{ callback_port: 49152 }, { redirect_uri: "http://127.0.0.1:49152/callback" }])("explains fixed native OAuth callback configuration and disables only page authorization: %j", async (oauth) => {
    backend([server({ type: "remote", command: undefined, url: "https://mcp.test", oauth })]);
    render(ConsolePageTestHost, { page: McpServers });
    const auth = await screen.findByRole("button", { name: t("mcp.auth") });
    expect(auth).toBeDisabled();
    expect(auth).toHaveAttribute("title", t("mcp.managerOAuthHint"));
    expect(screen.getByRole("button", { name: t("mcp.connect") })).toBeEnabled();
    expect(screen.getByText(t("mcp.managerOAuthHint"))).toBeInTheDocument();
    await fireEvent.click(screen.getByRole("button", { name: t("common.edit") }));
    expect(within(screen.getByRole("dialog")).getByText(t("mcp.managerOAuthHint"))).toBeInTheDocument();
    expect(within(screen.getByRole("dialog")).getByText(t("mcp.unsupportedOAuth"))).toBeInTheDocument();
  });
  it("shows project override and deletes with the exact scope/project/version contract", async () => {
    const mock = backend([{ ...server(), shadowed: true }, { ...server(), scope: "project", project_id: "project-test", version: 8 }]);
    render(ConsolePageTestHost, { page: McpServers });
    expect(await screen.findByText(t("mcp.shadowed"))).toBeInTheDocument();
    const personal = screen.getByRole("article", { name: `docs (${t("mcp.user")})` });
    const project = screen.getByRole("article", { name: `docs (${t("mcp.project")})` });
    expect(within(personal).getByRole("button", { name: t("mcp.connect") })).toBeDisabled();
    await fireEvent.click(within(project).getByRole("button", { name: t("common.delete") }));
    await waitFor(() => expect(mock.calls.some((call) => call.method === "DELETE")).toBe(true));
    expect(mock.calls.find((call) => call.method === "DELETE")?.path).toBe("/v1/mcp/servers/docs?scope=project&version=8&project_id=project-test");
  });
  it("ignores late save responses after a discarded editor is reopened", async () => {
    let resolve!: (response: Response) => void;
    const pending = new Promise<Response>((done) => { resolve = done; });
    const mock = backend([server()]);
    const regular = mock.fetch.getMockImplementation()!;
    mock.fetch.mockImplementation(async (input, init) => String(input) === "/v1/mcp/servers" && init?.method === "POST" ? pending : regular(input, init));
    render(ConsolePageTestHost, { page: McpServers });
    await fireEvent.click(await screen.findByRole("button", { name: t("common.edit") }));
    await fireEvent.input(screen.getByLabelText(t("mcp.cwd")), { target: { value: "/discarded" } });
    await fireEvent.click(screen.getByRole("button", { name: t("common.save") }));
    await screen.findByText(t("mcp.saving"));
    await fireEvent.click(within(screen.getByRole("dialog")).getAllByRole("button", { name: t("common.close") })[1]);
    await fireEvent.click(screen.getByRole("button", { name: t("mcp.add") }));
    await fireEvent.input(screen.getByLabelText(t("mcp.name")), { target: { value: "new-draft" } });
    resolve(json({ ...server(), version: 4 }));
    await waitFor(() => expect(screen.getByLabelText(t("mcp.name"))).toHaveValue("new-draft"));
    expect(screen.getByRole("button", { name: t("mcp.create") })).toBeInTheDocument();
  });
  it("prompts before navigating away from an unsaved new server", async () => {
    backend(); render(ConsolePageTestHost, { page: McpServers });
    await fireEvent.click(screen.getByRole("button", { name: t("mcp.add") }));
    await fireEvent.input(screen.getByLabelText(t("mcp.name")), { target: { value: "unsaved" } });
    vi.mocked(window.confirm).mockReturnValue(false);
    navigate("/skills");
    expect(screen.getByRole("dialog")).toBeInTheDocument();
    expect(window.location.search).toBe("?tab=mcp");
    expect(window.confirm).toHaveBeenCalledWith(t("common.unsavedChanges"));
  });
});

describe("Skills & MCP tabs", () => {
  it("defaults to the existing skills installer and supports URL deep links", async () => {
    const mock = backend(); navigate("/skills", { replace: true });
    render(ConsolePageTestHost, { page: LocalSkills });
    expect(screen.getByRole("tab", { name: t("mcp.skillsTab") })).toHaveAttribute("aria-selected", "true");
    expect(screen.getByRole("heading", { name: t("mcp.title") })).toBeInTheDocument();
    await waitFor(() => expect(mock.calls.some((call) => call.path.includes("/skill-packages"))).toBe(true));
    expect(mock.calls.some((call) => call.path.includes("/mcp/"))).toBe(false);
    await fireEvent.click(screen.getByRole("tab", { name: t("mcp.tab") }));
    expect(window.location.search).toBe("?tab=mcp");
    expect(await screen.findByText(t("mcp.empty"))).toBeInTheDocument();
    expect(screen.getByRole("tab", { name: t("mcp.tab") })).toHaveAttribute("aria-selected", "true");
  });
  it("opens MCP directly without fetching skill packages", async () => {
    const mock = backend(); render(ConsolePageTestHost, { page: LocalSkills });
    expect(await screen.findByText(t("mcp.empty"))).toBeInTheDocument();
    expect(mock.calls.every((call) => call.path.startsWith("/v1/mcp/servers"))).toBe(true);
  });
  it("switches accessible tabs with arrow keys", async () => {
    backend(); navigate("/skills", { replace: true });
    render(ConsolePageTestHost, { page: LocalSkills });
    await fireEvent.keyDown(screen.getByRole("tab", { name: t("mcp.skillsTab") }), { key: "ArrowRight" });
    expect(await screen.findByRole("tabpanel", { name: t("mcp.tab") })).toBeInTheDocument();
    expect(screen.getByRole("tab", { name: t("mcp.tab") })).toHaveFocus();
  });
});

import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/svelte";
import { afterEach, describe, expect, it, vi } from "vitest";
import { clearComposerDrafts, composerDraftKey, readComposerDraft, saveComposerDraft } from "../lib/composerDrafts";
import { t } from "../lib/i18n.svelte";
import { queryClient } from "../lib/query";
import type { Agent, AgentService, Issue, IssueComment, IssueWorkItemSpec } from "../lib/types";
import IssuePanelTestHost from "./IssuePanelTestHost.svelte";
import IssuesTestHost from "./IssuesTestHost.svelte";
import { navigate } from "../lib/router.svelte";

const stamp = "2026-11-02T00:00:00Z";
const versioned = { version: 1, created_at: stamp, updated_at: stamp };
const agents: Agent[] = [
  { ...versioned, id: "builder", name: "Builder", computer_id: "computer", agent_service_id: "opencode", current_revision_id: "builder-revision", status: "active" },
  { ...versioned, id: "reviewer", name: "Reviewer", computer_id: "computer", agent_service_id: "codex", current_revision_id: "reviewer-revision", status: "active" }
];
const services: AgentService[] = [
  { ...versioned, id: "opencode", computer_id: "computer", provider: "opencode", provider_version: "fixture", login_status: "authenticated", status: "ready", capabilities: { no_tools: true }, capability_version: 1 },
  { ...versioned, id: "codex", computer_id: "computer", provider: "codex", provider_version: "fixture", login_status: "authenticated", status: "ready", capabilities: { no_tools: false }, capability_version: 1 }
];
const issue: Issue = { ...versioned, id: "issue-test", project_id: "project-test", title: "Work", status: "open", worktree_hint: false, created_by_user_id: "user" };
const work = (key = "first", agent = "builder", title = "Implementation", after: string[] = []): IssueWorkItemSpec => ({ key, agent_id: agent, title, instruction: "", after });
const json = (data: unknown) => new Response(JSON.stringify({ data }), { status: 200, headers: { "Content-Type": "application/json" } });
const failure = () => new Response(JSON.stringify({ error: { code: "runtime_unavailable" } }), { status: 503, headers: { "Content-Type": "application/json" } });
const textbox = () => screen.getByLabelText(t("issues.commentInput"));
const editor = () => screen.getByTestId("issue-work-composer");
const titles = () => within(editor()).getAllByLabelText(t("issueWork.workTitle"));

function fixture(handler?: (path: string, init?: RequestInit) => Response | Promise<Response> | undefined) {
  vi.stubGlobal("EventSource", undefined);
  const posts: Array<{ path: string; body: Record<string, unknown>; headers: Record<string, string> }> = [];
  let comments: IssueComment[] = [];
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const path = new URL(String(input), "http://test").pathname;
    if (init?.method === "POST") posts.push({ path, body: JSON.parse(String(init.body)), headers: init.headers as Record<string, string> });
    const custom = handler?.(path, init);
    if (custom) return custom;
    if (path.endsWith("/comments") && init?.method === "POST") {
      const body = JSON.parse(String(init.body));
      const comment: IssueComment = { ...versioned, id: "comment-created", issue_id: issue.id, author_kind: "user", body: body.body, status: "published",
        work_items: body.work_items?.map((item: IssueWorkItemSpec) => ({ ...item, status: "waiting_dependencies", blocked_by: item.after })) };
      comments = [comment];
      return json(comment);
    }
    return json({ items: path.endsWith("/comments") ? comments : [] });
  }));
  return { posts, host: render(IssuePanelTestHost, { repositories: [], agents, services, issue }) };
}

async function multiple() {
  await fireEvent.input(textbox(), { target: { value: "@Builder @Reviewer background" } });
  return editor();
}

afterEach(() => { cleanup(); clearComposerDrafts(); queryClient.clear(); vi.unstubAllGlobals(); vi.restoreAllMocks(); navigate("/", { replace: true }); });

describe("work assignment entry and execution boundary", () => {
  it.each(["click", "keyboard"])("intercepts the second distinct Agent through %s without inserting its token", async (method) => {
    const { posts } = fixture();
    await fireEvent.input(textbox(), { target: { value: "@Builder @Rev" } });
    if (method === "click") await fireEvent.click(screen.getByRole("button", { name: "Reviewer" }));
    else await fireEvent.keyDown(textbox(), { key: "Enter" });
    expect(textbox()).toHaveValue("@Builder @Rev");
    expect(within(editor()).getAllByLabelText(t("issueWork.owner")).map((element) => (element as HTMLSelectElement).value)).toEqual(["builder", "reviewer"]);
    expect(posts).toEqual([]);
  });

  it.each(["submit", "ctrl", "meta"])("guards %s for an old draft with multiple typed mentions", async (method) => {
    saveComposerDraft(composerDraftKey("project-test", issue.id), { text: "@Builder @Reviewer background" });
    const { posts } = fixture();
    expect(screen.queryByTestId("issue-work-composer")).not.toBeInTheDocument();
    if (method === "submit") await fireEvent.submit(textbox().closest("form")!);
    else await fireEvent.keyDown(textbox(), { key: "Enter", ctrlKey: method === "ctrl", metaKey: method === "meta" });
    expect(editor()).toBeInTheDocument();
    expect(posts).toEqual([]);
    expect(textbox()).toHaveValue("@Builder @Reviewer background");
  });

  it("opens immediately for pasted multiple valid mentions, but not email/code/ambiguous targets", async () => {
    const { posts } = fixture();
    await multiple();
    await fireEvent.submit(textbox().closest("form")!);
    expect(posts).toEqual([]);
    await fireEvent.click(within(editor()).getByRole("button", { name: t("issueWork.cancel") }));
    await fireEvent.input(textbox(), { target: { value: "@Builder `@Reviewer` owner@Reviewer.test" } });
    expect(screen.queryByTestId("issue-work-composer")).not.toBeInTheDocument();
    await fireEvent.submit(textbox().closest("form")!);
    await waitFor(() => expect(posts).toHaveLength(1));
    expect(posts[0].body.mentioned_agent_ids).toEqual(["builder"]);
    expect(posts[0].body).not.toHaveProperty("work_items");
  });

  it("keeps repeated mentions of one Agent on the old direct-send path", async () => {
    const { posts } = fixture();
    await fireEvent.input(textbox(), { target: { value: "@Builder @Builder please work" } });
    expect(screen.queryByTestId("issue-work-composer")).not.toBeInTheDocument();
    await fireEvent.keyDown(textbox(), { key: "Enter", ctrlKey: true });
    await waitFor(() => expect(posts).toHaveLength(1));
    expect(posts[0].body).toEqual({ body: "@Builder @Builder please work", mentioned_agent_ids: ["builder"] });
    expect(posts[0].headers["Idempotency-Key"]).toBeUndefined();
  });

  it("offers separate non-nested multiple-work actions and sends repeated owners only through work_items", async () => {
    const { posts } = fixture();
    await fireEvent.input(textbox(), { target: { value: "@" } });
    const action = screen.getByRole("button", { name: t("issueWork.multiple", { agent: "Builder" }) });
    expect(action.parentElement?.tagName).not.toBe("BUTTON");
    expect(document.querySelector("button button")).toBeNull();
    await fireEvent.click(action);
    await fireEvent.input(titles()[0], { target: { value: "Implementation" } });
    await fireEvent.input(titles()[1], { target: { value: "Review" } });
    const rows = editor().querySelectorAll("fieldset");
    await fireEvent.click(within(rows[1]).getByRole("checkbox", { name: "Implementation" }));
    await fireEvent.submit(textbox().closest("form")!);
    await waitFor(() => expect(posts).toHaveLength(1));
    const sent = posts[0];
    expect(sent.headers["Idempotency-Key"]).toMatch(/^issue-work-/);
    expect(sent.body.mentioned_agent_ids).toEqual([]);
    const items = sent.body.work_items as IssueWorkItemSpec[];
    expect(items.map((item) => item.agent_id)).toEqual(["builder", "builder"]);
    expect(new Set(items.map((item) => item.key)).size).toBe(2);
    expect(items[1].after).toEqual([items[0].key]);
    await waitFor(() => expect(screen.queryByTestId("issue-work-composer")).not.toBeInTheDocument());
    const list = await screen.findByRole("list", { name: t("issueWork.title") });
    expect(list).toHaveTextContent(t("issueWork.waiting_dependencies"));
    expect(list).toHaveTextContent(t("issueWork.blockedBy", { items: "Implementation" }));
  });

  it("blocks deletion of required work instead of silently turning dependent work into immediate execution", async () => {
    const { posts } = fixture();
    await multiple();
    await fireEvent.input(titles()[0], { target: { value: "A" } });
    await fireEvent.input(titles()[1], { target: { value: "B" } });
    const rows = editor().querySelectorAll("fieldset");
    await fireEvent.click(within(rows[1]).getByRole("checkbox", { name: "A" }));
    const snapshot = readComposerDraft(composerDraftKey("project-test", issue.id));
    expect(within(rows[0]).getByText(t("issueWork.referencedBy", { items: "B" }))).toBeInTheDocument();
    const remove = within(rows[0]).getByRole("button", { name: t("issueWork.remove", { index: 1 }) });
    expect(remove).toBeDisabled();
    await fireEvent.click(remove);
    expect(readComposerDraft(composerDraftKey("project-test", issue.id)).workItems).toEqual(snapshot.workItems);
    expect(within(rows[1]).getByText(t("issueWork.waitForAll", { count: 1 }))).toBeInTheDocument();
    expect(screen.getByText(t("issueWork.executionCounts", { immediate: 1, waiting: 1 }))).toBeInTheDocument();
    await fireEvent.click(within(rows[0]).getByRole("checkbox", { name: "B" }));
    expect(within(editor()).getByRole("alert")).toHaveTextContent(t("issueWork.cycle"));
    await fireEvent.submit(textbox().closest("form")!);
    expect(posts).toEqual([]);
    // The user must explicitly remove the incoming dependency before deleting A.
    await fireEvent.click(within(rows[1]).getByRole("checkbox", { name: "A" }));
    expect(remove).toBeEnabled();
    await fireEvent.click(within(rows[0]).getByRole("button", { name: t("issueWork.remove", { index: 1 }) }));
    const draft = readComposerDraft(composerDraftKey("project-test", issue.id));
    expect(draft.workItems).toHaveLength(1);
    expect(draft.workItems[0].after).toEqual([]);
  });

  it("keys row identity by work key so deleting an earlier item preserves expanded controls on the surviving work", async () => {
    fixture();
    await multiple();
    await fireEvent.input(titles()[0], { target: { value: "A" } });
    await fireEvent.input(titles()[1], { target: { value: "B" } });
    const survivor = editor().querySelectorAll("fieldset")[1];
    const details = survivor.querySelector("details")!;
    expect(details.open).toBe(false);
    expect(within(survivor).getByText(t("issueWork.immediate"))).toBeInTheDocument();
    await fireEvent.click(details.querySelector("summary")!);
    details.open = true;
    await fireEvent.click(within(editor().querySelectorAll("fieldset")[0]).getByRole("button", { name: t("issueWork.remove", { index: 1 }) }));
    expect(editor().querySelector("fieldset")).toBe(survivor);
    expect(details.open).toBe(true);
    expect(screen.getByRole("button", { name: t("issueWork.send", { count: 1 }) })).toBeEnabled();
  });

  it("allows assignments with an empty background while ordinary comments still require text", async () => {
    const { posts } = fixture();
    await multiple();
    await fireEvent.input(titles()[0], { target: { value: "A" } });
    await fireEvent.input(titles()[1], { target: { value: "B" } });
    await fireEvent.input(textbox(), { target: { value: "" } });
    expect(screen.getByRole("button", { name: t("issueWork.send", { count: 2 }) })).toBeEnabled();
    await fireEvent.submit(textbox().closest("form")!);
    await waitFor(() => expect(posts).toHaveLength(1));
    expect(posts[0].body.body).toBe("");
    expect(posts[0].body.mentioned_agent_ids).toEqual([]);
    await waitFor(() => expect(screen.getByRole("button", { name: t("issues.send") })).toBeDisabled());
  });
});

describe("work draft lifecycle and idempotent retries", () => {
  it("keeps work-only edits during a delayed response and blocks same-tick duplicate shortcuts", async () => {
    let resolve!: (response: Response) => void;
    const response = new Promise<Response>((done) => resolve = done);
    const { posts } = fixture((_path, init) => init?.method === "POST" ? response : undefined);
    await multiple();
    await fireEvent.input(titles()[0], { target: { value: "A" } });
    await fireEvent.input(titles()[1], { target: { value: "B" } });
    textbox().dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", ctrlKey: true, bubbles: true, cancelable: true }));
    textbox().dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", metaKey: true, bubbles: true, cancelable: true }));
    await waitFor(() => expect(posts).toHaveLength(1));
    await fireEvent.input(titles()[0], { target: { value: "New edit" } });
    resolve(json({ ...versioned, id: "sent", issue_id: issue.id, author_kind: "user", body: "background", status: "published", work_items: [] }));
    await waitFor(() => expect(screen.getByRole("button", { name: t("issueWork.send", { count: 2 }) })).toBeEnabled());
    expect(titles()[0]).toHaveValue("New edit");
  });

  it("retains a failed work draft across unmounts and reuses the idempotency key on retry", async () => {
    let fail = true;
    const { posts, host } = fixture((_path, init) => init?.method === "POST" && fail ? failure() : undefined);
    await multiple();
    await fireEvent.input(titles()[0], { target: { value: "A" } });
    await fireEvent.input(titles()[1], { target: { value: "B" } });
    await fireEvent.submit(textbox().closest("form")!);
    expect(await within(textbox().closest("form")!).findByRole("alert")).toBeInTheDocument();
    expect(titles()[0]).toHaveValue("A");
    host.unmount();
    render(IssuePanelTestHost, { repositories: [], agents, services, issue });
    expect(titles()[0]).toHaveValue("A");
    fail = false;
    await fireEvent.submit(textbox().closest("form")!);
    await waitFor(() => expect(posts).toHaveLength(2));
    expect(posts[1].headers["Idempotency-Key"]).toBe(posts[0].headers["Idempotency-Key"]);
    expect(posts[1].body).toEqual(posts[0].body);
  });

  it("isolates work mode and work rows when switching tasks and restores them when returning", async () => {
    const { host } = fixture();
    await multiple();
    await fireEvent.input(titles()[0], { target: { value: "Keep this" } });
    await host.rerender({ repositories: [], agents, services, issue: { ...issue, id: "other" } });
    expect(textbox()).toHaveValue("");
    expect(screen.queryByTestId("issue-work-composer")).not.toBeInTheDocument();
    await host.rerender({ repositories: [], agents, services, issue });
    expect(titles()[0]).toHaveValue("Keep this");
  });
});

describe("explicit tool-free AI organization", () => {
  it("only plans on click with eligible planners and allowed executors; changes require review/update", async () => {
    const plan = [{ ...work(), instruction: "Detailed generated instructions" }, { ...work("review", "reviewer", "Review", ["first"]), instruction: "Also detailed" }];
    const { posts } = fixture((path) => path.endsWith("/work-plan") ? json({ work_items: plan }) : undefined);
    await multiple();
    expect(posts).toEqual([]);
    const planner = within(editor()).getByRole("combobox", { name: t("issueWork.planner") });
    expect(within(planner).getAllByRole("option").map((element) => (element as HTMLOptionElement).value)).toEqual(["builder"]);
    await fireEvent.click(within(editor()).getByRole("button", { name: t("issueWork.organize") }));
    await waitFor(() => expect(titles()[0]).toHaveValue("Implementation"));
    expect([...editor().querySelectorAll("fieldset > details:first-of-type")].every((element) => !(element as HTMLDetailsElement).open)).toBe(true);
    expect(posts[0].body).toEqual({ agent_id: "builder", input: "@Builder @Reviewer background", agent_ids: ["builder", "reviewer"] });
    await fireEvent.input(textbox(), { target: { value: "Updated background" } });
    expect(within(editor()).getByText(t("issueWork.stale"))).toBeInTheDocument();
    await fireEvent.keyDown(textbox(), { key: "Enter", metaKey: true });
    expect(posts).toHaveLength(1);
    await fireEvent.click(within(editor()).getByRole("button", { name: t("issueWork.keepManual") }));
    expect(screen.getByRole("button", { name: t("issueWork.send", { count: 2 }) })).toBeEnabled();
    await fireEvent.click(within(editor()).getByRole("button", { name: t("issueWork.organize") }));
    const confirmation = await screen.findByRole("dialog", { name: t("issueWork.replace") });
    expect(posts).toHaveLength(1);
    await fireEvent.click(within(confirmation).getByRole("button", { name: t("common.cancel") }));
    expect(titles()[0]).toHaveValue("Implementation");
  });

  it("rejects an AI plan that assigns work outside the allowed set without replacing manual rows", async () => {
    const { posts } = fixture((path) => path.endsWith("/work-plan") ? json({ work_items: [work("evil", "not-allowed", "Must not apply")] }) : undefined);
    await multiple();
    await fireEvent.input(titles()[0], { target: { value: "Manual" } });
    await fireEvent.click(within(editor()).getByRole("button", { name: t("issueWork.organize") }));
    const confirmation = await screen.findByRole("dialog", { name: t("issueWork.replace") });
    expect(posts).toEqual([]);
    await fireEvent.click(within(confirmation).getByRole("button", { name: t("common.confirm") }));
    expect(await within(editor()).findByText(t("issueWork.invalidPlan"))).toBeInTheDocument();
    expect(titles()[0]).toHaveValue("Manual");
    expect(posts).toHaveLength(1);
  });

  it.each(["input", "work", "cancel", "switch"])("aborts and ignores an old plan after %s, even if the response ignores cancellation", async (action) => {
    let resolve!: (response: Response) => void;
    let signal: AbortSignal | undefined;
    const response = new Promise<Response>((done) => resolve = done);
    const { host } = fixture((path, init) => {
      if (path.endsWith("/work-plan")) { signal = init?.signal ?? undefined; return response; }
    });
    await multiple();
    await fireEvent.click(within(editor()).getByRole("button", { name: t("issueWork.organize") }));
    await waitFor(() => expect(signal).toBeDefined());
    if (action === "input") await fireEvent.input(textbox(), { target: { value: "Changed" } });
    if (action === "work") await fireEvent.input(titles()[0], { target: { value: "New manual work" } });
    if (action === "cancel") await fireEvent.click(within(editor()).getByRole("button", { name: t("issueWork.cancel") }));
    if (action === "switch") await host.rerender({ repositories: [], agents, services, issue: { ...issue, id: "other" } });
    expect(signal!.aborted).toBe(true);
    resolve(json({ work_items: [work("late", "builder", "Old plan must not apply")] }));
    await Promise.resolve();
    await waitFor(() => expect(screen.queryByDisplayValue("Old plan must not apply")).not.toBeInTheDocument());
    if (action === "work") expect(titles()[0]).toHaveValue("New manual work");
    if (action === "switch") {
      await host.rerender({ repositories: [], agents, services, issue });
      expect(titles()[0]).toHaveValue("");
    }
  });

  it("does not offer Codex as planner when it is the only configured executor", async () => {
    const { host } = fixture();
    await host.rerender({ repositories: [], agents: [agents[1]], services, issue });
    await fireEvent.input(textbox(), { target: { value: "@Rev" } });
    await fireEvent.click(screen.getByRole("button", { name: t("issueWork.multiple", { agent: "Reviewer" }) }));
    expect(within(editor()).getAllByRole("combobox", { name: t("issueWork.owner") }).map((element) => (element as HTMLSelectElement).value)).toEqual(["reviewer", "reviewer"]);
    expect(within(within(editor()).getByRole("combobox", { name: t("issueWork.planner") })).queryByRole("option", { name: "Reviewer" })).not.toBeInTheDocument();
    expect(within(editor()).getByText(t("issueWork.noPlanner"))).toBeInTheDocument();
    expect(within(editor()).getByRole("button", { name: t("issueWork.organize") })).toBeDisabled();
  });

  it("keeps a selected unavailable organizer visible and never silently switches to another Provider", async () => {
    const alternative: Agent = { ...agents[0], id: "alternative", name: "Other planner", agent_service_id: "other-provider", current_revision_id: "other-revision" };
    const alternateService: AgentService = { ...services[0], id: "other-provider", provider: "other-provider" };
    const { host, posts } = fixture((path) => path.endsWith("/work-plan") ? json({ work_items: [work()] }) : undefined);
    await multiple();
    const picker = within(editor()).getByRole("combobox", { name: t("issueWork.planner") });
    expect(picker).toHaveValue("builder");
    await host.rerender({ repositories: [], agents: [...agents, alternative], services: [{ ...services[0], status: "offline" }, services[1], alternateService], issue });
    expect(picker).toHaveValue("builder");
    expect(within(picker).getByRole("option", { name: t("issueWork.plannerUnavailable", { agent: "Builder" }) })).toBeDisabled();
    const organize = within(editor()).getByRole("button", { name: t("issueWork.organize") });
    expect(organize).toBeDisabled();
    await fireEvent.click(organize);
    expect(posts).toEqual([]);
    await fireEvent.change(picker, { target: { value: alternative.id } });
    expect(organize).toBeEnabled();
    await fireEvent.click(organize);
    await waitFor(() => expect(posts).toHaveLength(1));
    expect(posts[0].body.agent_id).toBe(alternative.id);
  });

  it.each(["input", "work", "switch"])("dismisses an obsolete replacement confirmation after %s without requesting a plan", async (action) => {
    const { host, posts } = fixture();
    await multiple();
    await fireEvent.input(titles()[0], { target: { value: "Manual work must stay" } });
    await fireEvent.click(within(editor()).getByRole("button", { name: t("issueWork.organize") }));
    expect(await screen.findByRole("dialog", { name: t("issueWork.replace") })).toBeInTheDocument();
    if (action === "input") await fireEvent.input(textbox(), { target: { value: "Updated" } });
    if (action === "work") await fireEvent.input(titles()[0], { target: { value: "Updated manual work" } });
    if (action === "switch") await host.rerender({ repositories: [], agents, services, issue: { ...issue, id: "other" } });
    expect(screen.queryByRole("dialog", { name: t("issueWork.replace") })).not.toBeInTheDocument();
    expect(posts).toEqual([]);
    if (action !== "switch") expect(titles()[0]).toHaveValue(action === "work" ? "Updated manual work" : "Manual work must stay");
  });

  it("hydrates real service capabilities before the task panel mounts", async () => {
    let resolveIssues!: (response: Response) => void;
    const configured = agents.map((agent) => ({ ...agent, computer_id: "computer-test" }));
    vi.stubGlobal("EventSource", undefined);
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
      const path = new URL(String(input), "http://test").pathname;
      if (path.endsWith("/projects/project-test/issues")) return new Promise<Response>((resolve) => resolveIssues = resolve);
      if (path.endsWith("/agents")) return json({ items: configured });
      if (path.endsWith("/agent-services")) return json({ items: services });
      if (path.endsWith("/git/repositories")) return json({ repositories: [], generation: 1, truncated: false, warnings: [] });
      return json({ items: [] });
    }));
    navigate(`/issues?issue=${issue.id}`, { replace: true });
    render(IssuesTestHost);
    await waitFor(() => expect(queryClient.getQueryData(["agent-services", "computer-test"])).toEqual({ items: services }));
    resolveIssues(json({ items: [issue] }));
    await screen.findByRole("textbox", { name: t("issues.commentInput") });
    await multiple();
    const picker = within(editor()).getByRole("combobox", { name: t("issueWork.planner") });
    expect(within(picker).getByRole("option", { name: "Builder" })).toBeInTheDocument();
  });
});

describe("published work status", () => {
  it("refreshes from comments, handles unknown states and opens the bound existing session", async () => {
    const item = { ...work(), status: "future_status", blocked_by: [], issue_agent_instance_id: "instance" };
    let status = "future_status";
    fixture((path) => {
      if (path.endsWith("/comments")) return json({ items: [{ ...versioned, id: "published", issue_id: issue.id, author_kind: "user", body: "Background", status: "published", work_items: [{ ...item, status }] }] });
      if (path.endsWith("/agent-instances")) return json({ items: [{ ...versioned, id: "instance", issue_id: issue.id, agent_id: "builder", agent_revision_id: "revision", status: "idle", pending_mention_count: 0 }] });
    });
    expect(await screen.findByText(t("issueWork.unknownStatus", { status }))).toBeInTheDocument();
    status = "waiting_result";
    await queryClient.invalidateQueries({ queryKey: ["issue-comments", issue.id] });
    expect(await screen.findByText(t("issueWork.waiting_result"))).toBeInTheDocument();
    expect(screen.queryByText(t("status.succeeded"))).not.toBeInTheDocument();
    status = "blocked";
    await queryClient.invalidateQueries({ queryKey: ["issue-comments", issue.id] });
    expect(await screen.findByText(t("issueWork.blocked"))).toBeInTheDocument();
    const list = screen.getByRole("list", { name: t("issueWork.title") });
    await fireEvent.click(within(list).getByRole("button", { name: t("issues.openAgentSession") }));
    expect(await screen.findByRole("dialog", { name: t("issues.agentSessionLabel", { agent: "Builder", index: 1 }) })).toBeInTheDocument();
  });
});

import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/svelte";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { GitRepositorySummary } from "../lib/gitChanges";
import { t } from "../lib/i18n.svelte";
import { formatTime } from "../lib/helpers";
import { clearComposerDrafts } from "../lib/composerDrafts";
import { queryClient } from "../lib/query";
import { navigate } from "../lib/router.svelte";
import type { Agent, Issue, IssueAgentInstance, IssueComment, Run } from "../lib/types";
import IssuePanelTestHost from "./IssuePanelTestHost.svelte";
import IssuesTestHost from "./IssuesTestHost.svelte";

afterEach(() => {
  cleanup();
  clearComposerDrafts();
  queryClient.clear();
  vi.unstubAllGlobals();
});

describe("Issue project workspace mentions", () => {
  it("shows a background-only empty hint, not a fabricated description or comment", async () => {
    vi.stubGlobal("EventSource", undefined);
    vi.stubGlobal("fetch", vi.fn(async () => new Response(JSON.stringify({ data: { items: [] }, meta: {} }), {
      status: 200, headers: { "Content-Type": "application/json" }
    })));
    const now = "2026-08-11T10:00:00Z";
    const issue: Issue = {
      id: "issue-test", version: 1, project_id: "project-test", title: "测试", description: "",
      status: "open", worktree_hint: false, created_by_user_id: "user-test", created_at: now, updated_at: now
    };
    const host = render(IssuePanelTestHost, { props: { repositories: [], issue } });
    const empty = await screen.findByText(t("issues.noComments"));
    const timeline = screen.getByRole("region", { name: t("issues.timelineLabel") });
    expect(timeline.querySelector("article")).toBeNull();
    expect(empty.closest("[data-issue-comments-empty]")).toBeInTheDocument();
    expect(empty.closest("[data-issue-comments-empty]")?.className).not.toMatch(/bg-|border|rounded/);
    expect(screen.queryByText(t("issues.noDescription"))).not.toBeInTheDocument();

    // A genuine creation description remains accurately represented.
    await host.rerender({ repositories: [], issue: { ...issue, description: "创建时保存的真实描述" } });
    expect(screen.getByText("创建时保存的真实描述")).toBeInTheDocument();
    expect(timeline.querySelector(`[data-issue-description="${issue.id}"]`)).not.toBeNull();
    expect(screen.getByText(t("issues.noComments"))).toBeInTheDocument();
  });

  it.each(["working", "failed", "published"] as const)("renders an empty %s Agent record as a background status, not a public comment", async (status) => {
    vi.stubGlobal("EventSource", undefined);
    const now = "2026-08-11T10:00:00Z";
    const instance: IssueAgentInstance = {
      id: "instance-test", issue_id: "issue-test", agent_id: "agent-test", agent_revision_id: "revision-test",
      status: "idle", pending_mention_count: 0, version: 1, created_at: now, updated_at: now
    };
    const run: Run = {
      id: "run-test", issue_id: "issue-test", issue_agent_instance_id: instance.id, project_id: "project-test",
      agent_id: "agent-test", status: "failed", terminal_reason: "real-failed-run-details", input: [],
      read_only: false, lease_epoch: 1, purpose: "work", version: 1, created_at: now, updated_at: now
    };
    const comment: IssueComment = {
      id: "comment-user", issue_id: "issue-test", author_kind: "user", body: "@Builder 你好",
      status: "published", version: 1, created_at: now, updated_at: now
    };
    const placeholder: IssueComment = {
      ...comment, id: "comment-placeholder", author_kind: "agent", author_agent_id: "agent-test",
      run_id: run.id, status, body: status === "published" ? "" : "[teamva:working]"
    };
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
      const path = new URL(String(input), "http://teamva.test").pathname;
      let data: unknown = { items: [] };
      if (path.endsWith("/comments")) data = { items: [comment, placeholder] };
      else if (path.endsWith("/agent-instances")) data = { items: [instance] };
      else if (path.endsWith("/runs")) data = { items: [run] };
      else if (path.endsWith("/goal")) data = { goal: null };
      return new Response(JSON.stringify({ data, meta: {} }), { status: 200, headers: { "Content-Type": "application/json" } });
    }));
    render(IssuePanelTestHost, { props: { repositories: [] } });
    const user = await screen.findByText("@Builder 你好");
    expect(user.closest("article")).toHaveAttribute("data-comment-id", "comment-user");
    const placeholderRow = document.querySelector('[data-comment-id="comment-placeholder"]') as HTMLElement;
    expect(placeholderRow).not.toBeNull();
    expect(placeholderRow.tagName).toBe("DIV");
    expect(placeholderRow.querySelector("article, header, strong, .rounded-full")).toBeNull();
    expect(placeholderRow.className).not.toMatch(/bg-|border|rounded/);
    expect(screen.getByText(t("issues.issueMeta", { time: formatTime(now), count: 1 }))).toBeInTheDocument();
    expect(screen.queryByText(t("issues.issueMeta", { time: formatTime(now), count: 2 }))).not.toBeInTheDocument();
    expect(screen.queryByText("[teamva:working]")).not.toBeInTheDocument();
    await fireEvent.click(within(placeholderRow).getByRole("button", { name: t("issues.openAgentSession") }));
    expect(await screen.findByRole("dialog")).toBeInTheDocument();
    expect(await screen.findByText("real-failed-run-details")).toBeInTheDocument();
  });

  it("allows an @ mention without selecting a default repository", async () => {
    vi.stubGlobal("EventSource", undefined);
    const commentBodies: Array<Record<string, unknown>> = [];
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
        const raw =
          typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
        const url = new URL(raw, "http://teamva.test");
        if (url.pathname.endsWith("/issues/issue-test/comments") && init?.method === "POST") {
          commentBodies.push(JSON.parse(String(init.body)) as Record<string, unknown>);
          return new Response(
            JSON.stringify({
              data: {
                id: "comment-test",
                issue_id: "issue-test",
                author_kind: "user",
                body: "@Builder please work",
                status: "published",
                version: 1,
                created_at: "2026-08-11T10:00:00Z",
                updated_at: "2026-08-11T10:00:00Z"
              },
              meta: { request_id: "request-mention-workspace" }
            }),
            { status: 200, headers: { "Content-Type": "application/json" } }
          );
        }
        return new Response(
          JSON.stringify({
            data: { items: [] },
            meta: { request_id: "request-mention-partial" }
          }),
          { status: 200, headers: { "Content-Type": "application/json" } }
        );
      })
    );
    const repositories: GitRepositorySummary[] = [
      {
        id: "repo-partial",
        label: "Nested source",
        root_display: "Nested source",
        resource_ids: [],
        discovery_origin: "ancestor",
        worktree_kind: "main",
        scope: "resource_prefix",
        branch: null,
        operation: null,
        generation: 1,
        state: "clean",
        counts: {},
        warnings: []
      }
    ];
    render(IssuePanelTestHost, { props: { repositories } });

    const input = screen.getByLabelText("Comment input");
    await fireEvent.input(input, { target: { value: "@" } });
    const mention = await screen.findByRole("button", { name: "Builder" });
    expect(mention).toBeEnabled();

    await fireEvent.input(input, { target: { value: "@Builder please work" } });
    const form = input.closest("form");
    expect(form).not.toBeNull();
    await fireEvent.submit(form!);
    await waitFor(() => expect(commentBodies).toHaveLength(1));
    expect(commentBodies[0]).toEqual({
      body: "@Builder please work",
      mentioned_agent_ids: ["agent-test"]
    });
  });

  it("lists each participating agent once and inserts an @ mention on click", async () => {
    vi.stubGlobal("EventSource", undefined);
    const now = "2026-08-11T10:00:00Z";
    const instance = (id: string, agentID: string): IssueAgentInstance => ({
      id,
      issue_id: "issue-test",
      agent_id: agentID,
      agent_revision_id: "revision-test",
      status: "idle",
      pending_mention_count: 0,
      version: 1,
      created_at: now,
      updated_at: now
    });
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL) => {
        const raw =
          typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
        const url = new URL(raw, "http://teamva.test");
        let data: unknown = { items: [] };
        if (url.pathname.endsWith("/issues/issue-test/agent-instances")) {
          // Two sessions of the same agent must collapse into one participant.
          data = {
            items: [
              instance("instance-b2", "agent-test"),
              instance("instance-b1", "agent-test"),
              instance("instance-a1", "agent-architect")
            ]
          };
        }
        return new Response(
          JSON.stringify({ data, meta: { request_id: "request-participants" } }),
          {
            status: 200,
            headers: { "Content-Type": "application/json" }
          }
        );
      })
    );
    const architect: Agent = {
      id: "agent-architect",
      version: 1,
      computer_id: "computer-test",
      agent_service_id: "service-test",
      name: "Codex 架构师",
      status: "active",
      created_at: now,
      updated_at: now
    };
    const builder: Agent = {
      id: "agent-test",
      version: 1,
      computer_id: "computer-test",
      agent_service_id: "service-test",
      name: "Builder",
      status: "active",
      created_at: now,
      updated_at: now
    };
    render(IssuePanelTestHost, { props: { repositories: [], agents: [builder, architect] } });

    expect(await screen.findByText(t("issues.participants"))).toBeInTheDocument();
    const builders = await screen.findAllByRole("button", { name: "Builder" });
    expect(builders).toHaveLength(1);
    expect(screen.getAllByRole("button", { name: "Codex 架构师" })).toHaveLength(1);
    expect(
      screen.queryByText(t("issues.agentSessionLabel", { agent: "Builder", index: 1 }))
    ).not.toBeInTheDocument();

    const input = screen.getByLabelText("Comment input");
    expect(screen.getByText(t("issues.mentionHint"))).toBeInTheDocument();
    expect(builders[0]).toHaveAttribute(
      "title",
      t("issues.mentionAgentHint", { agent: "Builder" })
    );
    await fireEvent.click(builders[0]);
    expect(input).toHaveValue("@Builder ");
    await fireEvent.click(screen.getByRole("button", { name: "Codex 架构师" }));
    expect(input).toHaveValue("@Builder ");
    expect(screen.getByRole("region", { name: t("issueWork.title") })).toBeInTheDocument();
    expect(screen.getAllByLabelText(t("issueWork.owner")).map((control) => (control as HTMLSelectElement).value)).toEqual(["agent-test", "agent-architect"]);
  });

  it("activates an Agent whose name contains spaces", async () => {
    vi.stubGlobal("EventSource", undefined);
    const commentBodies: Array<Record<string, unknown>> = [];
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
        const raw =
          typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
        const url = new URL(raw, "http://teamva.test");
        if (url.pathname.endsWith("/issues/issue-test/comments") && init?.method === "POST") {
          commentBodies.push(JSON.parse(String(init.body)) as Record<string, unknown>);
          return new Response(
            JSON.stringify({
              data: {
                id: "comment-spaced",
                issue_id: "issue-test",
                author_kind: "user",
                body: "请 @Codex 架构师 看一下",
                status: "published",
                version: 1,
                created_at: "2026-08-11T10:00:00Z",
                updated_at: "2026-08-11T10:00:00Z"
              },
              meta: { request_id: "request-mention-spaced" }
            }),
            { status: 200, headers: { "Content-Type": "application/json" } }
          );
        }
        return new Response(
          JSON.stringify({
            data: { items: [] },
            meta: { request_id: "request-mention-spaced-partial" }
          }),
          { status: 200, headers: { "Content-Type": "application/json" } }
        );
      })
    );
    const spacedAgent: Agent = {
      id: "agent-architect",
      version: 1,
      computer_id: "computer-test",
      agent_service_id: "service-test",
      name: "Codex 架构师",
      status: "active",
      created_at: "2026-08-11T10:00:00Z",
      updated_at: "2026-08-11T10:00:00Z"
    };
    render(IssuePanelTestHost, { props: { repositories: [], agents: [spacedAgent] } });

    const input = screen.getByLabelText("Comment input");
    await fireEvent.input(input, { target: { value: "请 @Codex 架构师 看一下" } });
    const form = input.closest("form");
    expect(form).not.toBeNull();
    await fireEvent.submit(form!);
    await waitFor(() => expect(commentBodies).toHaveLength(1));
    expect(commentBodies[0]).toEqual({
      body: "请 @Codex 架构师 看一下",
      mentioned_agent_ids: ["agent-architect"]
    });
  });
});

describe("Issue comment edit and delete", () => {
  const now = "2026-08-11T10:00:00Z";
  const userComment: IssueComment = {
    id: "comment-user",
    issue_id: "issue-test",
    author_kind: "user",
    author_user_id: "user-test",
    body: "人类评论",
    status: "published",
    version: 1,
    created_at: now,
    updated_at: now
  };
  const agentComment: IssueComment = {
    id: "comment-agent",
    issue_id: "issue-test",
    author_kind: "agent",
    author_agent_id: "agent-test",
    body: "AI 回复",
    status: "published",
    version: 2,
    created_at: now,
    updated_at: now
  };

  function jsonResponse(data: unknown): Response {
    return new Response(JSON.stringify({ data, meta: { request_id: "request-comment-actions" } }), {
      status: 200,
      headers: { "Content-Type": "application/json" }
    });
  }

  function stubCommentFetch(): {
    patches: Array<Record<string, unknown>>;
    deletes: string[];
  } {
    const patches: Array<Record<string, unknown>> = [];
    const deletes: string[] = [];
    vi.stubGlobal("EventSource", undefined);
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
        const raw =
          typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
        const url = new URL(raw, "http://teamva.test");
        const method = (init?.method ?? "GET").toUpperCase();
        if (url.pathname === "/v1/issues/issue-test/comments" && method === "GET") {
          return jsonResponse({ items: [userComment, agentComment] });
        }
        if (url.pathname === "/v1/comments/comment-user" && method === "PATCH") {
          patches.push(JSON.parse(String(init?.body)) as Record<string, unknown>);
          return jsonResponse({ ...userComment, body: "人类评论（改正）", version: 2, edited_at: now });
        }
        if (url.pathname === "/v1/comments/comment-user" && method === "DELETE") {
          deletes.push(url.search);
          return new Response(null, { status: 204 });
        }
        if (url.pathname === "/v1/comments/comment-agent" && method === "DELETE") {
          deletes.push(url.search);
          return new Response(null, { status: 204 });
        }
        return jsonResponse({ items: [] });
      })
    );
    return { patches, deletes };
  }

  it("shows edit for user comments and delete for every comment", async () => {
    stubCommentFetch();
    render(IssuePanelTestHost, { props: { repositories: [] } });

    expect(await screen.findByText("人类评论")).toBeInTheDocument();
    expect(await screen.findByText("AI 回复")).toBeInTheDocument();
    for (const id of ["comment-user", "comment-agent"]) {
      const card = document.querySelector(`[data-comment-id="${id}"]`)!;
      expect(card).toHaveClass("bg-surface");
      expect(card.className).not.toContain("bg-accent");
    }
    expect(document.querySelector('[data-comment-id="comment-agent"] > span')).toHaveClass("bg-surface-subtle", "text-secondary");
    expect(screen.getAllByRole("button", { name: t("issues.editComment") })).toHaveLength(1);
    expect(screen.getAllByRole("button", { name: t("issues.deleteComment") })).toHaveLength(2);
  });

  it("edits a user comment with its expected version", async () => {
    const calls = stubCommentFetch();
    render(IssuePanelTestHost, { props: { repositories: [] } });
    await screen.findByText("人类评论");

    await fireEvent.click(screen.getByRole("button", { name: t("issues.editComment") }));
    const editor = screen.getByLabelText(t("issues.editComment"));
    expect(editor).toHaveValue("人类评论");
    await fireEvent.input(editor, { target: { value: "人类评论（改正）" } });
    await fireEvent.click(screen.getByRole("button", { name: t("common.save") }));

    await waitFor(() => expect(calls.patches).toHaveLength(1));
    expect(calls.patches[0]).toEqual({ body: "人类评论（改正）", expected_version: 1 });
  });

  it("deletes a comment after confirmation", async () => {
    const calls = stubCommentFetch();
    const confirm = vi.fn(() => true);
    vi.stubGlobal("confirm", confirm);
    render(IssuePanelTestHost, { props: { repositories: [] } });
    await screen.findByText("AI 回复");

    const article = document.querySelector('[data-comment-id="comment-agent"]');
    expect(article).not.toBeNull();
    await fireEvent.click(
      within(article as HTMLElement).getByRole("button", { name: t("issues.deleteComment") })
    );

    await waitFor(() => expect(calls.deletes).toHaveLength(1));
    expect(confirm).toHaveBeenCalledWith(t("common.confirmDelete"));
    expect(calls.deletes[0]).toBe("?expected_version=2");
  });

  it("keeps the comment when deletion is cancelled", async () => {
    const calls = stubCommentFetch();
    vi.stubGlobal("confirm", vi.fn(() => false));
    render(IssuePanelTestHost, { props: { repositories: [] } });
    await screen.findByText("人类评论");

    const article = document.querySelector('[data-comment-id="comment-user"]');
    expect(article).not.toBeNull();
    await fireEvent.click(
      within(article as HTMLElement).getByRole("button", { name: t("issues.deleteComment") })
    );
    expect(calls.deletes).toHaveLength(0);
  });
});

describe("Issue Changes", () => {
  it("loads changes only for Runs returned by the current Issue", async () => {
    vi.stubGlobal("EventSource", undefined);
    vi.stubGlobal(
      "ResizeObserver",
      class {
        observe() {}
        disconnect() {}
      }
    );
    const requestedChangeRuns: string[] = [];
    const now = "2026-08-11T10:00:00Z";
    const run = (id: string, issueId: string, minute: number): Run => ({
      id,
      issue_id: issueId,
      issue_agent_instance_id: `instance-${id}`,
      project_id: "project-test",
      status: "succeeded",
      read_only: false,
      lease_epoch: 1,
      input: [],
      purpose: "work",
      version: 1,
      created_at: `2026-08-11T10:${String(minute).padStart(2, "0")}:00Z`,
      updated_at: now,
      finished_at: now
    });
    const issueRuns = [
      run("run-current-a", "issue-test", 0),
      run("run-current-b", "issue-test", 1)
    ];
    const foreignRun = run("run-foreign", "issue-other", 2);
    // A foreign Run in an unrelated cache must never become an input to the
    // task Changes view. Its presence guards against broad project aggregation.
    queryClient.setQueryData(["issue-runs", "issue-other"], { items: [foreignRun] });

    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL) => {
        const raw =
          typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
        const url = new URL(raw, "http://teamva.test");
        let data: unknown = { items: [] };
        if (url.pathname.endsWith("/issues/issue-test/runs")) {
          data = { items: issueRuns };
        } else if (url.pathname.includes("/git/changes")) {
          const runId = url.pathname.split("/").at(-3) ?? "";
          requestedChangeRuns.push(runId);
          data = {
            run_id: runId,
            snapshots: [],
            provider_reports: [
              {
                id: `report-${runId}`,
                seq: 1,
                occurred_at: now,
                source: "provider.item.fileChange",
                paths: [`src/${runId}.ts`],
                paths_verified: true,
                scope: "repository",
                truncated: false
              }
            ],
            provider_reports_truncated: false
          };
        }
        return new Response(
          JSON.stringify({ data, meta: { request_id: "request-issue-changes" } }),
          {
            status: 200,
            headers: { "Content-Type": "application/json" }
          }
        );
      })
    );

    render(IssuePanelTestHost, { props: { repositories: [] } });
    const changesButton = await screen.findByRole("button", { name: "Changes" });
    expect(requestedChangeRuns).toEqual([]);
    await fireEvent.click(changesButton);

    expect(await screen.findByRole("dialog", { name: "Changes" })).toHaveAttribute(
      "data-testid",
      "issue-changes-dialog"
    );
    await waitFor(() =>
      expect(new Set(requestedChangeRuns)).toEqual(new Set(["run-current-a", "run-current-b"]))
    );
    expect(requestedChangeRuns).not.toContain("run-foreign");
    expect(await screen.findByRole("option", { name: "All task runs" })).toBeInTheDocument();
    expect(await screen.findByText("run-current-a.ts")).toBeInTheDocument();
    expect(await screen.findByText("run-current-b.ts")).toBeInTheDocument();
  });
});

describe("reactive Agent names", () => {
  afterEach(() => navigate("/", { replace: true }));
  const now = "2026-08-11T10:00:00Z";
  const issue: Issue = {
    id: "issue-test", version: 1, project_id: "project-test", title: "Deep-linked task", description: "",
    status: "open", worktree_hint: false, created_by_user_id: "user-test", created_at: now, updated_at: now
  };
  const agent: Agent = {
    id: "agent-test", version: 1, computer_id: "computer-test", agent_service_id: "service-test",
    name: "QA Codex", status: "active", created_at: now, updated_at: now
  };
  const instance: IssueAgentInstance = {
    id: "name-instance", issue_id: "issue-test", agent_id: agent.id, agent_revision_id: "revision-test",
    status: "idle", pending_mention_count: 0, version: 1, created_at: now, updated_at: now
  };
  const run = {
    id: "name-run", issue_id: "issue-test", project_id: "project-test", agent_id: agent.id,
    issue_agent_instance_id: instance.id, status: "succeeded", input: [], version: 1,
    created_at: now, updated_at: now
  };
  const comment = {
    id: "name-comment", issue_id: "issue-test", author_kind: "agent", author_agent_id: agent.id,
    run_id: run.id, body: "published summary", status: "published", version: 1,
    created_at: now, updated_at: now
  };
  const envelope = (data: unknown) => new Response(JSON.stringify({ data }), {
    status: 200, headers: { "Content-Type": "application/json" }
  });

  function nameFixture(path: string): unknown {
    if (path.endsWith("/projects/project-test/issues")) return { items: [issue] };
    if (path.endsWith("/agents")) return { items: [agent] };
    if (path.endsWith("/comments")) return { items: [comment] };
    if (path.endsWith("/agent-instances")) return { items: [instance] };
    if (path.endsWith("/runs")) return { items: [run] };
    if (path.endsWith("/goal")) return { goal: null };
    if (path.endsWith("/git/repositories")) return { repositories: [], truncated: false, warnings: [], generation: 1 };
    if (path.endsWith("/events")) return { items: [{
      id: "name-output", run_id: run.id, seq: 1, event_type: "output.snapshot",
      payload: { text: "session output", provider_id: "codex" }, occurred_at: now
    }] };
    return { items: [] };
  }

  it("updates existing authors and an open session when Agents arrive late and are renamed", async () => {
    vi.stubGlobal("EventSource", undefined);
    let resolveAgents!: (response: Response) => void;
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
      const path = new URL(String(input), "http://teamva.test").pathname;
      if (path.endsWith("/agents")) return new Promise<Response>((resolve) => { resolveAgents = resolve; });
      return envelope(nameFixture(path));
    }));
    navigate("/issues?issue=issue-test", { replace: true });
    render(IssuesTestHost);
    await fireEvent.click(await screen.findByRole("button", { name: t("issues.openAgentSession") }));
    const heading = await screen.findByRole("heading", { name: t("issues.agentSessionLabel", { agent: agent.id, index: 1 }) });
    expect(await screen.findByText("session output")).toBeInTheDocument();

    await waitFor(() => expect(resolveAgents).toBeDefined());
    resolveAgents(envelope({ items: [agent] }));
    await waitFor(() => expect(queryClient.getQueryData(["agents", agent.computer_id])).toEqual({ items: [agent] }));
    for (const name of ["QA Codex", "Renamed Codex"]) {
      if (name !== agent.name) queryClient.setQueryData(["agents", agent.computer_id], { items: [{ ...agent, name }] });
      const label = t("issues.agentSessionLabel", { agent: name, index: 1 });
      await waitFor(() => expect(heading).toHaveTextContent(label));
      const author = document.querySelector('[data-comment-id="name-comment"] strong');
      expect(author).toHaveTextContent(label);
      expect(screen.getByText("session output").closest("li")).toHaveTextContent(name);
    }
    queryClient.setQueryData(["agents", agent.computer_id], { items: [] });
    await waitFor(() => expect(heading).toHaveTextContent(t("issues.agentSessionLabel", { agent: agent.id, index: 1 })));
    expect(document.querySelector('[data-comment-id="name-comment"] strong')).toHaveTextContent(agent.id);
    expect(screen.getByText("session output").closest("li")).toHaveTextContent(agent.id);
  });

  it("reads names when the Agent query succeeds before the task panel mounts", async () => {
    vi.stubGlobal("EventSource", undefined);
    let resolveIssues!: (response: Response) => void;
    const sameNameAgent = { ...agent, id: "agent-same-name" };
    const commentBodies: Array<Record<string, unknown>> = [];
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const path = new URL(String(input), "http://teamva.test").pathname;
      if (path.endsWith("/comments") && init?.method === "POST") {
        commentBodies.push(JSON.parse(String(init.body)) as Record<string, unknown>);
        return envelope({ ...comment, id: "user-mention", author_kind: "user", author_agent_id: undefined });
      }
      if (path.endsWith("/projects/project-test/issues")) {
        return new Promise<Response>((resolve) => { resolveIssues = resolve; });
      }
      if (path.endsWith("/agents")) return envelope({ items: [agent, sameNameAgent] });
      return envelope(nameFixture(path));
    }));
    navigate("/issues?issue=issue-test", { replace: true });
    render(IssuesTestHost);
    await waitFor(() => expect(queryClient.getQueryData(["agents", agent.computer_id])).toEqual({ items: [agent, sameNameAgent] }));
    expect(document.querySelector('[data-comment-id="name-comment"]')).not.toBeInTheDocument();
    resolveIssues(envelope({ items: [issue] }));
    const author = () => document.querySelector('[data-comment-id="name-comment"] strong');
    await waitFor(() => expect(author()).toHaveTextContent(t("issues.agentSessionLabel", { agent: agent.name, index: 1 })));
    const input = screen.getByRole("textbox", { name: t("issues.commentInput") });
    await fireEvent.input(input, { target: { value: "@" } });
    const candidate = () => document.querySelector<HTMLButtonElement>(`button[data-agent-id="${agent.id}"]`);
    await waitFor(() => expect(candidate()).toBeInTheDocument());
    expect(document.querySelector(`button[data-agent-id="${sameNameAgent.id}"]`)).toBeInTheDocument();
    expect(candidate()).toHaveTextContent(agent.id);
    await fireEvent.click(candidate()!);
    await fireEvent.input(input, { target: { value: `@${agent.name} please work` } });
    await fireEvent.submit(input.closest("form")!);
    await waitFor(() => expect(commentBodies).toEqual([{
      body: `@${agent.name} please work`, mentioned_agent_ids: [agent.id]
    }]));
    await fireEvent.click(screen.getByRole("button", { name: t("issues.openAgentSession") }));
    expect(await screen.findByRole("heading", { name: t("issues.agentSessionLabel", { agent: agent.name, index: 1 }) })).toBeInTheDocument();
  });

  it("uses an already hydrated Agent cache on a first deep-link mount", async () => {
    vi.stubGlobal("EventSource", undefined);
    const fetcher = vi.fn(async (input: RequestInfo | URL) => envelope(nameFixture(new URL(String(input), "http://teamva.test").pathname)));
    vi.stubGlobal("fetch", fetcher);
    queryClient.setQueryData(["agents", agent.computer_id], { items: [agent] });
    navigate("/issues?issue=issue-test", { replace: true });
    render(IssuesTestHost);
    const label = t("issues.agentSessionLabel", { agent: agent.name, index: 1 });
    await waitFor(() => expect(document.querySelector('[data-comment-id="name-comment"] strong')).toHaveTextContent(label));
    await fireEvent.click(screen.getByRole("button", { name: t("issues.openAgentSession") }));
    expect(await screen.findByRole("heading", { name: label })).toBeInTheDocument();
    expect(fetcher.mock.calls.some(([input]) => String(input).endsWith("/agents"))).toBe(false);
  });

  it("does not reuse a previous project's Agent name for a different ID", async () => {
    vi.stubGlobal("EventSource", undefined);
    const otherIssue = { ...issue, id: "other-issue", project_id: "project-other" };
    const otherAgent = { ...agent, id: "agent-other", name: "QA OpenCode" };
    const otherInstance = { ...instance, id: "other-instance", issue_id: otherIssue.id, agent_id: otherAgent.id };
    const otherRun = { ...run, id: "other-run", issue_id: otherIssue.id, project_id: otherIssue.project_id, agent_id: otherAgent.id, issue_agent_instance_id: otherInstance.id };
    const otherComment = { ...comment, id: "other-comment", issue_id: otherIssue.id, author_agent_id: otherAgent.id, run_id: otherRun.id };
    let resolveOtherIssues!: (response: Response) => void;
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
      const path = new URL(String(input), "http://teamva.test").pathname;
      if (path === "/v1/projects/project-other/issues") return new Promise<Response>((resolve) => { resolveOtherIssues = resolve; });
      if (path === "/v1/issues/other-issue") return envelope(otherIssue);
      if (path === "/v1/issues/other-issue/comments") return envelope({ items: [otherComment] });
      if (path === "/v1/issues/other-issue/agent-instances") return envelope({ items: [otherInstance] });
      if (path === "/v1/issues/other-issue/runs" || path === "/v1/agent-instances/other-instance/runs") return envelope({ items: [otherRun] });
      return envelope(nameFixture(path));
    }));
    navigate("/issues?issue=issue-test", { replace: true });
    const host = render(IssuesTestHost);
    await fireEvent.input(await screen.findByRole("textbox", { name: t("issues.commentInput") }), { target: { value: "@QA Codex old-project draft" } });
    await fireEvent.click(await screen.findByRole("button", { name: t("issues.openAgentSession") }));
    expect(await screen.findByRole("heading", { name: t("issues.agentSessionLabel", { agent: agent.name, index: 1 }) })).toBeInTheDocument();
    navigate("/issues?issue=other-issue", { replace: true });
    await host.rerender({ projectId: otherIssue.project_id });
    await waitFor(() => expect(resolveOtherIssues).toBeDefined());
    expect(document.querySelector('[data-comment-id="name-comment"]')).not.toBeInTheDocument();
    expect(document.getElementById(`agent-session-title-${instance.id}`)).not.toBeInTheDocument();
    resolveOtherIssues(envelope({ items: [otherIssue] }));
    const otherAuthor = () => document.querySelector('[data-comment-id="other-comment"] strong');
    await waitFor(() => expect(otherAuthor()).toHaveTextContent(t("issues.agentSessionLabel", { agent: otherAgent.id, index: 1 })));
    queryClient.setQueryData(["agents", agent.computer_id], { items: [otherAgent] });
    await waitFor(() => expect(otherAuthor()).toHaveTextContent(t("issues.agentSessionLabel", { agent: otherAgent.name, index: 1 })));
    const input = screen.getByRole("textbox", { name: t("issues.commentInput") });
    expect(input).toHaveValue("");
    await fireEvent.input(input, { target: { value: "@" } });
    await waitFor(() => expect(document.querySelector(`button[data-agent-id="${otherAgent.id}"]`)).toBeInTheDocument());
    expect(document.querySelector(`button[data-agent-id="${agent.id}"]`)).not.toBeInTheDocument();
  });

  it("keeps historical authors and session numbers keyed by Agent ID, not a shared name", async () => {
    vi.stubGlobal("EventSource", undefined);
    const otherAgent = { ...agent, id: "agent-other" };
    const instances = [instance, { ...instance, id: "second-instance" }, { ...instance, id: "other-instance", agent_id: otherAgent.id }];
    const runs = instances.map((item, index) => ({ ...run, id: `run-${index}`, agent_id: item.agent_id, issue_agent_instance_id: item.id }));
    const comments = runs.map((item, index) => ({ ...comment, id: `comment-${index}`, run_id: item.id, author_agent_id: item.agent_id }));
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
      const path = new URL(String(input), "http://teamva.test").pathname;
      return envelope(path.endsWith("/comments") ? { items: comments }
        : path.endsWith("/agent-instances") ? { items: instances }
        : path.endsWith("/runs") ? { items: runs }
        : nameFixture(path));
    }));
    const host = render(IssuePanelTestHost, { props: { repositories: [], agents: [agent, otherAgent] } });
    const author = (index: number) => document.querySelector(`[data-comment-id="comment-${index}"] strong`);
    for (const [index, session] of [1, 2, 1].entries()) {
      await waitFor(() => expect(author(index)).toHaveTextContent(t("issues.agentSessionLabel", { agent: agent.name, index: session })));
    }
    await host.rerender({ repositories: [], agents: [agent, { ...otherAgent, name: "New Agent name" }] });
    expect(author(0)).toHaveTextContent(t("issues.agentSessionLabel", { agent: agent.name, index: 1 }));
    expect(author(1)).toHaveTextContent(t("issues.agentSessionLabel", { agent: agent.name, index: 2 }));
    expect(author(2)).toHaveTextContent(t("issues.agentSessionLabel", { agent: "New Agent name", index: 1 }));
    const oldRow = document.querySelector<HTMLElement>('[data-comment-id="comment-0"]')!;
    await fireEvent.click(within(oldRow).getByRole("button", { name: t("issues.openAgentSession") }));
    expect(await screen.findByRole("heading", { name: t("issues.agentSessionLabel", { agent: agent.name, index: 1 }) })).toBeInTheDocument();
  });
});

describe("old comment session association", () => {
  const now = "2026-08-11T10:00:00Z";
  const comment = {
    id: "old-comment", issue_id: "issue-test", author_kind: "agent", author_agent_id: "agent-test",
    run_id: "old-run", body: "old published summary", status: "published", version: 1,
    created_at: now, updated_at: now
  };
  const instance = {
    id: "old-instance", issue_id: "issue-test", agent_id: "agent-test", agent_revision_id: "revision-test",
    status: "idle", pending_mention_count: 0, version: 1, created_at: now, updated_at: now
  };
  const run = {
    id: "old-run", issue_id: "issue-test", project_id: "project-test", agent_id: "agent-test",
    issue_agent_instance_id: "old-instance", status: "succeeded", input: [], version: 1,
    created_at: now, updated_at: now
  };
  function stubLookup(resolveRun: () => Promise<Response>) {
    vi.stubGlobal("EventSource", undefined);
    const requests: string[] = [];
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
      const url = new URL(String(input), "http://teamva.test");
      const path = url.pathname;
      requests.push(path + url.search);
      if (path === "/v1/runs/old-run") return resolveRun();
      const data = path.endsWith("/comments") ? { items: [comment] }
        : path.endsWith("/issues/issue-test/agent-instances") ? { items: [instance] }
        : path.endsWith("/goal") ? { goal: null }
        : path.endsWith("/issues/issue-test/runs") ? { items: [], next_cursor: "older-page" }
        : { items: [] };
      return envelope(data);
    }));
    return requests;
  }
  function envelope(data: unknown) {
    return new Response(JSON.stringify({ data }), { status: 200, headers: { "Content-Type": "application/json" } });
  }

  it("opens the exact session without loading all old Run pages", async () => {
    const requests = stubLookup(async () => envelope(run));
    render(IssuePanelTestHost, { props: { repositories: [] } });
    const button = await screen.findByRole("button", { name: t("issues.openAgentSession") });
    expect(requests).not.toContain("/v1/runs/old-run");
    await fireEvent.click(button);
    await waitFor(() => expect(document.getElementById("agent-session-title-old-instance")).toBeInTheDocument());
    expect(requests.filter((path) => path === "/v1/runs/old-run")).toHaveLength(1);
    expect(requests.some((path) => path.startsWith("/v1/issues/issue-test/runs?") && path.includes("cursor="))).toBe(false);
  });

  it("rejects a Run from another Issue and offers a local retry", async () => {
    stubLookup(async () => envelope({ ...run, issue_id: "foreign-issue" }));
    render(IssuePanelTestHost, { props: { repositories: [] } });
    await fireEvent.click(await screen.findByRole("button", { name: t("issues.openAgentSession") }));
    const row = document.querySelector<HTMLElement>('[data-comment-id="old-comment"]')!;
    expect(await within(row).findByRole("alert")).toBeInTheDocument();
    expect(within(row).getByRole("button", { name: t("common.retry") })).toBeInTheDocument();
    expect(document.getElementById("agent-session-title-old-instance")).not.toBeInTheDocument();
  });

  it("does not reopen a session after switching tasks away and back", async () => {
    let resolve!: (response: Response) => void;
    stubLookup(() => new Promise<Response>((done) => { resolve = done; }));
    const host = render(IssuePanelTestHost, { props: { repositories: [] } });
    await fireEvent.click(await screen.findByRole("button", { name: t("issues.openAgentSession") }));
    const base: Omit<Issue, "id"> = {
      project_id: "project-test", title: "task", description: "", status: "open", worktree_hint: false,
      created_by_user_id: "user-test", version: 1, created_at: now, updated_at: now
    };
    await host.rerender({ repositories: [], issue: { ...base, id: "other-issue" } });
    await host.rerender({ repositories: [], issue: { ...base, id: "issue-test" } });
    resolve(envelope(run));
    await waitFor(() => expect(screen.getByRole("button", { name: t("issues.openAgentSession") })).toBeEnabled());
    expect(document.getElementById("agent-session-title-old-instance")).not.toBeInTheDocument();
  });
});

describe("Issue composer request snapshots", () => {
  const now = "2026-08-11T10:00:00Z";
  const issue = (id: string): Issue => ({
    id, project_id: "project-test", title: id, description: "", version: 1,
    status: "open", worktree_hint: false, created_by_user_id: "user-test",
    created_at: now, updated_at: now
  });
  const agent = (id: string): Agent => ({
    id, version: 1, computer_id: "computer-test", agent_service_id: "service-test",
    name: "Builder", status: "active", created_at: now, updated_at: now
  });
  const envelope = (data: unknown) => new Response(JSON.stringify({ data, meta: {} }), {
    status: 200, headers: { "Content-Type": "application/json" }
  });
  const result = (issueId = "issue-test") => envelope({
    id: "created", issue_id: issueId, author_kind: "user", body: "A", status: "published",
    version: 1, created_at: now, updated_at: now
  });

  it("guards same-tick Ctrl/Cmd-Enter and preserves B while the response for A is delayed", async () => {
    const posts: Array<{ path: string; body: Record<string, unknown> }> = [];
    let resolve!: (response: Response) => void;
    const response = new Promise<Response>((done) => { resolve = done; });
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const path = String(input);
      if (path.endsWith("/comments") && init?.method === "POST") {
        posts.push({ path, body: JSON.parse(String(init.body)) });
        return response;
      }
      return envelope({ items: [] });
    }));
    render(IssuePanelTestHost, { props: { repositories: [] } });
    const input = screen.getByLabelText(t("issues.commentInput"));
    await fireEvent.input(input, { target: { value: "A" } });
    // Dispatch without awaiting Svelte's mutation-state flush.
    input.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", ctrlKey: true, bubbles: true, cancelable: true }));
    input.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", metaKey: true, bubbles: true, cancelable: true }));
    await waitFor(() => expect(posts).toHaveLength(1));
    await fireEvent.input(input, { target: { value: "B" } });
    resolve(result());
    await waitFor(() => expect(screen.getByRole("button", { name: t("issues.send") })).toBeEnabled());
    expect(input).toHaveValue("B");
    expect(posts[0].body).toEqual({ body: "A", mentioned_agent_ids: [] });
  });

  it("retains the comment and shows a mutation failure inside the composer", async () => {
    vi.stubGlobal("fetch", vi.fn(async (_input: RequestInfo | URL, init?: RequestInit) => init?.method === "POST"
      ? new Response(JSON.stringify({ error: { code: "runtime_unavailable", message: "failed" }, meta: {} }), { status: 503, headers: { "Content-Type": "application/json" } })
      : envelope({ items: [] })));
    render(IssuePanelTestHost, { props: { repositories: [] } });
    const input = screen.getByLabelText(t("issues.commentInput"));
    await fireEvent.input(input, { target: { value: "do not lose me" } });
    await fireEvent.submit(input.closest("form")!);
    expect(await within(input.closest("form")!).findByRole("alert")).toBeInTheDocument();
    expect(input).toHaveValue("do not lose me");
    expect(screen.getByRole("button", { name: t("issues.send") })).toBeEnabled();
  });

  it("uses the selected second same-name Agent after an insertion before its token", async () => {
    const posts: Array<Record<string, unknown>> = [];
    vi.stubGlobal("fetch", vi.fn(async (_input: RequestInfo | URL, init?: RequestInit) => {
      if (init?.method === "POST") {
        posts.push(JSON.parse(String(init.body)));
        return result();
      }
      return envelope({ items: [] });
    }));
    render(IssuePanelTestHost, { props: { repositories: [], agents: [agent("first"), agent("second")] } });
    const input = screen.getByLabelText(t("issues.commentInput"));
    await fireEvent.input(input, { target: { value: "@" } });
    await fireEvent.keyDown(input, { key: "ArrowDown" });
    await fireEvent.keyDown(input, { key: "Enter" });
    expect(input).toHaveValue("@Builder ");
    await fireEvent.input(input, { target: { value: "请 @Builder " } });
    await fireEvent.submit(input.closest("form")!);
    await waitFor(() => expect(posts).toHaveLength(1));
    expect(posts[0].mentioned_agent_ids).toEqual(["second"]);
  });

  it("does not activate email, code, ambiguous, disabled, or unconfigured Agents", async () => {
    const posts: Array<Record<string, unknown>> = [];
    vi.stubGlobal("fetch", vi.fn(async (_input: RequestInfo | URL, init?: RequestInit) => {
      if (init?.method === "POST") {
        posts.push(JSON.parse(String(init.body)));
        return result();
      }
      return envelope({ items: [] });
    }));
    render(IssuePanelTestHost, { props: { repositories: [], agents: [
      agent("first"), agent("second"), { ...agent("paused"), name: "Paused", status: "disabled" },
      { ...agent("lumi"), name: "Lumi", agent_service_id: "" }
    ] } });
    const input = screen.getByLabelText(t("issues.commentInput"));
    await fireEvent.input(input, { target: { value: "owner@Builder.test `@Builder` @Builder @Paused @Lumi" } });
    await fireEvent.submit(input.closest("form")!);
    await waitFor(() => expect(posts).toHaveLength(1));
    expect(posts[0].mentioned_agent_ids).toEqual([]);
  });

  it("restores issue drafts across unmounts and never clears a different issue on late success", async () => {
    let resolve!: (response: Response) => void;
    const response = new Promise<Response>((done) => { resolve = done; });
    const paths: string[] = [];
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      if (init?.method === "POST") { paths.push(String(input)); return response; }
      return envelope({ items: [] });
    }));
    const first = render(IssuePanelTestHost, { props: { repositories: [], issue: issue("one") } });
    const input = screen.getByLabelText(t("issues.commentInput"));
    await fireEvent.input(input, { target: { value: "A" } });
    await fireEvent.submit(input.closest("form")!);
    await waitFor(() => expect(paths).toEqual(["/v1/issues/one/comments"]));
    first.unmount();
    const second = render(IssuePanelTestHost, { props: { repositories: [], issue: issue("two") } });
    await fireEvent.input(screen.getByLabelText(t("issues.commentInput")), { target: { value: "B" } });
    resolve(result("one"));
    await waitFor(() => expect(screen.getByLabelText(t("issues.commentInput"))).toHaveValue("B"));
    second.unmount();
    const reopened = render(IssuePanelTestHost, { props: { repositories: [], issue: issue("two") } });
    expect(screen.getByLabelText(t("issues.commentInput"))).toHaveValue("B");
    reopened.unmount();
    render(IssuePanelTestHost, { props: { repositories: [], issue: issue("one") } });
    await waitFor(() => expect(screen.getByLabelText(t("issues.commentInput"))).toHaveValue(""));
  });

  it("keeps a newer edit of the same comment after a delayed edit response", async () => {
    let resolve!: (response: Response) => void;
    const response = new Promise<Response>((done) => { resolve = done; });
    const comment = { id: "editable", issue_id: "issue-test", author_kind: "user", body: "original", status: "published", version: 1, created_at: now, updated_at: now };
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      if (init?.method === "PATCH") return response;
      return envelope({ items: String(input).endsWith("/comments") ? [comment] : [] });
    }));
    render(IssuePanelTestHost, { props: { repositories: [] } });
    await fireEvent.click(await screen.findByRole("button", { name: t("issues.editComment") }));
    const input = screen.getByLabelText(t("issues.editComment"));
    await fireEvent.input(input, { target: { value: "A" } });
    await fireEvent.click(screen.getByRole("button", { name: t("common.save") }));
    await fireEvent.input(input, { target: { value: "B" } });
    resolve(envelope({ ...comment, body: "A", version: 2 }));
    await waitFor(() => expect(screen.getByRole("button", { name: t("common.save") })).toBeEnabled());
    expect(input).toHaveValue("B");
  });

  it("does not clear a newly opened comment editor after another comment is deleted", async () => {
    let resolve!: (response: Response) => void;
    const response = new Promise<Response>((done) => { resolve = done; });
    const first = { id: "first", issue_id: "issue-test", author_kind: "user", body: "first body", status: "published", version: 1, created_at: now, updated_at: now };
    const second = { ...first, id: "second", body: "second body" };
    vi.stubGlobal("confirm", () => true);
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      if (init?.method === "DELETE") return response;
      return envelope({ items: String(input).endsWith("/comments") ? [first, second] : [] });
    }));
    render(IssuePanelTestHost, { props: { repositories: [] } });
    await screen.findByText("first body");
    const firstRow = document.querySelector<HTMLElement>('[data-comment-id="first"]')!;
    const secondRow = document.querySelector<HTMLElement>('[data-comment-id="second"]')!;
    await fireEvent.click(within(firstRow).getByRole("button", { name: t("issues.deleteComment") }));
    await fireEvent.click(within(secondRow).getByRole("button", { name: t("issues.editComment") }));
    await fireEvent.input(within(secondRow).getByRole("textbox", { name: t("issues.editComment") }), { target: { value: "new edit" } });
    resolve(new Response(null, { status: 204 }));
    await waitFor(() => expect(within(firstRow).getByRole("button", { name: t("issues.deleteComment") })).toBeEnabled());
    expect(within(secondRow).getByRole("textbox", { name: t("issues.editComment") })).toHaveValue("new edit");
  });

  it("opens and navigates status actions with the keyboard and restores trigger focus", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => envelope({ items: [] })));
    render(IssuePanelTestHost, { props: { repositories: [] } });
    const trigger = screen.getByRole("button", { name: t("issues.statusOpen") });
    trigger.focus();
    await fireEvent.keyDown(trigger, { key: "ArrowDown" });
    const options = await screen.findAllByRole("menuitemradio");
    await waitFor(() => expect(options[0]).toHaveFocus());
    await fireEvent.keyDown(options[0], { key: "ArrowDown" });
    expect(options[1]).toHaveFocus();
    await fireEvent.keyDown(options[1], { key: "End" });
    expect(options[2]).toHaveFocus();
    await fireEvent.keyDown(options[2], { key: "Escape" });
    expect(screen.queryByRole("menu")).not.toBeInTheDocument();
    expect(trigger).toHaveFocus();
  });

  it("keeps approval errors by the composer and query errors visible with retry", async () => {
    let failComments = true;
    const approval = { id: "approval", issue_id: "issue-test", run_id: "run", version: 1, action_digest: "digest", action_kind: "command", status: "pending", normalized_action: { command: "test" } };
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      if (init?.method === "POST" || (failComments && String(input).endsWith("/comments"))) {
        return new Response(JSON.stringify({ error: { code: "runtime_unavailable" }, meta: {} }), { status: 503, headers: { "Content-Type": "application/json" } });
      }
      return envelope({ items: String(input).includes("/approvals?") ? [approval] : [] });
    }));
    render(IssuePanelTestHost, { props: { repositories: [] } });
    const retry = await screen.findByRole("button", { name: t("common.retry") }, { timeout: 2500 });
    failComments = false;
    await fireEvent.click(retry);
    await waitFor(() => expect(screen.queryByRole("button", { name: t("common.retry") })).not.toBeInTheDocument());
    await fireEvent.click(await screen.findByRole("button", { name: t("issues.allowAction") }));
    const form = screen.getByLabelText(t("issues.commentInput")).closest("form")!;
    expect(await within(form).findByRole("alert")).toBeInTheDocument();
  });
});

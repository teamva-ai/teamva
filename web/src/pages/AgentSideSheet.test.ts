import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/svelte";
import { afterEach, describe, expect, it, vi } from "vitest";
import { t } from "../lib/i18n.svelte";
import { clearComposerDrafts } from "../lib/composerDrafts";
import { BoundedRunEventBuffer } from "../lib/activityEvents";
import type { GitRepositorySummary } from "../lib/gitChanges";
import {
  MAX_RETAINED_INSTANCE_RUN_BYTES, type InstanceRunsPage
} from "../lib/instanceRuns";
import { queryClient } from "../lib/query";
import type { IssueAgentGoal, IssueAgentInstance, Run, RunEvent } from "../lib/types";
import AgentSideSheetHarness from "../test/AgentSideSheetHarness.svelte";
import AgentSideSheetTestHost from "./AgentSideSheetTestHost.svelte";

function envelope(data: unknown) {
  return new Response(JSON.stringify({ data, meta: { request_id: "req-goal" } }), {
    status: 200,
    headers: { "Content-Type": "application/json" }
  });
}

function spentGoal(status: IssueAgentGoal["status"]): IssueAgentGoal {
  return {
    id: "goal-spent", issue_id: "issue-1", issue_agent_instance_id: "instance-1",
    agent_id: "agent-1", agent_revision_id: "revision-1", objective: "completed work",
    status, phase: "settled", token_budget: 300_000, tokens_used: 419_410,
    continuations_used: 1, max_continuations: 20, blocked_streak: 0,
    audit_failure_streak: 0, finalize_attempts: 1, summary_comment_id: "comment-spent",
    version: 3, created_at: "2026-08-11T00:00:00Z", updated_at: "2026-08-11T00:00:00Z"
  };
}

describe("AgentSideSheet", () => {
  afterEach(() => {
    cleanup();
    clearComposerDrafts();
    queryClient.clear();
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
    window.localStorage.clear();
  });

  it("shows a failed run.finished gateway body and expandable safe details rather than only provider_error", async () => {
    vi.stubGlobal("EventSource", undefined);
    const now = "2026-11-02T00:00:00Z";
    const run: Run = { id: "run-failed", issue_id: "issue-1", issue_agent_instance_id: "instance-1", project_id: "project-1", status: "failed", terminal_reason: "provider_error", purpose: "work", read_only: false, lease_epoch: 1, version: 1, created_at: now, updated_at: now, input: [] };
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
      const path = new URL(String(input), "http://teamva.test").pathname;
      if (path.endsWith("/goal")) return envelope({ goal: null });
      if (path.endsWith("/agent-instances/instance-1/runs")) return envelope({ items: [run] });
      if (path.endsWith("/runs/run-failed/events")) return envelope({ items: [{ id: "failure", run_id: run.id, seq: 1, event_type: "run.finished", occurred_at: now, payload: { status: "failed", error: { data: { message: "Gateway 503 fixture <script>not-executable</script>", response: { body: "Detailed upstream cause; API_KEY=fixture-only-key" } } } } }] });
      return envelope({ items: [] });
    }));
    const { container } = render(AgentSideSheetHarness);
    await waitFor(() => expect(container.querySelector('[data-activity-kind="error"]')).toHaveTextContent("Detailed upstream cause"));
    const row = container.querySelector('[data-activity-kind="error"]')!;
    expect(row).not.toHaveTextContent("provider_error");
    expect(row.querySelector("script")).toBeNull();
    await fireEvent.click(row.querySelector("button")!);
    expect(row.querySelector("pre")).toHaveTextContent("Gateway 503 fixture <script>not-executable</script>");
    expect(row.innerHTML).not.toContain("fixture-only-key");
  });

  it("keeps an internal first prompt out of user bubbles while preserving real messages and failed Runs", async () => {
    vi.stubGlobal("EventSource", undefined);
    const now = "2026-08-11T00:00:00Z";
    const run: Run = {
      id: "run-failed", issue_id: "issue-1", issue_agent_instance_id: "instance-1", project_id: "project-1",
      status: "failed", terminal_reason: "provider-unavailable-marker", purpose: "work", read_only: false,
      lease_epoch: 1, version: 1, created_at: now, updated_at: now,
      input: [{ type: "text", text: "# 任务（Issue）上下文\n用户 [comment_id=cmt_private]: @Lumi 你好", internal: true }]
    };
    const followup: Run = {
      ...run, id: "run-followup", status: "succeeded", terminal_reason: undefined,
      created_at: "2026-08-11T00:01:00Z",
      input: [{ type: "text", text: "# 任务（Issue）上下文\n这是用户主动发送的原文" }]
    };
    const event = (seq: number, event_type: string, payload: Record<string, unknown>): RunEvent => ({
      id: "event-" + seq, run_id: run.id, seq, event_type, payload, occurred_at: now
    });
    const history = [
      event(1, "user.steer", { text: "private-steer-marker", internal: true }),
      event(2, "user.steer", { text: "真正的追问", internal: false }),
      event(3, "output.snapshot", { provider_id: "opencode", id: "answer", text: "真正的 Agent 回复" })
    ];
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
      const path = new URL(String(input), "http://teamva.test").pathname;
      if (path.endsWith("/goal")) return envelope({ goal: null });
      if (path.endsWith("/agent-instances/instance-1/runs")) return envelope({ items: [followup, run] });
      if (path.endsWith("/runs/run-failed/events")) return envelope({ items: history });
      return envelope({ items: [] });
    }));
    render(AgentSideSheetHarness);
    expect(await screen.findByText("provider-unavailable-marker")).toBeInTheDocument();
    expect(await screen.findByText("真正的追问")).toBeInTheDocument();
    expect(await screen.findByText("真正的 Agent 回复")).toBeInTheDocument();
    expect(await screen.findByText(/这是用户主动发送的原文/)).toBeInTheDocument();
    expect(screen.queryByText(/comment_id=cmt_private/)).not.toBeInTheDocument();
    expect(screen.queryByText("private-steer-marker")).not.toBeInTheDocument();
  });

  it.each([2_000_000, 60_000, 1_001])("submits the armed goal through native validation with budget %s", async (budgetValue) => {
    let postedBody: Record<string, unknown> | undefined;
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const raw = typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
      const path = new URL(raw, "http://teamva.test").pathname;
      if (path.endsWith("/agent-instances/instance-1/runs")) return envelope({ items: [] });
      if (path.endsWith("/agent-instances/instance-1/goal")) return envelope({ goal: null });
      if (path.endsWith("/issues/issue-1/runs")) {
        postedBody = JSON.parse(String(init?.body)) as Record<string, unknown>;
        return envelope({
          id: "run-goal", issue_id: "issue-1", issue_agent_instance_id: "instance-1",
          agent_id: "agent-1", project_id: "project-1", status: "succeeded",
          read_only: false, lease_epoch: 1, input: [], purpose: "work", version: 1,
          created_at: "2026-08-11T00:00:00Z", updated_at: "2026-08-11T00:00:00Z"
        });
      }
      if (path.endsWith("/runs/run-goal/events")) return envelope({ items: [] });
      return envelope({ items: [] });
    }));

    render(AgentSideSheetHarness);
    const arm = await screen.findByRole("button", { name: t("issues.goal") });
    await waitFor(() => expect(arm).toBeEnabled());
    await fireEvent.click(arm);
    expect(screen.getByText(t("issues.goalArmed"))).toBeInTheDocument();
    const budget = screen.getByRole("spinbutton", { name: t("issues.goalTokenBudget") }) as HTMLInputElement;
    expect(budget).toHaveValue(2_000_000);
    expect(budget.checkValidity()).toBe(true);
    await fireEvent.input(budget, { target: { value: String(budgetValue) } });
    await fireEvent.blur(budget);
    expect(budget.checkValidity()).toBe(true);

    await fireEvent.input(screen.getByRole("textbox", { name: t("issues.sessionInput") }), {
      target: { value: "完成目标并运行测试" }
    });
    const send = screen.getByRole("button", { name: t("issues.send") });
    // fireEvent.submit bypasses native min/step validation and previously hid
    // the default 2,000,000 versus min=1,000/step=50,000 mismatch.
    send.closest("form")!.requestSubmit(send as HTMLButtonElement);
    await waitFor(() => expect(postedBody).toBeDefined());
    expect(postedBody).toMatchObject({
      issue_agent_instance_id: "instance-1",
      input: "完成目标并运行测试",
      goal: true,
      goal_token_budget: budgetValue,
      expected_issue_version: 1
    });
    expect(postedBody).not.toHaveProperty("git_repository_id");
  });

  it("previews, removes, and sends a selected image", async () => {
    let postedBody: Record<string, unknown> | undefined;
    vi.stubGlobal("EventSource", undefined);
    vi.spyOn(globalThis.crypto, "randomUUID").mockReturnValue("00000000-0000-4000-8000-000000000001");
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const raw = typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
      const path = new URL(raw, "http://teamva.test").pathname;
      if (path.endsWith("/agent-instances/instance-1/runs")) return envelope({ items: [] });
      if (path.endsWith("/agent-instances/instance-1/goal")) return envelope({ goal: null });
      if (path.endsWith("/issues/issue-1/runs")) {
        postedBody = JSON.parse(String(init?.body)) as Record<string, unknown>;
        return envelope({
          id: "run-image", issue_id: "issue-1", issue_agent_instance_id: "instance-1",
          agent_id: "agent-1", project_id: "project-1", status: "queued",
          read_only: false, lease_epoch: 1,
          input: [{ type: "image", mime_type: "image/png", filename: "capture.png" }],
          purpose: "work", version: 1,
          created_at: "2026-08-11T00:00:00Z", updated_at: "2026-08-11T00:00:00Z"
        });
      }
      return envelope({ items: [] });
    }));

    render(AgentSideSheetHarness);
    const picker = await screen.findByLabelText(t("issues.addImages"), { selector: "input" });
    const file = new File([new Uint8Array([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a])], "capture.png", { type: "image/png" });
    await fireEvent.change(picker, { target: { files: [file] } });
    expect(await screen.findByRole("button", { name: t("issues.previewImage", { name: "capture.png" }) })).toBeInTheDocument();
    await fireEvent.click(screen.getByRole("button", { name: t("issues.previewImage", { name: "capture.png" }) }));
    expect(screen.getByRole("dialog", { name: t("issues.imagePreview") })).toBeInTheDocument();
    await fireEvent.click(screen.getAllByRole("button", { name: t("common.close") }).at(-1)!);

    await fireEvent.submit(screen.getByRole("button", { name: t("issues.send") }).closest("form")!);
    await waitFor(() => expect(postedBody).toBeDefined());
    expect(postedBody).toMatchObject({
      input: "",
      attachments: [{
        type: "image", mime_type: "image/png", filename: "capture.png",
        data_url: expect.stringMatching(/^data:image\/png;base64,/)
      }]
    });
    await waitFor(() => expect(screen.queryByRole("button", { name: t("issues.previewImage", { name: "capture.png" }) })).not.toBeInTheDocument());
  });

  it("turns pasted image files into attachments without hijacking plain text paste", async () => {
    vi.stubGlobal("EventSource", undefined);
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
      const raw = typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
      const path = new URL(raw, "http://teamva.test").pathname;
      if (path.endsWith("/agent-instances/instance-1/runs")) return envelope({ items: [] });
      if (path.endsWith("/agent-instances/instance-1/goal")) return envelope({ goal: null });
      return envelope({ items: [] });
    }));
    render(AgentSideSheetHarness);
    const textarea = await screen.findByRole("textbox", { name: t("issues.sessionInput") });

    // 纯文本粘贴不拦截，保留浏览器默认的文本插入行为。
    const plainTextResult = await fireEvent.paste(textarea, { clipboardData: { files: [] } });
    expect(plainTextResult).toBe(true);

    // 粘贴截图按附件处理，且不让默认行为把文件名插入输入框。
    const file = new File(
      [new Uint8Array([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a])],
      "pasted.png",
      { type: "image/png" }
    );
    const imageResult = await fireEvent.paste(textarea, { clipboardData: { files: [file] } });
    expect(imageResult).toBe(false);
    expect(await screen.findByRole("button", {
      name: t("issues.previewImage", { name: "pasted.png" })
    })).toBeInTheDocument();
  });

  it("removes a selected image before sending", async () => {
    vi.stubGlobal("EventSource", undefined);
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
      const raw = typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
      const path = new URL(raw, "http://teamva.test").pathname;
      if (path.endsWith("/agent-instances/instance-1/goal")) return envelope({ goal: null });
      return envelope({ items: [] });
    }));
    render(AgentSideSheetHarness);
    const picker = await screen.findByLabelText(t("issues.addImages"), { selector: "input" });
    const file = new File([new Uint8Array([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a])], "remove.png", { type: "image/png" });
    await fireEvent.change(picker, { target: { files: [file] } });
    await fireEvent.click(await screen.findByRole("button", { name: t("issues.removeImage", { name: "remove.png" }) }));
    expect(screen.queryByRole("button", { name: t("issues.previewImage", { name: "remove.png" }) })).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: t("issues.send") })).toBeDisabled();
  });

  it("accepts a dropped image and sends it as an attachment", async () => {
    let postedBody: Record<string, unknown> | undefined;
    vi.stubGlobal("EventSource", undefined);
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const raw = typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
      const path = new URL(raw, "http://teamva.test").pathname;
      if (path.endsWith("/agent-instances/instance-1/runs")) return envelope({ items: [] });
      if (path.endsWith("/agent-instances/instance-1/goal")) return envelope({ goal: null });
      if (path.endsWith("/issues/issue-1/runs")) {
        postedBody = JSON.parse(String(init?.body)) as Record<string, unknown>;
        return envelope({
          id: "run-drop", issue_id: "issue-1", issue_agent_instance_id: "instance-1",
          agent_id: "agent-1", project_id: "project-1", status: "queued",
          read_only: false, lease_epoch: 1,
          input: [{ type: "image", mime_type: "image/png", filename: "drop.png" }],
          purpose: "work", version: 1,
          created_at: "2026-08-11T00:00:00Z", updated_at: "2026-08-11T00:00:00Z"
        });
      }
      return envelope({ items: [] });
    }));
    render(AgentSideSheetHarness);
    const form = (await screen.findByRole("textbox", { name: t("issues.sessionInput") })).closest("form")!;
    const file = new File([new Uint8Array([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a])], "drop.png", { type: "image/png" });
    const dataTransfer = { types: ["Files"], files: [file], dropEffect: "none" };
    await fireEvent.dragEnter(form, { dataTransfer });
    expect(screen.getByText(t("issues.dropImagesHere"))).toBeInTheDocument();
    await fireEvent.drop(form, { dataTransfer });
    expect(await screen.findByRole("button", { name: t("issues.previewImage", { name: "drop.png" }) })).toBeInTheDocument();
    await fireEvent.submit(form);
    await waitFor(() => expect(postedBody).toBeDefined());
    expect(postedBody?.attachments).toEqual([expect.objectContaining({ filename: "drop.png", type: "image" })]);
  });

  it("keeps attachments after a failed send", async () => {
    vi.stubGlobal("EventSource", undefined);
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
      const raw = typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
      const path = new URL(raw, "http://teamva.test").pathname;
      if (path.endsWith("/agent-instances/instance-1/goal")) return envelope({ goal: null });
      if (path.endsWith("/issues/issue-1/runs")) {
        return new Response(JSON.stringify({ error: { code: "runtime_unavailable", message: "failed", retryable: true }, meta: {} }), {
          status: 503, headers: { "Content-Type": "application/json" }
        });
      }
      return envelope({ items: [] });
    }));
    render(AgentSideSheetHarness);
    const picker = await screen.findByLabelText(t("issues.addImages"), { selector: "input" });
    const file = new File([new Uint8Array([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a])], "retry.png", { type: "image/png" });
    await fireEvent.change(picker, { target: { files: [file] } });
    await screen.findByRole("button", { name: t("issues.previewImage", { name: "retry.png" }) });
    await waitFor(() => expect(screen.getByRole("button", { name: t("issues.send") })).toBeEnabled());
    await fireEvent.submit(screen.getByRole("button", { name: t("issues.send") }).closest("form")!);
    expect(await within(screen.getByRole("button", { name: t("issues.send") }).closest("form")!).findByRole("alert")).toBeInTheDocument();
    await waitFor(() => expect(screen.getByRole("button", { name: t("issues.send") })).toBeEnabled());
    expect(screen.getByRole("button", { name: t("issues.previewImage", { name: "retry.png" }) })).toBeInTheDocument();
    expect(within(screen.getByRole("button", { name: t("issues.send") }).closest("form")!).getByRole("alert")).toBeInTheDocument();
  });

  it("preserves text and images when closing/reopening and isolates instance/project drafts", async () => {
    vi.stubGlobal("EventSource", undefined);
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => String(input).endsWith("/goal") ? envelope({ goal: null }) : envelope({ items: [] })));
    const instance: IssueAgentInstance = {
      id: "draft-one", issue_id: "issue-test", agent_id: "agent-test", agent_revision_id: "revision-test",
      status: "idle", pending_mention_count: 0, version: 1,
      created_at: "2026-08-11T00:00:00Z", updated_at: "2026-08-11T00:00:00Z"
    };
    const props = { instance, repositories: [], projectId: "project-test" };
    const host = render(AgentSideSheetTestHost, { props });
    await fireEvent.input(screen.getByLabelText(t("issues.sessionInput")), { target: { value: "first draft" } });
    const file = new File([new Uint8Array([0x89, 0x50, 0x4e, 0x47])], "retained.png", { type: "image/png" });
    await fireEvent.change(screen.getByLabelText(t("issues.addImages"), { selector: "input" }), { target: { files: [file] } });
    await screen.findByRole("button", { name: t("issues.previewImage", { name: "retained.png" }) });
    await host.rerender({ ...props, open: false });
    expect(screen.queryByLabelText(t("issues.sessionInput"))).not.toBeInTheDocument();
    await host.rerender({ ...props, open: true });
    expect(screen.getByLabelText(t("issues.sessionInput"))).toHaveValue("first draft");
    expect(screen.getByRole("button", { name: t("issues.previewImage", { name: "retained.png" }) })).toBeInTheDocument();
    await host.rerender({ ...props, instance: { ...instance, id: "draft-two" } });
    expect(screen.getByLabelText(t("issues.sessionInput"))).toHaveValue("");
    expect(screen.queryByRole("button", { name: t("issues.previewImage", { name: "retained.png" }) })).not.toBeInTheDocument();
    await fireEvent.input(screen.getByLabelText(t("issues.sessionInput")), { target: { value: "second draft" } });
    await host.rerender({ ...props, projectId: "another-project" });
    expect(screen.getByLabelText(t("issues.sessionInput"))).toHaveValue("");
    await host.rerender(props);
    expect(screen.getByLabelText(t("issues.sessionInput"))).toHaveValue("first draft");
    // Navigation metadata is allowed in sessionStorage; arbitrary draft text
    // and images are not. Do not depend on another test's navigation queue.
    const stored = Object.values(window.sessionStorage).join("\n");
    for (const privateDraft of ["first draft", "second draft", "retained.png", "data:image/"]) {
      expect(stored).not.toContain(privateDraft);
    }
  });

  it("guards duplicate keyboard sends and removes only submitted image IDs while retaining a newer B", async () => {
    vi.stubGlobal("EventSource", undefined);
    let resolve!: (response: Response) => void;
    const response = new Promise<Response>((done) => { resolve = done; });
    const posted: Array<Record<string, unknown>> = [];
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      if (init?.method === "POST") { posted.push(JSON.parse(String(init.body))); return response; }
      return String(input).endsWith("/goal") ? envelope({ goal: null }) : envelope({ items: [] });
    }));
    render(AgentSideSheetHarness);
    const input = screen.getByLabelText(t("issues.sessionInput"));
    await fireEvent.input(input, { target: { value: "A" } });
    const picker = screen.getByLabelText(t("issues.addImages"), { selector: "input" });
    const file = (name: string) => new File([new Uint8Array([0x89, 0x50, 0x4e, 0x47])], name, { type: "image/png" });
    await fireEvent.change(picker, { target: { files: [file("a.png")] } });
    await screen.findByRole("button", { name: t("issues.previewImage", { name: "a.png" }) });
    await waitFor(() => expect(screen.getByRole("button", { name: t("issues.send") })).toBeEnabled());
    input.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", ctrlKey: true, bubbles: true, cancelable: true }));
    input.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", metaKey: true, bubbles: true, cancelable: true }));
    await waitFor(() => expect(posted).toHaveLength(1));
    expect(input).toBeEnabled();
    await fireEvent.input(input, { target: { value: "B" } });
    await fireEvent.change(picker, { target: { files: [file("b.png")] } });
    await screen.findByRole("button", { name: t("issues.previewImage", { name: "b.png" }) });
    resolve(envelope({ id: "sent", issue_id: "issue-1", issue_agent_instance_id: "instance-1", project_id: "project-1", status: "succeeded", input: [], version: 1, purpose: "work", created_at: "2026-08-11T00:00:00Z", updated_at: "2026-08-11T00:00:00Z" }));
    await waitFor(() => expect(screen.getByRole("button", { name: t("issues.send") })).toBeEnabled());
    expect(input).toHaveValue("B");
    expect(screen.queryByRole("button", { name: t("issues.previewImage", { name: "a.png" }) })).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: t("issues.previewImage", { name: "b.png" }) })).toBeInTheDocument();
    expect(posted[0]).toMatchObject({ input: "A", attachments: [expect.objectContaining({ filename: "a.png" })] });
  });

  it("keeps a pending send locked across closing/reopening and settles the original draft only", async () => {
    vi.stubGlobal("EventSource", undefined);
    let resolve!: (response: Response) => void;
    const response = new Promise<Response>((done) => { resolve = done; });
    const sends: Array<RequestInit | undefined> = [];
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      if (init?.method === "POST") { sends.push(init); return response; }
      return String(input).endsWith("/goal") ? envelope({ goal: null }) : envelope({ items: [] });
    }));
    const instance: IssueAgentInstance = {
      id: "late-one", issue_id: "issue-test", agent_id: "agent-test", agent_revision_id: "revision-test", status: "idle",
      pending_mention_count: 0, version: 1, created_at: "2026-08-11T00:00:00Z", updated_at: "2026-08-11T00:00:00Z"
    };
    const props = { instance, repositories: [] };
    const host = render(AgentSideSheetTestHost, { props });
    const input = screen.getByLabelText(t("issues.sessionInput"));
    await fireEvent.input(input, { target: { value: "A" } });
    await waitFor(() => expect(screen.getByRole("button", { name: t("issues.send") })).toBeEnabled());
    await fireEvent.submit(input.closest("form")!);
    await waitFor(() => expect(sends).toHaveLength(1));
    await host.rerender({ ...props, open: false });
    expect(sends[0]?.signal?.aborted).not.toBe(true);
    await host.rerender({ ...props, open: true });
    expect(screen.getByLabelText(t("issues.sessionInput"))).toHaveValue("A");
    expect(screen.getByRole("button", { name: t("issues.send") })).toBeDisabled();
    await host.rerender({ ...props, instance: { ...instance, id: "late-two" } });
    await fireEvent.input(screen.getByLabelText(t("issues.sessionInput")), { target: { value: "B" } });
    resolve(envelope({ id: "sent", issue_id: "issue-test", issue_agent_instance_id: "late-one", project_id: "project-test", status: "succeeded", input: [], version: 1, purpose: "work", created_at: "2026-08-11T00:00:00Z", updated_at: "2026-08-11T00:00:00Z" }));
    await waitFor(() => expect(queryClient.getQueryData(["instance-runs", "late-one"])).toBeDefined());
    expect(screen.getByLabelText(t("issues.sessionInput"))).toHaveValue("B");
    await host.rerender(props);
    await waitFor(() => expect(screen.getByLabelText(t("issues.sessionInput"))).toHaveValue(""));
  });

  it("retains an image whose file read finishes after the original sheet is destroyed", async () => {
    vi.stubGlobal("EventSource", undefined);
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => String(input).endsWith("/goal") ? envelope({ goal: null }) : envelope({ items: [] })));
    let finishRead!: () => void;
    vi.stubGlobal("FileReader", class {
      result: string | undefined;
      onload?: () => void;
      readAsDataURL() {
        finishRead = () => { this.result = "data:image/png;base64,iVBORw=="; this.onload?.(); };
      }
    });
    const instance: IssueAgentInstance = {
      id: "reading", issue_id: "issue-test", agent_id: "agent-test", agent_revision_id: "revision-test", status: "idle",
      pending_mention_count: 0, version: 1, created_at: "2026-08-11T00:00:00Z", updated_at: "2026-08-11T00:00:00Z"
    };
    const props = { instance, repositories: [] };
    const host = render(AgentSideSheetTestHost, { props });
    const file = new File([new Uint8Array([0x89, 0x50, 0x4e, 0x47])], "reading.png", { type: "image/png" });
    await fireEvent.change(screen.getByLabelText(t("issues.addImages"), { selector: "input" }), { target: { files: [file] } });
    await host.rerender({ ...props, open: false });
    await host.rerender({ ...props, open: true });
    finishRead();
    expect(await screen.findByRole("button", { name: t("issues.previewImage", { name: "reading.png" }) })).toBeInTheDocument();
  });

  it("keeps query retry visible on Changes and places approval mutation errors by the composer", async () => {
    vi.stubGlobal("EventSource", undefined);
    let failRuns = true;
    const now = "2026-08-11T00:00:00Z";
    const run: Run = {
      id: "approval-run", issue_id: "issue-test", issue_agent_instance_id: "approval-instance", project_id: "project-test",
      version: 1, status: "waiting_approval", input: [], purpose: "work", read_only: false, lease_epoch: 1,
      created_at: now, updated_at: now
    };
    const approval = { id: "pending-approval", issue_id: "issue-test", run_id: run.id, status: "pending", action_kind: "command", action_digest: "digest", provider_approval_ref: "request", version: 1, created_at: now, updated_at: now };
    const event: RunEvent = { id: "requested", run_id: run.id, seq: 1, event_type: "approval.requested", payload: { request_id: "request", action_kind: "command", normalized_action: { command: "echo test" } }, occurred_at: now };
    const failure = () => new Response(JSON.stringify({ error: { code: "runtime_unavailable" }, meta: {} }), { status: 503, headers: { "Content-Type": "application/json" } });
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const path = new URL(String(input), "http://teamva.test").pathname;
      if (init?.method === "POST") return failure();
      if (path.endsWith("/agent-instances/approval-instance/runs")) return failRuns ? failure() : envelope({ items: [run] });
      if (path.endsWith("/goal")) return envelope({ goal: null });
      if (path.endsWith("/approvals")) return envelope({ items: [approval] });
      if (path.endsWith("/events")) return envelope({ items: [event] });
      if (path.includes("/git/changes")) return envelope({ run_id: run.id, snapshots: [], provider_reports: [], provider_reports_truncated: false });
      return envelope({ items: [] });
    }));
    const instance: IssueAgentInstance = {
      id: "approval-instance", issue_id: "issue-test", agent_id: "agent-test", agent_revision_id: "revision-test", status: "idle",
      pending_mention_count: 0, version: 1, created_at: now, updated_at: now
    };
    render(AgentSideSheetTestHost, { props: { instance, repositories: [] } });
    const retry = await screen.findByRole("button", { name: t("common.retry") }, { timeout: 2500 });
    await fireEvent.click(screen.getByRole("tab", { name: t("issues.changesTab") }));
    expect(retry).toBeInTheDocument();
    failRuns = false;
    await fireEvent.click(retry);
    await waitFor(() => expect(screen.queryByRole("button", { name: t("common.retry") })).not.toBeInTheDocument());
    await fireEvent.click(screen.getByRole("tab", { name: t("issues.activityTab") }));
    await fireEvent.click(await screen.findByRole("button", { name: t("issues.allowAction") }));
    expect(await within(screen.getByLabelText(t("issues.sessionInput")).closest("form")!).findByRole("alert")).toBeInTheDocument();
  });

  it("rejects an unsupported attachment without enabling send", async () => {
    vi.stubGlobal("EventSource", undefined);
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
      const raw = typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
      const path = new URL(raw, "http://teamva.test").pathname;
      if (path.endsWith("/agent-instances/instance-1/goal")) return envelope({ goal: null });
      return envelope({ items: [] });
    }));
    render(AgentSideSheetHarness);
    const picker = await screen.findByLabelText(t("issues.addImages"), { selector: "input" });
    await fireEvent.change(picker, {
      target: { files: [new File(["<svg/>"], "unsafe.svg", { type: "image/svg+xml" })] }
    });
    expect(await screen.findByRole("alert")).toHaveTextContent(t("issues.attachmentUnsupported"));
    expect(screen.getByRole("button", { name: t("issues.send") })).toBeDisabled();
  });

  it("displays persisted image metadata without an inline data URL", async () => {
    vi.stubGlobal("EventSource", undefined);
    const run: Run = {
      id: "run-image-history", version: 1, issue_id: "issue-test", issue_agent_instance_id: "instance-image-history",
      project_id: "project-test", status: "succeeded", read_only: false, lease_epoch: 1, purpose: "work",
      input: [{ type: "image", mime_type: "image/png", filename: "history.png" }],
      created_at: "2026-08-11T10:00:00Z", updated_at: "2026-08-11T10:01:00Z",
      finished_at: "2026-08-11T10:01:00Z"
    };
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
      const raw = typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
      const path = new URL(raw, "http://teamva.test").pathname;
      const data = path.endsWith("/agent-instances/instance-image-history/runs")
        ? { items: [run] }
        : path.endsWith("/agent-instances/instance-image-history/goal")
          ? { goal: null }
          : { items: [] };
      return envelope(data);
    }));
    const instance: IssueAgentInstance = {
      id: "instance-image-history", version: 1, issue_id: "issue-test", agent_id: "agent-test",
      agent_revision_id: "revision-test", status: "idle", pending_mention_count: 0,
      created_at: "2026-08-11T09:00:00Z", updated_at: "2026-08-11T10:01:00Z"
    };
    render(AgentSideSheetTestHost, { props: { instance, repositories: [] } });
    expect(await screen.findByText("history.png")).toBeInTheDocument();
    expect(document.documentElement.innerHTML).not.toContain("data:image");
  });

  it("renders durable goal state and pauses it", async () => {
    const actions: string[] = [];
    const activeGoal = {
      id: "goal-1", issue_id: "issue-1", issue_agent_instance_id: "instance-1",
      agent_id: "agent-1", agent_revision_id: "revision-1", objective: "完成全部工作",
      status: "active", phase: "evaluating", token_budget: 200_000, tokens_used: 12_500,
      continuations_used: 2, max_continuations: 20, blocked_streak: 0,
      audit_failure_streak: 0, finalize_attempts: 0, note: "测试仍需确认",
      summary_comment_id: "comment-1", current_run_id: "audit-1", version: 3,
      created_at: "2026-08-11T00:00:00Z", updated_at: "2026-08-11T00:00:00Z"
    };
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const raw = typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
      const path = new URL(raw, "http://teamva.test").pathname;
      if (path.endsWith("/agent-instances/instance-1/runs")) return envelope({ items: [] });
      if (path.endsWith("/agent-instances/instance-1/goal")) return envelope({ goal: activeGoal });
      if (path.endsWith("/goals/goal-1/pause") && init?.method === "POST") {
        actions.push(String(init.body));
        return envelope({ ...activeGoal, status: "paused", version: 4 });
      }
      return envelope({ items: [] });
    }));

    render(AgentSideSheetHarness);
    expect(await screen.findByText("测试仍需确认")).toBeInTheDocument();
    expect(screen.getByText(t("issues.goalEvaluating"))).toBeInTheDocument();
    await fireEvent.input(screen.getByRole("textbox", { name: t("issues.sessionInput") }), {
      target: { value: "不应并行发送" }
    });
    expect(screen.getByRole("button", { name: t("issues.send") })).toBeDisabled();
    await fireEvent.click(screen.getByRole("button", { name: t("issues.goalPause") }));
    await waitFor(() => expect(actions).toHaveLength(1));
    expect(JSON.parse(actions[0])).toEqual({ expected_version: 3 });
  });

  it.each(["budget_reached", "paused", "blocked"])("does not resume a %s goal whose existing budget is exhausted", async (status) => {
    const goal = {
      id: "goal-spent", issue_id: "issue-1", issue_agent_instance_id: "instance-1",
      agent_id: "agent-1", agent_revision_id: "revision-1", objective: "spent budget",
      status, phase: "settled", token_budget: 60_000, tokens_used: 139_950,
      continuations_used: 0, max_continuations: 20, blocked_streak: 0,
      audit_failure_streak: 0, finalize_attempts: 0, summary_comment_id: "comment-spent",
      version: 3, created_at: "2026-08-11T00:00:00Z", updated_at: "2026-08-11T00:00:00Z"
    };
    const posts: string[] = [];
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const raw = typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
      const path = new URL(raw, "http://teamva.test").pathname;
      if (init?.method === "POST") posts.push(path);
      if (path.endsWith("/agent-instances/instance-1/goal")) return envelope({ goal });
      return envelope({ items: [] });
    }));
    render(AgentSideSheetHarness);
    const resume = await screen.findByRole("button", { name: t("issues.goalResume") });
    expect(resume).toBeDisabled();
    expect(screen.getByText(t("goalBudget.exhausted"))).toBeInTheDocument();
    expect(screen.getByRole("button", { name: t("issues.goalCancel") })).toBeEnabled();
    await fireEvent.input(screen.getByRole("textbox", { name: t("issues.sessionInput") }), {
      target: { value: "不能绕过现有目标预算" }
    });
    expect(screen.getByRole("button", { name: t("issues.send") })).toBeDisabled();
    expect(screen.getByRole("button", { name: t("issues.goal") })).toBeDisabled();
    await fireEvent.click(resume);
    expect(posts.filter(path => path.endsWith("/resume"))).toEqual([]);
  });

  it.each(["completed", "cancelled"] as const)("allows ordinary continuation and a new Goal after an over-budget %s Goal", async (status) => {
    vi.stubGlobal("EventSource", undefined);
    const goal = spentGoal(status);
    const posts: Array<Record<string, unknown>> = [];
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const path = new URL(String(input), "http://teamva.test").pathname;
      if (path.endsWith("/agent-instances/instance-1/goal")) return envelope({ goal });
      if (path.endsWith("/issues/issue-1/runs") && init?.method === "POST") {
        posts.push(JSON.parse(String(init.body)));
        return envelope({
          id: "run-after-goal-" + posts.length, issue_id: "issue-1", issue_agent_instance_id: "instance-1",
          project_id: "project-1", status: "succeeded", input: [], purpose: "work", read_only: false,
          lease_epoch: 1, version: 1, created_at: goal.created_at, updated_at: goal.updated_at
        });
      }
      return envelope({ items: [] });
    }));
    render(AgentSideSheetHarness);
    expect(await screen.findByText(t(status === "completed" ? "issues.goalCompleted" : "issues.goalCancelled"))).toBeInTheDocument();
    expect(screen.getByText("419K / 300K")).toBeInTheDocument();
    expect(screen.queryByText(t("goalBudget.exhausted"))).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: t("issues.goalCancel") })).not.toBeInTheDocument();
    const input = screen.getByRole("textbox", { name: t("issues.sessionInput") });
    expect(input).toBeEnabled();
    await fireEvent.input(input, { target: { value: "普通继续" } });
    const send = screen.getByRole("button", { name: t("issues.send") });
    expect(send).toBeEnabled();
    await fireEvent.click(send);
    await waitFor(() => expect(posts).toHaveLength(1));
    expect(posts[0]).toMatchObject({ input: "普通继续", issue_agent_instance_id: "instance-1" });
    expect(posts[0]).not.toHaveProperty("goal");
    const arm = screen.getByRole("button", { name: t("issues.goal") });
    await waitFor(() => expect(arm).toBeEnabled());
    await fireEvent.click(arm);
    await fireEvent.input(input, { target: { value: "新目标" } });
    expect(send).toBeEnabled();
    await fireEvent.click(send);
    await waitFor(() => expect(posts).toHaveLength(2));
    expect(posts[1]).toMatchObject({ input: "新目标", goal: true, goal_token_budget: 2_000_000 });
  });

  it("keeps an over-budget active Goal locked to its existing lifecycle", async () => {
    vi.stubGlobal("EventSource", undefined);
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) =>
      String(input).endsWith("/goal") ? envelope({ goal: spentGoal("active") }) : envelope({ items: [] })));
    render(AgentSideSheetHarness);
    expect(await screen.findByText(t("goalBudget.exhausted"))).toBeInTheDocument();
    await fireEvent.input(screen.getByRole("textbox", { name: t("issues.sessionInput") }), {
      target: { value: "不能并行开始新目标" }
    });
    expect(screen.getByRole("button", { name: t("issues.send") })).toBeDisabled();
    expect(screen.getByRole("button", { name: t("issues.goal") })).toBeDisabled();
    expect(screen.getByRole("button", { name: t("issues.goalPause") })).toBeEnabled();
    expect(screen.getByRole("button", { name: t("issues.goalCancel") })).toBeEnabled();
  });

  it("does not let a historical over-budget Goal unlock new Goals during a real Run", async () => {
    vi.stubGlobal("EventSource", undefined);
    const goal = spentGoal("completed");
    const run: Run = {
      id: "run-still-active", issue_id: "issue-1", issue_agent_instance_id: "instance-1", project_id: "project-1",
      status: "running", input: [], purpose: "work", read_only: false, lease_epoch: 1, version: 1,
      created_at: goal.created_at, updated_at: goal.updated_at
    };
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
      const path = new URL(String(input), "http://teamva.test").pathname;
      if (path.endsWith("/goal")) return envelope({ goal });
      if (path.endsWith("/agent-instances/instance-1/runs")) return envelope({ items: [run] });
      return envelope({ items: [] });
    }));
    render(AgentSideSheetHarness);
    await screen.findByText(t("issues.goalCompleted"));
    expect(await screen.findByRole("button", { name: t("issues.stop") })).toBeEnabled();
    expect(screen.getByRole("button", { name: t("issues.goal") })).toBeDisabled();
    expect(screen.getByRole("button", { name: (name) => name.endsWith(t("issues.revise")) })).toBeDisabled();
    expect(screen.queryByText(t("goalBudget.exhausted"))).not.toBeInTheDocument();
  });

  it("does not query large change history until the Changes tab opens", async () => {
    const requested: string[] = [];
    vi.stubGlobal("EventSource", undefined);
    const run: Run = {
      id: "run-test", version: 1, issue_id: "issue-test", issue_agent_instance_id: "instance-test",
      project_id: "project-test", status: "succeeded",
      read_only: false, lease_epoch: 1, purpose: "work", input: [],
      created_at: "2026-08-11T10:00:00Z", updated_at: "2026-08-11T10:01:00Z",
      finished_at: "2026-08-11T10:01:00Z"
    };
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
      const raw = typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
      const url = new URL(raw, "http://teamva.test");
      requested.push(url.pathname + url.search);
      let data: unknown = { items: [] };
      if (url.pathname.endsWith("/agent-instances/instance-test/runs")) data = { items: [run] };
      else if (url.pathname.endsWith("/approvals")) data = { items: [] };
      else if (url.pathname.endsWith("/runs/run-test/events")) data = { items: [] };
      else if (url.pathname.endsWith("/runs/run-test/git/changes")) {
        data = {
          run_id: "run-test",
          snapshots: [],
          provider_reports: [{
            id: "report-test", seq: 2, occurred_at: "2026-08-11T10:00:30Z",
            source: "provider.item.fileChange", paths: ["src/provider-only.ts"],
            paths_verified: true, scope: "repository", truncated: false
          }],
          provider_reports_truncated: false
        };
      }
      return new Response(JSON.stringify({ data, meta: { request_id: "request-test" } }), {
        status: 200, headers: { "Content-Type": "application/json" }
      });
    }));

    const instance: IssueAgentInstance = {
      id: "instance-test", version: 1, issue_id: "issue-test", agent_id: "agent-test",
      agent_revision_id: "revision-test", status: "idle", pending_mention_count: 0,
      created_at: "2026-08-11T09:00:00Z", updated_at: "2026-08-11T10:01:00Z"
    };
    const repositories: GitRepositorySummary[] = [{
      id: "repo-test", common_id: "common-test", label: "App", root_display: "App",
      resource_ids: [], discovery_origin: "resource", worktree_kind: "main",
      worktree: { linked: false, main: true }, scope: "full", branch: null,
      operation: null, generation: 1, state: "clean",
      counts: { conflict: 0, staged: 0, unstaged: 0, untracked: 0 }, warnings: []
    }];
    render(AgentSideSheetTestHost, { props: { instance, repositories } });

    await waitFor(() => expect(requested.some((path) => path.includes("/runs/run-test/events?"))).toBe(true));
    expect(requested.some((path) => path.includes("/git/changes"))).toBe(false);
    await fireEvent.click(screen.getByRole("tab", { name: "Changes" }));
    await waitFor(() => expect(requested.some((path) => path.includes("/runs/run-test/git/changes?"))).toBe(true));
    await waitFor(() => expect(screen.getByText("provider-only.ts")).toBeInTheDocument());
    expect(screen.getByTestId("agent-run-changes")).toBeInTheDocument();
  });

  it("invalidates the completed Run Git history from the activity SSE", async () => {
    class FakeEventSource {
      static latest: FakeEventSource | undefined;
      listeners = new Map<string, Array<(event: Event) => void>>();
      constructor(public url: string) {
        FakeEventSource.latest = this;
      }
      addEventListener(type: string, listener: EventListenerOrEventListenerObject) {
        const callback = typeof listener === "function" ? listener : (event: Event) => listener.handleEvent(event);
        this.listeners.set(type, [...(this.listeners.get(type) ?? []), callback]);
      }
      close() {}
      emit(type: string, data: unknown) {
        const event = new MessageEvent(type, { data: JSON.stringify(data) });
        for (const listener of this.listeners.get(type) ?? []) listener(event);
      }
    }
    vi.stubGlobal("EventSource", FakeEventSource);
    const running: Run = {
      id: "run-active", version: 1, issue_id: "issue-test", issue_agent_instance_id: "instance-active",
      project_id: "project-test", status: "running",
      read_only: false, lease_epoch: 1, purpose: "work", input: [],
      created_at: "2026-08-11T11:00:00Z", updated_at: "2026-08-11T11:00:01Z"
    };
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
      const raw = typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
      const path = new URL(raw, "http://teamva.test").pathname;
      const data = path.endsWith("/agent-instances/instance-active/runs")
        ? { items: [running] }
        : { items: [] };
      return new Response(JSON.stringify({ data, meta: { request_id: "request-active" } }), {
        status: 200, headers: { "Content-Type": "application/json" }
      });
    }));
    const instance: IssueAgentInstance = {
      id: "instance-active", version: 1, issue_id: "issue-test", agent_id: "agent-test",
      agent_revision_id: "revision-test", status: "running", pending_mention_count: 0,
      created_at: "2026-08-11T10:00:00Z", updated_at: "2026-08-11T11:00:01Z"
    };
    const repositories: GitRepositorySummary[] = [{
      id: "repo-test", label: "App", root_display: "App", resource_ids: [],
      discovery_origin: "resource", worktree_kind: "main", scope: "full", branch: null,
      operation: null, generation: 1, state: "clean", counts: {}, warnings: []
    }];
    const invalidate = vi.spyOn(queryClient, "invalidateQueries");
    render(AgentSideSheetTestHost, { props: { instance, repositories } });
    await waitFor(() => expect(FakeEventSource.latest).toBeDefined());
    FakeEventSource.latest!.emit("run.completed", {
      id: "terminal-event", run_id: "run-active", seq: 1, event_type: "run.completed",
      payload: { status: "completed" }, occurred_at: "2026-08-11T11:01:00Z"
    });
    await waitFor(() => expect(invalidate.mock.calls.some(([filters]) =>
      JSON.stringify(filters?.queryKey) === JSON.stringify(["run-git-changes", "run-active"])
    )).toBe(true));
    expect(invalidate.mock.calls.some(([filters]) =>
      JSON.stringify(filters?.queryKey) === JSON.stringify(["instance-git-changes", "instance-active"])
    )).toBe(true);
  });

  it.each(["disconnect", "terminal", "poll", "visible", "gap"] as const)(
    "reconciles a long-open session after %s and retains its final tool/error without run.finished",
    async (recovery) => {
      class FakeEventSource {
        static latest: FakeEventSource | undefined;
        listeners = new Map<string, Array<(event: Event) => void>>();
        constructor(public url: string) { FakeEventSource.latest = this; }
        addEventListener(type: string, listener: EventListenerOrEventListenerObject) {
          const callback = typeof listener === "function" ? listener : (event: Event) => listener.handleEvent(event);
          this.listeners.set(type, [...(this.listeners.get(type) ?? []), callback]);
        }
        close() {}
        emit(type: string, data?: unknown) {
          for (const listener of this.listeners.get(type) ?? []) {
            listener(new MessageEvent(type, { data: JSON.stringify(data) }));
          }
        }
      }
      vi.stubGlobal("EventSource", FakeEventSource);
      let hidden = recovery === "visible";
      vi.spyOn(document, "hidden", "get").mockImplementation(() => hidden);
      const now = "2026-10-27T23:54:00Z";
      const run: Run = {
        id: "run-live-reconcile", version: 63, issue_id: "issue-test", issue_agent_instance_id: "instance-live-reconcile",
        project_id: "project-test", status: "running", read_only: false, lease_epoch: 1, purpose: "work",
        input: [{ type: "text", text: "继续" }], created_at: now, updated_at: now
      };
      const failed: Run = {
        ...run, version: 65, status: "failed", terminal_reason: "provider_failed",
        finished_at: "2026-10-27T23:56:00Z"
      };
      const event = (seq: number, event_type: string, payload: Record<string, unknown>): RunEvent => ({
        id: `live-event-${seq}`, run_id: run.id, seq, event_type, payload, occurred_at: now
      });
      const thought = event(63, "provider.event", {
        provider_id: "opencode", kind: "reasoning", part_id: "thought-1", text: "正在核对任务"
      });
      const write = event(64, "provider.event", {
        provider_id: "opencode", kind: "tool", phase: "success", name: "write", call_id: "write-1",
        input: { path: "notes/result.md" }, content: [{ type: "text", text: "Created file" }]
      });
      const failure = event(65, "run.error", {
        status: "failed", error: { type: "provider.internal", error: {
          error: { message: "Upstream request failed: Endpoint is unavailable.", status: 503 },
          headers: { authorization: "private-header-marker" }
        } }
      });
      let settled = false;
      const eventRequests: number[] = [];
      vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
        const url = new URL(String(input), "http://teamva.test");
        if (url.pathname.endsWith("/goal")) return envelope({ goal: null });
        if (url.pathname.endsWith("/agent-instances/instance-live-reconcile/runs")) {
          return envelope({ items: [settled ? failed : run] });
        }
        if (url.pathname.endsWith(`/runs/${run.id}/events`)) {
          const after = Number(url.searchParams.get("after"));
          eventRequests.push(after);
          return envelope({ items: after === 0 ? [thought] : settled && after === 63 ? [write, failure] : [] });
        }
        return envelope({ items: [] });
      }));
      const instance: IssueAgentInstance = {
        id: "instance-live-reconcile", version: 1, issue_id: "issue-test", agent_id: "agent-test",
        agent_revision_id: "revision-test", status: "running", pending_mention_count: 0,
        created_at: now, updated_at: now
      };
      render(AgentSideSheetTestHost, { props: { instance, repositories: [] } });
      expect(await screen.findByText(t("issues.sessionGenerating"))).toBeInTheDocument();
      await waitFor(() => expect(FakeEventSource.latest).toBeDefined());
      settled = true;
      if (recovery === "disconnect") FakeEventSource.latest!.emit("error");
      if (recovery === "gap") FakeEventSource.latest!.emit("run.error", failure);
      if (recovery === "terminal") {
        FakeEventSource.latest!.emit("provider.event", write);
        FakeEventSource.latest!.emit("run.error", failure);
      }
      if (recovery === "poll") {
        queryClient.setQueryData(["instance-runs-recent", instance.id], { items: [failed], retained_bytes: 1024 });
      }
      if (recovery === "visible") {
        hidden = false;
        document.dispatchEvent(new Event("visibilitychange"));
      }
      expect(await screen.findByText("Upstream request failed: Endpoint is unavailable. (HTTP 503)")).toBeInTheDocument();
      await waitFor(() => expect(screen.queryByText(t("issues.sessionGenerating"))).not.toBeInTheDocument());
      expect(screen.getByText(t("issues.sessionWriteFile"))).toBeInTheDocument();
      expect(screen.getByText("notes/result.md")).toBeInTheDocument();
      if (recovery !== "terminal") expect(eventRequests).toContain(63);
      expect(document.body.textContent).not.toContain("private-header-marker");
    }
  );

  it.each(["success", "failed"])("shows native IPC calls individually in sequence and reports the final HTTP 503 (%s read)", async (readPhase) => {
    vi.stubGlobal("EventSource", undefined);
    const now = "2026-10-27T23:54:00Z";
    const run: Run = {
      id: "run-ipc-screenshot", version: 64, issue_id: "issue-test", issue_agent_instance_id: "instance-ipc-screenshot",
      project_id: "project-test", status: "failed", terminal_reason: "provider_failed", read_only: false,
      lease_epoch: 1, purpose: "work", input: [{ type: "text", text: "private-prompt-marker", internal: true }],
      created_at: now, updated_at: "2026-10-27T23:56:00Z", finished_at: "2026-10-27T23:56:00Z"
    };
    const event = (seq: number, event_type: string, payload: Record<string, unknown>): RunEvent => ({
      id: `screenshot-${seq}`, run_id: run.id, seq, event_type, payload, occurred_at: now
    });
    const history = Array.from({ length: 64 }, (_, index) => event(index + 1, "provider.internal", { internal: true }));
    const set = (seq: number, type: string, payload: Record<string, unknown>) => { history[seq - 1] = event(seq, type, payload); };
    set(2, "user.steer", { internal: true, text: "private-steer-marker" });
    set(39, "provider.event", { provider_id: "opencode", kind: "reasoning", part_id: "thought-a", text: "准备调用 write 读取任务上下文" });
    for (const [name, sequences, finalPhase] of [
      ["write", [40, 42, 43, 45], "success"],
      ["read", [58, 60, 61, 62], readPhase]
    ] as const) {
      const phases = ["called", "progress", "progress", finalPhase];
      sequences.forEach((seq, index) => set(seq, "provider.event", {
        provider_id: "opencode", kind: "tool", name, call_id: `${name}-ipc`, phase: phases[index],
        ...(index === 0 ? { input: {
          path: `/Users/private-owner/.teamva/tool-requests/${run.id}/issue_get/private-request.json`,
          body: "private-ipc-request-marker"
        } } : {}),
        ...(index === 3 ? { content: [{ type: "text", text: "private-ipc-response-marker" }],
          error: finalPhase === "failed" ? "private-ipc-error-marker" : undefined } : {})
      }));
    }
    for (const seq of [47, 48]) set(seq, "provider.tool", {
      provider_id: "opencode", provider: "teamva", id: "issue-get-1", tool: "issue_get",
      status: seq === 47 ? "running" : "completed", input: { summary: "读取当前任务" },
      ...(seq === 48 ? { output: { title: "当前任务", comment_count: 1 } } : {})
    });
    set(63, "provider.event", { provider_id: "opencode", kind: "reasoning", part_id: "thought-b", text: "已调用 write，正在核对返回结果" });
    set(64, "run.error", {
      status: "failed", error: { error: { message: "Upstream request failed: Endpoint is unavailable.", status: 503, type: "provider.internal" } }
    });
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
      const path = new URL(String(input), "http://teamva.test").pathname;
      if (path.endsWith("/goal")) return envelope({ goal: null });
      if (path.endsWith("/agent-instances/instance-ipc-screenshot/runs")) return envelope({ items: [run] });
      if (path.endsWith(`/runs/${run.id}/events`)) return envelope({ items: history });
      return envelope({ items: [] });
    }));
    const instance: IssueAgentInstance = {
      id: "instance-ipc-screenshot", version: 1, issue_id: "issue-test", agent_id: "agent-test",
      agent_revision_id: "revision-test", status: "idle", pending_mention_count: 0,
      created_at: now, updated_at: now
    };
    const { container } = render(AgentSideSheetTestHost, { props: { instance, repositories: [] } });
    expect(await screen.findByText("Upstream request failed: Endpoint is unavailable. (HTTP 503)")).toBeInTheDocument();
    const tools = [...container.querySelectorAll('[data-activity-kind="tool"]')];
    expect(tools).toHaveLength(3);
    expect(tools[0]).toHaveTextContent(t("issues.sessionWriteFile"));
    expect(tools[0]).toHaveAttribute("data-activity-status", "completed");
    expect(tools[1]).toHaveTextContent(t("issues.sessionCollabIssueGet"));
    expect(tools[2]).toHaveTextContent(t("issues.sessionReadFile"));
    expect(tools[2]).toHaveAttribute("data-activity-status", readPhase === "failed" ? "failed" : "completed");
    for (const row of [tools[0], tools[2]]) {
      await fireEvent.click(row.querySelector("button")!);
      expect(row.querySelector("pre")).toHaveTextContent("private-ipc-request-marker");
      expect(row.querySelector("pre")).toHaveTextContent("private-ipc-response-marker");
    }
    expect(screen.getAllByText(t("issues.sessionCollabIssueGet"))).toHaveLength(1);
    expect(screen.getAllByText(t("issues.sessionThinking"))).toHaveLength(2);
    expect(screen.queryByText(t("issues.sessionGenerating"))).not.toBeInTheDocument();
    for (const secret of ["private-prompt-marker", "private-steer-marker"]) {
      expect(container.innerHTML).not.toContain(secret);
    }
  });

  it("keeps the 25-large-input recent Query cache bounded and visibly limited", async () => {
    vi.stubGlobal("EventSource", undefined);
    const hugeInput = "begin\n" + "x".repeat(256 * 1024) + "\nend";
    const largeRuns: Run[] = Array.from({ length: 25 }, (_, index) => ({
      id: `run-large-${index}`, version: 1, issue_id: "issue-test",
      issue_agent_instance_id: "instance-large", project_id: "project-test",
      status: "succeeded", read_only: false,
      lease_epoch: 1, purpose: "work", input: [{ type: "text", text: hugeInput, internal: true }],
      created_at: `2026-08-11T09:${String(index).padStart(2, "0")}:00Z`,
      updated_at: "2026-08-11T10:00:00Z", finished_at: "2026-08-11T10:00:00Z"
    }));
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
      const raw = typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
      const url = new URL(raw, "http://teamva.test");
      const data = url.pathname.endsWith("/agent-instances/instance-large/runs")
        ? { items: largeRuns, next_cursor: "older" }
        : { items: [] };
      return new Response(JSON.stringify({ data, meta: { request_id: "request-large" } }), {
        status: 200, headers: { "Content-Type": "application/json" }
      });
    }));
    const instance: IssueAgentInstance = {
      id: "instance-large", version: 1, issue_id: "issue-test", agent_id: "agent-test",
      agent_revision_id: "revision-test", status: "idle", pending_mention_count: 0,
      created_at: "2026-08-11T09:00:00Z", updated_at: "2026-08-11T10:00:00Z"
    };
    const repositories: GitRepositorySummary[] = [{
      id: "repo-test", label: "App", root_display: "App", resource_ids: [],
      discovery_origin: "resource", worktree_kind: "main", scope: "full", branch: null,
      operation: null, generation: 1, state: "clean", counts: {}, warnings: []
    }];
    render(AgentSideSheetTestHost, { props: { instance, repositories } });

    await waitFor(() => expect(
      queryClient.getQueryData<InstanceRunsPage>(["instance-runs-recent", "instance-large"])
    ).toBeDefined());
    const cached = queryClient.getQueryData<InstanceRunsPage>(["instance-runs-recent", "instance-large"]);
    expect(cached?.retained_bytes).toBeLessThanOrEqual(MAX_RETAINED_INSTANCE_RUN_BYTES);
    expect(cached?.content_limited).toBe(true);
    expect(cached?.items.every((item) => item.input[0].text!.length <= 64 * 1024)).toBe(true);
    expect(await screen.findByText(/Some Run input was compacted/)).toBeInTheDocument();
  });

  it("loads older turns from the Changes tab and finds a change only on turn 26", async () => {
    vi.stubGlobal("EventSource", undefined);
    const makeRun = (index: number): Run => ({
      id: index === 0 ? "run-old" : `run-${index}`, version: 1, issue_id: "issue-test",
      issue_agent_instance_id: "instance-pages", project_id: "project-test",
      status: "succeeded", read_only: false,
      lease_epoch: 1, purpose: "work", input: [],
      created_at: `2026-08-11T09:${String(index).padStart(2, "0")}:00Z`,
      updated_at: "2026-08-11T10:00:00Z", finished_at: "2026-08-11T10:00:00Z"
    });
    const loaded = Array.from({ length: 25 }, (_, index) => makeRun(index + 1));
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
      const raw = typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
      const url = new URL(raw, "http://teamva.test");
      let data: unknown = { items: [] };
      if (url.pathname.endsWith("/agent-instances/instance-pages/runs")) {
        data = url.searchParams.get("cursor")
          ? { items: [makeRun(0)] }
          : { items: loaded, next_cursor: "older-25" };
      } else if (url.pathname.includes("/git/changes")) {
        const runId = url.pathname.split("/").at(-3);
        data = {
          run_id: runId, snapshots: [], provider_reports_truncated: false,
          provider_reports: runId === "run-old" ? [{
            id: "old-report", seq: 1, occurred_at: "2026-08-11T09:00:30Z",
            source: "provider.item.fileChange", paths: ["src/old-only.ts"],
            paths_verified: true, scope: "repository", truncated: false
          }] : []
        };
      }
      return new Response(JSON.stringify({ data, meta: { request_id: "request-pages" } }), {
        status: 200, headers: { "Content-Type": "application/json" }
      });
    }));
    const instance: IssueAgentInstance = {
      id: "instance-pages", version: 1, issue_id: "issue-test", agent_id: "agent-test",
      agent_revision_id: "revision-test", status: "idle", pending_mention_count: 0,
      created_at: "2026-08-11T09:00:00Z", updated_at: "2026-08-11T10:00:00Z"
    };
    const repositories: GitRepositorySummary[] = [{
      id: "repo-test", label: "App", root_display: "App", resource_ids: [],
      discovery_origin: "resource", worktree_kind: "main", scope: "full", branch: null,
      operation: null, generation: 1, state: "clean", counts: {}, warnings: []
    }];
    render(AgentSideSheetTestHost, { props: { instance, repositories } });
    await fireEvent.click(await screen.findByRole("tab", { name: "Changes" }));
    expect(await screen.findByRole("option", { name: "Loaded turns" })).toBeInTheDocument();
    expect(await screen.findByText("No displayable changes in loaded turns")).toBeInTheDocument();
    expect(screen.queryByText("No displayable changes in this scope")).not.toBeInTheDocument();

    await fireEvent.click(screen.getByRole("button", { name: "Load older turns" }));
    expect(await screen.findByText("old-only.ts", {}, { timeout: 10_000 })).toBeInTheDocument();
  });

  it("hides the file change entry when the provider reports no paths", async () => {
    vi.stubGlobal("EventSource", undefined);
    const run: Run = {
      id: "run-empty-change", version: 1, issue_id: "issue-test",
      issue_agent_instance_id: "instance-empty-change", project_id: "project-test",
      status: "succeeded", read_only: false,
      lease_epoch: 1, purpose: "work", input: [], created_at: "2026-08-11T09:00:00Z",
      updated_at: "2026-08-11T10:00:00Z", finished_at: "2026-08-11T10:00:00Z"
    };
    const emptyChange: RunEvent = {
      id: "event-empty-change", run_id: run.id, seq: 1, event_type: "provider.item",
      payload: { item: { type: "fileChange", changes: [], status: "completed" } },
      occurred_at: "2026-08-11T09:00:01Z"
    };
    queryClient.setQueryData(["instance-runs", "instance-empty-change"], {
      items: [run], retained_bytes: 0
    });
    queryClient.setQueryData(["instance-events", "instance-empty-change"], {
      items: [emptyChange], buffer: new BoundedRunEventBuffer(1, 128 * 1024),
      nextByRun: { [run.id]: undefined },
      evictedCount: 0, evictedBytes: 0, incomplete: false
    });
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
      const raw = typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
      const url = new URL(raw, "http://teamva.test");
      const data = url.pathname.endsWith("/agent-instances/instance-empty-change/runs")
        ? { items: [run] }
        : { items: [] };
      return new Response(JSON.stringify({ data, meta: { request_id: "request-empty-change" } }), {
        status: 200, headers: { "Content-Type": "application/json" }
      });
    }));
    const instance: IssueAgentInstance = {
      id: "instance-empty-change", version: 1, issue_id: "issue-test", agent_id: "agent-test",
      agent_revision_id: "revision-test", status: "idle", pending_mention_count: 0,
      created_at: "2026-08-11T09:00:00Z", updated_at: "2026-08-11T10:00:00Z"
    };
    const repositories: GitRepositorySummary[] = [{
      id: "repo-test", label: "App", root_display: "App", resource_ids: [],
      discovery_origin: "resource", worktree_kind: "main", scope: "full", branch: null,
      operation: null, generation: 1, state: "clean", counts: {}, warnings: []
    }];
    render(AgentSideSheetTestHost, { props: { instance, repositories } });
    await screen.findByText("Builder · Session 1");
    expect(screen.queryByText(t("runChanges.areaReported"))).not.toBeInTheDocument();
    expect(screen.queryByText(t("issues.sessionChanges"))).not.toBeInTheDocument();
  });

  it("keeps an event-eviction warning visible after every HTTP cursor is exhausted", async () => {
    vi.stubGlobal("EventSource", undefined);
    const run: Run = {
      id: "run-evicted", version: 1, issue_id: "issue-test",
      issue_agent_instance_id: "instance-evicted", project_id: "project-test",
      status: "succeeded", read_only: false,
      lease_epoch: 1, purpose: "work", input: [], created_at: "2026-08-11T09:00:00Z",
      updated_at: "2026-08-11T10:00:00Z", finished_at: "2026-08-11T10:00:00Z"
    };
    const buffer = new BoundedRunEventBuffer(1, 128 * 1024);
    const event = (seq: number): RunEvent => ({
      id: `event-${seq}`, run_id: run.id, seq, event_type: "output.delta",
      payload: { text: String(seq) }, occurred_at: `2026-08-11T09:00:0${seq}Z`
    });
    buffer.upsert(event(1));
    buffer.upsert(event(2));
    queryClient.setQueryData(["instance-runs", "instance-evicted"], {
      items: [run], retained_bytes: 1_024
    });
    queryClient.setQueryData(["instance-events", "instance-evicted"], {
      items: buffer.values(), buffer, nextByRun: { [run.id]: undefined },
      evictedCount: buffer.evictedCount, evictedBytes: buffer.evictedBytes,
      incomplete: buffer.incomplete
    });
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
      const raw = typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
      const url = new URL(raw, "http://teamva.test");
      const data = url.pathname.endsWith("/agent-instances/instance-evicted/runs")
        ? { items: [run] }
        : { items: [] };
      return new Response(JSON.stringify({ data, meta: { request_id: "request-evicted" } }), {
        status: 200, headers: { "Content-Type": "application/json" }
      });
    }));
    const instance: IssueAgentInstance = {
      id: "instance-evicted", version: 1, issue_id: "issue-test", agent_id: "agent-test",
      agent_revision_id: "revision-test", status: "idle", pending_mention_count: 0,
      created_at: "2026-08-11T09:00:00Z", updated_at: "2026-08-11T10:00:00Z"
    };
    const repositories: GitRepositorySummary[] = [{
      id: "repo-test", label: "App", root_display: "App", resource_ids: [],
      discovery_origin: "resource", worktree_kind: "main", scope: "full", branch: null,
      operation: null, generation: 1, state: "clean", counts: {}, warnings: []
    }];
    render(AgentSideSheetTestHost, { props: { instance, repositories } });
    expect(await screen.findByText(/1 older activity events were evicted/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Load more activity" })).not.toBeInTheDocument();
  });

  it("shows collaboration tool calls and a generating placeholder for a running session", async () => {
    vi.stubGlobal("EventSource", undefined);
    const run: Run = {
      id: "run-collab", version: 1, issue_id: "issue-test", issue_agent_instance_id: "instance-collab",
      project_id: "project-test", status: "running", read_only: false, lease_epoch: 1,
      purpose: "work", input: [], created_at: "2026-08-11T09:00:00Z",
      updated_at: "2026-08-11T09:00:05Z"
    };
    const toolEvent: RunEvent = {
      id: "event-collab-tool", run_id: run.id, seq: 2, event_type: "provider.tool",
      payload: {
        provider_id: "codex", id: "req-collab", tool: "issue_comment_create", status: "completed",
        input: { summary: "已完成验收并更新总结评论" },
        output: { comment_id: "comment-1", version: 2 }, duration_ms: 12
      },
      occurred_at: "2026-08-11T09:00:04Z"
    };
    queryClient.setQueryData(["instance-runs", "instance-collab"], {
      items: [run], retained_bytes: 0
    });
    queryClient.setQueryData(["instance-events", "instance-collab"], {
      items: [toolEvent], buffer: new BoundedRunEventBuffer(1, 128 * 1024),
      nextByRun: { [run.id]: undefined }, evictedCount: 0, evictedBytes: 0, incomplete: false
    });
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
      const raw = typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
      const url = new URL(raw, "http://teamva.test");
      const data = url.pathname.endsWith("/agent-instances/instance-collab/runs")
        ? { items: [run] }
        : { items: [] };
      return new Response(JSON.stringify({ data, meta: { request_id: "request-collab" } }), {
        status: 200, headers: { "Content-Type": "application/json" }
      });
    }));
    const instance: IssueAgentInstance = {
      id: "instance-collab", version: 1, issue_id: "issue-test", agent_id: "agent-test",
      agent_revision_id: "revision-test", status: "running", pending_mention_count: 0,
      created_at: "2026-08-11T09:00:00Z", updated_at: "2026-08-11T09:00:05Z"
    };
    const repositories: GitRepositorySummary[] = [{
      id: "repo-test", label: "App", root_display: "App", resource_ids: [],
      discovery_origin: "resource", worktree_kind: "main", scope: "full", branch: null,
      operation: null, generation: 1, state: "clean", counts: {}, warnings: []
    }];
    render(AgentSideSheetTestHost, { props: { instance, repositories } });

    expect(await screen.findByText(t("issues.sessionCollabComment"))).toBeInTheDocument();
    expect(screen.getByText("已完成验收并更新总结评论")).toBeInTheDocument();
    expect(screen.getByText(t("issues.sessionGenerating"))).toBeInTheDocument();
    // The sidebar's initially selected instance can remain 'running' after
    // completion. The refreshed Run, not that stale snapshot, ends Loading.
    queryClient.setQueryData(["instance-runs", "instance-collab"], {
      items: [{ ...run, version: 2, status: "succeeded" }], retained_bytes: 0
    });
    await waitFor(() => expect(screen.queryByText(t("issues.sessionGenerating"))).not.toBeInTheDocument());
  });

  it("does not render reasoning streaming deltas as an assistant reply", async () => {
    vi.stubGlobal("EventSource", undefined);
    const run: Run = {
      id: "run-reasoning", version: 1, issue_id: "issue-test",
      issue_agent_instance_id: "instance-reasoning", project_id: "project-test",
      status: "succeeded", read_only: false, lease_epoch: 1, purpose: "work",
      input: [], created_at: "2026-08-11T09:00:00Z", updated_at: "2026-08-11T09:00:05Z",
      finished_at: "2026-08-11T09:00:05Z"
    };
    // 只加载了 reasoning 的流式 delta 与最终快照（最终 text part 的分页尚未加载）。
    // 修复后：思考文本只出现在思考条目，不会冒充 AI 回复。
    const events: RunEvent[] = [
      {
        id: "event-reasoning-delta", run_id: run.id, seq: 1, event_type: "provider.event",
        payload: { provider_id: "opencode", kind: "reasoning", part_id: "reasoning:msg-a:0", message_id: "msg-a", delta: "先分析问题" },
        occurred_at: "2026-08-11T09:00:01Z"
      },
      {
        id: "event-reasoning-snapshot", run_id: run.id, seq: 2, event_type: "provider.event",
        payload: { provider_id: "opencode", kind: "reasoning", part_id: "reasoning:msg-a:0", message_id: "msg-a", text: "先分析问题" },
        occurred_at: "2026-08-11T09:00:02Z"
      }
    ];
    queryClient.setQueryData(["instance-runs", "instance-reasoning"], {
      items: [run], retained_bytes: 0
    });
    queryClient.setQueryData(["instance-events", "instance-reasoning"], {
      items: events, buffer: new BoundedRunEventBuffer(1, 128 * 1024),
      nextByRun: { [run.id]: undefined }, evictedCount: 0, evictedBytes: 0, incomplete: false
    });
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
      const raw = typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
      const url = new URL(raw, "http://teamva.test");
      const data = url.pathname.endsWith("/agent-instances/instance-reasoning/runs")
        ? { items: [run] }
        : { items: [] };
      return new Response(JSON.stringify({ data, meta: { request_id: "request-reasoning" } }), {
        status: 200, headers: { "Content-Type": "application/json" }
      });
    }));
    const instance: IssueAgentInstance = {
      id: "instance-reasoning", version: 1, issue_id: "issue-test", agent_id: "agent-test",
      agent_revision_id: "revision-test", status: "idle", pending_mention_count: 0,
      created_at: "2026-08-11T09:00:00Z", updated_at: "2026-08-11T09:00:05Z"
    };
    const { container } = render(AgentSideSheetTestHost, { props: { instance, repositories: [] } });

    expect(await screen.findByText(t("issues.sessionThinking"))).toBeInTheDocument();
    expect(screen.getAllByText("先分析问题").length).toBeGreaterThan(0);
    await waitFor(() => expect(container.querySelectorAll('[data-entry-anchor^="assistant"]').length).toBe(0));
  });

  it("keeps a stable assistant reply across incremental event page loads", async () => {
    vi.stubGlobal("EventSource", undefined);
    const run: Run = {
      id: "run-final-reply", version: 1, issue_id: "issue-test",
      issue_agent_instance_id: "instance-final-reply", project_id: "project-test",
      status: "succeeded", read_only: false, lease_epoch: 1, purpose: "work",
      input: [], created_at: "2026-08-11T09:00:00Z", updated_at: "2026-08-11T09:00:05Z",
      finished_at: "2026-08-11T09:00:05Z"
    };
    const reasoningEvents: RunEvent[] = [
      {
        id: "event-reasoning-delta", run_id: run.id, seq: 1, event_type: "provider.event",
        payload: { provider_id: "opencode", kind: "reasoning", part_id: "reasoning:msg-a:0", message_id: "msg-a", delta: "先分析问题" },
        occurred_at: "2026-08-11T09:00:01Z"
      },
      {
        id: "event-reasoning-snapshot", run_id: run.id, seq: 2, event_type: "provider.event",
        payload: { provider_id: "opencode", kind: "reasoning", part_id: "reasoning:msg-a:0", message_id: "msg-a", text: "先分析问题" },
        occurred_at: "2026-08-11T09:00:02Z"
      }
    ];
    const finalEvents: RunEvent[] = [
      {
        id: "event-text-delta", run_id: run.id, seq: 3, event_type: "output.delta",
        payload: { provider_id: "opencode", part_id: "text:msg-b:0", message_id: "msg-b", text: "最终结论" },
        occurred_at: "2026-08-11T09:00:03Z"
      },
      {
        id: "event-text-snapshot", run_id: run.id, seq: 4, event_type: "provider.event",
        payload: { provider_id: "opencode", kind: "text", part_id: "text:msg-b:0", message_id: "msg-b", text: "最终结论" },
        occurred_at: "2026-08-11T09:00:04Z"
      }
    ];
    queryClient.setQueryData(["instance-runs", "instance-final-reply"], {
      items: [run], retained_bytes: 0
    });
    queryClient.setQueryData(["instance-events", "instance-final-reply"], {
      items: reasoningEvents, buffer: new BoundedRunEventBuffer(1, 128 * 1024),
      nextByRun: { [run.id]: "2" }, evictedCount: 0, evictedBytes: 0, incomplete: false
    });
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
      const raw = typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
      const url = new URL(raw, "http://teamva.test");
      const data = url.pathname.endsWith("/agent-instances/instance-final-reply/runs")
        ? { items: [run] }
        : { items: [] };
      return new Response(JSON.stringify({ data, meta: { request_id: "request-final-reply" } }), {
        status: 200, headers: { "Content-Type": "application/json" }
      });
    }));
    const instance: IssueAgentInstance = {
      id: "instance-final-reply", version: 1, issue_id: "issue-test", agent_id: "agent-test",
      agent_revision_id: "revision-test", status: "idle", pending_mention_count: 0,
      created_at: "2026-08-11T09:00:00Z", updated_at: "2026-08-11T09:00:05Z"
    };
    const { container } = render(AgentSideSheetTestHost, { props: { instance, repositories: [] } });

    // 第一阶段：只加载了思考事件。思考的流式文本不能冒充 AI 回复。
    expect(await screen.findByText(t("issues.sessionThinking"))).toBeInTheDocument();
    await waitFor(() => expect(container.querySelectorAll('[data-entry-anchor^="assistant"]').length).toBe(0));

    // 第二阶段：模拟点击“加载更多”，最终 text part 快照到达后仍然只有一条回复。
    queryClient.setQueryData(["instance-events", "instance-final-reply"], (current: any) => ({
      ...current,
      items: [...(current?.items ?? []), ...finalEvents],
      nextByRun: { [run.id]: undefined }
    }));

    await waitFor(() => {
      const replies = [...container.querySelectorAll('[data-entry-anchor^="assistant"]')];
      expect(replies).toHaveLength(1);
      expect(replies[0]?.textContent).toContain("最终结论");
      expect(replies[0]?.textContent).not.toContain("先分析问题");
    });
  });

  it("auto-loads remaining event pages for small sessions", async () => {
    vi.stubGlobal("EventSource", undefined);
    const run: Run = {
      id: "run-autoload", version: 1, issue_id: "issue-test",
      issue_agent_instance_id: "instance-autoload", project_id: "project-test",
      status: "succeeded", read_only: false, lease_epoch: 1, purpose: "work",
      input: [], created_at: "2026-08-11T09:00:00Z", updated_at: "2026-08-11T09:00:05Z",
      finished_at: "2026-08-11T09:00:05Z"
    };
    const pageOne: RunEvent = {
      id: "event-auto-1", run_id: run.id, seq: 1, event_type: "provider.item",
      payload: { provider_id: "codex", item: { id: "msg-1", type: "agentMessage", text: "第一页回复", status: "completed" } },
      occurred_at: "2026-08-11T09:00:01Z"
    };
    const pageTwo: RunEvent = {
      id: "event-auto-2", run_id: run.id, seq: 2, event_type: "provider.item",
      payload: { provider_id: "codex", item: { id: "msg-2", type: "agentMessage", text: "第二页回复", status: "completed" } },
      occurred_at: "2026-08-11T09:00:02Z"
    };
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
      const raw = typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
      const url = new URL(raw, "http://teamva.test");
      let data: unknown = { items: [] };
      if (url.pathname.endsWith("/agent-instances/instance-autoload/runs")) data = { items: [run] };
      else if (url.pathname.endsWith("/runs/run-autoload/events")) {
        const after = url.searchParams.get("after");
        data = after === "0"
          ? { items: [pageOne], next_cursor: "1" }
          : { items: [pageTwo], next_cursor: null };
      }
      return new Response(JSON.stringify({ data, meta: { request_id: "request-autoload" } }), {
        status: 200, headers: { "Content-Type": "application/json" }
      });
    }));
    const instance: IssueAgentInstance = {
      id: "instance-autoload", version: 1, issue_id: "issue-test", agent_id: "agent-test",
      agent_revision_id: "revision-test", status: "idle", pending_mention_count: 0,
      created_at: "2026-08-11T09:00:00Z", updated_at: "2026-08-11T09:00:05Z"
    };
    render(AgentSideSheetTestHost, { props: { instance, repositories: [] } });

    expect(await screen.findByText("第一页回复")).toBeInTheDocument();
    expect(await screen.findByText("第二页回复")).toBeInTheDocument();
    await waitFor(() =>
      expect(screen.queryByRole("button", { name: t("issues.loadMoreActivity") })).not.toBeInTheDocument()
    );
  });

  it("does not invent collaboration actions from native FileBridge file operations", async () => {
    vi.stubGlobal("EventSource", undefined);
    const run: Run = {
      id: "run-legacy-tools", version: 1, issue_id: "issue-test",
      issue_agent_instance_id: "instance-legacy-tools", project_id: "project-test",
      status: "succeeded", read_only: false, lease_epoch: 1, purpose: "work",
      input: [], created_at: "2026-08-11T09:00:00Z", updated_at: "2026-08-11T09:00:05Z",
      finished_at: "2026-08-11T09:00:05Z"
    };
    // Only explicit provider.tool events may represent collaboration actions.
    const events: RunEvent[] = [
      {
        id: "event-legacy-bash", run_id: run.id, seq: 1, event_type: "provider.event",
        payload: {
          part: {
            id: "prt-legacy-bash", type: "tool", tool: "bash",
            state: {
              status: "completed",
              input: { command: 'cat ".teamva/tool-requests/run-legacy-tools/issue_get/req-1.json"' },
              output: '{"data":{"title":"任务标题","comments":[{"body":"第一条评论"}]}}'
            }
          }
        },
        occurred_at: "2026-08-11T09:00:01Z"
      }
    ];
    for (const event of events) event.payload.provider_id = "opencode";
    queryClient.setQueryData(["instance-runs", "instance-legacy-tools"], {
      items: [run], retained_bytes: 0
    });
    queryClient.setQueryData(["instance-events", "instance-legacy-tools"], {
      items: events, buffer: new BoundedRunEventBuffer(1, 128 * 1024),
      nextByRun: { [run.id]: undefined }, evictedCount: 0, evictedBytes: 0, incomplete: false
    });
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
      const raw = typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
      const url = new URL(raw, "http://teamva.test");
      const data = url.pathname.endsWith("/agent-instances/instance-legacy-tools/runs")
        ? { items: [run] }
        : { items: [] };
      return new Response(JSON.stringify({ data, meta: { request_id: "request-legacy-tools" } }), {
        status: 200, headers: { "Content-Type": "application/json" }
      });
    }));
    const instance: IssueAgentInstance = {
      id: "instance-legacy-tools", version: 1, issue_id: "issue-test", agent_id: "agent-test",
      agent_revision_id: "revision-test", status: "idle", pending_mention_count: 0,
      created_at: "2026-08-11T09:00:00Z", updated_at: "2026-08-11T09:00:05Z"
    };
    render(AgentSideSheetTestHost, { props: { instance, repositories: [] } });

    await screen.findByText(t("issues.sessionHint"));
    expect(screen.queryByText(t("issues.sessionCollabIssueGet"))).not.toBeInTheDocument();
    expect(screen.queryByText(/第一条评论/)).not.toBeInTheDocument();
  });

  it("does not reconstruct a published comment from paired private request and response files", async () => {
    vi.stubGlobal("EventSource", undefined);
    const run: Run = {
      id: "run-merge-read", version: 1, issue_id: "issue-test",
      issue_agent_instance_id: "instance-merge-read", project_id: "project-test",
      status: "succeeded", read_only: false, lease_epoch: 1, purpose: "work",
      input: [], created_at: "2026-08-11T09:00:00Z", updated_at: "2026-08-11T09:00:05Z",
      finished_at: "2026-08-11T09:00:05Z"
    };
    const root = "/Users/test/.teamva/tool-requests/run-merge-read/issue_comment_create";
    const events: RunEvent[] = [
      {
        id: "event-merge-write", run_id: run.id, seq: 1, event_type: "provider.event",
        payload: {
          part: {
            id: "prt-merge-write", type: "tool", tool: "write",
            state: {
              status: "completed",
              input: {
                content: '{"body":"验收结论","mentioned_agent_ids":[]}',
                filePath: `${root}/req-m1.json`
              },
              output: "Wrote file successfully."
            }
          }
        },
        occurred_at: "2026-08-11T09:00:01Z"
      },
      {
        id: "event-merge-read", run_id: run.id, seq: 2, event_type: "provider.event",
        payload: {
          part: {
            id: "prt-merge-read", type: "tool", tool: "read",
            state: {
              status: "completed",
              input: { filePath: `${root}/req-m1.json.response.json` },
              output: `<path>${root}/req-m1.json.response.json</path>\n<content>\n1: {\n2:   "data": {\n3:     "comment_id": "cmt-m1",\n4:     "version": 2\n5:   }\n6: }\n</content>`
            }
          }
        },
        occurred_at: "2026-08-11T09:00:02Z"
      }
    ];
    for (const event of events) event.payload.provider_id = "opencode";
    queryClient.setQueryData(["instance-runs", "instance-merge-read"], {
      items: [run], retained_bytes: 0
    });
    queryClient.setQueryData(["instance-events", "instance-merge-read"], {
      items: events, buffer: new BoundedRunEventBuffer(1, 128 * 1024),
      nextByRun: { [run.id]: undefined }, evictedCount: 0, evictedBytes: 0, incomplete: false
    });
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
      const raw = typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
      const url = new URL(raw, "http://teamva.test");
      const data = url.pathname.endsWith("/agent-instances/instance-merge-read/runs")
        ? { items: [run] }
        : { items: [] };
      return new Response(JSON.stringify({ data, meta: { request_id: "request-merge-read" } }), {
        status: 200, headers: { "Content-Type": "application/json" }
      });
    }));
    const instance: IssueAgentInstance = {
      id: "instance-merge-read", version: 1, issue_id: "issue-test", agent_id: "agent-test",
      agent_revision_id: "revision-test", status: "idle", pending_mention_count: 0,
      created_at: "2026-08-11T09:00:00Z", updated_at: "2026-08-11T09:00:05Z"
    };
    render(AgentSideSheetTestHost, { props: { instance, repositories: [] } });

    await screen.findByText(t("issues.sessionHint"));
    expect(screen.queryByText(t("issues.sessionCollabComment"))).not.toBeInTheDocument();
    expect(screen.queryByText(/验收结论/)).not.toBeInTheDocument();
    expect(screen.queryByText(/cmt-m1/)).not.toBeInTheDocument();
    expect(screen.queryByText(/<content>/)).not.toBeInTheDocument();
  });

  it("shows collaboration tool parameters and results in the event detail", async () => {
    vi.stubGlobal("EventSource", undefined);
    const run: Run = {
      id: "run-tool-detail", version: 1, issue_id: "issue-test",
      issue_agent_instance_id: "instance-tool-detail", project_id: "project-test",
      status: "succeeded", read_only: false, lease_epoch: 1, purpose: "work",
      input: [], created_at: "2026-08-11T09:00:00Z", updated_at: "2026-08-11T09:00:05Z",
      finished_at: "2026-08-11T09:00:05Z"
    };
    const events: RunEvent[] = [
      {
        id: "event-tool-detail", run_id: run.id, seq: 1, event_type: "provider.tool",
        payload: {
          provider_id: "codex", id: "req-detail", tool: "issue_get", status: "completed",
          input: { summary: "读取任务上下文" },
          output: { title: "任务标题", status: "open", comment_count: 2 },
          duration_ms: 3
        },
        occurred_at: "2026-08-11T09:00:01Z"
      }
    ];
    queryClient.setQueryData(["instance-runs", "instance-tool-detail"], {
      items: [run], retained_bytes: 0
    });
    queryClient.setQueryData(["instance-events", "instance-tool-detail"], {
      items: events, buffer: new BoundedRunEventBuffer(1, 128 * 1024),
      nextByRun: { [run.id]: undefined }, evictedCount: 0, evictedBytes: 0, incomplete: false
    });
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
      const raw = typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
      const url = new URL(raw, "http://teamva.test");
      const data = url.pathname.endsWith("/agent-instances/instance-tool-detail/runs")
        ? { items: [run] }
        : { items: [] };
      return new Response(JSON.stringify({ data, meta: { request_id: "request-tool-detail" } }), {
        status: 200, headers: { "Content-Type": "application/json" }
      });
    }));
    const instance: IssueAgentInstance = {
      id: "instance-tool-detail", version: 1, issue_id: "issue-test", agent_id: "agent-test",
      agent_revision_id: "revision-test", status: "idle", pending_mention_count: 0,
      created_at: "2026-08-11T09:00:00Z", updated_at: "2026-08-11T09:00:05Z"
    };
    render(AgentSideSheetTestHost, { props: { instance, repositories: [] } });

    const label = await screen.findByText(t("issues.sessionCollabIssueGet"));
    const row = label.closest("button");
    expect(row).not.toBeNull();
    expect(row).toBeEnabled();
    await fireEvent.click(row!);
    expect(await screen.findByText(/参数/)).toBeInTheDocument();
    expect(screen.getAllByText(/读取任务上下文/).length).toBeGreaterThan(1);
    expect(screen.getByText(/返回/)).toBeInTheDocument();
    expect(screen.getByText(/任务标题/)).toBeInTheDocument();
  });

  it("starts a project-workspace Run without a repository selector", async () => {
    vi.stubGlobal("EventSource", undefined);
    let runPosts = 0;
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const raw = typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
      const url = new URL(raw, "http://teamva.test");
      let data: unknown = { items: [] };
      if (url.pathname.endsWith("/issues/issue-test/runs") && init?.method === "POST") {
        runPosts += 1;
        data = {
          id: "run-workspace", issue_id: "issue-test", issue_agent_instance_id: "instance-partial",
          agent_id: "agent-test", project_id: "project-test", status: "succeeded",
          read_only: false, lease_epoch: 1, input: [], purpose: "work", version: 1,
          created_at: "2026-08-11T10:00:00Z", updated_at: "2026-08-11T10:00:00Z"
        };
      }
      return new Response(JSON.stringify({ data, meta: { request_id: "request-partial-run" } }), {
        status: 200, headers: { "Content-Type": "application/json" }
      });
    }));
    const instance: IssueAgentInstance = {
      id: "instance-partial", version: 1, issue_id: "issue-test", agent_id: "agent-test",
      agent_revision_id: "revision-test", status: "idle", pending_mention_count: 0,
      created_at: "2026-08-11T09:00:00Z", updated_at: "2026-08-11T10:00:00Z"
    };
    const repositories: GitRepositorySummary[] = [{
      id: "repo-test", label: "Nested source", root_display: "Nested source", resource_ids: [],
      discovery_origin: "ancestor", worktree_kind: "main", scope: "resource_prefix",
      branch: null, operation: null, generation: 1, state: "clean", counts: {}, warnings: []
    }];
    render(AgentSideSheetTestHost, { props: { instance, repositories } });

    await fireEvent.input(screen.getByLabelText("Session input"), { target: { value: "Please run" } });
    expect(screen.queryByLabelText("Repository for the next run")).not.toBeInTheDocument();
    const send = screen.getByRole("button", { name: "Send" });
    await waitFor(() => expect(send).toBeEnabled());
    await fireEvent.click(send);
    await waitFor(() => expect(runPosts).toBe(1));
  });
});

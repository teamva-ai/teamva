import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/svelte";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { queryClient } from "../lib/query";
import { navigate } from "../lib/router.svelte";
import { t } from "../lib/i18n.svelte";
import { scheduleInstantLabel } from "../lib/scheduleConfig";
import { localeInfo } from "../lib/i18n.svelte";
import type { Agent } from "../lib/types";
import SchedulesTestHost from "./SchedulesTestHost.svelte";

beforeEach(() => navigate("/schedules", { replace: true }));
afterEach(() => {
  cleanup();
  queryClient.clear();
  vi.unstubAllGlobals();
  navigate("/workbench", { replace: true });
});

const historyTasks = ["first", "target"].map((id) => ({
  id: `schedule-${id}`, version: 1, created_at: "2026-10-01T00:00:00Z", updated_at: "2026-10-01T00:00:00Z",
  project_id: "project-test", name: `Schedule ${id}`, enabled: true, schedule_kind: "cron",
  schedule_expression: "0 9 * * *", timezone: "UTC", prompt: [{ type: "text", text: "Review" }],
  agent_id: "agent-test", issue_mode: "new", concurrency_policy: "forbid", misfire_policy: "skip"
}));
function stubHistory(runProject = "project-test") {
  const requests: string[] = [];
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
    const path = String(input);
    requests.push(path);
    let data: unknown = { items: [] };
    if (path.endsWith("/scheduled-tasks")) data = { items: historyTasks };
    if (path.endsWith("/runs")) data = { items: [
      {
        id: "trigger-known", scheduled_task_id: "schedule-target", run_id: "run-target", trigger_key: "manual:one",
        scheduled_for: "2026-10-01T09:00:00Z", created_at: "2026-10-01T09:00:00Z", status: "failed",
        dispatch_attempts: 3, retry_at: "2026-10-01T09:05:00Z", last_error: "agent_not_ready"
      },
      {
        id: "trigger-unsafe", scheduled_task_id: "schedule-target", trigger_key: "manual:two",
        scheduled_for: "2026-10-01T10:00:00Z", created_at: "2026-10-01T10:00:00Z", status: "failed",
        dispatch_attempts: 1, last_error: "password=should-never-appear-in-the-UI"
      }
    ] };
    if (path.endsWith("/runs/run-target")) data = {
      id: "run-target", project_id: runProject, issue_id: "issue-target", issue_agent_instance_id: "instance-target"
    };
    return new Response(JSON.stringify({ data }), { status: 200, headers: { "Content-Type": "application/json" } });
  }));
  return requests;
}

describe("schedule history diagnostics and exact navigation", () => {
  it("opens the requested task's history, shows bounded diagnostics, and resolves the exact Issue/Run", async () => {
    navigate("/schedules?schedule=schedule-target", { replace: true });
    const requests = stubHistory();
    // Workbench navigation commonly supplies an already-cached task list.
    queryClient.setQueryData(["schedules", "project-test"], { items: historyTasks });
    render(SchedulesTestHost);
    const dialog = await screen.findByRole("dialog", { name: t("schedules.historyTitle", { name: "Schedule target" }) });
    await within(dialog).findByText("agent_not_ready");
    expect(requests).toContain("/v1/scheduled-tasks/schedule-target/runs");
    expect(requests).not.toContain("/v1/scheduled-tasks/schedule-first/runs");
    const known = dialog.querySelector('[data-schedule-run-id="trigger-known"]') as HTMLElement;
    expect(known.querySelector("dd")).toHaveTextContent("3");
    expect(known).toHaveTextContent(t("schedules.dispatchAttempts"));
    expect(known).toHaveTextContent(scheduleInstantLabel("2026-10-01T09:05:00Z", "UTC", localeInfo.value));
    expect(within(dialog).getByText("schedule_dispatch_failed")).toBeInTheDocument();
    expect(dialog).not.toHaveTextContent("should-never-appear-in-the-UI");
    await fireEvent.click(within(known).getByRole("button", { name: t("schedules.openRun") }));
    await waitFor(() => expect(window.location.pathname + window.location.search).toBe("/issues?issue=issue-target&session=instance-target&run=run-target"));
    expect(requests).toContain("/v1/runs/run-target");
  });

  it("does not redirect to a run belonging to another project", async () => {
    navigate("/schedules?schedule=schedule-target", { replace: true });
    stubHistory("project-other");
    render(SchedulesTestHost);
    const open = await screen.findByRole("button", { name: t("schedules.openRun") });
    await fireEvent.click(open);
    expect(await screen.findByRole("alert")).toHaveTextContent(t("schedules.runUnavailable"));
    expect(window.location.pathname + window.location.search).toBe("/schedules?schedule=schedule-target");
  });

  it("does not open the first history when the requested schedule is missing", async () => {
    navigate("/schedules?schedule=missing", { replace: true });
    const requests = stubHistory();
    render(SchedulesTestHost);
    expect(await screen.findByText(t("schedules.notFound"))).toBeInTheDocument();
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(requests.some((path) => path.endsWith("/runs"))).toBe(false);
  });
});

type Request = { path: string; method: string; body: Record<string, unknown> };
function response(data: unknown) {
  return new Response(JSON.stringify({ data }), { headers: { "Content-Type": "application/json" } });
}
function failure() {
  return new Response(JSON.stringify({ error: { code: "internal_error", retryable: true } }), {
    status: 500, headers: { "Content-Type": "application/json" }
  });
}
function stubEditor(handler?: (request: Request) => Response | Promise<Response> | undefined) {
  const requests: Request[] = [];
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const request = { path: String(input), method: init?.method ?? "GET", body: init?.body ? JSON.parse(String(init.body)) : {} };
    requests.push(request);
    const result = handler?.(request);
    if (result) return result;
    if (request.path.endsWith("/preview")) return response({ times: [], dst_note: "" });
    if (request.method === "POST" || request.method === "PATCH") return response({ ...historyTasks[0], ...request.body });
    return response({ items: [] });
  }));
  return requests;
}
async function openCreate() {
  await fireEvent.click((await screen.findAllByRole("button", { name: t("schedules.newTask") }))[0]);
  return screen.findByRole("dialog", { name: t("schedules.createTitle") });
}
async function fillDraft(dialog: HTMLElement) {
  const fields = within(dialog);
  const name = fields.queryByLabelText(t("schedules.taskName"));
  if (name) await fireEvent.input(name, { target: { value: "Daily review" } });
  await fireEvent.input(fields.getByLabelText(t("schedules.prompt")), { target: { value: "Review changes" } });
  await fireEvent.change(fields.getByLabelText(t("schedules.agent")), { target: { value: "agent-test" } });
}
const issueItems = [
  { id: "issue-alpha", project_id: "project-test", status: "open", title: "Alpha review" },
  { id: "issue-beta", project_id: "project-test", status: "open", title: "Beta review" },
  { id: "issue-closed", project_id: "project-test", status: "closed", title: "Closed review" },
  { id: "issue-foreign", project_id: "project-other", status: "open", title: "Foreign review" }
];
const readyAgent: Agent = {
  id: "agent-test", version: 1, created_at: "2026-08-11T00:00:00Z", updated_at: "2026-08-11T00:00:00Z",
  computer_id: "computer-test", agent_service_id: "service-test", name: "Builder", status: "active", current_revision_id: "revision-test"
};

describe("Schedules structured editor", () => {
  it.each([false, true])("only includes a name after actual input, including a change back to the same name (%s)", async (edited) => {
    const task = historyTasks[0];
    const requests = stubEditor(({ path, method }) => path.endsWith("/scheduled-tasks") && method === "GET" ? response({ items: [task] }) : undefined);
    render(SchedulesTestHost);
    await fireEvent.click(await screen.findByRole("button", { name: t("schedules.edit") }));
    const dialog = await screen.findByRole("dialog", { name: t("schedules.editTitle") });
    if (edited) {
      const name = within(dialog).getByLabelText(t("schedules.taskName"));
      await fireEvent.input(name, { target: { value: "Another name" } });
      await fireEvent.input(name, { target: { value: task.name } });
    }
    await fireEvent.click(within(dialog).getByRole("button", { name: t("common.save") }));
    await waitFor(() => expect(requests.some((item) => item.method === "PATCH")).toBe(true));
    const patch = requests.find((item) => item.method === "PATCH")!.body;
    if (edited) expect(patch).toHaveProperty("name", task.name);
    else expect(patch).not.toHaveProperty("name");
  });

  it.each([false, true])("refreshes an asynchronous name/version without erasing a prompt or manual name draft (%s)", async (manual) => {
    const task = historyTasks[0];
    const requests = stubEditor(({ path, method }) => path.endsWith("/scheduled-tasks") && method === "GET" ? response({ items: [task] }) : undefined);
    render(SchedulesTestHost);
    await fireEvent.click(await screen.findByRole("button", { name: t("schedules.edit") }));
    const dialog = await screen.findByRole("dialog", { name: t("schedules.editTitle") });
    const name = within(dialog).getByLabelText(t("schedules.taskName"));
    const prompt = within(dialog).getByLabelText(t("schedules.prompt"));
    await fireEvent.input(prompt, { target: { value: "Prompt draft survives" } });
    if (manual) await fireEvent.input(name, { target: { value: "My schedule name" } });
    queryClient.setQueryData(["schedules", "project-test"], { items: [{ ...task, name: "Automatic schedule name", version: 2 }] });
    await waitFor(() => expect(name).toHaveValue(manual ? "My schedule name" : "Automatic schedule name"));
    expect(prompt).toHaveValue("Prompt draft survives");
    await fireEvent.click(within(dialog).getByRole("button", { name: t("common.save") }));
    await waitFor(() => expect(requests.some((item) => item.method === "PATCH")).toBe(true));
    const patch = requests.find((item) => item.method === "PATCH")!.body;
    expect(patch).toMatchObject({ expected_version: 2, prompt: [{ type: "text", text: "Prompt draft survives" }] });
    if (manual) expect(patch).toHaveProperty("name", "My schedule name");
    else expect(patch).not.toHaveProperty("name");
  });

  it("keeps configuration in the left pane and the single shared prompt control in the right pane", async () => {
    stubEditor();
    render(SchedulesTestHost);
    const dialog = await openCreate();
    const configuration = dialog.querySelector("[data-schedule-editor-configuration]") as HTMLElement;
    const promptPane = dialog.querySelector("[data-schedule-editor-prompt]") as HTMLElement;
    const layout = dialog.querySelector("[data-schedule-editor-layout]") as HTMLElement;
    expect(layout).toContainElement(configuration);
    expect(layout).toContainElement(promptPane);
    expect([...layout.children]).toEqual([configuration, promptPane]);
    expect(within(configuration).queryByLabelText(t("schedules.taskName"))).not.toBeInTheDocument();
    for (const label of [t("schedules.agent"), t("scheduleEditor.issueMode"), t("schedules.scheduleKind"), t("schedules.timezone")]) {
      expect(within(configuration).getByLabelText(label)).toBeInTheDocument();
    }
    expect(within(configuration).queryByLabelText(t("schedules.prompt"))).not.toBeInTheDocument();
    const prompt = within(promptPane).getByLabelText(t("schedules.prompt"));
    expect(prompt).toHaveClass("am-textarea");
    expect(prompt).toHaveAttribute("rows", "16");
    expect(prompt).toHaveAttribute("maxlength", "40000");
    expect(prompt).toBeRequired();
    expect(within(dialog).getAllByLabelText(t("schedules.prompt"))).toEqual([prompt]);
    expect(prompt.closest("form")).toBe(within(dialog).getByRole("form"));
    expect(within(dialog).getByRole("button", { name: t("schedules.create") })).toHaveAttribute("form", prompt.closest("form")!.id);
  });

  it("uses the same two panes when editing and preserves the existing prompt when timing and Issue mode change", async () => {
    stubEditor(({ path, method }) => {
      if (path.endsWith("/scheduled-tasks") && method === "GET") return response({ items: [historyTasks[0]] });
      if (path.endsWith("/issues")) return response({ items: issueItems });
    });
    render(SchedulesTestHost);
    await fireEvent.click(await screen.findByRole("button", { name: t("schedules.edit") }));
    const dialog = await screen.findByRole("dialog", { name: t("schedules.editTitle") });
    const configuration = dialog.querySelector("[data-schedule-editor-configuration]") as HTMLElement;
    const promptPane = dialog.querySelector("[data-schedule-editor-prompt]") as HTMLElement;
    const prompt = within(promptPane).getByLabelText(t("schedules.prompt"));
    expect(prompt).toHaveValue("Review");
    await fireEvent.input(prompt, { target: { value: "Review changes\nPreserve this detailed prompt." } });
    await fireEvent.change(within(configuration).getByLabelText(t("schedules.scheduleKind")), { target: { value: "weekly" } });
    await fireEvent.change(within(configuration).getByLabelText(t("scheduleEditor.issueMode")), { target: { value: "existing" } });
    await within(configuration).findByRole("option", { name: "Alpha review · issue-alpha" });
    expect(within(configuration).getByLabelText(t("scheduleEditor.selectIssue"))).toBeInTheDocument();
    expect(prompt).toHaveValue("Review changes\nPreserve this detailed prompt.");
    expect(within(promptPane).getAllByRole("textbox")).toEqual([prompt]);
  });

  it("opens creation from the central empty-state action as well as the header", async () => {
    stubEditor();
    render(SchedulesTestHost);
    await screen.findByText(t("schedules.noTasks"));
    const actions = screen.getAllByRole("button", { name: t("schedules.newTask") });
    expect(actions).toHaveLength(2);
    expect(actions[1].parentElement).toHaveTextContent(t("schedules.noTasksDesc"));
    await fireEvent.click(actions[1]);
    expect(await screen.findByRole("dialog", { name: t("schedules.createTitle") })).toBeInTheDocument();
  });

  it("defaults to daily 09:00 and a new Issue, then sends multiple exact times without cron expansion", async () => {
    const requests = stubEditor();
    render(SchedulesTestHost);
    const dialog = await openCreate();
    const fields = within(dialog);
    expect(fields.getByLabelText(t("schedules.scheduleKind"))).toHaveValue("daily");
    expect(fields.getByLabelText(t("scheduleEditor.time", { index: 1 }))).toHaveValue("09:00");
    expect(fields.getByLabelText(t("scheduleEditor.issueMode"))).toHaveValue("new");
    expect(requests.some((request) => request.path.endsWith("/issues"))).toBe(false);
    await fillDraft(dialog);
    await fireEvent.input(fields.getByLabelText(t("scheduleEditor.time", { index: 1 })), { target: { value: "17:45" } });
    await fireEvent.click(fields.getByRole("button", { name: t("scheduleEditor.addTime") }));
    await fireEvent.input(fields.getByLabelText(t("scheduleEditor.time", { index: 2 })), { target: { value: "08:15" } });
    await fireEvent.change(fields.getByLabelText(t("schedules.timezone")), { target: { value: "America/New_York" } });
    const create = fields.getByRole("button", { name: t("schedules.create") });
    const form = fields.getByRole("form");
    expect(create.closest("form")).toBeNull();
    expect(create).toHaveAttribute("form", form.id);
    await fireEvent.click(create);
    await waitFor(() => expect(requests.filter((request) => request.method === "POST" && request.path.endsWith("/scheduled-tasks"))).toHaveLength(1));
    const saved = requests.find((request) => request.method === "POST" && request.path.endsWith("/scheduled-tasks"))!;
    expect(saved.body).toMatchObject({
      schedule_kind: "daily", times: ["08:15", "17:45"], timezone: "America/New_York",
      schedule_expression: "", issue_mode: "new", issue_id: "", agent_id: "agent-test"
    });
    expect(saved.body).not.toHaveProperty("weekdays");
    expect(saved.body).not.toHaveProperty("name");
    expect(requests.some((request) => request.path.endsWith("/issues"))).toBe(false);
  });

  it("sends weekly weekdays and timezone, and requires at least one weekday", async () => {
    const requests = stubEditor();
    render(SchedulesTestHost);
    const dialog = await openCreate();
    const fields = within(dialog);
    await fillDraft(dialog);
    await fireEvent.change(fields.getByLabelText(t("schedules.scheduleKind")), { target: { value: "weekly" } });
    const create = fields.getByRole("button", { name: t("schedules.create") });
    await fireEvent.click(fields.getByRole("checkbox", { name: "Mon" }));
    expect(create).toBeDisabled();
    expect(fields.getByText(t("scheduleEditor.chooseWeekday"))).toBeInTheDocument();
    await fireEvent.click(fields.getByRole("checkbox", { name: "Fri" }));
    await fireEvent.click(fields.getByRole("checkbox", { name: "Sun" }));
    await fireEvent.input(fields.getByLabelText(t("schedules.timezone")), { target: { value: "Asia/Shanghai" } });
    await fireEvent.click(create);
    await waitFor(() => expect(requests.some((request) => request.method === "POST" && request.path.endsWith("/scheduled-tasks"))).toBe(true));
    expect(requests.find((request) => request.method === "POST" && request.path.endsWith("/scheduled-tasks"))?.body).toMatchObject({
      schedule_kind: "weekly", weekdays: [0, 5], times: ["09:00"], timezone: "Asia/Shanghai", schedule_expression: ""
    });
  });

  it("lazily loads only open project Issues, searches by title/id, and submits the existing target", async () => {
    const requests = stubEditor(({ path }) => path.endsWith("/issues") ? response({ items: issueItems }) : undefined);
    render(SchedulesTestHost);
    const dialog = await openCreate();
    const fields = within(dialog);
    await fillDraft(dialog);
    expect(requests.some((request) => request.path.endsWith("/issues"))).toBe(false);
    await fireEvent.change(fields.getByLabelText(t("scheduleEditor.issueMode")), { target: { value: "existing" } });
    const select = fields.getByLabelText(t("scheduleEditor.selectIssue"));
    await within(select).findByRole("option", { name: "Alpha review · issue-alpha" });
    expect(requests.filter((request) => request.path.endsWith("/issues")).map((request) => request.path)).toEqual(["/v1/projects/project-test/issues"]);
    expect(within(select).queryByRole("option", { name: /Closed review|Foreign review/ })).not.toBeInTheDocument();
    expect(fields.getByRole("button", { name: t("schedules.create") })).toBeDisabled();
    const search = fields.getByLabelText(t("scheduleEditor.issueSearch"));
    await fireEvent.input(search, { target: { value: "BETA" } });
    expect(within(select).queryByRole("option", { name: /Alpha review/ })).not.toBeInTheDocument();
    expect(within(select).getByRole("option", { name: /Beta review/ })).toBeInTheDocument();
    await fireEvent.input(search, { target: { value: "issue-alpha" } });
    expect(within(select).queryByRole("option", { name: /Beta review/ })).not.toBeInTheDocument();
    await fireEvent.change(select, { target: { value: "issue-alpha" } });
    await fireEvent.click(fields.getByRole("button", { name: t("schedules.create") }));
    await waitFor(() => expect(requests.some((request) => request.method === "POST" && request.path.endsWith("/scheduled-tasks"))).toBe(true));
    expect(requests.find((request) => request.method === "POST" && request.path.endsWith("/scheduled-tasks"))?.body).toMatchObject({ issue_mode: "existing", issue_id: "issue-alpha" });
  });

  it("edits an existing Issue target and explicitly clears it when switching to new Issue mode", async () => {
    const task = { ...historyTasks[0], issue_mode: "existing", issue_id: "issue-alpha" };
    const requests = stubEditor(({ path, method }) => {
      if (path.endsWith("/issues")) return response({ items: issueItems });
      if (path.endsWith("/scheduled-tasks") && method === "GET") return response({ items: [task] });
    });
    render(SchedulesTestHost);
    await fireEvent.click(await screen.findByRole("button", { name: t("schedules.edit") }));
    const dialog = await screen.findByRole("dialog", { name: t("schedules.editTitle") });
    const fields = within(dialog);
    await within(fields.getByLabelText(t("scheduleEditor.selectIssue"))).findByRole("option", { name: /Alpha review/ });
    expect(fields.getByLabelText(t("scheduleEditor.issueMode"))).toHaveValue("existing");
    expect(fields.getByLabelText(t("scheduleEditor.selectIssue"))).toHaveValue("issue-alpha");
    await fireEvent.change(fields.getByLabelText(t("scheduleEditor.issueMode")), { target: { value: "new" } });
    expect(fields.queryByLabelText(t("scheduleEditor.selectIssue"))).not.toBeInTheDocument();
    await fireEvent.click(fields.getByRole("button", { name: t("common.save") }));
    await waitFor(() => expect(requests.some((request) => request.method === "PATCH")).toBe(true));
    expect(requests.find((request) => request.method === "PATCH")?.body).toMatchObject({ issue_mode: "new", issue_id: "", expected_version: task.version });
  });

  it.each(["issue-deleted", "issue-closed", "issue-foreign"])("shows unavailable target %s explicitly and blocks saving", async (issueId) => {
    stubEditor(({ path, method }) => {
      if (path.endsWith("/issues")) return response({ items: issueItems });
      if (path.endsWith("/scheduled-tasks") && method === "GET") return response({ items: [{ ...historyTasks[0], issue_mode: "existing", issue_id: issueId }] });
    });
    render(SchedulesTestHost);
    await fireEvent.click(await screen.findByRole("button", { name: t("schedules.edit") }));
    const dialog = await screen.findByRole("dialog", { name: t("schedules.editTitle") });
    const fields = within(dialog);
    const select = fields.getByLabelText(t("scheduleEditor.selectIssue"));
    await within(select).findByRole("option", { name: /Alpha review/ });
    expect(select).toHaveValue(issueId);
    expect(within(select).getByRole("option", { name: `${issueId} · ${t("scheduleEditor.issueUnavailable")}` })).toBeDisabled();
    expect(fields.getByRole("button", { name: t("common.save") })).toBeDisabled();
  });

  it("preserves a draft when Issue loading fails and supports an explicit retry", async () => {
    let loads = 0;
    stubEditor(({ path }) => path.endsWith("/issues") ? (++loads === 1 ? failure() : response({ items: issueItems })) : undefined);
    render(SchedulesTestHost);
    const dialog = await openCreate();
    const fields = within(dialog);
    await fillDraft(dialog);
    await fireEvent.change(fields.getByLabelText(t("scheduleEditor.issueMode")), { target: { value: "existing" } });
    const retry = await fields.findByRole("button", { name: t("common.retry") });
    expect(fields.queryByLabelText(t("schedules.taskName"))).not.toBeInTheDocument();
    expect(fields.getByLabelText(t("schedules.prompt"))).toHaveValue("Review changes");
    expect(fields.getByRole("button", { name: t("schedules.create") })).toBeDisabled();
    expect(loads).toBe(1);
    await fireEvent.click(retry);
    const select = fields.getByLabelText(t("scheduleEditor.selectIssue"));
    await within(select).findByRole("option", { name: /Alpha review/ });
    expect(loads).toBe(2);
    await fireEvent.change(select, { target: { value: "issue-alpha" } });
    expect(fields.getByRole("button", { name: t("schedules.create") })).toBeEnabled();
  });

  it("keeps all editor fields after a failed save and retries the same payload", async () => {
    let saves = 0;
    const requests = stubEditor(({ path, method }) => path.endsWith("/scheduled-tasks") && method === "POST" ? (++saves === 1 ? failure() : response(historyTasks[0])) : undefined);
    render(SchedulesTestHost);
    const dialog = await openCreate();
    const fields = within(dialog);
    await fillDraft(dialog);
    await fireEvent.input(fields.getByLabelText(t("schedules.timezone")), { target: { value: "Asia/Tokyo" } });
    await fireEvent.click(fields.getByRole("button", { name: t("schedules.create") }));
    await fields.findByRole("alert");
    expect(fields.queryByLabelText(t("schedules.taskName"))).not.toBeInTheDocument();
    expect(fields.getByLabelText(t("schedules.prompt"))).toHaveValue("Review changes");
    expect(fields.getByLabelText(t("schedules.agent"))).toHaveValue("agent-test");
    expect(fields.getByLabelText(t("schedules.timezone"))).toHaveValue("Asia/Tokyo");
    expect(fields.getByLabelText(t("scheduleEditor.time", { index: 1 }))).toHaveValue("09:00");
    await fireEvent.click(fields.getByRole("button", { name: t("schedules.create") }));
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    const bodies = requests.filter((request) => request.method === "POST" && request.path.endsWith("/scheduled-tasks")).map((request) => request.body);
    expect(bodies).toHaveLength(2);
    expect(bodies[1]).toEqual(bodies[0]);
  });

  it("lists active same-computer Agents and enables every configured Agent, including Lumi", async () => {
    const requests = stubEditor();
    render(SchedulesTestHost, { agents: [readyAgent,
      { ...readyAgent, id: "lumi", name: "Lumi", system_role: "general" },
      { ...readyAgent, id: "unconfigured", name: "Unconfigured", current_revision_id: undefined },
      { ...readyAgent, id: "inactive", name: "Inactive", status: "disabled" },
      { ...readyAgent, id: "remote", name: "Other computer", computer_id: "computer-other" }
    ] });
    const dialog = await openCreate();
    const select = within(dialog).getByLabelText(t("schedules.agent"));
    expect(within(select).getByRole("option", { name: "Builder" })).toBeEnabled();
    expect(within(select).getByRole("option", { name: "Lumi" })).toBeEnabled();
    expect(within(select).getByRole("option", { name: `Unconfigured · ${t("scheduleEditor.unconfiguredAgent")}` })).toBeDisabled();
    expect(within(select).queryByRole("option", { name: /Inactive|Other computer/ })).not.toBeInTheDocument();
    expect(within(select).getAllByRole("option")).toHaveLength(4);
    await fillDraft(dialog);
    await fireEvent.change(select, { target: { value: "lumi" } });
    await fireEvent.click(within(dialog).getByRole("button", { name: t("schedules.create") }));
    await waitFor(() => expect(requests.find((request) => request.path.endsWith("/scheduled-tasks") && request.method === "POST")?.body.agent_id).toBe("lumi"));
  });

  it.each([true, false])("treats Lumi as a normal configured Agent (configured: %s)", async (configured) => {
    const requests = stubEditor();
    render(SchedulesTestHost, { agents: [{ ...readyAgent, id: "lumi", name: "Lumi", system_role: "general", current_revision_id: configured ? "revision-lumi" : undefined }] });
    const dialog = await openCreate();
    const fields = within(dialog);
    const select = fields.getByLabelText(t("schedules.agent"));
    const option = within(select).getByRole("option", { name: configured ? "Lumi" : `Lumi · ${t("scheduleEditor.unconfiguredAgent")}` });
    if (configured) {
      expect(option).toBeEnabled();
      expect(fields.queryByText(t("scheduleEditor.unconfiguredAgentsHint"))).not.toBeInTheDocument();
      await fillDraft(dialog);
      await fireEvent.change(select, { target: { value: "lumi" } });
      expect(fields.getByRole("button", { name: t("schedules.create") })).toBeEnabled();
      await fireEvent.click(fields.getByRole("button", { name: t("schedules.create") }));
      await waitFor(() => expect(requests.find((request) => request.path.endsWith("/scheduled-tasks") && request.method === "POST")?.body.agent_id).toBe("lumi"));
    } else {
      expect(option).toBeDisabled();
      expect(select).toHaveValue("");
      expect(fields.queryByText(t("scheduleEditor.noAgents"))).not.toBeInTheDocument();
      expect(fields.getByText(t("scheduleEditor.unconfiguredAgentsHint"))).toBeInTheDocument();
      expect(fields.getByRole("link", { name: t("scheduleEditor.configureAgent") })).toHaveAttribute("href", "/agents");
      await fillDraft(dialog);
      await fireEvent.change(select, { target: { value: "lumi" } });
      expect(fields.getByRole("button", { name: t("schedules.create") })).toBeDisabled();
      await fireEvent.submit(fields.getByRole("form"));
      expect(requests.some((request) => request.path.endsWith("/scheduled-tasks") && request.method === "POST")).toBe(false);
    }
  });

  it("shows an unconfigured ordinary Agent with configuration guidance but blocks creating a task", async () => {
    const requests = stubEditor();
    render(SchedulesTestHost, { agents: [{ ...readyAgent, current_revision_id: undefined }] });
    const dialog = await openCreate();
    const fields = within(dialog);
    expect(fields.getByText(t("scheduleEditor.unconfiguredAgentsHint"))).toBeInTheDocument();
    expect(fields.queryByText(t("scheduleEditor.noAgents"))).not.toBeInTheDocument();
    expect(fields.getByRole("link", { name: t("scheduleEditor.configureAgent") })).toHaveAttribute("href", "/agents");
    const select = fields.getByLabelText(t("schedules.agent"));
    expect(within(select).getByRole("option", { name: `Builder · ${t("scheduleEditor.unconfiguredAgent")}` })).toBeDisabled();
    expect(select).toHaveValue("");
    await fillDraft(dialog);
    expect(fields.getByRole("button", { name: t("schedules.create") })).toBeDisabled();
    await fireEvent.submit(fields.getByRole("form"));
    expect(requests.some((request) => request.path.endsWith("/scheduled-tasks") && request.method === "POST")).toBe(false);
  });

  it("blocks saving an existing task after its Agent becomes unconfigured", async () => {
    stubEditor(({ path, method }) => path.endsWith("/scheduled-tasks") && method === "GET" ? response({ items: [historyTasks[0]] }) : undefined);
    const view = render(SchedulesTestHost);
    await fireEvent.click(await screen.findByRole("button", { name: t("schedules.edit") }));
    const dialog = await screen.findByRole("dialog", { name: t("schedules.editTitle") });
    expect(within(dialog).getByRole("button", { name: t("common.save") })).toBeEnabled();
    await view.rerender({ agents: [{ ...readyAgent, current_revision_id: undefined }] });
    expect(within(dialog).getByRole("button", { name: t("common.save") })).toBeDisabled();
    expect(within(within(dialog).getByLabelText(t("schedules.agent"))).getByRole("option", { name: `Builder · ${t("scheduleEditor.unconfiguredAgent")}` })).toBeDisabled();
  });

  it.each(["create", "update"])("guards pending %s against repeated submit and stale cross-project completion", async (mode) => {
    let finish!: (value: Response) => void;
    const pending = new Promise<Response>((resolve) => { finish = resolve; });
    const requests = stubEditor(({ path, method }) => {
      if ((method === "POST" && path === "/v1/projects/project-test/scheduled-tasks") || method === "PATCH") return pending;
      if (method === "GET" && path === "/v1/projects/project-test/scheduled-tasks") return response({ items: mode === "update" ? [historyTasks[0]] : [] });
    });
    const view = render(SchedulesTestHost);
    let dialog: HTMLElement;
    if (mode === "update") {
      await fireEvent.click(await screen.findByRole("button", { name: t("schedules.edit") }));
      dialog = await screen.findByRole("dialog", { name: t("schedules.editTitle") });
    } else {
      dialog = await openCreate();
    }
    await fillDraft(dialog);
    const submit = within(dialog).getByRole("button", { name: t(mode === "update" ? "common.save" : "schedules.create") });
    await fireEvent.click(submit);
    await waitFor(() => expect(submit).toBeDisabled());
    expect(within(dialog).getByLabelText(t("schedules.prompt"))).toBeDisabled();
    await fireEvent.submit(within(dialog).getByRole("form"));
    expect(requests.filter((request) => request.method === "PATCH" || request.method === "POST" && request.path.endsWith("/scheduled-tasks"))).toHaveLength(1);
    const saved = requests.find((request) => request.method === (mode === "update" ? "PATCH" : "POST") && !request.path.endsWith("/preview"))!;
    expect(saved.path).toBe(mode === "update" ? `/v1/scheduled-tasks/${historyTasks[0].id}` : "/v1/projects/project-test/scheduled-tasks");
    await view.rerender({ projectId: "project-other" });
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    const newDialog = await openCreate();
    await fireEvent.input(within(newDialog).getByLabelText(t("schedules.prompt")), { target: { value: "Other project draft" } });
    const invalidation = vi.spyOn(queryClient, "invalidateQueries");
    finish(response({ ...historyTasks[0], ...saved.body }));
    await waitFor(() => expect(invalidation).toHaveBeenCalledWith({ queryKey: ["schedules", "project-test"] }));
    expect(invalidation).not.toHaveBeenCalledWith({ queryKey: ["schedules", "project-other"] });
    expect(newDialog).toBeInTheDocument();
    expect(within(newDialog).getByLabelText(t("schedules.prompt"))).toHaveValue("Other project draft");
    expect(saved.body).toMatchObject({ agent_id: "agent-test" });
    if (mode === "update") expect(saved.body).toHaveProperty("name", "Daily review");
    else expect(saved.body).not.toHaveProperty("name");
    expect(requests.some((request) => request.method === "POST" && request.path === "/v1/projects/project-other/scheduled-tasks")).toBe(false);
    invalidation.mockRestore();
  });
});

describe("Schedules project workspace", () => {
  it("switches cron to once and submits a DatePicker selection in the chosen IANA zone as RFC3339", async () => {
    const bodies: Array<Record<string, unknown>> = [];
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const path = String(input);
      let data: unknown = { items: [] };
      if (path === "/v1/projects/project-test/scheduled-tasks" && init?.method === "POST") {
        const body = JSON.parse(String(init.body));
        bodies.push(body);
        data = { ...historyTasks[0], ...body, id: "schedule-once" };
      }
      return new Response(JSON.stringify({ data }), { headers: { "Content-Type": "application/json" } });
    }));
    render(SchedulesTestHost);
    await fireEvent.click((await screen.findAllByRole("button", { name: t("schedules.newTask") }))[0]);
    const dialog = await screen.findByRole("dialog", { name: t("schedules.createTitle") });
    const form = within(dialog);
    expect(form.getByLabelText(t("schedules.scheduleKind"))).toHaveValue("daily");
    expect(form.getByLabelText(t("scheduleEditor.time", { index: 1 }))).toHaveValue("09:00");
    await fireEvent.change(form.getByLabelText(t("schedules.scheduleKind")), { target: { value: "cron" } });
    await fireEvent.input(form.getByLabelText(t("schedules.cronExpression")), { target: { value: "0 9 * * 1-5" } });
    await fireEvent.change(form.getByLabelText(t("schedules.scheduleKind")), { target: { value: "once" } });
    expect(form.queryByLabelText(t("schedules.cronExpression"))).not.toBeInTheDocument();
    const date = form.getByLabelText(t("scheduleEditor.dateTime"));
    expect(date).toHaveAttribute("type", "datetime-local");
    expect(date).toHaveValue("");
    const local = "2040-12-31T09:30";
    const zone = form.getByLabelText(t("schedules.timezone"));
    expect(zone).toHaveAttribute("list");
    await fireEvent.input(zone, { target: { value: "Asia/Tokyo" } });
    await fireEvent.input(date, { target: { value: local } });
    await fireEvent.input(form.getByLabelText(t("schedules.prompt")), { target: { value: "Review once" } });
    await fireEvent.change(form.getByLabelText(t("schedules.agent")), { target: { value: "agent-test" } });
    await fireEvent.click(form.getByRole("button", { name: t("schedules.create") }));
    await waitFor(() => expect(bodies).toHaveLength(1));
    expect(bodies[0]).toMatchObject({
      schedule_kind: "once", schedule_expression: "2040-12-31T00:30:00.000Z", timezone: "Asia/Tokyo",
      agent_id: "agent-test", prompt: [{ type: "text", text: "Review once" }]
    });
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
  });

  it("preserves an existing once timestamp's seconds and milliseconds when saving other edits", async () => {
    const expression = "2040-05-02T03:04:05.123-05:00";
    const task = { ...historyTasks[0], schedule_kind: "once", schedule_expression: expression, timezone: "Asia/Tokyo" };
    const bodies: Array<Record<string, unknown>> = [];
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const path = String(input);
      let data: unknown = { items: [] };
      if (path === "/v1/projects/project-test/scheduled-tasks") data = { items: [task] };
      if (path === `/v1/scheduled-tasks/${task.id}` && init?.method === "PATCH") {
        const body = JSON.parse(String(init.body));
        bodies.push(body);
        data = { ...task, ...body, version: 2 };
      }
      return new Response(JSON.stringify({ data }), { headers: { "Content-Type": "application/json" } });
    }));
    render(SchedulesTestHost);
    await fireEvent.click(await screen.findByRole("button", { name: t("schedules.edit") }));
    const dialog = await screen.findByRole("dialog", { name: t("schedules.editTitle") });
    const form = within(dialog);
    const date = form.getByLabelText(t("scheduleEditor.dateTime"));
    expect(date).toHaveValue("2040-05-02T17:04:05.123");
    expect(date).toHaveAttribute("step", "any");
    await fireEvent.input(form.getByLabelText(t("schedules.taskName")), { target: { value: "Renamed once task" } });
    await fireEvent.click(form.getByRole("button", { name: t("common.save") }));
    await waitFor(() => expect(bodies).toHaveLength(1));
    expect(bodies[0]).toMatchObject({
      name: "Renamed once task", schedule_kind: "once", schedule_expression: expression, expected_version: task.version
    });
    expect(new Date(String(bodies[0].schedule_expression)).getTime()).toBe(new Date(expression).getTime());
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
  });

  it("creates a task without fetching or sending a single repository ID", async () => {
    const requestBodies: Array<Record<string, unknown>> = [];
    let repositoryRequests = 0;
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const raw = typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
      const url = new URL(raw, "http://teamva.test");
      let data: unknown = { items: [] };
      if (url.pathname.endsWith("/git/repositories")) repositoryRequests += 1;
      if (url.pathname.endsWith("/scheduled-tasks") && init?.method === "POST") {
        requestBodies.push(JSON.parse(String(init.body)) as Record<string, unknown>);
        data = {
          id: "schedule-new", version: 1, created_at: "2026-08-11T00:00:00Z",
          updated_at: "2026-08-11T00:00:00Z", project_id: "project-test",
          name: "Daily review", enabled: true, schedule_kind: "cron",
          schedule_expression: "0 9 * * 1-5", timezone: "UTC",
          prompt: [{ type: "text", text: "Review changes" }], agent_id: "agent-test",
          concurrency_policy: "forbid", misfire_policy: "skip"
        };
      }
      return new Response(JSON.stringify({ data, meta: { request_id: "request-schedule" } }), {
        status: 200,
        headers: { "Content-Type": "application/json" }
      });
    }));

    render(SchedulesTestHost);
    await waitFor(() => expect(screen.getAllByRole("button", { name: t("schedules.newTask") }).length).toBeGreaterThan(0));
    await fireEvent.click(screen.getAllByRole("button", { name: t("schedules.newTask") })[0]);
    await waitFor(() => expect(screen.getByRole("dialog", { name: "New scheduled task" })).toBeInTheDocument());

    expect(screen.queryByLabelText("Task name")).not.toBeInTheDocument();
    await fireEvent.input(screen.getByLabelText("Instructions"), { target: { value: "Review changes" } });
    await fireEvent.change(screen.getByLabelText("Agent"), { target: { value: "agent-test" } });
    const create = screen.getByRole("button", { name: "Create" });
    expect(create).toBeEnabled();
    await fireEvent.click(create);
    await waitFor(() => expect(requestBodies).toHaveLength(1));
    expect(requestBodies[0]).not.toHaveProperty("git_repository_id");
    expect(repositoryRequests).toBe(0);
    expect(screen.queryByLabelText("Git repository")).not.toBeInTheDocument();
  });

  it("runs a persisted task without a repository-scope UI gate", async () => {
    let runNowPosts = 0;
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const raw = typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
      const url = new URL(raw, "http://teamva.test");
      let data: unknown = { items: [] };
      if (url.pathname.endsWith("/scheduled-tasks") && (!init?.method || init.method === "GET")) {
        data = { items: [{
          id: "schedule-workspace", version: 1, created_at: "2026-08-11T00:00:00Z",
          updated_at: "2026-08-11T00:00:00Z", project_id: "project-test",
          name: "Workspace task", enabled: true, schedule_kind: "cron",
          schedule_expression: "0 9 * * 1-5", timezone: "UTC",
          prompt: [{ type: "text", text: "Run safely" }], agent_id: "agent-test",
          concurrency_policy: "forbid", misfire_policy: "skip", dispatch_attempts: 0
        }] };
      } else if (url.pathname.endsWith("/scheduled-tasks/schedule-workspace/run-now") && init?.method === "POST") {
        runNowPosts += 1;
      }
      return new Response(JSON.stringify({ data, meta: { request_id: "request-mixed" } }), {
        status: 200, headers: { "Content-Type": "application/json" }
      });
    }));

    render(SchedulesTestHost);
    const runNow = await screen.findByRole("button", { name: "Run now" });
    expect(runNow).toBeEnabled();
    await fireEvent.click(runNow);
    await waitFor(() => expect(runNowPosts).toBe(1));
  });
});

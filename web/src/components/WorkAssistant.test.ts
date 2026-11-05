import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/svelte";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { tick } from "svelte";
import { t } from "../lib/i18n.svelte";
import { queryClient } from "../lib/query";
import { navigate } from "../lib/router.svelte";
import type { AssistantMessage } from "../lib/workRecords";
import WorkAssistantTestHost from "./WorkAssistantTestHost.svelte";

// Drive the real subscription boundary instead of coupling the parent scroll
// test to a module-level spy on an imported Svelte dependency.
const eventSources: AssistantEventSource[] = [];
class AssistantEventSource extends EventTarget {
  closed = false;
  constructor(readonly url: string) { super(); eventSources.push(this); }
  close() { this.closed = true; }
  emit(event: unknown) {
    this.dispatchEvent(new MessageEvent("output.delta", { data: JSON.stringify(event) }));
  }
}

const now = "2026-10-01T00:00:00Z";
function json(data: unknown, status = 200) {
  return new Response(JSON.stringify(status < 400 ? { data } : { error: { code: "service_unavailable" } }), {
    status, headers: { "Content-Type": "application/json" }
  });
}
function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((done) => { resolve = done; });
  return { promise, resolve };
}
function message(session = "session-a", status = "running"): AssistantMessage {
  return { id: `message-${session}`, session_id: session, author_kind: "agent", body: "", run_id: `run-${session}`, status, created_at: now };
}
function stubAssistant(options: {
  unconfigured?: boolean;
  name?: string;
  status?: string;
  stop?: Promise<Response>;
  send?: Promise<Response>;
  messages?: Promise<Response>;
} = {}) {
  const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const path = String(input).replace(/^\/v1/, "");
    if (path.endsWith("/stop")) return options.stop ?? json({});
    if (path.endsWith("/messages") && init?.method === "POST") return options.send ?? json({ id: "sent-run" });
    if (path.endsWith("/messages")) {
      if (options.messages) return options.messages;
      const session = path.includes("session-b") ? "session-b" : "session-a";
      return json({ items: options.status === "empty" ? [] : [message(session, options.status)] });
    }
    if (path.startsWith("/work-assistant/sessions")) return json({ items: [
      { id: "session-a", title: "Chat A", created_at: now, updated_at: now },
      { id: "session-b", title: "Chat B", created_at: now, updated_at: now }
    ] });
    return json({ id: "lumi", name: options.name ?? "Lumi", version: 1,
      ...(options.unconfigured ? {} : { current_revision_id: "revision-lumi" }) });
  });
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

beforeEach(() => {
  navigate("/work-records", { replace: true });
  localStorage.removeItem("teamva-work-chat-user-assistant-test-work_history");
  localStorage.removeItem("teamva-work-chat-user-assistant-test-inspirations");
  eventSources.length = 0;
  vi.stubGlobal("EventSource", AssistantEventSource);
  queryClient.setQueryDefaults(["assistant-messages"], { retry: false });
});
afterEach(() => {
  cleanup();
  queryClient.clear();
  queryClient.setQueryDefaults(["assistant-messages"], { retry: 1 });
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

describe("Lumi assistant session safety", () => {
  it("keeps the history and new-chat controls in real columns with shared md sizing", async () => {
    stubAssistant({ status: "empty" });
    render(WorkAssistantTestHost);
    const history = screen.getByRole("combobox", { name: t("work.history") });
    const create = screen.getByRole("button", { name: t("work.newChat") });
    expect(create).toHaveClass("am-button");
    expect(create).toHaveAttribute("data-size", "md");
    expect(create).toHaveAttribute("data-icon", "true");
    expect(create).toHaveAttribute("type", "button");
    expect(history.parentElement).toHaveAttribute("data-size", "md");
    expect(create.parentElement).toHaveClass("grid-cols-[minmax(0,1fr)_auto]");
    expect(create.parentElement).toContainElement(history);
    expect(create).toBeDisabled();
    await waitFor(() => expect(history).toHaveValue("session-a"));
    await waitFor(() => expect(create).toBeEnabled());
  });

  it.each(["work_history", "inspirations"] as const)(
    "only links unconfigured Lumi to the Agents page in %s and never configures or sends here",
    async (scope) => {
      const fetchMock = stubAssistant({ unconfigured: true, name: "My Lumi", status: "empty" });
      render(WorkAssistantTestHost, { scope });
      const link = await screen.findByRole("link", { name: t("controls.openAgentPage") });
      await waitFor(() => expect(document.querySelector("[data-assistant-messages]")).toHaveAttribute("aria-busy", "false"));
      expect(link).toHaveAttribute("href", "/agents?agent=lumi");
      expect(screen.getByText(t("controls.agentConfigurationCentralized"))).toBeInTheDocument();
      expect(screen.getByText(`${t("work.readonly")} · My Lumi`)).toBeInTheDocument();
      expect(screen.queryByRole("button", { name: t("work.configure") })).not.toBeInTheDocument();
      expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
      expect(screen.queryByLabelText(/provider|model/i)).not.toBeInTheDocument();
      expect(screen.queryByRole("button", { name: t("controls.chooseModel") })).not.toBeInTheDocument();
      expect(screen.getAllByRole("combobox")).toHaveLength(1);
      expect(screen.getByRole("combobox", { name: t("work.history") })).toBeInTheDocument();
      const input = screen.getByRole("textbox", { name: t(scope === "inspirations" ? "inspirations.question" : "work.question") });
      const send = screen.getByRole("button", { name: t("work.send") });
      expect(input).toBeDisabled();
      expect(send).toBeDisabled();
      // Even a programmatic submission must not bypass the missing-revision guard.
      await fireEvent.input(input, { target: { value: "must not send" } });
      await fireEvent.submit(input.closest("form")!);
      expect(send).toBeDisabled();
      expect(fetchMock.mock.calls.some(([path, init]) => String(path).includes("/work-assistant") && init?.method === "PUT")).toBe(false);
      expect(fetchMock.mock.calls.some(([, init]) => init?.method === "POST")).toBe(false);
    }
  );

  it.each([["running", "graceful", "work.stop"], ["cancelling", "force", "work.forceStop"]])(
    "stops %s with an explicit %s mode and the original run/session",
    async (status, mode, label) => {
      const pending = deferred<Response>();
      const fetchMock = stubAssistant({ status, stop: pending.promise });
      const invalidate = vi.spyOn(queryClient, "invalidateQueries");
      render(WorkAssistantTestHost);
      const stop = await screen.findByRole("button", { name: t(label) });
      await fireEvent.click(stop);
      await fireEvent.change(screen.getByRole("combobox", { name: t("work.history") }), { target: { value: "session-b" } });
      await waitFor(() => expect(fetchMock).toHaveBeenCalledWith("/v1/runs/run-session-a/stop", expect.objectContaining({
        method: "POST", body: JSON.stringify({ mode })
      })));
      pending.resolve(json({}));
      await waitFor(() => expect(invalidate).toHaveBeenCalledWith({ queryKey: ["assistant-messages", "session-a"] }));
      expect(fetchMock.mock.calls.filter(([path]) => String(path).endsWith("/stop"))).toHaveLength(1);
    }
  );

  it("keeps per-session drafts and does not clear B when A finishes sending the same text", async () => {
    const sending = deferred<Response>();
    const fetchMock = stubAssistant({ status: "empty", send: sending.promise });
    render(WorkAssistantTestHost);
    const input = screen.getByRole("textbox", { name: t("work.question") });
    const sessions = screen.getByRole("combobox", { name: t("work.history") });
    await waitFor(() => expect(sessions).toHaveValue("session-a"));
    await fireEvent.input(input, { target: { value: "same draft" } });
    await waitFor(() => expect(screen.getByRole("button", { name: t("work.send") })).toBeEnabled());
    await fireEvent.click(screen.getByRole("button", { name: t("work.send") }));
    await waitFor(() => expect(fetchMock).toHaveBeenCalledWith("/v1/work-assistant/sessions/session-a/messages", expect.objectContaining({ method: "POST" })));
    await fireEvent.change(sessions, { target: { value: "session-b" } });
    await waitFor(() => expect(input).toHaveValue(""));
    await fireEvent.input(input, { target: { value: "same draft" } });
    sending.resolve(json({ id: "run-sent" }));
    await waitFor(() => expect(screen.getByRole("button", { name: t("work.send") })).toBeEnabled());
    expect(input).toHaveValue("same draft");
    await fireEvent.change(sessions, { target: { value: "session-a" } });
    await waitFor(() => expect(input).toHaveValue(""));
    await fireEvent.change(sessions, { target: { value: "session-b" } });
    expect(input).toHaveValue("same draft");
  });

  it("snapshots the destination before beforeSend waits", async () => {
    const preparation = deferred<void>();
    const fetchMock = stubAssistant({ status: "empty" });
    render(WorkAssistantTestHost, { beforeSend: () => preparation.promise });
    const input = screen.getByRole("textbox", { name: t("work.question") });
    const sessions = screen.getByRole("combobox", { name: t("work.history") });
    await waitFor(() => expect(sessions).toHaveValue("session-a"));
    await fireEvent.input(input, { target: { value: "only A" } });
    await waitFor(() => expect(screen.getByRole("button", { name: t("work.send") })).toBeEnabled());
    await fireEvent.click(screen.getByRole("button", { name: t("work.send") }));
    await fireEvent.change(sessions, { target: { value: "session-b" } });
    await fireEvent.input(input, { target: { value: "only B" } });
    preparation.resolve();
    await waitFor(() => expect(fetchMock).toHaveBeenCalledWith("/v1/work-assistant/sessions/session-a/messages", expect.objectContaining({ method: "POST" })));
    expect(input).toHaveValue("only B");
  });

  it("shows loading and a retryable error instead of the empty-chat prompt", async () => {
    const messages = deferred<Response>();
    stubAssistant({ messages: messages.promise });
    render(WorkAssistantTestHost);
    await waitFor(() => expect(document.querySelector("[data-assistant-messages]")).toHaveAttribute("aria-busy", "true"));
    expect(screen.getByRole("status")).toHaveTextContent(t("common.loading"));
    expect(screen.queryByText(t("work.question"))).not.toBeInTheDocument();
    messages.resolve(json({}, 503));
    expect(await screen.findByRole("alert")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: t("common.retry") })).toBeInTheDocument();
    expect(screen.queryByText(t("work.question"))).not.toBeInTheDocument();
  });
});

describe("Lumi message scroll follow", () => {
  it("follows message/stream updates, preserves upward scroll, and can jump back to latest", async () => {
    stubAssistant();
    // This is a scroll/stream test, not a query hydration test. Pin the session
    // before mounting so late setup responses cannot replace its test messages.
    queryClient.setQueryData(["work-assistant"], { id: "lumi", version: 1, current_revision_id: "revision-lumi" });
    queryClient.setQueryData(["assistant-sessions", "work_history"], { items: [
      { id: "session-a", title: "Chat A", created_at: now, updated_at: now }
    ] });
    queryClient.setQueryData(["assistant-messages", "session-a"], { items: [message()] });
    render(WorkAssistantTestHost);
    await screen.findByRole("button", { name: t("work.stop") });
    // Parent controls may render before the child's subscription effect.
    await waitFor(() => expect(eventSources.some((source) => source.url.includes("/runs/run-session-a/"))).toBe(true));
    const viewport = document.querySelector<HTMLDivElement>("[data-assistant-messages]")!;
    let height = 1_000;
    Object.defineProperties(viewport, {
      scrollHeight: { configurable: true, get: () => height },
      clientHeight: { configurable: true, value: 300 }
    });
    queryClient.setQueryData(["assistant-messages", "session-a"], { items: [message(), { ...message(), id: "user-message", author_kind: "user", body: "hello" }] });
    await waitFor(() => expect(viewport.scrollTop).toBe(1_000));
    viewport.scrollTop = 200;
    await fireEvent.scroll(viewport);
    const events = eventSources.find((source) => source.url.includes("/runs/run-session-a/"))!;
    height = 1_300;
    events.emit({ id: "event-1", run_id: "run-session-a", seq: 1, event_type: "output.delta", payload: { provider_id: "test-provider", text: "stream one" }, occurred_at: now });
    await screen.findByText("stream one");
    expect(viewport.scrollTop).toBe(200);
    await fireEvent.click(screen.getByRole("button", { name: t("work.jumpToLatest") }));
    await waitFor(() => expect(viewport.scrollTop).toBe(1_300));
    height = 1_500;
    events.emit({ id: "event-2", run_id: "run-session-a", seq: 2, event_type: "output.delta", payload: { provider_id: "test-provider", text: " stream two" }, occurred_at: now });
    await screen.findByText("stream one stream two");
    await tick();
    expect(viewport.scrollTop).toBe(1_500);
    expect(screen.queryByRole("button", { name: t("work.jumpToLatest") })).not.toBeInTheDocument();
  });
});

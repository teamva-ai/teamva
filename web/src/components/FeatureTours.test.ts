import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/svelte";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { queryClient } from "../lib/query";
import { applyLocale, t } from "../lib/i18n.svelte";
import { FEATURE_TOUR_IDS, FEATURE_TOUR_ROUTES, featureTourQueryKey, type FeatureTourId } from "../lib/featureTours";
import { featureTourTranslations } from "../locales/featureTours";
import FeatureToursTestHost from "./FeatureToursTestHost.svelte";

const copy = featureTourTranslations["en-US"];
function envelope(data: unknown) { return new Response(JSON.stringify({ data }), { headers: { "Content-Type": "application/json" } }); }
function stub(options: { seen?: FeatureTourId[]; get?: Promise<Response>; save?: (id: FeatureTourId) => Response | Promise<Response> } = {}) {
  const seen = new Set<FeatureTourId>(options.seen ?? []);
  const saves: FeatureTourId[] = [];
  const fetch = vi.fn(async (input: string | URL | Request, init?: RequestInit) => {
    const path = String(input);
    if (path.endsWith("/me/feature-tours") && init?.method !== "POST") return options.get ?? envelope({ seen: [...seen] });
    const id = /\/me\/feature-tours\/([^/]+)\/seen$/.exec(path)?.[1] as FeatureTourId | undefined;
    if (id && init?.method === "POST") {
      saves.push(id);
      if (options.save) return options.save(id);
      seen.add(id);
      return envelope({ seen: [...seen] });
    }
    throw new Error("Unexpected request in feature tour test");
  });
  vi.stubGlobal("fetch", fetch);
  return { seen, saves, fetch };
}

beforeEach(() => { applyLocale("en-US"); });
afterEach(() => { cleanup(); queryClient.clear(); vi.unstubAllGlobals(); vi.restoreAllMocks(); document.querySelectorAll("[data-test-other-dialog]").forEach((element) => element.remove()); });

describe("first-entry feature tours", () => {
  it("presents and marks all seven columns once, with text, their own animation and explicit replay", async () => {
    const state = stub();
    const view = render(FeatureToursTestHost);
    for (const id of FEATURE_TOUR_IDS) {
      await view.rerender({ path: FEATURE_TOUR_ROUTES[id] });
      const dialog = await screen.findByRole("dialog", { name: copy.guides[id].title });
      expect(within(dialog).getByText(copy.guides[id].description)).toBeInTheDocument();
      expect(dialog.querySelector(`[data-feature-tour-animation="${id}"]`)).toBeInTheDocument();
      expect(dialog.querySelector(".tour-demo-label")).not.toBeInTheDocument();
      await waitFor(() => expect(dialog.contains(document.activeElement)).toBe(true));
      await waitFor(() => expect(queryClient.getQueryData(featureTourQueryKey("user-a"))).toEqual({ seen: FEATURE_TOUR_IDS.filter((item) => state.seen.has(item)) }));
      await fireEvent.click(within(dialog).getByRole("button", { name: copy.acknowledge }));
      expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    }
    expect(state.saves).toEqual([...FEATURE_TOUR_IDS]);
    await view.rerender({ path: "/workbench" });
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    await fireEvent.click(screen.getByRole("button", { name: copy.reopen }));
    const dialog = await screen.findByRole("dialog", { name: copy.guides.overview.title });
    const firstAnimation = dialog.querySelector("[data-feature-tour-animation]");
    await fireEvent.click(within(dialog).getByRole("button", { name: copy.pause }));
    expect(within(dialog).getByRole("button", { name: copy.play })).toHaveAttribute("aria-pressed", "true");
    await fireEvent.click(within(dialog).getByRole("button", { name: copy.replay }));
    expect(dialog.querySelector("[data-feature-tour-animation]")).not.toBe(firstAnimation);
    expect(within(dialog).getByRole("button", { name: copy.pause })).toHaveAttribute("aria-pressed", "false");
    await fireEvent.keyDown(document, { key: "Escape" });
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(state.saves).toHaveLength(7);
  });

  it("does not automatically reopen after a new mount or project-independent page refresh", async () => {
    const state = stub({ seen: ["resources"] });
    render(FeatureToursTestHost, { path: "/resources" });
    await waitFor(() => expect(queryClient.getQueryState(featureTourQueryKey("user-a"))?.status).toBe("success"));
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    cleanup(); queryClient.clear();
    render(FeatureToursTestHost, { path: "/resources" });
    await waitFor(() => expect(queryClient.getQueryState(featureTourQueryKey("user-a"))?.status).toBe("success"));
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(state.saves).toHaveLength(0);
  });

  it("defers to first-run setup without recording an unseen animation", async () => {
    const state = stub();
    const view = render(FeatureToursTestHost, { enabled: false });
    await waitFor(() => expect(queryClient.getQueryState(featureTourQueryKey("user-a"))?.status).toBe("success"));
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(state.saves).toHaveLength(0);
    await view.rerender({ enabled: true });
    await screen.findByRole("dialog", { name: copy.guides.overview.title });
    expect(state.saves).toEqual(["overview"]);
  });

  it("waits for an existing business dialog to close", async () => {
    const blocker = document.createElement("div"); blocker.setAttribute("role", "dialog"); blocker.dataset.testOtherDialog = ""; document.body.append(blocker);
    const state = stub(); render(FeatureToursTestHost);
    await waitFor(() => expect(queryClient.getQueryState(featureTourQueryKey("user-a"))?.status).toBe("success"));
    expect(document.querySelector("[data-feature-tour-dialog]")).not.toBeInTheDocument();
    expect(state.saves).toHaveLength(0);
    blocker.remove();
    await waitFor(() => expect(state.saves).toEqual(["overview"]));
    await screen.findByRole("dialog", { name: copy.guides.overview.title });
    expect(state.saves).toEqual(["overview"]);
  });

  it("uses the current route when a slow history response arrives", async () => {
    let resolve!: (response: Response) => void;
    const state = stub({ get: new Promise((done) => { resolve = done; }) });
    const view = render(FeatureToursTestHost);
    await view.rerender({ path: "/memories" });
    resolve(envelope({ seen: [] }));
    await screen.findByRole("dialog", { name: copy.guides.memories.title });
    expect(state.saves).toEqual(["memories"]);
  });

  it("keeps later seen sections when an earlier save response arrives after navigation", async () => {
    let finish!: (response: Response) => void;
    const pending = new Promise<Response>((resolve) => { finish = resolve; });
    stub({ save: (id) => id === "overview" ? pending : envelope({ seen: ["overview", "resources"] }) });
    const view = render(FeatureToursTestHost);
    await screen.findByRole("dialog", { name: copy.guides.overview.title });
    await view.rerender({ path: "/resources" });
    await screen.findByRole("dialog", { name: copy.guides.resources.title });
    await waitFor(() => expect(queryClient.getQueryData(featureTourQueryKey("user-a"))).toEqual({ seen: ["overview", "resources"] }));
    const lateWrite = vi.spyOn(queryClient, "setQueryData");
    finish(envelope({ seen: ["overview"] }));
    await waitFor(() => expect(lateWrite).toHaveBeenCalledWith(featureTourQueryKey("user-a"), expect.any(Function)));
    await waitFor(() => expect(queryClient.getQueryData(featureTourQueryKey("user-a"))).toEqual({ seen: ["overview", "resources"] }));
    expect(screen.getByRole("dialog", { name: copy.guides.resources.title })).toBeInTheDocument();
  });

  it("does not force a popup for unavailable history but keeps explicit help usable", async () => {
    const state = stub({ get: Promise.resolve(envelope({ malformed: true })) });
    render(FeatureToursTestHost);
    await waitFor(() => expect(queryClient.getQueryState(featureTourQueryKey("user-a"))?.status).toBe("error"));
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    const help = screen.getByRole("button", { name: copy.reopen });
    expect(help).toHaveAttribute("title", copy.seenUnavailable);
    await fireEvent.click(help);
    await screen.findByRole("dialog", { name: copy.guides.overview.title });
    expect(state.saves).toEqual(["overview"]);
  });

  it("allows closing after a save error and retries only through explicit replay", async () => {
    let failing = true;
    const state = stub({ save: (id) => failing ? new Response(JSON.stringify({ error: { code: "internal", message: "test" } }), { status: 500 }) : envelope({ seen: [id] }) });
    render(FeatureToursTestHost);
    const dialog = await screen.findByRole("dialog", { name: copy.guides.overview.title });
    expect(await within(dialog).findByRole("status")).toHaveTextContent(copy.saveFailed);
    await fireEvent.click(within(dialog).getByRole("button", { name: t("common.close") }));
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    failing = false;
    await fireEvent.click(screen.getByRole("button", { name: copy.reopen }));
    await waitFor(() => expect(state.saves).toHaveLength(2));
    await waitFor(() => expect(screen.queryByRole("status")).not.toBeInTheDocument());
  });

  it("has no tour or replay button on unrelated routes", async () => {
    const state = stub(); render(FeatureToursTestHost, { path: "/settings" });
    await waitFor(() => expect(queryClient.getQueryState(featureTourQueryKey("user-a"))?.status).toBe("success"));
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: copy.reopen })).not.toBeInTheDocument();
    expect(state.saves).toHaveLength(0);
  });

  it.each([true, false])("autoplays each opened tutorial without an extra Play click (reducedMotion=%s)", async (reducedMotion) => {
    vi.stubGlobal("matchMedia", vi.fn(() => ({ matches: reducedMotion, addEventListener: vi.fn(), removeEventListener: vi.fn() })));
    stub();
    const view = render(FeatureToursTestHost);
    let dialog = await screen.findByRole("dialog", { name: copy.guides.overview.title });
    expect(dialog.querySelector("[data-feature-tour-animation]")).toHaveAttribute("data-allow-motion", "true");
    expect(dialog.querySelector("[data-feature-tour-animation]")).toHaveAttribute("data-paused", "false");
    expect(within(dialog).getByRole("button", { name: copy.pause })).toHaveAttribute("aria-pressed", "false");
    await fireEvent.click(within(dialog).getByRole("button", { name: copy.pause }));
    expect(dialog.querySelector("[data-feature-tour-animation]")).toHaveAttribute("data-paused", "true");
    await fireEvent.click(within(dialog).getByRole("button", { name: copy.play }));
    expect(dialog.querySelector("[data-feature-tour-animation]")).toHaveAttribute("data-paused", "false");
    await view.rerender({ path: "/resources" });
    dialog = await screen.findByRole("dialog", { name: copy.guides.resources.title });
    expect(dialog.querySelector("[data-feature-tour-animation]")).toHaveAttribute("data-allow-motion", "true");
    expect(dialog.querySelector("[data-feature-tour-animation]")).toHaveAttribute("data-paused", "false");
    expect(within(dialog).getByRole("button", { name: copy.pause })).toBeInTheDocument();
  });

  it("resumes after returning to the page but preserves the user's explicit pause", async () => {
    let hidden = false;
    vi.spyOn(document, "hidden", "get").mockImplementation(() => hidden);
    stub(); render(FeatureToursTestHost);
    const dialog = await screen.findByRole("dialog", { name: copy.guides.overview.title });
    const animation = dialog.querySelector("[data-feature-tour-animation]");
    hidden = true;
    await fireEvent(document, new Event("visibilitychange"));
    expect(animation).toHaveAttribute("data-paused", "true");
    hidden = false;
    await fireEvent(document, new Event("visibilitychange"));
    expect(animation).toHaveAttribute("data-paused", "false");
    await fireEvent.click(within(dialog).getByRole("button", { name: copy.pause }));
    hidden = true;
    await fireEvent(document, new Event("visibilitychange"));
    hidden = false;
    await fireEvent(document, new Event("visibilitychange"));
    expect(animation).toHaveAttribute("data-paused", "true");
  });
});

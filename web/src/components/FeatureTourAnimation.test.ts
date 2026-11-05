import { cleanup, render } from "@testing-library/svelte";
import { afterEach, describe, expect, it, vi } from "vitest";
import FeatureTourAnimation from "./FeatureTourAnimation.svelte";
import source from "./FeatureTourAnimation.svelte?raw";

afterEach(() => { cleanup(); vi.restoreAllMocks(); });

const steps = ["第一步 — localized", "第二步 — localized", "第三步 — localized"] as const;
const scenes = {
  overview: ["project-progress", "task-summary", "summary-expanded"],
  issues: ["issue-compose", "agent-reply", "issue-completed"],
  conversations: ["parent-conversation", "frozen-parent", "branch-one", "branch-two", "branch-reply"],
  resources: ["directory-resource", "url-compose", "url-resource"],
  memories: ["memory-editor", "saved-memory", "reused-memory"],
  schedules: ["time-picker", "weekday-picker", "schedule-clock", "scheduled-issue"],
  changes: ["read-only-diff", "review-confirmed"],
} as const;

describe("feature tour illustrations", () => {
  it.each(Object.keys(scenes) as (keyof typeof scenes)[])("renders the distinct %s scene with localized copy and three cursor actions", (tour) => {
    const { container } = render(FeatureTourAnimation, { tour, title: "Localized title", steps });
    const root = container.querySelector(`[data-feature-tour-animation="${tour}"]`)!;
    expect(root).toHaveAttribute("aria-hidden", "true");
    expect(root).toHaveAttribute("data-paused", "false");
    expect(root.querySelector(".stage")).toHaveAttribute("viewBox", "0 0 720 390");
    expect(root.querySelectorAll("[data-tour-scene]")).toHaveLength(1);
    expect(root.querySelector(`[data-tour-scene="${tour}"]`)).not.toBeNull();
    for (const landmark of scenes[tour]) {
      expect(root.querySelector(`[data-tour-demo="${landmark}"]`)).not.toBeNull();
    }
    expect(root).toHaveTextContent("Localized title");
    for (const text of steps) expect(root).toHaveTextContent(text);
    expect(root.querySelectorAll("[data-tour-cursor]")).toHaveLength(1);
    expect([...root.querySelectorAll("[data-tour-click]")].map((node) => node.getAttribute("data-tour-click"))).toEqual(["one", "two", "three"]);
    expect(root.querySelector("button, input, select, textarea, a, [tabindex], img, iframe")).toBeNull();
  });

  it("freezes and resumes the same scene without replacing its animation timeline", async () => {
    const { container, rerender } = render(FeatureTourAnimation, { tour: "issues", title: "Tasks", steps });
    const stage = container.querySelector(".stage");
    await rerender({ tour: "issues", title: "Tasks", steps, paused: true });
    expect(container.firstElementChild).toHaveAttribute("data-paused", "true");
    expect(container.querySelector(".stage")).toBe(stage);
    await rerender({ tour: "issues", title: "Tasks", steps, paused: false });
    expect(container.firstElementChild).toHaveAttribute("data-paused", "false");
    expect(container.querySelector(".stage")).toBe(stage);
  });

  it("resets the timeline only when the feature changes and updates translated copy", async () => {
    const { container, rerender } = render(FeatureTourAnimation, { tour: "issues", title: "Tasks", steps });
    const oldStage = container.querySelector(".stage");
    const translated = ["Uno", "Dos", "Tres"] as const;
    await rerender({ tour: "resources", title: "Recursos", steps: translated, paused: false });
    expect(container.querySelector(".stage")).not.toBe(oldStage);
    expect(container.querySelector('[data-tour-scene="issues"]')).toBeNull();
    expect(container.firstElementChild).toHaveAttribute("data-feature-tour-animation", "resources");
    for (const text of translated) expect(container).toHaveTextContent(text);
  });

  it("requires an explicit per-illustration opt-in to override reduced motion", async () => {
    const { container, rerender } = render(FeatureTourAnimation, { tour: "issues", title: "Tasks", steps });
    expect(container.firstElementChild).not.toHaveAttribute("data-allow-motion");
    await rerender({ tour: "issues", title: "Tasks", steps, allowMotion: true });
    expect(container.firstElementChild).toHaveAttribute("data-allow-motion", "true");
    await rerender({ tour: "issues", title: "Tasks", steps, allowMotion: false });
    expect(container.firstElementChild).not.toHaveAttribute("data-allow-motion");
  });

  it("uses three equally sized conversation nodes with connectors anchored to their geometry", () => {
    const { container } = render(FeatureTourAnimation, { tour: "conversations", title: "Branches", steps });
    expect([...container.querySelectorAll("rect[data-tour-node]")].map((rect) => ({
      node: rect.getAttribute("data-tour-node"),
      x: Number(rect.getAttribute("x")),
      y: Number(rect.getAttribute("y")),
      width: Number(rect.getAttribute("width")),
      height: Number(rect.getAttribute("height")),
    }))).toEqual([
      { node: "parent", x: 38, y: 145, width: 244, height: 148 },
      { node: "continuation", x: 406, y: 68, width: 244, height: 148 },
      { node: "new-route", x: 406, y: 234, width: 244, height: 148 },
    ]);
    expect(container.querySelector('[data-tour-demo="branch-connections"] path')).toHaveAttribute(
      "d", "M282 207h43c26 0 26-65 52-65h29 M325 207c26 0 26 101 52 101h29",
    );
  });

  it("freezes only the shared prefix, preserves all four original messages in the full child, and starts a genuinely new route", () => {
    const { container } = render(FeatureTourAnimation, { tour: "conversations", title: "Branches", steps });
    const parent = container.querySelector('[data-tour-demo="parent-conversation"]')!;
    const fullChild = container.querySelector('[data-tour-demo="branch-one"]')!;
    const newChild = container.querySelector('[data-tour-demo="branch-two"]')!;
    const messageIds = (node: Element) => [...node.querySelectorAll("[data-tour-message]")].map((message) => message.getAttribute("data-tour-message"));
    expect(messageIds(fullChild)).toEqual(["prefix-1", "prefix-2", "original-3", "original-4"]);
    expect(messageIds(newChild)).toEqual(["prefix-1", "prefix-2", "new-route-3"]);
    const tail = parent.querySelector('[data-tour-demo="parent-continuation"]')!;
    expect(tail).toHaveClass("before-two");
    expect(tail.firstElementChild).toHaveClass("after-one");
    expect(messageIds(tail)).toEqual(["original-3", "original-4"]);
    for (const prefix of parent.querySelectorAll('[data-tour-message^="prefix-"]')) {
      expect(prefix.closest(".before-two")).toBeNull();
    }
    // Shared snippets must preserve the original message geometry and styling,
    // not replace the complete history with an explanatory caption.
    const normalizedMessage = (node: Element, message: string) => {
      const panel = node.querySelector("rect[data-tour-node]")!;
      return [...node.querySelectorAll(`[data-tour-message="${message}"] rect`)].map((rect) => ({
        x: Number(rect.getAttribute("x")) - Number(panel.getAttribute("x")),
        y: Number(rect.getAttribute("y")) - Number(panel.getAttribute("y")),
        width: rect.getAttribute("width"), height: rect.getAttribute("height"),
        radius: rect.getAttribute("rx"), className: rect.getAttribute("class"),
      }));
    };
    for (const id of ["prefix-1", "prefix-2", "original-3", "original-4"]) {
      expect(normalizedMessage(fullChild, id)).toEqual(normalizedMessage(parent, id));
    }
    for (const id of ["prefix-1", "prefix-2"]) {
      expect(normalizedMessage(newChild, id)).toEqual(normalizedMessage(parent, id));
    }
    expect(parent.querySelector('[data-tour-demo="frozen-parent"]')).toHaveClass("after-two");
    const caption = container.querySelector('[data-tour-demo="frozen-prefix-caption"]')!;
    expect(caption).toHaveClass("after-two");
    expect(caption).toHaveTextContent(steps[1]);
    expect(parent.contains(caption)).toBe(false);
    expect(fullChild).not.toHaveTextContent(steps[1]);
    expect(newChild.querySelector('[data-tour-demo="branch-reply"]')).toHaveClass("after-three");
    expect(newChild.querySelector('[data-tour-message="new-route-3"]')).toHaveTextContent(steps[2]);
    const lockPath = "M7 10V7a5 5 0 0 1 10 0v3 M5 10h14v11H5z M12 14v3";
    for (const child of [fullChild, newChild]) {
      expect(child.querySelectorAll("[data-tour-composer]")).toHaveLength(1);
      expect(child.querySelectorAll("[data-tour-send]")).toHaveLength(1);
      expect([...child.querySelectorAll("path")].some((path) => path.getAttribute("d") === lockPath)).toBe(false);
    }
  });

  it("clicks existing reply, fork, and new-route send targets at the three actual cursor stops", () => {
    const { container } = render(FeatureTourAnimation, { tour: "conversations", title: "Branches", steps });
    const stage = container.querySelector<SVGSVGElement>(".stage")!;
    const actions = ["reply", "fork", "new-route-send"];
    actions.forEach((action, index) => {
      const target = container.querySelector(`[data-tour-action="${action}"]`)!;
      const x = parseFloat(stage.style.getPropertyValue(`--x${index + 1}`));
      const y = parseFloat(stage.style.getPropertyValue(`--y${index + 1}`));
      const left = Number(target.getAttribute("x"));
      const top = Number(target.getAttribute("y"));
      expect(x).toBeGreaterThanOrEqual(left);
      expect(x).toBeLessThanOrEqual(left + Number(target.getAttribute("width")));
      expect(y).toBeGreaterThanOrEqual(top);
      expect(y).toBeLessThanOrEqual(top + Number(target.getAttribute("height")));
      if (index < 2) expect(target.closest(".before-two")).not.toBeNull();
      else expect(target.closest(".after-two")).not.toBeNull();
    });
    expect(container.querySelector('[data-tour-action="reply"]')!.closest(".after-one")).toBeNull();
    expect(container.querySelector('[data-tour-action="fork"]')!.closest(".after-one")).not.toBeNull();
  });

  it("is entirely local and owns no timers, animation frames, or requests on mount/update/unmount", async () => {
    const fetchSpy = vi.spyOn(globalThis, "fetch");
    const intervalSpy = vi.spyOn(globalThis, "setInterval");
    const rafSpy = vi.spyOn(globalThis, "requestAnimationFrame");
    const { rerender, unmount } = render(FeatureTourAnimation, { tour: "memories", title: "Memory", steps });
    await rerender({ tour: "changes", title: "Diff", steps, paused: true });
    unmount();
    expect(fetchSpy).not.toHaveBeenCalled();
    expect(intervalSpy).not.toHaveBeenCalled();
    expect(rafSpy).not.toHaveBeenCalled();
  });

  it("declares a full-animation pause and a motion-free completed illustration", () => {
    expect(source).toContain('--tour-duration: 11.2s');
    expect(source).toMatch(/\.tour-visual\[data-paused="true"\][^{]*\*\s*\{\s*animation-play-state:\s*paused !important/s);
    expect(source).toMatch(/@media\s*\(prefers-reduced-motion:\s*reduce\)[\s\S]*animation:\s*none !important;\s*transition:\s*none !important;/);
    expect(source).toMatch(/@media\s*\(prefers-reduced-motion:\s*reduce\)[\s\S]*\.cursor-presence\s*\{\s*display:\s*none;/);
    expect(source).toMatch(/\.before-one\s*\{\s*opacity:\s*0;/);
    expect(source).toMatch(/\.before-two\s*\{\s*opacity:\s*0;/);
    expect(source).toMatch(/\.before-three\s*\{\s*opacity:\s*0;/);
    expect(source).not.toMatch(/\b(?:setTimeout|setInterval|requestAnimationFrame|fetch)\s*\(/);
    expect(source.split("<style>")[1]).not.toMatch(/#[0-9a-f]{3,8}\b/i);
  });

  it("keeps opted-in tutorials at their full looping duration without forcing a paused timeline to run", () => {
    const playback = /\.tour-visual\[data-allow-motion="true"\]\s+\*\s*\{([^}]+)\}/.exec(source)?.[1];
    expect(playback).toMatch(/animation-duration:\s*var\(--tour-duration\)\s*!important/);
    expect(playback).toMatch(/animation-iteration-count:\s*infinite\s*!important/);
    expect(playback).not.toContain("animation-play-state");
  });
});

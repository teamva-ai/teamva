import { describe, expect, it } from "vitest";
import { createConversationZoom, type ConversationViewport } from "./conversationZoom";

function setup(scale = 0.7) {
  let viewport: ConversationViewport = { x: 40, y: 80, scale };
  let next = 0;
  const frames = new Map<number, FrameRequestCallback>();
  const zoom = createConversationZoom({
    getViewport: () => viewport,
    setViewport: (value) => { viewport = value; },
    requestFrame: (callback) => { frames.set(++next, callback); return next; },
    cancelFrame: (id) => { frames.delete(id); }
  });
  const frame = () => { const queued = [...frames.values()]; frames.clear(); queued.forEach((callback) => callback(0)); };
  const flush = () => { let count = 0; while (frames.size && count++ < 200) frame(); expect(frames.size).toBe(0); };
  return { zoom, frames, frame, flush, view: () => viewport };
}
const wheel = { deltaY: -120, deltaMode: 0, height: 600, x: 100, y: 130, ctrlKey: false };

describe("conversation logarithmic wheel zoom regression", () => {
  it("uses elapsed time rather than refresh rate and bounds a long background frame", () => {
    function simulate(hz: number) {
      let view = { x: 40, y: 80, scale: 0.7 };
      let next: FrameRequestCallback | undefined;
      const zoom = createConversationZoom({ getViewport: () => view, setViewport: (value) => { view = value; },
        requestFrame: (callback) => { next = callback; return 1; }, cancelFrame: () => { next = undefined; } });
      for (let i = 0; i < 100; i++) zoom.wheel(wheel);
      const frame = (time: number) => { const callback = next; next = undefined; callback?.(time); };
      frame(0);
      for (let index = 1; index <= hz / 10; index++) frame(index * 1000 / hz);
      const sampled = view.scale;
      frame(30_000);
      expect(view.scale / sampled).toBeLessThanOrEqual(1.04 + 1e-10);
      zoom.destroy();
      return sampled;
    }
    const values = [30, 60, 120].map(simulate);
    expect(Math.max(...values) - Math.min(...values)).toBeLessThan(0.004);
  });
  it("does not turn 70% into 200% on one large wheel event", () => {
    const state = setup();
    state.zoom.wheel({ ...wheel, deltaY: -10000 });
    expect(state.view().scale).toBe(0.7);
    state.flush();
    expect(state.view().scale).toBeGreaterThan(0.7);
    expect(state.view().scale).toBeLessThanOrEqual(0.7 * 1.1 + 1e-10);
  });

  it("caps a same-frame wheel burst and bounds every RAF catch-up step", () => {
    const state = setup();
    for (let i = 0; i < 100; i++) state.zoom.wheel({ ...wheel, deltaY: -10000 });
    expect(state.frames.size).toBe(1);
    let previous = state.view().scale;
    for (let i = 0; state.frames.size && i < 200; i++) {
      state.frame();
      expect(state.view().scale / previous).toBeLessThanOrEqual(1.04 + 1e-10);
      previous = state.view().scale;
    }
    expect(state.frames.size).toBe(0);
    expect(state.view().scale).toBeLessThanOrEqual(0.7 * 1.2 + 1e-10);
  });

  it("anchors the world point under the cursor throughout animation", () => {
    const state = setup();
    const before = state.view();
    state.zoom.wheel(wheel);
    while (state.frames.size) {
      state.frame();
      const after = state.view();
      expect((wheel.x - after.x) / after.scale).toBeCloseTo((wheel.x - before.x) / before.scale, 10);
      expect((wheel.y - after.y) / after.scale).toBeCloseTo((wheel.y - before.y) / before.scale, 10);
    }
  });

  it.each([{ deltaY: 1, deltaMode: 1, pixels: 16 }, { deltaY: 0.05, deltaMode: 2, pixels: 30 }])(
    "normalizes deltaMode $deltaMode into pixels", ({ deltaY, deltaMode, pixels }) => {
      const a = setup(), b = setup();
      a.zoom.wheel({ ...wheel, deltaY, deltaMode });
      b.zoom.wheel({ ...wheel, deltaY: pixels });
      a.flush(); b.flush();
      expect(a.view()).toEqual(b.view());
    }
  );

  it("handles ctrl pinch deliberately but still caps a large gesture", () => {
    const normal = setup(), pinch = setup();
    normal.zoom.wheel({ ...wheel, deltaY: -4 });
    pinch.zoom.wheel({ ...wheel, deltaY: -4, ctrlKey: true });
    normal.flush(); pinch.flush();
    expect(pinch.view().scale).toBeGreaterThan(normal.view().scale);
    pinch.zoom.wheel({ ...wheel, deltaY: -10000, ctrlKey: true });
    const before = pinch.view().scale;
    pinch.flush();
    expect(pinch.view().scale).toBeLessThanOrEqual(before * 1.1 + 1e-10);
  });

  it.each(["cancel", "destroy"] as const)("%s cancels queued RAF and cannot move a reset/panned/switched view", (action) => {
    const state = setup();
    state.zoom.wheel(wheel);
    state.zoom[action]();
    expect(state.frames.size).toBe(0);
    const before = state.view();
    state.frame();
    expect(state.view()).toEqual(before);
    if (action === "destroy") { state.zoom.wheel(wheel); expect(state.frames.size).toBe(0); }
    else { state.zoom.wheel(wheel); expect(state.frames.size).toBe(1); }
  });

  it("ignores invalid/zero deltas and respects the scale limits", () => {
    const state = setup(2);
    for (const deltaY of [0, NaN, Infinity, -Infinity]) state.zoom.wheel({ ...wheel, deltaY });
    expect(state.frames.size).toBe(0);
    state.zoom.wheel(wheel); state.flush();
    expect(state.view().scale).toBe(2);
    const minimum = setup(0.2);
    minimum.zoom.wheel({ ...wheel, deltaY: 10000 }); minimum.flush();
    expect(minimum.view().scale).toBe(0.2);
  });
});

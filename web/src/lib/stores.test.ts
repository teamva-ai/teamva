import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const key = "teamva-selection";
const loadStores = () => import("./stores.svelte");

beforeEach(() => {
  vi.resetModules();
  window.localStorage.clear();
  window.sessionStorage.clear();
});

afterEach(() => {
  vi.restoreAllMocks();
  window.localStorage.clear();
  window.sessionStorage.clear();
  vi.resetModules();
});

describe("window-scoped project selection", () => {
  it("starts without an explicit project in a new window", async () => {
    expect((await loadStores()).selection).toEqual({ computerId: "", projectId: "" });
  });

  it("uses the shared computer but ignores a legacy localStorage project", async () => {
    window.localStorage.setItem(key, JSON.stringify({ computerId: "shared-computer", projectId: "old-project" }));
    expect((await loadStores()).selection).toEqual({ computerId: "shared-computer", projectId: "" });
  });

  it("prefers this window's computer and project to shared selection", async () => {
    window.localStorage.setItem(key, JSON.stringify({ computerId: "shared-computer", projectId: "old-project" }));
    window.sessionStorage.setItem(key, JSON.stringify({ computerId: "window-computer", projectId: "window-project" }));
    expect((await loadStores()).selection).toEqual({ computerId: "window-computer", projectId: "window-project" });
  });

  it("persists project selection only for the current window and retains it on reload", async () => {
    const store = await loadStores();
    store.setComputer("computer");
    store.setProject("explicit-project");
    expect(JSON.parse(window.sessionStorage.getItem(key)!)).toEqual({ computerId: "computer", projectId: "explicit-project" });
    expect(JSON.parse(window.localStorage.getItem(key)!)).toEqual({ computerId: "computer" });
    vi.resetModules();
    expect((await loadStores()).selection).toEqual({ computerId: "computer", projectId: "explicit-project" });
  });

  it("isolates projects between windows sharing localStorage", async () => {
    const firstWindowStorage = window.sessionStorage;
    const firstWindow = await loadStores();
    firstWindow.setComputer("computer");
    firstWindow.setProject("first-project");

    const values = new Map<string, string>();
    const secondWindowStorage: Storage = {
      get length() { return values.size; },
      clear: () => values.clear(),
      getItem: (name) => values.get(name) ?? null,
      key: (index) => [...values.keys()][index] ?? null,
      removeItem: (name) => { values.delete(name); },
      setItem: (name, value) => { values.set(name, value); }
    };
    const windowStorage = vi.spyOn(window, "sessionStorage", "get").mockReturnValue(secondWindowStorage);
    vi.resetModules();
    const secondWindow = await loadStores();
    expect(secondWindow.selection).toEqual({ computerId: "computer", projectId: "" });
    secondWindow.setProject("second-project");
    expect(firstWindow.selection.projectId).toBe("first-project");

    windowStorage.mockReturnValue(firstWindowStorage);
    vi.resetModules();
    expect((await loadStores()).selection.projectId).toBe("first-project");
  });

  it("clears the project only when actually switching computers", async () => {
    const store = await loadStores();
    store.setComputer("first");
    store.setProject("project");
    store.setComputer("first");
    expect(store.selection.projectId).toBe("project");
    store.setComputer("second");
    expect(store.selection).toEqual({ computerId: "second", projectId: "" });
    expect(JSON.parse(window.sessionStorage.getItem(key)!)).toEqual({ computerId: "second", projectId: "" });
    expect(JSON.parse(window.localStorage.getItem(key)!)).toEqual({ computerId: "second" });
  });

  it("ignores malformed or non-string stored values", async () => {
    window.sessionStorage.setItem(key, "not json");
    window.localStorage.setItem(key, JSON.stringify({ computerId: 42, projectId: "legacy" }));
    expect((await loadStores()).selection).toEqual({ computerId: "", projectId: "" });
    window.sessionStorage.setItem(key, JSON.stringify({ computerId: null, projectId: {} }));
    window.localStorage.setItem(key, JSON.stringify({ computerId: "computer" }));
    vi.resetModules();
    expect((await loadStores()).selection).toEqual({ computerId: "computer", projectId: "" });
  });

  it("keeps selection usable when storage access throws", async () => {
    vi.spyOn(window, "sessionStorage", "get").mockImplementation(() => { throw new Error("blocked"); });
    vi.spyOn(window, "localStorage", "get").mockImplementation(() => { throw new Error("blocked"); });
    const store = await loadStores();
    expect(store.selection).toEqual({ computerId: "", projectId: "" });
    expect(() => {
      store.setComputer("computer");
      store.setProject("project");
    }).not.toThrow();
    expect(store.selection).toEqual({ computerId: "computer", projectId: "project" });
  });

  it("still persists the shared computer when session storage is unavailable", async () => {
    window.localStorage.setItem(key, JSON.stringify({ computerId: "shared" }));
    vi.spyOn(window, "sessionStorage", "get").mockImplementation(() => { throw new Error("blocked"); });
    const store = await loadStores();
    expect(store.selection.computerId).toBe("shared");
    store.setComputer("new-computer");
    store.setProject("project");
    expect(JSON.parse(window.localStorage.getItem(key)!)).toEqual({ computerId: "new-computer" });
  });

  it("still reloads window selection when localStorage is unavailable", async () => {
    vi.spyOn(window, "localStorage", "get").mockImplementation(() => { throw new Error("blocked"); });
    const store = await loadStores();
    store.setComputer("computer");
    store.setProject("project");
    vi.resetModules();
    expect((await loadStores()).selection).toEqual({ computerId: "computer", projectId: "project" });
  });

  it("keeps in-memory selection when storage writes throw", async () => {
    const store = await loadStores();
    vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => { throw new Error("quota exceeded"); });
    expect(() => {
      store.setComputer("computer");
      store.setProject("project");
    }).not.toThrow();
    expect(store.selection).toEqual({ computerId: "computer", projectId: "project" });
  });
});

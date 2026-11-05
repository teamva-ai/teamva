import { cleanup, fireEvent, render, screen } from "@testing-library/svelte";
import { afterEach, describe, expect, it, vi } from "vitest";
import UnsavedChangesTestHost from "../test/UnsavedChangesTestHost.svelte";
import { location, navigate, registerNavigationGuard, setQueryParam } from "./router.svelte";
import { confirmProjectChange } from "./unsavedChanges.svelte";

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  window.history.replaceState({}, "", "/");
  window.dispatchEvent(new PopStateEvent("popstate"));
});

describe("unsaved change ownership", () => {
  it("protects routes, query links, history and same-route project changes with live dirty state", async () => {
    const confirm = vi.spyOn(window, "confirm").mockReturnValue(false);
    const discard = vi.fn();
    const view = render(UnsavedChangesTestHost, { dirty: false, onDiscard: discard });
    navigate("/memories");
    expect(confirm).not.toHaveBeenCalled();
    await view.rerender({ dirty: true, onDiscard: discard });
    navigate("/agents");
    expect(location.path).toBe("/memories");
    setQueryParam("memory", "another");
    expect(location.search).toBe("");
    window.history.replaceState({}, "", "/workbench");
    window.dispatchEvent(new PopStateEvent("popstate"));
    expect(window.location.pathname).toBe("/memories");
    expect(confirmProjectChange("different-project")).toBe(false);
    expect(confirm).toHaveBeenCalledTimes(4);
    expect(discard).not.toHaveBeenCalled();
    confirm.mockReturnValue(true);
    expect(confirmProjectChange("different-project")).toBe(true);
    expect(discard).toHaveBeenCalledOnce();
    navigate("/agents");
    expect(location.path).toBe("/agents");
  });

  it("requests the browser unload prompt only while dirty and unregisters on unmount", async () => {
    const view = render(UnsavedChangesTestHost, { dirty: true });
    const dirtyUnload = new Event("beforeunload", { cancelable: true });
    window.dispatchEvent(dirtyUnload);
    expect(dirtyUnload.defaultPrevented).toBe(true);
    await view.rerender({ dirty: false });
    const cleanUnload = new Event("beforeunload", { cancelable: true });
    window.dispatchEvent(cleanUnload);
    expect(cleanUnload.defaultPrevented).toBe(false);
    await view.rerender({ dirty: true });
    view.unmount();
    const removedUnload = new Event("beforeunload", { cancelable: true });
    window.dispatchEvent(removedUnload);
    expect(removedUnload.defaultPrevented).toBe(false);
    const confirm = vi.spyOn(window, "confirm").mockReturnValue(false);
    expect(confirmProjectChange("another")).toBe(true);
    expect(confirm).not.toHaveBeenCalled();
  });

  it("allows explicitly confirmed local transitions without bypassing another editor", async () => {
    const confirm = vi.spyOn(window, "confirm").mockReturnValue(false);
    render(UnsavedChangesTestHost, { dirty: true });
    const otherGuard = vi.fn(() => false);
    const remove = registerNavigationGuard(otherGuard);
    try {
      await fireEvent.click(screen.getByRole("button", { name: "Confirmed change" }));
      expect(location.path).not.toBe("/confirmed");
      expect(otherGuard).toHaveBeenCalledOnce();
      expect(confirm).not.toHaveBeenCalled();
    } finally {
      remove();
    }
    await fireEvent.click(screen.getByRole("button", { name: "Confirmed change" }));
    expect(location.path).toBe("/confirmed");
    expect(confirmProjectChange("another")).toBe(false);
    expect(confirm).toHaveBeenCalledOnce();
  });

  it("does not discard an accepted draft if another owner cancels the transition", () => {
    vi.spyOn(window, "confirm").mockReturnValue(true);
    const discard = vi.fn();
    render(UnsavedChangesTestHost, { dirty: true, onDiscard: discard });
    const remove = registerNavigationGuard(() => false);
    try {
      expect(confirmProjectChange("other")).toBe(false);
      expect(discard).not.toHaveBeenCalled();
    } finally {
      remove();
    }
    expect(confirmProjectChange("other")).toBe(true);
    expect(discard).toHaveBeenCalledOnce();
  });
});

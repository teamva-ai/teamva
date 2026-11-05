import { QueryObserver } from "@tanstack/svelte-query";
import { afterEach, describe, expect, it, vi } from "vitest";
import { queryClient } from "./query";

let unsubscribe: (() => void) | undefined;

afterEach(() => {
  unsubscribe?.();
  unsubscribe = undefined;
  queryClient.clear();
  queryClient.unmount();
  vi.restoreAllMocks();
  vi.useRealTimers();
});

describe("query polling visibility defaults", () => {
  it("pauses intervals while hidden and resumes without refetchOnWindowFocus", async () => {
    vi.useFakeTimers();
    let hidden = true;
    vi.spyOn(document, "visibilityState", "get").mockImplementation(() => hidden ? "hidden" : "visible");
    queryClient.mount();
    const queryFn = vi.fn(async () => ({ items: [] }));
    const observer = new QueryObserver(queryClient, {
      queryKey: ["visibility-polling"], queryFn, refetchInterval: 5_000
    });
    unsubscribe = observer.subscribe(() => {});
    await vi.advanceTimersByTimeAsync(0);
    expect(queryFn).toHaveBeenCalledTimes(1);

    await vi.advanceTimersByTimeAsync(10_000);
    expect(queryFn).toHaveBeenCalledTimes(1);
    hidden = false;
    document.dispatchEvent(new Event("visibilitychange"));
    await vi.advanceTimersByTimeAsync(5_000);
    expect(queryFn).toHaveBeenCalledTimes(2);

    hidden = true;
    document.dispatchEvent(new Event("visibilitychange"));
    await vi.advanceTimersByTimeAsync(10_000);
    expect(queryFn).toHaveBeenCalledTimes(2);
    hidden = false;
    document.dispatchEvent(new Event("visibilitychange"));
    await vi.advanceTimersByTimeAsync(5_000);
    expect(queryFn).toHaveBeenCalledTimes(3);
  });
});

import { cleanup, render } from "@testing-library/svelte";
import { tick } from "svelte";
import { afterEach, expect, it, vi } from "vitest";
import { queryClient } from "../lib/query";
import { navigate } from "../lib/router.svelte";
import IssuesTestHost from "./IssuesTestHost.svelte";

afterEach(() => {
  cleanup();
  queryClient.clear();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  vi.useRealTimers();
  navigate("/", { replace: true });
});

it("resumes the task list after mounting in a hidden tab", async () => {
  vi.useFakeTimers();
  let hidden = true;
  vi.spyOn(document, "hidden", "get").mockImplementation(() => hidden);
  vi.spyOn(document, "visibilityState", "get").mockImplementation(() => hidden ? "hidden" : "visible");
  const listRequests = vi.fn();
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
    const path = new URL(String(input), "http://teamva.test").pathname;
    if (path.endsWith("/projects/project-test/issues")) listRequests();
    const data = path.endsWith("/git/repositories")
      ? { repositories: [], truncated: false, warnings: [], generation: 1 }
      : { items: [], total: 0 };
    return new Response(JSON.stringify({ data }), { headers: { "Content-Type": "application/json" } });
  }));
  navigate("/issues", { replace: true });
  render(IssuesTestHost);
  await tick();
  await vi.advanceTimersByTimeAsync(0);
  expect(listRequests).toHaveBeenCalledTimes(1);
  await vi.advanceTimersByTimeAsync(6_000);
  expect(listRequests).toHaveBeenCalledTimes(1);

  hidden = false;
  document.dispatchEvent(new Event("visibilitychange"));
  await vi.advanceTimersByTimeAsync(2_000);
  expect(listRequests).toHaveBeenCalledTimes(2);
  hidden = true;
  document.dispatchEvent(new Event("visibilitychange"));
  await vi.advanceTimersByTimeAsync(6_000);
  expect(listRequests).toHaveBeenCalledTimes(2);
});

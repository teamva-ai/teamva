import { afterEach, describe, expect, it } from "vitest";
import { location, matchPath, navigate } from "./router.svelte";

describe("browser router", () => {
  afterEach(() => {
    window.history.replaceState({}, "", "/");
    window.dispatchEvent(new PopStateEvent("popstate"));
  });
  it("rejects malformed URL encoding instead of crashing rendering", () => {
    expect(matchPath("/issues/:id", "/issues/%ZZ")).toBeNull();
    expect(matchPath("/issues/:id", "/issues/a%20b")).toEqual({ id: "a b" });
  });
  it("can replace the bootstrap entry without trapping the back button", () => {
    const before = window.history.length;
    navigate("/workbench", { replace: true });
    expect(location.path).toBe("/workbench");
    expect(window.history.length).toBe(before);
  });
});

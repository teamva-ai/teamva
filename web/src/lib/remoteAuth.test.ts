import { afterEach, describe, expect, it } from "vitest";
import { remoteReturnPath, requireRemoteLogin, remoteAuth } from "./remoteAuth.svelte";
import { navigate } from "./router.svelte";

afterEach(() => { navigate("/"); remoteAuth.active = false; });

describe("remote login navigation", () => {
  it("keeps application paths, search and hash", () => {
    expect(remoteReturnPath("/settings?tab=network#access")).toBe("/settings?tab=network#access");
  });
  it.each([null, "https://evil.test", "//evil.test", "/%2fevil.test", "/\\evil.test", "/%5cevil.test", "/%0aevil", "/%", "/remote/login", "/v1/auth/local-session", "/desktop/bootstrap", "/foo/../remote/login"])("rejects unsafe return destination %s", (path) => {
    expect(remoteReturnPath(path)).toBe("/workbench");
  });
  it("replaces the current route with login and does not nest redirects", () => {
    navigate("/settings?tab=network");
    requireRemoteLogin();
    expect(window.location.pathname).toBe("/remote/login");
    expect(new URLSearchParams(window.location.search).get("next")).toBe("/settings?tab=network");
    const first = window.location.href;
    requireRemoteLogin();
    expect(window.location.href).toBe(first);
  });
});

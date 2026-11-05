import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { api, APIError, apiBaseURL } from "./api";
import { hydratePreferences, session, preferences, setLocale, setTheme } from "./stores.svelte";
import { t } from "./i18n.svelte";
import { navigate } from "./router.svelte";
import { remoteAuth } from "./remoteAuth.svelte";

describe("API client", () => {
  beforeEach(() => {
    localStorage.clear();
    session.csrf = "session-csrf";
  });

  afterEach(() => {
    vi.unstubAllGlobals();
    session.csrf = "";
    remoteAuth.active = false;
    navigate("/");
  });

  it("routes revoked remote sessions to our password page with a return destination", async () => {
    navigate("/settings?tab=network");
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify({ error: { code: "remote_auth_required" } }), {
      status: 401, headers: { "Content-Type": "application/json", "X-Teamva-Remote": "true" }
    })));
    await expect(api("/auth/remote-session")).rejects.toMatchObject({ code: "remote_auth_required" });
    expect(session.csrf).toBe("");
    expect(remoteAuth.active).toBe(true);
    expect(window.location.pathname).toBe("/remote/login");
    expect(new URLSearchParams(window.location.search).get("next")).toBe("/settings?tab=network");
  });

  it("preserves an explicit POST method when the request has no body and sends the CSRF header", async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({
      data: { token: "one-time-token" },
      meta: { request_id: "req-token" }
    }), { status: 201, headers: { "Content-Type": "application/json" } }));
    vi.stubGlobal("fetch", fetchMock);

    await api("/projects", { method: "POST" });

    expect(fetchMock).toHaveBeenCalledWith(
      `${apiBaseURL}/projects`,
      expect.objectContaining({
        method: "POST",
        credentials: "include",
        headers: expect.objectContaining({ "X-CSRF-Token": "session-csrf" })
      })
    );
  });

  it("never attaches a Bearer token to any request", async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({
      data: { id: "usr" },
      meta: { request_id: "req-me" }
    }), { status: 200, headers: { "Content-Type": "application/json" } }));
    vi.stubGlobal("fetch", fetchMock);

    await api("/me");

    const [, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    const headers = (init.headers ?? {}) as Record<string, string>;
    expect(headers.Authorization ?? headers.authorization).toBeUndefined();
  });

  it("parses Retry-After into the error and clears the session on 401", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify({
      error: { code: "invalid_session", message: "会话已过期", retryable: false },
      meta: { request_id: "req-expired" }
    }), { status: 401, headers: { "Content-Type": "application/json", "Retry-After": "900" } })));

    await expect(api("/me")).rejects.toMatchObject({
      code: "invalid_session",
      requestId: "req-expired",
      status: 401,
      retryAfter: 900
    });
    expect(session.csrf).toBe("");
  });

  it("always uses the same-origin local API", () => {
    expect(apiBaseURL).toBe("/v1");
  });

  it("restores stale CSRF once without duplicating an admitted mutation", async () => {
    let writes = 0;
    const calls: string[] = [];
    vi.stubGlobal("fetch", vi.fn(async (url: string, init: RequestInit) => {
      calls.push(url);
      if (url.endsWith("/auth/csrf")) {
        return new Response(JSON.stringify({ data: { csrf_token: "fresh-csrf" } }), { status: 200 });
      }
      if ((init.headers as Record<string, string>)["X-CSRF-Token"] !== "fresh-csrf") {
        return new Response(JSON.stringify({ error: { code: "csrf_failed" } }), { status: 403 });
      }
      writes++;
      return new Response(JSON.stringify({ data: { id: "project" } }), { status: 201 });
    }));
    await expect(api("/projects", { body: { name: "local" } })).resolves.toEqual({ id: "project" });
    expect(writes).toBe(1);
    expect(calls).toEqual(["/v1/projects", "/v1/auth/csrf", "/v1/projects"]);
  });

  it("does not retry an origin rejection or repeatedly retry a CSRF rejection", async () => {
    const fetchMock = vi.fn(async (url: string) => {
      if (url.endsWith("/auth/csrf")) return new Response(JSON.stringify({ data: { csrf_token: "fresh" } }), { status: 200 });
      return new Response(JSON.stringify({ error: { code: "csrf_failed" } }), { status: 403 });
    });
    vi.stubGlobal("fetch", fetchMock);
    await expect(api("/projects", { body: {} })).rejects.toMatchObject({ code: "csrf_failed" });
    expect(fetchMock).toHaveBeenCalledTimes(3);
    fetchMock.mockClear();
    fetchMock.mockImplementation(async () => new Response(JSON.stringify({ error: { code: "csrf_origin" } }), { status: 403 }));
    await expect(api("/projects", { body: {} })).rejects.toMatchObject({ code: "csrf_origin" });
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  it("parses Retry-After on 429 rate limits", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify({
      error: { code: "rate_limited", message: "操作过于频繁", retryable: false },
      meta: { request_id: "req-429" }
    }), { status: 429, headers: { "Content-Type": "application/json", "Retry-After": "60" } })));

    try {
      await api("/projects", { method: "POST", body: {} });
      expect.unreachable();
    } catch (error) {
      expect(error).toBeInstanceOf(APIError);
      expect((error as APIError).retryAfter).toBe(60);
    }
  });

  it("uses the current interface language when an upstream error is not JSON", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response("bad gateway", { status: 502 })));

    await expect(api("/me")).rejects.toMatchObject({
      code: "invalid_response",
      message: t("errors.invalidResponse")
    });
  });

  it("keeps authenticated preferences in memory for server synchronization", () => {
    hydratePreferences({
      user_id: "usr-1", locale: "zh-CN", locale_explicit: false,
      theme: "system", allow_write: false, version: 1
    });
    setLocale("en-US");
    setTheme("dark");
    expect(preferences.locale).toBe("en-US");
    expect(preferences.theme).toBe("dark");
    expect(localStorage.getItem("teamva-preferences")).toBeNull();
    preferences.locale = "";
    preferences.theme = "system";
  });
});

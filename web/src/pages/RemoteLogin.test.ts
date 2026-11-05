import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/svelte";
import { afterEach, describe, expect, it, vi } from "vitest";
import RemoteLogin from "./RemoteLogin.svelte";
import { t } from "../lib/i18n.svelte";

afterEach(() => { cleanup(); vi.unstubAllGlobals(); });

describe("password-only remote login", () => {
  it("labels only the password input and uses matching shared controls without submitting on visibility changes", async () => {
    const fetch = vi.fn().mockImplementation(async () => new Response("{}", { status: 401 }));
    vi.stubGlobal("fetch", fetch);
    render(RemoteLogin);
    expect(screen.queryByText(t("remoteLogin.warning"))).not.toBeInTheDocument();
    const input = screen.getByLabelText(t("remoteLogin.password"), { selector: "input" });
    const show = screen.getByRole("button", { name: t("settings.networkAccessShow") });
    expect(input).toHaveClass("am-input");
    expect(input).toHaveAttribute("data-size", "md");
    expect(show).toHaveClass("am-button");
    expect(show).toHaveAttribute("data-size", "md");
    expect(show).toHaveAttribute("data-icon", "true");
    expect(show).toHaveAttribute("type", "button");
    const label = input.closest(".am-field")?.querySelector("label");
    expect(label).toHaveAttribute("for", input.id);
    expect(input.closest("label")).toBeNull();
    expect(show.closest("label")).toBeNull();
    const submit = vi.fn();
    input.closest("form")!.addEventListener("submit", submit);
    await fireEvent.input(input, { target: { value: "test-only-draft" } });
    await fireEvent.click(show);
    expect(input).toHaveAttribute("type", "text");
    expect(input).toHaveValue("test-only-draft");
    await fireEvent.click(screen.getByRole("button", { name: t("settings.networkAccessHide") }));
    expect(input).toHaveAttribute("type", "password");
    expect(input).toHaveValue("test-only-draft");
    expect(submit).not.toHaveBeenCalled();
    expect(fetch.mock.calls.filter((call) => call[1]?.method === "POST")).toEqual([]);
    expect(screen.getByRole("checkbox", { name: t("remoteLogin.trustDevice") })).not.toBeChecked();
  });

  it("has no username field and reports a wrong password without navigating", async () => {
    const fetch = vi.fn().mockImplementation(async () => new Response(JSON.stringify({ error: { code: "remote_password_invalid" } }), { status: 401 }));
    vi.stubGlobal("fetch", fetch);
    render(RemoteLogin);
    const input = screen.getByLabelText(t("remoteLogin.password"), { selector: "input" });
    expect(document.querySelectorAll('input[type="password"]')).toHaveLength(1);
    expect(document.querySelector('input[name="username"]')).toBeNull();
    expect(screen.getByRole("checkbox", { name: t("remoteLogin.trustDevice") })).not.toBeChecked();
    expect(input).toHaveAttribute("type", "password");
    const submit = screen.getByRole("button", { name: t("remoteLogin.signIn") });
    expect(submit).toBeDisabled();
    await fireEvent.input(input, { target: { value: "test-only-incorrect" } });
    await fireEvent.click(submit);
    await waitFor(() => expect(screen.getByRole("alert")).toHaveTextContent(t("remoteLogin.invalid")));
    const post = fetch.mock.calls.find((call) => call[1].method === "POST")!;
    expect(JSON.parse(post[1].body)).toEqual({ password: "test-only-incorrect", trust_device: false });
    expect(post[1].headers.Authorization).toBeUndefined();
  });
  it("keeps login errors and rate limits on the page", async () => {
    vi.stubGlobal("fetch", vi.fn().mockImplementation(async () => new Response(JSON.stringify({ error: { code: "remote_login_limited" } }), { status: 429, headers: { "Retry-After": "2" } })));
    render(RemoteLogin);
    await fireEvent.input(screen.getByLabelText(t("remoteLogin.password"), { selector: "input" }), { target: { value: "test-only-password" } });
    await fireEvent.click(screen.getByRole("button", { name: t("remoteLogin.signIn") }));
    await waitFor(() => expect(screen.getByRole("alert")).toHaveTextContent(t("remoteLogin.limited")));
  });

  it("sends explicit device trust only when selected and never stores the password locally", async () => {
    const fetch = vi.fn().mockImplementation(async () => new Response(JSON.stringify({ error: { code: "remote_password_invalid" } }), { status: 401 }));
    vi.stubGlobal("fetch", fetch);
    const store = vi.spyOn(Storage.prototype, "setItem");
    try {
      render(RemoteLogin);
      await fireEvent.click(screen.getByRole("checkbox", { name: t("remoteLogin.trustDevice") }));
      await fireEvent.input(screen.getByLabelText(t("remoteLogin.password"), { selector: "input" }), { target: { value: "test-only-private-device" } });
      await fireEvent.click(screen.getByRole("button", { name: t("remoteLogin.signIn") }));
      await waitFor(() => expect(screen.getByRole("alert")).toHaveTextContent(t("remoteLogin.invalid")));
      const post = fetch.mock.calls.find((call) => call[1].method === "POST")!;
      expect(JSON.parse(post[1].body)).toEqual({ password: "test-only-private-device", trust_device: true });
      expect(store).not.toHaveBeenCalled();
    } finally { store.mockRestore(); }
  });
});

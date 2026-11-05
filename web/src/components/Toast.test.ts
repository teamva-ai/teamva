import { render, screen, cleanup } from "@testing-library/svelte";
import { describe, expect, it, vi, afterEach, beforeEach } from "vitest";
import { tick } from "svelte";
import Toast from "./Toast.svelte";
import { toastSuccess, toastError, clearToastsForTest } from "../lib/toast.svelte";

describe("Toast", () => {
  beforeEach(() => {
    clearToastsForTest();
  });
  afterEach(() => {
    vi.useRealTimers();
    clearToastsForTest();
    cleanup();
  });

  it("renders a success toast with the message", async () => {
    render(Toast);
    toastSuccess("记忆已保存");
    await tick();
    expect(screen.getByText("记忆已保存")).toBeTruthy();
  });

  it("auto-dismisses after the duration", async () => {
    vi.useFakeTimers();
    render(Toast);
    toastSuccess("临时消息");
    await tick();
    expect(screen.getByText("临时消息")).toBeTruthy();
    vi.advanceTimersByTime(3500);
    await tick();
    expect(screen.queryByText("临时消息")).toBeNull();
  });

  it("renders error toasts", async () => {
    render(Toast);
    toastError("操作失败");
    await tick();
    expect(screen.getByText("操作失败")).toBeTruthy();
  });
});

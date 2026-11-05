import { afterEach, describe, expect, it, vi } from "vitest";
import { createSettingsAutoSave } from "./settingsAutoSave";

afterEach(() => vi.useRealTimers());
describe("settings serial coalescing queue", () => {
  it("never writes during creation or hydration", async () => {
    const save = vi.fn();
    const queue = createSettingsAutoSave({ save, changed: vi.fn() });
    queue.sync();
    await queue.flush();
    expect(save).not.toHaveBeenCalled();
  });
  it("serializes writes, coalesces later intents and never lets old results remove newer edits", async () => {
    let finish!: () => void;
    const save = vi.fn<(patch: Partial<{ theme: string; locale: string }>) => Promise<void>>()
      .mockImplementationOnce(() => new Promise((resolve) => { finish = resolve; }))
      .mockResolvedValue(undefined);
    const changed = vi.fn();
    const queue = createSettingsAutoSave({ save, changed });
    queue.enqueue({ theme: "dark" });
    queue.enqueue({ theme: "system" });
    queue.enqueue({ theme: "light", locale: "zh-CN" });
    expect(save).toHaveBeenCalledTimes(1);
    finish();
    await queue.flush();
    expect(save.mock.calls).toEqual([[{ theme: "dark" }], [{ theme: "light", locale: "zh-CN" }]]);
    expect(changed).toHaveBeenLastCalledWith({ intent: {}, status: "saved", error: null });
  });
  it("coalesces color input, supports immediate selects and flushes before the debounce expires", async () => {
    vi.useFakeTimers();
    const save = vi.fn(async (_patch: Partial<{ color: string; theme: string }>) => {});
    const queue = createSettingsAutoSave({ save, changed: vi.fn() });
    queue.enqueue({ color: "#123456" }, 200);
    queue.enqueue({ color: "#654321" }, 200);
    expect(save).not.toHaveBeenCalled();
    queue.enqueue({ theme: "dark" });
    await queue.flush();
    expect(save).toHaveBeenCalledExactlyOnceWith({ color: "#654321", theme: "dark" });
    queue.enqueue({ color: "#abcdef" }, 200);
    await queue.flush();
    expect(save).toHaveBeenLastCalledWith({ color: "#abcdef" });
    await vi.runAllTimersAsync();
    expect(save).toHaveBeenCalledTimes(2);
  });
  it("preserves failed intents and newer drafts without implicit retry, including version conflicts", async () => {
    const failure = { status: 409 };
    const save = vi.fn<(patch: Partial<{ theme: string; locale: string }>) => Promise<void>>()
      .mockRejectedValueOnce(failure).mockResolvedValue(undefined);
    const changed = vi.fn();
    const queue = createSettingsAutoSave({ save, changed });
    queue.enqueue({ theme: "dark" });
    await queue.flush();
    queue.sync();
    queue.enqueue({ locale: "fr-FR" });
    await queue.flush();
    expect(save).toHaveBeenCalledTimes(1);
    expect(queue.failed).toBe(true);
    expect(changed).toHaveBeenLastCalledWith({ intent: { theme: "dark", locale: "fr-FR" }, status: "error", error: failure });
    await queue.retry();
    expect(save).toHaveBeenLastCalledWith({ theme: "dark", locale: "fr-FR" });
    expect(queue.pending).toBe(false);
  });
  it("does not duplicate an identical password commit while it is pending", async () => {
    let finish!: () => void;
    const save = vi.fn((_patch: Partial<{ password: string }>) => new Promise<void>((resolve) => { finish = resolve; }));
    const queue = createSettingsAutoSave({ save, changed: vi.fn() });
    queue.enqueue({ password: "test-only-password" });
    queue.enqueue({ password: "test-only-password" });
    finish();
    await queue.flush();
    expect(save).toHaveBeenCalledTimes(1);
  });
  it("does not strand a retry started between a no-op flush and its finalizer", async () => {
    const save = vi.fn<(patch: Partial<{ theme: string }>) => Promise<void>>()
      .mockRejectedValueOnce(new Error("failed")).mockResolvedValue(undefined);
    const queue = createSettingsAutoSave({ save, changed: vi.fn() });
    queue.enqueue({ theme: "dark" });
    await queue.flush();
    const leaving = queue.flush();
    const retry = queue.retry();
    await Promise.all([leaving, retry]);
    expect(save).toHaveBeenCalledTimes(2);
    expect(queue.pending).toBe(false);
  });
});

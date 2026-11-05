import { describe, expect, it, vi } from "vitest";
import { createFrameScheduler } from "./frameScheduler";

describe("createFrameScheduler", () => {
  it("applies only the latest value once per animation frame", () => {
    let callback: FrameRequestCallback | undefined;
    const apply = vi.fn();
    const request = vi.fn((next: FrameRequestCallback) => {
      callback = next;
      return 7;
    });
    const scheduler = createFrameScheduler(apply, request, vi.fn());

    scheduler.schedule(510);
    scheduler.schedule(520);
    scheduler.schedule(540);

    expect(request).toHaveBeenCalledTimes(1);
    expect(apply).not.toHaveBeenCalled();
    callback?.(16);
    expect(apply).toHaveBeenCalledOnce();
    expect(apply).toHaveBeenLastCalledWith(540);
  });

  it("flushes the final pointer position and cancels the pending frame", () => {
    const apply = vi.fn();
    const cancel = vi.fn();
    const scheduler = createFrameScheduler(apply, () => 11, cancel);

    scheduler.schedule(680);
    scheduler.flush();

    expect(cancel).toHaveBeenCalledWith(11);
    expect(apply).toHaveBeenCalledWith(680);
    scheduler.flush();
    expect(apply).toHaveBeenCalledOnce();
  });
});

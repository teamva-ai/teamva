export interface FrameScheduler<T> {
  schedule(value: T): void;
  flush(): void;
  cancel(): void;
}

// Coalesces high-frequency input (pointermove, scroll, resize) to one DOM
// mutation per rendered frame. flush() is intentionally synchronous so a
// pointer-up can commit the final position even when it arrives before RAF.
export function createFrameScheduler<T>(
  apply: (value: T) => void,
  requestFrame: (callback: FrameRequestCallback) => number = requestAnimationFrame,
  cancelFrame: (handle: number) => void = cancelAnimationFrame
): FrameScheduler<T> {
  let frame = 0;
  let pending: T | undefined;
  let hasPending = false;

  const run = () => {
    frame = 0;
    if (!hasPending) return;
    const value = pending as T;
    pending = undefined;
    hasPending = false;
    apply(value);
  };

  return {
    schedule(value) {
      pending = value;
      hasPending = true;
      if (!frame) frame = requestFrame(run);
    },
    flush() {
      if (frame) {
        cancelFrame(frame);
        frame = 0;
      }
      run();
    },
    cancel() {
      if (frame) cancelFrame(frame);
      frame = 0;
      pending = undefined;
      hasPending = false;
    }
  };
}

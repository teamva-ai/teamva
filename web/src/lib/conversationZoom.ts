export interface ConversationViewport { x: number; y: number; scale: number }
export interface ConversationWheel {
  deltaY: number;
  deltaMode: number;
  height: number;
  x: number;
  y: number;
  ctrlKey: boolean;
}

const clamp = (value: number, min: number, max: number) => Math.min(max, Math.max(min, value));
const MIN_LOG = Math.log(0.2), MAX_LOG = Math.log(2);
const EVENT_LIMIT = Math.log(1.1), BACKLOG_LIMIT = Math.log(1.2), FRAME_LIMIT = Math.log(1.04);

/** Wheel input changes a bounded logarithmic target, never the displayed scale directly.
 * The backlog is bounded against the displayed camera, including same-frame bursts.
 * Call cancel before any other camera owner (pan/reset/navigation) changes the view.
 */
export function createConversationZoom(options: {
  getViewport: () => ConversationViewport;
  setViewport: (view: ConversationViewport) => void;
  requestFrame?: (callback: FrameRequestCallback) => number;
  cancelFrame?: (id: number) => void;
}) {
  const request = options.requestFrame ?? ((callback) => requestAnimationFrame(callback));
  const cancelFrame = options.cancelFrame ?? ((id) => cancelAnimationFrame(id));
  let frame: number | undefined;
  let target: number | undefined;
  let anchor = { x: 0, y: 0 };
  let destroyed = false;
  let previousTime: number | undefined;

  function animate(timestamp: number) {
    frame = undefined;
    if (destroyed || target === undefined) return;
    const view = options.getViewport();
    const current = Math.log(view.scale), distance = target - current;
    // Use elapsed time so a 120Hz display is not twice as fast as 60Hz.
    // Bound background-tab catch-up and retain the per-frame visual limit.
    const elapsed = previousTime === undefined || !Number.isFinite(timestamp) || timestamp <= previousTime
      ? 1000 / 60 : Math.min(timestamp - previousTime, 32);
    previousTime = Number.isFinite(timestamp) ? timestamp : undefined;
    const limit = Math.min(FRAME_LIMIT, 1.1 * elapsed / 1000);
    const step = Math.abs(distance) < 0.0005 ? distance : clamp(distance * (1 - Math.exp(-elapsed / 90)), -limit, limit);
    const scale = Math.exp(clamp(current + step, MIN_LOG, MAX_LOG));
    const ratio = scale / view.scale;
    options.setViewport({ x: anchor.x - (anchor.x - view.x) * ratio, y: anchor.y - (anchor.y - view.y) * ratio, scale });
    if (Math.abs(target - Math.log(scale)) > 1e-10) frame = request(animate);
    else { target = undefined; previousTime = undefined; }
  }

  function cancel() {
    if (frame !== undefined) cancelFrame(frame);
    frame = undefined;
    target = undefined;
    previousTime = undefined;
  }

  return {
    wheel(input: ConversationWheel) {
      if (destroyed || !Number.isFinite(input.deltaY) || input.deltaY === 0 || !Number.isFinite(input.x) || !Number.isFinite(input.y)) return;
      const pixels = input.deltaY * (input.deltaMode === 1 ? 16 : input.deltaMode === 2 ? input.height : 1);
      if (!Number.isFinite(pixels)) return;
      const current = Math.log(options.getViewport().scale);
      const delta = clamp(-pixels * (input.ctrlKey ? 0.003 : 0.0015), -EVENT_LIMIT, EVENT_LIMIT);
      target = clamp(clamp((target ?? current) + delta, current - BACKLOG_LIMIT, current + BACKLOG_LIMIT), MIN_LOG, MAX_LOG);
      anchor = { x: input.x, y: input.y };
      if (frame === undefined && Math.abs(target - current) > 1e-10) frame = request(animate);
    },
    cancel,
    destroy() { cancel(); destroyed = true; }
  };
}

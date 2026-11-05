/** A serial, coalescing intent queue. Only explicit user events enqueue writes.
 * Hydration calls sync(), which never writes. Failed intents remain until an
 * explicit retry; a newer edit cannot accidentally retry a version conflict. */
export type AutoSaveStatus = "idle" | "saving" | "saved" | "error";
export function createSettingsAutoSave<T extends object, R>(options: {
  save: (intent: Partial<T>) => Promise<R>;
  changed: (state: { intent: Partial<T>; status: AutoSaveStatus; error: unknown }) => void;
  saved?: (result: R, intent: Partial<T>) => void;
}) {
  const intents = new Map<keyof T, { value: T[keyof T]; revision: number }>();
  let revision = 0;
  let status: AutoSaveStatus = "idle";
  let error: unknown = null;
  let timer: ReturnType<typeof setTimeout> | undefined;
  let running: Promise<void> | undefined;
  const snapshot = (): Partial<T> => Object.fromEntries([...intents].map(([key, entry]) => [key, entry.value])) as Partial<T>;
  const notify = () => options.changed({ intent: snapshot(), status, error });
  function cancelTimer() { if (timer !== undefined) clearTimeout(timer); timer = undefined; }
  async function drain() {
    while (intents.size && status !== "error") {
      // A caller may still be waiting for its explicit debounce boundary.
      if (timer !== undefined) break;
      const batch = new Map(intents);
      const intent = snapshot();
      status = "saving";
      notify();
      try {
        const result = await options.save(intent);
        for (const [key, entry] of batch) {
          if (intents.get(key)?.revision === entry.revision) intents.delete(key);
        }
        options.saved?.(result, intent);
        status = intents.size ? "saving" : "saved";
        notify();
      } catch (cause) {
        status = "error";
        error = cause;
        cancelTimer();
        notify();
      }
    }
  }
  function start(): Promise<void> {
    if (running) return running;
    running = drain().finally(() => {
      running = undefined;
      // A retry/flush can arrive after drain has returned but before this
      // promise's finalizer. Do not strand that newly runnable intent.
      if (intents.size && status !== "error" && timer === undefined) return start();
    });
    return running;
  }
  return {
    enqueue(patch: Partial<T>, delay = 0) {
      for (const key of Object.keys(patch) as Array<keyof T>) {
        // Enter followed by blur, or clicking the password eye, must not submit
        // an identical credential twice and revoke device trust twice.
        if (intents.has(key) && Object.is(intents.get(key)!.value, patch[key])) continue;
        intents.set(key, { value: patch[key] as T[keyof T], revision: ++revision });
      }
      cancelTimer();
      if (status === "error") { notify(); return; }
      status = "saving";
      notify();
      if (delay) timer = setTimeout(() => { timer = undefined; void start(); }, delay);
      else void start();
    },
    flush() { cancelTimer(); return start(); },
    retry() {
      if (!intents.size) return Promise.resolve();
      error = null; status = "saving"; notify(); return this.flush();
    },
    sync: notify,
    get pending() { return intents.size > 0; },
    get failed() { return status === "error"; }
  };
}

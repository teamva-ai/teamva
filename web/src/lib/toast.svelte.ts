// Global toast notifications. Components push messages through toast.success /
// toast.error; the Toast.svelte container renders them and auto-dismisses.

export interface ToastItem {
  id: number;
  kind: "success" | "error" | "info";
  message: string;
}

const toasts = $state<ToastItem[]>([]);
let nextId = 1;

function push(kind: ToastItem["kind"], message: string, durationMs = 3200) {
  const id = nextId++;
  toasts.push({ id, kind, message });
  if (toasts.length > 4) {
    toasts.shift();
  }
  window.setTimeout(() => {
    const index = toasts.findIndex((item) => item.id === id);
    if (index !== -1) {
      toasts.splice(index, 1);
    }
  }, durationMs);
}

export function toastSuccess(message: string, durationMs?: number) {
  push("success", message, durationMs);
}

export function toastError(message: string, durationMs?: number) {
  push("error", message, durationMs);
}

export function toastList(): ToastItem[] {
  return toasts;
}

// Test-only helper: clears all pending toasts.
export function clearToastsForTest() {
  toasts.length = 0;
}

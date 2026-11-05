import { navigate } from "./router.svelte";

export const remoteAuth = $state({ active: false });

// Accept application-relative destinations only, never protocol-relative URLs,
// encoded slashes/backslashes, control characters or a return to the login API.
export function remoteReturnPath(value: string | null): string {
  if (!value || !value.startsWith("/") || value.startsWith("//")) return "/workbench";
  try {
    const decoded = decodeURIComponent(value);
    if (decoded.startsWith("//") || /[\\\u0000-\u001f\u007f]/.test(decoded)) return "/workbench";
    const url = new URL(value, window.location.origin);
    const path = new URL(decoded, window.location.origin).pathname;
    if (url.origin !== window.location.origin || /^\/(remote|v1|assets|desktop)(\/|$)/.test(path)) return "/workbench";
    return url.pathname + url.search + url.hash;
  } catch {
    return "/workbench";
  }
}

export function requireRemoteLogin() {
  if (window.location.pathname === "/remote/login") return;
  const next = remoteReturnPath(window.location.pathname + window.location.search + window.location.hash);
  navigate(`/remote/login?next=${encodeURIComponent(next)}`, { replace: true });
}

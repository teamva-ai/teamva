// Minimal history-based router state. Components live in src/components.
export const location = $state({
  path: window.location.pathname,
  search: window.location.search
});

export type NavigationIntent = { kind: "route" } | { kind: "project"; projectId: string };
type NavigationGuard = (to: string, intent: NavigationIntent) => boolean;
const navigationGuards = new Map<NavigationGuard, (() => void) | undefined>();
export function registerNavigationGuard(guard: NavigationGuard, onAllowed?: () => void) {
  navigationGuards.set(guard, onAllowed);
  return () => navigationGuards.delete(guard);
}
export function confirmNavigation(to: string, intent: NavigationIntent = { kind: "route" }) {
  const guards = [...navigationGuards];
  if (!guards.every(([guard]) => guard(to, intent))) return false;
  // Commit discards only after every owner agreed. A later guard rejecting the
  // transition must not destroy an earlier editor's still-visible draft.
  for (const [, onAllowed] of guards) onAllowed?.();
  return true;
}

function refresh() {
  location.path = window.location.pathname;
  location.search = window.location.search;
}

window.addEventListener("popstate", () => {
  if (!confirmNavigation(window.location.pathname + window.location.search)) {
    window.history.pushState({}, "", location.path + location.search);
    return;
  }
  refresh();
});

export function navigate(to: string, options: { replace?: boolean } = {}) {
  if (to === window.location.pathname + window.location.search) return;
  if (!confirmNavigation(to)) return;
  if (options.replace) window.history.replaceState({}, "", to);
  else window.history.pushState({}, "", to);
  refresh();
  window.scrollTo(0, 0);
}

export function queryParams(): URLSearchParams {
  return new URLSearchParams(location.search);
}

export function setQueryParam(name: string, value: string | null, replace = false) {
  const params = queryParams();
  if (value === null) {
    params.delete(name);
  } else {
    params.set(name, value);
  }
  const search = params.toString();
  const next = window.location.pathname + (search ? `?${search}` : "");
  if (next !== window.location.pathname + window.location.search && !confirmNavigation(next)) return;
  if (replace) {
    window.history.replaceState({}, "", next);
  } else {
    window.history.pushState({}, "", next);
  }
  refresh();
}

export function matchPath(pattern: string, pathname: string): Record<string, string> | null {
  const patternSegments = pattern.split("/").filter(Boolean);
  const pathSegments = pathname.split("/").filter(Boolean);
  const params: Record<string, string> = {};
  for (let i = 0; i < patternSegments.length; i++) {
    const segment = patternSegments[i];
    if (segment === "*") {
      return params;
    }
    const value = pathSegments[i];
    if (value === undefined) return null;
    if (segment.startsWith(":")) {
      try {
        params[segment.slice(1)] = decodeURIComponent(value);
      } catch {
        return null;
      }
    } else if (segment !== value) {
      return null;
    }
  }
  return pathSegments.length === patternSegments.length ? params : null;
}

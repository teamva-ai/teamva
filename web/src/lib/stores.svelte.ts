// Client-side state. The session token itself lives only in an HttpOnly
// SameSite cookie; this store keeps the per-session CSRF token in memory
// (never persisted). Workspace/project selection is local UI state. User
// preferences are hydrated from the server after authentication.

import type { UserPreferences } from "./types";
import { normalizeAppearance, type AppearancePreferences } from "./appearanceThemes";

const STORAGE = {
  selection: "teamva-selection"
};

function readSelection(storage: "sessionStorage" | "localStorage"): Partial<{ computerId: string; projectId: string }> {
  try {
    const raw = window[storage].getItem(STORAGE.selection);
    const value = raw ? JSON.parse(raw) : null;
    return {
      computerId: typeof value?.computerId === "string" ? value.computerId : undefined,
      projectId: typeof value?.projectId === "string" ? value.projectId : undefined
    };
  } catch {
    return {};
  }
}

function loadSelection(): { computerId: string; projectId: string } {
  const currentWindow = readSelection("sessionStorage");
  const shared = readSelection("localStorage");
  return {
    computerId: currentWindow.computerId ?? shared.computerId ?? "",
    // A new window starts at its default project, never another window's project.
    projectId: currentWindow.projectId ?? ""
  };
}

export const session = $state<{ csrf: string }>({ csrf: "" });

export function setCsrf(csrf: string) {
  session.csrf = csrf;
}

export function clearSession() {
  session.csrf = "";
  preferences.theme = "system";
  preferences.appearance = normalizeAppearance();
  preferences.allowWrite = false;
}

export const selection = $state<{ computerId: string; projectId: string }>(
  loadSelection()
);

export function setComputer(id: string) {
  if (selection.computerId === id) return;
  selection.computerId = id;
  selection.projectId = "";
  persistSelection();
}

export function setProject(id: string) {
  selection.projectId = id;
  persistSelection();
}

function persistSelection() {
  try {
    window.sessionStorage.setItem(STORAGE.selection, JSON.stringify(selection));
  } catch {
    // Storage can be unavailable in a private/restricted browser. Selection
    // still works in memory; persistence must not break navigation or creation.
  }
  try {
    window.localStorage.setItem(STORAGE.selection, JSON.stringify({ computerId: selection.computerId }));
  } catch {
    // Persist each storage independently so one failure does not affect the other.
  }
}

export type ThemePreference = "system" | "light" | "dark";

export const preferences = $state<{
  locale: string;
  theme: ThemePreference;
  appearance: AppearancePreferences;
  allowWrite: boolean;
}>({ locale: "", theme: "system", appearance: normalizeAppearance(), allowWrite: false });

export function setLocale(locale: string) {
  preferences.locale = locale;
}

export function setTheme(theme: ThemePreference) {
  preferences.theme = theme;
}

export function setAppearance(appearance: AppearancePreferences) {
  preferences.appearance = normalizeAppearance(appearance);
}

export function setAllowWrite(allow: boolean) {
  preferences.allowWrite = allow;
}

export function hydratePreferences(value: UserPreferences, effectiveLocale = value.locale) {
	preferences.locale = effectiveLocale;
  preferences.theme = value.theme;
  preferences.appearance = normalizeAppearance(value.appearance);
  preferences.allowWrite = value.allow_write;
}

import { preferences } from "./stores.svelte";
import {
  THEME_TOKEN_CSS_PROPERTIES,
  type ResolvedTheme
} from "./themeTokens";
import { appearanceTokens, contrastRatio, normalizeAppearance, type AppearancePreferences } from "./appearanceThemes";

export type ThemePreference = "system" | "light" | "dark";

let activePreference: ThemePreference = "system";
let activeAppearance = normalizeAppearance();
let preview: { owner: symbol; mode: ThemePreference; appearance: AppearancePreferences } | undefined;

function systemPrefersDark(): boolean {
  return typeof window !== "undefined" &&
    (window.matchMedia?.("(prefers-color-scheme: dark)").matches ?? false);
}

export function resolveTheme(theme: ThemePreference): ResolvedTheme {
  return theme === "dark" || (theme === "system" && systemPrefersDark()) ? "dark" : "light";
}

export function applyTheme(theme: ThemePreference, appearance: AppearancePreferences = preferences.appearance) {
  activePreference = theme;
  activeAppearance = normalizeAppearance(appearance);
  renderTheme();
}

function renderTheme() {
  const resolved = resolveTheme(preview?.mode ?? activePreference);
  const root = document.documentElement;
  const appearance = preview?.appearance ?? activeAppearance;
  const tokens = appearanceTokens(appearance, resolved);
  for (const [key, property] of Object.entries(THEME_TOKEN_CSS_PROPERTIES) as Array<
    [keyof typeof THEME_TOKEN_CSS_PROPERTIES, string]
  >) {
    root.style.setProperty(property, tokens[key]);
  }
  // A custom canvas can intentionally be dark while the selected mode is light
  // (or vice versa). Native controls and code highlighting follow rendered ink.
  const rendered = contrastRatio(tokens.canvas, "#000000") >= contrastRatio(tokens.canvas, "#ffffff") ? "light" : "dark";
  root.dataset.theme = rendered;
  root.dataset.palette = appearance.palette;
  root.style.colorScheme = rendered;
}

/** Editor-scoped preview never writes preferences and cannot survive unmount.
 * The owner token also prevents an older editor's cleanup clearing a new one. */
export function createThemePreview() {
  const owner = Symbol("appearance-preview");
  return {
    update(mode: ThemePreference, appearance: AppearancePreferences) {
      preview = { owner, mode, appearance: normalizeAppearance(appearance) };
      renderTheme();
    },
    dispose() {
      if (preview?.owner !== owner) return;
      preview = undefined;
      renderTheme();
    }
  };
}

let mediaListener: (() => void) | null = null;

export function subscribeTheme() {
  applyTheme(preferences.theme);
  const media = window.matchMedia?.("(prefers-color-scheme: dark)");
  const handler = () => {
    if ((preview?.mode ?? activePreference) === "system") renderTheme();
  };
  media?.addEventListener?.("change", handler);
  mediaListener = () => media?.removeEventListener?.("change", handler);
  return mediaListener;
}

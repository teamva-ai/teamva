import zhCN from "../locales/zh-CN.json";
import zhTW from "../locales/zh-TW.json";
import enUS from "../locales/en-US.json";
import jaJP from "../locales/ja-JP.json";
import deDE from "../locales/de-DE.json";
import frFR from "../locales/fr-FR.json";
import esES from "../locales/es-ES.json";
import ptBR from "../locales/pt-BR.json";
import ruRU from "../locales/ru-RU.json";
import { workTranslations } from "../locales/work";
import { inspirationTranslations } from "../locales/inspirations";
import { onboardingTranslations } from "../locales/onboarding";
import { controlTranslations } from "../locales/controls";
import { defaultProjectTranslations } from "../locales/defaultProject";
import { appearanceTranslations } from "../locales/appearance";
import { scheduleEditorTranslations } from "../locales/scheduleEditor";
import { executableAutosaveTranslations } from "../locales/executableAutosave";
import { settingsAutoSaveTranslations } from "../locales/settingsAutoSave";
import { mcpTranslations } from "../locales/mcp";
import { directoryPickerTranslations } from "../locales/directoryPicker";
import { gitAuthorizationTranslations } from "../locales/gitAuthorization";
import { goalBudgetTranslations } from "../locales/goalBudget";
import { issueWorkTranslations } from "../locales/issueWork";
import { preferences, setLocale as persistLocale } from "./stores.svelte";
import type { SupportedLocale, UserPreferences } from "./types";

export type { SupportedLocale } from "./types";

export const SUPPORTED_LOCALES = [
  "zh-CN",
  "zh-TW",
  "en-US",
  "ja-JP",
  "de-DE",
  "fr-FR",
  "es-ES",
  "pt-BR",
  "ru-RU"
] as const satisfies readonly SupportedLocale[];

export const LOCALE_OPTIONS: ReadonlyArray<{ value: SupportedLocale; label: string }> = [
  { value: "zh-CN", label: "简体中文" },
  { value: "zh-TW", label: "繁體中文" },
  { value: "en-US", label: "English" },
  { value: "ja-JP", label: "日本語" },
  { value: "de-DE", label: "Deutsch" },
  { value: "fr-FR", label: "Français" },
  { value: "es-ES", label: "Español" },
  { value: "pt-BR", label: "Português" },
  { value: "ru-RU", label: "Русский" }
];

const STORAGE_KEY = "teamva-locale";
type Dictionary = Record<string, unknown>;

const resources: Record<SupportedLocale, Dictionary> = {
  "zh-CN": zhCN,
  "zh-TW": zhTW,
  "en-US": enUS,
  "ja-JP": jaJP,
  "de-DE": deDE,
  "fr-FR": frFR,
  "es-ES": esES,
  "pt-BR": ptBR,
  "ru-RU": ruRU
};
for (const locale of SUPPORTED_LOCALES) {
  resources[locale].work = workTranslations[locale];
  resources[locale].inspirations = inspirationTranslations[locale];
  resources[locale].onboarding = onboardingTranslations[locale];
  resources[locale].controls = controlTranslations[locale];
  resources[locale].defaultProject = defaultProjectTranslations[locale];
  resources[locale].appearance = appearanceTranslations[locale];
  resources[locale].scheduleEditor = scheduleEditorTranslations[locale];
  resources[locale].executableAutosave = executableAutosaveTranslations[locale];
  resources[locale].settingsAutoSave = settingsAutoSaveTranslations[locale];
  resources[locale].mcp = mcpTranslations[locale];
  resources[locale].directoryPicker = directoryPickerTranslations[locale];
  resources[locale].gitAuthorization = gitAuthorizationTranslations[locale];
  resources[locale].goalBudget = goalBudgetTranslations[locale];
  resources[locale].issueWork = issueWorkTranslations[locale];
}

function deepGet(dict: unknown, key: string): string | undefined {
  const parts = key.split(".");
  let node: unknown = dict;
  for (const part of parts) {
    if (typeof node !== "object" || node === null) return undefined;
    node = (node as Record<string, unknown>)[part];
  }
  return typeof node === "string" ? node : undefined;
}

function interpolate(template: string, params?: Record<string, unknown>): string {
  if (!params) return template;
  return template.replace(/\{\{(\w+)\}\}/g, (_, name: string) =>
    params[name] !== undefined ? String(params[name]) : `{{${name}}}`
  );
}

export function normalizeLocaleTag(value: string): SupportedLocale | undefined {
  const normalized = value.trim().replaceAll("_", "-").toLowerCase();
  if (
    normalized === "zh-hant" ||
    normalized.startsWith("zh-hant-") ||
    normalized === "zh-tw" ||
    normalized.startsWith("zh-tw-") ||
    normalized === "zh-hk" ||
    normalized.startsWith("zh-hk-") ||
    normalized === "zh-mo" ||
    normalized.startsWith("zh-mo-")
  )
    return "zh-TW";
  if (
    normalized === "zh" ||
    normalized === "zh-hans" ||
    normalized.startsWith("zh-hans-") ||
    normalized === "zh-cn" ||
    normalized.startsWith("zh-cn-") ||
    normalized === "zh-sg" ||
    normalized.startsWith("zh-sg-")
  )
    return "zh-CN";
  if (normalized === "en" || normalized.startsWith("en-")) return "en-US";
  if (normalized === "ja" || normalized.startsWith("ja-")) return "ja-JP";
  if (normalized === "de" || normalized.startsWith("de-")) return "de-DE";
  if (normalized === "fr" || normalized.startsWith("fr-")) return "fr-FR";
  if (normalized === "es" || normalized.startsWith("es-")) return "es-ES";
  if (normalized === "pt" || normalized.startsWith("pt-")) return "pt-BR";
  if (normalized === "ru" || normalized.startsWith("ru-")) return "ru-RU";
  return undefined;
}

export function detectSystemLocale(): SupportedLocale {
  const candidates =
    typeof navigator === "undefined"
      ? []
      : [...(navigator.languages ?? []), navigator.language].filter(Boolean);
  for (const candidate of candidates) {
    const locale = normalizeLocaleTag(candidate);
    if (locale) return locale;
  }
  return "en-US";
}

export function detectBrowserLocale(): SupportedLocale {
  try {
    const stored = localStorage.getItem(STORAGE_KEY);
    if (stored && (SUPPORTED_LOCALES as readonly string[]).includes(stored)) {
      return stored as SupportedLocale;
    }
  } catch {
    // localStorage unavailable; fall through to operating-system detection.
  }
  return detectSystemLocale();
}

export const localeInfo = $state<{ value: SupportedLocale }>({ value: detectBrowserLocale() });

export function applyLocale(locale: SupportedLocale, persist = false) {
  localeInfo.value = locale;
  persistLocale(locale);
  try {
    if (persist) localStorage.setItem(STORAGE_KEY, locale);
    else localStorage.removeItem(STORAGE_KEY);
  } catch {
    // Ignore storage errors. The current session still switches immediately.
  }
  document.documentElement.lang = locale;
}

// A direct user action becomes authoritative on subsequent launches.
export function changeLocale(locale: SupportedLocale) {
  applyLocale(locale, true);
}

// Server preferences only override the operating system after the user has
// explicitly selected a language. Automatic first-run locale values are never
// persisted in localStorage, so a later OS language change is respected.
export function applyUserLocalePreference(
  value: Pick<UserPreferences, "locale" | "locale_explicit">
) {
  const locale = value.locale_explicit ? value.locale : detectSystemLocale();
  applyLocale(locale, value.locale_explicit);
  return locale;
}

export function t(key: string, params?: Record<string, unknown>): string {
  const dict = resources[localeInfo.value] ?? resources["en-US"];
  const value = deepGet(dict, key);
  if (value === undefined) {
    const fallback = deepGet(resources["en-US"], key);
    return interpolate(fallback ?? key, params);
  }
  return interpolate(value, params);
}

document.documentElement.lang = localeInfo.value;

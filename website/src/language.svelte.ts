export type Language = "en" | "zh";

function initialLanguage(): Language {
  const requested = new URL(location.href).searchParams.get("lang");
  if (requested === "en" || requested === "zh") return requested;
  try { return localStorage.getItem("teamva-site-language") === "zh" ? "zh" : "en"; }
  catch { return "en"; }
}

export const siteLanguage = $state({ value: initialLanguage() });

export function setLanguage(value: Language) {
  siteLanguage.value = value;
  try { localStorage.setItem("teamva-site-language", value); } catch { /* Private browsing can deny storage. */ }
  const url = new URL(location.href);
  url.searchParams.set("lang", value);
  history.replaceState(null, "", url);
}

export function languageHref(href: string, language = siteLanguage.value) {
  const url = new URL(href, location.href);
  url.searchParams.set("lang", language);
  return url.pathname + url.search + url.hash;
}

import { afterEach, beforeEach, describe, expect, it } from "vitest";
import {
  applyLocale, applyUserLocalePreference, changeLocale, detectBrowserLocale, detectSystemLocale,
  LOCALE_OPTIONS, localeInfo, normalizeLocaleTag, SUPPORTED_LOCALES, t
} from "./i18n.svelte";
import { auditActionLabel } from "./helpers";

describe("i18n", () => {
  afterEach(() => { localStorage.clear(); applyLocale("en-US"); });
  beforeEach(() => {
    localStorage.clear();
    applyLocale("zh-CN");
  });

  it("translates known keys", () => {
    expect(t("status.running")).toBe("运行中");
    expect(t("common.save")).toBe("保存");
    expect(t("shell.issues")).toBe("任务");
    expect(t("localSkills.title")).toBe("技能 & MCP");
    expect(t("programmingTools.title")).toBe("本机编程工具");
  });

  it("uses the shared navigation labels and localized skill terminology", () => {
    expect(t("shell.conversations")).toBe("分支对话");
    expect(t("conversations.title")).toBe("分支对话");
    expect(t("shell.skills")).toBe("技能 & MCP");
    expect(t("agents.boundSkills")).toBe("Agent 使用的技能");
    expect(t("controls.agentConfigurationCentralized")).toContain("技能");

    for (const locale of SUPPORTED_LOCALES) {
      applyLocale(locale);
      expect(t("shell.agents")).toBe("Agents");
      expect(t("agents.title")).toBe("Agents");
      expect(t("defaultProject.hint")).toContain("默认项目");
      expect(t("defaultProject.hint")).not.toContain("不使用项目");
      expect(t("defaultProject.hint")).not.toMatch(/配色|colors?|Farben|couleurs|colores|cores|цвета/i);
      expect(t("settings.projectTheme")).toBe("settings.projectTheme");
      expect(t("settings.projectThemeHint")).toBe("settings.projectThemeHint");
      expect(t("shell.skills")).toBe(locale.startsWith("zh-") ? "技能 & MCP" : "Skills & MCP");
    }
  });

  it("falls back to the raw key for unknown keys", () => {
    expect(t("status.not_a_real_status")).toBe("status.not_a_real_status");
  });

  it("translates dotted audit actions emitted by the API", () => {
    expect(auditActionLabel("user.preferences_updated")).toBe("更新用户偏好");
    expect(auditActionLabel("unknown.action")).toBe("unknown.action");
  });

  it("interpolates parameters", () => {
    expect(t("issues.count", { count: 2 })).toBe("2 个任务");
    expect(t("resources.pathHint", { computer: "开发 Mac" })).toContain("开发 Mac");
    expect(t("issues.title")).toBe("任务");
    expect(t("localSkills.desc", { computer: "开发 Mac" })).toContain("开发 Mac");

    changeLocale("en-US");
    expect(t("issues.count", { count: 2 })).toBe("2 tasks");
    expect(t("localSkills.desc", { computer: "Engineering Mac" })).toContain("Engineering Mac");
  });

  it("switches language immediately and persists", () => {
    changeLocale("en-US");
    expect(localeInfo.value).toBe("en-US");
    expect(t("shell.workbench")).toBe("Overview");
    expect(t("common.save")).toBe("Save");
    expect(localStorage.getItem("teamva-locale")).toBe("en-US");
    // back to Chinese
    changeLocale("zh-CN");
    expect(t("shell.workbench")).toBe("概览");
  });

  it("renders memory and Git round sources without leftover interpolation braces", () => {
    for (const locale of SUPPORTED_LOCALES) {
      applyLocale(locale);
      const memory = t("memories.versionInfo", { key: "amqa-bounds", version: 2, source: "QA_SOURCE" });
      const round = t("runChanges.roundSource", { round: 3, source: "QA_SOURCE" });
      expect(memory, locale).toContain("amqa-bounds");
      expect(memory, locale).toContain("2");
      expect(round, locale).toContain("3");
      for (const label of [memory, round]) {
        expect(label, locale).toContain("QA_SOURCE");
        expect(label, locale).not.toMatch(/[{}]/);
      }
    }
  });

  it("maps operating-system language families and Chinese scripts", () => {
    localStorage.removeItem("teamva-locale");
    expect(normalizeLocaleTag("zh-Hant-HK")).toBe("zh-TW");
    expect(normalizeLocaleTag("zh_Hans_SG")).toBe("zh-CN");
    expect(normalizeLocaleTag("ja")).toBe("ja-JP");
    expect(normalizeLocaleTag("de-AT")).toBe("de-DE");
    expect(normalizeLocaleTag("fr-CA")).toBe("fr-FR");
    expect(normalizeLocaleTag("es-MX")).toBe("es-ES");
    expect(normalizeLocaleTag("pt-PT")).toBe("pt-BR");
    expect(normalizeLocaleTag("ru")).toBe("ru-RU");
    expect(normalizeLocaleTag("it-IT")).toBeUndefined();

    Object.defineProperty(navigator, "languages", { value: ["de-DE", "en-US"], configurable: true });
    Object.defineProperty(navigator, "language", { value: "de-DE", configurable: true });
    expect(detectSystemLocale()).toBe("de-DE");
    expect(detectBrowserLocale()).toBe("de-DE");

    Object.defineProperty(navigator, "languages", { value: ["it-IT"], configurable: true });
    Object.defineProperty(navigator, "language", { value: "it-IT", configurable: true });
    expect(detectSystemLocale()).toBe("en-US");
    expect(SUPPORTED_LOCALES).toHaveLength(9);
    expect(LOCALE_OPTIONS.map((item) => item.value)).toEqual(SUPPORTED_LOCALES);
  });

  it("only lets an explicit user preference override the operating system", () => {
    Object.defineProperty(navigator, "languages", { value: ["ja-JP"], configurable: true });
    Object.defineProperty(navigator, "language", { value: "ja-JP", configurable: true });

    changeLocale("de-DE");
    expect(localStorage.getItem("teamva-locale")).toBe("de-DE");
    expect(applyUserLocalePreference({ locale: "zh-CN", locale_explicit: false })).toBe("ja-JP");
    expect(localeInfo.value).toBe("ja-JP");
    expect(localStorage.getItem("teamva-locale")).toBeNull();

    expect(applyUserLocalePreference({ locale: "fr-FR", locale_explicit: true })).toBe("fr-FR");
    expect(localeInfo.value).toBe("fr-FR");
    expect(localStorage.getItem("teamva-locale")).toBe("fr-FR");
  });
});

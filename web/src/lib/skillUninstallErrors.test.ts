import { afterEach, describe, expect, it } from "vitest";
import zhCN from "../locales/zh-CN.json";
import zhTW from "../locales/zh-TW.json";
import enUS from "../locales/en-US.json";
import jaJP from "../locales/ja-JP.json";
import deDE from "../locales/de-DE.json";
import frFR from "../locales/fr-FR.json";
import esES from "../locales/es-ES.json";
import ptBR from "../locales/pt-BR.json";
import ruRU from "../locales/ru-RU.json";
import { APIError } from "./api";
import { applyLocale, SUPPORTED_LOCALES } from "./i18n.svelte";
import { localizedAPIErrorMessage } from "./localizedMessages";

const translations = {
  "zh-CN": [zhCN, "解除所有绑定", "停止相关运行"],
  "zh-TW": [zhTW, "解除所有綁定", "停止相關執行"],
  "en-US": [enUS, "Unbind it from all Agents", "stop related runs"],
  "ja-JP": [jaJP, "すべての関連付けを解除", "処理を停止"],
  "de-DE": [deDE, "alle Verknüpfungen", "stoppen"],
  "fr-FR": [frFR, "toutes ses associations", "arrêtez les exécutions"],
  "es-ES": [esES, "todas sus vinculaciones", "detén las ejecuciones"],
  "pt-BR": [ptBR, "todos os vínculos", "interrompa as execuções"],
  "ru-RU": [ruRU, "все его привязки", "остановите связанные запуски"]
} as const;

afterEach(() => applyLocale("en-US"));

describe("skill uninstall errors", () => {
  it.each(SUPPORTED_LOCALES)("has actionable, code-owned copy in %s without a wire-message fallback", (locale) => {
    applyLocale(locale);
    const [dictionary, unbind, stop] = translations[locale];
    const expected = dictionary.errors.codes.skill_in_use;
    // Check each actual locale resource, not t() alone: English fallback could
    // otherwise conceal a missing translation.
    expect(expected).toContain(unbind);
    expect(expected).toContain(stop);
    expect(expected).not.toBe(dictionary.errors.codes.invalid_argument);
    expect(localizedAPIErrorMessage("skill_in_use", 409)).toBe(expected);
    for (const message of ["private-runtime-payload", "内部路径 /private/skill-path"]) {
      const error = new APIError(409, { error: { code: "skill_in_use", message, retryable: false } });
      expect(error.message).toBe(expected);
      expect(error.message).not.toContain(message);
      expect(error.code).toBe("skill_in_use");
      expect(error.status).toBe(409);
      expect(error.retryable).toBe(false);
    }
  });
});

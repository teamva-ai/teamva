import { afterEach, describe, expect, it } from "vitest";
import { applyLocale, SUPPORTED_LOCALES, t } from "../lib/i18n.svelte";
import { executableAutosaveTranslations } from "./executableAutosave";

afterEach(() => applyLocale("en-US"));

describe("executable autosave translations", () => {
  it("registers both nonempty instructions for all nine locales", () => {
    expect(Object.keys(executableAutosaveTranslations).sort()).toEqual([...SUPPORTED_LOCALES].sort());
    for (const locale of SUPPORTED_LOCALES) {
      applyLocale(locale);
      expect(Object.keys(executableAutosaveTranslations[locale]).sort()).toEqual(["hint", "retryHint"]);
      for (const key of ["hint", "retryHint"] as const) {
        expect(executableAutosaveTranslations[locale][key].trim()).not.toBe("");
        expect(t(`executableAutosave.${key}`)).toBe(executableAutosaveTranslations[locale][key]);
        expect(t(`executableAutosave.${key}`)).not.toContain("{{");
      }
    }
  });
});

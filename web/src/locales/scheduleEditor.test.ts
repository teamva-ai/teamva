import { afterEach, describe, expect, it } from "vitest";
import { applyLocale, SUPPORTED_LOCALES, t } from "../lib/i18n.svelte";
import { scheduleEditorTranslations } from "./scheduleEditor";
import zhCN from "./zh-CN.json";
import zhTW from "./zh-TW.json";

const expectedKeys = [
  "daily", "weekly", "times", "addTime", "removeTime", "weekdays", "chooseWeekday",
  "dateTime", "invalidDate", "cronPreset", "time", "timezoneHint", "summaryDaily",
  "summaryWeekly", "issueMode", "issueNew", "issueExisting", "issueNewHint",
  "issueExistingHint", "selectIssue", "issueSearch", "issueMissing", "issueUnavailable",
  "agentHint", "noAgents", "configureAgent", "scopeHint", "serverRunningHint",
  "targetLabel", "newIssueEachRun", "existingIssueLabel", "saveError", "previewLoading",
  "previewInvalid", "loadIssuesFailed", "noAgentNote", "builtinAgentNote",
  "unconfiguredAgent", "unconfiguredAgentsHint"
].sort();

describe("schedule editor translations", () => {
  afterEach(() => applyLocale("en-US"));

  it("provides every key for all nine locales with matching interpolation variables", () => {
    expect(Object.keys(scheduleEditorTranslations).sort()).toEqual([...SUPPORTED_LOCALES].sort());
    const source = scheduleEditorTranslations["en-US"];
    for (const locale of SUPPORTED_LOCALES) {
      const pack = scheduleEditorTranslations[locale];
      expect(Object.keys(pack).sort(), locale).toEqual(expectedKeys);
      for (const key of Object.keys(source) as Array<keyof typeof source>) {
        expect(pack[key].trim(), `${locale}.${key}`).not.toBe("");
        expect(pack[key].match(/\{\{\w+\}\}/g)?.sort() ?? [], `${locale}.${key}`)
          .toEqual(source[key].match(/\{\{\w+\}\}/g)?.sort() ?? []);
      }
    }
  });

  it("registers the namespace and resolves all strings without raw interpolation", () => {
    const params = { index: 2, timezone: "Asia/Shanghai", times: "09:00, 17:00", weekdays: "Mon, Fri", name: "Lumi" };
    for (const locale of SUPPORTED_LOCALES) {
      applyLocale(locale);
      for (const key of expectedKeys) {
        const translated = t(`scheduleEditor.${key}`, params);
        expect(translated, `${locale}.${key}`).not.toBe(`scheduleEditor.${key}`);
        expect(translated, `${locale}.${key}`).not.toContain("{{");
      }
      expect(t("scheduleEditor.removeTime", params)).toContain("2");
      expect(t("scheduleEditor.timezoneHint", params)).toContain("Asia/Shanghai");
      expect(t("scheduleEditor.summaryWeekly", params)).toContain("Mon, Fri");
      expect(t("scheduleEditor.builtinAgentNote", params)).toContain("Agent");
      // Existing feature-local namespaces must remain registered.
      expect(t("controls.dateTime")).not.toBe("controls.dateTime");
      expect(t("appearance.title")).not.toBe("appearance.title");
    }
  });

  it("keeps Chinese scheduled-task names consistent", () => {
    for (const [pack, term] of [[zhCN, "定时任务"], [zhTW, "定時任務"]] as const) {
      expect(pack.shell.schedules).toBe(term);
      for (const key of ["title", "newTask", "taskList", "createTitle", "editTitle", "noTasks", "created", "updated"] as const) {
        expect(pack.schedules[key], key).toContain(term);
      }
    }
    expect(scheduleEditorTranslations["zh-CN"].issueExistingHint).toContain("已暂停或关闭的 Issue 不会自动恢复");
    expect(scheduleEditorTranslations["en-US"].issueExistingHint).toContain("not resumed or reopened automatically");
    expect(scheduleEditorTranslations["zh-CN"].serverRunningHint).toContain("仅在应用运行时触发");
    expect(scheduleEditorTranslations["en-US"].serverRunningHint).toContain("only while the app is running");
  });
});

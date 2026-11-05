import { afterEach, describe, expect, it } from "vitest";
import { APIError } from "./api";
import { applyLocale, SUPPORTED_LOCALES, t } from "./i18n.svelte";
import {
  GIT_WARNING_CODES, localizedGitDiffMessage, localizedGitWarning,
  localizedMessageContractKeys
} from "./localizedMessages";

afterEach(() => applyLocale("en-US"));

describe("stable Git and API message localization", () => {
  it("resolves every stable warning, diff, and API key in all nine locales", () => {
    for (const locale of SUPPORTED_LOCALES) {
      applyLocale(locale);
      for (const key of localizedMessageContractKeys()) {
        expect(t(key), `${locale}:${key}`).not.toBe(key);
        expect(t(key), `${locale}:${key}`).toBeTruthy();
      }
      for (const code of GIT_WARNING_CODES) {
        expect(localizedGitWarning({ code, message: "后端中文不应显示" })).not.toContain("后端中文不应显示");
      }
    }
  });

  it("prefers stable codes and reasons, while using English wire prose only for unknown codes", () => {
    applyLocale("de-DE");
    expect(localizedGitWarning({
      code: "status_unavailable", message: "暂时无法读取仓库状态"
    })).toBe(t("gitMessages.warnings.status_unavailable"));
    expect(localizedGitDiffMessage({
      message_code: "diff_timeout", message: "差异读取超时", stats: {}
    })).toBe(t("gitMessages.diff.timeout"));
    expect(localizedGitDiffMessage({
      message: "中文后端消息", truncation: { truncated: true, reason: "response_budget" }, stats: {}
    })).toBe(t("gitMessages.diff.responseBudget"));
    expect(localizedGitWarning({ code: "future_warning", message: "Future Git warning" }))
      .toBe("Future Git warning");
  });

  it("localizes known API codes before backend prose and suppresses unknown Chinese prose outside Chinese locales", () => {
    applyLocale("fr-FR");
    const known = new APIError(409, {
      error: { code: "snapshot_expired", message: "当前 Git 快照已失效" }
    });
    expect(known.message).toBe(t("errors.codes.snapshot_expired"));
    expect(known.message).not.toContain("当前");

    const unknownEnglish = new APIError(409, {
      error: { code: "future_conflict", message: "A future conflict occurred" }
    });
    expect(unknownEnglish.message).toBe("A future conflict occurred");
    const unknownChinese = new APIError(409, {
      error: { code: "future_conflict", message: "未来状态冲突" }
    });
    expect(unknownChinese.message).toBe(t("errors.requestFailed", { status: 409 }));
  });
});

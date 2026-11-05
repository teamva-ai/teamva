import { describe, expect, it } from "vitest";
import { mcpTranslations } from "./mcp";

describe("MCP capability boundary copy", () => {
  it.each(["en-US", "zh-CN"] as const)("explains manager/native OAuth differences without losing unknown fields in %s", (locale) => {
    const copy = mcpTranslations[locale];
    expect(copy.status.auth_pending).not.toBe("");
    expect(copy.managerOAuthHint).toContain("loopback");
    expect(copy.managerOAuthHint).toContain("callback_port");
    expect(copy.managerOAuthHint).toContain("redirect_uri");
    expect(copy.managerOAuthHint).toContain("Provider");
    for (const field of ["client_id", "client_secret", "scope", "callback_port", "redirect_uri", "auth_server_metadata_url"]) expect(copy.unsupportedOAuth).toContain(field);
    expect(copy.unsupportedOAuth).toMatch(locale === "zh-CN" ? /保留.*不会静默删除.*拒绝/ : /retained.*not silently removed.*reject/);
  });
  it("keeps explicit English fallbacks for untranslated locales", () => {
    for (const locale of ["ja-JP", "de-DE", "fr-FR", "es-ES", "pt-BR", "ru-RU"] as const) {
      expect(mcpTranslations[locale].managerOAuthHint).toBe(mcpTranslations["en-US"].managerOAuthHint);
    }
  });
});

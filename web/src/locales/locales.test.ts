import { describe, expect, it } from "vitest";
import ts from "typescript";
import zhCN from "./zh-CN.json";
import zhTW from "./zh-TW.json";
import enUS from "./en-US.json";
import jaJP from "./ja-JP.json";
import deDE from "./de-DE.json";
import frFR from "./fr-FR.json";
import esES from "./es-ES.json";
import ptBR from "./pt-BR.json";
import ruRU from "./ru-RU.json";
import { issueWorkTranslations } from "./issueWork";

function flatten(node: unknown, prefix = "", result: Record<string, string> = {}) {
  if (typeof node === "string") {
    result[prefix] = node;
    return result;
  }
  if (typeof node === "object" && node !== null) {
    for (const [key, value] of Object.entries(node)) {
      flatten(value, prefix ? `${prefix}.${key}` : key, result);
    }
  }
  return result;
}

const packs = { zhCN, zhTW, enUS, jaJP, deDE, frFR, esES, ptBR, ruRU };
const source = flatten(enUS);

describe("locale resources", () => {
  it("translates work assignments and keeps every interpolation in all nine locales", () => {
    const source = issueWorkTranslations["en-US"];
    expect(Object.keys(issueWorkTranslations)).toHaveLength(9);
    for (const pack of Object.values(issueWorkTranslations)) {
      expect(Object.keys(pack).sort()).toEqual(Object.keys(source).sort());
      for (const [key, value] of Object.entries(source)) {
        const translated = pack[key as keyof typeof source];
        expect(translated).toBeTruthy();
        expect(translated.match(/\{\{\w+\}\}/g)?.sort() ?? []).toEqual(value.match(/\{\{\w+\}\}/g)?.sort() ?? []);
      }
    }
  });
  it("does not silently overwrite duplicate JSON keys", () => {
    const files = import.meta.glob("./*.json", { query: "?raw", import: "default", eager: true });
    for (const [name, text] of Object.entries(files)) {
      const sourceFile = ts.parseJsonText(name, text as string);
      const visit = (node: ts.Node) => {
        if (ts.isObjectLiteralExpression(node)) {
          const keys = node.properties.filter(ts.isPropertyAssignment).map((property) =>
            ts.isStringLiteral(property.name) ? property.name.text : property.name.getText(sourceFile));
          expect(new Set(keys).size, `${name}: duplicate key near offset ${node.pos}`).toBe(keys.length);
        }
        ts.forEachChild(node, visit);
      };
      visit(sourceFile);
    }
  });
  for (const [name, pack] of Object.entries(packs)) {
    it(`${name} has every key and preserves interpolation variables`, () => {
      const localized = flatten(pack);
      expect(Object.keys(localized).sort()).toEqual(Object.keys(source).sort());
      for (const [key, english] of Object.entries(source)) {
        expect(localized[key], key).toBeTruthy();
        const sourceVariables = english.match(/\{\{\w+\}\}/g)?.sort() ?? [];
        const localizedVariables = localized[key].match(/\{\{\w+\}\}/g)?.sort() ?? [];
        expect(localizedVariables, key).toEqual(sourceVariables);
      }
    });
  }

  it("uses localized, user-facing copy for the Changes entry", () => {
    expect(zhCN.shell.changes).toBe("变更");
    expect(zhCN.changes.title).toBe("变更");
    expect(zhCN.changes.description).toBe("查看项目中各 Git 仓库的文件变更。");
    expect(zhCN.changes.description).not.toContain("绝对路径");

    expect(zhTW.shell.changes).toBe("變更");
    expect(zhTW.changes.title).toBe("變更");
    expect(zhTW.changes.description).not.toContain("絕對路徑");
  });
});

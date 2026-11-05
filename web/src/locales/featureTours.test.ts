import { describe, expect, it } from "vitest";
import type { SupportedLocale } from "../lib/types";
import { featureTourTranslations, type FeatureTourId } from "./featureTours";
import zhCN from "./zh-CN.json";
import zhTW from "./zh-TW.json";
import enUS from "./en-US.json";
import jaJP from "./ja-JP.json";
import deDE from "./de-DE.json";
import frFR from "./fr-FR.json";
import esES from "./es-ES.json";
import ptBR from "./pt-BR.json";
import ruRU from "./ru-RU.json";

const navigation = {
  "zh-CN": zhCN.shell,
  "zh-TW": zhTW.shell,
  "en-US": enUS.shell,
  "ja-JP": jaJP.shell,
  "de-DE": deDE.shell,
  "fr-FR": frFR.shell,
  "es-ES": esES.shell,
  "pt-BR": ptBR.shell,
  "ru-RU": ruRU.shell
} satisfies Record<SupportedLocale, typeof enUS.shell>;

const tourIds: FeatureTourId[] = [
  "overview",
  "issues",
  "conversations",
  "resources",
  "memories",
  "schedules",
  "changes"
];
const locales = Object.keys(navigation) as SupportedLocale[];

const conversationSemantics: Record<SupportedLocale, readonly [string, string, string, string]> = {
  "zh-CN": [
    "冻结共同前缀",
    "原对话保留全部历史（含分叉点后的消息）",
    "新分支从分叉点继续",
    "两条都可续聊"
  ],
  "zh-TW": [
    "凍結共同前綴",
    "原對話保留全部歷史（含分岔點之後的訊息）",
    "新分支從分岔點繼續",
    "兩條都可續聊"
  ],
  "en-US": [
    "freeze the shared prefix",
    "original conversation keeps its full history, including later messages",
    "a new branch continues from the fork point",
    "both can continue"
  ],
  "ja-JP": [
    "共通の履歴をその返信まで固定",
    "元の会話は分岐点より後のメッセージを含む全履歴を保ち",
    "新しい分岐は分岐点から始まり",
    "どちらも会話を続けられます"
  ],
  "de-DE": [
    "friere den gemeinsamen Verlauf bis dorthin ein",
    "ursprüngliche Unterhaltung behält den gesamten Verlauf einschließlich späterer Nachrichten",
    "der neue Zweig beginnt am Verzweigungspunkt",
    "beide lassen sich fortsetzen"
  ],
  "fr-FR": [
    "figer l’historique commun jusqu’à celle-ci",
    "conversation d’origine conserve tout son historique, y compris les messages suivants",
    "la nouvelle branche repart du point de bifurcation",
    "les deux peuvent se poursuivre"
  ],
  "es-ES": [
    "fijar el historial común hasta ese punto",
    "conversación original conserva todo su historial, incluidos los mensajes posteriores",
    "la nueva rama continúa desde el punto de bifurcación",
    "ambas pueden seguir"
  ],
  "pt-BR": [
    "congelar o histórico comum até esse ponto",
    "conversa original mantém todo o histórico, incluindo as mensagens posteriores",
    "o novo ramo começa no ponto de bifurcação",
    "ambos podem continuar"
  ],
  "ru-RU": [
    "зафиксируйте общую историю до него",
    "Исходный диалог сохраняет всю историю, включая последующие сообщения",
    "новая ветка начинается с точки развилки",
    "оба диалога можно продолжать"
  ]
};

describe("feature tour translations", () => {
  it("covers exactly the nine supported locales", () => {
    expect(Object.keys(featureTourTranslations).sort()).toEqual([...locales].sort());
  });

  it.each(locales)("provides complete, concise guides and navigation titles in %s", (locale) => {
    const messages = featureTourTranslations[locale];
    const { guides, ...controls } = messages;

    expect(Object.keys(guides).sort()).toEqual([...tourIds].sort());
    expect(Object.keys(controls).sort()).toEqual([
      "acknowledge",
      "intro",
      "pause",
      "play",
      "reopen",
      "replay",
      "saveFailed",
      "seenUnavailable"
    ]);
    for (const value of Object.values(controls)) {
      expect(value.trim()).not.toBe("");
      expect(value).toBe(value.trim());
    }
    for (const id of tourIds) {
      const guide = guides[id];
      expect(guide.title).toBe(navigation[locale][id === "overview" ? "workbench" : id]);
      expect(guide.description.trim()).not.toBe("");
      expect(guide.steps).toHaveLength(3);
      expect(new Set(guide.steps).size).toBe(3);
      for (const step of guide.steps) {
        expect(step.trim()).not.toBe("");
        expect(step).toBe(step.trim());
        expect(step.length).toBeLessThanOrEqual(40);
      }
    }
  });

  it.each(locales.filter((locale) => locale !== "en-US"))(
    "does not use English fallback copy in %s",
    (locale) => {
      const english = featureTourTranslations["en-US"];
      const { guides, ...controls } = featureTourTranslations[locale];
      for (const key of Object.keys(controls) as (keyof typeof controls)[]) {
        expect(controls[key]).not.toBe(english[key]);
      }
      for (const id of tourIds) {
        expect(guides[id].description).not.toBe(english.guides[id].description);
        expect(guides[id].steps).not.toEqual(english.guides[id].steps);
      }
    }
  );

  it.each(locales)("distinguishes preserved history from the new branch in %s", (locale) => {
    const description = featureTourTranslations[locale].guides.conversations.description;
    for (const meaning of conversationSemantics[locale]) {
      expect(description).toContain(meaning);
    }
  });

  it("describes current capabilities without inventing actions", () => {
    const zh = featureTourTranslations["zh-CN"];
    const en = featureTourTranslations["en-US"];
    expect(zh.guides.issues.description).toContain("已发布的结果");
    expect(en.guides.issues.description).toContain("published results");
    expect(zh.guides.resources.description).toContain("以实际检测为准");
    expect(en.guides.resources.description).toContain("actual checks");
    expect(zh.guides.memories.description).toMatch(/后续工作参考.*版本记录和归档/);
    expect(en.guides.memories.description).toMatch(/future reference.*versions and archive/);
    expect(zh.guides.schedules.description).toMatch(
      /时间、时区与执行 Agent.*新建任务或追加到已有任务/
    );
    expect(en.guides.schedules.description).toMatch(
      /time, time zone, and Agent.*new task or append/
    );
    expect(zh.guides.changes.description).toContain("只读审查，不修改仓库");
    expect(en.guides.changes.description).toMatch(/read-only.*does not modify/);
  });
});

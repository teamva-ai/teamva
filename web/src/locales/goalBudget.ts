import type { SupportedLocale } from "../lib/types";

const rows: Record<SupportedLocale, { exhausted: string }> = {
  "zh-CN": { exhausted: "Token 预算已耗尽。请取消此目标，再新建目标并设置新的预算。" },
  "zh-TW": { exhausted: "Token 預算已耗盡。請取消此目標，再新增目標並設定新的預算。" },
  "en-US": { exhausted: "Token budget exhausted. Cancel this goal, then create a new goal with a new budget." },
  "ja-JP": { exhausted: "Token 予算を使い切りました。この目標をキャンセルし、新しい予算で目標を作成してください。" },
  "de-DE": { exhausted: "Token-Budget aufgebraucht. Dieses Ziel abbrechen und ein neues Ziel mit neuem Budget erstellen." },
  "fr-FR": { exhausted: "Budget de tokens épuisé. Annulez cet objectif, puis créez-en un nouveau avec un nouveau budget." },
  "es-ES": { exhausted: "Presupuesto de tokens agotado. Cancela este objetivo y crea uno nuevo con otro presupuesto." },
  "pt-BR": { exhausted: "Orçamento de tokens esgotado. Cancele este objetivo e crie outro com um novo orçamento." },
  "ru-RU": { exhausted: "Бюджет токенов исчерпан. Отмените эту цель и создайте новую с новым бюджетом." }
};

export const goalBudgetTranslations = rows;

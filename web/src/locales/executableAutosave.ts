import type { SupportedLocale } from "../lib/types";

export const executableAutosaveTranslations = {
  "zh-CN": { hint: "输入完成后按回车或移开焦点，立即应用。", retryHint: "应用失败，已保留输入；按回车或移开焦点可重试。" },
  "zh-TW": { hint: "輸入完成後按 Enter 或移開焦點，立即套用。", retryHint: "套用失敗，已保留輸入；按 Enter 或移開焦點可重試。" },
  "en-US": { hint: "Press Enter or leave the field when finished to apply immediately.", retryHint: "Could not apply. Your input is preserved; press Enter or leave the field to retry." },
  "ja-JP": { hint: "入力が完了したら Enter を押すかフォーカスを外すと、すぐに適用されます。", retryHint: "適用できませんでした。入力は保持されています。Enter を押すかフォーカスを外して再試行してください。" },
  "de-DE": { hint: "Nach der Eingabe Enter drücken oder das Feld verlassen, um sofort anzuwenden.", retryHint: "Anwenden fehlgeschlagen. Die Eingabe bleibt erhalten; mit Enter oder Verlassen des Feldes erneut versuchen." },
  "fr-FR": { hint: "Une fois la saisie terminée, appuyez sur Entrée ou quittez le champ pour appliquer immédiatement.", retryHint: "Application impossible. Votre saisie est conservée ; appuyez sur Entrée ou quittez le champ pour réessayer." },
  "es-ES": { hint: "Al terminar, pulsa Intro o sal del campo para aplicar inmediatamente.", retryHint: "No se pudo aplicar. Se conserva lo escrito; pulsa Intro o sal del campo para reintentar." },
  "pt-BR": { hint: "Ao terminar, pressione Enter ou saia do campo para aplicar imediatamente.", retryHint: "Não foi possível aplicar. A entrada foi preservada; pressione Enter ou saia do campo para tentar novamente." },
  "ru-RU": { hint: "Закончив ввод, нажмите Enter или покиньте поле, чтобы применить сразу.", retryHint: "Не удалось применить. Ввод сохранён; нажмите Enter или покиньте поле для повторной попытки." }
} satisfies Record<SupportedLocale, { hint: string; retryHint: string }>;

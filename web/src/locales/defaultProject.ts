import type { SupportedLocale } from "../lib/types";

export const defaultProjectTranslations: Record<SupportedLocale, { badge: string; hint: string }> = {
  "zh-CN": { badge: "默认", hint: "这是默认项目，名称固定为“默认项目”，不可删除；仍可编辑说明。" },
  "zh-TW": { badge: "預設", hint: "這是預設專案，名稱固定為「默认项目」，不可刪除；仍可編輯說明。" },
  "en-US": { badge: "Default", hint: "The default project is named “默认项目” and cannot be renamed or deleted. Its description remains editable." },
  "ja-JP": { badge: "既定", hint: "既定のプロジェクト名は「默认项目」で、変更・削除できません。説明は編集できます。" },
  "de-DE": { badge: "Standard", hint: "Das Standardprojekt heißt „默认项目“ und kann nicht umbenannt oder gelöscht werden. Die Beschreibung bleibt bearbeitbar." },
  "fr-FR": { badge: "Par défaut", hint: "Le projet par défaut se nomme « 默认项目 » et ne peut être renommé ni supprimé. Sa description reste modifiable." },
  "es-ES": { badge: "Predeterminado", hint: "El proyecto predeterminado se llama «默认项目» y no se puede renombrar ni eliminar. Puedes editar su descripción." },
  "pt-BR": { badge: "Padrão", hint: "O projeto padrão se chama “默认项目” e não pode ser renomeado ou excluído. A descrição continua editável." },
  "ru-RU": { badge: "По умолчанию", hint: "Проект по умолчанию называется «默认项目»; его нельзя переименовать или удалить. Описание можно менять." }
};

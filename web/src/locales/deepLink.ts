import type { SupportedLocale } from "../lib/types";

const en = {
  memoryUnavailable: "Requested memory unavailable",
  memoryUnavailableHint: "This memory was not found in the current project and view. It may have been deleted, belong to another project, or require the other archived view. Choose a memory from the list or switch views.",
  graphUnavailable: "Requested conversation unavailable",
  graphUnavailableHint: "This conversation was not found in the current project. It may have been deleted or belong to another project. Choose a conversation from the list."
};

export const deepLinkTranslations: Record<SupportedLocale, typeof en> = {
  "en-US": en,
  "zh-CN": {
    memoryUnavailable: "无法打开指定的记忆",
    memoryUnavailableHint: "当前项目和视图中未找到这条记忆。它可能已被删除、属于其他项目，或位于另一归档视图。请选择列表中的记忆，或切换视图。",
    graphUnavailable: "无法打开指定的分支对话",
    graphUnavailableHint: "当前项目中未找到这个分支对话。它可能已被删除或属于其他项目，请选择列表中的分支对话。"
  },
  "zh-TW": {
    memoryUnavailable: "無法開啟指定的記憶",
    memoryUnavailableHint: "目前專案及檢視中找不到這條記憶。它可能已刪除、屬於其他專案，或位於另一個封存檢視。請選擇清單中的記憶，或切換檢視。",
    graphUnavailable: "無法開啟指定的分支對話",
    graphUnavailableHint: "目前專案中找不到這個分支對話。它可能已刪除或屬於其他專案，請選擇清單中的分支對話。"
  },
  "ja-JP": {
    memoryUnavailable: "指定されたメモリを開けません",
    memoryUnavailableHint: "現在のプロジェクトと表示にメモリが見つかりません。削除済み、別のプロジェクト、または別のアーカイブ表示にある可能性があります。一覧から選択するか表示を切り替えてください。",
    graphUnavailable: "指定された会話を開けません",
    graphUnavailableHint: "現在のプロジェクトに会話が見つかりません。削除済みか別のプロジェクトにある可能性があります。一覧から会話を選択してください。"
  },
  "de-DE": {
    memoryUnavailable: "Angeforderte Erinnerung nicht verfügbar",
    memoryUnavailableHint: "Diese Erinnerung wurde im aktuellen Projekt und in dieser Ansicht nicht gefunden. Sie wurde möglicherweise gelöscht, gehört zu einem anderen Projekt oder befindet sich in der anderen Archivansicht. Wählen Sie einen Eintrag oder wechseln Sie die Ansicht.",
    graphUnavailable: "Angeforderte Unterhaltung nicht verfügbar",
    graphUnavailableHint: "Diese Unterhaltung wurde im aktuellen Projekt nicht gefunden. Sie wurde möglicherweise gelöscht oder gehört zu einem anderen Projekt. Wählen Sie eine Unterhaltung aus der Liste."
  },
  "fr-FR": {
    memoryUnavailable: "Mémoire demandée indisponible",
    memoryUnavailableHint: "Cette mémoire est introuvable dans le projet et la vue actuels. Elle a peut-être été supprimée, appartient à un autre projet ou se trouve dans l’autre vue d’archives. Choisissez une mémoire ou changez de vue.",
    graphUnavailable: "Conversation demandée indisponible",
    graphUnavailableHint: "Cette conversation est introuvable dans le projet actuel. Elle a peut-être été supprimée ou appartient à un autre projet. Choisissez une conversation dans la liste."
  },
  "es-ES": {
    memoryUnavailable: "Memoria solicitada no disponible",
    memoryUnavailableHint: "No se encontró esta memoria en el proyecto y la vista actuales. Puede haberse eliminado, pertenecer a otro proyecto o estar en la otra vista de archivo. Selecciona una memoria de la lista o cambia de vista.",
    graphUnavailable: "Conversación solicitada no disponible",
    graphUnavailableHint: "No se encontró esta conversación en el proyecto actual. Puede haberse eliminado o pertenecer a otro proyecto. Selecciona una conversación de la lista."
  },
  "pt-BR": {
    memoryUnavailable: "Memória solicitada indisponível",
    memoryUnavailableHint: "Esta memória não foi encontrada no projeto e na visualização atuais. Ela pode ter sido excluída, pertencer a outro projeto ou estar na outra visualização de arquivo. Selecione uma memória na lista ou altere a visualização.",
    graphUnavailable: "Conversa solicitada indisponível",
    graphUnavailableHint: "Esta conversa não foi encontrada no projeto atual. Ela pode ter sido excluída ou pertencer a outro projeto. Selecione uma conversa na lista."
  },
  "ru-RU": {
    memoryUnavailable: "Запрошенная память недоступна",
    memoryUnavailableHint: "Запись не найдена в текущем проекте и представлении. Возможно, она удалена, относится к другому проекту или находится в другом представлении архива. Выберите запись из списка или смените представление.",
    graphUnavailable: "Запрошенный диалог недоступен",
    graphUnavailableHint: "Диалог не найден в текущем проекте. Возможно, он удалён или относится к другому проекту. Выберите диалог из списка."
  }
};

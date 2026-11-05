import type { SupportedLocale } from "../lib/types";

const en = {
  unavailable: "Cannot browse this directory; make sure it is within the current user’s home directory and you have read permission.",
  expired: "The directory command has expired. Please retry.",
  scopeHint: "Directory browsing is limited to the current user’s home directory."
};
type Messages = typeof en;

export const directoryPickerTranslations: Record<SupportedLocale, Messages> = {
  "en-US": en,
  "zh-CN": {
    unavailable: "无法浏览此目录；请确认位于当前用户主目录内且有读取权限。",
    expired: "目录命令已失效，请重试。",
    scopeHint: "目录浏览范围仅限当前用户主目录。"
  },
  "zh-TW": {
    unavailable: "無法瀏覽此目錄；請確認位於目前使用者主目錄內且有讀取權限。",
    expired: "目錄命令已失效，請重試。",
    scopeHint: "目錄瀏覽範圍僅限目前使用者主目錄。"
  },
  "ja-JP": {
    unavailable: "このディレクトリを参照できません。現在のユーザーのホームディレクトリ内にあり、読み取り権限があることを確認してください。",
    expired: "ディレクトリコマンドの有効期限が切れました。再試行してください。",
    scopeHint: "参照できる範囲は現在のユーザーのホームディレクトリ内に限られます。"
  },
  "de-DE": {
    unavailable: "Dieses Verzeichnis kann nicht durchsucht werden. Es muss im persönlichen Verzeichnis des aktuellen Benutzers liegen und lesbar sein.",
    expired: "Der Verzeichnisbefehl ist abgelaufen. Bitte erneut versuchen.",
    scopeHint: "Es können nur Verzeichnisse im persönlichen Verzeichnis des aktuellen Benutzers durchsucht werden."
  },
  "fr-FR": {
    unavailable: "Impossible de parcourir ce dossier. Vérifiez qu’il se trouve dans le dossier personnel de l’utilisateur actuel et que vous disposez des droits de lecture.",
    expired: "La commande de dossier a expiré. Réessayez.",
    scopeHint: "La navigation est limitée au dossier personnel de l’utilisateur actuel."
  },
  "es-ES": {
    unavailable: "No se puede explorar este directorio. Comprueba que esté dentro del directorio personal del usuario actual y que tengas permiso de lectura.",
    expired: "El comando de directorio ha caducado. Vuelve a intentarlo.",
    scopeHint: "La exploración se limita al directorio personal del usuario actual."
  },
  "pt-BR": {
    unavailable: "Não foi possível explorar este diretório. Verifique se ele está na pasta pessoal do usuário atual e se você tem permissão de leitura.",
    expired: "O comando de diretório expirou. Tente novamente.",
    scopeHint: "A navegação está limitada à pasta pessoal do usuário atual."
  },
  "ru-RU": {
    unavailable: "Не удалось открыть каталог. Убедитесь, что он находится в домашнем каталоге текущего пользователя и у вас есть права на чтение.",
    expired: "Срок действия команды каталога истёк. Повторите попытку.",
    scopeHint: "Просмотр ограничен домашним каталогом текущего пользователя."
  }
};

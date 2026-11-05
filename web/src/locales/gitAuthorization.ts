import type { SupportedLocale } from "../lib/types";

const en = {
  title: "Resource authorization needs checking",
  description: "Git access was denied by the resource authorization check, not a network disconnection. Check and explicitly revalidate the directories on this project’s Resources page, then return and refresh. No resource will be authorized automatically.",
  paused: "Repository discovery and automatic reconnection are paused until resource authorization is checked.",
  goToResources: "Check this project’s resources"
};
type Messages = typeof en;

export const gitAuthorizationTranslations: Record<SupportedLocale, Messages> = {
  "en-US": en,
  "zh-CN": {
    title: "资源授权需检查",
    description: "资源授权检查拒绝了 Git 访问，并非网络断开。请到当前项目资源页检查并显式重新验证目录，然后返回刷新。不会自动授权任何资源。",
    paused: "仓库发现和自动重连已暂停，请先检查资源授权。",
    goToResources: "前往当前项目资源页"
  },
  "zh-TW": {
    title: "資源授權需檢查",
    description: "資源授權檢查拒絕了 Git 存取，並非網路中斷。請到目前專案資源頁檢查並明確重新驗證目錄，再返回重新整理。不會自動授權任何資源。",
    paused: "儲存庫探索和自動重新連線已暫停，請先檢查資源授權。",
    goToResources: "前往目前專案資源頁"
  },
  "ja-JP": {
    title: "リソースの認可を確認してください",
    description: "Git へのアクセスはリソースの認可確認で拒否されました。ネットワーク切断ではありません。このプロジェクトのリソースページでディレクトリを明示的に再検証し、戻って更新してください。自動で認可されるリソースはありません。",
    paused: "リソースの認可を確認するまで、リポジトリ検出と自動再接続は停止しています。",
    goToResources: "このプロジェクトのリソースを確認"
  },
  "de-DE": {
    title: "Ressourcenberechtigung prüfen",
    description: "Die Ressourcenprüfung hat den Git-Zugriff verweigert; es liegt kein Verbindungsabbruch vor. Prüfen und validieren Sie die Verzeichnisse auf der Ressourcenseite dieses Projekts erneut und aktualisieren Sie danach diese Seite. Ressourcen werden nicht automatisch autorisiert.",
    paused: "Repository-Suche und automatische Neuverbindung sind bis zur Prüfung der Ressourcenberechtigung angehalten.",
    goToResources: "Ressourcen dieses Projekts prüfen"
  },
  "fr-FR": {
    title: "Vérifier les autorisations des ressources",
    description: "L’accès Git a été refusé par le contrôle des ressources, et non par une coupure réseau. Vérifiez et revalidez explicitement les dossiers dans les ressources de ce projet, puis revenez actualiser. Aucune ressource ne sera autorisée automatiquement.",
    paused: "La découverte des dépôts et la reconnexion automatique sont suspendues en attendant la vérification des autorisations.",
    goToResources: "Vérifier les ressources de ce projet"
  },
  "es-ES": {
    title: "Revisar la autorización de recursos",
    description: "La comprobación de recursos denegó el acceso a Git; no se trata de una desconexión de red. Revisa y vuelve a validar explícitamente los directorios en los recursos de este proyecto; después regresa y actualiza. No se autorizarán recursos automáticamente.",
    paused: "La búsqueda de repositorios y la reconexión automática están pausadas hasta revisar la autorización.",
    goToResources: "Revisar los recursos de este proyecto"
  },
  "pt-BR": {
    title: "Verifique a autorização dos recursos",
    description: "A verificação dos recursos negou o acesso ao Git; não é uma desconexão de rede. Verifique e revalide explicitamente os diretórios nos recursos deste projeto, depois volte e atualize. Nenhum recurso será autorizado automaticamente.",
    paused: "A descoberta de repositórios e a reconexão automática estão pausadas até verificar a autorização.",
    goToResources: "Verificar os recursos deste projeto"
  },
  "ru-RU": {
    title: "Проверьте разрешения ресурсов",
    description: "Проверка разрешений ресурсов отклонила доступ к Git. Это не разрыв сетевого соединения. Явно проверьте каталоги повторно на странице ресурсов текущего проекта, затем вернитесь и обновите данные. Автоматическое разрешение доступа не выполняется.",
    paused: "Поиск репозиториев и автоматическое переподключение приостановлены до проверки разрешений ресурсов.",
    goToResources: "Проверить ресурсы текущего проекта"
  }
};

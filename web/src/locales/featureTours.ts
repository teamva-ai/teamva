import type { SupportedLocale } from "../lib/types";

export type FeatureTourId =
  "overview" | "issues" | "conversations" | "resources" | "memories" | "schedules" | "changes";

export interface FeatureTourGuide {
  title: string;
  description: string;
  steps: readonly [string, string, string];
}

export interface FeatureTourMessages {
  intro: string;
  acknowledge: string;
  pause: string;
  play: string;
  replay: string;
  saveFailed: string;
  seenUnavailable: string;
  reopen: string;
  guides: Record<FeatureTourId, FeatureTourGuide>;
}

// The animation uses simulated data; these guides describe current capabilities only.
// Keep each guide compact so the nine dictionaries are easy to compare.
// prettier-ignore
export const featureTourTranslations: Record<SupportedLocale, FeatureTourMessages> = {
  "zh-CN": {
    intro: "用几秒钟，认识这个栏目",
    acknowledge: "开始使用",
    pause: "暂停动画",
    play: "播放动画",
    replay: "重新播放",
    saveFailed: "暂时无法保存已看状态，下次进入时可能再次显示。",
    seenUnavailable: "暂时无法读取引导状态，仍可手动查看演示。",
    reopen: "了解这个栏目",
    guides: {
      overview: {
        title: "概览",
        description: "集中查看项目 Agent 状态、进行中的任务与资源入口，快速找到下一步工作。",
        steps: ["查看运行状态", "跟进当前任务", "打开项目资源"],
      },
      issues: {
        title: "任务",
        description: "创建协作任务，用 @Agent 邀请执行。公开讨论与已发布的结果集中留在任务中，方便追踪进展。",
        steps: ["创建协作任务", "点名协作执行", "追踪讨论结果"],
      },
      conversations: {
        title: "分支对话",
        description: "从某条聊天回复分叉，冻结共同前缀。原对话保留全部历史（含分叉点后的消息），新分支从分叉点继续，两条都可续聊。",
        steps: ["选择回复分叉", "冻结共同前缀", "分别继续对话"],
      },
      resources: {
        title: "资源",
        description: "添加代码目录、资料目录或网址，为 Agent 提供工作上下文。目录权限与 Git 识别以实际检测为准。",
        steps: ["添加工作资源", "确认目录权限", "提供项目上下文"],
      },
      memories: {
        title: "项目记忆",
        description: "保存项目约定与知识，供后续工作参考。通过版本记录和归档管理内容，让重要信息有据可查。",
        steps: ["保存约定知识", "查看历史版本", "归档过时内容"],
      },
      schedules: {
        title: "定时任务",
        description: "设置时间、时区与执行 Agent，按计划新建任务或追加到已有任务。每次触发与执行都有记录可查。",
        steps: ["设置时间时区", "选择执行助手", "追踪任务执行"],
      },
      changes: {
        title: "变更",
        description: "浏览已注册 Git 仓库的文件树，挑选文件查看差异。当前仅支持只读审查，不修改仓库。",
        steps: ["浏览仓库文件", "挑选审查文件", "查看只读差异"],
      },
    },
  },
  "zh-TW": {
    intro: "用幾秒鐘，認識這個欄目",
    acknowledge: "開始使用",
    pause: "暫停動畫",
    play: "播放動畫",
    replay: "重新播放",
    saveFailed: "暫時無法儲存已看狀態，下次進入時可能再次顯示。",
    seenUnavailable: "暫時無法讀取導覽狀態，仍可手動查看示範。",
    reopen: "認識這個欄目",
    guides: {
      overview: {
        title: "概覽",
        description: "集中查看專案 Agent 狀態、進行中的任務與資源入口，快速找到下一步工作。",
        steps: ["查看執行狀態", "跟進目前任務", "開啟專案資源"],
      },
      issues: {
        title: "任務",
        description: "建立協作任務，用 @Agent 邀請執行。公開討論與已發布的結果集中保留在任務中，方便追蹤進度。",
        steps: ["建立協作任務", "點名協作執行", "追蹤討論結果"],
      },
      conversations: {
        title: "分支對話",
        description: "從某則聊天回覆分岔，凍結共同前綴。原對話保留全部歷史（含分岔點之後的訊息），新分支從分岔點繼續，兩條都可續聊。",
        steps: ["選擇回覆分岔", "凍結共同前綴", "分別繼續對話"],
      },
      resources: {
        title: "資源",
        description: "新增程式碼目錄、資料目錄或網址，為 Agent 提供工作脈絡。目錄權限與 Git 辨識以實際偵測為準。",
        steps: ["新增工作資源", "確認目錄權限", "提供專案脈絡"],
      },
      memories: {
        title: "專案記憶",
        description: "儲存專案約定與知識，供後續工作參考。透過版本紀錄與封存管理內容，讓重要資訊有據可查。",
        steps: ["儲存約定知識", "查看歷史版本", "封存過時內容"],
      },
      schedules: {
        title: "定時任務",
        description: "設定時間、時區與執行 Agent，依排程建立任務或附加到既有任務。每次觸發與執行都有紀錄可查。",
        steps: ["設定時間時區", "選擇執行助手", "追蹤任務執行"],
      },
      changes: {
        title: "變更",
        description: "瀏覽已登錄 Git 儲存庫的檔案樹，挑選檔案查看差異。目前僅支援唯讀審查，不修改儲存庫。",
        steps: ["瀏覽儲存庫檔案", "挑選審查檔案", "查看唯讀差異"],
      },
    },
  },
  "en-US": {
    intro: "Get to know this section in a few seconds",
    acknowledge: "Get started",
    pause: "Pause animation",
    play: "Play animation",
    replay: "Replay animation",
    saveFailed: "Your viewed status could not be saved right now. This guide may appear again next time.",
    seenUnavailable: "Guide status is temporarily unavailable. You can still open the demo manually.",
    reopen: "Explore this section",
    guides: {
      overview: {
        title: "Overview",
        description: "See project Agent status, active tasks, and resource shortcuts in one place to find your next step.",
        steps: ["View Agent status", "Follow active tasks", "Open project resources"],
      },
      issues: {
        title: "Tasks",
        description: "Create a collaborative task and mention @Agent to get work started. Follow public discussion and published results in the task.",
        steps: ["Create a task", "Mention an Agent", "Track discussion and results"],
      },
      conversations: {
        title: "Graph",
        description: "Fork from a chat reply to freeze the shared prefix. The original conversation keeps its full history, including later messages; a new branch continues from the fork point, and both can continue.",
        steps: ["Fork a reply", "Freeze the parent prefix", "Continue both branches"],
      },
      resources: {
        title: "Resources",
        description: "Add code directories, document directories, or URLs as working context for Agents. Directory permissions and Git detection reflect actual checks.",
        steps: ["Add work resources", "Check directory permissions", "Share project context"],
      },
      memories: {
        title: "Memories",
        description: "Save project conventions and knowledge for future reference. Review versions and archive content to keep important information traceable.",
        steps: ["Save project knowledge", "Review past versions", "Archive outdated content"],
      },
      schedules: {
        title: "Scheduled tasks",
        description: "Choose a time, time zone, and Agent to create a new task or append to an existing one on schedule. Each trigger and run is traceable.",
        steps: ["Set time & timezone", "Choose an Agent", "Track task runs"],
      },
      changes: {
        title: "Changes",
        description: "Browse the file tree of a registered Git repository and select files to inspect their diffs. Review is currently read-only and does not modify the repository.",
        steps: ["Browse repository files", "Select review files", "Inspect read-only diffs"],
      },
    },
  },
  "ja-JP": {
    intro: "このセクションを数秒でご紹介",
    acknowledge: "使い始める",
    pause: "アニメーションを一時停止",
    play: "アニメーションを再生",
    replay: "最初から再生",
    saveFailed: "閲覧済みの状態を保存できませんでした。次回もこのガイドが表示される場合があります。",
    seenUnavailable: "ガイドの状態を一時的に取得できません。デモは手動で開けます。",
    reopen: "このセクションについて",
    guides: {
      overview: {
        title: "概要",
        description: "プロジェクトの Agent の状態、進行中のタスク、リソースへの入口をまとめて確認し、次の作業を見つけられます。",
        steps: ["稼働状況を確認", "進行中のタスク", "リソースを開く"],
      },
      issues: {
        title: "タスク",
        description: "共同作業のタスクを作成し、@Agent で実行を依頼します。公開の議論と投稿された結果をタスク内で追跡できます。",
        steps: ["タスクを作成", "Agent に依頼", "議論と結果を確認"],
      },
      conversations: {
        title: "グラフ対話",
        description: "チャットの返信から分岐し、共通の履歴をその返信まで固定します。元の会話は分岐点より後のメッセージを含む全履歴を保ち、新しい分岐は分岐点から始まり、どちらも会話を続けられます。",
        steps: ["返信から分岐", "共通の履歴を固定", "それぞれ会話を継続"],
      },
      resources: {
        title: "リソース",
        description: "コード用・資料用のディレクトリや URL を追加し、Agent の作業に必要な情報を提供します。ディレクトリの権限と Git の判定には実際の検出結果が使われます。",
        steps: ["リソースを追加", "ディレクトリ権限を確認", "作業の情報を共有"],
      },
      memories: {
        title: "プロジェクトメモリ",
        description: "プロジェクトの決まりや知識を保存し、今後の作業で参照できます。過去のバージョンの確認やアーカイブで情報を整理できます。",
        steps: ["決まりと知識を保存", "過去の版を確認", "古い情報をアーカイブ"],
      },
      schedules: {
        title: "予定タスク",
        description: "時刻・タイムゾーン・実行する Agent を選び、予定に沿ってタスクを新規作成、または既存のタスクに追記します。各起動と実行の記録を確認できます。",
        steps: ["時刻とタイムゾーンを設定", "実行 Agent を選択", "タスクの実行を確認"],
      },
      changes: {
        title: "変更",
        description: "登録済みの Git リポジトリのファイルツリーを開き、ファイルを選んで差分を確認できます。現在は読み取り専用のレビューで、リポジトリは変更しません。",
        steps: ["ファイルツリーを開く", "確認するファイルを選択", "読み取り専用で差分確認"],
      },
    },
  },
  "de-DE": {
    intro: "Lerne diesen Bereich in wenigen Sekunden kennen",
    acknowledge: "Loslegen",
    pause: "Animation pausieren",
    play: "Animation abspielen",
    replay: "Erneut abspielen",
    saveFailed: "Der Anzeigestatus konnte gerade nicht gespeichert werden. Diese Einführung erscheint beim nächsten Besuch möglicherweise erneut.",
    seenUnavailable: "Der Status der Einführung ist vorübergehend nicht verfügbar. Die Demo lässt sich weiterhin manuell öffnen.",
    reopen: "Diesen Bereich kennenlernen",
    guides: {
      overview: {
        title: "Übersicht",
        description: "Sieh den Status der Projekt-Agents, laufende Aufgaben und Ressourcenzugänge auf einen Blick und finde deinen nächsten Arbeitsschritt.",
        steps: ["Agent-Status prüfen", "Laufende Aufgaben verfolgen", "Projektressourcen öffnen"],
      },
      issues: {
        title: "Aufgaben",
        description: "Erstelle eine gemeinsame Aufgabe und beauftrage einen Agent per @Agent. Verfolge öffentliche Diskussionen und veröffentlichte Ergebnisse direkt in der Aufgabe.",
        steps: ["Aufgabe erstellen", "Agent erwähnen", "Diskussion und Ergebnisse verfolgen"],
      },
      conversations: {
        title: "Graph",
        description: "Verzweige ab einer Chatantwort und friere den gemeinsamen Verlauf bis dorthin ein. Die ursprüngliche Unterhaltung behält den gesamten Verlauf einschließlich späterer Nachrichten; der neue Zweig beginnt am Verzweigungspunkt, und beide lassen sich fortsetzen.",
        steps: ["Ab Antwort verzweigen", "Gemeinsamen Verlauf einfrieren", "Beide Zweige fortsetzen"],
      },
      resources: {
        title: "Ressourcen",
        description: "Füge Code- oder Dokumentverzeichnisse sowie URLs als Arbeitskontext für Agents hinzu. Verzeichnisrechte und Git-Erkennung beruhen auf tatsächlichen Prüfungen.",
        steps: ["Arbeitsressourcen hinzufügen", "Verzeichnisrechte prüfen", "Projektkontext bereitstellen"],
      },
      memories: {
        title: "Projekterinnerungen",
        description: "Speichere Projektkonventionen und Wissen als Referenz für spätere Arbeiten. Mit Versionen und Archivierung bleiben wichtige Informationen nachvollziehbar.",
        steps: ["Projektwissen speichern", "Frühere Versionen ansehen", "Veraltete Inhalte archivieren"],
      },
      schedules: {
        title: "Geplante Aufgaben",
        description: "Wähle Zeit, Zeitzone und Agent, um planmäßig eine Aufgabe zu erstellen oder eine bestehende zu ergänzen. Jede Auslösung und Ausführung ist nachvollziehbar.",
        steps: ["Zeit und Zeitzone festlegen", "Agent auswählen", "Aufgabenausführungen verfolgen"],
      },
      changes: {
        title: "Änderungen",
        description: "Durchsuche den Dateibaum eines registrierten Git-Repositorys und wähle Dateien für den Änderungsvergleich aus. Die Prüfung ist derzeit schreibgeschützt und verändert das Repository nicht.",
        steps: ["Repository-Dateien durchsuchen", "Dateien zur Prüfung auswählen", "Unterschiede schreibgeschützt prüfen"],
      },
    },
  },
  "fr-FR": {
    intro: "Découvrez cette rubrique en quelques secondes",
    acknowledge: "Commencer",
    pause: "Mettre l’animation en pause",
    play: "Lire l’animation",
    replay: "Revoir l’animation",
    saveFailed: "Impossible d’enregistrer le statut de lecture pour le moment. Ce guide pourrait réapparaître à votre prochaine visite.",
    seenUnavailable: "Le statut du guide est momentanément indisponible. Vous pouvez toujours ouvrir la démo manuellement.",
    reopen: "Découvrir cette rubrique",
    guides: {
      overview: {
        title: "Vue d’ensemble",
        description: "Retrouvez l’état des Agents du projet, les tâches en cours et les accès aux ressources pour choisir votre prochaine action.",
        steps: ["Voir l’état des Agents", "Suivre les tâches actives", "Ouvrir les ressources"],
      },
      issues: {
        title: "Tâches",
        description: "Créez une tâche collaborative et mentionnez @Agent pour lancer le travail. Suivez les discussions publiques et les résultats publiés dans la tâche.",
        steps: ["Créer une tâche", "Mentionner un Agent", "Suivre discussions et résultats"],
      },
      conversations: {
        title: "Graphe",
        description: "Bifurquez depuis une réponse pour figer l’historique commun jusqu’à celle-ci. La conversation d’origine conserve tout son historique, y compris les messages suivants ; la nouvelle branche repart du point de bifurcation, et les deux peuvent se poursuivre.",
        steps: ["Bifurquer depuis une réponse", "Figer l’historique commun", "Poursuivre les deux branches"],
      },
      resources: {
        title: "Ressources",
        description: "Ajoutez des dossiers de code, de documentation ou des URL comme contexte de travail des Agents. Les droits des dossiers et la détection Git reposent sur des vérifications réelles.",
        steps: ["Ajouter des ressources", "Vérifier les droits d’accès", "Fournir le contexte"],
      },
      memories: {
        title: "Mémoire du projet",
        description: "Conservez les conventions et connaissances du projet pour les travaux à venir. Consultez les versions et archivez les contenus pour garder des informations traçables.",
        steps: ["Conserver les connaissances", "Consulter les versions", "Archiver les contenus obsolètes"],
      },
      schedules: {
        title: "Tâches planifiées",
        description: "Choisissez l’heure, le fuseau horaire et l’Agent pour créer une tâche ou compléter une tâche existante selon un calendrier. Chaque déclenchement et chaque exécution sont traçables.",
        steps: ["Régler heure et fuseau", "Choisir un Agent", "Suivre les exécutions"],
      },
      changes: {
        title: "Modifications",
        description: "Parcourez l’arborescence d’un dépôt Git enregistré et sélectionnez des fichiers pour examiner leurs différences. La revue est actuellement en lecture seule et ne modifie pas le dépôt.",
        steps: ["Parcourir les fichiers", "Choisir les fichiers", "Examiner les différences"],
      },
    },
  },
  "es-ES": {
    intro: "Conoce esta sección en unos segundos",
    acknowledge: "Empezar",
    pause: "Pausar animación",
    play: "Reproducir animación",
    replay: "Volver a reproducir",
    saveFailed: "No se ha podido guardar que ya has visto la guía. Puede volver a aparecer en tu próxima visita.",
    seenUnavailable: "El estado de la guía no está disponible temporalmente. Puedes abrir la demostración de forma manual.",
    reopen: "Conocer esta sección",
    guides: {
      overview: {
        title: "Resumen",
        description: "Consulta el estado de los Agents del proyecto, las tareas en curso y los accesos a recursos para decidir tu siguiente paso.",
        steps: ["Ver estado de Agents", "Seguir tareas activas", "Abrir recursos del proyecto"],
      },
      issues: {
        title: "Tareas",
        description: "Crea una tarea colaborativa y menciona a @Agent para ponerla en marcha. Sigue la discusión pública y los resultados publicados en la tarea.",
        steps: ["Crear una tarea", "Mencionar a un Agent", "Seguir debates y resultados"],
      },
      conversations: {
        title: "Grafo",
        description: "Bifurca desde una respuesta para fijar el historial común hasta ese punto. La conversación original conserva todo su historial, incluidos los mensajes posteriores; la nueva rama continúa desde el punto de bifurcación, y ambas pueden seguir.",
        steps: ["Bifurcar desde una respuesta", "Fijar el historial común", "Continuar ambas ramas"],
      },
      resources: {
        title: "Recursos",
        description: "Añade directorios de código, de documentación o URL como contexto de trabajo para los Agents. Los permisos de directorio y la detección de Git se basan en comprobaciones reales.",
        steps: ["Añadir recursos de trabajo", "Comprobar permisos de directorio", "Aportar contexto del proyecto"],
      },
      memories: {
        title: "Memoria del proyecto",
        description: "Guarda convenciones y conocimientos del proyecto para futuras consultas. Revisa versiones y archiva contenido para mantener la información importante localizable.",
        steps: ["Guardar conocimientos del proyecto", "Revisar versiones anteriores", "Archivar contenido obsoleto"],
      },
      schedules: {
        title: "Tareas programadas",
        description: "Elige hora, zona horaria y Agent para crear una tarea o añadir contenido a una existente según la programación. Cada activación y ejecución se puede rastrear.",
        steps: ["Configurar hora y zona", "Elegir un Agent", "Seguir las ejecuciones"],
      },
      changes: {
        title: "Cambios",
        description: "Explora el árbol de un repositorio Git registrado y selecciona archivos para ver sus diferencias. La revisión es actualmente de solo lectura y no modifica el repositorio.",
        steps: ["Explorar archivos del repositorio", "Seleccionar archivos para revisar", "Ver las diferencias"],
      },
    },
  },
  "pt-BR": {
    intro: "Conheça esta seção em poucos segundos",
    acknowledge: "Começar a usar",
    pause: "Pausar animação",
    play: "Reproduzir animação",
    replay: "Reproduzir novamente",
    saveFailed: "Não foi possível salvar que você já viu este guia. Ele pode aparecer novamente na próxima visita.",
    seenUnavailable: "O status do guia está temporariamente indisponível. Você ainda pode abrir a demonstração manualmente.",
    reopen: "Conhecer esta seção",
    guides: {
      overview: {
        title: "Visão geral",
        description: "Veja o status dos Agents do projeto, as tarefas em andamento e os acessos aos recursos para decidir o próximo passo.",
        steps: ["Ver status dos Agents", "Acompanhar tarefas em andamento", "Abrir recursos do projeto"],
      },
      issues: {
        title: "Tarefas",
        description: "Crie uma tarefa colaborativa e mencione @Agent para iniciar o trabalho. Acompanhe a discussão pública e os resultados publicados na tarefa.",
        steps: ["Criar uma tarefa", "Mencionar um Agent", "Acompanhar discussões e resultados"],
      },
      conversations: {
        title: "Grafo",
        description: "Bifurque de uma resposta para congelar o histórico comum até esse ponto. A conversa original mantém todo o histórico, incluindo as mensagens posteriores; o novo ramo começa no ponto de bifurcação, e ambos podem continuar.",
        steps: ["Bifurcar de uma resposta", "Congelar o histórico comum", "Continuar os dois ramos"],
      },
      resources: {
        title: "Recursos",
        description: "Adicione diretórios de código, de documentação ou URLs como contexto de trabalho dos Agents. As permissões dos diretórios e a detecção de Git refletem verificações reais.",
        steps: ["Adicionar recursos de trabalho", "Verificar permissões dos diretórios", "Fornecer contexto do projeto"],
      },
      memories: {
        title: "Memória do projeto",
        description: "Salve convenções e conhecimentos do projeto para consultas futuras. Revise versões e arquive conteúdo para manter as informações importantes rastreáveis.",
        steps: ["Salvar conhecimentos do projeto", "Revisar versões anteriores", "Arquivar conteúdo desatualizado"],
      },
      schedules: {
        title: "Tarefas agendadas",
        description: "Escolha horário, fuso e Agent para criar uma tarefa ou acrescentar conteúdo a uma existente conforme o agendamento. Cada acionamento e execução fica registrado.",
        steps: ["Definir horário e fuso", "Escolher um Agent", "Acompanhar as execuções"],
      },
      changes: {
        title: "Alterações",
        description: "Explore a árvore de um repositório Git registrado e selecione arquivos para ver as diferenças. A revisão é atualmente somente leitura e não modifica o repositório.",
        steps: ["Explorar arquivos do repositório", "Selecionar arquivos para revisão", "Ver as diferenças"],
      },
    },
  },
  "ru-RU": {
    intro: "Познакомьтесь с разделом за несколько секунд",
    acknowledge: "Начать работу",
    pause: "Приостановить анимацию",
    play: "Воспроизвести анимацию",
    replay: "Воспроизвести заново",
    saveFailed: "Не удалось сохранить отметку о просмотре. При следующем посещении это руководство может появиться снова.",
    seenUnavailable: "Статус руководства временно недоступен. Демонстрацию по-прежнему можно открыть вручную.",
    reopen: "Познакомиться с разделом",
    guides: {
      overview: {
        title: "Обзор",
        description: "Смотрите состояние Agents проекта, текущие задачи и ссылки на ресурсы в одном месте, чтобы выбрать следующий шаг.",
        steps: ["Проверить состояние Agents", "Следить за текущими задачами", "Открыть ресурсы проекта"],
      },
      issues: {
        title: "Задачи",
        description: "Создайте совместную задачу и упомяните @Agent, чтобы поручить работу. Следите за открытым обсуждением и опубликованными результатами в задаче.",
        steps: ["Создать задачу", "Упомянуть Agent", "Отслеживать обсуждение и результаты"],
      },
      conversations: {
        title: "Граф",
        description: "Создайте развилку от ответа в чате и зафиксируйте общую историю до него. Исходный диалог сохраняет всю историю, включая последующие сообщения; новая ветка начинается с точки развилки, и оба диалога можно продолжать.",
        steps: ["Создать развилку от ответа", "Зафиксировать общую историю", "Продолжить обе ветки"],
      },
      resources: {
        title: "Ресурсы",
        description: "Добавляйте каталоги кода, документов или URL как рабочий контекст для Agents. Права на каталоги и наличие Git определяются реальными проверками.",
        steps: ["Добавить рабочие ресурсы", "Проверить права на каталоги", "Предоставить контекст проекта"],
      },
      memories: {
        title: "Память проекта",
        description: "Сохраняйте договорённости и знания проекта для будущей работы. Просматривайте версии и архивируйте материалы, чтобы отслеживать важную информацию.",
        steps: ["Сохранить знания проекта", "Просмотреть прошлые версии", "Архивировать устаревшие материалы"],
      },
      schedules: {
        title: "Запланированные задачи",
        description: "Выберите время, часовой пояс и Agent, чтобы по расписанию создавать задачу или дополнять существующую. Каждое срабатывание и выполнение можно отследить.",
        steps: ["Указать время и пояс", "Выбрать Agent", "Отследить выполнение задач"],
      },
      changes: {
        title: "Изменения",
        description: "Просматривайте дерево зарегистрированного Git-репозитория и выбирайте файлы для сравнения изменений. Сейчас доступен только просмотр без изменения репозитория.",
        steps: ["Открыть дерево файлов", "Выбрать файлы для проверки", "Просмотреть различия"],
      },
    },
  },
};

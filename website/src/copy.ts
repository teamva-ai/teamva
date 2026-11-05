const en = {
  title: "Teamva — Your agents. One superpower.",
  description: "A local-first workspace for the Super-agent era. Automatically detect supported coding CLIs on your computer, coordinate agent teams, and branch conversations.",
  skip: "Skip to content", home: "Teamva home", preview: "PREVIEW",
  nav: { workspace: "Workspace", research: "Super-agent era", why: "Why Teamva", docs: "Documentation", start: "Start building", open: "Open navigation", close: "Close navigation", label: "Primary navigation" },
  hero: {
    badge: "THE DEEPSEEK SUPER-AGENT MOMENT. YOUR LOCAL WORKSPACE.",
    line1: "Your agents.", line2: "One superpower.",
    description: "Bring your AI team together around one goal.",
    detail: "Give your agents a shared mission—and give yourself room to think.",
    primary: "Build your agent workspace", secondary: "Explore the product",
    proof: ["Local-first", "Multiple coding agents", "macOS + browser"],
    previewTitle: "One team. Step in when it matters.",
    selector: "Workspace views", tasks: "Teamwork", intervention: "Step in", graph: "Graph chats",
    taskAlt: "Product Manager, Frontend Developer, Backend Developer, and QA Engineer discuss a shared task with public handoffs, combining deep analysis and efficient execution",
    interventionAlt: "Frontend Developer’s session sidebar with conversation history, a change of direction, and a follow-up draft beside the shared task",
    graphAlt: "Teamva graph conversations branching into guided and expert onboarding explorations"
  },
  era: {
    kicker: "01 / THE SUPER-AGENT MOMENT", side: "LESS ORCHESTRATING. MORE CREATING.",
    heading: "Capable agents.\nCoordinated work.", emphasis: "One direction.\nShared momentum.",
    chip: "DEEPSEEK RESEARCH · OCTOBER 31, 2026", title: "Super-agent ambition. A local-first direction.",
    paragraphs: [
      "DeepSeek's Super-agent research explores how parallel work, event-driven execution, and coordinated sessions can help capable agents work together.",
      "Teamva brings the coordination problem to your desktop: explicit task assignments, independent agent sessions, branchable conversations, and visible project context. Keep your attention on the direction while the team moves work forward."
    ],
    source: "Read DeepSeek's research prototype",
    disclaimer: "Teamva is an independently developed local workspace. The research linked here is by DeepSeek.",
    principles: ["You set the direction.", "Specialists take the work.", "You review the evidence."]
  },
  engines: {
    kicker: "USE THE CODING TOOLS YOU ALREADY HAVE", title: "Already installed? Already part of your workflow.",
    body: "Teamva automatically detects supported coding CLIs on your computer and connects them to the workspace. Keep using your Codex or OpenCode setup, including supported local authentication and model-provider configuration.",
    cards: [
      { name: "Codex", note: "Detect the installed CLI. Reuse supported local sign-in and configuration." },
      { name: "OpenCode", note: "Detect your installed CLI, or use the bundled runtime. Keep supported model-provider configuration." },
      { name: "Claude Code", note: "Read and edit authorized files, follow task context, and publish results through controlled tools." }
    ],
    nextTitle: "Your tools today. Room to grow.",
    nextBody: "The Provider API supports dedicated adapters for additional coding agents. More integrations are planned, building on the tools you already use.",
    link: "How provider integration works"
  },
  tour: {
    kicker: "02 / INSIDE THE WORKSPACE", side: "REAL SCREENS. CONNECTED WORK.",
    heading: "A place for your team.", emphasis: "A clear view of the work.", lead: "Two ways to think. One place to build.", detail: "Tasks, conversations, and context come together.",
    label: "Workspace screenshots", full: "Open full-resolution screenshot", alt: "Teamva product screenshot with illustrative demo data.",
    note: "Real product screens. Demo data.",
    screens: [
      { label: "Agent teamwork", title: "Real roles. A shared conversation.", text: "Product managers shape the scope, developers build, and QA engineers review. Combine reasoning-focused agents for analysis with execution-focused agents for implementation, connected through public handoffs." },
      { label: "Graph conversations", title: "Give every good what-if a path.", text: "Branch from a reply. Freeze the shared starting point. Explore different directions in independent, writable conversations." },
      { label: "Step into a session", title: "Keep the team moving. Guide one agent.", text: "Open a developer’s session beside the shared task. Read its history, ask a focused question, or adjust the direction while keeping the wider discussion in view." },
      { label: "Project resources", title: "Your project is bigger than one repo.", text: "Bring frontend, API, and shared-library repositories together with document folders, reference links, and websites." },
      { label: "Agent configuration", title: "Different roles. Your choice of engine.", text: "Give specialists their own prompts, coding CLI, model, and supported Skills. Manage each role’s configuration in one place." },
      { label: "Skills", title: "Give expertise a permanent home.", text: "Browse the Skills catalog, install selected packages locally, then bind and order Skills for supported agents." },
      { label: "MCP", title: "Connect the tools your work needs.", text: "Manage personal and project MCP configurations. Inspect connections and capabilities; supported providers use them on the next run." },
      { label: "Project memory", title: "Decisions that carry the work forward.", text: "Keep versioned facts, constraints, and decisions with the project. Give the next task a better starting point." }
    ]
  },
  features: {
    kicker: "03 / BUILT FOR THE WHOLE JOB", side: "FROM PROMPT TO PROJECT.",
    heading: "Work that flows.", emphasis: "From one step to the next.", lead: "Parallel work. New directions. A steady rhythm.", detail: "One continuous way to move forward.",
    stories: [
      { theme: "PARALLEL / CONNECTED", heading: "Work in parallel.", emphasis: "Meet at the right moment.", text: "Independent jobs move together. Dependencies release the next step when results are ready; structured mentions and public summaries carry the handoff.", labels: ["Product", "Frontend", "Backend", "Review"] },
      { theme: "EXPLORE / REMEMBER", heading: "Follow the what-if.", emphasis: "Keep what you learn.", text: "Branch a reply into independent conversations. Decisions, run history, and project memory stay on your machine, ready for the next task.", labels: ["Shared start", "Direction A", "Direction B"] },
      { theme: "REPEAT / STAY IN CONTROL", heading: "Give work a rhythm.", emphasis: "Keep the final say.", text: "On your running machine, reviews and maintenance begin on schedule. Follow the activity, respond to supported approvals, or stop a run and pause a session whenever you need.", labels: ["Scheduled", "In motion", "Your call"] }
    ]
  },
  context: {
    kicker: "04 / BRING THE WHOLE PROJECT", heading: "The whole project.\nA shared context.", emphasis: "Better-grounded work.",
    body: "Your frontend is in one repo. Your API in another. The spec lives in a folder, and the reference is a website. Teamva gives them a shared project home.", link: "See project resources",
    label: "A shared project connects repositories, documentation, links, memory, Skills and MCP", core: "One project.", sub: "Shared working context.",
    nodes: [{ title: "Repositories", detail: "web / api / design-system" }, { title: "Docs & links", detail: "specs / websites / references" }, { title: "Skills & MCP", detail: "expertise / connected tools" }, { title: "Project memory", detail: "decisions / facts / constraints" }]
  },
  local: {
    kicker: "05 / LOCAL BY DESIGN", heading: "Your work.", emphasis: "Stored on your machine.",
    body: "Tasks, conversations, and project memory are stored on your computer.", detail: "Built around your privacy and your choice of model services.",
    boundary: "Your chosen model service handles model requests under its own data policy.",
    layers: ["Local tasks and conversations", "Lasting project memory", "Password-protected remote access"], note: "Set a password and enable Remote access to use the workspace from other devices on a trusted LAN or VPN."
  },
  faq: {
    kicker: "06 / MAKE IT YOUR WORKSPACE", side: "START WITH WHAT MATTERS TO YOU.", heading: "Your questions.", emphasis: "Clear answers.",
    items: [
      ["What can I organize in Teamva?", "Bring tasks, agent conversations, repositories, reference links, project memory, Skills, and MCP configurations into one workspace. Assign specialists, follow their progress, and review published results."],
      ["How do I connect my coding tools?", "Teamva automatically detects supported installed CLIs. Codex and OpenCode can reuse supported local authentication and configuration. The controlled Claude Code adapter provides authorized file and task tools. More integrations are planned through the Provider API; the guide covers setup for each tool."],
      ["Where is my work stored?", "Tasks, conversations, and project memory are stored locally. Your coding agent sends model requests and selected context to the model service you configure, whose data policy applies. The guide’s Data boundaries section explains the flow."],
      ["How can I use another device?", "Set a password and enable Remote access in local settings, then connect through a trusted LAN or VPN. The guide’s Permissions section covers network access and safe setup."],
      ["How should I organize parallel edits?", "Use separate worktrees or directories when agents need independent development state. Sessions can share authorized resources, so review their permissions before running. The guide explains resource authorization and security boundaries."],
      ["What needs to stay running?", "Keep your computer and the Teamva service running while agents and scheduled tasks execute. You can preview schedule triggers and inspect each run’s history."],
      ["How do I get started?", "Build the current development preview from source and follow the setup guide to configure your coding tools. macOS packaging produces universal ZIP and drag-to-Applications DMG artifacts that are currently unsigned and not notarized."]
    ]
  },
  start: {
    kicker: "YOUR NEXT PROJECT DESERVES A TEAM.", heading: "Build with agents.", emphasis: "Make room for ideas.",
    body: "Start the local workspace from your source checkout.", requirements: "Go 1.25.x · Node.js 24.x · pnpm 11.x · a configured coding provider",
    copy: "Copy", copyLabel: "Copy browser quick-start command", copied: "Copied. Run in your Teamva source checkout.", copyError: "Clipboard unavailable. Select and copy the command above.",
    note: "Development preview · macOS builds are unsigned and not notarized.", docs: "Read the setup guide"
  },
  footer: { line1: "A workspace for the Super-agent era.", line2: "Built around your work. Running on your machine.", tour: "Product tour", docs: "Documentation", research: "Research context", github: "Teamva on GitHub", independent: "Independent project", motto: "ONE TEAM. CONNECTED PROGRESS.", support: "Connect Codex and OpenCode, or use controlled Claude Code file and task tools. More integrations planned." }
};

export type SiteCopy = typeof en;

const zh: SiteCopy = {
  title: "Teamva — 让你的 Agents，协作成一支团队。",
  description: "面向 Super-agent 时代的本地优先工作台。自动检测并适配电脑上已安装的编程 CLI，协调多 Agent 任务，探索图对话。",
  skip: "跳到正文", home: "Teamva 首页", preview: "开发预览",
  nav: { workspace: "工作台", research: "Super-agent 时代", why: "为什么选 Teamva", docs: "使用文档", start: "开始构建", open: "打开导航", close: "关闭导航", label: "主导航" },
  hero: {
    badge: "DEEPSEEK SUPER-AGENT 时代，协作从本机开始",
    line1: "你的 Agents。", line2: "协作成一支团队。",
    description: "让你的 AI 团队，围绕一个目标协作。", detail: "给 Agents 一个共同目标，把注意力留给真正需要你思考的事。",
    primary: "构建你的 Agent 工作台", secondary: "探索真实界面", proof: ["本地优先", "多编程 Agent 协作", "macOS 与浏览器"],
    previewTitle: "团队协作，随时介入。",
    selector: "工作台视图", tasks: "多 Agent 协作", intervention: "随时介入", graph: "图对话",
    taskAlt: "产品经理、前端开发、后端开发、测试工程师在公共任务里讨论与交接，结合深度分析和高效执行",
    interventionAlt: "打开前端开发的会话侧栏，查看历史、调整方向并追问，同时保留公共任务讨论", graphAlt: "Teamva 图对话：从共同起点分出两条独立探索路径"
  },
  era: {
    kicker: "01 / SUPER-AGENT 时刻", side: "少一点调度，多一点创造。",
    heading: "从能干的 Agent，\n到有序的协作。", emphasis: "目标一致，\n工作向前。",
    chip: "DEEPSEEK 研究 · 2026 年 10 月 31 日", title: "Super-agent 的方向，本地优先的实践。",
    paragraphs: [
      "DeepSeek 的 Super-agent 研究，探索并行推进、事件驱动与会话协调如何帮助能干的 Agent 一起工作。",
      "Teamva 把协作问题带到你的电脑上：明确分工、独立 Agent 会话、可分叉的图对话，以及看得见的项目上下文。你专注方向，团队推进工作。"
    ],
    source: "阅读 DeepSeek 官方研究原型", disclaimer: "Teamva 是独立开发的本地工作台。所引用的研究来自 DeepSeek。",
    principles: ["你决定方向。", "专家接手工作。", "你验收结果。"]
  },
  engines: {
    kicker: "接上你已经在用的编程工具", title: "已经装好，就接着用。",
    body: "Teamva 会自动检测电脑上已安装的受支持编程 CLI，接入工作台。继续使用你的 Codex、OpenCode，并按各自支持范围沿用本机登录状态与模型供应商配置。",
    cards: [
      { name: "Codex", note: "自动检测已安装的 CLI，按支持范围复用本机登录与配置。" },
      { name: "OpenCode", note: "检测你已安装的 CLI，也可使用内置运行时；沿用受支持的模型供应商配置。" },
      { name: "Claude Code", note: "通过受控工具读写授权文件、查看任务上下文，并发布工作结果。" }
    ],
    nextTitle: "沿用今天的工具，迎接更多可能。", nextBody: "Provider API 支持为更多编程 Agent 编写专属适配器。后续计划继续扩展，让工作台接上更多你习惯的工具。", link: "了解编程工具接入"
  },
  tour: {
    kicker: "02 / 走进工作台", side: "真实界面，连贯工作。", heading: "团队有了共同空间。", emphasis: "工作有了清晰脉络。", lead: "两种思考方式，一个工作的地方。", detail: "任务、对话与上下文，在这里汇聚。",
    label: "工作台截图导览", full: "打开原始高清截图", alt: "Teamva 真实产品界面，使用明确标注的演示数据。",
    note: "真实界面，演示数据。",
    screens: [
      { label: "多 Agent 协作", title: "真实岗位，在同一个任务里对话。", text: "产品经理明确需求，前后端开发并行实现，测试工程师把关验收。让擅长深度分析的 Agent 负责需求与评审，让执行高效的 Agent 推进实现，公开讨论与交接把团队连起来。" },
      { label: "图对话", title: "让每个好点子，都有自己的路径。", text: "从一条回复分叉，冻结共同起点，让不同方向在独立、可继续的对话中展开。" },
      { label: "随时介入", title: "团队继续协作，你精准调整一位。", text: "在公共任务旁打开开发者的会话历史，追问细节、补充要求、调整方向，同时掌握团队的讨论与进展。" },
      { label: "项目资源", title: "你的项目，不止一个仓库。", text: "将前端、API、公共库仓库，与本地文档目录、参考链接和网站放进同一个项目。" },
      { label: "Agent 配置", title: "分工不同，引擎由你选择。", text: "为专家选择提示词、编程 CLI、模型和受支持的 Skills，在同一个地方管理每种角色的配置。" },
      { label: "Skills", title: "给可复用的专长一个位置。", text: "浏览技能目录，选择并完整安装到本机，再为支持的 Agent 绑定与排序。" },
      { label: "MCP", title: "连接工作所需的工具。", text: "管理个人与项目 MCP 配置，查看连接和能力目录，由支持的编程工具在下一次运行时使用。" },
      { label: "项目记忆", title: "让重要决定，成为下一步的起点。", text: "保存版本化的事实、约束与决策，让下一项工作从更好的地方开始。" }
    ]
  },
  features: {
    kicker: "03 / 面向完整工作", side: "从提示词，走向项目。", heading: "让工作连起来。", emphasis: "从这一步，到下一步。", lead: "并行、探索、日常执行。", detail: "一条连贯的工作流。",
    stories: [
      { theme: "并行 · 交接", heading: "各自并行。", emphasis: "交接有序。", text: "独立工作同时推进，后续任务等待前置结果。结构化 @ 和公共总结，让下一位 Agent 接上思路。", labels: ["产品", "前端", "后端", "评审"] },
      { theme: "探索 · 记忆", heading: "尽管探索。", emphasis: "每一步都有来处。", text: "从一条回复分出不同方向，在各自的会话里继续。决策、运行历史与项目记忆保存在本机，下一次从这里接续。", labels: ["共同起点", "方向一", "方向二"] },
      { theme: "节奏 · 控制", heading: "让好流程按时发生。", emphasis: "把决定留在你手中。", text: "电脑与服务运行时，评审、研究与维护按计划开始。需要时查看活动、回应支持的审批，随时停止运行或暂停会话。", labels: ["定时开始", "持续推进", "由你决定"] }
    ]
  },
  context: {
    kicker: "04 / 带上整个项目", heading: "完整项目，\n共同上下文。", emphasis: "让工作更有依据。", body: "前端在一个仓库，API 在另一个仓库；规格写在文档里，参考资料在网站上。Teamva 给它们一个共同的项目空间。", link: "看看项目资源",
    label: "一个项目连接代码仓库、文档、链接、记忆、Skills 与 MCP", core: "一个项目。", sub: "共同的工作上下文。",
    nodes: [{ title: "代码仓库", detail: "前端 / API / 设计系统" }, { title: "文档与链接", detail: "规格 / 网站 / 参考资料" }, { title: "Skills 与 MCP", detail: "专业能力 / 连接工具" }, { title: "项目记忆", detail: "决策 / 事实 / 约束" }]
  },
  local: {
    kicker: "05 / 从设计开始就本地优先", heading: "你的工作。", emphasis: "保存在你的电脑。", body: "任务、对话与项目记忆，都保存在本机。", detail: "重视你的隐私，模型服务由你选择。",
    boundary: "模型请求由你选择的供应商处理，适用其数据政策。", layers: ["本地保存任务与对话", "持续积累项目记忆", "密码保护的远程访问"], note: "设置密码并开启远程访问后，可通过可信局域网或 VPN，在其他设备使用同一个工作台。"
  },
  faq: {
    kicker: "06 / 找到适合你的用法", side: "从你关心的问题开始。", heading: "你的问题。", emphasis: "清楚回答。",
    items: [
      ["Teamva 能帮我组织哪些工作？", "把任务、Agent 对话、代码仓库、参考链接、项目记忆、Skills 与 MCP 配置放进同一个工作台。明确专家分工，跟进执行过程，再验收公开结果。"],
      ["怎样接上我已经在用的编程工具？", "Teamva 自动检测受支持的已安装 CLI。Codex 和 OpenCode 可按支持范围沿用本机登录与配置；受控 Claude Code 适配器提供授权文件与任务工具。后续计划通过 Provider API 扩展更多接入，各工具的配置方式见使用指南。"],
      ["我的工作数据保存在哪里？", "任务、对话与项目记忆保存在本机。编程 Agent 根据你的配置，将模型请求及选定上下文发送给模型供应商，适用对应数据政策。使用指南的「数据边界」章节说明具体流向。"],
      ["怎样从其他设备访问？", "在本机设置中配置密码并开启远程访问，即可通过可信局域网或 VPN 连接。使用指南的「权限与隔离」章节提供网络访问与安全设置说明。"],
      ["怎样安排多个 Agent 并行修改？", "需要独立开发状态时，使用不同的 worktree 或目录。会话可以共享授权资源，运行前应检查权限；使用指南说明资源授权和安全边界。"],
      ["执行工作时需要保持什么运行？", "Agent 和定时任务执行时，电脑与 Teamva 服务需要保持运行。你可以提前预览触发时间，并查看每次运行的历史。"],
      ["怎样开始使用？", "从源码构建当前开发预览，按使用指南配置编程工具。macOS 打包提供 universal ZIP 与拖动安装 DMG 制品，目前未签名、未公证。"]
    ]
  },
  start: {
    kicker: "下一个项目，值得一支团队。", heading: "与 Agents 一起构建。", emphasis: "把空间留给想法。", body: "在 Teamva 源码目录启动本地工作台。", requirements: "Go 1.25.x · Node.js 24.x · pnpm 11.x · 已配置的编程工具",
    copy: "复制", copyLabel: "复制浏览器快速启动命令", copied: "已复制。请在 Teamva 源码目录运行。", copyError: "无法访问剪贴板，请选中并复制上方命令。", note: "开发预览 · macOS 制品未签名、未公证。", docs: "阅读配置指南"
  },
  footer: { line1: "面向 Super-agent 时代的工作台。", line2: "围绕你的工作，运行在你的电脑。", tour: "产品导览", docs: "使用文档", research: "研究背景", github: "Teamva GitHub", independent: "独立项目", motto: "一支团队，让工作连贯向前。", support: "接入 Codex、OpenCode，或使用受控 Claude Code 文件与任务工具；后续计划扩展更多接入。" }
};

export const siteCopy = { en, zh };

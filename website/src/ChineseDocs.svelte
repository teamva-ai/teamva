<script module lang="ts">
  export const sections: { id: string; title: string; heading: string; paragraphs: string[]; bullets?: string[]; code?: string }[] = [
    { id: "mental-model", title: "协作模型", heading: "同一个工作台，两类清晰职责", paragraphs: ["Teamva 将你使用的编程工具组织成协作工作台。编程 Provider 负责理解代码、调用受支持的工具和执行任务；Teamva 负责项目、任务、明确分工、独立会话、记忆、调度与可见的结果。", "你决定方向，专家执行工作，最后回到任务中验收。模型供应商的认证、数据政策与费用按各自规则适用。"] },
    { id: "before-you-begin", title: "开始之前", heading: "沿用已有工具，准备本地工作台", paragraphs: ["当前提供 macOS 应用与浏览器工作台。Teamva 会自动检测受支持版本的 Codex、OpenCode 等编程 CLI，并按各工具的支持范围沿用本机认证与配置。检测用于确认工具与能力，模型访问需要完成相应认证和配置。", "源码运行需要 Go 1.25.x、Node.js 24.x 和 pnpm 11.x；浏览器使用可直接启动本地服务。新开发构建只初始化空数据库，请显式选择新的数据目录，并保留日常数据。"] },
    { id: "providers", title: "编程工具接入", heading: "自动检测本机 CLI，沿用熟悉的工具", paragraphs: ["Codex 和 OpenCode 是主要接入。OpenCode 也提供内置运行时；你可以在设置中指定自己的可执行文件路径。受控 Claude Code 适配器支持读写授权文件、查看任务上下文与发布结果；工具范围限于文件和任务，不包含 Shell、外部 MCP、用户 Skills 或插件。", "每个 Provider 都有自己的能力声明与适配实现。审批、恢复、图片、MCP 与 Skills 以所选工具的实际能力为准。认证凭据保存在本机受保护的位置。", "Provider API 支持为更多编程 Agent 编写专属适配器，后续计划继续扩展接入。新增适配器需要实现、构建并通过对应工具验收。"] },
    { id: "first-workflow", title: "第一个工作流", heading: "从目标，走到可验收的结果", paragraphs: ["先定义工作，再让 Agent 执行。多 Agent 分工提交前可以编辑；可选的 AI 拆分生成草案，由你确认后启动。"], bullets: ["创建项目：命名这组工作，并填写必要说明。", "添加资源：登记一个或多个代码目录、文档目录和参考 URL。", "配置 Agent：选择 Provider、模型、系统提示词，以及该 Provider 支持的已安装 Skills。", "创建任务：说明目标，@ 单个 Agent；需要多位专家时，明确填写工作分配。", "打开会话：查看活动、工具与支持的审批，在同一会话追问。", "验收结果：查看 Agent 显式发布的公共总结与变更证据。"], code: "pnpm install\npnpm build\npnpm start:browser" },
    { id: "projects-resources", title: "项目与资源", heading: "一个项目，可以跨多个仓库", paragraphs: ["项目把任务、资源、记忆与计划任务放到同一个工作上下文中，代码继续保留在你登记的本地目录。", "代码目录适合前端、后端、设计系统或 SDK；文档目录可以放规格与参考资料；URL 可登记文档、需求链接或产品网站。目录权限和 Git 检测以实际校验为准，URL 作为参考上下文保存；读取网站内容取决于所选工具与网络权限。", "多个 Agent 可能访问同一份授权资源。需要隔离并行修改时，请使用独立目录或 Git worktree；独立会话本身不提供文件系统安全隔离。"] },
    { id: "issues-comments", title: "任务与评论", heading: "任务是持续协作的容器", paragraphs: ["任务包含标题、说明、状态、公共评论、Agent 会话、Run 与变更证据。任务状态由用户控制，你可以在验收一次或多次执行结果后决定是否完成任务。", "@ 使用结构化数据，提交准确的 Agent ID，确保激活对象明确。用户评论与 Agent 主动发布的总结保留在任务里，内部控制消息和工具活动留在对应会话。"] },
    { id: "work-assignments", title: "工作分配", heading: "明确负责人，也明确什么时候开始", paragraphs: ["普通用户评论激活一个不同的 Agent；选择第二位 Agent 时，会打开工作分配表单。每份工作有名称、负责人、可选详细要求与开始条件。同一 Agent 可以负责多份独立工作。", "无前置工作可立即开始；有依赖的工作由系统等待，前置成功且发布结果后才释放。依赖需要保持无环；失败、停止或缺少结果时，后续工作继续受阻。每份工作都可以打开自己的会话。", "可选的 AI 拆分使用你所选的 Provider，返回待确认的草案；你检查并提交后，系统再安排执行。"] },
    { id: "graph-conversations", title: "图对话", heading: "同一个起点，继续探索不同方向", paragraphs: ["从一条 Agent 回复分叉，会冻结共同前缀。原对话保留后续历史，新分支从选定回复开始；两条可写分支都能继续，各自有 Agent 和串行 Run。节点可以拖动、调整尺寸与缩放查看。", "图对话共用项目资源和记忆，与任务评论、@ 和工作分配分别管理。你可以沿不同分支继续讨论，再自行整理和选择结果。"] },
    { id: "agents-sessions", title: "Agent 与会话", heading: "Agent 是配置，会话承载工作", paragraphs: ["Agent 定义名称、说明、Provider、模型、系统提示词与受支持的 Skills。配置修订记录当时的选择，执行链路通过配置世代处理后续变化，让正在进行的 Run 保持稳定。", "分别在两条评论或两份明确分工中激活同一 Agent，会创建独立会话。创建多份工作应使用独立评论或工作分配；侧栏追问只继续当前选中的会话。"] },
    { id: "multi-agent-semantics", title: "多 Agent 语义", heading: "实例之间并行，实例内部串行", paragraphs: ["不同 Agent 实例可以同时工作，同一实例内的 Run 按顺序执行，让追问接在所需的上下文之后。", "Agent 可以通过公开评论和结构化 @ 把工作交给另一位 Agent。禁止直接 @ 自己，但允许 A → B → A 这样的间接回路；显式工作依赖仍需保持无环。", "评审与修改循环由用户掌握。需要结束或检查时，可以停止 Run、暂停会话或关闭任务；间接回路不会自动终止。"] },
    { id: "memory-skills", title: "记忆与 Skills", heading: "持久知识与可执行能力，分开管理", paragraphs: ["项目记忆保存版本化的事实、决策、约束和进展，可以查看历史、归档与恢复。", "Skills 是包含说明、参考资料和可选脚本的完整本地包。先安装，再为支持的 Agent 绑定和排序，执行前检查所选安装内容。", "Skill 脚本与 hooks 以当前用户权限执行。摘要校验确认包的版本与完整性；安全性需要你在安装或更新时像审查代码一样检查。"] },
    { id: "mcp", title: "MCP 配置", heading: "按作用域连接，也按 Provider 能力连接", paragraphs: ["技能与 MCP 页面提供独立 MCP 管理器。个人配置和项目配置分开，同名时项目项覆盖个人项，禁用配置同样参与覆盖。保存配置不会启动服务；显式连接用于探测能力目录。", "启用配置由支持的 Provider 在下一次 Run 注入，活动工作保持原有配置。Codex 和 OpenCode 按各自原生规则转换，受控 Claude Code 适配器不开放外部 MCP。", "秘密保存在本机私有文件，不进入公开响应、Run 命令、事件或诊断。工作记录与灵感等全局只读助手会话不注入自定义 MCP。"] },
    { id: "runs-approvals", title: "执行与审批", heading: "看清 Agent 此刻在做什么", paragraphs: ["会话侧栏展示流式输出、工具活动、上下文整理、重试和变更，并与模型最终回复清楚区分。", "Provider 真实发起的审批请求会显示为界面中的确认操作。可用审批与权限规则以所选 Provider 的原生能力为准。"] },
    { id: "stop-pause-recover", title: "停止、暂停与恢复", heading: "选择够用的最小打断", paragraphs: ["调整方向时停止当前 Run；需要先检查时暂停会话，处理完后恢复；结束后续执行时关闭任务。底层取消队列也会约束尚在等待的依赖工作。", "控制状态与 Runtime 状态分别持久化在本地 SQLite 中。服务重新启动后会协调未完成的活动；数据库基线或完整性校验失败时，保留数据库并拒绝启动，不自动升级、清空或重建。"] },
    { id: "schedules", title: "定时任务", heading: "让周期性工作也有可检查的来龙去脉", paragraphs: ["选择已经配置且启用的 Agent，填写提示词，使用日、周、单次或 Cron 计划和 IANA 时区。可以预览触发时间、立即运行、检查历史，也能新建任务或追加到同项目已有任务。", "计划任务在本地执行，电脑和 Teamva 服务需要保持运行。调度遵守任务的暂停、关闭与项目边界；新任务默认跳过错过的触发，并避免自身重叠执行。"] },
    { id: "changes-review", title: "变更与验收", heading: "把总结与仓库证据一起检查", paragraphs: ["Runtime 会尝试记录项目中就绪 Git 资源的执行前后状态，按仓库分组展示变更，方便分别检查 diff。", "捕获超时、中断或无法归属的变更会明确提示。验收时请结合真实测试、仓库状态与运行证据；演示截图使用预写数据。"] },
    { id: "data-boundaries", title: "数据边界", heading: "本机保存工作，按配置处理模型请求", paragraphs: ["任务、对话与项目记忆保存在本机。界面、本地服务、调度器与执行状态也在你的电脑上运行。", "编程 Agent 可能把提示词与代码上下文发送给配置的模型供应商，对应数据政策与费用适用。使用受支持的本地模型时，请按所选工具和模型的配置确认请求路径；模型推理的位置由该配置决定。"] },
    { id: "permissions", title: "权限与隔离", heading: "明确授权资源，按可信网络开放访问", paragraphs: ["项目资源限定运行时授权的目录根，授权范围保持在已批准的资源内。worktree 可以隔离 Git 开发状态，但不是操作系统安全沙箱。", "默认仅监听 loopback，本机入口始终保留且免访问密码。在本机「设置 → 远程访问」配置密码并显式开启后，服务会在具体局域网 IPv4 地址上增加同端口号的独立监听。外部访问必须通过密码登录；远程客户端不能修改开关或密码。", "远程访问仅适用于可信局域网或 VPN。HTTP 传输不加密，请勿直接暴露到公网；访问密码保护的是同一本机工作台，不提供多用户权限隔离。", "仅安装和运行可信的 Skills、MCP 与 Provider。给 Agent 授权就是允许它使用相应工具，运行前请审查所需权限。"] },
    { id: "product-boundaries", title: "当前支持范围", heading: "从当前工具开始，逐步扩展工作台", paragraphs: ["当前提供浏览器与 macOS 本地工作台，主要接入 Codex、OpenCode，并包含受控 Claude Code 文件与任务工具。Provider API 支持继续编写更多编程工具适配器。", "当前为源码开发预览。macOS 打包分别生成 arm64 与 x64 的 ZIP 和拖动安装 DMG 制品，使用 ad-hoc 签名，均未公证。Windows/Linux 桌面包、Cursor 适配器、云同步、多人共享数据库、在线备份与自动数据库修复不在当前实现中。", "Teamva 是独立开发的本地工作台。官网引用的 Super-agent 研究来自 DeepSeek，用于介绍协作问题的研究背景。"] }
  ];
  export const chineseDocTitles: Record<string, string> = { overview: "概览", ...Object.fromEntries(sections.map((section) => [section.id, section.title])) };
</script>

<header class="doc-hero" id="overview">
  <div class="doc-kicker">产品指南 · macOS 与浏览器开发预览</div>
  <h1>组织团队，<br/>看清工作。</h1>
  <p>从已有编程工具开始，用项目、明确分工、独立会话和图对话组织工作；把过程与结果保留在自己的电脑上。</p>
  <div class="doc-hero__meta"><span>配置、协作与安全</span><span>中文使用指南</span></div>
</header>
<div class="doc-callout doc-callout--info"><div></div><div><strong>当前为开发预览。</strong><p>本指南介绍当前的配置与工作流程。编程工具按各自能力接入；运行新开发构建时，请选择新的数据目录并保留原有数据。</p></div></div>
{#each sections as section}
  <section class="doc-section" id={section.id}>
    <div class="doc-section__eyebrow">{section.title}</div><h2>{section.heading}</h2>
    {#each section.paragraphs as paragraph}<p>{paragraph}</p>{/each}
    {#if section.bullets}<ol class="guide-list">{#each section.bullets as item}<li>{item}</li>{/each}</ol>{/if}
    {#if section.code}<div class="doc-code"><div>在 Teamva 源码目录运行</div><pre><code>{section.code}</code></pre></div>{/if}
  </section>
{/each}

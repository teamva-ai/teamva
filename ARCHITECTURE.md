# Teamva — Agent、Issue 与图对话领域架构

> 状态：已实现的领域模型基线。
> 主分支维护同一个本地服务，普通浏览器与 Wails 窗口均是一等客户端。进程拓扑、HTTP、本地 SQLite、内嵌 Runtime
> 与实施顺序以 [`docs/DESKTOP_ARCHITECTURE.md`](docs/DESKTOP_ARCHITECTURE.md) 为准。
> 本文定义 Agent、Issue、独立图对话、评论、审批和协作语义。
> 当前尚无已发布版本，主分支只维护当前协议与数据基线。旧库在只读阶段拒绝启动并保留原文件，不升级、不回填、不自动重建。

## 1. 背景与目标

当前系统由本机 Agent、任务和独立会话组成：

1. **本机 Agent 体系**：用户在「本机」菜单中配置可复用的 Agent（= 本机某个确定软件如 opencode/codex + 固定模型 + 提示词 + 绑定的 Skills），并可命名、备注。
2. **Issue（任务）协作**：左侧任务列表，右侧 Issue 详情（标题、描述、评论流，语义即 issue/工单）。人类评论中 @Agent 激活执行；多个 Agent 可在同一 Issue 并行工作、互相委派、互相查看脱敏上下文；Agent 通过工具读取 Issue 上下文并向 Issue 发评论。
3. **本机 Skills**：商店提供可安装的 Skill Package。用户先把完整制品安装到本机，再从 Agent 编辑页选择已安装的版本；Agent 不能直接绑定未安装条目。
4. **独立图对话**：项目下的单 Agent 对话树；节点固定配置与分叉上下文，用户可以平移、拖动、分叉和停止运行。图消息不等同于 Issue 公共评论。

术语约定：代码中统一使用 `issue`（任务=issue，仅是翻译差异）。

## 2. 当前领域模型

```text
AgentService                    // 本机探测的软件能力（opencode/codex + 版本 + 模型列表），只读
  └─ Agent                      // 用户配置、可复用：引用 AgentService + 固定 model + name/note
       └─ AgentRevision         // 不可变配置版本：软件、模型、系统提示词
            └─ AgentRevisionSkillPackage  // 绑定本机已安装的不可变 Skill 版本
                 └─ ComputerSkillPackage
                      └─ SkillPackageVersion

Project
  ├─ ConversationGraph          // 独立对话树
  │    └─ ConversationNode      // 配置快照 + 不可变分叉上下文
  │         ├─ ConversationMessage
  │         └─ Run → RunEvent   // 同节点串行，与 IssueID 互斥
  ├─ ProjectResource            // 本机目录/URL；ready Git 目录组成项目 Workspace 授权
  └─ Issue                      // 任务/工单：标题、描述、状态、worktree 提示
       ├─ IssueComment          // 评论（人/Agent）
       │    ├─ IssueCommentMention   // 结构化 @（user 或 agent）
       │    └─ IssueCommentRevision  // 评论修订历史
       └─ IssueAgentInstance    // 每次结构化 @ 创建的独立会话
            └─ IssueAgentGeneration  // Agent 配置变更后换代
                 └─ Run         // 一次 @、追问或修正
                      └─ RunEvent
```

关键约束：

- 同一个 Agent 在同一个 Issue 中可以有多个实例；每次结构化 @ 都创建新实例，侧栏追问和“修正评论”只续接当前实例。
- Agent 是「配置」，IssueAgentInstance 是一次独立会话。实例固定创建时的 AgentRevision，避免后续编辑污染正在工作的会话。
- 评论中的 @ 使用结构化 `mentioned_agent_ids`，不允许按名称解析。

## 3. 当前数据结构

**核心表：**

- `agents`：id, computer_id, agent_service_id, model, name, note, status, version, created_at, updated_at
- `agent_revisions`：id, agent_id, agent_service_id, model, system_prompt, version（不可变快照）
- `agent_revision_skill_packages`：agent_revision_id, skill_package_version_id, position
- `skill_packages`：id, slug(unique), name, description, source_url, license, builtin
- `skill_package_versions`：id, skill_package_id, commit_sha, content_digest, manifest(JSON), file_count, size_bytes, executable_files(JSON), provider_install(JSON), created_at（不可变）
- `computer_skill_packages`：computer_id, skill_package_version_id, version, created_at, updated_at
- `issues`：id, project_id, title, description, status(open/resolved/closed), worktree_hint(bool), created_by_user_id, version, created_at, updated_at
- `issue_comments`：id, issue_id, author_kind(user|agent), author_user_id, author_agent_id, body, run_id, status(working/published/failed), edited_at, created_at, version
- `issue_comment_mentions`：id, comment_id, target_kind(user|agent), target_agent_id, consumed_at（一条 mention 只消费一次）
- `issue_comment_revisions`：id, comment_id, body, created_at
- `issue_agent_instances`：id, issue_id, agent_id, agent_revision_id, status(idle/running/paused/queued), last_activity_at
- `issue_agent_generations`：id, issue_agent_instance_id, agent_revision_id, native_session_key, created_at

**主要关联：**

- `runs` 关联 `issue_id`、`issue_agent_instance_id`、`issue_agent_generation_id` 和 `agent_id`。
- `project_resources.git_detected` 记录代码目录是否属于 Git 仓库；Issue、Run 和 Schedule 不选择或绑定单个仓库。
- `scheduled_tasks.agent_id` 直接引用本机 Agent。
- `conversation_graphs` 属于 Project；`conversation_nodes` 固定 AgentRevision 与 `inherited_messages`；图 Run 通过 `conversation_node_id` 归属，不共用 Issue 实例或 Goal。

## 4. Issue 状态

无状态机，仅用户手动修改，三态语义：

- `open`：正在处理或等待处理
- `resolved`：用户确认问题已解决
- `closed`：不再处理（取消、重复等）

Agent 不自动改状态。

## 5. Skill Package

- 商店条目 = GitHub 仓库（如 `greensock/gsap-skills` 一个条目），安装时包含该仓库全部 SKILL.md / references / assets / scripts / hooks（用户已确认完整安装原仓库能力）。
- 安装时从内置包声明的 HTTPS GitHub 源解析并固定 commit SHA，生成内容 digest，记录文件 manifest 和版本信息。
- 安装器下载固定制品，校验 digest、路径穿越、压缩炸弹、文件数与体积后写入本机 Skills 目录。启动 Agent 时再次校验目录摘要，并复制到实例私有 runtime 目录；不污染用户全局 OpenCode/Codex 配置。
- 版本变更由用户显式安装，不做后台静默升级。安装不同版本前需从 Agent 解绑并卸载当前版本；绑定新的 Skill 版本会创建新 AgentRevision，已有 Issue 实例下次唤醒时换代。
- 当前不允许用户输入任意 GitHub URL 安装未经审计的包；只提供内置包。

## 6. Agent 配置与换代

- Agent 基础字段、配置修订和 Skill 绑定在一个 SQLite 事务中保存；失败全量回滚，并发修改由版本号裁决。
- 模型/提示词/Skills 实际变化才生成新的 AgentRevision（不可变）；只改名称或备注不触发换代。
- IssueAgentInstance 下次被唤醒时自动切换最新 AgentRevision：创建新 Generation、启动新的 provider native session（node 本地 native session key 绑定 generation_id）。
- SideSheet 保留旧 Generation 的记录，中间显示「Agent 配置已更新」分隔线。

## 7. node 端执行模型

- **Provider API**：`node/provider/` 是公开接入边界，`codex/`、`opencode/` 分别拥有工厂、配置、原生协议和事件投影。`provider.json` 在构建时自动发现并生成 catalog；新 Coding CLI 不需要修改核心注册表或前端工具列表。版本、扩展流程和标准事件见 [`docs/PROVIDER_API.md`](docs/PROVIDER_API.md)。
- **项目 Workspace**：普通 Run 使用稳定的 `<workspaceRoot>/projects/<project-scope-hash>` 作为 CWD；hash 同时绑定项目与完整资源批准范围。相同 scope 复用目录，不同 scope 使用同级独立目录和 `AGENTS.md`，不能覆盖活动 Run 的仓库地图。旧工作区和 native home 保留，不跨新版 scope/CWD 恢复旧会话。
- **资源批准**：显式验证将 Resource ID、验证版本、canonical path 与目录身份摘要原子保存在 Runtime 私有 `resource-authorizations/`。目录或祖先替换、身份变化、缺失/损坏批准均失败闭合；读取和重启不自动补批准，既有 ready Resource 需要用户显式重新验证。
- **精确写权限**：完整仓库授权其规范仓库根；`resource_prefix` 只授权一个或多个互不相交的精确 Resource 根，不扩大到共同 Git 根。Codex 使用原生 workspace writable roots；OpenCode 使用 V2 原生工具/目录权限。目录或工具权限不等于任意 Shell 的 OS sandbox，具体能力与验证边界见 Provider 实现及验收证据。
- **RuntimeManager**：按 `(issue_agent_instance_id, generation_id, project_id, workspace_scope_hash)` 懒启动独立编程工具 runtime（独立 OpenCode serve / Codex app-server 进程）；授权范围变化时不复用旧 Provider/native session；空闲超时回收后仅在完全相同的键下恢复。
- **并发**：不同 IssueAgentInstance 可并行；同一实例内 Run 串行。每次新 mention 创建新实例，侧栏追问续接当前实例；忙碌时用户可通过原生 steer 干预。资源不足必须显式失败，不能无限重试。
- **配置注入**：system_prompt + Skills 写入该实例私有 runtime 配置目录（opencode 用独立 XDG_CONFIG_HOME / OPENCODE_CONFIG；codex 用独立 CODEX_HOME + auth symlink），与任务目录解耦。
- **多仓库变更历史**：每个 Run 最多接纳 64 个 Git Resource、发现 100 个仓库，并为每个仓库保存 start/end 快照（最多 200 行）。捕获失败、超时或进程中断都保存显式截断快照，不伪装成干净状态；Run Changes 按仓库分组，无法确认归属的 Provider report 独立展示。
- **Worktree**：平台不创建、不登记、不追踪 worktree。若 Issue 勾选 `worktree_hint`，仅向 Agent 注入提示词「如需操作 Git 仓库，先创建 git worktree，创建后把 worktree 名称与路径写进评论，供其他 Agent 可见」。git 判定由 node 执行 `git -C <dir> rev-parse --is-inside-work-tree` 回填 `project_resources.git_detected`。

## 8. Agent 工具与 ToolBroker

工具由 adapter 挂载：支持 MCP 的 Provider 使用 Run 级 HTTP MCP，其余暂用 FileBridge；两者统一通过 **ToolBroker → 进程内 ToolCaller → ToolService → SQLite** 读写。
请求保留 deadline、幂等键与 Run 作用域，不再经过远端 WebSocket/Node 凭据。工具作用域由当前 Run 自动绑定
（user/computer/issue/instance/run），不接受 Agent 传入任意 issue_id。

**工具集：**

```text
issue_get()
issue_comment_create(body, mentioned_agent_ids)      // 幂等，comment 落库 + 激活 mentions
issue_comment_update_own(comment_id, expected_version, body)  // 只能编辑自己发布的评论
issue_agents_list()
issue_agent_status(agent_id)
issue_agent_context(agent_id, cursor)                // 脱敏执行记录
```

**Computer Use（官方 MCP 接入）：** 设备设置页提供开关（`computers.computer_use_enabled`）。
每个新 Run 的 `run.start` 携带该开关，Runtime 生成 Provider 实例配置时按上游一致格式追加
`open-computer-use` MCP server（OpenCode 写 `mcp.open-computer-use`，Codex 写
`[mcp_servers."open-computer-use"]`，command `open-computer-use` + args `mcp`）。本实现不复刻
上游执行器，只做配置注入；Authorization/Screen Recording 由用户对 `open-computer-use` 授权。

- Issue Agent 完成 Run 后必须通过工具发送公共评论；若未发送，标记「未发布公共总结」，不自动把模型输出冒充评论。图对话的助手文本只成为该节点的消息。
- 评论、结构化 mentions、activations 必须在同一事务保存；每条 mention 只消费一次（防重试重复执行）。

**跨 Agent 上下文可见性（脱敏执行记录）：** 公共评论、状态、用户消息、工具名称/状态/脱敏结果、可公开的 reasoning summary、最终回复；不暴露隐藏思维链、凭据、provider 原始事件。

## 9. 委派与人工急停

- 禁止 Agent 直接 @ 自己。
- A→B→A 等**间接循环完全允许**（产品明确支持，不做自动终止）。
- 同一实例忙碌时新 activation 排队。
- Issue 底部横幅提供人工急停：停止单个 Agent 实例、暂停该 Issue 全部 Agent、恢复执行、清空待执行队列。
- 检测到重复互相委派时仅提示，不自动停止。

## 10. 前端结构

- **导航分组**：项目内容（概览 / 变更 / 任务 / 图对话 / 资源 / 项目记忆 / 计划任务）· 本机（Agent / Skills / 编程工具）· 设置与活动记录。
- **Agent 管理页**：左侧已有 Agent，右侧原位创建/编辑；Skills 只能从本机已安装列表绑定、解绑和排序，并提供前往本机 Skills 的入口。
- **Issue 页**：左侧 Issue 列表（状态徽标、评论数、在跑实例圆点）；右侧详情（标题/描述/评论流 + 底部输入框 @ 补全 + 提交激活）。创建弹窗：标题 + 描述 + 「创建 Git Worktree」勾选（项目存在 `git_detected` 的 code_directory 才可勾选，否则灰置）。
- **底部横幅**：该 Issue 参与的 Agent 实例；运行时名字前蓝色实心圆点闪烁；点击圆点/名字或 Agent 评论打开右侧 **SideSheet**。
- **SideSheet**：该 Agent 实例的消息流（用户消息、工具调用、脱敏 reasoning summary、流式输出、最终回复）；可继续发消息（新 Run，复用实例会话）；发送旁「修正」按钮（实例空闲时可点）→ 修正 Run → 完成后编辑该 Agent 的上一条评论（PATCH + 修订历史）。
- **实时性**：会话侧栏使用 Run 事件流展示增量内容；任务、评论、实例和确认列表在窗口可见时短轮询，窗口隐藏时暂停。

## 11. 当前模型边界

- Issue 协作与独立图对话并存，共用 Project、Resource、Agent、Memory 与运行基础设施，但不混用评论、mention、实例或 Goal。
- 图对话每次发送将用户消息、回复占位、Run、节点状态和 outbox 原子提交；同幂等键重放原消息。忙碌或 Provider 不可用不会留下孤立消息。
- 分叉新增冻结前缀父副本和可写前缀分支；原节点完整保留全部消息及 Run 关联，作为另一个可写延续分支，不删除尾部历史。三者继承源节点的持久化宽高。前缀与完整延续分别重放自己的上下文，每次使用新 Provider 会话，不能混入另一分支的原生历史。详见 [图对话分叉语义](docs/CONVERSATION_FORKS.md)。
- 计划任务直接引用 Agent；审批和活动记录通过 Run 关联任务与 Agent 会话。
- 当前没有需要兼容的已发布版本，不增加双读、双写、旧字段 fallback 或弃用期逻辑。

## 12. 实施顺序

1. OpenAPI、领域模型与破坏性数据库迁移。
2. AgentRevision / SkillPackage 后端、本机 Skills 安装和 Agent 已安装 Skills 选择。
3. Issue / 评论 / 结构化 mentions / activations 与前端主体。
4. IssueAgentInstance / Generation / Run 调度与 Issue SSE。
5. node RuntimeManager / Skill 安装器 / ToolBroker / 进程内 ToolCaller。
6. opencode / codex 独立 runtime 与工具注入。
7. SideSheet / 修正评论 / 无限委派与人工急停。
8. 概览 / 计划任务 / 活动记录页面适配。
9. 并行、换代、Skill 更新、循环暂停与端到端测试。

## 13. 明确能力与信任边界

1. 无并发上限：多实例并行可能耗尽电脑资源（node 仅做资源故障熔断）。
2. Agent 间接循环委派是明确产品能力：不设置委派深度或自动终止，所有 mention/Run 持久化并由用户通过人工急停控制。
3. 完整执行内置 Skill 的 scripts/hooks：相当于信任仓库代码以当前用户权限运行，UI 需明确风险提示。

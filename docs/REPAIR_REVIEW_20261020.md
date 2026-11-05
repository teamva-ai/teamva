# 两轮审查修复记录（2026-10-20）

## 范围与操作边界

本轮修复两轮源码审查确认的缺陷，保留先前尚未提交的 Lumi 默认名称调整。
没有创建提交、推送、发布、替换桌面 App，也没有操作默认用户数据或调用真实模型。
修复不改变不同 Agent Instance 并行、同实例串行以及允许间接循环委派的产品语义。

## 已修复清单

| 类别 | 问题与处理 | 主要回归位置 |
| --- | --- | --- |
| 任务作用域 | 只允许修改当前 Issue 内本 Agent 的评论；用户 HTTP 不能伪造 Agent 作者/Run | `toolservice/sqlite_issue_tools_test.go`、`httpapi/handlers_issue_comment_author_test.go` |
| 停止围栏 | 评论、修订、mention 与活动 Run 授权在同一事务提交；正在停止或终态 Run 不再写入 | `toolservice/sqlite_issue_tools_test.go` |
| 委派与幂等 | 同条总结可再次委派同一目标；每次有效激活独立持久化，同一请求重试不重复激活 | `toolservice/sqlite_issue_tools_test.go`、`dispatch/local_test.go` |
| 原子性 | 更新总结、校验目标、创建 mention、持久化幂等回执同事务；失败不发布半成品 | `toolservice/sqlite_issue_tools_test.go` |
| 文件桥 | 严格透传 `append_new`；文件重试稳定幂等键；超限返回明确有界错误；随 Run 取消 | `runtime/filebridge_lifecycle_test.go`、`runtime/runtime_bridge_test.go` |
| 长会话 | 总结按实例与原 Run 精确关联，不受最近 100 Run 窗口影响 | `httpapi/issue_history_regression_test.go` |
| Run 历史 | 最新优先的稳定游标分页；聚合 SSE 分页补齐所有 Run 的事件尾页，保留轮询兜底 | `store/sqlite_issue_history_test.go`、`httpapi/issue_history_regression_test.go` |
| 提示词预算 | HTTP 追问与 mention 激活均使用有界最近窗口；排队过久的 mention 仍保留原触发评论 | `httpapi/issue_history_regression_test.go`、`dispatch/mention_context_test.go` |
| 工具读取 | `issue_get` 有界最近评论与裁剪标志，可按 `comment_id` 精确读取当前 Issue 评论；context 游标固定实例、限制事件与响应大小 | `toolservice/sqlite_issue_tools_test.go`、`runtime/filebridge_lifecycle_test.go` |
| 审批 | 活动 Run 的过期审批持久化为 expired，并排队 denied/once；旧批准、停止、终态及恢复均有 fencing | `store/sqlite_approval_lifecycle_test.go`、`store/sqlite_approval_recovery_test.go` |
| 调度 | Provider 探测独立单飞，不阻塞事件/停止/计划任务；取消和关闭等待安全 | `dispatch/probe_test.go`、`embedded/controller_test.go` |
| 日历 | 创建、更新、预览和推进共用日历规则；无未来日期拒绝，异常计划仅禁用一次 | `domain/schedule_test.go`、`store/sqlite_schedule_calendar_test.go` |
| 搜索 | SQL 候选筛选与分页、Top K 归并、最新事件窗口、总扫描/内存预算、私密内容排除；部分扫描不伪装成完整无匹配 | `store/sqlite_project_search_regression_test.go`、`GlobalSearch.test.ts` |
| Skill 卸载 | 写锁内校验所有权/CAS/当前及活动 Run 绑定；文件确认后提交删除，失败保留可重试记录；Node 严格叶名称校验 | `store/sqlite_skill_uninstall_test.go`、`httpapi/skill_uninstall_test.go`、`runtime/skill_slug_regression_test.go` |
| 工作记录 | 服务端独立 operation ID，不使用客户端 trace 作主键；标题按 UTF-8 摘要裁剪，不阻止长标题对象编辑/删除 | `httpapi/work_operation_regression_test.go` |
| Provider 配置 | 支持合法 JSONC 注释/尾逗号，解析错误脱敏且可见；动态端口由真实进程 stdout 提供，不再预占后释放 | `opencode/jsonc_test.go`、`opencode/startup_test.go` |
| 原生会话保留 | 通用 `DataDir` 将 Codex/OpenCode 原生会话、SQLite 与可回收配置分离；配置额度不扫描持久数据，同 key 重建保留会话文件 | `runtime/provider_data_retention_test.go`、`codex/provider_test.go`、`opencode/provider_test.go` |
| 子进程 | 追踪容量按当前存活出生身份而非累计历史计算；保留 PID 复用、UID、lease 与进程组隔离 | `process/watchdog_unix_test.go` |
| Lumi | 显式 graceful/force 停止，固定会话/Run 快照；滚动跟随、上滚保持、跳到最新；会话草稿隔离 | `WorkAssistant.test.ts` |
| 评论输入 | 同 tick 防重复提交；按提交快照清理；编辑/删除请求不清空后来打开的编辑器；错误靠近输入区 | `IssuePanel.test.ts` |
| mention 输入 | 保留所选 Agent ID，屏蔽邮箱/代码/歧义及不可用 Agent；同名 Agent 不再选错 | `mentions.test.ts`、`IssuePanel.test.ts` |
| 草稿 | 按项目/Issue/实例保留会话内草稿与附件；全局附件预算；刷新提示；不把任意正文或图片写入浏览器存储 | `composerDrafts.test.ts`、`AgentSideSheet.test.ts` |
| 编辑保护 | Agent/Memory 路由、刷新及项目切换确认；归档可保存/放弃/取消；迟到响应不覆盖新输入 | `unsavedChanges.test.ts`、`Memories.test.ts`、`Agents.test.ts` |
| 资源 | 旧创建响应不能关闭新弹窗；正确刷新来源项目；桌面与移动均可按版本重命名 | `Resources.test.ts` |
| 导航 | 概览条目、资源和计划任务使用对象深链；计划失败显示有界诊断并可打开 Run；搜索 debounce 内禁止选择旧结果 | `Workbench.test.ts`、`Schedules.test.ts`、`GlobalSearch.test.ts` |
| 任务页 | 可见编辑入口、键盘菜单、状态/标题筛选、更新排序、列表分段、移动主从切换与独立深链加载；较早评论按需读取精确 Run 关联会话，不加载全部历史提示词，跨任务迟到响应不打开错误侧栏 | `Issues.test.ts`、`IssuePanel.test.ts` |
| 可访问性 | 自定义键盘焦点、三主题浅/暗文字对比度、文字强调/品牌色分离、StatusBadge warning 映射 | `appCss.test.ts`、`projectThemes.test.ts`、`StatusBadge.test.ts` |
| 首次引导 | 焦点接管、Tab 圈定、背景 inert，Escape/遮罩不跳过；稳定语言切换与懒加载测试 | `App.onboarding.test.ts`、`pages/Onboarding.test.ts` |
| 全局审批提示 | 顶栏跨项目待审批提示、精确会话入口；逐项展开动作后才能决定，无批量盲批 | `PendingApprovals.test.ts` |
| 文档契约 | Policy 当前仅存储、不参与执行拦截，实际权限由 Provider 原生策略与 Approval 决定；OpenAPI 与九语言同步 | `protocol/openapi/teamva.yaml`、`locales.test.ts` |

## 重要限制

- 本轮不新增全局行动中心、受管 worktree、失败 Run 重试或 Token 账本；它们是产品建议，不是已存在功能的缺陷修复。
- Skill 文件删除与 SQLite 提交不是跨介质原子事务。部分删除或提交失败保留记录并提供重试，不承诺恢复已删文件，也不引入在线备份。
- OpenCode 已消除先释放端口再启动的窗口，并禁用代理/重定向；HTTP Basic 本身不认证服务端，不能声称防御拥有本机进程控制能力的任意恶意同 UID 攻击者。
- 搜索有公开的扫描预算，`truncated=true` 也可能伴随零条结果。它不是完整历史无匹配的证明。
- 草稿保护不等于跨刷新自动恢复；用户明确放弃或确认刷新后，会话内未发送内容可以被丢弃。
- SQLite 基线发生变化。遵守当前仓库“不迁移旧库”的约定，只对新隔离数据库验收；未删除、升级或重建已有用户数据库。
- 原生会话保留回归使用真实文件及 SQLite fixture，覆盖 30 天／128 份／512 MiB 配置回收与 4097 个历史数据 home；不等于使用真实模型验收续聊。未迁移既有原生数据，未分类配置目录保守保留。

## 验收证据的分类

- 自动化：后端 race/vet、Web 单元/交互测试、Svelte 检查、生产构建、协议/SQL 基线测试、ShellCheck、OpenAPI lint 与 diff 检查分别记录。
- 最终 `pnpm verify:backend` 通过：Server、Node、Desktop 全量 `go test -race` 和 `go vet`，Protocol 全量测试；包含分离 `DataDir` 后的整合构建与回归。
- `pnpm check:providers`、ShellCheck、`git diff --check` 通过；OpenAPI lint 无错误，保留 `/health` 无 4xx 与 `/desktop/bootstrap` 使用 303 的两条提示，不伪造不存在的响应以消除提示。
- Web 最终普通顺序与固定随机顺序（seed=20261020）均为 **55 文件、342 项通过**；Svelte 检查 0 error / 0 warning，生产构建通过，保留部分 chunk 超过 500 kB 的构建提示。此前暴露的测试依赖导入 spy、未隔离导航存储和并行资源竞争已分别处理或通过串行门禁复核，没有扩大等待超时掩盖失败。
- 真实本地 API 冒烟：29 个请求通过，使用新基线、新隔离目录、关闭 Runtime 的当前服务构建，不调用模型；验收服务已关闭。
- 真实浏览器：独立 TaskSpace、随机 loopback 端口及新数据目录，确认引导焦点/inert、资源创建及重命名、超过 1000 字节中文标题的创建/编辑、任务离页草稿保留、搜索 debounce Enter，以及 390×844 详情全宽、底栏与无横向溢出。未录制 Cookie/CSRF trace 或 HAR；TaskSpace 与服务已关闭。此为核心定向验收，不是完整浏览器用例套件；其后补充的旧评论会话查找与草稿图片预算文案仅经过自动化回归。
- 原生运行时：内置 OpenCode 生命周期测试未发送 prompt、未调用账号模型；不是完整模型执行验收。
- 未进行真实 Wails、真实 Codex/OpenCode 模型任务或公开制品验收。不能把单元测试、浏览器、原生生命周期和真实模型证据混算。

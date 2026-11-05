# 工作台导航、即时设置与 MCP 验收

## 2026-10-23 行为

- 项目导航固定为「概览、任务、分支对话、资源、项目记忆、定时任务、变更」。内置项目改名「默认项目」，仍按真实稳定 ID 保护；只对该身份的旧固定名称做幂等更新。
- 本机仅保留「Agents」「技能 & MCP」；编程工具不再是导航项，设置中的 CLI 配置保留，旧深链不出现错误页。
- Agent 列表为名字右侧真实 CLI、下方备注，不显示模型；未配置明确标记。Lumi 新建和旧版生成的只读备注补默认说明，不覆盖用户备注/模型/提示词/技能。
- 设置选择即时生效并串行自动保存，快速修改不被旧响应覆盖；离页先 flush，失败/409 保留草稿。密码只在有效回车/离开密码组后提交，不逐字提交、不因点击眼睛按钮误改密码。CLI 路径回车/失焦应用，无保存按钮。
- 定时任务空状态中央有创建按钮。未配置 Agent 可见但禁用并说明原因；配置后的 Lumi 与普通 Agent 一样可选并执行定时 Issue，不再按 Lumi 身份限制能力。
- Lumi 只保留不可删除、保持启用的身份保护；移除调度与只读 Provider 过滤，配置后可被 @、执行 Issue 和定时任务。工作记录/灵感入口仍是各自独立的只读会话；旧版生成的只读备注按精确匹配迁移，自定义备注不动。
- 技能/MCP Tab、JSON 导入、作用域、密钥保留、配置开关与自动保存、连接/目录、OAuth、协议/Code Mode/分阶段超时见 [MCP](MCP.md)。输入无外圈与字段轨道对齐约束保持。

## 自动化证据

- `pnpm verify:web`：72 文件、**564 项测试通过**；Svelte **0 错误、0 警告**，生产构建通过，仅既有大分块提示。
- `pnpm verify:backend`：server/node/desktop 全部 race 与 vet、protocol 测试通过，包含 MCP 私有存储/真实 stdio/HTTP/OAuth fixture、版本锁、认证/同源/CSRF/项目所有权、只读隔离及凭据不进入数据库/日志。
- 官方 MCP Go SDK v1.8.0 的 2026 协议 fixture、PKCE/state/issuer、动态客户端注册、刷新、重启恢复、有效 Bearer 注入和撤销通过；固定回调仅透传原生 Provider，不伪称本页授权支持。
- 独立 server/desktop 模块依赖已 tidy，并以 `GOWORK=off go list -mod=readonly ./...` 校验；Provider 生成检查、OpenAPI lint、ShellCheck、脚本语法及 diff 检查通过。OpenAPI 保留原有 2 条建议警告。

## 真实浏览器和 CLI

- 最终完整 Chrome **107/107 通过**，覆盖原 97 项及新增 MCP 2 项、工作台细节 8 项；包括桌面/触摸、导航顺序、备注与 CLI 的真实几何、空状态、未配置边界、自动保存竞争/离页/409、密码提交，以及 JSON 导入、真实 MCP stdio 握手/目录、断开/删除、凭据保留和项目覆盖。
- 最终 110 张截图保留于隔离验收目录 `teamva-workbench-refine.gNe2gn/screenshots/`，不进入仓库；未记录 trace/HAR。人工查看 MCP 桌面/移动、Agent 列表及定时任务空态。
- Ego TaskSpace 135 查看默认项目、导航、Lumi 备注和 CLI、中央创建按钮；设置改为 dark 后无需保存且重载保留。实际连接离线 MCP 并展示工具/提示词/资源；已断开并关闭空间。一次 Ego 截图请求超时，后续语义快照确认页面正常；最终 MCP 截图使用完整 Chrome 套件结果。
- 真实 **OpenCode 2.0.18** 使用隔离 HOME/XDG 连接离线 MCP，验证 argv/env/cwd、资源目录、disabled 和隔绝不受控项目 MCP；真实 **Codex 0.163.0** 验证原生 TOML 字段与类型、mcp list/get。没有复制用户凭据、调用模型或生产 MCP，fixture 子进程残留为 0。
- Lumi 普通 Agent 修正后复跑：后端 `go test -race ./...`、`go vet` 全部通过；Web 72 文件 **564 项**通过；完整 Chrome **107/107** 通过。Ego TaskSpace 143 在隔离服务中打开真实定时任务编辑器，Agent 原生 select 的 `Lumi` 选项 `disabled=false`，页面不再出现“只读助手”。隔离服务通过 API 创建 Lumi 定时任务返回 201，随后暂停；未触发 Run 或模型。
- 未做真实 Codex MCP 连通、生产 OAuth 登录/刷新或远程 HTTP header 实发验收；这些不能用配置解析通过代替。未做真实 Wails 或公开制品验收，未提交、推送、发布或升级 Ego Lite。

## 当前工作台

61556 工作台已使用最终前后端，保留原数据和端口。无活动/排队 Run 时正常关闭旧进程，再启动新二进制；readyz 200，返回前端与最终构建一致。更新前后业务内容、普通项目/Agent、Lumi 的非备注配置、默认项目非名称配置、用户偏好及外观摘要一致，业务表数量无意外变化；默认项目新名和 Lumi 新备注已确认。Lumi 在现有数据中已有 Provider/current revision，重启后直接作为普通 Agent 可选；真实 API 创建 Lumi 定时任务返回 201，随后暂停并清理本次验证任务及其工作记录。不改 SQL 基线、不清库或迁移。隔离验收服务已停止，用户工作台保留运行。

## Lumi 后续复核

- 普通 mention/Issue Run 为可写作用域，MCP 按 Run.ReadOnly 而非 Lumi 身份分流；调度创建、更新、run-now、到期触发没有 `system_role` 排除，未配置 Agent 仍不可执行。
- 复核发现并修复漏项：服务端会传入已绑定技能，但 Runtime 旧校验拒绝所有带技能的全局助手启动。现允许通过原有摘要校验、安装到实例私有目录的合法技能，拒绝路径型 slug；保持原会话作用域 hash、只读文件权限、工具白名单、无项目根及无自定义 MCP。
- 新增 fake adapter 真正 Start 的回归，检查技能内容与载荷实际到达 Provider 配置、恶意技能与越权作用域拒绝、旧会话隔离、停止和 IPC 清理。没有调用真实模型。
- 本轮 `pnpm verify:web` 72 文件/564 项、全量 server/node 测试、Lumi/Schedule API 与 Runtime 相关定向 race/vet、protocol、Provider 生成检查和 diff 检查通过。浏览器 107 项为上轮证据，本轮没有新增真实浏览器、模型、Wails 或公开制品验收。
- 修正版已更新至 61556，readyz 200、前后端制品匹配，业务/配置摘要不变；没有创建验证任务或修改用户业务数据。
- **保留差异：Lumi 仍不能禁用。** API 与 SQL 触发器都保留不可删除、保持启用的身份保护；这是现有边界，不得把当前状态描述成字面上的“仅禁止删除”。本轮没有修改 SQL 基线或移除该保护。

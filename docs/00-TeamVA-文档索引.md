# Teamva 文档索引

> 状态：本地工作台当前主分支基线
> 更新：2026-08-08
> 当前发布目标：macOS Wails 桌面应用

## 1. 权威顺序

发生冲突时按以下顺序判断：

1. `main` 分支可执行代码、SQLite 约束和自动化测试；
2. `protocol/openapi/teamva.yaml` 与 `protocol/node/wire.go`；
3. 根目录 `ARCHITECTURE.md` 与 `docs/DESKTOP_ARCHITECTURE.md`；
4. 本目录 01–12 文档；

README 或部署草稿不得覆盖当前主线。不保留旧运行路径的兼容实现。

## 2. 当前文档

| 编号 | 文档 | 内容 |
| --- | --- | --- |
| 01 | [产品目标与范围](01-Teamva-产品目标与范围.md) | 当前产品为谁解决什么问题 |
| 02 | [系统架构设计](02-Teamva-系统架构设计.md) | Wails、loopback HTTP、SQLite 与 Embedded Runtime |
| 03 | [多 Agent 协作与调度](03-Teamva-多对话协作与调度.md) | Issue、mention、循环委派、审批和计划任务 |
| 04 | [领域模型与数据设计](04-Teamva-领域模型与数据设计.md) | 本地所有权树与两份 SQLite |
| 05 | [API 与事件协议](05-Teamva-API与事件协议.md) | localhost HTTP/SSE 与 Embedded Runtime 协议 |
| 06 | [权限安全与审计](06-Teamva-权限安全与审计.md) | loopback Session、文件权限、工具和 Skill 风险 |
| 07 | [前端信息架构与交互](07-Teamva-前端信息架构与交互.md) | 本地桌面导航和 Issue 工作流 |
| 08 | [构建、发布与可靠性](08-Teamva-部署运维与可靠性.md) | macOS 构建、更新、日志和故障边界 |
| 09 | [测试验收与质量策略](09-Teamva-测试验收与质量策略.md) | 什么证据才算桌面功能完成 |
| 10 | [实施路线图](10-Teamva-实施路线图与工程计划.md) | 首发剩余工作和后续演进 |
| 11 | [架构决策记录](11-Teamva-架构决策记录.md) | 当前有效决策 |
| 12 | [当前实现与验收基线](12-Teamva-当前实现与验收基线.md) | 已实现能力、证据和缺口 |

## 3. 统一术语

| 术语 | 含义 |
| --- | --- |
| Desktop | `Teamva.app`，当前唯一发布目标 |
| Local API | 进程内启动的单端口 Go HTTP 服务，默认仅本机，显式开启后同端口支持局域网密码访问 |
| Control Store | `app.db`，保存用户、项目、Agent、Issue、Run、策略等权威状态 |
| Runtime Store | `runtime/runtime.db`，保存执行幂等、事件 outbox 和 Provider session |
| Agent Service | 本机探测出的 Codex 或 OpenCode Provider 能力 |
| Agent | 用户配置的模型、提示词和 Skill 组合 |
| Issue | 用户和多个 Agent 共同工作的任务/工单 |
| Mention | 评论中的结构化 `@Agent` 激活记录 |
| Run | 某个 Agent 实例的一次可停止、可审计执行 |

## 4. 当前基线

- 主产品离线独立运行，不依赖 Teamva 云服务或 PostgreSQL；
- Svelte 只通过同源 HTTP/SSE 访问业务，不直接调用 Wails 领域 binding；
- SQLite 为桌面权威状态，命令先持久化再执行；
- Codex/OpenCode 凭据留在 Provider 自己的本机目录；
- Agent 可通过评论间接循环委派，直接 self-mention 仍拒绝；
- 当前不提供、也不排期在线备份/恢复；失败时保留数据库而不是重建；

## 5. 变更纪律

- HTTP 路由变化同步 OpenAPI 与路由测试；
- SQLite 只保留当前基线；旧库必须只读拒绝并保留，不升级、不回填、不清库，验证使用新数据目录；
- 多 Agent 调度变化必须覆盖 mention 幂等、忙碌排队、循环委派和人工急停；
- 浏览器和桌面只使用同源 Web 构建；
- 发布结论必须包含真实应用、真实浏览器和至少一个真实 Provider 证据。

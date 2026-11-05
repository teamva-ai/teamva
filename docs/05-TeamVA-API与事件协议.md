# Teamva API 与事件协议

## 1. 权威契约

HTTP schema 以 `protocol/openapi/teamva.yaml` 为准，Gin method/path 契约测试保证实现与文档一致。
本文件只描述边界，不复制全部字段。

## 2. 本地 HTTP 规则

- SPA、JSON API 和 SSE 同源；
- 监听地址必须是 loopback，端口随机；
- JSON 成功响应使用 `data`，错误包含稳定 `code`、可读 message 和 request ID；
- 写请求要求 HttpOnly Session、CSRF header 和精确 Origin/Referer；
- `Idempotency-Key` 用于可重试的 Run、计划任务和命令；
- 可编辑对象通过 `expected_version` 做乐观锁。

## 3. 桌面 bootstrap

1. Wails 启动本地服务并生成 256-bit nonce；
2. WebView 请求 `GET /desktop/bootstrap?nonce=...`；
3. nonce 原子消费、单次使用、30 秒过期且不落日志；
4. 服务设置 HttpOnly/SameSite Session，303 跳转到 `/workbench`；
5. Web 调用 `GET /v1/auth/bootstrap` 获取用户、偏好和 CSRF。

本地模式不注册邮箱验证码、密码重置、Device Code 或 Node WebSocket 登录入口。

## 4. API 分组

| 分组 | 代表路径 | 用途 |
| --- | --- | --- |
| System | `/v1/system/capabilities` | 当前本机运行能力 |
| Identity | `/v1/auth/bootstrap`, `/v1/me/preferences` | 本地 Session 与偏好 |
| Computer | `/v1/computers`, `/agent-services`, `/agents` | singleton Computer 与 Agent |
| Project | `/v1/projects`, `/resources` | 项目和真实本地目录/URL |
| Issue | `/v1/projects/{id}/issues`, `/comments` | 任务、评论、mention、修订 |
| Execution | `/v1/runs`, `/approvals` | Run、事件、停止和审批 |
| Knowledge | `/memories`, `/policy-rules` | 项目记忆和策略记录（Policy 当前不参与执行拦截） |
| Automation | `/scheduled-tasks` | cron、run-now、运行历史 |
| Audit | `/v1/audit-events` | 用户与 Agent 操作记录 |

Agent 配置统一使用 `PATCH /v1/agents/{id}`，带 `expected_version`，共同提交名称、备注、Provider（`agent_service_id`）、模型、提示词和 Skill 绑定；系统 Agent Lumi 与普通 Agent 共用契约。不保留 `PUT /v1/work-assistant`，`GET /v1/work-assistant` 仅用于定位系统 Agent。系统 Agent Lumi 不可删除且不能禁用；除此之外，其公共配置字段、Issue 参与和定时任务能力与普通 Agent 一致。工作记录、灵感等全局助手入口使用只读 Run，使用已保存的用户提示词与 Skills，但工具授权仍由执行入口强制限制，不能反向解释为 Lumi 身份只读。

任务描述仅在创建时填写，创建后仍可读取但不可修改。`PATCH /v1/issues/{id}` 只更新标题、状态和 worktree 提示；包含 `description`（包括 `null`）时返回 400，标题、版本等字段均不变。空标题可通过点击标题内联补全，仍须携带 `expected_version`。

## 5. SSE 与事件

Issue SSE 推送评论、mention/instance 状态、Run event 和评论修订。事件先持久化再通知；断线重连从游标补读，
进程内通知只负责降低延迟，不是事实来源。

Run event 按 `(run_id, lease_epoch, sequence)` 排序。累计 ACK 只有在 Control Store 事务提交后才发送。

## 6. Agent 工具

- `issue_get`：读取 Issue 和有界最近公共评论，`truncated` 标记裁剪；可传 `comment_id` 精确读取当前 Issue 内的单条评论全文；
- `issue_comment_create`：发评论并可结构化 @ 其他 Agent；
- `issue_comment_update_own`：只修正当前有效 Run 所属 Issue 内本 Agent 自己的评论；
- 评论写入、修订、每次新增 mention 和幂等回执在同一事务提交；同条总结再次委派同一目标也生成独立 mention，重试同一请求不重复激活；
- `issue_comment_create` 的 `append_new` 在文件桥完整透传，只有明确要求另发评论时使用；
- `issue_agent_context` 的版本化游标固定实例，每页最多一个 Run、128 个事件并返回 `truncated` / `next_cursor`；不会因新一次 @ 串到其他会话；
- `issue_agents_list`：列出可委派 Agent；
- 文件和 shell 工具：受 Resource、Provider 原生权限/Approval 和 run scope 约束；Policy CRUD 不构成执行授权。

直接 self-mention 返回 `self_mention`；间接循环不返回 cycle 错误。

## 7. Embedded Runtime 协议

`protocol/node/wire.go` 定义 Local Dispatcher 与 Embedded Runtime 共用的 command/event envelope。
桌面进程内直接传输，不要求用户安装 CLI、注册设备或连接远端服务。

`run.start` 必须包含 `project_id`，不接受 `cwd`、`path` 或单仓库绑定字段；Server 投影当前项目最多 64 个
ready Git Resource。Node 最多发现 100 个仓库，以项目及完整授权 scope 的稳定 Workspace 为 CWD，并通过该 scope 独立的
`AGENTS.md` 暴露机器生成的仓库地图和精确可写根；不同 scope 不覆盖彼此地图。Resource 必须经过显式验证写入私有持久批准锚点，缺失或目录身份变化需用户重新验证，不在读取或重启时自动补批准。每个 Run 最多持久化 100 组 start/end 快照（200 行）。

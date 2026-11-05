# 图节点持续目标

## 使用与边界

在未锁定、空闲的图节点输入区勾选「持续目标」，输入目标并选择 Token 预算后启动。
默认预算 2,000,000 Token，可取消预算限制；工作轮数仍有 21 轮上限。

- 保持现有图对话的**只读权限**，不会因为开启目标而自动获得写文件权限。
- 首次目标文本只作为一条用户消息写入；后续工作结果追加为 Agent 消息。
- 每轮结束后由独立审核 Run 判断 `continue / complete / blocked`。
- 审核 Run 使用独立 Provider 会话、专用系统提示和禁用工具设置，不共享工作线程；审核原文不作为聊天回复展示。
- 暂停与取消都会持久化停止请求；等待当前 Run 结束后才能恢复。
- 阻塞可恢复；预算或工作轮数耗尽时，取消旧目标再创建新目标。
- 未结束目标占用节点，即使暂时没有活跃 Run，也不能另发普通消息、分叉或删除该节点。
- 已完成/取消的目标留作最近一次状态展示；分支不会继承目标状态。

## 持久化与恢复

当前基线 `server/sqlite/schema/conversation_goals.sql` 定义 `conversation_goals`、`conversation_goal_runs`；不升级旧库。
不创建隐藏 Issue，不复用 `issue_agent_goals`，不写 Issue 公共评论。

目标变更、下一次 Run、消息占位与 outbox 在事务中写入。Run 关联表对 `(goal_id, ordinal)`
唯一约束；创建按 `(node_id, idempotency_key)` 重放，内容不同则 409。
调度器只推进 active 目标，重启读取已持久化的运行结果，不靠浏览器保持循环。
一个节点最多有一个未结束目标；不同节点的目标可以独立推进。

Token 统计从已去重的 `usage.updated` 重算，**包含审核开销**；取消后晚到的终态用量也会记录。
预算按 Provider 用量上报检查，可发出停止请求，但不能保证单次模型调用精确截止在预算值，
也不推测 Provider 未上报的 Token。

## Local API

- `GET /v1/conversation-nodes/:node_id/goal`：最近目标，未创建则 `{goal: null}`。
- `POST /v1/conversation-nodes/:node_id/goals`：`{objective, token_budget}`，要求 `Idempotency-Key`。
- `POST /v1/conversation-nodes/:node_id/goals/:goal_id/:action`：`action` 为 pause/resume/cancel，
  请求 `{expected_version}`；版本冲突 409，目标跨节点/跨用户访问 404。
- 图详情返回 `goals` 数组（每节点最多一个最近目标），供页面一次轮询更新。

## 独立验证

```bash
pnpm test:server
pnpm verify:web
```

后端定向回归（在 server 目录）：

```bash
go test -race ./internal/store -run TestConversationGoal -count=1
go test -race ./internal/httpapi -run TestConversationGoalHTTPContract -count=1
go test -race ./internal/dispatch -run TestDispatcherCompletesConversationGoalWithoutIssue -count=1
```

真实 Chrome UI 定向用例（先启动隔离本地服务并设置状态文件，不调用模型）：

```bash
pnpm test:browser --grep @goal
```

这项浏览器测试使用固定响应验证 UI；真实 SQLite/outbox 的自动推进由后端独立测试覆盖。
它们不替代真实 Provider 或 Wails 验收。升级运行中的服务前需重新构建后端；
基线或完整性检查失败必须保留数据库并拒绝启动；新构建请显式选择新的数据目录。

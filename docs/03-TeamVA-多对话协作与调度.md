# Teamva 多 Agent 协作与调度

## 1. Issue 是协作容器

当前模型没有 Conversation/Folder。Project 下的 Issue 保存标题、描述、状态、公共评论、参与 Agent、Run 和审批。
公共评论是用户与 Agent 之间以及 Agent 互相委派的可审计事实。

## 2. 结构化 mention

- 评论正文中的显示名称仅用于阅读；真正激活使用 `mentioned_agent_ids`；
- 评论和 mentions 在同一事务保存；
- 每条 mention 有独立 ID、lease、retry/dead-letter 状态和唯一消费语义；
- 同一 Agent Instance 忙碌时 mention 延迟重试，不阻塞其他 Agent；
- 删除目标或永久前置条件错误进入 dead letter，不无限热循环。

委派本身是异步交接，不是同步子任务调用。正常 Issue 工具提示词要求：发布含结构化 @ 的委派评论并确认本次工具回执后结束当前 Run；不要用 sleep、native wait 或任务/状态/上下文查询轮询等待被委派者，也不要再覆盖本轮已发布的委派评论。需要结果返回后继续时，委派正文明确要求对方完成后在结果评论的 `mentioned_agent_ids` 中 @回委派者的真实 Agent ID，触发新的独立 Run；只写正文 @名字不会激活。收到结果不需要机械回@，但有实际需要的间接循环仍允许。

这是注入给模型的协作策略，不是强制取消或新的权限边界，不保证模型必然遵守。等待本次文件 IPC 工具回执、正常读取上下文、用户显式要求的评论修正及 Goal 的单条 working→final/审核门禁均保持原语义；结束当前 Run 不代表 Goal 已完成。

## 3. 循环委派

- Agent 不能直接 @ 自己；
- `A → B → A`、`A → B → C → A` 等间接循环明确允许；
- 系统不设置委派深度，不因检测到重复路径自动终止；
- 每一次回委派仍产生新的 durable mention 和 Run，因此可追踪、可停止、可恢复；
- UI 可提示重复委派，但不能悄悄丢弃；
- 用户通过停止单个实例、暂停整个 Issue、恢复和清空待执行队列控制循环。

允许循环不等于无条件并发：同一实例仍串行，不同实例并行。

## 4. Run 与上下文

mention 激活提示包含 Issue 标题、描述、状态、worktree 提示和公共评论历史。Agent 可看到其他 Agent 的
公共评论、工具名称/状态和脱敏结果，但不能看到隐藏思维链、凭据或 Provider 原始私有事件。

Agent 完成工作后必须调用 `issue_comment_create` 发布总结；模型最终文本不能被服务端伪装成公共评论。

## 5. 审批与策略

- Policy 数据模型可保存 allow、deny 或 require_approval，但当前不接入执行链；实际工具权限由 Provider 原生策略管理；
- shell、文件写入等高风险动作可形成 Approval；
- 决定绑定 run、action digest、version 和 scope；
- 重试必须幂等，过期或已决定的 Approval 不可重复决定。
- 活动 Run 的审批到期会持久化为 expired，并以 denied/once 下发 Provider；不自动批准，也不要求用户停止 Run 才能清除。

## 6. 计划任务

- 支持 cron 预览、启停、手动 run-now 和运行历史；
- 创建、更新、预览和推进使用同一日历规则；无未来日期的 Cron 拒绝创建，异常持久计划仅禁用一次，不进入到期热循环；
- 计划任务绑定 Agent，触发后创建普通 durable Run；
- concurrency/misfire 策略由 Store 原子执行；
- 计划任务与 mention 使用相同 Runtime、工具、审批和事件链路。

## 7. 验收

- 单 Agent mention → 评论 → succeeded；
- 多 Agent 同时 mention 均完成；
- `A → B → A` 产生三个成功 Run 和按序公共评论；
- 同一实例忙碌时回委派排队后继续；
- self-mention 返回明确错误；
- 暂停、停止、恢复和清队列不丢失已持久化事实；
- SIGKILL 后未完成命令和事件按幂等规则恢复。

# Teamva 领域模型与数据设计

## 1. 所有权树

```text
Local User
├─ Preferences
├─ Singleton Computer
│  ├─ AgentService
│  └─ Agent -> AgentRevision -> SkillPackageVersion
└─ Project
   ├─ Resource
   ├─ Issue
   │  ├─ IssueComment -> IssueCommentMention / Revision
   │  └─ IssueAgentInstance -> Generation -> Run -> RunEvent / Approval
   ├─ MemoryEntry -> MemoryVersion
   ├─ PolicyRule
   └─ ScheduledTask -> ScheduledTaskRun
```

User/Computer 是本机数据的所有权边界。首次启动自动创建唯一 User 和 Computer，UI 不提供选择或注册入口。
用户 Preferences 保存 system/light/dark 外观模式及全局 `appearance = { palette, accent?, background?, control? }`，七套明暗配色统一应用于整个工作台，默认使用 `default`。Project 领域模型、创建/编辑及公开 API 不包含 `theme`，切换项目不改变配色。SQLite 物理 `projects.theme` 旧列不改基线、不迁移，仅新建时写固定遗留占位满足 schema，读取/更新不参与配色。旧存 `appearance.palette=project` 只读投影为 `default`，不回写或增加版本，不覆盖其它全局预设与自定义色；详见 [全局外观与配色](APPEARANCE.md)。

## 2. 两份 SQLite

### Control Store `app.db`

保存产品领域、Session、策略、审计、command outbox 和事件收件事实，是 UI/API 的权威来源。

### Runtime Store `runtime/runtime.db`

保存 Provider command 幂等、未 ACK 事件、run native session 和 generation conversation session。
它不能越权修改 Control Store；工具请求统一经 ToolService 校验 run scope。

## 3. 一致性规则

- ID 使用带类型前缀的 UUIDv7 文本；
- 时间统一 UTC；
- 可编辑对象使用 `version` 乐观锁；
- 外键、唯一键和 CHECK 保证结构不变量；
- comment 与 mentions、Run 与 command outbox 在同一事务创建；
- command/event 使用稳定幂等键和累计 ACK；
- self-mention 拒绝，间接 mention cycle 不建唯一图约束；
- 所有跨对象查询显式带 user/project/issue scope。

## 4. SQLite

业务层依赖细分 Repository 接口，SQLite 是桌面控制面的权威实现。契约测试覆盖 preferences、
project/resource、Agent/Revision、Issue/comment/revision、Memory、Approval 和 Schedule。

## 5. Migration

- 控制面基线位于 `server/sqlite/schema/`，运行库基线位于 `node/internal/localstore/schema.sql`，均嵌入应用；
- `database_baseline` 记录基线 ID、定义 hash 和结构 hash；不存在历史升级链；
- 已有数据库在只读连接上验证基线和完整性，再进入恢复流程；
- 基线不匹配、结构被改动或 integrity check 失败时保留原数据库并拒绝启动；
- 不自动删除、重建或静默降级用户数据。

## 6. 数据服务边界

当前产品不需要在线备份、在线恢复、云同步或数据库损坏修复服务；这些不属于当前路线图。
诊断导出只包含版本、平台、服务状态、数据库文件元数据和脱敏日志，不包含数据库、WAL、Runtime session 或凭据。

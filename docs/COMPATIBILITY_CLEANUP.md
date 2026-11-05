# 当前基线与历史兼容清理（2026-09-14）

## 决策

产品没有需要兼容的已发布版本。用户明确选择：**新基线，拒绝旧库**。
本轮不保留一次性迁移、不升级旧数据，也不删除或重建用户现有数据库。

**新构建不能直接打开本轮之前的 app.db/runtime.db。** 使用新的
`TEAMVA_LOCAL_DATA_DIR` 进行开发和验收；旧文件保留供用户自行处理。
现存文件缺少当前基线标识时直接拒绝，不根据“看起来像空库/旧库”猜测初始化。

## 已删除的历史路径

| 类别 | 删除/收敛内容 |
| --- | --- |
| PostgreSQL / Atlas | `server/migrations/` 全部旧 SQL 与 atlas.sum、`server/atlas.hcl`、旧 PostgreSQL/SMTP/Cloudflare 环境示例 |
| 独立 Node CLI | `node/.env.example` 中旧 daemon HOME/设备密钥配置；远端设备授权类型、设备状态和废弃握手类型 |
| Node WebSocket | `Frame`、NewFrame、DecodePayload、hello/heartbeat/ready/ack 等 transport 类型；改成当前嵌入式命令/结果和批量事件预算 |
| 工作区绑定 | 一直为空/为 0 的 WorkspaceBindingVersion 字段、Runtime 回调、命令指纹成员和数据库列；项目 Resource 授权和 scope hash 保留 |
| app.db 升级链 | 删除全部增量 SQLite migration；当前结构直接定义，旧仓库选择列不再残留为 NULL |
| runtime.db 回填 | 删除打开时重建统计表/触发器、游标回填和表结构创建分支；缺失/不一致元数据直接拒绝 |
| 旧身份字段 | password_hash、email_verified_at、password_changed_at、step-up、会话旧来源枚举、废弃绝对会话过期字段 |
| 其他遗留列 | executable_path_fingerprint、provider_event_ref、wire_size_trusted；当前事件的 JSON、字节数及时间格式约束仍保留 |
| Provider 猜测 | 没有 provider_id 的原生输出不再遍历解码器猜测来源；没有身份的 Provider 输出不展示、不进入最终答案 |
| 旧 Provider 形态 | Codex assistantMessage/message 别名，OpenCode session.next.*、permission.v2.asked、旧 action/normalized_action.id 回退 |
| FileBridge 历史推断 | 不再从 write/read/bash 请求文件反推“读取任务/发布评论”，不恢复旧 request content；只认当前显式 provider.tool 事件 |
| 序列化兼容 | GitRepositoryList.Items 的 Go-only 别名、外层 data 包装事件回退、旧成功 poison completion 重解释 |
| 死文案/类型 | 远端邮箱密码、设备注册、PoP/relay、旧 wait-group 错误映射，九种语言中的旧身份审计文案 |
| 过时说明 | 协议 README、数据库启动/开发指南和网站文案统一为当前实现；MVP 文档改名，纠正必须升级已有库的旧要求 |

## 新数据库行为

- 控制面：`server/sqlite/schema/`，按领域拆分文件，但共同构成**一个基线**，不是新的升级链。
- 执行面：`node/internal/localstore/schema.sql`。
- 共用验证：`protocol/sqlitebaseline`；`database_baseline` 保存基线 ID、定义 hash、结构 hash。
- 已有文件先以只读连接检查完整性与基线；通过后才允许 WAL、身份/密钥初始化、当前版本恢复与保留策略。
- 初始化仅发生在新数据库，事务失败不留下部分结构；不匹配或结构漂移时保留文件并拒绝启动。
- 旧库、缺失统计元数据、缺失游标和旧诊断格式的测试改为“拒绝且不改写”，不以删测试掩盖数据风险。

## 明确保留的当前能力

这些不是历史兼容，不能因为关键词包含“旧/历史/fallback”就删除：

- Cookie 轮换的 previous-token 短暂重叠窗口：解决当前并发请求，而非兼容旧登录协议。
- CSRF、同源检查、LAN 默认关闭和令牌验证；目录/文件身份校验、资源授权与 Provider 实例隔离。
- 当前数据库的崩溃恢复、命令幂等、ACK 前缀、高水位、事件体积限制和损坏载荷安全隔离。
  旧格式不会被自动重解释为当前结果。
- 超过当前容量限制时的安全背压与排空；当前错误/取消不会变成成功。
- 当前 Provider SDK 实际存在的类型、不同原生格式，以及真正的“不支持能力”返回。
- 浏览器/Wails 能力检测、跨平台进程回收、目录打开等正常平台分支。
- Memory/评论/Agent 修订、运行记录和审计等**业务历史**；它们不是旧架构兼容代码。
- 旧请求被拒绝、文件不被改写的负向测试，以及注明轮次的验收证据。

## 验证

- `pnpm test:backend` 通过；最终 `pnpm verify:backend` 的 server/node/desktop race + vet、protocol 测试全部通过。
- Web：35 个文件、**201 项测试通过**；Svelte check 0 error / 0 warning。
- 工作台隔离生产构建、官网 check/build 均通过；工作台仍有既有大 chunk 提示。
- 使用新数据目录的真实 Chrome **44/44 通过**：核心流程、Provider 通用事件、图交互、目标、会话安全与桌面/移动布局。
  本机证据目录：`am-no-compat-20260914/browser-2p1ra1/results/`。
- Provider 生成文件检查、验收比较工具测试、ShellCheck 和差异检查通过。
- 本轮没有真实 Wails 窗口或公开制品验收；浏览器不调用模型。上轮真实 Provider 记录不能直接冒充这次新基线验收。

## 操作边界

没有迁移、清理、覆盖或重启正式数据/服务；没有提交、推送或发布。
保留此前工作区中的 Provider 重构、真实运行修复和 UI 稳定性工作。
新基线回归只使用临时数据库，验收服务已关闭。

# Teamva 权限安全与审计

## 1. 信任边界

| 边界 | 结论 |
| --- | --- |
| Wails WebView → Local API | 只信任有效 Session、CSRF 和精确同源请求 |
| 其他本机进程 → loopback | 不因来自 localhost 自动信任 |
| Local API → SQLite | Repository 事务和 scope 是权威 |
| Runtime → ToolService | 每次校验 computer/run/issue/agent scope |
| Agent → 文件/shell | 项目及完整授权 scope 的稳定 Workspace 为 CWD，仓库地图相互隔离；显式批准目录身份固定，替换后失败闭合。文件工具权限与任意 Shell 的 OS sandbox 必须分别验证，不能将 prompt、目录 allow 或禁用全部 Shell 当作完整安全与功能验收 |
| Skill scripts/hooks | 等同于以当前用户权限运行受信任代码 |
| Codex/OpenCode 凭据 | 留在 Provider 本机目录，不进入 Teamva 数据库/诊断 |

## 2. 本地 Session

- 本地 HTTP 必须只监听 loopback；
- bootstrap nonce 256-bit、单次、30 秒、内存保存；
- Cookie HttpOnly/SameSite，写请求继续校验 CSRF 和 Origin/Referer；
- `runtime.json`、日志和数据库使用当前用户私有权限；
- 第二个桌面进程由单实例锁拒绝，不能并发打开同一 SQLite。

## 3. 数据与日志

- 数据目录 0700；数据库、runtime state 和日志 0600；
- 日志在写入前脱敏 nonce、token、authorization、cookie、password 和 secret；
- 诊断 ZIP 排除 `app.db`、WAL、`runtime.db`、bootstrap state 和 Provider 凭据；
- 基线/integrity 失败保留数据库，拒绝旧库，不升级、回填、清库或重建；
- 产品不提供在线备份/恢复，诊断导出也不能用于恢复。

## 4. Agent 与工具

- Agent 只能修改当前 run scope 允许的对象；
- Agent 只能修正当前 Issue 内自己的公共评论；写入事务拒绝已结束或正在停止的 Run；
- 结构化 mention 必须指向同一用户、同一 Computer 上有效 Agent；
- 禁止直接 self-mention；间接循环委派允许且全部审计；
- shell、文件写入等动作由 Provider 原生权限策略拒绝或产生 Approval；`policy-rules` 当前仅保存规则，不参与执行决策，不能作为强制安全边界；
- Run 不接受客户端 CWD、Path 或单仓库选择；Server 只下发租户内 ready Git Resource，Node 再规范化并重验路径身份；
- `resource_prefix` 的 Codex/OpenCode 写权限只覆盖每个精确 Resource 根，不提升到共同祖先或 Git 根；
- Provider/native session 的复用键包含 project 与授权范围 hash，范围变化后必须换实例；
- ToolService 响应脱敏，评论写入以独立幂等键和同事务持久化回执合并重试；内存缓存不是唯一的防重依据。

## 5. 人工急停

循环委派和多 Agent 并行可能消耗 CPU、内存和 Provider 额度。产品不自动截断间接循环，但必须保留：

- 停止单个 Agent Instance；
- 暂停整个 Issue；
- 恢复 Issue；
- 清空尚未执行队列；
- 在 UI 中显示运行、排队、审批和错误状态。

## 6. 审计

项目、资源、Agent、Issue、评论修订、Run、Approval、Policy、Schedule 和关键设置变化都进入审计。
审计记录可包含对象 ID、动作和脱敏元数据，不包含凭据、完整工具秘密输入或隐藏思维链。

## 7. 本机安全

loopback 服务仍使用一次性 bootstrap nonce、HttpOnly Session、CSRF 和 Origin 校验。本机运行不意味着
任意本机进程都能调用 API；Provider 凭据也不得进入控制数据库、诊断包或活动记录。

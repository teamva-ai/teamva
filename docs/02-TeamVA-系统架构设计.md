# Teamva 系统架构设计

## 1. 进程拓扑

```text
Teamva.app
├─ Wails native window
│  └─ WebView ── HTTP/SSE ──> 127.0.0.1:<random>
├─ Go Local API + scheduler
│  ├─ app.db                 桌面控制面权威状态
│  └─ Local Dispatcher      durable command/event 调度
└─ Embedded Runtime
   ├─ runtime/runtime.db     执行幂等、outbox、native session
   └─ Codex / OpenCode child process
```

应用只监听 loopback，端口由操作系统分配，不开放局域网或公网入口。

## 2. Wails 职责

- 单实例锁、窗口生命周期和原生菜单；
- 启动/关闭完整本地服务；
- 用一次性 bootstrap URL 把 WebView 导航到同源应用；
- 日志、诊断导出、更新检查和保存对话框。

业务 CRUD、Run、审批、SSE 和设置全部通过 HTTP，不走 Wails 领域 binding。

## 3. 本地服务

`server/app.StartLocal` 负责：

1. 建立权限为 0700 的数据目录并取得进程锁；
2. 只读验证已有 SQLite 的当前基线；仅对新空库初始化结构，随后 seed 本地 identity/skills；
3. 恢复 durable execution 状态；
4. 启动 Embedded Runtime、mention/schedule worker；
5. 在同一个服务端口提供 SPA、API 和 SSE；默认仅 loopback，显式开启局域网后仍用同一端口号；
6. 关闭时停止新请求、回收 Provider 子进程、checkpoint WAL。

## 4. 存储边界

- `app.db`：产品领域权威状态；
- `runtime/runtime.db`：Provider 执行和传输恢复状态；
- `runtime/workspaces/projects/<project-scope-hash>/`：按项目及完整授权 scope 隔离的稳定 CWD 与 `AGENTS.md`；
- `runtime/resource-authorizations/`：显式验证产生的私有批准版本、canonical path 和目录身份摘要；不进入 SQL 基线、不自动回填；
- `logs/`：0600、单文件 5 MiB、最多三份历史；
- `runtime.json`：当前 loopback endpoint，只供本地生命周期/测试读取；
- Provider 凭据继续由 Codex/OpenCode 自己管理，不进入上述数据库。

SQLite 使用 foreign keys、WAL、NORMAL synchronous 和 busy timeout。领域层通过细分 Repository 接口
访问数据，不把 SQL 或数据库实现细节泄漏到页面和 Runtime。

## 5. 执行链路

```text
comment @Agent
  -> durable mention
  -> scheduler claim
  -> Run + command outbox
  -> Embedded Runtime
  -> Codex/OpenCode
  -> ToolService / run events
  -> SQLite transaction
  -> SSE notify UI
```

同一 Agent Instance 内 Run 串行，不同实例可并行。Agent 评论可以创建新的 mention；间接循环不做图检测或
自动终止，直接 self-mention 在 ToolService 拒绝。

Run 不绑定单个 Git 仓库。Server 在 `run.start` 下发项目全部 ready Git Resource（最多 64 个），Runtime
最多发现 100 个仓库，以项目及完整授权 scope 的稳定 Workspace 作为 CWD，并用独立 `AGENTS.md` 告知 Agent 精确可写根；不同 scope 不覆盖活动 Run 的地图。完整
仓库授权仓库根；`resource_prefix` 只授权注册 Resource 的规范根，即使多个根属于同一个祖先仓库也不扩大。
Provider 实例与 native session 绑定项目及完整授权范围。

Runtime 在 Provider 执行前后为每个仓库保存快照，每 Run 最多 200 行。全局捕获超时后，尚未处理的仓库
也会写入带 warning 的 fallback；重启时 start-only 行补成明确 incomplete 的 end 行。HTTP 在统一字节、
路径预算内返回并按仓库分组，无法确认仓库归属的 Provider report 保持独立。

## 6. 崩溃恢复

- `processing` command 在异常退出后进入 uncertain，再按幂等状态恢复；
- 未 ACK event 保留并重放；
- 活动 Run 在失去 Provider 子进程后进入可解释终态，不永久 running；
- native session 和 generation conversation session 持久化；
- 基线/integrity check 失败时停止启动并保留原数据库。

项目不提供在线备份/恢复服务，也不把诊断包当作备份。

## 7. 开发阶段变更原则

当前没有需要兼容的已发布版本。只维护当前路径，不增加旧字段 fallback、双读双写、旧协议分支或旧配置迁移。
旧数据库明确拒绝启动并保留原文件。新构建使用新数据目录，绝不为了启动而自动清库。

# Teamva 架构决策记录

## ADR-001：本地 Wails 桌面是当前主产品

决定：`main` 发布 macOS Wails 应用。
原因：用户需要离线、低依赖、直接复用本机 Provider 的工作台。

## ADR-002：HTTP 是唯一前端业务边界

决定：Svelte 只访问同源 Local API/SSE；Wails binding 只管原生生命周期。
原因：保持 API 可黑盒测试，避免第二套私有业务接口。

## ADR-003：双 SQLite 持久化

决定：`app.db` 保存控制面，`runtime.db` 保存执行恢复。
原因：领域事实与 Provider 传输状态隔离，并都能在崩溃后恢复。

## ADR-004：本地身份仍保留 User/Computer

决定：首次启动 seed 本地 User 和 singleton Computer，UI 隐藏远端设备概念。
原因：本机身份让领域对象保持明确所有权，同时不给用户增加账号或设备配置负担。

## ADR-005：命令先持久化再执行

决定：Run、mention、审批、event 使用 durable outbox/inbox 和幂等键。
原因：进程退出、Provider 异常和重试不能丢任务或重复事实。

## ADR-006：Embedded Runtime 复用 Node Adapter

决定：桌面 Embedded Runtime 复用 Codex/OpenCode Manager、Adapter、Run 级 MCP 工具通道（FileBridge 仅作回退）和 ToolBroker。
原因：不复制 Provider 协议和 session 生命周期。

## ADR-007：领域依赖细分 Store 契约

决定：领域服务只依赖 consumer-owned interfaces，SQLite 细节留在 Store 内部。
原因：避免业务层耦合 SQL，并让事务和所有权边界可独立测试。

## ADR-008：Issue 取代 Conversation

决定：Project 下以 Issue、公共评论、Agent Instance 和 Run 协作。
原因：任务状态、委派、工具和审计比聊天窗口更适合多 Agent 工作。

## ADR-009：允许 Agent 间接循环委派

决定：拒绝直接 self-mention，但允许 `A → B → A` 等间接循环，不设置深度或自动终止。
原因：循环复核和互相补充是明确产品能力；控制权通过串行实例、持久记录和人工急停实现。

## ADR-010：本地安全不等于关闭鉴权

决定：loopback 服务仍使用一次性 nonce、HttpOnly Session、CSRF 和 Origin。
原因：其他本机进程不应自动获得业务权限。

## ADR-011：当前不提供在线备份和恢复

决定：在线备份、在线恢复、云同步和损坏修复不进入当前路线图。
原因：当前本地产品不需要该服务；更重要的不变量是失败时保留数据库、绝不自动清库。

## ADR-012：首版只构建 macOS universal2

决定：首版生成 ZIP、VERSION 和 SHA256SUMS；当前未签名未公证，更新只下载校验。
原因：先收敛真实 macOS 产品闭环，再决定签名、公证和其他平台。

## ADR-013：开发阶段不保留历史兼容路径

决定：不为旧行为、旧 API、旧配置或旧数据结构新增 fallback、双读双写和弃用期分支。
原因：产品尚未发布，保持一条明确路径比维护未被用户依赖的历史行为更重要。

## 决策变更流程

改变上述决策时必须同步代码、OpenAPI/Store 契约、测试、README、文档 00/10/12，并重新执行真实边界验收。

# Embedded Runtime 协议

本目录是本机控制面、`node/embedded` 与 Provider API 共用的当前 Go 契约。
不提供独立 Node daemon、远端注册/心跳握手或 Node WebSocket transport。

- 当前命令协议版本为 6；只接受这一版本，没有旧版本转换器。
- `CommandEnvelope` 保留稳定 MessageID、ComputerID、过期时间、幂等键与 trace context。
- 工作区由 Project 和已授权 Resource 确定，不再携带 workspace_binding_version。
- 全局工作记录助手是明确的例外作用域：`run.start.assistant_session_id` 非空、`project_id` 为空，必须 `read_only=true`，禁止 Issue/图节点、Git Resource、Skill、Computer Use 和外部 native session。工作区是应用内 `workspaces/home/<session-key>`，不是伪造的 Project。
- 助手挂载 `work_records_query`、`work_record_get`、`work_history_read` 等只读文件桥；服务端依据持久化 Run 的用户、设备和 `purpose=work_assistant` 再鉴权。原始历史按需只读，分页读取，不复制进工作记录。
- 全局灵感助手复用相同执行边界，会话在控制面标记 `scope=inspirations`。只读文件桥新增 `inspiration_list` 和 `inspiration_get`，读取当前用户的便签列表和 Markdown；不提供编辑、完成或删除工具。所选便签仅以 ID 和提交时版本元信息进入提示，正文按需检索。
- 文件桥目录属于宿主 IPC：OpenCode 保留原有的精确桥接目录许可；Codex 为助手的隔离 CWD/IPC 使用限定 sandbox write roots，关闭网络和临时目录写入，审批策略为 never，不授予任何业务仓库写根。普通只读 Run 的原有 sandbox 不变。
- Runtime 事件按 Run/lease 持久化递增序号；传输至少一次，消费幂等，持久化完成后 ACK。
- `run.stop.mode` 传递 `graceful` / `force`（未指定时为 graceful）。force 先取消执行/工具上下文，原生中断确认与退出收尾共用 2 秒窗口；窗口内保留已接受的诊断与压缩事件，超时则在同一 sink 屏障内先收拢悬空压缩、再持久化唯一 cancelled terminal，之后才确认 stop。先到的原生终态保持权威；终态后回调不再追加事件。此确认表示执行流已封口，不承诺操作系统进程已完成物理退出；实例仍由原 Run 持有直到 Provider 清理结束。
- graceful 对仍注册的 `run.start` 返回 `terminal_pending=true`：只确认中断请求，server 可完成 stop 控制命令，但不得据此合成 Run 终态；保持 cancelling，等待原 Run 的终态/失败收尾或 force 升级。force 的确认仍必须在上述持久化屏障之后，不能返回 pending。
- Provider 事件带 `provider_id`；原生解码器不猜测没有身份的来源。
- `MaxCommandPayloadBytes`、`MaxCommandResultBytes`、`MaxRunEventBatchBytes` 分别约束当前 JSON 载荷。
  `EncodeCommandResult` 直接检查结果，不包装废弃的 WebSocket Frame。
- SQLite 当前基线验证位于 `protocol/sqlitebaseline`；旧库拒绝启动并保留，不自动升级或重建。

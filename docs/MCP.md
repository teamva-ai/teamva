# 技能 & MCP

本机入口 `/skills` 使用页内 Tab 在「技能」与「MCP」之间切换，`/skills?tab=mcp` 可直接打开 MCP。技能的商店、安装、卸载、目录打开、拖动和 Agent 绑定仍使用原有实现。

## 配置

配置以 [OpenCode V2 MCP 文档](https://opencode.ai/v2/docs/mcp-servers) 的 `mcp.servers` 条目为准：

- 本地：命令 argv（不经过 Shell）、工作目录、环境变量；远程：HTTP URL、请求头、OAuth。
- 个人配置适用于所有项目；当前项目同名条目覆盖个人配置，项目的 disabled 条目同样阻止个人条目回落生效。
- OAuth 具有 Client ID、Client secret、scope、回调端口、重定向 URI 和授权服务器元数据 URL。
- 管理器内置授权使用临时随机 loopback 回调，并校验 state、PKCE 和 issuer；固定回调端口/URI 只保存并透传原生 Provider，不是本页授权器的能力，不额外暴露第二个固定服务端口。
- 协议 `legacy` / `auto` / `2026-07-28`、Code Mode 默认/开启/关闭、启动/目录/执行超时（毫秒）。
- JSON 导入支持 OpenCode V1/V2、Claude `mcpServers`、VS Code `servers` 和独立条目/命名映射；明确选择后导入，拒绝覆盖同作用域的同名条目。已有配置合法修改自动保存，版本冲突保留草稿。
- 创建或保存不执行命令，不代表连接成功；需要显式连接或开始授权。状态与工具、提示词、资源目录来自实际连接，此页面不调用工具。
- 本页连接/断开管理器自己的探测会话，不代表全部 Provider 实例的连接状态；启用开关决定后续 Run 的有效配置，不打断运行中的 Run。

## 安全与运行边界

- 管理 API 只允许本机 loopback 的本机已认证用户；写操作继续经过严格同源与 CSRF。远程客户端不能管理或读取配置。项目范围必须通过现有项目所有权检查。
- MCP 配置与 OAuth 凭据只保存在本机独立私有目录和 0600 文件中，以原子替换写入，不进入 app.db/runtime.db、持久化命令、事件、工作记录正文、日志或诊断归档。读取失败保留原文件，不自动重建。
- 环境变量、HTTP 请求头的值和 OAuth client secret 不回显。响应中使用 `__TEAMVA_KEEP_SECRET__` 保留哨兵；编辑器显示空白和「已设置；留空保留」，删除对应键/密钥才删除存储值，不能手工输入哨兵创建新密钥。
- 非认证配置（超时、启停、协议和 Code Mode）编辑保留已授权凭据；端点或认证字段变化则撤销旧缓存，禁止把旧凭据带到新的服务。读取 API 返回的是安全投影，不是可以导出秘密的配置备份。
- 子进程只以 argv 执行；任意自定义 MCP 可以执行本机程序或连接外部服务，添加前必须信任其来源。连接产生的原始网络错误、stderr 和 OAuth token 不返回 UI。
- 正常可写 Issue Agent 在下一次 Run 准备时按项目解析配置，私密快照只进入受保护的 Provider 实例配置；配置变化回收空闲 adapter，不中断活动 Run。工作记录、灵感等只读会话以及只读分支对话不会获得自定义 MCP 工具；Lumi 执行普通 Issue 时按正常配置 Agent 规则使用，不被身份额外限制。
- OpenCode 使用 V2 `mcp.servers`；Codex 转换为原生 TOML `mcp_servers`，共享基础本地/远程配置。OpenCode 专属 protocol、Code Mode、catalog timeout 和授权元数据 URL 不伪装成 Codex 能力。Computer Use 保留名称不能被用户自定义条目覆盖。

## HTTP 契约

`GET/POST /v1/mcp/servers`、`DELETE /v1/mcp/servers/{name}`、`POST /v1/mcp/servers/{name}/{connect|disconnect|auth|revoke}`。条目身份为 scope、project_id、name，新增 version=0，更新/删除使用版本锁。配置通过 HTTP 的专用内存通道交给管理器，不作为业务 Runtime command。

新建条目使用随机版本种子防止删除重建 ABA，已有条目保存严格递增；前端可据此拒绝晚到的旧读取，但不能假定初始版本为 1。

## 验证范围

管理器使用离线 MCP/OAuth fixture 测试真实握手、目录和授权生命周期；Provider 配置与 Runtime 测试验证真实字段转换、复用实例配置刷新、只读隔离和秘密不进入数据库。浏览器专项 `scripts/e2e/mcp.spec.mjs` 只显式启动离线 stdio fixture，不调用模型或生产 MCP。

自动化检查、真实浏览器、真实 CLI 原生协议检查、真实模型、Wails 与公开制品验收须分别报告；保存配置、readyz 或构建成功不能替代这些证据。

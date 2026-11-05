# 本机 MCP Manager

公开包供 `server/app` 导入，不依赖控制面数据库、Runtime、Provider 或模型。

## API 与秘密边界

- `New(Config{DataDir: absolutePath})`：只加载配置，**不执行命令、不连接 MCP**。存于 `DataDir/mcp/config.json`，私有目录 0700、文件 0600，临时文件写入、fsync、rename、目录 fsync。拒绝符号链接及损坏配置，不重建损坏文件。macOS 固定系统 `/var/` 别名被规范成 `/private/var/`；其他调用者路径中的符号链接均拒绝。
- 同一目录一个 Manager 持有排他文件锁；第二个实例返回 `ErrConflict`。同一 Manager 并发操作串行；创建使用随机 48 位正整数种子防止实际 delete/recreate ABA，更新严格 +1，JS 安全整数溢出拒绝且不修改旧数据。
- `Save(Input)`：Version 0 创建，非零必须匹配；全量替换 config，删除 key 即删除秘密。不会启动进程。修改会断开旧连接；只有 remote URL/oauth/environment/headers 认证身份变化才清除 OAuth 缓存，disabled/codemode/protocol/timeout 编辑保留缓存。
- `List(ctx, projectID)`：同时保留 user 和该 project 的同名条目，保留 scope；被同名 project（包括 disabled）覆盖的 user 条目带 `shadowed:true`。`Effective` 才应用 project 覆盖 user。不会暴露其他 project。
- `View.Config` 的 environment/headers **全部值**和 oauth.client_secret 均为 `__TEAMVA_KEEP_SECRET__`。更新现有同名 key 时该哨兵保留旧值；创建或新增 key 不接受哨兵。URL 不接受 userinfo、fragment、敏感查询参数；命令不接受明显秘密参数，应把秘密放 environment。
- **`Effective` 是仅限服务器内存的秘密 API**，不可直接响应客户端、记录日志、写数据库/事件/诊断或命令参数。已登录远端会按需刷新 token，再注入 `headers.Authorization: Bearer ...` 与 `oauth:false`；不修改用户保存的 headers。过期而无法刷新会返回泛化授权错误，不输出失效凭据。
- 错误为泛化错误码，`ErrInvalid`、`ErrConflict`、`ErrNotFound` 支持 `errors.Is`。不回显 SDK 错误、URL、stderr 或凭据。SDK logger 和本地 stderr 均丢弃；目录文本中的已知配置/token 秘密也会脱敏。

## 连接与协议

`Connect` 显式执行本地 argv（不经过 shell），只继承最小运行环境，并合并私有 environment。超时、断开、编辑、删除、关闭会取消连接并清理进程组。远端优先 Streamable HTTP，非强制 2026 的配置失败时尝试传统 SSE；禁止跨 origin 请求和 redirect 携带认证头。

使用官方 `github.com/modelcontextprotocol/go-sdk v1.8.0`。`legacy` 从 2025-11-25 initialize 开始；`auto` 先实际 discovery，再允许降级；强制 `2026-07-28` 仅在实际协商结果为此版本时成功，否则报告 `unsupported` / `mcp_protocol_unsupported`，绝不伪装支持。目录包括分页 tools、resources、resource templates 和 prompts，各设数量和阶段时间限制。管理器不执行工具或模型；codemode、execution timeout 等 Provider 配置原样保留。

## OAuth

`StartAuth(ctx,scope,projectID,name)` 返回 `authorization_url`、`status`，由本机管理端打开 URL，UI 通过 List 轮询状态。支持受保护资源 metadata、授权服务 metadata（含显式 `auth_server_metadata_url`）、预注册客户端与动态客户端注册、S256 PKCE、随机 state、issuer 校验、随机独立 `127.0.0.1` 回调监听。授权码、客户端秘密、access/refresh token 从不返回 UI。仅 HTTPS 和字面 loopback HTTP 可用于 OAuth，所有重定向关闭，不把 MCP 自定义 headers 携带到 metadata/授权服务。回调一用即关、十分钟超时；取消、删除、断开、关机及时关闭。成功交换后原子私有保存并自动连接。

`RevokeAuth` 清除本机 OAuth 缓存并断开连接；**不是**向授权服务承诺 RFC 7009 全局 token 撤销，不删除用户手填 Authorization。刷新序列化以支持 refresh-token rotation，并保留 RFC 8707 resource 指示。

明确限制：Manager 自有回调必须是随机临时端口；配置中的固定 `callback_port` / `redirect_uri` 会安全保存并透传 Provider，但不能用于 Manager 自有登录（StartAuth 返回泛化失败）。不偷偷忽略固定重定向、不绑定固定或外部 callback。客户端元数据文档注册、DPoP、交互式工具执行不在本包范围。

## 本地验收

```sh
cd node
GOWORK=off go test -race ./mcpmanager
GOWORK=off go vet ./mcpmanager
```

测试仅运行临时目录、测试二进制 stdio fixture 与 loopback HTTP/OAuth fixture，不调用生产 MCP 或模型。覆盖存储权限、损坏/符号链接拒绝、范围覆盖、版本并发、哨兵、真实三类目录、legacy/auto/2026 协议、授权回调/PKCE、凭据脱敏、真实刷新、Effective 内存注入、撤销保留手工 header 和关机回调清理。

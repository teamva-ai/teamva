# Codex MCP 适配范围

当前已核对 `codex-cli 0.163.0` 的版本化原生 schema：
[RawMcpServerConfig / McpServerOAuthConfig](https://github.com/openai/codex/blob/rust-v0.163.0/codex-rs/core/config.schema.json)。

- 使用 `[mcp_servers."名称"]`（不是 TOML array-of-tables），本地 argv 转为 `command` / `args`，支持 `env`、`cwd`、`enabled`；远程支持 `url` / `http_headers`。
- 毫秒 `timeout.startup` / `timeout.execution` 转为秒 `startup_timeout_sec` / `tool_timeout_sec`。
- 此版本真实支持 `[mcp_servers."名称".oauth]` 的 `client_id`、`client_secret`、`callback_port`、`callback_url`。公共配置的 `redirect_uri` 映射到 `callback_url`，`scope` 拆为服务器级 `scopes`。这些是已公开的原生字段，并非假定支持。
- 不转译 OpenCode 专属 `protocol`、`codemode`、`timeout.catalog`、`oauth.auth_server_metadata_url`。Codex 无对应 `oauth:false` 禁用选项，不承诺两者的认证行为完全相同。
- `client_secret` 等仅留在实例私有 0600 配置中；CLI 的 `mcp list/get --json` 可能包含 env/header 值，测试仅在内存比较，不输出原始结果。
- 原生配置解析验收不等于 OAuth 登录/刷新、远程连通或模型实际调用验收；本测试不启动登录流程、不访问模型。

独立无模型验收：

```sh
cd node
TEAMVA_CODEX_TEST=1 go test ./provider/codex -run '^TestNativeCodexMCPConfigWithoutModel$' -v
TEAMVA_OPENCODE_TEST=1 go test ./provider/opencode -run '^TestBundledMCPContractWithoutModel$' -v
```

可用 `TEAMVA_CODEX_BINARY` / `TEAMVA_OPENCODE_BINARY` 指定要验收的二进制。测试记录实际版本；HOME/XDG/CODEX_HOME 隔离，子进程环境采用允许列表，不复制用户认证文件。OpenCode 测试启动真实内置服务器与离线 MCP fixture，仅查询 MCP 状态及资源目录，不创建模型会话。

## 0.163 原生工具审批与活动

Teamva 不向用户展示审批：Run 级 `teamva-issue` server 设置
`default_tools_approval_mode = "approve"`，所有 MCP 工具调用预批准；其余动作使用
`approvalPolicy=never`，需要升权的动作直接失败。`mcpServer/elicitation/request`
（`mode=form`、空 object schema、`_meta.codex_approval_kind=mcp_tool_call`）的映射与
`ApprovalDecision` 处理仅作为防御性回退保留，正常 Run 不应再产生
`approval.requested`。普通输入表单、URL/OAuth、设备验证不假装支持。原生返回值必须是
`action=accept|decline`、`content=null`、`_meta=null`，不能返回 command 的 `decision=accept`。

旧适配器对该方法返回 -32601；原生 app-server 将客户端错误转成 Decline，进而显示
`user rejected MCP tool call`，所以并不代表真的有人拒绝。确定性原生测试已复现旧行为，
并验证修复后等待宿主决定、允许成功、拒绝失败，未改变 filesystem profile。
取消 Run 或原生 interrupt 均不执行待审批工具，过期决定不可再使用；Run 结束清除该 Run
的 pending approval。不支持的非空输入表单返回 unsupported，由原生安全拒绝，不发第二张二元审批卡。

`view.ts` 保留 `mcpToolCall` 的 server/tool、arguments、原生 status、result/error 与 durationMs，
没有 shell exit_code。`item/mcpToolCall/progress.message` 是进度快照，不拼成 stdout/result；
最终结果仅从原生 result 提取。ingress 用共享预算恢复 text blocks，保留有界结构化结果，
不恢复图片、资源附件或二进制；缓存淘汰、最终快照刷新与 interrupted 均有独立测试。

```sh
TEAMVA_CODEX_TEST=1 go test -race ./provider/codex -run NativeCodexMCPApproval -v
```

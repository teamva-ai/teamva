# Claude Code Provider

`node/provider/claude/` 通过官方 CLI 的双向 `stream-json` SDK 协议接入。目录发现注册，id/默认 command 均为 `claude`；不在 Runtime、Controller 或 UI 增加 Provider switch。最低要求为官方 CLI 支持本接入使用的 `--bare`、空 `--setting-sources`、`--strict-mcp-config` 及 SDK MCP；本轮真实协议验证版本为 **2.1.316**。

## 能力与限制

- 支持流式正文、上游实际公开的 thinking、原生 resume/session ID、interrupt、只读及 Issue 协作工具。
- Issue 工具优先使用 Runtime 提供的 Run 级 HTTP MCP（`teamva-issue`）；宿主未提供时才回退到 FileBridge。两种传输都由同一 ToolBroker 授权，模型不能传 run/issue 身份。
- **不提供 Shell、Git、任意进程执行、网络工具、原生 Read/Write/Agent、用户 Skills、Computer Use、图片、steer、外部自定义 MCP 或用户审批升级**；未实现的能力不声明，配置不静默忽略。
- 该版本不是任意 Shell 的操作系统沙箱；没有使用 `acceptEdits`、`bypassPermissions` 或危险跳过权限来冒充沙箱。CLI 原生工具集合严格设为空；唯一可调用工具由宿主 SDK MCP 实现，无工具子进程，不会让工具继承模型认证环境。
- 文件工具只有 `read_file`、`list_directory`、`write_file`。读限制在 CWD 与实例授权仓库；写**只**允许显式 `WritableRepositories`（CWD 不隐式获得写权限）。ReadOnly 移除写工具并在调用端再次拒绝；DisableTools 连 SDK MCP 都不注册。
- 文件访问在 Run 建立时固定授权根与完整祖先链的目录 FD，并校验各名称到 inode 的绑定；每次访问校验身份并只从固定 FD 派生，绝不重新按根路径 `OpenRoot`。根或祖先被替换时失败关闭；初次固定也使用逐级 `O_NOFOLLOW` 与打开前后身份比较。仅规范化 macOS 固定系统别名 `/var`、`/tmp`、`/etc`，不解析项目自带的根/祖先 symlink。
- 根内路径拒绝 symlink、私有 Provider home、`.git`、`.claude`、`.teamva`、`.env` 等敏感路径。读在打开前拒绝非 regular 对象，并以 `O_NOFOLLOW|O_NONBLOCK` 打开后再次检查 regular file/link count，FIFO 的替换竞态不能阻塞宿主 open。读拒绝多硬链接，写通过原子替换避免修改外部硬链接 inode。只提供有界单目录列表/UTF-8 文本读写，不提供递归删除、任意 mkdir 或权限变更。

这些限制意味着模型不能真实运行项目测试或 Git 命令；必须在回复中如实说明，不能把静态推断当作执行结果。若之后扩展 Shell，必须另行实现、审计与验收真实 OS 权限及认证环境隔离，不能只加工具名。

## 配置与认证

- `ConfigDir/instance.json` 只存宿主实例配置，刷新后下一 Run 重新读取；不包含账号配置、hooks、插件、认证或外部 MCP。
- `InstanceConfig.WorkspaceInstructions` 是宿主生成的 `AGENTS.md` **文件路径**，不是提示词正文。每个 Run 通过固定父目录 FD、安全非阻塞打开与 UTF-8/大小检查读取实际正文，再与 SystemPrompt 一并写入私有 system-prompt 文件；文件缺失、非 regular、symlink、非法 UTF-8 或超过 1 MiB 时在启动 CLI 前失败，不依赖 bare 模式下被禁用的自动发现。
- 原生 HOME、`CLAUDE_CONFIG_DIR`、临时目录及 session registry 位于实例独立 `DataDir`；CLI 原生 transcript 留在该私有原生 home，不扫描或修改用户原生会话。
- 始终使用 `--bare --disable-slash-commands --setting-sources= --strict-mcp-config --tools ''`，不执行用户/项目 hooks、slash skill、插件或未知 MCP。官方 CLI 自带插件的 init 元数据并不代表载入用户插件；init 不整体进入事件。
- 仅从 `~/.claude/settings.json` 的 `env` 投影 `ANTHROPIC_BASE_URL`、API Key/Auth Token、显式模型与 effort 等必要字段；不导入 OAuth、keychain、`apiKeyHelper`、任意 env 或 inherited `NODE_OPTIONS`。配置文件须为有界 regular file；端点只允许 HTTPS 或 loopback HTTP，不允许 URL 用户认证、query 或 fragment。
- `CLAUDE_CODE_AUTO_COMPACT_WINDOW` 也可显式投影，但只接受规范十进制整数字符串 100000..1000000，单位为实际 token；不接受空值、单位、浮点、指数、符号或越界 clamp，不继承进程同名环境。测试加速用的 MAX_RETRIES/API_TIMEOUT 等不属于产品白名单。
- 官方 `--bare` 只认 API_KEY，因此有 API_KEY 时优先使用；仅有 AUTH_TOKEN 时只在子进程内映射为 API_KEY，不同时传两者，不改用户原文件。秘密不进入命令参数、实例配置、事件、Runtime 命令、数据库或诊断。适配器不打印或传递 raw 原生帧；所有对外文本在进入 Sink 前清除已知认证值与常见凭据字段/令牌模式。
- Probe 使用隔离环境执行真实 `--version` / `--help` / `auth status --json`；认证状态只代表 CLI 识别已配置凭据，**不声称模型调用成功或 Key 一定有效**。模型列表只来自用户显式配置，不编造可用性或免费承诺。

## 原生协议与公共总结

官方 `initialize` control request 完成后发送 SDK user frame；宿主处理 `can_use_tool` 与 `mcp_message` control request，并回复官方 `control_response` 格式。只允许本 Run 的 `mcp__teamva__*` 受控工具，未知工具、server、hooks 或权限升级全部拒绝。

`mcp_message` 的 `tools/call` 在执行任何文件/IPC 副作用之前，递归检查已 JSON 解码的 arguments 字符串键和值（含嵌套对象、数组和 Unicode escape）。若包含本次 `accountEnvironment` 投影的完整 API Key/Auth Token，返回安全 `isError` 工具回执，不执行调用、不改写参数、不写请求文件，也不终止整个 Run。不能只依靠可被 native 跳过的 `can_use_tool`。这只是已知投影凭据的最小防线，不扫描其它用户秘密，不承诺任意编码或跨工具编码 DLP；不含已知凭据的业务参数保持原样。

回退路径中，宿主 FileBridge 的当前 `ToolRequestDir` 中已创建的固定工具目录才被映射为同名 SDK MCP 工具；调用原子发布唯一请求 JSON，最多等候 30 秒读取真实 `.response.json` 回执。MCP 路径则由 Runtime 直接启动 `teamva-issue` HTTP server，工具调用走同一 ToolBroker。ReadOnly 不暴露业务写工具；DisableTools 不启动任一通道。**Issue 公共总结仍必须调用 `issue_comment_create`**，普通 assistant 文本绝不转换为评论。

Session ID 使用宿主生成 UUID；resume 同时校验当前实例 registry、真实原生 transcript 与 CWD，不能跨 workspace 恢复。子进程通过公共 process-tree/watchdog 管理，关闭或取消会收拢后代；interrupt 先发原生控制消息，未完成时进程树终止兜底。

## 事件与错误

正文/思考/工具/重试/用量/压缩均投影为公共语义事件，`view.ts` 不解析 raw。同一 native message 的多 content-block assistant 快照合并，不把 tool result、用户消息、隐藏 thinking、API 错误或压缩摘要当作最终回答。全部 Provider 事件保留 `Sequence=0`，由 Runtime 与 ToolBroker 共用游标原子分配；只有原生 `system/init` 确认正确 session ID 后才发送一次 `run.started`（含 `native_session_id`），重复 init 不重复发送，spawn/initialize control 成功不能伪造开始。

- 原生 `result.subtype=success` **仍可能 `is_error=true`**；失败保留 CLI 实际给出的安全 message、error code 与 HTTP status，缺失的上游正文/status 不编造。
- 原生错误通过公共 `provider.event {kind:"error"}` 发送，不能发浏览器未注册的私有事件名 `provider.error/provider.status`。`system/api_retry` 也通过公共 `provider.event` 保留实际 attempt/max_retries/retry_delay_ms/error_status、映射 message/code/status_code，并显式 `willRetry:true`；适配器不额外重发用户请求。错误/重试/终态/失败压缩在调用第一个 Sink **之前**复用公共 URL userinfo/query/header/DSN 诊断清洗，不依赖后续终态补救。
- 已知投影凭据的正文 delta 使用跨帧状态，暂缓未决的凭据前缀；累计正文/思考快照同样先清除完整秘密并隐藏未决前缀，再进入 Sink。完成时仍未决的前缀保守替换为 `[REDACTED]`，可能延迟/遮蔽与凭据前缀相同的尾部，而不是先把敏感片段入库、再靠最终 snapshot 修正。
- `status=compacting` → running；`status=null/requesting` 不代表完成；只凭 `compact_boundary` 或显式 `compact_result=success` 完成。`compact_result=failed` 透传安全 `compact_error`。Run 终止但无完成信号时收拢 failed/cancelled，不因 exit 0 伪造成功。
- `/compact` 本地命令本身可返回成功但压缩失败；保留原生 Run 结果，压缩保持失败，错误不进入最终回答。托管实例关闭 slash commands，因此不承诺可通过用户 slash 调用手动压缩；自动原生压缩事件照常支持。
- 用量仅使用原生每 turn 的 main-loop `usage`，保留已报告的计数；不把包含恢复历史的累计 `total_cost_usd/modelUsage` 当成本 Run delta，不以错误零值声称零消耗。

长度预算与脱敏独立：诊断文本最多 64 KiB，先脱敏再按完整 UTF-8 字符截断并附显式 `[diagnostic truncated]`；普通文件为 1 MiB UTF-8、FileBridge 回执为 8 MiB 有效 UTF-8 JSON，超界/非法数据明确失败，不发残缺 JSON。正常文件、回执和最终回答不套用诊断 64 KiB 截断。原生输入 JSON 单帧仍有 8 MiB 防护，持久化总预算由 Runtime 控制。适配器不扫描/改写 Claude 自己的私有原生 transcript，也不承诺识别任意未知或编码后的秘密。

## 验收

```sh
go -C node test -race ./provider/claude
TEAMVA_CLAUDE_TEST=1 TEAMVA_CLAUDE_PATH=/absolute/path/to/claude \
  go -C node test -race -v ./provider/claude -run TestRealClaudeSDKMCP -count=1
```

默认测试不读真实用户配置、不联网：fake CLI 覆盖隔离 argv/env、initialize、权限拒绝、SDK MCP→FileBridge 真文件回执、resume、取消、错误、压缩 completed/failed/cancelled；21 个官方 CLI 实际故障 result 摘录作为固定公共事件投影回归。安全测试实际执行授权根/祖先替换及并发重绑定、FIFO open/取消（含 regular 检查后的 FIFO 替换窗口）、第一条 Sink 错误脱敏、已知 Key 的每一个切分点、工作区指令刷新/失败，以及 70 KiB 级 UTF-8 文件/回执/最终回答。opt-in 验证真实已安装 CLI + 本地合成 Anthropic SSE 网关，覆盖 normal/read-only/no-tools/issue、>64 KiB SDK MCP 实际读取回传、FileBridge 请求回执、原生 transcript 与 resume 收紧工具范围。**这不替代真实官方模型、UI/Wails、完整 Issue 多 Agent 或公开制品验收**；授权真实调用由上层验收单独提供证据。

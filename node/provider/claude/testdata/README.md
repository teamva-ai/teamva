# 官方 CLI 原生协议固定样本

`native-results.json` 是 Claude Code **2.1.316** 于 2026-11-02 在隔离 HOME、合成 loopback Anthropic SSE 网关下实际输出的 21 个 result 的必要字段摘录；不是第三方模型真实服务的错误承诺。保留了原生 subtype=success + is_error=true、api_error_status=null 与 CLI 丢失上游细节的实际行为。fake canary 已统一替换；端口仅为已关闭的临时 listener，不含认证数据或用户配置。

原始证据：`.teamva/claude-provider-20261102-mobz52m_/evidence/claude-native-fixture-*.json` 与 `claude-native-gateway-matrix.json`（忽略目录、不提交）。

SDK 控制协议依据官方 `anthropics/claude-agent-sdk-python` 的 `query.py` / `subprocess_cli.py` 及官方 npm `@anthropic-ai/claude-agent-sdk` 的 `sdk.d.ts`。DeepSeek Fetch 抓取 GitHub raw 正文失败（404）后改用 curl 获取官方源码，未使用非官方协议逆向代码。

## 宿主事件与启动确认回归

`TestHostInsertedEventsOwnTheSharedDurableCursor` 模拟 FileBridge/ToolBroker 在 SDK MCP 回执前插入宿主公共事件；Provider 必须保持 `Event.Sequence=0`，由 Runtime 统一分配 durable cursor。官方 CLI loopback SDK MCP 测试同样覆盖宿主插入事件。

`TestNativeInitConfirmsRunStartedExactlyOnce` 覆盖 Start、Resume、重复 init、模型 API 失败、原生启动/initialize 失败以及缺失/错误 session ID。`run.started` 只能在原生 `system/init` 确认正确 session ID 并记录授权范围后发送一次，spawn 或 control initialize 成功不等于 Run 已开始。

## 自动压缩窗口的环境白名单

2026-11-02 经 DeepSeek Search 核对官方 [model-config（来源日期 2026-10-27）](https://code.claude.com/docs/en/model-config) 与 [autoCompactWindow（来源日期 2026-10-19）](https://code.claude.com/docs/en/settings-reference#autocompactwindow)：`CLAUDE_CODE_AUTO_COMPACT_WINDOW` 无后缀数字按实际 token 数解释，与 `/autocompact 200` 的千 token 简写不同；文档范围为 100000..1000000，且最终仍受模型上下文窗口限制。

本接入只接受该已知 env 的规范十进制整数字符串（100000..1000000），不接受原生 CLI 可能支持的 k/M 单位、浮点、指数、空白、符号、超范围 clamp 或非法值忽略行为。字段缺失时不继承同名进程环境。`CLAUDE_CODE_MAX_RETRIES`、`API_TIMEOUT_MS` 等测试加速选项仍不投影，不借此开放任意 env。真实 CLI loopback 测试使用有效的 100000 窗口，不调用官方模型。

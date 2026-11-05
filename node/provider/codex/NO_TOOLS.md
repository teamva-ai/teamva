# Codex NoTools：当前明确失败关闭

`InstanceConfig.DisableTools` 与 `RunRequest.DisableTools` 任一为 true 时，适配器在任何
Provider 启动、IPC/temp 准备、thread/start/resume/turn 前返回清晰 unsupported 错误。
实例级限制不能被后续 request 的 false 放松。配置不会注入 custom MCP / Computer Use。
普通 Issue 与 `ReadOnly=true, DisableTools=false` 的 Lumi 不走此分支，原有合法工具保留。

不能把 readonly、approvalPolicy=never、空 dynamicTools 或隐藏 tool catalog 当成 NoTools。
固定官方 0.163 源码显示：

* config ToolsToml 只提供少量单工具开关，没有公共 app-server deny-all ToolPolicy；
* ToolMode 只有 direct / code_mode / code_mode_only，collaborationMode 只有 default / plan；
* TUI `temporary_structured_request.rs` 有关闭 features、MCP、环境的临时结构化请求 recipe；
  但 `tools/mod.rs::requested_tool_mode` 的模型 descriptor 优先于 features；
  `tools/spec_plan.rs` 也会依据模型 `experimental_supported_tools` 注册 clock，绕过对应 feature false。

已真实验证该官方 recipe（另加 explicit stable-environment/sleep/async false 控制）：
loopback 模型请求 advertised tools 数量为 **0**，却仍执行诱导返回的原生
`clock.curr_time`。所以不能将其包装成产品的“严格全工具禁用”。生产现在拒绝这样的 Run，
不调用模型、不回退到有工具模式，也不自动切换其他 Provider。

仓库保留可复现原生回归 `no_tools_native_test.go`：先验证实例/request 两种限制在
Start/Resume 前拒绝恶意工具提示，再单独使用低层 API 证明该 recipe 的空目录漏洞。
后半段的 clock 执行是“不具备 NoTools”的反例，不是产品功能通过；不用于生产请求。

```sh
TEAMVA_CODEX_TEST=1 go test -race ./provider/codex -run NativeCodexNoTools -count=1 -v
```

需要工具彻底禁用的 audit/分支，应由用户选择已验证 NoTools 的 Provider，或等待/接入原生
app-server 暴露真正的 registry-level ToolPolicy。此版本不能宣称 Codex audit/分支已可用。

## SystemPrompt

工厂冻结的非空 SystemPrompt 作为原生 `baseInstructions` 传给 thread/start/resume，
不是仅写入 AGENTS.md 后作为 user 层指令。真实本地模型请求已断言该提示出现在
system/developer 高优先级层；原生持久 CODEX_HOME、精确文件权限、auth 保护均不改变。

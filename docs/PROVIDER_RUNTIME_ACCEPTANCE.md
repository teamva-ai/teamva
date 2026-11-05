# Provider API 重构后的真实运行验收（2026-09-14）

## 2026-11-02：Claude Code 与三 CLI 异常链路补充

- 新增独立 `claude` Provider，真实 Claude Code 2.1.316 通过 DeepSeek 官方 Anthropic 接口及应用内 Issue 公共总结；权限与未支持能力见 `CLAUDE_PROVIDER.md`，不能据此声称支持 Shell 或任意外部 MCP。
- 三 CLI 各 15 项应用级 loopback 网关用例，共 45 项逐条核对实际数据库与 live SSE，包含 HTTP 错误、空/坏/中断流、超时、重试恢复和用户停止。30 项原生失败、9 项有界取消、6 项成功/恢复；有界取消不冒充原生故障终态。额外原生专项及详细证据见 `.teamva/claude-provider-20261102-mobz52m_/验收报告.md`。
- 错误在 Runtime 与控制面落库前去敏；压缩失败独立于 Run 成功。三者具体网关错误及 Claude 实际自动压缩失败卡均经桌面/移动 Chrome 验证；另有压缩运行/完成/失败/取消的协议、持久化及 UI fixture 证据，不能混作大上下文真实模型全部通过。
- **下文“控制面取消后再追加迟到终态”的描述仅属于历史版本**：当前 command result 前先导入持久事件；force stop 给原生中断/压缩收尾有界窗口，再封口为唯一取消终态，确认后的晚到事件不新增公共 Run 序号。物理 Provider 退出仍单独收拢，不提前复用实例。旧数据库中的历史事件不删除、不改写。
- Codex 500/429 等错误正文可能已被原生 CLI 泛化，应用只能保留 CLI 实际公开的诊断，不能还原丢失的上游原文；429 恢复用例显式提供 `Retry-After: 0`，无该头的首次失败另行保留。
- 本轮未提交、部署、启动真实 Wails 或验收公开制品。以下 2026-09-14 的版本、计数和证据均保持为历史记录，不代表本轮版本或门禁。

## 结论

不是“原样全部通过”：真实调用发现并修复了启动状态、压缩事件、摘要分类、取消收尾和进程清理问题。
修复后的正文流、最终 response、工具请求/返回、压缩开始/完成、失败和取消状态已有下述证据。
**Codex 即时 stdout 首段缺失仍是未解决的上游边界**，延迟输出对照不能替代它。

本轮使用 Codex **0.146.0 / gpt-5.6-sol**、OpenCode **1.18.29 / deepseek/deepseek-v4-flash**，
全部在新建的隔离项目、临时 SQLite/工作区与真实 Chrome 中运行；有真实模型调用，没有子代理。
未重启正式服务、未修改正式数据或 Provider 全局配置。前端构建输出到临时目录。

## 真实成功场景

| 场景 | 数据库 / SSE 事件数 | 输出 delta 数 | 正文更新状态数 | 思考更新状态数 | 开始/结束与刷新 |
| --- | ---: | ---: | ---: | ---: | --- |
| Codex Issue：工具、公共评论、真实压缩、2s 首段输出对照 | 532 / 532 | 467 | 92 | 0（上游未公开文字） | 通过 |
| OpenCode Issue：工具、公共评论、真实压缩、2s 首段输出对照 | 4606 / 4606 | 4392 | 15 | 97 | 通过 |
| Codex 图对话 | 847 / 847 | 837 | 156 | 0（仅状态） | 通过 |
| OpenCode 图对话 | 1140 / 1140 | 1117 | 44 | 14 | 通过 |

delta 数包括 OpenCode 原生共用通道里的思考/摘要，不等于全部都是助手正文。
以上场景均逐项核对事件序号、类型、payload，无 SSE 重放/缺号；按 item/part 拼接的增量与快照逐字一致。
正常 Run 均有一个 `run.started`、开始和完成时间，并进入 `running → succeeded`。

工具场景真实执行了分阶段 A/B 输出、退出码 3 的预期失败命令，以及 `issue_get`、`issue_comment_create`。
可以展开参数和返回；延迟对照中实际观察到了“只有 A、尚未出现 B”的中间输出，不是从命令字符串补造 stdout。

图对话最终 `conversation_messages.body` 与最后一个真实助手快照直接比较完全一致，状态为 `published`。
图的内置 prompt 不允许工具，所以中途停止测试使用长文本生成，不把未执行图工具的模型回复误判成 Provider 故障。

### 真实压缩

- Codex：同一压缩 ID 在 seq **183 → 185** 从 `running` 进入 `completed`；浏览器采到 **284** 个压缩中状态样本。
- OpenCode：同一摘要消息 ID 在 seq **315 → 1962** 从 `running` 进入 `completed`；浏览器采到 **62** 个压缩中状态样本。
- 压缩后继续完成工具和最终正文；刷新仍只显示真实回复，不把 OpenCode 内部摘要当成 response。

为了在有界测试内触发真正的上游压缩，仅测试子进程降低阈值：Codex auto-compact 32000；
OpenCode context/input/output 元数据为 28000/24000/2048，reserved=4096，并注入公开重复文本。
这不是生产配置，不是模拟 `context.compaction` 事件。

## 发现并修复

1. **OpenCode 在执行但服务端不进入 running**：原生 `session.status: busy` 没有映射为 `run.started`。
   现在按当前 session 首次 busy 发一次开始事件；重复 busy 不重复开始。原生 retry 也转换为可观察的重试信息。
2. **OpenCode 实际压缩但平台未识别**：安装版本的完成事件是 `session.compacted`，原实现监听了错误名称。
   现在用 `message.updated.info.summary=true` 识别真实开始/失败，用 `session.compacted` 识别完成，保持同一压缩 ID。
   版本证据：<https://github.com/anomalyco/opencode/blob/v1.18.29/packages/schema/src/session-compaction-event.ts>。
3. **压缩摘要污染 response**：摘要消息也是 assistant/text，但不是用户回复。Go 正文提取与前端投影均排除 summary 消息及其思考。
4. **图节点未显示压缩状态**：补上压缩行；同一 ID 的状态原位更新，不累积一条永远 running 的旧记录。
5. **停止后局部 spinner 未结束**：主 loading 已结束，OpenCode 思考行仍可能转圈。Run 结束后未收到操作完成事件的思考、工具、压缩行显示为中断，不伪造成功结果；补齐九种语言的压缩中断文案。
6. **致命错误后的 Runtime 重复终态**：`run.error` 现在也参与 Runtime 的终态闸门，不再额外补发同一 Runtime 的失败结束。
7. **取消误标为 Provider 失败**：因 Run context 取消而结束时，Runtime 返回 `cancelled/interrupted`，不再将预期取消标成 `failed/provider_error`。
8. **Codex 压缩估算被算成用量增量**：原生压缩后可能上报 total 非零、输入/输出/缓存/思考计数均零的上下文重估。
   此类更新保留为原生诊断事件，不再累加为 `usage.updated` 的消耗增量；正常压缩模型用量仍保留。
   成功复测中实际观测到 input/output/total 为 `0/0/28821`，记录类型已是 `provider.event`，而非用量增量。
9. **进程清理竞态**：Codex 先关 stdin、watchdog 先杀父进程再扫描，可能漏掉刚 fork 的后代。
   调整为保留父进程进行进程树清理，并在杀父进程前捕获祖先关系。原有 sink 失败清理用例修复前反复失败，修复后原样连续 **10 次通过**。

## 真实失败与停止

最终四项结果：

| Provider | 场景 | Run | Agent 回复占位 | 主 loading | 仍运行的活动行 |
| --- | --- | --- | --- | --- | ---: |
| Codex | 明确无效模型 | failed | failed | 已停止 | 0 |
| Codex | 生成中强制停止 | cancelled | failed（未完成回复） | 已停止 | 0 |
| OpenCode | 明确无效模型 | failed | failed | 已停止 | 0 |
| OpenCode | 生成中强制停止 | cancelled | failed（未完成回复） | 已停止 | 0 |

**终态的权威是 Run 记录，不是“最后一条事件的字面状态”。** 控制面可能先落库取消/失败事件，
随后再导入 Provider 的迟到终态作为审计记录。通过 SQLite `runtime_sequence` 可区分两者；
这不是同一个 Runtime 重复发终态，也不是重复 SSE 帧。终态保护确保迟到事件不覆盖 `cancelled/failed`。
最后的真实停止中，控制面和迟到 Runtime 均记录取消，而非把取消改写成失败。

## 尚未解决、未覆盖和测试方法修正

- **Codex 即时输出缺 A**：基线第一条命令 `exec-f94649f9-00f0-4d9b-94a3-bf9dd550a6ae` 的原生
  `outputDelta` 与 `aggregatedOutput` 只有 B。A 后来出现在模型文字、命令参数或公共总结中，不算 stdout 证据。
  服务端不能恢复上游没有交付的数据。没有升级 CLI，也没有伪造补齐。
- 最初压缩阈值设为 12000，低于该测试不能被继续压缩的约 28–30k 上下文；触发七次真实压缩后超时取消。
  这是测试阈值不合理，不当作生产压缩策略通过证据；之后校准阈值，得到一次压缩并正常完成的双 Provider 结果。
- 一次 OpenCode 测试把 yield 等待窗口误设成 timeout=1000，命令被提前杀死，不是流式丢包。
  已明确测试 prompt 的 timeout=15000；原生 bash completed+exit=null 的异常结束也不再显示为普通成功。
- 最后一轮 OpenCode 图的旧比较器把 Markdown 列表标记当成可见文本，产生一次误报。
  已用数据库直接证明正文与原生快照完全相同，并修正显示文本比较器、增加两项测试；离线回放检查通过，原始失败字段没有覆盖。
- 压缩失败、运行结束但没有压缩完成通知的显示分支有协议/组件回归，**不声称已让两家真实模型都自然触发压缩失败**。
- 真实重试/原生审批弹窗未在本轮刻意触发；retry 映射有协议测试，不能等同于真实网络故障恢复验收。
- 本轮没有真实 Wails 窗口或公开制品证据。

## 证据位置

本机临时目录：`am-post-provider-acceptance/`（报告和截图不提交）：

- `am-stream-IMktzI/`：第一轮基线，含 Codex 原生标记观察记录和 OpenCode 缺少开始事件的证据。
- `am-stream-ghymAI/`：过低阈值导致反复压缩的超时试验，数据库保留，仅可作为异常试验记录。
- `am-stream-3VdBOM/`：识别 OpenCode 压缩事件/摘要污染、timeout 混淆的证据。
- **`am-stream-SCijd0/report.json`**：双 Provider 工具、response、真实压缩通过。
- **`am-stream-ZEOoPu/report.json`**：最终四项真实失败/停止通过；残留测试进程列表为空。
- **`am-stream-EMxtm1/report.json`**、`markup-check.json`：最终双 Provider 图流式、正文落库、显示格式核验。

所有本轮测试服务已退出；最后对整个临时验收根目录再审计，未发现带本轮路径标记的残留进程。
实例私有配置/凭据链接已删除。**不要打包上传 data/、数据库或整个临时目录**；只分享审查后的报告/事件/截图。
没有终止或重启用户正式服务。

## 自动化与重跑

- `pnpm test:backend` 通过；前端 **35 文件、200 测试通过**。
- Provider、进程管理、Runtime、server Store/HTTP 的定向 `go test -race` 通过；node/server `go vet` 通过。
- Svelte 检查 0 error / 0 warning；隔离生产构建通过，仍有既有大 chunk 提示。
- 验收比较工具四项 `node --test` 通过（显示文本、工具参数/输出隔离）；生成文件与脚本语法、差异检查通过。

脚本支持 `TEAMVA_STREAM_WEB_DIR` 指向隔离构建，`TEAMVA_STREAMING_TEMP_ROOT` 指向临时根目录。

```bash
# 会产生真实调用，须使用授权账号；不会自动更新正式服务。
TEAMVA_STREAMING_ACCEPT_REAL_CALLS=1 pnpm test:runtime:streaming

# 真实压缩与分阶段工具输出对照（阈值仅影响测试子进程）
TEAMVA_STREAMING_ACCEPT_REAL_CALLS=1 TEAMVA_STREAM_COMPACTION=1 \
  TEAMVA_STREAM_SURFACE=issue TEAMVA_STREAM_TOOL_DELAY_MS=2000 \
  pnpm test:runtime:streaming

# 错误模型与流式生成中停止
TEAMVA_STREAMING_ACCEPT_REAL_CALLS=1 TEAMVA_STREAM_STATES_ONLY=1 \
  pnpm test:runtime:streaming
```

# 真实流式输出验收（2026-09-14）

> 本文记录 Provider API 重构前的验收。重构后的真实压缩、错误与停止专项及最新修复，见
> [PROVIDER_RUNTIME_ACCEPTANCE.md](PROVIDER_RUNTIME_ACCEPTANCE.md)。

## 范围与方法

本轮没有子代理。使用隔离数据目录、真实 localhost 服务、真实 Chrome，以及本机已授权的
Codex 0.146.0 / gpt-5.6-sol 和 OpenCode 1.18.29 / deepseek-v4-flash。
没有修改正式任务、数据库或 Provider 配置；测试仅在临时项目工作区执行公开标记命令和 FileBridge 调用。

每次运行同时采集：

1. 独立 SSE 消费端收到的所有事件（含序号和 payload）；
2. 结束后分页读取的持久化事件；
3. 按 native item/part ID 拼接的全部文本 delta，与该消息最终快照逐字对比；
4. 浏览器约每 100ms 的正文、思考、工具参数/返回/状态快照；
5. 结束后的截图、刷新重放截图和文本一致性结果。

不是每个 chunk 都要求单独绘制一帧：界面合并约 50ms 内的更新，但消息内容不得丢失或重复。
验证的是 Provider 对外上报的事件，不代表能获取模型未公开的内部推理。

## 实测发现并修正的问题

- **SSE 心跳重放**：心跳后再次发送上一页，造成重复序号；基线 Codex 出现 77、147 重复。
  心跳现在只触发读取未发送的新事件；终态检查前后重新排空事件，避免结束与心跳并发时漏掉最后一页。
- **思考不实时**：OpenCode reasoning delta 原先被忽略，界面只在最终快照到达时显示整段。
  现在按 part/item ID 合并，思考与正文各自逐步更新，快照只替换自身内容。
- **刷新后缺少回复**：基线 OpenCode 共 3024 条事件但界面按 1500 个原始事件限制自动加载，
  刷新后漏掉最终回复。自动加载现按语义条目数与字节保留预算限制，不再把 chunk 数当作消息数。
- **工具互相覆盖**：不同协作工具复用同一请求文件名时 ID 冲突，`issue_get` 被 `issue_comment_create` 覆盖。
  工具事件 ID 加工具命名空间，前端也按工具身份合并。
- **工具详情与状态**：参数与输出并列展示，命令 stdout 追加到对应工具行；非零退出码显示失败而非普通完成。
- **图对话假流式**：基线 Codex 图运行有 800 个正文 delta，节点却只在结束时显示全文。
  节点现在直接订阅 Run SSE，显示正文、思考和工具轨迹；历史执行详情按需读取，终态正文不重复显示。
- **OpenCode 最终回复混入思考**：后端终态文本提取只读取 `provider.item/output.delta`，
  忽略 OpenCode `provider.event` 中的 part 类型。现按 part 身份与角色排除 reasoning/user，
  只把真正正文保存为节点最终消息。这也修正目标审核读取的最终结果。
- **结束后仍 Loading**：侧栏使用打开时的旧实例对象，且较旧轮询结果能覆盖新版本 Run。
  现以加载后的 Run/目标状态为准，Run 合并拒绝版本倒退。
- **图节点体验**：流式文本自动跟随到底部（用户上滚后不强拉）；目标复选框尺寸固定；停止请求补齐 API 要求的 mode。

## 最终复测结果

| 场景 | 持久化/SSE 事件 | 文本 delta（含思考） | 正文实时更新状态数 | 思考实时更新状态数 | 刷新一致 |
| --- | ---: | ---: | ---: | ---: | --- |
| Codex 任务会话，首次 stdout 延迟 2s 对照 | 522 | 460 | 90 | 0（仅思考状态） | 是 |
| Codex 图对话 | 839 | 829 | 236 | 0（未公开思考文本） | 是 |
| OpenCode 任务会话，首次 stdout 即时输出 | 2128 | 1998 | 13 | 69 | 是 |
| OpenCode 图对话 | 1948 | 1925 | 30 | 57 | 是 |

以上四项的 SSE 与持久化事件逐项一致、无重复/缺号；各 item/part 的 delta 拼接与最终文本快照一致。
两种 Provider 的对照任务均能展开工具参数/返回、显示预期失败、看到分阶段输出；终态 Loading 已停止。

证据保存在本机临时目录（原始事件与截图未提交）：

- 最终 OpenCode：`am-stream-yEWdC5/report.json`
- Codex 延迟 stdout 对照与图对话：`am-stream-fX4q4S/report.json`
- Codex 即时 stdout 异常：`am-stream-5BaSIe/report.json`、`codex-native-metrics.ndjson`

所有测试服务已停止。本轮 Provider 实例目录已清理，报告/事件/截图保留；不要打包上传整个临时数据目录。

## 仍存在的 Provider 边界（不能宣称“所有情况都正常”）

Codex 0.146.0 在本轮的“命令启动后立刻输出 A、1.2s 后输出 B”场景中，原生 app-server
通知只上报了 B。独立、透明的 stdio 观察器记录了方法名和公开标记所在字段，证明第一段 A
并非在本地 SQLite/SSE/前端中丢失；后续模型文字提到 A 不能替代真实 stdout 证据。
`thread/read` 对该线程也没有返回可恢复的 commandExecution 结果。

把首次 stdout 延迟 2s 后，A/B 均进入原生通知、落库、SSE 和界面，说明下游分块链路正常；
**这只是对照测试，不是修复 Codex 即时 stdout 缺失**。默认验收脚本保留即时输出用例，遇到此问题仍返回失败。
不能从命令参数或模型总结中拼造未收到的 stdout。

Codex 本轮部分 reasoning item 的 summary/content 为空：只显示思考状态，不伪造思考文字。
本轮未做真实 Wails 窗口验收，也未自动修写过去已经保存错误正文的历史消息。

## 自动化回归

- server 全量 `go test ./...` 与 `go vet ./...` 通过；SSE 心跳/终态竞态、
  OpenCode 最终文本及目标审核提取新增回归，并通过定向 `-race`。
- node 全量测试与 vet 通过；Codex Adapter/Runtime 的 race 测试通过。
- Web 类型检查零错误、185 项测试通过、生产构建通过（仍有既有 chunk 大小提示）。
- 原生消息物化测试覆盖分块思考、消息身份、快照去重、同名不同工具、命令失败状态；
  图节点测试覆盖实时 SSE 与按需回放，侧栏测试覆盖终态 Loading 结束。
- 正式服务没有自动重启；这些后端修复需要重新构建并启动后端后生效。

## 重跑

```bash
# 先构建前端；这条命令会调用真实 Provider，不属于日常 test:backend/test:web。
pnpm build
TEAMVA_STREAMING_ACCEPT_REAL_CALLS=1 \
TEAMVA_STREAM_PROVIDERS=opencode pnpm test:runtime:streaming

# 复现 Codex 即时 stdout 边界（可能失败，是有效的验收信号）
TEAMVA_STREAMING_ACCEPT_REAL_CALLS=1 \
TEAMVA_STREAM_PROVIDERS=codex pnpm test:runtime:streaming

# 仅验证流式通道的延迟输出对照，不掩盖上面的即时输出问题
TEAMVA_STREAMING_ACCEPT_REAL_CALLS=1 \
TEAMVA_STREAM_PROVIDERS=codex TEAMVA_STREAM_TOOL_DELAY_MS=2000 \
pnpm test:runtime:streaming
```

可选 `TEAMVA_STREAM_SURFACE=issue|graph` 缩小范围；脚本自动编译并启动隔离后端、
选择探测到的模型，超时会停止测试 Run。它不重启正在运行的正式服务。

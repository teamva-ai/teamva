# 当前基线代码质量审查（2026-09-16）

## 原则与范围

没有已发布版本，只维护当前产品和当前协议。此次扫描了 server、node、web 的兼容标记、仓库接口、全局助手、工作记录、灵感和路由实现，优先处理有明确收益和可验证边界的问题。不是以删除所有含 `legacy` / `fallback` 的代码为目标，也不宣称有限的一次审查能证明整个系统绝对最优。

不删除数据库只读基线/完整性校验、不回填旧库、不清理用户数据；不删除并发会话续期窗口、运行恢复、Provider 能力检测等当前安全/运行机制。跨端口设备信任是当前需求，不是旧协议兼容。

## 本轮落实的优化

| 问题 | 调整 |
| --- | --- |
| 全局助手的 scope/context 使用可变参数，默默选择默认或只取第一项 | 改为 `AssistantScope`、`AssistantSelection`、`AssistantMessageRequest`；默认值只由 HTTP 边界处理，仓库调用明确传入 |
| 助手单文件混合配置、会话、执行、提示词和查询 | 拆成 general agent、assistant sessions、assistant turns、assistant reads、prompts，各模块职责单一 |
| 当前必备功能用类型断言临时判断是否支持 | 统一在 `workspace_repository.go` 声明，纳入 `CoreLocalRepository`，增加编译期 SQLite 实现断言；移除空 `NetworkAccess` 接口 |
| 引用校验、容量检查先于幂等重放 | 先在同一事务中鉴权并匹配已有请求；引用删除、Provider 下线、会话满额后仍可重放，不重复执行 |
| 发送前加载整段会话且在事务外读取 | 只取最近 60 条，在加载前由 SQLite 统计字节预算，并与 outbox 写入共享事务快照 |
| 历史工具先全量读取再切片，单条消息也读取整节点 | 改成 SQLite `LIMIT/OFFSET` 分页及按 ID 单条查询；分页与总数在同一只读事务中读取 |
| 查询工具吞掉未知参数和错误类型 | 严格 JSON 解码，拒绝未知身份字段、字符串/小数分页值、未知历史类型 |
| 条件编译器为每个叶条件重新构造 SQL 字段表 | 字段表达式只初始化一次；独立编译器限制深度/数量，并拒绝条件组与叶字段混用 |
| 相同 Markdown/属性也增加版本并制造记录 | 无实质变化时不执行 UPDATE，不增加版本；并发版本校验仍保留 |
| 消息可关联错误 Run 或重复生成回复 | 增加会话/Run 关联触发器、每 Run 每 author 唯一约束、当前查询格式检查；普通 Agent 不能无 Provider |
| 响应捕获超限后可能保留不完整 JSON | 超限即丢弃捕获缓冲，不再拼接后续片段；事务变更记录仍保留 |
| 日志通过旧页面和运行时修改翻译字典实现 | 改名 `Logs.svelte`，直接使用 `logs` 字典，删除 `/activity` 重定向和失效导航翻译 |
| 新增 Svelte/TS 代码挤在少数长行 | 引入固定版本 Prettier/Svelte 插件，格式化相关新模块；保留功能测试验证，没有为了减少行数做晦涩抽象 |

## 格式化

只格式化本次涉及的文件，避免全仓无关 diff。可复现命令：

```bash
pnpm --filter @teamva/web format src/components/WorkAssistant.svelte
pnpm --filter @teamva/web format:check src/components/WorkAssistant.svelte
```

## 回归重点

- 引用删除、Provider 下线、满 500 条会话后的幂等重放；并发相同请求仅一个 Run 和一对消息；不同问题/查询范围拒绝重用幂等键。
- 分页边界、精确消息查询、跨用户拒绝、未知查询参数与混合条件结构拒绝。
- 当前数据库约束、事务提交、无变化保存和响应捕获上限。
- 浏览器工作记录、设置日志、灵感、Markdown 编辑、只读助手配置及单端口/长期设备信任回归。

## 仍需保留的验证边界

- 自动化/fake Provider 和浏览器测试不能替代真实 Codex/OpenCode 模型调用、原生审批、真实 Wails 窗口与发布制品验收。
- `AgentSideSheet.svelte`、Runtime/dispatcher 等较大模块仍有继续按领域拆分的空间；此次未进行没有明确行为边界的全仓重写。
- 生产规模数据下的全文搜索、工作记录数量和读写负载需要单独基准。本轮消除了明确重复分配和全量读取，但不凭感觉宣称吞吐提升百分比。
- 当前 SQLite 基线随约束变更，不维护迁移链，不自动转换旧消息结构；运行验收使用新的隔离数据目录。

## 本轮执行证据

- `pnpm test:all` 通过；前端 40 个文件、231 项测试通过，Svelte 检查零错误零警告，隔离目录生产构建通过。
- `pnpm verify:backend` 完整通过：server/node/desktop 全包 race 与 vet，protocol 普通测试。
- 真实 Chrome 完整 **53/53** 通过，新增“只支持当前日志入口”的回归；原工作记录、灵感、Markdown 编辑、长期设备信任和响应式流程均保留。
- 40 条条件的编译基准在本机 Apple M4 上约 3.7 μs/op、7384 B/op、95 allocs/op；这是当前基准，不声称相对旧实现的速度提升比例。
- 相关 Svelte/TS 模块格式检查、OpenAPI lint 与 `git diff --check` 通过。OpenAPI 保留既有 health 4XX、bootstrap 2XX 两条警告。
- 浏览器产物位于仓库外 `teamva-quality.RR8Geo/browser-final/`。验收服务完成后关闭，原 41720 运行实例和原数据未替换。
- 未进行真实 Wails 窗口、真实模型问答/原生审批或公开制品发布；没有提交或推送代码。

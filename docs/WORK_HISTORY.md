# 工作记录与只读回顾助手

## 产品入口

- 主导航的「工作记录」(`/work-records`) 为全局页面，不跟随当前项目缩小范围；可主动按项目筛选。
- 日志位于「设置 → 日志」(`/settings?tab=logs`)；不保留旧 `/activity` 入口或重定向。
- 表格提供关键词/完整 ID、日期、项目，以及 AND/OR 条件组、等于/不等于/包含/排除；条件最多 60 个、嵌套最多 5 层。可命名保存和删除筛选。
- 详情在行内展开，保留右侧聊天；展示来源、目标、完整标识、后续动作和原对象链接。移动端助手作为可关闭侧栏。

## 持久化与采集边界

`work_records` 独立于 `audit_events`。类型为：

1. `operation`：已提交的 HTTP 业务动作，记录 pending → succeeded/failed；请求 ID、客户端/页面 ID、入口类型来自当前请求。发生进程中断且结果未收敛的操作启动后标为 unknown，不伪造成功。
2. `change`：SQLite 业务事务内生成的变更记录。Project、Agent、Issue、Resource、图、节点、消息/评论、Run、Memory、Schedule 和 Policy 使用显式字段白名单；写入失败回滚业务变更。
3. `navigation`：客户端观察到的实际项目/任务/图/节点切换。以随机 ID 幂等上报，浏览器页面独立序号，短暂失败重试；排除搜索输入、筛选、滚动、重复点击及未提交取消。导航不是业务操作的权威证明。

HTTP 操作在提交前建档，成功后关联其事务变更并补全元数据。Run 的不可变 snapshot 保存 Agent 修订 ID 和来源工作记录 ID，异步变更沿 Run 关联回用户动作。后补元数据失败不抹掉已提交的事务记录；这与“把整个 HTTP 请求和业务事务做成一个事务”不同，异常情况明确保留 unknown/独立底层变更。

保存正常名称、路径、非正文属性及修改前后值，不打码。记录适用的 Project/Agent/revision/Issue/graph/node/message/comment/Run ID、分叉来源、时间、客户端和结果。Provider 回传后补充 native session、turn、Run 历史容器和 command outbox ID。

**不采集消息正文、提示词、任务描述、任意原始请求/错误、密码、令牌、Cookie、Provider 凭据或 Secret 值。** 数据库触发器不序列化整行；API 补充字段也使用独立白名单。

对象 ID 不做级联删除外键。原对象删除后操作痕迹仍在，链接标记已删除/不可访问；不从工作记录恢复上下文。记录长期保存，用户可确认后按当前筛选和截止时间清除匹配记录及其关联变更；不删除原业务对象、助手聊天或技术日志，清除动作写入日志。

## 系统 Agent Lumi

- 每台本机 Computer 一个 `system_role=general` 的系统 Agent，默认名称 Lumi；可以修改名称（如 Roomie），角色不由名称判断。可先没有 Provider/current revision，未配置时从助手入口前往 `/agents?agent=<id>` 完成配置。
- 数据库唯一索引、保护触发器和 API 同时禁止删除/禁用，普通 Agent 创建 API 不能伪造 system_role。
- Lumi 与普通 Agent 共用 Agent 页表单，名称、备注、Provider、模型、提示词与 Skills 均可编辑；仅在该页显式保存，通过公共 `PATCH /v1/agents/{id}` 一次提交配置、修订和 Skill 绑定，并校验 `expected_version`。不保留独立的 `PUT /v1/work-assistant` 配置接口，工作记录、灵感、引导等入口只提供前往 Agent 页的链接。
- 工作记录入口固定使用该系统 Agent。用户保存的提示词与 Skills 会实际传入 Run，并与入口的系统只读说明组合；入口授权仍固定，不能因提示词、Skills 或 Provider 配置获得业务写权限。这个只读限制属于工作记录入口的 Run 作用域，不是 Lumi 作为 Agent 的身份属性；Lumi 执行普通 Issue 或定时任务时按普通 Agent 权限运行。

## 全局助手运行

- `assistant_sessions`、`assistant_messages` 保存全局多会话；没有虚构项目。`runs.assistant_session_id` 与业务 project/Issue 作用域互斥，数据库约束只读及 `purpose=work_assistant`。
- 复用现有 outbox、Runtime、Provider 管理、SSE、停止、terminal 文本归档和重启恢复。同一会话串行；消息幂等重试必须与原问题、查询范围一致。
- 每轮冻结当前表格筛选和选中记录 ID。默认在此范围检索，用户明确要求才扩大；授权上限始终是本机工作台当前用户。
- 会话请求使用明确的 `AssistantScope` 与 `AssistantMessageRequest`，仓库接口没有旧式可变参数。用户归属、幂等重放、容量、引用和历史读取在同一事务内检查；已提交请求重放不依赖引用是否仍存在、Provider 是否仍在线或会话是否已满。
- 会话列表当前返回最近 200 个；旧会话仍可通过会话 ID 链接打开。每会话最多 500 条消息，提示中最多重放最近 60 条，超过上下文预算要求新建会话；原历史仍保留，可通过只读工具分页查询。
- CWD 是 `runtime/workspaces/home/<session-key>`，会话之间隔离。这里放工作说明与宿主 IPC，不复制全库或原始上下文，不传递数据库、访问密码、Cookie。

三项只读工具：

| 工具 | 用途 |
| --- | --- |
| `work_records_query` | 与表格同一套查询能力，分页、跨项目、组合条件 |
| `work_record_get` | 元数据、后续动作、可访问的对象链接 |
| `work_history_read` | 按 ID 读取原始任务/节点/消息/评论/助手会话；列表有分页边界 |

ToolService 从持久化 Run 取得 owner/computer/scope 并验证运行仍有效，不使用模型传入的身份。助手桥只暴露以上工具，任何业务写工具均拒绝。Provider 侧关闭 Computer Use、提权、业务写根；宿主 MCP 工具通道（或回退文件桥）所需的隔离通信不等于业务写权限。Codex 与 OpenCode 的具体约束见 `protocol/node/README.md`。

## 开发和验收

本次修改当前 SQLite 基线。既有基线不升级、不回填导航、不删除旧库；新版本验收与启动必须用新的隔离数据目录。

自动化覆盖事务记录失败回滚、正文不进入记录、分叉来源 ID、组合筛选校验、跨用户拒绝、保存筛选、删除痕迹、清除边界、Lumi 保护、助手幂等/串行/重启和 Runtime 只读作用域。

真实 Chrome 用 `@work-history` 测试桌面和手机宽度：创建任务→查询记录→保存筛选→行内详情→跨项目跳转→设置日志→助手新会话→保护 Lumi→确认清除但保留原任务。

真实 Provider 需在授权账号、隔离目录和明确测试问题上另行验收，不以 fake Adapter 或浏览器页面成功代替真实模型工具调用证据。

### 本次验收（2026-09-16）

- `pnpm test:all` 通过：server、node、protocol、desktop 及 Web（38 个文件、227 项）。Web 检查 0 错误/0 警告，生产构建通过，保留已有的大 chunk 提示。
- server 的 store/httpapi/toolservice/app 和 node 的 runtime/codex 相关包 `go test -race` 通过；server/node/desktop `go vet` 通过。
- 隔离服务启用 Runtime 后真实 Chrome 完整 **48/48** 通过，包括 1280px/390px 工作记录流程、页面布局、同端口远程访问和本地目录命令。纯 UI 模拟图用例对模拟导航单独 stub；真实导航由工作记录流程和后端测试验证。
- 最终浏览器产物位于仓库外 `teamva-work-history.CielDr/browser-delivery/`。未记录 trace/HAR；远程访问测试密码仅内存生成。
- OpenAPI lint、ShellCheck（启动脚本）、浏览器脚本语法和 `git diff --check` 通过；OpenAPI 保留已有 health 4XX、bootstrap 2XX 两条警告。
- 本次未执行真实 Wails 窗口操作、真实 Codex/OpenCode 模型问答或公开制品发布。真实模型工具调用与回答质量仍需用户配置模型后另行验收。

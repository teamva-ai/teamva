# 架构与浏览器审查记录

状态：进行中。以下是代码与实测记录，不代表完整产品验收已经结束。

## 本轮已修正

- 浏览器首次进入不再依赖桌面 nonce：同源 JSON POST 建立或恢复本地 HttpOnly Session，后续写请求仍校验 CSRF。
- loopback 服务校验 Host，防止 DNS 重绑定；拒绝 iframe 嵌入；缺失静态文件/下载返回 404，不再伪装成 SPA HTML。
- Web 固定使用同源 `/v1`，移除远端构建 profile、旧登录启动屏、Cloudflare Pages 入口和旧 CLI 安装脚本，以及未使用的 `svelte-routing`。
- 图对话将用户消息、回复占位、Run、节点状态和 outbox 放入一个幂等事务；忙碌/失败不会留下幽灵消息，重复提交不会重复创建消息。
- 分叉固定上下文快照与 AgentRevision；全量上下文重放使用独立 Provider 会话；运行中的图/子树拒绝删除和分叉；失败节点不再伪装成 idle。
- 图卡片使用即时拖动坐标；失败时回滚位置；发送失败保留草稿及幂等键；运行中提供停止按钮；移动端可以切换图并使用纵向节点列表。
- Issue 公共评论只能由工具发布，移除模型最终文本冒充公共评论的路径；图运行结束同样执行通用 Approval 收尾。
- 合并 mention claim 的旧类型别名与转发方法，去掉多余 Provider 查询；Runtime 不再按旧 Resource ID 回查路径。
- Runtime 移除旧 Git 快照时间/主键重建以及旧 event wire 字段回填；不支持的 schema 拒绝启动并保留原始内容。
- 正常空 Git 项目允许保持 SSE 订阅，发现的仓库集合变化后重新订阅；前端仅识别当前 `git.changed` 协议，不再接收历史事件别名/包装。
- 外壳使用动态 viewport 和父容器布局；资源、记忆、计划任务、图会话不再越过移动导航；增加窄屏项目创建/管理入口、明确的未知路由页。
- 增加失败可见且防重复提交的确认对话框；localStorage 不可写时仍可正常选择项目。
- 移除旧 Node CLI/daemon、Cloudflare/Docker 部署、PostgreSQL、邮件密码/设备注册 API、远端 relay 与整套内存 Store。仍有价值的 HTTP 核心、安全、权限隔离、Session 与分页测试迁移到真实 SQLite；保留并行、幂等、恢复与 ToolService 回归。
- Agent 基础字段、不可变修订和 Skill 绑定统一原子保存；失败回滚、并发版本裁决、省略字段保留、相同配置不重复换代均已测试。活动运行、排队提及或启用的计划任务仍引用 Agent 时禁止删除。
- Agent 编辑器在修订加载期间禁止写入，固定编辑版本，保存后立即刷新全局 Agent 列表；切换配置时保护草稿。
- Memory 新增归档列表与只读查看/恢复流程；默认选中的第一条记忆同样保护未保存编辑，后台刷新不会覆盖草稿或偷偷采用新版本。
- 资源创建捕获发起时的项目/设备/表单值，异步目录注册不再读切换后的项目；URL 仅接受无凭据 HTTP(S)，创建后状态为 ready；所有资源修改/删除/验证核对 URL 中的项目归属；窄屏使用卡片而非挤成竖排的表格。
- 修复 Computer Use 开关调用不存在的设备编辑 API：增加独立 PATCH computer-use 配置接口，明确与安装/实际控制分离。
- 修复暗色主题 AM 标识对比度（实测修复前约 1.18:1，修复后通过 4.5:1 门槛）；截图等待设置保存并关闭动画，避免截取主题过渡帧。
- 增加多标签页初次引导竞争后的 CSRF 一次恢复重试；只重试明确在写入前拒绝的 csrf_failed，不重试来源拒绝，也不循环重试。
- `pnpm start:browser` 成为明确入口；README/AGENTS/主架构文档已更新为浏览器和 Wails 共用本地服务。API、浏览器与 Git/Provider 验收脚本不再依赖桌面 nonce，目录命令只使用 completed 当前状态。
- 目录选择/注册与资源验证统一为单一路径：HTTP 只在一个事务内持久化命令并唤起 dispatcher，执行、租约续期和结果写回都在 dispatcher 完成；修复了过期命令状态更新因“无命令可领”而随事务回滚的缺陷。资源验证结果带回 resource/version 并只在仍处于 validation_pending 时落地，落后结果不会覆盖改名、删除或重新验证。
- 修正 `resource.validate` 结果契约（显式 resource_id/validation_version），Node 端要求非零版本；新增浏览器用例通过真实 Chrome 走完注册→轮询→验证→ready 全流程。
- 删除 `node/internal/protocol` 60 行转发别名层，Runtime 直接使用 `protocol/node`；新增跨项目切换浏览器用例验证旧 `?memory=` 选择不会泄漏到新项目。
- 图对话详情增加单次读取预算（节点/消息数/消息字节）；超限显式返回 `conversation_too_large`（413），不静默丢弃节点或生成超大响应。错误目录同时提供中英文提示。

- 可选远程访问默认关闭：本机设置页显式开启并设置密码后动态增加独立 IPv4 监听，本机 loopback 入口不变；远程通过自有密码登录页建立 HttpOnly 会话，无用户名或 Basic 弹窗，严格同源与 CSRF 保留。
- 「设置 → 远程访问」管理开关、端口和密码（至少 8 字符，最多 72 UTF-8 字节，app_metadata 仅存 bcrypt 哈希，不回显）。配置持久化，关闭或改密码断开远程连接；远程不能修改安全设置。`TEAMVA_LOCAL_ACCESS_TOKEN` 仅为 CLI 首次密码种子，没有密码时 `pnpm start:lan` 仍不监听外部。地址列表过滤 VPN/点对点网卡，可信 VPN 客户端仍可使用已知的 IPv4 地址连接。
- 协作工具调用不再完全不可见：FileBridge 每次处理 Issue 工具请求时，把 running/completed/failed 投影成 `provider.tool` Run 事件（只带裁剪后的摘要与结果，不含请求文件路径），前端时间线据此显示「读取任务 / 发布评论 / 修正评论 / 查看 Agent」等条目。这修复了「Agent 正在发评论但界面毫无过程」的感知缺口；内部文件协议细节仍对用户隐藏。
- Agent 会话时间线新增「正在生成…」占位：Run 运行中在底部显示转圈占位并保持滚动跟随，直到出现输出或运行结束。此前只有头部一个状态点，首条输出前整个时间线是空白的。
- 测试分层与提速：根目录新增按层命令（`test:web`/`test:server`/`test:node`/`test:protocol`/`test:desktop`/`test:backend`/`test:all`），Playwright 用例增加 `@smoke`/`@layout`/`@mobile`/`@desktop` 标签并新增 `test:browser:smoke`、`test:browser:layout` 入口；AGENTS.md/README 给出「按改动范围选择命令」矩阵，只有跨层契约改动或发布前才跑全量浏览器验收。
- 修复前端测试偶发失败：jsdom 不实现 `scrollIntoView`，IssuePanel 搜索定位 effect 产生 unhandled rejection，导致 159 项全过但 vitest 以 exit 1 结束；测试 setup 统一打桩后连续 5 次稳定通过。
- 前端 vitest 提速：28 个测试文件共享模块图（`pool: threads` + `isolate: false` + `maxWorkers: 4`），本机 `pnpm test:web` 22.8s → 约 10–11s；顺序打乱后同样通过。
- Go 门禁提速：去掉历史 `-p=1` 强制串行，server `go test -race ./...` 174s → 约 95s（测试缓存命中时进一步缩短），node race 42.5s，desktop race 2.7s；普通功能测试 server 8.6s、node 约 45s（gitread 的 fsnotify 用例占 38s）。
- 修复 Agent 会话时间线在分页加载时「AI 回复消失」：OpenCode 的 reasoning（思考）与正文共用 `output.delta`，前端此前把所有 delta 累积成 AI 回复，等最终 text part 快照加载后 delta 被整体跳过、改用快照，导致用户点击「加载更多」时先看到思考文本冒充的长回复、随后整段回复突然消失。现在预扫描建立 part 类型索引，`output.delta` 只累积正文 text part 的文本，reasoning/user 流被忽略（part 类型未知时保留旧回退，兼容不发送 part 快照的 Provider）。新增两阶段回归测试（先中间态、再追加后续分页），并验证测试在修复前确实失败；真实数据（验收AgentB 370 事件 Run）点击「加载更多」全程不再出现错误长回复，最终稳定为 3 个思考 + 1 条最终回复。
- 会话事件分页改为小会话自动补齐：分页单位是底层事件条数（100/页），一次普通 Run 就可能产生数百条流式事件，界面却只有几个条目，此前几条消息也要手点「加载更多活动」。现在累计事件低于 1500 条时在后台自动逐页加载，超过预算的长会话才显示按钮；无进展的游标会被清空，避免自动加载原地循环。
- 旧版本 Run 的协作工具调用可见：这类调用表现为 FileBridge 协议文件的 bash/write/read，此前被 `protocolNoise` 整段隐藏，导致界面只剩连续思考。现在从命令/`filePath` 解析 `.teamva/tool-requests/<run>/<tool>/` 并映射为「读取任务 / 发布评论 / 修正评论」等语义条目；已有 FileBridge 专属事件的新 Run 继续用它，避免重复显示。真实会话验证：3 个思考之间穿插「读取任务 ×3、发布评论 ×2」，只有一条最终回复气泡，且不再出现「加载更多」按钮。
- 协作工具条目可展开查看参数与返回：旧协议条目的 `detail` 来自工具状态（write 请求取 `state.input.content` 的请求 JSON，read 响应取 `state.output`）；新 `provider.tool` 事件的详情同时展示参数摘要与返回摘要。`issue_get` 的返回摘要新增 `description`（200 字符）与最多 5 条评论预览（每条 160 字符），让「读取任务」详情能看到任务上下文而不是只有标题和数量。真实会话验证：3 个「读取任务」条目全部可展开，write/read 条目能看到完整请求与响应 JSON；新增 node/web 回归测试各 1 个。
- 修正「发布评论显示成读取文件」：FileBridge 一次调用在旧数据里是 write（写请求）+ read（读响应）两条记录，read 展开是 `filePath + <content>` 文件读取形态；同时活动投影层把 `input.content`（请求正文）当作屏蔽键丢弃，参数只剩路径。现在把同一请求的 write/read 合并为一条语义记录（read 的响应文件路径与请求配对），响应解析成干净 JSON；投影层只对工具请求把 `content` 受控恢复为 `request`（预算内）。真实会话验证：「发布评论」只有一条，参数是评论请求 JSON、返回是 `{comment_id, version}` 响应；「读取任务」返回完整 Issue 上下文。
- 任务允许空标题创建：`POST /issues` 不再要求标题，`CreateIssueRequest` 移除必填；迁移 `0019_untitled_issues.sql` 重建 issues 表去掉 `length(trim(title)) > 0` CHECK（保留状态触发器与索引）。SQLite 无法修改 CHECK，迁移运行器改为在专用连接上临时关闭外键、提交前仍执行完整性校验（含 `foreign_key_check`）后再恢复，测试辅助与 integrity fixture 同步。界面空标题显示「未命名任务」占位；任务列表新增右键菜单「编辑标题与描述」，弹窗保存 PATCH 并实时更新列表。新增前后端回归测试与真实 Chrome 验证（空标题创建 → 右键编辑 → 列表更新，验证任务已清理）。
- 图对话节点布局与分支体验：迁移 `0020_conversation_node_layout.sql` 为节点增加 `width`/`height`/`locked`（默认 380×560，比原来的 ~440 更高）。节点右下角可以拖动调整大小并持久化（300–1000 × 260–1600）；分叉时新父节点与原节点立即锁定（不能发送消息，只能继续从消息创建分支，`SendConversationMessage` 拒绝锁定节点），只有新分支节点可继续对话。分叉几何改为：新父节点保持原位置、原节点与新分支在右列上下对称，新父节点位于两者连线的中央高度（`ForkConversationsNode` 计算列间距与行间距），同列节点中心对齐。新分支节点返回冻结的 `inherited_messages` 快照，界面显示「继承的聊天记录」与「已分支」标记。拖动改为 requestAnimationFrame 合并 pointermove，空消息数组使用稳定引用，避免整画布与所有消息列表重渲染。后端测试覆盖锁定/拒绝发送/尺寸持久化/对称布局；真实 Chrome 验证节点 380×560 初始尺寸、resize 到 540×680 持久化、分叉后锁定与继承记录、新父节点居中且同列对齐，验证图已删除。
- 图对话节点持续目标已实现：`0021_conversation_goals.sql` 建立独立目标与 Run 关联表，不复用 Issue Goal ID，也不创建隐藏 Issue/公共评论。每次工作结果成为节点消息；独立审核使用新 Run、只读权限、禁用工具及专用系统提示，审核 JSON 不进入聊天消息。审核要求严格 JSON，失败或格式错误显式阻塞；未完成自动续跑，默认最多 21 轮工作，工作与审核 Token 均计入预算。暂停/取消与停止 outbox 原子持久化，停止中拒绝恢复；目标在轮次间保留节点执行权，未结束前拒绝普通发送、分叉和删除。重启后可从已落库结果继续审核，重复调度不重复排队。节点 UI 提供目标开关、预算与进度、暂停/恢复/取消，九语言文案齐全。
- 本次目标验证分层：server 全量测试通过；新增 SQLite 生命周期、预算/轮数、并发幂等、暂停取消晚到事件、隔离与重开恢复测试，HTTP 和真实 dispatcher/outbox 使用确定性 Runtime 验证自动工作→审核→续跑→审核→完成（不调用模型）。新增测试通过 `-race`，server `go vet` 通过。前端 `verify:web` 177 项测试、类型检查与构建通过；真实 Chrome 的单个 `@goal` 用例用固定 HTTP 响应验收预算输入/审核状态/暂停恢复取消并截图，约 9 秒。本轮没有真实 Codex/OpenCode 目标调用或真实 Wails 验收，没有自动迁移用户正式数据库。

## 已获得的验证证据

- 修改前：Svelte check 无错误/警告，149 项前端测试通过；server/node/protocol/desktop 的 `go test ./...` 全部通过。
- 修改后阶段验证：Svelte check 无错误/警告，158 项前端测试通过；server、node、protocol、desktop 普通 Go 测试通过。ShellCheck（含新浏览器启动脚本）通过；OpenAPI lint 有效，仍有 3 个规则提示（健康接口、303 引导和未引用的偏好 schema）。
- 新增真实 Chrome 测试入口 `pnpm test:browser`，必须提供隔离服务的 `TEAMVA_LOCAL_STATE_FILE`；不记录含 Cookie 的 trace/HAR，不调用模型。
- 第一轮 Chrome：27 项中 25 项通过，暴露空项目 Git SSE 404。第二轮扩大到 31 项，暴露归档标签重复 JSON key；修复并新增 JSON 源 AST 重复 key 检查。第三轮 33/33；命令路径统一后第四轮 34/34 通过（1280×820、390×844、表单、拖动、草稿、归档恢复、开关、主题、Cookie 属性与刷新，以及本地目录资源 dispatcher 全流程）。
- 已实际读图：浏览器认证阻塞（修改前）、桌面概览/图会话/活动/编程工具、窄屏图会话/记忆/Agent/资源/任务/Skills/编程工具/计划任务，以及暗色设置修复前后、记忆归档和恢复。资源竖排已修正并确认；窄屏任务列表仍占固定 220px，后续改善主从切换。
- 浏览器中真实创建项目、图节点拖动并刷新保持位置、模拟 HTTP 503 时保留草稿、窄屏创建/管理项目均已通过。
- 更新后的 local-api-smoke.sh 和 local-browser-smoke.sh 已在无需 nonce、已有测试项目的同一隔离服务上通过。
- 完整门禁全部通过：server/node/desktop `go test -race -p=1 ./...` 与 `go vet`、protocol 测试、web check/test/build、ShellCheck（含启动脚本）、`git diff --check`；OpenAPI valid，仅有 3 个已知规则提示。
- 真实 Wails 边界验收（隔离 HOME，未触碰正式数据）：`wails build` 成功；启动后 bridge 消费 nonce（日志 `/desktop/bootstrap`），WebView 实际加载 `/workbench`、静态资源并轮询 `/v1/computers|agents|agent-services`；同进程 loopback 服务对普通浏览器也可用（curl `POST /v1/auth/local-session` 200 并读到本地用户）；SIGTERM 后退出来干净、`runtime.json` 被移除、无残留 Provider 子进程。
- 局域网模式验证：单元测试覆盖“拒绝无 opt-in 的非 loopback 绑定、拒绝无效初始令牌、loopback 免令牌、外部同源建会话、外部跨源拒绝、外部写操作需 CSRF、设置更新令牌后旧令牌立即失效、重启后仍从数据库读取”；真实非 loopback 接口 E2E 通过（无凭据 401 + WWW-Authenticate、正确令牌 200、错误令牌 401）；真实 Chrome 在 LAN 地址加载工作台并在设置页读到已保存令牌，无 JS 错误且无任何页面请求收到 401/403（Basic 凭据由浏览器缓存后 fetch/SSE 自动携带）。
- 设置页新增「局域网访问」区：显示监听状态、可复制的 external 地址、掩码令牌（显示/隐藏/复制）与风险提示；保存后立即生效。更新后的完整 Chrome 回归 35/35 通过（本轮因全新数据目录探针较慢，共 6.3 分钟）。
- 协作工具事件验证（真实 Codex `gpt-5.6-sol`，隔离数据目录与临时项目）：真实 Run 期间 `run_events` 出现 `provider.tool` 事件，`issue_get`/`issue_comment_create` 各有 running/completed 两条，payload 带固定 marker 摘要与 comment_id/version；Agent 评论按 marker 发布成功。真实 Chrome 打开同一隔离服务，会话时间线显示「读取任务 0.1s issue_get」「发布评论 0.1s <marker>」；另一次真实 Run 运行中截图确认「正在生成…」占位与滚动跟随，运行结束后占位消失。
- Node 新增 `TestFileBridgeReportsCollaborationToolEvents` 与 `TestFileBridgeReportsFailedEventForInvalidRequest`；Web 新增会话协作工具与生成占位渲染用例；Svelte check 0 错误 0 警告，159 项前端测试通过，node 全量测试通过。
- 测试分层实测（隔离服务 `127.0.0.1:43122`，真实 Chrome）：全量 35 项 2.3 分钟；`@smoke` 7 项 31.6s（登录/路由、3 个桌面布局、图拖动、记忆归档恢复、暗色对比度）；`@layout` 24 项 1.6 分钟；`pnpm test:backend` 56s；`pnpm verify:backend` 94.7s（含缓存命中）；`pnpm verify:web` 21.4s。
- 覆盖率基线（`go test -coverpkg=./...`）：server 70.0%、node 69.4%。`server/internal/httpapi` 仍有 32/225 个函数零覆盖（多为错误分支与少量端点），列为后续补测方向；浏览器验收继续负责真实渲染、SSE 与跨层流程。

## 继续审查与验证项

1. 当前已经删除 outbox metadata/cursor 回填、旧 poison completion 重解释和整条数据库升级链。两库只初始化当前基线，旧库只读拒绝并保留；缺失当前元数据不自动重建。旧仓库选择列已经从基线删除。
2. 协议转发别名、BindingVersion 回调/字段、远端 WebSocket Frame 和设备注册数据类型均已删除，当前只保留嵌入式命令契约。
3. ~~目录/资源命令双执行路径~~ 已统一为单一持久化 dispatcher 路径。
4. ~~图快照读取预算~~ 已增加显式上限与测试；后续若图规模增长可再做按节点分页。
5. ~~跨项目选择~~ 已加 key 重建与浏览器交叉用例。
6. Resource 重命名仅 API 可用、UI 未暴露入口；文档 `资源注册、验证、修改、删除` 中的“修改”目前只有改名 API。可在资源操作菜单加“重命名”入口（属功能补充，本轮未做）。窄屏 Issue 列表占约 30% 高度，可用但可进一步做主从切换。
7. 密码、邮箱验证、step-up、旧设备注册、执行路径指纹、wire_size_trusted 等旧字段及相关映射已删除。`computers.node_version='embedded'` 与当前协议版本仍用于本机运行时身份，不是历史兼容。
8. 已完成本轮截图逐张审查、race/vet 门禁与真实 Wails 隔离验收；真实多仓库 Git/Changes Chrome 脚本未在本轮运行（需运行中的多仓库 fixture 与 CDP 流程）。真实 Provider 模型调用未执行（会产生真实调用，需用户明确授权与测试 Resource）。
9. 网站文案、发行脚本和协议注释在本轮大清理后已整体核对引用；`README`/`AGENTS`/主架构文档已更新。最终报告区分各类证据。

## 操作边界

- 起始工作区干净；所有变更未提交；全程未调用子智能体。
- 数据与测试服务在仓库外的隔离临时目录；不使用用户正式应用数据。
- 当前真实 Provider 证据仅包括本机探测到 Codex/OpenCode；尚未进行本轮真实模型任务验收，也没有公开发布或正式签名操作。
- 当前隔离服务使用外部临时目录 `teamva-review-20260911/data-pass3`，监听 `127.0.0.1:43119`；为后续验收仍在运行。截图在忽略的 `web/test-results/` 中，重新运行 Playwright 会重建该目录。
- 最终报告必须分别列出自动化、真实 Chrome、真实 Wails、真实 Provider 与公开制品证据，不能用一个代替另一个。

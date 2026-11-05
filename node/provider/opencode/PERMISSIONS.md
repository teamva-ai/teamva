# OpenCode V2 的托管文件写入边界

## 契约依据

- [V2 permissions](https://opencode.ai/v2/docs/permissions)
- [V2 API](https://opencode.ai/v2/docs/api) 与实际内置服务的 `/openapi.json`
- [V2 plugins](https://opencode.ai/v2/docs/build/plugins)
- 固定版本源码：[Permission](https://github.com/anomalyco/opencode/blob/v2.0.18/packages/core/src/permission.ts)、
  [FileAccess](https://github.com/anomalyco/opencode/blob/v2.0.18/packages/core/src/file-access.ts)、
  [Shell](https://github.com/anomalyco/opencode/blob/v2.0.18/packages/core/src/tool/plugin/shell.ts)。

V2 使用 `permissions: [{action, resource, effect}]`，最后匹配者生效；不是 V1 的
`permission` map，也不是 `bash`/`task` action。`POST /api/session` 可接收会话规则，
`PATCH /api/session/{id}` 的 `permissions` **替换**旧规则。配置/原生 Agent 规则先评估，
会话规则在后；配置的 `deny` 不会被持久化的 always approval 覆盖。

## 根因与实现

原先普通 Run 返回空规则，且工厂没有保存 `InstanceConfig.WritableRepositories`。
V2 默认允许外部目录；`location.directory` 也不是写入沙箱。尤其当 Location 位于较大的
Git 项目之内，兄弟目录仍属于内部资源，根本不会触发 `external_directory`。

这是代码中的授权映射缺失与默认策略风险，**不是此前真实 Run 已实际越权的证据**。
主线程 Ego 在修复前的真实 Run 观察到合法 QA 仓库触发了 `external_directory` 人工
审批；实际 Agent、会话或持久化策略可以令其 ask，不能用文档的默认 allow 覆盖该观测。
修复既让 host 已批准的精确资源根不再重复 ask，又对未授权根采用明确 deny 策略。

现在由 Provider 工厂复制、去重并规范化不可变的精确写入根。会话默认拒绝 `edit` 与
`external_directory`，仅对指定根生成绝对路径和 Location 相对路径例外，不授权父目录
或同前缀兄弟目录。V2 通配符 `*` 跨越 `/`，因此根等于/包含 Location 时还显式拒绝
离开授权祖先的 `../` 路径及无授权绝对路径。无法字面表达的 `*`、`?`、反斜杠根、
相对根、缺失目录和文件系统根拒绝配置，不降级放开。

`ReadOnly` 为实例与 Run 的逻辑或，只读 Run 不使用仓库写入根。唯一传输写入例外是
host 明确提供的 `RunRequest.ToolRequestDir`，不再按 `.teamva/tool-requests/*`
整棵授权；同工作区的其他 Run IPC 目录也拒绝。绑定 Skills、私有配置和原生数据目录
对原生文件工具始终不可写。恢复会话前检查 Location 未改变，并替换规则，撤销旧 Run 的写入和 IPC 例外。
只读会话按本地读取动作白名单限制未知动作，因此 writable 实例恢复成只读时，先前加载的
自定义 MCP 也不能保留业务写入能力；显式 Computer Use 独立能力除外，不因此授予文件工具写权。

### 原生路径别名补充

2.0.18 的 FileAccess 按**词法路径**生成权限资源，不能误称为真实路径沙箱。
Provider 内嵌的 V2 `permission.evaluate` 插件仅能把允许/询问收紧为拒绝：写入目标或
最近存在父级的 `realpath` 不一致、符号链接目标、硬链接文件、无法确认的路径均拒绝。
它不放宽任何原生规则。加载失败或尚未激活时 Provider 在模型请求之前失败关闭。
插件同时移除会改变相对规则含义且没有原生权限检查的 `opencode_session_move`。

普通编码保留 native formatter/LSP/snapshot 行为；macOS 上子进程继承 Seatbelt，
Windows/Linux 不启用 Teamva 的 OS 写入沙箱。只读或 NoTools 实例关闭这些自动编码副作用。
原生 subagent 在正常 Run 中可用，子会话继承会话规则；只读时仍拒绝。Location 移动工具始终移除。

## macOS 的真实 OS 写入边界

V2 官方权限配置没有在上述文档中提供可据以授权任意 Shell 的 OS sandbox；命令扫描器
只能尽力推断路径。本 Provider 在 macOS **实际以 `/usr/bin/sandbox-exec -f` 启动 CLI**，
不是往 opencode.json 里猜测添加 sandbox 字段，也不是靠提示词或命令前缀判断文件写入。
`sandbox-exec` 是 Apple 已弃用的接口；当前已实测 macOS 27.0.1 arm64，不能据此承诺
其他 macOS 版本或未来系统一定支持。启动前用无凭据的 `true` 验证 profile 可初始化，
不可用时明确拒绝 Run，未来替代实现是维护风险而不是无沙箱回退理由。

- Seatbelt 默认拒绝，仅开放读取、执行/派生、同沙箱进程信号、网络及窄平台服务。
  文件写入只允许精确业务根、本实例原生数据、当前 Run IPC；配置/Skills/其他 IPC 拒绝。
- 原生 DB、日志、cache/state/tmp 和常用 npm/Go/Python/Bun 缓存放在实例数据目录下，
  不开放全局 `/tmp`、用户缓存或父目录。配置和认证文件可供 CLI 读取；V2 API Key 与公共
  目录采用下述窄投影，不复制或迁移原生账户库；fixture 只用临时凭据。
- Apple Git shim 的全局 xcrun 缓存不在授权范围。读取 `xcode-select -p` 后仅在私有 bin
  中链接实际 Git（不复制二进制）；其他 xcrun 使用官方 `xcrun_nocache`，不放开全局目录。
- **正常 Run 的 Shell/原生 subagent 仅在真实 OS 沙箱进程启动后放开**；Git、解释器、
  构建和测试可运行，其后代也受内核约束。只读 Run 仅在有本 Run 精确 IPC 时开放 Shell，
  用于合法 FileBridge heredoc；此时 OS 不授权任何业务根/CWD 写入，无 IPC 时仍拒绝 Shell。
- IPC 或 ReadOnly 范围改变时停止空闲原生进程树，按新 profile 重启；会话从本实例 DB
  恢复。新 Run 不沿用旧 IPC 写权，不打断活动 Run 以改授权。
- macOS 上无可用 sandbox-exec 或 profile 初始化失败时明确拒绝 Run，没有静默无沙箱回退；
  Windows/Linux 不启用 Teamva 的 OS 写入沙箱，进程权限交给 OpenCode 与操作系统。
  只读/NoTools 仍由权限规则约束，不把“禁用所有 Shell”当成正常编码功能通过。

## 原始认证与会话校验启动

独立 OS fixture 复核发现并修复了两个条件缺口：原始 `auth.json` 若落在 business root 内，
第一版 profile 会允许经私有 auth symlink 写回；首次 `VerifySession` 也曾在 Run 准备之前
裸启动 native CLI。这是认证来源/启动边界的实测，不是此前真实模型已越权的证明。

- 新增写保护代码只记录两种原始认证候选路径的元数据/真实目标，另保护本实例保留的 auth binding
  及其目标。即使真实来源位于业务写入根或实例数据根内，也无条件拒绝写、删、改名和
  新建硬链接；其祖先目录拒绝 unlink/rename，不能先搬走目录再绕开字面路径保护。
  原生 edit 规则同样拒绝这些文件。路径解析处理符号链接、旧 binding 和 dangling target，
  不追加读取/复制账户内容，原有 Provider 认证接入未改；这里只保证写保护，仍不宣称读取隔离。
- 所有 managed 冷启动入口统一在启动任何 native/plugin/MCP 代码前选择 OS profile。
  `VerifySession` 使用无业务写入根、无 Run IPC 的最小 profile，仅允许必要私有 native
  数据/缓存。暖校验也撤销上次业务/IPC 范围；后续 Start/Resume 停止空闲校验进程，按本 Run
  精确范围重启并从原私有 DB 恢复。profile 选择与 spawn 共用启动锁，不留竞态裸启动窗口。
- 公共 Adapter 接口未改。专用 discovery Probe 保持独立 version/model catalog 的无模型
  诊断路径，仍只连接自己启动的私有服务，不用用户默认服务验证会话；它不能被复用来
   VerifySession 或 Start/Resume，不为 rootless Probe 伪造 business write 根。

## 会话默认模型

### V2 账户与公共目录投影

旧接线的 Probe 直接使用全局 native DB，而托管实例从空 DB 启动，只从旧 `auth.json` 复制
API Key；V2 的 `credential` 以及 `kv['models-dev:catalog']` 都没有进入托管视图。因此两者
既可能缺失已登录的 Key Provider，也可能分别使用当前目录和过期内置快照；仅在托管实例
自己的目录内核验默认候选，无法纠正这个来源差异。

现在 Probe 与托管实例使用相同窄投影和各自独立的新 native DB：

- 以 SQLite `mode=ro`、`query_only` 读取默认 V2 `opencode.db`，不经原生启动打开用户库，
  不迁移、升级、改写、全库复制或导入会话。错误只返回固定消息，不暴露 DB 内容/路径。
- 按原生 active、创建时间、ID 选择每个 integration 的当前账户；SQL 只向 host 返回
  类型及选中 `key` 的 Key 字段，**不读取或注入 OAuth token**。选中 OAuth 时不回退到
  同供应商的旧 JSON Key。明确的用户 Provider Key 配置优先。
- Key 仅进入既有原子写入的 0600 私有 OpenCode 配置；不进入 app.db、runtime.db、命令
  参数、事件或日志。原始账户 DB、真实目标及 WAL/SHM 均加入 OS/原生工具写保护，即使其
  父目录被列为业务写入根也不放开。
- 仅将固定公共 catalog cache 的 body 导出为 0600 元数据文件，通过原生
  `OPENCODE_MODELS_PATH` 的**文件路径**提供给隔离实例；不导出其他 KV 或凭据表。
  这是配置重建时的目录快照，不宣称持续同步用户账户或目录缓存。

OpenAI 等 OAuth-only 登录在此投影中仍不支持；Probe 不再凭全局 OAuth 目录宣称托管模型
可用。需要该供应商时应另配置其 API Key；未来 OAuth 集成需要独立刷新/撤销设计。
`TestBundledNativeAccountProjectionWithoutInference` 使用 fake V2 DB、fake Key/OAuth 和公共
目录元数据验证 Probe/托管目录一致、源库字节不变、真实 OS 拒绝 DB/WAL/SHM 写入；
没有 prompt、真实账户或推理请求，不据此宣称真实 Key 已通过上游认证。

新建 Run 优先使用明确的 `RunRequest.Model`；未指定时从同一 Location 的 `/api/config`
按文档优先级读取最后一个根 `model`。此接口是 Config.Entry 数组，不是 envelope；根选择
支持字符串及 `{providerID, model, variant}`。只解码模型字段，不记录其他配置内容。
明确的 Run/根配置是用户意图，即使已不在目录中也不擅自换成其他模型或供应商。

**`/api/model/default` 非 null 不代表用户已配置默认。** 固定 2.0.18 的初始内置快照与异步
更新目录可给出过期候选。没有明确根默认时，自动候选必须在当前原生 `/api/model` 目录中；
空目录沿用最多 8 秒 warmup。候选缺失时重新检查原生默认，仍须目录核验，否则取当前首项，
显式写入 session.model。使用 `providerID` 与目录 ID `id`，不能用可能不同的上游 `modelID`。
无目录或接口异常时明确失败；不写死替代型号，不在模型请求失败后更换供应商重试。

Resume 保留已有明确模型与 variant；仅旧会话未选模型时按本 Run 显式选择或上述默认规则
调用原生 `/model`。恢复前的 Location 校验、累计 usage baseline 和权限整组替换保持不变。
`TestBundledDefaultModelSelectionWithoutInference` 使用隔离 HOME、合成 Provider 和真实内置
API/schema 检查创建、Resume、空模型补选及 usage/权限不变；不提交 prompt、不调用真实模型。
`TestBundledUnconfiguredDefaultStaysInCurrentCatalogueWithoutInference` 则没有根模型或 Provider
覆盖，通过原生 `OPENCODE_MODELS_URL` 提供可控的纯元数据刷新，将真实初始默认响应延迟到
真实目录更新完成后交给适配器，确定性复现非 null 旧候选已不在当前目录的竞态。冷启动、
暖实例及进程重建均断言保存的选择属于当前目录、usage=0、prompt 与推理端点请求数均为零；
这不是实际模型可用性或真实 Lumi Issue 成功的证据。

## System override 与真正的 NoTools（Goal 审核 / 独立分支）

固定 `v2.0.18`（tag `cd9a14a6b688d4021bee381dfd39d2cef9c0f862`）的
[`baseTranscript`](https://github.com/anomalyco/opencode/blob/v2.0.18/packages/core/src/session/model-request.ts)
只有 `agent.system` 非空才替换默认 coding prompt。该版本虽然接受配置 `instructions`，
但 instruction plugin 没消费这个字段；把 SystemPrompt 放进 instructions.md 的旧接线
不能保证 system override 实际生效。现在配置使用 **`agents.build.system`**，新会话显式选
build，Resume 恢复该 Agent；模型发送前核验原生 `/api/agent/build` 的 system 精确匹配，
缺失/不匹配则失败关闭。工作区说明由受控 context hook 注入，不再依赖未消费的字段。

`DisableTools` 不等于 `ReadOnly`：普通只读助手仍可读取和使用精确的 FileBridge IPC；
NoTools 则不能执行任何原生/自定义工具，也没有 FileBridge、Computer Use 或自定义 MCP。
Runtime 将协议字段同时传入不可变 InstanceConfig 与 RunRequest，配置指纹覆盖该值；
忙实例不换配置，空闲实例切换能力时重建原生进程。直接 SDK 调用若仅把一个已带工具实例的
RunRequest 改成 NoTools，明确失败，避免已经配置的启动 MCP 被误称为已撤销。

[`Tool.snapshot`](https://github.com/anomalyco/opencode/blob/v2.0.18/packages/core/src/tool.ts)
不只读取工具 registry，还会单独合成 Code Mode `execute`。因此 NoTools 使用最终
`{action:"*",resource:"*",effect:"deny"}`，在只读/Skill/IPC 例外之前返回，真实移除
整个模型工具目录与可执行快照（含 execute），不是仅关 FileBridge。不可变插件另外移除
registry 工具、清空请求 tools 并 deny 全部权限；恢复旧会话时丢弃旧 system 中的历史工具
目录，仅保留真实 agent.system 与本 Run 明确注入的工作区/绑定 Skill 文本。无工具的 Skill
正文由 host 从已校验的私有副本注入，脚本/工具权限不会随提示词授予。NoTools OS profile
也不授权业务根或 IPC；若原生服务仍报告 tool.success，则明确报契约错误并停止进程，
不把它投影为审核成功。普通 Shell/Seatbelt/auth/最小 Verify profile 不放宽、不退化。

确定性原生验收（仅 localhost fake model / fake auth，无真实推理或账户）：

```sh
TEAMVA_OPENCODE_TEST=1 go test ./provider/opencode \
  -run '^TestBundled(NoToolsAndSystemOverrideOffline|ParallelNoToolsCatalogsOffline)$' -count=1 -v
```

测试检查**真实出站模型 HTTP** 的审核 JSON system 强约束、空 tools/无 Code Mode 目录，
强发 execute/read/write/shell 仍不能执行，最终 JSON 原样成为 canonical `output.delta`。
覆盖冷 Resume 的只读助手→审核→独立分支→只读助手→普通 Issue，恢复合法 read、Shell 与
精确 IPC，并用真正重叠的两个原生实例检查系统提示/工具目录/输出不串线。此证据不替代真实
模型、Ego 浏览器或 Wails 验收，也不声称能使不遵循指令的模型必然输出合法 JSON。

## Skill 原生绑定隔离

隔离 HOME 的真实 2.0.18 复现证明：只设置 `XDG_CONFIG_HOME` 和显式 `skills` 来源不足以
隔离技能；原生来源是叠加关系，仍会加入 HOME 下 `.agents/skills`、`.claude/skills`、
私有 native skills 目录及内置 `opencode/report`。已有“禁用外部技能”环境变量在该版本
未阻断这些来源。出站模型 HTTP 确实包含未绑定描述，原生 skill 工具确实可加载未绑定
合成正文；这不是依据真实模型自述推断的 bug，也没有读取用户实际全局技能/认证来复现。

- `skills` 配置只列当前绑定的私有目录，不再扫描包含残留制品的整棵 `skills/`。
- 保留一个已安装制品包含多个嵌套 `SKILL.md` 的现有契约，按固定版本的真实 parent-derived
  ID 逐项绑定；根 README 等非入口文本不变成技能。原生 ID 冲突/通配符无法精确表达则拒绝。
- 受控插件用官方 `ctx.skill.transform` 从**原生注册表**移除所有非精确 ID＋私有
  `SKILL.md` 路径的定义；同 ID 的 HOME 定义不能替代私有副本，缺失/软链接/重复绑定拒绝。
- 固定版本的 compatibility/config source transforms 在用户插件 setup **之后**注册。
  因此不能把 setup 中一次 filter 误当成生效：host 在每次 Run 先调用受控 RPC `seal`，
  原生 RPC handler 等待全部插件激活，再将 deny-only filter 注册到最后；随后对
  `/api/skill` 的 ID、路径和数量逐项核验，不匹配则在创建/提交 prompt 之前失败关闭。
  source refresh 会按既有顺序重放 transforms，不能绕过最后的过滤。
- 会话权限默认拒绝 `skill *`，仅允许精确绑定 ID；原生 permission hook 另核验该 ID
  当前定义的路径。NoTools 仍优先 deny 全部、空执行快照，只注入 host 绑定正文。
- 冷 Resume 的 native initial instruction snapshot 会保留旧目录。context hook 只替换
  native 补充 system 中的旧 `<available_skills>`，重新渲染当前绑定；不移除正常 coding/
  工具 system，不篡改用户会话记录。NoTools→普通助手时也重新加入正确目录。

契约参考：V2 [Skills](https://opencode.ai/v2/docs/skills)、
[Skill transform](https://opencode.ai/v2/docs/build/plugins#skills)、
[RPC](https://opencode.ai/v2/docs/build/plugins/rpc)，以及固定 tag 的
`packages/core/src/{plugin/internal.ts,config/plugin/compatibility.ts,config/plugin/skill.ts,skill.ts}`
和 `packages/server/src/handlers/rpc.ts`。

`TestBundledBoundSkillsIsolationOffline` 检查实际出站请求、原生注册表、绑定正文加载和
未绑定/跨实例加载失败，覆盖冷/暖 Resume、重建、重绑、零绑定、NoTools、正常 Issue 和
真正重叠的不同绑定实例；另让原生仅能发现同 ID HOME 替身，断言在 localhost 模型 HTTP
之前失败关闭。所有来源/正文都是合成哨兵，未调用外部模型或改用户技能。

这是**自动发现/Skill 工具的绑定边界**，不是文件读取保密沙箱。允许的 read/Shell 仍有
既有读取能力，用户直接传入的文本、旧会话已合法加载的正文也不会被抹除。已经泄漏到旧
会话历史中的内容不能被撤回；目录刷新不能等同于遗忘历史或清理数据库。

## 边界和未承诺事项

### Run 临时文件与 zsh heredoc

`TMPDIR` 单独不足以改变 zsh here-document 的文件前缀。带 FileBridge 的 Run 现在由 host
在精确 `ToolRequestDir/.tmp` 创建 0700 目录；使用 `os.Root` 锚定 IPC/tmp inode，拒绝符号
链接、文件冒充与祖先替换，创建后及启动前再次核验。暖进程在同路径丢失/替换临时目录时
失败关闭，不静默重建。临时路径属于配置指纹的一部分，换 Run IPC 时原生进程与环境一并更换。

原生进程的 `TMPDIR/TMP/TEMP` 指向该目录，`TMPPREFIX` 指向其 `zsh` 前缀。
固定 2.0.18 [`Shell.create`](https://github.com/anomalyco/opencode/blob/v2.0.18/packages/core/src/shell.ts)
从 `process.env` 或 session environment 取环境后调用原生 `shell.create.before` hook。
受控插件通过此 hook 核验目录/祖先身份并覆盖这四项，防止保存的 session 环境保留旧 Run
临时路径；没有猜测 V1 `shellEnvironment` 配置字段。无桥/NoTools 不创建 Run `.tmp`，不注入
Run 临时环境、不增加 IPC 或全局 temp 写权；必要 native DB/cache 的既有最小范围不变。

`TestBundledRunTempHeredocOffline` 用真实内置 CLI + zsh/Python/mktemp/Node 首试执行，覆盖
普通 Run、只读 IPC 助手、同暖进程保存环境覆盖、换 IPC 的 warm Resume、冷重建/Verify/Resume
和两个真正重叠的独立实例。旧 Run `.tmp`、其他 Run、兄弟目录和只读业务目录不可写，原生
成功 Shell 的 exit=0，实际拒绝的 Shell exit≠0，不能把后者伪造成成功退出。

macOS 系统 `mktemp` 的无参数默认目录可来自 confstr，实际会忽略 TMPDIR；不会为它放开
公共 `/tmp` 或用户全局 temp。测试使用标准的 `mktemp "$TMPDIR/fixture.XXXXXXXX"` 明确模板，
不把裸 `mktemp`、模型多次试错后绕过或只用原生 write 当作该场景通过。

### JSON 审核的能力边界

2.0.18 session create/prompt API 没有标准逐 Run `response_format` / JSON Schema 字段，
也没有独立的内置 conversational/NoTools Agent；`agent.system` 是可用的原生系统替换。
底层 `LLM.generateObject` 使用具名生成工具，不适用于这里的真正 NoTools 审核契约。
某些协议可用 provider/model raw body overlay 传输 JSON 模式，但它不是统一的 session
结构化输出保证，需要明确模型/网关支持及独立验收，不能给普通编码 Run 全局强加或静默
退回提示词。当前不扩大平台协议，也不解析 DSML 文本为工具调用或成功 verdict。

fake model 返回 JSON 只证明系统/目录/输出通道接线；真实模型仍可返回无效文本。必须区分
原生工具事件与看似工具调用的 assistant DSML 文本：后者没有原生执行证据，仍应作为无效
审核结果保留，而不是启用工具补救或假称该真实模型已经通过。

Seatbelt 会解析符号链接，并拒绝创建跨边界硬链接，但**已有硬链接**的授权别名仍能写同一
inode。因此启动前扫描写入根，将已有多链接文件设为只读，并禁止重命名其祖先目录以
绕开字面路径排除。此检查可取消；大型目录会有扫描成本。保守策略也会使授权根中原有
硬链接文件不能被编辑/删除。没有声称防御并发的、沙箱外同用户文件系统攻击者；这种
攻击者可以在扫描后修改目录/链接，基于 FD 的强 inode 边界仍需要其他架构支持。

这是**本 CLI 进程树的本机文件写入边界，不是读取保密性或网络/全主机安全沙箱**。
Shell 可读配置与原始认证，网络没有隔离；不能声称防止秘密被模型读取/外发，也不能
把通过 HTTP、外部服务或 GUI 操作让另一个进程写文件视为已经被此 profile 覆盖。
正常本地 MCP 子进程同样继承写边界；远端 MCP、已运行的应用以及 Computer Use 的 GUI
间接效果不是本机子进程直接文件写入。Computer Use 不增加原生 edit 或 OS 写入根；
其 GUI/Accessibility 服务适配尚需专门验收，不能据文件 fixture 宣称它完全可用或完全隔离。

Git linked worktree 若需要修改范围外的 common Git 元数据，必须由 host 明确提供相应
授权根；Provider 不读取 `.git` 指针就擅自放开父仓库。本轮验证的是根内 Git commit、
两个均已授权根之间的 worktree，以及根内 Node 测试，不声称任意范围外 Git 元数据可写。

## 已有测试与验收分界

```sh
go test -race ./provider/opencode ./provider/catalog
go vet ./provider/opencode ./provider/catalog
TEAMVA_OPENCODE_TEST=1 go test ./provider/opencode \
  -run '^TestBundledPermission(BoundaryWithoutModel|FileToolsOffline)$' -count=1 -v
go test ./provider/opencode -run '^TestMacOS' -count=1 -v
```

原生测试使用真实内置 2.0.18 和隔离 HOME/数据目录；权限 API 测试不传账户凭据并关闭
模型目录拉取，没有 prompt 或推理请求。覆盖真实会话规则保存、实际权限 evaluator 与别名 hook、
精确根/兄弟/父级/资料目录/混合多文件、软硬链接、无 OS 沙箱时 Shell 拒绝、ReadOnly 和 IPC 撤销。
同时覆盖先前项目级 broad always approval 不能覆盖当前会话 deny。
**权限 API 验证不是实际工具执行或真实模型验收。** 另一个离线原生工具测试使用确定性的
localhost 合成 HTTP 响应，清空环境里的真实 Provider 凭据，只配置 fixture 假凭据，
不发真实模型请求（并不宣称是断网测试）。它实际执行 V2 的 shell（touch/Git commit/
node --test）、write/edit/patch、混合补丁拒绝、路径别名拒绝、只读 Resume 与精确 IPC；
同时断言原生工具事件、Shell exit=0 和实际文件内容。独立 OS 测试还覆盖真实 Git worktree、
Shell 重定向、子进程继承、预存硬链接与其祖先重命名、原始认证链接只读、范围撤销和引号根。
追加测试将 fake auth 来源放进业务写入根，验证直接/多级链接写入、删除、目标和祖先
重命名均被 OS 拒绝；启动 fixture 在任何 API 请求之前尝试写 root 外、business root 和
IPC，证明冷 Verify 已受最小边界约束。真实 2.0.18 合成工具测试还先冷 Verify 再 Start，
保存 session 后关闭整个 Client，重建 Client 冷 Verify 旧 session 再 Resume；校验期间
localhost 合成模型请求数不增加，原 fake auth 保持未变，合法 Shell/Git/Node 测试仍成功。
这仍不是实际大模型验收。真实模型的文件工具对抗写入应由
主线程经用户要求的 Ego 浏览器，在专用 fixture 内继续验证，不能用模型自述代替文件
与原生工具事件证据，也不能拿用户目录做写入实验。

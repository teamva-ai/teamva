# Teamva 开发与发布指南

本文供在本仓库工作的开发者和 Agent 使用。当前 `main` 的唯一产品主线是本地优先工作台：
同一个 loopback 服务支持普通浏览器和 macOS Wails 窗口。产品尚在开发，没有需要兼容的已发布版本；只维护当前行为、API、配置和数据结构。

## 安全与操作边界

- 不把密钥、令牌、密码、Cookie、Provider 凭据、数据库连接串或 Secret 值写入仓库、命令输出或回复；
- `.env`、`.dev.vars`、本地数据目录和生成制品保持未跟踪；
- 仓库可能有用户修改，只改当前任务文件，不覆盖或清理无关变更；
- 生产部署、公开发布、正式签名和生产数据库写操作需要用户明确授权；
- 测试通过、`readyz` 或构建成功不能替代真实 Wails、浏览器和 Provider 验收；
- 当前产品不需要在线备份、在线恢复、云同步或数据库损坏修复，不为此引入功能；
- 数据库基线或 integrity 检查失败必须保留数据库并拒绝启动，绝不升级旧库、回填或自动清库重建；
- 本机与局域网只使用一个服务端口号。远程访问默认关闭，在本机「设置 → 远程访问」显式开启并设置密码后，才在具体局域网 IPv4 地址上增加同端口号的独立监听 socket；localhost 入口始终保留且免访问密码，外部入口必须密码登录，开关即时生效并持久化。禁止无密码 wildcard 监听。设置页不再提供第二个端口；`pnpm start:browser` / `pnpm start:lan` 默认服务端口 41720，后者仅提供 CLI 首次配置入口，无密码不监听外部；
- 不把访问密码写入仓库、日志、URL 或回复；仅 bcrypt 哈希存于本机 app.db，不回显密码或哈希，不进入诊断导出或 Provider 子进程环境。远程使用自有密码登录页与 HttpOnly 会话，不使用用户名或浏览器 Basic 弹窗；远程客户端不能修改此开关/密码；HTTP 仅用于可信 LAN/VPN。
- 「信任此设备」首次需要在登录时显式勾选：服务端长期保留信任，不设置到期时间，仅实际修改访问密码时原子撤销所有设备信任。重启、关闭再开启远程访问、同 IP 更换服务端口不撤销信任；关闭远程访问仍立即停止外部监听和连接。本机仅持久化随机凭据摘要和 Host，不存密码或原始 Cookie。浏览器 HttpOnly Cookie 按 Chrome 400 天存储上限签发，每次已认证访问续期；这不是服务端信任 TTL。浏览器清理登录数据、更换浏览器或 IP 时可能需要重新登录，不能承诺绕过浏览器自身的数据保留限制。

## 当前产品架构

- `desktop/`：Wails v2 应用壳、菜单、单实例、日志、诊断和更新；
- `web/`：浏览器与 Wails 共用的 Svelte 5 UI；
- `server/app/`：可嵌入的 localhost 服务组合层；
- `server/internal/`：领域、HTTP、SQLite Store、调度和 ToolService；
- `server/sqlite/schema/`：当前控制面 SQLite 基线；`node/internal/localstore/schema.sql` 为运行库基线；
- `node/embedded/`：桌面可导入的通用 Provider Controller；
- `node/provider/`：公开 Provider API、自动发现 catalog，以及各 Coding Agent/CLI 的独立接入目录；
- `node/internal/`：Runtime、Run 级 MCP 工具通道、FileBridge 回退和 ToolBroker；
- `protocol/`：Local API OpenAPI 与 Embedded Runtime 协议；
- `scripts/e2e/`：API、Chrome 和真实 Provider 验收；

本地服务在同一个 loopback 端口同时提供 SPA、HTTP API 和 SSE，开启局域网后仍共用该端口号。自动分配的端口会持久化供重启复用；CLI 默认 41720。Svelte 业务只走 HTTP，Wails binding
只处理窗口和原生能力。`app.db` 保存控制面，`runtime/runtime.db` 保存执行恢复；Codex/OpenCode 凭据
留在 Provider 自己的本机目录。

权威文档顺序：代码/测试 → OpenAPI/协议 → `ARCHITECTURE.md` 与
`docs/DESKTOP_ARCHITECTURE.md` → `docs/00–12`。

## 开发环境

需要 Go 1.25.x、Node.js 24.x、pnpm 11.x、jq、ShellCheck、Chrome 和 Wails v2.13.x。

```bash
./scripts/phase0/probe-environment.sh
pnpm install
go version
node --version
pnpm --version
```

桌面开发不需要 PostgreSQL 或 Docker。

现有旧 app.db/runtime.db 不再支持。新构建只初始化空数据库；旧库在只读校验阶段拒绝，不会被覆盖。
验证或启动新构建必须显式选择新的 `TEAMVA_LOCAL_DATA_DIR`，不要为了启动而自动删除默认数据目录。

新增 Coding Agent 接入只放在 `node/provider/<id>/`，按 `docs/PROVIDER_API.md` 实现 manifest 和工厂。
构建时自动生成注册代码，不在核心增加 Provider 名称 switch 或枚举。直接使用 Go 构建命令前运行
`pnpm generate:providers`；使用 `pnpm check:providers` 检查生成文件是否过期。

OpenCode（v2 协议）由应用内置：运行 `./scripts/vendor/opencode.sh` 按平台拉取固定版本到 git 忽略的
`vendor/opencode/`（不做全局安装、不改 PATH）；运行时解析顺序为设置页自定义路径 →
`TEAMVA_OPENCODE_PATH` → 内置二进制 → PATH，桌面发布脚本会把
darwin-arm64/darwin-x64 载荷打进 App Resources。设置页「编程 CLI」可为任意 Provider 指定外部
可执行文件路径并立即生效（回收空闲实例，不打断运行中的 Run）。真实内置二进制的集成测试用
`TEAMVA_OPENCODE_TEST=1`（跑真实模型再加 `TEAMVA_OPENCODE_MODEL=<provider/model>`）。

## 构建和启动桌面应用

桌面 Web 必须同源构建：

```bash
pnpm --filter @teamva/web build
./scripts/desktop/stage-frontend.sh
(cd desktop && go run github.com/wailsapp/wails/v2/cmd/wails@v2.13.0 build)
open "desktop/build/bin/Teamva.app"
```

禁止手工维护 `desktop/frontend/dist` 第二份源码；该目录是忽略的构建暂存区。

普通浏览器运行：`pnpm build && pnpm start:browser`，打开日志里的 loopback origin。
首次访问通过严格同源的 `POST /v1/auth/local-session` 引导；仅 Wails 的私有桥使用一次性 nonce。

隔离的 localhost API 调试：

```bash
export TEAMVA_LOCAL_DATA_DIR="$(mktemp -d)"
export TEAMVA_LOCAL_WEB_DIR="$PWD/web/dist"
(cd server && go run ./cmd/local)
```

`runtime.json` 和临时数据不提交；bootstrap nonce 仅供 Wails 私有桥使用，绝不输出。

## 多 Agent 语义

本机默认项目名固定为「默认项目」，使用真实稳定 Project ID 与只读 `is_default` 投影，不能删除或改名，说明仍可编辑，项目不提供配色设置；保护按身份而非名称。初始化幂等新增该项目，仅按真实受保护身份将旧名称「不使用项目」一次更新为「默认项目」，不改普通同名项目、SQL 基线或迁移/清理数据。新窗口无显式选择时默认进入，窗口内手动选择与刷新通过 sessionStorage 保留，明确项目深链仍有效；不得用 localStorage 的旧项目选择覆盖新窗口默认。默认项目是普通业务容器；Lumi 的只读边界仅属于工作记录/灵感等全局助手会话，不是 Agent 身份限制。详见 `docs/DEFAULT_PROJECT.md`。

工作记录位于全局 `/work-records`，原活动记录位于「设置 → 日志」。记录正常名称、ID、时间、来源/目标和非正文变更，不复制上下文或认证秘密；底层变更随业务事务提交。系统内置 Agent Lumi 保留不可删除且保持启用的身份保护；除此之外可像普通 Agent 一样配置、参与 Issue 和定时任务，不得再以 Lumi 身份限制只读或执行。Lumi（含 Roomie 等自定义名称）与普通 Agent 只在 `/agents?agent=<id>` 共用表单编辑名称、备注、Provider、模型、提示词和 Skills，通过公共 `PATCH /v1/agents/{id}` 显式保存；不保留 `PUT /v1/work-assistant` 或其他入口的配置表单。工作记录助手是独立入口的全局会话，使用专属 home 和只读工具作用域，用户提示词与 Skills 实际传入该会话 Run，但不扩大权限、不伪造项目。详见 `docs/WORK_HISTORY.md`。

灵感位于全局 `/inspirations`，按项目/未分组展示便利贴；项目是分组而非所有权边界。正文只存 Markdown TEXT，不存编辑器 JSON/HTML 双份文档；变更记录不复制正文。灵感助手复用 Lumi，但使用独立 inspirations 会话范围，仅可检索列表和已保存内容，不授予编辑/完成/删除权限。详见 `docs/INSPIRATIONS.md`。

首次配置引导：空状态（无用户 Agent 且 Lumi 未配置）时默认入口自动打开遮罩弹窗，子页显示提示条。严格只有「编程 CLI 检测 → 认识 Lumi」两步，完成后进入任务 `/issues`，不自动打开 Lumi 配置；引导不编辑、不自动保存配置、不提供 API 第三步。未配置 Lumi 的 Agent 草稿优先内置 OpenCode 与其默认模型，不伪造可用性或免费承诺。模型供应商弹窗在 Agent 页对所有 Agent 可用，测试仅显式发送一次 Hello；保存供应商只选入当前 Agent 草稿，需公共保存才生效。API Key 留在 Provider 本机私有 0600 文件，禁止进入数据库、Runtime 命令、日志、响应或诊断；配置/测试仅本机已认证同源 CSRF 入口。跳过/完成持久化在 `app_metadata`，设置可重开；provider.json 提示由生成器校验。详见 `docs/ONBOARDING.md`。

业务 `input/select/textarea` 必须复用 `web/src/ui`，不要重建页面私有控件样式。横排普通 Field 使用 FieldGroup，共享 label/control/hint 轨道，不能靠父 Grid stretch、固定 label 高度或局部偏移对齐；无标签工具栏保留专用可换行布局并使用统一控件尺寸。几何回归应断言真实 border box，覆盖非对称 hint、多行标签与触摸设备，不以无溢出或 CSS 类检查替代。尺寸、Select 箭头间距、Modal 焦点/inert/嵌套栈、Field 自动关联及 DatePicker ISO 时区和秒/毫秒规则见 `web/src/ui/README.md`；辅助控件使用 `inheritField={false}` 并提供独立可访问名称。

输入类组件在所有状态下禁止 Outline，包含键盘 focus-visible、focus-within、校验、只读、禁用、复合选择器及富文本/代码编辑区域，也不能用 box-shadow/ring 外圈替代。统一由首层 input-policy important 重置，焦点使用边框/颜色并保留键盘操作；普通非输入按钮、导航和链接的键盘焦点提示不受此约束。

项目导航顺序为概览、任务、分支对话、资源、项目记忆、定时任务、变更。本机导航仅列 Agents、技能 & MCP；编程 CLI 配置保留设置中，旧编程工具深链仍可访问。Agent 列表名字右侧显示真实 CLI，下面显示备注，不显示模型，未配置不能伪造连接。Lumi 空白或旧版生成的只读备注可更新为默认说明，保留自定义备注；Lumi 配置后与普通 Agent 一样可参与定时 Issue。

顶栏中部保留全局搜索，右侧留空，不显示 Coding CLI 名称、版本、就绪指示或无可用工具占位。审批提醒与功能帮助放在左侧品牌旁，保留安全确认和首次演示行为；工具详情仍在设置和 Agent 配置中提供。

`/skills` 的技能/MCP 页内 Tab 使用原有技能操作与独立 MCP 管理器。MCP 配置/授权只在本机已认证同源 CSRF 入口管理，秘密在私有 0600 文件中，不进数据库、命令、事件、日志或诊断；响应只给保留哨兵。个人/项目同名按项目覆盖（含 disabled），新配置不自动启动程序；管理器显式连接是目录探测，启用配置供正常 Agent 下一 Run 注入，不打断活动 Run；工作记录/灵感等只读会话不注入自定义 MCP，Lumi 执行普通 Issue 时按配置 Agent 规则使用。OpenCode/Codex 按各自原生能力转换，不能假称支持对方专属字段；本页 OAuth 仅临时随机 loopback 回调，固定回调配置仅透传支持的原生 Provider。详见 `docs/MCP.md`。

全局外观在「设置 → 常规」配置，复用 UserPreferences 的 appearance 与版本锁，通过既有 app_metadata 原子持久化，不修改数据库基线。七套配色支持明暗、自定义背景/强调色/独立控件色与可读性修正，默认 `default` 为浅色 `#fbfbfb` 画布/白卡片/`#09090b` 文字，强调与主按钮使用低饱和深绿 `#52763a` 配白字、强调文字用 `#426523`；暗色保留 `#111114` 画布/`#18181b` 卡片/`#fafafa` 文字及 Silken 品牌绿 `#c2ff75` 强调与主按钮；图标及官网品牌色不变。`Appearance.control` 派生主按钮、开关、复选框及输入底色/边框 token。修改即时生效并串行自动保存，无独立保存按钮，失败保留草稿，版本冲突不得静默覆盖。不再提供「跟随项目」或项目配色，Project 创建/编辑与公开 API 均不含 theme；SQLite 的遗留 `projects.theme` 列不改基线、不迁移，仅新建时写固定占位满足 schema，读/更新不参与配色。旧存 `appearance.palette=project` 仅只读投影到 `default`，不回写、不改版本，不覆盖其它全局预设或用户自定义色。远程访问开关即时应用；首次密码或替换密码仅回车/离开密码组提交完整有效值，不能逐字提交或因显示密码按钮误触修改信任。编程 CLI 路径完成输入后回车/失焦应用。详见 `docs/APPEARANCE.md`。

调色板的 `input` 仅更新本地即时预览，不因拖动停顿而按防抖周期持久化；`change`、失焦或离页时将最终颜色一次提交到既有串行队列。重复 change/blur、拖回当前有效色不发请求、不增加偏好版本或审计；失败和版本冲突仍保留草稿并显式重试，不过滤、合并或删除真实历史日志来掩盖高频写入。

项目左侧「定时任务」沿用 `/schedules` 与既有调度器，支持 daily/weekly 多精确时间、once、Cron 及 IANA 时区。新任务选择已配置 Agent（包括配置后的 Lumi），每次新建 Issue 或追加到本项目已有 Issue；不可绕过暂停/关闭或跨项目边界。Issue、用户提示评论、mention、实例、Run 与触发记录须事务原子、同触发幂等；完成总结仍由 Agent 显式工具发布。扩展配置原子保存在 app_metadata，保留日历标记防缺失配置静默退回独立运行，不改 SQL 基线或迁移库。详见 `docs/PROJECT_SCHEDULES.md`。

- Issue 是协作容器，评论使用结构化 `mentioned_agent_ids` 激活 Agent；
- 同一 Agent Instance 内 Run 串行，不同实例可并行；
- 禁止 Agent 直接 @ 自己；
- 明确允许 `A → B → A` 等间接循环，不设委派深度、不自动终止；
- 每次 mention 和 Run 必须持久化、可观察、可恢复；
- 用户通过停止实例、暂停/恢复 Issue 和清空待执行队列急停；
- 单 Agent 的普通 @ 保持直接发送；第二个不同 Agent 转入回复框下的工作分配，粘贴/快捷键/HTTP 不能绕过。`work_items` 可重复 Agent 并显式依赖本批次工作，和普通 `mentioned_agent_ids` 互斥；任务定义通过 app_metadata 原子持久化，不改 SQL 基线。等待由系统调度，不让模型轮询；可选整理必须严格 NoTools，不能降级或偷偷换 Provider。详见 `docs/ISSUE_WORK_ITEMS.md`。
- Issue Agent 完成后必须使用 `issue_comment_create` 发布公共总结，不能把模型文本伪装成评论；独立图对话的文本回复不是 Issue 公共评论。
- Issue 委派是异步交接：提示词要求发布结构化 @ 并确认工具回执后结束当前 Run，不轮询等待对方、不二次回写本轮委派评论；需要继续时在委派正文明确要求结果评论通过 `mentioned_agent_ids` @回本 Agent，激活新 Run，不机械相互唤醒。这是提示词策略而非强制取消，不改变正常评论修正、允许的间接循环或 Goal 完成门禁。

修改 mention/dispatcher/ToolService 时必须覆盖并行、循环、self-mention、忙碌退避、幂等和急停。

## 分层测试：按改动范围选择命令

前后端可以独立测完自己，不要因为改了任一层就跑浏览器验收。浏览器测试只在改动
跨层契约（HTTP 路由、SSE 事件、交互/布局）或发布前执行。

| 改动范围 | 命令 | 典型耗时 |
| --- | --- | --- |
| 只改 server | `pnpm test:server` | 约 9s（测试缓存命中时更快） |
| 只改 node | `pnpm test:node` | 约 45s（gitread 的 fsnotify 用例占大头） |
| 只改 web | `pnpm test:web` | 约 15s |
| 只改 protocol | `pnpm test:protocol` | 约 1s |
| 只改 desktop | `pnpm test:desktop` | 约 3s |
| 后端全部（server+node+protocol+desktop） | `pnpm test:backend` | 约 1m |
| 前后端全部（不含浏览器） | `pnpm test:all` | 约 1m15s |
| 真实浏览器冒烟：登录/路由/核心交互/桌面设置 | `pnpm test:browser:smoke` | 约 30s |
| 浏览器布局与截图回归（24 个页面尺寸用例） | `pnpm test:browser:layout` | 约 1m35s |
| 发布前完整浏览器验收（35 项） | `pnpm test:browser` | 约 2m20s |

`test:browser*` 需要先按“真实本地验收”一节启动隔离服务并设置
`TEAMVA_LOCAL_STATE_FILE`；它们不调用模型。Playwright 标签：`@smoke`（核心）、
`@layout`（页面尺寸回归）、`@mobile`/`@desktop`（按宽度）、其余为流程用例。

## 完整门禁（提交/发布前）

```bash
pnpm verify:web      # svelte-check + vitest + 生产构建
pnpm verify:backend  # server/node/desktop: go test -race + go vet；protocol 普通测试
shellcheck scripts/e2e/*.sh scripts/phase0/*.sh scripts/release/*.sh
npx --yes @redocly/cli@2.2.2 lint protocol/openapi/teamva.yaml
git diff --check
```

`go test -race` 可以命中测试缓存，强制重跑时使用 `-count=1`；默认包级并行（实测 server 约 96s、
node 约 43s），不再使用 `-p=1`。若某台机器资源紧张导致抖动，可临时加回 `-p=1`。

## 真实本地验收

先启动隔离的 `server/cmd/local`，提供 `TEAMVA_LOCAL_STATE_FILE`；浏览器/API 脚本不需要 nonce：

- `scripts/e2e/local-api-smoke.sh`：Project/Resource/Issue/Memory/Policy/Schedule/Audit；
- `scripts/e2e/local-browser-smoke.sh`：真实 Chrome、HttpOnly Session、桌面与移动宽度；
- `pnpm test:browser`：真实 Chrome 全页面、表单、拖动、错误恢复、记忆恢复和截图回归；
- `scripts/e2e/local-runtime-smoke.sh`：真实 Codex/OpenCode、工具评论和计划任务。

真实 Provider 测试可指定：

```bash
export TEAMVA_LOCAL_PROVIDER=codex   # 或 opencode
export TEAMVA_LOCAL_MODEL="<probe 返回的模型，可省略>"
./scripts/e2e/local-runtime-smoke.sh
```

Provider 测试会产生真实调用，只在授权账号、临时数据目录和明确测试 Resource 上执行。完成后检查没有
遗留 Codex/OpenCode 子进程。

## 桌面发布制品

```bash
export TEAMVA_RELEASE_VERSION="$(tr -d '\r\n' < VERSION)"
TEAMVA_VERSION="$TEAMVA_RELEASE_VERSION" \
  ./scripts/release/build-desktop.sh
```

脚本生成 macOS arm64 与 x64 各自的 ZIP 与 DMG（应用使用 ad-hoc 签名以便启动）、`VERSION` 和 `SHA256SUMS`。Linux 用
`TEAMVA_VERSION="$VERSION" ./scripts/release/build-linux.sh <amd64|arm64> <输出目录>` 生成 AppImage，脚本只在
对应架构的 Linux 构建机原生运行。Windows 用
`TEAMVA_VERSION="$VERSION" ./scripts/release/build-windows.sh amd64 <输出目录>` 生成含内置运行时的便携 ZIP。DMG 挂载后是「拖进 Applications」的安装窗口：
背景图带箭头，`解除拦截.txt` / `Unblock.txt` 放在图标下方，说明首次打开被 Gatekeeper 拦截时执行的
`xattr` 命令。图标位置和窗口尺寸来自 `desktop/build/dmg/DS_Store` 模板，不需要构建机授予
「自动化 → 访达」权限，因此可在 CI 和无 GUI 会话中稳定复现。模板只在条目名或坐标变化时才需要重建，
方法见 `docs/DESKTOP_ARCHITECTURE.md`。

当前 macOS 制品使用 ad-hoc 签名、未使用 Developer ID 签名、未公证；Windows/Linux 制品未签名。更新只下载并验证 SHA-256，用户退出后手工替换应用。发布前必须真实检查
`/downloads/desktop/*`，缺失文件应返回 404，不能被 SPA fallback 伪装成 200 HTML。

`Release` workflow（推送 v* 标签或手动触发）构建 macOS arm64/x64 DMG 与 ZIP、Windows x64 便携包与
Linux x86_64/arm64 AppImage。它先创建草稿 Release，全部平台和合并后的 `sha256.txt` 上传成功才自动公开；
任一失败保持草稿，避免半套产物出现在下载页。Windows 版暂不提供 Git 变更面板（Git 安全沙箱仅覆盖
macOS/Linux），也不实现在线备份/恢复。

## 完整应用验收清单

从 Finder 启动真实 `.app` 后逐项验证：

1. 单实例、bootstrap、窗口/菜单、打开数据目录；
2. Project 创建/修改和 singleton Computer 引导；
3. 本地目录/URL Resource 注册、验证、修改、删除；
4. Codex/OpenCode 诊断，Agent 配置，本机 Skill 安装、打开、卸载和绑定；
5. 任务、评论、每次 @ 的独立会话、修订、单 Agent、多 Agent、`A → B → A` 循环；
6. Agent 侧栏、流式输出、上下文整理、工具、原生 Approval、停止和暂停/恢复；
7. Memory 版本、归档/恢复；
8. Schedule 预览、CRUD、run-now 和历史；
9. 活动记录、设置、主题和九种语言；
10. 日志轮转、诊断脱敏、更新检查；
11. 正常退出、重开、SIGKILL 恢复和子进程清理；
12. 1280×820、最小窗口和移动宽度回归。

最终报告必须区分自动化、真实浏览器、真实 Wails、真实 Codex/OpenCode 和公开制品证据。

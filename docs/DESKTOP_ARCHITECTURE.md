# Teamva 本地服务、浏览器与桌面架构

> 状态：当前主分支实现基线。没有需要兼容的已发布版本，浏览器与 Wails 共用同一套本地业务实现。

## 1. 产品边界

Teamva 是本地优先的多 Coding Agent 工作台，支持本机普通浏览器与 macOS Wails 窗口：

- Wails v2.13.x 提供原生窗口、菜单、单实例、日志、诊断和更新下载；
- Svelte 5 前端负责全部业务界面；
- Go 服务可由 `server/cmd/local` 独立启动，也可嵌入 Wails；默认只监听 loopback，CLI 启动脚本默认 41720，Wails 首次自动分配并持久化，局域网启用后共用同一端口号；
- SQLite 保存项目、资源、Agent、Skills、任务、评论、运行、记忆、计划任务和活动记录；
- Codex 与 OpenCode 直接在用户电脑运行，并复用各自已有的登录状态；
- 项目文件、Skill 制品、运行目录和编程工具凭据不离开本机。

当前不包含账号注册、设备绑定、云同步、多人共享数据库或在线安装器。Windows 仅提供未签名便携包，暂不含 Git 安全沙箱。远程访问是同一本机工作台的可选密码保护入口，不是独立云服务或多用户系统。

## 2. 核心约束

1. **HTTP 是前端唯一业务边界。** Svelte 不直接调用 Go 领域方法；Wails binding 只处理窗口能力。
2. **单端口，默认不暴露到局域网。** 本机 loopback 入口始终保留且免访问密码；「设置 → 远程访问」配置密码并显式开启后，
   在具体 LAN IPv4 地址上增加同端口号的独立 socket，不绑定 wildcard，不增加第二个端口。开关和 bcrypt 哈希持久化并即时生效，不重启 Wails；端口仅在启动时选择。
   局域网地址访问通过自有登录页输入密码，登录后使用端口专属 HttpOnly Cookie；写操作仍校验严格同源与 CSRF。本机浏览器使用 localhost / 127.0.0.1 免访问密码。
   登录时可显式勾选「信任此设备」，服务端无到期时间，跨重启、停用再开启及同 IP 换端口保留；随机凭据摘要与网络设置共同持久化，仅实际修改密码时原子撤销信任。HttpOnly Cookie 每次访问续期，受浏览器自身存储上限约束。
   关闭/改密码断开远程 SSE 与连接，本机窗口不受影响；远程不可修改此安全设置或使用 Wails bootstrap。
   无密码不监听外部；局域网绑定失败仍允许本机窗口打开修正，本机端口冲突则明确拒绝启动；明文 HTTP 仅适用于可信 LAN/VPN。
3. **浏览器使用同源引导。** `POST /v1/auth/local-session` 建立或恢复 HttpOnly Session；严格校验 Origin、JSON 类型与 Fetch Metadata。只有 Wails 私有桥使用 30 秒有效的一次性 nonce。
4. **命令先持久化再执行。** Run、资源探测、停止和确认操作都经过 durable command/outbox。
5. **Agent 配置与会话分离。** Agent 是可复用配置；每次任务评论中的结构化 `@Agent` 创建独立会话。
6. **Skills 必须先安装。** Agent 只能绑定当前电脑上已完整安装并通过摘要校验的 Skill 版本。
7. **权限由编程工具决定。** Codex/OpenCode 允许的操作直接执行；只有工具主动询问时才展示确认按钮。
8. **旧库和损坏库不自动处理。** 基线或完整性校验失败时保留原始文件并停止启动，不升级、回填或清库。

## 3. 进程拓扑

```text
Teamva.app
├─ Wails native shell
│  ├─ native decorated window
│  ├─ application menu
│  ├─ single-instance lock
│  └─ update / diagnostics
├─ embedded Go service
│  ├─ loopback HTTP API
│  ├─ SQLite repositories
│  ├─ scheduler
│  ├─ durable command dispatcher
│  └─ audit and lifecycle services
└─ embedded runtime controller
   ├─ Codex app-server adapters
   ├─ OpenCode adapters
   ├─ runtime SQLite
   ├─ ToolBroker
   └─ stable per-project-and-authorization-scope workspaces + per-session Provider config
```

Codex/OpenCode 子进程按会话需要启动，空闲后回收；应用退出时统一停止，不遗留后台进程。
独立浏览器模式不启动 Wails native shell，其余服务、SQLite、调度器和 Runtime 完全相同；也可直接访问已启动 Wails 的 loopback origin。

## 4. 启动与本地会话

共同启动顺序为：

1. 解析应用数据目录并初始化脱敏轮转日志；
2. 只读检查已有 SQLite 的当前基线与完整性，仅对新空库初始化结构；
3. 启动 scheduler、runtime controller 和随机 loopback HTTP 服务；
4. 服务提供 SPA、API 和 SSE，并校验精确 Host，拒绝 DNS 重绑定和页面嵌入；
5. 普通浏览器打开日志中的 loopback origin，通过同源 POST 建立会话；
6. Wails WebView 保持在 `wails:` origin，由私有 HTTP bridge 用 nonce 换取会话；Cookie 留在 bridge 的内存 cookie jar，不向 JavaScript 暴露；
7. 两种客户端都只通过同源 `/v1/*` 请求业务数据，最终进入 `/workbench`。

bootstrap nonce 不进入 URL 历史、localStorage、数据库或普通日志。前端刷新后继续使用 HttpOnly Session，
不需要用户登录。

## 5. 本地数据

默认数据目录位于操作系统应用数据目录，主要内容为：

```text
app.db                         业务与控制状态
runtime/runtime.db             命令幂等、事件、native session 和运行状态
runtime/resource-authorizations/ 私有持久 Resource 批准身份锚点（0700/0600，不自动回填）
runtime/agents/                Agent 私有运行配置与 Skills 副本
runtime/workspaces/projects/   按项目及完整授权 scope 隔离的稳定 CWD 与 AGENTS.md
runtime/skills/                当前电脑已安装的完整 Skill 制品
logs/                          桌面壳的脱敏轮转日志
```

两份数据库都使用 WAL。启动检查 schema 与完整性，退出前 checkpoint。诊断导出只包含版本、平台、
服务状态、数据库文件元数据和脱敏日志，不包含数据库正文、WAL、bootstrap 状态或编程工具凭据。

## 6. 领域与页面

左侧导航分为两组：

- 项目内容：概览、任务、分支对话、资源、项目记忆、定时任务、变更；
- 本机：Agents、技能 & MCP。技能与 MCP 在同页通过 Tab 切换；编程 CLI 配置保留在设置中，不再列入本机导航。

设置和活动记录位于底部。项目内容只展示当前项目；本机内容跨项目复用。

### 6.1 概览

概览显示项目名称、备注、任务数、计划任务、资源和项目记忆。标题与备注支持原位编辑，相关条目可直接
跳转到对应页面。

### 6.2 任务与会话

任务只有 `open`、`resolved`、`closed` 三种状态。详情按任务描述与评论组成单一竖向内容流，评论之间
只用分隔线区分；用户和 Agent 头像位于各自内容行内。

用户发送带结构化 mention 的评论后：

1. 每个被提及 Agent 立即得到一条 `working` 评论占位；
2. 每个 mention 创建独立 `IssueAgentInstance` 和 Run；
3. 用户可立即打开会话侧栏查看流式输出、工具调用和上下文整理；
4. Agent 发布评论后替换自己的 working 占位；
5. 侧栏追问和“修正评论”只续接当前实例，不创建新的任务会话；
6. 内部控制消息带 `internal` 标记，只在会话侧栏显示，不出现在公共任务评论里。

同一个 Agent 在同一个任务中被再次 @ 时，必须创建新会话。多个 Agent 可并行工作，也可通过结构化
mention 互相委派；禁止直接 @ 自己，允许 `A → B → A` 间接循环。

### 6.3 Agent 与 Skills

Agent 编辑页采用左右结构：左边是已有 Agent，右边原位创建或编辑。配置包含软件、模型、备注、系统
提示词和已绑定 Skills。模型留空时使用编程工具的默认模型。
基础配置、不可变修订与绑定在同一事务中保存；界面在修订加载时禁止编辑/保存，切换 Agent 时保护草稿，后台刷新不会偷偷改变提交所用的版本号。

本机 Skills 页面左右展示“已安装”和“商店”。安装过程复制完整仓库制品并校验固定摘要；安装按钮在完成
前保持 loading。已安装条目可以重新安装、打开目录或卸载。Agent 页面只展示本机已安装的 Skills，支持
添加、移除和排序，并提供前往本机 Skills 的入口。

### 6.4 资源

资源支持代码目录、资料目录和 URL。代码目录可按操作系统能力打开：

- macOS：Finder、终端、VS Code、Zed；
- Windows：资源管理器、终端和已安装编辑器；
- Linux：探测到的文件管理器、终端和已安装编辑器。

终端和编辑器使用资源目录作为工作目录。按钮只在对应程序可用时出现。

所有普通 Run（包括任务、mention 和计划任务）都在当前项目及完整授权 scope 的稳定 Workspace 中启动，不在 Issue 或
Schedule 上选择单个 Git 仓库。Runtime 将项目内已显式批准的 ready Git 目录投影成该 scope 专属 `AGENTS.md` 仓库地图；不同 scope 不能覆盖活动 Run 的地图，一个
Run 可以处理多个仓库。完整仓库根可写，祖先仓库中的部分 Resource 仅授权其精确根，互不相交的根不会
被合并扩大。

### 6.5 计划任务

计划任务引用一个已启用 Agent，支持 cron 或单次时间、时区、未来触发预览、立即运行、暂停、恢复、
编辑和运行记录。同一个计划任务不并发执行；错过触发时间时跳过本次运行。

### 6.6 图对话与记忆

图节点固定 AgentRevision 和分叉上下文，发送的消息、回复占位与 Run 原子落库；运行中提供停止按钮，
不允许删除或改写仍在执行的分支。桌面使用可平移、缩放和拖动的画布，窄屏使用同一批节点的纵向列表与图选择器。

从已发布 Agent 消息分叉时，原子新增冻结父副本和可写新分支；原节点作为另一个可写子分支，
完整保留分叉点前后的全部消息、Run 关联与原有继承上下文，不截断或软删尾部。
冻结父副本仅复制截至分叉点的消息；新分支本地消息为空，继承截至分叉点的已发布上下文。
两条子分支及冻结父副本均继承源节点的持久化宽高，既有子树整体平移并避开其他节点。
已冻结源节点仍可再次分叉，此时只解锁该源节点，不改写旧后代的父链、上下文、锁定状态或运行。
详细上下文、并发边界和历史软删风险见 [图对话分叉语义](CONVERSATION_FORKS.md)。

记忆页面分为当前记忆与已归档记忆。归档内容不进入任务上下文；用户可查看历史并恢复。编辑版本固定在加载时，
未保存草稿不会被后台刷新覆盖，切换记忆/归档视图前会确认。

## 7. 编程工具与权限

“编程工具”页面列出当前支持的 Codex 和 OpenCode，显示连接状态、版本、最近探测时间和诊断按钮。
诊断按真实顺序检查可执行文件、版本、后台协议、登录状态和模型列表，并保留逐步日志供用户定位问题。
OpenCode 默认使用应用内置运行时；设置页「编程 CLI」可为任意编程工具保存外部可执行文件路径，
保存后立即热更新（回收空闲实例，不打断进行中的 Run），留空恢复自动解析。

适配器不预判 `ls`、读文件或写文件是否需要确认。它们把 Codex/OpenCode 的原生确认请求转换为统一
Approval；没有原生请求就不创建 Approval。用户的允许/拒绝决定再回传给当前编程工具。

## 8. Runtime、流式事件与上下文整理

Issue 每个世代固定 AgentRevision 和 native session。图会话显式重放上下文，每次 Run 使用新的 native session。
Run 在同一会话内串行，不同会话可以并行。
Runtime 统一产生用户消息、助手增量、工具调用、确认请求、上下文整理和终态事件。

每个 Run 最多从 64 个 Git Resource 发现 100 个仓库，并按仓库保存 start/end 两阶段快照（最多 200 行）。
Changes UI 按仓库展示这些快照；无法可靠关联仓库的 Provider 变更报告单独展示。捕获超时、保存失败或
崩溃恢复产生带 warning 的截断元数据，不把未知状态显示成干净工作区。

会话侧栏通过 Run 事件流实时追加内容；普通列表使用可见时轮询，窗口不可见时暂停。UI 只识别当前协议：

- Codex：`contextCompaction` started/completed；
- OpenCode：`session.compaction.started/ended/failed` 标识压缩过程与结果；压缩摘要不作为普通助手正文；
- 当前 Run 事件类型，不接受旧事件别名。

侧栏把连续文本增量合并为同一消息，不按 chunk 单独换行；拖动宽度时只更新 CSS 变量，拖动结束后才
持久化宽度，避免高频重排和写存储。

## 9. 原生窗口与界面语言

Wails 使用带标题的系统装饰窗口。窗口圆角、阴影、缩放和红绿灯由当前操作系统管理，不写死圆角半径。
macOS 使用 hidden-inset 标题栏扩展内容区，同时为红绿灯保留拖动区和安全间距。

首次启动根据操作系统语言选择简体中文、繁体中文、英语、日语、德语、法语、西班牙语、葡萄牙语或
俄语；用户主动选择后保存本机偏好，并优先使用该选择。浅色、深色或跟随系统与七套全局配色都是用户级本机偏好，
配色默认使用 `default`，支持独立背景色、强调色及控件色并即时自动保存。项目不再定制配色，切换项目不改变完整语义 token 集；
Project 创建/编辑及公开 API 不含 `theme`，物理 SQLite 旧列仅保留固定遗留占位的写入约束，不改基线或迁移。详见 [全局外观与配色](APPEARANCE.md)。

## 10. 构建与验收

浏览器启动：`pnpm build && pnpm start:browser`。Wails 使用完全相同的同源 Web 构建：

```bash
pnpm --filter @teamva/web build
./scripts/desktop/stage-frontend.sh
(cd desktop && go run github.com/wailsapp/wails/v2/cmd/wails@v2.13.0 build)
```

日常门禁：

```bash
pnpm --filter @teamva/web check
pnpm --filter @teamva/web test
(cd server && go test ./... && go vet ./...)
(cd node && go test ./... && go vet ./...)
(cd protocol && go test ./...)
(cd desktop && go test ./... && go vet ./...)
git diff --check
```

真实验收按边界拆分：

- `local-api-smoke.sh`：项目、资源、Agent、任务、评论、记忆、计划任务和活动记录；
- `local-browser-smoke.sh`：本地 Session、桌面与窄屏布局；
- `pnpm test:browser`：真实 Chrome 全页面、交互、失败恢复和截图；需要提供隔离服务的 runtime 状态路径；
- `local-runtime-smoke.sh`：真实 Codex/OpenCode、Agent Skills、任务评论和计划任务；
- `local-goal-runtime-smoke.sh`：真实 Codex/OpenCode 的持续目标、独立审核、自动续跑、Token 预算与最终总结；
- 最终 `.app`：系统窗口、导航、弹层、侧栏拖动、资源打开器和退出清理。

## 11. 开发阶段变更原则

主分支只维护当前协议、当前 schema 和当前界面。不新增旧字段读取、旧事件别名、双读双写、旧配置转换
或旧页面入口。结构变化直接修改当前基线与测试；文档只写现在可运行、可验证的产品行为。

## 12. DMG 安装盘布局模板

应用与安装盘使用用户确认的 Silken 图标。`desktop/build/appicon.png` 为 1024px 原生图标；发布脚本在 Wails 构建后复制 `appicon.icns` 到 App Resources，保留 Apple 原生渲染与小尺寸编码。`icon_installer.icns` 与其一致，安装背景标题使用同源标记；浏览器与官网复用 `assets/brand/icon.png`。详见 [品牌素材](../assets/brand/README.md)。

`scripts/release/build-dmg.sh` 把 `.app`、`/Applications` 链接、`解除拦截.txt`、`Unblock.txt` 和背景
TIFF 装进一个 HFS+ 卷，`.DS_Store` 直接取自 `desktop/build/dmg/DS_Store`。

**为什么不通过 Finder 写 `.DS_Store`：** 用 AppleScript 让访达设置图标坐标，要求运行构建的进程在
「系统设置 → 隐私与安全性 → 自动化」里获得控制「访达」的权限。CI、远程 shell 和后台 Agent 拿不到
这个授权，且 macOS 的平台行为是直接超时（`-1712`）而不报权限错误，构建会静默退化成默认排布。
提交一份预生成的 `.DS_Store` 把布局变成构建产物的一部分，脚本就不再需要任何交互式授权。

模板中的背景图引用是 Finder 别名，按**卷名 + 相对路径**解析，因此：

- 卷名必须是 `Teamva`（脚本在卷名不同时打印警告）；
- 背景文件必须叫 `.background.tiff`，由 `sips` + `tiffutil` 在构建时从两张 PNG 合成；
- 条目名必须与脚本写入的名字完全一致。

### 重建模板

条目名或坐标变化时才需要重建。任选一台有 GUI 的 macOS 机器：

```bash
python3 -m venv /tmp/dsbuild && /tmp/dsbuild/bin/pip install dmgbuild

mkdir -p /tmp/tpl/source && cd /tmp/tpl
mkdir -p source/Teamva.app && ln -s /Applications source/Applications
printf 'zh\n' > "source/解除拦截.txt" && printf 'en\n' > source/Unblock.txt
cp <repo>/desktop/build/dmg_background.png    source/bg.png
cp <repo>/desktop/build/dmg_background@2x.png source/bg@2x.png
cp <repo>/desktop/build/icon_installer.icns   source/icon.icns

cat > settings.py <<'PY'
volume_name = 'Teamva'
format = 'UDZO'
files = ['/tmp/tpl/source/Teamva.app', '/tmp/tpl/source/解除拦截.txt', '/tmp/tpl/source/Unblock.txt']
symlinks = {'Applications': '/Applications'}
background = '/tmp/tpl/source/bg.png'
icon = '/tmp/tpl/source/icon.icns'
icon_locations = {
    'Teamva.app': (150, 110),
    'Applications': (470, 110),
    '解除拦截.txt': (170, 385),
    'Unblock.txt': (450, 385),
}
window_rect = ((100, 100), (620, 560))
default_view = 'icon-view'
icon_size = 96
text_size = 13
PY

/tmp/dsbuild/bin/dmgbuild -s settings.py Teamva /tmp/tpl/tpl.dmg
hdiutil attach /tmp/tpl/tpl.dmg -nobrowse -noautoopen -readonly -mountpoint /tmp/tpl/mnt -quiet
cp /tmp/tpl/mnt/.DS_Store <repo>/desktop/build/dmg/DS_Store
hdiutil detach /tmp/tpl/mnt -quiet
```

坐标与 `desktop/build/dmg_background.png` 上的图形一一对应，改背景排版必须同时改
`icon_locations`。挂载生成的 DMG 用真实访达窗口核对后再提交。

# Coding Agent Provider API v1

## 目标与边界

采用**构建时自动发现**，不是运行时加载 Go plugin 或扫描任意可执行文件。
新增 Coding Agent/CLI 时，只在 `node/provider/<provider-id>/` 增加接入实现；常规构建、启动和
后端测试会生成注册代码，不需要编辑 Controller、Runtime、Manager、HTTP 路由、Provider 枚举或前端工具列表。
新增目录后仍需重新构建并重启使用新制品，不能让已经运行的旧二进制自动获得新代码。

目前实现的是 Codex、OpenCode 和 Claude Code。zcode、pi 等尚未实现；框架支持为它们编写
独立接入目录，并不意味着仅填写一个 CLI 名称就能推导出它的登录、会话和输出协议。
纯文本 CLI 可以只实现最小执行能力；没有的恢复、审批、图片或只读能力必须如实报告。

Claude Code 接入的受控工具范围、认证投影与验收边界见 [CLAUDE_PROVIDER.md](CLAUDE_PROVIDER.md)。

## 目录

```text
node/provider/
  api.go                   # Probe、执行、恢复、steer、停止、审批、会话校验、关闭
  definition.go            # 版本、元数据、工厂、实例配置、能力声明
  output.go                # 最终正文投影接口
  changes.go               # 显式文件变更报告接口
  unsupported.go           # 可选操作的显式不支持实现
  workspace.go             # 路径规范化、凭据链接等公共工具
  view.ts                  # 无 UI/DOM 依赖的浏览器投影接口
  process/                 # 公共子进程组、watchdog、跨平台退出管理
  catalog/
    catalog.go             # 通用查询与投影分发
    providers_generated.go # 自动生成；禁止手工注册新 Provider
  codex/
    provider.json          # 自描述元数据、诊断步骤和能力
    provider.go            # 工厂、Codex 配置、凭据位置、终态/变更解码
    codex.go               # app-server 通信和执行生命周期
    output.go              # Codex 正文与最终答案解析
    view.ts                # Codex item/delta/工具/思考的展示投影
    *_test.go
  opencode/
    provider.json          # 自描述元数据；id=opencode，默认命令 opencode（内置运行时）
    provider.go            # 实例配置、用户 provider 段合并、凭据链接与注入
    opencode.go            # serve、/api/* 协议、SSE 与执行生命周期
    output.go              # 助手正文投影（排除推理与工具文本）
    view.ts                # 文本/推理/工具事件的展示投影
    *_test.go
node/cmd/provider-gen/     # 目录发现与确定性注册生成器
```

Provider 的配置格式、认证文件位置、原生协议和原生事件类型留在自身目录。
实例串行、跨实例并行、项目授权、SQLite/outbox、幂等、取消、工具鉴权、事件预算与 Git 路径校验留在核心。
不要把 Issue/Store/Svelte 页面反向接入 Provider；公共 Go API 只依赖标准库与 `protocol/node`。

OpenCode 是唯一的 OpenCode 接入（v2 `/api/*` 协议，不再支持 v1）：`opencode serve` 起前台服务，HTTP Basic 用户名固定为 `opencode`；2.0.x 读取 `OPENCODE_PASSWORD` 并回退 `OPENCODE_SERVER_PASSWORD`，更新版本只认后者，适配器两者都设置以同时兼容内置与外部 v2 二进制。会话创建携带 `model`、`location.directory` 与会话级权限 `{action, resource, effect}`（最后匹配生效），正文/推理/工具/用量/审批事件通过 `GET /api/event` 的 SSE 下发（事件类型在 data JSON 的 `type` 字段）。应用内置该运行时：`scripts/vendor/opencode.sh` 按平台从 npm 拉取固定版本到 `vendor/opencode/`（git 忽略、不做全局安装），运行时的可执行解析顺序为设置页自定义路径 → `TEAMVA_<ID>_PATH` → App 资源 / 仓库 vendor / 数据目录内置二进制 → PATH；桌面发布脚本把 darwin-arm64 与 darwin-x64 两个载荷打进 App Resources。

OpenCode 实例的隔离分三处：1) 实例配置写在 `configDir/opencode/opencode.json`，通过 `XDG_CONFIG_HOME` 加载，并合并用户全局配置里的 `provider` 段，使本机自定义 Provider/模型在实例内可用；2) `XDG_DATA_HOME` 指向 `InstanceOptions.DataDir`，会话与数据库不与用户自己的 opencode 或其他实例争用（共享同一个大库会让启动偶发卡住数分钟）；3) 托管实例使用受控环境：剥离继承的 `OPENCODE_*`/`XDG_*` 变量，并设置 `OPENCODE_DISABLE_EXTERNAL_SKILLS` / `OPENCODE_DISABLE_CLAUDE_CODE_SKILLS`。凭据从 `~/.config/opencode/auth.json` 或 `~/.local/share/opencode/auth.json` 链接到 `DataDir/opencode/auth.json`；实例数据根初始没有 v2 凭据库，链接的 `auth.json` 不会被自动迁移，实例配置生成时把其中 `type=api` 的 Key 注入对应 `provider.<id>.options.apiKey`（已有 Key 不覆盖，oauth 凭据不注入）。

### 配置、持久数据与缓存

`InstanceOptions` 明确分层：

- `ConfigDir`：`runtimeRoot/instances/<scope digest>`，可重建配置。仅已持久化会话、无活动实例且带宿主 `config_only` 标记的目录参与默认 30 天／128 份／512 MiB 配置回收。
- `DataDir`：`runtimeRoot/provider-data/<scope digest>`，不可由配置回收删除的原生会话与数据库。摘要同时绑定 Provider、instance、generation、Project 与 workspace 授权 scope；同 key 重建继续使用相同目录，任一边界变化必须隔离。不扫描该目录计算配置数量或容量，历史会话数量不会耗尽配置扫描上限。
- `CacheDir`：可跨实例复用的 Provider 缓存，不得存放唯一的原生会话数据。

### 可选的模型 API 配置

Provider 可通过 `Definition.ModelSetup` 声明 `Settings` / `Save` / `Test` 能力。Controller 与 Dispatcher 只按发现的 Provider ID 转发，不在核心注册模型供应商枚举。敏感输入经直接 in-process facade 传递，不进入 Runtime durable command inbox；HTTP 层只开放本机已认证、同源 CSRF 入口。

当前 OpenCode 接入提供常见供应商及 OpenAI 兼容配置：独立 `teamva-model-providers.json` 保存在 OpenCode 用户配置目录，0600 原子写入，不覆盖原有 JSON/JSONC 或认证文件；只对外返回 `key_configured`。生成实例配置时，托管模型映射到 `teamva-<preset>/<model>`，使用 V2 `providers` / `package` / `settings` 格式。探测客户端会重新加载这些定义，空闲实例回收而活动 Run 不被配置保存打断。

模型供应商弹窗统一位于 Agent 页，对普通 Agent 和系统 Agent 都可用，不放在引导或助手侧栏。供应商 `Save` 保存的是 Provider 私有配置，返回的模型引用只选入当前 Agent 草稿；Agent 的名称、备注、Provider、模型、提示词和 Skills 仍通过公共 `PATCH /v1/agents/{id}` 显式保存，不把供应商保存等同于 Agent 保存。

`Test` 仅在用户点击后发送一次固定 `Hello`，不调用 Coding Agent 工具，也不自动保存或重试；分别使用 OpenAI Chat Completions、Anthropic Messages、Google GenerateContent 协议。要求真实回复，拒绝重定向、HTML、错误 JSON 或过大响应，不回传上游正文/错误/认证头。自定义端点需要 HTTPS（本机 loopback HTTP 除外），禁止 URL 内认证、query 与 fragment；改变端点不能隐式复用旧密钥。

Codex 的 `CODEX_HOME` 指向 `DataDir`，因为原生 `sessions/`、SQLite 等也写在该 home 下；仅 `config.toml`、`AGENTS.md`、`skills` 用同 key 的安全符号链接指向 `ConfigDir`，配置刷新/重建仍即时生效。既有同名非托管条目拒绝覆盖，凭据只在原生 home 建立缺失的登录链接，不复制或覆盖用户凭据。OpenCode 保持 `XDG_CONFIG_HOME=ConfigDir`、`XDG_DATA_HOME=DataDir`。

直接调用 factory 且未提供 `DataDir` 时，Codex 仍使用调用方的 `ConfigDir`，OpenCode 使用 `ConfigDir/data`；托管 Manager 始终提供独立 `DataDir`。不自动移动、迁移、复制或清理既有原生数据；缺少新分层标记的既有目录按不可自动回收处理，防御性的 `data` 保留也不参与配置额度。这不是旧数据库兼容、备份或恢复机制。

## 新增 Provider

### 1. 新建目录和 manifest

例如 `node/provider/mycli/provider.json`：

```json
{
  "api_version": 1,
  "id": "mycli",
  "display_name": "My Coding CLI",
  "command": "mycli",
  "description": "本机 Coding CLI 接入",
  "features": ["streaming"],
  "diagnostic_steps": [
    {"id": "executable", "label": "CLI 可执行文件"},
    {"id": "authentication", "label": "本机登录状态"}
  ]
}
```

- `id` 必须与目录名一致，匹配 `^[a-z][a-z0-9_-]{0,63}$`。
- 不支持的 API 版本、未知 manifest 字段、缺失工厂文件会让生成失败；不是悄悄忽略。
- `command` 是可执行文件名称，不是经 shell 执行的命令字符串。参数组装属于接入实现。
- `description_key` 是可选的既有 UI 翻译键；不提供时直接显示 `description`，新增 Provider 不要求修改核心语言文件。
- 新增元数据与诊断步骤自动进入 Go catalog 和前端工具列表；前端通过 Vite glob 读取同一份 manifest。
- `no_tools` 只声明严格禁止所有原生及宿主工具的能力，不等同于 `read_only`、隐藏工具目录或提示词承诺。分工草案整理依赖此能力；不支持时在模型启动前拒绝，不能自动降级或替换 Provider。详见 `ISSUE_WORK_ITEMS.md`。

### 2. 实现工厂与执行接口

`provider.go` 导出以下精确签名：

```go
func Definition(metadata provider.Metadata) provider.Definition
```

返回 `Metadata`、`NewProbe`、`NewInstance`、`Configure`：

| 接口 | 职责 |
| --- | --- |
| `NewProbe(executable)` | 创建仅用于发现/诊断的客户端；CLI 缺失应由 Probe 报 unavailable，而非构造时运行模型 |
| `NewInstance(InstanceOptions)` | 为单独实例创建客户端、私有配置和必要的凭据链接；绝不能返回 Probe 单例 |
| `Configure(configDir, InstanceConfig)` | 刷新可复用实例的配置，不扩大当前请求的工作区授权 |
| `Adapter.Probe` | 实际版本、可用性、登录、模型和运行能力；不能把“进程能启动”当作已认证 |
| `Adapter.Start/Resume` | 消费 RunRequest，同步向 Sink 发送真实事件，结束时返回原生会话身份或错误 |
| `Steer/Interrupt/DecideApproval` | 只操作指定 Run 的实例，不发送到全局 Probe 客户端 |
| `VerifySession` | 只校验本实例/授权范围下的原生会话 |
| `Close` | 清理本实例所有子进程，须支持重复关闭和退出并发 |
| 可选 `DiagnosticAdapter.Diagnose` | 提供真实逐项诊断；缺省使用 Probe 的通用诊断 |

可嵌入 `provider.UnsupportedOperations` 获得显式返回 `ErrUnsupported` 的可选操作，
但仍需实现 `Name`、`Probe`、`Start`。不要把尚未实现的恢复/审批写成返回 nil 的空函数。

完整、可编译的最小扩展示例在
`node/cmd/provider-gen/testdata/fixturecli/`。它仅用于测试，不会被产品构建发现，也不执行模型。

### 3. 声明真实能力

`features` 使用 `streaming`、`resume`、`interrupt`、`approval`、`steer`、`images`、`read_only`、
`skills`、`computer_use`、`file_bridge`。

核心在执行前检查图片、只读、Skills、Computer Use 和 Issue 文件工具桥需求；缺失时返回
`capability_unsupported`，不降级为更宽权限。未声明 resume 的 CLI 不会被自动要求恢复旧会话。
steer/审批同样检查声明；不具备原生 interrupt 的 CLI 使用 Run context 取消，`Start` 必须尊重取消并退出。
其他可选操作必须在实现中正确拒绝不支持的请求；能力声明不能代替真实的权限隔离和子进程取消。

`read_only` 包括工具限制和目录写权限，不是仅在 prompt 中告诉模型“不要修改文件”。
如果上游不能落实，不要声明该能力，该 CLI 也就不能承接需要只读保证的独立目标审核。

### 4. 输出标准事件，或在自身目录实现原生投影

推荐新 CLI 直接发送以下 Provider API 语义事件，不需要新增前端模块：

| 事件 | 主要 payload |
| --- | --- |
| `output.delta` | `message_id`、`text`；增量只包含助手正文 |
| `output.snapshot` | `id`、`text`、可选 `final: true`；完整替换该消息，不能用它重复追加 |
| `reasoning.snapshot` | `id`、`text`、`status`、可选 `duration_ms`；仅显示上游实际公开的思考 |
| `provider.tool` | `id`、`tool`、`input`、`output/error`、`status`、可选 `exit_code`、`duration_ms` |
| `provider.event` | 公共扩展事件；诊断错误 `kind: error`、安全 `message/code/status_code`；原生正在重试时显式 `willRetry: true`，不得另造浏览器未注册的私有事件类型 |
| `files.changed` / `workspace.diff` | `id`、`paths/changes`、显式 `patch/diff`；仍经过核心路径检查与大小限制 |
| `usage.updated` | 使用现有 Embedded Runtime 的 usage 结构，不能将未知用量当作零消耗证明 |
| `context.compaction` | 稳定 `id` 与 `status: running/completed/failed`；运行中断不代表压缩成功 |
| `approval.requested` | 使用现有审批协议的 request_id、action_kind、normalized_action 等字段 |
| `run.finished` | `status: succeeded/failed/cancelled`、可选 `reason`；终态之后不再输出 |

`message_id`/工具 ID 在单 Run 中必须稳定；不同消息不要复用 ID。流式与完成快照必须相符。
无消息 ID 的简单 `output.delta` 仍可用于单段文本 CLI，但多消息交错应使用明确身份。

Codex/OpenCode 保留真实原生事件时，通过可选 `NewOutputProjector`、`TerminalStatus`、`FileChange`
钩子返回统一的正文/终态/变更结构。前端可选 `view.ts` 默认导出 `ProviderView`，在同一目录处理原生
item/part 合并、受预算限制的正文提取和语义展示投影。新模块由 glob 发现，不手工 import 到页面。

Runtime 会给持久化事件附上权威 `provider_id`，只让所属 Provider 的原生解码器处理它，
防止未来 CLI 的同名 payload 字段被误认成 Codex/OpenCode。正文解码器按 Run 独立创建，不能跨会话保存 role/part 状态。
没有 provider_id 的 Provider 输出明确拒绝，不猜测接入方，不恢复旧事件格式。

公开 Issue 总结仍必须经 `issue_comment_create`；`output.snapshot` 不是公共评论。
最小纯文本 CLI 可以用于独立图对话；要参与 Issue 多 Agent 协作，必须真实支持 `file_bridge`
所需的文件读写/执行能力，不能把普通终端回复冒充协作工具调用。

### 5. 构建与校验

```bash
pnpm generate:providers
pnpm check:providers
pnpm test:node
pnpm test:server
pnpm test:web
pnpm build
```

`pnpm build`、`start:browser`、`start:lan`、后端分层测试/门禁、Wails 前端暂存和桌面发布构建均已接入生成。
直接使用 Go/Wails 底层命令时，先执行 `go -C node generate ./provider/catalog`。
`providers_generated.go` 应与接入目录一同纳入源码审查，但不得手工维护。
如果接入选择额外的第三方 Go SDK，仍需按常规更新 `node/go.mod`/`go.sum`；这属于依赖声明，
不需要新增核心接入分支。使用标准库或现有依赖的 CLI 接入可以只新增 Provider 目录。

可执行文件覆盖优先级：设置页保存的 `ProviderPaths[id]`（控制面 app_metadata 持久化） →
`TEAMVA_<大写ID，连字符替换成下划线>_PATH` → App 资源 / 仓库 vendor / 数据目录内置二进制 →
manifest 的 `command`（PATH lookup）。例如新增 pi 后可使用 `TEAMVA_PI_PATH`，无需给核心增加
`PiPath` 字段。本地服务启动时把持久化设置合并进 `embedded.Config.ProviderPaths`；通过设置修改后由
Runtime 立即热更新：替换诊断客户端并回收空闲实例，进行中的 Run 不被打断（结束后自然使用新路径）。
访问令牌不能放入这些参数或 Provider 子进程环境。

## 验收要求

- `TestNewDirectoryRunsWithoutCoreEdits` 在临时源码副本中只增加 fixturecli 目录，重新生成并编译，
  验证发现、两轮独立 Start（没有 resume）、持久化输出/终态、不支持能力拒绝、无原生 interrupt 时的取消和实例回收。
- Native 编解码回归继续覆盖 Codex/OpenCode 的审批、附件、取消、usage、终态、进程退出及配置授权。
- 前后端标准事件测试验证未来 Provider 不依赖原生编解码器；Provider 身份隔离测试防止误解析。
- 新的真实 CLI 接入仍必须用授权账号和临时数据验证；单测、mock CLI 和浏览器协议 fixture 不能替代真实 Provider 证据。

这是受信任的编译期扩展接口，不是运行时沙箱。新 Provider 的代码与应用拥有同等本机权限，必须审查后构建；
不得承诺任意第三方目录都能安全加载，也不提供运行时自动下载或热更新插件。
事件投影必须是有界、无副作用的转换；不得在数据库事务/前端投影中启动进程、访问网络或读取凭据。

## 本轮重构验证（2026-09-14）

- `pnpm test:backend` 在原生解码器身份隔离用例调整后通过；最终 `pnpm verify:backend` 的 server/node/desktop race 与 vet、protocol 测试全部通过。
- 前端最终 35 文件、196 测试通过；Svelte 检查 0 error / 0 warning；生产构建输出到独立临时目录，未覆盖正式服务的 `web/dist`。构建仍有既有大 chunk 提示。
- `pnpm check:providers`、修改过的 Shell 脚本 ShellCheck、浏览器脚本语法和 `git diff --check` 通过。
- OpenAPI 校验有效；保留 health 缺少 4xx、bootstrap 为 303、UserPreferences 未引用三条既有警告。
- 隔离真实 Chrome 的 20 项定向回归通过，包含目录驱动的工具列表、标准事件 SSE 展示、桌面/移动布局、图交互、持续目标 UI 与前一轮稳定性用例。标准事件用例使用协议 fixture，不是模型调用。
- 本机浏览器截图证据目录为临时目录 `am-provider-api-20260914/run-hExjnV/results/`，隔离服务已关闭。
- 本轮未进行真实 Wails 窗口、真实 Codex/OpenCode 模型任务或公开发布制品验收；原生 CLI 单测/本机配置探测不能代替这些证据。未重启正式服务、未提交或推送。

之后已进行重构后的真实模型专项验收，发现并修复启动/压缩/取消等问题；完整结果与仍存在的上游边界见
[PROVIDER_RUNTIME_ACCEPTANCE.md](PROVIDER_RUNTIME_ACCEPTANCE.md)。

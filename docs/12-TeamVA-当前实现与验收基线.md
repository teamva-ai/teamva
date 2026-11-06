# Teamva 当前实现与验收基线

> 快照日期：2026-08-09
> 分支：`main`
> 性质：本地桌面发布候选，不是已正式公开发布声明。

## 1. 当前实现

### 1.1 Desktop/Web

- Wails v2 macOS 壳、单实例、原生菜单；
- 同源 Svelte 5 UI、九种语言、主题、响应式；
- 一次性 bootstrap、HttpOnly Session、CSRF；
- Project、任务、Agent、本机 Skills、Resource、Memory、Schedule、Activity、Settings；
- 私有轮转日志、脱敏诊断和 SHA-256 更新下载。

### 1.2 Local API/Store

- Go/Gin 单端口 API、默认仅 loopback、本机免访问密码、可选的同端口局域网密码登录、SQLite WAL；
- 本地 User/Computer/Preferences 自动 seed；
- 完整 Agent/Issue/Run/Approval/Policy/Schedule/Audit 领域；
- durable mention、command outbox、event inbox、SSE；
- 基线定义/结构校验、数据目录锁、graceful close 和 SIGKILL 恢复。

### 1.3 Runtime

- 自动探测 Codex/OpenCode 和模型；
- Embedded Controller、RuntimeManager、Run 级 MCP 工具通道、FileBridge 回退、ToolBroker；
- 资源注册/验证、工具评论、原生审批、停止、native session、上下文整理事件；
- 多 Agent 并行；禁止直接 @ 自己，允许间接循环委派。

### 1.4 会话与 Skill

- 每次结构化 `@Agent` 创建独立会话，侧栏追问和修正只续接当前会话；
- Skill 必须先完整安装到本机，再绑定给 Agent；运行时校验已安装目录摘要；
- 用户消息与内部控制消息有明确标记，内部修正指令只在当前会话侧栏显示。

## 2. 当前验证证据

- Server、Node、Desktop 完整 race test 和 vet；Protocol 通过；
- Web check 0 error/0 warning、35 tests、同源生产构建；
- SQLite Repository contract；
- 400 条 SQLite 并发评论、三 Agent 并行 mention、100 Run RuntimeManager soak；
- 两个独立 SIGKILL 子进程测试恢复 app/runtime 数据；
- 真实 OpenCode 与 Codex 完成 Resource、Issue 工具评论、Run 和手动计划任务；
- 真实 Chrome 1280×820 和 390×844 验证本地 Session/UI；
- Wails v2.13 macOS arm64 开发构建已从真实 `.app` 启动并逐页验收；arm64/x64 发布制品另由发布脚本构建。

新增循环委派门禁应证明：用户激活 A，A @B，B 回 @A，A 再完成；三个 Run 全部 succeeded，评论顺序完整。

## 3. 当前明确边界

- 不提供、也不需要在线备份、在线恢复、云同步或数据库损坏修复；
- 基线/integrity 失败保留数据库并拒绝启动；旧库不升级或回填；
- Windows 提供未签名便携包，暂不含 Git 变更面板；Linux 提供未签名 AppImage；
- 当前 macOS 制品使用 ad-hoc 签名、未使用 Developer ID 签名、未公证；Windows/Linux 制品未签名，更新需手工替换；
- Agent 间接循环不自动终止，依赖人工急停和可观察状态；
- Skill scripts/hooks 以当前用户权限执行。

## 4. 首发剩余门禁

1. 在最终 release commit 上复验 Codex/OpenCode 与循环委派；
2. 建立 CI 或等价持续门禁；
3. 决定是否正式签名/公证；
4. 构建并校验 arm64/x64 发布制品；
5. 部署 `/downloads/desktop`，真实下载和升级；
6. 创建 release commit/tag 和无秘密验收报告。

## 5. 复验命令

```bash
(cd server && go test -race -p=1 ./... && go vet ./...)
(cd node && go test -race -p=1 ./... && go vet ./...)
(cd protocol && go test ./...)
(cd desktop && go test -race ./... && go vet ./...)
pnpm --filter @teamva/web check
pnpm --filter @teamva/web test
pnpm --filter @teamva/web build
shellcheck scripts/e2e/*.sh scripts/phase0/*.sh scripts/release/*.sh
npx --yes @redocly/cli@2.2.2 lint protocol/openapi/teamva.yaml
git diff --check
```

最后还必须执行 localhost API、真实 Chrome、真实 Codex/OpenCode 和 Wails `.app` 验收。
任何 skip、mock 或静态检查都不能替代对应真实边界。

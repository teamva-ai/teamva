# Lumi 公共编辑器与共享 UI 组件验收（2026-10-22）

## 当前实现

- Lumi 与普通 Agent 在 `/agents?agent=<id>` 使用相同表单，公共 `PATCH /v1/agents/{id}` 原子保存名称、备注、Provider、模型、提示词和 Skills。首次保存创建 revision；切换 Provider 保留旧 revision；拒绝跨电脑服务。
- Lumi 不显示删除操作，服务端与 SQL 仍保护内置身份；当前底层禁止禁用的既有规则未变。本轮没有修改数据库基线。
- 初始化只有 CLI 检测与认识 Lumi 两步，随后跳转 Agent 页；工作记录与灵感助手没有配置表单，未配置时仅提供导航并禁用对话输入/发送。删除独立 `PUT /work-assistant`。
- Agent 页供应商弹窗适用于所有 Agent；保存供应商只填入草稿，仍需公共保存。Hello 仅显式测试，不自动调用。同一端点追加模型不会移除其他 Agent 使用的旧模型。
- 全局助手 Run 实际携带自定义提示词和 Skills，但始终保留固定只读系统边界、全局会话及专属 home。
- `web/src/ui` 统一 Input、Select、Textarea、Checkbox、Switch、DatePicker、FileInput、Button、Field、Modal、ModelPicker。业务原生表单控件由 AST 边界测试约束，卡片/树/导航等专用交互不机械改造成普通按钮。
- 单行控件桌面 32/36/40px、触摸最小 44px；Select 箭头右侧留白 12px（计入边框实测 13px），文字右 padding 38px；模型输入自由填写，默认不展开模型标签。
- Modal 共用焦点圈定、inert 和嵌套栈；Field 主控件自动关联 label/hint，辅助控件显式取消继承；一次性计划以原生日期控件按浏览器时区选择并序列化 RFC3339，保留秒/毫秒。

## 自动化证据

- `pnpm verify:web`：Svelte 检查 0 错误/0 警告，57 文件、367 测试通过，生产构建通过。
- 固定随机顺序 `--sequence.shuffle --sequence.seed=20261022 --maxWorkers=4`：57 文件、367 测试通过。
- `pnpm verify:backend`：server/node/desktop race 与 vet、protocol 测试通过。没有启用真实模型集成测试环境变量。
- 日期/助手/模型触发按钮定向回归同时通过默认时区与 `Asia/Shanghai`。
- Provider 生成检查、脚本语法、ShellCheck、`git diff --check` 通过。OpenAPI lint 通过，保留 health 4xx 与 bootstrap 303 两条既有建议警告；生产构建保留部分 chunk 超过 500kB 的体积提示。

## 真实浏览器与运行边界

隔离服务使用全新数据目录及 HOME/XDG；Ego TaskSpace 108 验证初始引导、Lumi 公共保存、供应商表单、无独立助手配置和控件几何，已关闭该 TaskSpace。浏览器发现并修复了引导第一步期间仅追踪 query.error、导致第二步可能一直加载的问题，增加成功数据变更订阅和回归。

仓库 Chrome 套件进一步覆盖全站路由、表单、布局和响应式交互；新增 1280×820 桌面、390×844 触摸模式的几何断言，检查真实控件高度、Select 箭头间距、模型触发按钮及页面无横向溢出。

最终完整 Chrome 套件 **59/59 通过（43.4s）**，包括 28 个页面尺寸布局用例和两项新增共享控件几何用例；验证桌面默认高度 36px、触摸高度 44px 及箭头与文字留白。截图位于忽略的 `web/test-results/`，测试没有录制 trace/HAR。

脚本同步采用实际落库等待、重复运行的唯一草稿值、可见任务标题和明确的按钮作用域。资源命令辅助请求仅在 `csrf_failed`（处理器执行前拒绝）时恢复 Cookie 对应的 CSRF 并重试一次，与客户端策略一致，不重试其他 403，也不掩盖业务错误。

已将最终前端与后端更新到当前 `http://127.0.0.1:61556` 工作台，沿用原独立数据目录及端口，未清库或迁移。启动前确认无活动 Run；重启后 readyz 为 200、前端内容与最终构建一致、既有业务表数量不变。隔离验收服务已停止。

尚不把自动化或 Chrome 证据当作真实 Wails 窗口、真实 Codex/OpenCode 模型调用或公开制品验收。本轮不提交、不推送、不发布，不升级 Ego Lite。

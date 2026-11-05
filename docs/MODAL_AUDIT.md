# 弹窗宽度与居中审查（2026-10-26）

## 范围与修复

检查 `web/src` 的全部 `<Modal>`、`role="dialog"`、`aria-modal`、`bg-backdrop`、全屏遮罩与手写对话框，排除测试宿主。当前有 **21 个公共 Modal 调用点**（含共享 ConfirmDialog 包装）与 **5 个有意保留的手写 dialog**。

- 公共 Modal：统一挂到 `document.body`，不受页面/抽屉的 transform、contain、滚动或裁切影响；遮罩用零最小尺寸网格轨道，水平/垂直居中；默认宽度上限 640px，受视口留白限制；超高正文内部滚动，头部和底栏保留。长标题/说明可断行。
- 嵌套：层级自动高于当前弹窗，包括 90 层初始配置内打开默认 50 层诊断；焦点与 inert 使用栈/引用计数，替换加载弹窗、关闭内层或整体卸载不会提前解除背景保护。
- 编程 CLI：按**面板容器宽度**而非窗口宽度切换列（960px 隐藏检测时间，680px 改为卡片），列可收缩，操作列不再被截断；表头/正文共用相同列宽。诊断操作移到固定底栏，长日志仅正文滚动。
- 初始配置：懒加载与 Computer 加载态均复用公共 Modal，移除另一套焦点约束；完成只进入 `/issues`，不打开 Lumi 配置。
- 全局搜索：从上方 10vh 偏移改为水平/垂直居中，宽度上限仍为 720px，受视口留白限制。

## 公共 Modal 清单

| 文件 | 用途 | 宽度上限 px | 处理 |
| --- | --- | ---: | --- |
| `App.svelte` | 初始配置懒加载 | 860 | 手写框改公共 Modal |
| `pages/Onboarding.svelte` | Computer 加载/失败 | 860 | 手写框改公共 Modal |
| `pages/OnboardingGuide.svelte` | 两步初始配置 | 860 | 公共约束 + 容器内 CLI 布局 |
| `components/ProgrammingToolsPanel.svelte` | CLI 诊断 | 640 | 公共约束 + 固定操作底栏 |
| `pages/ConsoleShell.svelte` ×2 | 新建/管理项目 | 640 | 沿用公共约束 |
| `pages/Issues.svelte` ×2 | 新建/修改任务 | 640 | 沿用公共约束 |
| `pages/Conversations.svelte` ×2 | 新建图/分支对话 | 640 | 沿用公共约束 |
| `pages/Resources.svelte` ×2 | 添加/改名资源 | 640 | 沿用公共约束 |
| `pages/Memories.svelte` ×2 | 新建记忆/归档未保存修改 | 640 | 沿用公共约束，动作可换行 |
| `pages/Schedules.svelte` ×2 | 新建/编辑定时任务、触发历史 | 720 / 640 | 沿用公共约束，编辑底栏已独立 |
| `pages/Agents.svelte` | 配置大模型 API | 640 | 沿用公共约束，不改变凭据流程 |
| `pages/McpServers.svelte` ×2 | MCP 编辑/导入 | 820 / 720 | 沿用公共约束，不改变秘密存储 |
| `components/PendingApprovals.svelte` | 原生审批 | 640 | 公共 portal 避免被会话抽屉裁切 |
| `components/ConfirmDialog.svelte` | 共享确认 | 640 | 沿用公共约束 |

共享 ConfirmDialog 在 ConsoleShell、Conversations、Resources、Agents（2）、Inspirations、Memories（3）、WorkRecords 共 10 处复用；全部继承公共宽度/居中修复，不逐页增加私有样式。

## 手写 dialog 与非居中例外

| 文件 | 用途 | 结论 |
| --- | --- | --- |
| `components/GlobalSearch.svelte` | 项目内全局搜索 | 本轮修复为居中，保留专用结果列表/键盘选择 |
| `pages/ConsoleShell.svelte` | 锚定项目选择器 | 非模态锚定弹出层，位置/宽度跟随触发器，**不应居中** |
| `pages/IssuePanel.svelte` | 任务详情右侧抽屉 | 有意贴右、可调整宽度，**不应居中**；由并行任务处理 |
| `pages/AgentSideSheet.svelte` | Agent 会话右侧抽屉 | 有意贴右、可调整宽度，**不应居中**；由并行任务处理 |
| `pages/AgentSideSheet.svelte` | 抽屉内图片预览 | 在所属抽屉中居中，图片 max-width/max-height 限制，保留局部范围；由并行任务维护 |

资源打开方式菜单、任务右键菜单、手机导航面板、日期选择器与目录选择器内联区域不是居中模态框，不把它们强行移到窗口中央。未发现其他原生 `<dialog>` 或 `alertdialog`。

## 回归与证据边界

- 组件测试覆盖 portal 清理、原有 inert 恢复、嵌套层级与焦点、Escape、诊断底栏、初始配置完成路由与持久化失败重试；登录页只移除提示文案，认证/显式设备信任测试保持。
- `scripts/e2e/modal-geometry.spec.mjs` 为独立 UI 夹具（无业务服务、无真实模型），覆盖 1280×820、975×650、800×600、390×844 的 28 个真实 border-box 用例，包括 480/640/720/820/860 上限、transform/contain 祖先、超长正文、嵌套诊断底栏和 CLI 操作按钮边界。执行：`pnpm exec playwright test --config scripts/e2e/modal-geometry.config.mjs`。这不是 Provider 或 Wails 验收。
- 本轮已在真实产品中用 Ego 独立验证 14 类业务弹窗、30 组 border-box 测量（1280×820、975×650、390×844）：居中误差均小于 1px、无横向溢出、可见底栏在视口内；部署后的引导完成实际进入 `/issues`。独立几何夹具 28/28、整站真实 Chrome 114/114 通过。右侧抽屉和锚定项目选择器仍按上表保留原位置。部分 Ego 截图请求超时，几何与交互结果使用实际 DOM 测量，不将失败截图冒充成功；另保留成功的诊断弹窗与 Lumi 真实会话截图。
- 本轮没有真实 Wails GUI 或公开发布验收；不得把浏览器、单测或夹具构建成功写成这些验收已通过。

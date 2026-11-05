# 输入组件无 Outline 规则

输入类组件在任何页面和状态下不显示 Outline，也不使用 box-shadow 或 Tailwind ring 替代外圈。保留原有边框、尺寸、label、键盘与输入绑定；聚焦通过边框/颜色反馈，显式校验错误的边框优先于普通焦点色。

## 范围与实现

- 原生 input（含日期、时间、文件、颜色、checkbox、radio 等）、select、textarea。
- 输入型 ARIA 角色、可编辑文本、ProseMirror 和 CodeMirror 编辑区域。
- Select/Switch/Field/ModelPicker 的可见包装层及复合输入动作；模型触发、快捷选项及展开按钮使用统一标记。
- 普通非输入按钮、链接、导航和其他非输入键盘目标保留原有焦点提示，不全局禁用 focus 或 Tab。
- `app.css` 首先声明 `input-policy` 层；共享样式在该层以 important 重置 outline 样式/宽度/偏移和 box-shadow，因此后续 utility 或未分层的 important 焦点规则不能重新加上外圈。
- 不把光标、文本选择背景、既有边框或浏览器/OS 自己的原生弹层误当成页面 Outline；不通过改变 tabindex、拦截键盘或取消原生选择器来达到视觉效果。

## 验证

样式测试检查层顺序与无状态重置，真实 Chrome 用例 `scripts/e2e/input-focus.spec.mjs` 应检查实际 Input/Select/Textarea/ModelPicker、原生类型与 ARIA、鼠标按下/悬停、程序聚焦、真实 Tab 与 `:focus-visible`、focus-within 包装层、invalid、readonly、disabled、七套配色明暗（含 `default`）、自定义控件色和 forced-colors。

额外注入 utility 层及未分层的 important Outline/阴影冲突，仍断言 `outlineStyle=none`、`outlineWidth=0px`、`outlineOffset=0px`、`boxShadow=none`；普通按钮/链接作保留外框的阴性对照。真实灵感编辑器与源码也检查，无模型调用或 Run。原生弹层不在页面 CSS 验收范围内。

### 2026-10-23 验收

以下为当时六套配色的历史结果，不代表新增 `default` 或独立控件色已经通过验收；当前配色要求见 [全局外观与配色](APPEARANCE.md)。

- `pnpm verify:web` 与固定随机顺序 seed 20261023：67 文件、461 项测试通过，Svelte 检查及生产构建通过；仅保留既有分块体积提示。
- 专项真实 Chrome 6/6、完整套件 **97/97 通过**，包含桌面与触摸输入、真实按下/键盘状态、选择器包装层、校验/只读/禁用、important 冲突、六套配色明暗与 forced-colors；同时验证错误边框与普通非输入焦点提示仍存在。
- Ego TaskSpace 134 实测 Input 与 Select 的真实 focus-visible 状态均为 outline none/0px、偏移 0px、无 box-shadow，Select 外层同样无 Outline，已关闭 TaskSpace。原图与专项截图保留于隔离验收目录，不写入仓库或 trace/HAR。
- 仅 Web/文档变更；61556 工作台已先合并哈希资源、保留旧资源，再原子替换前端 index。响应与最终构建一致、readyz 200、PID 未变；没有重启后端或操作业务数据库，隔离服务已停止。
- 未进行真实 Wails、真实 Codex/OpenCode 模型或公开制品验收；未提交、推送、发布或升级浏览器工具。

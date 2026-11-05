# 表单同行对齐修复（2026-10-22）

## 原因与实测

定时任务弹窗的 Issue 目标和 Agent 使用独立 Field，仅右侧有 hint。Field 的隐式 auto Grid 轨道被父 Grid 拉到同高，额外高度分摊到标签/控件行，使左侧 Select 下移；不是两个 Select 本身高度不同。

隔离真实浏览器实测：修复前两个 Select 顶部差 **11.390625px**，修复后差 **0px**。输入/动作栏也按实际控件行测量，不对齐包含 hint 的整个 Field 底部。

## 修复范围

- Field 固定为 label、单一 control 区域、hint 三个语义部分，并从顶部排布。ModelPicker 多根节点、chips、DatePicker 提示和 DirectoryPicker 复合内容整体留在 control 中。
- 新增 FieldGroup，利用 subgrid 共享同行轨道；长标签、非对称或多行 hint 不再改变相邻控件起点。提供单列折叠与控制行 actions 槽，不用位移、固定标签高度或隐藏溢出掩盖问题。
- 定时任务、Agent 配置、设置语言/主题、记忆标识/类型统一使用 FieldGroup。
- 工作记录的搜索/项目、起止时间，以及灵感标题/项目改用明确的 Field 结构；查询按钮、完成选项对齐控制行，无标签筛选工具栏保持专用布局。
- 设置与远程登录的密码输入/显示按钮统一共享尺寸；远程登录不再把可交互按钮嵌在 label 中。
- DirectoryPicker 的路径和选择按钮使用真实 Grid 占位，不再以固定右 padding 猜测按钮宽度；工具栏可换行。
- 条件构建器删除、变更来源筛选、助手新建会话等使用统一按钮尺寸及可换行组合；变更来源不再让 auto 列挤压会话 Select。
- 普通卡片、树、导航、聊天、星期选择、目录列表及刻意内嵌的图标动作不机械替换成字段组。

## 验证方法

- `FieldGroup.test.ts` 验证 label/hint ID、唯一 control 槽、复合模型选择、disabled fieldset、断点/列数与动作独立性。
- AST 边界检查禁止业务回退到普通多列 Grid 中的独立平行 Field。
- `field-layout.spec.mjs` 使用真实 Chrome 的 border box 断言同行控件 top/bottom/height 差 **≤1px**，先检查原始页面，再对 label/hint 注入真实换行压力，最后恢复原文。
- 覆盖 1280、800、390（触摸）宽度；定时任务新增/编辑及两种 Issue 模式、Agent/Input/Select/ModelPicker、设置、记忆、工作记录与灵感动作槽、密码按钮和目录复合控件；单列时检查不重叠、不横向溢出。
- 回归截图仅保存在忽略的测试制品及隔离目录，不录制 trace/HAR、不调用真实模型。

## 最终证据

- `pnpm verify:web`：65 文件、441 项测试通过；Svelte 检查 0 错误/0 警告，生产构建通过。固定随机顺序 seed 20261022 同样 441 项通过。
- 完整真实 Chrome **84/84 通过**，包括 9 项新增几何/压力回归；1280、800、390px 各检查原始布局、两至三行标签与多行 hint，并严格断言误差 ≤1px，覆盖动作与控制行。
- Ego TaskSpace 123 实测定时任务错位从 11.390625px 降至 0px；工作记录查询动作与两个字段也为同一顶部、同高。TaskSpace 已关闭；另保留 before/after 截图和局部截图，及最终几何截图。
- 仅 Web/文档变更，没有修改或重测后端业务代码。当前 61556 工作台通过“先复制新哈希资源、保留旧资源，再原子替换 index.html”完成前端热更新；响应与最终构建一致，readyz 200、服务 PID 未变，未重启服务或操作数据库。隔离服务已停止。
- 未进行真实 Wails、真实 Codex/OpenCode 模型或公开制品验收；未提交、推送、发布或升级 Ego Lite。构建仅保留既有分块体积提示。

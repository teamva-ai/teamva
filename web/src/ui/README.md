# 共享 UI 控件

浏览器与 Wails 共用的 Svelte 5 控件库，从 `../ui`（按调用目录调整）导入。业务 `input/select/textarea` 必须经本目录组件渲染；不要复制原生控件、页面私有尺寸或替代焦点样式。`boundary.test.ts` 检查该边界。业务布局归页面，字段内部轨道、同行控件对齐、几何、图标间距和状态归本库与 `controls.css`（由应用样式加载）。

## 组件与绑定

| 组件 | 用法 |
| --- | --- |
| `Input` | `bind:value`、`bind:ref`，支持原生属性、事件与 `type`；`value` 可为字符串或数字 |
| `Select` | `bind:value`、`bind:ref`，子内容写原生 `option`；`class` 作用于外层，`ref` 指向原生 select |
| `Textarea` | `bind:value`、`bind:ref`，支持 `rows` 等原生属性 |
| `Checkbox` / `Switch` | `bind:checked`、`bind:ref`，提供可访问名称 |
| `FileInput` | 使用 `accept`、`multiple`、`onchange` 或 `ref.files`，不绑定文件路径 |
| `DatePicker` | `bind:value`，`type="date"`（默认）、`datetime-local` 或 `time` |
| `Button` | 默认 `type="button"`；提交表单显式 `type="submit"`；`loading` 同时禁用按钮 |
| `ModelPicker` | 自由输入模型名与建议列表，可用 `chips` 显示快捷项；`oncommit` 只通知业务，不代表自动保存 |
| `Field` / `FieldGroup` / `Modal` | 统一标签、同行轨道、辅助说明及对话框可访问性，见下文 |

`Input`、`Select` 和 `Button` 使用 `controlSize="sm|md|lg"`，桌面高度分别为 **32/36/40px**，默认 `md`；粗指针设备最小触摸高度 **44px**。Textarea 是多行控件，不强制单行高度。Checkbox/Switch 应放在可点击 label 中保留触摸目标。按钮 `variant` 为 `primary|secondary|ghost|danger`；纯图标按钮使用 `icon` 并提供 `aria-label`。

Select 保留原生键盘交互，箭头距右边 **12px**，右侧文字留白 **38px**，箭头不接收指针事件；不要用页面 CSS 覆盖这些尺寸。统一支持禁用、`aria-invalid`、焦点与主题状态，验证文案和保存行为仍由业务负责。

控件配色使用全局 `--color-control*`：主按钮/已选开关/复选框使用 `control`，主按钮和开关滑块使用可读的 `controlText`，普通按钮与输入使用派生的 `controlSubtle/controlBorder`，输入焦点使用 `controlFocus`。控件色与内容强调色独立，危险操作继续使用 danger 语义色；禁止在业务按钮上硬编码颜色或用正文 ink 替代控件色。

输入类控件在任何页面和状态下均不显示 Outline，也不得用 box-shadow/Tailwind ring 替代外圈。该规则包括鼠标、键盘 `:focus-visible`、`:focus-within`、错误、只读、禁用、日期/文件/模型选择器、checkbox/switch、输入型 ARIA 角色及可编辑文本/代码区域。`input-policy` 是最先声明的 CSS 层，以 important 重置 outline 样式、宽度和偏移、box-shadow，优先于其他层中的 focus/ring 工具类；不要在组件中追加内联 important 焦点样式绕过它。输入仍保留 Tab、键盘、label 和边框/颜色反馈，错误边框在聚焦时继续显示；普通非输入按钮、链接与导航继续保留键盘焦点外框。浏览器或 OS 自己的弹层不属于页面 CSS。

## Field：一个主控件

```svelte
<script lang="ts">
  import { Field, Input, Select, Checkbox } from "../ui";
  let name = $state("");
  let provider = $state("");
  let extra = $state(false);
</script>

<Field label="名称" hint="显示在 Agent 列表中">
  <Input bind:value={name} required />
</Field>
<Field label="Provider">
  <Select bind:value={provider}>
    <option value="">请选择</option>
    <option value="opencode">OpenCode</option>
  </Select>
  <label><Checkbox bind:checked={extra} inheritField={false} />辅助选项</label>
</Field>
```

Field 自动生成主控件 `id`、label 的 `for`、`aria-labelledby` 与 hint 的 `aria-describedby`。一个 Field 只保留一个继承上下文的主控件；同区域的辅助控件必须设 `inheritField={false}` 并提供自己的 label 或 `aria-label`，避免重复 ID 和错误关联。可在 Field 指定 `id`；显式传给控件的 ARIA 属性优先，无需重复自动关联。

## FieldGroup：同行控件必须共用轨道

```svelte
<FieldGroup collapseAt="sm">
  <Field label="Issue 目标"><Select bind:value={target}>...</Select></Field>
  <Field label="Agent" hint="沿用 Agent 的已保存配置">
    <Select bind:value={agent}>...</Select>
  </Field>
</FieldGroup>
```

- 普通横排 Field 使用 FieldGroup，不再直接放到独立 `grid-cols-2/3` 中。`items-start` 只能避免高度分摊，不能解决一侧标签换行后的错位。
- 默认两列；`columns={1|2|3}`；`collapseAt="sm|md|lg"` 分别在小于 640/768/1024px 时变为独立单列。现代浏览器使用 subgrid 共享 label/control/hint 轨道；不支持时回退顶对齐布局。
- Field 的 children 整体放在唯一 `.am-field__control` 中。ModelPicker 的 datalist、可选 chips，以及目录选择器等复合内容不会变成额外的字段轨道；不要强迫整个复合控件与单行 Input 等高。
- 单行字段组可用 `actions` snippet 放提交按钮或 checkbox 选项：动作与字段共用控制行，而不是对齐含 hint 的整个 Field 底部。多个逻辑行各自使用一个带 actions 的 FieldGroup；不为按钮伪造空 label。
- 无可见标签的工具栏、条件构建器、聊天输入、卡片和树保留专用可换行布局；其 Input/Select/Button 仍须使用一致的尺寸与触摸规则。嵌入式的小号图标动作可以有意居中，但不能覆盖输入区域。
- 位置回归必须用浏览器真实 border box 验证，同行控件上下边及高度差 ≤1px，并覆盖非对称 hint、多行标签、窄屏和触摸设备；不只检查 CSS 类或“无横向溢出”。

## Modal：焦点与嵌套

用条件渲染挂载/卸载 Modal，提供 `title`、`onClose`、内容以及可选的 `footer`、`headerExtra` snippet。`description` 自动关联 `aria-describedby`。`maxWidth` 为像素宽度上限（默认 640），始终受视口与留白约束；弹窗水平、垂直居中，超高内容仅在正文内滚动，头部/底栏保留。`layer` 是视觉层级下限，嵌套弹窗自动高于当前顶层；`centered` 只居中标题。

正文使用自然内容基准 `flex: 0 1 auto`，不要改回 `flex: 1` / `0%` 基准，也不要为消除首开闪动固定高度或延时显示。弹窗布局回归除最终尺寸外，还须采样首次与重复打开的首个可见绘制帧，验证静态内容的高度、位置稳定；异步内容确实变化与布局自身塌陷应分别判断。

- 自动聚焦 `initialFocus` CSS 选择器，其次 autofocus、首个表单控件或可聚焦元素，无可用控件时聚焦对话框。
- 圈定 Tab/Shift+Tab 与外部焦点，跳过隐藏、disabled、inert 控件；对话框外背景设为 inert，卸载时恢复。
- 模块级嵌套栈确保只有顶层处理 Escape/遮罩及焦点约束；关闭顶层后恢复打开前的焦点且不滚动页面。
- 遮罩挂到 `document.body`，避免页面/抽屉的 transform、contain 和 overflow 改写视口定位或裁切弹窗；主题仍使用根节点 token。不要通过父 DOM 查询已移出的弹窗，也不要给公共 Modal 另设固定宽度或私有居中位移。
- `dismissible={false}` 禁止关闭按钮、Escape 和遮罩关闭；`closeDisabled` 暂时禁止关闭。业务取消/保存按钮仍需自行处理状态。

不要另写全局 Escape 或 focus trap 与 Modal 抢焦点；父层应在子层关闭后再卸载，保持栈式生命周期。

## DatePicker：ISO 时间不截断

```svelte
<Field label="执行时间">
  <DatePicker type="datetime-local" iso bind:value={runAt} />
</Field>
```

`iso` 配合 `datetime-local` 时，绑定值为绝对 ISO/RFC3339 时间，界面按浏览器本地时区显示，编辑后转为 UTC ISO；默认 `step="any"`，保留秒和毫秒。不要用 `slice(0, 16)` 去掉时区或精度。无效/空值转换为空字符串，原生日期校验及业务必填验证由调用者处理。未启用该组合时直接绑定原生日期/时间字符串。

## 验证

运行 `pnpm test:web` 验证控件与业务复用边界；`pnpm verify:web` 额外执行类型检查和生产构建。交互或布局改动按仓库要求补真实浏览器验收，不把组件测试当作 Wails 或真实 Provider 证据。

# Product screenshots and illustrative examples

[English project guide](../PROJECT.md) · [中文项目说明](../PROJECT.zh-CN.md)

## What the screenshots show

The images are PNG captures of the actual shared browser/Wails UI, rendered by real Chrome at **1600 × 1100 CSS pixels / 2× device scale** (3200 × 2200 image pixels). They are not rebuilt HTML mockups or upscaled 1× screenshots.

The current captures use the approved Silken brand mark. The capture checks its actual loaded bytes against `assets/brand/icon.png`; branding changes require recapturing the real UI, not painting over an old screenshot.

- English: eight images at `docs/screenshots/*.png`, with actual `en-US` UI preferences and English demo content.
- 中文：八张截图位于 `docs/screenshots/zh/*.png`，使用应用真实的 `zh-CN` 语言设置；任务、分工、角色、提示词、对话、图节点、记忆及备注均为中文，演示正文使用中文全角标点。

Each language uses its **own fresh temporary database and independent loopback service**. Language is saved through the real preferences API, not overlaid onto screenshots. Brand names, repository names, model references, and MCP server identifiers keep their technical spelling; MCP names must satisfy the application's ASCII identifier rules.

The Atlas launch is an **illustrative scenario**, not a record of model execution:

- **Public multi-agent collaboration (`multitask.png`):** no conversation sidebar is open. Five compact public comments show Product Manager → Frontend Developer → Backend Developer → QA Engineer → Product Manager, with structured handoffs and strictly increasing timestamps. The original assignment and dependencies remain earlier in the same real timeline.
- **Independent conversation and intervention (`agent-conversation.png`):** a separate screenshot opens Frontend Developer's actual session sidebar. Its two prewritten rounds include the initial plan, a user direction change, and the revised reply; its composer contains an **unsent** follow-up draft. The shared collaboration task remains identifiable behind it.
- **Responsibilities, not model rankings:** Product Manager / 产品经理 handles **Deep analysis / 深度分析** of product requirements; QA Engineer / 测试工程师 handles acceptance review. Frontend Developer / 前端开发 and Backend Developer / 后端开发 handle **Efficient execution / 高效执行** of implementation. These labels describe the example's division of work, not model intelligence, speed, scores, or measured performance. Provider and model fields use the real installed CLI's catalog.
- **Graph exploration:** compare guided setup and an expert command center from the same shared starting point.
- **Project context:** three real temporary Git directories, a document directory, specification and website URLs, project memory, agent configuration, the Skills catalog, and MCP configuration examples.

The completed Runs, comments, and graph replies are inserted as fixtures labeled **“Demo data.” / “演示数据。”**. No performance, tool-execution, or test-success claims can be inferred from them. The sample MCP services are not connected. Skills screenshots show the actual catalog, not fabricated installations. Existing product labels and catalog descriptions are captured as the current app supplies them; the capture script never invents translations for native UI or third-party metadata.

中文说明：`multitask.png` 只看四个岗位在公共任务中的交接，不打开会话侧栏；`agent-conversation.png` 单独看前端开发的两轮历史、用户改方向及未发送追问。产品经理负责需求范围、测试工程师负责验收评审，标记为“深度分析”；前后端负责实现，标记为“高效执行”。它们是分工标签，不是模型优劣或速度宣传。公开评论通过终态夹具写入，结构化 @ 均已完成，不触发执行。

## Reproduce safely

Requirements: the normal Go/Node/pnpm development environment, an installed coding CLI that can be configured, and Chrome. The capture does **not** call a model, test Provider credentials, or connect to an external MCP service.

From the repository root:

```bash
pnpm install
pnpm build # includes pnpm generate:providers and the production product UI build
```

Start the English service in one terminal and repeat this block in a **second terminal** for Chinese. Each invocation must create a new directory. Keep the two printed paths as `EN_DIR` and `ZH_DIR` for the capture terminal:

```bash
SHOWCASE_TMP="${TMPDIR:?}/opencode"
mkdir -p "$SHOWCASE_TMP"
export TEAMVA_LOCAL_DATA_DIR="$(mktemp -d "$SHOWCASE_TMP/teamva-showcase.XXXXXX")"
export TEAMVA_LOCAL_STATE_FILE="$TEAMVA_LOCAL_DATA_DIR/runtime.json"
export TEAMVA_LOCAL_WEB_DIR="$PWD/web/dist"
export TEAMVA_LOCAL_HTTP_ADDR="127.0.0.1:0"
export TEAMVA_LOCAL_ALLOW_EXTERNAL=false
printf '%s\n' "$TEAMVA_LOCAL_DATA_DIR"
go -C server run ./cmd/local
```

Keep both isolated services running. In another terminal, seed each once and capture both locales in a single real-Chrome test run:

```bash
export EN_DIR="<fresh English teamva-showcase directory>"
export ZH_DIR="<fresh Chinese teamva-showcase directory>"
node scripts/showcase/seed.mjs "$EN_DIR" en
node scripts/showcase/seed.mjs "$ZH_DIR" zh
TEAMVA_SHOWCASE_MANIFESTS="$EN_DIR/showcase.json,$ZH_DIR/showcase.json" \
  pnpm exec playwright test --config scripts/showcase/playwright.config.mjs
```

The optional seed language argument is strictly `en|zh` (default `en`). To capture only one language, the singular `TEAMVA_SHOWCASE_MANIFEST` still works. A combined run rejects repeated locales, so two projects cannot overwrite the same output files.

The seed refuses a directory outside the system temporary root, an unexpected directory name, a non-loopback service, or a database containing existing tasks, user agents, Runs, resources, memories, graphs, schedules, inspirations, installed Skills, or extra projects. It uses normal local authenticated APIs for setup and actual directory validation, waits for CLI discovery and all seven resources to be ready, then writes only prewritten terminal-state fixtures to that **temporary database**. Agent model fields come from the real installed CLI's probed catalog; discovery does not invoke a model or verify model execution. It never writes a database baseline or queues a Run. Assertions check foreign keys, six terminal Runs, Frontend Developer's chronological two-Run history, five ordered public agent comments, six graph messages, completed mentions without self-mentions, no active Runs, zero Provider sessions, and that the command outbox contains only resource-validation commands.

For **each language**, the browser check verifies the saved locale and actual HTML language, opens **all four** agent sessions, verifies their prewritten replies, captures eight screens, and rejects business mutations that could start execution. Every capture asserts no error alerts, no old role names, and the exact 3200 × 2200 PNG dimensions. The public task image uses actual scrolling of the existing timeline, not layout changes or fabricated DOM. Its checks require no sidebar, all five public authors and their @ handoffs fully inside the timeline's visible border box, in chronological order, with both responsibility labels visible. The separate conversation image uses the real resize control's default **620 CSS-pixel sidebar** and checks both rounds' headings and the draft composer are in view. It types a draft but never sends it. All four Agents' names and prompts must load from real API responses without old role names or page errors. No trace, video, cookie export, or HAR is recorded. `showcase.json` contains only local origin, locale, demo IDs and public handoff labels, not authentication material.

The seed is deliberately **one-shot**. If setup fails midway, retain the directory for diagnosis and start again with a new temporary directory; do not clear or repair an existing application database. After capture, use Ctrl+C in each isolated service terminal and retain both data directories. Do not stop an unrelated website development service. Never reuse showcase data as your normal workspace.

## Files

The following eight basenames exist at **both** `docs/screenshots/` (English) and `docs/screenshots/zh/` (Chinese):

| Image | Surface |
| --- | --- |
| `multitask.png` | Public four-role collaboration with five ordered comments, structured @ handoffs, and no conversation sidebar |
| `graph.png` | Frozen parent and two writable graph branches |
| `agent-conversation.png` | Frontend Developer's separate session sidebar, two prewritten rounds, a user direction change, and an unsent follow-up |
| `resources.png` | Multiple repositories, document directory, links and website |
| `agents.png` | Role, provider, model, prompt and Skill-binding form |
| `skills.png` | Local installation area and real Skills catalog |
| `mcp.png` | Personal and project configuration; not connected |
| `memories.png` | Versioned project context |

The English website uses the root images; the Chinese website uses `zh/`. Vite imports the same assets, so README and site do not accumulate separate screenshot copies.

## Research and marketing boundaries

Research checked **2026-11-04**:

- [DeepSeek's official Super-agent research prototype](https://github.com/deepseek-ai/dsh-superagent-prototype), published **2026-10-31**. Its README explicitly describes research code, not an official production product, and notes that some article designs are absent from the prototype.
- [People's Daily coverage](https://j.people.com.cn/n3/2026/1102/c94476-20514657.html), **2026-11-02**: Router/Worker separation, parallel work, and moving session coordination away from humans.

Teamva's copy relates that research discussion to its own implemented work assignments, independent sessions, persisted state, and local control. It does not claim a DeepSeek partnership, implementation lineage, benchmark advantage, autonomous chat monitoring, cloud persistence, or work while the computer is off.

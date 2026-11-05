// Illustration data in a FRESH temporary service only. Never calls a model.
import assert from "node:assert/strict";
import { readFile, writeFile, mkdir, realpath } from "node:fs/promises";
import { join, resolve, basename } from "node:path";
import { tmpdir } from "node:os";
import { spawnSync } from "node:child_process";
import { DatabaseSync } from "node:sqlite";
import { randomUUID } from "node:crypto";
import { setTimeout } from "node:timers/promises";

const locale = process.argv[3] || "en";
assert(["en", "zh"].includes(locale) && process.argv.length <= 4, "Usage: seed.mjs <fresh-directory> [en|zh]");
const text = (en, zh) => locale === "zh" ? zh : en;
const demoLabel = text("Demo data.", "演示数据。");
const directory = await realpath(resolve(process.argv[2] || "."));
const temporaryRoot = await realpath(tmpdir());
assert(directory.startsWith(temporaryRoot + "/") && basename(directory).startsWith("teamva-showcase."),
  "Use a fresh mktemp directory named teamva-showcase.* under TMPDIR; never your app data.");
const { origin } = JSON.parse(await readFile(join(directory, "runtime.json"), "utf8"));
const url = new URL(origin);
assert(url.protocol === "http:" && url.hostname === "127.0.0.1", "Only isolated loopback services are allowed.");
const db = new DatabaseSync(join(directory, "app.db"));
db.exec("PRAGMA foreign_keys=ON; PRAGMA busy_timeout=5000;");
for (const table of ["issues", "runs", "project_resources", "project_memory_entries", "conversation_graphs", "scheduled_tasks", "inspirations", "computer_skill_packages"]) {
  assert.equal(db.prepare(`SELECT count(*) AS n FROM ${table}`).get().n, 0, `Refusing a non-empty workspace (${table}).`);
}
assert.equal(db.prepare("SELECT count(*) AS n FROM projects").get().n, 1,
  "Only the automatically provisioned default project may exist.");
assert.equal(db.prepare("SELECT count(*) AS n FROM agents WHERE system_role <> 'general'").get().n, 0, "Refusing existing agents.");

let cookie = "", csrf = "";
async function api(path, body, method = body === undefined ? "GET" : "POST") {
  const response = await fetch(origin + "/v1" + path, { method, headers: {
    Origin: origin, "Content-Type": "application/json", Cookie: cookie, "X-CSRF-Token": csrf
  }, ...(body === undefined ? {} : { body: JSON.stringify(body) }) });
  const cookies = response.headers.getSetCookie();
  if (cookies.length) cookie = cookies.map((value) => value.split(";")[0]).join("; ");
  assert(response.ok, `${method} ${path}: HTTP ${response.status}`);
  return response.status === 204 ? undefined : (await response.json()).data;
}
csrf = (await api("/auth/local-session", {})).csrf_token;
const computer = (await api("/computers")).items[0];
let services = [];
for (let attempt = 0; attempt < 120; attempt++) {
  const catalog = (await api(`/computers/${computer.id}/agent-services`)).items;
  services = catalog.filter((s) => s.status === "ready" && s.capabilities?.models?.length);
  if (services.length && catalog.every((s) => s.last_probed_at)) break;
  assert(attempt < 119, "Real CLI discovery did not complete with an available model catalog.");
  await setTimeout(1000);
}
assert(services.length, "A real installed coding CLI with a probed model catalog is needed (not to run it).");
const preferences = await api("/me/preferences");
await api("/me/preferences", { expected_version: preferences.version, locale: text("en-US", "zh-CN"), theme: "light", appearance: { palette: "default" } }, "PATCH");
const lumi = (await api(`/computers/${computer.id}/agents`)).items.find((agent) => agent.system_role === "general");
assert(lumi && !lumi.current_revision_id, "Only an unconfigured built-in Lumi may exist.");
await api(`/agents/${lumi.id}`, { expected_version: lumi.version,
  note: text("Built-in assistant for project work and ideas.", "内置助手，协助项目工作与灵感整理。"),
  agent_service_id: services[0].id, model: services[0].capabilities.models[0],
  system_prompt: text("Help organize project work and ideas. Stay within the requested scope.", "协助整理项目工作与灵感，仅处理约定范围。") }, "PATCH");
await api("/system/onboarding", { dismissed: true }, "PUT");
for (const tour of ["overview", "issues", "conversations", "resources", "memories", "schedules", "changes"]) {
  await api(`/me/feature-tours/${tour}/seen`, {});
}
const project = await api("/projects", { computer_id: computer.id, name: text("Atlas · Product launch", "Atlas：产品发布"),
  description: text("Three repositories, one shared outcome. Demo data.", "三个代码仓库，一个共同目标。演示数据。") });
const people = text([
  ["Product Manager", "Deep analysis · product scope, requirements and acceptance criteria"],
  ["Frontend Developer", "Efficient execution · accessible interfaces and draft-preserving error states"],
  ["Backend Developer", "Efficient execution · API contracts, validation and atomic writes"],
  ["QA Engineer", "Deep analysis · acceptance review, keyboard flow and retry boundaries"]
], [
  ["产品经理", "深度分析：明确产品范围、需求与验收标准"],
  ["前端开发", "高效执行：实现无障碍界面与保留草稿的错误反馈"],
  ["后端开发", "高效执行：实现接口契约、校验与原子写入"],
  ["测试工程师", "深度分析：验收评审、键盘流程与重试边界"]
]);
const agents = [];
for (const [index, [name, note]] of people.entries()) {
  const service = services[index % services.length];
  agents.push(await api(`/computers/${computer.id}/agents`, {
    name, note, agent_service_id: service.id, model: service.capabilities.models[0],
    system_prompt: text(`You are ${name}. ${note}. Stay within the requested scope. Summarize changes and evidence. Ask before destructive actions.`,
      `你是${name}。${note}。仅处理约定范围，交接时总结变更与验证依据。破坏性操作前先征求确认。`)
  }));
}
for (const name of ["atlas-web", "atlas-api", "atlas-design-system", "product-docs"]) {
  const path = join(directory, "example", name);
  await mkdir(path, { recursive: true });
  await writeFile(join(path, "README.md"), `# ${name}\n\n${demoLabel}\n`);
  if (name !== "product-docs") {
    const result = spawnSync("git", ["init", "--quiet", "--initial-branch=main", path]);
    assert.equal(result.status, 0, "Could not initialize demo repository.");
  }
  const resource = await api(`/projects/${project.id}/resources`, { kind: name === "product-docs" ? "document_directory" : "code_directory", name, path });
  await api(`/projects/${project.id}/resources/${resource.id}/validate`, { expected_version: resource.version });
}
for (const [name, link] of [[text("Launch specification", "发布需求说明"), "https://example.com/atlas/spec"], [text("Product website", "产品官网"), "https://example.com/atlas"], [text("Svelte documentation", "Svelte 文档"), "https://svelte.dev/docs"]]) {
  await api(`/projects/${project.id}/resources`, { kind: "url", name, url: link });
}
for (let attempt = 0; attempt < 60; attempt++) {
  const resources = (await api(`/projects/${project.id}/resources`)).items;
  if (resources.length === 7 && resources.every((r) => r.status === "ready")) break;
  assert(attempt < 59, "Demo resources did not finish real validation.");
  await setTimeout(1000);
}
for (const [key, kind, title, content] of text([
  ["architecture", "decision", "One product. Three repositories.", "Svelte frontend, Go API, shared design system. Keep API contracts explicit and review changes across repositories."],
  ["quality", "constraint", "Accessibility is a release gate", "Keyboard navigation, visible button focus, readable contrast, reduced motion. Test narrow layouts before release."],
  ["review", "fact", "Every handoff has a public summary", "Record what changed, what was checked, and what remains uncertain. The next specialist should not have to reconstruct the work."]
], [
  ["architecture", "decision", "一个产品，三个代码仓库。", "Svelte 前端、Go API、共享设计系统。明确接口契约，跨仓库评审相关变更。"],
  ["quality", "constraint", "无障碍是发布门槛", "支持键盘操作、按钮焦点提示、清晰对比度与减少动效。发布前验证窄屏布局。"],
  ["review", "fact", "每次交接都有公共总结", "记录变更、验证依据与尚存疑问，让下一位协作者直接接续工作。"]
])) await api(`/projects/${project.id}/memories`, { key, kind, title, content, source_type: "user" });
const issue = await api(`/projects/${project.id}/issues`, { title: text("Atlas onboarding · One task, four agents", "Atlas 新手引导：一个任务，四位协作者"), status: "open",
  description: text("## One launch. Four specialists.\n\nProduct Manager defines the scope. Frontend Developer and Backend Developer implement in parallel. QA Engineer reviews acceptance criteria.\n\n*Demo data.*",
    "## 一次发布，四位岗位伙伴。\n\n产品经理明确范围，前端开发与后端开发并行实现，测试工程师评审验收标准。\n\n*演示数据。*"), worktree_hint: true });
const comment = await api(`/issues/${issue.id}/comments`, { body: text("Product Manager defines requirements → Frontend Developer + Backend Developer implement → QA Engineer reviews.\n\n*Demo data.*",
  "产品经理明确需求 → 前端开发 ＋ 后端开发实现 → 测试工程师评审。\n\n*演示数据。*"), mentioned_agent_ids: [] });
for (const [title, status] of text([
  ["Audit the payment API authorization boundary", "open"], ["Explore three dashboard directions", "open"],
  ["Prepare the weekly dependency review", "open"], ["Document the release checklist", "resolved"],
  ["Design keyboard-friendly navigation", "resolved"], ["Review error recovery across repositories", "open"]
], [
  ["审查支付接口的授权边界", "open"], ["探索三种仪表盘设计方向", "open"],
  ["准备每周依赖审查", "open"], ["整理发布检查清单", "resolved"],
  ["设计支持键盘操作的导航", "resolved"], ["评审跨仓库错误恢复策略", "open"]
])) await api(`/projects/${project.id}/issues`, { title, status, description: demoLabel, worktree_hint: false });
const graph = (await api(`/projects/${project.id}/conversation-graphs`, { title: text("Explore the onboarding architecture", "探索新手引导架构"), agent_id: agents[0].id, node_title: text("01 · Shared starting point", "01：共同起点") })).graph;
await api("/mcp/servers", { name: "atlas-project-tools", scope: "project", project_id: project.id, version: 0,
  config: { type: "local", command: [process.execPath, resolve("scripts/e2e/fixtures/mcp-server.mjs")], disabled: false } });
await api("/mcp/servers", { name: "reference-library", scope: "user", version: 0,
  config: { type: "remote", url: "https://example.com/mcp", disabled: true } });

// Seed completed illustrations directly: POSTing mentions/messages would run a
// real provider. Never mark active work or enqueue commands in this fixture.
const uid = (prefix) => `${prefix}_${randomUUID().replaceAll("-", "")}`;
const stamp = new Date().toISOString();
function insert(table, row) {
  const keys = Object.keys(row);
  db.prepare(`INSERT INTO ${table} (${keys.join(",")}) VALUES (${keys.map(() => "?").join(",")})`).run(...keys.map((key) => row[key]));
}
const outputs = text([
  "### Shared contract\n\nKeep the onboarding draft local until confirmation.\n\n**Frontend Developer:** Workspace → Connect → Review.\n\n**Backend Developer:** validate before an atomic write. QA Engineer reviews the combined flow.",
  "### Frontend plan\n\nBuild **Workspace → Connect → Review** against Product Manager's scope.\n\nKeep drafts on validation errors. Backend Developer owns the error contract; QA Engineer reviews keyboard flow.",
  "### API proposal\n\nUse one atomic workspace-creation request. Reject duplicate names and invalid references before writing.\n\nReturn structured validation errors so the UI can preserve its draft. Never treat a partial write as success.",
  "### Review checklist\n\n- Contract matches both repositories\n- Invalid input preserves the form draft\n- Keyboard-only flow reaches every action\n- Retry cannot create a duplicate workspace"
], [
  "### 共享契约\n\n用户确认前，新手引导草稿仅保存在本地。\n\n**前端开发：**工作区 → 连接 → 确认。\n\n**后端开发：**原子写入前校验。测试工程师评审整体流程。",
  "### 前端方案\n\n按照产品经理的范围实现**工作区 → 连接 → 确认**。\n\n校验出错保留草稿，后端开发明确错误契约，测试工程师评审键盘流程。",
  "### API 方案\n\n通过单次原子请求创建工作区，写入前拒绝重名与无效引用。\n\n返回结构化校验错误，让界面保留草稿；部分写入不能视为成功。",
  "### 评审清单\n\n- 两个仓库遵循同一契约\n- 无效输入保留表单草稿\n- 纯键盘可完成全部操作\n- 重试不会重复创建工作区"
]);
function completedRun(agent, instance, generation, input, output, offset = 0) {
  const run = uid("run"), event = uid("event");
  const time = new Date(Date.parse(stamp) + offset * 1000).toISOString();
  const provider = services.find((s) => s.id === agent.agent_service_id).provider;
  insert("runs", { id: run, issue_id: issue.id, issue_agent_instance_id: instance, issue_agent_generation_id: generation,
    agent_id: agent.id, project_id: project.id, computer_id: computer.id, status: "succeeded", agent_service_id: agent.agent_service_id,
    provider, model: agent.model, read_only: 1, input: JSON.stringify([{ type: "text", text: input }]),
    snapshot: "{}", idempotency_key: uid("demo"), created_at: time, started_at: time, finished_at: time });
  const payload = JSON.stringify({ provider_id: provider, id: `demo-reply-${run}`, text: output + `\n\n*${demoLabel}*` });
  const prefix = JSON.stringify({ id: event, run_id: run, lease_epoch: 1, sequence: 1, type: "output.snapshot" }).slice(0, -1) + ',"payload":';
  const suffix = `,"occurred_at":${JSON.stringify(time.replace(/\.?0+Z$/, "Z"))}}`;
  insert("run_events", { id: event, run_id: run, sequence: 1, event_type: "output.snapshot", payload,
    payload_wire: payload, event_wire_prefix: prefix, event_wire_suffix: suffix,
    payload_wire_bytes: Buffer.byteLength(payload), event_wire_bytes: Buffer.byteLength(prefix + payload + suffix), occurred_at: time.replace("Z", "000000Z") });
  return run;
}
const work = [];
const publicComments = [];
db.exec("BEGIN IMMEDIATE");
try {
  for (const [index, agent] of agents.entries()) {
    const instance = uid("inst"), generation = uid("gen"), mention = uid("wmen");
    const title = text(["Define the shared contract", "Build the onboarding interface", "Implement the workspace API", "Review the integrated experience"],
      ["明确共享契约", "实现新手引导界面", "实现工作区接口", "评审整体体验"])[index];
    insert("issue_agent_instances", { id: instance, issue_id: issue.id, agent_id: agent.id, agent_revision_id: agent.current_revision_id, status: "idle", last_activity_at: stamp });
    insert("issue_agent_generations", { id: generation, issue_agent_instance_id: instance, agent_revision_id: agent.current_revision_id });
    const run = completedRun(agent, instance, generation, text(`${title}. ${demoLabel}`, `${title}。${demoLabel}`), outputs[index], index * 60);
    if (index === 1) {
      completedRun(agent, instance, generation,
        text("Keep this keyboard-first. Move advanced options to Review, not the first step.", "优先支持键盘操作。高级选项移到确认步骤，不要放在第一步。"),
        text("### Direction updated\n\nMove advanced controls to **Review** and preserve Backend Developer's error contract.\n\nInclude the retry path in the handoff to **QA Engineer**.",
          "### 方向已调整\n\n高级选项移至**确认**，保持后端开发约定的错误契约。\n\n向**测试工程师**交接时附上重试路径。"), 90);
    }
    insert("issue_comment_mentions", { id: mention, comment_id: comment.id, target_kind: "agent", target_agent_id: agent.id, status: "completed", consumed_at: stamp });
    work.push({ key: `step-${index}`, agent_id: agent.id, title, instruction: title,
      after: index === 0 ? [] : index === 3 ? ["step-1", "step-2"] : ["step-0"], mention_id: mention, run_id: run, issue_agent_instance_id: instance, status: "succeeded" });
  }
  insert("app_metadata", { key: `issue_work:v1:${issue.id}:${comment.id}`, value: JSON.stringify({ schema: 1, issue_id: issue.id, comment_id: comment.id, items: work }) });
  const handoffs = text([
    [0, "**Deep analysis · Scope** Keep Workspace → Connect → Review; defer advanced options. @Frontend Developer and @Backend Developer, implement against one validation contract. *Demo data.*", [1, 2]],
    [1, "**Efficient execution · UI** I’ll preserve the draft on errors. @Backend Developer, can we use 422 for invalid fields and 409 for a duplicate workspace?", [2]],
    [2, "**Efficient execution · API** Agreed: 422 includes field errors; 409 means the name exists. Creation is atomic. @QA Engineer, review retry and duplicate-submit cases.", [3]],
    [3, "**Deep analysis · Acceptance** Gate on keyboard-only completion, preserved drafts, and no duplicate on retry. @Product Manager, confirm advanced options stay in Review.", [0]],
    [0, "**Deep analysis · Decision** Confirmed: advanced options belong in Review. @Frontend Developer, make the retry path explicit and hand it back to QA Engineer.", [1]]
  ], [
    [0, "**深度分析·需求范围** 保留工作区 → 连接 → 确认，高级选项后置。@前端开发、@后端开发，请按同一校验契约实现。*演示数据。*", [1, 2]],
    [1, "**高效执行·前端实现** 出错时保留草稿。@后端开发，字段无效统一返回 422，工作区重名返回 409，可以吗？", [2]],
    [2, "**高效执行·接口约定** 可以：422 附字段错误，409 表示重名；创建保持原子性。@测试工程师，请评审重试与重复提交场景。", [3]],
    [3, "**深度分析·验收评审** 门槛是纯键盘完成、出错保留草稿、重试不重复创建。@产品经理，请确认高级选项只放在确认步骤。", [0]],
    [0, "**深度分析·产品确认** 已确认，高级选项放在确认步骤。@前端开发，请明确重试路径，再交给测试工程师评审。", [1]]
  ]);
  for (const [index, [author, body, targets]] of handoffs.entries()) {
    const time = new Date(Date.parse(stamp) + (index * 60 + 10) * 1000).toISOString();
    const id = uid("comment");
    const run = index < 4 ? work[author].run_id : completedRun(agents[0], work[0].issue_agent_instance_id,
      db.prepare("SELECT id FROM issue_agent_generations WHERE issue_agent_instance_id=?").get(work[0].issue_agent_instance_id).id,
      text("Confirm the acceptance scope requested by QA Engineer.", "确认测试工程师提出的验收范围。"), body, 240);
    insert("issue_comments", { id, issue_id: issue.id, author_kind: "agent", author_agent_id: agents[author].id,
      body, run_id: run, created_at: time, updated_at: time });
    for (const target of targets) {
      assert.notEqual(target, author, "An agent must not mention itself.");
      insert("issue_comment_mentions", { id: uid("men"), comment_id: id, target_kind: "agent", target_agent_id: agents[target].id,
        status: "completed", available_at: time, consumed_at: time });
    }
    publicComments.push({ id, author: agents[author].name, targets: targets.map((target) => agents[target].name) });
  }
  const root = db.prepare("SELECT * FROM conversation_nodes WHERE graph_id=?").get(graph.id);
  db.prepare("UPDATE conversation_nodes SET position_x=0,position_y=190,width=360,height=470,locked=1 WHERE id=?").run(root.id);
  const rootMessage = uid("msg");
  const messageTime = (offset) => new Date(Date.parse(stamp) + offset * 1000).toISOString().replace("Z", "000000Z");
  insert("conversation_messages", { id: uid("msg"), node_id: root.id, author_kind: "user", body: text("How should we structure the onboarding flow? Compare two directions before committing.", "新手引导应该如何组织？先比较两种方向，再决定方案。"), created_at: messageTime(0) });
  insert("conversation_messages", { id: rootMessage, node_id: root.id, author_kind: "agent", body: text("Start with one shared contract, then branch into two independent explorations.\n\n**A · Guided setup:** fewer decisions per screen.\n\n**B · Command center:** faster for experienced users.",
    "从共享契约出发，分成两个独立方向探索。\n\n方案一：分步引导，每屏只做少量决策。\n\n方案二：操作中心，让熟练用户更快完成设置。") + `\n\n*${demoLabel}*`, created_at: messageTime(1) });
  for (const [index, title] of text(["02 · Guided setup", "03 · Command center"], ["02：分步引导", "03：操作中心"]).entries()) {
    const node = uid("node");
    insert("conversation_nodes", { id: node, graph_id: graph.id, parent_id: root.id, fork_message_id: rootMessage,
      agent_id: agents[index + 1].id, agent_revision_id: agents[index + 1].current_revision_id, title,
      inherited_messages: JSON.stringify([{ author: "agent", body: text("Explore onboarding from the same shared contract. Demo data.", "基于同一共享契约探索新手引导。演示数据。") }]),
      position_x: 470, position_y: index * 480, width: 410, height: 450 });
    insert("conversation_messages", { id: uid("msg"), node_id: node, author_kind: "user", body: text(["Explore the guided approach.", "Explore the expert approach."], ["探索分步引导方案。", "探索面向熟练用户的方案。"])[index], created_at: messageTime(2) });
    insert("conversation_messages", { id: uid("msg"), node_id: node, author_kind: "agent", body: text([
      "### One decision at a time\n\nWorkspace → Connect → Review.\n\nProgress is visible, drafts survive validation errors, and the final step previews the exact changes.",
      "### Everything in one place\n\nA compact command center with sensible defaults.\n\nKeep the advanced controls available without turning the first screen into a settings page."
    ], [
      "### 一次只做一个决策\n\n工作区 → 连接 → 确认。\n\n进度始终可见，校验出错后保留草稿，最后一步预览即将发生的变更。",
      "### 在同一处完成操作\n\n紧凑的操作中心，搭配合理默认值。\n\n高级选项随时可用，但不把第一屏变成复杂的设置页。"
    ])[index] + `\n\n*${demoLabel}*`, created_at: messageTime(3) });
  }
  assert.equal(db.prepare("PRAGMA foreign_key_check").all().length, 0);
  assert.equal(db.prepare("SELECT count(*) AS n FROM runs WHERE issue_agent_instance_id=?").get(work[1].issue_agent_instance_id).n, 2);
  const history = db.prepare("SELECT created_at,input FROM runs WHERE issue_agent_instance_id=? ORDER BY created_at").all(work[1].issue_agent_instance_id);
  assert(Date.parse(history[1].created_at) > Date.parse(history[0].created_at));
  assert(JSON.parse(history[1].input)[0].text.includes(text("keyboard-first", "键盘操作")));
  assert.equal(db.prepare("SELECT count(*) AS n FROM runs").get().n, 6);
  const publicHistory = db.prepare("SELECT created_at FROM issue_comments WHERE issue_id=? AND author_kind='agent' ORDER BY created_at,id").all(issue.id);
  assert.equal(publicHistory.length, 5);
  assert(publicHistory.every((row, index) => !index || Date.parse(row.created_at) > Date.parse(publicHistory[index - 1].created_at)));
  assert.equal(db.prepare("SELECT count(*) AS n FROM issue_comment_mentions WHERE status <> 'completed'").get().n, 0);
  assert.equal(db.prepare("SELECT count(*) AS n FROM issue_comment_mentions m JOIN issue_comments c ON c.id=m.comment_id WHERE c.author_agent_id=m.target_agent_id").get().n, 0);
  assert.equal(db.prepare("SELECT count(*) AS n FROM conversation_messages").get().n, 6);
  assert.equal(db.prepare("SELECT count(*) AS n FROM runs WHERE status NOT IN ('succeeded','failed','cancelled','lost')").get().n, 0);
  assert.equal(db.prepare("SELECT count(*) AS n FROM command_outbox WHERE command_name <> 'resource.validate'").get().n, 0);
  const runtime = new DatabaseSync(join(directory, "runtime/runtime.db"), { readOnly: true });
  for (const table of ["native_sessions", "conversation_sessions"]) assert.equal(runtime.prepare(`SELECT count(*) AS n FROM ${table}`).get().n, 0);
  runtime.close();
  db.exec("COMMIT");
} catch (error) { db.exec("ROLLBACK"); throw error; }
db.close();
await writeFile(join(directory, "showcase.json"), JSON.stringify({ origin, locale, project: project.id, computer: computer.id,
  issue: issue.id, graph: graph.id, sessions: work.map((w) => w.issue_agent_instance_id), agents: agents.map((a) => a.id), publicComments }, null, 2));
console.log("Illustrative workspace ready. No model calls or queued executions. Manifest: " + join(directory, "showcase.json"));

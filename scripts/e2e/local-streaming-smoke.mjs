// Opt-in real Provider acceptance. Uses only a disposable local service, no
// production data, no browser trace/HAR, and no credentials in the report.
import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { spawn, spawnSync } from "node:child_process";
import { mkdtemp, readFile, writeFile, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { chromium } from "@playwright/test";
import { auditProviderProcesses } from "./provider-process-audit.mjs";
import { normalizeDisplayText, toolOutput, firstPhasedTool } from "./streaming-text.mjs";

if (process.env.TEAMVA_STREAMING_ACCEPT_REAL_CALLS !== "1") {
  throw new Error("真实 Provider 验收会产生模型调用；确认后设置 TEAMVA_STREAMING_ACCEPT_REAL_CALLS=1。");
}
const root = resolve(dirname(fileURLToPath(import.meta.url)), "../..");
const directory = await mkdtemp(join(process.env.TEAMVA_STREAMING_TEMP_ROOT || tmpdir(), "am-stream-"));
const providers = (process.env.TEAMVA_STREAM_PROVIDERS || "codex,opencode").split(",");
const firstOutputDelay = Number(process.env.TEAMVA_STREAM_TOOL_DELAY_MS || 0);
const compactionMode = process.env.TEAMVA_STREAM_COMPACTION === "1";
const statesOnly = process.env.TEAMVA_STREAM_STATES_ONLY === "1";
assert(Number.isInteger(firstOutputDelay) && firstOutputDelay >= 0 && firstOutputDelay <= 5000);
assert(providers.every((p) => ["codex", "opencode"].includes(p)), "unsupported Provider");
const report = { directory, providers: [], compaction_mode: compactionMode, first_output_delay_ms: firstOutputDelay, results: [], errors: [], started_at: new Date().toISOString() };
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const digest = (text) => createHash("sha256").update(text).digest("hex");
const normalize = normalizeDisplayText;
const terminal = (state) => ["succeeded", "failed", "cancelled", "lost"].includes(state);
const toolResult = toolOutput;
const phasedTool = firstPhasedTool;
const save = (name, value) => writeFile(join(directory, name), JSON.stringify(value, null, 2), { mode: 0o600 });
let server, browser, context, origin, csrf;
const running = new Set();
const observedRuns = new Map();
let serverLog = "";

async function api(method, path, data, key) {
  const response = await context.request.fetch(`${origin}/v1${path}`, {
    method, data, timeout: 30_000,
    headers: { Origin: origin, ...(csrf ? { "X-CSRF-Token": csrf } : {}), ...(key ? { "Idempotency-Key": key } : {}) }
  });
  assert(response.ok(), `${method} ${path}: HTTP ${response.status()}`);
  return response.status() === 204 ? null : (await response.json()).data;
}

async function waitFor(fn, timeout = 30_000) {
  const deadline = Date.now() + timeout;
  while (Date.now() < deadline) {
    const value = await fn();
    if (value) return value;
    await sleep(100);
  }
  throw new Error("bounded wait timed out");
}

async function captureSSE(runID) {
  const events = [];
  const cookie = (await context.cookies(origin)).map((c) => `${c.name}=${c.value}`).join("; ");
  const controller = new AbortController();
  const done = (async () => {
    const response = await fetch(`${origin}/v1/runs/${runID}/events/stream?after_seq=0`, {
      headers: { Cookie: cookie }, signal: controller.signal
    });
    assert.equal(response.status, 200);
    assert.match(response.headers.get("content-type"), /text\/event-stream/);
    const decoder = new TextDecoder();
    let buffer = "";
    for await (const chunk of response.body) {
      buffer += decoder.decode(chunk, { stream: true });
      let split;
      while ((split = buffer.indexOf("\n\n")) >= 0) {
        const frame = buffer.slice(0, split);
        buffer = buffer.slice(split + 2);
        const data = frame.split("\n").filter((line) => line.startsWith("data:")).map((line) => line.slice(5).trimStart()).join("\n");
        if (data) events.push({ ...JSON.parse(data), observed_at: Date.now() });
      }
    }
  })();
  // The rejection remains observable at collection, but never becomes an
  // unhandled promise while the Provider is still working.
  done.catch(() => {});
  return { events, done, stop: () => controller.abort() };
}

async function allEvents(runID) {
  const events = [];
  let after = "0";
  do {
    const page = await api("GET", `/runs/${runID}/events?limit=100&after=${after}`);
    events.push(...page.items);
    after = page.next_cursor;
  } while (after);
  return events;
}

function analyze(events) {
  const counts = {}, parts = new Map(), deltas = new Map(), roles = new Map();
  for (const event of events) {
    counts[event.event_type] = (counts[event.event_type] ?? 0) + 1;
    const p = event.payload;
    if (p.info?.id && p.info?.role) roles.set(p.info.id, p.info.summary ? "compaction" : p.info.role);
    if (p.item?.id) parts.set(p.item.id, p.item);
    if (p.part?.id) parts.set(p.part.id, p.part);
    if (event.event_type === "output.delta") {
      const key = p.part_id || p.item_id || "unknown";
      const previous = deltas.get(key) || { text: "", count: 0 };
      previous.text += p.text || "";
      previous.count++;
      deltas.set(key, previous);
    }
  }
  const groups = [...deltas].map(([id, delta]) => {
    const part = parts.get(id);
    const snapshot = typeof part?.text === "string" ? part.text : null;
    return { id, kind: part?.type ?? "unknown", chunks: delta.count, bytes: Buffer.byteLength(delta.text),
      snapshot_bytes: snapshot === null ? null : Buffer.byteLength(snapshot),
      exact_match: snapshot === null ? null : snapshot === delta.text, sha256: digest(delta.text) };
  });
  const replies = [...parts.values()].filter((p) => (p.type === "agentMessage" || p.type === "text") && !["user", "compaction"].includes(roles.get(p.messageID)) && p.text);
  const thoughts = [...parts.values()].filter((p) => p.type === "reasoning");
  return { counts, groups, replyTexts: replies.map((p) => p.text), thoughts: thoughts.length };
}

async function uiSnapshot(page, surface) {
  return page.evaluate((surface) => {
    const graphReplies = document.querySelectorAll("article.conversation-node .conversation-stream-reply");
    const messages = surface === "issue"
      ? [...document.querySelectorAll('.session-timeline-entry[data-entry-anchor^="assistant"]')]
        .map((e) => e.querySelector("article")?.lastElementChild?.textContent ?? "")
      : [...(graphReplies.length ? graphReplies : document.querySelectorAll("article.conversation-node div.border.bg-surface-subtle.text-text"))]
        .map((e) => e.textContent ?? "").filter((text) => text !== "正在工作…");
    const rows = [...document.querySelectorAll('[class~="group/activity"]')].map((row) => ({
      label: row.querySelector("strong")?.textContent || "", text: row.querySelector("span[title]")?.getAttribute("title") || "",
      detail: row.querySelector("pre")?.textContent || "", expanded: row.querySelector("button")?.getAttribute("aria-expanded"),
      kind: row.getAttribute("data-activity-kind"), status: row.getAttribute("data-activity-status")
    }));
    const scope = surface === "issue" ? document.querySelector(".session-sheet-panel") : document.querySelector("article.conversation-node");
    const loading = [...(scope?.querySelectorAll("span,p") ?? [])].map((e) => e.textContent?.trim() ?? "")
      .filter((text) => ["正在生成…", "正在工作…", "正在发送…", "正在压缩上下文…"].includes(text));
    return { at: Date.now(), messages, rows, loading, generating: loading.length > 0,
      active_rows: rows.filter((r) => ["running", "pending", "started", "inProgress", "in_progress"].includes(r.status)).length };
  }, surface);
}

async function observeRun(page, runID, prefix, surface = "issue", graphID) {
  observedRuns.set(runID, { page, prefix, surface });
  const stream = await captureSSE(runID);
  running.add(runID);
  const samples = [];
  const deadline = Date.now() + 240_000;
  let status = "queued";
  try {
    while (Date.now() < deadline) {
      const sample = await uiSnapshot(page, surface);
      sample.status = status;
      samples.push(sample);
      if (samples.length % 5 === 0) status = (await api("GET", `/runs/${runID}`)).status;
      if (terminal(status)) break;
      await sleep(100);
    }
    assert(terminal(status), "Provider run timed out");
    running.delete(runID);
    await Promise.race([stream.done, sleep(5000).then(() => { throw new Error("SSE did not close after terminal"); })]);
    const events = await allEvents(runID);
    await save(`${prefix}-events.json`, events);
    await save(`${prefix}-sse.json`, stream.events);
    await save(`${prefix}-ui-samples.json`, samples);
    const analysis = analyze(events);
    const finalRun = await api("GET", `/runs/${runID}`);
    const terminalEvents = events.filter((e) => ["run.completed", "run.finished", "run.error"].includes(e.event_type));
    const compactions = events.filter((e) => e.event_type === "context.compaction");
    let persistedResponseMatches = null;
    if (graphID) {
      const graph = await api("GET", `/conversation-graphs/${graphID}`);
      const response = graph.messages.find((m) => m.run_id === runID && m.author_kind === "agent");
      persistedResponseMatches = response?.status === "published" && response.body.trim() === analysis.replyTexts.at(-1)?.trim();
    }
    assert.deepEqual(events.map((e) => e.seq), events.map((_, i) => i + 1), "persisted sequence gap");
    const wireEvents = stream.events.map((e) => [e.seq, e.event_type, e.payload]);
    const persistedEvents = events.map((e) => [e.seq, e.event_type, e.payload]);
    const sseExact = JSON.stringify(wireEvents) === JSON.stringify(persistedEvents);
    await sleep(2200);
    const live = await uiSnapshot(page, surface);
    await page.screenshot({ path: join(directory, `${prefix}-live.png`), animations: "disabled" });
    await page.reload({ waitUntil: "domcontentloaded" });
    await sleep(3000);
    if (surface === "graph") {
      for (const button of await page.getByRole("button", { name: "查看执行输出", exact: true }).all()) await button.click();
      await sleep(1000);
    }
    const replay = await uiSnapshot(page, surface);
    let expandedTools = 0;
    for (const row of await page.locator('[class~="group/activity"]').all()) {
      if (await row.locator("strong").first().textContent() === "思考" || !await row.locator("pre").count()) continue;
      const button = row.locator("button").first();
      if (await button.getAttribute("aria-expanded") === "false") await button.click();
      if (await button.getAttribute("aria-expanded") === "true") expandedTools++;
    }
    await save(`${prefix}-ui-final.json`, { live, replay });
    await page.screenshot({ path: join(directory, `${prefix}-replay.png`), animations: "disabled" });
    const result = {
      provider: prefix, status, events: events.length, sse_exact: sseExact,
      persisted_response_matches: persistedResponseMatches,
      terminal_events: terminalEvents.length, finished_at_present: Boolean(finalRun.finished_at), started_at_present: Boolean(finalRun.started_at),
      started_events: analysis.counts["run.started"] ?? 0,
      compactions: compactions.map((e) => ({ seq: e.seq, id: e.payload.id, status: e.payload.status })),
      provider_identity_matches: events.filter((e) => e.payload.provider_id).every((e) => e.payload.provider_id === prefix.split("-")[0]),
      sse_total: stream.events.length, sse_unique: new Set(stream.events.map((e) => e.seq)).size,
      ...analysis, replies_live: live.messages.length, replies_replay: replay.messages.length,
      progressive_reply_states: new Set(samples.filter((s) => !terminal(s.status)).map((s) => s.messages.join("\n")).filter(Boolean)).size,
      progressive_thought_states: new Set(samples.filter((s) => !terminal(s.status)).map((s) => s.rows.filter((r) => r.label === "思考").map((r) => r.text).filter((text) => text.trim()).join("\n")).filter(Boolean)).size,
      final_reply_matches: analysis.replyTexts.every((text) => replay.messages.some((m) => normalize(m) === normalize(text))),
      reply_count_matches: analysis.replyTexts.length === replay.messages.length,
      replay_stable: JSON.stringify(live.messages.map(normalize)) === JSON.stringify(replay.messages.map(normalize)),
      terminal_spinner_stopped: !live.generating && !replay.generating && live.active_rows === 0 && replay.active_rows === 0,
      loading_states: [...new Set(samples.flatMap((s) => s.loading))],
      sampled_run_states: [...new Set(samples.map((s) => s.status))],
      compaction_visible: replay.rows.some((r) => r.kind === "compaction"),
      compaction_progress_states: [...new Set(samples.flatMap((s) => s.rows.filter((r) => r.kind === "compaction").map((r) => r.status)))],
      tool_labels: replay.rows.filter((r) => r.label !== "思考").map((r) => r.label),
      expanded_tool_details: expandedTools,
      expected_tool_output_visible: surface !== "issue" || (["STREAM_TOOL_A", "STREAM_TOOL_B"].every((s) => toolResult(phasedTool(replay.rows)).includes(s)) &&
        toolResult(replay.rows.find((r) => r.kind === "tool" && r.text.includes("STREAM_EXPECTED_FAILURE"))).includes("STREAM_EXPECTED_FAILURE")),
      partial_tool_output_visible: surface !== "issue" || samples.some((s) => toolResult(phasedTool(s.rows)).includes("STREAM_TOOL_A") && !toolResult(phasedTool(s.rows)).includes("STREAM_TOOL_B")),
      expected_failure_status_visible: surface !== "issue" || replay.rows.some((r) => r.status === "failed" && toolResult(r).includes("STREAM_EXPECTED_FAILURE"))
    };
    delete result.replyTexts;
    report.results.push(result);
    console.log(JSON.stringify(result));
    if (!sseExact || persistedResponseMatches === false || terminalEvents.length !== 1 || !result.finished_at_present || !result.started_at_present || result.started_events !== 1 || !result.provider_identity_matches || !result.final_reply_matches || !result.reply_count_matches || !result.replay_stable || !result.terminal_spinner_stopped || !result.expected_tool_output_visible || !result.partial_tool_output_visible || !result.expected_failure_status_visible ||
      result.groups.some((g) => g.exact_match === false) || result.progressive_reply_states < 2 || status !== "succeeded" ||
      (compactionMode && (!compactions.some((e) => e.payload.status === "completed") || !result.compaction_visible)) ||
      (surface === "issue" && !["读取任务", "发布评论"].every((label) => result.tool_labels.includes(label)))) process.exitCode = 1;
  } catch (error) {
    await save(`${prefix}-incomplete-sse.json`, stream.events);
    await save(`${prefix}-incomplete-ui-samples.json`, samples);
    try { await save(`${prefix}-incomplete-events.json`, await allEvents(runID)); } catch {}
    throw error;
  } finally { stream.stop(); }
}

async function stateScenarios(page, project, agent, provider, service, model) {
  for (const scenario of ["failure", "stop"]) {
    const scenarioAgent = await api("POST", `/computers/${project.computer_id}/agents`, {
      name: `State ${scenario} ${provider}`, agent_service_id: service.id, skill_version_ids: [],
      model: scenario === "failure" ? (provider === "codex" ? "teamva-acceptance-invalid-model" : "teamva-acceptance-invalid/missing") : model,
      system_prompt: "隔离状态验收。只执行用户明确给出的公开测试命令；不读取文件、凭据、环境变量或工作区外数据。不创建子代理。"
    });
    const title = `State ${provider} ${scenario}`;
    const graph = await api("POST", `/projects/${project.id}/conversation-graphs`, { title, agent_id: scenarioAgent.id });
    await page.goto(`${origin}/conversations`);
    await page.getByRole("button", { name: new RegExp(title) }).first().click();
    const card = page.locator(`[data-node-id="${graph.nodes[0].id}"]`);
    await card.getByRole("textbox", { name: "消息输入" }).fill(scenario === "failure" ? "Reply with STATE_OK. Do not use tools."
      : "只输出纯文本，不使用工具。依次输出1000行，每行以 STOP_LINE_ 加行号开头，再写20个汉字解释流式输出，不省略、不提前总结。此长回复用于测试中途停止。");
    const responsePromise = page.waitForResponse((r) => r.url().endsWith(`/conversation-nodes/${graph.nodes[0].id}/messages`) && r.request().method() === "POST");
    await card.getByRole("button", { name: "发送", exact: true }).click();
    const response = await responsePromise;
    assert(response.ok(), "state scenario send failed");
    const message = (await response.json()).data;
    const runID = message.run_id, stream = await captureSSE(runID);
    running.add(runID);
    observedRuns.set(runID, { page, prefix: `${provider}-${scenario}`, surface: "graph" });
    try {
      if (scenario === "stop") {
        await waitFor(() => stream.events.some((e) => e.event_type === "output.delta" && typeof e.payload.text === "string" && e.payload.text.trim()), 90_000);
        await api("POST", `/runs/${runID}/stop`, { mode: "force" }, `state-stop-${runID}`);
      }
      const run = await waitFor(async () => { const value = await api("GET", `/runs/${runID}`); return terminal(value.status) ? value : null; }, 90_000);
      await sleep(3000);
      const ui = await uiSnapshot(page, "graph");
      const events = await allEvents(runID);
      await save(`${provider}-${scenario}-events.json`, events);
      await save(`${provider}-${scenario}-sse.json`, stream.events);
      await save(`${provider}-${scenario}-ui.json`, ui);
      await page.screenshot({ path: join(directory, `${provider}-${scenario}.png`) });
      const graphState = await api("GET", `/conversation-graphs/${graph.graph.id}`);
      const result = { provider, scenario, model: scenario === "stop" ? model : "invalid-model", status: run.status,
        finished_at_present: Boolean(run.finished_at), spinner_stopped: !ui.generating, active_activity_rows: ui.active_rows,
        reply_status: graphState.messages.find((m) => m.run_id === runID && m.author_kind === "agent")?.status,
        terminal_events: events.filter((e) => ["run.completed", "run.finished", "run.error"].includes(e.event_type))
          .map((e) => ({ seq: e.seq, type: e.event_type, status: e.payload.status, reason: e.payload.reason })) };
      report.results.push(result); console.log(JSON.stringify(result));
      if (run.status !== (scenario === "stop" ? "cancelled" : "failed") || !result.finished_at_present || !result.spinner_stopped || ui.active_rows !== 0 || result.reply_status !== "failed") process.exitCode = 1;
      running.delete(runID);
    } finally { stream.stop(); }
  }
}

try {
  const binary = join(directory, "teamva-local");
  const generated = spawnSync("go", ["-C", join(root, "node"), "generate", "./provider/catalog"], { stdio: "inherit" });
  assert.equal(generated.status, 0, "Provider generation failed");
  const build = spawnSync("go", ["-C", join(root, "server"), "build", "-o", binary, "./cmd/local"], { stdio: "inherit" });
  assert.equal(build.status, 0, "backend build failed");
  const env = { ...process.env, GIN_MODE: "release", TEAMVA_LOCAL_DATA_DIR: join(directory, "data"),
    TEAMVA_LOCAL_WEB_DIR: process.env.TEAMVA_STREAM_WEB_DIR || join(root, "web/dist"), TEAMVA_LOCAL_HTTP_ADDR: "127.0.0.1:0",
    TEAMVA_LOCAL_ALLOW_EXTERNAL: "false", TEAMVA_LOCAL_DISABLE_RUNTIME: "false" };
  delete env.TEAMVA_LOCAL_ACCESS_TOKEN;
  delete env.TEAMVA_LOCAL_BOOTSTRAP_NONCE;
  if (compactionMode) {
    env.TEAMVA_STREAM_CODEX_COMPACT_LIMIT = "32000";
    // Test-only model metadata: lower the local auto-compaction threshold,
    // never change the real account's configuration or synthesize events.
    env.OPENCODE_CONFIG_CONTENT = JSON.stringify({ compaction: { auto: true, prune: false, reserved: 4096 },
      provider: { deepseek: { models: { "deepseek-v4-flash": { limit: { context: 28000, input: 24000, output: 2048 } } } } } });
  }
  if (providers.includes("codex")) {
    const target = process.env.TEAMVA_CODEX_PATH || spawnSync("which", ["codex"], { encoding: "utf8" }).stdout.trim();
    assert(target, "Codex executable not found");
    const shim = join(directory, "codex-observer.mjs");
    await writeFile(shim, `#!${process.execPath}\nimport ${JSON.stringify(new URL("./provider-stream-observer.mjs", import.meta.url).href)};\n`, { mode: 0o700 });
    env.TEAMVA_CODEX_PATH = shim;
    env.TEAMVA_STREAM_NATIVE_BINARY = target;
    env.TEAMVA_STREAM_NATIVE_METRICS = join(directory, "codex-native-metrics.ndjson");
  }
  server = spawn(binary, [], { env, stdio: ["ignore", "pipe", "pipe"] });
  server.stdout.on("data", (b) => { serverLog += b.toString(); });
  server.stderr.on("data", (b) => { serverLog += b.toString(); });
  origin = await waitFor(async () => {
    if (server.exitCode !== null || server.signalCode !== null) throw new Error("isolated server exited before readiness; inspect private service.log");
    try { return JSON.parse(await readFile(join(directory, "data/runtime.json"), "utf8")).origin; } catch { return null; }
  }, 120_000);
  assert.equal(new URL(origin).hostname, "127.0.0.1");
  browser = await chromium.launch({ executablePath: process.env.TEAMVA_CHROME_BIN || "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome", args: ["--no-proxy-server"] });
  context = await browser.newContext({ viewport: { width: 1440, height: 1000 }, locale: "zh-CN" });
  csrf = (await api("POST", "/auth/local-session", {})).csrf_token;
  const computer = (await api("GET", "/computers")).items[0];
  for (const provider of providers) {
    const service = await waitFor(async () => (await api("GET", `/computers/${computer.id}/agent-services`)).items.find((s) => s.provider === provider && s.status === "ready"), 90_000);
    const models = service.capabilities.models;
    const model = process.env[`TEAMVA_STREAM_MODEL_${provider.toUpperCase()}`] ||
      (provider === "opencode" && models.includes("deepseek/deepseek-v4-flash") ? "deepseek/deepseek-v4-flash" : models[0]);
    report.providers.push({ provider, version: service.provider_version, model });
    const project = await api("POST", "/projects", { computer_id: computer.id, name: `Stream ${provider}`, description: "隔离流式验收" });
    const agent = await api("POST", `/computers/${computer.id}/agents`, { name: `Stream ${provider}`, agent_service_id: service.id,
      model, system_prompt: "这是隔离验收。只操作本次任务注入的 FileBridge 文件，不读取任何凭据、环境变量或工作区外文件。不要创建子代理。严格执行用户测试步骤。", skill_version_ids: [] });
    const page = await context.newPage();
    page.on("pageerror", (error) => report.errors.push({ provider, error: error.message }));
    await page.addInitScript(({ computerId, projectId }) => sessionStorage.setItem("teamva-selection", JSON.stringify({ computerId, projectId })), { computerId: computer.id, projectId: project.id });
    if (statesOnly) { await stateScenarios(page, project, agent, provider, service, model); await page.close(); continue; }
    if (process.env.TEAMVA_STREAM_SURFACE !== "graph") {
    const issue = await api("POST", `/projects/${project.id}/issues`, { title: `Streaming ${provider}`, description: "验证流式事件，不修改项目源码。" });
    const streamCommand = firstOutputDelay
      ? `node -e "setTimeout(()=>{console.log('STREAM_TOOL_A');setTimeout(()=>console.log('STREAM_TOOL_B'),1200)},${firstOutputDelay})"`
      : `node -e "console.log('STREAM_TOOL_A');setTimeout(()=>console.log('STREAM_TOOL_B'),1200)"`;
    const prompt = `${compactionMode ? "以下是仅用于触发真实上下文压缩的无敏感填充，不要复述：\n" + "stable-context-token ".repeat(2600) + "\n填充结束。\n" : ""}请按顺序执行，不要合并下面两次原生命令工具调用：
1. 先直接向用户输出正文 STREAM_BEFORE_TOOL，并简述你将进行的验证（约 80 字）。
2. 调用原生命令工具执行 ${streamCommand}。如果工具支持 yield_time_ms 参数，设为 1000。注意 yield_time_ms 是等待首批输出的窗口，不是 timeout；如果工具只有 timeout 参数，应设为 15000 毫秒，禁止设为 1000 毫秒。
3. 再调用一次原生命令工具执行 node -e "console.log('STREAM_EXPECTED_FAILURE');process.exit(3)"，这是预期失败，不需要修复。
4. 使用当前 Run 的 FileBridge 调用 issue_get，随后用 issue_comment_create 发布公共总结。两个工具可用相同请求文件名 req-stream.json（各自目录不同）。读取并核对工具响应。
5. 最终正文不要重复整段公共总结。以 STREAM_FINAL_BEGIN 开头，输出 18 行中文验证说明（每行 20-30 字，用“段01：”等前缀，不用 Markdown 列表），最后一行 STREAM_FINAL_END。
所有标记只是公开测试文本，不读取或输出任何密钥或环境变量。`;
    await api("POST", `/issues/${issue.id}/comments`, { body: prompt, mentioned_agent_ids: [agent.id] });
    const run = await waitFor(async () => (await api("GET", `/issues/${issue.id}/runs`)).items[0]);
    running.add(run.id);
    await page.goto(`${origin}/issues?issue=${issue.id}&session=${run.issue_agent_instance_id}`, { waitUntil: "domcontentloaded" });
    await observeRun(page, run.id, `${provider}-issue`);
    }
    if (process.env.TEAMVA_STREAM_SURFACE !== "issue") {
      const graph = await api("POST", `/projects/${project.id}/conversation-graphs`, { title: `Stream graph ${provider}`, agent_id: agent.id });
      const nodeID = graph.nodes[0].id;
      await page.goto(`${origin}/conversations`, { waitUntil: "domcontentloaded" });
      await page.getByRole("button", { name: new RegExp(`Stream graph ${provider}`) }).first().click();
      const body = "不使用任何工具，也不发 Issue 评论。这是流式显示验收。请给出两种图布局方案的简要比较，然后输出 40 行中文说明（每行约25字，不用Markdown列表，以段01：等标记），正文以 GRAPH_STREAM_BEGIN 开头、以 GRAPH_STREAM_END 结束。";
      const card = page.locator(`[data-node-id="${nodeID}"]`);
      await card.getByRole("textbox", { name: "消息输入" }).fill(body);
      const [response] = await Promise.all([
        page.waitForResponse((r) => r.url().endsWith(`/conversation-nodes/${nodeID}/messages`) && r.request().method() === "POST"),
        card.getByRole("button", { name: "发送", exact: true }).click()
      ]);
      assert(response.ok(), "graph send failed");
      const message = (await response.json()).data;
      running.add(message.run_id);
       await observeRun(page, message.run_id, `${provider}-graph`, "graph", graph.graph.id);
    }
    await page.close();
  }
} catch (error) {
  report.errors.push({ error: error.message });
  process.exitCode = 1;
} finally {
  for (const id of running) {
    try {
      await api("POST", `/runs/${id}/stop`, { mode: "force" }, `cleanup-${id}`);
      await sleep(1500);
      const run = await api("GET", `/runs/${id}`);
      const observed = observedRuns.get(id);
      const ui = observed ? await uiSnapshot(observed.page, observed.surface) : null;
      report.cleanup_states ??= [];
      report.cleanup_states.push({ run_id: id, status: run.status, finished_at_present: Boolean(run.finished_at), loading_stopped: ui ? !ui.generating : null });
      if (observed) await save(`${observed.prefix}-cancelled-events.json`, await allEvents(id));
    } catch {}
  }
  if (browser) await browser.close();
  if (server && server.exitCode === null && server.signalCode === null) {
    server.kill("SIGTERM");
    await Promise.race([new Promise((r) => server.once("exit", r)), sleep(15_000)]);
    if (server.exitCode === null && server.signalCode === null) {
      server.kill("SIGKILL");
      await new Promise((r) => server.once("exit", r));
    }
  }
  // Provider instance directories can contain copied credentials or auth
  // symlinks. Remove only this test's private instances after shutdown; keep
  // the event/SSE/screenshot evidence, never export Provider authentication.
  try {
    let remaining = auditProviderProcesses(directory);
    if (remaining.length) { await sleep(2000); remaining = auditProviderProcesses(directory); }
    report.remaining_test_processes = remaining;
    if (remaining.length) { report.errors.push({ error: "isolated Provider processes remained after shutdown" }); process.exitCode = 1; }
  } catch { report.errors.push({ error: "could not audit isolated Provider process cleanup" }); process.exitCode = 1; }
  await rm(join(directory, "data/runtime/agents"), { recursive: true, force: true });
  await writeFile(join(directory, "service.log"), serverLog, { mode: 0o600 });
  report.finished_at = new Date().toISOString();
  await save("report.json", report);
  console.log(`验收证据：${directory}`);
  if (report.errors.length) { console.log(JSON.stringify(report.errors)); process.exitCode = 1; }
}

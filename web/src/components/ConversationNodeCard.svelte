<script lang="ts">
  import { Button, Checkbox, Input, Select, Textarea } from "../ui";
  import { Bot, GitFork, GripVertical, LoaderCircle, Pencil, Send, Square, Target, Trash2, User } from "lucide-svelte";
  import ErrorNotice from "./ErrorNotice.svelte";
  import RichText from "./RichText.svelte";
  import ConversationRunActivity from "./ConversationRunActivity.svelte";
  import ConversationMessageOutcome from "./ConversationMessageOutcome.svelte";
  import { t } from "../lib/i18n.svelte";
  import { formatTime } from "../lib/helpers";
  import { idempotencyKey } from "../lib/api";
  import { goalBudgetExhausted } from "../lib/goalBudget";
  import { tick } from "svelte";
  import type { Agent, ConversationGoal, ConversationMessage, ConversationNode } from "../lib/types";

  let {
    node,
    position,
    messages,
    agents,
    selected,
    busy,
    goal = null,
    onCreateGoal,
    onGoalAction,
    onChangeAgent,
    onRename,
    scale = 1,
    onSelect,
    onSend,
    onFork,
    onDelete,
    onStop,
    onDragStart,
    onDragMove,
    onDragEnd,
    onResize,
    onRunSettled
  }: {
    node: ConversationNode;
    position: { x: number; y: number };
    messages: ConversationMessage[];
    agents: Agent[];
    selected: boolean;
    busy: boolean;
    goal?: ConversationGoal | null;
    onCreateGoal?: (objective: string, tokenBudget: number | null, key: string, binding: AgentBinding) => Promise<unknown>;
    onGoalAction?: (goal: ConversationGoal, action: "pause" | "resume" | "cancel") => Promise<unknown>;
    onChangeAgent?: (agentId: string) => Promise<unknown>;
    onRename?: (title: string) => Promise<unknown>;
    scale?: number;
    onSelect: () => void;
    onSend: (body: string, key: string, binding: AgentBinding) => Promise<unknown>;
    onFork: (message: ConversationMessage) => void;
    onDelete: () => void;
    onStop: (runId: string) => Promise<unknown>;
    onDragStart: (event: PointerEvent) => void;
    onDragMove: (event: PointerEvent) => void;
    onDragEnd: (event: PointerEvent) => void;
    onResize: (size: { width: number; height: number }) => void;
    onRunSettled?: () => void;
  } = $props();

  type AgentBinding = { expected_agent_id: string; expected_agent_revision_id: string };

  const minimumWidth = 300;
  const maximumWidth = 1_000;
  const minimumHeight = 260;
  const maximumHeight = 1_600;

  let draft = $state("");
  let pending = $state(false);
  let error = $state<unknown>(null);
  let messageViewport = $state<HTMLDivElement>();
  let stickToBottom = $state(true);
  function followOutput() {
    if (!stickToBottom) return;
    void tick().then(() => {
      if (messageViewport && stickToBottom) messageViewport.scrollTop = messageViewport.scrollHeight;
    });
  }
  $effect(() => { void messages.length; void node.status; followOutput(); });
  let submission: { body: string; key: string; goal: boolean; budget: number | null; agentId: string; revisionId: string } | undefined;
  let changingAgent = $state(false);
  let selectedAgentId = $state("");
  let editingTitle = $state(false);
  let titleDraft = $state("");
  let renaming = $state(false);
  let goalArmed = $state(false);
  let budgetEnabled = $state(true);
  let budget = $state(2_000_000);
  const liveGoal = $derived(goal && !["completed", "cancelled"].includes(goal.status));
  const validBudget = $derived(!budgetEnabled || Number.isSafeInteger(budget) && budget >= 1000 && budget <= 100_000_000);
  const goalLabel = $derived(goal ? t(goal.status === "active"
    ? (goal.phase === "evaluating" ? "issues.goalEvaluating" : "issues.goalActive")
    : ({ paused: "issues.goalPaused", blocked: "issues.goalBlocked", budget_reached: "issues.goalBudgetReached",
      completed: "issues.goalCompleted", cancelled: "issues.goalCancelled" }[goal.status])) : "");
  let resized = $state<{ width: number; height: number } | null>(null);
  let resizing = $state<{ startX: number; startY: number; originWidth: number; originHeight: number } | null>(null);
  const agent = $derived(agents.find((item) => item.id === node.agent_id));
  const availableAgents = $derived(agents.filter((item) => item.status === "active" && Boolean(item.current_revision_id)));
  const revisionId = $derived(node.agent_revision_id ?? agent?.current_revision_id ?? "");
  $effect(() => { selectedAgentId = node.agent_id; });
  const activeRunId = $derived(messages.find((message) => message.status === "working")?.run_id);
  const running = $derived(node.status === "running");
  const nodeBusy = $derived(node.status !== "idle" && node.status !== "failed");
  const canSend = $derived(
    Boolean(draft.trim()) && !liveGoal && !node.locked && !busy && !pending && !changingAgent && !nodeBusy &&
    agent?.status === "active" && Boolean(agent.current_revision_id) && Boolean(revisionId) &&
    (!goalArmed || validBudget && [...draft.trim()].length <= 5000)
  );
  const inherited = $derived(node.inherited_messages ?? []);
  const nodeWidth = $derived(resized?.width ?? node.width ?? 380);
  const nodeHeight = $derived(resized?.height ?? node.height ?? 560);

  // 节点尺寸落库后释放本地预览，提交失败时仍保留用户拖出的尺寸。
  $effect(() => {
    if (!resized) return;
    if (Math.round(resized.width) === Math.round(node.width) && Math.round(resized.height) === Math.round(node.height)) {
      resized = null;
    }
  });

  function clamp(value: number, minimum: number, maximum: number): number {
    return Math.min(maximum, Math.max(minimum, value));
  }

  function startResize(event: PointerEvent) {
    if (event.button !== 0 || pending || running) return;
    event.preventDefault();
    event.stopPropagation();
    resizing = {
      startX: event.clientX, startY: event.clientY,
      originWidth: nodeWidth, originHeight: nodeHeight
    };
    (event.currentTarget as HTMLElement).setPointerCapture(event.pointerId);
  }

  function moveResize(event: PointerEvent) {
    if (!resizing) return;
    const factor = scale > 0 ? scale : 1;
    resized = {
      width: clamp(resizing.originWidth + (event.clientX - resizing.startX) / factor, minimumWidth, maximumWidth),
      height: clamp(resizing.originHeight + (event.clientY - resizing.startY) / factor, minimumHeight, maximumHeight)
    };
  }

  function endResize(event: PointerEvent) {
    if (!resizing) return;
    resizing = null;
    const handle = event.currentTarget as HTMLElement;
    if (handle.hasPointerCapture(event.pointerId)) handle.releasePointerCapture(event.pointerId);
    if (event.type === "pointercancel" || !resized) return;
    if (Math.round(resized.width) === Math.round(node.width) && Math.round(resized.height) === Math.round(node.height)) {
      resized = null;
      return;
    }
    onResize({ width: resized.width, height: resized.height });
  }

  async function submit(event: SubmitEvent) {
    event.preventDefault();
    if (!canSend) return;
    const body = draft.trim();
    const tokenBudget = goalArmed && budgetEnabled ? budget : null;
    const binding = { expected_agent_id: node.agent_id, expected_agent_revision_id: revisionId };
    if (!submission || submission.body !== body || submission.goal !== goalArmed || submission.budget !== tokenBudget ||
      submission.agentId !== binding.expected_agent_id || submission.revisionId !== binding.expected_agent_revision_id) {
      submission = { body, key: idempotencyKey(goalArmed ? "conversation-goal" : "conversation-message"), goal: goalArmed,
        budget: tokenBudget, agentId: binding.expected_agent_id, revisionId: binding.expected_agent_revision_id };
    }
    pending = true;
    error = null;
    try {
      if (goalArmed && onCreateGoal) {
        await onCreateGoal(body, tokenBudget, submission.key, binding);
        goalArmed = false;
      } else {
        await onSend(body, submission.key, binding);
      }
      if (draft.trim() === body) draft = "";
      submission = undefined;
    } catch (cause) {
      error = cause;
    } finally {
      pending = false;
    }
  }

  async function changeAgent(agentId: string) {
    if (!onChangeAgent || node.locked || busy || pending || changingAgent || nodeBusy || liveGoal || agentId === node.agent_id) return;
    if (!availableAgents.some((item) => item.id === agentId)) return;
    changingAgent = true;
    error = null;
    try { await onChangeAgent(agentId); } catch (cause) { error = cause; }
    finally { changingAgent = false; selectedAgentId = node.agent_id; }
  }

  async function rename(event: SubmitEvent) {
    event.preventDefault();
    const title = titleDraft.trim();
    if (!onRename || !title || renaming) return;
    renaming = true;
    error = null;
    try { await onRename(title); editingTitle = false; } catch (cause) { error = cause; }
    finally { renaming = false; }
  }

  async function stopRun() {
    if (!activeRunId || pending) return;
    pending = true;
    error = null;
    try { await onStop(activeRunId); } catch (cause) { error = cause; } finally { pending = false; }
  }

  async function changeGoal(action: "pause" | "resume" | "cancel") {
    if (!goal || !onGoalAction || pending) return;
    if (action === "resume" && goalBudgetExhausted(goal)) return;
    pending = true;
    error = null;
    try { await onGoalAction(goal, action); } catch (cause) { error = cause; } finally { pending = false; }
  }
</script>

<article
  class="conversation-node pointer-events-auto absolute flex flex-col overflow-hidden rounded-lg border bg-surface shadow-sm {node.locked ? 'border-dashed border-border-strong' : ''} {selected ? 'border-accent ring-2 ring-accent/25' : 'border-border'}"
  style:left={`${position.x}px`}
  style:top={`${position.y}px`}
  style:width={`${nodeWidth}px`}
  style:height={`${nodeHeight}px`}
  data-node-id={node.id}
  onpointerdown={(event) => event.stopPropagation()}
>
  <header class="flex min-h-11 items-center gap-2 border-b border-border bg-surface-subtle px-3">
    <button
      class="grid size-7 shrink-0 touch-none cursor-grab place-items-center rounded-sm border-0 bg-transparent text-muted active:cursor-grabbing max-md:hidden"
      type="button"
      aria-label={t("conversations.dragNode")}
      title={t("conversations.dragNode")}
      onpointerdown={onDragStart}
      onpointermove={onDragMove}
      onpointerup={onDragEnd}
      onpointercancel={onDragEnd}
    ><GripVertical size={12} /></button>
    {#if editingTitle}
      <form class="flex min-w-0 flex-1 flex-wrap items-center gap-1 py-1" onsubmit={rename}>
        <Input class="min-w-0 flex-1" controlSize="sm" bind:value={titleDraft} maxlength={200} required aria-label={t("conversations.forkNodeTitle")} disabled={renaming} />
        <Button controlSize="sm" type="submit" disabled={!titleDraft.trim() || renaming}>{t("common.save")}</Button>
        <Button controlSize="sm" type="button" disabled={renaming} onclick={() => editingTitle = false}>{t("common.cancel")}</Button>
      </form>
    {:else}
    <button class="min-w-0 flex-1 cursor-pointer border-0 bg-transparent text-left" type="button" onclick={onSelect}>
      <strong class="block truncate text-xs font-semibold">{node.title || t("conversations.untitledNode")}</strong>
      <span class="mt-0.5 flex items-center gap-1.5 text-2xs text-muted">
        <Bot size={11} />
        <span class="truncate">{agent?.name ?? node.agent_id}</span>
        {#if node.status === "running"}
          <span class="size-1.5 rounded-full bg-accent"></span>
        {:else if node.status === "failed"}
          <span class="size-1.5 rounded-full bg-danger"></span>
        {/if}
        {#if node.locked}
          <span class="inline-flex items-center gap-1 rounded-full border border-border-strong px-1.5 py-px font-medium">
            <GitFork size={9} />{t("conversations.nodeForked")}
          </span>
        {/if}
      </span>
    </button>
    {#if onRename}<Button type="button" icon controlSize="sm" variant="ghost" aria-label={`${t("common.edit")} · ${t("conversations.forkNodeTitle")}`} onclick={() => { titleDraft = node.title; editingTitle = true; }}><Pencil size={12} /></Button>{/if}
    {/if}
    <button
      class="grid size-6 shrink-0 cursor-pointer place-items-center rounded-sm border-0 bg-transparent text-muted hover:bg-surface-hover hover:text-danger"
      type="button"
      aria-label={t("conversations.deleteNode")}
      title={t("conversations.deleteNode")}
      onclick={onDelete}
      disabled={nodeBusy || pending || busy || changingAgent || Boolean(liveGoal)}
    ><Trash2 size={12} /></button>
  </header>

  <div class="flex min-h-0 flex-1 flex-col gap-2 overflow-y-auto overscroll-contain px-3 py-2.5" bind:this={messageViewport} data-conversation-message-scroll={node.id}
    onscroll={() => { if (messageViewport) stickToBottom = messageViewport.scrollHeight - messageViewport.scrollTop - messageViewport.clientHeight < 72; }}>
    {#if inherited.length}
      <div class="grid gap-1.5 rounded-md border border-dashed border-border-strong/70 bg-surface-subtle/60 p-2" aria-label={t("conversations.inheritedHistory")}>
        <span class="text-[10px] font-medium uppercase tracking-wide text-muted">{t("conversations.inheritedHistory")}</span>
        {#each inherited as entry, index (index)}
          <div class="flex gap-1.5 {entry.author === 'user' ? 'justify-end' : 'justify-start'}">
            <div class="w-fit max-w-[85%] rounded-md px-2.5 py-1.5 text-xs leading-[1.6] {entry.author === 'user' ? 'user-message bg-ink/70 text-canvas' : 'border border-border bg-surface text-text'}">
              <RichText text={entry.body} tone={entry.author === "user" ? "inverse" : "default"} />
            </div>
          </div>
        {/each}
      </div>
    {/if}
    {#if !messages.length && !inherited.length}
      <p class="m-0 py-2 text-center text-2xs text-muted">{t("conversations.emptyNode")}</p>
    {/if}
    {#each messages as message (message.id)}
      <span id={`message-${message.id}`} class="block" aria-hidden="true"></span>
      <div class="flex gap-1.5 {message.author_kind === 'user' ? 'justify-end' : 'justify-start'}">
        {#if message.author_kind === "agent"}
          <span class="mt-0.5 grid size-5 shrink-0 place-items-center rounded-full bg-accent-subtle text-accent"><Bot size={11} /></span>
        {/if}
        <div class="min-w-0 w-fit max-w-[85%]">
          {#if message.author_kind === "agent" && message.run_id}
            <ConversationRunActivity runId={message.run_id} active={message.status === "working"}
              publishedBody={message.status === "published" ? message.body : ""} onSettled={onRunSettled} onUpdate={followOutput} />
          {/if}
          {#if message.status !== "working" || !message.run_id}
          <div
            class="rounded-md px-2.5 py-1.5 text-xs leading-[1.6] {message.author_kind === 'user' ? 'user-message bg-ink text-canvas' : 'border border-border bg-surface-subtle text-text'}"
          >
            {#if message.status === "working"}
              <span class="inline-flex items-center gap-1.5 text-secondary"><LoaderCircle size={11} class="animate-spin" />{t("conversations.working")}</span>
            {:else if message.status === "failed"}
              {#if message.run_id}<ConversationMessageOutcome runId={message.run_id} />
              {:else}<span class="text-danger">{t("conversations.failed")}</span>{/if}
            {:else}
              <div class:conversation-stream-reply={message.author_kind === "agent"}><RichText text={message.body} tone={message.author_kind === "user" ? "inverse" : "default"} /></div>
            {/if}
          </div>
          {/if}
          <div class="mt-1 flex items-center gap-2 text-[10px] text-muted {message.author_kind === 'user' ? 'justify-end' : ''}">
            <span>{formatTime(message.created_at)}</span>
            {#if message.author_kind === "agent" && message.status === "published" && !running && !liveGoal}
              <button
                class="inline-flex cursor-pointer items-center gap-1 rounded-sm border-0 bg-transparent p-0 text-[10px] font-medium text-accent hover:underline"
                type="button"
                title={t("conversations.forkHint")}
                 onclick={() => onFork(message)}
                 disabled={busy || pending || changingAgent}
              ><GitFork size={11} />{t("conversations.fork")}</button>
            {/if}
          </div>
        </div>
        {#if message.author_kind === "user"}
          <span class="user-message mt-0.5 grid size-5 shrink-0 place-items-center rounded-full bg-ink text-canvas"><User size={11} /></span>
        {/if}
      </div>
    {/each}
  </div>

  {#if error}<div class="px-3"><ErrorNotice {error} /></div>{/if}
  {#if !node.locked && onChangeAgent}
    <div class="shrink-0 border-t border-border px-3 py-2">
      <Select class="w-full" controlSize="sm" aria-label={t("conversations.agent")} bind:value={selectedAgentId}
        disabled={busy || pending || changingAgent || nodeBusy || Boolean(liveGoal)} onchange={(event) => { void changeAgent(event.currentTarget.value); }}>
        {#if !availableAgents.some((item) => item.id === node.agent_id)}<option value={node.agent_id} disabled>{agent?.name ?? node.agent_id}</option>{/if}
        {#each availableAgents as item (item.id)}<option value={item.id}>{item.name}</option>{/each}
      </Select>
    </div>
  {/if}
  {#if goal}
    <section class="shrink-0 grid gap-1.5 border-t border-border bg-surface-subtle px-3 py-2 text-2xs" aria-label={t("issues.goalStatus")}>
      <div class="flex items-center gap-1.5"><Target size={12} /><strong>{goalLabel}</strong>
        {#if goal.status === "active"}<LoaderCircle size={11} class="animate-spin" />{/if}
      </div>
      <p class="m-0 max-h-16 overflow-y-auto whitespace-pre-wrap break-words">{goal.objective}</p>
      <span class="text-muted tabular-nums">{t("conversations.goalRounds", { count: goal.work_runs, max: goal.max_work_runs })} · {goal.tokens_used.toLocaleString()} / {goal.token_budget?.toLocaleString() ?? "∞"} Token</span>
      {#if goal.note}<p class="m-0 max-h-14 overflow-y-auto text-muted">{goal.note}</p>{/if}
      {#if goal.status_reason}<code class="break-words text-muted">{goal.status_reason}</code>{/if}
      {#if goalBudgetExhausted(goal)}
        <p class="m-0 text-muted">{t("goalBudget.exhausted")}</p>
      {/if}
      {#if liveGoal && onGoalAction}
        <div class="flex flex-wrap gap-2">
          {#if goal.status === "active"}
            <button class="min-h-7 rounded-sm border border-border px-2" type="button" disabled={pending} onclick={() => changeGoal("pause")}>{t("issues.goalPause")}</button>
          {:else if goal.status !== "budget_reached"}
            <button class="min-h-7 rounded-sm border border-border px-2" type="button" disabled={pending || running || goalBudgetExhausted(goal) || (goal.status !== "paused" && goal.work_runs >= goal.max_work_runs)} onclick={() => changeGoal("resume")}>{t("issues.goalResume")}</button>
          {/if}
          <button class="min-h-7 rounded-sm border border-danger/35 px-2 text-danger" type="button" disabled={pending} onclick={() => changeGoal("cancel")}>{t("issues.goalCancel")}</button>
        </div>
      {/if}
    </section>
  {/if}
  {#if node.locked}
    <p class="m-0 flex items-center gap-1.5 border-t border-border bg-surface-subtle px-3 py-2.5 text-2xs leading-relaxed text-muted">
      <GitFork size={11} class="shrink-0" />{t("conversations.nodeLocked")}
    </p>
  {:else if !liveGoal}
  {#if onCreateGoal}
    <div class="shrink-0 grid gap-1.5 border-t border-border px-3 py-2 text-2xs">
      <label class="flex items-center gap-2"><Checkbox type="checkbox" bind:checked={goalArmed} disabled={pending || busy || changingAgent || nodeBusy} />{t("issues.goal")}</label>
      {#if goalArmed}
        <p class="m-0 text-muted">{t("conversations.goalHint")}</p>
        <label class="flex items-center gap-2"><Checkbox type="checkbox" bind:checked={budgetEnabled} disabled={pending} />{t("issues.goalTokenBudget")}</label>
        {#if budgetEnabled}<Input class="min-w-0 w-full" type="number" min="1000" max="100000000" step="1" bind:value={budget} disabled={pending} aria-label={t("issues.goalTokenBudget")} />{/if}
      {/if}
    </div>
  {/if}
  <form class="flex items-end gap-2 border-t border-border bg-surface px-2.5 py-2" onsubmit={submit}>
    <Textarea
      class="min-h-[36px] flex-1 resize-none !rounded-sm !border-border !py-1.5 !text-xs"
      rows={1}
      maxlength={goalArmed ? 5000 : 20000}
      bind:value={draft}
      placeholder={t("conversations.inputPlaceholder")}
      aria-label={t("conversations.inputLabel")}
      onkeydown={(event) => {
        if (!event.isComposing && (event.metaKey || event.ctrlKey) && event.key === "Enter") {
          event.preventDefault();
          void submit(new SubmitEvent("submit"));
        }
      }}
    ></Textarea>
    {#if running && activeRunId}
      <button class="inline-grid size-9 shrink-0 cursor-pointer place-items-center rounded-sm border border-danger/35 bg-danger-subtle text-danger disabled:opacity-40" type="button" disabled={pending} aria-label={t("issues.stop")} onclick={stopRun}><Square size={14} /></button>
    {:else}<Button variant="primary" icon
      type="submit"
      disabled={!canSend}
      aria-label={goalArmed ? t("conversations.goalStart") : t("conversations.send")}
    >
      {#if busy || pending}<LoaderCircle size={14} class="animate-spin" />{:else}<Send size={14} />{/if}
    </Button>{/if}
  </form>
  {/if}

  <button
    class="absolute bottom-0 right-0 z-10 hidden size-5 cursor-nwse-resize touch-none place-items-end rounded-tl-md border-0 bg-transparent p-1 text-muted hover:text-accent md:grid"
    type="button"
    aria-label={t("conversations.resizeNode")}
    title={t("conversations.resizeNode")}
    onpointerdown={startResize}
    onpointermove={moveResize}
    onpointerup={endResize}
    onpointercancel={endResize}
  >
    <svg width="9" height="9" viewBox="0 0 9 9" aria-hidden="true"><path d="M8 1v7H1" fill="none" stroke="currentColor" stroke-width="1.3" stroke-linecap="round" /></svg>
  </button>
</article>

<style>
  :global([data-theme="dark"]) .user-message {
    background-color: var(--color-surface-hover);
    color: var(--color-text);
  }

  @media (max-width: 767px) {
    .conversation-node {
      position: relative;
      left: auto !important;
      top: auto !important;
      width: 100% !important;
      height: auto !important;
      max-height: 480px;
      flex-shrink: 0;
    }
  }
</style>

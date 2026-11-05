<script lang="ts">
  import { Button, Select, Textarea } from "../ui";
  import { createMutation, createQuery } from "@tanstack/svelte-query";
  import { ArrowDown, Bot, Plus, Send, Square, X } from "lucide-svelte";
  import { tick, untrack } from "svelte";
  import { api, idempotencyKey } from "../lib/api";
  import { queryClient } from "../lib/query";
  import { t } from "../lib/i18n.svelte";
  import { location } from "../lib/router.svelte";
  import { useConsole } from "../pages/shared.svelte";
  import type { Agent, Page, Run } from "../lib/types";
  import type { AssistantMessage, AssistantSession, WorkQuery } from "../lib/workRecords";
  import ConversationRunActivity from "./ConversationRunActivity.svelte";
  import RichText from "./RichText.svelte";
  import ErrorNotice from "./ErrorNotice.svelte";
  let {
    query,
    selected,
    onClose,
    scope = "work_history",
    inspirationIDs = [],
    inspirationQuery,
    contextLabel = "",
    beforeSend
  }: {
    query: WorkQuery;
    selected: string[];
    onClose: () => void;
    scope?: "work_history" | "inspirations";
    inspirationIDs?: string[];
    inspirationQuery?: { search: string; status: string };
    contextLabel?: string;
    beforeSend?: () => Promise<void>;
  } = $props();
  const questionHint = $derived(
    t(scope === "inspirations" ? "inspirations.question" : "work.question")
  );
  const scopeHint = $derived(t(scope === "inspirations" ? "inspirations.scope" : "work.scope"));
  let preparing = $state(false),
    prepareError = $state<{ key: string; error: unknown } | null>(null);
  const context = useConsole();
  let selectedSession = $state("");
  let drafts = $state<Record<string, string>>({});
  const attempts = new Map<string, { fingerprint: string; key: string }>();
  let restoredScope = $state("");
  let messageViewport = $state<HTMLDivElement>();
  let stickToBottom = $state(true);
  const agent = createQuery<Agent>(() => ({
    queryKey: ["work-assistant"],
    queryFn: () => api("/work-assistant")
  }));
  const assistantTitle = $derived(
    t(scope === "inspirations" ? "inspirations.assistant" : "work.assistant")
  );
  const sessions = createQuery<Page<AssistantSession>>(() => ({
    queryKey: ["assistant-sessions", scope],
    queryFn: () => api(`/work-assistant/sessions?scope=${scope}`)
  }));
  const activeID = $derived(selectedSession || sessions.data?.items[0]?.id || "");
  const draftKey = $derived(`${scope}:${activeID}`);
  const draft = $derived(drafts[draftKey] ?? "");
  $effect(() => {
    if (context.user?.id && restoredScope !== scope) {
      selectedSession = "";
      try {
        selectedSession =
          new URLSearchParams(location.search).get("session") ||
          localStorage.getItem(`teamva-work-chat-${context.user.id}-${scope}`) ||
          "";
      } catch {
        /* private storage */
      }
      restoredScope = scope;
    }
  });
  $effect(() => {
    if (context.user?.id && activeID) {
      try {
        localStorage.setItem(`teamva-work-chat-${context.user.id}-${scope}`, activeID);
      } catch {
        /* private storage */
      }
    }
  });
  $effect(() => {
    const params = new URLSearchParams(location.search);
    const id = params.get("session");
    if (id) selectedSession = id;
  });
  const messages = createQuery<Page<AssistantMessage>>(() => ({
    queryKey: ["assistant-messages", activeID],
    queryFn: ({ signal, queryKey }) => api(`/work-assistant/sessions/${encodeURIComponent(String(queryKey[1]))}/messages`, { signal }),
    enabled: Boolean(activeID),
    refetchInterval: 1500
  }));
  const loading = $derived(agent.isLoading || sessions.isLoading || Boolean(activeID && messages.isLoading));
  function followOutput() {
    if (!stickToBottom) return;
    const session = activeID;
    const viewport = messageViewport;
    void tick().then(() => {
      if (viewport && viewport === messageViewport && session === activeID && stickToBottom) {
        viewport.scrollTop = viewport.scrollHeight;
      }
    });
  }
  function jumpToLatest() {
    stickToBottom = true;
    followOutput();
  }
  $effect(() => {
    void draftKey;
    untrack(jumpToLatest);
  });
  $effect(() => {
    void messages.data;
    untrack(followOutput);
  });
  const isActive = (status: string) =>
    ["queued", "starting", "running", "waiting_approval", "cancelling"].includes(status);
  const running = $derived(
    messages.data?.items.find((m) => m.author_kind === "agent" && isActive(m.status))
  );
  const newChat = createMutation<AssistantSession, Error, { scope: typeof scope; title: string; originKey: string }>(() => ({
    mutationFn: (input) => api("/work-assistant/sessions", { body: { title: input.title, scope: input.scope } }),
    onSuccess: (v, input) => {
      if (draftKey === input.originKey) selectedSession = v.id;
      void queryClient.invalidateQueries({ queryKey: ["assistant-sessions"] });
    }
  }));
  const send = createMutation<
    Run,
    Error,
    {
      session: string;
      scope: typeof scope;
      draftKey: string;
      body: string;
      query: WorkQuery;
      selected: string[];
      inspirations: string[];
      inspirationQuery?: { search: string; status: string };
      key: string;
    }
  >(() => ({
    mutationFn: (v) =>
      api(`/work-assistant/sessions/${encodeURIComponent(v.session)}/messages`, {
        body: {
          body: v.body,
          query: v.query,
          selected_ids: v.selected,
          ...(v.scope === "inspirations"
            ? { inspiration_ids: v.inspirations, inspiration_query: v.inspirationQuery }
            : {})
        },
        headers: { "Idempotency-Key": v.key }
      }),
    onSuccess: (_v, input) => {
      if (drafts[input.draftKey] === input.body) delete drafts[input.draftKey];
      attempts.delete(input.draftKey);
      void queryClient.invalidateQueries({ queryKey: ["assistant-messages", input.session] });
      void queryClient.invalidateQueries({ queryKey: ["assistant-sessions"] });
    }
  }));
  const stop = createMutation<unknown, Error, { runId: string; session: string; mode: "graceful" | "force" }>(() => ({
    mutationFn: (input) => api(`/runs/${encodeURIComponent(input.runId)}/stop`, {
      method: "POST", body: { mode: input.mode }
    }),
    onSuccess: (_value, input) => queryClient.invalidateQueries({ queryKey: ["assistant-messages", input.session] })
  }));
  const currentError = $derived(
    agent.error ?? sessions.error ?? messages.error ??
    (prepareError?.key === draftKey ? prepareError.error : null) ??
    (send.variables?.draftKey === draftKey ? send.error : null) ??
    (newChat.variables?.originKey === draftKey ? newChat.error : null) ??
    (stop.variables?.session === activeID ? stop.error : null)
  );
  async function submit(event: SubmitEvent) {
    event.preventDefault();
    if (!draft.trim() || loading || agent.error || sessions.error || messages.error || send.isPending || newChat.isPending || running || preparing) return;
    if (!agent.data?.current_revision_id) {
      return;
    }
    const body = draft;
    const startingSession = activeID;
    const originKey = draftKey;
    const sendScope = scope;
    const title = assistantTitle;
    const queryScope = JSON.parse(JSON.stringify(query)) as WorkQuery;
    queryScope.offset = 0;
    const ids = [...selected],
      noteIDs = [...inspirationIDs];
    const noteQuery = inspirationQuery ? { ...inspirationQuery } : undefined;
    preparing = true;
    prepareError = null;
    try {
      await beforeSend?.();
      let id = startingSession;
      if (!id) {
        const created = await newChat.mutateAsync({ scope: sendScope, title, originKey });
        id = created.id;
        drafts[`${sendScope}:${id}`] = body;
        if (drafts[originKey] === body) delete drafts[originKey];
      }
      const fingerprint = JSON.stringify({
        body,
        query: queryScope,
        selected: ids,
        inspirations: noteIDs,
        inspirationQuery: noteQuery,
        session: id
      });
      const targetKey = `${sendScope}:${id}`;
      let attempt = attempts.get(targetKey);
      if (attempt?.fingerprint !== fingerprint) {
        attempt = { fingerprint, key: idempotencyKey("work-assistant") };
        attempts.set(targetKey, attempt);
      }
      send.mutate({
        session: id,
        scope: sendScope,
        draftKey: targetKey,
        body,
        query: queryScope,
        selected: ids,
        inspirations: noteIDs,
        inspirationQuery: noteQuery,
        key: attempt.key
      });
    } catch (error) {
      prepareError = { key: originKey, error };
    } finally {
      preparing = false;
    }
  }
</script>

<aside
  class="flex h-full min-h-0 min-w-0 flex-col border-l border-border bg-surface"
  aria-label={assistantTitle}
>
  <header class="flex items-center gap-2 border-b border-border px-4 py-3">
    <Bot size={17} class="text-accent" />
    <div class="min-w-0 flex-1">
      <h2 class="text-sm font-semibold">{assistantTitle}</h2>
      <p class="text-2xs text-muted">{t("work.readonly")} · {agent.data?.name || "Lumi"}</p>
    </div>
    <button type="button" class="size-8" aria-label={t("common.close")} onclick={onClose}
      ><X size={16} /></button
    >
  </header>
  <div class="grid min-w-0 grid-cols-[minmax(0,1fr)_auto] gap-2 border-b border-border p-3">
    <Select
      aria-label={t("work.history")}
      value={activeID}
      onchange={(e) => (selectedSession = e.currentTarget.value)}
      ><option value="">{t("work.history")}</option
      >{#each sessions.data?.items ?? [] as chat}<option value={chat.id}
          >{chat.title} · {new Date(chat.created_at).toLocaleString()}</option
        >{/each}</Select
    ><Button
      type="button"
      icon
      aria-label={t("work.newChat")}
      disabled={newChat.isPending || sessions.isLoading}
      onclick={() => newChat.mutate({ scope, title: assistantTitle, originKey: draftKey })}><Plus size={16} /></Button
    >
  </div>
  {#if currentError}<div
      class="p-3"
    >
      <ErrorNotice
        error={currentError}
        onRetry={agent.error || sessions.error || messages.error ? () => {
          if (agent.error) void agent.refetch();
          if (sessions.error) void sessions.refetch();
          if (messages.error) void messages.refetch();
        } : undefined}
      />
    </div>{/if}
  {#if agent.data && !agent.data.current_revision_id}
    <div class="grid gap-2 border-b border-border bg-surface-subtle p-4 text-xs">
      <p>{t("controls.agentConfigurationCentralized")}</p>
      <a class="text-accent underline" href={`/agents?agent=${encodeURIComponent(agent.data.id)}`}>{t("controls.openAgentPage")}</a>
    </div>
  {/if}
  {#if contextLabel}<p
      class="border-b border-border bg-surface-subtle px-4 py-2 text-xs font-medium"
    >
      {contextLabel}
    </p>{/if}
  <div bind:this={messageViewport} class="min-h-0 flex-1 space-y-4 overflow-y-auto p-4" aria-live="polite"
    aria-busy={loading} data-assistant-messages
    onscroll={() => {
      if (messageViewport) stickToBottom = messageViewport.scrollHeight - messageViewport.scrollTop - messageViewport.clientHeight < 72;
    }}>
    {#if loading}<p role="status" class="py-8 text-sm text-muted">{t("common.loading")}</p>
    {:else if !messages.error && !messages.data?.items.length}<p class="py-8 text-sm leading-relaxed text-muted">
        {questionHint}
      </p>{/if}
    {#each messages.data?.items ?? [] as message (message.id)}
      <article
        class="min-w-0 rounded-md border border-border p-3 {message.author_kind === 'user'
          ? 'bg-surface-subtle'
          : 'bg-surface'}"
        data-assistant-message={message.id}
      >
        <div class="mb-2 flex justify-between gap-2 text-2xs text-muted">
          <span>{message.author_kind === "user" ? t("issues.you") : assistantTitle}</span><time
            >{new Date(message.created_at).toLocaleTimeString()}</time
          >
        </div>
        {#if message.body}<RichText text={message.body} enableLinks />{/if}
        {#if message.author_kind === "agent"}<ConversationRunActivity
            runId={message.run_id}
            active={isActive(message.status)}
            publishedBody={message.body}
            onUpdate={followOutput}
            onSettled={() => {
              void queryClient.invalidateQueries({ queryKey: ["assistant-messages", message.session_id] });
            }}
          />{#if !isActive(message.status) && message.status !== "succeeded"}<p
              class="mt-2 text-xs text-warning"
            >
              {message.status}
            </p>{/if}{/if}
      </article>
    {/each}
  </div>
  {#if !stickToBottom}<button type="button" class="mx-auto mb-2 flex items-center gap-1 rounded border border-border bg-surface px-3 py-1.5 text-xs" onclick={jumpToLatest}>
    <ArrowDown size={13} />{t("work.jumpToLatest")}
  </button>{/if}
  <form class="grid gap-2 border-t border-border p-3" onsubmit={submit}>
    <p class="text-2xs text-muted">
      {scopeHint}
      {selected.length + inspirationIDs.length
        ? t("work.selected", { count: selected.length + inspirationIDs.length })
        : ""}
    </p>
    <Textarea
      aria-label={questionHint}
      disabled={!agent.data?.current_revision_id}
      placeholder={questionHint}
      rows={3}
      value={draft}
      oninput={(event) => { drafts[draftKey] = event.currentTarget.value; }}
      maxlength={40000}></Textarea>
    <div class="flex justify-end gap-2">
      {#if running}<button
          type="button"
          class="flex items-center gap-1 rounded border border-border px-3 py-2 text-xs"
          disabled={stop.isPending}
          onclick={() => {
            if (running?.run_id) stop.mutate({ runId: running.run_id, session: activeID, mode: running.status === "cancelling" ? "force" : "graceful" });
          }}><Square size={12} />{t(running.status === "cancelling" ? "work.forceStop" : "work.stop")}</button
        >{/if}<Button type="submit" variant="primary" class="text-xs"
        disabled={!draft.trim() || !agent.data?.current_revision_id ||
          loading || Boolean(agent.error || sessions.error || messages.error) ||
          send.isPending ||
          newChat.isPending ||
          Boolean(running) ||
          preparing}><Send size={13} />{t("work.send")}</Button
      >
    </div>
  </form>
</aside>

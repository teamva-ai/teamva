<script lang="ts">
  import { ChevronDown, ChevronRight, LoaderCircle } from "lucide-svelte";
  import { BoundedRunEventBuffer } from "../lib/activityEvents";
  import { projectRunActivity } from "../lib/runActivityProjection";
  import { compactionPresentation, settledActivityStatus } from "../lib/compaction";
  import { subscribeRunEvents } from "../lib/runEventStream";
  import { diagnosticText, isFailureEvent } from "../lib/runDiagnostics";
  import type { RunEvent } from "../lib/types";
  import { t } from "../lib/i18n.svelte";
  import AgentActivityRow from "./AgentActivityRow.svelte";
  import RichText from "./RichText.svelte";

  let { runId, active, publishedBody = "", onSettled, onUpdate }: {
    runId: string;
    active: boolean;
    publishedBody?: string;
    onSettled?: () => void;
    onUpdate?: () => void;
  } = $props();
  let opened = $state(false);
  let events = $state<RunEvent[]>([]);
  let evicted = $state(0);
  let streamState = $state("connecting");
  let terminalSeen = $state(false);
  const running = $derived(active && !terminalSeen);
  const rec = (value: unknown): Record<string, unknown> => value && typeof value === "object" ? value as Record<string, unknown> : {};
  const str = (value: unknown) => typeof value === "string" ? value : "";
  const json = (value: unknown) => typeof value === "string" ? value : JSON.stringify(value, null, 2);

  $effect(() => { if (active) opened = true; });
  $effect(() => { void events.length; void publishedBody; onUpdate?.(); });
  $effect(() => {
    if (!opened || !runId) return;
    terminalSeen = false;
    const buffer = new BoundedRunEventBuffer();
    let flushTimer: ReturnType<typeof setTimeout> | undefined;
    const flush = () => { events = buffer.values(); evicted = buffer.evictedCount; flushTimer = undefined; };
    const stop = subscribeRunEvents({
      runId,
      onEvent: (event) => {
        buffer.upsert(event);
        if (!flushTimer) flushTimer = setTimeout(flush, 50);
      },
      onState: (state) => { streamState = state; },
      onTerminal: () => {
        terminalSeen = true;
        if (flushTimer) clearTimeout(flushTimer);
        flush();
        onSettled?.();
      }
    });
    return () => { if (flushTimer) clearTimeout(flushTimer); stop(); };
  });

  const rows = $derived.by(() => {
    const materialized = projectRunActivity(events);
    const result: Array<{ id: string; kind: "assistant" | "reasoning" | "tool" | "retry" | "error" | "compaction"; text: string; label: string; detail?: string; status?: string; time: string }> = [];
    for (const event of materialized) {
      const base = { id: event.id, time: event.occurred_at };
      if (event.event_type === "output.delta") {
        const previous = result.at(-1);
        if (previous?.id.startsWith("delta:") && previous.kind === "assistant") previous.text += str(event.payload.text);
        else result.push({ ...base, id: "delta:" + event.id, kind: "assistant", text: str(event.payload.text), label: "" });
      } else if (event.event_type === "output.snapshot") {
        const text = str(event.payload.text), previous = result.at(-1);
        if (previous?.id.startsWith("delta:") && text.startsWith(previous.text)) result.pop();
        if (text) result.push({ ...base, kind: "assistant", text, label: "" });
      } else if (event.event_type === "reasoning.snapshot") {
        result.push({ ...base, kind: "reasoning", label: t("issues.sessionThinking"), text: str(event.payload.text),
          status: settledActivityStatus(str(event.payload.status) || "running", running) });
      } else if (event.event_type === "provider.tool") {
        const state = event.payload, tool = str(state.tool);
        result.push({ ...base, kind: "tool", label: tool === "command" || tool === "bash" ? t("issues.sessionShellCommand") : tool || t("issues.sessionTool"),
          text: str(rec(state.input).summary) || str(rec(state.input).command),
          detail: json({ input: state.input, output: state.output ?? null, error: state.error, exit_code: state.exit_code }),
          status: settledActivityStatus(str(state.status), running) });
      } else if (event.event_type === "context.compaction") {
        const presentation = compactionPresentation(str(event.payload.status), running);
        const message = diagnosticText(event.payload);
        result.push({ ...base, kind: "compaction", ...presentation, ...(message ? { text: message, detail: message } : {}) });
      } else if (event.payload.willRetry === true || isFailureEvent(event)) {
        const retry = event.payload.willRetry === true;
        const message = diagnosticText(event.payload, t("issues.sessionFailed"));
        result.push({ ...base, kind: retry ? "retry" : "error", label: t(retry ? "issues.sessionRetrying" : "issues.sessionFailed"), text: message, detail: message,
          status: retry ? settledActivityStatus("running", running) : "failed" });
      }
    }
    // The published body represents one answer, not every earlier message with
    // identical text (for example a repeated answer on either side of a tool).
    const published = publishedBody.trim();
    let duplicate = -1;
    if (!active && published) {
      for (let index = result.length - 1; index >= 0; index--) {
        if (result[index].kind === "assistant" && result[index].text.trim() === published) {
          duplicate = index;
          break;
        }
      }
    }
    return result.filter((_, index) => index !== duplicate);
  });
</script>

<div class="grid min-w-0 gap-1.5" data-run-activity={runId}>
  <button type="button" class="flex items-center gap-1 border-0 bg-transparent p-0 text-left text-2xs text-muted" aria-expanded={opened} onclick={() => { opened = !opened; }}>
    {#if opened}<ChevronDown size={11} />{:else}<ChevronRight size={11} />{/if}{t("issues.sessionOutput")}
  </button>
  {#if opened}
    {#if evicted}<p role="status" class="m-0 text-2xs text-warning">{t("issues.activityHistoryLimited", { count: evicted })}</p>{/if}
    {#if running && streamState === "retrying"}<span role="status" class="text-2xs text-warning">{t("issues.sessionRetrying")}</span>{/if}
    {#each rows as row (row.id)}
      {#if row.kind === "assistant"}
        <div class="conversation-stream-reply rounded-md border border-border bg-surface-subtle px-2.5 py-1.5 text-text"><RichText text={row.text} /></div>
      {:else}
        <AgentActivityRow kind={row.kind} label={row.label} text={row.text} detail={row.detail} status={row.status} occurredAt={row.time} />
      {/if}
    {/each}
    {#if running}<span class="inline-flex items-center gap-1 text-2xs text-muted"><LoaderCircle size={11} class="animate-spin" />{t("conversations.working")}</span>{/if}
  {/if}
</div>

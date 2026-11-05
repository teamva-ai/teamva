<script lang="ts">
  import { createQuery } from "@tanstack/svelte-query";
  import { api } from "../lib/api";
  import { t } from "../lib/i18n.svelte";
  import type { Run } from "../lib/types";
  import ErrorNotice from "./ErrorNotice.svelte";

  let { runId }: { runId: string } = $props();
  // Conversation storage intentionally has a single unpublished terminal
  // message state. Resolve its Run outcome instead of calling user cancellation
  // a failed execution, also when activity is collapsed after a cold refresh.
  const run = createQuery<Run>(() => ({
    queryKey: ["conversation-message-outcome", runId],
    queryFn: ({ queryKey, signal }) => api<Run>(`/runs/${encodeURIComponent(String(queryKey[1]))}`, { signal }),
    enabled: Boolean(runId),
    refetchInterval: (query) => query.state.data && !["succeeded", "failed", "cancelled", "lost"].includes(query.state.data.status) ? 500 : false
  }));
</script>

{#if run.error}
  <ErrorNotice error={run.error} />
{:else if !run.data}
  <span class="text-muted">{t("common.loading")}</span>
{:else if run.data.status === "cancelled" || run.data.status === "cancelling" || run.data.status === "lost"}
  <span class="text-muted">{t(`status.${run.data.status}`)}</span>
{:else if ["queued", "starting", "running", "waiting_approval"].includes(run.data.status)}
  <span class="text-muted">{t(`status.${run.data.status}`)}</span>
{:else}
  <span class="text-danger">{t("conversations.failed")}</span>
{/if}

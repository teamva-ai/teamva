<script lang="ts">
  import { Button } from "../ui";
  import { t } from "../lib/i18n.svelte";
  import type { IssueWorkItem } from "../lib/types";

  let { items, agentName, pending = false, onOpenSession }: {
    items: IssueWorkItem[]; agentName: (id: string) => string; pending?: boolean;
    onOpenSession: (item: IssueWorkItem) => void;
  } = $props();
  const standardStatuses = ["queued", "starting", "running", "waiting_approval", "cancelling", "succeeded", "failed", "cancelled"];
  const workStatuses = ["ready", "waiting_dependencies", "waiting_result", "blocked", "waiting", "interrupted"];
  function statusLabel(status: string) {
    return standardStatuses.includes(status) ? t(`status.${status}`) : workStatuses.includes(status)
      ? t(`issueWork.${status}`) : t("issueWork.unknownStatus", { status });
  }
  const names = $derived((keys: string[]) => keys.map((key) => items.find((item) => item.key === key)?.title || key).join(", "));
</script>

<ul class="m-0 mt-3 grid list-none gap-2 p-0" aria-label={t("issueWork.title")}>
  {#each items as item (item.key)}
    <li class="grid min-w-0 gap-1 rounded-sm border border-border bg-surface-subtle px-3 py-2" data-work-item-key={item.key} data-work-status={item.status}>
      <div class="flex min-w-0 flex-wrap items-center gap-2 text-xs">
        <strong class="min-w-0 break-words">{item.title}</strong>
        <span class="text-muted">{agentName(item.agent_id)}</span>
        <span class="ml-auto rounded-sm bg-surface-hover px-2 py-1 text-secondary" role="status">{statusLabel(item.status)}</span>
      </div>
      {#if item.after?.length}<p class="m-0 break-words text-2xs text-muted">{t("issueWork.after", { items: names(item.after) })}</p>{/if}
      {#if item.blocked_by?.length}<p class="m-0 break-words text-2xs text-warning">{t("issueWork.blockedBy", { items: names(item.blocked_by) })}</p>{/if}
      {#if item.status_reason === "issue_not_open"}<p class="m-0 text-2xs text-warning">{t("issueWork.issueNotOpen")}</p>{/if}
      {#if item.status_reason === "issue_paused"}<p class="m-0 text-2xs text-warning">{t("issueWork.pausedSession")}</p>{/if}
      {#if item.issue_agent_instance_id}
        <Button controlSize="sm" class="justify-self-start" disabled={pending} onclick={() => onOpenSession(item)}>{t("issues.openAgentSession")}</Button>
      {/if}
    </li>
  {/each}
</ul>

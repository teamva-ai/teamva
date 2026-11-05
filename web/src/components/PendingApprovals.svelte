<script lang="ts">
  import { Button } from "../ui";
  import { createMutation, createQuery } from "@tanstack/svelte-query";
  import { BellRing } from "lucide-svelte";
  import { api } from "../lib/api";
  import { t } from "../lib/i18n.svelte";
  import { location, navigate } from "../lib/router.svelte";
  import { queryClient } from "../lib/query";
  import type { Approval, ConversationNode, Page, Run } from "../lib/types";
  import ErrorNotice from "./ErrorNotice.svelte";
  import Modal from "../ui/Modal.svelte";
  import StatusBadge from "./StatusBadge.svelte";

  let open = $state(false);
  let opening = $state("");
  let linkError = $state<unknown>(null);
  let selected = $state<Approval | null>(null);
  const approvals = createQuery<Page<Approval>>(() => ({
    queryKey: ["approvals", "pending", "global"],
    queryFn: ({ signal }) => api<Page<Approval>>("/approvals?status=pending", { signal }),
    retry: false,
    refetchInterval: 5_000
  }));
  const pending = $derived(approvals.data?.items ?? []);
  const decide = createMutation<Approval, Error, { item: Approval; decision: "approved" | "denied" }>(() => ({
    mutationFn: ({ item, decision }) => api<Approval>(`/approvals/${encodeURIComponent(item.id)}/decisions`, {
      body: { expected_version: item.version, action_digest: item.action_digest, decision, scope: "once" }
    }),
    onSuccess: () => {
      selected = null;
      void queryClient.invalidateQueries({ queryKey: ["approvals"] });
      void queryClient.invalidateQueries({ queryKey: ["instance-approvals"] });
      void queryClient.invalidateQueries({ queryKey: ["issue-runs"] });
      void queryClient.invalidateQueries({ queryKey: ["conversation-detail"] });
    }
  }));

  async function review(item: Approval) {
    if (opening) return;
    opening = item.id;
    linkError = null;
    try {
      const run = await api<Run>(`/runs/${encodeURIComponent(item.run_id)}`);
      const params = new URLSearchParams({ project: item.project_id, run: run.id });
      let path = "/issues";
      if (run.issue_id) {
        params.set("issue", run.issue_id);
        if (run.issue_agent_instance_id) params.set("session", run.issue_agent_instance_id);
      } else if (run.conversation_node_id) {
        const node = await api<ConversationNode>(`/conversation-nodes/${encodeURIComponent(run.conversation_node_id)}`);
        path = "/conversations";
        params.set("graph", node.graph_id);
        params.set("node", node.id);
      } else {
        throw new Error(t("issues.sessionApprovalUnavailable"));
      }
      const target = `${path}?${params}`;
      navigate(target);
      if (location.path + location.search === target) open = false;
    } catch (error) {
      linkError = error;
    } finally {
      opening = "";
    }
  }
</script>

{#if pending.length || approvals.error}
  <button
    type="button"
    class="inline-flex min-h-8 shrink-0 items-center gap-1 rounded-sm border border-warning/40 bg-warning-subtle px-2 text-xs font-medium text-warning hover:bg-surface-hover"
    aria-label={t("issues.sessionApproval")}
    title={t("issues.sessionApproval")}
    aria-haspopup="dialog"
    onclick={() => { open = true; selected = null; linkError = null; decide.reset(); }}
  >
    <BellRing size={15} aria-hidden="true" />
    <span aria-live="polite">{pending.length || "!"}</span>
  </button>
{/if}

{#if open}
  <Modal title={t("issues.sessionApproval")} onClose={() => { if (!decide.isPending) open = false; }}>
    <div class="grid gap-3">
      {#if approvals.error}<ErrorNotice error={approvals.error} onRetry={() => void approvals.refetch()} />{/if}
      {#if linkError}<ErrorNotice error={linkError} />{/if}
      {#if decide.error}<ErrorNotice error={decide.error} />{/if}
      {#if !pending.length && !approvals.error}<p class="text-sm text-muted">{t("issues.noPendingApprovals")}</p>{/if}
      {#each pending as item (item.id)}
        <div class="flex min-w-0 flex-wrap items-center gap-3 rounded-sm border border-border p-3">
          <div class="min-w-0 flex-1">
            <strong class="block truncate text-sm">{item.action_kind}</strong>
            <StatusBadge status={item.status} />
          </div>
          <Button type="button"
            disabled={decide.isPending} onclick={() => { selected = selected?.id === item.id ? null : item; decide.reset(); }}>{t("issues.sessionApproval")}</Button>
          <Button type="button"
            disabled={Boolean(opening)} onclick={() => void review(item)}>{t("issues.openAgentSession")}</Button>
        </div>
        {#if selected?.id === item.id}
          <section class="grid gap-3 rounded-sm border border-warning/40 bg-warning-subtle p-3" aria-label={t("issues.sessionApprovalPending")}>
            <p class="break-all text-xs text-secondary">{item.run_id} · {item.risk}</p>
            <pre class="max-h-72 overflow-auto whitespace-pre-wrap break-all rounded-sm bg-surface p-3 text-xs text-text">{JSON.stringify(item.normalized_action, null, 2)}</pre>
            <div class="flex justify-end gap-2">
              <Button type="button" variant="danger" disabled={decide.isPending}
                onclick={() => decide.mutate({ item, decision: "denied" })}>{t("issues.denyAction")}</Button>
              <Button type="button" variant="primary" disabled={decide.isPending}
                onclick={() => decide.mutate({ item, decision: "approved" })}>{t("issues.allowAction")}</Button>
            </div>
          </section>
        {/if}
      {/each}
    </div>
  </Modal>
{/if}

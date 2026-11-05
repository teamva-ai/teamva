<script lang="ts">
  import { createQuery } from "@tanstack/svelte-query";
  import { api } from "../lib/api";
  import { t } from "../lib/i18n.svelte";
  import { navigate } from "../lib/router.svelte";
  import type { WorkDetail } from "../lib/workRecords";
  import ErrorNotice from "./ErrorNotice.svelte";
  let { id }: { id: string } = $props();
  const detail = createQuery<WorkDetail>(() => ({
    queryKey: ["work-detail", id],
    queryFn: () => api(`/work-records/${encodeURIComponent(id)}`),
    refetchInterval: 5000
  }));
  const json = (value: unknown) => JSON.stringify(value, null, 2);
</script>

<div class="grid min-w-0 gap-4 bg-surface-subtle p-4 text-xs" data-work-detail={id}>
  {#if detail.error}<ErrorNotice error={detail.error} />{/if}
  {#if detail.data}
    <div class="grid grid-cols-2 gap-3 max-lg:grid-cols-1">
      {#each [[t("work.before"), detail.data.record.before], [t("work.after"), detail.data.record.after]] as [title, values]}
        <section class="min-w-0">
          <h3 class="mb-2 font-semibold">{String(title)}</h3>
          <pre
            class="max-h-64 overflow-auto whitespace-pre-wrap break-all rounded border border-border bg-surface p-3">{json(
              values
            )}</pre>
        </section>
      {/each}
    </div>
    <section>
      <h3 class="mb-2 font-semibold">{t("work.related")}</h3>
      <div class="flex flex-wrap gap-2">
        {#each detail.data.links as link (`${link.kind}:${link.id}`)}
          <button
            type="button"
            class="max-w-full rounded border border-border bg-surface px-3 py-2 text-left disabled:opacity-60"
            disabled={link.deleted}
            onclick={() => navigate(link.url)}
          >
            <span class="block break-all">{link.kind} · {link.title || link.id}</span><code
              class="break-all text-muted">{link.id}</code
            >{#if link.deleted}<span class="block text-warning">{t("work.deleted")}</span>{/if}
          </button>
        {/each}
      </div>
    </section>
    <details>
      <summary class="cursor-pointer font-semibold">{t("work.detail")} · IDs / metadata</summary>
      <pre
        class="mt-2 max-h-80 overflow-auto whitespace-pre-wrap break-all rounded border border-border bg-surface p-3">{json(
          detail.data.record
        )}</pre>
    </details>
    <section>
      <h3 class="mb-2 font-semibold">{t("work.followups")} · {detail.data.children.length}</h3>
      <ol class="grid gap-2">
        {#each detail.data.children as child (child.id)}<li
            class="rounded border border-border bg-surface p-2"
          >
            <div class="flex flex-wrap gap-2">
              <time>{new Date(child.occurred_at).toLocaleString()}</time><strong
                >{child.action}</strong
              ><span>{child.actor_type}</span><span>{child.title}</span>
            </div>
            <code class="break-all text-muted">{child.object_id} · {child.id}</code>
            <details>
              <summary>{t("work.detail")}</summary>
              <pre class="max-h-64 overflow-auto whitespace-pre-wrap break-all">{json(child)}</pre>
            </details>
          </li>{/each}
      </ol>
    </section>
  {:else}<p>{t("common.loading")}</p>{/if}
</div>

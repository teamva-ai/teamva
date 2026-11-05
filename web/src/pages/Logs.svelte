<script lang="ts">
  import { createQuery } from "@tanstack/svelte-query";
  import { Activity as ActivityIcon, CheckCircle2, XCircle } from "lucide-svelte";
  import { t } from "../lib/i18n.svelte";
  import { api } from "../lib/api";
  import type { AuditEvent, Page } from "../lib/types";
  import EmptyState from "../components/EmptyState.svelte";
  import ErrorNotice from "../components/ErrorNotice.svelte";
  import Loading from "../components/Loading.svelte";
  import PageHeader from "../components/PageHeader.svelte";
  import { auditActionLabel, formatTime } from "../lib/helpers";

  const audit = createQuery<Page<AuditEvent>>(() => ({
    queryKey: ["audit", "all"],
    queryFn: () => api<Page<AuditEvent>>("/audit-events"),
    refetchInterval: 5_000
  }));

  const activityIconClass: Record<string, string> = {
    succeeded: "text-success",
    failed: "text-danger"
  };

  function actorLabel(actor: string): string {
    if (actor === "user") return t("issues.you");
    if (actor === "agent") return t("issues.agent");
    return t("logs.system");
  }

  function outcomeLabel(outcome: string): string {
    const label = t(`status.${outcome}`);
    return label === `status.${outcome}` ? outcome : label;
  }

  function detailLabel(details: Record<string, unknown>): string {
    const fields = Array.isArray(details.fields)
      ? details.fields.filter((item): item is string => typeof item === "string")
      : [];
    if (fields.length) {
      const fieldLabels: Record<string, string> = {
        locale: t("settings.language"),
        theme: t("settings.theme"),
        allow_write: t("settings.allowWrite")
      };
      return fields.map((field) => fieldLabels[field] ?? field).join("、");
    }
    const message = details.message;
    if (typeof message === "string" && message.trim()) return message;
    return "—";
  }
</script>

<PageHeader title={t("logs.title")} description={t("logs.desc")} />
{#if audit.error}<div class="border-b border-border px-5 py-3">
    <ErrorNotice error={audit.error} />
  </div>{/if}
{#if audit.isLoading}
  <Loading block />
{:else}
  <section class="overflow-hidden bg-surface">
    <div class="flex items-center justify-between gap-4 border-b border-border px-5 py-4">
      <div>
        <h2 class="text-md font-semibold tracking-[-0.01em]">{t("logs.recent")}</h2>
        <p class="mt-1.5 text-sm leading-normal text-pretty text-secondary">
          {t("logs.recentDesc")}
        </p>
      </div>
    </div>
    {#if !audit.data?.items.length}
      <EmptyState title={t("logs.noEvents")} description={t("logs.noEventsDesc")} />
    {:else}
      <ol class="m-0 list-none p-0">
        {#each audit.data.items as event (event.id)}
          <li
            class="grid grid-cols-[auto_minmax(180px,0.6fr)_minmax(260px,1fr)_auto] items-center gap-3 border-b border-border px-5 py-3 text-2xs max-md:grid-cols-[auto_1fr]"
          >
            <span class={activityIconClass[event.outcome] ?? "text-warning"}>
              {#if event.outcome === "succeeded"}<CheckCircle2 size={16} />
              {:else if event.outcome === "failed"}<XCircle size={16} />
              {:else}<ActivityIcon size={16} />{/if}
            </span>
            <div class="grid gap-1">
              <strong>{auditActionLabel(event.action)}</strong>
              <span class="text-muted"
                >{t("logs.actor", {
                  actor: actorLabel(event.actor_type),
                  outcome: outcomeLabel(event.outcome)
                })}</span
              >
            </div>
            <span
              class="max-md:col-start-2 overflow-hidden text-ellipsis whitespace-nowrap text-secondary"
              title={JSON.stringify(event.details)}>{detailLabel(event.details)}</span
            >
            <time class="max-md:col-start-2 text-muted">{formatTime(event.occurred_at)}</time>
          </li>
        {/each}
      </ol>
    {/if}
  </section>
{/if}

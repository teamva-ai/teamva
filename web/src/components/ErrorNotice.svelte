<script lang="ts">
  import { Button } from "../ui";
  import { XCircle } from "lucide-svelte";
  import { t } from "../lib/i18n.svelte";
  import { APIError } from "../lib/api";

  let { error, onRetry }: { error: unknown; onRetry?: () => void } = $props();

  let message = $derived.by(() => {
    let text = error instanceof Error ? error.message : t("errors.unknown");
    if (error instanceof APIError && error.retryAfter) {
      text += t("errors.retryAfter", { seconds: error.retryAfter });
    }
    return text;
  });
</script>

<div
  class="mx-auto mb-4 flex max-w-[1120px] items-start gap-3 rounded-sm border border-danger/30 bg-danger-subtle px-4 py-3 text-sm text-danger"
  role="alert"
>
  <XCircle size={17} aria-hidden="true" />
  <p class="min-w-0 flex-1 leading-relaxed">{message}</p>
  {#if onRetry}
    <Button class="ml-auto" type="button" onclick={onRetry}>{t("common.retry")}</Button>
  {/if}
</div>

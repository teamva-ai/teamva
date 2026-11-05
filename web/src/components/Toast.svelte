<script lang="ts">
  import { toastList } from "../lib/toast.svelte";
  import { CheckCircle2, XCircle, Info } from "lucide-svelte";

  const toasts = toastList();

  const kindClasses: Record<string, string> = {
    success: "text-success",
    error: "text-danger",
    info: "text-secondary"
  };
</script>

{#if toasts.length > 0}
  <div class="fixed bottom-5 right-5 z-[100] flex max-w-[380px] flex-col gap-2" aria-live="polite">
    {#each toasts as toast (toast.id)}
      <div
        class="flex animate-[toast-in_0.18s_ease-out] items-center gap-2 rounded-md bg-surface px-4 py-3 text-sm text-text shadow-raised {kindClasses[toast.kind]}"
        role="status"
      >
        {#if toast.kind === "success"}<CheckCircle2 size={16} />
        {:else if toast.kind === "error"}<XCircle size={16} />
        {:else}<Info size={16} />{/if}
        <span>{toast.message}</span>
      </div>
    {/each}
  </div>
{/if}

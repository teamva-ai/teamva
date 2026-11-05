<script lang="ts">
  import { CheckCircle2 } from "lucide-svelte";
  import type { Snippet } from "svelte";

  let { children }: { children?: Snippet } = $props();
</script>

<div
  class="mx-auto mb-4 flex max-w-[1120px] items-start gap-3 rounded-sm border border-success/30 bg-success-subtle px-4 py-3 text-sm text-success"
  role="status"
>
  <CheckCircle2 size={17} aria-hidden="true" />
  <span>{@render children?.()}</span>
</div>

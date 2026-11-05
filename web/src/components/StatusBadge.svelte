<script lang="ts">
  import { statusLabel, statusTone } from "../lib/helpers";

  let { status }: { status: string } = $props();

  const toneClasses = {
    running: "bg-running-subtle text-running",
    warning: "bg-warning-subtle text-warning",
    success: "bg-success-subtle text-success",
    danger: "bg-danger-subtle text-danger",
    neutral: "bg-surface-hover text-secondary"
  } satisfies Record<ReturnType<typeof statusTone>, string>;
</script>

<span
  class="inline-flex min-h-6 items-center rounded-sm px-[9px] text-xs font-semibold tabular-nums {toneClasses[statusTone(status)]}"
>
  {statusLabel(status)}
</span>

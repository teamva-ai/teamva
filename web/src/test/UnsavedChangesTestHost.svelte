<script lang="ts">
  import { navigate } from "../lib/router.svelte";
  import { useUnsavedChanges } from "../lib/unsavedChanges.svelte";

  let { dirty = false, onDiscard = () => {} }: { dirty?: boolean; onDiscard?: () => void } = $props();
  const guard = useUnsavedChanges(() => dirty, { onDiscard: () => onDiscard() });
</script>

<button onclick={() => guard.allowChanges(() => navigate("/confirmed"))}>Confirmed change</button>

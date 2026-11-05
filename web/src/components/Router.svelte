<script lang="ts">
  import type { Snippet } from "svelte";
  import { location } from "../lib/router.svelte";

  let { children }: { children: Snippet } = $props();
  // Touch location.path so the router re-renders on navigation.
  $effect(() => {
    void location.path;
  });
</script>

{@render children()}

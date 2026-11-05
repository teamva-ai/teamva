<script lang="ts">
  import type { Snippet } from "svelte";
  import { location, matchPath } from "../lib/router.svelte";

  let { path, children }: { path: string; children: Snippet } = $props();
  const matched = $derived(matchPath(path, location.path) !== null);
</script>

{#if matched}{@render children()}{/if}

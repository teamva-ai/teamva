<script lang="ts">
  import { QueryClientProvider } from "@tanstack/svelte-query";
  import ProgrammingToolsPanel from "../components/ProgrammingToolsPanel.svelte";
  import { queryClient } from "../lib/query";
  import type { Computer } from "../lib/types";

  let { computer, autoDetect = false }: { computer: Computer; autoDetect?: boolean } = $props();
</script>

<QueryClientProvider client={queryClient}>
  <ProgrammingToolsPanel {computer} {autoDetect} />
</QueryClientProvider>

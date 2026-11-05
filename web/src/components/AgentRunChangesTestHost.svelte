<script lang="ts">
  import { QueryClientProvider } from "@tanstack/svelte-query";
  import { queryClient } from "../lib/query";
  import AgentRunChanges from "./AgentRunChanges.svelte";
</script>

<QueryClientProvider client={queryClient}>
  <AgentRunChanges scopeId="source-controls-test" runs={[]} repositories={[]} width={360} />
</QueryClientProvider>

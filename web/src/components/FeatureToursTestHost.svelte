<script lang="ts">
  import { QueryClientProvider } from "@tanstack/svelte-query";
  import { queryClient } from "../lib/query";
  import FeatureTours from "./FeatureTours.svelte";
  let { userId = "user-a", path = "/workbench", enabled = true }: { userId?: string; path?: string; enabled?: boolean } = $props();
</script>

<QueryClientProvider client={queryClient}><FeatureTours {userId} {path} {enabled} /></QueryClientProvider>

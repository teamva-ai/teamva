<script lang="ts">
  import { QueryClientProvider } from "@tanstack/svelte-query";
  import { queryClient } from "../lib/query";
  import PendingApprovals from "./PendingApprovals.svelte";
</script>

<QueryClientProvider client={queryClient}><PendingApprovals /></QueryClientProvider>

<script lang="ts">
  import { QueryClientProvider } from "@tanstack/svelte-query";
  import { queryClient } from "../lib/query";
  import GlobalSearch from "./GlobalSearch.svelte";
</script>

<QueryClientProvider client={queryClient}>
  <GlobalSearch projectId="project-test" projectName="Atlas" />
</QueryClientProvider>

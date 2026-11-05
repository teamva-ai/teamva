<script lang="ts">
  import { QueryClientProvider } from "@tanstack/svelte-query";
  import ProviderExecutableEditor from "../components/ProviderExecutableEditor.svelte";
  import { queryClient } from "../lib/query";

  let { provider, name }: { provider: string; name: string } = $props();
</script>

<QueryClientProvider client={queryClient}>
  <ProviderExecutableEditor {provider} {name} />
</QueryClientProvider>

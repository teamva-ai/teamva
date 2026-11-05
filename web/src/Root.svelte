<script lang="ts">
  import { QueryClientProvider } from "@tanstack/svelte-query";
  import { queryClient } from "./lib/query";
  import App from "./App.svelte";
</script>

<!--
  Query hooks read Svelte context while App is initialized, so the provider
  must be an ancestor component rather than markup inside App itself.
-->
<QueryClientProvider client={queryClient}>
  <App />
</QueryClientProvider>

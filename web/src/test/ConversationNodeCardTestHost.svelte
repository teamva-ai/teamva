<script lang="ts">
  import type { ComponentProps } from "svelte";
  import { QueryClientProvider } from "@tanstack/svelte-query";
  import { queryClient } from "../lib/query";
  import ConversationNodeCard from "../components/ConversationNodeCard.svelte";
  let { card }: { card: ComponentProps<typeof ConversationNodeCard> } = $props();
</script>

<QueryClientProvider client={queryClient}><ConversationNodeCard {...card} /></QueryClientProvider>

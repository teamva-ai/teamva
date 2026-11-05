<script lang="ts">
  import { QueryClientProvider } from "@tanstack/svelte-query";
  import { queryClient } from "../lib/query";
  import type { User, UserPreferences } from "../lib/types";
  import { provideConsoleContext } from "../pages/shared.svelte";
  import WorkAssistant from "./WorkAssistant.svelte";

  let { beforeSend, scope = "work_history" }: {
    beforeSend?: () => Promise<void>;
    scope?: "work_history" | "inspirations";
  } = $props();
  const user: User = {
    id: "user-assistant-test", version: 1, created_at: "2026-10-01T00:00:00Z",
    updated_at: "2026-10-01T00:00:00Z", email: "owner@example.test", display_name: "Owner", status: "active"
  };
  const preferences: UserPreferences = {
    user_id: user.id, locale: "en-US", locale_explicit: true, theme: "light", allow_write: false, version: 1
  };
  provideConsoleContext({
    user, computers: [], projects: [], services: [], agents: [], preferences, preferencesSaving: false,
    updatePreferences: async () => preferences, openProjectCreator: () => {}, refresh: () => {}
  });
</script>

<QueryClientProvider client={queryClient}>
  <WorkAssistant query={{ offset: 0, limit: 50 }} selected={[]} onClose={() => {}} {scope} {beforeSend} />
</QueryClientProvider>

<script lang="ts">
  import { QueryClientProvider } from "@tanstack/svelte-query";
  import { queryClient } from "../lib/query";
  import type { Computer, Project } from "../lib/types";
  import Changes from "./Changes.svelte";
  import { provideConsoleContext } from "./shared.svelte";

  let {
    projectId = "project-race"
  }: {
    projectId?: string;
  } = $props();

  const computer: Computer = {
    id: "computer-race", name: "This Mac", version: 1, user_id: "user-race",
    platform: "darwin", architecture: "arm64", fingerprint: "fingerprint-race", computer_use_enabled: false,
    status: "online", created_at: "2026-08-11T00:00:00Z", updated_at: "2026-08-11T00:00:00Z"
  };
  const project = $derived<Project>({
    id: projectId, computer_id: computer.id, name: "Race project", description: "",
    version: 1, created_at: "2026-08-11T00:00:00Z", updated_at: "2026-08-11T00:00:00Z"
  });

  provideConsoleContext({
    user: {
      id: "user-race", email: "owner@example.test", display_name: "Owner",
      status: "active",
      version: 1, created_at: "2026-08-11T00:00:00Z", updated_at: "2026-08-11T00:00:00Z"
    },
    computers: [computer], get projects() { return [project]; }, services: [], agents: [],
    computer, get project() { return project; },
    preferences: {
      user_id: "user-race", locale: "en-US", locale_explicit: true,
      theme: "system", allow_write: false, version: 1
    },
    preferencesSaving: false,
    updatePreferences: async () => {
      throw new Error("not implemented in test host");
    },
    openProjectCreator: () => {},
    refresh: () => {}
  });
</script>

<QueryClientProvider client={queryClient}>
  <Changes />
</QueryClientProvider>

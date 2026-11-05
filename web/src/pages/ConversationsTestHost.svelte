<script lang="ts">
  import { QueryClientProvider } from "@tanstack/svelte-query";
  import { queryClient } from "../lib/query";
  import type { Agent, Computer, Project, User, UserPreferences } from "../lib/types";
  import { provideConsoleContext } from "./shared.svelte";
  import Conversations from "./Conversations.svelte";

  let { projectId = "project-test", agentList }: { projectId?: string | null; agentList?: Agent[] } = $props();
  const now = "2026-10-23T00:00:00Z";
  const user: User = {
    id: "user-test", version: 1, created_at: now, updated_at: now,
    email: "owner@example.test", display_name: "Owner", status: "active"
  };
  const computer: Computer = {
    id: "computer-test", version: 1, created_at: now, updated_at: now,
    user_id: user.id, name: "This Mac", platform: "darwin", architecture: "arm64",
    fingerprint: "test", computer_use_enabled: false, status: "online"
  };
  const project = $derived<Project | undefined>(projectId ? {
    id: projectId, version: 1, created_at: now, updated_at: now,
    computer_id: computer.id, name: projectId
  } : undefined);
  const agent: Agent = {
    id: "agent-test", version: 1, created_at: now, updated_at: now,
    computer_id: computer.id, agent_service_id: "service-test", name: "Fixture Agent", status: "active", current_revision_id: "revision-test"
  };
  const preferences: UserPreferences = {
    user_id: user.id, locale: "en-US", locale_explicit: true, theme: "light", allow_write: false, version: 1
  };
  provideConsoleContext({
    user, computers: [computer], get projects() { return project ? [project] : []; }, services: [], get agents() { return agentList ?? [agent]; }, computer,
    get project() { return project; }, preferences, preferencesSaving: false,
    updatePreferences: async () => preferences, openProjectCreator: () => {}, refresh: () => {}
  });
</script>

<QueryClientProvider client={queryClient}><Conversations /></QueryClientProvider>

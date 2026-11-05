<script lang="ts">
  import { QueryClientProvider } from "@tanstack/svelte-query";
  import { queryClient } from "../lib/query";
  import type { Agent, Computer, Project, User, UserPreferences } from "../lib/types";
  import Changes from "./Changes.svelte";
  import Issues from "./Issues.svelte";
  import Schedules from "./Schedules.svelte";
  import { provideConsoleContext } from "./shared.svelte";

  let {
    route = "changes",
    projectId = "project-route"
  }: {
    route?: "changes" | "issues" | "schedules";
    projectId?: string;
  } = $props();

  const now = "2026-08-11T00:00:00Z";
  const user: User = {
    id: "user-route", email: "owner@example.test", display_name: "Owner", status: "active",
    version: 1, created_at: now, updated_at: now
  };
  const computer: Computer = {
    id: "computer-route", user_id: user.id, name: "This Mac", platform: "darwin",
    architecture: "arm64", fingerprint: "fingerprint-route", computer_use_enabled: false, status: "online",
    version: 1, created_at: now, updated_at: now
  };
  const project = $derived<Project>({
    id: projectId, computer_id: computer.id, name: `Project ${projectId}`,
    description: "", version: 1, created_at: now, updated_at: now
  });
  const agent: Agent = {
    id: "agent-route", computer_id: computer.id, agent_service_id: "service-route",
    name: "Builder", status: "active", version: 1, created_at: now, updated_at: now
  };
  const preferences: UserPreferences = {
    user_id: user.id, locale: "en-US", locale_explicit: true,
    theme: "light", allow_write: false, version: 1
  };

  provideConsoleContext({
    user, computers: [computer], get projects() { return [project]; }, services: [], agents: [agent],
    computer, get project() { return project; }, preferences, preferencesSaving: false,
    updatePreferences: async () => preferences,
    openProjectCreator: () => {}, refresh: () => {}
  });
</script>

<QueryClientProvider client={queryClient}>
  {#if route === "changes"}
    <Changes />
  {:else if route === "issues"}
    <Issues />
  {:else}
    <Schedules />
  {/if}
</QueryClientProvider>

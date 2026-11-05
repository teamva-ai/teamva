<script lang="ts">
  import type { Component } from "svelte";
  import { QueryClientProvider } from "@tanstack/svelte-query";
  import { queryClient } from "../lib/query";
  import type {
    AgentService,
    Computer,
    Project,
    User,
    UserPreferences,
    UserPreferencesPatch
  } from "../lib/types";
  import { provideConsoleContext } from "../pages/shared.svelte";

  let {
    page: Page,
    savePreferences,
    services,
    projectId = "project-test",
    isDefault = false,
    projectName = "Project"
  }: {
    page: Component;
    savePreferences?: (patch: UserPreferencesPatch) => Promise<UserPreferences>;
    services?: AgentService[];
    projectId?: string;
    isDefault?: boolean;
    projectName?: string;
  } = $props();
  const now = "2026-09-14T00:00:00Z";
  const user: User = {
    id: "user-test",
    version: 1,
    created_at: now,
    updated_at: now,
    email: "owner@example.test",
    display_name: "Owner",
    status: "active"
  };
  const computer: Computer = {
    id: "computer-test",
    version: 1,
    created_at: now,
    updated_at: now,
    user_id: user.id,
    name: "This Mac",
    platform: "darwin",
    architecture: "arm64",
    fingerprint: "test",
    computer_use_enabled: false,
    status: "online"
  };
  const project = $derived<Project>({
    id: projectId,
    version: 1,
    created_at: now,
    updated_at: now,
    computer_id: computer.id,
    name: projectName,
    is_default: isDefault
  });
  let preferences = $state<UserPreferences>({
    user_id: user.id,
    locale: "en-US",
    locale_explicit: true,
    theme: "light",
    allow_write: false,
    version: 1
  });
  let saving = $state(false);
  provideConsoleContext({
    user,
    computers: [computer],
    get projects() { return [project]; },
    get services() {
      return services ?? [];
    },
    agents: [],
    computer,
    get project() { return project; },
    get preferences() {
      return preferences;
    },
    get preferencesSaving() {
      return saving;
    },
    updatePreferences: async (patch) => {
      saving = true;
      try {
        preferences = savePreferences
          ? await savePreferences(patch)
          : { ...preferences, ...patch, version: preferences.version + 1 };
        return preferences;
      } finally {
        saving = false;
      }
    },
    openProjectCreator: () => {},
    refresh: () => {}
  });
</script>

<QueryClientProvider client={queryClient}><Page /></QueryClientProvider>

<script lang="ts">
  import { QueryClientProvider } from "@tanstack/svelte-query";
  import type { GitRepositorySummary } from "../lib/gitChanges";
  import { queryClient } from "../lib/query";
  import type { Agent, AgentService, Issue } from "../lib/types";
  import IssuePanel from "./IssuePanel.svelte";

  let { repositories, agents: agentsProp, services = [], issue: issueProp, projectId = "project-test" }: {
    repositories: GitRepositorySummary[];
    agents?: Agent[];
    services?: AgentService[];
    issue?: Issue;
    projectId?: string;
  } = $props();
  const now = "2026-08-11T10:00:00Z";
  const defaultIssue: Issue = {
    id: "issue-test", version: 1, project_id: "project-test",
    title: "Workspace task", description: "Test",
    status: "open", worktree_hint: false, created_by_user_id: "user-test",
    created_at: now, updated_at: now
  };
  const defaultAgents: Agent[] = [{
    id: "agent-test", version: 1, computer_id: "computer-test",
    agent_service_id: "service-test", name: "Builder", status: "active",
    created_at: now, updated_at: now
  }];
  const agents = $derived(agentsProp ?? defaultAgents);
</script>

<QueryClientProvider client={queryClient}>
  <IssuePanel
    issue={issueProp ?? defaultIssue}
    {agents}
    {services}
    {projectId}
    {repositories}
    onIssueChange={() => {}}
  />
</QueryClientProvider>

<script lang="ts">
  import { QueryClientProvider } from "@tanstack/svelte-query";
  import type { GitRepositorySummary } from "../lib/gitChanges";
  import { queryClient } from "../lib/query";
  import type { IssueAgentInstance } from "../lib/types";
  import AgentSideSheet from "../pages/AgentSideSheet.svelte";

  const instance: IssueAgentInstance = {
    id: "instance-1",
    issue_id: "issue-1",
    agent_id: "agent-1",
    agent_revision_id: "revision-1",
    status: "idle",
    pending_mention_count: 0,
    version: 1,
    created_at: "2026-08-11T00:00:00Z",
    updated_at: "2026-08-11T00:00:00Z"
  };
  const repositories: GitRepositorySummary[] = [{
    id: "repo-test", label: "App", root_display: "App", resource_ids: [],
    discovery_origin: "resource", worktree_kind: "main", scope: "full", branch: null,
    operation: null, generation: 1, state: "clean", counts: {}, warnings: []
  }];
</script>

<QueryClientProvider client={queryClient}>
  <AgentSideSheet
    issueId="issue-1"
    projectId="project-1"
    expectedIssueVersion={1}
    {instance}
    agentName="Test Agent"
    sessionLabel="Test Agent · Session 1"
    {repositories}
    onClose={() => {}}
    onInstanceChange={() => {}}
  />
</QueryClientProvider>

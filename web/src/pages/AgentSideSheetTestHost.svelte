<script lang="ts">
  import { QueryClientProvider } from "@tanstack/svelte-query";
  import type { GitRepositorySummary } from "../lib/gitChanges";
  import { queryClient } from "../lib/query";
  import type { IssueAgentInstance } from "../lib/types";
  import AgentSideSheet from "./AgentSideSheet.svelte";

  let {
    instance, repositories, open = true, projectId = "project-test", onClose = () => {}
  }: {
    instance: IssueAgentInstance;
    repositories: GitRepositorySummary[];
    open?: boolean;
    projectId?: string;
    onClose?: () => void;
  } = $props();
</script>

<QueryClientProvider client={queryClient}>
  {#if open}
  <AgentSideSheet
    issueId={instance.issue_id}
    {projectId}
    expectedIssueVersion={1}
    {instance}
    agentName="Builder"
    sessionLabel="Builder · Session 1"
    {repositories}
    {onClose}
    onInstanceChange={() => {}}
  />
  {/if}
</QueryClientProvider>

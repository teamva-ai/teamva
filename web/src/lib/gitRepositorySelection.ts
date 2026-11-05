import type { ContentBlock } from "./types";

export function issueRunRequestBody(options: {
  instanceId: string;
  expectedIssueVersion: number;
  input?: string;
  attachments?: ContentBlock[];
  intent?: "message" | "revise_comment";
  internal?: boolean;
  goal?: boolean;
  goalTokenBudget?: number;
}): Record<string, unknown> {
  return {
    issue_agent_instance_id: options.instanceId,
    input: options.input,
    attachments: options.attachments,
    intent: options.intent,
    internal: options.internal,
    goal: options.goal,
    goal_token_budget: options.goalTokenBudget,
    expected_issue_version: options.expectedIssueVersion
  };
}

export function issueCreateRequestBody(options: {
  title: string;
  description: string;
  worktreeHint: boolean;
}): Record<string, unknown> {
  return {
    title: options.title.trim(),
    description: options.description.trim(),
    worktree_hint: options.worktreeHint
  };
}

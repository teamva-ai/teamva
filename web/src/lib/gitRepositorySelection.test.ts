import { describe, expect, it } from "vitest";
import {
  issueCreateRequestBody, issueRunRequestBody
} from "./gitRepositorySelection";

describe("project workspace request bodies", () => {
  it("creates Issue run requests without a single-repository binding", () => {
    expect(issueRunRequestBody({
      instanceId: "instance-1", expectedIssueVersion: 7,
      intent: "revise_comment", internal: true
    })).toMatchObject({
      issue_agent_instance_id: "instance-1",
      expected_issue_version: 7,
      intent: "revise_comment",
      internal: true
    });
    expect(issueRunRequestBody({
      instanceId: "instance-1", expectedIssueVersion: 7
    })).not.toHaveProperty("git_repository_id");
  });

  it("carries image attachment blocks to an Issue run", () => {
    const attachments = [{
      type: "image" as const, mime_type: "image/png", filename: "capture.png",
      data_url: "data:image/png;base64,iVBORw0KGgo="
    }];
    expect(issueRunRequestBody({
      instanceId: "instance-1", expectedIssueVersion: 7, attachments
    })).toMatchObject({ attachments });
  });

  it("creates Issues without a default repository binding", () => {
    expect(issueCreateRequestBody({
      title: " Task ", description: " Details ", worktreeHint: true
    })).toEqual({
      title: "Task", description: "Details", worktree_hint: true
    });
  });
});

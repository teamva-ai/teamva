import { render, screen } from "@testing-library/svelte";
import { describe, expect, it, vi } from "vitest";
import { t } from "../lib/i18n.svelte";
import type { IssueWorkItem } from "../lib/types";
import IssueWorkItems from "./IssueWorkItems.svelte";

describe("Issue work status projections", () => {
  it("shows real dispatch, approval and stop states without treating them as unknown", () => {
    const statuses = ["ready", "waiting_approval", "cancelling", "waiting_dependencies"];
    const items: IssueWorkItem[] = statuses.map((status, index) => ({
      key: `work-${index}`, agent_id: "agent-a", title: `work ${index}`, instruction: "", after: [], status,
      status_reason: index === 0 ? "issue_paused" : index === 3 ? "issue_not_open" : undefined
    }));
    render(IssueWorkItems, { items, agentName: () => "Agent A", onOpenSession: vi.fn() });
    expect(screen.getByText(t("issueWork.ready"))).toBeInTheDocument();
    expect(screen.getByText(t("status.waiting_approval"))).toBeInTheDocument();
    expect(screen.getByText(t("status.cancelling"))).toBeInTheDocument();
    expect(screen.getByText(t("issueWork.pausedSession"))).toBeInTheDocument();
    expect(screen.getByText(t("issueWork.issueNotOpen"))).toBeInTheDocument();
    expect(screen.queryByText(t("issueWork.unknownStatus", { status: "ready" }))).not.toBeInTheDocument();
  });
});

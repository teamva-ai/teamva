import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/svelte";
import { afterEach, describe, expect, it, vi } from "vitest";
import { queryClient } from "../lib/query";
import { navigate } from "../lib/router.svelte";
import PendingApprovalsTestHost from "./PendingApprovalsTestHost.svelte";

const approval = {
  id: "approval-1", version: 3, project_id: "another-project", issue_id: "issue-2", run_id: "run-2",
  status: "pending", action_kind: "shell", action_digest: "digest-2", risk: "high",
  normalized_action: { command: "echo review-this-action" }, expires_at: "2027-01-01T00:00:00Z"
};
const response = (data: unknown) => new Response(JSON.stringify({ data }), {
  status: 200, headers: { "Content-Type": "application/json" }
});

afterEach(() => {
  cleanup();
  queryClient.clear();
  vi.unstubAllGlobals();
  navigate("/workbench", { replace: true });
});

describe("global pending approvals", () => {
  it("surfaces approvals from another project and opens the exact session", async () => {
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
      const path = new URL(String(input), "http://localhost").pathname;
      if (path.endsWith("/runs/run-2")) return response({
        id: "run-2", project_id: "another-project", issue_id: "issue-2", issue_agent_instance_id: "session-2"
      });
      return response({ items: [approval] });
    }));
    render(PendingApprovalsTestHost);
    await fireEvent.click(await screen.findByRole("button", { name: "Approval required" }));
    const dialog = screen.getByRole("dialog", { name: "Approval required" });
    await fireEvent.click(within(dialog).getByRole("button", { name: "Open session" }));
    await waitFor(() => {
      const params = new URLSearchParams(window.location.search);
      expect(window.location.pathname).toBe("/issues");
      expect(Object.fromEntries(params)).toEqual({ project: "another-project", issue: "issue-2", session: "session-2", run: "run-2" });
    });
  });

  it("requires opening an individual action before sending a scoped decision", async () => {
    const requests: RequestInit[] = [];
    let decided = false;
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      if (String(input).includes("/decisions")) {
        requests.push(init!);
        decided = true;
        return response({ ...approval, status: "approved" });
      }
      return response({ items: decided ? [] : [approval] });
    }));
    render(PendingApprovalsTestHost);
    await fireEvent.click(await screen.findByRole("button", { name: "Approval required" }));
    const dialog = screen.getByRole("dialog");
    expect(within(dialog).queryByRole("button", { name: "Approve" })).not.toBeInTheDocument();
    await fireEvent.click(within(dialog).getByRole("button", { name: "Approval required" }));
    expect(within(dialog).getByText(/review-this-action/)).toBeInTheDocument();
    await fireEvent.click(within(dialog).getByRole("button", { name: "Approve" }));
    await waitFor(() => expect(requests).toHaveLength(1));
    expect(JSON.parse(String(requests[0].body))).toEqual({
      expected_version: 3, action_digest: "digest-2", decision: "approved", scope: "once"
    });
  });

  it("does not turn a failed global query into an invisible zero count", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => new Response(JSON.stringify({ error: { code: "unavailable" } }), {
      status: 503, headers: { "Content-Type": "application/json" }
    })));
    render(PendingApprovalsTestHost);
    await fireEvent.click(await screen.findByRole("button", { name: "Approval required" }));
    expect(screen.getByRole("dialog")).toBeInTheDocument();
    expect(screen.getByRole("alert")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Retry" })).toBeInTheDocument();
  });
});

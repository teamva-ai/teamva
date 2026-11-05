import { describe, expect, it } from "vitest";
import type { Run } from "./types";
import { scheduleErrorCode, scheduleRunTarget } from "./scheduleHistory";

describe("schedule history safety", () => {
  it("only exposes known bounded codes, never arbitrary diagnostics or credential-like strings", () => {
    expect(scheduleErrorCode("agent_not_ready")).toBe("agent_not_ready");
    expect(scheduleErrorCode("schedule_no_next_run")).toBe("schedule_no_next_run");
    expect(scheduleErrorCode("")).toBe("");
    for (const value of ["secret_token", "x".repeat(10_000), "/private/provider/credentials", "password=secret"]) {
      expect(scheduleErrorCode(value)).toBe("schedule_dispatch_failed");
    }
  });

  it("requires matching run/project ownership before constructing a deep link", () => {
    const run = { id: "run/target", project_id: "project", issue_id: "issue target", issue_agent_instance_id: "instance" } as Run;
    expect(scheduleRunTarget(run, "project", "run/target")).toBe("/issues?issue=issue+target&session=instance&run=run%2Ftarget");
    expect(scheduleRunTarget(run, "other", run.id)).toBeNull();
    expect(scheduleRunTarget(run, "project", "other-run")).toBeNull();
    expect(scheduleRunTarget({ ...run, issue_id: "" }, "project", run.id)).toBeNull();
  });
});

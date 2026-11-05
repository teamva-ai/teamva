import { expect, it } from "vitest";
import { diagnosticText, sanitizeDiagnosticPayload } from "./runDiagnostics";
import { projectActivityRunEvent } from "./activityEvents";

it("keeps native V2 response.body and removes credentials before HTTP/SSE Activity retention", () => {
  const payload = {
    error: { message: "Gateway 429 rejected", status: 429, response: { body: '{"error":{"message":"Quota exhausted"},"Authorization":"Bearer fixture-only-auth"}' } },
    detail: "https://fixture-only-user:fixture-only-password@gateway.test/v1?key=fixture-only-key&model=example",
    headers: { Cookie: "session=fixture-only-cookie" }
  };
  const event = projectActivityRunEvent({ id: "event", run_id: "run", seq: 1, event_type: "run.error", payload, occurred_at: "2026-11-02T00:00:00Z" })!;
  expect(JSON.stringify(event)).not.toContain("fixture-only-");
  expect(diagnosticText(event.payload)).toContain("Quota exhausted");
  expect(diagnosticText(event.payload)).toContain("HTTP 429");
  expect(diagnosticText(event.payload)).toContain("model=example");
  expect(JSON.stringify(payload)).toContain("fixture-only-auth");
  expect(sanitizeDiagnosticPayload(event.payload)).toEqual(event.payload);
});

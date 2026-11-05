import { request } from "@playwright/test";

// Existing business-flow tests deliberately start after the seven tutorials.
// The dedicated first-entry run opts out and uses a fresh isolated data dir.
// This is test setup only, never a product flag or a client-side persistence bypass.
export default async function setup(config) {
  if (process.env.TEAMVA_TEST_UNSEEN_FEATURE_TOURS === "1") return;
  const baseURL = config.projects[0].use.baseURL;
  const client = await request.newContext({ baseURL });
  try {
    const headers = { Origin: baseURL, "Content-Type": "application/json" };
    const session = await client.post("/v1/auth/local-session", { headers, data: {} });
    if (!session.ok()) throw new Error(`Tutorial test setup: local authentication returned ${session.status()}`);
    headers["X-CSRF-Token"] = (await session.json()).data.csrf_token;
    const response = await client.get("/v1/me/feature-tours", { headers });
    if (!response.ok()) throw new Error(`Tutorial test setup: feature tour API returned ${response.status()}; rebuild the isolated server`);
    const seen = (await response.json()).data.seen;
    for (const id of ["overview", "issues", "conversations", "resources", "memories", "schedules", "changes"]) {
      if (seen.includes(id)) continue;
      const saved = await client.post(`/v1/me/feature-tours/${id}/seen`, { headers, data: {} });
      if (!saved.ok()) throw new Error(`Tutorial test setup: marking ${id} returned ${saved.status()}`);
    }
  } finally {
    await client.dispose();
  }
}

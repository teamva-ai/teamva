/** The same source manifests drive Go registration and frontend discovery. */
export interface ProviderDefinition {
  api_version: number;
  id: string;
  display_name: string;
  command: string;
  description: string;
  description_key?: string;
  install_hint?: string;
  login_hint?: string;
  docs_url?: string;
  diagnostic_steps?: Array<{ id: string; label: string }>;
}

const manifests = import.meta.glob<ProviderDefinition>("../../../node/provider/*/provider.json", {
  eager: true, import: "default"
});
export const providerDefinitions = Object.values(manifests).sort((a, b) => a.id.localeCompare(b.id));
export function providerDefinition(id: string): ProviderDefinition | undefined {
  return providerDefinitions.find((item) => item.id === id);
}

const views = import.meta.glob<import("../../../node/provider/view").ProviderView>("../../../node/provider/*/view.ts", {
  eager: true, import: "default"
});
export const providerViews = Object.entries(views).sort(([a], [b]) => a.localeCompare(b))
  .map(([path, view]) => ({ ...view, providerId: path.split("/").at(-2)! }));
export function viewsForPayload(payload: Record<string, unknown>) {
  return providerViews.filter((view) => view.providerId === payload.provider_id);
}

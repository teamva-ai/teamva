import type { FeatureTourId } from "../locales/featureTours";

export type { FeatureTourId } from "../locales/featureTours";

export const FEATURE_TOUR_IDS = ["overview", "issues", "conversations", "resources", "memories", "schedules", "changes"] as const satisfies readonly FeatureTourId[];

export const FEATURE_TOUR_ROUTES: Record<FeatureTourId, string> = {
  overview: "/workbench", issues: "/issues", conversations: "/conversations", resources: "/resources",
  memories: "/memories", schedules: "/schedules", changes: "/changes"
};

export interface FeatureTourState { seen: FeatureTourId[] }

export function featureTourForPath(path: string): FeatureTourId | undefined {
  return FEATURE_TOUR_IDS.find((id) => FEATURE_TOUR_ROUTES[id] === path);
}

export function parseFeatureTourState(value: unknown): FeatureTourState {
  const seen = (value as { seen?: unknown } | null)?.seen;
  if (!Array.isArray(seen) || seen.some((id) => !FEATURE_TOUR_IDS.includes(id))) {
    throw new Error("Invalid feature tour state");
  }
  return { seen: FEATURE_TOUR_IDS.filter((id) => seen.includes(id)) };
}

export function mergeFeatureTourState(previous: FeatureTourState | undefined, next: FeatureTourState): FeatureTourState {
  return { seen: FEATURE_TOUR_IDS.filter((id) => previous?.seen.includes(id) || next.seen.includes(id)) };
}

export function featureTourQueryKey(userId: string) { return ["feature-tours", userId] as const; }

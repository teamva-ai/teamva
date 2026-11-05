import { describe, expect, it } from "vitest";
import { FEATURE_TOUR_IDS, FEATURE_TOUR_ROUTES, featureTourForPath, featureTourQueryKey, mergeFeatureTourState, parseFeatureTourState } from "./featureTours";

describe("feature tour state", () => {
  it("only maps the seven existing project columns", () => {
    for (const id of FEATURE_TOUR_IDS) expect(featureTourForPath(FEATURE_TOUR_ROUTES[id])).toBe(id);
    for (const path of ["/settings", "/agents", "/skills", "/work-records", "/inspirations", "/missing", "/issues/other"])
      expect(featureTourForPath(path)).toBeUndefined();
  });
  it("normalizes a valid seen set without inventing seen sections", () => {
    expect(parseFeatureTourState({ seen: ["changes", "overview", "changes"] })).toEqual({ seen: ["overview", "changes"] });
    expect(parseFeatureTourState({ seen: [] })).toEqual({ seen: [] });
  });
  it.each([null, {}, { seen: null }, { seen: "overview" }, { seen: ["settings"] }, { seen: [1] }])("rejects malformed state rather than treating it as an empty first visit (%j)", (value) => {
    expect(() => parseFeatureTourState(value)).toThrow("Invalid feature tour state");
  });
  it("merges out-of-order acknowledgements and scopes cache keys to users", () => {
    expect(mergeFeatureTourState({ seen: ["resources", "changes"] }, { seen: ["overview"] }))
      .toEqual({ seen: ["overview", "resources", "changes"] });
    expect(featureTourQueryKey("user-a")).not.toEqual(featureTourQueryKey("user-b"));
  });
});

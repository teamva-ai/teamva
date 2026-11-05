import { describe, expect, it } from "vitest";
import { visibleTextContent } from "./helpers";

describe("visibleTextContent", () => {
  it("keeps user messages and hides internal provider control instructions", () => {
    expect(visibleTextContent([
      { text: "visible" },
      { text: "internal revise-comment prompt", internal: true },
      { text: " follow-up", internal: false }
    ])).toBe("visible follow-up");
  });

  it("returns an empty string for an internal-only run", () => {
    expect(visibleTextContent([{ text: "internal", internal: true }])).toBe("");
  });
});

import { cleanup, render } from "@testing-library/svelte";
import { afterEach, describe, expect, it } from "vitest";
import { statusLabel, statusTone } from "../lib/helpers";
import StatusBadge from "./StatusBadge.svelte";

afterEach(cleanup);

describe("status badge semantic tones", () => {
  it.each(["waiting_approval", "pending", "pending_validation", "validation_pending", "queued"])(
    "renders %s with the warning palette instead of falling back to neutral", (status) => {
      const { container } = render(StatusBadge, { status });
      expect(container.firstElementChild).toHaveClass("bg-warning-subtle", "text-warning");
      expect(container.firstElementChild).toHaveTextContent(statusLabel(status));
    }
  );

  it.each([
    ["ready", "success"], ["failed", "danger"], ["probing", "running"], ["unknown", "neutral"]
  ])("maps %s to its explicit %s tone", (status, tone) => {
    const { container } = render(StatusBadge, { status });
    expect(statusTone(status)).toBe(tone);
    expect(container.firstElementChild).toHaveClass(tone === "neutral" ? "text-secondary" : `text-${tone}`);
  });
});

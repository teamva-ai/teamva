import { cleanup, fireEvent, render, screen } from "@testing-library/svelte";
import { afterEach, describe, expect, it } from "vitest";
import { t } from "../lib/i18n.svelte";
import type { WorkCondition } from "../lib/workRecords";
import WorkFilterBuilder from "./WorkFilterBuilder.svelte";

afterEach(cleanup);

describe("WorkFilterBuilder condition controls", () => {
  it("uses a shared default-size icon button to remove a condition without submitting", async () => {
    const value: WorkCondition = { join: "and", conditions: [{ field: "action", operator: "contains", value: "create" }] };
    const { container } = render(WorkFilterBuilder, { value });
    const remove = screen.getByRole("button", { name: t("common.delete") });
    expect(remove).toHaveClass("am-button");
    expect(remove).toHaveAttribute("data-size", "md");
    expect(remove).toHaveAttribute("data-icon", "true");
    expect(remove).toHaveAttribute("type", "button");
    expect(remove).not.toHaveClass("size-9");
    expect(screen.getByRole("textbox", { name: t("work.value") }).parentElement).toHaveClass("max-sm:grid-cols-1");
    expect(container.querySelector(".am-field-group")).toBeNull();
    await fireEvent.click(remove);
    expect(value.conditions).toEqual([]);
  });
});

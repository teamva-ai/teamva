import { cleanup, render, screen, within } from "@testing-library/svelte";
import { afterEach, describe, expect, it } from "vitest";
import FieldGroupTestHost from "./FieldGroupTestHost.svelte";

afterEach(cleanup);

describe("FieldGroup structure and accessibility", () => {
  it("keeps actions outside Field context and in their own shared control track", () => {
    render(FieldGroupTestHost, { showActions: true });
    const group = screen.getByTestId("field-group");
    const action = within(group).getByRole("button", { name: "Apply fields" });
    expect(group).toHaveAttribute("data-has-actions", "true");
    expect(action).toHaveAttribute("type", "button");
    expect(action.closest(".am-field")).toBeNull();
    expect(action.closest(".am-field-group__action-controls")?.parentElement).toHaveClass("am-field-group__actions");
    expect(action).not.toHaveAttribute("aria-labelledby");
  });

  it("keeps direct fields with a single label/control/hint track and associated IDs", () => {
    render(FieldGroupTestHost);
    const group = screen.getByTestId("field-group");
    const fields = [...group.children];
    expect(fields).toHaveLength(3);
    for (const field of fields) {
      expect(field).toHaveClass("am-field");
      expect(field.querySelectorAll(":scope > .am-field__label")).toHaveLength(1);
      expect(field.querySelectorAll(":scope > .am-field__control")).toHaveLength(1);
      const label = field.querySelector<HTMLLabelElement>(":scope > .am-field__label")!;
      const control = document.getElementById(label.htmlFor)!;
      expect(field.querySelector(".am-field__control")).toContainElement(control);
      expect(control).toHaveAttribute("aria-labelledby", label.id);
      const hint = field.querySelector(":scope > .am-field__hint");
      if (hint) expect(control).toHaveAttribute("aria-describedby", hint.id);
      else expect(control).not.toHaveAttribute("aria-describedby");
    }
    expect(screen.getByLabelText("Name")).toHaveAttribute("id", "group-name");
    expect(screen.getByLabelText("Name")).toHaveAccessibleDescription("Name hint");
    const ids = [...document.querySelectorAll("[id]")].map((element) => element.id);
    expect(new Set(ids).size).toBe(ids.length);
  });

  it("keeps ModelPicker's composite input and auxiliary trigger in one control slot", () => {
    render(FieldGroupTestHost);
    const input = screen.getByRole("combobox", { name: "Model" });
    const field = input.closest(".am-field")!;
    const slot = field.querySelector(":scope > .am-field__control")!;
    expect(slot).toContainElement(input);
    const trigger = within(slot as HTMLElement).getByRole("button");
    expect(trigger).toHaveAttribute("type", "button");
    expect(trigger).toHaveAttribute("aria-controls", input.getAttribute("list"));
    expect(trigger).not.toHaveAttribute("aria-labelledby");
    expect(trigger).not.toHaveAttribute("id", input.id);
    expect(input).toHaveAccessibleDescription("Choose or enter a model");
    expect(field.querySelectorAll(":scope > .am-field__control")).toHaveLength(1);
  });

  it("removes the description association when a hint is removed", async () => {
    const { rerender } = render(FieldGroupTestHost);
    await rerender({ hint: "" });
    expect(screen.getByLabelText("Name")).not.toHaveAttribute("aria-describedby");
    expect(screen.getByLabelText("Name").closest(".am-field")!.querySelector(".am-field__hint")).toBeNull();
  });

  it("preserves native disabled fieldset semantics through the group", () => {
    render(FieldGroupTestHost, { disabled: true });
    for (const name of ["Name", "Provider", "Model"]) expect(screen.getByLabelText(name)).toBeDisabled();
    for (const button of screen.getAllByRole("button")) expect(button).toBeDisabled();
  });

  it.each(["sm", "md", "lg"] as const)("forwards the %s breakpoint, columns and HTML attributes without extra slot wrappers", (collapseAt) => {
    render(FieldGroupTestHost, { columns: 3, collapseAt });
    const group = screen.getByTestId("field-group");
    expect(group).toHaveClass("am-field-group", `am-field-group--${collapseAt}`, "host-group");
    expect(group).toHaveAttribute("aria-label", "Configuration");
    expect(group.style.getPropertyValue("--am-field-columns")).toBe("3");
    expect(group.querySelectorAll(":scope > .am-field")).toHaveLength(3);
    expect(within(group).queryByRole("button", { name: "Outside group" })).toBeNull();
  });

  it("defaults to two columns and the medium collapse breakpoint", () => {
    render(FieldGroupTestHost);
    const group = screen.getByTestId("field-group");
    expect(group).toHaveClass("am-field-group--md");
    expect(group.style.getPropertyValue("--am-field-columns")).toBe("2");
  });
});

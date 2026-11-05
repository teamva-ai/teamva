import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/svelte";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { applyLocale, t } from "../lib/i18n.svelte";
import { fromLocalDateTime, toLocalDateTime } from "./dateValue";
import ControlsTestHost from "./ControlsTestHost.svelte";

beforeEach(() => applyLocale("en-US"));
afterEach(cleanup);
const state = () => JSON.parse(screen.getByLabelText("State").textContent ?? "{}");

describe("shared form primitives", () => {
  it("preserves string, number, textarea and native DOM-ref bindings", async () => {
    render(ControlsTestHost);
    const text = screen.getByLabelText("Text field");
    await fireEvent.input(text, { target: { value: "new text" } });
    await fireEvent.input(screen.getByLabelText("Count"), { target: { value: "12" } });
    await fireEvent.input(screen.getByLabelText("Notes"), { target: { value: "new notes" } });
    expect(state()).toMatchObject({ text: "new text", count: 12, note: "new notes" });
    await fireEvent.input(screen.getByLabelText("Count"), { target: { value: "" } });
    // Preserve Svelte's native numeric binding: an empty number input is null.
    expect(state().count).toBeNull();
    await fireEvent.click(screen.getByRole("button", { name: "Focus text" }));
    expect(text).toHaveFocus();
    expect(text).toHaveAttribute("required");
    expect(text).toHaveAttribute("maxlength", "100");
  });

  it("keeps native select values, numeric options, labels and change callbacks", async () => {
    render(ControlsTestHost);
    const select = screen.getByRole("combobox", { name: "Choice" });
    await fireEvent.change(select, { target: { value: "two" } });
    await fireEvent.change(screen.getByRole("combobox", { name: "Numeric choice" }), { target: { value: "8" } });
    expect(state()).toMatchObject({ selected: "two", numeric: 8, changes: 1 });
    const wrapper = select.closest(".am-select")!;
    const icon = wrapper.querySelector(".am-select__icon");
    expect(icon).toHaveAttribute("aria-hidden", "true");
    expect(select).toHaveClass("am-select__control");
    expect(within(wrapper as HTMLElement).getByRole("option", { name: "Two" })).toBeInTheDocument();
  });

  it("uses checkbox and switch semantics without inheriting text-input sizing", async () => {
    render(ControlsTestHost);
    const checkbox = screen.getByRole("checkbox", { name: "Check choice" });
    const toggle = screen.getByRole("switch", { name: "Switch choice" });
    await fireEvent.click(checkbox); await fireEvent.click(toggle);
    expect(checkbox).toBeChecked(); expect(toggle).toBeChecked();
    expect(state()).toMatchObject({ checked: true, enabled: true });
    expect(checkbox).toHaveClass("am-checkbox");
    expect(checkbox).not.toHaveClass("am-input");
    expect(toggle).toHaveAttribute("type", "checkbox");
  });

  it("does not duplicate a composite field's ID or label onto auxiliary controls", () => {
    render(ControlsTestHost);
    const primary = screen.getByLabelText("Composite field");
    const auxiliary = screen.getByRole("checkbox", { name: "Auxiliary option" });
    expect(primary).toHaveAccessibleDescription("Primary control description");
    expect(auxiliary).not.toHaveAttribute("aria-labelledby");
    expect(auxiliary.id).not.toBe(primary.id);
    const ids = [...document.querySelectorAll("[id]")].map((node) => node.id);
    expect(new Set(ids).size).toBe(ids.length);
  });

  it("forwards fieldset disabled state and prevents disabled submissions", async () => {
    render(ControlsTestHost, { props: { disabled: true } });
    for (const name of ["Text field", "Count", "Choice", "Notes", "Date", "ISO time"]) expect(screen.getByLabelText(name)).toBeDisabled();
    expect(screen.getByRole("checkbox", { name: "Check choice" })).toBeDisabled();
    expect(screen.getByRole("switch", { name: "Switch choice" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Submit" })).toBeDisabled();
    await fireEvent.click(screen.getByRole("button", { name: "Submit" }));
    expect(state().submissions).toBe(0);
  });

  it("keeps explicit Submit/Reset behavior and native form reset bindings", async () => {
    render(ControlsTestHost);
    await fireEvent.input(screen.getByLabelText("Text field"), { target: { value: "changed" } });
    await fireEvent.click(screen.getByRole("checkbox", { name: "Check choice" }));
    await fireEvent.click(screen.getByRole("button", { name: "Submit" }));
    expect(state().submissions).toBe(1);
    await fireEvent.click(screen.getByRole("button", { name: "Reset" }));
    await waitFor(() => expect(state()).toMatchObject({ text: "initial", checked: false }));
    expect(screen.getByRole("button", { name: "Focus text" })).toHaveAttribute("type", "button");
  });

  it("preserves date values and serializes local datetime selections as RFC3339", async () => {
    render(ControlsTestHost);
    expect(screen.getByLabelText("Date")).toHaveAttribute("type", "date");
    const iso = screen.getByLabelText("ISO time");
    expect(iso).toHaveValue(toLocalDateTime("2040-01-02T03:04:05.123Z"));
    const chosen = "2040-12-31T09:30";
    await fireEvent.input(iso, { target: { value: chosen } });
    expect(state().iso).toBe(new Date(chosen).toISOString());
    await fireEvent.input(iso, { target: { value: "" } });
    expect(state().iso).toBe("");
  });

  it("limits model chips while retaining an off-page selected model and all suggestions", async () => {
    render(ControlsTestHost);
    const more = screen.getByRole("button", { name: t("controls.modelShowMore", { count: 40 }) });
    expect(screen.queryByRole("button", { name: "model-39" })).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "model-20" })).toBeInTheDocument();
    expect(document.querySelectorAll("datalist option")).toHaveLength(40);
    await fireEvent.click(more);
    await fireEvent.click(screen.getByRole("button", { name: "model-39" }));
    expect(state().model).toBe("model-39");
    await fireEvent.click(screen.getByRole("button", { name: t("controls.modelShowLess") }));
    expect(screen.getByRole("button", { name: "model-39" })).toBeInTheDocument();
  });

  it.each([false, true])("opens model suggestions without submitting the parent form (picker throws: %s)", async (throws) => {
    render(ControlsTestHost);
    const input = screen.getByRole("combobox", { name: "Model selection" });
    const showPicker = vi.fn(() => { if (throws) throw new DOMException("Unsupported picker", "NotSupportedError"); });
    Object.defineProperty(input, "showPicker", { configurable: true, value: showPicker });
    const trigger = screen.getByRole("button", { name: t("controls.chooseModel") });
    expect(trigger).toHaveAttribute("type", "button");
    expect(trigger.closest("form")).toBe(screen.getByRole("form", { name: "Primitive form" }));
    expect(trigger).toHaveAttribute("aria-controls", input.getAttribute("list"));
    await fireEvent.click(trigger);
    expect(showPicker).toHaveBeenCalledOnce();
    expect(input).toHaveFocus();
    expect(state()).toMatchObject({ model: "model-20", submissions: 0 });
    // The parent really is submittable; do not mask the button's native default.
    await fireEvent.click(screen.getByRole("button", { name: "Submit" }));
    expect(state().submissions).toBe(1);
  });
});

describe("date conversion", () => {
  it("round-trips explicit offsets including seconds and milliseconds", () => {
    for (const value of ["2040-01-02T03:04:00+08:00", "2040-05-02T03:04:05.123-05:00", "2040-01-02T00:00:00Z"]) {
      expect(new Date(fromLocalDateTime(toLocalDateTime(value))).getTime()).toBe(new Date(value).getTime());
    }
  });
  it("handles empty/invalid fields without RangeError or fabricated dates", () => {
    expect(toLocalDateTime("")).toBe(""); expect(toLocalDateTime("not-a-time")).toBe("");
    expect(fromLocalDateTime("")).toBe(""); expect(fromLocalDateTime("not-a-time")).toBe("");
  });
});

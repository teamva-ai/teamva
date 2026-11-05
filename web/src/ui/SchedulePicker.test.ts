import { cleanup, fireEvent, render, screen } from "@testing-library/svelte";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { applyLocale, t } from "../lib/i18n.svelte";
import { defaultScheduleConfig, type ScheduleConfig } from "../lib/scheduleConfig";
import SchedulePickerTestHost from "./SchedulePickerTestHost.svelte";

beforeEach(() => applyLocale("en-US"));
afterEach(cleanup);
const state = (): ScheduleConfig => JSON.parse(screen.getByLabelText("Schedule state").textContent ?? "{}");
const initial = (overrides: Partial<ScheduleConfig> = {}): ScheduleConfig => ({ ...defaultScheduleConfig(), timezone: "UTC", ...overrides });
const changeKind = (kind: string) => fireEvent.change(screen.getByLabelText(t("schedules.scheduleKind")), { target: { value: kind } });

describe("SchedulePicker native bindings", () => {
  it("edits multiple exact time pairs, retains one row, and has no duplicate IDs", async () => {
    render(SchedulePickerTestHost, { initial: initial() });
    expect(screen.getByRole("button", { name: t("scheduleEditor.removeTime", { index: 1 }) })).toBeDisabled();
    await fireEvent.input(screen.getByLabelText(t("scheduleEditor.time", { index: 1 })), { target: { value: "09:15" } });
    await fireEvent.click(screen.getByRole("button", { name: t("scheduleEditor.addTime") }));
    await fireEvent.input(screen.getByLabelText(t("scheduleEditor.time", { index: 2 })), { target: { value: "14:45" } });
    expect(state().times).toEqual(["09:15", "14:45"]);
    const ids = [...document.querySelectorAll("[id]")].map((node) => node.id);
    expect(new Set(ids).size).toBe(ids.length);
    await fireEvent.click(screen.getByRole("button", { name: t("scheduleEditor.removeTime", { index: 1 }) }));
    expect(state().times).toEqual(["14:45"]);
  });
  it("uses Sunday=0, preserves times, and restores expression drafts across kinds", async () => {
    render(SchedulePickerTestHost, { initial: initial({ kind: "once", expression: "2040-01-02T03:04:05.123Z" }) });
    await changeKind("weekly");
    await fireEvent.click(screen.getByRole("checkbox", { name: "Sun" }));
    expect(state().weekdays).toEqual([1, 0]);
    expect(state().times).toEqual(["09:00"]);
    await changeKind("cron");
    await fireEvent.input(screen.getByLabelText(t("schedules.cronExpression")), { target: { value: "15 9 * * 1" } });
    await changeKind("once");
    expect(state().expression).toBe("2040-01-02T03:04:05.123Z");
    await changeKind("cron");
    expect(state().expression).toBe("15 9 * * 1");
  });
  it("edits a selected-zone wall clock and changes zones without losing seconds/ms", async () => {
    render(SchedulePickerTestHost, { initial: initial({ kind: "once", timezone: "Asia/Shanghai", expression: "2040-01-02T03:04:05.123Z" }) });
    const date = screen.getByLabelText(t("scheduleEditor.dateTime"));
    expect(date).toHaveValue("2040-01-02T11:04:05.123");
    const zone = screen.getByLabelText(t("schedules.timezone"));
    await fireEvent.input(zone, { target: { value: "America/New_York" } });
    await fireEvent.change(zone);
    expect(state().expression).toBe("2040-01-02T16:04:05.123Z");
    expect(date).toHaveValue("2040-01-02T11:04:05.123");
    await fireEvent.input(date, { target: { value: "2040-02-01T12:34:56.789" } });
    expect(state().expression).toBe("2040-02-01T17:34:56.789Z");
    await fireEvent.input(zone, { target: { value: "Invalid/Zone" } });
    await fireEvent.change(zone);
    expect(date).toHaveValue("2040-02-01T12:34:56.789");
    await fireEvent.input(zone, { target: { value: "Asia/Shanghai" } });
    await fireEvent.change(zone);
    expect(state().expression).toBe("2040-02-01T04:34:56.789Z");
  });
  it("keeps DST gaps invalid instead of normalizing them to another time", async () => {
    render(SchedulePickerTestHost, { initial: initial({ kind: "once", timezone: "America/New_York", expression: "2026-03-07T12:00:00Z" }) });
    const date = screen.getByLabelText(t("scheduleEditor.dateTime"));
    await fireEvent.input(date, { target: { value: "2026-03-08T02:30" } });
    expect(state().expression).toBe("");
    expect(date).toHaveAttribute("aria-invalid", "true");
    expect(date).toHaveValue("2026-03-08T02:30");
  });
  it("supports disabled fields and caps the editor at 64 time rows", () => {
    render(SchedulePickerTestHost, { initial: initial({ times: Array(64).fill("09:00") }), disabled: true });
    expect(screen.getByRole("button", { name: t("scheduleEditor.addTime") })).toBeDisabled();
    expect(screen.getByLabelText(t("scheduleEditor.time", { index: 1 }))).toBeDisabled();
    expect(screen.getByLabelText(t("schedules.scheduleKind"))).toBeDisabled();
  });
});

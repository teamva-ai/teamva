import { describe, expect, it } from "vitest";
import { defaultScheduleConfig, fromZonedLocal, normalizeSchedule, scheduleSummary, scheduleInstantLabel, scheduleValid, toZonedLocal, validTimezone, type ScheduleConfig } from "./scheduleConfig";

const config = (overrides: Partial<ScheduleConfig> = {}): ScheduleConfig => ({ ...defaultScheduleConfig(), timezone: "UTC", ...overrides });

describe("schedule configuration", () => {
  it("labels next runs in the task timezone rather than the viewing browser timezone", () => {
    const instant = "2040-10-26T09:15:00Z";
    expect(scheduleInstantLabel(instant, "UTC", "en-GB")).toContain("09:15");
    expect(scheduleInstantLabel(instant, "Asia/Shanghai", "en-GB")).toContain("17:15");
    expect(scheduleInstantLabel(undefined, "UTC")).toBe("-");
    expect(scheduleInstantLabel("bad-date", "UTC")).toBe("-");
    expect(scheduleInstantLabel(instant, "Nowhere/Nope")).toBe("-");
  });
  it("defaults to daily 09:00 in the system IANA timezone", () => {
    expect(defaultScheduleConfig()).toMatchObject({ kind: "daily", times: ["09:00"], timezone: Intl.DateTimeFormat().resolvedOptions().timeZone });
  });
  it("sorts and deduplicates paired times without introducing extra runs", () => {
    const original = config({ times: ["14:45", "09:15", "14:45"], weekdays: [5, 1, 5] });
    expect(normalizeSchedule(original)).toMatchObject({ times: ["09:15", "14:45"], weekdays: [1, 5] });
    expect(original.times).toHaveLength(3);
    expect(scheduleValid(original)).toBe(true);
    expect(scheduleSummary(original)).toBe("09:15, 14:45 · UTC");
  });
  it("validates daily/weekly limits and leaves cron validation to the server", () => {
    for (const times of [[], ["9:00"], ["24:00"], ["09:60"], ["09:00:01"], Array(65).fill("09:00")]) expect(scheduleValid(config({ times }))).toBe(false);
    expect(scheduleValid(config({ times: Array(64).fill("09:00") }))).toBe(true);
    for (const weekdays of [[], [-1], [7], [1.5]]) expect(scheduleValid(config({ kind: "weekly", weekdays }))).toBe(false);
    expect(scheduleValid(config({ kind: "weekly", weekdays: [0, 1, 6] }))).toBe(true);
    expect(scheduleValid(config({ kind: "cron", expression: "" }))).toBe(false);
    expect(scheduleValid(config({ kind: "cron", expression: "not a cron; server decides" }))).toBe(true);
    expect(scheduleValid(config({ kind: "other" as ScheduleConfig["kind"] }))).toBe(false);
    expect(scheduleValid(config({ timezone: "Nowhere/Nope" }))).toBe(false);
    expect(validTimezone("+08:00")).toBe(false);
  });
  it("summarizes weekly days with explicit locale, without ambient locale state", () => {
    const value = config({ kind: "weekly", times: ["14:45", "09:15"], weekdays: [1, 5] });
    expect(scheduleSummary(value, "en-US")).toBe("Mon, Fri · 09:15, 14:45 · UTC");
    expect(scheduleSummary(value, "zh-CN")).toContain("周一");
  });
  it("requires real RFC3339 calendar instants for once", () => {
    for (const expression of ["", "2040-01-02T09:30", "2040-02-30T09:30:00Z", "2040-01-02T24:00:00Z", "2040-01-02T09:30:00+24:00"]) expect(scheduleValid(config({ kind: "once", expression }))).toBe(false);
    expect(scheduleValid(config({ kind: "once", expression: "2040-01-02T09:30:05.123+08:00" }))).toBe(true);
  });
});

describe("selected-timezone wall clocks", () => {
  it("converts UTC+8 independently of the browser zone and keeps precision", () => {
    const iso = "2040-01-02T03:04:05.123Z";
    expect(toZonedLocal(iso, "Asia/Shanghai")).toBe("2040-01-02T11:04:05.123");
    expect(fromZonedLocal("2040-01-02T11:04:05.123", "Asia/Shanghai")).toBe(iso);
    expect(fromZonedLocal("2040-01-02T11:04", "Asia/Shanghai")).toBe("2040-01-02T03:04:00.000Z");
    expect(toZonedLocal("2040-01-02T11:04:05.123+08:00", "America/New_York")).toBe("2040-01-01T22:04:05.123");
  });
  it("rejects nonexistent DST times and deterministically takes the earlier overlap", () => {
    expect(fromZonedLocal("2026-03-08T02:30", "America/New_York")).toBe("");
    expect(fromZonedLocal("2026-11-01T01:30:05.123", "America/New_York")).toBe("2026-11-01T05:30:05.123Z");
    expect(fromZonedLocal("2026-10-04T02:15", "Australia/Lord_Howe")).toBe("");
    expect(fromZonedLocal("2026-04-05T01:45", "Australia/Lord_Howe")).toBe("2026-04-04T14:45:00.000Z");
    expect(fromZonedLocal("2011-12-30T12:00", "Pacific/Apia")).toBe("");
  });
  it("rejects invalid dates, zones and input rather than rolling the calendar", () => {
    for (const wall of ["", "garbage", "2026-02-29T10:00", "2026-04-31T10:00", "2026-00-01T10:00", "2026-01-01T24:00", "2026-01-01T10:60", "2026-01-01T10:00:60"]) expect(fromZonedLocal(wall, "UTC")).toBe("");
    expect(fromZonedLocal("2026-01-01T10:00", "bad zone")).toBe("");
    expect(toZonedLocal("2026-02-30T10:00:00Z", "UTC")).toBe("");
    expect(toZonedLocal("2026-01-01T10:00:00Z", "bad zone")).toBe("");
  });
});

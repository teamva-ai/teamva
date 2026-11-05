import { describe, expect, it } from "vitest";
import {
  APPEARANCE_PRESETS, DEFAULT_APPEARANCE, appearanceTokens, appearanceTokenStyle,
  contrastRatio, normalizeAppearance, palettePreviewStyle,
  type AppearancePalette, type AppearancePreferences
} from "./appearanceThemes";
import { BASE_THEME_PRESETS, THEME_TOKEN_CSS_PROPERTIES, type ThemeTokens } from "./themeTokens";

const modes = ["light", "dark"] as const;
const surfaces = ["canvas", "surface", "surfaceSubtle", "surfaceHover", "surface2"] as const;
const states = ["success", "warning", "danger", "running", "queued"] as const;

function expectReadable(tokens: ThemeTokens): void {
  const backgrounds = [...surfaces.map((key) => tokens[key]), tokens.accentSubtle, tokens.controlSubtle,
    ...states.map((key) => tokens[`${key}Subtle`])];
  for (const key of ["text", "textSecondary", "textMuted", "secondary", "muted", "code", "accentText", ...states] as const) {
    for (const background of backgrounds) {
      expect(contrastRatio(tokens[key], background), `${key} ${tokens[key]} on ${background}`).toBeGreaterThanOrEqual(4.5);
    }
  }
  expect(contrastRatio(tokens.ink, tokens.canvas)).toBeGreaterThanOrEqual(7);
  expect(contrastRatio(tokens.inkHover, tokens.canvas)).toBeGreaterThanOrEqual(7);
  expect(contrastRatio(tokens.controlText, tokens.control)).toBeGreaterThanOrEqual(4.5);
  expect(contrastRatio(tokens.controlText, tokens.controlHover)).toBeGreaterThanOrEqual(4.5);
  for (const bg of [...surfaces.map((key) => tokens[key]), tokens.controlSubtle]) {
    expect(contrastRatio(tokens.controlFocus, bg)).toBeGreaterThanOrEqual(4.5);
  }
  expect(tokens.secondary).toBe(tokens.textSecondary);
  expect(tokens.muted).toBe(tokens.textMuted);
}

describe("appearance themes", () => {
  it("normalizes missing, unknown and hostile values without mutating input/defaults", () => {
    expect(DEFAULT_APPEARANCE).toEqual({ palette: "default" });
    for (const input of [undefined, null, {}, { palette: "unknown" as AppearancePalette }, { palette: "project" as AppearancePalette }]) {
      expect(normalizeAppearance(input)).toEqual(DEFAULT_APPEARANCE);
    }
    expect(normalizeAppearance({ palette: "ocean", accent: " #aBc ", background: "#ABCDEF", control: "#Def" }))
      .toEqual({ palette: "ocean", accent: "#aabbcc", background: "#abcdef", control: "#ddeeff" });
    for (const color of ["red", "transparent", "#abcd", "#11223344", "#ggg", "url(x);color:red", "var(--secret)", "", null, 42]) {
      expect(normalizeAppearance({ accent: color, background: color, control: color } as Partial<AppearancePreferences>))
        .toEqual(DEFAULT_APPEARANCE);
    }
    expect(normalizeAppearance()).not.toBe(DEFAULT_APPEARANCE);
  });

  it("calculates known WCAG contrast ratios, including short hex", () => {
    expect(contrastRatio("#000", "#fff")).toBe(21);
    expect(contrastRatio("#123456", "#123456")).toBe(1);
    expect(contrastRatio("#767676", "#ffffff")).toBeCloseTo(4.542, 2);
    expect(contrastRatio("#123", "#abc")).toBe(contrastRatio("#abc", "#123"));
    expect(() => contrastRatio("red", "#fff")).toThrow(TypeError);
  });

  it("ships seven complete palette pairs with independent controls and consistent translation keys", () => {
    expect(APPEARANCE_PRESETS.map((preset) => preset.id))
      .toEqual(["default", "hallmark", "chamber", "flexoki", "ocean", "forest", "iris"]);
    const keys = Object.keys(THEME_TOKEN_CSS_PROPERTIES).sort();
    for (const preset of APPEARANCE_PRESETS) {
      expect(preset.labelKey).toBe(`appearance.${preset.id}`);
      expect(preset.descriptionKey).toBe(`appearance.${preset.id}Desc`);
      for (const mode of modes) {
        expect(Object.keys(preset[mode]).sort()).toEqual(keys);
        for (const value of Object.values(preset[mode])) expect(value.length).toBeGreaterThan(0);
        expectReadable(preset[mode]);
        expectReadable(appearanceTokens({ palette: preset.id }, mode));
      }
    }
    const distinct = APPEARANCE_PRESETS.filter((preset) => ["hallmark", "ocean", "forest", "iris"].includes(preset.id));
    for (const mode of modes) {
      for (const key of ["canvas", "surface", "surfaceSubtle", "surfaceHover", "border", "text", "accent"] as const) {
        expect(new Set(distinct.map((preset) => preset[mode][key])).size, `${mode} ${key}`).toBe(4);
      }
    }
  });

  it("softens the light palette's green while retaining the dark Silken accent", () => {
    const light = appearanceTokens({ palette: "default" }, "light");
    expect(light.canvas).toBe("#fbfbfb"); expect(light.accent).toBe("#52763a"); expect(light.control).toBe("#52763a");
    expect(light.accentText).toBe("#426523");
    expect(light.controlText).toBe("#ffffff");
    const dark = appearanceTokens({ palette: "default" }, "dark");
    expect(dark.canvas).toBe("#111114"); expect(dark.accent).toBe("#c2ff75"); expect(dark.control).toBe("#c2ff75");
    expect(dark.controlText).toBe("#000000");
    for (const tokens of [light, dark]) {
      expectReadable(tokens);
      expect(contrastRatio(tokens.controlBorder, tokens.controlSubtle)).toBeGreaterThanOrEqual(3);
    }
    expect(appearanceTokens({ palette: "unknown" as AppearancePalette }, "dark")).toEqual(dark);
    expect(appearanceTokens({ palette: "project" as AppearancePalette }, "light")).toEqual(light);
  });

  it("changes control colors independently from content accent and background for every preset", () => {
    for (const preset of APPEARANCE_PRESETS) for (const mode of modes) {
      const original = appearanceTokens({ palette: preset.id }, mode);
      for (const control of ["#000000", "#ffffff", "#777777", "#ff0000", "#00ff00", "#0000ff"]) {
        const tokens = appearanceTokens({ palette: preset.id, control }, mode);
        expect(tokens.control).toBe(control);
        expect(tokens.canvas).toBe(original.canvas);
        expect(tokens.accent).toBe(original.accent);
        expect(tokens.controlSubtle).not.toBeUndefined();
        expectReadable(tokens);
      }
      const accented = appearanceTokens({ palette: preset.id, accent: "#789abc" }, mode);
      expect(accented.control).toBe(original.control);
    }
  });

  it("keeps every extreme custom background/accent readable in either resolved mode", () => {
    for (const mode of modes) {
      for (const background of ["#000000", "#ffffff", "#777777", "#999999", "#ff0000", "#00ff00", "#0000ff"]) {
        for (const accent of ["#000000", "#ffffff", "#777777", "#ff00ff", "#00ff00", "#0000ff"]) {
          const tokens = appearanceTokens({ palette: "iris", background, accent, control: accent }, mode);
          expectReadable(tokens);
          expect(tokens.accent).toBe(accent);
          expect(tokens.surface).not.toBe(tokens.canvas);
          expect(tokens.surface2).toBe(tokens.surfaceSubtle);
          expect(tokens.borderStrong).not.toBe(tokens.border);
          // Semantic states remain their own colors, never replaced by the custom accent.
          expect(tokens.danger).not.toBe(tokens.success);
          expect(tokens.dangerSubtle).not.toBe(tokens.successSubtle);
          expect(new Set([tokens.reportBlue, tokens.reportGreen, tokens.reportPink, tokens.reportLime, tokens.reportCyan]).size).toBe(5);
        }
      }
    }
  });

  it("handles accent-only and background-only customizations without losing semantic aliases", () => {
    for (const preset of APPEARANCE_PRESETS) {
      for (const mode of modes) {
        for (const overrides of [{ accent: "#fff" }, { accent: "#000" }, { background: "#888" }]) {
          const tokens = appearanceTokens({ palette: preset.id, ...overrides }, mode);
          for (const foreground of ["text", "textSecondary", "textMuted", "accentText", "code"] as const) {
            for (const bg of [...surfaces.map((key) => tokens[key]), tokens.accentSubtle]) {
              expect(contrastRatio(tokens[foreground], bg)).toBeGreaterThanOrEqual(4.5);
            }
          }
          expect(contrastRatio(tokens.ink, tokens.canvas)).toBeGreaterThanOrEqual(7);
        }
      }
    }
  });

  it("never mutates base or appearance constants and always returns a fresh copy", () => {
    const basesBefore = JSON.stringify(BASE_THEME_PRESETS);
    const appearancesBefore = JSON.stringify(APPEARANCE_PRESETS);
    for (const mode of modes) {
      for (const palette of APPEARANCE_PRESETS.map((preset) => preset.id)) {
        appearanceTokens({ palette, background: "#444", accent: "#fff", control: "#234567" }, mode);
        const tokens = appearanceTokens({ palette }, mode);
        tokens.canvas = "#abcdef";
        expect(appearanceTokens({ palette }, mode).canvas).not.toBe("#abcdef");
      }
    }
    expect(JSON.stringify(BASE_THEME_PRESETS)).toBe(basesBefore);
    expect(JSON.stringify(APPEARANCE_PRESETS)).toBe(appearancesBefore);
  });

  it("offers bounded previews and uses the authoritative CSS map for the full theme", () => {
    expect(palettePreviewStyle("ocean")).toContain("--preview-canvas:#eef4f8");
    expect(palettePreviewStyle("default", "dark")).toContain("--preview-canvas:#111114");
    expect(palettePreviewStyle("default")).toContain("--preview-control:#52763a");
    const tokens = appearanceTokens({ palette: "forest" }, "dark");
    const style = appearanceTokenStyle(tokens);
    for (const [key, property] of Object.entries(THEME_TOKEN_CSS_PROPERTIES)) {
      expect(style).toContain(`${property}:${tokens[key as keyof ThemeTokens]}`);
    }
    expect(style).not.toContain("undefined");
    expect(palettePreviewStyle("hallmark")).not.toContain("url(");
  });
});

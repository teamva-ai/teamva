import { describe, expect, it } from "vitest";
import { readFile } from "node:fs/promises";
import { resolve } from "node:path";
import {
  THEME_TOKEN_CSS_PROPERTIES
} from "./themeTokens";
import { APPEARANCE_PRESETS, appearanceTokens } from "./appearanceThemes";

function luminance(hex: string): number {
  const channels = hex.slice(1).match(/../g)!.map((channel) => parseInt(channel, 16) / 255)
    .map((channel) => channel <= 0.04045 ? channel / 12.92 : ((channel + 0.055) / 1.055) ** 2.4);
  return channels.reduce((total, channel, index) => total + channel * [0.2126, 0.7152, 0.0722][index], 0);
}

function contrast(foreground: string, background: string): number {
  const values = [luminance(foreground), luminance(background)].sort((a, b) => b - a);
  return (values[0] + 0.05) / (values[1] + 0.05);
}

describe("global theme tokens", () => {
  it("ships complete and distinct light/dark semantic token sets", () => {
    const tokenKeys = Object.keys(THEME_TOKEN_CSS_PROPERTIES).sort();
    expect(APPEARANCE_PRESETS.map((preset) => preset.id)).toEqual([
      "default", "hallmark", "chamber", "flexoki", "ocean", "forest", "iris"
    ]);
    for (const preset of APPEARANCE_PRESETS) {
      expect(Object.keys(preset.light).sort(), `${preset.id} light`).toEqual(tokenKeys);
      expect(Object.keys(preset.dark).sort(), `${preset.id} dark`).toEqual(tokenKeys);
      expect(preset.light.canvas).not.toBe(preset.dark.canvas);
      expect(preset.light.accent).toMatch(/^#/);
      expect(preset.dark.accent).toMatch(/^#/);
    }
  });

  it.each(APPEARANCE_PRESETS)("keeps $id small text readable on every application surface", (preset) => {
    for (const mode of ["light", "dark"] as const) {
      const tokens = preset[mode];
      for (const foreground of ["textMuted", "muted", "accentText", "warning", "queued"] as const) {
        for (const background of ["canvas", "surface", "surfaceSubtle", "surfaceHover", "surface2",
          "accentSubtle", "warningSubtle", "successSubtle", "dangerSubtle"] as const) {
          expect(contrast(tokens[foreground], tokens[background]),
            `${preset.id} ${mode}: ${foreground} on ${background}`).toBeGreaterThanOrEqual(4.5);
        }
      }
    }
  });

  it("keeps startup CSS defaults in sync with the runtime default palette", async () => {
    const css = await readFile(resolve(process.cwd(), "src/app.css"), "utf8");
    const light = css.match(/@theme\s*\{([\s\S]*?)\n\}/)![1];
    const dark = css.match(/:root\[data-theme="dark"\]\s*\{([\s\S]*?)\n\}/)![1];
    for (const [mode, block] of [["light", light], ["dark", dark]] as const) {
      for (const [token, property] of Object.entries(THEME_TOKEN_CSS_PROPERTIES)) {
        expect(block.match(new RegExp(`${property}:\\s*([^;]+);`))?.[1], `${mode} ${property}`)
          .toBe(appearanceTokens({ palette: "default" }, mode)[token as keyof typeof THEME_TOKEN_CSS_PROPERTIES]);
      }
    }
  });
});

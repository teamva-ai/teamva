import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { applyTheme, createThemePreview, resolveTheme, subscribeTheme } from "./theme";
import { APPEARANCE_PRESETS, appearanceTokens } from "./appearanceThemes";
import { THEME_TOKEN_CSS_PROPERTIES } from "./themeTokens";
import { preferences } from "./stores.svelte";

beforeEach(() => {
  preferences.theme = "system";
  preferences.appearance = { palette: "default" };
  applyTheme("light", { palette: "default" });
});

afterEach(() => {
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  document.documentElement.removeAttribute("data-theme");
  document.documentElement.removeAttribute("data-project-theme");
  document.documentElement.removeAttribute("data-palette");
  document.documentElement.removeAttribute("style");
  preferences.theme = "system";
  preferences.appearance = { palette: "default" };
});

describe("theme application", () => {
  it("applies only the user's global palette, without project state", () => {
    applyTheme("dark", { palette: "chamber" });
    const root = document.documentElement;
    expect(root.dataset.theme).toBe("dark");
    expect(root.dataset.projectTheme).toBeUndefined();
    expect(root.style.getPropertyValue("--color-canvas")).toBe("#120f0e");
    expect(root.style.getPropertyValue("--color-accent")).toBe("#da7c47");
    expect(root.style.getPropertyValue("--color-accent-text")).toBe("#da7c47");

    applyTheme("light", { palette: "flexoki" });
    expect(root.dataset.theme).toBe("light");
    expect(root.dataset.projectTheme).toBeUndefined();
    expect(root.style.getPropertyValue("--color-canvas")).toBe("#fffdf4");
    expect(root.style.getPropertyValue("--color-danger")).toBe("#af3029");
    expect(root.style.getPropertyValue("--color-accent-text")).toBe("#a8440f");
  });

  it("uses the operating-system mode with the default global palette", () => {
    vi.stubGlobal("matchMedia", vi.fn(() => ({ matches: true })));
    expect(resolveTheme("system")).toBe("dark");
    applyTheme("system");
    expect(document.documentElement.dataset.theme).toBe("dark");
    expect(document.documentElement.style.getPropertyValue("--color-canvas")).toBe("#111114");
  });
});

describe("global appearance and preview", () => {
  it.each(APPEARANCE_PRESETS.map((preset) => preset.id))("applies every %s token in both modes", (palette) => {
    for (const mode of ["light", "dark"] as const) {
      applyTheme(mode, { palette });
      const root = document.documentElement;
      const tokens = appearanceTokens({ palette }, mode);
      expect(root.dataset.palette).toBe(palette);
      expect(root.dataset.projectTheme).toBeUndefined();
      expect(root.dataset.theme).toBe(mode);
      expect(root.style.colorScheme).toBe(mode);
      for (const [key, cssProperty] of Object.entries(THEME_TOKEN_CSS_PROPERTIES)) {
        expect(root.style.getPropertyValue(cssProperty), `${palette}/${mode}/${key}`).toBe(tokens[key as keyof typeof tokens]);
      }
    }
  });

  it("updates the control tokens without changing content accent, background or persisted preferences", () => {
    applyTheme("light", { palette: "default", control: "#254f96" });
    const root = document.documentElement;
    expect(root.style.getPropertyValue("--color-control")).toBe("#254f96");
    expect(root.style.getPropertyValue("--color-control-text")).toBe("#ffffff");
    expect(root.style.getPropertyValue("--color-accent")).toBe("#52763a");
    expect(root.style.getPropertyValue("--color-canvas")).toBe("#fbfbfb");
    expect(preferences.appearance).toEqual({ palette: "default" });
  });

  it("uses saved appearance by default and clears custom colors when returning to a preset", () => {
    preferences.appearance = { palette: "ocean", accent: "#123456", background: "#eef4f8" };
    applyTheme("light");
    const root = document.documentElement;
    expect(root.dataset.palette).toBe("ocean");
    expect(root.style.getPropertyValue("--color-accent")).toBe("#123456");
    expect(root.style.getPropertyValue("--color-canvas")).toBe("#eef4f8");
    applyTheme("dark", { palette: "iris" });
    const expected = appearanceTokens({ palette: "iris" }, "dark");
    expect(root.dataset.palette).toBe("iris");
    expect(root.style.getPropertyValue("--color-accent")).toBe(expected.accent);
    expect(root.style.getPropertyValue("--color-canvas")).toBe(expected.canvas);
    // Applying a theme is presentation-only; persistence remains the caller's job.
    expect(preferences.appearance).toEqual({ palette: "ocean", accent: "#123456", background: "#eef4f8" });
  });

  it("rolls back the entire unsaved appearance and mode when its preview is disposed", () => {
    applyTheme("light", { palette: "forest", accent: "#246842", control: "#174327" });
    const root = document.documentElement;
    const savedStyle = root.getAttribute("style");
    const preview = createThemePreview();
    try {
      preview.update("dark", { palette: "iris", accent: "#ab98de", background: "#181020", control: "#efbcde" });
      expect(root.dataset.theme).toBe("dark");
      expect(root.dataset.palette).toBe("iris");
      expect(root.style.getPropertyValue("--color-accent")).toBe("#ab98de");
      expect(root.getAttribute("style")).not.toBe(savedStyle);
      expect(preferences.appearance).toEqual({ palette: "default" });
      preview.dispose();
      expect(root.dataset.theme).toBe("light");
      expect(root.dataset.palette).toBe("forest");
      expect(root.style.getPropertyValue("--color-control")).toBe("#174327");
      expect(root.getAttribute("style")).toBe(savedStyle);
    } finally {
      preview.dispose();
    }
  });

  it("restores the latest saved appearance rather than a stale preview snapshot", () => {
    applyTheme("light", { palette: "default" });
    const preview = createThemePreview();
    try {
      preview.update("dark", { palette: "ocean" });
      applyTheme("light", { palette: "forest" });
      expect(document.documentElement.dataset.palette).toBe("ocean");
      expect(document.documentElement.dataset.theme).toBe("dark");
      preview.dispose();
      expect(document.documentElement.dataset.palette).toBe("forest");
      expect(document.documentElement.dataset.projectTheme).toBeUndefined();
      expect(document.documentElement.dataset.theme).toBe("light");
      expect(document.documentElement.style.getPropertyValue("--color-canvas")).toBe(appearanceTokens({ palette: "forest" }, "light").canvas);
    } finally {
      preview.dispose();
    }
  });

  it("does not let an older editor's cleanup remove a newer preview", () => {
    applyTheme("light", { palette: "default" });
    const older = createThemePreview();
    const newer = createThemePreview();
    try {
      older.update("light", { palette: "ocean" });
      newer.update("dark", { palette: "iris" });
      older.dispose();
      expect(document.documentElement.dataset.palette).toBe("iris");
      expect(document.documentElement.dataset.theme).toBe("dark");
      newer.dispose();
      expect(document.documentElement.dataset.palette).toBe("default");
      expect(document.documentElement.dataset.theme).toBe("light");
    } finally {
      older.dispose();
      newer.dispose();
    }
  });

  it("tracks system-mode changes for a global preview and detaches the media listener", () => {
    let dark = false;
    const media = new EventTarget();
    Object.defineProperty(media, "matches", { get: () => dark });
    const add = vi.spyOn(media, "addEventListener");
    const remove = vi.spyOn(media, "removeEventListener");
    vi.stubGlobal("matchMedia", vi.fn(() => media));
    preferences.theme = "system";
    preferences.appearance = { palette: "forest" };
    const unsubscribe = subscribeTheme();
    const preview = createThemePreview();
    try {
      expect(add).toHaveBeenCalledWith("change", expect.any(Function));
      preview.update("system", { palette: "ocean" });
      expect(document.documentElement.dataset.theme).toBe("light");
      const lightCanvas = document.documentElement.style.getPropertyValue("--color-canvas");
      dark = true;
      media.dispatchEvent(new Event("change"));
      expect(document.documentElement.dataset.theme).toBe("dark");
      expect(document.documentElement.dataset.palette).toBe("ocean");
      expect(document.documentElement.style.getPropertyValue("--color-canvas")).not.toBe(lightCanvas);
      preview.update("light", { palette: "iris" });
      media.dispatchEvent(new Event("change"));
      expect(document.documentElement.dataset.theme).toBe("light");
      expect(document.documentElement.dataset.palette).toBe("iris");
      preview.dispose();
      expect(document.documentElement.dataset.theme).toBe("dark");
      expect(document.documentElement.dataset.palette).toBe("forest");
      unsubscribe?.();
      expect(remove).toHaveBeenCalledWith("change", add.mock.calls[0][1]);
      dark = false;
      media.dispatchEvent(new Event("change"));
      expect(document.documentElement.dataset.theme).toBe("dark");
    } finally {
      preview.dispose();
      unsubscribe?.();
    }
  });
});

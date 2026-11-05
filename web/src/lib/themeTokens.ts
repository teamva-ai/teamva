export type ResolvedTheme = "light" | "dark";

export interface ThemeTokens {
  canvas: string;
  surface: string;
  surfaceSubtle: string;
  surfaceHover: string;
  surface2: string;
  inverseCanvas: string;
  border: string;
  borderStrong: string;
  text: string;
  textSecondary: string;
  textMuted: string;
  ink: string;
  inkHover: string;
  accent: string;
  accentText: string;
  accentHover: string;
  accentSubtle: string;
  control: string;
  controlHover: string;
  controlText: string;
  controlSubtle: string;
  controlBorder: string;
  controlFocus: string;
  reportBlue: string;
  reportGreen: string;
  reportPink: string;
  reportLime: string;
  reportCyan: string;
  success: string;
  successSubtle: string;
  warning: string;
  warningSubtle: string;
  danger: string;
  dangerSubtle: string;
  running: string;
  runningSubtle: string;
  queued: string;
  queuedSubtle: string;
  code: string;
  secondary: string;
  muted: string;
  shadow: string;
  shadowStrong: string;
  backdrop: string;
}

type BaseTokens = Omit<ThemeTokens, "controlHover" | "controlText" | "controlSubtle" | "controlBorder" | "controlFocus">;
interface BaseThemePreset { id: "hallmark" | "chamber" | "flexoki"; light: BaseTokens; dark: BaseTokens; }

// Hallmark is the product's original warm cream/charcoal palette. Chamber and
// Flexoki keep complete token sets so production colors are never derived at runtime.
// Chamber/Flexoki theme data is adapted from OpenChamber (MIT); see THIRD_PARTY_NOTICES.md.
export const BASE_THEME_PRESETS: readonly BaseThemePreset[] = [
  {
    id: "hallmark",
    light: {
      canvas: "#f5f1ec", surface: "#fffdfa", surfaceSubtle: "#ebe7e1", surfaceHover: "#e6e0d6",
      surface2: "#ebe7e1", inverseCanvas: "#141210", border: "#d3cec6", borderStrong: "#b6b0a6",
      text: "#111111", textSecondary: "#57534d", textMuted: "#635f58", ink: "#111111", inkHover: "#2b2b2b",
      accent: "#ff5600", accentText: "#b23a00", accentHover: "#e04e00", accentSubtle: "#ffede5",
      control: "#111111",
      reportBlue: "#65b5ff", reportGreen: "#0bdf50", reportPink: "#ff2067", reportLime: "#b3e01c", reportCyan: "#03b2cb",
      success: "#17754a", successSubtle: "#e6f0e9", warning: "#7f5706", warningSubtle: "#f6efdd",
      danger: "#b3433c", dangerSubtle: "#f8eae8", running: "#17754a", runningSubtle: "#e6f0e9",
      queued: "#7f5706", queuedSubtle: "#f6efdd", code: "#2a2724", secondary: "#57534d", muted: "#635f58",
      shadow: "rgb(70 58 40 / 8%)", shadowStrong: "rgb(60 50 30 / 14%)", backdrop: "rgb(17 16 14 / 42%)"
    },
    dark: {
      canvas: "#171512", surface: "#1f1c18", surfaceSubtle: "#26221d", surfaceHover: "#2d2923",
      surface2: "#26221d", inverseCanvas: "#0f0d0b", border: "#3a352e", borderStrong: "#4a443b",
      text: "#f0ece6", textSecondary: "#b8b2a8", textMuted: "#9e978c", ink: "#f0ece6", inkHover: "#ffffff",
      accent: "#ff7847", accentText: "#ff7847", accentHover: "#ff9a6b", accentSubtle: "#33241c",
      control: "#f0ece6",
      reportBlue: "#65b5ff", reportGreen: "#57bd85", reportPink: "#ff6b9a", reportLime: "#b3e01c", reportCyan: "#32c2d6",
      success: "#57bd85", successSubtle: "#143225", warning: "#d9a94e", warningSubtle: "#322915",
      danger: "#e56a60", dangerSubtle: "#351c1a", running: "#57bd85", runningSubtle: "#143225",
      queued: "#d9a94e", queuedSubtle: "#322915", code: "#e8e2da", secondary: "#b8b2a8", muted: "#9e978c",
      shadow: "rgb(0 0 0 / 32%)", shadowStrong: "rgb(0 0 0 / 42%)", backdrop: "rgb(0 0 0 / 62%)"
    }
  },
  {
    id: "chamber",
    light: {
      canvas: "#fdfcfa", surface: "#f8f7f5", surfaceSubtle: "#f7f6f4", surfaceHover: "#f0efec",
      surface2: "#f7f6f4", inverseCanvas: "#393a34", border: "#e5e1de", borderStrong: "#cbc7c2",
      text: "#393a34", textSecondary: "#5c5c54", textMuted: "#6b6b63", ink: "#393a34", inkHover: "#24251f",
      accent: "#b35017", accentText: "#ac4b14", accentHover: "#9a4310", accentSubtle: "#f7e9df",
      control: "#393a34",
      reportBlue: "#2d72c4", reportGreen: "#5f8d3d", reportPink: "#b94d80", reportLime: "#9a8402", reportCyan: "#2e808f",
      success: "#5f8d3d", successSubtle: "#edf3e8", warning: "#805f0c", warningSubtle: "#f4f0e3",
      danger: "#b7493f", dangerSubtle: "#f7e9e7", running: "#5f8d3d", runningSubtle: "#edf3e8",
      queued: "#805f0c", queuedSubtle: "#f4f0e3", code: "#393a34", secondary: "#5c5c54", muted: "#6b6b63",
      shadow: "rgb(57 58 52 / 8%)", shadowStrong: "rgb(57 58 52 / 14%)", backdrop: "rgb(57 57 52 / 42%)"
    },
    dark: {
      canvas: "#120f0e", surface: "#181715", surfaceSubtle: "#171615", surfaceHover: "#24211f",
      surface2: "#171616", inverseCanvas: "#090706", border: "#242323", borderStrong: "#504e4c",
      text: "#c9c5ba", textSecondary: "#aaa69c", textMuted: "#948f85", ink: "#c9c5ba", inkHover: "#e1ddd2",
      accent: "#da7c47", accentText: "#da7c47", accentHover: "#eb8c57", accentSubtle: "#2d1d15",
      control: "#c9c5ba",
      reportBlue: "#479fe6", reportGreen: "#76ad4f", reportPink: "#d5839f", reportLime: "#bda94a", reportCyan: "#5d99a9",
      success: "#76ad4f", successSubtle: "#1f2a18", warning: "#c67f13", warningSubtle: "#2b2215",
      danger: "#da5b4a", dangerSubtle: "#2d1917", running: "#76ad4f", runningSubtle: "#1f2a18",
      queued: "#c67f13", queuedSubtle: "#2b2215", code: "#c9c5ba", secondary: "#aaa69c", muted: "#948f85",
      shadow: "rgb(0 0 0 / 34%)", shadowStrong: "rgb(0 0 0 / 48%)", backdrop: "rgb(0 0 0 / 64%)"
    }
  },
  {
    id: "flexoki",
    light: {
      canvas: "#fffdf4", surface: "#fbfaf2", surfaceSubtle: "#f6f5ee", surfaceHover: "#eeece3",
      surface2: "#f7f4ec", inverseCanvas: "#100f0f", border: "#e6e4db", borderStrong: "#cecdc3",
      text: "#100f0f", textSecondary: "#4f4e4b", textMuted: "#686762", ink: "#100f0f", inkHover: "#282726",
      accent: "#bc5215", accentText: "#a8440f", accentHover: "#da702c", accentSubtle: "#f8e8da",
      control: "#100f0f",
      reportBlue: "#205ea6", reportGreen: "#66800b", reportPink: "#a02f6f", reportLime: "#ad8301", reportCyan: "#24837b",
      success: "#66800b", successSubtle: "#eef1df", warning: "#a8440f", warningSubtle: "#f8e8da",
      danger: "#af3029", dangerSubtle: "#f6e5e2", running: "#66800b", runningSubtle: "#eef1df",
      queued: "#a8440f", queuedSubtle: "#f8e8da", code: "#100f0f", secondary: "#4f4e4b", muted: "#686762",
      shadow: "rgb(16 15 15 / 8%)", shadowStrong: "rgb(16 15 15 / 14%)", backdrop: "rgb(16 15 15 / 42%)"
    },
    dark: {
      canvas: "#171515", surface: "#1c1a19", surfaceSubtle: "#1c1b1a", surfaceHover: "#282624",
      surface2: "#1e1d1c", inverseCanvas: "#100f0f", border: "#2b2a28", borderStrong: "#403e3c",
      text: "#cecdc3", textSecondary: "#aaa8a0", textMuted: "#95938e", ink: "#cecdc3", inkHover: "#fffdf4",
      accent: "#da702c", accentText: "#e37a35", accentHover: "#f9ae77", accentSubtle: "#27180e",
      control: "#cecdc3",
      reportBlue: "#4385be", reportGreen: "#a0af54", reportPink: "#ce5d97", reportLime: "#d0a215", reportCyan: "#3aa99f",
      success: "#a0af54", successSubtle: "#252b16", warning: "#e37a35", warningSubtle: "#2d2117",
      danger: "#d14d41", dangerSubtle: "#2c1918", running: "#a0af54", runningSubtle: "#252b16",
      queued: "#e37a35", queuedSubtle: "#2d2117", code: "#cecdc3", secondary: "#aaa8a0", muted: "#95938e",
      shadow: "rgb(0 0 0 / 34%)", shadowStrong: "rgb(0 0 0 / 48%)", backdrop: "rgb(0 0 0 / 64%)"
    }
  }
] as const;

export const THEME_TOKEN_CSS_PROPERTIES: Readonly<Record<keyof ThemeTokens, string>> = {
  canvas: "--color-canvas", surface: "--color-surface", surfaceSubtle: "--color-surface-subtle",
  surfaceHover: "--color-surface-hover", surface2: "--color-surface-2", inverseCanvas: "--color-inverse-canvas",
  border: "--color-border", borderStrong: "--color-border-strong", text: "--color-text",
  textSecondary: "--color-text-secondary", textMuted: "--color-text-muted", ink: "--color-ink",
  inkHover: "--color-ink-hover", accent: "--color-accent", accentText: "--color-accent-text", accentHover: "--color-accent-hover",
  accentSubtle: "--color-accent-subtle", reportBlue: "--color-report-blue", reportGreen: "--color-report-green",
  control: "--color-control", controlHover: "--color-control-hover", controlText: "--color-control-text",
  controlSubtle: "--color-control-subtle", controlBorder: "--color-control-border", controlFocus: "--color-control-focus",
  reportPink: "--color-report-pink", reportLime: "--color-report-lime", reportCyan: "--color-report-cyan",
  success: "--color-success", successSubtle: "--color-success-subtle", warning: "--color-warning",
  warningSubtle: "--color-warning-subtle", danger: "--color-danger", dangerSubtle: "--color-danger-subtle",
  running: "--color-running", runningSubtle: "--color-running-subtle", queued: "--color-queued",
  queuedSubtle: "--color-queued-subtle", code: "--color-code", secondary: "--color-secondary",
  muted: "--color-muted", shadow: "--color-shadow", shadowStrong: "--color-shadow-strong",
  backdrop: "--color-backdrop"
};

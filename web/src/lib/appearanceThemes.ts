import {
  BASE_THEME_PRESETS, THEME_TOKEN_CSS_PROPERTIES,
  type ThemeTokens
} from "./themeTokens";

export type AppearancePalette = "default" | "hallmark" | "chamber" | "flexoki" | "ocean" | "forest" | "iris";

export interface AppearancePreferences {
  palette: AppearancePalette;
  accent?: string;
  background?: string;
  control?: string;
}

export interface AppearancePreset {
  id: AppearancePalette;
  labelKey: string;
  descriptionKey: string;
  light: ThemeTokens;
  dark: ThemeTokens;
}

export const DEFAULT_APPEARANCE: Readonly<AppearancePreferences> = { palette: "default" };

type Mode = "light" | "dark";

/** Only opaque CSS hex colors are persisted; CSS expressions/alpha cannot escape a preview. */
function normalizeHex(value: unknown): string | undefined {
  if (typeof value !== "string") return undefined;
  const hex = value.trim().toLowerCase();
  if (/^#[0-9a-f]{6}$/.test(hex)) return hex;
  if (/^#[0-9a-f]{3}$/.test(hex)) return `#${[...hex.slice(1)].map((c) => c + c).join("")}`;
  return undefined;
}

export function normalizeAppearance(input?: Partial<AppearancePreferences> | null): AppearancePreferences {
  const palette = input?.palette;
  const accent = normalizeHex(input?.accent);
  const background = normalizeHex(input?.background);
  const control = normalizeHex(input?.control);
  return {
    palette: APPEARANCE_PRESETS.some((preset) => preset.id === palette) ? palette! : "default",
    ...(accent ? { accent } : {}),
    ...(background ? { background } : {}),
    ...(control ? { control } : {})
  };
}

function channels(hex: string): number[] {
  const normalized = normalizeHex(hex);
  if (!normalized) throw new TypeError("Expected an opaque #RGB or #RRGGBB color");
  return [1, 3, 5].map((start) => parseInt(normalized.slice(start, start + 2), 16));
}

function luminance(hex: string): number {
  return channels(hex).reduce((sum, channel, index) => {
    const srgb = channel / 255;
    const linear = srgb <= 0.04045 ? srgb / 12.92 : ((srgb + 0.055) / 1.055) ** 2.4;
    return sum + linear * [0.2126, 0.7152, 0.0722][index];
  }, 0);
}

/** WCAG relative-luminance contrast; accepts opaque three/six-digit hex colors. */
export function contrastRatio(a: string, b: string): number {
  const first = luminance(a);
  const second = luminance(b);
  return (Math.max(first, second) + 0.05) / (Math.min(first, second) + 0.05);
}

function mix(from: string, to: string, amount: number): string {
  const end = channels(to);
  return `#${channels(from).map((value, index) =>
    Math.round(value + (end[index] - value) * amount).toString(16).padStart(2, "0")
  ).join("")}`;
}

/** Preserve hue whenever possible, moving only as far toward readable ink as necessary. */
function readable(color: string, backgrounds: readonly string[], minimum = 4.5): string {
  const passes = (candidate: string) => backgrounds.every((bg) => contrastRatio(candidate, bg) >= minimum);
  if (passes(color)) return color;
  const worstContrast = (candidate: string) => Math.min(...backgrounds.map((bg) => contrastRatio(candidate, bg)));
  const target = worstContrast("#000000") >= worstContrast("#ffffff") ? "#000000" : "#ffffff";
  for (let step = 1; step <= 100; step++) {
    const candidate = mix(color, target, step / 100);
    if (passes(candidate)) return candidate;
  }
  return target;
}

const SURFACES = ["canvas", "surface", "surfaceSubtle", "surfaceHover", "surface2"] as const;
const STATES = ["success", "warning", "danger", "running", "queued"] as const;
const REPORTS = ["reportBlue", "reportGreen", "reportPink", "reportLime", "reportCyan"] as const;

type PaletteTokens = Omit<ThemeTokens, "controlHover" | "controlText" | "controlSubtle" | "controlBorder" | "controlFocus">;

function accessibleCopy(source: PaletteTokens): ThemeTokens {
  const dark = luminance(source.canvas) < 0.179;
  const controlSubtle = mix(source.surface, source.control, dark ? 0.04 : 0.025);
  const controlText = contrastRatio(source.control, "#000000") >= contrastRatio(source.control, "#ffffff") ? "#000000" : "#ffffff";
  const tokens: ThemeTokens = { ...source,
    controlHover: readable(mix(source.control, dark ? "#000000" : "#ffffff", 0.12), [controlText]),
    controlText,
    controlSubtle,
    controlBorder: readable(mix(source.canvas, source.control, dark ? 0.45 : 0.32), [source.surface, controlSubtle], 3),
    controlFocus: readable(source.control, [...SURFACES.map((key) => source[key]), controlSubtle])
  };
  const backgrounds = [...SURFACES.map((key) => tokens[key]), tokens.accentSubtle, tokens.controlSubtle,
    ...STATES.map((key) => tokens[`${key}Subtle`])];
  tokens.text = readable(tokens.text, backgrounds);
  tokens.textSecondary = readable(tokens.textSecondary, backgrounds);
  tokens.textMuted = readable(tokens.textMuted, backgrounds);
  tokens.ink = readable(tokens.ink, [tokens.canvas], 7);
  tokens.inkHover = readable(tokens.inkHover, [tokens.canvas], 7);
  // Accent text belongs to navigation/content; controls have independent fill and focus tokens.
  tokens.accentText = readable(tokens.accentText, backgrounds);
  for (const state of STATES) tokens[state] = readable(tokens[state], backgrounds);
  for (const report of REPORTS) tokens[report] = readable(tokens[report], SURFACES.map((key) => tokens[key]));
  tokens.code = readable(tokens.code, backgrounds);
  tokens.secondary = tokens.textSecondary;
  tokens.muted = tokens.textMuted;
  return tokens;
}

// New palettes deliberately tint the entire hierarchy, not just the accent. The existing
// three remain the source of their own semantic/status colors and shadow treatment.
function palettePair(light: Partial<ThemeTokens>, dark: Partial<ThemeTokens>): Pick<AppearancePreset, "light" | "dark"> {
  const base = BASE_THEME_PRESETS[0];
  return {
    light: accessibleCopy({ ...base.light, ...light }),
    dark: accessibleCopy({ ...base.dark, ...dark })
  };
}

function preset(id: AppearancePreset["id"], pair: Pick<AppearancePreset, "light" | "dark">): AppearancePreset {
  return { id, labelKey: `appearance.${id}`, descriptionKey: `appearance.${id}Desc`, ...pair };
}

export const APPEARANCE_PRESETS: readonly AppearancePreset[] = [
  // Silken green belongs on actions; neutral surfaces and readable green ink keep it quiet.
  preset("default", palettePair({
    canvas: "#fbfbfb", surface: "#ffffff", surfaceSubtle: "#f4f4f5", surfaceHover: "#eeeef0",
    surface2: "#f4f4f5", inverseCanvas: "#0b0b0c", border: "#ececef", borderStrong: "#e4e4e7",
    text: "#09090b", textSecondary: "#64636e", textMuted: "#81818b", ink: "#0b0b0c", inkHover: "#18181b",
    accent: "#52763a", accentText: "#426523", accentHover: "#42652f", accentSubtle: "#f0f5eb", control: "#52763a",
    reportBlue: "#2171cc", reportGreen: "#1c882d", reportPink: "#df134d", reportLime: "#bc9c00", reportCyan: "#00849b",
    success: "#1c882d", successSubtle: "#e8f9e7", warning: "#7a5b00", warningSubtle: "#fff6d9",
    danger: "#c5000b", dangerSubtle: "#ffedec", running: "#1c882d", runningSubtle: "#e8f9e7",
    queued: "#7a5b00", queuedSubtle: "#fff6d9",
    code: "#09090b", shadow: "rgb(15 23 42 / 5%)", shadowStrong: "rgb(15 23 42 / 12%)", backdrop: "rgb(9 9 11 / 42%)"
  }, {
    canvas: "#111114", surface: "#18181b", surfaceSubtle: "#1e1e21", surfaceHover: "#27272a",
    surface2: "#27272a", inverseCanvas: "#050506", border: "#1f1f22", borderStrong: "#3b3b3d",
    text: "#fafafa", textSecondary: "#9f9fa9", textMuted: "#7f7f89", ink: "#fafafa", inkHover: "#ffffff",
    accent: "#c2ff75", accentText: "#c2ff75", accentHover: "#d1ff97", accentSubtle: "#23301c", control: "#c2ff75",
    reportBlue: "#59a6ff", reportGreen: "#4aa651", reportPink: "#fa5570", reportLime: "#d3b63b", reportCyan: "#00acc1",
    success: "#4aa651", successSubtle: "#1b351c", warning: "#cb9400", warningSubtle: "#3a2b07",
    danger: "#ff6467", dangerSubtle: "#4f1a1b", running: "#4aa651", runningSubtle: "#1b351c",
    queued: "#cb9400", queuedSubtle: "#3a2b07",
    code: "#e4e4e7", shadow: "rgb(0 0 0 / 20%)", shadowStrong: "rgb(0 0 0 / 34%)", backdrop: "rgb(0 0 0 / 62%)"
  })),
  ...BASE_THEME_PRESETS.map((source) => preset(source.id, {
    light: accessibleCopy(source.light), dark: accessibleCopy(source.dark)
  })),
  preset("ocean", palettePair({
    canvas: "#eef4f8", surface: "#f9fcff", surfaceSubtle: "#e3edf4", surfaceHover: "#d8e6ef",
    surface2: "#e3edf4", inverseCanvas: "#101d2a", border: "#c4d5e2", borderStrong: "#91afc5",
    text: "#172b3d", textSecondary: "#38556c", textMuted: "#4d6477", ink: "#172b3d", inkHover: "#0b1c2c",
    accent: "#2878aa", accentText: "#205e88", accentHover: "#1d638f", accentSubtle: "#dcecf7",
    control: "#234963",
    reportBlue: "#246cb1", reportGreen: "#287852", reportPink: "#a04479", reportLime: "#647526", reportCyan: "#197386",
    code: "#24475f", shadow: "rgb(31 65 91 / 8%)", shadowStrong: "rgb(31 65 91 / 15%)", backdrop: "rgb(15 30 46 / 44%)"
  }, {
    canvas: "#101a24", surface: "#162431", surfaceSubtle: "#1d2e3d", surfaceHover: "#263948",
    surface2: "#1d2e3d", inverseCanvas: "#09111a", border: "#304758", borderStrong: "#49667b",
    text: "#e0edf5", textSecondary: "#b2cadb", textMuted: "#91afc4", ink: "#e0edf5", inkHover: "#f6fbff",
    accent: "#71b5df", accentText: "#88c2e8", accentHover: "#9dcfed", accentSubtle: "#1b3549",
    control: "#acd2ec",
    reportBlue: "#83b9ef", reportGreen: "#82bea2", reportPink: "#d999bb", reportLime: "#bdc782", reportCyan: "#7bc6d3",
    code: "#c1dce9"
  })),
  preset("forest", palettePair({
    canvas: "#eff3ee", surface: "#fbfdf9", surfaceSubtle: "#e2eae0", surfaceHover: "#d7e2d5",
    surface2: "#e2eae0", inverseCanvas: "#18251c", border: "#c4d1c0", borderStrong: "#97ae93",
    text: "#22372a", textSecondary: "#435e49", textMuted: "#526851", ink: "#22372a", inkHover: "#12251a",
    accent: "#537e60", accentText: "#386347", accentHover: "#416c4e", accentSubtle: "#dfebdf",
    control: "#365940",
    reportBlue: "#426d98", reportGreen: "#37734e", reportPink: "#9b5275", reportLime: "#6c742f", reportCyan: "#357776",
    code: "#36523c", shadow: "rgb(34 57 37 / 8%)", shadowStrong: "rgb(34 57 37 / 15%)", backdrop: "rgb(18 35 23 / 44%)"
  }, {
    canvas: "#151e18", surface: "#1c2920", surfaceSubtle: "#243329", surfaceHover: "#2d3d30",
    surface2: "#243329", inverseCanvas: "#0d140f", border: "#394d3d", borderStrong: "#526d55",
    text: "#e5ede1", textSecondary: "#bccdb4", textMuted: "#a1b598", ink: "#e5ede1", inkHover: "#f8fff4",
    accent: "#9ab99a", accentText: "#a6c8a3", accentHover: "#bbd7b4", accentSubtle: "#263d2c",
    control: "#bfd6b8",
    reportBlue: "#9ab8d5", reportGreen: "#a0c49c", reportPink: "#d3a0b5", reportLime: "#c8ca8d", reportCyan: "#8fc7bc",
    code: "#c5d9bd"
  })),
  preset("iris", palettePair({
    canvas: "#f2f0f8", surface: "#fdfbff", surfaceSubtle: "#e8e3f2", surfaceHover: "#dfd8ec",
    surface2: "#e8e3f2", inverseCanvas: "#241d33", border: "#d0c6e0", borderStrong: "#ab99c5",
    text: "#30263f", textSecondary: "#574768", textMuted: "#685776", ink: "#30263f", inkHover: "#21172e",
    accent: "#7c61ad", accentText: "#694997", accentHover: "#694e99", accentSubtle: "#e9e0f5",
    control: "#5e467e",
    reportBlue: "#506bab", reportGreen: "#4c785b", reportPink: "#a54b80", reportLime: "#76702d", reportCyan: "#39777f",
    code: "#56406e", shadow: "rgb(63 42 88 / 8%)", shadowStrong: "rgb(63 42 88 / 15%)", backdrop: "rgb(29 19 43 / 44%)"
  }, {
    canvas: "#1b1725", surface: "#251f31", surfaceSubtle: "#2e273d", surfaceHover: "#383047",
    surface2: "#2e273d", inverseCanvas: "#120e1a", border: "#463b58", borderStrong: "#665479",
    text: "#ede5f6", textSecondary: "#cbbbdc", textMuted: "#b49ec9", ink: "#ede5f6", inkHover: "#fdf8ff",
    accent: "#b69adf", accentText: "#c4aaeb", accentHover: "#d3bff1", accentSubtle: "#38284b",
    control: "#d1b9ec",
    reportBlue: "#a3b4ed", reportGreen: "#9fc5ac", reportPink: "#deabd1", reportLime: "#c7c591", reportCyan: "#96c8d5",
    code: "#d4c1e7"
  }))
];

/** Mid-gray cannot reach 7:1 against *any* ink. Nudge it to the nearest readable
 * light/dark family instead of silently claiming an impossible contrast guarantee. */
function accessibleCanvas(background: string, mode: Mode): string {
  const target = mode === "dark" ? "#000000" : "#ffffff";
  const ink = mode === "dark" ? "#ffffff" : "#000000";
  for (let step = 0; step <= 100; step++) {
    const candidate = mix(background, target, step / 100);
    if (contrastRatio(ink, candidate) >= 7) return candidate;
  }
  return target;
}

export function appearanceTokens(appearance: AppearancePreferences, resolved: Mode): ThemeTokens {
  const preferences = normalizeAppearance(appearance);
  const source = APPEARANCE_PRESETS.find((item) => item.id === preferences.palette)!;
  const tokens = { ...source[resolved] };
  if (preferences.background) {
    // A custom background may be black in light mode (and vice versa). All layers,
    // shadows and semantic state fills must follow the actual background, not the toggle.
    const mode: Mode = luminance(preferences.background) < 0.179 ? "dark" : "light";
    const semantic = source[mode];
    const dark = mode === "dark";
    tokens.canvas = accessibleCanvas(preferences.background, mode);
    tokens.surface = !dark && luminance(tokens.canvas) > 0.98
      ? mix(tokens.canvas, "#000000", 0.012)
      : mix(tokens.canvas, "#ffffff", dark ? 0.025 : 0.5);
    tokens.surfaceSubtle = mix(tokens.canvas, dark ? "#ffffff" : "#000000", dark ? 0.055 : 0.025);
    tokens.surfaceHover = mix(tokens.canvas, dark ? "#ffffff" : "#000000", dark ? 0.085 : 0.055);
    tokens.surface2 = tokens.surfaceSubtle;
    tokens.inverseCanvas = mix(tokens.canvas, "#000000", dark ? 0.4 : 0.9);
    tokens.border = mix(tokens.canvas, dark ? "#ffffff" : "#000000", dark ? 0.2 : 0.18);
    tokens.borderStrong = mix(tokens.canvas, dark ? "#ffffff" : "#000000", dark ? 0.34 : 0.32);
    tokens.text = semantic.text;
    tokens.textSecondary = semantic.textSecondary;
    tokens.textMuted = semantic.textMuted;
    tokens.ink = semantic.ink;
    tokens.inkHover = semantic.inkHover;
    tokens.control = semantic.control;
    tokens.code = semantic.code;
    tokens.shadow = semantic.shadow;
    tokens.shadowStrong = semantic.shadowStrong;
    tokens.backdrop = semantic.backdrop;
    for (const state of STATES) {
      tokens[state] = semantic[state];
      tokens[`${state}Subtle`] = mix(tokens.canvas, semantic[state], dark ? 0.1 : 0.06);
    }
    for (const report of REPORTS) tokens[report] = semantic[report];
    tokens.accentSubtle = mix(tokens.canvas, preferences.accent ?? tokens.accent, dark ? 0.12 : 0.07);
  }
  if (preferences.accent) {
    tokens.accent = preferences.accent;
    tokens.accentText = preferences.accent;
    const dark = luminance(tokens.canvas) < 0.179;
    tokens.accentHover = mix(preferences.accent, dark ? "#ffffff" : "#000000", 0.14);
    tokens.accentSubtle = mix(tokens.canvas, preferences.accent, dark ? 0.12 : 0.07);
  }
  if (preferences.control) tokens.control = preferences.control;
  return accessibleCopy(tokens);
}

export function palettePreviewStyle(palette: AppearancePalette, resolved: Mode = "light"): string {
  const tokens = appearanceTokens({ palette }, resolved);
  return [
    `--preview-canvas:${tokens.canvas}`, `--preview-surface:${tokens.surface}`,
    `--preview-border:${tokens.border}`, `--preview-text:${tokens.text}`,
    `--preview-muted:${tokens.textMuted}`, `--preview-accent:${tokens.accent}`, `--preview-control:${tokens.control}`
  ].join(";");
}

/** The single global token map remains authoritative for UI CSS variable application. */
export function appearanceTokenStyle(tokens: ThemeTokens): string {
  return (Object.keys(THEME_TOKEN_CSS_PROPERTIES) as (keyof ThemeTokens)[])
    .map((key) => `${THEME_TOKEN_CSS_PROPERTIES[key]}:${tokens[key]}`).join(";");
}

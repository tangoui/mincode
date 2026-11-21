/**
 * Editor theme data shared by the contribution validator
 * (editor-themes.ts) and the Monaco runtime (services/editor-theme.ts): the
 * Monaco theme conversion and the user's custom editor theme model. Imports
 * no contribution contract, so the entry chunk stays small. No DOM, Vue or
 * IPC here.
 */
import type { editor } from "@/services/monaco-runtime.js";

export type EditorThemeKind = "light" | "dark";

export interface EditorThemeRule {
  token: string;
  /** Hex without '#', as Monaco expects. */
  foreground?: string;
  background?: string;
  fontStyle?: string;
}

export interface EditorThemeData {
  kind: EditorThemeKind;
  /** Lower-cased "#rrggbb" / "#rrggbbaa" keyed by Monaco color id. */
  colors: Record<string, string>;
  rules: EditorThemeRule[];
}

export const EDITOR_HEX_PATTERN = /^#(?:[0-9a-fA-F]{3}|[0-9a-fA-F]{6}|[0-9a-fA-F]{8})$/;

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

/** "#rgb" → "#rrggbb", lower-cased. */
export function expandEditorHex(value: string): string {
  const lower = value.toLowerCase();
  if (lower.length === 4) return `#${lower[1]}${lower[1]}${lower[2]}${lower[2]}${lower[3]}${lower[3]}`;
  return lower;
}

/** Stable, Monaco-safe theme name for a contributed editor theme id. */
export function monacoEditorThemeName(id: string): string {
  let hash = 0x811c9dc5;
  for (let index = 0; index < id.length; index += 1) {
    hash ^= id.charCodeAt(index);
    hash = Math.imul(hash, 0x01000193) >>> 0;
  }
  return `mincode-editor-${hash.toString(16).padStart(8, "0")}`;
}

/** Monaco theme name of the user's custom editor theme. */
export const MONACO_CUSTOM_EDITOR_THEME = "mincode-editor-custom";

/**
 * Monaco `defineTheme` data. The gutter, minimap and widgets default to the
 * editor background so a theme that sets only the required colors still
 * looks coherent.
 */
export function editorMonacoTheme(theme: EditorThemeData): editor.IStandaloneThemeData {
  const colors = { ...theme.colors };
  const background = colors["editor.background"];
  for (const key of ["editorGutter.background", "minimap.background", "editorWidget.background", "editorSuggestWidget.background", "editorHoverWidget.background"]) {
    if (background && !colors[key]) colors[key] = background;
  }
  return {
    base: theme.kind === "dark" ? "vs-dark" : "vs",
    inherit: true,
    rules: theme.rules.map((rule) => ({ ...rule })),
    colors,
  };
}

function ruleForeground(theme: Pick<EditorThemeData, "rules">, token: string): string | undefined {
  const rule = theme.rules.find((item) => item.token === token && item.foreground);
  return rule?.foreground ? `#${rule.foreground}` : undefined;
}

/** Background, text and three token colors for the settings swatch. */
export function editorThemeSwatches(theme: EditorThemeData): string[] {
  const colors = [theme.colors["editor.background"], theme.colors["editor.foreground"]];
  for (const token of ["keyword", "string", "function", "type", "number"]) {
    const color = ruleForeground(theme, token);
    if (color && !colors.includes(color)) colors.push(color);
    if (colors.length >= 5) break;
  }
  return colors.filter((color): color is string => Boolean(color));
}

// ---------------------------------------------------------------------------
// Custom editor theme (Settings > General > Editor theme > Custom)

export const CUSTOM_EDITOR_THEME_FIELDS = [
  "background", "foreground", "cursor", "selection", "lineHighlight", "lineNumber",
  "comment", "keyword", "string", "number", "constant", "function", "type", "variable", "operator", "tag", "attribute",
] as const;

export type CustomEditorThemeField = (typeof CUSTOM_EDITOR_THEME_FIELDS)[number];

/** Fields that may be translucent (#rrggbbaa); the others must be opaque. */
export const CUSTOM_EDITOR_THEME_ALPHA_FIELDS: ReadonlySet<CustomEditorThemeField> = new Set(["selection", "lineHighlight"]);

export interface CustomEditorTheme {
  kind: EditorThemeKind;
  colors: Record<CustomEditorThemeField, string>;
}

/** Monaco color ids set by each custom field. */
const CUSTOM_COLOR_TARGETS: Partial<Record<CustomEditorThemeField, string[]>> = {
  background: ["editor.background", "editorGutter.background", "minimap.background"],
  foreground: ["editor.foreground", "editorLineNumber.activeForeground"],
  cursor: ["editorCursor.foreground"],
  selection: ["editor.selectionBackground"],
  lineHighlight: ["editor.lineHighlightBackground"],
  lineNumber: ["editorLineNumber.foreground"],
};

/** Monaco tokens colored by each syntax field. */
const CUSTOM_TOKEN_TARGETS: Partial<Record<CustomEditorThemeField, string[]>> = {
  comment: ["comment"],
  keyword: ["keyword", "storage"],
  string: ["string", "attribute.value", "regexp"],
  number: ["number"],
  constant: ["constant", "variable.predefined", "predefined"],
  function: ["function"],
  type: ["type", "type.identifier"],
  variable: ["variable"],
  operator: ["operator", "delimiter"],
  tag: ["tag", "metatag"],
  attribute: ["attribute.name", "key"],
};

export const DEFAULT_CUSTOM_EDITOR_THEMES: Readonly<Record<EditorThemeKind, CustomEditorTheme>> = {
  dark: {
    kind: "dark",
    colors: {
      background: "#1e1f22", foreground: "#d4d7dd", cursor: "#f0f0f0", selection: "#3d5a8a99", lineHighlight: "#ffffff0d",
      lineNumber: "#6f737a", comment: "#7f8793", keyword: "#cf8e6d", string: "#6aab73", number: "#2aacb8", constant: "#c77dbb",
      function: "#56a8f5", type: "#e5c07b", variable: "#d4d7dd", operator: "#a9b1bd", tag: "#d5b778", attribute: "#bababa",
    },
  },
  light: {
    kind: "light",
    colors: {
      background: "#ffffff", foreground: "#1f2328", cursor: "#0969da", selection: "#0969da33", lineHighlight: "#0000000a",
      lineNumber: "#6e7781", comment: "#6a737d", keyword: "#cf222e", string: "#0a3069", number: "#0550ae", constant: "#8250df",
      function: "#6639ba", type: "#953800", variable: "#1f2328", operator: "#57606a", tag: "#116329", attribute: "#0550ae",
    },
  },
};

function cloneCustom(theme: CustomEditorTheme): CustomEditorTheme {
  return { kind: theme.kind, colors: { ...theme.colors } };
}

/** A valid custom theme: invalid or missing colors fall back to the kind's defaults. */
export function normalizeCustomEditorTheme(value: unknown): CustomEditorTheme {
  const record = isRecord(value) ? value : {};
  const kind: EditorThemeKind = record.kind === "light" ? "light" : "dark";
  const theme = cloneCustom(DEFAULT_CUSTOM_EDITOR_THEMES[kind]);
  const colors = isRecord(record.colors) ? record.colors : {};
  for (const field of CUSTOM_EDITOR_THEME_FIELDS) {
    const color = colors[field];
    if (typeof color !== "string" || !EDITOR_HEX_PATTERN.test(color)) continue;
    const expanded = expandEditorHex(color);
    if (expanded.length === 9 && !CUSTOM_EDITOR_THEME_ALPHA_FIELDS.has(field) && !expanded.endsWith("ff")) continue;
    theme.colors[field] = expanded;
  }
  return { kind, colors: theme.colors };
}

/** The editor theme data of a custom theme. */
export function customEditorThemeData(theme: CustomEditorTheme): EditorThemeData {
  const colors: Record<string, string> = {};
  const rules: EditorThemeRule[] = [];
  for (const field of CUSTOM_EDITOR_THEME_FIELDS) {
    const color = theme.colors[field];
    for (const key of CUSTOM_COLOR_TARGETS[field] ?? []) colors[key] = color;
    for (const token of CUSTOM_TOKEN_TARGETS[field] ?? []) {
      rules.push({ token, foreground: color.slice(1), ...(field === "comment" ? { fontStyle: "italic" } : {}) });
    }
  }
  return { kind: theme.kind, colors, rules };
}

/** Starts a custom theme from an existing editor theme (fields it lacks keep the kind's defaults). */
export function customEditorThemeFrom(theme: EditorThemeData): CustomEditorTheme {
  const custom = cloneCustom(DEFAULT_CUSTOM_EDITOR_THEMES[theme.kind]);
  for (const field of CUSTOM_EDITOR_THEME_FIELDS) {
    const [key] = CUSTOM_COLOR_TARGETS[field] ?? [];
    const [token] = CUSTOM_TOKEN_TARGETS[field] ?? [];
    const color = key ? theme.colors[key] : token ? ruleForeground(theme, token) : undefined;
    if (color) custom.colors[field] = expandEditorHex(color);
  }
  return normalizeCustomEditorTheme(custom);
}

export interface Rgba {
  r: number;
  g: number;
  b: number;
  a: number;
}

/** Expands #rgb to #rrggbb; #rrggbb and #rrggbbaa are returned lower-cased. */
export function expandHexColor(hex: string): string {
  const value = hex.replace(/^#/, "").toLowerCase();
  if (value.length === 3) return `#${[...value].map((digit) => digit + digit).join("")}`;
  return `#${value}`;
}

export function parseHexColor(hex: string): Rgba {
  if (!EDITOR_HEX_PATTERN.test(hex)) throw new Error(`invalid hex color ${hex}`);
  let value = expandEditorHex(hex).slice(1);
  if (value.length === 6) value += "ff";
  const parsed = Number.parseInt(value, 16);
  return {
    r: ((parsed >>> 24) & 0xff) / 255,
    g: ((parsed >>> 16) & 0xff) / 255,
    b: ((parsed >>> 8) & 0xff) / 255,
    a: (parsed & 0xff) / 255,
  };
}

/** Composites a (possibly translucent) color on an opaque backdrop. */
export function compositeOver(color: Rgba, backdrop: Rgba): Rgba {
  return {
    r: color.r * color.a + backdrop.r * (1 - color.a),
    g: color.g * color.a + backdrop.g * (1 - color.a),
    b: color.b * color.a + backdrop.b * (1 - color.a),
    a: 1,
  };
}

export function relativeLuminance(color: Rgba): number {
  const channel = (value: number) => (value <= 0.03928 ? value / 12.92 : ((value + 0.055) / 1.055) ** 2.4);
  return 0.2126 * channel(color.r) + 0.7152 * channel(color.g) + 0.0722 * channel(color.b);
}

export function contrastRatio(foreground: Rgba, background: Rgba): number {
  const first = relativeLuminance(foreground);
  const second = relativeLuminance(background);
  return (Math.max(first, second) + 0.05) / (Math.min(first, second) + 0.05);
}

/** Contrast floor (x:1) a custom theme field is held to; undefined when not checked. */
function customFieldFloor(field: CustomEditorThemeField): number | undefined {
  if (field === "foreground") return 4.5;
  if (field === "cursor" || field === "selection") return 3;
  if (["background", "lineHighlight", "lineNumber"].includes(field)) return undefined;
  return 3;
}

/**
 * Custom theme fields below the contrast floors, with their ratio.
 * Custom themes are the user's choice, so these are warnings, not errors.
 * The selection is checked as the foreground text on the selection.
 */
export function customEditorThemeLowContrast(theme: CustomEditorTheme): Array<{ field: CustomEditorThemeField; ratio: number; floor: number }> {
  const background = parseHexColor(theme.colors.background);
  const low: Array<{ field: CustomEditorThemeField; ratio: number; floor: number }> = [];
  for (const field of CUSTOM_EDITOR_THEME_FIELDS) {
    const floor = customFieldFloor(field);
    if (floor === undefined) continue;
    let ratio: number;
    if (field === "selection") {
      const selection = compositeOver(parseHexColor(theme.colors.selection), background);
      ratio = contrastRatio(parseHexColor(theme.colors.foreground), selection);
    } else {
      ratio = contrastRatio(compositeOver(parseHexColor(theme.colors[field]), background), background);
    }
    if (ratio + 1e-9 < floor) low.push({ field, ratio, floor });
  }
  return low;
}

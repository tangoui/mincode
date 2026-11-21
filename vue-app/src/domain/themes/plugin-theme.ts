/**
 * Frontend stage of the `themes` contribution point: validation of a theme
 * the Go host already accepted (backend/plugin/contrib_themes.go),
 * WCAG contrast checks, and the pure mappings to CSS custom properties,
 * Monaco theme data and an xterm palette. No DOM, Vue or IPC here; the
 * runtime wiring lives in services/ui-theme.ts and
 * services/contributions/themes.ts.
 */
import {
  THEME_CONTRAST_FLOORS,
  THEME_DEFAULT_DANGER,
  THEME_LIMITS,
  THEME_MONACO_COLORS,
  THEME_OPTIONAL_TOKENS,
  THEME_REQUIRED_TOKENS,
  THEME_TERMINAL_KEYS,
  type ThemeTerminalKey,
  type ThemeToken,
} from "@/domain/themes/tokens";
import { resolvePluginTitle } from "@/domain/plugins/title";

export type PluginThemeKind = "light" | "dark";

export interface PluginThemeTokenRule {
  token: string;
  foreground?: string;
  background?: string;
  fontStyle?: string;
}

export interface PluginThemeData {
  id: string;
  title: string;
  titleLocalized?: Record<string, string>;
  kind: PluginThemeKind;
  colors: Partial<Record<ThemeToken, string>>;
  editor?: { colors?: Record<string, string>; rules?: PluginThemeTokenRule[] };
  terminal?: Partial<Record<ThemeTerminalKey, string>>;
}

/** A validated theme with the plugin that contributed it. */
export interface PluginThemeRecord extends PluginThemeData {
  pluginId: string;
  pluginName: string;
}

export type PluginThemeValidation = { ok: true; theme: PluginThemeData } | { ok: false; error: string };

const THEME_ID_PATTERN = /^[a-z0-9][a-z0-9._-]{0,63}$/;
const HEX_COLOR_PATTERN = /^#(?:[0-9a-fA-F]{3}|[0-9a-fA-F]{6}|[0-9a-fA-F]{8})$/;
const RULE_COLOR_PATTERN = /^(?:[0-9a-fA-F]{3}|[0-9a-fA-F]{6}|[0-9a-fA-F]{8})$/;
const RULE_TOKEN_PATTERN = /^[A-Za-z0-9._-]*$/;
const FONT_STYLES = new Set(["italic", "bold", "underline", "strikethrough"]);
const REQUIRED = new Set<string>(THEME_REQUIRED_TOKENS);
const KNOWN_TOKENS = new Set<string>([...THEME_REQUIRED_TOKENS, ...THEME_OPTIONAL_TOKENS]);
const MONACO_COLORS = new Set<string>(THEME_MONACO_COLORS);
const TERMINAL_KEYS = new Set<string>(THEME_TERMINAL_KEYS);
const THEME_FIELDS = new Set(["id", "title", "titleLocalized", "kind", "colors", "editor", "terminal"]);

/**
 * True for a syntactically valid plugin theme id `<pluginId>.<name>`. Used
 * to keep a stored selection whose plugin is not (yet) installed.
 */
export function isPluginThemeId(value: unknown): value is string {
  return (
    typeof value === "string" &&
    THEME_ID_PATTERN.test(value) &&
    value.includes(".") &&
    !value.endsWith(".") &&
    !value.includes("..")
  );
}

export function isHexColor(value: unknown): value is string {
  return typeof value === "string" && HEX_COLOR_PATTERN.test(value);
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

// ---------------------------------------------------------------------------
// Colors and contrast

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
  if (!HEX_COLOR_PATTERN.test(hex) && !RULE_COLOR_PATTERN.test(hex)) throw new Error(`invalid hex color ${hex}`);
  let value = expandHexColor(hex).slice(1);
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

/**
 * Returns the first contrast floor the theme misses, or undefined. Mirrors
 * validateThemeContrast in contrib_themes.go: translucent backgrounds are
 * composited on white (light) or black (dark), text on its background.
 */
export function themeContrastProblem(theme: Pick<PluginThemeData, "kind" | "colors">): string | undefined {
  const page: Rgba = theme.kind === "dark" ? { r: 0, g: 0, b: 0, a: 1 } : { r: 1, g: 1, b: 1, a: 1 };
  for (const floor of THEME_CONTRAST_FLOORS) {
    const foregroundHex = theme.colors[floor.foreground] ?? (floor.foreground === "danger" ? THEME_DEFAULT_DANGER : undefined);
    const backgroundHex = theme.colors[floor.background];
    if (!foregroundHex || !backgroundHex) return `colors.${foregroundHex ? floor.background : floor.foreground} is missing`;
    const background = compositeOver(parseHexColor(backgroundHex), page);
    const ratio = contrastRatio(compositeOver(parseHexColor(foregroundHex), background), background);
    if (ratio + 1e-9 < floor.min) {
      return `contrast of ${floor.foreground} on ${floor.background} is ${ratio.toFixed(2)}:1, below the ${floor.min.toFixed(1)}:1 floor`;
    }
  }
  return undefined;
}

// ---------------------------------------------------------------------------
// Validation

function validateColorMap(value: unknown, allowed: ReadonlySet<string>, field: string): Record<string, string> {
  if (value === undefined) return {};
  if (!isRecord(value)) throw new Error(`${field} must be an object`);
  const colors: Record<string, string> = {};
  for (const [key, color] of Object.entries(value)) {
    if (!allowed.has(key)) throw new Error(`${field} uses unsupported key "${key}"`);
    if (!isHexColor(color)) throw new Error(`${field}.${key} must be a hex color (#rgb, #rrggbb or #rrggbbaa)`);
    colors[key] = color;
  }
  return colors;
}

function validateRule(value: unknown, index: number): PluginThemeTokenRule {
  if (!isRecord(value)) throw new Error(`editor.rules[${index}] must be an object`);
  for (const key of Object.keys(value)) {
    if (!["token", "foreground", "background", "fontStyle"].includes(key)) {
      throw new Error(`editor.rules[${index}] uses unsupported field "${key}"`);
    }
  }
  const { token, foreground, background, fontStyle } = value;
  if (typeof token !== "string" || token.length > THEME_LIMITS.ruleTokenMaxLength || !RULE_TOKEN_PATTERN.test(token)) {
    throw new Error(`editor.rules[${index}].token is invalid`);
  }
  const rule: PluginThemeTokenRule = { token };
  for (const [field, color] of [["foreground", foreground], ["background", background]] as const) {
    if (color === undefined) continue;
    if (typeof color !== "string" || !RULE_COLOR_PATTERN.test(color)) {
      throw new Error(`editor.rules[${index}].${field} must be a hex color without '#'`);
    }
    rule[field] = color;
  }
  if (fontStyle !== undefined) {
    if (typeof fontStyle !== "string") throw new Error(`editor.rules[${index}].fontStyle must be a string`);
    const styles = fontStyle.split(/\s+/).filter(Boolean);
    if (styles.some((style) => !FONT_STYLES.has(style)) || new Set(styles).size !== styles.length) {
      throw new Error(`editor.rules[${index}].fontStyle may only combine italic, bold, underline and strikethrough`);
    }
    rule.fontStyle = fontStyle;
  }
  return rule;
}

/** Validates one untrusted theme entry of `pluginId`. Never throws. */
export function validatePluginTheme(raw: unknown, pluginId: string): PluginThemeValidation {
  try {
    if (!isRecord(raw)) throw new Error("theme must be an object");
    for (const key of Object.keys(raw)) {
      if (!THEME_FIELDS.has(key)) throw new Error(`theme uses unsupported field "${key}"`);
    }
    const { id, title, titleLocalized, kind } = raw;
    if (!isPluginThemeId(id) || !id.startsWith(`${pluginId}.`) || id.length === pluginId.length + 1) {
      throw new Error(`theme id must start with "${pluginId}."`);
    }
    if (typeof title !== "string" || !title.trim() || [...title.trim()].length > THEME_LIMITS.titleMaxRunes) {
      throw new Error("theme title is invalid");
    }
    let localized: Record<string, string> | undefined;
    if (titleLocalized !== undefined) {
      if (!isRecord(titleLocalized)) throw new Error("titleLocalized must be an object");
      localized = {};
      for (const [locale, text] of Object.entries(titleLocalized)) {
        if (typeof text !== "string" || !text.trim()) throw new Error(`titleLocalized.${locale} must be text`);
        localized[locale] = text.trim();
      }
    }
    if (kind !== "light" && kind !== "dark") throw new Error('theme kind must be "light" or "dark"');
    const colors = validateColorMap(raw.colors, KNOWN_TOKENS, "colors") as Partial<Record<ThemeToken, string>>;
    for (const token of REQUIRED) {
      if (!(token in colors)) throw new Error(`colors is missing required token "${token}"`);
    }
    let editor: PluginThemeData["editor"];
    if (raw.editor !== undefined) {
      if (!isRecord(raw.editor)) throw new Error("editor must be an object");
      for (const key of Object.keys(raw.editor)) {
        if (key !== "colors" && key !== "rules") throw new Error(`editor uses unsupported field "${key}"`);
      }
      const rulesRaw = raw.editor.rules;
      if (rulesRaw !== undefined && !Array.isArray(rulesRaw)) throw new Error("editor.rules must be a list");
      if (Array.isArray(rulesRaw) && rulesRaw.length > THEME_LIMITS.tokenRulesMax) {
        throw new Error(`editor.rules exceeds ${THEME_LIMITS.tokenRulesMax} rules`);
      }
      editor = {
        colors: validateColorMap(raw.editor.colors, MONACO_COLORS, "editor.colors"),
        rules: (rulesRaw ?? []).map(validateRule),
      };
    }
    const terminal = raw.terminal === undefined ? undefined : validateColorMap(raw.terminal, TERMINAL_KEYS, "terminal");
    const theme: PluginThemeData = {
      id,
      title: title.trim(),
      ...(localized ? { titleLocalized: localized } : {}),
      kind,
      colors,
      ...(editor ? { editor } : {}),
      ...(terminal ? { terminal: terminal as PluginThemeData["terminal"] } : {}),
    };
    const contrast = themeContrastProblem(theme);
    if (contrast) throw new Error(contrast);
    return { ok: true, theme };
  } catch (error) {
    return { ok: false, error: error instanceof Error ? error.message : String(error) };
  }
}

/** Localized theme title: each tag of `locale` (a tag or useVueI18n().localeChain), then its language, then the plain title. */
export function pluginThemeTitle(
  theme: Pick<PluginThemeData, "title" | "titleLocalized">,
  locale: string | readonly string[],
): string {
  return resolvePluginTitle(theme, locale);
}

// ---------------------------------------------------------------------------
// Mappings

/** `r, g, b` of a hex color (alpha ignored), the shape of `--app-accent-rgb`. */
export function hexToRgbTriplet(hex: string): string {
  const color = parseHexColor(hex);
  return [color.r, color.g, color.b].map((channel) => Math.round(channel * 255)).join(", ");
}

/**
 * CSS custom properties of a theme: every provided token as `--app-<token>`
 * plus the derived `--app-accent-rgb`. Values are validated hex colors only,
 * written through CSSOM `style.setProperty`, never as CSS text.
 */
export function pluginThemeCssProperties(theme: Pick<PluginThemeData, "colors">): Record<string, string> {
  const properties: Record<string, string> = {};
  for (const [token, value] of Object.entries(theme.colors)) {
    if (KNOWN_TOKENS.has(token) && isHexColor(value)) properties[`--app-${token}`] = expandHexColor(value);
  }
  if (theme.colors.accent && isHexColor(theme.colors.accent)) {
    properties["--app-accent-rgb"] = hexToRgbTriplet(theme.colors.accent);
  }
  return properties;
}

/** Stable, CSS/Monaco-safe theme name for a plugin theme id. */
export function monacoPluginThemeName(id: string): string {
  let hash = 0x811c9dc5;
  for (let index = 0; index < id.length; index += 1) {
    hash ^= id.charCodeAt(index);
    hash = Math.imul(hash, 0x01000193) >>> 0;
  }
  return `mincode-plugin-${hash.toString(16).padStart(8, "0")}`;
}

export interface MonacoThemeDefinition {
  base: "vs" | "vs-dark";
  inherit: true;
  rules: PluginThemeTokenRule[];
  colors: Record<string, string>;
}

function translucent(hex: string, alpha: string): string {
  const expanded = expandHexColor(hex);
  return expanded.length === 7 ? `${expanded}${alpha}` : expanded;
}

/**
 * Monaco `defineTheme` data: workbench-derived defaults (surface, text,
 * accent) overlaid with the theme's own editor colors, and its token rules.
 */
export function pluginMonacoTheme(theme: PluginThemeData): MonacoThemeDefinition {
  const { colors } = theme;
  const derived: Record<string, string> = {};
  const set = (key: string, value: string | undefined) => {
    if (value) derived[key] = expandHexColor(value);
  };
  set("editor.background", colors.surface);
  set("editor.foreground", colors.text);
  set("editorCursor.foreground", colors.accent);
  set("editorLineNumber.foreground", colors["text-disabled"]);
  set("editorLineNumber.activeForeground", colors["text-regular"]);
  set("editorWidget.background", colors.surface);
  set("editorWidget.border", colors.border);
  set("editorSuggestWidget.background", colors.surface);
  set("editorSuggestWidget.border", colors.border);
  set("editorSuggestWidget.selectedBackground", colors["accent-soft"]);
  set("editor.lineHighlightBackground", colors.muted);
  set("focusBorder", colors.accent);
  if (colors.accent) derived["editor.selectionBackground"] = translucent(colors.accent, "55");
  for (const [key, value] of Object.entries(theme.editor?.colors ?? {})) {
    if (MONACO_COLORS.has(key) && isHexColor(value)) derived[key] = expandHexColor(value);
  }
  return {
    base: theme.kind === "dark" ? "vs-dark" : "vs",
    inherit: true,
    rules: (theme.editor?.rules ?? []).map((rule) => ({ ...rule })),
    colors: derived,
  };
}

/**
 * xterm palette of a theme: background/foreground/cursor/selection derived
 * from the workbench tokens, overlaid with the theme's terminal section.
 * The caller supplies the ANSI base palette for the theme kind.
 */
export function pluginTerminalTheme<T extends object>(theme: PluginThemeData, base: T): T & Record<string, string> {
  const { colors } = theme;
  const derived: Record<string, string> = {};
  if (colors.surface) derived.background = colors.surface;
  if (colors.text) derived.foreground = colors.text;
  if (colors.accent) derived.cursor = colors.accent;
  if (colors.surface) derived.cursorAccent = colors.surface;
  if (colors["accent-soft"]) derived.selectionBackground = colors["accent-soft"];
  for (const [key, value] of Object.entries(theme.terminal ?? {})) {
    if (TERMINAL_KEYS.has(key) && isHexColor(value)) derived[key] = value;
  }
  return { ...base, ...derived };
}

// ---------------------------------------------------------------------------
// Snapshot collection

export interface ThemePluginIssue {
  pluginId: string;
  /** Index into contributes.themes, or -1 for issues across entries. */
  entry: number;
  theme?: string;
  message: string;
  /** `host`: rejected by the Go manifest validator; `frontend`: rejected when registering. */
  stage: "host" | "frontend";
}

interface ThemeContributionSource {
  pluginId: string;
  pluginName: string;
  point: { entries: unknown[]; issues: ReadonlyArray<{ entry: number; id?: string; message: string }> };
}

/**
 * Validates every contributed theme of a snapshot. Themes are global: the
 * workspace scope of a process plugin does not hide its themes. At most
 * THEME_LIMITS.maxThemesPerPlugin per plugin; the first of a duplicate id wins.
 */
export function collectPluginThemes(contributions: readonly ThemeContributionSource[]): {
  themes: PluginThemeRecord[];
  issues: ThemePluginIssue[];
} {
  const themes: PluginThemeRecord[] = [];
  const issues: ThemePluginIssue[] = [];
  const seen = new Set<string>();
  for (const contribution of contributions) {
    for (const issue of contribution.point.issues) {
      issues.push({ pluginId: contribution.pluginId, stage: "host", entry: issue.entry, message: issue.message, ...(issue.id ? { theme: issue.id } : {}) });
    }
    let count = 0;
    contribution.point.entries.forEach((raw, entry) => {
      const result = validatePluginTheme(raw, contribution.pluginId);
      if (!result.ok) {
        issues.push({ pluginId: contribution.pluginId, stage: "frontend", entry, message: result.error });
        return;
      }
      const issue = (message: string) => issues.push({ pluginId: contribution.pluginId, stage: "frontend", entry, theme: result.theme.id, message });
      if (count >= THEME_LIMITS.maxThemesPerPlugin) return issue(`more than ${THEME_LIMITS.maxThemesPerPlugin} themes`);
      if (seen.has(result.theme.id)) return issue(`duplicate theme id "${result.theme.id}"`);
      seen.add(result.theme.id);
      count += 1;
      themes.push({ ...result.theme, pluginId: contribution.pluginId, pluginName: contribution.pluginName });
    });
  }
  return { themes, issues };
}

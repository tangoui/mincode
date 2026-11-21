/**
 * Frontend stage of the `editorThemes` contribution point (declarative and
 * process plugins, global scope) and the user's custom editor theme. The Go
 * host (backend/plugin/contrib_editor_themes.go) validates each
 * theme at install: the `<pluginId>.<name>` id, allowlisted Monaco color ids
 * with hex values, token rules and the contrast floors. Entries are
 * re-validated here before they reach Monaco (services/editor-theme.ts).
 * No DOM, Vue or IPC here.
 */
import contributionContract from "../plugins/contribution-points.json" with { type: "json" };
import type { PointContribution } from "../plugins/contributions.ts";
import { resolvePluginTitle } from "../plugins/title.ts";
import {
  CUSTOM_EDITOR_THEME_FIELDS,
  expandEditorHex,
  type CustomEditorTheme,
  type CustomEditorThemeField,
  type EditorThemeData,
  type EditorThemeRule,
} from "./editor-theme-data.ts";
import { compositeOver, contrastRatio, parseHexColor, type Rgba } from "./plugin-theme.ts";

export {
  EDITOR_THEMES_CONTRIBUTION_POINT,
  EDITOR_THEME_CUSTOM,
  EDITOR_THEME_FOLLOW,
  isPluginEditorThemeId,
  normalizeEditorThemePreference,
} from "./editor-theme-id.ts";
export * from "./editor-theme-data.ts";

interface EditorThemesContract {
  maxEntries: number;
  limits: {
    titleMaxRunes: number;
    localesMax: number;
    tokenRulesMax: number;
    contrastForegroundTenths: number;
    contrastRuleTenths: number;
    contrastCursorTenths: number;
    contrastSelectionTenths: number;
  };
  monacoColors: string[];
  lists: { requiredColors: string[]; kinds: string[] };
}

const CONTRACT = (contributionContract as unknown as { points: Record<string, EditorThemesContract> }).points.editorThemes;

export const EDITOR_THEME_MAX_ENTRIES = CONTRACT.maxEntries;
export const EDITOR_THEME_TITLE_MAX_RUNES = CONTRACT.limits.titleMaxRunes;
export const EDITOR_THEME_RULES_MAX = CONTRACT.limits.tokenRulesMax;
export const EDITOR_THEME_COLORS: ReadonlySet<string> = new Set(CONTRACT.monacoColors);
export const EDITOR_THEME_REQUIRED_COLORS: readonly string[] = CONTRACT.lists.requiredColors;

export interface EditorThemeRecord extends EditorThemeData {
  /** `<pluginId>.<name>`. */
  id: string;
  pluginId: string;
  pluginName: string;
  title: string;
  titleLocalized?: Record<string, string>;
}

export interface EditorThemeIssue {
  pluginId: string;
  entry: number;
  theme?: string;
  message: string;
  stage: "host" | "frontend";
}

export type EditorThemeValidation =
  | { ok: true; theme: Omit<EditorThemeRecord, "pluginId" | "pluginName"> }
  | { ok: false; error: string };

const PLUGIN_ID_PATTERN = /^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$/;
const NAME_PATTERN = /^[a-z0-9][a-z0-9-]{0,31}$/;
const LOCALE_PATTERN = /^[A-Za-z]{2,8}(?:-[A-Za-z0-9]{1,8}){0,3}$/;
const HEX_PATTERN = /^#(?:[0-9a-fA-F]{3}|[0-9a-fA-F]{6}|[0-9a-fA-F]{8})$/;
const RULE_HEX_PATTERN = /^(?:[0-9a-fA-F]{3}|[0-9a-fA-F]{6}|[0-9a-fA-F]{8})$/;
const RULE_TOKEN_PATTERN = /^[A-Za-z0-9._-]{0,128}$/;
const FONT_STYLES = new Set(["italic", "bold", "underline", "strikethrough"]);
const FIELDS = new Set(["id", "title", "titleLocalized", "kind", "colors", "rules"]);
const RULE_FIELDS = new Set(["token", "foreground", "background", "fontStyle"]);
// eslint-disable-next-line no-control-regex
const UNSAFE_TEXT = /[\u0000-\u001f\u007f-\u009f\u2028\u2029\u202a-\u202e\u2066-\u2069\u200b-\u200f\ufeff]/;

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function titleText(value: unknown): string | undefined {
  if (typeof value !== "string") return undefined;
  const trimmed = value.trim();
  if (!trimmed || UNSAFE_TEXT.test(trimmed) || Array.from(trimmed).length > EDITOR_THEME_TITLE_MAX_RUNES) return undefined;
  return trimmed;
}

function withHash(value: string): string {
  return value.startsWith("#") ? value : `#${value}`;
}

/**
 * Every contrast floor the theme misses (mirrors validateEditorThemeContrast
 * in Go). Translucent colors are composited on the editor background.
 */
export function editorThemeContrastProblems(theme: Pick<EditorThemeData, "colors" | "rules">): string[] {
  const { colors, rules } = theme;
  const limits = CONTRACT.limits;
  const background = parseHexColor(colors["editor.background"] ?? "#000000");
  const problems: string[] = [];
  const check = (label: string, value: string, backdrop: Rgba, tenths: number, against: string) => {
    const ratio = contrastRatio(compositeOver(parseHexColor(value), backdrop), backdrop);
    const minimum = tenths / 10;
    if (ratio + 1e-9 < minimum) problems.push(`contrast of ${label} on ${against} is ${ratio.toFixed(2)}:1, below ${minimum.toFixed(1)}:1`);
  };
  const foreground = colors["editor.foreground"] ?? "#ffffff";
  check("editor.foreground", foreground, background, limits.contrastForegroundTenths, "editor.background");
  if (colors["editorCursor.foreground"]) {
    check("editorCursor.foreground", colors["editorCursor.foreground"], background, limits.contrastCursorTenths, "editor.background");
  }
  if (colors["editor.selectionBackground"]) {
    const selection = compositeOver(parseHexColor(colors["editor.selectionBackground"]), background);
    check("editor.foreground", foreground, selection, limits.contrastSelectionTenths, "editor.selectionBackground");
  }
  rules.forEach((rule, index) => {
    if (!rule.foreground) return;
    const backdrop = rule.background ? compositeOver(parseHexColor(withHash(rule.background)), background) : background;
    const against = rule.background ? "its background" : "editor.background";
    check(`rules[${index}] ("${rule.token}") foreground`, withHash(rule.foreground), backdrop, limits.contrastRuleTenths, against);
  });
  return problems;
}

function validateColors(value: unknown): Record<string, string> {
  if (!isRecord(value)) throw new Error("colors must be an object");
  const colors: Record<string, string> = {};
  for (const [key, color] of Object.entries(value)) {
    if (!EDITOR_THEME_COLORS.has(key)) throw new Error(`colors uses unsupported key "${key}"`);
    if (typeof color !== "string" || !HEX_PATTERN.test(color)) throw new Error(`colors.${key} must be a hex color`);
    colors[key] = expandEditorHex(color);
  }
  for (const key of EDITOR_THEME_REQUIRED_COLORS) {
    const color = colors[key];
    if (!color) throw new Error(`colors is missing required color "${key}"`);
    if (color.length === 9 && !color.endsWith("ff")) throw new Error(`color "${key}" must be opaque`);
  }
  return colors;
}

function validateRules(value: unknown): EditorThemeRule[] {
  if (value === undefined || value === null) return [];
  if (!Array.isArray(value)) throw new Error("rules must be an array");
  if (value.length > EDITOR_THEME_RULES_MAX) throw new Error(`rules exceeds ${EDITOR_THEME_RULES_MAX} rules`);
  return value.map((raw, index) => {
    if (!isRecord(raw)) throw new Error(`rules[${index}] must be an object`);
    for (const key of Object.keys(raw)) {
      if (!RULE_FIELDS.has(key)) throw new Error(`rules[${index}] has unknown field "${key}"`);
    }
    if (typeof raw.token !== "string" || !RULE_TOKEN_PATTERN.test(raw.token)) throw new Error(`rules[${index}].token is invalid`);
    const rule: EditorThemeRule = { token: raw.token };
    for (const field of ["foreground", "background"] as const) {
      const color = raw[field];
      if (color === undefined || color === "") continue;
      if (typeof color !== "string" || !RULE_HEX_PATTERN.test(color)) throw new Error(`rules[${index}].${field} must be a hex color without '#'`);
      rule[field] = color.toLowerCase();
    }
    if (raw.fontStyle !== undefined) {
      if (typeof raw.fontStyle !== "string") throw new Error(`rules[${index}].fontStyle must be a string`);
      const styles = raw.fontStyle.trim().split(/\s+/).filter(Boolean);
      if (styles.some((style) => !FONT_STYLES.has(style)) || new Set(styles).size !== styles.length) {
        throw new Error(`rules[${index}].fontStyle may only combine italic, bold, underline and strikethrough`);
      }
      rule.fontStyle = styles.join(" ");
    }
    return rule;
  });
}

/** Re-validates one theme the host accepted. */
export function validateEditorTheme(raw: unknown, pluginId: string): EditorThemeValidation {
  try {
    if (!isRecord(raw)) throw new Error("theme must be an object");
    for (const key of Object.keys(raw)) {
      if (!FIELDS.has(key)) throw new Error(`unknown field "${key}"`);
    }
    const id = typeof raw.id === "string" ? raw.id.trim() : "";
    if (!id.startsWith(`${pluginId}.`) || !NAME_PATTERN.test(id.slice(pluginId.length + 1))) {
      throw new Error(`id must be "${pluginId}.<name>"`);
    }
    const title = titleText(raw.title);
    if (!title) throw new Error("title is invalid");
    let titleLocalized: Record<string, string> | undefined;
    if (raw.titleLocalized !== undefined && raw.titleLocalized !== null) {
      if (!isRecord(raw.titleLocalized)) throw new Error("titleLocalized must be an object");
      const entries = Object.entries(raw.titleLocalized);
      if (entries.length > CONTRACT.limits.localesMax) throw new Error("titleLocalized has too many locales");
      titleLocalized = {};
      for (const [locale, text] of entries) {
        const clean = titleText(text);
        if (!LOCALE_PATTERN.test(locale) || !clean) throw new Error(`titleLocalized.${locale} is invalid`);
        titleLocalized[locale] = clean;
      }
      if (Object.keys(titleLocalized).length === 0) titleLocalized = undefined;
    }
    if (raw.kind !== "light" && raw.kind !== "dark") throw new Error("kind must be light or dark");
    const colors = validateColors(raw.colors);
    const rules = validateRules(raw.rules);
    const [problem] = editorThemeContrastProblems({ colors, rules });
    if (problem) throw new Error(problem);
    return { ok: true, theme: { id, title, ...(titleLocalized ? { titleLocalized } : {}), kind: raw.kind, colors, rules } };
  } catch (error) {
    return { ok: false, error: error instanceof Error ? error.message : String(error) };
  }
}

/** Validates every contributed theme; skipped entries become issues, never exceptions. */
export function collectEditorThemes(contributions: readonly PointContribution[]): {
  themes: EditorThemeRecord[];
  issues: EditorThemeIssue[];
} {
  const themes: EditorThemeRecord[] = [];
  const issues: EditorThemeIssue[] = [];
  const seen = new Set<string>();
  for (const contribution of contributions) {
    if (!PLUGIN_ID_PATTERN.test(contribution.pluginId)) continue;
    for (const issue of contribution.point?.issues ?? []) {
      issues.push({ pluginId: contribution.pluginId, entry: issue.entry, message: issue.message, stage: "host", ...(issue.id ? { theme: issue.id } : {}) });
    }
    const entries = Array.isArray(contribution.point?.entries) ? contribution.point.entries : [];
    entries.slice(0, EDITOR_THEME_MAX_ENTRIES).forEach((raw, entry) => {
      const result = validateEditorTheme(raw, contribution.pluginId);
      if (!result.ok) {
        issues.push({ pluginId: contribution.pluginId, entry, message: result.error, stage: "frontend" });
        return;
      }
      if (seen.has(result.theme.id)) {
        issues.push({ pluginId: contribution.pluginId, entry, theme: result.theme.id, message: "duplicate theme id", stage: "frontend" });
        return;
      }
      seen.add(result.theme.id);
      themes.push({ ...result.theme, pluginId: contribution.pluginId, pluginName: contribution.pluginName || contribution.pluginId });
    });
  }
  themes.sort((left, right) => left.kind.localeCompare(right.kind) || left.title.localeCompare(right.title) || left.id.localeCompare(right.id));
  return { themes, issues };
}

export function editorThemeTitle(theme: { title: string; titleLocalized?: Record<string, string> }, locale: string | readonly string[]) {
  return resolvePluginTitle({ title: theme.title, titleLocalized: theme.titleLocalized }, locale);
}

/** Install preview (manifest `contributes.editorThemes`): inline titles, or the file path of a referenced theme. */
export function editorThemePreviewSummary(contributes: unknown): string[] {
  const entries = isRecord(contributes) ? contributes.editorThemes : undefined;
  if (!Array.isArray(entries)) return [];
  return entries.slice(0, EDITOR_THEME_MAX_ENTRIES).flatMap((entry) => {
    if (!isRecord(entry)) return [];
    const title = titleText(entry.title);
    if (title) return [title];
    const path = typeof entry.path === "string" && !UNSAFE_TEXT.test(entry.path) ? entry.path.slice(0, 256) : "";
    return path ? [path] : [];
  });
}


/** Contrast floor (x:1) a custom theme field is held to; undefined when not checked. */
function customFieldFloor(field: CustomEditorThemeField): number | undefined {
  const limits = CONTRACT.limits;
  if (field === "foreground") return limits.contrastForegroundTenths / 10;
  if (field === "cursor" || field === "selection") return (field === "cursor" ? limits.contrastCursorTenths : limits.contrastSelectionTenths) / 10;
  if (["background", "lineHighlight", "lineNumber"].includes(field)) return undefined;
  return limits.contrastRuleTenths / 10;
}

/**
 * Custom theme fields below the plugin contrast floors, with their ratio.
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

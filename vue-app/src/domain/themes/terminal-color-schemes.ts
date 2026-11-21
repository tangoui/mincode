/**
 * Frontend stage of the `terminalColorSchemes` contribution point
 * (declarative and process plugins, global scope). The Go host
 * (backend/plugin/contrib_terminal_color_schemes.go) validates each
 * scheme at install: the `<pluginId>.<name>` id, hex-only colours (alpha only
 * for the selection keys) and the contrast floors. Entries are re-validated
 * here before they reach the terminal palette registry in
 * services/terminal-theme.ts. No DOM, Vue or IPC here.
 */
import contributionContract from "../plugins/contribution-points.json" with { type: "json" };
import type { PointContribution } from "../plugins/contributions.ts";
import { resolvePluginTitle } from "../plugins/title.ts";
import { compositeOver, contrastRatio, parseHexColor, type Rgba } from "./plugin-theme.ts";
import { isPluginTerminalColorSchemeId } from "./terminal-color-scheme-id.ts";

export { TERMINAL_COLOR_SCHEMES_CONTRIBUTION_POINT } from "./terminal-color-scheme-id.ts";

interface TerminalColorSchemesContract {
  maxEntries: number;
  limits: {
    titleMaxRunes: number;
    localesMax: number;
    contrastForegroundTenths: number;
    contrastCursorTenths: number;
    contrastSelectionTenths: number;
    contrastAnsiTenths: number;
    contrastBrightBlackTenths: number;
  };
  lists: { requiredColors: string[]; optionalColors: string[]; alphaColors: string[]; kinds: string[] };
}

const CONTRACT = (contributionContract as unknown as { points: Record<string, TerminalColorSchemesContract> }).points
  .terminalColorSchemes;

export const TERMINAL_COLOR_SCHEME_MAX_ENTRIES = CONTRACT.maxEntries;
export const TERMINAL_COLOR_SCHEME_TITLE_MAX_RUNES = CONTRACT.limits.titleMaxRunes;
export const TERMINAL_COLOR_SCHEME_LOCALES_MAX = CONTRACT.limits.localesMax;
export const TERMINAL_COLOR_SCHEME_REQUIRED_COLORS: readonly string[] = CONTRACT.lists.requiredColors;
export const TERMINAL_COLOR_SCHEME_OPTIONAL_COLORS: readonly string[] = CONTRACT.lists.optionalColors;
export const TERMINAL_COLOR_SCHEME_ALPHA_COLORS: readonly string[] = CONTRACT.lists.alphaColors;

/** Chromatic ANSI colours held to the ANSI floor (mirrors terminalColorSchemeAnsiChecked in Go). */
export const TERMINAL_COLOR_SCHEME_ANSI_CHECKED: readonly string[] = [
  "red", "green", "yellow", "blue", "magenta", "cyan",
  "brightRed", "brightGreen", "brightYellow", "brightBlue", "brightMagenta", "brightCyan",
];

/** Swatch order for the settings preview. */
export const TERMINAL_COLOR_SCHEME_SWATCHES: readonly string[] = ["red", "green", "yellow", "blue", "magenta", "cyan"];

export type TerminalColorSchemeKind = "light" | "dark";

export interface TerminalColorSchemeRecord {
  /** `<pluginId>.<name>`. */
  id: string;
  pluginId: string;
  pluginName: string;
  title: string;
  titleLocalized?: Record<string, string>;
  kind: TerminalColorSchemeKind;
  /** Lower-cased hex colours keyed by xterm ITheme key. */
  colors: Record<string, string>;
}

export interface TerminalColorSchemeIssue {
  pluginId: string;
  entry: number;
  scheme?: string;
  message: string;
  stage: "host" | "frontend";
}

export type TerminalColorSchemeValidation =
  | { ok: true; scheme: Omit<TerminalColorSchemeRecord, "pluginId" | "pluginName"> }
  | { ok: false; error: string };

const PLUGIN_ID_PATTERN = /^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$/;
const NAME_PATTERN = /^[a-z0-9][a-z0-9-]{0,31}$/;
const LOCALE_PATTERN = /^[A-Za-z]{2,8}(?:-[A-Za-z0-9]{1,8}){0,3}$/;
const HEX_PATTERN = /^#[0-9a-fA-F]{6}$/;
const HEX_ALPHA_PATTERN = /^#[0-9a-fA-F]{8}$/;
const FIELDS = new Set(["id", "title", "titleLocalized", "kind", "colors"]);
// eslint-disable-next-line no-control-regex
const UNSAFE_TEXT = /[\u0000-\u001f\u007f-\u009f\u2028\u2029\u202a-\u202e\u2066-\u2069\u200b-\u200f\ufeff]/;

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function titleText(value: unknown): string | undefined {
  if (typeof value !== "string") return undefined;
  const trimmed = value.trim();
  if (!trimmed || UNSAFE_TEXT.test(trimmed) || Array.from(trimmed).length > TERMINAL_COLOR_SCHEME_TITLE_MAX_RUNES) return undefined;
  return trimmed;
}

export { isPluginTerminalColorSchemeId };

/** Returns the first contrast floor the scheme misses (mirrors validateTerminalColorSchemeContrast in Go). */
export function terminalColorSchemeContrastProblem(kind: TerminalColorSchemeKind, colors: Record<string, string>): string | undefined {
  const parse = (key: string): Rgba => parseHexColor(colors[key] ?? "#000000");
  const background = parse("background");
  const check = (key: string, foreground: Rgba, backdrop: Rgba, tenths: number, against: string) => {
    const ratio = contrastRatio(foreground, backdrop);
    const minimum = tenths / 10;
    return ratio + 1e-9 < minimum ? `contrast of ${key} on ${against} is ${ratio.toFixed(2)}:1, below ${minimum.toFixed(1)}:1` : undefined;
  };
  const limits = CONTRACT.limits;
  const selectionKey = colors.selectionForeground ? "selectionForeground" : "foreground";
  const selection = compositeOver(parse("selectionBackground"), background);
  return (
    check("foreground", parse("foreground"), background, limits.contrastForegroundTenths, "background") ??
    check("cursor", parse("cursor"), background, limits.contrastCursorTenths, "background") ??
    check(selectionKey, parse(selectionKey), selection, limits.contrastSelectionTenths, "selectionBackground") ??
    TERMINAL_COLOR_SCHEME_ANSI_CHECKED.map((key) => check(key, parse(key), background, limits.contrastAnsiTenths, "background")).find(Boolean) ??
    (kind === "dark" ? check("brightBlack", parse("brightBlack"), background, limits.contrastBrightBlackTenths, "background") : undefined)
  );
}

function validateColors(value: unknown): Record<string, string> {
  if (!isRecord(value)) throw new Error("colors must be an object");
  for (const key of TERMINAL_COLOR_SCHEME_REQUIRED_COLORS) {
    if (!(key in value)) throw new Error(`colors is missing required color "${key}"`);
  }
  const colors: Record<string, string> = {};
  for (const [key, color] of Object.entries(value)) {
    if (!TERMINAL_COLOR_SCHEME_REQUIRED_COLORS.includes(key) && !TERMINAL_COLOR_SCHEME_OPTIONAL_COLORS.includes(key)) {
      throw new Error(`colors has unknown color "${key}"`);
    }
    if (typeof color !== "string") throw new Error(`color "${key}" must be a string`);
    const opaque = HEX_PATTERN.test(color);
    const alpha = HEX_ALPHA_PATTERN.test(color) && TERMINAL_COLOR_SCHEME_ALPHA_COLORS.includes(key);
    if (!opaque && !alpha) throw new Error(`color "${key}" must be a #rrggbb hex color`);
    colors[key] = color.toLowerCase();
  }
  return colors;
}

/** Re-validates one scheme the host accepted. */
export function validateTerminalColorScheme(raw: unknown, pluginId: string): TerminalColorSchemeValidation {
  try {
    if (!isRecord(raw)) throw new Error("scheme must be an object");
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
      if (entries.length > TERMINAL_COLOR_SCHEME_LOCALES_MAX) throw new Error("titleLocalized has too many locales");
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
    const problem = terminalColorSchemeContrastProblem(raw.kind, colors);
    if (problem) throw new Error(problem);
    return { ok: true, scheme: { id, title, ...(titleLocalized ? { titleLocalized } : {}), kind: raw.kind, colors } };
  } catch (error) {
    return { ok: false, error: error instanceof Error ? error.message : String(error) };
  }
}

/** Validates every contributed scheme; skipped entries become issues, never exceptions. */
export function collectTerminalColorSchemes(contributions: readonly PointContribution[]): {
  schemes: TerminalColorSchemeRecord[];
  issues: TerminalColorSchemeIssue[];
} {
  const schemes: TerminalColorSchemeRecord[] = [];
  const issues: TerminalColorSchemeIssue[] = [];
  const seen = new Set<string>();
  for (const contribution of contributions) {
    if (!PLUGIN_ID_PATTERN.test(contribution.pluginId)) continue;
    for (const issue of contribution.point?.issues ?? []) {
      issues.push({ pluginId: contribution.pluginId, entry: issue.entry, message: issue.message, stage: "host", ...(issue.id ? { scheme: issue.id } : {}) });
    }
    const entries = Array.isArray(contribution.point?.entries) ? contribution.point.entries : [];
    entries.slice(0, TERMINAL_COLOR_SCHEME_MAX_ENTRIES).forEach((raw, entry) => {
      const result = validateTerminalColorScheme(raw, contribution.pluginId);
      if (!result.ok) {
        issues.push({ pluginId: contribution.pluginId, entry, message: result.error, stage: "frontend" });
        return;
      }
      if (seen.has(result.scheme.id)) {
        issues.push({ pluginId: contribution.pluginId, entry, scheme: result.scheme.id, message: "duplicate scheme id", stage: "frontend" });
        return;
      }
      seen.add(result.scheme.id);
      schemes.push({ ...result.scheme, pluginId: contribution.pluginId, pluginName: contribution.pluginName || contribution.pluginId });
    });
  }
  schemes.sort((left, right) => left.title.localeCompare(right.title) || left.id.localeCompare(right.id));
  return { schemes, issues };
}

/** The xterm palette of a scheme; optional keys fall back to safe derivations. */
export function terminalColorSchemePalette(scheme: Pick<TerminalColorSchemeRecord, "colors">): Record<string, string> {
  const colors = scheme.colors;
  const palette: Record<string, string> = {};
  for (const key of [...TERMINAL_COLOR_SCHEME_REQUIRED_COLORS, ...TERMINAL_COLOR_SCHEME_OPTIONAL_COLORS]) {
    if (colors[key]) palette[key] = colors[key];
  }
  palette.cursorAccent ??= colors.background ?? "#000000";
  palette.selectionForeground ??= colors.foreground ?? "#ffffff";
  return palette;
}

export function terminalColorSchemeTitle(
  scheme: { title: string; titleLocalized?: Record<string, string> },
  locale: string | readonly string[],
) {
  return resolvePluginTitle({ title: scheme.title, titleLocalized: scheme.titleLocalized }, locale);
}

/** Install preview (manifest `contributes.terminalColorSchemes`): inline titles, or the file path of a referenced scheme. */
export function terminalColorSchemePreviewSummary(contributes: unknown): string[] {
  const entries = isRecord(contributes) ? contributes.terminalColorSchemes : undefined;
  if (!Array.isArray(entries)) return [];
  return entries.slice(0, TERMINAL_COLOR_SCHEME_MAX_ENTRIES).flatMap((entry) => {
    if (!isRecord(entry)) return [];
    const title = titleText(entry.title);
    if (title) return [title];
    const path = typeof entry.path === "string" && !UNSAFE_TEXT.test(entry.path) ? entry.path.slice(0, 256) : "";
    return path ? [path] : [];
  });
}

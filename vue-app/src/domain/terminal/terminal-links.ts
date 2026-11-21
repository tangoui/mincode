/**
 * Pure model of the `terminalLinks` contribution point. The Go host
 * (backend/plugin/contrib_terminal_links.go) rejects regex
 * constructs outside the RE2/JS common subset and URL templates without a
 * fixed host; this module re-checks every entry, compiles it (JavaScript
 * dialect, `u` flag), runs the regex safety heuristics, rejects patterns that
 * match the empty string, and scans terminal lines within a bounded budget.
 * Captures only ever fill the path/query/fragment of a URL (percent-encoded)
 * or a local file position. Nothing here renders HTML or touches xterm, IPC
 * or Vue.
 */
import type { PointContribution } from "@/domain/plugins/contributions";
import { regexSafetyIssue } from "@/domain/editor/regex-safety";

export const TERMINAL_LINKS_CONTRIBUTION_POINT = "terminalLinks";

// Mirrors `points.terminalLinks` in plugins/api/contribution-points.json
// (vue-app/scripts/terminal-links-contributions.test.mjs keeps them in sync).
export const TERMINAL_LINK_MAX_ENTRIES = 16;
export const TERMINAL_LINK_PATTERN_MAX_LENGTH = 256;
export const TERMINAL_LINK_URL_MAX_LENGTH = 1024;
export const TERMINAL_LINK_LINE_SCAN_MAX_CHARS = 2048;
export const TERMINAL_LINK_WRAPPED_ROWS_MAX = 4;
export const TERMINAL_LINK_MATCHES_PER_LINE = 32;
export const TERMINAL_LINK_PER_LINE_BUDGET_MS = 4;
export const TERMINAL_LINK_OVERRUNS_BEFORE_DISABLE = 3;
export const TERMINAL_LINK_ACTIVE_PATTERNS_MAX = 32;
export const TERMINAL_LINK_CAPTURE_MAX_RUNES = 512;
/** Largest accepted line/column number of a file target. */
export const TERMINAL_LINK_MAX_POSITION = 10_000_000;

export type TerminalLinkTerminal = "local" | "ssh" | "docker";
export type TerminalLinkTarget =
  | { type: "url"; url: string }
  | { type: "file"; path: string; line?: string; column?: string };

const TERMINALS: readonly TerminalLinkTerminal[] = ["local", "ssh", "docker"];
const SCHEMES = ["https", "http"] as const;
const ID_PATTERN = /^[a-z0-9][a-z0-9-]{0,31}$/;
const PLUGIN_ID_PATTERN = /^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$/;
const PLACEHOLDER = /\{([0-9])\}/g;
const CAPTURE = /^\{([0-9])\}$/;
const MAX_GROUPS = 9;
// eslint-disable-next-line no-control-regex
const UNSAFE_TEXT = /[\u0000-\u001f\u007f-\u009f\u2028\u2029\u202a-\u202e\u2066-\u2069\u200b-\u200f\u2060\ufeff]/;
// eslint-disable-next-line no-control-regex
const UNSAFE_NAME = /[\u0000-\u001f\u007f-\u009f\u2028\u2029\u202a-\u202e\u2066-\u2069\u200b-\u200f\u2060\ufeff]/g;
/** Built-in http(s) links (the xterm web-links addon); plugin links never overlap them. */
const BUILTIN_URL = /https?:\/\/[^\s"'<>]+/g;
const CHECK_EVERY_MATCHES = 8;

export interface TerminalLinkRule {
  /** `<pluginId>/<ruleId>`. */
  key: string;
  pluginId: string;
  pluginName: string;
  id: string;
  pattern: string;
  ignoreCase: boolean;
  target: TerminalLinkTarget;
  terminals: TerminalLinkTerminal[];
  /** Capturing groups in `pattern`. */
  groups: number;
}

export interface CompiledTerminalLinkRule extends TerminalLinkRule {
  regex: RegExp;
}

export interface TerminalLinkMatch {
  rule: CompiledTerminalLinkRule;
  /** UTF-16 offsets into the scanned text; `end` is exclusive. */
  start: number;
  end: number;
  text: string;
  /** `groups[0]` is the whole match. */
  groups: Array<string | undefined>;
}

export interface TerminalLinkScanResult {
  links: TerminalLinkMatch[];
  /** Rule keys that exceeded the time budget during this scan. */
  overruns: string[];
}

export interface TerminalLinkScanOptions {
  now?: () => number;
  budgetMs?: number;
  maxChars?: number;
  maxMatches?: number;
}

function objectValue(value: unknown): Record<string, unknown> | undefined {
  return typeof value === "object" && value !== null && !Array.isArray(value) ? (value as Record<string, unknown>) : undefined;
}

function sanitizeName(value: string) {
  return Array.from(value.replace(UNSAFE_NAME, "").trim()).slice(0, 64).join("");
}

/**
 * Checks the regex source like the host: length, printable, no
 * backreferences, lookaround, named groups, inline modifiers or \p escapes.
 * Returns the capture count, or undefined.
 */
export function terminalLinkPatternGroups(pattern: string): number | undefined {
  const length = Array.from(pattern).length;
  if (length === 0 || length > TERMINAL_LINK_PATTERN_MAX_LENGTH || UNSAFE_TEXT.test(pattern)) return undefined;
  let groups = 0;
  let inClass = false;
  for (let index = 0; index < pattern.length; index += 1) {
    const char = pattern[index];
    if (char === "\\") {
      const next = pattern[index + 1];
      if (next === undefined) return undefined;
      if ((next >= "1" && next <= "9") || next === "p" || next === "P") return undefined;
      if (next === "k" && pattern[index + 2] === "<") return undefined;
      index += 1;
    } else if (char === "[") {
      inClass = true;
    } else if (char === "]") {
      inClass = false;
    } else if (char === "(" && !inClass) {
      if (pattern[index + 1] === "?") {
        if (pattern[index + 2] !== ":") return undefined;
        continue;
      }
      groups += 1;
      if (groups > MAX_GROUPS) return undefined;
    }
  }
  return groups;
}

function placeholderInRange(value: string, groups: number) {
  for (const match of value.matchAll(PLACEHOLDER)) {
    if (Number(match[1]) > groups) return false;
  }
  return true;
}

/** Checks a URL template like the host: fixed scheme and host, placeholders only after the host path "/". */
export function terminalLinkUrlTemplateValid(template: string, groups: number): boolean {
  if (!template || template.length > TERMINAL_LINK_URL_MAX_LENGTH || UNSAFE_TEXT.test(template) || /[ \\]/.test(template)) {
    return false;
  }
  const scheme = SCHEMES.find((candidate) => template.startsWith(`${candidate}://`));
  if (!scheme) return false;
  const authorityStart = scheme.length + 3;
  const rest = template.slice(authorityStart);
  const pathStart = rest.search(/[/?#]/);
  const authority = pathStart < 0 || rest[pathStart] !== "/" ? template : template.slice(0, authorityStart + pathStart);
  PLACEHOLDER.lastIndex = 0;
  if (/\{[0-9]\}/.test(authority)) return false;
  if (!placeholderInRange(template, groups)) return false;
  const filled = template.replace(PLACEHOLDER, "x");
  if (/[{}]/.test(filled)) return false;
  try {
    const url = new URL(filled);
    return url.protocol === `${scheme}:` && Boolean(url.hostname) && !url.username && !url.password;
  } catch {
    return false;
  }
}

function parseTarget(value: unknown, groups: number): TerminalLinkTarget | undefined {
  const record = objectValue(value);
  if (!record) return undefined;
  if (record.type === "url") {
    if (Object.keys(record).some((key) => key !== "type" && key !== "url")) return undefined;
    return typeof record.url === "string" && terminalLinkUrlTemplateValid(record.url, groups)
      ? { type: "url", url: record.url }
      : undefined;
  }
  if (record.type !== "file") return undefined;
  if (Object.keys(record).some((key) => !["type", "path", "line", "column"].includes(key))) return undefined;
  const capture = (field: unknown, required: boolean): string | undefined | null => {
    if (field === undefined || field === null) return required ? null : undefined;
    if (typeof field !== "string") return null;
    const match = CAPTURE.exec(field);
    return match && Number(match[1]) <= groups ? field : null;
  };
  const path = capture(record.path, true);
  const line = capture(record.line, false);
  const column = capture(record.column, false);
  if (path === null || path === undefined || line === null || column === null) return undefined;
  return { type: "file", path, ...(line ? { line } : {}), ...(column ? { column } : {}) };
}

/** Re-validates one entry; returns undefined when anything is out of contract. */
export function parseTerminalLinkEntry(
  value: unknown,
  plugin: { pluginId: string; pluginName: string },
): TerminalLinkRule | undefined {
  const record = objectValue(value);
  if (!record || typeof record.id !== "string" || !ID_PATTERN.test(record.id)) return undefined;
  if (typeof record.pattern !== "string") return undefined;
  const groups = terminalLinkPatternGroups(record.pattern);
  if (groups === undefined) return undefined;
  if (record.ignoreCase !== undefined && record.ignoreCase !== null && typeof record.ignoreCase !== "boolean") return undefined;
  const target = parseTarget(record.target, groups);
  if (!target) return undefined;
  let terminals: TerminalLinkTerminal[];
  if (record.terminals === undefined || record.terminals === null) {
    terminals = target.type === "file" ? ["local"] : [...TERMINALS];
  } else {
    if (!Array.isArray(record.terminals) || record.terminals.length === 0) return undefined;
    if (!record.terminals.every((item) => TERMINALS.includes(item as TerminalLinkTerminal))) return undefined;
    terminals = TERMINALS.filter((item) => (record.terminals as unknown[]).includes(item));
  }
  if (target.type === "file" && terminals.some((terminal) => terminal !== "local")) return undefined;
  return {
    key: `${plugin.pluginId}/${record.id}`,
    pluginId: plugin.pluginId,
    pluginName: sanitizeName(plugin.pluginName) || plugin.pluginId,
    id: record.id,
    pattern: record.pattern,
    ignoreCase: record.ignoreCase === true,
    target,
    terminals,
    groups,
  };
}

export function terminalLinkFlags(rule: Pick<TerminalLinkRule, "ignoreCase">) {
  return `gu${rule.ignoreCase ? "i" : ""}`;
}

/**
 * Compiles a rule for scanning. Returns an issue when the pattern does not
 * compile, fails the regex safety heuristics, or matches the empty string.
 */
export function compileTerminalLinkRule(
  rule: TerminalLinkRule,
): { rule: CompiledTerminalLinkRule } | { issue: string } {
  const safety = regexSafetyIssue(rule.pattern, { ignoreCase: rule.ignoreCase, unicode: true });
  if (safety) return { issue: safety };
  let regex: RegExp;
  try {
    regex = new RegExp(rule.pattern, terminalLinkFlags(rule));
  } catch (error) {
    return { issue: `invalid regular expression: ${error instanceof Error ? error.message : String(error)}` };
  }
  regex.lastIndex = 0;
  if (regex.exec("") !== null) return { issue: "pattern matches the empty string" };
  regex.lastIndex = 0;
  return { rule: { ...rule, regex } };
}

/**
 * Rules of plugins active in the workspace: at most 16 per plugin and 32 in
 * total, taken in plugin id order.
 */
export function resolveTerminalLinkRules(contributions: readonly PointContribution[]): TerminalLinkRule[] {
  const rules: TerminalLinkRule[] = [];
  const seen = new Set<string>();
  const sorted = [...contributions].sort((left, right) => left.pluginId.localeCompare(right.pluginId));
  for (const contribution of sorted) {
    if (!contribution.activeInWorkspace || !PLUGIN_ID_PATTERN.test(contribution.pluginId)) continue;
    const entries = Array.isArray(contribution.point?.entries) ? contribution.point.entries : [];
    for (const entry of entries.slice(0, TERMINAL_LINK_MAX_ENTRIES)) {
      if (rules.length >= TERMINAL_LINK_ACTIVE_PATTERNS_MAX) return rules;
      const rule = parseTerminalLinkEntry(entry, contribution);
      if (!rule || seen.has(rule.key)) continue;
      seen.add(rule.key);
      rules.push(rule);
    }
  }
  return rules;
}

function builtinUrlRanges(text: string): Array<[number, number]> {
  return Array.from(text.matchAll(BUILTIN_URL), (match) => [match.index ?? 0, (match.index ?? 0) + match[0].length]);
}

function overlaps(ranges: ReadonlyArray<readonly [number, number]>, start: number, end: number) {
  return ranges.some(([left, right]) => start < right && left < end);
}

/**
 * Scans one (logical) terminal line. At most `maxChars` characters and
 * `maxMatches` links; the clock is checked between rules and every 8 matches,
 * and a rule running when the budget is exceeded is reported in `overruns`
 * (the scan then stops). Ranges overlapping a built-in http(s) link or an
 * earlier plugin link are skipped.
 */
export function scanTerminalLine(
  line: string,
  rules: readonly CompiledTerminalLinkRule[],
  options: TerminalLinkScanOptions = {},
): TerminalLinkScanResult {
  const now = options.now ?? (() => performance.now());
  const budget = options.budgetMs ?? TERMINAL_LINK_PER_LINE_BUDGET_MS;
  const maxMatches = options.maxMatches ?? TERMINAL_LINK_MATCHES_PER_LINE;
  const text = line.slice(0, options.maxChars ?? TERMINAL_LINK_LINE_SCAN_MAX_CHARS);
  const links: TerminalLinkMatch[] = [];
  const overruns: string[] = [];
  if (!text || rules.length === 0) return { links, overruns };
  const taken: Array<[number, number]> = builtinUrlRanges(text);
  const started = now();
  for (const rule of rules) {
    if (links.length >= maxMatches) break;
    if (now() - started > budget) {
      overruns.push(rule.key);
      break;
    }
    const regex = new RegExp(rule.regex.source, rule.regex.flags);
    let count = 0;
    let exceeded = false;
    let match: RegExpExecArray | null;
    while (links.length < maxMatches && (match = regex.exec(text)) !== null) {
      if (match[0] === "") {
        // Zero-width guard: never loop on an empty match.
        regex.lastIndex += 1;
        continue;
      }
      const start = match.index;
      const end = start + match[0].length;
      if (!overlaps(taken, start, end)) {
        taken.push([start, end]);
        links.push({ rule, start, end, text: match[0], groups: Array.from(match) });
      }
      count += 1;
      if (count % CHECK_EVERY_MATCHES === 0 && now() - started > budget) {
        exceeded = true;
        break;
      }
    }
    if (exceeded || now() - started > budget) {
      overruns.push(rule.key);
      break;
    }
  }
  links.sort((left, right) => left.start - right.start);
  return { links, overruns };
}

/**
 * Builds the URL of a match: every `{n}` becomes the percent-encoded capture
 * (empty when the group did not participate). The scheme and host come from
 * the template and must be unchanged. Undefined when a capture is too long
 * or the result is not a valid http(s) URL on the template host.
 */
export function resolveTerminalLinkUrl(template: string, groups: ReadonlyArray<string | undefined>): string | undefined {
  let tooLong = false;
  const filled = template.replace(PLACEHOLDER, (_token, digit: string) => {
    const capture = groups[Number(digit)] ?? "";
    if (Array.from(capture).length > TERMINAL_LINK_CAPTURE_MAX_RUNES) tooLong = true;
    return encodeURIComponent(capture);
  });
  if (tooLong || filled.length > TERMINAL_LINK_URL_MAX_LENGTH * 4) return undefined;
  try {
    const expected = new URL(template.replace(PLACEHOLDER, "x"));
    const url = new URL(filled);
    if (url.protocol !== "http:" && url.protocol !== "https:") return undefined;
    if (url.protocol !== expected.protocol || url.host !== expected.host || url.username || url.password) return undefined;
    return url.href;
  } catch {
    return undefined;
  }
}

export interface TerminalLinkFilePosition {
  path: string;
  line?: number;
  column?: number;
}

function position(value: string | undefined): number | undefined | null {
  if (value === undefined || value === "") return undefined;
  if (!/^\d{1,8}$/.test(value)) return null;
  const number = Number(value);
  return number >= 1 && number <= TERMINAL_LINK_MAX_POSITION ? number : null;
}

/** Resolves a file target's captures; undefined when the path is empty/too long or a position is not 1..10^7. */
export function resolveTerminalLinkFile(
  target: Extract<TerminalLinkTarget, { type: "file" }>,
  groups: ReadonlyArray<string | undefined>,
): TerminalLinkFilePosition | undefined {
  const pick = (placeholder: string | undefined) => {
    if (!placeholder) return undefined;
    const match = CAPTURE.exec(placeholder);
    return match ? groups[Number(match[1])] : undefined;
  };
  const path = pick(target.path)?.trim() ?? "";
  if (!path || Array.from(path).length > TERMINAL_LINK_CAPTURE_MAX_RUNES || UNSAFE_TEXT.test(path)) return undefined;
  const line = position(pick(target.line));
  const column = position(pick(target.column));
  if (line === null || column === null) return undefined;
  return { path, ...(line ? { line } : {}), ...(line && column ? { column } : {}) };
}

/** Joins a relative file path onto the terminal cwd (POSIX or Windows); absolute paths are returned as-is. */
export function terminalLinkAbsolutePath(path: string, cwd: string | undefined): string | undefined {
  if (path.startsWith("/") || /^[A-Za-z]:[\\/]/.test(path)) return path;
  if (path.startsWith("~")) return undefined;
  if (!cwd) return undefined;
  const separator = cwd.includes("\\") && !cwd.includes("/") ? "\\" : "/";
  return `${cwd.replace(/[\\/]+$/, "")}${separator}${path.replace(/^\.[\\/]/, "")}`;
}

/** Plain-text hover label: "<target> · <pluginName>". */
export function terminalLinkHoverText(target: string, pluginName: string) {
  const clean = (value: string) => Array.from(value.replace(UNSAFE_NAME, "")).slice(0, 256).join("");
  return `${clean(target)} · ${clean(pluginName)}`;
}

export interface TerminalLinkPreviewEntry {
  id: string;
  pattern: string;
  /** `scheme://host` of a URL target, or "file". */
  target: string;
  terminals: TerminalLinkTerminal[];
}

/** Install preview (manifest `contributes.terminalLinks`): each rule's pattern, target host and terminals. */
export function terminalLinkPreviewSummary(contributes: unknown): TerminalLinkPreviewEntry[] {
  const entries = objectValue(contributes)?.terminalLinks;
  if (!Array.isArray(entries)) return [];
  return entries.slice(0, TERMINAL_LINK_MAX_ENTRIES).flatMap((entry): TerminalLinkPreviewEntry[] => {
    const rule = parseTerminalLinkEntry(entry, { pluginId: "plugin", pluginName: "plugin" });
    if (!rule) return [];
    let target = "file";
    if (rule.target.type === "url") {
      try {
        const url = new URL(rule.target.url.replace(PLACEHOLDER, "x"));
        target = `${url.protocol}//${url.host}`;
      } catch {
        return [];
      }
    }
    return [{ id: rule.id, pattern: rule.pattern, target, terminals: rule.terminals }];
  });
}

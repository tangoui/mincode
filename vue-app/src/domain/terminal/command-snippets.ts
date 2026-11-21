/**
 * Pure model of the `commandSnippets` contribution point. The Go host
 * (backend/plugin/contrib_command_snippets.go) validates libraries at
 * install; entries are still untrusted here and are re-checked before they
 * reach the terminal. A snippet is never executed: expansion fills declared
 * variables with single-line user input, `insertionPlan` strips trailing
 * newlines and never produces "\r", and multi-line text is only inserted when
 * the shell has bracketed paste enabled (after the paste confirmation).
 * Nothing here touches IPC, xterm or Vue.
 */
import type { PointContribution } from "@/domain/plugins/contributions";
import { resolvePluginTitle } from "@/domain/plugins/title";
import type { TerminalProfilePlatform } from "@/domain/terminal/profiles";

export const COMMAND_SNIPPETS_CONTRIBUTION_POINT = "commandSnippets";

// Mirrors `points.commandSnippets` in plugins/api/contribution-points.json
// (vue-app/scripts/command-snippets-contributions.test.mjs keeps them in sync).
export const COMMAND_SNIPPET_MAX_LIBRARIES = 8;
export const COMMAND_SNIPPET_MAX_PER_LIBRARY = 200;
export const COMMAND_SNIPPET_MAX_PER_PLUGIN = 500;
export const COMMAND_SNIPPET_TITLE_MAX_RUNES = 64;
export const COMMAND_SNIPPET_DESCRIPTION_MAX_RUNES = 256;
export const COMMAND_SNIPPET_LOCALES_MAX = 16;
export const COMMAND_SNIPPET_COMMAND_MAX_BYTES = 4096;
export const COMMAND_SNIPPET_COMMAND_MAX_LINES = 32;
export const COMMAND_SNIPPET_VARIABLES_MAX = 8;
export const COMMAND_SNIPPET_VARIABLE_TITLE_MAX_RUNES = 64;
export const COMMAND_SNIPPET_VARIABLE_DEFAULT_MAX_RUNES = 256;
export const COMMAND_SNIPPET_VARIABLE_VALUE_MAX_RUNES = 1024;
export const COMMAND_SNIPPET_TAGS_MAX = 8;
export const COMMAND_SNIPPET_TAG_MAX_RUNES = 24;

export type CommandSnippetTarget = "local" | "ssh" | "docker";
export type CommandSnippetQuote = "none" | "posix";
export type CommandSnippetShell = "posix" | "powershell" | "cmd";

const TARGETS: readonly CommandSnippetTarget[] = ["local", "ssh", "docker"];
const PLATFORMS: readonly TerminalProfilePlatform[] = ["darwin", "linux", "windows"];
const QUOTES: readonly CommandSnippetQuote[] = ["none", "posix"];
const ID_PATTERN = /^[a-z0-9][a-z0-9-]{0,31}$/;
const PLUGIN_ID_PATTERN = /^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$/;
const LOCALE_PATTERN = /^[A-Za-z]{2,8}(?:-[A-Za-z0-9]{1,8}){0,3}$/;
const VARIABLE_PATTERN = /^[A-Za-z][A-Za-z0-9_]{0,31}$/;
const TOKEN_PATTERN = /\{\{\s*([A-Za-z][A-Za-z0-9_]*)\s*\}\}/g;
const POSIX_SAFE_VALUE = /^[A-Za-z0-9_./:=@%+,-]+$/;
// eslint-disable-next-line no-control-regex
const UNSAFE_TEXT = /[\u0000-\u001f\u007f-\u009f\u2028\u2029\u202a-\u202e\u2066-\u2069\u200b-\u200f\u2060\ufeff]/g;
// eslint-disable-next-line no-control-regex
const UNSAFE_LINE = /[\u0000-\u001f\u007f-\u009f\u2028\u2029\u202a-\u202e\u2066-\u2069\u200b-\u200f\u2060\ufeff]/;
// Command text keeps tab and newline; everything else unsafe rejects the snippet.
// eslint-disable-next-line no-control-regex
const UNSAFE_COMMAND = /[\u0000-\u0008\u000b-\u001f\u007f-\u009f\u2028\u2029\u202a-\u202e\u2066-\u2069\u200b-\u200f\u2060\ufeff]/;
// Variable values: one line, no control/format/separator characters (\p{Cc}\p{Cf}\p{Zl}\p{Zp}).
const UNSAFE_VALUE = /[\p{Cc}\p{Cf}\p{Zl}\p{Zp}]/u;

export interface CommandSnippetVariable {
  name: string;
  title: string;
  default: string;
  quote: CommandSnippetQuote;
}

export interface CommandSnippet {
  /** `<pluginId>/<libraryId>/<commandId>`. */
  key: string;
  pluginId: string;
  pluginName: string;
  libraryId: string;
  libraryTitle: string;
  libraryTitleLocalized?: Record<string, string>;
  id: string;
  title: string;
  titleLocalized?: Record<string, string>;
  description: string;
  descriptionLocalized?: Record<string, string>;
  /** Normalized text ("\n"-joined, no trailing whitespace or newline). */
  command: string;
  variables: CommandSnippetVariable[];
  tags: string[];
  targets: CommandSnippetTarget[];
  platforms: TerminalProfilePlatform[];
}

export interface CommandSnippetExpansion {
  text: string;
  /** Variables whose value could not be quoted for this shell (cmd.exe): shown as a notice. */
  unquoted: string[];
  /** First invalid variable (multi-line, control character or too long), if any. */
  invalid?: string;
}

export type CommandSnippetInsertion =
  | { mode: "single"; text: string }
  | { mode: "multiline"; text: string }
  | { mode: "blocked"; text: string };

function objectValue(value: unknown): Record<string, unknown> | undefined {
  return typeof value === "object" && value !== null && !Array.isArray(value) ? (value as Record<string, unknown>) : undefined;
}

function runes(value: string) {
  return Array.from(value).length;
}

function sanitizeText(value: string, maxRunes: number) {
  return Array.from(value.replace(UNSAFE_TEXT, "").trim()).slice(0, maxRunes).join("");
}

/** Single-line text within `maxRunes`; undefined when unsafe or too long (never truncated silently for data). */
function lineText(value: unknown, maxRunes: number): string | undefined {
  if (value === undefined || value === null) return "";
  if (typeof value !== "string") return undefined;
  const trimmed = value.trim();
  return UNSAFE_LINE.test(trimmed) || runes(trimmed) > maxRunes ? undefined : trimmed;
}

function localized(value: unknown, maxRunes: number): Record<string, string> | undefined {
  const record = objectValue(value);
  if (!record) return undefined;
  const result: Record<string, string> = {};
  let count = 0;
  for (const [locale, text] of Object.entries(record)) {
    if (count >= COMMAND_SNIPPET_LOCALES_MAX) break;
    if (!LOCALE_PATTERN.test(locale) || typeof text !== "string") continue;
    const clean = sanitizeText(text, maxRunes);
    if (!clean) continue;
    result[locale] = clean;
    count += 1;
  }
  return count > 0 ? result : undefined;
}

function subset<T extends string>(value: unknown, allowed: readonly T[]): T[] | undefined {
  if (value === undefined || value === null) return [...allowed];
  if (!Array.isArray(value) || value.length === 0) return undefined;
  if (!value.every((item) => allowed.includes(item as T))) return undefined;
  return allowed.filter((item) => value.includes(item));
}

/** Normalizes command text like the host; undefined when out of contract. */
export function normalizeCommandSnippetText(value: unknown): string | undefined {
  let text: string;
  if (Array.isArray(value)) {
    if (value.length > COMMAND_SNIPPET_COMMAND_MAX_LINES || !value.every((line) => typeof line === "string")) return undefined;
    text = value.join("\n");
  } else if (typeof value === "string") {
    text = value;
  } else {
    return undefined;
  }
  if (text.includes("\r") || UNSAFE_COMMAND.test(text)) return undefined;
  text = text
    .split("\n")
    .map((line) => line.replace(/[ \t]+$/, ""))
    .join("\n")
    .replace(/^\n+|\n+$/g, "");
  if (!text.trim()) return undefined;
  if (new TextEncoder().encode(text).length > COMMAND_SNIPPET_COMMAND_MAX_BYTES) return undefined;
  if (text.split("\n").length > COMMAND_SNIPPET_COMMAND_MAX_LINES) return undefined;
  return text;
}

function parseVariable(value: unknown): CommandSnippetVariable | undefined {
  const record = objectValue(value);
  if (!record || typeof record.name !== "string" || !VARIABLE_PATTERN.test(record.name)) return undefined;
  const title = lineText(record.title, COMMAND_SNIPPET_VARIABLE_TITLE_MAX_RUNES);
  const fallback = lineText(record.default, COMMAND_SNIPPET_VARIABLE_DEFAULT_MAX_RUNES);
  if (title === undefined || fallback === undefined) return undefined;
  const quote = record.quote === undefined || record.quote === null ? "none" : record.quote;
  if (!QUOTES.includes(quote as CommandSnippetQuote)) return undefined;
  return { name: record.name, title, default: fallback, quote: quote as CommandSnippetQuote };
}

interface LibraryContext {
  pluginId: string;
  pluginName: string;
  libraryId: string;
  libraryTitle: string;
  libraryTitleLocalized?: Record<string, string>;
  targets: CommandSnippetTarget[];
  platforms: TerminalProfilePlatform[];
}

function parseCommand(value: unknown, library: LibraryContext): CommandSnippet | undefined {
  const record = objectValue(value);
  if (!record || typeof record.id !== "string" || !ID_PATTERN.test(record.id)) return undefined;
  const title = lineText(record.title, COMMAND_SNIPPET_TITLE_MAX_RUNES);
  const description = lineText(record.description, COMMAND_SNIPPET_DESCRIPTION_MAX_RUNES);
  if (!title || description === undefined) return undefined;
  const command = normalizeCommandSnippetText(record.command);
  if (!command) return undefined;
  const rawVariables = record.variables ?? [];
  if (!Array.isArray(rawVariables) || rawVariables.length > COMMAND_SNIPPET_VARIABLES_MAX) return undefined;
  const variables: CommandSnippetVariable[] = [];
  for (const raw of rawVariables) {
    const variable = parseVariable(raw);
    if (!variable || variables.some((item) => item.name === variable.name)) return undefined;
    variables.push(variable);
  }
  for (const match of command.matchAll(TOKEN_PATTERN)) {
    const name = match[1] ?? "";
    if (VARIABLE_PATTERN.test(name) && !variables.some((item) => item.name === name)) return undefined;
  }
  const rawTags = record.tags ?? [];
  if (!Array.isArray(rawTags) || rawTags.length > COMMAND_SNIPPET_TAGS_MAX) return undefined;
  const tags: string[] = [];
  for (const raw of rawTags) {
    const tag = lineText(raw, COMMAND_SNIPPET_TAG_MAX_RUNES);
    if (!tag) return undefined;
    if (!tags.includes(tag)) tags.push(tag);
  }
  return {
    ...library,
    key: `${library.pluginId}/${library.libraryId}/${record.id}`,
    id: record.id,
    title,
    titleLocalized: localized(record.titleLocalized, COMMAND_SNIPPET_TITLE_MAX_RUNES),
    description,
    descriptionLocalized: localized(record.descriptionLocalized, COMMAND_SNIPPET_DESCRIPTION_MAX_RUNES),
    command,
    variables,
    tags,
  };
}

/**
 * Re-validates one library; returns its commands, or undefined when the
 * library itself is out of contract. Invalid commands are dropped.
 */
export function parseCommandSnippetLibrary(
  value: unknown,
  plugin: { pluginId: string; pluginName: string },
): CommandSnippet[] | undefined {
  const record = objectValue(value);
  if (!record || typeof record.id !== "string" || !ID_PATTERN.test(record.id)) return undefined;
  const title = lineText(record.title, COMMAND_SNIPPET_TITLE_MAX_RUNES);
  const targets = subset(record.targets, TARGETS);
  const platforms = subset(record.platforms, PLATFORMS);
  if (!title || !targets || !platforms) return undefined;
  if (!Array.isArray(record.commands) || record.commands.length === 0) return undefined;
  const library: LibraryContext = {
    pluginId: plugin.pluginId,
    pluginName: sanitizeText(plugin.pluginName, COMMAND_SNIPPET_TITLE_MAX_RUNES) || plugin.pluginId,
    libraryId: record.id,
    libraryTitle: title,
    libraryTitleLocalized: localized(record.titleLocalized, COMMAND_SNIPPET_TITLE_MAX_RUNES),
    targets,
    platforms,
  };
  const commands: CommandSnippet[] = [];
  const seen = new Set<string>();
  for (const raw of record.commands.slice(0, COMMAND_SNIPPET_MAX_PER_LIBRARY)) {
    const command = parseCommand(raw, library);
    if (!command || seen.has(command.id)) continue;
    seen.add(command.id);
    commands.push(command);
  }
  return commands;
}

/**
 * Every snippet from plugins active in the workspace: deduplicated, at most
 * the per-plugin budget, in plugin id then library order.
 */
export function resolveCommandSnippets(contributions: readonly PointContribution[]): CommandSnippet[] {
  const result: CommandSnippet[] = [];
  const sorted = [...contributions].sort((left, right) => left.pluginId.localeCompare(right.pluginId));
  const seenPlugins = new Set<string>();
  for (const contribution of sorted) {
    if (!contribution.activeInWorkspace || !PLUGIN_ID_PATTERN.test(contribution.pluginId)) continue;
    if (seenPlugins.has(contribution.pluginId)) continue;
    seenPlugins.add(contribution.pluginId);
    const entries = Array.isArray(contribution.point?.entries) ? contribution.point.entries : [];
    const libraries = new Set<string>();
    let count = 0;
    for (const entry of entries.slice(0, COMMAND_SNIPPET_MAX_LIBRARIES)) {
      const commands = parseCommandSnippetLibrary(entry, contribution);
      if (!commands?.length || libraries.has(commands[0]!.libraryId)) continue;
      libraries.add(commands[0]!.libraryId);
      for (const command of commands) {
        if (count >= COMMAND_SNIPPET_MAX_PER_PLUGIN) break;
        result.push(command);
        count += 1;
      }
    }
  }
  return result;
}

/** Snippets offered in a terminal: target matches; the platform list applies to local terminals only. */
export function commandSnippetsForTerminal(
  snippets: readonly CommandSnippet[],
  target: CommandSnippetTarget,
  platform: TerminalProfilePlatform,
): CommandSnippet[] {
  return snippets.filter(
    (snippet) => snippet.targets.includes(target) && (target !== "local" || snippet.platforms.includes(platform)),
  );
}

export function commandSnippetTitle(
  snippet: { title: string; titleLocalized?: Record<string, string> },
  locale: string | readonly string[],
) {
  return resolvePluginTitle({ title: snippet.title, titleLocalized: snippet.titleLocalized }, locale);
}

export function commandSnippetLibraryTitle(snippet: CommandSnippet, locale: string | readonly string[]) {
  return resolvePluginTitle({ title: snippet.libraryTitle, titleLocalized: snippet.libraryTitleLocalized }, locale);
}

export function commandSnippetDescription(snippet: CommandSnippet, locale: string | readonly string[]) {
  if (!snippet.description && !snippet.descriptionLocalized) return "";
  return resolvePluginTitle({ title: snippet.description, titleLocalized: snippet.descriptionLocalized }, locale);
}

/** Case-insensitive search over title, description, tags, command, library and plugin names. */
export function filterCommandSnippets(
  snippets: readonly CommandSnippet[],
  query: string,
  locale: string | readonly string[],
): CommandSnippet[] {
  const terms = query.toLowerCase().split(/\s+/).filter(Boolean);
  if (!terms.length) return [...snippets];
  return snippets.filter((snippet) => {
    const haystack = [
      commandSnippetTitle(snippet, locale),
      commandSnippetDescription(snippet, locale),
      commandSnippetLibraryTitle(snippet, locale),
      snippet.pluginName,
      snippet.command,
      ...snippet.tags,
    ]
      .join("\n")
      .toLowerCase();
    return terms.every((term) => haystack.includes(term));
  });
}

/** The shell dialect used for `quote: "posix"` variables in a terminal. */
export function commandSnippetShell(target: CommandSnippetTarget, platform: TerminalProfilePlatform): CommandSnippetShell {
  return target === "local" && platform === "windows" ? "powershell" : "posix";
}

/** Validates one user-entered value: single line, no control/format characters, bounded. */
export function commandSnippetValueIssue(value: string): "control" | "too-long" | undefined {
  if (UNSAFE_VALUE.test(value)) return "control";
  if (runes(value) > COMMAND_SNIPPET_VARIABLE_VALUE_MAX_RUNES) return "too-long";
  return undefined;
}

function quoteValue(value: string, shell: CommandSnippetShell): string | undefined {
  if (value && POSIX_SAFE_VALUE.test(value)) return value;
  if (shell === "posix") return `'${value.replace(/'/g, `'\\''`)}'`;
  if (shell === "powershell") return `'${value.replace(/'/g, "''")}'`;
  return undefined;
}

/**
 * Replaces declared `{{name}}` tokens with the user's values (or defaults).
 * Other `{{...}}` text stays literal. `quote: "posix"` variables are quoted
 * for the shell; cmd.exe has no safe quoting, so those values are inserted as
 * typed and listed in `unquoted`.
 */
export function expandCommandSnippet(
  snippet: Pick<CommandSnippet, "command" | "variables">,
  values: Readonly<Record<string, string>>,
  options: { shell: CommandSnippetShell },
): CommandSnippetExpansion {
  const unquoted: string[] = [];
  let invalid: string | undefined;
  const byName = new Map(snippet.variables.map((variable) => [variable.name, variable]));
  const text = snippet.command.replace(TOKEN_PATTERN, (token, name: string) => {
    const variable = byName.get(name);
    if (!variable) return token;
    const raw = Object.prototype.hasOwnProperty.call(values, name) ? values[name] ?? "" : variable.default;
    if (commandSnippetValueIssue(raw)) {
      invalid ??= name;
      return token;
    }
    if (variable.quote !== "posix") return raw;
    const quoted = quoteValue(raw, options.shell);
    if (quoted === undefined) {
      if (!unquoted.includes(name)) unquoted.push(name);
      return raw;
    }
    return quoted;
  });
  return invalid ? { text, unquoted, invalid } : { text, unquoted };
}

/**
 * How expanded text may reach the terminal. Trailing newlines are always
 * stripped and "\r" never appears. One line is inserted as typed input;
 * several lines need bracketed paste (so the shell does not run each line)
 * and then the paste confirmation; without it insertion is blocked (copy only).
 */
export function commandSnippetInsertionPlan(text: string, bracketedPasteMode: boolean): CommandSnippetInsertion {
  const clean = text.replace(/\r\n?/g, "\n").replace(/\r/g, "").replace(/\n+$/g, "");
  if (!clean.includes("\n")) return { mode: "single", text: clean };
  return bracketedPasteMode ? { mode: "multiline", text: clean } : { mode: "blocked", text: clean };
}

export interface CommandSnippetPreviewEntry {
  library: string;
  title: string;
  /** Library targets. */
  targets: CommandSnippetTarget[];
  commands: number;
  /** Declared file (`{"path"}` libraries are listed by path). */
  path?: string;
}

/** Install preview (manifest `contributes.commandSnippets`): library titles, targets and command counts. */
export function commandSnippetPreviewSummary(contributes: unknown): CommandSnippetPreviewEntry[] {
  const entries = objectValue(contributes)?.commandSnippets;
  if (!Array.isArray(entries)) return [];
  return entries.slice(0, COMMAND_SNIPPET_MAX_LIBRARIES).flatMap((entry): CommandSnippetPreviewEntry[] => {
    const record = objectValue(entry);
    if (!record) return [];
    if (typeof record.path === "string" && Object.keys(record).length === 1) {
      const path = sanitizeText(record.path, 256);
      return path ? [{ library: path, title: path, targets: [...TARGETS], commands: 0, path }] : [];
    }
    const commands = parseCommandSnippetLibrary(record, { pluginId: "plugin", pluginName: "plugin" });
    if (!commands) return [];
    const targets = subset(record.targets, TARGETS) ?? [];
    return [
      {
        library: String(record.id),
        title: typeof record.title === "string" ? sanitizeText(record.title, COMMAND_SNIPPET_TITLE_MAX_RUNES) : String(record.id),
        targets,
        commands: commands.length,
      },
    ];
  });
}

/**
 * Frontend stage of the `snippets` contribution point. The Go host already
 * validated each snippet file structurally (strict JSON, bounds, prefixes
 * without whitespace; backend/plugin/contrib_snippets.go) and sends
 * normalized entries `{language, path, snippets: [{name, prefixes, body,
 * description?}]}`. This module re-checks that shape (entries are untrusted),
 * validates the snippet body syntax against the subset MinCode supports, and
 * groups the accepted snippets per Monaco language. Pure: no Monaco, Vue or
 * IPC; the Monaco wiring lives in services/monaco-snippets.ts.
 *
 * Supported body syntax (VS Code / TextMate subset):
 * - tabstops `$1`, `${1}`, placeholders `${1:text}` (nested), choices
 *   `${1|a,b|}`, the final tabstop `$0`;
 * - allowlisted variables `$NAME`, `${NAME}`, `${NAME:default}`;
 * - `\$`, `\}` and `\\` escapes; any other `$` is literal text.
 * Rejected: regex transforms (`${1/…/…/}`, `${NAME/…/…/}`), CLIPBOARD and
 * every variable outside the allowlist (paths, workspace names).
 */
import type { PointContribution } from "@/domain/plugins/contributions";

export const SNIPPET_CONTRIBUTION_POINT = "snippets";
/** Completion items offered per request (Monaco filters and ranks them further). */
export const SNIPPET_MAX_SUGGESTIONS = 200;
export const SNIPPET_LANGUAGE_PATTERN = /^[a-z][a-z0-9+#._-]{0,31}$/;

const MAX_NESTING = 16;
const MAX_PREVIEW_LENGTH = 2000;
const MAX_BODY_BYTES = 16 * 1024;
const MAX_PREFIX_LENGTH = 64;
const MAX_PREFIXES = 8;
const MAX_SNIPPETS_PER_PLUGIN = 4000;

/** Variables Monaco resolves locally without exposing paths, workspace data or the clipboard. */
export const SNIPPET_VARIABLES: ReadonlySet<string> = new Set([
  "TM_SELECTED_TEXT",
  "TM_CURRENT_LINE",
  "TM_CURRENT_WORD",
  "TM_LINE_INDEX",
  "TM_LINE_NUMBER",
  "TM_FILENAME",
  "TM_FILENAME_BASE",
  "CURRENT_YEAR",
  "CURRENT_YEAR_SHORT",
  "CURRENT_MONTH",
  "CURRENT_MONTH_NAME",
  "CURRENT_MONTH_NAME_SHORT",
  "CURRENT_DATE",
  "CURRENT_DAY_NAME",
  "CURRENT_DAY_NAME_SHORT",
  "CURRENT_HOUR",
  "CURRENT_MINUTE",
  "CURRENT_SECOND",
  "CURRENT_SECONDS_UNIX",
  "CURRENT_TIMEZONE_OFFSET",
  "UUID",
  "RANDOM",
  "RANDOM_HEX",
  "BLOCK_COMMENT_START",
  "BLOCK_COMMENT_END",
  "LINE_COMMENT",
]);

export class SnippetSyntaxError extends Error {
  readonly offset: number;

  constructor(message: string, offset: number) {
    super(`${message} (at offset ${offset})`);
    this.name = "SnippetSyntaxError";
    this.offset = offset;
  }
}

function isDigit(char: string | undefined) {
  return char !== undefined && char >= "0" && char <= "9";
}

function isNameStart(char: string | undefined) {
  return char !== undefined && /[_A-Za-z]/.test(char);
}

function isNameChar(char: string | undefined) {
  return char !== undefined && /[_A-Za-z0-9]/.test(char);
}

/**
 * Parses a snippet body. Returns a plain-text preview (placeholders show
 * their text, choices their first option, variables their default or name);
 * throws SnippetSyntaxError for unsupported or malformed syntax.
 */
export function parseSnippetBody(body: string): string {
  let position = 0;
  const read = (pattern: (char: string | undefined) => boolean) => {
    const start = position;
    while (pattern(body[position])) position += 1;
    return body.slice(start, position);
  };
  const expect = (char: string, what: string) => {
    if (body[position] !== char) throw new SnippetSyntaxError(`expected "${char}" to close ${what}`, position);
    position += 1;
  };
  const variable = (name: string, at: number) => {
    if (name === "CLIPBOARD") throw new SnippetSyntaxError("the CLIPBOARD variable is not allowed", at);
    if (!SNIPPET_VARIABLES.has(name)) throw new SnippetSyntaxError(`unsupported variable ${name}`, at);
  };
  const choice = (at: number) => {
    const options: string[] = [];
    let current = "";
    for (;;) {
      const char = body[position];
      if (char === undefined) throw new SnippetSyntaxError("unterminated choice", at);
      if (char === "\\" && [",", "|", "\\", "$", "}"].includes(body[position + 1] ?? "")) {
        current += body[position + 1];
        position += 2;
      } else if (char === "," || char === "|") {
        options.push(current);
        current = "";
        position += 1;
        if (char === "|") break;
      } else {
        current += char;
        position += 1;
      }
    }
    expect("}", "the choice");
    if (options.every((option) => option === "")) throw new SnippetSyntaxError("a choice needs at least one option", at);
    return options[0] ?? "";
  };
  // Parses text until the end (depth 0) or an unescaped "}" (nested placeholder).
  const text = (depth: number): string => {
    if (depth > MAX_NESTING) throw new SnippetSyntaxError(`placeholders nest deeper than ${MAX_NESTING}`, position);
    let preview = "";
    while (position < body.length) {
      const char = body[position];
      if (char === "\\" && ["$", "}", "\\"].includes(body[position + 1] ?? "")) {
        preview += body[position + 1];
        position += 2;
        continue;
      }
      if (char === "}" && depth > 0) return preview;
      if (char !== "$") {
        preview += char;
        position += 1;
        continue;
      }
      const at = position;
      const next = body[position + 1];
      if (isDigit(next)) {
        position += 1;
        read(isDigit);
        continue;
      }
      if (isNameStart(next)) {
        position += 1;
        const name = read(isNameChar);
        variable(name, at);
        preview += name;
        continue;
      }
      if (next !== "{") {
        preview += "$";
        position += 1;
        continue;
      }
      position += 2;
      if (isDigit(body[position])) {
        read(isDigit);
        const marker = body[position];
        if (marker === "}") {
          position += 1;
        } else if (marker === ":") {
          position += 1;
          preview += text(depth + 1);
          expect("}", "the placeholder");
        } else if (marker === "|") {
          position += 1;
          preview += choice(at);
        } else if (marker === "/") {
          throw new SnippetSyntaxError("regex transforms are not allowed", at);
        } else {
          throw new SnippetSyntaxError("malformed tabstop", at);
        }
        continue;
      }
      if (isNameStart(body[position])) {
        const name = read(isNameChar);
        variable(name, at);
        const marker = body[position];
        if (marker === "}") {
          position += 1;
          preview += name;
        } else if (marker === ":") {
          position += 1;
          preview += text(depth + 1);
          expect("}", "the variable default");
        } else if (marker === "/") {
          throw new SnippetSyntaxError("regex transforms are not allowed", at);
        } else {
          throw new SnippetSyntaxError("malformed variable", at);
        }
        continue;
      }
      throw new SnippetSyntaxError('"${" must be followed by a tabstop number or a variable name', at);
    }
    if (depth > 0) throw new SnippetSyntaxError("unterminated placeholder", position);
    return preview;
  };
  const preview = text(0);
  return preview.length > MAX_PREVIEW_LENGTH ? `${preview.slice(0, MAX_PREVIEW_LENGTH)}…` : preview;
}

/** Returns an error message for an unsupported body, or undefined when it is valid. */
export function validateSnippetBody(body: string): string | undefined {
  try {
    parseSnippetBody(body);
    return undefined;
  } catch (error) {
    return error instanceof Error ? error.message : String(error);
  }
}

/** One completion candidate: a snippet prefix of one plugin for one language. */
export interface SnippetItem {
  language: string;
  prefix: string;
  name: string;
  body: string;
  description?: string;
  /** Plain-text preview of the body (never rendered as HTML or Markdown). */
  preview: string;
  pluginId: string;
  pluginName: string;
  /** Index into the plugin's contributes.snippets. */
  entry: number;
}

export interface SnippetPluginIssue {
  pluginId: string;
  /** Index into contributes.snippets, or -1 for issues across entries. */
  entry: number;
  /** `language:path` of the snippet file, when known. */
  file?: string;
  snippet?: string;
  message: string;
  /** `host`: rejected by the Go manifest validator; `frontend`: rejected here or by the Monaco stage. */
  stage: "host" | "frontend";
}

export interface SnippetRegistry {
  /** Accepted snippets per Monaco language, in plugin and file order. */
  byLanguage: Map<string, SnippetItem[]>;
  issues: SnippetPluginIssue[];
}

function objectValue(value: unknown): Record<string, unknown> | undefined {
  return typeof value === "object" && value !== null && !Array.isArray(value) ? (value as Record<string, unknown>) : undefined;
}

function validPrefix(value: unknown): value is string {
  return typeof value === "string" && value.length > 0 && [...value].length <= MAX_PREFIX_LENGTH && !/[\s\p{Cc}]/u.test(value);
}

/**
 * Validates every contributed snippet file and groups the accepted snippets
 * per language. A malformed entry skips its file; an unsupported body skips
 * that snippet. Every skip is reported, never thrown.
 */
export function buildSnippetRegistry(contributions: readonly PointContribution[]): SnippetRegistry {
  const byLanguage = new Map<string, SnippetItem[]>();
  const issues: SnippetPluginIssue[] = [];
  for (const contribution of contributions) {
    for (const issue of contribution.point.issues) {
      issues.push({
        pluginId: contribution.pluginId,
        stage: "host",
        entry: issue.entry,
        message: issue.message,
        ...(issue.id ? { file: issue.id } : {}),
      });
    }
    let total = 0;
    contribution.point.entries.forEach((value, entry) => {
      const record = objectValue(value);
      const language = typeof record?.language === "string" ? record.language : "";
      const path = typeof record?.path === "string" ? record.path : "";
      const file = `${language}:${path}`;
      const report = (message: string, snippet?: string) =>
        issues.push({ pluginId: contribution.pluginId, stage: "frontend", entry, file, message, ...(snippet ? { snippet } : {}) });
      if (!record || !SNIPPET_LANGUAGE_PATTERN.test(language) || !path || !Array.isArray(record.snippets)) {
        report("malformed snippet file entry");
        return;
      }
      const accepted: SnippetItem[] = [];
      for (const raw of record.snippets) {
        const snippet = objectValue(raw);
        const name = typeof snippet?.name === "string" ? snippet.name.trim() : "";
        const prefixes = Array.isArray(snippet?.prefixes) ? snippet.prefixes : [];
        const body = typeof snippet?.body === "string" ? snippet.body : "";
        const description = typeof snippet?.description === "string" ? snippet.description : "";
        if (!name || prefixes.length === 0 || prefixes.length > MAX_PREFIXES || !prefixes.every(validPrefix)
          || !body.trim() || new TextEncoder().encode(body).length > MAX_BODY_BYTES) {
          report("malformed snippet", name || undefined);
          continue;
        }
        if (total >= MAX_SNIPPETS_PER_PLUGIN) {
          report(`plugin snippets exceed ${MAX_SNIPPETS_PER_PLUGIN} in total`, name);
          break;
        }
        let preview: string;
        try {
          preview = parseSnippetBody(body);
        } catch (error) {
          report(`unsupported snippet body: ${error instanceof Error ? error.message : String(error)}`, name);
          continue;
        }
        total += 1;
        for (const prefix of new Set(prefixes as string[])) {
          accepted.push({
            language,
            prefix,
            name,
            body,
            ...(description ? { description } : {}),
            preview,
            pluginId: contribution.pluginId,
            pluginName: contribution.pluginName || contribution.pluginId,
            entry,
          });
        }
      }
      if (accepted.length === 0) return;
      const list = byLanguage.get(language) ?? [];
      list.push(...accepted);
      byLanguage.set(language, list);
    });
  }
  return { byLanguage, issues };
}

/**
 * Chooses the candidates for one completion request. `typed` is the
 * non-whitespace text before the cursor. Each result carries how many
 * trailing characters of `typed` it replaces (the longest suffix of `typed`
 * that starts at a word boundary and starts the prefix, case-insensitively).
 * With nothing typed every
 * snippet is a candidate; otherwise only prefixes that match. At most
 * `limit` results, longer matches first, then registration order.
 */
export function selectSnippetSuggestions(
  items: readonly SnippetItem[],
  typed: string,
  limit = SNIPPET_MAX_SUGGESTIONS,
): Array<{ item: SnippetItem; replace: number }> {
  const tail = typed.toLowerCase();
  const word = (char: string | undefined) => char !== undefined && /[\p{L}\p{N}_]/u.test(char);
  // A replaced suffix starts at a word boundary, so "foo" never completes "one".
  const boundary = (start: number) => start === 0 || !word(tail[start - 1]) || !word(tail[start]);
  const results: Array<{ item: SnippetItem; replace: number; order: number }> = [];
  items.forEach((item, order) => {
    const prefix = item.prefix.toLowerCase();
    let replace = 0;
    for (let length = Math.min(tail.length, prefix.length, MAX_PREFIX_LENGTH); length > 0; length -= 1) {
      const start = tail.length - length;
      if (boundary(start) && prefix.startsWith(tail.slice(start))) {
        replace = length;
        break;
      }
    }
    if (tail.length === 0 || replace > 0) results.push({ item, replace, order });
  });
  results.sort((left, right) => right.replace - left.replace || left.order - right.order);
  return results.slice(0, Math.max(0, limit)).map(({ item, replace }) => ({ item, replace }));
}

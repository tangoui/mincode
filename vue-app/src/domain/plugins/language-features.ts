/**
 * Frontend stage of the `languageFeatures` contribution point: re-validation
 * of the providers the Go host accepted
 * (backend/plugin/contrib_language_features.go), the request window
 * sent to `plugin_host_language_feature` and the re-validation of its hover /
 * completion / code action results (language_feature_invoke.go; the
 * definition, references, codeLens, inlayHints and documentSymbol results are
 * re-validated in language-feature-results.ts). Results are plain text: a
 * hover is rendered as escaped text, a completion inserts its text
 * literally. Code action ranges are re-checked against the current model and
 * a code action only carries edits of the current document.
 * No DOM, Vue, IPC or Monaco here; the editor wiring lives in
 * services/monaco-language-features.ts.
 */
import type { PointContribution } from "@/domain/plugins/contributions";
import {
  PROVIDER_ACTION_ID_PATTERN,
  PROVIDER_ENTRY_ID_PATTERN,
  boundedLine,
  boundedMultiline,
  collectProviderEntries,
  isRecord,
  localizedTitles,
  providerPreviewTitles,
  responseCount,
  utf8Bytes,
  type ProviderPointIssue,
} from "@/domain/plugins/provider-points";
import { resolvePluginTitle } from "@/domain/plugins/title";

export const LANGUAGE_FEATURES_CONTRIBUTION_POINT = "languageFeatures";

/** Mirrors contribution-points.json languageFeatures limits (the host enforces them first). */
export const LANGUAGE_FEATURE_LIMITS = {
  maxEntries: 8,
  titleMaxRunes: 64,
  localesMax: 16,
  languagesMax: 16,
  triggerCharactersMax: 8,
  textMaxBytes: 65536,
  payloadMaxBytes: 81920,
  hoverContentsMax: 4,
  hoverContentMaxRunes: 4096,
  completionItemsMax: 100,
  completionLabelMaxRunes: 128,
  completionInsertTextMaxRunes: 4096,
  completionDetailMaxRunes: 256,
  completionDocumentationMaxRunes: 2048,
  codeActionItemsMax: 20,
  codeActionTitleMaxRunes: 128,
  codeActionEditsMax: 50,
  codeActionEditTextMaxBytes: 65536,
  codeActionDiagnosticsMax: 20,
  codeActionDiagnosticMessageMaxRunes: 1024,
  timeoutMs: 2000,
  definitionLocationsMax: 20,
  referencesLocationsMax: 500,
  referencesTimeoutMs: 5000,
  codeLensesMax: 100,
  codeLensTitleMaxRunes: 64,
  codeLensArgsMaxBytes: 1024,
  codeLensArgsMaxProperties: 8,
  inlayHintsMax: 300,
  inlayHintLabelMaxRunes: 64,
  documentSymbolsMax: 500,
  documentSymbolDepthMax: 8,
  symbolNameMaxRunes: 128,
  symbolDetailMaxRunes: 128,
  locationPathMaxBytes: 1024,
  columnMax: 100000,
  passiveConcurrentPerPlugin: 2,
  passiveDebounceMs: 300,
  passiveFailuresMax: 3,
  passiveSuspendMs: 60000,
  lineMax: 10_000_000,
} as const;

/**
 * Mirrors contribution-points.json languageFeatures lists.features (order
 * fixed). hover and completion need host 0.1.0, the rest host 0.3.0
 * (valueSinceHost.features).
 */
export const LANGUAGE_FEATURE_KINDS = [
  "hover", "completion", "definition", "references", "codeLens", "inlayHints", "documentSymbol", "codeAction",
] as const;
export type LanguageFeatureKind = (typeof LANGUAGE_FEATURE_KINDS)[number];
const FEATURES: ReadonlySet<string> = new Set(LANGUAGE_FEATURE_KINDS);

/** Features the editor requests without a user gesture (debounced, own slots, suspended after failures). */
export const PASSIVE_LANGUAGE_FEATURES: ReadonlySet<LanguageFeatureKind> = new Set<LanguageFeatureKind>([
  "codeLens", "inlayHints", "documentSymbol",
]);

/** Mirrors lists.symbolKinds; the index is the Monaco SymbolKind value (0-25). */
export const SYMBOL_KINDS: readonly string[] = [
  "file", "module", "namespace", "package", "class", "method", "property", "field", "constructor", "enum",
  "interface", "function", "variable", "constant", "string", "number", "boolean", "array", "object", "key",
  "null", "enumMember", "struct", "event", "operator", "typeParameter",
];

/** Mirrors lists.inlayHintKinds: type is Monaco InlayHintKind 1, parameter is 2. */
export const INLAY_HINT_KINDS = ["type", "parameter"] as const;

export const COMPLETION_KINDS = [
  "text", "method", "function", "constructor", "field", "variable", "class", "interface", "module",
  "property", "unit", "value", "enum", "keyword", "snippet", "color", "file", "reference", "folder",
  "constant", "struct", "event", "operator", "type",
] as const;
export type CompletionKind = (typeof COMPLETION_KINDS)[number];
const KINDS: ReadonlySet<string> = new Set(COMPLETION_KINDS);

export const CODE_ACTION_KINDS = [
  "quickfix", "refactor", "refactor.extract", "refactor.inline", "refactor.rewrite", "source", "source.fixAll", "source.organizeImports",
] as const;
export type CodeActionKind = (typeof CODE_ACTION_KINDS)[number];
const ACTION_KINDS: ReadonlySet<string> = new Set(CODE_ACTION_KINDS);

const DIAGNOSTIC_SEVERITIES = ["error", "warning", "info", "hint"] as const;
export type DiagnosticSeverity = (typeof DIAGNOSTIC_SEVERITIES)[number];
const SEVERITIES: ReadonlySet<string> = new Set(DIAGNOSTIC_SEVERITIES);

const LANGUAGE_ID_PATTERN = /^[a-z][a-z0-9+#._-]{0,31}$/;
const TRIGGER_PATTERN = /^[!-~]$/;

export interface PluginLanguageFeature {
  pluginId: string;
  pluginName: string;
  id: string;
  title: string;
  titleLocalized?: Record<string, string>;
  languages: string[];
  features: LanguageFeatureKind[];
  triggerCharacters?: string[];
}

export type LanguageFeatureIssue = ProviderPointIssue;

export interface LanguageCompletionItem {
  label: string;
  insertText?: string;
  detail?: string;
  documentation?: string;
  kind?: CompletionKind;
}

export interface HoverResponse {
  contents: string[];
  dropped: number;
  truncated: boolean;
}

export interface CompletionResponse {
  items: LanguageCompletionItem[];
  incomplete: boolean;
  dropped: number;
  truncated: boolean;
}

function uniqueStrings(value: unknown, max: number, pattern: RegExp): string[] | false {
  if (!Array.isArray(value) || value.length === 0 || value.length > max) return false;
  const out: string[] = [];
  for (const item of value) {
    if (typeof item !== "string" || !pattern.test(item) || out.includes(item)) return false;
    out.push(item);
  }
  return out;
}

/** A 1-based range of the current document (columns in UTF-16 units, like Monaco). */
export interface PluginRange {
  startLine: number;
  startColumn: number;
  endLine: number;
  endColumn: number;
}

export interface PluginTextEdit {
  range: PluginRange;
  text: string;
}

/** A validated quick fix: sorted, non-overlapping edits of the current document only. */
export interface PluginCodeAction {
  title: string;
  kind?: CodeActionKind;
  isPreferred?: boolean;
  edits: PluginTextEdit[];
}

/** One editor diagnostic sent with a codeAction request. */
export interface LanguageFeatureDiagnostic {
  range: PluginRange;
  message: string;
  severity: DiagnosticSeverity;
  source?: string;
}

/** What result validation needs from the current model. */
export interface LanguageFeatureDocument {
  lineCount: number;
  maxColumn(line: number): number;
}

/** The host's codeAction answer before it is checked against the model. */
export interface CodeActionResponse {
  actions: unknown[];
  dropped: number;
  truncated: boolean;
}

/** Re-validates one host-accepted entry (defense in depth). */
export function validateLanguageFeatureEntry(
  raw: unknown,
): { ok: true; value: Omit<PluginLanguageFeature, "pluginId" | "pluginName"> } | { ok: false; error: string } {
  if (!isRecord(raw)) return { ok: false, error: "entry must be an object" };
  const id = typeof raw.id === "string" ? raw.id : "";
  if (!PROVIDER_ENTRY_ID_PATTERN.test(id)) return { ok: false, error: `language feature provider id "${id}" is invalid` };
  const title = boundedLine(raw.title, LANGUAGE_FEATURE_LIMITS.titleMaxRunes, true);
  if (!title) return { ok: false, error: `language feature provider "${id}" title is invalid` };
  const titleLocalized = localizedTitles(raw.titleLocalized, LANGUAGE_FEATURE_LIMITS.titleMaxRunes, LANGUAGE_FEATURE_LIMITS.localesMax);
  if (titleLocalized === false) return { ok: false, error: `language feature provider "${id}" titleLocalized is invalid` };
  const languages = uniqueStrings(raw.languages, LANGUAGE_FEATURE_LIMITS.languagesMax, LANGUAGE_ID_PATTERN);
  if (!languages) return { ok: false, error: `language feature provider "${id}" languages are invalid` };
  const features = uniqueStrings(raw.features, LANGUAGE_FEATURE_KINDS.length, /^[A-Za-z]+$/);
  if (!features || features.some((feature) => !FEATURES.has(feature))) {
    return { ok: false, error: `language feature provider "${id}" features are invalid` };
  }
  let triggerCharacters: string[] | undefined;
  if (raw.triggerCharacters !== undefined) {
    const triggers = Array.isArray(raw.triggerCharacters) && raw.triggerCharacters.length === 0
      ? []
      : uniqueStrings(raw.triggerCharacters, LANGUAGE_FEATURE_LIMITS.triggerCharactersMax, TRIGGER_PATTERN);
    if (!triggers || (triggers.length && !features.includes("completion"))) {
      return { ok: false, error: `language feature provider "${id}" triggerCharacters are invalid` };
    }
    if (triggers.length) triggerCharacters = triggers;
  }
  if (typeof raw.action !== "string" || !PROVIDER_ACTION_ID_PATTERN.test(raw.action)) {
    return { ok: false, error: `language feature provider "${id}" action is invalid` };
  }
  return {
    ok: true,
    value: {
      id,
      title,
      ...(titleLocalized ? { titleLocalized } : {}),
      languages,
      features: features as LanguageFeatureKind[],
      ...(triggerCharacters ? { triggerCharacters } : {}),
    },
  };
}

/** The providers of the active process plugins, in ascending plugin id order. */
export function collectLanguageFeatures(
  contributions: readonly PointContribution[],
): { providers: PluginLanguageFeature[]; issues: LanguageFeatureIssue[] } {
  const { entries, issues } = collectProviderEntries(
    contributions,
    LANGUAGE_FEATURE_LIMITS.maxEntries,
    validateLanguageFeatureEntry,
    "language feature provider",
  );
  return { providers: entries, issues };
}

export function languageFeatureTitle(provider: PluginLanguageFeature, locale: string | readonly string[]): string {
  return resolvePluginTitle(provider, locale);
}

/** The text window of a request: whole lines, `startLine` is the 1-based number of its first line. */
export interface LanguageFeatureWindow {
  text: string;
  startLine: number;
}

/**
 * The text sent for a request at `line` (1-based): the whole document when
 * it fits in textMaxBytes, else the largest window of whole lines around the
 * cursor line that fits (preferring lines above the cursor 2:1, as context
 * usually precedes the cursor). Undefined when the cursor line alone does not
 * fit.
 */
export function languageFeatureWindow(lines: readonly string[], line: number, maxBytes: number = LANGUAGE_FEATURE_LIMITS.textMaxBytes): LanguageFeatureWindow | undefined {
  if (line < 1 || line > lines.length || line > LANGUAGE_FEATURE_LIMITS.lineMax) return undefined;
  const sizes = lines.map((text) => utf8Bytes(text));
  const total = sizes.reduce((sum, size) => sum + size, 0) + Math.max(0, lines.length - 1);
  if (total <= maxBytes) return { text: lines.join("\n"), startLine: 1 };
  const cursor = line - 1;
  let used = sizes[cursor]!;
  if (used > maxBytes) return undefined;
  let first = cursor;
  let last = cursor;
  for (;;) {
    const canUp = first > 0 && used + sizes[first - 1]! + 1 <= maxBytes;
    const canDown = last < lines.length - 1 && used + sizes[last + 1]! + 1 <= maxBytes;
    if (!canUp && !canDown) break;
    if (canUp && (!canDown || cursor - first < 2 * (last - cursor) + 2)) {
      first -= 1;
      used += sizes[first]! + 1;
    } else {
      last += 1;
      used += sizes[last]! + 1;
    }
  }
  return { text: lines.slice(first, last + 1).join("\n"), startLine: first + 1 };
}

/** An inlayHints request window: the sent text plus the requested line range inside it. */
export interface LanguageFeatureRangeWindow extends LanguageFeatureWindow {
  range: { startLine: number; endLine: number };
}

/**
 * The text sent for an inlayHints request over the visible lines
 * [startLine, endLine] (1-based): the whole document when it fits in
 * textMaxBytes, else whole lines from the visible region outward (alternating
 * above and below) until the limit. When the visible region alone is larger,
 * the window starts at startLine and `range` is cut to the window's last
 * line. Undefined when the lines are out of the document or startLine alone
 * does not fit.
 */
export function languageFeatureRangeWindow(
  lines: readonly string[],
  startLine: number,
  endLine: number,
  maxBytes: number = LANGUAGE_FEATURE_LIMITS.textMaxBytes,
): LanguageFeatureRangeWindow | undefined {
  if (!Number.isInteger(startLine) || !Number.isInteger(endLine)) return undefined;
  if (startLine < 1 || startLine > endLine || endLine > lines.length || endLine > LANGUAGE_FEATURE_LIMITS.lineMax) return undefined;
  // Lines are measured on demand: a large document costs about the window, not the whole text.
  const measured: number[] = [];
  const size = (index: number) => (measured[index] ??= utf8Bytes(lines[index]!));
  let total = -1;
  for (let index = 0; index < lines.length && total <= maxBytes; index += 1) total += size(index) + 1;
  if (total <= maxBytes) return { text: lines.join("\n"), startLine: 1, range: { startLine, endLine } };
  let first = startLine - 1;
  let last = first;
  let used = size(first);
  if (used > maxBytes) return undefined;
  // The visible region, cut where the limit is reached.
  while (last < endLine - 1 && used + size(last + 1) + 1 <= maxBytes) {
    last += 1;
    used += size(last) + 1;
  }
  const range = { startLine, endLine: last + 1 };
  if (last === endLine - 1) {
    for (;;) {
      const canUp = first > 0 && used + size(first - 1) + 1 <= maxBytes;
      const canDown = last < lines.length - 1 && used + size(last + 1) + 1 <= maxBytes;
      if (!canUp && !canDown) break;
      if (canUp && (!canDown || startLine - 1 - first <= last - (endLine - 1))) {
        first -= 1;
        used += size(first) + 1;
      } else {
        last += 1;
        used += size(last) + 1;
      }
    }
  }
  return { text: lines.slice(first, last + 1).join("\n"), startLine: first + 1, range };
}

/** Escapes text for a Markdown hover so plugin text never renders as markup, links or HTML. */
export function escapeHoverMarkdown(value: string): string {
  return value
    .replace(/[\\`*_{}[\]()#+\-.!|<>~&]/g, (char) => `\\${char}`)
    .replace(/\n/g, "  \n");
}

/** Re-validates a hover response: invalid or blank blocks are dropped and counted. */
export function parseHoverResponse(value: unknown): HoverResponse {
  const response: HoverResponse = { contents: [], dropped: 0, truncated: false };
  if (!isRecord(value)) return response;
  response.dropped = responseCount(value.dropped);
  response.truncated = value.truncated === true;
  if (!Array.isArray(value.contents)) return response;
  if (value.contents.length > LANGUAGE_FEATURE_LIMITS.hoverContentsMax) response.truncated = true;
  for (const raw of value.contents.slice(0, LANGUAGE_FEATURE_LIMITS.hoverContentsMax)) {
    const content = boundedMultiline(raw, LANGUAGE_FEATURE_LIMITS.hoverContentMaxRunes);
    if (!content || !content.trim()) {
      response.dropped += 1;
      continue;
    }
    response.contents.push(content);
  }
  return response;
}

function normalizeCompletionItem(raw: unknown): LanguageCompletionItem | undefined {
  if (!isRecord(raw)) return undefined;
  const label = boundedLine(raw.label, LANGUAGE_FEATURE_LIMITS.completionLabelMaxRunes, true);
  const insertText = boundedMultiline(raw.insertText, LANGUAGE_FEATURE_LIMITS.completionInsertTextMaxRunes);
  const detail = boundedLine(raw.detail, LANGUAGE_FEATURE_LIMITS.completionDetailMaxRunes, false);
  const documentation = boundedMultiline(raw.documentation, LANGUAGE_FEATURE_LIMITS.completionDocumentationMaxRunes);
  const kind = raw.kind;
  if (!label || insertText === false || detail === false || documentation === false) return undefined;
  if (kind !== undefined && (typeof kind !== "string" || !KINDS.has(kind))) return undefined;
  return {
    label,
    ...(insertText ? { insertText } : {}),
    ...(detail ? { detail } : {}),
    ...(documentation && documentation.trim() ? { documentation } : {}),
    ...(kind ? { kind: kind as CompletionKind } : {}),
  };
}

/** Re-validates a completion response: invalid items are dropped and counted. */
export function parseCompletionResponse(value: unknown): CompletionResponse {
  const response: CompletionResponse = { items: [], incomplete: false, dropped: 0, truncated: false };
  if (!isRecord(value)) return response;
  response.dropped = responseCount(value.dropped);
  response.truncated = value.truncated === true;
  response.incomplete = value.incomplete === true;
  if (!Array.isArray(value.items)) return response;
  if (value.items.length > LANGUAGE_FEATURE_LIMITS.completionItemsMax) response.truncated = true;
  for (const raw of value.items.slice(0, LANGUAGE_FEATURE_LIMITS.completionItemsMax)) {
    const item = normalizeCompletionItem(raw);
    if (!item) {
      response.dropped += 1;
      continue;
    }
    response.items.push(item);
  }
  return response;
}

function hasOnlyKeys(value: Record<string, unknown>, allowed: readonly string[]): boolean {
  return Object.keys(value).every((key) => allowed.includes(key));
}

function isLineNumber(value: unknown, max: number): value is number {
  return typeof value === "number" && Number.isInteger(value) && value >= 1 && value <= max;
}

/** A range object inside the model (1-based; a column may be one past the line end), or undefined. */
function documentRange(value: unknown, model: LanguageFeatureDocument): PluginRange | undefined {
  if (!isRecord(value) || !hasOnlyKeys(value, ["startLine", "startColumn", "endLine", "endColumn"])) return undefined;
  const { startLine, startColumn, endLine, endColumn } = value;
  if (!isLineNumber(startLine, model.lineCount) || !isLineNumber(endLine, model.lineCount) || startLine > endLine) return undefined;
  if (!isLineNumber(startColumn, model.maxColumn(startLine)) || !isLineNumber(endColumn, model.maxColumn(endLine))) return undefined;
  if (startLine === endLine && startColumn > endColumn) return undefined;
  return { startLine, startColumn, endLine, endColumn };
}

function comparePositions(lineA: number, columnA: number, lineB: number, columnB: number): number {
  return lineA !== lineB ? lineA - lineB : columnA - columnB;
}

/** Splits a host answer into its raw list and counters (validated against the model later). */
function documentResponse(value: unknown, key: "actions"): { items: unknown[]; dropped: number; truncated: boolean } {
  if (!isRecord(value)) return { items: [], dropped: 0, truncated: false };
  return {
    items: Array.isArray(value[key]) ? value[key] : [],
    dropped: responseCount(value.dropped),
    truncated: value.truncated === true,
  };
}

export function parseCodeActionResponse(value: unknown): CodeActionResponse {
  const { items, dropped, truncated } = documentResponse(value, "actions");
  return { actions: items, dropped, truncated };
}

function documentCodeAction(item: unknown, model: LanguageFeatureDocument): PluginCodeAction | undefined {
  if (!isRecord(item) || !hasOnlyKeys(item, ["title", "kind", "isPreferred", "edits"])) return undefined;
  const title = boundedLine(item.title, LANGUAGE_FEATURE_LIMITS.codeActionTitleMaxRunes, true);
  if (!title) return undefined;
  if (item.kind !== undefined && (typeof item.kind !== "string" || !ACTION_KINDS.has(item.kind))) return undefined;
  if (item.isPreferred !== undefined && typeof item.isPreferred !== "boolean") return undefined;
  if (!Array.isArray(item.edits) || item.edits.length === 0 || item.edits.length > LANGUAGE_FEATURE_LIMITS.codeActionEditsMax) return undefined;
  const edits: PluginTextEdit[] = [];
  for (const edit of item.edits) {
    if (!isRecord(edit) || !hasOnlyKeys(edit, ["range", "text"])) return undefined;
    const range = documentRange(edit.range, model);
    const text = typeof edit.text === "string" ? boundedMultiline(edit.text, Number.MAX_SAFE_INTEGER) : false;
    if (!range || typeof text !== "string" || utf8Bytes(text) > LANGUAGE_FEATURE_LIMITS.codeActionEditTextMaxBytes) return undefined;
    edits.push({ range, text });
  }
  edits.sort((a, b) => comparePositions(a.range.startLine, a.range.startColumn, b.range.startLine, b.range.startColumn));
  for (let index = 1; index < edits.length; index += 1) {
    const previous = edits[index - 1]!.range;
    const current = edits[index]!.range;
    if (comparePositions(current.startLine, current.startColumn, previous.endLine, previous.endColumn) < 0) return undefined;
  }
  return {
    title,
    ...(item.kind ? { kind: item.kind as CodeActionKind } : {}),
    ...(item.isPreferred === true ? { isPreferred: true } : {}),
    edits,
  };
}

/**
 * The quick fixes of `raw` (a list) that edit only the current model: the
 * first codeActionItemsMax are considered and an action is dropped whole
 * when it carries any field besides title/kind/isPreferred/edits (so never a
 * URI or a command), has 0 or more than codeActionEditsMax edits, or any edit
 * is outside the model, oversized, unsafe or overlaps another.
 */
export function validateCodeActionResult(raw: unknown, model: LanguageFeatureDocument): PluginCodeAction[] {
  if (!Array.isArray(raw)) return [];
  const actions: PluginCodeAction[] = [];
  for (const item of raw.slice(0, LANGUAGE_FEATURE_LIMITS.codeActionItemsMax)) {
    const action = documentCodeAction(item, model);
    if (action) actions.push(action);
  }
  return actions;
}

// eslint-disable-next-line no-control-regex -- unsafe characters are replaced in diagnostic text
const UNSAFE_DIAGNOSTIC_TEXT = /[\u0000-\u0008\u000b-\u001f\u007f-\u009f\u2028\u2029\u202a-\u202e\u2066-\u2069\u200b-\u200f\u2060\ufeff]/g;

function cutRunes(value: string, max: number): string {
  const runes = [...value];
  return runes.length > max ? runes.slice(0, max).join("") : value;
}

/**
 * The diagnostics sent with a codeAction request: at most
 * codeActionDiagnosticsMax with a range inside the model; unsafe characters
 * are removed, the message is cut to codeActionDiagnosticMessageMaxRunes and
 * the source to one line of titleMaxRunes. Blank messages are skipped.
 */
export function codeActionDiagnostics(
  diagnostics: readonly { range: PluginRange; message: string; severity: string; source?: string }[],
  model: LanguageFeatureDocument,
): LanguageFeatureDiagnostic[] {
  const out: LanguageFeatureDiagnostic[] = [];
  for (const diagnostic of diagnostics) {
    if (out.length >= LANGUAGE_FEATURE_LIMITS.codeActionDiagnosticsMax) break;
    const range = documentRange(diagnostic.range, model);
    if (!range || !SEVERITIES.has(diagnostic.severity) || typeof diagnostic.message !== "string") continue;
    const message = cutRunes(diagnostic.message.replace(/\r\n?/g, "\n").replace(UNSAFE_DIAGNOSTIC_TEXT, "").trim(), LANGUAGE_FEATURE_LIMITS.codeActionDiagnosticMessageMaxRunes);
    if (!message.trim()) continue;
    const source = typeof diagnostic.source === "string"
      ? cutRunes(diagnostic.source.replace(/[\n\t]/g, " ").replace(UNSAFE_DIAGNOSTIC_TEXT, "").trim(), LANGUAGE_FEATURE_LIMITS.titleMaxRunes).trim()
      : "";
    out.push({ range, message, severity: diagnostic.severity as DiagnosticSeverity, ...(source ? { source } : {}) });
  }
  return out;
}

/** Install-preview summary: the titles of the language feature providers a manifest would add. */
export function languageFeaturePreviewSummary(contributes: unknown): string[] {
  return providerPreviewTitles(
    contributes,
    LANGUAGE_FEATURES_CONTRIBUTION_POINT,
    LANGUAGE_FEATURE_LIMITS.maxEntries,
    LANGUAGE_FEATURE_LIMITS.titleMaxRunes,
  );
}

/**
 * Frontend re-validation of the definition, references, codeLens, inlayHints
 * and documentSymbol results of the `languageFeatures` contribution point.
 * The Go host validates and caps them first
 * (backend/plugin/language_feature_results.go); this mirrors the
 * same rules as defense in depth. Also: the document's project root and the
 * location target under it, the passive failure gate (debounced kinds
 * pause after repeated plugin failures, for this session only) and the
 * click-time re-check of a CodeLens command (codeLensCommandAccepts).
 *
 * Kept apart from language-features.ts so the plugin manager page (which
 * statically imports that file for the install preview) does not carry this
 * code. No DOM, Vue, IPC or Monaco here; the editor wiring lives in
 * services/monaco-language-features.ts.
 */
import {
  INLAY_HINT_KINDS,
  LANGUAGE_FEATURE_LIMITS,
  SYMBOL_KINDS,
} from "@/domain/plugins/language-features";
import {
  PROVIDER_ACTION_ID_PATTERN,
  boundedLine,
  hasUnsafeLineText,
  isProjectRelativePath,
  isRecord,
  responseCount,
  scalarArgs,
  utf8Bytes,
  type ScalarArgs,
} from "@/domain/plugins/provider-points";
import type { ContributionErrorCode } from "@/services/plugin-contribution-requests";

/** 1-based range; lines are absolute document lines. */
export interface LanguageRange {
  startLine: number;
  startColumn: number;
  endLine: number;
  endColumn: number;
}

export interface LanguagePosition {
  line: number;
  column: number;
}

/** A definition or reference target; no path is the requesting document. */
export interface LanguageLocation {
  path?: string;
  range: LanguageRange;
  selectionRange?: LanguageRange;
}

export interface LocationsResponse {
  locations: LanguageLocation[];
  dropped: number;
  truncated: boolean;
}

/** A CodeLens; without command it is display-only. args only with a command. */
export interface LanguageCodeLens {
  range: LanguageRange;
  title: string;
  command?: string;
  args?: ScalarArgs;
}

export interface CodeLensResponse {
  lenses: LanguageCodeLens[];
  dropped: number;
  truncated: boolean;
}

export type InlayHintKind = (typeof INLAY_HINT_KINDS)[number];

export interface LanguageInlayHint {
  position: LanguagePosition;
  label: string;
  kind?: InlayHintKind;
  paddingLeft?: true;
  paddingRight?: true;
}

export interface InlayHintsResponse {
  hints: LanguageInlayHint[];
  dropped: number;
  truncated: boolean;
}

/** A document symbol; kind is one of SYMBOL_KINDS, selectionRange defaults to range. */
export interface LanguageSymbol {
  name: string;
  detail?: string;
  kind: string;
  range: LanguageRange;
  selectionRange: LanguageRange;
  children?: LanguageSymbol[];
}

export interface DocumentSymbolResponse {
  symbols: LanguageSymbol[];
  dropped: number;
  truncated: boolean;
}

/**
 * Mirrors contribution-points.json sensitiveFilenames (fileTemplates /
 * customEditors) as used by the host's workspacePathDenied: exact basename,
 * case-insensitive.
 */
export const LOCATION_SENSITIVE_FILENAMES: readonly string[] = [
  ".env", ".env.local", "server.pem", "server.key", "id_rsa", "id_ed25519", "cert.p12", "cert.pfx", "vault.kdbx",
  ".npmrc", ".netrc", ".pgpass", "app.keystore", "app.jks", "credentials", "terraform.tfstate",
];
const VCS_SEGMENTS: readonly string[] = [".git", ".hg", ".svn"];

/** Cc, Cf and U+2028 / U+2029: the whole item is dropped, never stripped (contextRuneRefused). */
const REFUSED_RUNE = /[\p{Cc}\p{Cf}\p{Zl}\p{Zp}]/u;
/** % ? # NUL and look-alike dots and slashes (languageLocationPathCharsOK); \ and : fail isProjectRelativePath. */
const REFUSED_PATH_CHAR = /[%?#\0\u{ff0e}\u{ff61}\u{2024}\u{3002}\u{fe52}\u{ff0f}\u{2215}\u{2044}\u{29f8}\u{ff3c}\u{2216}\u{29f9}\u{fe68}]/u;

const SYMBOL_KIND_SET: ReadonlySet<string> = new Set(SYMBOL_KINDS);
const INLAY_HINT_KIND_SET: ReadonlySet<string> = new Set(INLAY_HINT_KINDS);

function hasOnlyKeys(value: Record<string, unknown>, allowed: readonly string[]): boolean {
  return Object.keys(value).every((key) => allowed.includes(key));
}

/** An optional item field: JSON null is absent, as for the host's pointer fields. */
function optional(value: unknown): unknown {
  return value === null ? undefined : value;
}

/** Single-line result text: the contribution line rules plus no Cc / Cf / U+2028 / U+2029. */
function resultLine(value: unknown, maxRunes: number, required: boolean): string | undefined | false {
  const line = boundedLine(value, maxRunes, required);
  if (line && REFUSED_RUNE.test(line)) return false;
  return line;
}

/**
 * A JSON integer in [1, max], as Go's int decode: strings, booleans,
 * fractions, NaN and infinities fail. (A JSON `1.0` already parses to the
 * number 1 here; the host has rejected such results before they arrive.)
 */
function boundedInt(value: unknown, max: number): value is number {
  return typeof value === "number" && Number.isSafeInteger(value) && value >= 1 && value <= max;
}

function lineValid(value: unknown): value is number {
  return boundedInt(value, LANGUAGE_FEATURE_LIMITS.lineMax);
}

function columnValid(value: unknown): value is number {
  return boundedInt(value, LANGUAGE_FEATURE_LIMITS.columnMax);
}

function parseRange(value: unknown): LanguageRange | undefined {
  if (!isRecord(value) || !hasOnlyKeys(value, ["startLine", "startColumn", "endLine", "endColumn"])) return undefined;
  const { startLine, startColumn, endLine, endColumn } = value;
  if (!lineValid(startLine) || !lineValid(endLine) || !columnValid(startColumn) || !columnValid(endColumn)) return undefined;
  if (endLine < startLine || (endLine === startLine && endColumn < startColumn)) return undefined;
  return { startLine, startColumn, endLine, endColumn };
}

function parsePosition(value: unknown): LanguagePosition | undefined {
  if (!isRecord(value) || !hasOnlyKeys(value, ["line", "column"])) return undefined;
  if (!lineValid(value.line) || !columnValid(value.column)) return undefined;
  return { line: value.line, column: value.column };
}

function rangeContains(outer: LanguageRange, inner: LanguageRange): boolean {
  const notAfter = (line: number, column: number, otherLine: number, otherColumn: number) =>
    line < otherLine || (line === otherLine && column <= otherColumn);
  return notAfter(outer.startLine, outer.startColumn, inner.startLine, inner.startColumn)
    && notAfter(inner.endLine, inner.endColumn, outer.endLine, outer.endColumn);
}

/**
 * Whether a location path may be used: a clean project-relative path of at
 * most locationPathMaxBytes, without URL, shell or look-alike characters, not
 * a VCS internal and not a sensitive file. The host also resolves it on disk
 * under the project root; this is the lexical part.
 */
export function isLocationPathAllowed(value: unknown): value is string {
  if (!isProjectRelativePath(value, LANGUAGE_FEATURE_LIMITS.locationPathMaxBytes)) return false;
  if (REFUSED_RUNE.test(value) || REFUSED_PATH_CHAR.test(value) || value.startsWith("~") || value.startsWith("$")) return false;
  const segments = value.toLowerCase().split("/");
  if (segments.some((segment) => VCS_SEGMENTS.includes(segment))) return false;
  return !LOCATION_SENSITIVE_FILENAMES.includes(segments[segments.length - 1]!);
}

function emptyCounts(value: unknown): { dropped: number; truncated: boolean } {
  return isRecord(value) ? { dropped: responseCount(value.dropped), truncated: value.truncated === true } : { dropped: 0, truncated: false };
}

/** Items of `key` cut to `max` (truncated) and parsed one by one (dropped). */
function parseList<T>(value: unknown, key: string, max: number, parse: (raw: unknown) => T | undefined) {
  const counts = emptyCounts(value);
  const items: T[] = [];
  const list = isRecord(value) ? value[key] : undefined;
  if (!Array.isArray(list)) return { items, ...counts };
  if (list.length > max) counts.truncated = true;
  for (const raw of list.slice(0, max)) {
    const item = parse(raw);
    if (item === undefined) counts.dropped += 1;
    else items.push(item);
  }
  return { items, ...counts };
}

function parseLocation(raw: unknown, allowSelection: boolean): LanguageLocation | undefined {
  if (!isRecord(raw) || !hasOnlyKeys(raw, ["path", "range", "selectionRange"])) return undefined;
  const range = parseRange(raw.range);
  if (!range) return undefined;
  const location: LanguageLocation = { range };
  const path = optional(raw.path);
  if (path !== undefined) {
    if (!isLocationPathAllowed(path)) return undefined;
    location.path = path;
  }
  if (raw.selectionRange !== undefined) {
    if (!allowSelection) return undefined;
    const selection = parseRange(raw.selectionRange);
    if (!selection || !rangeContains(range, selection)) return undefined;
    location.selectionRange = selection;
  }
  return location;
}

/** Re-validates a definition result (≤ definitionLocationsMax; selectionRange inside range). */
export function parseDefinitionResponse(value: unknown): LocationsResponse {
  const { items, dropped, truncated } = parseList(value, "locations", LANGUAGE_FEATURE_LIMITS.definitionLocationsMax, (raw) => parseLocation(raw, true));
  return { locations: items, dropped, truncated };
}

/** Re-validates a references result (≤ referencesLocationsMax; no selectionRange). */
export function parseReferencesResponse(value: unknown): LocationsResponse {
  const { items, dropped, truncated } = parseList(value, "locations", LANGUAGE_FEATURE_LIMITS.referencesLocationsMax, (raw) => parseLocation(raw, false));
  return { locations: items, dropped, truncated };
}

function parseCodeLens(raw: unknown): LanguageCodeLens | undefined {
  if (!isRecord(raw) || !hasOnlyKeys(raw, ["range", "title", "command", "args"])) return undefined;
  const range = parseRange(raw.range);
  const title = resultLine(raw.title, LANGUAGE_FEATURE_LIMITS.codeLensTitleMaxRunes, true);
  if (!range || !title || title.includes("$(")) return undefined;
  const lens: LanguageCodeLens = { range, title };
  const command = optional(raw.command);
  if (command === undefined) return raw.args === undefined ? lens : undefined;
  if (typeof command !== "string" || !PROVIDER_ACTION_ID_PATTERN.test(command)) return undefined;
  lens.command = command;
  const args = scalarArgs(raw.args, LANGUAGE_FEATURE_LIMITS.codeLensArgsMaxProperties, LANGUAGE_FEATURE_LIMITS.codeLensArgsMaxBytes);
  if (args === false) return undefined;
  if (args) lens.args = args;
  return lens;
}

/** Re-validates a CodeLens result (≤ codeLensesMax; plain single-line titles without "$("). */
export function parseCodeLensResponse(value: unknown): CodeLensResponse {
  const { items, dropped, truncated } = parseList(value, "lenses", LANGUAGE_FEATURE_LIMITS.codeLensesMax, parseCodeLens);
  return { lenses: items, dropped, truncated };
}

function parseInlayHint(raw: unknown, range?: { startLine: number; endLine: number }): LanguageInlayHint | undefined {
  if (!isRecord(raw) || !hasOnlyKeys(raw, ["position", "label", "kind", "paddingLeft", "paddingRight"])) return undefined;
  const position = parsePosition(raw.position);
  const label = resultLine(raw.label, LANGUAGE_FEATURE_LIMITS.inlayHintLabelMaxRunes, true);
  if (!position || !label) return undefined;
  if (range && (position.line < range.startLine || position.line > range.endLine)) return undefined;
  const kind = optional(raw.kind);
  if (kind !== undefined && (typeof kind !== "string" || !INLAY_HINT_KIND_SET.has(kind))) return undefined;
  for (const padding of [optional(raw.paddingLeft), optional(raw.paddingRight)]) {
    if (padding !== undefined && typeof padding !== "boolean") return undefined;
  }
  return {
    position,
    label,
    ...(kind !== undefined ? { kind: kind as InlayHintKind } : {}),
    ...(raw.paddingLeft === true ? { paddingLeft: true as const } : {}),
    ...(raw.paddingRight === true ? { paddingRight: true as const } : {}),
  };
}

/**
 * Re-validates an inlay hints result (≤ inlayHintsMax). With `range` (the
 * request range), hints on other lines are dropped.
 */
export function parseInlayHintsResponse(value: unknown, range?: { startLine: number; endLine: number }): InlayHintsResponse {
  const { items, dropped, truncated } = parseList(value, "hints", LANGUAGE_FEATURE_LIMITS.inlayHintsMax, (raw) => parseInlayHint(raw, range));
  return { hints: items, dropped, truncated };
}

interface SymbolWalk {
  count: number;
  dropped: number;
  truncated: boolean;
}

function parseSymbol(raw: unknown): { symbol: LanguageSymbol; children: unknown[] } | undefined {
  if (!isRecord(raw) || !hasOnlyKeys(raw, ["name", "detail", "kind", "range", "selectionRange", "children"])) return undefined;
  const name = resultLine(raw.name, LANGUAGE_FEATURE_LIMITS.symbolNameMaxRunes, true);
  const detail = resultLine(optional(raw.detail),LANGUAGE_FEATURE_LIMITS.symbolDetailMaxRunes, false);
  const range = parseRange(raw.range);
  if (!name || detail === false || !range || typeof raw.kind !== "string" || !SYMBOL_KIND_SET.has(raw.kind)) return undefined;
  let selectionRange = range;
  if (raw.selectionRange !== undefined) {
    const selection = parseRange(raw.selectionRange);
    if (!selection || !rangeContains(range, selection)) return undefined;
    selectionRange = selection;
  }
  if (raw.children !== undefined && raw.children !== null && !Array.isArray(raw.children)) return undefined;
  return {
    symbol: { name, ...(detail ? { detail } : {}), kind: raw.kind, range, selectionRange },
    children: Array.isArray(raw.children) ? raw.children : [],
  };
}

function parseSymbols(items: readonly unknown[], depth: number, walk: SymbolWalk): LanguageSymbol[] {
  const out: LanguageSymbol[] = [];
  for (const raw of items) {
    if (walk.count >= LANGUAGE_FEATURE_LIMITS.documentSymbolsMax) {
      walk.truncated = true;
      break;
    }
    const parsed = parseSymbol(raw);
    if (!parsed) {
      walk.dropped += 1;
      continue;
    }
    walk.count += 1;
    if (parsed.children.length) {
      if (depth >= LANGUAGE_FEATURE_LIMITS.documentSymbolDepthMax) {
        walk.truncated = true;
      } else {
        const children = parseSymbols(parsed.children, depth + 1, walk);
        if (children.length) parsed.symbol.children = children;
      }
    }
    out.push(parsed.symbol);
  }
  return out;
}

/**
 * Re-validates a documentSymbol result: at most documentSymbolsMax nodes in
 * document order and documentSymbolDepthMax levels (the rest truncated); an
 * invalid node is dropped with its children (dropped + 1).
 */
export function parseDocumentSymbolResponse(value: unknown): DocumentSymbolResponse {
  const counts = emptyCounts(value);
  const list = isRecord(value) ? value.symbols : undefined;
  if (!Array.isArray(list)) return { symbols: [], ...counts };
  const walk: SymbolWalk = { count: 0, dropped: counts.dropped, truncated: counts.truncated };
  const symbols = parseSymbols(list, 1, walk);
  return { symbols, dropped: walk.dropped, truncated: walk.truncated };
}

function normalizeRoot(path: string): string {
  return path.replace(/\\/g, "/").replace(/\/+$/, "");
}

/** Bounds the request projectRoot (languageFeatureProjectRootMaxBytes in Go). */
export const LANGUAGE_FEATURE_PROJECT_ROOT_MAX_BYTES = 4096;

function wellFormed(value: string): boolean {
  try {
    encodeURIComponent(value);
    return true;
  } catch {
    return false;
  }
}

/**
 * Whether the host accepts `root` as a request projectRoot
 * (normalizeLanguageFeatureRequest): at most 4096 UTF-8 bytes, well-formed,
 * free of the characters checkContributionRunes refuses (controls, line
 * separators, bidi and zero-width characters, so also ZWJ emoji) and
 * absolute. A root that fails is left out of the request: the host would
 * reject the whole request, and without it same-document locations still
 * work.
 */
export function isHostProjectRoot(root: string): boolean {
  if (!root || utf8Bytes(root) > LANGUAGE_FEATURE_PROJECT_ROOT_MAX_BYTES || !wellFormed(root) || hasUnsafeLineText(root)) return false;
  return root.startsWith("/") || /^[A-Za-z]:[\\/]/.test(root) || root.startsWith("\\\\");
}

/**
 * The project root a document belongs to: the longest root that contains
 * `path` (drive paths compare case-insensitively), returned as given. The
 * filesystem root and drive roots never match (the host's
 * canonicalTaskProjectRoot refuses them). jsonSchemaTargetForPath uses the
 * same algorithm except drive roots, which it still treats as project roots.
 */
export function projectRootForPath(path: string, roots: readonly string[]): string | undefined {
  const normalized = path.replace(/\\/g, "/");
  const drive = /^[A-Za-z]:\//.test(normalized);
  let best: string | undefined;
  let bestLength = 0;
  for (const root of roots) {
    const prefix = `${normalizeRoot(root)}/`;
    if (prefix === "/" || /^[A-Za-z]:\/$/.test(prefix)) continue;
    const inside = drive ? normalized.toLowerCase().startsWith(prefix.toLowerCase()) : normalized.startsWith(prefix);
    if (inside && normalized.length > prefix.length && prefix.length > bestLength) {
      best = root;
      bestLength = prefix.length;
    }
  }
  return best;
}

/**
 * Where a location points: the requesting document (no path, or the
 * document's own relative path) or an absolute path under the document's
 * project root (forward slashes). Undefined when the path is not allowed or
 * the root is not absolute. Callers build a `file:` URI from absolutePath
 * (never by parsing a URL string).
 */
export function locationTargetPath(
  root: string,
  documentRelativePath: string,
  path: string | undefined,
): { sameDocument: true } | { sameDocument: false; absolutePath: string } | undefined {
  if (path === undefined) return { sameDocument: true };
  if (!isLocationPathAllowed(path)) return undefined;
  if (path === documentRelativePath) return { sameDocument: true };
  const base = normalizeRoot(root);
  if (!base.startsWith("/") && !/^[A-Za-z]:\/[^/]/.test(base)) return undefined;
  return { sameDocument: false, absolutePath: `${base}/${path}` };
}

/** Error codes that count toward a passive suspension (plugin-caused failures). */
const COUNTED_FAILURES: ReadonlySet<string> = new Set(["timeout", "plugin_error", "invalid_result", "too_large"]);

export interface PassiveFailureGate {
  /** False while `key` is suspended. */
  allowed(key: string): boolean;
  /** Records a call outcome: ok clears the run; counted codes add up to a suspension. */
  record(key: string, outcome: "ok" | ContributionErrorCode | "unknown"): void;
  /** Clears every count and suspension (provider list changed). */
  reset(): void;
  /** Keys suspended now. */
  suspensions(): string[];
}

/**
 * Per-key consecutive failure counter for the passive kinds: after
 * `failuresMax` counted failures (timeout, plugin_error, invalid_result,
 * too_large) the key is suspended for `suspendMs`; busy, cancelled and other
 * host-side codes do not count; ok clears the run. Session-only state.
 */
export function createPassiveFailureGate(options: { failuresMax?: number; suspendMs?: number; now?: () => number } = {}): PassiveFailureGate {
  const failuresMax = options.failuresMax ?? LANGUAGE_FEATURE_LIMITS.passiveFailuresMax;
  const suspendMs = options.suspendMs ?? LANGUAGE_FEATURE_LIMITS.passiveSuspendMs;
  const now = options.now ?? Date.now;
  const failures = new Map<string, number>();
  const suspendedUntil = new Map<string, number>();

  function suspended(key: string): boolean {
    const until = suspendedUntil.get(key);
    if (until === undefined) return false;
    if (now() < until) return true;
    suspendedUntil.delete(key);
    return false;
  }

  return {
    allowed: (key) => !suspended(key),
    record(key, outcome) {
      // Outcomes of calls already in flight when the key was suspended neither extend nor end it.
      if (suspended(key)) return;
      if (outcome === "ok") {
        failures.delete(key);
        return;
      }
      if (!COUNTED_FAILURES.has(outcome)) return;
      const count = (failures.get(key) ?? 0) + 1;
      if (count >= failuresMax) {
        failures.delete(key);
        suspendedUntil.set(key, now() + suspendMs);
      } else {
        failures.set(key, count);
      }
    },
    reset() {
      failures.clear();
      suspendedUntil.clear();
    },
    suspensions: () => [...suspendedUntil.keys()].filter((key) => suspended(key)),
  };
}

/** The parts of a registered plugin command a CodeLens click re-checks (plugin-service PluginCommand). */
export interface CodeLensCommandShape {
  action?: string;
  target?: unknown;
  /**
   * The higher of the command's risk and its action's risk (the host's
   * contributionCommandEffectiveRisk); a command without a risk that runs a
   * dangerous action is dangerous.
   */
  effectiveRisk: string;
  inputSchema?: Record<string, unknown>;
}

const INPUT_NUMBER_MAX = 2 ** 53;

function inputValueMatches(property: unknown, value: ScalarArgs[string]): boolean {
  if (!isRecord(property)) return false;
  switch (property.type) {
    case "string":
      if (typeof value !== "string" || hasUnsafeLineText(value)) return false;
      if (typeof property.maxLength === "number" && property.maxLength > 0 && [...value].length > property.maxLength) return false;
      break;
    case "boolean":
      if (typeof value !== "boolean") return false;
      break;
    case "integer":
    case "number":
      if (typeof value !== "number" || !Number.isFinite(value) || Math.abs(value) > INPUT_NUMBER_MAX) return false;
      if (property.type === "integer" && !Number.isInteger(value)) return false;
      break;
    default:
      return false;
  }
  return !Array.isArray(property.enum) || property.enum.includes(value);
}

/**
 * Whether a CodeLens may still run `command` (the current registered command,
 * looked up again at click time): an action command that is not a view
 * target; with `args` it must also have effective risk "read" and an inputSchema the
 * args satisfy (declared properties, types, maxLength, enum, required: the
 * host's validateCommandInputValues) within the CodeLens args limits.
 * Write / dangerous commands run without args through the confirmation path.
 */
export function codeLensCommandAccepts(command: CodeLensCommandShape | undefined, args?: ScalarArgs): boolean {
  if (!command || !command.action || command.target !== undefined) return false;
  if (args === undefined) return true;
  if (command.effectiveRisk !== "read" || !isRecord(command.inputSchema)) return false;
  if (scalarArgs(args, LANGUAGE_FEATURE_LIMITS.codeLensArgsMaxProperties, LANGUAGE_FEATURE_LIMITS.codeLensArgsMaxBytes) === false) return false;
  const properties = isRecord(command.inputSchema.properties) ? command.inputSchema.properties : {};
  for (const [name, value] of Object.entries(args)) {
    if (!Object.prototype.hasOwnProperty.call(properties, name) || !inputValueMatches(properties[name], value)) return false;
  }
  const required = command.inputSchema.required;
  return !Array.isArray(required) || required.every((name) => typeof name === "string" && Object.prototype.hasOwnProperty.call(args, name));
}

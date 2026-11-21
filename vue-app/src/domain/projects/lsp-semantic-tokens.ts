/**
 * LSP semantic tokens → Monaco. Standalone Monaco colors a semantic token by
 * matching its type against the theme's *token rules* and falls back to the
 * default foreground, so raw LSP types (class, parameter, …) that no theme
 * defines would erase the Monarch colors. Every server legend is therefore
 * remapped onto a fixed set of scopes the themes actually color; unknown types
 * are dropped so Monarch keeps them. Modifiers are dropped for the same reason.
 */

/** The legend registered with Monaco; each entry is a theme rule scope. */
export const MONACO_SEMANTIC_SCOPES = [
  "type",
  "function",
  "variable",
  "variable.parameter",
  "constant",
  "keyword",
  "string",
  "number",
  "regexp",
  "comment",
  "operator",
  "annotation",
] as const;

export type MonacoSemanticScope = (typeof MONACO_SEMANTIC_SCOPES)[number];

const LSP_TYPE_SCOPES: Readonly<Record<string, MonacoSemanticScope>> = {
  namespace: "type",
  type: "type",
  class: "type",
  enum: "type",
  interface: "type",
  struct: "type",
  typeParameter: "type",
  function: "function",
  method: "function",
  macro: "function",
  variable: "variable",
  property: "variable",
  event: "variable",
  parameter: "variable.parameter",
  enumMember: "constant",
  keyword: "keyword",
  modifier: "keyword",
  string: "string",
  number: "number",
  regexp: "regexp",
  comment: "comment",
  operator: "operator",
  decorator: "annotation",
};

/** Token types the client advertises in `initialize` (the LSP standard set). */
export const LSP_SEMANTIC_TOKEN_TYPES = Object.keys(LSP_TYPE_SCOPES);

/** Modifiers the client advertises; they are accepted but not rendered. */
export const LSP_SEMANTIC_TOKEN_MODIFIERS = [
  "declaration", "definition", "readonly", "static", "deprecated",
  "abstract", "async", "modification", "documentation", "defaultLibrary",
];

export interface LspSemanticTokensProvider {
  legend: { tokenTypes: string[]; tokenModifiers: string[] };
  full: boolean;
}

/** `semanticTokensProvider` capability; only full-document requests are used. */
export function parseSemanticTokensProvider(raw: unknown): LspSemanticTokensProvider | undefined {
  if (!raw || typeof raw !== "object") return undefined;
  const provider = raw as { legend?: unknown; full?: unknown };
  const legend = provider.legend as { tokenTypes?: unknown; tokenModifiers?: unknown } | undefined;
  if (!legend || !Array.isArray(legend.tokenTypes)) return undefined;
  const full = provider.full === true || (Boolean(provider.full) && typeof provider.full === "object");
  if (!full) return undefined;
  const strings = (value: unknown) => (Array.isArray(value) ? value.map((item) => (typeof item === "string" ? item : "")) : []);
  return { legend: { tokenTypes: strings(legend.tokenTypes), tokenModifiers: strings(legend.tokenModifiers) }, full };
}

/** Server legend index → Monaco scope index, or -1 to drop the token. */
export function semanticScopeMap(tokenTypes: readonly string[]): Int32Array {
  const map = new Int32Array(tokenTypes.length);
  tokenTypes.forEach((type, index) => {
    const scope = LSP_TYPE_SCOPES[type];
    map[index] = scope ? MONACO_SEMANTIC_SCOPES.indexOf(scope) : -1;
  });
  return map;
}

/**
 * Re-encodes LSP relative token data (deltaLine, deltaStart, length, type,
 * modifiers) for the Monaco legend. Dropped tokens fold their deltas into the
 * next kept one; malformed trailing data is ignored.
 */
export function remapSemanticTokens(data: ArrayLike<number>, scopeMap: Int32Array): Uint32Array {
  const count = Math.floor(data.length / 5);
  const out = new Uint32Array(count * 5);
  let written = 0;
  let line = 0;
  let char = 0;
  let lastLine = 0;
  let lastChar = 0;
  for (let index = 0; index < count; index += 1) {
    const base = index * 5;
    const deltaLine = data[base] >>> 0;
    const deltaStart = data[base + 1] >>> 0;
    line += deltaLine;
    char = deltaLine ? deltaStart : char + deltaStart;
    const type = data[base + 3] >>> 0;
    const scope = type < scopeMap.length ? scopeMap[type] : -1;
    const length = data[base + 2] >>> 0;
    if (scope < 0 || length === 0) continue;
    out[written] = line - lastLine;
    out[written + 1] = line === lastLine ? char - lastChar : char;
    out[written + 2] = length;
    out[written + 3] = scope;
    out[written + 4] = 0;
    written += 5;
    lastLine = line;
    lastChar = char;
  }
  return written === out.length ? out : out.slice(0, written);
}

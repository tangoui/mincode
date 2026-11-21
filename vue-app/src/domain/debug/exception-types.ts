/**
 * The "pause only on these exception types" list of a debugger that declares
 * `exceptionTypes` (saved per project and debugger type, sent through the
 * debugger's settingsAction). Mirrors the host rule in
 * backend/plugin/debuggers_launch.go (normalizeDebuggerExceptionTypes),
 * which stays authoritative: each item is trimmed, at most `maxRunes`
 * characters, a dotted identifier; duplicates are dropped (first one kept);
 * at most `max` distinct items; [] is valid.
 */

/** Mirrors contribution-points.json debuggers limits. */
export const EXCEPTION_TYPE_LIMITS = {
  max: 64,
  maxRunes: 256,
};

/** The host's pattern; without the `u` flag `\w` is ASCII, like Go's. */
const EXCEPTION_TYPE_PATTERN = /^[A-Za-z_$][\w$]*(\.[A-Za-z_$][\w$]*)*$/;

export type ExceptionTypesProblem = "format" | "tooLong" | "tooMany";

export type ExceptionTypesResult =
  | { ok: true; types: string[] }
  /** `index` is the item's position in the input, `value` the trimmed item. */
  | { ok: false; reason: ExceptionTypesProblem; index: number; value: string };

function runeCount(value: string): number {
  let count = 0;
  for (const _char of value) count += 1;
  return count;
}

/** The list the host would accept, or the first item it would refuse. */
export function normalizeExceptionTypes(values: readonly string[]): ExceptionTypesResult {
  const types: string[] = [];
  const seen = new Set<string>();
  for (const [index, raw] of values.entries()) {
    const value = raw.trim();
    if (runeCount(value) > EXCEPTION_TYPE_LIMITS.maxRunes) return { ok: false, reason: "tooLong", index, value };
    if (!EXCEPTION_TYPE_PATTERN.test(value)) return { ok: false, reason: "format", index, value };
    if (seen.has(value)) continue;
    if (types.length === EXCEPTION_TYPE_LIMITS.max) return { ok: false, reason: "tooMany", index, value };
    seen.add(value);
    types.push(value);
  }
  return { ok: true, types };
}

/** The items of the list input: one per line or comma-separated; blank entries are not items. */
export function parseExceptionTypesText(text: string): string[] {
  return text
    .split(/[\r\n,]/)
    .map((item) => item.trim())
    .filter((item) => item !== "");
}

/**
 * A stored list from untrusted data: null unless it is an array; non-string
 * and refused items are dropped, duplicates and items beyond the limit too,
 * so a stored list never makes the settings sync fail.
 */
export function storedExceptionTypes(value: unknown): string[] | null {
  if (!Array.isArray(value)) return null;
  const types: string[] = [];
  for (const item of value) {
    if (types.length === EXCEPTION_TYPE_LIMITS.max) break;
    if (typeof item !== "string") continue;
    const result = normalizeExceptionTypes([item]);
    if (result.ok && !types.includes(result.types[0]!)) types.push(result.types[0]!);
  }
  return types;
}

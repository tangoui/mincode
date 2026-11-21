/**
 * Method (DAP function) breakpoints: saved per project and debugger type as
 * `{ name, enabled }` and sent with `setFunctionBreakpoints` (enabled ones,
 * in order) to an adapter that declares supportsFunctionBreakpoints. The
 * adapter defines the syntax of `name`; the debugger plugin describes it in
 * its `functionBreakpoints` texts. Name rules mirror the host's start-request
 * check (backend/plugin/debuggers_launch.go
 * debuggerFunctionBreakpoints), which stays authoritative: trimmed, non-empty,
 * one line without control or bidi characters, at most 1024 characters, at
 * most `functionBreakpointsMax` per start.
 */
import type { DebugBreakpointListItemResult } from "./types.ts";

/** One saved method breakpoint. */
export interface StoredFunctionBreakpoint {
  name: string;
  enabled: boolean;
}

/** The adapter's answer for one sent method breakpoint, with its name. */
export interface DebugFunctionBreakpointResult extends DebugBreakpointListItemResult {
  name: string;
}

export const FUNCTION_BREAKPOINT_LIMITS = {
  max: 256,
  /** Go debuggerBreakpointNameRunes (not a contract limit). */
  maxRunes: 1024,
};

export type FunctionBreakpointNameResult = { ok: true; name: string } | { ok: false; reason: "empty" | "tooLong" | "invalid" };

/** Go isUnsafeContributionRune with neither newline nor tab allowed (also used for data breakpoint ids). */
export function isUnsafeRune(code: number): boolean {
  return (
    code < 0x20 ||
    code === 0x7f ||
    (code >= 0x80 && code <= 0x9f) ||
    code === 0x2028 ||
    code === 0x2029 ||
    (code >= 0x202a && code <= 0x202e) ||
    (code >= 0x2066 && code <= 0x2069) ||
    (code >= 0x200b && code <= 0x200f) ||
    code === 0x2060 ||
    code === 0xfeff ||
    // A lone surrogate is not valid UTF-8 once encoded.
    (code >= 0xd800 && code <= 0xdfff)
  );
}

/** The name the host would accept (trimmed), or why it would refuse it. */
export function normalizeFunctionBreakpointName(raw: string): FunctionBreakpointNameResult {
  const name = raw.trim();
  if (name === "") return { ok: false, reason: "empty" };
  let runes = 0;
  for (const char of name) {
    runes += 1;
    if (isUnsafeRune(char.codePointAt(0)!)) return { ok: false, reason: "invalid" };
  }
  if (runes > FUNCTION_BREAKPOINT_LIMITS.maxRunes) return { ok: false, reason: "tooLong" };
  return { ok: true, name };
}

/**
 * A stored list from untrusted data: null unless it is an array; items
 * without a valid name are dropped, so are repeated names (the first is
 * kept) and items beyond the host's limit. `enabled` defaults to true.
 */
export function storedFunctionBreakpoints(value: unknown): StoredFunctionBreakpoint[] | null {
  if (!Array.isArray(value)) return null;
  const list: StoredFunctionBreakpoint[] = [];
  for (const item of value) {
    if (list.length === FUNCTION_BREAKPOINT_LIMITS.max) break;
    if (typeof item !== "object" || item === null || typeof (item as { name?: unknown }).name !== "string") continue;
    const result = normalizeFunctionBreakpointName((item as { name: string }).name);
    if (!result.ok || list.some((saved) => saved.name === result.name)) continue;
    list.push({ name: result.name, enabled: (item as { enabled?: unknown }).enabled !== false });
  }
  return list;
}

/** The names sent to the adapter: the enabled ones, in order. */
export function enabledFunctionBreakpointNames(list: readonly StoredFunctionBreakpoint[] | undefined): string[] {
  return (list ?? []).filter((item) => item.enabled).map((item) => item.name);
}

/**
 * Exactly one result per sent breakpoint (DAP answers in request order):
 * item i of the adapter's list, unverified when the list is shorter or the
 * item is malformed; extra items are dropped. Without a list (a failed
 * request) each one is unverified with `error` as its message.
 */
export function breakpointListItemResults(list: unknown, sentCount: number, error?: string): DebugBreakpointListItemResult[] {
  const items = Array.isArray(list) ? list : [];
  return Array.from({ length: sentCount }, (_, index): DebugBreakpointListItemResult => {
    const item: unknown = items[index];
    if (typeof item !== "object" || item === null || Array.isArray(item)) {
      return items.length === 0 && error ? { verified: false, message: error } : { verified: false };
    }
    const record = item as Record<string, unknown>;
    const result: DebugBreakpointListItemResult = { verified: record.verified === true };
    if (typeof record.id === "number") result.id = record.id;
    if (!result.verified && typeof record.message === "string" && record.message !== "") result.message = record.message;
    return result;
  });
}

/** What a sent function or data breakpoint is besides the adapter's answer (its name, or its dataId and access type). */
export type BreakpointListIdentity<T extends DebugBreakpointListItemResult> = Omit<T, keyof DebugBreakpointListItemResult>;

/** result without the adapter's answer. */
export function identityOf<T extends DebugBreakpointListItemResult>(result: T): BreakpointListIdentity<T> {
  const { verified: _verified, id: _id, message: _message, ...identity } = result;
  return identity;
}

/**
 * The results paired with what they were sent for (`identities`, in request
 * order): exactly one per sent item, a missing result unverified (see
 * breakpointListItemResults for `list` and `error`).
 */
export function pairedListResults<T extends DebugBreakpointListItemResult>(
  identities: readonly BreakpointListIdentity<T>[],
  list: unknown,
  error?: string,
): T[] {
  return pairResults<T>(identities, breakpointListItemResults(list, identities.length, error));
}

/** identities[i] with results[i]; a missing result is unverified. */
export function pairResults<T extends DebugBreakpointListItemResult>(
  identities: readonly BreakpointListIdentity<T>[],
  results: readonly DebugBreakpointListItemResult[],
): T[] {
  return identities.map((identity, index) => ({ ...identity, ...(results[index] ?? { verified: false }) }) as T);
}

/**
 * A DAP `breakpoint` event for the list item with adapter id `id` (an adapter
 * may verify one only once its class is loaded): its new state replaces the
 * old; `bp` is the event's breakpoint (just `{ id }` for a removed one, which
 * reads as unverified: the saved list still has it). Other items stay.
 */
export function listResultsWithEvent<T extends DebugBreakpointListItemResult>(list: readonly T[], id: number, bp: Record<string, unknown>): T[] {
  return list.map((result) => {
    if (result.id !== id) return result;
    const next = { ...identityOf(result), verified: bp.verified === true, id } as T;
    if (!next.verified && typeof bp.message === "string" && bp.message !== "") next.message = bp.message;
    return next;
  });
}

/** The results paired with the names they were sent for; missing results are unverified. */
export function functionBreakpointResults(names: readonly string[], results: readonly DebugBreakpointListItemResult[]): DebugFunctionBreakpointResult[] {
  return pairResults<DebugFunctionBreakpointResult>(names.map((name) => ({ name })), results);
}

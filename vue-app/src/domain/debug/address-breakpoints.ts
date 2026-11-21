/**
 * Breakpoints on debugger-provided sources: a source the adapter itself
 * addresses (its DAP `source.path` is not a project file but an address such
 * as a library class URI). They are saved per project under the address, sent
 * verbatim with the start request (`addressBreakpoints`) and with
 * `debug_set_breakpoints {address}`; MinCode never parses an address.
 *
 * The address rule mirrors the host's only one (Go debug.ValidDebuggerAddress):
 * `<scheme>://` with a scheme of at least two characters that is not `file`,
 * at most 4096 characters, no control, line/paragraph separator, bidi or
 * zero-width characters. The host refuses a whole start for one bad address,
 * so an address that fails here is never saved nor sent.
 */

export const DEBUGGER_ADDRESS_LIMITS = {
  /** Characters (code points) of one address. */
  maxRunes: 4096,
  /** Addresses with breakpoints per project (and per start request). */
  maxAddresses: 256,
} as const;

const ADDRESS_SCHEME = /^([a-zA-Z][a-zA-Z0-9+.-]+):\/\//;

/** Go internal/textpolicy UnsafeRune(r, false, false). */
export function unsafeCodePoint(code: number): boolean {
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
    code === 0xfeff
  );
}

/** The value is an address the host accepts (see the module comment). */
export function isValidDebuggerAddress(value: unknown): value is string {
  if (typeof value !== "string") return false;
  const match = ADDRESS_SCHEME.exec(value);
  if (!match || match[1]!.toLowerCase() === "file") return false;
  let runes = 0;
  for (const char of value) {
    const code = char.codePointAt(0)!;
    // A lone surrogate is not valid UTF-8 (Go would see U+FFFD instead).
    if (code >= 0xd800 && code <= 0xdfff) return false;
    if (unsafeCodePoint(code) || (runes += 1) > DEBUGGER_ADDRESS_LIMITS.maxRunes) return false;
  }
  return true;
}

/**
 * Where a source breakpoint list lives: an absolute project file path, or a
 * debugger address of a debugger type (the `type` of the session whose
 * source it is: an address is only sent to sessions of that type). Line,
 * condition, hit count, logpoint and enable editing work the same on both.
 */
export type BreakpointAt = string | { address: string; type: string };

/** The debugger address of at, or undefined for a file path. */
export function breakpointAddress(at: BreakpointAt): string | undefined {
  return typeof at === "string" ? undefined : at.address;
}

/** A stable string for at (a file path as it is, an address marked as one with its type). */
export function breakpointAtKey(at: BreakpointAt): string {
  return typeof at === "string" ? `file\u0000${at}` : `address\u0000${at.type}\u0000${at.address}`;
}

/** Whether at's breakpoints go to a session of the debugger type (files: every type). */
export function breakpointAppliesTo(at: BreakpointAt, type: string): boolean {
  return typeof at === "string" || at.type === type;
}

/**
 * What the breakpoints dialog shows for an address: its last segment
 * (query and fragment dropped, percent-decoding when it decodes) without its
 * extension, e.g. `…/java.util/ArrayList.class?=…` → `ArrayList`; the
 * address itself when it has no such segment.
 */
export function addressBreakpointName(address: string): string {
  const withoutScheme = address.replace(ADDRESS_SCHEME, "");
  const segment = withoutScheme.replace(/[?#].*$/s, "").split("/").filter(Boolean).pop() ?? "";
  let decoded = segment;
  try {
    decoded = decodeURIComponent(segment);
  } catch {
    // Keep the raw segment.
  }
  const dot = decoded.lastIndexOf(".");
  const name = dot > 0 ? decoded.slice(0, dot) : decoded;
  return name || address;
}

/**
 * The session that opens a debugger address for the breakpoints dialog, from
 * the project's sessions of the address's debugger type: the dialog's own
 * session (preferredId) while it has not ended, else the newest one that has
 * not ended; undefined when every one ended (an ended session reads no source).
 */
export function addressSourceSession<T extends { id: string; state: string; startedAt: number }>(
  sameType: readonly T[],
  preferredId: string,
): T | undefined {
  const live = sameType.filter((session) => session.state !== "ended");
  return live.find((session) => session.id === preferredId) ?? [...live].sort((a, b) => b.startedAt - a.startedAt)[0];
}

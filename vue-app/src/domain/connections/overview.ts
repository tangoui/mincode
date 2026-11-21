/** Maximum number of entries shown in the "Recently opened" row. */
export const RECENT_LIMIT = 8;

/**
 * Keeps items opened after `clearedAt`, newest first, capped at `limit`.
 * Items without an open time (never opened) are skipped. The input is not mutated.
 */
export function pickRecent<T>(
  items: readonly T[],
  openedAt: (item: T) => number | undefined,
  clearedAt: number,
  limit = RECENT_LIMIT,
): T[] {
  const entries: Array<{ item: T; at: number }> = [];
  for (const item of items) {
    const at = openedAt(item);
    if (typeof at === "number" && Number.isFinite(at) && at > clearedAt) entries.push({ item, at });
  }
  entries.sort((a, b) => b.at - a.at);
  return entries.slice(0, Math.max(0, limit)).map((entry) => entry.item);
}

// Fixed avatar palette (shared with the AI conversation group accents). These
// colors are content identifiers rather than theme colors, so they are literal.
const AVATAR_PALETTE = [
  "#b7791f",
  "#c05621",
  "#2f855a",
  "#0f766e",
  "#0e7490",
  "#4f46e5",
  "#7c3aed",
  "#be185d",
] as const;

/** Picks a stable palette color for a seed (FNV-1a hash over UTF-16 code units). */
export function avatarColor(seed: string): string {
  let hash = 0x811c9dc5;
  for (let index = 0; index < seed.length; index += 1) {
    hash ^= seed.charCodeAt(index);
    hash = Math.imul(hash, 0x01000193);
  }
  return AVATAR_PALETTE[(hash >>> 0) % AVATAR_PALETTE.length];
}

const CJK_CHARACTER = /^[\p{Script=Han}\p{Script=Hiragana}\p{Script=Katakana}\p{Script=Hangul}]$/u;
const ASCII_ALNUM = /[A-Za-z0-9]/g;

/**
 * Avatar text: the first CJK character, the initials of the first two words,
 * or the first two ASCII letters/digits of a single word. Empty names give "?".
 */
export function avatarInitials(name: string): string {
  const trimmed = name.trim();
  if (!trimmed) return "?";

  const first = String.fromCodePoint(trimmed.codePointAt(0)!);
  if (CJK_CHARACTER.test(first)) return first;

  const words = trimmed.split(/\s+/);
  if (words.length >= 2) {
    const a = words[0].match(ASCII_ALNUM)?.[0];
    const b = words[1].match(ASCII_ALNUM)?.[0];
    if (a && b) return `${a}${b}`.toUpperCase();
  }

  const letters = trimmed.match(ASCII_ALNUM);
  if (letters) return letters.slice(0, 2).join("").toUpperCase();
  return first.toUpperCase();
}

export interface SshAddress {
  username: string;
  host: string;
  port?: number;
}

const SSH_USERNAME = /^[^\s@]+$/;
const SSH_HOSTNAME = /^[^\s@/\\[\]:]+$/;
const SSH_IPV6 = /^[0-9A-Za-z:.%_-]*:[0-9A-Za-z:.%_-]*$/;
const SSH_PORT = /^\d{1,5}$/;

/**
 * Parses `user@host` or `user@host:port` (host may be `[ipv6]`).
 * Returns null for anything else, including out-of-range ports.
 */
export function parseSshAddress(input: string): SshAddress | null {
  const value = input.trim();
  const at = value.indexOf("@");
  if (at <= 0 || value.indexOf("@", at + 1) >= 0) return null;

  const username = value.slice(0, at);
  const destination = value.slice(at + 1);
  if (!SSH_USERNAME.test(username) || !destination) return null;

  let host: string;
  let portText: string | undefined;
  if (destination.startsWith("[")) {
    const end = destination.indexOf("]");
    if (end < 0) return null;
    host = destination.slice(1, end);
    if (!SSH_IPV6.test(host)) return null;
    const rest = destination.slice(end + 1);
    if (rest) {
      if (!rest.startsWith(":")) return null;
      portText = rest.slice(1);
    }
  } else {
    const colon = destination.indexOf(":");
    host = colon < 0 ? destination : destination.slice(0, colon);
    if (colon >= 0) portText = destination.slice(colon + 1);
    if (!SSH_HOSTNAME.test(host)) return null;
  }

  if (portText === undefined) return { username, host };
  if (!SSH_PORT.test(portText)) return null;
  const port = Number(portText);
  if (port < 1 || port > 65535) return null;
  return { username, host, port };
}

const MINUTE = 60_000;
const HOUR = 60 * MINUTE;
const DAY = 24 * HOUR;

/**
 * Localized "time since" label: seconds, minutes, hours, then days up to a
 * week; older timestamps show the month and day (plus year when it differs).
 */
export function relativeTime(timestamp: number, now: number, locale: string): string {
  const elapsed = Math.max(0, now - timestamp);
  const relative = new Intl.RelativeTimeFormat(locale, { numeric: "auto" });
  if (elapsed < MINUTE) return relative.format(-Math.floor(elapsed / 1000), "second");
  if (elapsed < HOUR) return relative.format(-Math.floor(elapsed / MINUTE), "minute");
  if (elapsed < DAY) return relative.format(-Math.floor(elapsed / HOUR), "hour");
  const days = Math.floor(elapsed / DAY);
  if (days <= 7) return relative.format(-days, "day");

  const date = new Date(timestamp);
  const sameYear = date.getFullYear() === new Date(now).getFullYear();
  return new Intl.DateTimeFormat(locale, {
    year: sameYear ? undefined : "numeric",
    month: "short",
    day: "numeric",
  }).format(date);
}

/** Moves a keyboard highlight within `[0, length - 1]`; -1 means nothing to highlight. */
export function moveHighlight(current: number, delta: 1 | -1, length: number): number {
  if (length <= 0) return -1;
  const from = current < 0 ? -1 : Math.min(current, length);
  return Math.min(length - 1, Math.max(0, from + delta));
}

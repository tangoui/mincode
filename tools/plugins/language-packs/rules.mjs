// The Go host's per-message rules for a localizations messages asset
// (app/backend/plugin/contrib_localizations.go). The host rejects the
// whole asset when one message breaks them, so pack tools leave such messages
// out and the pack tests re-check them.
import { readFileSync } from "node:fs";

const contract = JSON.parse(readFileSync(new URL("../../../plugins/api/contribution-points.json", import.meta.url), "utf8"));
export const SPEC = contract.points.localizations;
export const LIMITS = SPEC.limits;

const KEY_PATTERN = /^[A-Za-z0-9][A-Za-z0-9_.:@/+-]*$/;

function isHiddenRune(char) {
  const code = char.codePointAt(0);
  return /\p{Cc}/u.test(char)
    || code === 0x2028 || code === 0x2029
    || (code >= 0x202a && code <= 0x202e) || (code >= 0x2066 && code <= 0x2069)
    || (code >= 0x200b && code <= 0x200f) || code === 0x2060 || code === 0xfeff;
}

export function hostAcceptsMessage(key, value) {
  if (key.length > LIMITS.keyMaxLength || !KEY_PATTERN.test(key)) return false;
  if (typeof value !== "string" || !value.trim()) return false;
  const chars = [...value];
  if (chars.length > LIMITS.valueMaxRunes) return false;
  return chars.every((char) => char === "\n" || !isHiddenRune(char));
}

export function sortedMessages(messages) {
  return Object.fromEntries(Object.entries(messages).sort(([a], [b]) => (a < b ? -1 : a > b ? 1 : 0)));
}

export function serializeMessages(messages) {
  return `${JSON.stringify(sortedMessages(messages), null, 2)}\n`;
}

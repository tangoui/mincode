const SENSITIVE_HEADER_NAMES = [
  "authorization", "proxy-authorization", "cookie", "set-cookie", "x-api-key", "x-auth-token", "x-access-token", "x-csrf-token",
];
const SENSITIVE_KEY_FRAGMENTS = [
  "secret", "token", "password", "passwd", "pwd", "apikey", "api_key", "api-key", "auth", "credential", "session", "cookie",
  "privatekey", "private_key", "accesskey", "access_key", "signature",
];
/** Credential shapes; the first eight also apply to prose (documentation, descriptions). */
const CREDENTIAL_PATTERNS = [
  /eyJ[\w-]+\.eyJ[\w-]+\./,
  /-----BEGIN [A-Z ]*PRIVATE KEY-----/,
  /AKIA[0-9A-Z]{16}/,
  /gh[pousr]_[A-Za-z0-9]{36}/,
  /github_pat_/,
  /xox[baprs]-/,
  /sk-[A-Za-z0-9]{20,}/,
  /AIza[0-9A-Za-z_-]{35}/,
  /Basic [A-Za-z0-9+/=]{12,}/,
  /Bearer [A-Za-z0-9._~+/-]{16,}/,
];
const PROSE_PATTERN_COUNT = 8;


/** Reports whether a value looks like a credential (prose skips the Basic/Bearer shapes). */
export function looksLikeHttpCredential(value: string, prose = false): boolean {
  const patterns = prose ? CREDENTIAL_PATTERNS.slice(0, PROSE_PATTERN_COUNT) : CREDENTIAL_PATTERNS;
  return patterns.some((pattern) => pattern.test(value));
}

/** A sensitive header name, or any key that contains a sensitive fragment (case-insensitive). */
export function isSensitiveHttpKey(key: string, header: boolean): boolean {
  const lower = key.trim().toLowerCase();
  if (header && SENSITIVE_HEADER_NAMES.includes(lower)) return true;
  return SENSITIVE_KEY_FRAGMENTS.some((fragment) => lower.includes(fragment));
}

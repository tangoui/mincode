import { normalizeTerminalText } from "./context-menu.ts";

export type TerminalAiAction =
  | "explain-command"
  | "analyze-output"
  | "diagnose-error"
  | "optimize-command"
  | "security-review"
  | "generate-command"
  | "ask-context";

export interface PreparedTerminalAiContext {
  text: string;
  redactionCount: number;
  truncated: boolean;
}

const DEFAULT_CONTEXT_LIMIT = 16_000;
const CONTEXT_HEAD_LENGTH = 4_000;
const TRUNCATION_MARKER = "\n\n[... terminal context truncated ...]\n\n";

const PRIVATE_KEY_PATTERN = /-----BEGIN [^-\n]*PRIVATE KEY-----[\s\S]*?-----END [^-\n]*PRIVATE KEY-----/gi;
const SENSITIVE_ASSIGNMENT_PATTERN = new RegExp(
  String.raw`\b([A-Za-z_][A-Za-z0-9_]*(?:PASSWORD|PASSWD|TOKEN|SECRET|API[_-]?KEY|ACCESS[_-]?KEY|PRIVATE[_-]?KEY)[A-Za-z0-9_]*)\s*=\s*(?:"[^"]*"|'[^']*'|[^\s;|&]+)`,
  "gi"
);
const SENSITIVE_OPTION_PATTERN = new RegExp(
  String.raw`(--?(?:password|passwd|token|secret|api[_-]?key|access[_-]?key|private[_-]?key))(?:\s*=\s*|\s+)(?:"[^"]*"|'[^']*'|[^\s;|&]+)`,
  "gi"
);
const AUTHORIZATION_PATTERN = /\b(Authorization\s*:\s*)(?:Bearer|Basic)\s+[^\s'";]+/gi;
const URL_CREDENTIAL_PATTERN = /\b([a-z][a-z0-9+.-]*:\/\/)([^\s/:@]+):([^\s/@]+)@/gi;
const SENSITIVE_QUERY_PATTERN = /([?&](?:password|passwd|token|secret|api[_-]?key|access[_-]?key)=)[^&#\s]+/gi;

function redactMatches(
  value: string,
  pattern: RegExp,
  replacement: string | ((substring: string, ...args: string[]) => string)
) {
  let count = 0;
  const text = value.replace(pattern, (...args) => {
    count += 1;
    if (typeof replacement === "string") return replacement;
    return replacement(...(args as [string, ...string[]]));
  });
  return { text, count };
}

export function redactTerminalAiContext(value: string) {
  let text = normalizeTerminalText(value);
  let redactionCount = 0;

  const apply = (
    pattern: RegExp,
    replacement: string | ((substring: string, ...args: string[]) => string)
  ) => {
    const result = redactMatches(text, pattern, replacement);
    text = result.text;
    redactionCount += result.count;
  };

  apply(PRIVATE_KEY_PATTERN, "[REDACTED PRIVATE KEY]");
  apply(SENSITIVE_ASSIGNMENT_PATTERN, (_match, name) => `${name}=[REDACTED]`);
  apply(SENSITIVE_OPTION_PATTERN, (_match, option) => `${option} [REDACTED]`);
  apply(AUTHORIZATION_PATTERN, (_match, prefix) => `${prefix}[REDACTED]`);
  apply(URL_CREDENTIAL_PATTERN, (_match, scheme, username) => `${scheme}${username}:[REDACTED]@`);
  apply(SENSITIVE_QUERY_PATTERN, (_match, prefix) => `${prefix}[REDACTED]`);

  return { text, redactionCount };
}

export function prepareTerminalAiContext(
  value: string,
  limit = DEFAULT_CONTEXT_LIMIT
): PreparedTerminalAiContext {
  const redacted = redactTerminalAiContext(value);
  const normalizedLimit = Math.max(1_000, Math.floor(limit));
  if (redacted.text.length <= normalizedLimit) {
    return { ...redacted, truncated: false };
  }

  const headLength = Math.min(CONTEXT_HEAD_LENGTH, Math.floor(normalizedLimit / 3));
  const tailLength = Math.max(0, normalizedLimit - headLength - TRUNCATION_MARKER.length);
  return {
    text: `${redacted.text.slice(0, headLength)}${TRUNCATION_MARKER}${redacted.text.slice(-tailLength)}`,
    redactionCount: redacted.redactionCount,
    truncated: true,
  };
}

export function markdownFenceForTerminalContext(value: string) {
  let length = 3;
  for (const match of value.match(/`+/g) ?? []) length = Math.max(length, match.length + 1);
  return "`".repeat(length);
}

export function buildTerminalAiPrompt(options: {
  instruction: string;
  contextLabel: string;
  terminalLabel: string;
  safetyInstruction: string;
  context: string;
}) {
  const context = options.context.trim();
  if (!context) return options.instruction.trim();
  const fence = markdownFenceForTerminalContext(context);
  return [
    options.instruction.trim(),
    `${options.terminalLabel}\n\n${options.contextLabel}`,
    `${fence}text\n${context}\n${fence}`,
    options.safetyInstruction.trim(),
  ].filter(Boolean).join("\n\n");
}

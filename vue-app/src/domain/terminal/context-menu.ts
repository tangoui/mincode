export type TerminalContextMenuAction =
  | "copy"
  | "paste"
  | "select-all"
  | "search-selection"
  | "clear"
  | "copy-markdown"
  | "copy-url"
  | "open-url"
  | "copy-path"
  | "reveal-sftp"
  | "bookmark"
  | "send-interrupt"
  | "send-eof"
  | "split-horizontal"
  | "split-vertical"
  | "reload"
  | "close"
  | "copy-ssh-command"
  | "ai-assistant";

export interface TerminalTextContext {
  text: string;
  line: string;
  token: string;
  url?: string;
  path?: string;
}

export interface TerminalPasteAnalysis {
  text: string;
  lineCount: number;
  hasTrailingNewline: boolean;
  dangerousPatterns: string[];
  requiresConfirmation: boolean;
}

const URL_PATTERN = /^https?:\/\/[^\s]+$/i;
const UNIX_PATH_PATTERN = /^(?:\/|~\/|\.\.?\/)[^\s]+$/;
const WINDOWS_PATH_PATTERN = /^[A-Za-z]:[\\/][^\s]+$/;
const LEADING_WRAPPERS = new Set(["(", "'", '"', "`", "[", "{", "<"]);
const TRAILING_WRAPPERS = /[)'"`\]}>.,:;!?]+$/;

const DANGEROUS_PASTE_PATTERNS: ReadonlyArray<[string, RegExp]> = [
  ["privileged", /(^|[;&|]\s*)sudo\s+/im],
  ["recursive-delete", /(^|[;&|]\s*)rm\s+[^\n]*-(?:[^\n]*r[^\n]*f|[^\n]*f[^\n]*r)\b/im],
  ["remote-script", /\b(?:curl|wget)\b[^\n|]*\|\s*(?:ba)?sh\b/im],
  ["disk-write", /\b(?:mkfs(?:\.[a-z0-9]+)?|dd\s+[^\n]*\b(?:if|of)=)\b/im],
  ["database-destructive", /\b(?:drop\s+(?:database|schema|table)|truncate\s+table|delete\s+from)\b/im],
  ["overwrite-redirect", /(^|[^>])>(?![>&])\s*[^\s]/m],
  ["shutdown", /\b(?:shutdown|reboot|halt|poweroff)\b/im],
];

export function normalizeTerminalText(value: string) {
  return value.replace(/\r\n?/g, "\n").split("\u0000").join("");
}

export function stripTerminalTrailingNewlines(value: string) {
  return normalizeTerminalText(value).replace(/\n+$/g, "");
}

export function cleanTerminalToken(value: string) {
  let token = normalizeTerminalText(value).trim();
  while (token && LEADING_WRAPPERS.has(token[0])) token = token.slice(1);
  return token.replace(TRAILING_WRAPPERS, "");
}

export function terminalTokenAtColumn(line: string, column: number) {
  if (!line) return "";
  const safeColumn = Math.max(0, Math.min(Math.floor(column), line.length - 1));
  if (/\s/.test(line[safeColumn] ?? "")) return "";
  let start = safeColumn;
  let end = safeColumn + 1;
  while (start > 0 && !/\s/.test(line[start - 1] ?? "")) start -= 1;
  while (end < line.length && !/\s/.test(line[end] ?? "")) end += 1;
  return cleanTerminalToken(line.slice(start, end));
}

export function extractTerminalUrl(value: string) {
  const token = cleanTerminalToken(value);
  return URL_PATTERN.test(token) ? token : undefined;
}

export function extractTerminalPath(value: string) {
  const token = cleanTerminalToken(value);
  return UNIX_PATH_PATTERN.test(token) || WINDOWS_PATH_PATTERN.test(token) ? token : undefined;
}

export function canRevealRemotePath(path?: string) {
  return Boolean(path && (path.startsWith("/") || path.startsWith("~/")));
}

export function createTerminalTextContext(selection: string, line = "", column = 0): TerminalTextContext {
  const selected = normalizeTerminalText(selection);
  const normalizedLine = normalizeTerminalText(line).trimEnd();
  const token = selected.trim() || terminalTokenAtColumn(normalizedLine, column);
  return {
    text: selected,
    line: selected.trim() ? "" : normalizedLine,
    token,
    url: extractTerminalUrl(token),
    path: extractTerminalPath(token),
  };
}

export function terminalAiContextText(context: TerminalTextContext) {
  return context.text.trim() || context.line.trim() || context.token.trim();
}

export function analyzeTerminalPaste(value: string): TerminalPasteAnalysis {
  const text = normalizeTerminalText(value);
  const hasTrailingNewline = /\n$/.test(text);
  const lineCount = text ? text.split("\n").length - (hasTrailingNewline ? 1 : 0) : 0;
  const dangerousPatterns = DANGEROUS_PASTE_PATTERNS.filter(([, pattern]) => pattern.test(text)).map(
    ([name]) => name
  );
  return {
    text,
    lineCount: Math.max(lineCount, text ? 1 : 0),
    hasTrailingNewline,
    dangerousPatterns,
    requiresConfirmation: lineCount > 1 || hasTrailingNewline || dangerousPatterns.length > 0,
  };
}

export function terminalTextAsMarkdown(value: string) {
  const text = normalizeTerminalText(value).replace(/\n+$/g, "");
  let longestFence = 3;
  for (const match of text.match(/`+/g) ?? []) {
    longestFence = Math.max(longestFence, match.length + 1);
  }
  const fence = "`".repeat(longestFence);
  return `${fence}\n${text}\n${fence}`;
}

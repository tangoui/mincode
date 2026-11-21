import type { AiAgentEventActivityInput } from "./agent-event-activity.ts";
import { normalizeAiEventText } from "./event-presentations.ts";

export type AiAgentEventDetailKind = "json" | "markdown" | "code" | "terminal";

export interface AiAgentEventDetailPresentation {
  kind: AiAgentEventDetailKind;
  content: string;
  language?: string;
}

const MARKDOWN_EXTENSIONS = new Set(["md", "markdown", "mdx", "mdwn"]);
const FAILED_STATUSES = new Set(["failed", "error", "cancelled"]);
const TERMINAL_TOOL_NAMES = new Set([
  "workspace.run_command",
  "ssh.run_command",
  "terminal.send_input",
  "terminal.run_command",
  "shell.run",
]);

const LANGUAGE_BY_EXTENSION: Readonly<Record<string, string>> = {
  bash: "bash",
  bat: "batch",
  c: "c",
  cc: "cpp",
  cjs: "javascript",
  clj: "clojure",
  cljs: "clojure",
  cmd: "batch",
  conf: "ini",
  cpp: "cpp",
  cs: "csharp",
  css: "css",
  cts: "typescript",
  cxx: "cpp",
  dart: "dart",
  env: "dotenv",
  erl: "erlang",
  ex: "elixir",
  exs: "elixir",
  fish: "fish",
  fs: "fsharp",
  fsx: "fsharp",
  go: "go",
  groovy: "groovy",
  h: "c",
  hpp: "cpp",
  htm: "html",
  html: "html",
  ini: "ini",
  java: "java",
  js: "javascript",
  json: "json",
  jsonc: "jsonc",
  jsx: "jsx",
  kt: "kotlin",
  kts: "kotlin",
  less: "less",
  lua: "lua",
  mjs: "javascript",
  mts: "typescript",
  php: "php",
  properties: "properties",
  ps1: "powershell",
  py: "python",
  pyw: "python",
  r: "r",
  rb: "ruby",
  rs: "rust",
  sass: "sass",
  scala: "scala",
  scss: "scss",
  sh: "shell",
  sol: "solidity",
  sql: "sql",
  styl: "stylus",
  swift: "swift",
  toml: "toml",
  ts: "typescript",
  tsx: "tsx",
  vue: "vue",
  xhtml: "html",
  xml: "xml",
  yaml: "yaml",
  yml: "yaml",
  zsh: "shell",
};

const LANGUAGE_BY_FILENAME: Readonly<Record<string, string>> = {
  containerfile: "dockerfile",
  dockerfile: "dockerfile",
  gemfile: "ruby",
  justfile: "makefile",
  makefile: "makefile",
  procfile: "shell",
  rakefile: "ruby",
};

function asRecord(value: unknown): Record<string, unknown> | undefined {
  return value && typeof value === "object" && !Array.isArray(value)
    ? value as Record<string, unknown>
    : undefined;
}

function parseToolInput(value?: string) {
  if (!value?.trim()) return undefined;
  try {
    return asRecord(JSON.parse(value));
  } catch {
    return undefined;
  }
}

function firstString(record: Record<string, unknown> | undefined, keys: string[]) {
  for (const key of keys) {
    const value = record?.[key];
    if (typeof value === "string" && value.trim()) return value.trim();
  }
  return undefined;
}

function targetPath(event: AiAgentEventActivityInput) {
  return firstString(parseToolInput(event.toolInput), [
    "path",
    "file_path",
    "filePath",
    "relativePath",
    "target",
  ]);
}

function fileIdentity(path?: string) {
  const filename = path?.replace(/\\/g, "/").split("/").pop()?.toLowerCase() ?? "";
  const extension = filename.includes(".") ? filename.split(".").pop() ?? "" : "";
  return { filename, extension };
}

function jsonPresentation(content: string): AiAgentEventDetailPresentation | undefined {
  try {
    const parsed = JSON.parse(content) as unknown;
    if (parsed === null || typeof parsed !== "object") return undefined;
    return {
      kind: "json",
      language: "json",
      content: JSON.stringify(parsed, null, 2),
    };
  } catch {
    return undefined;
  }
}

function shebangLanguage(content: string) {
  const firstLine = content.split("\n", 1)[0]?.toLowerCase() ?? "";
  if (!firstLine.startsWith("#!")) return undefined;
  if (firstLine.includes("python")) return "python";
  if (firstLine.includes("node")) return "javascript";
  if (firstLine.includes("bash")) return "bash";
  if (firstLine.includes("zsh")) return "shell";
  if (firstLine.includes("sh")) return "shell";
  return undefined;
}

/**
 * Selects a safe, content-aware renderer for an agent event detail.
 * Valid structured JSON wins first; file reads then use their path extension,
 * command output remains verbatim, and other human-readable text uses Markdown.
 */
export function presentAiAgentEventDetail(
  event: AiAgentEventActivityInput,
  detail = event.detail,
): AiAgentEventDetailPresentation {
  const content = normalizeAiEventText(detail).trim();
  const json = jsonPresentation(content);
  if (json) return json;

  const toolName = event.toolName?.trim().toLowerCase() ?? "";
  if (TERMINAL_TOOL_NAMES.has(toolName)) {
    return { kind: "terminal", language: "terminal", content };
  }
  if (FAILED_STATUSES.has(event.status?.trim().toLowerCase() ?? "")) {
    return { kind: "markdown", language: "markdown", content };
  }

  const { filename, extension } = fileIdentity(targetPath(event));
  if (MARKDOWN_EXTENSIONS.has(extension)) {
    return { kind: "markdown", language: "markdown", content };
  }

  const language = LANGUAGE_BY_FILENAME[filename]
    ?? LANGUAGE_BY_EXTENSION[extension]
    ?? shebangLanguage(content);
  if (language) return { kind: "code", language, content };

  return { kind: "markdown", language: "markdown", content };
}

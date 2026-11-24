import { ASK_USER_TOOL_NAME } from "./ask-user.ts";
import { normalizeAiEventText } from "./event-presentations.ts";

export type AiAgentActivityKind =
  | "analysis"
  | "plan"
  | "modeChange"
  | "readFile"
  | "openFile"
  | "searchCode"
  | "searchFiles"
  | "runTest"
  | "codeCheck"
  | "buildProject"
  | "gitOperation"
  | "runCommand"
  | "writeFile"
  | "createFile"
  | "deleteFile"
  | "browserAction"
  | "databaseAction"
  | "httpAction"
  | "remoteAction"
  | "connectorCall"
  | "systemAction"
  | "genericTool";

export interface AiAgentEventActivityInput {
  kind: "thought" | "tool" | "plan" | "mode";
  title: string;
  detail: string;
  status?: string;
  toolName?: string;
  toolInput?: string;
}

export interface AiAgentEventActivity {
  kind: AiAgentActivityKind;
  target?: string;
  /** Whether the compact activity row should expose its raw/structured result. */
  expandable: boolean;
}

const READ_FILE_TOOL_NAMES = new Set([
  "workspace.read_file",
  "workspace.open_project_file",
  "workspace.open_file",
  "filesystem.read_file",
  "fs.read_file",
]);
const OPEN_FILE_TOOL_NAMES = new Set([
  "workspace.open_project_file",
  "workspace.open_file",
]);
const SEARCH_CODE_TOOL_NAMES = new Set([
  "workspace.search_code",
  "workspace.regex_search",
  "workspace.grep",
  "filesystem.search_code",
]);
const SEARCH_FILE_TOOL_NAMES = new Set([
  "workspace.search",
  "workspace.search_file_index",
  "workspace.list_files",
  "workspace.list_all_files",
  "filesystem.search",
]);
const COMMAND_TOOL_NAMES = new Set([
  "workspace.run_command",
  "ssh.run_command",
  "terminal.send_input",
  "terminal.run_command",
  "shell.run",
]);
const WRITE_FILE_TOOL_NAMES = new Set([
  "workspace.write_file",
  "workspace.save_project_file",
  "workspace.replace_apply",
  "filesystem.write_file",
  "fs.write_file",
]);
const CREATE_FILE_TOOL_NAMES = new Set([
  "workspace.create_directory",
  "filesystem.create_directory",
  "fs.create_directory",
]);
const DELETE_FILE_TOOL_NAMES = new Set([
  "workspace.delete_entry",
  "filesystem.delete_file",
  "filesystem.delete_directory",
  "fs.delete_file",
]);
const FAILED_STATUSES = new Set(["failed", "error", "cancelled"]);
const GENERIC_TITLES = new Set(["tool call", "工具调用", "tool", "工具"]);

function text(value: unknown) {
  return typeof value === "string" && value.trim() ? value.trim() : undefined;
}

function asRecord(value: unknown): Record<string, unknown> | undefined {
  return value && typeof value === "object" && !Array.isArray(value)
    ? value as Record<string, unknown>
    : undefined;
}

function parseRecord(value: string | undefined) {
  const normalized = normalizeAiEventText(value ?? "").trim();
  if (!normalized) return undefined;
  try {
    return asRecord(JSON.parse(normalized) as unknown);
  } catch {
    return undefined;
  }
}

function firstText(record: Record<string, unknown> | undefined, keys: string[]) {
  for (const key of keys) {
    const value = text(record?.[key]);
    if (value) return value;
  }
  return undefined;
}

function normalizedToolName(event: AiAgentEventActivityInput) {
  return (event.toolName ?? "").trim().toLowerCase();
}

function titleLooksLike(event: AiAgentEventActivityInput, ...terms: string[]) {
  const normalizedTitle = event.title.trim().toLowerCase();
  return terms.some((term) => normalizedTitle.includes(term));
}

function isFailed(event: AiAgentEventActivityInput) {
  return FAILED_STATUSES.has((event.status ?? "").trim().toLowerCase());
}

function redactSensitiveText(value: string | undefined) {
  if (!value) return undefined;
  return value
    .replace(/((?:--|\b)(?:token|password|passwd|api[-_]?key|secret|authorization)(?:=|\s+))([^\s"']+)/gi, "$1••••••")
    .replace(/(https?:\/\/[^\s/:]+:)([^@\s]+)(@)/gi, "$1••••••$3");
}

function toolTarget(event: AiAgentEventActivityInput, keys: string[]) {
  const input = parseRecord(event.toolInput);
  const detail = parseRecord(event.detail);
  return redactSensitiveText(firstText(input, keys) ?? firstText(detail, keys));
}

function fallbackTarget(event: AiAgentEventActivityInput) {
  const title = event.title.trim();
  return title && !GENERIC_TITLES.has(title.toLowerCase()) ? title : undefined;
}

function categorizedTarget(event: AiAgentEventActivityInput, keys: string[]) {
  return toolTarget(event, keys) ?? fallbackTarget(event);
}

function commandActivityKind(command: string | undefined): AiAgentActivityKind {
  const normalized = (command ?? "").trim().toLowerCase();
  if (!normalized) return "runCommand";
  if (
    /(^|\s)(go\s+test|pytest|phpunit|cargo\s+test|dotnet\s+test)(\s|$)/.test(normalized)
    || /(^|\s)(pnpm|npm|yarn|bun)(\s+run)?\s+test(?::|\s|$)/.test(normalized)
    || /(^|\s)(pnpm|npm|yarn|bun)\s+(exec|x)\s+(vitest|jest)(\s|$)/.test(normalized)
    || /(^|\s)(vitest|jest)(\s|$)/.test(normalized)
    || /(^|\s)node\s+.*--test(\s|$)/.test(normalized)
    || /scripts\/(run-tests|.+\.test)\.(mjs|js|ts)(\s|$)/.test(normalized)
  ) return "runTest";
  if (
    /(^|\s)(eslint|vue-tsc|tsc|phpstan|psalm|golangci-lint|go\s+vet)(\s|$)/.test(normalized)
    || /(^|\s)(pnpm|npm|yarn|bun)\s+(exec|x)\s+(eslint|vue-tsc|tsc)(\s|$)/.test(normalized)
    || /(^|\s)(pnpm|npm|yarn|bun)(\s+run)?\s+(lint|typecheck|check)(:|\s|$)/.test(normalized)
  ) return "codeCheck";
  if (
    /(^|\s)(vite|webpack|rollup)(\s+task|\s+run)?\s+.*(build|package)/.test(normalized)
    || /(^|\s)(pnpm|npm|yarn|bun)(\s+run)?\s+build(?::|\s|$)/.test(normalized)
    || /(^|\s)(go\s+build|cargo\s+build)(\s|$)/.test(normalized)
  ) return "buildProject";
  if (/^git\s+/.test(normalized) || /(^|\s)git\s+(status|diff|log|add|commit|push|pull|fetch|rebase|merge)(\s|$)/.test(normalized)) {
    return "gitOperation";
  }
  return "runCommand";
}

function prefixActivity(
  event: AiAgentEventActivityInput,
  toolName: string,
): AiAgentEventActivity | undefined {
  const targetKeys = [
    "path",
    "filePath",
    "relativePath",
    "query",
    "command",
    "url",
    "table",
    "key",
    "name",
    "view",
    "repoPath",
    "connectionId",
    "toolName",
  ];
  const target = categorizedTarget(event, targetKeys);
  const expandable = Boolean(event.detail.trim()) || isFailed(event);
  if (toolName.startsWith("git.")) return { kind: "gitOperation", target, expandable };
  if (toolName.startsWith("browser.") || toolName.startsWith("computer.")) {
    return { kind: "browserAction", target, expandable };
  }
  if (toolName.startsWith("database.") || toolName.startsWith("redis.")) {
    return { kind: "databaseAction", target, expandable };
  }
  if (toolName.startsWith("http.")) return { kind: "httpAction", target, expandable };
  if (toolName.startsWith("ssh.") || toolName.startsWith("sftp.")) {
    return { kind: "remoteAction", target, expandable };
  }
  if (toolName === "settings.call_mcp_tool") {
    return { kind: "connectorCall", target, expandable };
  }
  if (toolName.startsWith("system.") || toolName.startsWith("devtools.")) {
    return { kind: "systemAction", target, expandable };
  }
  return undefined;
}

/**
 * Produces a concise, i18n-ready activity type and target from a persisted event.
 * Tool input is preferred because completed calls usually replace `detail` with output.
 */
export function describeAiAgentEventActivity(event: AiAgentEventActivityInput): AiAgentEventActivity {
  if (event.kind === "thought") {
    return { kind: "analysis", expandable: Boolean(event.detail.trim()) };
  }
  if (event.kind === "plan") {
    return { kind: "plan", expandable: Boolean(event.detail.trim()) };
  }
  if (event.kind === "mode") {
    return { kind: "modeChange", target: fallbackTarget(event), expandable: Boolean(event.detail.trim()) };
  }

  const toolName = normalizedToolName(event);
  if (READ_FILE_TOOL_NAMES.has(toolName) || (!toolName && titleLooksLike(event, "read file", "读取文件"))) {
    return {
      kind: OPEN_FILE_TOOL_NAMES.has(toolName) ? "openFile" : "readFile",
      target: toolTarget(event, ["path", "filePath", "relativePath", "uri", "target"]),
      expandable: Boolean(event.detail.trim()) || isFailed(event),
    };
  }
  if (SEARCH_CODE_TOOL_NAMES.has(toolName) || (!toolName && titleLooksLike(event, "search code", "代码搜索", "搜索代码"))) {
    return {
      kind: "searchCode",
      target: toolTarget(event, ["query", "pattern", "search", "keyword"]),
      expandable: true,
    };
  }
  if (SEARCH_FILE_TOOL_NAMES.has(toolName) || (!toolName && titleLooksLike(event, "search file", "文件搜索", "搜索文件"))) {
    return {
      kind: "searchFiles",
      target: toolTarget(event, ["query", "pattern", "search", "keyword", "path"]),
      expandable: true,
    };
  }
  if (COMMAND_TOOL_NAMES.has(toolName) || (!toolName && titleLooksLike(event, "run command", "运行命令", "执行命令"))) {
    const command = toolTarget(event, ["command", "cmd", "script", "input"]);
    return { kind: commandActivityKind(command), target: command, expandable: true };
  }
  if (WRITE_FILE_TOOL_NAMES.has(toolName) || (!toolName && titleLooksLike(event, "write file", "写入文件", "保存文件", "编辑文件"))) {
    return {
      kind: "writeFile",
      target: toolTarget(event, ["path", "filePath", "relativePath", "target"]),
      expandable: true,
    };
  }
  if (CREATE_FILE_TOOL_NAMES.has(toolName) || (!toolName && titleLooksLike(event, "create file", "创建文件", "创建目录"))) {
    return {
      kind: "createFile",
      target: toolTarget(event, ["path", "filePath", "relativePath", "target"]),
      expandable: true,
    };
  }
  if (DELETE_FILE_TOOL_NAMES.has(toolName) || (!toolName && titleLooksLike(event, "delete file", "删除文件", "删除目录"))) {
    return {
      kind: "deleteFile",
      target: toolTarget(event, ["path", "filePath", "relativePath", "target"]),
      expandable: true,
    };
  }

  const categorized = prefixActivity(event, toolName);
  if (categorized) return categorized;
  const name = event.toolName?.trim();
  const title = fallbackTarget(event);
  return {
    kind: "genericTool",
    target: toolName === ASK_USER_TOOL_NAME ? title ?? name : name || title,
    expandable: Boolean(event.detail.trim()) || isFailed(event),
  };
}

/** Formats JSON tool output only after the user explicitly opens an activity row. */
export function formatAiAgentEventDetail(detail: string) {
  const normalized = normalizeAiEventText(detail).trim();
  if (!normalized) return "";
  try {
    const parsed = JSON.parse(normalized) as unknown;
    return typeof parsed === "object" && parsed !== null ? JSON.stringify(parsed, null, 2) : normalized;
  } catch {
    return normalized;
  }
}

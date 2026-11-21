import { SESSION_PROVIDER_IDS, type SessionProviderId } from "@/domain/ai/session-deep-link";

/**
 * "CC 会话" 页面的工具配置：内置 CLI 的启用/排序/命令模板/数据目录覆盖，
 * 以及用户自定义的 JSONL/JSON 会话来源。纯函数，供视图、设置对话框与测试共用。
 */

export const CUSTOM_SESSION_TOOL_PREFIX = "custom:";
export const MAX_CUSTOM_SESSION_TOOLS = 32;

export type SessionToolId = string;
export type CustomSessionFormat = "jsonl" | "json";

export interface CustomSessionFields {
  sessionId: string;
  cwd: string;
  title: string;
  messages: string;
  role: string;
  content: string;
  timestamp: string;
  model: string;
}

export type CustomSessionFieldKey = keyof CustomSessionFields;

export interface BuiltinSessionToolPrefs {
  enabled: boolean;
  dir: string;
  resumeTemplate: string;
  newTemplate: string;
}

export interface CustomSessionTool {
  id: string;
  name: string;
  mark: string;
  enabled: boolean;
  dir: string;
  glob: string;
  format: CustomSessionFormat;
  fields: CustomSessionFields;
  userRoles: string;
  assistantRoles: string;
  resumeTemplate: string;
  newTemplate: string;
}

export interface SessionToolPrefs {
  version: 1;
  order: string[];
  builtins: Record<SessionProviderId, BuiltinSessionToolPrefs>;
  custom: CustomSessionTool[];
}

export interface BuiltinSessionToolDefinition {
  id: SessionProviderId;
  mark: string;
  defaultDir: string;
  resumeTemplate: string;
  newTemplate: string;
  /** CLI 自身识别的数据目录环境变量；覆盖目录时注入到新开的终端。 */
  dirEnv?: string;
}

/** 发送给后端 list/get/delete_ai_cli_session 的 custom 参数。 */
export interface CustomSessionBackendConfig {
  dir: string;
  glob: string;
  format: CustomSessionFormat;
  fields: CustomSessionFields;
  userRoles: string;
  assistantRoles: string;
  processName?: string;
}

export const BUILTIN_SESSION_TOOLS: readonly BuiltinSessionToolDefinition[] = [
  { id: "claude", mark: "CC", defaultDir: "~/.claude", resumeTemplate: "claude --resume {id}", newTemplate: "claude", dirEnv: "CLAUDE_CONFIG_DIR" },
  { id: "pi", mark: "PI", defaultDir: "~/.pi/agent", resumeTemplate: "pi --session {id}", newTemplate: "pi", dirEnv: "PI_CODING_AGENT_DIR" },
  { id: "codex", mark: "CX", defaultDir: "~/.codex", resumeTemplate: "codex resume {id}", newTemplate: "codex", dirEnv: "CODEX_HOME" },
  { id: "gemini", mark: "GM", defaultDir: "~/.gemini", resumeTemplate: "gemini --resume {id}", newTemplate: "gemini" },
  { id: "opencode", mark: "OC", defaultDir: "~/.local/share/opencode", resumeTemplate: "opencode --session {id}", newTemplate: "opencode" },
  { id: "kimi", mark: "KM", defaultDir: "~/.kimi-code", resumeTemplate: "kimi --session {id}", newTemplate: "kimi" },
  { id: "qodercn", mark: "QC", defaultDir: "~/.qoder-cn", resumeTemplate: "qodercn --resume {id}", newTemplate: "qodercn" },
  { id: "qoder", mark: "QD", defaultDir: "~/.qoder", resumeTemplate: "qoder --resume {id}", newTemplate: "qoder" },
  { id: "grok", mark: "GK", defaultDir: "~/.grok", resumeTemplate: "grok --resume {id}", newTemplate: "grok" },
];

/** 与后端 custom_sessions.go 的默认字段保持一致，界面上用作占位提示。 */
export const DEFAULT_CUSTOM_FIELDS: Readonly<CustomSessionFields> = {
  sessionId: "sessionId,session_id",
  cwd: "cwd,projectRoot,workdir,directory",
  title: "title",
  messages: "messages",
  role: "role,type,message.role",
  content: "content,message.content,text",
  timestamp: "timestamp,time,created_at,createdAt",
  model: "model,message.model",
};

export const CUSTOM_SESSION_FIELD_KEYS: readonly CustomSessionFieldKey[] = [
  "sessionId",
  "cwd",
  "title",
  "messages",
  "role",
  "content",
  "timestamp",
  "model",
];

export const DEFAULT_CUSTOM_USER_ROLES = "user,human";
export const DEFAULT_CUSTOM_ASSISTANT_ROLES = "assistant,ai,model,gemini,agent,bot";

export function defaultCustomGlob(format: CustomSessionFormat) {
  return format === "json" ? "*.json" : "*.jsonl";
}

const builtinIds = new Set<string>(SESSION_PROVIDER_IDS);
const builtinById = new Map(BUILTIN_SESSION_TOOLS.map((tool) => [tool.id, tool]));
const PROCESS_NAME_PATTERN = /^[A-Za-z0-9._+-]+$/;
const ENV_ASSIGNMENT_PATTERN = /^[A-Za-z_][A-Za-z0-9_]*=/;
const ENV_NAME_PATTERN = /^[A-Za-z_][A-Za-z0-9_]*$/;
const CUSTOM_ID_PATTERN = /^custom:[a-z0-9]+(?:-[a-z0-9]+)*$/;
const MAX_TEXT_LENGTH = 512;

export function isBuiltinSessionToolId(id: string): id is SessionProviderId {
  return builtinIds.has(id);
}

export function isCustomSessionToolId(id: string) {
  return CUSTOM_ID_PATTERN.test(id);
}

export function builtinSessionTool(id: SessionProviderId): BuiltinSessionToolDefinition {
  return builtinById.get(id) as BuiltinSessionToolDefinition;
}

/** CSS 类名后缀：自定义工具 id 含冒号，统一归为 is-custom。 */
export function sessionToolClassSuffix(id: string) {
  return isBuiltinSessionToolId(id) ? id : "custom";
}

function text(value: unknown, max = MAX_TEXT_LENGTH) {
  return typeof value === "string" ? value.trim().slice(0, max) : "";
}

function record(value: unknown): Record<string, unknown> {
  return value && typeof value === "object" && !Array.isArray(value)
    ? (value as Record<string, unknown>)
    : {};
}

export function createDefaultBuiltinPrefs(): BuiltinSessionToolPrefs {
  return { enabled: true, dir: "", resumeTemplate: "", newTemplate: "" };
}

export function createDefaultSessionToolPrefs(): SessionToolPrefs {
  return {
    version: 1,
    order: BUILTIN_SESSION_TOOLS.map((tool) => tool.id),
    builtins: Object.fromEntries(
      BUILTIN_SESSION_TOOLS.map((tool) => [tool.id, createDefaultBuiltinPrefs()]),
    ) as Record<SessionProviderId, BuiltinSessionToolPrefs>,
    custom: [],
  };
}

export function emptyCustomFields(): CustomSessionFields {
  return { sessionId: "", cwd: "", title: "", messages: "", role: "", content: "", timestamp: "", model: "" };
}

/** 由名称派生 "custom:<slug>"，与已有 id 冲突时追加 -2、-3…… */
export function slugifyCustomToolId(name: string, existingIds: Iterable<string> = []) {
  const taken = new Set(existingIds);
  const slug =
    name
      .toLowerCase()
      .replace(/[^a-z0-9]+/g, "-")
      .replace(/^-+|-+$/g, "")
      .slice(0, 40)
      .replace(/-+$/g, "") || "tool";
  let candidate = `${CUSTOM_SESSION_TOOL_PREFIX}${slug}`;
  for (let suffix = 2; taken.has(candidate); suffix += 1) {
    candidate = `${CUSTOM_SESSION_TOOL_PREFIX}${slug}-${suffix}`;
  }
  return candidate;
}

export function defaultCustomMark(name: string) {
  const letters = name.replace(/[^A-Za-z0-9]/g, "");
  return (letters || name.trim() || "?").slice(0, 2).toUpperCase();
}

export function createCustomSessionTool(name: string, existingIds: Iterable<string> = []): CustomSessionTool {
  const trimmed = name.trim();
  return {
    id: slugifyCustomToolId(trimmed, existingIds),
    name: trimmed,
    mark: defaultCustomMark(trimmed),
    enabled: true,
    dir: "",
    glob: "",
    format: "jsonl",
    fields: emptyCustomFields(),
    userRoles: "",
    assistantRoles: "",
    resumeTemplate: "",
    newTemplate: "",
  };
}

function normalizeBuiltin(value: unknown): BuiltinSessionToolPrefs {
  const source = record(value);
  return {
    enabled: typeof source.enabled === "boolean" ? source.enabled : true,
    dir: text(source.dir),
    resumeTemplate: text(source.resumeTemplate),
    newTemplate: text(source.newTemplate),
  };
}

function normalizeCustomFields(value: unknown): CustomSessionFields {
  const source = record(value);
  const fields = emptyCustomFields();
  for (const key of CUSTOM_SESSION_FIELD_KEYS) fields[key] = text(source[key]);
  return fields;
}

export function normalizeCustomSessionTool(value: unknown, takenIds: Set<string>): CustomSessionTool | null {
  const source = record(value);
  const name = text(source.name, 60);
  if (!name) return null;
  const rawId = text(source.id, 80);
  const id = isCustomSessionToolId(rawId) && !takenIds.has(rawId) ? rawId : slugifyCustomToolId(name, takenIds);
  const mark = Array.from(text(source.mark, 12)).slice(0, 3).join("") || defaultCustomMark(name);
  return {
    id,
    name,
    mark,
    enabled: typeof source.enabled === "boolean" ? source.enabled : true,
    dir: text(source.dir),
    glob: text(source.glob, 200),
    format: source.format === "json" ? "json" : "jsonl",
    fields: normalizeCustomFields(source.fields),
    userRoles: text(source.userRoles),
    assistantRoles: text(source.assistantRoles),
    resumeTemplate: text(source.resumeTemplate),
    newTemplate: text(source.newTemplate),
  };
}

export function normalizeSessionToolPrefs(value: unknown): SessionToolPrefs {
  const source = record(value);
  const builtinSource = record(source.builtins);
  const builtins = Object.fromEntries(
    BUILTIN_SESSION_TOOLS.map((tool) => [tool.id, normalizeBuiltin(builtinSource[tool.id])]),
  ) as Record<SessionProviderId, BuiltinSessionToolPrefs>;

  const custom: CustomSessionTool[] = [];
  const takenIds = new Set<string>(SESSION_PROVIDER_IDS);
  for (const item of Array.isArray(source.custom) ? source.custom : []) {
    if (custom.length >= MAX_CUSTOM_SESSION_TOOLS) break;
    const tool = normalizeCustomSessionTool(item, takenIds);
    if (!tool) continue;
    takenIds.add(tool.id);
    custom.push(tool);
  }

  const known = new Set<string>([...SESSION_PROVIDER_IDS, ...custom.map((tool) => tool.id)]);
  const order: string[] = [];
  for (const id of Array.isArray(source.order) ? source.order : []) {
    if (typeof id === "string" && known.has(id) && !order.includes(id)) order.push(id);
  }
  for (const id of known) if (!order.includes(id)) order.push(id);

  return { version: 1, order, builtins, custom };
}

export function findCustomSessionTool(prefs: SessionToolPrefs, id: string) {
  return prefs.custom.find((tool) => tool.id === id);
}

export function isSessionToolEnabled(prefs: SessionToolPrefs, id: string) {
  if (isBuiltinSessionToolId(id)) return prefs.builtins[id].enabled;
  return findCustomSessionTool(prefs, id)?.enabled === true;
}

export function enabledSessionToolIds(prefs: SessionToolPrefs) {
  return prefs.order.filter((id) => isSessionToolEnabled(prefs, id));
}

export function moveSessionTool(order: readonly string[], id: string, delta: number) {
  const index = order.indexOf(id);
  const target = index + delta;
  if (index < 0 || target < 0 || target >= order.length) return [...order];
  const next = [...order];
  next.splice(index, 1);
  next.splice(target, 0, id);
  return next;
}

export function quoteShellArgument(value: string) {
  return /^[a-zA-Z0-9_./:@=-]+$/.test(value) ? value : `'${value.replace(/'/g, "'\\''")}'`;
}

/** 渲染命令模板：{id}/{cwd} 替换为经 shell 转义的值，缺省值替换为空。 */
export function renderCommandTemplate(template: string, vars: { id?: string; cwd?: string }) {
  return template
    .replace(/\{(id|cwd)\}/g, (_match, name: "id" | "cwd") => {
      const value = vars[name];
      return value ? quoteShellArgument(value) : "";
    })
    .trim();
}

export function sessionToolTemplates(prefs: SessionToolPrefs, id: string) {
  if (isBuiltinSessionToolId(id)) {
    const definition = builtinSessionTool(id);
    const custom = prefs.builtins[id];
    return {
      resume: custom.resumeTemplate || definition.resumeTemplate,
      newSession: custom.newTemplate || definition.newTemplate,
    };
  }
  const tool = findCustomSessionTool(prefs, id);
  const newSession = tool?.newTemplate ?? "";
  return { resume: tool?.resumeTemplate || newSession, newSession };
}

export function buildSessionToolResumeCommand(prefs: SessionToolPrefs, id: string, sessionId: string, cwd?: string) {
  return renderCommandTemplate(sessionToolTemplates(prefs, id).resume, { id: sessionId, cwd });
}

export function buildSessionToolNewCommand(prefs: SessionToolPrefs, id: string, cwd?: string) {
  return renderCommandTemplate(sessionToolTemplates(prefs, id).newSession, { cwd });
}

/** 后端 set_ai_session_roots 的参数：只包含填写了目录覆盖的内置工具。 */
export function builtinRootsPayload(prefs: SessionToolPrefs): Record<string, string> {
  const roots: Record<string, string> = {};
  for (const tool of BUILTIN_SESSION_TOOLS) {
    const dir = prefs.builtins[tool.id].dir;
    if (dir) roots[tool.id] = dir;
  }
  return roots;
}

/**
 * 需要注入终端的环境变量。resolvedRoots 是后端返回的绝对路径（~ 已展开），
 * 终端的 extraEnv 不会再做 ~ 展开。
 */
export function sessionToolEnv(id: string, resolvedRoots: Readonly<Record<string, string>>): Record<string, string> {
  if (!isBuiltinSessionToolId(id)) return {};
  const definition = builtinSessionTool(id);
  const dir = resolvedRoots[id];
  return definition.dirEnv && dir ? { [definition.dirEnv]: dir } : {};
}

/** 复制命令时的环境变量前缀，例如 `CODEX_HOME=/x/codex `。 */
export function envPrefix(env: Readonly<Record<string, string>>) {
  return Object.entries(env)
    .filter(([name, value]) => ENV_NAME_PATTERN.test(name) && value)
    .map(([name, value]) => `${name}=${quoteShellArgument(value)} `)
    .join("");
}

function unquote(token: string) {
  return token.replace(/^(['"])(.*)\1$/, "$2");
}

/** 从命令模板推断 CLI 进程名，后端据此判断会话是否仍在运行。 */
export function processNameFromTemplate(template: string) {
  for (const raw of template.trim().split(/\s+/)) {
    if (!raw || ENV_ASSIGNMENT_PATTERN.test(raw) || raw === "env" || raw === "exec") continue;
    const token = unquote(raw);
    const name = token.split(/[\\/]/).pop() ?? "";
    if (PROCESS_NAME_PATTERN.test(name) && name !== "." && name !== "..") return name;
    return "";
  }
  return "";
}

export function customToolBackendConfig(tool: CustomSessionTool): CustomSessionBackendConfig {
  const processName =
    processNameFromTemplate(tool.newTemplate) || processNameFromTemplate(tool.resumeTemplate);
  return {
    dir: tool.dir,
    glob: tool.glob,
    format: tool.format,
    fields: { ...tool.fields },
    userRoles: tool.userRoles,
    assistantRoles: tool.assistantRoles,
    ...(processName ? { processName } : {}),
  };
}

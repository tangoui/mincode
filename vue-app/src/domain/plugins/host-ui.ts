// Parsers for the generic host UI requests plugins send through host.editor.open,
// host.view.open, host.diff.open, host.terminal.open, host.quickPick and
// host.inputBox (backend/plugin/host_ui.go). The backend already
// validates; these re-check the event shape before the UI acts on it.
// Prompts (host.quickPick, host.inputBox, approvals, service prompts) are
// parsed by host-prompts.ts, loaded when the first prompt arrives; it shares
// the helpers exported here.

export const PLUGIN_ID = /^[a-z0-9][a-z0-9._-]{1,127}$/;
const VIEW_ID = /^[A-Za-z0-9._-]{1,64}$/;
const ENTRY = /^[A-Za-z0-9][A-Za-z0-9._:/-]{0,127}$/;
export const PROMPT_ID = /^prompt-[0-9]+-[0-9]+$/;
/** Origin of the prompts the host shows for service calls it makes (serviceCallHostOwner); never a plugin ID. */
export const HOST_PROMPT_ORIGIN = "mincode:host";
const MAX_DIFF_SIDE = 256 * 1024;
// C0/C1 controls, DEL, zero-width and bidirectional formatting characters.
// eslint-disable-next-line no-control-regex
const UNSAFE_COMMAND = /[\u0000-\u001f\u007f-\u009f\u200b-\u200f\u202a-\u202e\u2066-\u2069\u061c\ufeff]/;

export function record(value: unknown): Record<string, unknown> | undefined {
  return value && typeof value === "object" && !Array.isArray(value) ? (value as Record<string, unknown>) : undefined;
}

export function text(value: unknown, max: number): string | undefined {
  return typeof value === "string" && value.length <= max ? value : undefined;
}

/**
 * Display text: the backend already bounds it in runes, so a longer string
 * (for example many emoji, which count twice in UTF-16) is cut here instead of
 * dropping the whole request.
 */
export function label(value: unknown, max: number): string | undefined {
  if (typeof value !== "string") return undefined;
  const runes = Array.from(value);
  return runes.length <= max ? value : runes.slice(0, max).join("");
}

export function utf8Length(value: string) {
  return new TextEncoder().encode(value).byteLength;
}

// Monaco language ids → file extensions the diff editor detects languages by.
const LANGUAGE_EXTENSIONS: Record<string, string> = {
  typescript: "ts", javascript: "js", typescriptreact: "tsx", javascriptreact: "jsx", python: "py", go: "go",
  java: "java", kotlin: "kt", rust: "rs", ruby: "rb", php: "php", csharp: "cs", cpp: "cpp", c: "c",
  shell: "sh", bash: "sh", powershell: "ps1", markdown: "md", plaintext: "txt", yaml: "yaml", json: "json",
  xml: "xml", html: "html", css: "css", scss: "scss", less: "less", sql: "sql", dockerfile: "dockerfile",
  ini: "ini", toml: "toml", swift: "swift", dart: "dart", lua: "lua", vue: "vue",
};

/** Model path for a host.diff.open dialog: `language` may be a Monaco language id or an extension. */
export function diffModelPath(key: number | string, language?: string) {
  const id = (language ?? "").toLowerCase();
  const extension = LANGUAGE_EXTENSIONS[id] ?? (/^[a-z0-9+#_-]{1,16}$/.test(id) ? id : "txt");
  return extension === "dockerfile" ? `plugin-diff/${key}/Dockerfile` : `plugin-diff/${key}.${extension}`;
}

function position(value: unknown): number | undefined {
  return typeof value === "number" && Number.isInteger(value) && value > 0 && value <= 10_000_000 ? value : undefined;
}

export interface Origin {
  pluginId: string;
  pluginName: string;
}

export function origin(raw: Record<string, unknown>, allowHost = false): Origin | undefined {
  const pluginId = text(raw.pluginId, 128);
  if (!pluginId || !(PLUGIN_ID.test(pluginId) || (allowHost && pluginId === HOST_PROMPT_ORIGIN))) return undefined;
  return { pluginId, pluginName: label(raw.pluginName, 200)?.trim() || pluginId };
}

export interface PluginEditorOpenRequest extends Origin {
  root: string;
  path: string;
  line?: number;
  column?: number;
}

export function parsePluginEditorOpen(value: unknown): PluginEditorOpenRequest | undefined {
  const raw = record(value);
  const from = raw && origin(raw);
  const root = raw && text(raw.root, 4096);
  const path = raw && text(raw.path, 4096);
  if (!raw || !from || !root || !path || path.startsWith("/") || path.split("/").includes("..")) return undefined;
  const request: PluginEditorOpenRequest = { ...from, root, path };
  const line = position(raw.line);
  const column = position(raw.column);
  if (line) request.line = line;
  if (line && column) request.column = column;
  return request;
}

export interface PluginViewOpenHostRequest extends Origin {
  targetPluginId: string;
  viewId: string;
  entry?: string;
  params?: Record<string, string | number | boolean>;
}

export function parsePluginViewOpen(value: unknown): PluginViewOpenHostRequest | undefined {
  const raw = record(value);
  const from = raw && origin(raw);
  const target = raw && text(raw.targetPluginId, 128);
  const viewId = raw && text(raw.viewId, 64);
  if (!raw || !from || !target || !PLUGIN_ID.test(target) || !viewId || !VIEW_ID.test(viewId)) return undefined;
  const request: PluginViewOpenHostRequest = { ...from, targetPluginId: target, viewId };
  if (raw.entry !== undefined) {
    const entry = text(raw.entry, 128);
    if (!entry || !ENTRY.test(entry)) return undefined;
    request.entry = entry;
  }
  const params = record(raw.params);
  if (raw.params !== undefined) {
    if (!params || !request.entry || Object.keys(params).length > 16) return undefined;
    const out: Record<string, string | number | boolean> = {};
    for (const [key, item] of Object.entries(params)) {
      if (!key || key.length > 128) return undefined;
      if (typeof item === "string" && item.length <= 1024) out[key] = item;
      else if (typeof item === "boolean" || (typeof item === "number" && Number.isFinite(item))) out[key] = item;
      else return undefined;
    }
    request.params = out;
  }
  return request;
}

export interface PluginDiffSide {
  title: string;
  content: string;
}

export interface PluginDiffRequest extends Origin {
  title: string;
  language?: string;
  left: PluginDiffSide;
  right: PluginDiffSide;
}

function diffSide(value: unknown): PluginDiffSide | undefined {
  const raw = record(value);
  const content = raw && text(raw.content, MAX_DIFF_SIDE);
  if (!raw || content === undefined) return undefined;
  return { title: label(raw.title, 200) ?? "", content };
}

export function parsePluginDiffOpen(value: unknown): PluginDiffRequest | undefined {
  const raw = record(value);
  const from = raw && origin(raw);
  const title = raw && label(raw.title, 200)?.trim();
  const left = raw && diffSide(raw.left);
  const right = raw && diffSide(raw.right);
  if (!raw || !from || !title || !left || !right) return undefined;
  const request: PluginDiffRequest = { ...from, title, left, right };
  const language = text(raw.language, 32);
  if (language && /^[A-Za-z0-9+#._-]+$/.test(language)) request.language = language;
  return request;
}

export interface PluginTerminalOpenRequest extends Origin {
  title?: string;
  cwd?: string;
  command?: string;
}

export function parsePluginTerminalOpen(value: unknown): PluginTerminalOpenRequest | undefined {
  const raw = record(value);
  const from = raw && origin(raw);
  if (!raw || !from) return undefined;
  const request: PluginTerminalOpenRequest = { ...from };
  const title = label(raw.title, 200)?.trim();
  const cwd = text(raw.cwd, 4096);
  const command = text(raw.command, 4096)?.trim();
  // Same rule as the backend: what the confirmation shows is exactly what runs.
  if (command && UNSAFE_COMMAND.test(command)) return undefined;
  if (title) request.title = title;
  if (cwd) request.cwd = cwd;
  if (command) request.command = command;
  return request;
}

/** The prompt id of an event that could not be parsed, so it can still be answered as cancelled. */
export function promptEventId(value: unknown): string | undefined {
  const id = record(value)?.id;
  return typeof id === "string" && PROMPT_ID.test(id) ? id : undefined;
}

export interface PluginProjectOpenRequest extends Origin {
  path: string;
}

export function parsePluginProjectOpen(value: unknown): PluginProjectOpenRequest | undefined {
  const raw = record(value);
  const from = raw && origin(raw);
  const path = raw && text(raw.path, 4096);
  if (!from || !path || !(/^(?:\/|[A-Za-z]:[\\/]|\\\\)/.test(path)) || path.includes("\r") || path.includes("\n") || path.includes(String.fromCharCode(0))) return undefined;
  return { ...from, path };
}

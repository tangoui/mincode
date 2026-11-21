/**
 * Plugin file and project templates (contribution point `fileTemplates`),
 * pure logic.
 *
 * Trust boundary: the Go host (backend/plugin/contrib_file_templates.go)
 * validates entries and serves each `.tmpl` source by the sha256 of its
 * canonical bytes; this module re-checks entries defensively (the "frontend"
 * layer), renders the fixed `{{mincode.<variable>}}` allowlist (any other
 * `{{...}}` stays literal) and re-runs every target path rule after
 * substitution. There are no hooks, scripts or expressions. The write plan is
 * applied by services/file-template-service.ts with create-only writes.
 */
import contributionContract from "../plugins/contribution-points.json" with { type: "json" };
import type { PointContribution } from "@/domain/plugins/contributions";

export const FILE_TEMPLATES_CONTRIBUTION_POINT = "fileTemplates";

// Mirrors `points.fileTemplates` in plugins/api/contribution-points.json
// (the host enforces them first).
const MAX_ENTRIES = contributionContract.points.fileTemplates.maxEntries;
const ID_PATTERN = /^[a-z0-9][a-z0-9-]{0,31}$/;
const TITLE_MAX_RUNES = 64;
const DESCRIPTION_MAX_RUNES = 256;
const CATEGORY_MAX_RUNES = 24;
export const FILE_TEMPLATE_FILES_MAX = 64;
export const FILE_TEMPLATE_FILE_MAX_BYTES = 262144;
export const FILE_TEMPLATE_MAX_BYTES = 2097152;
const TARGET_PATH_MAX_LENGTH = 200;
/** Rendered paths may grow through substitution; this caps the result. */
const RENDERED_PATH_MAX_LENGTH = 512;
const TARGET_DEPTH_MAX = 8;
export const FILE_TEMPLATE_NAME_MAX_LENGTH = 64;
const DEFAULT_NAME_PATTERN = /^[A-Za-z0-9][A-Za-z0-9._ -]{0,63}$/;
const ASSET_KEY_PATTERN = /^[0-9a-f]{64}$/;
export const FILE_TEMPLATE_VARIABLES = [
  "name", "fileName", "projectName", "date", "year", "nameCamel", "namePascal", "nameKebab", "nameSnake",
] as const;
const DENIED_PATH_SEGMENTS = [".git", ".hg", ".svn", "node_modules", ".mincode"];
const SENSITIVE_FILENAMES = [
  ".env", ".env.local", "server.pem", "server.key", "id_rsa", "id_ed25519", "cert.p12", "cert.pfx", "vault.kdbx",
  ".npmrc", ".netrc", ".pgpass", "app.keystore", "app.jks", "credentials", "terraform.tfstate",
];
const VARIABLE_TOKEN = /\{\{mincode\.([^{}]*)\}\}/g;
const SEGMENT_CHARACTERS = /^[A-Za-z0-9._@+-]*$/;

export type FileTemplateVariable = (typeof FILE_TEMPLATE_VARIABLES)[number];
export type FileTemplateVariables = Record<FileTemplateVariable, string>;
export type FileTemplateKind = "file" | "project";

export interface PluginFileTemplateFile {
  /** Unrendered target path template. */
  path: string;
  /** sha256 content key of the `.tmpl` asset. */
  source: string;
  size: number;
}

export interface RenderableFileTemplate {
  kind: FileTemplateKind;
  files: PluginFileTemplateFile[];
}

export interface PluginFileTemplate extends RenderableFileTemplate {
  origin?: "plugin";
  /** `<pluginId>.<id>`. */
  key: string;
  pluginId: string;
  pluginName: string;
  id: string;
  kind: FileTemplateKind;
  title: string;
  titleLocalized?: Record<string, string>;
  description: string;
  descriptionLocalized?: Record<string, string>;
  category: string;
  defaultName: string;
  files: PluginFileTemplateFile[];
}

export interface PluginFileTemplateIssue {
  pluginId: string;
  stage: "host" | "frontend";
  entry: number;
  id?: string;
  message: string;
}

export interface PluginFileTemplateRegistry {
  templates: PluginFileTemplate[];
  issues: PluginFileTemplateIssue[];
}

function objectValue(value: unknown): Record<string, unknown> | undefined {
  return typeof value === "object" && value !== null && !Array.isArray(value) ? (value as Record<string, unknown>) : undefined;
}

function isHiddenCodePoint(code: number): boolean {
  return (
    code < 0x20 ||
    (code >= 0x7f && code <= 0x9f) ||
    code === 0x2028 ||
    code === 0x2029 ||
    (code >= 0x202a && code <= 0x202e) ||
    (code >= 0x2066 && code <= 0x2069) ||
    (code >= 0x200b && code <= 0x200f) ||
    code === 0xfeff
  );
}

function hasHiddenCharacters(value: string): boolean {
  for (const char of value) if (isHiddenCodePoint(char.codePointAt(0) ?? 0)) return true;
  return false;
}

function validLine(value: unknown, maxRunes: number): value is string {
  return typeof value === "string" && Boolean(value.trim()) && [...value].length <= maxRunes && !hasHiddenCharacters(value);
}

function localizedMap(value: unknown, maxRunes: number): Record<string, string> | undefined {
  const record = objectValue(value);
  if (!record) return undefined;
  const result: Record<string, string> = {};
  for (const [locale, text] of Object.entries(record).slice(0, 16)) {
    if (locale.trim() && locale.length <= 32 && validLine(text, maxRunes)) result[locale] = text.trim();
  }
  return Object.keys(result).length ? result : undefined;
}

function isFileTemplateVariable(name: string): name is FileTemplateVariable {
  return (FILE_TEMPLATE_VARIABLES as readonly string[]).includes(name);
}

function isDeniedSegment(segment: string) {
  const lower = segment.toLowerCase();
  return DENIED_PATH_SEGMENTS.some((denied) => denied === lower);
}

/** A sensitive filename (case-insensitive): templates never create these. */
export function isSensitiveTemplateFilename(name: string) {
  const lower = name.toLowerCase();
  return SENSITIVE_FILENAMES.some((sensitive) => sensitive === lower);
}

/** Checks an unrendered target path (the host rules); returns an error message or null. */
export function templateTargetPathError(path: string): string | null {
  if (!path || path.trim() !== path) return "target path is empty or has surrounding spaces";
  if (path.length > TARGET_PATH_MAX_LENGTH) return "target path is too long";
  if (path.startsWith("/") || path.includes("\\") || (path.length >= 2 && path[1] === ":")) return "target path must be relative";
  const segments = path.split("/");
  if (segments.length > TARGET_DEPTH_MAX) return "target path is too deep";
  for (const segment of segments) {
    if (!segment || segment === "." || segment === "..") return "target path has an empty, \".\" or \"..\" segment";
    for (const match of segment.matchAll(VARIABLE_TOKEN)) {
      if (!isFileTemplateVariable(match[1] ?? "")) return `target path uses unknown variable {{mincode.${match[1]}}}`;
    }
    if (!SEGMENT_CHARACTERS.test(segment.replace(VARIABLE_TOKEN, ""))) return "target path has unsupported characters";
    if (isDeniedSegment(segment)) return `target path must not use the ${segment} directory`;
  }
  if (isSensitiveTemplateFilename(segments[segments.length - 1] ?? "")) return "target path names a sensitive file";
  return null;
}

/** Checks a rendered target path; substituted values were already sanitized. */
export function renderedTargetPathError(path: string): string | null {
  if (!path || path.length > RENDERED_PATH_MAX_LENGTH) return "rendered path is empty or too long";
  if (path.startsWith("/") || path.includes("\\") || /^[A-Za-z]:/.test(path)) return "rendered path must be relative";
  const segments = path.split("/");
  if (segments.length > TARGET_DEPTH_MAX) return "rendered path is too deep";
  for (const segment of segments) {
    if (!segment.trim() || segment === "." || segment === ".." || segment.trim() !== segment) return "rendered path has an invalid segment";
    if (hasHiddenCharacters(segment) || segment.includes("{{mincode.")) return "rendered path has an invalid segment";
    if (isDeniedSegment(segment)) return `rendered path must not use the ${segment} directory`;
  }
  if (isSensitiveTemplateFilename(segments[segments.length - 1] ?? "")) return "rendered path names a sensitive file";
  return null;
}

/**
 * A user-entered name (file base name or new project folder): non-empty,
 * at most 64 characters, no "/", "\\", "..", control or bidi characters,
 * not "." and not a denied or sensitive name.
 */
export function isValidTemplateName(value: string): boolean {
  const name = value.trim();
  return (
    Boolean(name) &&
    name.length <= FILE_TEMPLATE_NAME_MAX_LENGTH &&
    name !== "." &&
    !name.includes("..") &&
    !/[\\/]/.test(name) &&
    !hasHiddenCharacters(name) &&
    !isDeniedSegment(name) &&
    !isSensitiveTemplateFilename(name)
  );
}

function words(value: string): string[] {
  return value
    .replace(/([a-z0-9])([A-Z])/g, "$1 $2")
    .replace(/([A-Z]+)([A-Z][a-z])/g, "$1 $2")
    .split(/[^A-Za-z0-9]+/)
    .filter(Boolean);
}

function localDate(now: Date) {
  const pad = (value: number) => String(value).padStart(2, "0");
  return `${now.getFullYear()}-${pad(now.getMonth() + 1)}-${pad(now.getDate())}`;
}

/**
 * The substitution values: `name` as typed (trimmed), `fileName` the same,
 * `projectName` the project folder name, `date` the local YYYY-MM-DD, `year`
 * and the camel/Pascal/kebab/snake forms of `name`.
 */
export function templateVariables(name: string, projectName: string, now = new Date()): FileTemplateVariables {
  const trimmed = name.trim();
  const tokens = words(trimmed);
  const parts = tokens.length ? tokens : ["template"];
  if (/^[0-9]/.test(parts[0] ?? "")) parts.unshift("template");
  const lower = parts.map((part) => part.toLowerCase());
  const capital = lower.map((part) => part.charAt(0).toUpperCase() + part.slice(1));
  return {
    name: trimmed,
    fileName: trimmed,
    projectName: projectName.trim(),
    date: localDate(now),
    year: String(now.getFullYear()),
    nameCamel: lower.length ? lower[0] + capital.slice(1).join("") : trimmed,
    namePascal: capital.length ? capital.join("") : trimmed,
    nameKebab: lower.length ? lower.join("-") : trimmed,
    nameSnake: lower.length ? lower.join("_") : trimmed,
  };
}

/** Replaces only allowlisted `{{mincode.<variable>}}` tokens; any other `{{...}}` stays literal. */
export function renderTemplateText(text: string, variables: FileTemplateVariables): string {
  return text.replace(VARIABLE_TOKEN, (token, name: string) => (isFileTemplateVariable(name) ? variables[name] : token));
}

function parseEntry(contribution: PointContribution, value: unknown): PluginFileTemplate | string {
  const record = objectValue(value);
  if (!record) return "malformed file template entry";
  const id = typeof record.id === "string" ? record.id : "";
  if (!ID_PATTERN.test(id)) return "invalid file template id";
  const kind = record.kind;
  if (kind !== "file" && kind !== "project") return "invalid file template kind";
  if (!validLine(record.title, TITLE_MAX_RUNES)) return "invalid file template title";
  const files = record.files;
  if (!Array.isArray(files) || files.length === 0 || files.length > FILE_TEMPLATE_FILES_MAX) return "invalid file template files";
  if (kind === "file" && files.length !== 1) return "a file template must have exactly one file";
  const parsedFiles: PluginFileTemplateFile[] = [];
  const seen = new Set<string>();
  let total = 0;
  for (const candidate of files) {
    const file = objectValue(candidate);
    if (!file || typeof file.path !== "string" || typeof file.source !== "string" || !ASSET_KEY_PATTERN.test(file.source)) {
      return "malformed file template file";
    }
    const error = templateTargetPathError(file.path);
    if (error) return error;
    const folded = file.path.toLowerCase();
    if (seen.has(folded)) return "file template lists a path twice";
    seen.add(folded);
    const size = typeof file.size === "number" && Number.isInteger(file.size) && file.size >= 0 ? file.size : -1;
    if (size < 0 || size > FILE_TEMPLATE_FILE_MAX_BYTES) return "invalid file template size";
    total += size;
    if (total > FILE_TEMPLATE_MAX_BYTES) return "file template is too large";
    parsedFiles.push({ path: file.path, source: file.source, size });
  }
  const description = validLine(record.description, DESCRIPTION_MAX_RUNES) ? record.description.trim() : "";
  const category = validLine(record.category, CATEGORY_MAX_RUNES) ? record.category.trim() : "";
  const defaultName = typeof record.defaultName === "string" && DEFAULT_NAME_PATTERN.test(record.defaultName) && !record.defaultName.includes("..")
    ? record.defaultName
    : "";
  const titleLocalized = localizedMap(record.titleLocalized, TITLE_MAX_RUNES);
  const descriptionLocalized = localizedMap(record.descriptionLocalized, DESCRIPTION_MAX_RUNES);
  return {
    key: `${contribution.pluginId}.${id}`,
    pluginId: contribution.pluginId,
    pluginName: contribution.pluginName.trim() || contribution.pluginId,
    id,
    kind,
    title: (record.title as string).trim(),
    ...(titleLocalized ? { titleLocalized } : {}),
    description,
    ...(descriptionLocalized ? { descriptionLocalized } : {}),
    category,
    defaultName,
    files: parsedFiles,
  };
}

/**
 * Normalizes the `fileTemplates` snapshot. Templates only create files the
 * user confirms, so every enabled plugin contributes regardless of the
 * workspace. Invalid entries are skipped and reported.
 */
export function buildPluginFileTemplateRegistry(contributions: readonly PointContribution[]): PluginFileTemplateRegistry {
  const templates: PluginFileTemplate[] = [];
  const issues: PluginFileTemplateIssue[] = [];
  for (const contribution of contributions) {
    for (const issue of contribution.point.issues) {
      issues.push({ pluginId: contribution.pluginId, stage: "host", entry: issue.entry, message: issue.message, ...(issue.id ? { id: issue.id } : {}) });
    }
    if (contribution.point.entries.length > MAX_ENTRIES) issues.push({pluginId: contribution.pluginId, stage: "frontend", entry: MAX_ENTRIES, message: "file template entry limit exceeded"});
    const seen = new Set<string>();
    contribution.point.entries.slice(0, MAX_ENTRIES).forEach((value, entry) => {
      const parsed = parseEntry(contribution, value);
      if (typeof parsed === "string" || seen.has(parsed.id)) {
        const id = objectValue(value)?.id;
        issues.push({
          pluginId: contribution.pluginId,
          stage: "frontend",
          entry,
          message: typeof parsed === "string" ? parsed : "duplicate file template id",
          ...(typeof id === "string" && id ? { id } : {}),
        });
        return;
      }
      seen.add(parsed.id);
      templates.push(parsed);
    });
  }
  return { templates, issues };
}

export interface FileTemplateTarget {
  /** Rendered path relative to the template's target folder. */
  relativePath: string;
  source: string;
  size: number;
}

/**
 * Renders every target path and re-runs the path rules; paths must stay
 * unique (case-insensitively). Throws with a message on the first problem.
 */
export function renderTargetPaths(template: RenderableFileTemplate, variables: FileTemplateVariables): FileTemplateTarget[] {
  const seen = new Set<string>();
  return template.files.map((file) => {
    const relativePath = renderTemplateText(file.path, variables);
    const error = renderedTargetPathError(relativePath);
    if (error) throw new Error(`${relativePath}: ${error}`);
    const folded = relativePath.toLowerCase();
    if ([...seen].some(old => old === folded || old.startsWith(folded + "/") || folded.startsWith(old + "/"))) throw new Error(`${relativePath}: conflicting target`);
    seen.add(folded);
    return { relativePath, source: file.source, size: file.size };
  });
}

export interface FileTemplateWrite {
  /** Path passed to local_fs_write_text_file, relative to its projectRoot. */
  filePath: string;
  relativePath: string;
  content: string;
  bytes: number;
}

function joinRelative(parent: string, child: string) {
  const base = parent.replace(/^\/+|\/+$/g, "");
  return base ? `${base}/${child}` : child;
}

/**
 * Builds the ordered write plan. `parent` is the folder (relative to the
 * projectRoot the writes use) that receives the files; `sources` maps asset
 * keys to their verified text.
 */
export function planTemplateWrites(
  template: RenderableFileTemplate,
  variables: FileTemplateVariables,
  parent: string,
  sources: ReadonlyMap<string, string>,
): FileTemplateWrite[] {
  const encoder = new TextEncoder();
  return renderTargetPaths(template, variables).map((target) => {
    const text = sources.get(target.source);
    if (text === undefined) throw new Error(`${target.relativePath}: template source is unavailable`);
    const content = renderTemplateText(text, variables);
    return {
      filePath: joinRelative(parent, target.relativePath),
      relativePath: target.relativePath,
      content,
      bytes: encoder.encode(content).length,
    };
  });
}

/** Install preview: the declared templates with their kind and file count. */
export function fileTemplatePreviewSummary(contributes: unknown): Array<{ title: string; kind: FileTemplateKind; files: number }> {
  const entries = objectValue(contributes)?.[FILE_TEMPLATES_CONTRIBUTION_POINT];
  if (!Array.isArray(entries)) return [];
  return entries.slice(0, MAX_ENTRIES).flatMap((entry) => {
    const record = objectValue(entry);
    if (!record || !validLine(record.title, TITLE_MAX_RUNES)) return [];
    const kind: FileTemplateKind = record.kind === "project" ? "project" : "file";
    return [{ title: record.title.trim(), kind, files: Array.isArray(record.files) ? record.files.length : 0 }];
  });
}

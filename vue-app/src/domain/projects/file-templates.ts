/** Validates and renders project templates for create-only writes. */
export const FILE_TEMPLATE_FILES_MAX = 64;
export const FILE_TEMPLATE_FILE_MAX_BYTES = 262144;
export const FILE_TEMPLATE_MAX_BYTES = 2097152;
const TARGET_PATH_MAX_LENGTH = 200;
/** Rendered paths may grow through substitution; this caps the result. */
const RENDERED_PATH_MAX_LENGTH = 512;
const TARGET_DEPTH_MAX = 8;
export const FILE_TEMPLATE_NAME_MAX_LENGTH = 64;
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

export interface FileTemplateFile {
  /** Unrendered target path template. */
  path: string;
  /** sha256 content key of the `.tmpl` asset. */
  source: string;
  size: number;
}

export interface RenderableFileTemplate {
  kind: FileTemplateKind;
  files: FileTemplateFile[];
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

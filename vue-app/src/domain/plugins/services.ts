/**
 * Pure model of the `services` contribution point (process plugins only).
 * The Go host (backend/plugin/contrib_services.go) validates the
 * manifest entries and lists the active providers; the frontend revalidates
 * what it is given with the same rules and decides which services the file
 * tree, the editor selection, the command palette and the newer surfaces
 * (SFTP files, result sets, HTTP responses, terminal selections:
 * servicesForSurface) offer. The other mirrors
 * are the CLI (tools/mincode-cli/services.go) and the cloud
 * (MarketplaceArtifact.php assertServicesEntry). Nothing here touches Vue or
 * IPC, and service-surfaces.ts is imported type-only so the menu target
 * builders stay out of the entry chunk.
 */
import contributionContract from "./contribution-points.json" with { type: "json" };
import { parseCommandInputForm } from "@/domain/commands/input-schema";
import { serviceNameError } from "@/domain/plugins/service-name";
import { resolvePluginTitle } from "@/domain/plugins/title";
import type { ServiceSurfaceTarget } from "@/domain/plugins/service-surfaces";

export { serviceNameError };

export const SERVICES_CONTRIBUTION_POINT = "services";

const spec = contributionContract.points.services;

/** Read from the contract so a raised limit never silently drops services here. */
export const SERVICE_LIMITS = {
  maxEntries: spec.maxEntries,
  titleMaxRunes: spec.limits.titleMaxRunes,
  descriptionMaxRunes: spec.limits.descriptionMaxRunes,
  localesMax: spec.limits.localesMax,
  serviceMaxLength: spec.limits.serviceMaxLength,
  acceptsMax: spec.limits.acceptsMax,
  textDefaultBytes: spec.limits.textDefaultBytes,
  textMaxBytes: spec.limits.textMaxBytes,
  envelopeReserveBytes: spec.limits.envelopeReserveBytes,
  fileMaxBytes: spec.limits.fileMaxBytes,
  filesDefault: spec.limits.filesDefault,
  filesMax: spec.limits.filesMax,
  paramsMaxBytes: spec.limits.paramsMaxBytes,
  paramsMaxProperties: spec.limits.paramsMaxProperties,
  timeoutDefaultMs: spec.limits.timeoutDefaultMs,
  timeoutMaxMs: spec.limits.timeoutMaxMs,
  dbResultMaxRows: spec.limits.dbResultMaxRows,
  dbResultMaxBytes: spec.limits.dbResultMaxBytes,
  httpResponseMaxBytes: spec.limits.httpResponseMaxBytes,
  httpResponseHeadersMax: spec.limits.httpResponseHeadersMax,
  httpHeaderValueMaxBytes: spec.limits.httpHeaderValueMaxBytes,
  surfaceContextMaxBytes: spec.limits.surfaceContextMaxBytes,
  remoteContextPathMaxBytes: spec.limits.remoteContextPathMaxBytes,
  remoteInputStallMs: spec.limits.remoteInputStallMs,
} as const;

export const SERVICE_INPUT_KINDS = ["none", "text", "file", "files"] as const;
export type ServiceInputKind = (typeof SERVICE_INPUT_KINDS)[number];
export const SERVICE_OUTPUT_KINDS = ["none", "text", "json", "file", "files"] as const;
export type ServiceOutputKind = (typeof SERVICE_OUTPUT_KINDS)[number];
/** Every surface this frontend can offer a service on (contract order is read below). */
const KNOWN_SERVICE_SURFACES = ["fileMenu", "selection", "palette", "sftpFile", "dbResult", "httpResponse", "terminalSelection"] as const;
export type ServiceSurface = (typeof KNOWN_SERVICE_SURFACES)[number];
/**
 * The contract's surfaces, in contract order. A test checks that the
 * frontend knows every one of them (a surface it cannot offer would
 * otherwise drop the entries that declare it).
 */
export const SERVICE_SURFACES: readonly ServiceSurface[] = spec.lists.surfaces.filter((surface): surface is ServiceSurface =>
  (KNOWN_SERVICE_SURFACES as readonly string[]).includes(surface));
/** The surfaces a manifest gets when it omits `surfaces` (design ruling 2: newer surfaces are opt-in). */
const DEFAULT_SERVICE_SURFACES: readonly ServiceSurface[] = ["fileMenu", "selection", "palette"];
/** The surfaces added in host 0.3.0, which the install preview names. */
export const NEWER_SERVICE_SURFACES = ["sftpFile", "dbResult", "httpResponse", "terminalSelection"] as const satisfies readonly ServiceSurface[];
export type NewerServiceSurface = (typeof NEWER_SERVICE_SURFACES)[number];
/** Contract `httpResponseTextTypes`: the response content types an httpResponse service can receive. */
export const SERVICE_HTTP_RESPONSE_TEXT_TYPES: readonly string[] = spec.lists.httpResponseTextTypes;
export const SERVICE_RISKS = ["read", "write", "dangerous"] as const;
export type ServiceRisk = (typeof SERVICE_RISKS)[number];

/** A normalized input or output shape (accepts/maxBytes/maxFiles: input only; mime: output only). */
export interface ServiceShape {
  kind: ServiceInputKind | ServiceOutputKind;
  accepts?: string[];
  maxBytes?: number;
  maxFiles?: number;
  mime?: string;
}

/** One provider of a service, as listed by the host (`plugin_services_list`). */
export interface PluginServiceEntry {
  pluginId: string;
  pluginName: string;
  entryId: string;
  service: string;
  title: string;
  /** Locale tag → title; shown through serviceEntryTitle. */
  titleLocalized?: Record<string, string>;
  description: string;
  input: ServiceShape;
  output: ServiceShape;
  params?: Record<string, unknown>;
  surfaces: ServiceSurface[];
  risk: ServiceRisk;
  timeoutMs: number;
  isDefault: boolean;
}

/** Providers of one service merged for a menu or the palette. */
export interface ServiceGroup {
  service: string;
  /** Display title: the first provider's, localized when a locale was given. */
  title: string;
  providers: PluginServiceEntry[];
  /** False when no provider can take the input (selection larger than every maxBytes). */
  fits: boolean;
}

/**
 * Built-in extension → MIME table; keep identical to serviceMimeTypes in
 * backend/plugin/service_mime.go (a test compares them). MIME
 * accepts match by extension only, content is never sniffed.
 */
export const SERVICE_MIME_TYPES: Readonly<Record<string, string>> = Object.freeze({
  // Images
  ".png": "image/png",
  ".jpg": "image/jpeg",
  ".jpeg": "image/jpeg",
  ".gif": "image/gif",
  ".webp": "image/webp",
  ".bmp": "image/bmp",
  ".tif": "image/tiff",
  ".tiff": "image/tiff",
  ".svg": "image/svg+xml",
  ".ico": "image/x-icon",
  ".heic": "image/heic",
  ".heif": "image/heif",
  ".avif": "image/avif",
  ".jxl": "image/jxl",
  ".psd": "image/vnd.adobe.photoshop",
  ".djvu": "image/vnd.djvu",
  // Audio
  ".mp3": "audio/mpeg",
  ".wav": "audio/wav",
  ".ogg": "audio/ogg",
  ".oga": "audio/ogg",
  ".opus": "audio/opus",
  ".flac": "audio/flac",
  ".aac": "audio/aac",
  ".m4a": "audio/mp4",
  ".weba": "audio/webm",
  ".mid": "audio/midi",
  ".midi": "audio/midi",
  ".aif": "audio/aiff",
  ".aiff": "audio/aiff",
  ".amr": "audio/amr",
  // Video
  ".mp4": "video/mp4",
  ".m4v": "video/mp4",
  ".mov": "video/quicktime",
  ".webm": "video/webm",
  ".mkv": "video/x-matroska",
  ".avi": "video/x-msvideo",
  ".wmv": "video/x-ms-wmv",
  ".flv": "video/x-flv",
  ".mpeg": "video/mpeg",
  ".mpg": "video/mpeg",
  ".ogv": "video/ogg",
  ".3gp": "video/3gpp",
  // Fonts
  ".ttf": "font/ttf",
  ".otf": "font/otf",
  ".woff": "font/woff",
  ".woff2": "font/woff2",
  ".ttc": "font/collection",
  // Documents
  ".pdf": "application/pdf",
  ".ofd": "application/ofd",
  ".rtf": "application/rtf",
  ".doc": "application/msword",
  ".dot": "application/msword",
  ".docx": "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
  ".dotx": "application/vnd.openxmlformats-officedocument.wordprocessingml.template",
  ".docm": "application/vnd.ms-word.document.macroenabled.12",
  ".xls": "application/vnd.ms-excel",
  ".xlsx": "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
  ".xltx": "application/vnd.openxmlformats-officedocument.spreadsheetml.template",
  ".xlsm": "application/vnd.ms-excel.sheet.macroenabled.12",
  ".xlsb": "application/vnd.ms-excel.sheet.binary.macroenabled.12",
  ".ppt": "application/vnd.ms-powerpoint",
  ".pptx": "application/vnd.openxmlformats-officedocument.presentationml.presentation",
  ".ppsx": "application/vnd.openxmlformats-officedocument.presentationml.slideshow",
  ".potx": "application/vnd.openxmlformats-officedocument.presentationml.template",
  ".pptm": "application/vnd.ms-powerpoint.presentation.macroenabled.12",
  ".vsdx": "application/vnd.ms-visio.drawing",
  ".odt": "application/vnd.oasis.opendocument.text",
  ".ods": "application/vnd.oasis.opendocument.spreadsheet",
  ".odp": "application/vnd.oasis.opendocument.presentation",
  ".odg": "application/vnd.oasis.opendocument.graphics",
  ".odf": "application/vnd.oasis.opendocument.formula",
  ".ott": "application/vnd.oasis.opendocument.text-template",
  // E-books
  ".epub": "application/epub+zip",
  ".mobi": "application/x-mobipocket-ebook",
  ".azw": "application/vnd.amazon.ebook",
  ".azw3": "application/vnd.amazon.ebook",
  ".fb2": "application/x-fictionbook+xml",
  ".cbz": "application/vnd.comicbook+zip",
  // Archives
  ".zip": "application/zip",
  ".tar": "application/x-tar",
  ".gz": "application/gzip",
  ".tgz": "application/gzip",
  ".bz2": "application/x-bzip2",
  ".xz": "application/x-xz",
  ".zst": "application/zstd",
  ".7z": "application/x-7z-compressed",
  ".rar": "application/vnd.rar",
  ".jar": "application/java-archive",
  ".iso": "application/x-iso9660-image",
  // Text and structured data
  ".txt": "text/plain",
  ".log": "text/plain",
  ".md": "text/markdown",
  ".markdown": "text/markdown",
  ".csv": "text/csv",
  ".tsv": "text/tab-separated-values",
  ".html": "text/html",
  ".htm": "text/html",
  ".xhtml": "application/xhtml+xml",
  ".css": "text/css",
  ".json": "application/json",
  ".jsonl": "application/jsonl",
  ".ndjson": "application/x-ndjson",
  ".geojson": "application/geo+json",
  ".ipynb": "application/x-ipynb+json",
  ".xml": "application/xml",
  ".yaml": "application/yaml",
  ".yml": "application/yaml",
  ".toml": "application/toml",
  ".ics": "text/calendar",
  ".vcf": "text/vcard",
  ".srt": "application/x-subrip",
  ".vtt": "text/vtt",
  ".sql": "application/sql",
  ".sqlite": "application/vnd.sqlite3",
  ".parquet": "application/vnd.apache.parquet",
  ".wasm": "application/wasm",
  // Source code
  ".js": "text/javascript",
  ".mjs": "text/javascript",
  ".cjs": "text/javascript",
  ".py": "text/x-python",
  ".go": "text/x-go",
  ".java": "text/x-java",
  ".kt": "text/x-kotlin",
  ".c": "text/x-c",
  ".h": "text/x-c",
  ".cpp": "text/x-c++",
  ".cc": "text/x-c++",
  ".hpp": "text/x-c++",
  ".cs": "text/x-csharp",
  ".rs": "text/x-rust",
  ".rb": "text/x-ruby",
  ".php": "text/x-php",
  ".swift": "text/x-swift",
  ".sh": "application/x-sh",
});

const ENTRY_ID_PATTERN = /^[a-z0-9][a-z0-9-]{0,31}$/;
const PLUGIN_ID_PATTERN = /^[a-z0-9][a-z0-9._-]{1,127}$/;
const ACCEPT_EXT_PATTERN = /^\.[a-z0-9][a-z0-9_+-]*(\.[a-z0-9][a-z0-9_+-]*)?$/;
const ACCEPT_MIME_PATTERN = /^[a-z][a-z0-9.+-]*\/(\*|[a-z0-9][a-z0-9.+-]*)$/;
const OUTPUT_MIME_PATTERN = /^[a-z][a-z0-9.+-]*\/[a-z0-9][a-z0-9.+-]*$/;
// Go's strings.TrimSpace set (unicode.IsSpace); String.prototype.trim also strips U+FEFF, which the host rejects.
const GO_TRIM = /^[\t\n\v\f\r \u0085\u00a0\u1680\u2000-\u200a\u2028\u2029\u202f\u205f\u3000]+|[\t\n\v\f\r \u0085\u00a0\u1680\u2000-\u200a\u2028\u2029\u202f\u205f\u3000]+$/g;

/** Surfaces that can supply each input kind: a selection gives text, the file menu files, the palette prompts for anything. */
const COMPATIBLE_SURFACES: Record<ServiceInputKind, readonly ServiceSurface[]> = {
  none: ["palette"],
  text: ["selection", "palette", "terminalSelection"],
  file: ["fileMenu", "palette", "sftpFile", "dbResult", "httpResponse"],
  files: ["fileMenu", "palette", "sftpFile", "dbResult", "httpResponse"],
};

/** The host's isUnsafeContributionRune for single-line text, plus lone surrogates (invalid UTF-8 there). */
function hasUnsafeRune(text: string): boolean {
  for (const char of text) {
    const code = char.codePointAt(0) ?? 0;
    if (code < 0x20 || (code >= 0x7f && code <= 0x9f) || code === 0x2028 || code === 0x2029
      || (code >= 0x202a && code <= 0x202e) || (code >= 0x2066 && code <= 0x2069)
      || (code >= 0x200b && code <= 0x200f) || code === 0x2060 || code === 0xfeff
      || (code >= 0xd800 && code <= 0xdfff)) return true;
  }
  return false;
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return Boolean(value && typeof value === "object" && !Array.isArray(value));
}

function isInteger(value: unknown): value is number {
  return typeof value === "number" && Number.isInteger(value);
}

/**
 * Go's strings.ToLower (simple case mapping): the one difference from
 * toLowerCase that can matter here is U+0130, which Go maps to a plain "i".
 */
function goLower(text: string): string {
  return text.replace(/\u0130/g, "i").toLowerCase();
}

export function utf8ByteLength(text: string): number {
  return new TextEncoder().encode(text).length;
}

/** validateContributionLine: trimmed text, or undefined when invalid ("" when optional and empty). */
function line(value: unknown, maxRunes: number, required: boolean): string | undefined {
  if (value === undefined || value === null) return required ? undefined : "";
  if (typeof value !== "string") return undefined;
  const text = value.replace(GO_TRIM, "");
  if (!text) return required ? undefined : "";
  if ([...text].length > maxRunes || hasUnsafeRune(text)) return undefined;
  return text;
}

/**
 * validateContributionLocalized: trimmed titles by locale, undefined when
 * absent or empty, null when invalid (bad locale, title or too many locales).
 */
function localizedTitles(value: unknown): Record<string, string> | undefined | null {
  if (value === undefined || value === null) return undefined;
  if (!isRecord(value)) return null;
  const entries = Object.entries(value);
  if (entries.length > SERVICE_LIMITS.localesMax) return null;
  const titles: Record<string, string> = {};
  for (const [locale, title] of entries) {
    if (!locale.replace(GO_TRIM, "") || utf8ByteLength(locale) > 32 || hasUnsafeRune(locale)) return null;
    const text = line(title, SERVICE_LIMITS.titleMaxRunes, true);
    if (text === undefined) return null;
    titles[locale] = text;
  }
  return entries.length ? titles : undefined;
}

/** An entry's display title: its titleLocalized for `locale` (a tag or locale chain), else its title. */
export function serviceEntryTitle(entry: Pick<PluginServiceEntry, "title" | "titleLocalized">, locale?: string | readonly string[]): string {
  return locale === undefined ? entry.title : resolvePluginTitle(entry, locale);
}

/** Lower-cased accepts, or undefined when missing, too many, malformed or duplicated after lower-casing. */
export function normalizeServiceAccepts(accepts: unknown): string[] | undefined {
  if (!Array.isArray(accepts) || accepts.length === 0 || accepts.length > SERVICE_LIMITS.acceptsMax) return undefined;
  const normalized: string[] = [];
  for (const raw of accepts) {
    if (typeof raw !== "string") return undefined;
    const accept = goLower(raw);
    if (!ACCEPT_EXT_PATTERN.test(accept) && !ACCEPT_MIME_PATTERN.test(accept)) return undefined;
    if (normalized.includes(accept)) return undefined;
    normalized.push(accept);
  }
  return normalized;
}

/**
 * Explicit surfaces, validated, or the default: every one of fileMenu,
 * selection and palette compatible with the input kind (the newer surfaces
 * must be explicit). fileMenu/selection need no workspace.read (the provider
 * only gets what the user picked there). Result follows the contract order;
 * undefined when invalid.
 */
export function resolveServiceSurfaces(requested: unknown, inputKind: ServiceInputKind): ServiceSurface[] | undefined {
  const compatible = COMPATIBLE_SURFACES[inputKind];
  if (requested === undefined || requested === null) return compatible.filter((surface) => DEFAULT_SERVICE_SURFACES.includes(surface));
  if (!Array.isArray(requested)) return undefined;
  const wanted = new Set<ServiceSurface>();
  for (const surface of requested) {
    if (!SERVICE_SURFACES.includes(surface as ServiceSurface) || wanted.has(surface) || !compatible.includes(surface)) return undefined;
    wanted.add(surface);
  }
  return SERVICE_SURFACES.filter((surface) => wanted.has(surface));
}

function parseInput(raw: unknown): ServiceShape | undefined {
  if (!isRecord(raw) || !SERVICE_INPUT_KINDS.includes(raw.kind as ServiceInputKind)) return undefined;
  const kind = raw.kind as ServiceInputKind;
  const isFile = kind === "file" || kind === "files";
  if ((raw.accepts !== undefined && !isFile) || (raw.maxBytes !== undefined && kind === "none") || (raw.maxFiles !== undefined && kind !== "files")) return undefined;
  const bounded = (value: unknown, fallback: number, maximum: number) => (value === undefined ? fallback : isInteger(value) && value >= 1 && value <= maximum ? value : undefined);
  if (kind === "none") return { kind };
  if (kind === "text") {
    const maxBytes = bounded(raw.maxBytes, SERVICE_LIMITS.textDefaultBytes, SERVICE_LIMITS.textMaxBytes);
    return maxBytes === undefined ? undefined : { kind, maxBytes };
  }
  const accepts = normalizeServiceAccepts(raw.accepts);
  const maxBytes = bounded(raw.maxBytes, SERVICE_LIMITS.fileMaxBytes, SERVICE_LIMITS.fileMaxBytes);
  if (!accepts || maxBytes === undefined) return undefined;
  if (kind === "file") return { kind, accepts, maxBytes };
  const maxFiles = bounded(raw.maxFiles, SERVICE_LIMITS.filesDefault, SERVICE_LIMITS.filesMax);
  return maxFiles === undefined ? undefined : { kind, accepts, maxBytes, maxFiles };
}

function parseOutput(raw: unknown): ServiceShape | undefined {
  if (!isRecord(raw) || !SERVICE_OUTPUT_KINDS.includes(raw.kind as ServiceOutputKind)) return undefined;
  const kind = raw.kind as ServiceOutputKind;
  if (raw.mime === undefined || raw.mime === "") return { kind };
  if (kind === "none" || kind === "json" || typeof raw.mime !== "string" || !OUTPUT_MIME_PATTERN.test(raw.mime)) return undefined;
  return { kind, mime: raw.mime };
}

function parseParams(raw: unknown): Record<string, unknown> | undefined | null {
  if (raw === undefined || raw === null) return undefined;
  if (!isRecord(raw) || utf8ByteLength(JSON.stringify(raw)) > SERVICE_LIMITS.paramsMaxBytes) return null;
  if (isRecord(raw.properties) && Object.keys(raw.properties).length > SERVICE_LIMITS.paramsMaxProperties) return null;
  return parseCommandInputForm(raw) ? raw : null;
}

/** Revalidates one provider listed by the host; undefined when it breaks a rule. */
export function parsePluginServiceEntry(raw: unknown): PluginServiceEntry | undefined {
  if (!isRecord(raw)) return undefined;
  const pluginId = typeof raw.pluginId === "string" ? raw.pluginId : "";
  const entryId = typeof raw.entryId === "string" ? raw.entryId.replace(GO_TRIM, "") : "";
  const service = typeof raw.service === "string" ? raw.service : "";
  if (!PLUGIN_ID_PATTERN.test(pluginId) || !ENTRY_ID_PATTERN.test(entryId) || serviceNameError(service)) return undefined;
  const title = line(raw.title, SERVICE_LIMITS.titleMaxRunes, true);
  const titleLocalized = localizedTitles(raw.titleLocalized);
  const description = line(raw.description, SERVICE_LIMITS.descriptionMaxRunes, false);
  const input = parseInput(raw.input);
  const output = parseOutput(raw.output);
  const params = parseParams(raw.params);
  if (title === undefined || titleLocalized === null || description === undefined || !input || !output || params === null) return undefined;
  const surfaces = resolveServiceSurfaces(raw.surfaces, input.kind as ServiceInputKind);
  const risk = raw.risk as ServiceRisk;
  const timeoutMs = raw.timeoutMs === undefined ? SERVICE_LIMITS.timeoutDefaultMs : raw.timeoutMs;
  if (!surfaces || !SERVICE_RISKS.includes(risk) || !isInteger(timeoutMs) || timeoutMs < 1 || timeoutMs > SERVICE_LIMITS.timeoutMaxMs) return undefined;
  const pluginName = typeof raw.pluginName === "string" && raw.pluginName.trim() ? raw.pluginName.trim() : pluginId;
  const entry: PluginServiceEntry = { pluginId, pluginName, entryId, service, title, description, input, output, surfaces, risk, timeoutMs, isDefault: raw.isDefault === true };
  if (titleLocalized) entry.titleLocalized = titleLocalized;
  if (params) entry.params = params;
  return entry;
}

/** Revalidated providers from a host list, dropping invalid entries and repeated plugin/entry pairs. */
export function resolvePluginServices(raw: unknown): PluginServiceEntry[] {
  if (!Array.isArray(raw)) return [];
  const seen = new Set<string>();
  const result: PluginServiceEntry[] = [];
  for (const item of raw) {
    const entry = parsePluginServiceEntry(item);
    const key = entry ? `${entry.pluginId}/${entry.entryId}` : "";
    if (!entry || seen.has(key)) continue;
    seen.add(key);
    result.push(entry);
  }
  return result;
}

/** Why a declared service is not a live provider (`plugin_services_declared`). */
export const SERVICE_INACTIVE_REASONS = ["disabled", "inactiveWorkspace", "unavailable"] as const;
export type ServiceInactiveReason = (typeof SERVICE_INACTIVE_REASONS)[number];

/** One service entry an installed plugin declares, live or not (plugin details). */
export type PluginDeclaredServiceEntry = PluginServiceEntry & (
  | { active: true; inactiveReason?: undefined }
  | { active: false; inactiveReason: ServiceInactiveReason }
);

/**
 * Revalidated entries from `plugin_services_declared` for pluginId: entries
 * of another plugin, without an explicit active flag or with an unknown
 * inactive reason are dropped, as are invalid entries and repeated entry IDs.
 */
export function resolveDeclaredPluginServices(raw: unknown, pluginId: string): PluginDeclaredServiceEntry[] {
  if (!Array.isArray(raw)) return [];
  const seen = new Set<string>();
  const result: PluginDeclaredServiceEntry[] = [];
  for (const item of raw) {
    const entry = parsePluginServiceEntry(item);
    if (!entry || entry.pluginId !== pluginId || seen.has(entry.entryId) || !isRecord(item)) continue;
    const reason = item.inactiveReason as ServiceInactiveReason;
    if (item.active === true) {
      result.push({ ...entry, active: true });
    } else if (item.active === false && SERVICE_INACTIVE_REASONS.includes(reason)) {
      result.push({ ...entry, isDefault: false, active: false, inactiveReason: reason });
    } else {
      continue;
    }
    seen.add(entry.entryId);
  }
  return result;
}

/** Last extension of the base name (Go's filepath.Ext). */
function fileExtension(name: string): string {
  const base = name.slice(Math.max(name.lastIndexOf("/"), name.lastIndexOf("\\")) + 1);
  const dot = base.lastIndexOf(".");
  return dot < 0 ? "" : base.slice(dot);
}

/**
 * Whether fileName matches one of a service's (lower-case) accepts: an
 * extension matches the end of the lower-cased name; a MIME type or wildcard
 * matches through SERVICE_MIME_TYPES, so an unknown extension matches no MIME
 * entry. Mirrors serviceAccepts in service_mime.go.
 */
export function serviceAccepts(accepts: readonly string[], fileName: string): boolean {
  const name = goLower(fileName);
  const extension = fileExtension(name);
  const mime = Object.prototype.hasOwnProperty.call(SERVICE_MIME_TYPES, extension) ? SERVICE_MIME_TYPES[extension] : undefined;
  return accepts.some((accept) => {
    if (accept.startsWith(".")) return name.endsWith(accept);
    if (!mime) return false;
    if (accept.endsWith("/*")) return mime.startsWith(accept.slice(0, -1));
    return accept === mime;
  });
}

function compareProviders(left: PluginServiceEntry, right: PluginServiceEntry) {
  return Number(right.isDefault) - Number(left.isDefault)
    || left.pluginName.localeCompare(right.pluginName)
    || left.pluginId.localeCompare(right.pluginId)
    || left.entryId.localeCompare(right.entryId);
}

/**
 * Merges providers by service name. Each group's title is its first provider's
 * (the default provider first), localized for `locale` when given; groups are
 * sorted by title.
 */
function groupServices(
  entries: readonly PluginServiceEntry[],
  locale: string | readonly string[] | undefined,
  fits: (entry: PluginServiceEntry) => boolean = () => true,
): ServiceGroup[] {
  const byService = new Map<string, PluginServiceEntry[]>();
  for (const entry of entries) {
    const providers = byService.get(entry.service) ?? [];
    providers.push(entry);
    byService.set(entry.service, providers);
  }
  const groups: ServiceGroup[] = [];
  for (const [service, providers] of byService) {
    const fitting = providers.filter(fits);
    // When some providers can take the input, only those are offered.
    const offered = (fitting.length ? fitting : providers).sort(compareProviders);
    groups.push({ service, title: serviceEntryTitle(offered[0], locale), providers: offered, fits: fitting.length > 0 });
  }
  return groups.sort((left, right) => left.title.localeCompare(right.title) || left.service.localeCompare(right.service));
}

/** One service in the plugin manager catalog with every provider effective in the workspace. */
export interface ServiceCatalogGroup {
  service: string;
  /** Sorted by plugin name and ID, not by default, so choosing a default does not reorder rows. */
  providers: PluginServiceEntry[];
  /** The default provider's plugin ID; empty when the user is asked every time. */
  defaultPluginId: string;
}

/** Providers grouped by service name, sorted by service name. */
export function serviceCatalogGroups(entries: readonly PluginServiceEntry[]): ServiceCatalogGroup[] {
  const byService = new Map<string, PluginServiceEntry[]>();
  for (const entry of entries) {
    const providers = byService.get(entry.service) ?? [];
    providers.push(entry);
    byService.set(entry.service, providers);
  }
  return [...byService]
    .map(([service, providers]) => ({
      service,
      providers: providers.sort((left, right) =>
        left.pluginName.localeCompare(right.pluginName) || left.pluginId.localeCompare(right.pluginId) || left.entryId.localeCompare(right.entryId)),
      defaultPluginId: providers.find((entry) => entry.isDefault)?.pluginId ?? "",
    }))
    .sort((left, right) => left.service.localeCompare(right.service));
}

/**
 * File-menu services whose accepts match every selected file and whose count
 * limit admits them. `locale` (here and in servicesForSelection /
 * servicesForPalette) localizes the group titles.
 */
export function servicesForFiles(entries: readonly PluginServiceEntry[], fileNames: readonly string[], locale?: string | readonly string[]): ServiceGroup[] {
  if (fileNames.length === 0) return [];
  return groupServices(entries.filter((entry) => {
    if (!entry.surfaces.includes("fileMenu")) return false;
    const { kind, accepts = [], maxFiles = SERVICE_LIMITS.filesDefault } = entry.input;
    if (kind === "file" ? fileNames.length !== 1 : kind !== "files" || fileNames.length > maxFiles) return false;
    return fileNames.every((name) => serviceAccepts(accepts, name));
  }), locale);
}

/**
 * Editor-selection services. A group fits when some provider's input.maxBytes
 * admits the selection's UTF-8 size; the host rechecks the serialized
 * envelope, whose overhead the providers' 8 KiB payload reserve covers.
 */
export function servicesForSelection(entries: readonly PluginServiceEntry[], text: string, locale?: string | readonly string[]): ServiceGroup[] {
  const bytes = utf8ByteLength(text);
  const candidates = entries.filter((entry) => entry.surfaces.includes("selection") && entry.input.kind === "text");
  return groupServices(candidates, locale, (entry) => bytes <= (entry.input.maxBytes ?? SERVICE_LIMITS.textDefaultBytes));
}

/** Command-palette services. */
export function servicesForPalette(entries: readonly PluginServiceEntry[], locale?: string | readonly string[]): ServiceGroup[] {
  return groupServices(entries.filter((entry) => entry.surfaces.includes("palette")), locale);
}

/** What the command palette asks the user for before running a service. */
export type PaletteServiceInput =
  | { kind: "none" }
  /** maxBytes: the largest any offered provider takes (the host picks the provider). */
  | { kind: "text"; maxBytes: number }
  /** extensions: accepts as file-dialog filters, MIME accepts expanded through SERVICE_MIME_TYPES. */
  | { kind: "file" | "files"; accepts: string[]; extensions: string[]; maxFiles: number };

/** Extensions matching a service's accepts, for native file-dialog filters. */
export function serviceAcceptExtensions(accepts: readonly string[]): string[] {
  const extensions = new Set<string>();
  for (const accept of accepts) {
    if (accept.startsWith(".")) {
      extensions.add(accept);
      continue;
    }
    for (const [extension, mime] of Object.entries(SERVICE_MIME_TYPES)) {
      if (accept.endsWith("/*") ? mime.startsWith(accept.slice(0, -1)) : mime === accept) extensions.add(extension);
    }
  }
  return [...extensions];
}

/**
 * The input a palette service asks for, following its preferred provider (the
 * default first): runPluginService only offers providers with the same input
 * kind, so text limits and file accepts merge across those.
 */
export function paletteServiceInput(group: ServiceGroup): PaletteServiceInput {
  const kind = group.providers[0]?.input.kind ?? "none";
  const providers = group.providers.filter((entry) => entry.input.kind === kind);
  if (kind === "text") {
    return { kind, maxBytes: Math.max(...providers.map((entry) => entry.input.maxBytes ?? SERVICE_LIMITS.textDefaultBytes)) };
  }
  if (kind === "file" || kind === "files") {
    const accepts = [...new Set(providers.flatMap((entry) => entry.input.accepts ?? []))];
    const maxFiles = kind === "file" ? 1 : Math.max(...providers.map((entry) => entry.input.maxFiles ?? SERVICE_LIMITS.filesDefault));
    return { kind, accepts, extensions: serviceAcceptExtensions(accepts), maxFiles };
  }
  return { kind: "none" };
}

// ---------------------------------------------------------------------------
// The newer surfaces (sftpFile, dbResult, httpResponse, terminalSelection):
// which services a host menu offers for its target. The request is built at
// click time in domain/plugins/service-surfaces.ts.

/** The file name dbResult content is handed over as (Go serviceDBResultFileName). */
export const SERVICE_DB_RESULT_FILE_NAME = "result.json";
const EXCHANGE_NAME_MAX_BYTES = 255;
const EXCHANGE_EXT_MAX_BYTES = 32;
const HTTP_CONTENT_TYPE_MAX_BYTES = 256;
const LONE_SURROGATE = /[\ud800-\udbff](?![\udc00-\udfff])|(?<![\ud800-\udbff])[\udc00-\udfff]/g;

/**
 * The name a file gets in the service exchange, as sanitizeExchangeName in
 * service_exchange.go: the base name, control / format characters and
 * `<>:"|?*` replaced by "_", trailing dots and spaces removed, at most 255
 * bytes (a short extension kept). Used to match accepts the way the host will.
 */
export function serviceExchangeName(name: string): string {
  let text = name.replace(LONE_SURROGATE, "_");
  text = text.slice(Math.max(text.lastIndexOf("/"), text.lastIndexOf("\\")) + 1);
  text = text.replace(/[\p{Cc}\p{Cf}<>:"|?*]/gu, "_").replace(GO_TRIM, "").replace(/[. ]+$/, "");
  if (!text) return "file";
  if (utf8ByteLength(text) <= EXCHANGE_NAME_MAX_BYTES) return text;
  let extension = fileExtension(text);
  if (utf8ByteLength(extension) > EXCHANGE_EXT_MAX_BYTES) extension = "";
  const budget = EXCHANGE_NAME_MAX_BYTES - utf8ByteLength(extension);
  let stem = "";
  let used = 0;
  for (const char of text) {
    const size = utf8ByteLength(char);
    if (used + size > budget) break;
    stem += char;
    used += size;
  }
  return stem + extension;
}

/**
 * The lower-cased type/subtype of a Content-Type value ("" is text/plain);
 * undefined when malformed. Mirrors serviceMediaType in
 * service_surface_context.go.
 */
function httpMediaType(contentType: string): string | undefined {
  const text = contentType.replace(GO_TRIM, "");
  if (!text) return "text/plain";
  if (utf8ByteLength(text) > HTTP_CONTENT_TYPE_MAX_BYTES || /\p{Cc}/u.test(text)) return undefined;
  const mediaType = goLower((text.split(";", 1)[0] ?? "").replace(GO_TRIM, ""));
  const slash = mediaType.indexOf("/");
  if (slash <= 0 || slash === mediaType.length - 1 || /[ \t,]/.test(mediaType)) return undefined;
  return mediaType;
}

/** Go's path.Match for a contract text type ("text/*", "application/*+json"): `*` stays within one segment. */
function textTypePattern(pattern: string): RegExp {
  return new RegExp(`^${pattern.split("*").map((part) => part.replace(/[.+?^${}()|[\]\\]/g, "\\$&")).join("[^/]*")}$`);
}
const TEXT_TYPE_PATTERNS = SERVICE_HTTP_RESPONSE_TEXT_TYPES.map(textTypePattern);

function textMediaType(contentType: string): string | undefined {
  const mediaType = httpMediaType(contentType);
  return mediaType !== undefined && TEXT_TYPE_PATTERNS.some((pattern) => pattern.test(mediaType)) ? mediaType : undefined;
}

/** Whether an HTTP response of this content type can go to a service (contract httpResponseTextTypes). */
export function isTextResponseType(contentType: string): boolean {
  return textMediaType(contentType) !== undefined;
}

/**
 * The name an httpResponse body is handed over as: `response` plus an
 * extension by type (json, xml, html, javascript / ecmascript, csv, yaml,
 * markdown, ndjson, otherwise .txt); null for a binary or malformed type.
 * Mirrors httpResponseFileName in service_surface_context.go.
 */
export function httpResponseFileName(contentType: string): string | null {
  const mediaType = textMediaType(contentType);
  if (mediaType === undefined) return null;
  const subtype = mediaType.slice(mediaType.indexOf("/") + 1);
  const plus = subtype.lastIndexOf("+");
  const suffix = plus >= 0 ? subtype.slice(plus + 1) : "";
  let extension = ".txt";
  if (subtype === "x-ndjson" || subtype === "ndjson") extension = ".ndjson";
  else if (subtype === "json" || suffix === "json") extension = ".json";
  else if (subtype === "xml" || suffix === "xml") extension = ".xml";
  else if (subtype === "html") extension = ".html";
  else if (subtype === "javascript" || subtype === "ecmascript" || subtype === "x-javascript") extension = ".js";
  else if (subtype === "csv") extension = ".csv";
  else if (subtype === "yaml" || subtype === "x-yaml") extension = ".yaml";
  else if (subtype === "markdown" || subtype === "x-markdown") extension = ".md";
  return `response${extension}`;
}

/** ANSI CSI (7- and 8-bit) and OSC sequences (ended by BEL or ST, or unterminated up to the end). */
// eslint-disable-next-line no-control-regex -- matching terminal escape sequences is the point
const TERMINAL_ESCAPES = /(?:\u001b\[|\u009b)[0-?]*[ -/]*[@-~]|(?:\u001b\]|\u009d)[\s\S]*?(?:\u0007|\u001b\\|\u009c|$)/g;
// eslint-disable-next-line no-control-regex -- C0 except tab and LF, DEL and C1
const TERMINAL_CONTROLS = /[\u0000-\u0008\u000b-\u001f\u007f-\u009f]/g;

/**
 * Terminal selection text as a service receives it (design ruling 22): CRLF
 * and CR become LF, ANSI CSI and OSC sequences go, then every C0 control
 * other than tab and LF (NUL included), DEL and the C1 controls. Never
 * truncated.
 */
export function normalizeTerminalServiceText(text: string): string {
  return text.replace(/\r\n?/g, "\n").replace(TERMINAL_ESCAPES, "").replace(TERMINAL_CONTROLS, "");
}

/** Why a service shown in a menu cannot run right now. */
export type ServiceDisabledReason = "tooLarge" | "tooLargeFile" | "queryRunning" | "pendingChanges" | "responseTruncated";

/** A service group in a surface's menu; greyed out, with the reason, when disabledReason is set. */
export interface ServiceSurfaceGroup extends ServiceGroup {
  disabledReason?: ServiceDisabledReason;
}

function fileInputCandidates(entries: readonly PluginServiceEntry[], surface: ServiceSurface, fileName: string) {
  return entries.filter((entry) =>
    entry.surfaces.includes(surface)
    && (entry.input.kind === "file" || entry.input.kind === "files")
    && serviceAccepts(entry.input.accepts ?? [], fileName));
}

function withReason(groups: ServiceGroup[], reason: (group: ServiceGroup) => ServiceDisabledReason | undefined): ServiceSurfaceGroup[] {
  return groups.map((group) => {
    const disabledReason = reason(group);
    return disabledReason ? { ...group, disabledReason } : group;
  });
}

/**
 * The services a host menu offers for its target (only entries that declare
 * the target's surface), each with a disabled reason when its input cannot
 * go now: text over every provider's maxBytes (tooLarge), a remote file or
 * response over it (tooLargeFile), a query still running (queryRunning),
 * unsaved table edits (pendingChanges), a response the host cut at 10 MiB
 * (responseTruncated). Nothing is read or serialized here; dbResult only
 * counts the rows.
 */
export function servicesForSurface(entries: readonly PluginServiceEntry[], target: ServiceSurfaceTarget, locale?: string | readonly string[]): ServiceSurfaceGroup[] {
  switch (target.surface) {
    case "fileMenu":
      return servicesForFiles(entries, target.files.map((file) => file.name), locale);
    case "sftpFile": {
      if (target.remote.kind !== "file") return [];
      const size = target.file.size;
      const candidates = fileInputCandidates(entries, "sftpFile", serviceExchangeName(target.file.name));
      const groups = groupServices(candidates, locale, (entry) => size <= (entry.input.maxBytes ?? SERVICE_LIMITS.fileMaxBytes));
      return withReason(groups, (group) => (group.fits ? undefined : "tooLargeFile"));
    }
    case "dbResult": {
      if (target.columns.length === 0 || target.rows().length === 0) return [];
      const reason = target.running ? "queryRunning" : target.pendingChanges ? "pendingChanges" : undefined;
      return withReason(groupServices(fileInputCandidates(entries, "dbResult", SERVICE_DB_RESULT_FILE_NAME), locale), () => reason);
    }
    case "httpResponse": {
      const name = httpResponseFileName(target.response.contentType);
      if (!name) return [];
      const size = target.response.sizeBytes;
      const truncated = size > SERVICE_LIMITS.httpResponseMaxBytes;
      // Every provider that takes the file, regardless of size, as the host's
      // input.fits: the default (or the prompt) is then the one the host will
      // use, and surfaceRunRequest checks the body against it on click instead
      // of silently switching to a provider that happens to fit.
      const groups = groupServices(fileInputCandidates(entries, "httpResponse", name), locale).map((group) => ({
        ...group,
        fits: group.providers.some((entry) => size <= (entry.input.maxBytes ?? SERVICE_LIMITS.fileMaxBytes)),
      }));
      return withReason(groups, (group) => (truncated ? "responseTruncated" : group.fits ? undefined : "tooLargeFile"));
    }
    case "terminalSelection": {
      const text = normalizeTerminalServiceText(target.selection);
      if (!text) return [];
      const bytes = utf8ByteLength(text);
      const candidates = entries.filter((entry) => entry.surfaces.includes("terminalSelection") && entry.input.kind === "text");
      const groups = groupServices(candidates, locale, (entry) => bytes <= (entry.input.maxBytes ?? SERVICE_LIMITS.textDefaultBytes));
      return withReason(groups, (group) => (group.fits ? undefined : "tooLarge"));
    }
  }
}

/**
 * The newer surfaces a manifest's services declare explicitly, in contract
 * order (the install preview warns about them; an omitted `surfaces` never
 * includes them).
 */
export function servicePreviewSurfaces(contributes: unknown): NewerServiceSurface[] {
  const entries = isRecord(contributes) ? contributes[SERVICES_CONTRIBUTION_POINT] : undefined;
  if (!Array.isArray(entries)) return [];
  const declared = new Set<unknown>();
  for (const entry of entries.slice(0, SERVICE_LIMITS.maxEntries)) {
    if (isRecord(entry) && Array.isArray(entry.surfaces)) for (const surface of entry.surfaces) declared.add(surface);
  }
  return NEWER_SERVICE_SURFACES.filter((surface) => declared.has(surface));
}

/**
 * Frontend stage of the `fileIcons` contribution point: re-validation of a
 * file icon theme the Go host already accepted
 * (backend/plugin/contrib_file_icons.go) and the pure resolver that
 * maps a file or folder name to an icon definition. No DOM, Vue or IPC
 * here; the runtime wiring lives in services/contributions/file-icons.ts and
 * stores/file-icon-theme.ts.
 *
 * Snapshot entries carry only content-hash asset keys (never icon bytes or
 * paths); icons are fetched through services/contribution-assets.ts and
 * rendered in image context only (<img> or a CSS mask over a blob: URL).
 * Mapping lookups use Map, never plain-object indexing, so names such as
 * "__proto__" or "constructor" cannot reach Object.prototype.
 */
import { isContributionAssetKey } from "@/domain/plugins/contribution-assets";
import { resolvePluginTitle } from "@/domain/plugins/title";

export const FILE_ICONS_CONTRIBUTION_POINT = "fileIcons";

/** Mirrors contribution-points.json fileIcons limits (the host enforces them first). */
export const FILE_ICON_LIMITS = {
  titleMaxRunes: 64,
  iconsPerTheme: 1024,
  mappingsMax: 8192,
  keyMaxLength: 128,
  /** points.fileIcons.maxEntries */
  themesPerPlugin: 6,
} as const;

const THEME_ID_PATTERN = /^[a-z0-9][a-z0-9._-]{0,63}$/;
const DEFINITION_ID_PATTERN = /^[A-Za-z0-9_.-]{1,64}$/;
const EXTENSION_PATTERN = /^[a-z0-9][a-z0-9._+-]{0,63}$/;
// Controls, line/paragraph separators, bidirectional overrides and isolates.
// eslint-disable-next-line no-control-regex -- rejecting control characters is the point
const UNSAFE_TEXT_PATTERN = /[\u0000-\u001f\u007f-\u009f\u2028\u2029\u202a-\u202e\u2066-\u2069]/;

const DEFAULT_FIELDS = ["file", "folder", "folderExpanded", "rootFolder", "rootFolderExpanded"] as const;
const MAP_FIELDS = ["fileExtensions", "fileNames", "folderNames", "folderNamesExpanded"] as const;
type DefaultField = (typeof DEFAULT_FIELDS)[number];
type MapField = (typeof MAP_FIELDS)[number];

export interface FileIconMappings {
  file?: string;
  folder?: string;
  folderExpanded?: string;
  rootFolder?: string;
  rootFolderExpanded?: string;
  fileExtensions: ReadonlyMap<string, string>;
  fileNames: ReadonlyMap<string, string>;
  folderNames: ReadonlyMap<string, string>;
  folderNamesExpanded: ReadonlyMap<string, string>;
}

export interface FileIconThemeData {
  id: string;
  title: string;
  titleLocalized?: Record<string, string>;
  monochrome: boolean;
  /** Icon definition id -> content-hash asset key. */
  icons: ReadonlyMap<string, string>;
  /** Default mappings; `file` is always set. */
  base: FileIconMappings & { file: string };
  /** Overrides used while a light MinCode theme is active. */
  light?: FileIconMappings;
}

/** A validated theme with the plugin that contributed it. */
export interface FileIconThemeRecord extends FileIconThemeData {
  pluginId: string;
  pluginName: string;
}

export type FileIconThemeValidation = { ok: true; theme: FileIconThemeData } | { ok: false; error: string };

export interface FileIconThemeIssue {
  pluginId: string;
  stage: "host" | "frontend";
  entry: number;
  theme?: string;
  message: string;
}

export interface FileIconTarget {
  name: string;
  isDir?: boolean;
  expanded?: boolean;
  /** A project root folder in the tree. */
  root?: boolean;
}

/** The resolved icon: definition id plus its asset key. */
export interface ResolvedFileIcon {
  definition: string;
  key: string;
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function isSafeText(value: string, maxRunes: number): boolean {
  const length = [...value].length;
  return length > 0 && length <= maxRunes && !UNSAFE_TEXT_PATTERN.test(value);
}

function isValidNameKey(key: string): boolean {
  return (
    key.length > 0 &&
    new TextEncoder().encode(key).length <= FILE_ICON_LIMITS.keyMaxLength &&
    !key.includes("/") &&
    !key.includes("\\") &&
    !UNSAFE_TEXT_PATTERN.test(key)
  );
}

function parseMappings(
  raw: Record<string, unknown>,
  icons: ReadonlyMap<string, string>,
  path: string,
  counter: { count: number },
): FileIconMappings | string {
  const result: FileIconMappings = {
    fileExtensions: new Map(),
    fileNames: new Map(),
    folderNames: new Map(),
    folderNamesExpanded: new Map(),
  };
  for (const field of DEFAULT_FIELDS) {
    const value = raw[field];
    if (value === undefined || value === "") continue;
    if (typeof value !== "string" || !icons.has(value)) return `${path}${field} references an unknown icon definition`;
    result[field satisfies DefaultField] = value;
    counter.count += 1;
  }
  for (const field of MAP_FIELDS) {
    const value = raw[field];
    if (value === undefined || value === null) continue;
    if (!isRecord(value)) return `${path}${field} must be an object`;
    const target = new Map<string, string>();
    for (const [key, definition] of Object.entries(value)) {
      const lower = key.toLowerCase();
      const valid = field === "fileExtensions" ? EXTENSION_PATTERN.test(lower) : isValidNameKey(lower);
      if (!valid) return `${path}${field} key "${key}" is invalid`;
      if (typeof definition !== "string" || !icons.has(definition)) {
        return `${path}${field}["${key}"] references an unknown icon definition`;
      }
      if (target.has(lower)) return `${path}${field} repeats key "${lower}"`;
      target.set(lower, definition);
      counter.count += 1;
      if (counter.count > FILE_ICON_LIMITS.mappingsMax) return `more than ${FILE_ICON_LIMITS.mappingsMax} mappings`;
    }
    (result as Record<MapField, ReadonlyMap<string, string>>)[field] = target;
  }
  return result;
}

/**
 * Re-validates one snapshot entry. The host already rejected everything
 * unsafe; this guards the renderer against a malformed snapshot.
 */
export function validateFileIconTheme(raw: unknown, pluginId: string): FileIconThemeValidation {
  if (!isRecord(raw)) return { ok: false, error: "entry is not an object" };
  const id = raw.id;
  if (typeof id !== "string" || !THEME_ID_PATTERN.test(id) || !id.startsWith(`${pluginId}.`) || id.length === pluginId.length + 1) {
    return { ok: false, error: `file icon theme id must be "${pluginId}.<name>"` };
  }
  const title = typeof raw.title === "string" ? raw.title.trim() : "";
  if (!isSafeText(title, FILE_ICON_LIMITS.titleMaxRunes)) return { ok: false, error: "invalid title" };
  let titleLocalized: Record<string, string> | undefined;
  if (isRecord(raw.titleLocalized)) {
    for (const [locale, value] of Object.entries(raw.titleLocalized)) {
      if (typeof value !== "string" || !isSafeText(value.trim(), FILE_ICON_LIMITS.titleMaxRunes)) continue;
      titleLocalized ??= Object.create(null) as Record<string, string>;
      titleLocalized[locale] = value.trim();
    }
  }
  if (!isRecord(raw.icons)) return { ok: false, error: "icons must be an object" };
  const iconEntries = Object.entries(raw.icons);
  if (iconEntries.length === 0 || iconEntries.length > FILE_ICON_LIMITS.iconsPerTheme) {
    return { ok: false, error: `needs 1-${FILE_ICON_LIMITS.iconsPerTheme} icons` };
  }
  const icons = new Map<string, string>();
  for (const [definition, key] of iconEntries) {
    if (!DEFINITION_ID_PATTERN.test(definition) || !isContributionAssetKey(key)) {
      return { ok: false, error: `icon "${definition}" has no valid asset key` };
    }
    icons.set(definition, key);
  }
  const counter = { count: 0 };
  const base = parseMappings(raw, icons, "", counter);
  if (typeof base === "string") return { ok: false, error: base };
  if (!base.file) return { ok: false, error: 'missing default "file" icon' };
  let light: FileIconMappings | undefined;
  if (raw.light !== undefined && raw.light !== null) {
    if (!isRecord(raw.light)) return { ok: false, error: "light must be an object" };
    const parsed = parseMappings(raw.light, icons, "light.", counter);
    if (typeof parsed === "string") return { ok: false, error: parsed };
    light = parsed;
  }
  return {
    ok: true,
    theme: {
      id,
      title,
      ...(titleLocalized ? { titleLocalized } : {}),
      monochrome: raw.monochrome === true,
      icons,
      base: base as FileIconMappings & { file: string },
      ...(light ? { light } : {}),
    },
  };
}

interface FileIconContributionSource {
  pluginId: string;
  pluginName: string;
  point: { entries: unknown[]; issues: ReadonlyArray<{ entry: number; id?: string; message: string }> };
}

/**
 * Validates every contributed theme of a snapshot. File icon themes are
 * global: a process plugin's workspace scope does not hide them.
 */
export function collectFileIconThemes(contributions: readonly FileIconContributionSource[]): {
  themes: FileIconThemeRecord[];
  issues: FileIconThemeIssue[];
} {
  const themes: FileIconThemeRecord[] = [];
  const issues: FileIconThemeIssue[] = [];
  const seen = new Set<string>();
  for (const contribution of contributions) {
    for (const issue of contribution.point.issues) {
      issues.push({ pluginId: contribution.pluginId, stage: "host", entry: issue.entry, message: issue.message, ...(issue.id ? { theme: issue.id } : {}) });
    }
    let count = 0;
    contribution.point.entries.forEach((raw, entry) => {
      const result = validateFileIconTheme(raw, contribution.pluginId);
      if (!result.ok) {
        issues.push({ pluginId: contribution.pluginId, stage: "frontend", entry, message: result.error });
        return;
      }
      const issue = (message: string) =>
        issues.push({ pluginId: contribution.pluginId, stage: "frontend", entry, theme: result.theme.id, message });
      if (count >= FILE_ICON_LIMITS.themesPerPlugin) return issue(`more than ${FILE_ICON_LIMITS.themesPerPlugin} file icon themes`);
      if (seen.has(result.theme.id)) return issue(`duplicate file icon theme id "${result.theme.id}"`);
      seen.add(result.theme.id);
      count += 1;
      themes.push({ ...result.theme, pluginId: contribution.pluginId, pluginName: contribution.pluginName });
    });
  }
  return { themes, issues };
}

/** The title for `locale` (a tag or useVueI18n().localeChain; exact tag, then its language), else the default title. */
export function fileIconThemeTitle(
  theme: Pick<FileIconThemeData, "title" | "titleLocalized">,
  locale: string | readonly string[],
): string {
  return resolvePluginTitle(theme, locale);
}

/**
 * Extension candidates of a lower-cased file name, longest first:
 * "a.d.ts" -> ["d.ts", "ts"]; ".eslintrc" -> ["eslintrc"].
 */
export function fileIconExtensionCandidates(name: string): string[] {
  const candidates: string[] = [];
  for (let index = name.indexOf("."); index >= 0; index = name.indexOf(".", index + 1)) {
    const candidate = name.slice(index + 1);
    if (candidate) candidates.push(candidate);
  }
  return candidates;
}

/**
 * Resolves the icon definition for a file or folder. Light overrides win at
 * every step while `light` is true. Files: fileNames, then the longest
 * matching (compound) extension, then `file`. Folders: rootFolder(Expanded)
 * for project roots, then folderNamesExpanded (expanded only), folderNames,
 * then folderExpanded/folder. `undefined` means "use the built-in icon".
 */
export function resolveFileIconDefinition(theme: FileIconThemeData, target: FileIconTarget, light: boolean): ResolvedFileIcon | undefined {
  const layers = light && theme.light ? [theme.light, theme.base] : [theme.base];
  const name = target.name.toLowerCase();
  const pickDefault = (field: DefaultField) => {
    for (const layer of layers) if (layer[field]) return layer[field];
    return undefined;
  };
  const pickMap = (field: MapField, key: string) => {
    for (const layer of layers) {
      const value = layer[field].get(key);
      if (value) return value;
    }
    return undefined;
  };
  let definition: string | undefined;
  if (target.isDir) {
    if (target.root) definition = target.expanded ? (pickDefault("rootFolderExpanded") ?? pickDefault("rootFolder")) : pickDefault("rootFolder");
    if (!definition && target.expanded) definition = pickMap("folderNamesExpanded", name);
    definition ??= pickMap("folderNames", name);
    if (!definition && target.expanded) definition = pickDefault("folderExpanded");
    definition ??= pickDefault("folder");
  } else {
    definition = pickMap("fileNames", name);
    if (!definition) {
      for (const extension of fileIconExtensionCandidates(name)) {
        definition = pickMap("fileExtensions", extension);
        if (definition) break;
      }
    }
    definition ??= pickDefault("file");
  }
  if (!definition) return undefined;
  const key = theme.icons.get(definition);
  return key ? { definition, key } : undefined;
}

/** Install-preview summary: number of themes and inline titles (references show as files). */
export function fileIconPreviewSummary(contributes: unknown): { count: number; titles: string[] } | null {
  const entries = isRecord(contributes) ? contributes[FILE_ICONS_CONTRIBUTION_POINT] : undefined;
  if (!Array.isArray(entries) || entries.length === 0) return null;
  const titles = entries.flatMap((entry) => {
    if (!isRecord(entry)) return [];
    if (typeof entry.title === "string" && entry.title.trim()) return [entry.title.trim().slice(0, FILE_ICON_LIMITS.titleMaxRunes)];
    if (typeof entry.path === "string" && entry.path.trim()) return [entry.path.trim().slice(0, 256)];
    return [];
  });
  return { count: entries.length, titles };
}

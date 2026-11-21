/**
 * Frontend stage of the `aiCatalogs` contribution point: re-validation of
 * catalog entries the Go host already accepted
 * (backend/plugin/contrib_ai_catalogs.go) and of the responses of
 * `plugin_host_ai_catalog_search` / `plugin_host_ai_catalog_install`
 * (ai_catalog_invoke.go). No DOM, Vue or IPC here; the runtime wiring lives
 * in services/contributions/ai-catalogs.ts, stores/plugin-ai-catalogs.ts and
 * services/plugin-ai-catalog-service.ts.
 *
 * A catalog only helps the user find connectors and skills in Settings → AI →
 * Discover. Items are plain text; install content is returned by the plugin
 * (never fetched from a URL it names), parsed by the regular MCP config /
 * SKILL.md parsers and saved as ordinary user configuration after the user
 * confirms it.
 */
import type { PointContribution } from "@/domain/plugins/contributions";
import { normalizeSearchResultURL } from "@/domain/plugins/search-providers";
import { resolvePluginTitle } from "@/domain/plugins/title";

export const AI_CATALOGS_CONTRIBUTION_POINT = "aiCatalogs";

/** Mirrors contribution-points.json aiCatalogs limits (the host enforces them first). */
export const AI_CATALOG_LIMITS = {
  maxEntries: 4,
  titleMaxRunes: 48,
  descriptionMaxRunes: 256,
  localesMax: 16,
  queryMaxRunes: 256,
  payloadMaxBytes: 2048,
  resultMaxBytes: 65536,
  itemsMax: 50,
  itemIdMaxLength: 64,
  itemNameMaxRunes: 80,
  itemDescriptionMaxRunes: 280,
  urlMaxLength: 2048,
  installResultMaxBytes: 524288,
  timeoutMs: 8000,
  concurrentPerPlugin: 2,
} as const;

/** Mirrors contribution-points.json aiCatalogs lists.kinds. */
export const AI_CATALOG_KINDS = ["mcp", "skill"] as const;
export type AiCatalogKind = (typeof AI_CATALOG_KINDS)[number];

const CATALOG_ID_PATTERN = /^[a-z][a-z0-9-]{0,31}$/;
const PLUGIN_ID_PATTERN = /^[a-z0-9][a-z0-9._-]{0,63}$/;
const ACTION_ID_PATTERN = /^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$/;
const ITEM_ID_PATTERN = /^[A-Za-z0-9][A-Za-z0-9._:@/-]*$/;
const LOCALE_PATTERN = /^[A-Za-z]{2,8}(-[A-Za-z0-9]{1,8}){0,3}$/;
// eslint-disable-next-line no-control-regex -- rejecting control characters is the point
const UNSAFE_TEXT_PATTERN = /[\u0000-\u001f\u007f-\u009f\u2028\u2029\u202a-\u202e\u2066-\u2069\u200b-\u200f\u2060\ufeff]/;
// Install content may span lines and use tabs; everything else stays rejected.
// eslint-disable-next-line no-control-regex -- rejecting control characters is the point
const UNSAFE_CONTENT_PATTERN = /[\u0000-\u0008\u000b-\u001f\u007f-\u009f\u2028\u2029\u202a-\u202e\u2066-\u2069\u200b-\u200f\u2060\ufeff]/;

export interface PluginAiCatalog {
  pluginId: string;
  pluginName: string;
  fingerprint?: string;
  id: string;
  kind: AiCatalogKind;
  title: string;
  titleLocalized?: Record<string, string>;
  description?: string;
}

export interface AiCatalogIssue {
  pluginId: string;
  stage: "host" | "frontend";
  entry: number;
  catalog?: string;
  message: string;
}

export interface AiCatalogItem {
  id: string;
  name: string;
  description?: string;
  docsUrl?: string;
}

export interface AiCatalogSearchResponse {
  items: AiCatalogItem[];
  dropped: number;
  truncated: boolean;
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function runeCount(value: string) {
  return [...value].length;
}

function boundedLine(value: unknown, maxRunes: number, required: boolean): string | undefined | false {
  if (value === undefined && !required) return undefined;
  if (typeof value !== "string") return false;
  const trimmed = value.trim();
  if (!trimmed) return required ? false : undefined;
  if (runeCount(trimmed) > maxRunes || UNSAFE_TEXT_PATTERN.test(trimmed)) return false;
  return trimmed;
}

function localizedLines(value: unknown, maxRunes: number): Record<string, string> | undefined | false {
  if (value === undefined) return undefined;
  if (!isRecord(value)) return false;
  const entries = Object.entries(value);
  if (entries.length > AI_CATALOG_LIMITS.localesMax) return false;
  const out: Record<string, string> = {};
  for (const [locale, text] of entries) {
    const line = boundedLine(text, maxRunes, true);
    if (!line || !LOCALE_PATTERN.test(locale)) return false;
    out[locale] = line;
  }
  return entries.length ? out : undefined;
}

function isKind(value: unknown): value is AiCatalogKind {
  return typeof value === "string" && (AI_CATALOG_KINDS as readonly string[]).includes(value);
}

/** Re-validates one host-accepted entry (defense in depth). */
export function validateAiCatalogEntry(
  raw: unknown,
): { ok: true; catalog: Omit<PluginAiCatalog, "pluginId" | "pluginName"> } | { ok: false; error: string } {
  if (!isRecord(raw)) return { ok: false, error: "entry must be an object" };
  const id = typeof raw.id === "string" ? raw.id : "";
  if (!CATALOG_ID_PATTERN.test(id)) return { ok: false, error: `AI catalog id "${id}" is invalid` };
  if (!isKind(raw.kind)) return { ok: false, error: `AI catalog "${id}" kind is invalid` };
  const title = boundedLine(raw.title, AI_CATALOG_LIMITS.titleMaxRunes, true);
  if (!title) return { ok: false, error: "title is invalid" };
  const titleLocalized = localizedLines(raw.titleLocalized, AI_CATALOG_LIMITS.titleMaxRunes);
  if (titleLocalized === false) return { ok: false, error: "titleLocalized is invalid" };
  const description = boundedLine(raw.description, AI_CATALOG_LIMITS.descriptionMaxRunes, false);
  if (description === false) return { ok: false, error: "description is invalid" };
  if (localizedLines(raw.descriptionLocalized, AI_CATALOG_LIMITS.descriptionMaxRunes) === false) return { ok: false, error: "descriptionLocalized is invalid" };
  for (const field of ["searchAction", "installAction"]) {
    if (typeof raw[field] !== "string" || !ACTION_ID_PATTERN.test(raw[field])) return { ok: false, error: `${field} is invalid` };
  }
  return {
    ok: true,
    catalog: {
      id,
      kind: raw.kind,
      title,
      ...(titleLocalized ? { titleLocalized } : {}),
      ...(description ? { description } : {}),
    },
  };
}

/** Collects the catalogs of process plugins active in this workspace, in plugin id order. */
export function collectAiCatalogs(contributions: readonly PointContribution[]): {
  catalogs: PluginAiCatalog[];
  issues: AiCatalogIssue[];
} {
  const catalogs: PluginAiCatalog[] = [];
  const issues: AiCatalogIssue[] = [];
  const ordered = [...contributions].sort((a, b) => (a.pluginId < b.pluginId ? -1 : a.pluginId > b.pluginId ? 1 : 0));
  for (const contribution of ordered) {
    if (contribution.runtime !== "process" || !contribution.activeInWorkspace || !PLUGIN_ID_PATTERN.test(contribution.pluginId)) continue;
    for (const issue of contribution.point.issues) {
      issues.push({ pluginId: contribution.pluginId, stage: "host", entry: issue.entry, message: issue.message, ...(issue.id ? { catalog: issue.id } : {}) });
    }
    const seen = new Set<string>();
    contribution.point.entries.slice(0, AI_CATALOG_LIMITS.maxEntries).forEach((raw, entry) => {
      const result = validateAiCatalogEntry(raw);
      if (!result.ok) {
        issues.push({ pluginId: contribution.pluginId, stage: "frontend", entry, message: result.error });
        return;
      }
      if (seen.has(result.catalog.id)) {
        issues.push({ pluginId: contribution.pluginId, stage: "frontend", entry, catalog: result.catalog.id, message: `duplicate AI catalog id "${result.catalog.id}"` });
        return;
      }
      seen.add(result.catalog.id);
      catalogs.push({ ...result.catalog, pluginId: contribution.pluginId, pluginName: contribution.pluginName, fingerprint: contribution.point.fingerprint });
    });
  }
  return { catalogs, issues };
}

/** The catalog's title for a locale chain (plugin translations only). */
export function aiCatalogTitle(catalog: PluginAiCatalog, locale: string | readonly string[]): string {
  return resolvePluginTitle(catalog, locale);
}

/**
 * The Discover entry id (also the install source of a skill) of a plugin
 * catalog item. The `plugin:` scheme marks it as something the host must
 * never fetch.
 */
export function pluginCatalogEntryId(pluginId: string, catalogId: string, itemId: string): string {
  return `plugin:${pluginId}/${catalogId}/${itemId}`;
}

/** Re-validates a `plugin_host_ai_catalog_search` response; invalid items are dropped and counted. */
export function parseAiCatalogSearchResponse(value: unknown): AiCatalogSearchResponse {
  const response: AiCatalogSearchResponse = { items: [], dropped: 0, truncated: false };
  if (!isRecord(value) || !Array.isArray(value.items)) return response;
  response.dropped = typeof value.dropped === "number" && Number.isSafeInteger(value.dropped) && value.dropped > 0 ? value.dropped : 0;
  response.truncated = value.truncated === true || value.items.length > AI_CATALOG_LIMITS.itemsMax;
  const seen = new Set<string>();
  for (const raw of value.items.slice(0, AI_CATALOG_LIMITS.itemsMax)) {
    const item = isRecord(raw) ? raw : undefined;
    const id = typeof item?.id === "string" && item.id.length <= AI_CATALOG_LIMITS.itemIdMaxLength && ITEM_ID_PATTERN.test(item.id) ? item.id : "";
    const name = boundedLine(item?.name, AI_CATALOG_LIMITS.itemNameMaxRunes, true);
    const description = boundedLine(item?.description, AI_CATALOG_LIMITS.itemDescriptionMaxRunes, false);
    const docsUrl = item?.docsUrl === undefined ? undefined : normalizeSearchResultURL(item.docsUrl) ?? false;
    const known = item ? Object.keys(item).every((key) => key === "id" || key === "name" || key === "description" || key === "docsUrl") : false;
    if (!id || !name || description === false || docsUrl === false || !known || seen.has(id)) {
      response.dropped += 1;
      continue;
    }
    seen.add(id);
    response.items.push({ id, name, ...(description ? { description } : {}), ...(docsUrl ? { docsUrl } : {}) });
  }
  return response;
}

/** Re-validates a `plugin_host_ai_catalog_install` response and returns its content. */
export function parseAiCatalogInstallResponse(value: unknown): string {
  const content = isRecord(value) && typeof value.content === "string" ? value.content : "";
  if (
    !content.trim() ||
    UNSAFE_CONTENT_PATTERN.test(content) ||
    new TextEncoder().encode(content).byteLength > AI_CATALOG_LIMITS.installResultMaxBytes
  ) {
    throw new Error("invalid_plugin_catalog_content");
  }
  return content;
}

/** Install-preview summary: the Discover sources a manifest would add. */
export function aiCatalogPreviewSummary(contributes: unknown): { kind: AiCatalogKind; title: string }[] {
  const entries = isRecord(contributes) ? contributes[AI_CATALOGS_CONTRIBUTION_POINT] : undefined;
  if (!Array.isArray(entries)) return [];
  return entries.slice(0, AI_CATALOG_LIMITS.maxEntries).flatMap((entry) => {
    if (!isRecord(entry) || !isKind(entry.kind)) return [];
    const title = typeof entry.title === "string" ? entry.title.trim().slice(0, AI_CATALOG_LIMITS.titleMaxRunes) : "";
    return title ? [{ kind: entry.kind, title }] : [];
  });
}

import type { LocationQueryRaw } from "vue-router";
import type { WorkspaceView } from "@/types/workspace";
import type { ServiceGroup } from "@/domain/plugins/services";
import {
  searchFileScopeIsCurrent,
  type SearchFileCurrentScope,
  type SearchFileScope,
  type SearchFileTarget,
  type SearchProviderRuntime,
} from "@/domain/plugins/search-providers";

export type CommandPaletteKind =
  | "command"
  | "workspace"
  | "tab"
  | "project"
  | "file"
  | "ssh"
  | "database"
  | "http"
  /** A result of a plugin search provider (contributes.searchProviders). */
  | "plugin-result"
  /** A plugin service offered on the palette (contributes.services). */
  | "plugin-service";

export type CommandPaletteAction =
  | { type: "open-view"; view: WorkspaceView; query?: LocationQueryRaw }
  | { type: "select-tab"; tabId: string }
  | { type: "switch-workspace"; workspaceId: string }
  | { type: "open-settings" }
  | { type: "open-ai" }
  | { type: "select-project"; root: string }
  | {
      type: "open-file";
      root: string;
      path: string;
      line?: number;
      column?: number;
    }
  | { type: "open-ssh"; profileId: string }
  | { type: "open-database"; connectionId: string }
  | { type: "open-http-request"; collectionId: string; requestId: string }
  | { type: "registered-command"; commandId: string }
  // Plugin search provider results (host-validated targets only).
  | {
      type: "plugin-open-view";
      pluginId: string;
      view: string;
      providerId: string;
      args?: Record<string, string | number | boolean>;
    }
  | {
      type: "plugin-run-command";
      pluginId: string;
      command: string;
      args?: Record<string, string | number | boolean>;
    }
  | { type: "open-url"; url: string }
  // A plugin search file result: keeps the host-filled scope and runtime of
  // its response; opened only after Go re-validates it (never from the store).
  | {
      type: "plugin-open-file";
      pluginId: string;
      providerId: string;
      fileScope: SearchFileScope;
      providerRuntime: SearchProviderRuntime;
      target: SearchFileTarget;
    }
  // Plugin service: the palette asks for its input, then runs it through plugin-services.ts.
  | { type: "plugin-service"; service: string };

export interface CommandPaletteSearchScope {
  /** Project-relative path rendered above the matching content. */
  path?: string;
}

export interface CommandPaletteSearchMetadata {
  /** Optional scope label for a content-search result. */
  scope?: CommandPaletteSearchScope;
  /** Bounded matching text rendered as a result preview. */
  detail?: string;
  /** One-based location in the matched file. */
  line?: number;
  /** One-based location in the matched line. */
  column?: number;
}

export interface CommandPaletteItem {
  id: string;
  kind: CommandPaletteKind;
  title: string;
  subtitle?: string;
  /**
   * Section label overriding the default kind grouping, used to split project
   * search results by the workspace project they came from.
   */
  group?: string;
  /** Optional content-search presentation data; file items retain kind `file`. */
  search?: CommandPaletteSearchMetadata;
  keywords?: string[];
  priority?: number;
  usageCount?: number;
  lastUsedAt?: number;
  /** Display label of the command's primary keybinding (e.g. "⌘,"). */
  keybinding?: string;
  action: CommandPaletteAction;
}

export type CommandPaletteScope = "all" | "files" | "content";

/** An open request for the lazily loaded palette host; `seq` changes per request. */
export interface CommandPaletteOpenRequest {
  seq: number;
  /** Preferred search scope; falls back to "all" when unavailable. */
  scope?: CommandPaletteScope;
  /** Initial query text, e.g. ">" to list commands. */
  prefix?: string;
}

export type CommandPaletteMode =
  | "all"
  | "commands"
  | "navigation"
  | "resources";

export interface ParsedCommandPaletteQuery {
  mode: CommandPaletteMode;
  query: string;
  prefix: "" | ">" | "@" | "#";
}

export interface CommandPaletteGroupEntry {
  item: CommandPaletteItem;
  /** Position in the flattened render order, shared with keyboard selection. */
  index: number;
}

export interface CommandPaletteGroup {
  /** Stable identity for rendering; either an explicit group label or a kind. */
  key: string;
  /** Ready-to-render label; absent when the caller should localize `kind`. */
  label?: string;
  kind: CommandPaletteKind;
  entries: CommandPaletteGroupEntry[];
}

const MODE_KINDS: Record<
  Exclude<CommandPaletteMode, "all">,
  ReadonlySet<CommandPaletteKind>
> = {
  commands: new Set(["command", "workspace", "plugin-service"]),
  navigation: new Set(["tab", "file"]),
  resources: new Set(["project", "ssh", "database", "http"]),
};

const KIND_ORDER: Record<CommandPaletteKind, number> = {
  command: 0,
  workspace: 1,
  tab: 2,
  project: 3,
  file: 4,
  ssh: 5,
  database: 6,
  http: 7,
  "plugin-result": 8,
  "plugin-service": 9,
};

/**
 * Palette items for services whose surfaces include the palette (groups from
 * servicesForPalette): one item per service, titled "<prefix><title>" with the
 * localized prefix, findable by service name, the providers' untranslated
 * titles and their plugin names.
 */
export function pluginServicePaletteItems(
  groups: readonly ServiceGroup[],
  prefix: string,
): CommandPaletteItem[] {
  return groups.map((group, index) => {
    const pluginNames = [...new Set(group.providers.map((entry) => entry.pluginName))];
    return {
      id: `plugin-service:${group.service}`,
      kind: "plugin-service",
      title: `${prefix}${group.title}`,
      subtitle: group.providers[0]?.description || pluginNames.join(" · "),
      // The untranslated title stays searchable next to the localized one.
      keywords: [...new Set([group.service, group.title, ...group.providers.map((entry) => entry.title), ...pluginNames])],
      priority: 90 + index,
      action: { type: "plugin-service", service: group.service },
    };
  });
}

/**
 * Drops plugin file results whose scope is no longer the published one; view,
 * command and URL results never depend on the file scope and are kept.
 */
export function withoutStalePluginFileResults(
  items: readonly CommandPaletteItem[],
  current: SearchFileCurrentScope | undefined,
): CommandPaletteItem[] {
  return items.filter(
    (item) =>
      item.action.type !== "plugin-open-file" ||
      searchFileScopeIsCurrent(item.action.fileScope, current),
  );
}

function normalize(value: string) {
  return value.trim().toLocaleLowerCase();
}

function fuzzySubsequence(value: string, query: string) {
  let queryIndex = 0;
  for (
    let index = 0;
    index < value.length && queryIndex < query.length;
    index += 1
  ) {
    if (value[index] === query[queryIndex]) queryIndex += 1;
  }
  return queryIndex === query.length;
}

function usageScore(item: CommandPaletteItem) {
  const lastUsedAt = item.lastUsedAt ?? 0;
  const count = Math.min(Math.max(item.usageCount ?? 0, 0), 100);
  return lastUsedAt + count * 86_400_000;
}

function matchScore(item: CommandPaletteItem, query: string) {
  if (!query) return 0;
  const title = normalize(item.title);
  const subtitle = normalize(item.subtitle ?? "");
  const keywords = (item.keywords ?? []).map(normalize);

  if (title === query) return 0;
  if (title.startsWith(query)) return 10;
  if (title.includes(query)) return 20;
  if (keywords.some((keyword) => keyword.startsWith(query))) return 30;
  if (keywords.some((keyword) => keyword.includes(query))) return 40;
  if (subtitle.includes(query)) return 50;
  if (fuzzySubsequence(title, query)) return 60;
  if (keywords.some((keyword) => fuzzySubsequence(keyword, query))) return 70;
  if (fuzzySubsequence(subtitle, query)) return 80;
  return Number.POSITIVE_INFINITY;
}

export function parseCommandPaletteQuery(
  value: string,
): ParsedCommandPaletteQuery {
  const trimmed = value.trimStart();
  const prefix = trimmed[0];
  if (prefix === ">")
    return { mode: "commands", query: normalize(trimmed.slice(1)), prefix };
  if (prefix === "@")
    return { mode: "navigation", query: normalize(trimmed.slice(1)), prefix };
  if (prefix === "#")
    return { mode: "resources", query: normalize(trimmed.slice(1)), prefix };
  return { mode: "all", query: normalize(trimmed), prefix: "" };
}

/**
 * Buckets ranked results into palette sections. Items carrying an explicit
 * `group` split by that label — project search uses it to separate results per
 * workspace project — and everything else falls back to grouping by kind.
 *
 * Groups appear in the order each key is first ranked, which keeps the strongest
 * match inside the leading group, and every entry carries the index it occupies
 * in the flattened render order so keyboard selection stays aligned with what is
 * on screen.
 */
export function groupCommandPaletteItems(
  items: readonly CommandPaletteItem[],
): CommandPaletteGroup[] {
  const order: string[] = [];
  const buckets = new Map<string, CommandPaletteItem[]>();
  for (const item of items) {
    const key = item.group ?? item.kind;
    const bucket = buckets.get(key);
    if (bucket) {
      bucket.push(item);
      continue;
    }
    buckets.set(key, [item]);
    order.push(key);
  }

  let index = 0;
  return order.map((key) => {
    const bucket = buckets.get(key) ?? [];
    const first = bucket[0];
    return {
      key,
      label: first?.group,
      kind: first?.kind ?? "command",
      entries: bucket.map((item) => ({ item, index: index++ })),
    };
  });
}

export function filterCommandPaletteItems(
  items: readonly CommandPaletteItem[],
  value: string,
  limit = 50,
): CommandPaletteItem[] {
  const parsed = parseCommandPaletteQuery(value);
  const allowedKinds =
    parsed.mode === "all" ? undefined : MODE_KINDS[parsed.mode];

  return items
    .map((item, index) => ({
      item,
      index,
      score: matchScore(item, parsed.query),
    }))
    .filter(
      ({ item, score }) =>
        (!allowedKinds || allowedKinds.has(item.kind)) &&
        Number.isFinite(score),
    )
    .sort((left, right) => {
      const scoreDifference = left.score - right.score;
      if (scoreDifference) return scoreDifference;
      const usageDifference = usageScore(right.item) - usageScore(left.item);
      if (usageDifference) return usageDifference;
      return (
        (left.item.priority ?? 100) - (right.item.priority ?? 100) ||
        KIND_ORDER[left.item.kind] - KIND_ORDER[right.item.kind] ||
        left.index - right.index
      );
    })
    .slice(0, Math.max(0, limit))
    .map(({ item }) => item);
}

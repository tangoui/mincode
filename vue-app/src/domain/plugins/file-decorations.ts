/**
 * Frontend stage of the `fileDecorations` contribution point:
 * re-validation of the providers the Go host accepted
 * (backend/plugin/contrib_file_decorations.go) and of the
 * `plugin_host_file_decorations` answer, the colour token mapping, which
 * tree nodes may carry a decoration and the per-provider request queue
 * (debounce, refresh cap, one request in flight, failure suspension, LRU
 * cache). The WebView only asks for one directory and the names it shows;
 * a decoration is a badge of at most two characters and/or a colour token,
 * never CSS, and never changes how the file name looks. No DOM, Vue or
 * IPC here; the request itself is injected.
 */
import type { PointContribution } from "@/domain/plugins/contributions";
import {
  PROVIDER_ACTION_ID_PATTERN,
  PROVIDER_ENTRY_ID_PATTERN,
  PROVIDER_PLUGIN_ID_PATTERN,
  boundedLine,
  hasUnsafeLineText,
  isRecord,
  utf8Bytes,
} from "@/domain/plugins/provider-points";

export const FILE_DECORATIONS_CONTRIBUTION_POINT = "fileDecorations";

/** Mirrors contribution-points.json fileDecorations maxEntries and the limits the frontend uses. */
export const FILE_DECORATION_LIMITS = {
  maxEntries: 1,
  titleMaxRunes: 32,
  namesMax: 500,
  nameMaxBytes: 255,
  directoryMaxBytes: 1024,
  badgeMaxRunes: 2,
  tooltipMaxRunes: 128,
  debounceMs: 150,
  refreshDirectoriesMax: 32,
  cachedDirectoriesMax: 64,
  failuresMax: 3,
  suspendMs: 60_000,
} as const;

export type DecorationColor = "accent" | "success" | "warning" | "danger" | "info" | "muted";

/** The only colours a decoration can have: theme tokens, keyed by the contract's lists.colors. */
export const DECORATION_COLOR_TOKENS: Readonly<Record<DecorationColor, string>> = Object.freeze({
  accent: "var(--app-accent)",
  success: "var(--app-success)",
  warning: "var(--app-warning)",
  danger: "var(--app-danger)",
  info: "var(--app-info)",
  muted: "var(--app-text-muted)",
});

export interface PluginDecorationProvider {
  pluginId: string;
  pluginName: string;
  id: string;
  title: string;
  activeInWorkspace: boolean;
}

export interface TreeDecoration {
  badge?: string;
  color?: DecorationColor;
  tooltip?: string;
  pluginName: string;
}

type Decoration = Omit<TreeDecoration, "pluginName">;

/** Plugin name (case-insensitive), then plugin id: the order in which providers win a node. */
function compareProviders(a: Pick<PluginDecorationProvider, "pluginId" | "pluginName">, b: typeof a) {
  const nameA = a.pluginName.toLowerCase();
  const nameB = b.pluginName.toLowerCase();
  if (nameA !== nameB) return nameA < nameB ? -1 : 1;
  return a.pluginId < b.pluginId ? -1 : a.pluginId > b.pluginId ? 1 : 0;
}

/**
 * The providers of the process plugins in the snapshot, at most maxEntries
 * per plugin, sorted by plugin name (case-insensitive) then plugin id.
 * Inactive plugins are kept with activeInWorkspace false.
 */
export function collectDecorationProviders(contributions: readonly PointContribution[]): PluginDecorationProvider[] {
  const providers: PluginDecorationProvider[] = [];
  for (const contribution of contributions) {
    if (contribution.runtime !== "process" || !PROVIDER_PLUGIN_ID_PATTERN.test(contribution.pluginId)) continue;
    const raw = contribution.point.entries[0];
    if (!isRecord(raw) || typeof raw.id !== "string" || !PROVIDER_ENTRY_ID_PATTERN.test(raw.id)) continue;
    const title = boundedLine(raw.title, FILE_DECORATION_LIMITS.titleMaxRunes, true);
    if (!title || typeof raw.action !== "string" || !PROVIDER_ACTION_ID_PATTERN.test(raw.action)) continue;
    providers.push({
      pluginId: contribution.pluginId,
      pluginName: contribution.pluginName,
      id: raw.id,
      title,
      activeInWorkspace: contribution.activeInWorkspace,
    });
  }
  return providers.sort(compareProviders);
}

const COLORS = new Set(Object.keys(DECORATION_COLOR_TOKENS));
// Letters, digits, punctuation or symbols only (Go: IsLetter/IsDigit/IsPunct/IsSymbol), no marks or format characters.
const BADGE_CHAR_PATTERN = /^[\p{L}\p{N}\p{P}\p{S}]$/u;

function validBadge(value: unknown): string | undefined {
  if (typeof value !== "string") return undefined;
  const badge = value.trim();
  const chars = [...badge];
  if (!chars.length || chars.length > FILE_DECORATION_LIMITS.badgeMaxRunes || hasUnsafeLineText(badge)) return undefined;
  return chars.every((char) => BADGE_CHAR_PATTERN.test(char)) ? badge : undefined;
}

function validDecoration(raw: unknown): Decoration | undefined {
  if (!isRecord(raw)) return undefined;
  const decoration: Decoration = {};
  if (raw.badge !== undefined) {
    const badge = validBadge(raw.badge);
    if (!badge) return undefined;
    decoration.badge = badge;
  }
  if (raw.color !== undefined) {
    if (typeof raw.color !== "string" || !COLORS.has(raw.color)) return undefined;
    decoration.color = raw.color as DecorationColor;
  }
  if (raw.tooltip !== undefined) {
    const tooltip = boundedLine(raw.tooltip, FILE_DECORATION_LIMITS.tooltipMaxRunes, false);
    if (tooltip === false) return undefined;
    if (tooltip) decoration.tooltip = tooltip;
  }
  return decoration.badge || decoration.color ? decoration : undefined;
}

/**
 * The decorations of a `plugin_host_file_decorations` answer for the names
 * that were requested. Keys that were not asked for and items with an
 * invalid badge, colour or tooltip are left out; anything else is an empty map.
 */
export function parseFileDecorationsResponse(value: unknown, requested: readonly string[]): Map<string, Decoration> {
  const result = new Map<string, Decoration>();
  const decorations = isRecord(value) ? value.decorations : undefined;
  if (!isRecord(decorations)) return result;
  for (const name of requested) {
    if (!Object.prototype.hasOwnProperty.call(decorations, name) || result.has(name)) continue;
    const decoration = validDecoration(decorations[name]);
    if (decoration) result.set(name, decoration);
  }
  return result;
}

/** Project roots, worktree nodes and the node being renamed never carry a plugin decoration. */
export function decorationEligible(node: { isProjectRoot?: boolean; worktree?: unknown; path: string }, renamingPath: string): boolean {
  return !node.isProjectRoot && !node.worktree && node.path !== "" && !(renamingPath && node.path === renamingPath);
}

/** The decoration of the first provider (callers pass them in provider order) that has one. */
export function pickDecoration(perProvider: readonly (TreeDecoration | undefined)[]): TreeDecoration | undefined {
  return perProvider.find((decoration) => decoration !== undefined);
}

// Go checkContributionRunes + contextRuneRefused: control, format (Cf) and line/paragraph separators.
const UNSAFE_PATH_CHAR = /[\p{Cc}\p{Cf}\u2028\u2029]/u;
// Go languageLocationPathCharsOK: URL/shell specials and look-alike dots and slashes.
const DIRECTORY_REFUSED_CHAR = /[%?#\\:\uff0e\uff61\u2024\u3002\ufe52\uff0f\u2215\u2044\u29f8\uff3c\u2216\u29f9\ufe68]/u;

/**
 * Whether the host accepts the directory of a decoration request (Go
 * normalizeFileDecorationsRequest: validateContributionRelativePath +
 * languageLocationPathCharsOK): "" or a clean relative path. Anything else
 * would come back invalid_request, so it is never sent.
 */
export function decorationDirectoryOK(directory: string): boolean {
  if (directory === "") return true;
  if (utf8Bytes(directory) > FILE_DECORATION_LIMITS.directoryMaxBytes || UNSAFE_PATH_CHAR.test(directory)) return false;
  if (DIRECTORY_REFUSED_CHAR.test(directory) || directory.startsWith("~") || directory.startsWith("$")) return false;
  return directory.split("/").every((segment) => segment !== "" && segment !== "." && segment !== "..");
}

/** Whether the host accepts a name of a decoration request (Go validFileDecorationName): one path segment. */
export function decorationNameOK(name: string): boolean {
  return name !== "" && name !== "." && name !== ".." && !/[/\\]/.test(name) &&
    utf8Bytes(name) <= FILE_DECORATION_LIMITS.nameMaxBytes && !UNSAFE_PATH_CHAR.test(name);
}

/**
 * Whether a host error code counts toward the failure gate: the provider
 * timed out, failed or answered badly, or the host could not reach it.
 * Requests the host refused (invalid_request, forbidden: e.g. a symlink
 * leaving the project), a vanished provider (not_found), too_large and
 * cancellations say nothing about the provider and never suspend it.
 */
export function decorationFailureCounts(code: string): boolean {
  return code === "timeout" || code === "unavailable" || code === "busy" || code === "invalid_result" || code === "plugin_error";
}

/** A directory of the open tree and the names it shows. */
export interface DecorationDirectory {
  projectRoot: string;
  directory: string;
  names: string[];
}

interface TreeNodeData {
  projectRoot: string;
  path: string;
  name: string;
  isDir?: boolean;
  isProjectRoot?: boolean;
  worktree?: unknown;
}

/** The names a directory shows to decorate: worktree and project nodes are projects, not children. */
export function decorationNamesOf(children: readonly TreeNodeData[]): string[] {
  const names: string[] = [];
  for (const child of children) {
    if (child.worktree || child.isProjectRoot) continue;
    names.push(child.name);
    if (names.length === FILE_DECORATION_LIMITS.namesMax) break;
  }
  return names;
}

/**
 * The directory of an el-tree node whose children are already loaded
 * (re-expansion: Element Plus calls load only once), else undefined.
 * The project root is the "" directory.
 */
export function loadedDirectoryOf(node: { loaded?: boolean; data?: TreeNodeData; childNodes?: readonly { data?: TreeNodeData }[] }): DecorationDirectory | undefined {
  const data = node.data;
  if (!node.loaded || !data?.isDir) return undefined;
  const children = (node.childNodes ?? []).flatMap((child) => (child.data ? [child.data] : []));
  return { projectRoot: data.projectRoot, directory: data.isProjectRoot ? "" : data.path, names: decorationNamesOf(children) };
}

/** Orders directories oldest first by the given recency keys (unknown ones first), so a capped round keeps the most recent. */
export function orderByRecency<T extends { projectRoot: string; directory: string }>(entries: readonly T[], recentKeys: readonly string[]): T[] {
  const rank = new Map(recentKeys.map((key, index) => [key, index]));
  const position = (entry: T) => rank.get(`${entry.projectRoot}\u0000${entry.directory}`) ?? -1;
  return entries.map((entry, index) => ({ entry, index })).sort((a, b) => position(a.entry) - position(b.entry) || a.index - b.index).map(({ entry }) => entry);
}

export interface DecorationQueueOptions {
  debounceMs: number;
  refreshDirectoriesMax: number;
  cachedDirectoriesMax: number;
  failuresMax: number;
  suspendMs: number;
  now(): number;
  setTimer(fn: () => void, ms: number): unknown;
  clearTimer(handle: unknown): void;
  request(
    provider: PluginDecorationProvider,
    projectRoot: string,
    directory: string,
    names: string[],
    signal: AbortSignal,
  ): Promise<Map<string, Decoration> | "failed" | "rejected">;
}

export interface DecorationQueue {
  enqueue(provider: PluginDecorationProvider, projectRoot: string, directory: string, names: string[]): void;
  cancelDirectory(projectRoot: string, directory: string): void;
  clear(pluginId?: string): void;
  /** Drops queued and in-flight requests of every provider; cache, failure count and suspension stay. */
  cancelAll(): void;
  lookup(projectRoot: string, path: string): TreeDecoration | undefined;
  onChange(fn: () => void): () => void;
}

interface DirectoryJob {
  key: string;
  projectRoot: string;
  directory: string;
  names: string[];
}

interface ProviderState {
  provider: PluginDecorationProvider;
  /** Enqueued during the debounce window, most recent last. */
  pending: Map<string, DirectoryJob>;
  timer?: unknown;
  /** The current round (at most refreshDirectoriesMax), sent one at a time. */
  round: DirectoryJob[];
  inFlight?: { job: DirectoryJob; controller: AbortController };
  failures: number;
  suspendedUntil: number;
  /** Directory key → name → decoration; Map order is least recently stored first. */
  cache: Map<string, Map<string, Decoration>>;
}

// NUL never occurs in a project root or relative path, so the key is unambiguous.
const directoryKey = (projectRoot: string, directory: string) => `${projectRoot}\u0000${directory}`;

function within(directory: string, parent: string) {
  return parent === "" || directory === parent || directory.startsWith(`${parent}/`);
}

/**
 * One queue per provider: enqueues within debounceMs are merged into a
 * round of at most refreshDirectoriesMax directories (the most recently
 * enqueued; older ones wait for the next expansion), sent one request at a
 * time. Directories and names the host would refuse are left out, and a
 * directory without names is not sent. failuresMax consecutive failures
 * suspend the provider for suspendMs (rounds that start meanwhile are
 * dropped, cached decorations stay); a rejected request is not a failure. Each
 * provider caches cachedDirectoriesMax directories, least recently stored
 * evicted first.
 */
export function createDecorationQueue(options: DecorationQueueOptions): DecorationQueue {
  const states = new Map<string, ProviderState>();
  const listeners = new Set<() => void>();
  let ordered: ProviderState[] = [];

  const notify = () => {
    for (const listener of [...listeners]) listener();
  };
  const reorder = () => {
    ordered = [...states.values()].sort((a, b) => compareProviders(a.provider, b.provider));
  };

  function stateFor(provider: PluginDecorationProvider) {
    const key = `${provider.pluginId}/${provider.id}`;
    let state = states.get(key);
    if (!state) {
      state = { provider, pending: new Map(), round: [], failures: 0, suspendedUntil: 0, cache: new Map() };
      states.set(key, state);
      reorder();
    } else if (state.provider !== provider) {
      state.provider = provider;
      reorder();
    }
    return state;
  }

  function startRound(state: ProviderState) {
    state.timer = undefined;
    const jobs = [...state.pending.values()];
    state.pending.clear();
    if (options.now() < state.suspendedUntil) return;
    const merged = new Map(state.round.map((job) => [job.key, job]));
    for (const job of jobs) {
      merged.delete(job.key);
      merged.set(job.key, job);
    }
    state.round = [...merged.values()].slice(-options.refreshDirectoriesMax);
    pump(state);
  }

  function pump(state: ProviderState) {
    if (state.inFlight || options.now() < state.suspendedUntil) return;
    const job = state.round.shift();
    if (!job) return;
    const controller = new AbortController();
    const inFlight = { job, controller };
    state.inFlight = inFlight;
    const finish = (result: Map<string, Decoration> | "failed" | "rejected") => {
      if (state.inFlight !== inFlight) return;
      state.inFlight = undefined;
      if (controller.signal.aborted || result === "rejected") return pump(state);
      if (result === "failed") {
        state.failures += 1;
        if (state.failures >= options.failuresMax) {
          state.failures = 0;
          state.suspendedUntil = options.now() + options.suspendMs;
          state.round = [];
          return;
        }
      } else {
        state.failures = 0;
        state.cache.delete(job.key);
        state.cache.set(job.key, result);
        while (state.cache.size > options.cachedDirectoriesMax) state.cache.delete(state.cache.keys().next().value as string);
        notify();
      }
      pump(state);
    };
    options.request(state.provider, job.projectRoot, job.directory, job.names, controller.signal).then(finish, () => finish("failed"));
  }

  function dropState(state: ProviderState) {
    if (state.timer !== undefined) options.clearTimer(state.timer);
    state.timer = undefined;
    state.pending.clear();
    state.round = [];
    state.inFlight?.controller.abort();
    state.inFlight = undefined;
  }

  return {
    enqueue(provider, projectRoot, directory, names) {
      if (!decorationDirectoryOK(directory)) return;
      const sendable = [...new Set(names.filter(decorationNameOK))].slice(0, FILE_DECORATION_LIMITS.namesMax);
      if (!sendable.length) return;
      const state = stateFor(provider);
      const key = directoryKey(projectRoot, directory);
      state.pending.delete(key);
      state.pending.set(key, { key, projectRoot, directory, names: sendable });
      if (state.timer !== undefined) options.clearTimer(state.timer);
      state.timer = options.setTimer(() => startRound(state), options.debounceMs);
    },
    cancelDirectory(projectRoot, directory) {
      const cancelled = (job: DirectoryJob) => job.projectRoot === projectRoot && within(job.directory, directory);
      for (const state of states.values()) {
        for (const [key, job] of state.pending) if (cancelled(job)) state.pending.delete(key);
        state.round = state.round.filter((job) => !cancelled(job));
        if (state.inFlight && cancelled(state.inFlight.job)) {
          state.inFlight.controller.abort();
          state.inFlight = undefined;
          pump(state);
        }
      }
    },
    clear(pluginId) {
      for (const [key, state] of states) {
        if (pluginId !== undefined && state.provider.pluginId !== pluginId) continue;
        dropState(state);
        states.delete(key);
      }
      reorder();
      notify();
    },
    cancelAll() {
      for (const state of states.values()) dropState(state);
    },
    lookup(projectRoot, path) {
      const slash = path.lastIndexOf("/");
      const key = directoryKey(projectRoot, slash < 0 ? "" : path.slice(0, slash));
      const name = path.slice(slash + 1);
      return pickDecoration(
        ordered.map((state) => {
          const decoration = state.cache.get(key)?.get(name);
          return decoration && { ...decoration, pluginName: state.provider.pluginName };
        }),
      );
    },
    onChange(fn) {
      listeners.add(fn);
      return () => listeners.delete(fn);
    },
  };
}

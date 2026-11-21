/**
 * Runtime registry for plugin contribution points. The Go host owns
 * validation and returns every enabled plugin's contributions through
 * `plugin_host_contributions` (nothing is activated). Each point has one
 * frontend consumer, registered with `registerContributionConsumer`; this
 * module fetches the snapshot for the registered points, and hands every
 * consumer its point's data whenever that data changes.
 *
 * - Refreshes run at startup and on `plugin:contributions-changed`
 *   (debounced 50 ms). A generation counter plus the host's monotonically
 *   increasing revision discard stale responses.
 * - A consumer's previous registration is disposed before the new data is
 *   applied, and only when the point's data actually changed.
 * - A failed refresh keeps the last applied state and records the error.
 * - Browser preview has no plugin host: nothing is fetched or applied.
 * - It runs in the main window and in popup windows alike (keymaps apply
 *   to both).
 */
import {
  parseContributionsChanged,
  pointContributions,
  pointContributionsKey,
  type PluginContributionsSnapshot,
  type PointContribution,
} from "@/domain/plugins/contributions";
import { fetchPluginContributions } from "@/services/plugin-service";
import { isDesktopRuntime, listenAppEvent } from "@/services/desktop-bridge";

export const PLUGIN_CONTRIBUTIONS_CHANGED_EVENT = "plugin:contributions-changed";
const REFRESH_DEBOUNCE_MS = 50;

export interface ContributionConsumer {
  /** contributes.<point> key; must be "enabled" in contribution-points.json. */
  point: string;
  /**
   * Applies the point's current contributions (possibly empty). Returns a
   * disposer that removes everything it registered; it runs before the next
   * apply and when the sync stops.
   */
  apply: (contributions: PointContribution[]) => (() => void) | void;
  /** Receives refresh, apply and dispose failures; the last applied state stays in place. */
  onError?: (message: string) => void;
}

interface ConsumerState {
  consumer: ContributionConsumer;
  appliedKey?: string;
  dispose?: () => void;
}

const consumers = new Map<string, ConsumerState>();
let snapshot: PluginContributionsSnapshot | undefined;
let generation = 0;
let running = false;
let timer: ReturnType<typeof setTimeout> | undefined;
let stopListener: (() => void) | undefined;
let lastSyncError: string | undefined;

function errorMessage(error: unknown) {
  return error instanceof Error ? error.message : String(error);
}

function disposeConsumer(state: ConsumerState) {
  const dispose = state.dispose;
  state.dispose = undefined;
  state.appliedKey = undefined;
  try {
    dispose?.();
  } catch (error) {
    state.consumer.onError?.(`dispose ${state.consumer.point} contributions: ${errorMessage(error)}`);
  }
}

function applyConsumer(state: ConsumerState, current: PluginContributionsSnapshot) {
  const contributions = pointContributions(current, state.consumer.point);
  const key = pointContributionsKey(contributions);
  if (state.appliedKey === key) return;
  disposeConsumer(state);
  try {
    state.dispose = state.consumer.apply(contributions) ?? undefined;
    state.appliedKey = key;
  } catch (error) {
    state.consumer.onError?.(`apply ${state.consumer.point} contributions: ${errorMessage(error)}`);
  }
}

/**
 * Registers the single consumer of a point. When a snapshot is already
 * loaded it is applied immediately. Returns an unregister function that also
 * disposes what the consumer registered.
 */
export function registerContributionConsumer(consumer: ContributionConsumer): () => void {
  if (consumers.has(consumer.point)) throw new Error(`contribution point ${consumer.point} already has a consumer`);
  const state: ConsumerState = { consumer };
  consumers.set(consumer.point, state);
  if (running) {
    if (snapshot && snapshot.plugins.some((plugin) => consumer.point in plugin.points)) applyConsumer(state, snapshot);
    // The last snapshot was fetched for the previous point set; fetch again with this point included.
    scheduleContributionRefresh(0);
  }
  return () => {
    if (consumers.get(consumer.point) !== state) return;
    consumers.delete(consumer.point);
    disposeConsumer(state);
  };
}

/** Fetches the snapshot for the registered points and applies what changed. */
export async function refreshPluginContributions(): Promise<void> {
  if (!running) return;
  const current = ++generation;
  const points = [...consumers.keys()].sort();
  if (points.length === 0) return;
  try {
    const next = await fetchPluginContributions(points);
    // A newer refresh started, or the host already answered with a newer revision.
    if (current !== generation || !running) return;
    if (snapshot && next.revision < snapshot.revision) return;
    snapshot = next;
    lastSyncError = undefined;
    for (const state of consumers.values()) applyConsumer(state, next);
  } catch (error) {
    if (current !== generation) return;
    lastSyncError = `load plugin contributions: ${errorMessage(error)}`;
    for (const state of consumers.values()) state.consumer.onError?.(lastSyncError);
  }
}

function scheduleContributionRefresh(delay = REFRESH_DEBOUNCE_MS) {
  if (!running) return;
  if (timer) clearTimeout(timer);
  timer = setTimeout(() => {
    timer = undefined;
    void refreshPluginContributions();
  }, delay);
}

/** The error of the last failed refresh (plugin host unavailable), if any. */
export function pluginContributionSyncError(): string | undefined {
  return lastSyncError;
}

/** The last applied snapshot (diagnostics). */
export function pluginContributionsSnapshot(): PluginContributionsSnapshot | undefined {
  return snapshot;
}

/**
 * Starts syncing in the desktop runtime and returns a stop function that
 * disposes every consumer's registrations. Browser preview: no-op.
 */
export function startContributionSync(): () => void {
  if (!isDesktopRuntime() || running) return () => undefined;
  running = true;
  stopListener = listenAppEvent<unknown>(PLUGIN_CONTRIBUTIONS_CHANGED_EVENT, (payload) => {
    const event = parseContributionsChanged(payload);
    // Already reflected by a newer snapshot.
    if (snapshot && event.revision > 0 && event.revision <= snapshot.revision) return;
    scheduleContributionRefresh();
  });
  void refreshPluginContributions();
  return () => {
    if (!running) return;
    running = false;
    generation++;
    if (timer) clearTimeout(timer);
    timer = undefined;
    stopListener?.();
    stopListener = undefined;
    for (const state of consumers.values()) disposeConsumer(state);
    snapshot = undefined;
  };
}

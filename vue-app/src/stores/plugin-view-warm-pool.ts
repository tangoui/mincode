// Warm pool of closed plugin view tabs (see domain/plugin-ui/warm-pool.ts).
//
// Intentionally global and never persisted: it only names live frames that the
// per-workspace PluginWorkspaceView hosts still keep mounted, every entry
// carries its workspace ID, and the limit applies across workspaces.
//
// The store owns invalidation. A parked frame is destroyed (its UI session
// closed) when:
// - the backend revokes the plugin's UI ("plugin:ui-revoked": disable, reload,
//   crash/error, stop, uninstall, upgrade/downgrade, Clear Plugin Data,
//   development generation swap, workspace reconcile stopping the plugin);
// - any "plugin:state-changed" or "plugin:development-changed" event names the
//   plugin (state, enablement or generation may have changed);
// - the active workspace changes or the backend reconciles workspaces
//   ("plugin:workspace-changed"): only the active workspace keeps entries;
// - it is older than PLUGIN_VIEW_WARM_TTL_MS, or the least recently parked
//   beyond the configured limit (0 disables the pool and empties it);
// - its frame leaves the ready state (PluginWorkspaceView reports it), or its
//   host unmounts.
// Reuse is additionally revalidated by PluginViewFrame before it is shown.
import { acceptHMRUpdate, defineStore } from "pinia";
import { computed, ref, watch } from "vue";
import {
  expirePluginViewWarmEntries,
  nextPluginViewWarmExpiry,
  normalizePluginViewWarmLimit,
  parkPluginViewWarmEntry,
  PLUGIN_UI_REVOKED_EVENT,
  PLUGIN_VIEW_WARM_TTL_MS,
  takePluginViewWarmEntry,
  trimPluginViewWarmEntries,
  type PluginViewWarmEntry,
} from "@/domain/plugin-ui/warm-pool";
import { readSettingsSnapshot, settingsSnapshotVersion } from "@/services/settings-persistence";
import { isDesktopRuntime, listenAppEvent } from "@/services/desktop-bridge";
import { useWorkbenchStore } from "@/stores/workbench";

export { PLUGIN_UI_REVOKED_EVENT };

function readLimit() {
  try {
    return normalizePluginViewWarmLimit(readSettingsSnapshot().pluginViewWarmFrames);
  } catch {
    return normalizePluginViewWarmLimit(undefined);
  }
}

function eventPluginId(value: unknown, field: "id" | "pluginId") {
  if (!value || typeof value !== "object") return "";
  const id = (value as Record<string, unknown>)[field];
  return typeof id === "string" ? id.trim() : "";
}

// Listeners and watchers of the current store instance; an HMR update disposes
// them before the new module installs its own, so nothing is registered twice.
const disposers: Array<() => void> = [];
function stopOnDispose(stop: () => void) {
  disposers.push(stop);
}

export const usePluginViewWarmPoolStore = defineStore("plugin-view-warm-pool", () => {
  const entries = ref<PluginViewWarmEntry[]>([]);
  const limit = ref(readLimit());
  const ttlMs = ref(PLUGIN_VIEW_WARM_TTL_MS);
  const workbench = useWorkbenchStore();
  let expiryTimer: ReturnType<typeof setTimeout> | undefined;
  // Connected (ready) workspace page frames, by tab ID: closing one of these tabs parks it.
  const readyFrames = new Map<string, Omit<PluginViewWarmEntry, "parkedAt">>();

  const workspaceIds = computed(() => [...new Set(entries.value.map((entry) => entry.workspaceId))]);

  function scheduleExpiry() {
    if (expiryTimer) clearTimeout(expiryTimer);
    expiryTimer = undefined;
    const delay = nextPluginViewWarmExpiry(entries.value, Date.now(), ttlMs.value);
    if (delay === undefined) return;
    expiryTimer = setTimeout(() => {
      expiryTimer = undefined;
      expire();
    }, delay + 5);
  }

  function setEntries(next: PluginViewWarmEntry[]) {
    entries.value = next;
    scheduleExpiry();
  }

  function expire(now = Date.now()) {
    const result = expirePluginViewWarmEntries(entries.value, now, ttlMs.value);
    if (result.expired.length > 0) entries.value = result.entries;
    scheduleExpiry();
    return result.expired;
  }

  function entriesFor(workspaceId: string) {
    return entries.value.filter((entry) => entry.workspaceId === workspaceId);
  }

  function has(key: string) {
    return entries.value.some((entry) => entry.key === key);
  }

  /** Parks a closed tab's frame; returns false when the pool is disabled (the caller destroys it). */
  function park(entry: Omit<PluginViewWarmEntry, "parkedAt">, now = Date.now()) {
    if (limit.value <= 0) return false;
    const result = parkPluginViewWarmEntry(entries.value, { ...entry, parkedAt: now }, limit.value);
    setEntries(result.entries);
    return result.entries.some((item) => item.key === entry.key);
  }

  /** Takes the entry of a reopened tab; false when none matches (the frame must start fresh). */
  function take(target: { key: string; workspaceId: string; pluginId: string; viewId: string }, now = Date.now()) {
    const result = takePluginViewWarmEntry(entries.value, target, now, ttlMs.value);
    if (result.entries.length !== entries.value.length) setEntries(result.entries);
    return Boolean(result.entry);
  }

  function discardWhere(predicate: (entry: PluginViewWarmEntry) => boolean) {
    if (!entries.value.some(predicate)) return;
    setEntries(entries.value.filter((entry) => !predicate(entry)));
  }

  function discard(key: string) {
    discardWhere((entry) => entry.key === key);
  }

  function discardPlugin(pluginId: string) {
    if (pluginId) discardWhere((entry) => entry.pluginId === pluginId);
  }

  function discardWorkspace(workspaceId: string) {
    discardWhere((entry) => entry.workspaceId === workspaceId);
  }

  function discardInactiveWorkspaces() {
    const active = workbench.activeWorkspaceId;
    discardWhere((entry) => entry.workspaceId !== active);
  }

  function clear() {
    setEntries([]);
  }

  function setLimit(value: unknown) {
    limit.value = normalizePluginViewWarmLimit(value);
    const result = trimPluginViewWarmEntries(entries.value, limit.value);
    if (result.evicted.length > 0) setEntries(result.entries);
  }

  // The limit follows the settings from the start, so a change made before the first park applies to it.
  stopOnDispose(watch(settingsSnapshotVersion, () => setLimit(readLimit())));

  /** A workspace page frame completed its handshake; closing its tab will park it. */
  function trackFrame(frame: Omit<PluginViewWarmEntry, "parkedAt">) {
    readyFrames.set(frame.key, { ...frame });
  }

  /** The frame is no longer ready (failed, popped out, unmounted): closing its tab destroys it. */
  function untrackFrame(key: string) {
    readyFrames.delete(key);
  }

  // Parks in the same synchronous step that removes the tab, before any
  // component re-renders: App.vue keeps a workspace's plugin host mounted
  // while it has entries, so closing the workspace's last plugin tab must
  // never unmount (and destroy) the frame that is about to be parked. A tab
  // that still exists (popped out) is not closed and is never parked; only
  // tabs of the active workspace are parked.
  function parkClosedFrames(tabIds: readonly string[]) {
    if (readyFrames.size === 0) return;
    const present = new Set(tabIds);
    for (const [key, frame] of [...readyFrames]) {
      if (present.has(key)) continue;
      readyFrames.delete(key);
      if (frame.workspaceId === workbench.activeWorkspaceId) park(frame);
    }
  }
  stopOnDispose(watch(() => workbench.tabs.map((tab) => tab.id), parkClosedFrames, { flush: "sync" }));
  stopOnDispose(watch(() => workbench.activeWorkspaceId, () => discardInactiveWorkspaces()));
  // Revocation listeners are installed with the store (App.vue creates it at startup).
  if (isDesktopRuntime()) {
    stopOnDispose(listenAppEvent<unknown>(PLUGIN_UI_REVOKED_EVENT, (event) => discardPlugin(eventPluginId(event, "pluginId"))));
    stopOnDispose(listenAppEvent<unknown>("plugin:state-changed", (event) => discardPlugin(eventPluginId(event, "id"))));
    stopOnDispose(listenAppEvent<unknown>("plugin:development-changed", (event) => discardPlugin(eventPluginId(event, "pluginId"))));
    stopOnDispose(listenAppEvent<unknown>("plugin:workspace-changed", () => discardInactiveWorkspaces()));
  }

  return {
    entries,
    limit,
    ttlMs,
    workspaceIds,
    entriesFor,
    has,
    park,
    take,
    expire,
    discard,
    discardPlugin,
    discardWorkspace,
    discardInactiveWorkspaces,
    clear,
    setLimit,
    trackFrame,
    untrackFrame,
  };
});

if (import.meta.hot) {
  import.meta.hot.dispose(() => {
    for (const stop of disposers.splice(0)) stop();
  });
  import.meta.hot.accept(acceptHMRUpdate(usePluginViewWarmPoolStore, import.meta.hot));
}

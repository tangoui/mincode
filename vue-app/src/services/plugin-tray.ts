/**
 * Keeps the plugin section of the system tray menu in sync (`menus.tray`,
 * host 0.3.0). Loaded on demand by the menus consumer
 * (services/contributions/menus.ts) only when a snapshot has tray entries,
 * so the entry chunk carries none of this.
 *
 * The visible set is recomputed whenever the plugin command table (enabled
 * plugins effective in this workspace; `plugin:context-changed` refreshes the
 * contexts), the app lock or the UI language changes, and sent — debounced
 * and deduplicated — through `plugin_host_tray_set`. Locked: the empty set.
 * Only the main window syncs: popup windows have no command table and would
 * otherwise send an empty set. Browser preview has no tray.
 */
import { watch } from "vue";
import { createTraySync, trayItemsToSync, type TrayEntry } from "@/domain/plugins/tray";
import { useVueI18n } from "@/i18n/messages";
import { pluginCommandState } from "@/services/plugin-commands";
import { isPopupWindow } from "@/services/popup-tab";
import { invokeBackend, isDesktopRuntime } from "@/services/desktop-bridge";
import { useAppLockStore } from "@/stores/app-lock";

// One sync for the window: a new snapshot's start supersedes the previous
// cleanup's pending empty set instead of flashing an empty tray.
const sync = createTraySync({
  invoke: (args) => invokeBackend("plugin_host_tray_set", { ...args }),
  setTimer: (callback, ms) => setTimeout(callback, ms),
  clearTimer: (handle) => clearTimeout(handle as ReturnType<typeof setTimeout>),
});

/** Starts syncing these declared entries; the cleanup sends the empty set once and stops watching. */
export function startPluginTraySync(entries: readonly TrayEntry[]): () => void {
  if (!isDesktopRuntime() || isPopupWindow()) return () => {};
  const appLock = useAppLockStore();
  const { localeChain } = useVueI18n();
  // A new snapshot always syncs once: the host may have dropped a group
  // (plugin crashed or disabled) while the visible set stayed the same.
  sync.resync();
  const stop = watch(
    [pluginCommandState, () => appLock.locked, localeChain],
    ([state, locked, locale]) => sync.update(locale, trayItemsToSync(entries, state.commands, state.contexts, locked)),
    { immediate: true },
  );
  return () => {
    stop();
    sync.clear(localeChain.value);
  };
}

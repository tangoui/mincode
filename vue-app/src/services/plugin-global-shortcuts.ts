/**
 * Plugin global shortcuts (settings → shortcuts). The Go host owns the
 * registrations, conflicts and the local-only settings
 * (backend/plugin/global_shortcuts.go); this adapter only calls its
 * dedicated commands and listens for `plugin:global-shortcuts-changed`.
 * Browser preview has no plugin host: `globalShortcutsAvailable()` is false and
 * the settings page says so instead of showing fake entries.
 */
import {
  parseGlobalShortcutStatus,
  parseGlobalShortcutStatuses,
  type GlobalShortcutStatus,
} from "@/domain/keybindings/plugin-global-shortcuts";
import { invokeBackend, isDesktopRuntime, listenAppEvent } from "@/services/desktop-bridge";

export const GLOBAL_SHORTCUTS_CHANGED_EVENT = "plugin:global-shortcuts-changed";

export function globalShortcutsAvailable(): boolean {
  return isDesktopRuntime();
}

export async function listGlobalShortcuts(): Promise<GlobalShortcutStatus[]> {
  if (!globalShortcutsAvailable()) return [];
  return parseGlobalShortcutStatuses(await invokeBackend<unknown>("plugin_global_shortcuts_list"));
}

function status(value: unknown, command: string): GlobalShortcutStatus {
  const parsed = parseGlobalShortcutStatus(value);
  if (!parsed) throw new Error(`${command}: invalid response`);
  return parsed;
}

/** Turns an entry on or off and/or changes its key ("" restores the default). */
export async function setGlobalShortcut(change: {
  pluginId: string;
  id: string;
  enabled?: boolean;
  key?: string;
}): Promise<GlobalShortcutStatus> {
  return status(await invokeBackend<unknown>("plugin_global_shortcuts_set", { ...change }), "plugin_global_shortcuts_set");
}

/** Restores the manifest default key (the switch is unchanged). */
export async function resetGlobalShortcut(pluginId: string, id: string): Promise<GlobalShortcutStatus> {
  return status(await invokeBackend<unknown>("plugin_global_shortcuts_reset", { pluginId, id }), "plugin_global_shortcuts_reset");
}

/** Pushes the current in-app keys (accelerators) for the backend's overlap note. */
export async function setGlobalShortcutInAppKeys(keys: readonly string[]): Promise<void> {
  if (!globalShortcutsAvailable()) return;
  await invokeBackend<null>("plugin_global_shortcuts_set_inapp_keys", { keys: [...keys] });
}

export function onGlobalShortcutsChanged(handler: () => void): () => void {
  if (!globalShortcutsAvailable()) return () => undefined;
  return listenAppEvent<unknown>(GLOBAL_SHORTCUTS_CHANGED_EVENT, () => handler());
}

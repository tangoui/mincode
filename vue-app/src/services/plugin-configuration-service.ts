/**
 * IPC adapter for plugin settings (`contributes.configuration`). Every
 * payload is validated by domain/plugins/configuration.ts before it reaches
 * the UI. There is deliberately no command that reads a secret setting back:
 * secrets are write-only from the frontend.
 */
import {
  parseConfigurationState,
  parsePluginConfigurationSync,
  type PluginConfigurationScope,
  type PluginConfigurationState,
  type PluginConfigurationSyncData,
  type PluginConfigurationValue,
} from "@/domain/plugins/configuration";
import { PLUGIN_CONTRIBUTIONS_CHANGED_EVENT } from "./plugin-contributions";
import { invokeBackend, isDesktopRuntime, listenAppEvent } from "./desktop-bridge";

export const PLUGIN_CONFIGURATION_CHANGED_EVENT = "plugin:configuration-changed";

export interface PluginConfigurationChanged {
  pluginId: string;
  keys: string[];
}

/** Installed plugins (enabled or not) that declare a valid settings schema. */
export async function listConfigurablePlugins(): Promise<string[]> {
  const raw = await invokeBackend<unknown>("plugin_host_configuration_list", {});
  return Array.isArray(raw) ? raw.filter((id): id is string => typeof id === "string" && id.trim() !== "") : [];
}

export async function getPluginConfiguration(id: string, workspaceId?: string): Promise<PluginConfigurationState> {
  const raw = await invokeBackend<unknown>("plugin_host_configuration_get", { id, ...(workspaceId ? { workspaceId } : {}) });
  const state = parseConfigurationState(raw);
  if (!state) throw new Error(`plugin_host_configuration_get: invalid settings payload for ${id}`);
  return state;
}

/** Writes values into one scope; `null` removes a key so the inherited value applies again. */
export function setPluginConfiguration(
  id: string,
  scope: PluginConfigurationScope,
  values: Record<string, PluginConfigurationValue | null>,
  workspaceId?: string,
) {
  return invokeBackend<boolean>("plugin_host_configuration_set", { id, scope, values, ...(workspaceId ? { workspaceId } : {}) });
}

/** Removes every stored non-secret value (used to recover an unreadable store). */
export function resetPluginConfiguration(id: string) {
  return invokeBackend<boolean>("plugin_host_configuration_set", { id, resetAll: true });
}

/** Stores a secret setting; `null` clears it. Accepts orphaned keys for clearing. */
export function setPluginConfigurationSecret(id: string, key: string, value: string | null) {
  return invokeBackend<boolean>("plugin_host_configuration_set_secret", { id, key, value });
}

export function listenPluginConfigurationChanged(handler: (event: PluginConfigurationChanged) => void) {
  return listenAppEvent<unknown>(PLUGIN_CONFIGURATION_CHANGED_EVENT, (payload) => {
    const raw = payload && typeof payload === "object" ? payload as Record<string, unknown> : undefined;
    const pluginId = typeof raw?.pluginId === "string" ? raw.pluginId : "";
    if (!pluginId) return;
    const keys = Array.isArray(raw?.keys) ? raw.keys.filter((key): key is string => typeof key === "string") : [];
    handler({ pluginId, keys });
  });
}

/**
 * Host events after which the configurable-plugin list or a plugin's schema
 * may have changed: install/uninstall, enable/disable, development session
 * attach/detach/generation swap (all announced as
 * `plugin:contributions-changed`), plus state and workspace changes. The
 * handler receives no payload; callers debounce and re-read. Returns a stop
 * function that removes every listener.
 */
export function listenPluginSettingsInvalidated(handler: () => void): () => void {
  const stops = [
    listenAppEvent<unknown>(PLUGIN_CONTRIBUTIONS_CHANGED_EVENT, () => handler()),
    listenAppEvent<unknown>("plugin:state-changed", () => handler()),
    listenAppEvent<unknown>("plugin:workspace-changed", () => handler()),
  ];
  return () => {
    for (const stop of stops) stop();
  };
}

/** Global non-secret plugin settings for configuration sync; undefined outside the desktop runtime. */
export async function exportPluginConfigurationSync(): Promise<PluginConfigurationSyncData | undefined> {
  if (!isDesktopRuntime()) return undefined;
  return parsePluginConfigurationSync(await invokeBackend<unknown>("plugin_host_configuration_sync_export", {}));
}

export async function importPluginConfigurationSync(data: PluginConfigurationSyncData) {
  if (!isDesktopRuntime()) throw new Error("plugin_host_configuration_sync_import requires the MinCode desktop runtime");
  return invokeBackend<{ applied: string[]; skipped: string[] }>("plugin_host_configuration_sync_import", { data });
}

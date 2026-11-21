import { loadPersistedValue, savePersistedValue } from "@/services/persistent-store";

export const PLUGIN_MENU_EVENT = "app:plugin-menu-changed";
export const PLUGIN_CATALOG_EVENT = "app:plugin-catalog-changed";
const KEY = "app_plugin_menu_v1";
export function normalizePluginMenu(value: unknown): string[] {
  return Array.isArray(value) ? [...new Set(value.filter((id): id is string => typeof id === "string" && !!id.trim()))] : [];
}
export async function loadPluginMenu(): Promise<string[]> {
  return normalizePluginMenu(await loadPersistedValue<unknown>(KEY));
}
export async function savePluginMenu(ids: string[]): Promise<void> {
  const normalized = normalizePluginMenu(ids);
  await savePersistedValue(KEY, normalized);
  window.dispatchEvent(new CustomEvent(PLUGIN_MENU_EVENT, { detail: normalized }));
}

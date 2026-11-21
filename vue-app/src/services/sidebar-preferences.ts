import { loadPersistedValue, savePersistedValue } from "@/services/persistent-store";
import { sidebarItems, type SidebarItemId } from "@/services/sidebar-items";

export type { SidebarItemId } from "@/services/sidebar-items";
export type SidebarVisibility = Record<SidebarItemId, boolean>;

export const SIDEBAR_VISIBILITY_KEY = "app_sidebar_visibility_v1";
export const SIDEBAR_VISIBILITY_EVENT = "app:sidebar-visibility-changed";

export const sidebarItemIds: readonly SidebarItemId[] = sidebarItems.map((item) => item.id);

export function createDefaultSidebarVisibility(): SidebarVisibility {
  return Object.fromEntries(sidebarItemIds.map((id) => [id, true])) as SidebarVisibility;
}

export function normalizeSidebarVisibility(value: unknown): SidebarVisibility {
  const source = value && typeof value === "object" && !Array.isArray(value)
    ? value as Record<string, unknown>
    : {};
  return Object.fromEntries(
    sidebarItemIds.map((id) => [id, typeof source[id] === "boolean" ? source[id] : true])
  ) as SidebarVisibility;
}

export async function loadSidebarVisibility(): Promise<SidebarVisibility> {
  return normalizeSidebarVisibility(await loadPersistedValue<unknown>(SIDEBAR_VISIBILITY_KEY));
}

export async function saveSidebarVisibility(value: SidebarVisibility): Promise<void> {
  const normalized = normalizeSidebarVisibility(value);
  await savePersistedValue(SIDEBAR_VISIBILITY_KEY, normalized);
  window.dispatchEvent(new CustomEvent<SidebarVisibility>(SIDEBAR_VISIBILITY_EVENT, {
    detail: normalized,
  }));
}

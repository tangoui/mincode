/**
 * Plugin database driver catalog (contributes.databaseDrivers). Holds the
 * re-validated driver descriptors for the connection dialog, the lazily
 * fetched icon blob: URLs, and a cache of host grant/availability statuses.
 * The catalog is fed by the contribution consumer
 * (services/contributions/database-drivers.ts) and, on desktop, refreshed from
 * the gate-checked `plugin_host_database_drivers_list` command. Nothing here
 * routes a connection: the host resolves every connect by reference.
 */
import { acceptHMRUpdate, defineStore } from "pinia";
import { computed, ref, shallowRef, triggerRef } from "vue";
import type { PointContribution } from "@/domain/plugins/contributions";
import {
  DATABASE_DRIVERS_CONTRIBUTION_POINT,
  resolvePluginDatabaseDrivers,
  type DatabaseDriverGrantRef,
  type DatabaseDriverGrantStatus,
  type DatabaseDriverIssue,
  type PluginDatabaseDriver,
} from "@/domain/plugins/database-drivers";
import type { DatabaseDriverRef, DbType } from "@/domain/database/types";
import { contributionAssetObjectURL, releasePluginAssets } from "@/services/contribution-assets";
import { getDatabaseDriverStatus, listDatabaseDrivers } from "@/services/database-driver-service";
import { isDesktopRuntime } from "@/services/desktop-bridge";

function statusKey(ref: DatabaseDriverGrantRef) {
  return `${ref.connectionId}\u0000${ref.pluginId}\u0000${ref.driverId}`;
}

/** Groups `plugin_host_database_drivers_list` rows into per-plugin contributions. */
function contributionsFromList(raw: unknown): PointContribution[] | undefined {
  const list = typeof raw === "object" && raw !== null ? (raw as { drivers?: unknown }).drivers : undefined;
  if (!Array.isArray(list)) return undefined;
  const byPlugin = new Map<string, PointContribution>();
  for (const item of list) {
    if (typeof item !== "object" || item === null) continue;
    const row = item as Record<string, unknown>;
    if (typeof row.pluginId !== "string") continue;
    let contribution = byPlugin.get(row.pluginId);
    if (!contribution) {
      contribution = {
        pluginId: row.pluginId,
        pluginName: typeof row.pluginName === "string" ? row.pluginName : row.pluginId,
        version: typeof row.pluginVersion === "string" ? row.pluginVersion : "",
        source: "",
        runtime: "process",
        activeInWorkspace: true,
        point: { fingerprint: "", entries: [], issues: [] },
      };
      byPlugin.set(row.pluginId, contribution);
    }
    contribution.point.entries.push(row);
  }
  return [...byPlugin.values()];
}

export const useDatabaseDriversStore = defineStore("database-drivers", () => {
  const drivers = ref<PluginDatabaseDriver[]>([]);
  const issues = ref<DatabaseDriverIssue[]>([]);
  const loaded = ref(false);
  const syncError = ref<string>();
  const refreshing = ref(false);
  // blob: URL per (plugin, icon key); null = unavailable (built-in fallback).
  const iconUrls = shallowRef(new Map<string, string | null>());
  const pendingIcons = new Set<string>();
  let iconGeneration = 0;
  const statuses = shallowRef(new Map<string, DatabaseDriverGrantStatus>());

  const byKey = computed(() => new Map(drivers.value.map((driver) => [driver.key, driver])));

  function releaseIcons(pluginIds: readonly string[]) {
    iconGeneration += 1;
    pendingIcons.clear();
    iconUrls.value = new Map();
    for (const pluginId of new Set(pluginIds)) releasePluginAssets(pluginId);
  }

  function applyContributions(contributions: readonly PointContribution[]) {
    const previous = drivers.value.map((driver) => driver.pluginId);
    const result = resolvePluginDatabaseDrivers(contributions);
    releaseIcons([...previous, ...result.drivers.map((driver) => driver.pluginId)]);
    drivers.value = result.drivers;
    issues.value = result.issues;
    loaded.value = true;
    syncError.value = undefined;
  }

  function setContributions(contributions: readonly PointContribution[]) {
    applyContributions(contributions);
  }

  function clearContributions() {
    releaseIcons(drivers.value.map((driver) => driver.pluginId));
    drivers.value = [];
    issues.value = [];
    loaded.value = false;
  }

  function setSyncError(message: string | undefined) {
    syncError.value = message;
  }

  /**
   * Reloads the catalog from the host's gate-checked driver list (desktop
   * only). Keeps the snapshot data when the command is unavailable.
   */
  async function refresh() {
    if (!isDesktopRuntime() || refreshing.value) return;
    refreshing.value = true;
    try {
      const contributions = contributionsFromList(await listDatabaseDrivers());
      if (contributions) applyContributions(contributions);
    } catch (error) {
      syncError.value = error instanceof Error ? error.message : String(error);
    } finally {
      refreshing.value = false;
    }
  }

  function driverFor(ref: DatabaseDriverRef | undefined): PluginDatabaseDriver | undefined {
    if (!ref) return undefined;
    return byKey.value.get(`plugin:${ref.pluginId}/${ref.driverId}`);
  }

  /** Plugin drivers a connection of `dbType` can be bound to (same stored type). */
  function compatibleDrivers(dbType: DbType): PluginDatabaseDriver[] {
    return drivers.value.filter((driver) => driver.dbType === dbType);
  }

  /** The cached icon blob: URL, null when unavailable, undefined while loading or undeclared. */
  function iconUrl(driver: Pick<PluginDatabaseDriver, "pluginId" | "icon"> | undefined): string | null | undefined {
    if (!driver?.icon) return null;
    return iconUrls.value.get(`${driver.pluginId}\u0000${driver.icon}`);
  }

  function ensureIcon(driver: Pick<PluginDatabaseDriver, "pluginId" | "icon"> | undefined) {
    if (!driver?.icon) return;
    const id = `${driver.pluginId}\u0000${driver.icon}`;
    if (iconUrls.value.has(id) || pendingIcons.has(id)) return;
    pendingIcons.add(id);
    const generation = iconGeneration;
    void contributionAssetObjectURL(driver.pluginId, DATABASE_DRIVERS_CONTRIBUTION_POINT, driver.icon)
      .catch(() => null)
      .then((url) => {
        if (generation !== iconGeneration) return;
        pendingIcons.delete(id);
        // Only blob: URLs reach <img src>; plugin SVG is never injected as markup.
        iconUrls.value.set(id, typeof url === "string" && url.startsWith("blob:") ? url : null);
        triggerRef(iconUrls);
      });
  }

  function statusFor(ref: DatabaseDriverGrantRef): DatabaseDriverGrantStatus | undefined {
    return statuses.value.get(statusKey(ref));
  }

  /** Refreshes host statuses for the given references; failures leave the cache unchanged. */
  async function refreshStatuses(refs: readonly DatabaseDriverGrantRef[]): Promise<DatabaseDriverGrantStatus[]> {
    const result = await getDatabaseDriverStatus(refs);
    const next = new Map(statuses.value);
    for (const status of result) next.set(statusKey(status), status);
    statuses.value = next;
    return result;
  }

  function markGranted(ref: DatabaseDriverGrantRef) {
    const current = statuses.value.get(statusKey(ref));
    const next = new Map(statuses.value);
    next.set(statusKey(ref), { ...ref, available: current?.available ?? true, granted: true });
    statuses.value = next;
  }

  function forgetStatuses(connectionId: string) {
    const next = new Map([...statuses.value].filter(([, status]) => status.connectionId !== connectionId));
    statuses.value = next;
  }

  return {
    drivers,
    issues,
    loaded,
    syncError,
    refreshing,
    setContributions,
    clearContributions,
    setSyncError,
    refresh,
    driverFor,
    compatibleDrivers,
    iconUrl,
    ensureIcon,
    statusFor,
    refreshStatuses,
    markGranted,
    forgetStatuses,
  };
});

if (import.meta.hot) {
  import.meta.hot.accept(acceptHMRUpdate(useDatabaseDriversStore, import.meta.hot));
}

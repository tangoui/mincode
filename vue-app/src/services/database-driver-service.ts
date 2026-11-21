/**
 * Plugin database drivers (contributes.databaseDrivers): the IPC boundary.
 *
 * The WebView never talks to a driver plugin. Every connect goes through the
 * existing `db_*` commands; the Go host routes a connection with a `driver`
 * reference to the plugin process and releases its credentials only when a
 * credential-release grant for exactly (connectionId, pluginId, driverId)
 * exists. The grant is recorded when the user picks the plugin driver in the
 * connection dialog (Save / Test), or after the protected confirm below. The
 * confirm is required in every approval mode; AI tools can neither grant nor
 * bind. Grants live on the host and are never exported.
 */
import { defineComponent, h, ref as vueRef } from "vue";
import { ElCheckbox, ElMessageBox } from "element-plus";
import {
  createCredentialReleaseGate,
  databaseDriverErrorCode,
  isDatabaseConnectionId,
  normalizeDatabaseDriverRef,
  parseDatabaseDriverGrantStatuses,
  type CredentialReleaseDecision,
  type DatabaseDriverGrantRef,
  type DatabaseDriverGrantStatus,
} from "@/domain/plugins/database-drivers";
import type { DatabaseDriverRef } from "@/domain/database/types";
import { useVueI18n } from "@/i18n/messages";
import { invokeBackend, isDesktopRuntime } from "@/services/desktop-bridge";

export { databaseDriverErrorCode };

const STATUS_BATCH = 256;

function requireDesktop() {
  if (!isDesktopRuntime()) throw new Error("unavailable: plugin database drivers need the MinCode desktop app");
}

function requireRef(ref: DatabaseDriverGrantRef): DatabaseDriverGrantRef {
  const driver = normalizeDatabaseDriverRef(ref);
  if (!driver || !isDatabaseConnectionId(ref.connectionId)) throw new Error("not_found: invalid database driver reference");
  return { connectionId: ref.connectionId, ...driver };
}

/** Records the credential-release grant for one connection and driver. */
export async function grantDatabaseDriver(ref: DatabaseDriverGrantRef): Promise<void> {
  requireDesktop();
  const target = requireRef(ref);
  await invokeBackend<unknown>("plugin_host_database_driver_grant", { ...target });
}

/** Revokes the grant(s) of a connection (every plugin when `pluginId` is omitted). */
export async function revokeDatabaseDriverGrant(target: { connectionId: string; pluginId?: string }): Promise<void> {
  if (!isDesktopRuntime() || !isDatabaseConnectionId(target.connectionId)) return;
  await invokeBackend<unknown>("plugin_host_database_driver_revoke", {
    connectionId: target.connectionId,
    ...(target.pluginId ? { pluginId: target.pluginId } : {}),
  });
}

/** Drops grants of connections that no longer exist. */
export async function pruneDatabaseDriverGrants(liveConnectionIds: readonly string[]): Promise<void> {
  if (!isDesktopRuntime()) return;
  await invokeBackend<unknown>("plugin_host_database_driver_prune", {
    liveConnectionIds: liveConnectionIds.filter(isDatabaseConnectionId),
  });
}

/**
 * Availability and grant state per reference, in request order. Outside the
 * desktop runtime every driver reads as unavailable.
 */
export async function getDatabaseDriverStatus(refs: readonly DatabaseDriverGrantRef[]): Promise<DatabaseDriverGrantStatus[]> {
  if (refs.length === 0) return [];
  if (!isDesktopRuntime()) return parseDatabaseDriverGrantStatuses([], refs);
  const result: DatabaseDriverGrantStatus[] = [];
  for (let index = 0; index < refs.length; index += STATUS_BATCH) {
    const batch = refs.slice(index, index + STATUS_BATCH);
    const raw = await invokeBackend<unknown>("plugin_host_database_driver_status", {
      refs: batch.map((ref) => ({ connectionId: ref.connectionId, pluginId: ref.pluginId, driverId: ref.driverId })),
    });
    result.push(...parseDatabaseDriverGrantStatuses(raw, batch));
  }
  return result;
}

/**
 * The user's explicit per-plugin trust: while set, connections bound to the
 * plugin's drivers release credentials without a per-connection grant. Host
 * frame only; AI tools never call these.
 */
export async function getDatabaseDriverTrust(pluginId: string): Promise<boolean> {
  if (!isDesktopRuntime() || !pluginId) return false;
  const raw = await invokeBackend<unknown>("plugin_host_database_driver_trust_get", { pluginId });
  return typeof raw === "object" && raw !== null && (raw as { trusted?: unknown }).trusted === true;
}

export async function setDatabaseDriverTrust(pluginId: string, trusted: boolean): Promise<void> {
  requireDesktop();
  await invokeBackend<unknown>("plugin_host_database_driver_trust_set", { pluginId, trusted });
}

/** Gate-checked driver list (the same entries as the `databaseDrivers` snapshot). */
export async function listDatabaseDrivers(): Promise<unknown> {
  requireDesktop();
  return invokeBackend<unknown>("plugin_host_database_drivers_list", {});
}

export interface DatabaseDriverReleaseTarget {
  connectionName: string;
  host?: string;
  port?: number;
  username?: string;
  pluginName: string;
  pluginId: string;
  pluginVersion?: string;
  driverTitle: string;
}

/**
 * Protected confirm: "send the credentials of connection X (host:port, user)
 * to plugin Y (id, version)?" with an "always trust this plugin" checkbox.
 * Cancel keeps the initial focus. `confirmed` is true only when the user
 * confirmed; `trust` only when the box was also ticked.
 */
export async function confirmDatabaseDriverCredentialRelease(
  target: DatabaseDriverReleaseTarget,
): Promise<CredentialReleaseDecision> {
  const { t } = useVueI18n();
  const endpoint = [target.host ? (target.port ? `${target.host}:${target.port}` : target.host) : "", target.username ?? ""]
    .filter(Boolean)
    .join(", ");
  const plugin = [target.pluginId, target.pluginVersion].filter(Boolean).join(", ");
  const trust = vueRef(false);
  const TrustCheckbox = defineComponent({
    setup: () => () =>
      h(
        ElCheckbox,
        {
          class: "app-database-driver-grant__trust",
          modelValue: trust.value,
          "onUpdate:modelValue": (value: string | number | boolean) => {
            trust.value = value === true;
          },
        },
        () => t("pluginContrib.databaseDrivers.grantAlwaysTrust"),
      ),
  });
  const body = h("div", { class: "app-database-driver-grant" }, [
    h(
      "p",
      t("pluginContrib.databaseDrivers.grantBody", {
        connection: endpoint ? `${target.connectionName} (${endpoint})` : target.connectionName,
        plugin: `${target.pluginName} (${plugin})`,
        driver: target.driverTitle,
      }),
    ),
    h("p", { class: "app-database-driver-grant__hint" }, t("pluginContrib.databaseDrivers.grantHint")),
    ...(target.pluginId ? [h(TrustCheckbox)] : []),
  ]);
  try {
    await ElMessageBox.confirm(body, t("pluginContrib.databaseDrivers.grantTitle"), {
      type: "warning",
      confirmButtonText: t("pluginContrib.databaseDrivers.grantConfirm"),
      cancelButtonText: t("common.cancel"),
      distinguishCancelAndClose: true,
      autofocus: false,
      customClass: "app-database-driver-grant-confirm",
    });
    return { confirmed: true, trust: trust.value };
  } catch {
    return { confirmed: false, trust: false };
  }
}

const credentialReleaseGate = createCredentialReleaseGate<DatabaseDriverReleaseTarget>({
  status: async (grantRef) => (await getDatabaseDriverStatus([grantRef]))[0],
  confirm: confirmDatabaseDriverCredentialRelease,
  grant: grantDatabaseDriver,
  setTrust: setDatabaseDriverTrust,
});

/**
 * Makes sure a connection that was not bound in the dialog (imported, synced,
 * or after an `untrusted:` connect error) may release its credentials. Skips
 * the prompt when the host already holds a grant or the user trusts the
 * plugin; otherwise shows the protected confirm (one at a time, shared by
 * concurrent requests for the same connection) and records the grant, plus
 * plugin trust when the user ticked "always trust". Resolves to false when
 * the user cancelled.
 */
export function ensureDatabaseDriverGrant(ref: DatabaseDriverGrantRef, target: DatabaseDriverReleaseTarget): Promise<boolean> {
  return credentialReleaseGate(ref, target);
}

/** Localized message for a host error; falls back to the raw message. */
export function databaseDriverErrorMessage(error: unknown): string {
  const { t } = useVueI18n();
  switch (databaseDriverErrorCode(error)) {
    case "unavailable":
    case "not_found":
      return t("pluginContrib.databaseDrivers.unavailable");
    case "untrusted":
      return t("pluginContrib.databaseDrivers.notGranted");
    case "forbidden":
      return t("pluginContrib.databaseDrivers.forbidden");
    case "cancelled":
      return t("pluginContrib.databaseDrivers.cancelUnconfirmed");
    case "protocol":
      return t("pluginContrib.databaseDrivers.failed");
    default:
      return error instanceof Error ? error.message : String(error);
  }
}

export function databaseDriverGrantRef(connectionId: string, driver: DatabaseDriverRef): DatabaseDriverGrantRef {
  return { connectionId, pluginId: driver.pluginId, driverId: driver.driverId };
}

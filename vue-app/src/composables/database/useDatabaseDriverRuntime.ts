import { reactive, watch } from "vue";
import { useRouter } from "vue-router";
import { ElMessage, ElMessageBox } from "element-plus";
import {
  databaseDriverInfo,
  databaseTypeLabels,
  type DatabaseConnection,
  type DbType,
} from "@/domain/database/types";
import {
  isPluginDriverConnection,
  type DatabaseDriverDescriptor,
  type DatabaseDriverUnavailableReason,
} from "@/domain/database/driver-descriptor";
import {
  bindConnectionToPluginDriver,
  databaseDriverErrorCode,
  databaseDriverPluginSearch,
  databaseDriverTitle,
  sameDatabaseDriverRef,
  type PluginDatabaseDriver,
} from "@/domain/plugins/database-drivers";
import { useVueI18n } from "@/i18n/messages";
import type { MessageKey } from "@/i18n/locales/zh";
import { loadSupportedDatabaseTypes } from "@/services/database-capabilities";
import {
  databaseDriverErrorMessage,
  databaseDriverGrantRef,
  ensureDatabaseDriverGrant,
  revokeDatabaseDriverGrant,
} from "@/services/database-driver-service";
import { isDesktopRuntime } from "@/services/desktop-bridge";
import { useDatabaseDriversStore } from "@/stores/database-drivers";
import { useDatabaseWorkspaceStore } from "@/stores/database-workspace";

/**
 * 连接类型标签（侧栏、概览共用）：插件驱动连接显示“驱动 · 插件”，驱动不可用时追加标注。
 * 不含副作用，可在多个组件中使用。
 */
export function useDatabaseDriverLabels() {
  const { t } = useVueI18n();
  const databaseStore = useDatabaseWorkspaceStore();
  const driversStore = useDatabaseDriversStore();

  function driverUnavailable(connection: DatabaseConnection) {
    const descriptor = databaseStore.connectionDescriptor(connection);
    return descriptor?.source === "plugin" && !descriptor.available;
  }

  function connectionTypeLabel(connection: DatabaseConnection) {
    const descriptor = databaseStore.connectionDescriptor(connection);
    if (descriptor?.source !== "plugin") {
      return databaseTypeLabels[connection.dbType] ?? connection.dbType;
    }
    const label = t("pluginContrib.databaseDrivers.runtime.via", {
      driver: descriptor.title,
      plugin: descriptor.pluginName || descriptor.pluginId || "",
    });
    return descriptor.available
      ? label
      : `${label} · ${t("pluginContrib.databaseDrivers.runtime.unavailableBadge")}`;
  }

  /**
   * “切换驱动…”菜单项：内置类型连接已绑定插件驱动（可改回内置或换插件），
   * 或存在可替代该内置类型的插件驱动时显示。
   */
  function canSwitchDriver(connection: DatabaseConnection) {
    if (connection.dbType === "plugin") return false;
    return (
      Boolean(connection.driver) ||
      driversStore.compatibleDrivers(connection.dbType).some((driver) => driver.replaces)
    );
  }

  return { driverUnavailable, connectionTypeLabel, canSwitchDriver };
}

/** 文件数据库识别结果（db_detect_file_type）。builtinDriver=false 表示当前构建未内置该类型驱动。 */
export interface DetectedFileDatabase {
  dbType: Extract<DbType, "sqlite" | "duckdb">;
  filePath: string;
  fileName: string;
  builtinDriver?: boolean;
}

/**
 * 数据库工作区里插件驱动连接的运行时交互：不可用说明（启用/安装插件、改用内置驱动）、
 * 连接错误文案、驱动插件停止通知。绝不在驱动缺失时静默回落到内置驱动（E-15），
 * 改用内置驱动必须由用户显式确认。
 */
export function useDatabaseDriverRuntime() {
  const { t, localeChain } = useVueI18n();
  const router = useRouter();
  const databaseStore = useDatabaseWorkspaceStore();
  const driversStore = useDatabaseDriversStore();
  /** 批量改绑对话框（DatabaseDriverRebindDialog）的状态；由视图懒加载挂载。 */
  const driverRebind = reactive<{ visible: boolean; initialTarget?: string; connectionIds: string[] }>({
    visible: false,
    connectionIds: [],
  });

  function descriptorOf(connection: DatabaseConnection) {
    return databaseStore.connectionDescriptor(connection);
  }

  const reasonKeys: Record<DatabaseDriverUnavailableReason, MessageKey> = {
    notInstalled: "pluginContrib.databaseDrivers.runtime.reason.notInstalled",
    disabled: "pluginContrib.databaseDrivers.runtime.reason.disabled",
    error: "pluginContrib.databaseDrivers.runtime.reason.error",
    workspace: "pluginContrib.databaseDrivers.runtime.reason.workspace",
    capability: "pluginContrib.databaseDrivers.runtime.reason.capability",
    noDriver: "pluginContrib.databaseDrivers.runtime.reason.noDriver",
    protocol: "pluginContrib.databaseDrivers.runtime.reason.protocol",
  };

  function reasonText(descriptor: DatabaseDriverDescriptor | undefined) {
    return t(reasonKeys[descriptor?.unavailableReason ?? "error"]);
  }

  function pluginLabel(descriptor: DatabaseDriverDescriptor | undefined) {
    if (!descriptor?.pluginId) return "";
    return descriptor.pluginName && descriptor.pluginName !== descriptor.pluginId
      ? `${descriptor.pluginName} (${descriptor.pluginId})`
      : descriptor.pluginId;
  }

  function driverUnavailable(connection: DatabaseConnection) {
    const descriptor = descriptorOf(connection);
    return descriptor?.source === "plugin" && !descriptor.available;
  }

  function builtinFallbackAvailable(connection: DatabaseConnection) {
    return (
      connection.dbType !== "plugin" &&
      Boolean(databaseDriverInfo[connection.dbType]?.available)
    );
  }

  /** 用户显式把连接改回内置驱动：去掉驱动绑定与驱动选项，撤销授权由持久化时的 prune/删除处理。 */
  async function useBuiltinDriver(connection: DatabaseConnection) {
    if (!builtinFallbackAvailable(connection)) return false;
    try {
      await ElMessageBox.confirm(
        t("pluginContrib.databaseDrivers.runtime.useBuiltinBody", {
          connection: connection.name,
          type: databaseDriverInfo[connection.dbType].label,
        }),
        t("pluginContrib.databaseDrivers.runtime.useBuiltinTitle"),
        {
          type: "warning",
          confirmButtonText: t("pluginContrib.databaseDrivers.runtime.useBuiltinConfirm"),
          cancelButtonText: t("common.cancel"),
          autofocus: false,
        },
      );
    } catch {
      return false;
    }
    const { driver, options: _options, ...rest } = connection;
    await databaseStore.saveConnection(rest);
    if (driver) {
      await revokeDatabaseDriverGrant({ connectionId: connection.id, pluginId: driver.pluginId }).catch(
        () => undefined,
      );
    }
    await databaseStore.refreshDriverStatuses();
    return true;
  }

  /** 连接不可用驱动时的说明对话框：打开插件管理，或（有内置驱动时）改用内置驱动。 */
  async function explainUnavailableDriver(connection: DatabaseConnection) {
    const descriptor = descriptorOf(connection);
    const canUseBuiltin = builtinFallbackAvailable(connection);
    try {
      await ElMessageBox.confirm(
        t("pluginContrib.databaseDrivers.runtime.unavailableBody", {
          connection: connection.name,
          driver: descriptor?.title ?? connection.driver?.driverId ?? "",
          plugin: pluginLabel(descriptor),
          reason: reasonText(descriptor),
        }),
        t("pluginContrib.databaseDrivers.runtime.unavailableTitle"),
        {
          type: "warning",
          confirmButtonText: t("pluginContrib.databaseDrivers.runtime.openPluginManager"),
          cancelButtonText: canUseBuiltin
            ? t("pluginContrib.databaseDrivers.runtime.useBuiltin")
            : t("common.cancel"),
          distinguishCancelAndClose: true,
          autofocus: false,
        },
      );
      await router.push({
        name: "plugins",
        query: descriptor?.pluginId ? { search: descriptor.pluginId } : {},
      });
    } catch (action) {
      if (action === "cancel" && canUseBuiltin) await useBuiltinDriver(connection);
    }
  }

  /**
   * 当前构建未内置该类型驱动（如 nocoredrivers 构建去掉的 DuckDB/Oracle/YashanDB/ClickHouse）且连接仍绑定内置驱动。
   * 浏览器预览（无法查询构建能力）返回 false。
   */
  async function builtinDriverMissing(connection: DatabaseConnection) {
    if (connection.driver || connection.dbType === "plugin") return false;
    const supported = await loadSupportedDatabaseTypes();
    return Boolean(supported && !supported.has(connection.dbType));
  }

  /** 改绑对话框的初始目标：连接之外恰好只有一个可替代该类型的插件驱动时预选它。 */
  function rebindTargetFor(connection: DatabaseConnection) {
    const others = driversStore
      .compatibleDrivers(connection.dbType)
      .filter((driver) => driver.replaces && !sameDatabaseDriverRef(connection.driver, driver));
    return others.length === 1 ? others[0].key : undefined;
  }

  /**
   * 打开“切换驱动”对话框（E-10 显式改绑）：预选该连接，用户可在对话框里全选同类型连接批量改绑。
   * 不传连接时不预选任何连接和目标。
   */
  async function openDriverRebind(connection?: DatabaseConnection) {
    await driversStore.refresh();
    driverRebind.initialTarget = connection ? rebindTargetFor(connection) : undefined;
    driverRebind.connectionIds = connection ? [connection.id] : [];
    driverRebind.visible = true;
  }

  /**
   * 持久化对话框改绑后的连接。对话框已在用户确认后记录新驱动的授权（并移除该连接在其他插件的授权）；
   * saveConnection 只撤销旧插件的授权，不会删掉刚记录的授权，因此改绑后连接不会再次询问。
   */
  async function applyDriverRebind(changed: DatabaseConnection[]) {
    try {
      for (const next of changed) await databaseStore.saveConnection(next);
      await databaseStore.refreshDriverStatuses(changed.map((connection) => connection.id));
    } catch (error) {
      ElMessage.error(databaseDriverErrorMessage(error));
    }
  }

  /** 打开插件管理（同一目录含插件市场条目），按类型 ID 搜索提供该驱动的插件。 */
  async function openDriverPluginManager(dbType: DbType) {
    await router.push({ name: "plugins", query: { search: databaseDriverPluginSearch(dbType) } });
  }

  /**
   * 内置驱动缺失时的说明：需要驱动插件，改绑必须由用户显式完成（E-10）。
   * 已有兼容插件驱动时打开“切换驱动”对话框（可批量改绑同类型连接），否则打开插件管理/市场。
   */
  async function explainMissingBuiltinDriver(connection: DatabaseConnection) {
    await driversStore.refresh();
    const type = databaseTypeLabels[connection.dbType] ?? connection.dbType;
    const canRebind = driversStore.compatibleDrivers(connection.dbType).some((driver) => driver.replaces);
    const body = [
      t("pluginContrib.databaseDrivers.runtime.builtinMissingBody", { connection: connection.name, type }),
      canRebind
        ? t("pluginContrib.databaseDrivers.runtime.builtinMissingRebind", { type })
        : t("pluginContrib.databaseDrivers.runtime.builtinMissingInstall", { type }),
    ].join(" ");
    try {
      await ElMessageBox.confirm(body, t("pluginContrib.databaseDrivers.runtime.builtinMissingTitle"), {
        type: "warning",
        confirmButtonText: canRebind
          ? t("pluginContrib.databaseDrivers.runtime.switchDriver")
          : t("pluginContrib.databaseDrivers.runtime.openPluginManager"),
        cancelButtonText: t("common.cancel"),
        autofocus: false,
      });
    } catch {
      return;
    }
    if (canRebind) await openDriverRebind(connection);
    else await openDriverPluginManager(connection.dbType);
  }

  /** 可以打开该文件的插件驱动：替代该内置类型且以文件路径连接。 */
  function fileDriversFor(dbType: DbType): PluginDatabaseDriver[] {
    return driversStore
      .compatibleDrivers(dbType)
      .filter((driver) => driver.replaces && driver.fields.includes("filePath"));
  }

  /**
   * 打开/拖入的数据库文件在当前构建没有内置驱动（精简构建里的 DuckDB）时的处理：
   * - 没有可用的插件驱动：说明并引导打开插件管理/市场安装提供该驱动的插件，绝不自动安装；
   * - 已安装并启用：把新连接绑定到插件驱动（多个时选第一个，与连接对话框排序一致），
   *   先经受保护确认记录凭据释放授权，用户取消则不保存连接。
   * 返回可保存并连接的连接；返回 undefined 表示用户未继续。
   */
  async function prepareFileDatabaseConnection(
    detected: DetectedFileDatabase,
    connection: DatabaseConnection,
  ): Promise<DatabaseConnection | undefined> {
    if (detected.builtinDriver !== false || connection.driver) return connection;
    await driversStore.refresh();
    const type = databaseTypeLabels[detected.dbType] ?? detected.dbType;
    const [driver] = fileDriversFor(detected.dbType);
    if (!driver) {
      try {
        await ElMessageBox.confirm(
          t("pluginContrib.databaseDrivers.runtime.fileDriverMissingBody", { file: detected.fileName, type }),
          t("pluginContrib.databaseDrivers.runtime.builtinMissingTitle"),
          {
            type: "info",
            confirmButtonText: t("pluginContrib.databaseDrivers.runtime.openPluginManager"),
            cancelButtonText: t("common.cancel"),
            autofocus: false,
          },
        );
        await openDriverPluginManager(detected.dbType);
      } catch {
        // 用户取消：不保存连接，也不回落到其他驱动。
      }
      return undefined;
    }
    const bound = bindConnectionToPluginDriver(connection, driver);
    if (!bound.driver) return undefined;
    if (isDesktopRuntime()) {
      const ref = databaseDriverGrantRef(bound.id, bound.driver);
      const granted = await ensureDatabaseDriverGrant(ref, {
        connectionName: bound.name,
        host: bound.filePath,
        pluginId: driver.pluginId,
        pluginName: driver.pluginName,
        pluginVersion: driver.pluginVersion,
        driverTitle: databaseDriverTitle(driver, localeChain.value),
      });
      if (!granted) return undefined;
      driversStore.markGranted(ref);
    }
    return bound;
  }

  /** 插件驱动连接的错误文案；内置驱动保持原始错误（driver_required 除外）。 */
  function connectErrorMessage(connection: DatabaseConnection, cause: unknown) {
    if (databaseDriverErrorCode(cause) === "driver_required") {
      const type = databaseTypeLabels[connection.dbType] ?? connection.dbType;
      return `${t("pluginContrib.databaseDrivers.runtime.builtinMissingTitle")}: ${t(
        "pluginContrib.databaseDrivers.builtinMissingHint",
        { type },
      )}`;
    }
    if (!isPluginDriverConnection(connection) || !databaseDriverErrorCode(cause)) {
      return String(cause);
    }
    const detail = cause instanceof Error ? cause.message : String(cause);
    const message = databaseDriverErrorMessage(cause);
    return message === detail ? message : `${message} (${detail})`;
  }

  // 驱动插件停止（禁用、崩溃、重载、卸载）时宿主关闭会话，这里提示用户；结果标签页保留。
  watch(
    () => databaseStore.driverStoppedNotice,
    (notice) => {
      if (!notice) return;
      const connection = databaseStore.connections.find(
        (item) => item.driver?.pluginId === notice.pluginId,
      );
      const descriptor = connection ? descriptorOf(connection) : undefined;
      ElMessage.warning(
        t("pluginContrib.databaseDrivers.runtime.sessionsClosed", {
          plugin: descriptor?.pluginName || notice.pluginId,
        }),
      );
    },
  );

  return {
    driverRebind,
    openDriverRebind,
    applyDriverRebind,
    prepareFileDatabaseConnection,
    driverUnavailable,
    builtinDriverMissing,
    explainMissingBuiltinDriver,
    explainUnavailableDriver,
    connectErrorMessage,
    useBuiltinDriver,
  };
}

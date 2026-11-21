// 数据库驱动描述符（Phase E / E11）：把内置驱动与插件驱动统一成一个抽象，
// 运行时（store / 工作区 / 侧栏 / AI 工具）只通过这里判断连接的方言与能力。
// 纯函数模块：不访问 IPC、Pinia 或 i18n。
import {
  databaseDefaultPorts,
  databaseTypeLabels,
  type DatabaseConnection,
  type DatabaseDriverRef,
  type DbType,
} from "./types";
import {
  DATABASE_DRIVER_DIALECT_TYPES,
  DATABASE_DRIVER_FEATURES,
  type DatabaseDriverFeature,
  type DatabaseDriverSqlDialect,
} from "@/domain/plugins/database-drivers";

export type { DatabaseDriverFeature, DatabaseDriverSqlDialect };
export const databaseDriverFeatures = DATABASE_DRIVER_FEATURES;

/** 插件驱动不可用的原因，与 plugin_host_database_driver_status 的 reason 一致。 */
export type DatabaseDriverUnavailableReason =
  | "notInstalled"
  | "disabled"
  | "error"
  | "workspace"
  | "capability"
  | "noDriver"
  | "protocol";

const unavailableReasons = new Set<string>([
  "notInstalled",
  "disabled",
  "error",
  "workspace",
  "capability",
  "noDriver",
  "protocol",
]);

/**
 * 插件驱动目录条目的结构子集（T4a 的 PluginDatabaseDriver），
 * 这里只依赖结构，便于纯函数测试。
 */
export interface DatabaseDriverSource {
  pluginId: string;
  pluginName?: string;
  driverId: string;
  title: string;
  sqlDialect?: string;
  dialectType?: DbType;
  fields?: readonly string[];
  defaultPort?: number;
  features?: readonly string[];
}

/** plugin_host_database_driver_status 的单条结果。 */
export interface DatabaseDriverStatus {
  available: boolean;
  reason?: string;
  granted?: boolean;
}

export interface DatabaseDriverDescriptor {
  key: string;
  source: "builtin" | "plugin";
  dbType: DbType;
  dialectType: DbType;
  title: string;
  pluginId?: string;
  driverId?: string;
  pluginName?: string;
  features: readonly DatabaseDriverFeature[];
  fields: readonly string[];
  defaultPort: number;
  available: boolean;
  unavailableReason?: DatabaseDriverUnavailableReason;
}

export function dialectTypeForSqlDialect(dialect: string | undefined): DbType {
  // generic（或未知方言）映射到中性的 "plugin"：它不在任何内置能力白名单里，
  // 编辑器按通用 SQL 处理，关键字补全保持通用。
  return (
    DATABASE_DRIVER_DIALECT_TYPES[
      (dialect ?? "generic") as DatabaseDriverSqlDialect
    ] ?? "plugin"
  );
}

export function isPluginDriverConnection(
  connection: Pick<DatabaseConnection, "driver"> | undefined | null,
): connection is { driver: DatabaseDriverRef } {
  const driver = connection?.driver;
  return Boolean(
    driver &&
      typeof driver.pluginId === "string" &&
      driver.pluginId &&
      typeof driver.driverId === "string" &&
      driver.driverId,
  );
}

export function pluginDriverKey(ref: DatabaseDriverRef) {
  return `plugin:${ref.pluginId}/${ref.driverId}`;
}

function knownFeatures(features: readonly string[] | undefined) {
  const allowed = new Set<string>(DATABASE_DRIVER_FEATURES);
  return [...new Set(features ?? [])].filter((feature) =>
    allowed.has(feature),
  ) as DatabaseDriverFeature[];
}

export function builtinDriverDescriptor(
  dbType: DbType,
): DatabaseDriverDescriptor {
  return {
    key: `builtin:${dbType}`,
    source: "builtin",
    dbType,
    dialectType: dbType,
    title: databaseTypeLabels[dbType] ?? dbType,
    // 内置驱动的能力由 db-type-capabilities.ts 的白名单决定，这里不重复声明。
    features: [],
    fields: [],
    defaultPort: databaseDefaultPorts[dbType] ?? 0,
    available: dbType !== "plugin",
    unavailableReason: dbType === "plugin" ? "noDriver" : undefined,
  };
}

/**
 * 根据目录条目与宿主状态生成插件驱动描述符。目录里找不到驱动时
 * 失败关闭：available=false，绝不回落到内置驱动（E-15）。
 */
export function pluginDriverDescriptor(
  connection: Pick<DatabaseConnection, "dbType"> & { driver: DatabaseDriverRef },
  source: DatabaseDriverSource | undefined,
  status?: DatabaseDriverStatus,
): DatabaseDriverDescriptor {
  const ref = connection.driver;
  const dialectType =
    connection.dbType !== "plugin"
      ? connection.dbType
      : (source?.dialectType ?? dialectTypeForSqlDialect(source?.sqlDialect));
  let unavailableReason: DatabaseDriverUnavailableReason | undefined;
  if (status && !status.available) {
    unavailableReason = unavailableReasons.has(status.reason ?? "")
      ? (status.reason as DatabaseDriverUnavailableReason)
      : "error";
  } else if (!source && !status) {
    // 宿主状态是权威来源；只有没有状态且目录里也找不到时才判定为未安装。
    unavailableReason = "notInstalled";
  }
  return {
    key: pluginDriverKey(ref),
    source: "plugin",
    dbType: connection.dbType,
    dialectType,
    title: source?.title || ref.driverId,
    pluginId: ref.pluginId,
    driverId: ref.driverId,
    pluginName: source?.pluginName || undefined,
    features: knownFeatures(source?.features),
    fields: [...(source?.fields ?? [])],
    defaultPort: source?.defaultPort ?? 0,
    available: !unavailableReason,
    unavailableReason,
  };
}

export function findDriverSource(
  catalog: readonly DatabaseDriverSource[] | undefined,
  ref: DatabaseDriverRef,
) {
  return catalog?.find(
    (entry) =>
      entry.pluginId === ref.pluginId && entry.driverId === ref.driverId,
  );
}

export function resolveConnectionDriver(
  connection: Pick<DatabaseConnection, "dbType" | "driver">,
  catalog?: readonly DatabaseDriverSource[],
  status?: DatabaseDriverStatus,
): DatabaseDriverDescriptor {
  if (isPluginDriverConnection(connection)) {
    const withDriver = connection as Pick<DatabaseConnection, "dbType"> & {
      driver: DatabaseDriverRef;
    };
    return pluginDriverDescriptor(
      withDriver,
      findDriverSource(catalog, withDriver.driver),
      status,
    );
  }
  return builtinDriverDescriptor(connection.dbType);
}

export function connectionDialectType(
  connection: Pick<DatabaseConnection, "dbType" | "driver">,
  catalog?: readonly DatabaseDriverSource[],
): DbType {
  return resolveConnectionDriver(connection, catalog).dialectType;
}

/** 插件驱动是否声明了某项能力；内置驱动恒为 undefined（交给内置白名单判断）。 */
export function descriptorSupports(
  descriptor: DatabaseDriverDescriptor,
  feature: DatabaseDriverFeature,
): boolean {
  return descriptor.source === "plugin" && descriptor.features.includes(feature);
}

/**
 * 仅供方言相关的纯函数（SQL 引号、分页、EXPLAIN、模板等）使用的连接投影：
 * dbType 替换成方言代表类型，驱动绑定保持不变。不得用于持久化或连接配置。
 */
export function dialectProjection<T extends Pick<DatabaseConnection, "dbType">>(
  connection: T,
  descriptor: DatabaseDriverDescriptor,
): T {
  return descriptor.dialectType === connection.dbType
    ? connection
    : { ...connection, dbType: descriptor.dialectType };
}

/** AI 审批预览中的驱动标签原料；内置驱动返回 undefined。 */
export function pluginDriverLabelParts(
  descriptor: DatabaseDriverDescriptor,
): { pluginName: string; pluginId: string } | undefined {
  if (descriptor.source !== "plugin" || !descriptor.pluginId) return undefined;
  return {
    pluginName: descriptor.pluginName || descriptor.pluginId,
    pluginId: descriptor.pluginId,
  };
}

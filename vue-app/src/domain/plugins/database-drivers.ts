/**
 * Pure model of the `databaseDrivers` contribution point (process plugins that
 * declare the `database.driver` capability). The Go host
 * (backend/plugin/contrib_database_drivers.go) validates entries at
 * install, sanitizes the icon and routes every connect by reference
 * (`DatabaseConnection.driver`). Entries are still untrusted here: they are
 * re-checked and used only for the driver catalog, the connection dialog form
 * and the install preview. The WebView never talks to a driver plugin; the
 * credential release grant is a host command (services/database-driver-service.ts).
 *
 * Plugin connections keep `dbType` equal to the driver's `replaces` type, or
 * `"plugin"` for new database types. Existing DbType-keyed helpers receive
 * `dialectType`: the replaced built-in type, else a representative of the
 * declared `sqlDialect`. Nothing here touches IPC or Vue.
 */
import type { PointContribution } from "@/domain/plugins/contributions";
import {
  configurationFields,
  isValidConfigurationValue,
  type PluginConfigurationField,
  type PluginConfigurationProperty,
  type PluginConfigurationSchema,
} from "@/domain/plugins/configuration";
import { isContributionAssetKey } from "@/domain/plugins/contribution-assets";
import { resolvePluginTitle } from "@/domain/plugins/title";
import type { DatabaseConnection, DatabaseDriverRef, DbType } from "@/domain/database/types";

export const DATABASE_DRIVERS_CONTRIBUTION_POINT = "databaseDrivers";

// Mirrors `points.databaseDrivers` in plugins/api/contribution-points.json
// (vue-app/scripts/database-drivers-contributions.test.mjs keeps them in sync).
export const DATABASE_DRIVER_MAX_ENTRIES = 64;
export const DATABASE_DRIVER_TITLE_MAX_RUNES = 64;
export const DATABASE_DRIVER_DESCRIPTION_MAX_RUNES = 256;
export const DATABASE_DRIVER_LOCALES_MAX = 16;
export const DATABASE_DRIVER_OPTIONS_MAX = 16;
export const DATABASE_DRIVER_OPTION_KEY_MAX_LENGTH = 64;
export const DATABASE_DRIVER_OPTION_STRING_MAX_LENGTH = 1024;
export const DATABASE_DRIVER_ENUM_MAX = 32;
export const DATABASE_DRIVER_FILE_EXTENSIONS_MAX = 8;

export const DATABASE_DRIVER_CONNECTION_FIELDS = [
  "host",
  "port",
  "database",
  "username",
  "password",
  "token",
  "filePath",
  "ssl",
  "org",
  "authSource",
] as const;
export const DATABASE_DRIVER_SECRET_FIELDS = ["password", "token"] as const;
export const DATABASE_DRIVER_FEATURES = [
  "cancel",
  "readOnly",
  "multiDatabase",
  "metadata.tables",
  "metadata.columns",
  "metadata.primaryKeys",
  "metadata.indexes",
  "metadata.foreignKeys",
  "metadata.triggers",
  "metadata.ddl",
  "tableRows",
  "rows.update",
  "rows.insert",
  "rows.delete",
  "destructive.dropTable",
  "destructive.clearTable",
  "destructive.dropDatabase",
  "destructive.clearDatabase",
  "transaction.snapshotWrite",
] as const;
export const DATABASE_DRIVER_SQL_DIALECTS = [
  "generic",
  "mysql",
  "postgres",
  "sqlite",
  "sqlserver",
  "oracle",
  "clickhouse",
  "duckdb",
  "trino",
] as const;
export const DATABASE_DRIVER_CATEGORIES = ["relational", "analytics", "nosql", "domestic", "vector", "other"] as const;
export const DATABASE_DRIVER_REPLACEABLE_TYPES = [
  "duckdb",
  "oracle",
  "clickhouse",
  "sqlserver",
  "firebird",
  "dameng",
  "yashandb",
  "tdengine",
  "trino",
  "presto",
] as const satisfies readonly DbType[];

/**
 * Plugin manager search term for a database type or driver id: the id itself.
 * Driver plugins list every driver id they contribute (and every type they
 * replace) in their tags, so the catalog search finds them without the host
 * naming any plugin. Nothing is installed or bound automatically.
 */
export function databaseDriverPluginSearch(driverId: string): string {
  return driverId;
}

export type DatabaseDriverField = (typeof DATABASE_DRIVER_CONNECTION_FIELDS)[number];
export type DatabaseDriverFeature = (typeof DATABASE_DRIVER_FEATURES)[number];
export type DatabaseDriverSqlDialect = (typeof DATABASE_DRIVER_SQL_DIALECTS)[number];
export type DatabaseDriverCategory = (typeof DATABASE_DRIVER_CATEGORIES)[number];
export type DatabaseDriverReplaceableType = (typeof DATABASE_DRIVER_REPLACEABLE_TYPES)[number];
export type DatabaseDriverOptionValue = string | number | boolean;
export type DatabaseDriverOptions = Record<string, DatabaseDriverOptionValue>;

/**
 * Representative built-in type per SQL dialect, used by DbType-keyed helpers
 * (quoting, templates, keywords) for plugin-only database types. "generic"
 * maps to the neutral `"plugin"` type.
 */
export const DATABASE_DRIVER_DIALECT_TYPES: Readonly<Record<DatabaseDriverSqlDialect, DbType>> = {
  generic: "plugin",
  mysql: "mysql",
  postgres: "postgres",
  sqlite: "sqlite",
  sqlserver: "sqlserver",
  oracle: "oracle",
  clickhouse: "clickhouse",
  duckdb: "duckdb",
  trino: "trino",
};

/** Re-validated display data of one plugin database driver. */
export interface PluginDatabaseDriver {
  /** `plugin:<pluginId>/<driverId>`. */
  key: string;
  pluginId: string;
  pluginName: string;
  pluginVersion: string;
  driverId: string;
  title: string;
  titleLocalized?: Record<string, string>;
  description?: string;
  descriptionLocalized?: Record<string, string>;
  /** Content-hash asset key of the sanitized icon (image context only). */
  icon?: string;
  category: DatabaseDriverCategory;
  sqlDialect: DatabaseDriverSqlDialect;
  /** Built-in type this driver can stand in for (a suggestion, never auto-routing). */
  replaces?: DatabaseDriverReplaceableType;
  /** DbType stored on bound connections: `replaces`, else `"plugin"`. */
  dbType: DbType;
  /** DbType handed to dialect-keyed helpers. */
  dialectType: DbType;
  fields: DatabaseDriverField[];
  required: DatabaseDriverField[];
  defaultPort?: number;
  fileExtensions: string[];
  /** Non-secret driver options in the configuration point's restricted dialect. */
  options?: PluginConfigurationSchema;
  features: DatabaseDriverFeature[];
  specFingerprint?: string;
}

export interface DatabaseDriverIssue {
  pluginId: string;
  index: number;
  id?: string;
  reason: string;
}

const PLUGIN_ID_PATTERN = /^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$/;
const DRIVER_ID_PATTERN = /^[a-z][a-z0-9-]{0,31}$/;
const OPTION_KEY_PATTERN = /^[a-z][a-zA-Z0-9]{0,63}$/;
const LOCALE_PATTERN = /^[A-Za-z]{2,8}(?:-[A-Za-z0-9]{1,8}){0,3}$/;
const FILE_EXTENSION_PATTERN = /^\.[a-z0-9]{1,16}$/;
const FINGERPRINT_PATTERN = /^[0-9a-f]{64}$/;
const KEY_PREFIX = "plugin:";
// eslint-disable-next-line no-control-regex
const UNSAFE_TEXT = /[\u0000-\u001f\u007f-\u009f\u2028\u2029\u202a-\u202e\u2066-\u2069\u200b-\u200f\ufeff]/g;

type JsonRecord = Record<string, unknown>;

function objectValue(value: unknown): JsonRecord | undefined {
  return typeof value === "object" && value !== null && !Array.isArray(value) ? (value as JsonRecord) : undefined;
}

function sanitizeText(value: string, maxRunes: number) {
  const clean = value.replace(UNSAFE_TEXT, "").trim();
  return Array.from(clean).slice(0, maxRunes).join("");
}

function includes<T extends string>(list: readonly T[], value: unknown): value is T {
  return typeof value === "string" && (list as readonly string[]).includes(value);
}

function parseLocalized(value: unknown, maxRunes: number): Record<string, string> | undefined {
  const record = objectValue(value);
  if (!record) return undefined;
  const result: Record<string, string> = {};
  let count = 0;
  for (const [locale, text] of Object.entries(record)) {
    if (count >= DATABASE_DRIVER_LOCALES_MAX) break;
    if (!LOCALE_PATTERN.test(locale) || typeof text !== "string") continue;
    const clean = sanitizeText(text, maxRunes);
    if (!clean) continue;
    result[locale] = clean;
    count += 1;
  }
  return count > 0 ? result : undefined;
}

function uniqueSubset<T extends string>(value: unknown, list: readonly T[]): T[] | undefined {
  if (!Array.isArray(value) || value.length > list.length * 2) return undefined;
  const result: T[] = [];
  for (const item of value) {
    if (!includes(list, item)) return undefined;
    if (!result.includes(item)) result.push(item);
  }
  return result;
}

function finite(value: unknown): number | undefined {
  return typeof value === "number" && Number.isFinite(value) ? value : undefined;
}

/** Parses one option declaration; undefined when it leaves the restricted dialect. */
function parseOption(value: unknown): PluginConfigurationProperty | undefined {
  const raw = objectValue(value);
  if (!raw) return undefined;
  const type = raw.type;
  if (type !== "string" && type !== "integer" && type !== "number" && type !== "boolean") return undefined;
  // v1 has no secret options (E-4), and no pattern/format/array constraints.
  if (raw.secret !== undefined || raw.pattern !== undefined || raw.format !== undefined || raw.items !== undefined) return undefined;
  const property: PluginConfigurationProperty = { type, scope: "global" };
  const title = typeof raw.title === "string" ? sanitizeText(raw.title, DATABASE_DRIVER_TITLE_MAX_RUNES) : "";
  if (title) property.title = title;
  property.titleLocalized = parseLocalized(raw.titleLocalized, DATABASE_DRIVER_TITLE_MAX_RUNES);
  const description = typeof raw.description === "string" ? sanitizeText(raw.description, DATABASE_DRIVER_DESCRIPTION_MAX_RUNES) : "";
  if (description) property.description = description;
  property.descriptionLocalized = parseLocalized(raw.descriptionLocalized, DATABASE_DRIVER_DESCRIPTION_MAX_RUNES);
  if (type === "string") {
    const maxLength = raw.maxLength === undefined ? DATABASE_DRIVER_OPTION_STRING_MAX_LENGTH : finite(raw.maxLength);
    if (maxLength === undefined || !Number.isInteger(maxLength) || maxLength < 1 || maxLength > DATABASE_DRIVER_OPTION_STRING_MAX_LENGTH) return undefined;
    property.maxLength = maxLength;
  } else if (type !== "boolean") {
    if (raw.minimum !== undefined) {
      property.minimum = finite(raw.minimum);
      if (property.minimum === undefined) return undefined;
    }
    if (raw.maximum !== undefined) {
      property.maximum = finite(raw.maximum);
      if (property.maximum === undefined) return undefined;
    }
    if (property.minimum !== undefined && property.maximum !== undefined && property.minimum > property.maximum) return undefined;
  }
  if (raw.enum !== undefined) {
    if (type === "boolean" || !Array.isArray(raw.enum) || raw.enum.length === 0 || raw.enum.length > DATABASE_DRIVER_ENUM_MAX) return undefined;
    const values = raw.enum as unknown[];
    const valid = values.every((item) => isValidConfigurationValue({ ...property, enum: undefined }, item));
    if (!valid || new Set(values).size !== values.length) return undefined;
    property.enum = values as Array<string | number>;
    const labels = objectValue(raw.enumLabelsLocalized);
    if (labels) {
      const parsed: Record<string, string[]> = {};
      for (const [locale, list] of Object.entries(labels).slice(0, DATABASE_DRIVER_LOCALES_MAX)) {
        if (!LOCALE_PATTERN.test(locale) || !Array.isArray(list) || list.length !== values.length) continue;
        if (!list.every((item) => typeof item === "string")) continue;
        parsed[locale] = (list as string[]).map((item) => sanitizeText(item, DATABASE_DRIVER_TITLE_MAX_RUNES));
      }
      if (Object.keys(parsed).length) property.enumLabelsLocalized = parsed;
    }
  }
  if (raw.default !== undefined) {
    if (!isValidConfigurationValue(property, raw.default)) return undefined;
    property.default = raw.default as DatabaseDriverOptionValue;
  }
  return property;
}

function parseOptions(value: unknown): PluginConfigurationSchema | undefined | null {
  if (value === undefined || value === null) return undefined;
  const raw = objectValue(value);
  if (!raw) return null;
  const entries = Object.entries(raw);
  if (entries.length > DATABASE_DRIVER_OPTIONS_MAX) return null;
  const properties: Record<string, PluginConfigurationProperty> = {};
  for (const [key, entry] of entries) {
    if (key.length > DATABASE_DRIVER_OPTION_KEY_MAX_LENGTH || !OPTION_KEY_PATTERN.test(key)) return null;
    const property = parseOption(entry);
    if (!property) return null;
    properties[key] = property;
  }
  return entries.length ? { properties } : undefined;
}

/** `plugin:<pluginId>/<driverId>`. */
export function pluginDatabaseDriverKey(ref: DatabaseDriverRef): string {
  return `${KEY_PREFIX}${ref.pluginId}/${ref.driverId}`;
}

/** Splits a catalog key; undefined for built-in or malformed keys. */
export function parsePluginDatabaseDriverKey(key: string): DatabaseDriverRef | undefined {
  if (!key.startsWith(KEY_PREFIX)) return undefined;
  const rest = key.slice(KEY_PREFIX.length);
  const index = rest.lastIndexOf("/");
  if (index <= 0) return undefined;
  return normalizeDatabaseDriverRef({ pluginId: rest.slice(0, index), driverId: rest.slice(index + 1) });
}

/** Validates a persisted or untrusted driver reference. */
export function normalizeDatabaseDriverRef(value: unknown): DatabaseDriverRef | undefined {
  const record = objectValue(value);
  if (!record) return undefined;
  const { pluginId, driverId } = record;
  if (typeof pluginId !== "string" || !PLUGIN_ID_PATTERN.test(pluginId)) return undefined;
  if (typeof driverId !== "string" || !DRIVER_ID_PATTERN.test(driverId)) return undefined;
  return { pluginId, driverId };
}

export function sameDatabaseDriverRef(left: DatabaseDriverRef | undefined, right: DatabaseDriverRef | undefined): boolean {
  if (!left || !right) return !left && !right;
  return left.pluginId === right.pluginId && left.driverId === right.driverId;
}

/** DbType for dialect-keyed helpers: the replaced type, else the dialect representative. */
export function databaseDriverDialectType(sqlDialect: DatabaseDriverSqlDialect, replaces?: DbType): DbType {
  return replaces ?? DATABASE_DRIVER_DIALECT_TYPES[sqlDialect];
}

/** Re-validates one snapshot entry; returns undefined when anything is out of contract. */
export function parsePluginDatabaseDriverEntry(
  value: unknown,
  plugin: { pluginId: string; pluginName: string; version?: string },
): PluginDatabaseDriver | undefined {
  const record = objectValue(value);
  if (!record || !PLUGIN_ID_PATTERN.test(plugin.pluginId)) return undefined;
  const id = record.id;
  if (typeof id !== "string" || !DRIVER_ID_PATTERN.test(id)) return undefined;
  const category = record.category ?? "other";
  if (!includes(DATABASE_DRIVER_CATEGORIES, category)) return undefined;
  const sqlDialect = record.sqlDialect ?? "generic";
  if (!includes(DATABASE_DRIVER_SQL_DIALECTS, sqlDialect)) return undefined;
  let replaces: DatabaseDriverReplaceableType | undefined;
  if (record.replaces !== undefined && record.replaces !== null && record.replaces !== "") {
    if (!includes(DATABASE_DRIVER_REPLACEABLE_TYPES, record.replaces)) return undefined;
    replaces = record.replaces;
  }
  const connection = objectValue(record.connection);
  if (!connection) return undefined;
  const fields = uniqueSubset(connection.fields, DATABASE_DRIVER_CONNECTION_FIELDS);
  if (!fields || fields.length === 0) return undefined;
  // The host encodes an empty optional list as null; treat it like an omitted list.
  const required = connection.required == null ? [] : uniqueSubset(connection.required, DATABASE_DRIVER_CONNECTION_FIELDS);
  if (!required || !required.every((field) => fields.includes(field))) return undefined;
  let defaultPort: number | undefined;
  if (connection.defaultPort != null) {
    const port = connection.defaultPort;
    if (typeof port !== "number" || !Number.isInteger(port) || port < 1 || port > 65535 || !fields.includes("port")) return undefined;
    defaultPort = port;
  }
  let fileExtensions: string[] = [];
  if (connection.fileExtensions != null) {
    const list = connection.fileExtensions;
    if (!Array.isArray(list) || list.length > DATABASE_DRIVER_FILE_EXTENSIONS_MAX || !fields.includes("filePath")) return undefined;
    if (!list.every((item) => typeof item === "string" && FILE_EXTENSION_PATTERN.test(item))) return undefined;
    fileExtensions = [...new Set(list as string[])];
  }
  const options = parseOptions(connection.options);
  if (options === null) return undefined;
  const features = record.features == null ? [] : uniqueSubset(record.features, DATABASE_DRIVER_FEATURES);
  if (!features) return undefined;
  // Finalize rules: row edits need keys and columns, destructive needs tables.
  if (features.some((feature) => feature.startsWith("rows.")) && !(features.includes("metadata.primaryKeys") && features.includes("metadata.columns"))) {
    return undefined;
  }
  if (features.some((feature) => feature.startsWith("destructive.")) && !features.includes("metadata.tables")) return undefined;
  const icon = record.icon === undefined || record.icon === null || record.icon === "" ? undefined : record.icon;
  if (icon !== undefined && !isContributionAssetKey(icon)) return undefined;
  const title = typeof record.title === "string" ? sanitizeText(record.title, DATABASE_DRIVER_TITLE_MAX_RUNES) : "";
  const description = typeof record.description === "string"
    ? sanitizeText(record.description, DATABASE_DRIVER_DESCRIPTION_MAX_RUNES)
    : "";
  const specFingerprint = typeof record.specFingerprint === "string" && FINGERPRINT_PATTERN.test(record.specFingerprint)
    ? record.specFingerprint
    : undefined;
  return {
    key: pluginDatabaseDriverKey({ pluginId: plugin.pluginId, driverId: id }),
    pluginId: plugin.pluginId,
    pluginName: sanitizeText(plugin.pluginName, DATABASE_DRIVER_TITLE_MAX_RUNES) || plugin.pluginId,
    pluginVersion: sanitizeText(plugin.version ?? "", 64),
    driverId: id,
    title: title || id,
    titleLocalized: parseLocalized(record.titleLocalized, DATABASE_DRIVER_TITLE_MAX_RUNES),
    ...(description ? { description } : {}),
    descriptionLocalized: parseLocalized(record.descriptionLocalized, DATABASE_DRIVER_DESCRIPTION_MAX_RUNES),
    ...(icon ? { icon } : {}),
    category,
    sqlDialect,
    ...(replaces ? { replaces } : {}),
    dbType: replaces ?? "plugin",
    dialectType: databaseDriverDialectType(sqlDialect, replaces),
    fields,
    required,
    ...(defaultPort ? { defaultPort } : {}),
    fileExtensions,
    ...(options ? { options } : {}),
    features,
    ...(specFingerprint ? { specFingerprint } : {}),
  };
}

/**
 * Builds the plugin driver catalog: process plugins only, deduplicated,
 * sorted by plugin id then driver id. Skipped entries are reported as issues.
 */
export function resolvePluginDatabaseDrivers(contributions: readonly PointContribution[]): {
  drivers: PluginDatabaseDriver[];
  issues: DatabaseDriverIssue[];
} {
  const drivers: PluginDatabaseDriver[] = [];
  const issues: DatabaseDriverIssue[] = [];
  const seen = new Set<string>();
  for (const contribution of contributions) {
    if (contribution.runtime !== "process" || !PLUGIN_ID_PATTERN.test(contribution.pluginId)) continue;
    const entries = Array.isArray(contribution.point?.entries) ? contribution.point.entries : [];
    entries.slice(0, DATABASE_DRIVER_MAX_ENTRIES).forEach((entry, index) => {
      const id = typeof objectValue(entry)?.id === "string" ? String(objectValue(entry)!.id).slice(0, 64) : undefined;
      const driver = parsePluginDatabaseDriverEntry(entry, contribution);
      if (!driver) {
        issues.push({ pluginId: contribution.pluginId, index, ...(id ? { id } : {}), reason: "invalid entry" });
        return;
      }
      if (seen.has(driver.key)) {
        issues.push({ pluginId: contribution.pluginId, index, id: driver.driverId, reason: "duplicate id" });
        return;
      }
      seen.add(driver.key);
      drivers.push(driver);
    });
  }
  drivers.sort((left, right) => left.pluginId.localeCompare(right.pluginId) || left.driverId.localeCompare(right.driverId));
  return { drivers, issues };
}

export function databaseDriverTitle(
  driver: { title: string; titleLocalized?: Record<string, string> },
  locale: string | readonly string[],
): string {
  return resolvePluginTitle({ title: driver.title, titleLocalized: driver.titleLocalized }, locale);
}

export function databaseDriverDescription(driver: PluginDatabaseDriver, locale: string | readonly string[]): string {
  if (!driver.description && !driver.descriptionLocalized) return "";
  return resolvePluginTitle({ title: driver.description ?? "", titleLocalized: driver.descriptionLocalized }, locale);
}

export function databaseDriverSupports(driver: Pick<PluginDatabaseDriver, "features">, feature: DatabaseDriverFeature): boolean {
  return driver.features.includes(feature);
}

export function databaseDriverHasField(driver: Pick<PluginDatabaseDriver, "fields">, field: DatabaseDriverField): boolean {
  return driver.fields.includes(field);
}

/** Secret fields this driver receives (password/token), for disclosure copy. */
export function databaseDriverSecretFields(driver: Pick<PluginDatabaseDriver, "fields">): DatabaseDriverField[] {
  return driver.fields.filter((field) => (DATABASE_DRIVER_SECRET_FIELDS as readonly string[]).includes(field));
}

// ---------------------------------------------------------------------------
// Options form (non-secret, reuses the configuration point's field model).

export function databaseDriverOptionFields(driver: PluginDatabaseDriver, locale: string | readonly string[]): PluginConfigurationField[] {
  return driver.options ? configurationFields(driver.options, locale) : [];
}

/** Declared defaults for every option that has one. */
export function databaseDriverOptionDefaults(driver: PluginDatabaseDriver): DatabaseDriverOptions {
  const result: DatabaseDriverOptions = {};
  for (const [key, property] of Object.entries(driver.options?.properties ?? {})) {
    if (property.default !== undefined && !Array.isArray(property.default)) result[key] = property.default;
  }
  return result;
}

/** Keeps only declared, valid option values (unknown keys and invalid values are dropped). */
export function sanitizeDatabaseDriverOptions(driver: PluginDatabaseDriver, values: unknown): DatabaseDriverOptions {
  const raw = objectValue(values);
  const result: DatabaseDriverOptions = {};
  if (!raw) return result;
  for (const [key, property] of Object.entries(driver.options?.properties ?? {})) {
    if (!Object.prototype.hasOwnProperty.call(raw, key)) continue;
    const value = raw[key];
    if (Array.isArray(value)) continue;
    if (isValidConfigurationValue(property, value)) result[key] = value as DatabaseDriverOptionValue;
  }
  return result;
}

/** Keys of option values that are present but invalid (for form feedback). */
export function invalidDatabaseDriverOptions(driver: PluginDatabaseDriver, values: unknown): string[] {
  const raw = objectValue(values) ?? {};
  return Object.entries(driver.options?.properties ?? {})
    .filter(([key, property]) => {
      if (!Object.prototype.hasOwnProperty.call(raw, key)) return false;
      const value = raw[key];
      if (value === undefined || value === null) return false;
      if (value === "" && property.type === "string" && !property.enum?.length) return false;
      return !isValidConfigurationValue(property, value);
    })
    .map(([key]) => key);
}

// ---------------------------------------------------------------------------
// Connection binding (explicit only; `replaces` never routes automatically).

type ConnectionFieldValues = Pick<
  DatabaseConnection,
  "host" | "port" | "database" | "username" | "password" | "token" | "filePath" | "org" | "authSource" | "ssl"
>;

/** Required fields that are empty in `connection` (port 0 counts as empty). */
export function missingDatabaseDriverFields(driver: PluginDatabaseDriver, connection: Partial<ConnectionFieldValues>): DatabaseDriverField[] {
  return driver.required.filter((field) => {
    const value = connection[field];
    if (field === "ssl") return false;
    if (field === "port") return typeof value !== "number" || value < 1 || value > 65535;
    return typeof value !== "string" || !value.trim();
  });
}

/**
 * Binds a connection to a plugin driver: `dbType` becomes the driver's
 * `replaces` type (or "plugin"), options keep valid values of the same driver
 * and otherwise start from the declared defaults. Credentials are untouched;
 * the caller records the release grant.
 */
export function bindConnectionToPluginDriver<T extends DatabaseConnection>(connection: T, driver: PluginDatabaseDriver): T {
  const sameDriver = sameDatabaseDriverRef(connection.driver, driver);
  const options = {
    ...databaseDriverOptionDefaults(driver),
    ...(sameDriver ? sanitizeDatabaseDriverOptions(driver, connection.options) : {}),
  };
  const typeChanged = connection.dbType !== driver.dbType;
  return {
    ...connection,
    dbType: driver.dbType,
    driver: { pluginId: driver.pluginId, driverId: driver.driverId },
    options,
    ...(typeChanged && driver.defaultPort ? { port: driver.defaultPort } : {}),
  };
}

/**
 * "Use built-in driver": drops the plugin binding. Only possible when the
 * connection keeps a built-in type (never for `dbType: "plugin"`); the caller
 * checks that this host actually has the built-in driver.
 */
export function unbindConnectionDriver<T extends DatabaseConnection>(connection: T): T | undefined {
  if (connection.dbType === "plugin") return undefined;
  const next = { ...connection };
  delete next.driver;
  delete next.options;
  return next;
}

/**
 * Connections that could be rebound to `driver` (E-10): same built-in type as
 * `replaces`, not already bound to this driver. Plugin-only drivers have no
 * candidates (their connections are created bound).
 */
export function databaseDriverRebindCandidates<T extends DatabaseConnection>(connections: readonly T[], driver: PluginDatabaseDriver): T[] {
  if (!driver.replaces) return [];
  return connections.filter((connection) => connection.dbType === driver.replaces && !sameDatabaseDriverRef(connection.driver, driver));
}

/** Connections bound to a plugin driver of `dbType` that could go back to the built-in driver. */
export function databaseDriverBuiltinCandidates<T extends DatabaseConnection>(connections: readonly T[], dbType: DbType): T[] {
  if (dbType === "plugin") return [];
  return connections.filter((connection) => connection.dbType === dbType && connection.driver);
}

export type DatabaseDriverRebindTarget = { kind: "plugin"; driver: PluginDatabaseDriver } | { kind: "builtin" };

/**
 * Applies an explicit (bulk) rebind to the selected connection ids. Returns
 * only the connections that changed; ineligible ones are left out.
 */
export function applyDatabaseDriverRebind<T extends DatabaseConnection>(
  connections: readonly T[],
  ids: readonly string[],
  target: DatabaseDriverRebindTarget,
): T[] {
  const selected = new Set(ids);
  const changed: T[] = [];
  for (const connection of connections) {
    if (!selected.has(connection.id)) continue;
    if (target.kind === "builtin") {
      if (!connection.driver) continue;
      const next = unbindConnectionDriver(connection);
      if (next) changed.push(next);
      continue;
    }
    const { driver } = target;
    if (sameDatabaseDriverRef(connection.driver, driver)) continue;
    // Only same-type rebinding: a plugin driver never changes a connection's database type.
    if (connection.dbType !== driver.dbType) continue;
    changed.push(bindConnectionToPluginDriver(connection, driver));
  }
  return changed;
}

// ---------------------------------------------------------------------------
// Built-in + plugin catalog for the connection dialog.

export interface DatabaseDriverCatalogEntry {
  /** `builtin:<dbType>` or `plugin:<pluginId>/<driverId>`. */
  key: string;
  source: "builtin" | "plugin";
  dbType: DbType;
  dialectType: DbType;
  /** Undefined for built-ins (their gating stays in db-type-capabilities.ts). */
  features?: readonly DatabaseDriverFeature[];
  plugin?: PluginDatabaseDriver;
}

export function builtinDatabaseDriverKey(dbType: DbType): string {
  return `builtin:${dbType}`;
}

/** Built-in types (the host's supported list; null means unknown, keep all) followed by plugin drivers. */
export function buildDatabaseDriverCatalog(
  builtinTypes: readonly DbType[],
  supported: ReadonlySet<DbType> | null,
  drivers: readonly PluginDatabaseDriver[],
): DatabaseDriverCatalogEntry[] {
  const builtins = builtinTypes
    .filter((dbType) => dbType !== "plugin" && (supported?.has(dbType) ?? true))
    .map<DatabaseDriverCatalogEntry>((dbType) => ({ key: builtinDatabaseDriverKey(dbType), source: "builtin", dbType, dialectType: dbType }));
  const plugins = drivers.map<DatabaseDriverCatalogEntry>((driver) => ({
    key: driver.key,
    source: "plugin",
    dbType: driver.dbType,
    dialectType: driver.dialectType,
    features: driver.features,
    plugin: driver,
  }));
  return [...builtins, ...plugins];
}

// ---------------------------------------------------------------------------
// Credential release grant status (host commands, see E5).

export type DatabaseDriverUnavailableReason =
  | "notInstalled"
  | "disabled"
  | "error"
  | "workspace"
  | "capability"
  | "noDriver"
  | "protocol";

const UNAVAILABLE_REASONS: readonly DatabaseDriverUnavailableReason[] = [
  "notInstalled",
  "disabled",
  "error",
  "workspace",
  "capability",
  "noDriver",
  "protocol",
];

export interface DatabaseDriverGrantRef extends DatabaseDriverRef {
  connectionId: string;
}

export interface DatabaseDriverGrantStatus extends DatabaseDriverGrantRef {
  available: boolean;
  reason?: DatabaseDriverUnavailableReason;
  granted: boolean;
  /** The user explicitly trusts the plugin: credentials cross without a per-connection grant. */
  trusted?: boolean;
}

const CONNECTION_ID_MAX = 128;

export function isDatabaseConnectionId(value: unknown): value is string {
  return typeof value === "string" && value.length > 0 && value.length <= CONNECTION_ID_MAX && ![...value].some((char) => char.charCodeAt(0) < 0x20);
}

/**
 * Parses `plugin_host_database_driver_status`: an array (or `{statuses}` /
 * `{items}`) in request order. Items that name their ref are matched by it,
 * others by position. Anything malformed reads as unavailable and ungranted.
 */
export function parseDatabaseDriverGrantStatuses(raw: unknown, refs: readonly DatabaseDriverGrantRef[]): DatabaseDriverGrantStatus[] {
  const record = objectValue(raw);
  const list = Array.isArray(raw) ? raw : Array.isArray(record?.statuses) ? record.statuses : Array.isArray(record?.items) ? record.items : [];
  return refs.map((ref, index) => {
    const byRef = list.find((item) => {
      const entry = objectValue(item);
      return entry?.connectionId === ref.connectionId && entry.pluginId === ref.pluginId && entry.driverId === ref.driverId;
    });
    const entry = objectValue(byRef ?? list[index]);
    const named = entry && typeof entry.connectionId === "string";
    const matches = entry && (!named || (entry.connectionId === ref.connectionId && entry.pluginId === ref.pluginId && entry.driverId === ref.driverId));
    const available = Boolean(matches && entry!.available === true);
    const reason = matches && includes(UNAVAILABLE_REASONS, entry!.reason) ? entry!.reason : undefined;
    return {
      ...ref,
      available,
      ...(!available ? { reason: reason ?? "notInstalled" } : {}),
      granted: Boolean(matches && entry!.granted === true),
      trusted: Boolean(matches && entry!.trusted === true),
    };
  });
}

/** Whether connecting may release credentials without asking: an explicit grant or explicit plugin trust. */
export function databaseDriverReleaseAllowed(status: Pick<DatabaseDriverGrantStatus, "granted" | "trusted"> | undefined): boolean {
  return Boolean(status?.granted || status?.trusted);
}

export interface CredentialReleaseDecision {
  confirmed: boolean;
  /** The user ticked "always trust this plugin". */
  trust: boolean;
}

export interface CredentialReleaseGateDeps<Target> {
  /** Fresh host status for the reference; undefined when it cannot be read. */
  status(ref: DatabaseDriverGrantRef): Promise<Pick<DatabaseDriverGrantStatus, "granted" | "trusted"> | undefined>;
  /** Shows the protected credential-release confirm. */
  confirm(target: Target): Promise<CredentialReleaseDecision>;
  grant(ref: DatabaseDriverGrantRef): Promise<void>;
  setTrust(pluginId: string, trusted: true): Promise<void>;
}

function grantRefKey(ref: DatabaseDriverGrantRef) {
  return `${ref.connectionId}\u0000${ref.pluginId}\u0000${ref.driverId}`;
}

/**
 * Serializes credential-release prompts. Concurrent requests for the same
 * reference share one prompt; requests for other references wait for the
 * open prompt and then re-read the host status, so a grant or trust recorded
 * meanwhile (another prompt, the connection dialog, the rebind dialog) is
 * honoured instead of asking again. Resolves true when release is allowed.
 */
export function createCredentialReleaseGate<Target>(deps: CredentialReleaseGateDeps<Target>) {
  const inFlight = new Map<string, Promise<boolean>>();
  let queue: Promise<unknown> = Promise.resolve();

  async function run(ref: DatabaseDriverGrantRef, target: Target): Promise<boolean> {
    const current = await deps.status(ref).catch(() => undefined);
    if (databaseDriverReleaseAllowed(current)) return true;
    const decision = await deps.confirm(target);
    if (!decision.confirmed) return false;
    await deps.grant(ref);
    if (decision.trust) await deps.setTrust(ref.pluginId, true);
    return true;
  }

  return function ensure(ref: DatabaseDriverGrantRef, target: Target): Promise<boolean> {
    const key = grantRefKey(ref);
    const pending = inFlight.get(key);
    if (pending) return pending;
    const task = queue.then(() => run(ref, target));
    queue = task.catch(() => undefined);
    const tracked = task.finally(() => {
      if (inFlight.get(key) === tracked) inFlight.delete(key);
    });
    inFlight.set(key, tracked);
    return tracked;
  };
}

/** Host error code prefix (`untrusted: …`, `unavailable: …`), if any. */
export function databaseDriverErrorCode(error: unknown): string | undefined {
  const message = error instanceof Error ? error.message : typeof error === "string" ? error : "";
  const match = /^(?:[A-Za-z]+Error:\s*)?([a-z_]+):/.exec(message.trim());
  return match?.[1];
}

// ---------------------------------------------------------------------------
// Install preview (manifest entries, before the host decoded them).

export interface DatabaseDriverPreview {
  title: string;
  dialect: string;
  replaces?: string;
  secretFields: string[];
  writes: boolean;
  destructive: boolean;
}

/** One line per declared driver; only cheap shape checks (the host validates). */
export function databaseDriverPreviewSummary(contributes: unknown): DatabaseDriverPreview[] {
  const list = objectValue(contributes)?.[DATABASE_DRIVERS_CONTRIBUTION_POINT];
  if (!Array.isArray(list)) return [];
  const result: DatabaseDriverPreview[] = [];
  for (const entry of list.slice(0, DATABASE_DRIVER_MAX_ENTRIES)) {
    const record = objectValue(entry);
    if (!record) continue;
    const id = typeof record.id === "string" ? sanitizeText(record.id, 32) : "";
    const title = (typeof record.title === "string" ? sanitizeText(record.title, DATABASE_DRIVER_TITLE_MAX_RUNES) : "") || id;
    if (!title) continue;
    const fields = Array.isArray(objectValue(record.connection)?.fields) ? (objectValue(record.connection)!.fields as unknown[]) : [];
    const features = Array.isArray(record.features) ? record.features.filter((item): item is string => typeof item === "string") : [];
    result.push({
      title,
      dialect: includes(DATABASE_DRIVER_SQL_DIALECTS, record.sqlDialect) ? record.sqlDialect : "generic",
      ...(includes(DATABASE_DRIVER_REPLACEABLE_TYPES, record.replaces) ? { replaces: record.replaces } : {}),
      secretFields: DATABASE_DRIVER_SECRET_FIELDS.filter((field) => fields.includes(field)),
      writes: features.some((feature) => feature.startsWith("rows.") || feature === "transaction.snapshotWrite"),
      destructive: features.some((feature) => feature.startsWith("destructive.")),
    });
  }
  return result;
}

/**
 * Import of database connections saved by other tools. The Go side
 * (backend/dbimport) reads and decrypts the tool files and returns
 * a password-free preview; this module validates that preview, resolves each
 * row against the local catalog (built-in types of this build, installed
 * plugin drivers, existing connections) and parses pasted connection URLs.
 * It never sees imported passwords except those typed into pasted URLs.
 */
import { parseDatabaseConnectionUrl, type ParsedDatabaseConnectionUrl } from "./connection-url.ts";
import { databaseDefaultPorts, type DatabaseConnection, type DatabaseDriverRef, type DbType } from "./types.ts";

export const DATABASE_IMPORT_FILE_SOURCES = ["dbeaver", "navicat", "datagrip", "workbench", "heidisql"] as const;
export type DatabaseImportFileSource = (typeof DATABASE_IMPORT_FILE_SOURCES)[number];
export type DatabaseImportSource = DatabaseImportFileSource | "url";

/** Product names are brands and stay untranslated. */
export const databaseImportSourceLabels: Readonly<Record<DatabaseImportSource, string>> = {
  dbeaver: "DBeaver",
  navicat: "Navicat",
  datagrip: "DataGrip",
  workbench: "MySQL Workbench",
  heidisql: "HeidiSQL",
  url: "URL",
};

/** How each source is picked: a folder, a single file, or either. */
export const databaseImportPickers: Readonly<Record<DatabaseImportFileSource, "file" | "directory" | "either">> = {
  dbeaver: "either",
  navicat: "file",
  datagrip: "either",
  workbench: "file",
  heidisql: "file",
};

export const DATABASE_IMPORT_MAX_URL_LINES = 200;
const MAX_ROWS = 2000;
const MAX_TEXT = 4096;

export type DatabaseImportStatus = "ready" | "needsPlugin" | "unsupported";

export interface DatabaseImportTunnel {
  type: "ssh" | "socks5";
  host: string;
  port: number;
  username?: string;
  authType?: "password" | "key";
  keyPath?: string;
  hasPassword: boolean;
  hasPassphrase: boolean;
}

export interface DatabaseImportCandidate {
  id: string;
  name: string;
  folder?: string;
  sourceType: string;
  dbType?: DbType;
  pluginDriverId?: string;
  status: DatabaseImportStatus;
  host: string;
  port: number;
  database?: string;
  username?: string;
  filePath?: string;
  authSource?: string;
  ssl: boolean;
  hasPassword: boolean;
  passwordUnavailable: boolean;
  tunnel?: DatabaseImportTunnel;
  warnings: string[];
}

export interface DatabaseImportPreview {
  importId: string;
  source: DatabaseImportFileSource;
  path: string;
  rows: DatabaseImportCandidate[];
  warnings: string[];
}

export interface DetectedImportLocation {
  source: DatabaseImportFileSource;
  path: string;
  label: string;
}

type UnknownRecord = Record<string, unknown>;

function isRecord(value: unknown): value is UnknownRecord {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function text(value: unknown): string {
  return typeof value === "string" ? value.slice(0, MAX_TEXT) : "";
}

function optionalText(value: unknown): string | undefined {
  return text(value) || undefined;
}

function port(value: unknown): number {
  return typeof value === "number" && Number.isInteger(value) && value > 0 && value <= 65535 ? value : 0;
}

function codes(value: unknown): string[] {
  if (!Array.isArray(value)) return [];
  return value.filter((item): item is string => typeof item === "string" && /^[a-z_]{1,48}$/.test(item)).slice(0, 16);
}

function isFileSource(value: unknown): value is DatabaseImportFileSource {
  return typeof value === "string" && (DATABASE_IMPORT_FILE_SOURCES as readonly string[]).includes(value);
}

function isKnownDbType(value: unknown): value is DbType {
  return typeof value === "string" && value !== "plugin" && Object.prototype.hasOwnProperty.call(databaseDefaultPorts, value);
}

function parseTunnel(value: unknown): DatabaseImportTunnel | undefined {
  if (!isRecord(value) || (value.type !== "ssh" && value.type !== "socks5")) return undefined;
  const host = text(value.host);
  if (!host) return undefined;
  return {
    type: value.type,
    host,
    port: port(value.port) || (value.type === "ssh" ? 22 : 1080),
    username: optionalText(value.username),
    authType: value.authType === "key" ? "key" : value.type === "ssh" ? "password" : undefined,
    keyPath: optionalText(value.keyPath),
    hasPassword: value.hasPassword === true,
    hasPassphrase: value.hasPassphrase === true,
  };
}

function parseCandidate(value: unknown): DatabaseImportCandidate | undefined {
  if (!isRecord(value)) return undefined;
  const id = text(value.id);
  if (!/^r\d{1,6}$/.test(id)) return undefined;
  let status: DatabaseImportStatus =
    value.status === "ready" || value.status === "needsPlugin" ? value.status : "unsupported";
  const dbType = isKnownDbType(value.dbType) ? value.dbType : undefined;
  const pluginDriverId = /^[a-z][a-z0-9-]{0,31}$/.test(text(value.pluginDriverId)) ? text(value.pluginDriverId) : undefined;
  if (status === "ready" && !dbType) status = "unsupported";
  if (status === "needsPlugin" && !pluginDriverId) status = "unsupported";
  return {
    id,
    name: text(value.name) || id,
    folder: optionalText(value.folder),
    sourceType: text(value.sourceType),
    dbType,
    pluginDriverId,
    status,
    host: text(value.host),
    port: port(value.port),
    database: optionalText(value.database),
    username: optionalText(value.username),
    filePath: optionalText(value.filePath),
    authSource: optionalText(value.authSource),
    ssl: value.ssl === true,
    hasPassword: value.hasPassword === true,
    passwordUnavailable: value.passwordUnavailable === true,
    tunnel: parseTunnel(value.tunnel),
    warnings: codes(value.warnings),
  };
}

/** Validates the `db_import_preview` payload. */
export function parseDatabaseImportPreview(raw: unknown): DatabaseImportPreview | undefined {
  if (!isRecord(raw) || !isFileSource(raw.source)) return undefined;
  const importId = text(raw.importId);
  if (!importId || !Array.isArray(raw.rows)) return undefined;
  const rows: DatabaseImportCandidate[] = [];
  for (const item of raw.rows.slice(0, MAX_ROWS)) {
    const row = parseCandidate(item);
    if (row) rows.push(row);
  }
  return { importId, source: raw.source, path: text(raw.path), rows, warnings: codes(raw.warnings) };
}

/** Validates the `db_import_detect` payload. */
export function parseDetectedImportLocations(raw: unknown): DetectedImportLocation[] {
  if (!Array.isArray(raw)) return [];
  const locations: DetectedImportLocation[] = [];
  for (const item of raw.slice(0, 64)) {
    if (!isRecord(item) || !isFileSource(item.source)) continue;
    const path = text(item.path);
    if (path) locations.push({ source: item.source, path, label: text(item.label) || path });
  }
  return locations;
}

/** Extracts the stable code of a `db_import: <code>` backend error. */
export function databaseImportErrorCode(error: unknown): string | undefined {
  const message = error instanceof Error ? error.message : typeof error === "string" ? error : "";
  return /db_import: ([a-z_]+)/.exec(message)?.[1];
}

// ── resolution against the local catalog ──

export interface DatabaseImportDriver {
  pluginId: string;
  driverId: string;
  replaces?: DbType;
}

export type DatabaseImportExisting = Pick<
  DatabaseConnection,
  "name" | "dbType" | "host" | "port" | "database" | "username" | "filePath" | "driver"
>;

export interface DatabaseImportContext {
  connections: readonly DatabaseImportExisting[];
  drivers: readonly DatabaseImportDriver[];
  /** Built-in types of this build; null when unknown (every type assumed). */
  supportedTypes: ReadonlySet<DbType> | null;
}

export interface ResolvedDatabaseImportRow {
  candidate: DatabaseImportCandidate;
  status: DatabaseImportStatus;
  /** Plugin driver the row will be bound to. */
  driver?: DatabaseDriverRef;
  /** Driver id a plugin must contribute when status is needsPlugin. */
  requiredDriverId?: string;
  /** Name of the existing connection (or earlier row) this one duplicates. */
  duplicateOf?: string;
  /** Rows imported by default: ready and not a duplicate. */
  defaultSelected: boolean;
}

const fileTypes = new Set<DbType>(["sqlite", "duckdb"]);

/**
 * Duplicate identity: type + host + port + database + user (file path for
 * file databases). Plugin-only types use their driver id as type.
 */
export function databaseImportIdentity(parts: {
  type: string;
  host?: string;
  port?: number;
  database?: string;
  username?: string;
  filePath?: string;
}): string {
  const type = parts.type.toLowerCase();
  if (fileTypes.has(type as DbType) || (!parts.host && parts.filePath)) {
    return [type, (parts.filePath ?? "").trim()].join("\u0000");
  }
  return [
    type,
    (parts.host ?? "").trim().toLowerCase(),
    String(parts.port ?? 0),
    (parts.database ?? "").trim(),
    (parts.username ?? "").trim(),
  ].join("\u0000");
}

function existingIdentity(connection: DatabaseImportExisting): string {
  const type = connection.dbType === "plugin" ? `plugin:${connection.driver?.driverId ?? ""}` : connection.dbType;
  return databaseImportIdentity({ ...connection, type });
}

function candidateIdentity(candidate: DatabaseImportCandidate): string {
  const type = candidate.dbType ?? `plugin:${candidate.pluginDriverId ?? candidate.sourceType}`;
  return databaseImportIdentity({ ...candidate, type });
}

function pickDriver(
  drivers: readonly DatabaseImportDriver[],
  match: (driver: DatabaseImportDriver) => boolean,
): DatabaseImportDriver | undefined {
  return drivers.find(match);
}

/** Resolves the preview rows against the local catalog. */
export function resolveDatabaseImportRows(
  candidates: readonly DatabaseImportCandidate[],
  context: DatabaseImportContext,
): ResolvedDatabaseImportRow[] {
  const known = new Map<string, string>();
  for (const connection of context.connections) {
    const identity = existingIdentity(connection);
    if (!known.has(identity)) known.set(identity, connection.name);
  }
  return candidates.map((candidate) => {
    const row: ResolvedDatabaseImportRow = { candidate, status: candidate.status, defaultSelected: false };
    if (candidate.status === "needsPlugin") {
      const driver = pickDriver(context.drivers, (item) => item.driverId === candidate.pluginDriverId && !item.replaces);
      if (driver) {
        row.status = "ready";
        row.driver = { pluginId: driver.pluginId, driverId: driver.driverId };
      } else {
        row.requiredDriverId = candidate.pluginDriverId;
      }
    } else if (candidate.status === "ready" && candidate.dbType) {
      const dbType = candidate.dbType;
      if (context.supportedTypes && !context.supportedTypes.has(dbType)) {
        const driver = pickDriver(context.drivers, (item) => item.replaces === dbType);
        if (driver) {
          row.driver = { pluginId: driver.pluginId, driverId: driver.driverId };
        } else {
          row.status = "needsPlugin";
          row.requiredDriverId = dbType;
        }
      }
    }
    if (row.status !== "unsupported") {
      const identity = candidateIdentity(candidate);
      row.duplicateOf = known.get(identity);
      if (!known.has(identity)) known.set(identity, candidate.name);
    }
    row.defaultSelected = row.status === "ready" && !row.duplicateOf;
    return row;
  });
}

export interface DatabaseImportCommitRow {
  id: string;
  driver?: DatabaseDriverRef;
}

/** Commit payload rows for the selected, importable rows (preview order). */
export function databaseImportCommitRows(
  rows: readonly ResolvedDatabaseImportRow[],
  selectedIds: ReadonlySet<string>,
): DatabaseImportCommitRow[] {
  return rows
    .filter((row) => row.status === "ready" && selectedIds.has(row.candidate.id))
    .map((row) => (row.driver ? { id: row.candidate.id, driver: { ...row.driver } } : { id: row.candidate.id }));
}

/** Target group for a row: `group/folder` when folders are kept. */
export function databaseImportTargetGroup(group: string, folder: string | undefined, keepFolders: boolean): string {
  const base = group.trim();
  const sub = (folder ?? "").replace(/^[/\s]+|[/\s]+$/g, "");
  if (!keepFolders || !sub) return base;
  const combined = base ? `${base}/${sub}` : sub;
  return combined.length > 128 ? base : combined;
}

// ── pasted URLs ──

export interface DatabaseImportUrlRow {
  line: number;
  parsed: ParsedDatabaseConnectionUrl;
  candidate: DatabaseImportCandidate;
}

export interface DatabaseImportUrlResult {
  rows: DatabaseImportUrlRow[];
  /** 1-based line numbers that could not be parsed. */
  invalidLines: number[];
  truncated: boolean;
}

/** `jdbc:mysql://…` → `mysql://…`; other values unchanged. */
function stripJdbcPrefix(value: string): string {
  return /^jdbc:[a-z][a-z0-9+.-]*:\/\//i.test(value) ? value.slice(5) : value;
}

function urlConnectionName(parsed: ParsedDatabaseConnectionUrl): string {
  return parsed.database ? `${parsed.host}/${parsed.database}` : parsed.host;
}

/** Parses pasted connection URLs, one per line (blank and # lines skipped). */
export function parseDatabaseImportUrls(textValue: string): DatabaseImportUrlResult {
  const result: DatabaseImportUrlResult = { rows: [], invalidLines: [], truncated: false };
  const lines = textValue.split(/\r?\n/);
  for (let index = 0; index < lines.length; index += 1) {
    const value = lines[index].trim();
    if (!value || value.startsWith("#")) continue;
    if (result.rows.length + result.invalidLines.length >= DATABASE_IMPORT_MAX_URL_LINES) {
      result.truncated = true;
      break;
    }
    const parsed = parseDatabaseConnectionUrl(stripJdbcPrefix(value));
    if (!parsed) {
      result.invalidLines.push(index + 1);
      continue;
    }
    result.rows.push({
      line: index + 1,
      parsed,
      candidate: {
        id: `u${index + 1}`,
        name: urlConnectionName(parsed),
        sourceType: value.slice(0, value.indexOf(":")).toLowerCase(),
        dbType: parsed.dbType,
        status: "ready",
        host: parsed.host,
        port: parsed.port,
        database: parsed.database || undefined,
        username: parsed.username || undefined,
        authSource: parsed.authSource || undefined,
        ssl: parsed.ssl,
        hasPassword: Boolean(parsed.password || parsed.token),
        passwordUnavailable: false,
        warnings: [],
      },
    });
  }
  return result;
}

/** Builds the connection saved for one pasted URL row. */
export function databaseConnectionFromImportUrl(
  row: DatabaseImportUrlRow,
  resolved: Pick<ResolvedDatabaseImportRow, "driver">,
  options: { id: string; group: string; now: number },
): DatabaseConnection {
  const { parsed } = row;
  const secret = Boolean(parsed.password || parsed.token);
  return {
    id: options.id,
    name: row.candidate.name,
    dbType: parsed.dbType,
    host: parsed.host,
    port: parsed.port,
    username: parsed.username || undefined,
    password: parsed.password || undefined,
    database: parsed.database || undefined,
    token: parsed.token || undefined,
    org: parsed.org || undefined,
    authSource: parsed.authSource || undefined,
    ssl: parsed.ssl,
    savePassword: secret,
    group: options.group.trim() || undefined,
    tags: [],
    driver: resolved.driver ? { ...resolved.driver } : undefined,
    createdAt: options.now,
    updatedAt: options.now,
  };
}

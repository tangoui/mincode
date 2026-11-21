import { importInlineTableData } from "@/services/database-transfer-service";
import type { BackendCommand } from "@/generated/backend-commands";
import { computed, ref, shallowRef } from "vue";
import { acceptHMRUpdate, defineStore } from "pinia";
import {
  registerDatabaseWorkspaceStore,
  takePendingDatabaseWorkspaceScope,
} from "@/stores/database-workspace-shell";
import type {
  DatabaseColumnInfo,
  DatabaseConnection,
  DatabaseConnectionConfig,
  DatabaseDiagram,
  DatabaseDiagramTable,
  DatabaseQueryResult,
  DatabasePrimaryKeyInfo,
  DatabaseSchemaInfo,
  DatabaseSchemaComparison,
  DatabaseSchemaSyncExecutionResult,
  DatabaseSchemaSyncPlan,
  DatabaseSchemaSyncSelection,
  DatabaseTableDetails,
  DatabaseTunnelConfig,
  DatabaseInstallEnvironment,
  OdbcDriverStatus,
  RedisKeyInfo,
  RedisKeyListResult,
  SchemaTable,
} from "@/domain/database/types";
import { createDatabaseMetadataCache, type DatabaseMetadataKind } from "@/domain/database/metadata-cache";
import { onBackupRestored } from "@/domain/backup/restored-event";
import { createRequestGenerationGate } from "@/domain/database/request-generation";
import {
  databaseConnectionCapabilities,
  loadsTablesPerDatabase,
} from "@/domain/database/db-type-capabilities";
import {
  resolveConnectionDriver,
} from "@/domain/database/driver-descriptor";
import type { InsertSqlMode } from "@/domain/database/insert-sql";
import type { TableDataFormat } from "@/domain/database/object-naming";
import {
  createQueryResultProgressBatcher,
  createQueryResultStream,
  type DatabaseQueryResultChunk,
  type DatabaseQueryStreamSummary,
} from "@/domain/database/query-result-stream";
import type { RowChange } from "@/domain/database/grid-pending-changes";
import { tableStructureCapabilities } from "@/domain/database/table-structure-capabilities";
import { resolvePrimaryKeyColumns } from "@/domain/database/table-index";
import { normalizeConnectionGroupOrder } from "@/domain/connections/group-order";
import { isPersistedWriteFrozen, loadPersistedValue, savePersistedValue } from "@/services/persistent-store";
import { decryptField, encryptField } from "@/services/field-crypto";
import { invokeBackend, isDesktopRuntime, listenAppEvent } from "@/services/desktop-bridge";
import {
  runWithSafety,
  sqlSafetyArgs,
  SqlSafetyPartialError,
  type SqlSafetyConfirm,
  type SqlSafetyOrigin,
  type SqlSafetyToken,
} from "@/services/sql-safety-service";
import { isStreamAvailable, JSONStream } from "@/services/desktop-stream";

// describeBackendError 提取后端错误的可读文案。
// IPC拒绝时可能抛出带 "RuntimeError" 名称的 Error，直接 String()
// 会带上 "RuntimeError: " 前缀，对用户没有信息量。
function describeBackendError(reason: unknown): string {
  return reason instanceof Error ? reason.message : String(reason);
}

const DATABASE_CONNECTIONS_KEY = "app_db_connections";
const browserStorageKey = "app_db_connections";
const DATABASE_CONNECTION_GROUP_ORDER_KEY = "app_db_connection_group_order";
export interface DatabaseQueryExecutionOptions {
  onProgress?: (result: DatabaseQueryResult) => void;
  /**
   * SQL 安全网：写语句先检查再确认。默认 origin "user"、source "query"（查询窗口）；
   * 已有专用确认界面的内部结构调用（设计器、改字符集）传 origin "preconfirmed"、source "designer"。
   */
  safety?: { origin: SqlSafetyOrigin; source?: string };
}

type QueryStreamRuntime = {
  stream: ReturnType<typeof createQueryResultStream>;
  progress?: ReturnType<typeof createQueryResultProgressBatcher>;
};

const STREAMING_QUERY_DB_TYPES = new Set([
  "sqlite", "duckdb", "postgres", "kingbase", "gaussdb", "opengauss", "highgo", "vastbase",
  "opentenbase", "gbase8c", "cockroachdb", "yugabyte", "greenplum", "questdb", "materialize", "risingwave",
  "cloudberry", "redshift", "sqlserver", "oracle", "dameng", "yashandb", "firebird", "clickhouse",
  "trino", "presto", "tdengine",
]);

function optional(value: string | undefined) {
  const trimmed = value?.trim();
  return trimmed || undefined;
}

function connectionConfig(connection: DatabaseConnection): DatabaseConnectionConfig {
  return {
    dbType: connection.dbType,
    host: connection.host.trim(),
    port: connection.port,
    username: optional(connection.username),
    password: connection.password || undefined,
    database: optional(connection.database),
    filePath: optional(connection.filePath),
    token: optional(connection.token),
    org: optional(connection.org),
    authSource: optional(connection.authSource),
    ssl: connection.ssl,
    // 所有连接都带 connectionId：SQL 安全网按它归属操作记录。
    connectionId: connection.id,
  };
}

async function decryptSecret(value: string | undefined) {
  if (!value || !value.startsWith("enc:v2:")) return value;
  return decryptField(value);
}

async function encryptSecret(value: string | undefined, enabled: boolean) {
  if (!enabled || !value) return undefined;
  return encryptField(value);
}

async function decryptTunnelSecrets(
  tunnel: DatabaseTunnelConfig | undefined
): Promise<DatabaseTunnelConfig | undefined> {
  if (!tunnel) return undefined;
  if (tunnel.type === "ssh") {
    const [password, passphrase] = await Promise.all([
      decryptSecret(tunnel.password),
      decryptSecret(tunnel.passphrase),
    ]);
    return { ...tunnel, password, passphrase };
  }
  return { ...tunnel, password: await decryptSecret(tunnel.password) };
}

async function persistTunnel(
  tunnel: DatabaseTunnelConfig | undefined,
  saveSecrets: boolean
): Promise<DatabaseTunnelConfig | undefined> {
  if (!tunnel) return undefined;
  if (tunnel.type === "ssh") {
    const [password, passphrase] = await Promise.all([
      encryptSecret(tunnel.password, saveSecrets),
      encryptSecret(tunnel.passphrase, saveSecrets),
    ]);
    return { ...tunnel, password, passphrase };
  }
  return {
    ...tunnel,
    password: await encryptSecret(tunnel.password, saveSecrets),
  };
}

function sanitizeConnectionForBrowser(connection: DatabaseConnection): DatabaseConnection {
  if (!connection.tunnel) return { ...connection, password: undefined, token: undefined };
  if (connection.tunnel.type === "ssh") {
    return {
      ...connection,
      password: undefined,
      token: undefined,
      tunnel: { ...connection.tunnel, password: undefined, passphrase: undefined },
    };
  }
  return {
    ...connection,
    password: undefined,
    token: undefined,
    tunnel: { ...connection.tunnel, password: undefined },
  };
}

async function loadPersistedConnections() {
  if (!isDesktopRuntime()) {
    const raw = localStorage.getItem(browserStorageKey);
    if (!raw) return [];
    try {
      const value: unknown = JSON.parse(raw);
      return Array.isArray(value) ? (value as DatabaseConnection[]) : [];
    } catch {
      return [];
    }
  }

  const stored = await invokeBackend<unknown>("store_get", { key: DATABASE_CONNECTIONS_KEY });
  if (!Array.isArray(stored)) return [];
  return Promise.all(
    (stored as DatabaseConnection[]).map(async (connection) => {
      // 并发发起，使所有连接的密文字段合并进同一次 decrypt_fields 批量调用。
      const [password, token, tunnel] = await Promise.all([
        decryptSecret(connection.password),
        decryptSecret(connection.token),
        decryptTunnelSecrets(connection.tunnel),
      ]);
      return { ...connection, password, token, tunnel };
    })
  );
}

async function persistConnections(connections: DatabaseConnection[]) {
  if (isPersistedWriteFrozen(DATABASE_CONNECTIONS_KEY)) return;
  localStorage.setItem(
    browserStorageKey,
    JSON.stringify(connections.map(sanitizeConnectionForBrowser))
  );
  if (!isDesktopRuntime()) return;

  const persisted = await Promise.all(
    connections.map(async (connection) => {
      const [password, token, tunnel] = await Promise.all([
        encryptSecret(connection.password, connection.savePassword),
        encryptSecret(connection.token, connection.savePassword),
        persistTunnel(connection.tunnel, connection.savePassword),
      ]);
      return { ...connection, password, token, tunnel };
    })
  );
  // Re-checked after encrypting: a backup restore may have frozen the key meanwhile.
  if (isPersistedWriteFrozen(DATABASE_CONNECTIONS_KEY)) return;
  await invokeBackend<void>("store_set", { key: DATABASE_CONNECTIONS_KEY, value: persisted });
  // 已删除连接的凭据释放授权不能残留；授权本身从不随连接持久化或导出。
}

function connectionPersistenceSnapshot(connections: DatabaseConnection[]) {
  return connections.map((connection) => ({
    ...connection,
    tags: connection.tags ? [...connection.tags] : undefined,
    tunnel: connection.tunnel ? { ...connection.tunnel } : undefined,
  }));
}

interface DatabaseTablePageOptions {
  pageToken?: string;
  namespace?: string;
}

interface DatabaseRowMutationOptions {
  namespace?: string;
}

interface DatabaseWorkspaceRuntime {
  activeConnectionId?: string;
  sessionIds: Record<string, string>;
  schemas: Record<string, DatabaseSchemaInfo>;
  selectedDatabase: string;
  selectedTable: string;
  result?: DatabaseQueryResult;
  tableColumns: DatabaseColumnInfo[];
  primaryKeys: string[];
  page: number;
  pageSize: number;
  redisDbIndex: number;
  redisPattern: string;
  redisCursor: string;
  redisKeys: RedisKeyListResult;
  selectedRedisKey: string;
  redisKeyInfo?: RedisKeyInfo;
  currentQueryId: string;
  queryRunning: boolean;
  loading: boolean;
  error: string;
}

export const useDatabaseWorkspaceStore = defineStore("database-workspace", () => {
  registerDatabaseWorkspaceStore(() => useDatabaseWorkspaceStore());
  const initialized = ref(false);
  const activeWorkspaceScopeId = ref(takePendingDatabaseWorkspaceScope() ?? "workspace:default");
  const workspaceRuntimes = new Map<string, DatabaseWorkspaceRuntime>();
  const connections = ref<DatabaseConnection[]>([]);
  const connectionGroupOrder = ref<string[]>([]);
  let connectionPersistenceQueue: Promise<void> = Promise.resolve();
  let deferredConnectionPersistenceTimer: ReturnType<typeof setTimeout> | undefined;
  let deferredConnectionPersistenceSnapshot: DatabaseConnection[] | undefined;

  function enqueueConnectionPersistence(snapshot: DatabaseConnection[]) {
    const task = connectionPersistenceQueue.then(() => persistConnections(snapshot));
    connectionPersistenceQueue = task.catch(() => undefined);
    return task;
  }

  function persistConnectionsNow() {
    if (deferredConnectionPersistenceTimer !== undefined) {
      clearTimeout(deferredConnectionPersistenceTimer);
      deferredConnectionPersistenceTimer = undefined;
      deferredConnectionPersistenceSnapshot = undefined;
    }
    return enqueueConnectionPersistence(connectionPersistenceSnapshot(connections.value));
  }

  function persistConnectionsAfterInteraction() {
    deferredConnectionPersistenceSnapshot = connectionPersistenceSnapshot(connections.value);
    if (deferredConnectionPersistenceTimer !== undefined) {
      clearTimeout(deferredConnectionPersistenceTimer);
    }
    deferredConnectionPersistenceTimer = setTimeout(() => {
      deferredConnectionPersistenceTimer = undefined;
      const snapshot = deferredConnectionPersistenceSnapshot;
      deferredConnectionPersistenceSnapshot = undefined;
      if (!snapshot) return;
      // 最近连接时间只是辅助排序元数据；持久化失败不应反向打断已建立的会话。
      void enqueueConnectionPersistence(snapshot).catch(() => undefined);
    }, 350);
  }
  const activeConnectionId = ref<string>();
  const sessionIds = ref<Record<string, string>>({});
  const schemas = shallowRef<Record<string, DatabaseSchemaInfo>>({});
  const databaseTableLoads = new Map<string, Promise<SchemaTable[]>>();
  const databaseTableLoadGenerations = new Map<string, number>();
  // Tables fetched per `${sessionId}:${database}`; the navigation schema carries none for per-database connections.
  const fetchedDatabaseTables = new Map<string, SchemaTable[]>();
  const loadedDatabaseTables = new Set<string>();
  const selectedDatabase = ref("");
  const selectedTable = ref("");
  // Results are replaced wholesale and can hold thousands of rows; deep proxies would only cost CPU and memory.
  const result = shallowRef<DatabaseQueryResult>();
  const tableColumns = ref<DatabaseColumnInfo[]>([]);
  const primaryKeys = ref<string[]>([]);
  const page = ref(1);
  const pageSize = ref(100);
  const currentQueryId = ref("");
  const queryRunning = ref(false);
  const loading = ref(false);
  const error = ref("");

  const redisDbIndex = ref(0);
  const redisPattern = ref("*");
  const redisCursor = ref("0");
  const redisKeys = ref<RedisKeyListResult>({ keys: [], nextCursor: "0", total: 0 });
  const selectedRedisKey = ref("");
  const redisKeyInfo = ref<RedisKeyInfo>();
  const redisListRequestSequences = new Map<string, number>();
  const redisKeyRequestSequences = new Map<string, number>();
  const requestGate = createRequestGenerationGate();
  const metadataCache = createDatabaseMetadataCache();
  const queryStreams = new Map<string, QueryStreamRuntime>();
  let stopQueryChunks: (() => void) | undefined;
  let queryChunksReady: Promise<boolean> | undefined;
  let stopExpiredSessions: (() => void) | undefined;
  function connectionDescriptor(connection: Pick<DatabaseConnection, "dbType" | "driver"> | undefined) {
    return connection ? resolveConnectionDriver(connection) : undefined;
  }

  function connectionCapabilities(
    connection: Pick<DatabaseConnection, "id" | "dbType" | "driver">,
  ) {
    return databaseConnectionCapabilities(connection, connectionDescriptor(connection));
  }

  function connectionDialectType(connection: Pick<DatabaseConnection, "id" | "dbType" | "driver">) {
    return connectionDescriptor(connection)?.dialectType ?? connection.dbType;
  }

  async function openSession(connection: DatabaseConnection) {
    if (!resolveConnectionDriver(connection).available) throw new Error("driver_required");
    return invokeBackend<string>("db_connect", { config: connectionConfig(connection) });
  }

  function ensureDatabaseEventListeners() {
    if (!isDesktopRuntime()) return;
    const handleQueryChunk = (chunk: DatabaseQueryResultChunk) => {
      const runtime = queryStreams.get(chunk.queryId);
      if (!runtime) return;
      try {
        runtime.stream.append(chunk);
        runtime.progress?.schedule();
      } catch {
        runtime.progress?.cancel();
        runtime.stream.release();
        queryStreams.delete(chunk.queryId);
        void invokeBackend<boolean>("db_cancel_query", { queryId: chunk.queryId }).catch(() => false);
      }
    };
    if (isStreamAvailable()) {
      // 数据通道断开后，所有进行中的查询都必须失败并取消，不能展示半截结果。
      stopQueryChunks ??= (() => {
        const socket = JSONStream("db-query-chunks");
        let stopped = false;
        let transportFailed = false;
        let readySettled = false;
        let resolveReady: (ready: boolean) => void = () => {};
        const failTransport = () => {
          if (stopped || transportFailed) return;
          transportFailed = true;
          if (!readySettled) {
            readySettled = true;
            resolveReady(false);
          }
          const reason = new Error("Database result Stream disconnected before query completion");
          for (const [queryId, runtime] of queryStreams) {
            runtime.progress?.cancel();
            runtime.stream.fail(reason);
            void invokeBackend<boolean>("db_cancel_query", { queryId }).catch(() => false);
          }
          stopQueryChunks = undefined;
          queryChunksReady = undefined;
          try {
            socket.close();
          } catch {
            // 连接可能已经关闭
          }
        };
        queryChunksReady = new Promise<boolean>((resolve) => {
          resolveReady = resolve;
          const timer = window.setTimeout(() => {
            if (readySettled) return;
            readySettled = true;
            resolve(false);
            stopped = true;
            socket.close();
          }, 5000);
          socket.addEventListener("message", (event: MessageEvent) => {
            const frame = event.data as DatabaseQueryResultChunk | { subscribed?: boolean } | undefined;
            if (frame && "subscribed" in frame && frame.subscribed) {
              window.clearTimeout(timer);
              if (!readySettled) {
                readySettled = true;
                resolve(true);
              }
              return;
            }
            if (frame && "queryId" in frame && frame.queryId) handleQueryChunk(frame);
          });
          socket.addEventListener("error", () => {
            window.clearTimeout(timer);
            failTransport();
          });
          socket.addEventListener("close", () => {
            window.clearTimeout(timer);
            failTransport();
          });
        });
        return () => {
          stopped = true;
          try {
            socket.close();
          } catch {
            // 连接可能已关闭
          }
        };
      })();
    } else {
      stopQueryChunks ??= listenAppEvent<DatabaseQueryResultChunk>("db:query-result-chunk", handleQueryChunk);
    }
    stopExpiredSessions ??= listenAppEvent<{ sessionIds?: string[] }>("db:sessions-expired", (event) => {
      const expired = new Set(event.sessionIds ?? []);
      if (expired.size === 0) return;
      const runtimes = new Map(workspaceRuntimes);
      runtimes.set(activeWorkspaceScopeId.value, runtimeSnapshot());
      for (const [scopeId, runtime] of runtimes) {
        for (const [connectionId, sessionId] of Object.entries(runtime.sessionIds)) {
          if (!expired.has(sessionId)) continue;
          metadataCache.invalidate({ sessionId });
          requestGate.invalidateSession(sessionId);
          updateRuntimeForScope(scopeId, (current) => removeConnectionFromRuntime(current, connectionId));
        }
      }
    });
  }

  const desktopRuntime = computed(() => isDesktopRuntime());
  const activeConnection = computed(() =>
    connections.value.find((connection) => connection.id === activeConnectionId.value)
  );
  const activeSessionId = computed(() =>
    activeConnectionId.value ? sessionIds.value[activeConnectionId.value] : undefined
  );
  const activeSchema = computed(() =>
    activeConnectionId.value ? schemas.value[activeConnectionId.value] : undefined
  );
  const connectedConnections = computed(() =>
    connections.value.filter((connection) => Boolean(sessionIds.value[connection.id]))
  );
  const isRedis = computed(() => activeConnection.value?.dbType === "redis");

  function runtimeSnapshot(): DatabaseWorkspaceRuntime {
    return {
      activeConnectionId: activeConnectionId.value,
      sessionIds: { ...sessionIds.value },
      schemas: { ...schemas.value },
      selectedDatabase: selectedDatabase.value,
      selectedTable: selectedTable.value,
      result: result.value,
      tableColumns: [...tableColumns.value],
      primaryKeys: [...primaryKeys.value],
      page: page.value,
      pageSize: pageSize.value,
      redisDbIndex: redisDbIndex.value,
      redisPattern: redisPattern.value,
      redisCursor: redisCursor.value,
      redisKeys: { ...redisKeys.value, keys: [...redisKeys.value.keys] },
      selectedRedisKey: selectedRedisKey.value,
      redisKeyInfo: redisKeyInfo.value,
      currentQueryId: currentQueryId.value,
      queryRunning: queryRunning.value,
      loading: loading.value,
      error: error.value,
    };
  }

  function emptyRuntime(): DatabaseWorkspaceRuntime {
    return {
      sessionIds: {},
      schemas: {},
      selectedDatabase: "",
      selectedTable: "",
      tableColumns: [],
      primaryKeys: [],
      page: 1,
      pageSize: 100,
      redisDbIndex: 0,
      redisPattern: "*",
      redisCursor: "0",
      redisKeys: { keys: [], nextCursor: "0", total: 0 },
      selectedRedisKey: "",
      currentQueryId: "",
      queryRunning: false,
      loading: false,
      error: "",
    };
  }

  function applyRuntime(runtime: DatabaseWorkspaceRuntime) {
    activeConnectionId.value = runtime.activeConnectionId;
    sessionIds.value = { ...runtime.sessionIds };
    schemas.value = { ...runtime.schemas };
    selectedDatabase.value = runtime.selectedDatabase;
    selectedTable.value = runtime.selectedTable;
    result.value = runtime.result;
    tableColumns.value = [...runtime.tableColumns];
    primaryKeys.value = [...runtime.primaryKeys];
    page.value = runtime.page;
    pageSize.value = runtime.pageSize;
    redisDbIndex.value = runtime.redisDbIndex;
    redisPattern.value = runtime.redisPattern;
    redisCursor.value = runtime.redisCursor;
    redisKeys.value = { ...runtime.redisKeys, keys: [...runtime.redisKeys.keys] };
    selectedRedisKey.value = runtime.selectedRedisKey;
    redisKeyInfo.value = runtime.redisKeyInfo;
    currentQueryId.value = runtime.currentQueryId;
    queryRunning.value = runtime.queryRunning;
    loading.value = runtime.loading;
    error.value = runtime.error;
  }

  function switchWorkspaceScope(scopeId: string) {
    const normalized = scopeId.trim();
    if (!normalized || normalized === activeWorkspaceScopeId.value) return;
    const inactiveRuntime = runtimeSnapshot();
    inactiveRuntime.result = undefined;
    inactiveRuntime.loading = false;
    workspaceRuntimes.set(activeWorkspaceScopeId.value, inactiveRuntime);
    activeWorkspaceScopeId.value = normalized;
    applyRuntime(workspaceRuntimes.get(normalized) ?? emptyRuntime());
  }

  async function discardWorkspaceScope(scopeId: string) {
    const normalized = scopeId.trim();
    if (!normalized || normalized === activeWorkspaceScopeId.value) return false;
    const runtime = workspaceRuntimes.get(normalized);
    workspaceRuntimes.delete(normalized);
    nextRedisRequestSequence(redisListRequestSequences, normalized);
    nextRedisRequestSequence(redisKeyRequestSequences, normalized);
    if (runtime && isDesktopRuntime()) {
      await Promise.allSettled(
        Object.values(runtime.sessionIds).map((sessionId) =>
          invokeBackend<void>("db_disconnect", { sessionId })
        )
      );
    }
    return true;
  }

  function updateRuntimeForScope(
    scopeId: string,
    update: (runtime: DatabaseWorkspaceRuntime) => void
  ) {
    const runtime =
      scopeId === activeWorkspaceScopeId.value
        ? runtimeSnapshot()
        : (workspaceRuntimes.get(scopeId) ?? emptyRuntime());
    update(runtime);
    if (scopeId === activeWorkspaceScopeId.value) applyRuntime(runtime);
    else workspaceRuntimes.set(scopeId, runtime);
  }

  function runtimeForScope(scopeId: string) {
    return scopeId === activeWorkspaceScopeId.value
      ? runtimeSnapshot()
      : (workspaceRuntimes.get(scopeId) ?? emptyRuntime());
  }

  function requireSessionForScope(scopeId: string, connectionId?: string) {
    const runtime = runtimeForScope(scopeId);
    const resolvedConnectionId = connectionId ?? runtime.activeConnectionId;
    const sessionId = resolvedConnectionId
      ? runtime.sessionIds[resolvedConnectionId]
      : undefined;
    if (!sessionId) throw new Error("Database session is not connected");
    return sessionId;
  }

  function clearRuntimeResult(runtime: DatabaseWorkspaceRuntime) {
    runtime.selectedTable = "";
    runtime.result = undefined;
    runtime.tableColumns = [];
    runtime.primaryKeys = [];
    runtime.page = 1;
    runtime.redisKeys = { keys: [], nextCursor: "0", total: 0 };
    runtime.selectedRedisKey = "";
    runtime.redisKeyInfo = undefined;
  }

  function removeConnectionFromRuntime(runtime: DatabaseWorkspaceRuntime, connectionId: string) {
    delete runtime.sessionIds[connectionId];
    delete runtime.schemas[connectionId];
    if (runtime.activeConnectionId !== connectionId) return;
    runtime.activeConnectionId = Object.keys(runtime.sessionIds)[0];
    runtime.selectedDatabase = runtime.activeConnectionId
      ? (runtime.schemas[runtime.activeConnectionId]?.databases[0]?.name ?? "")
      : "";
    clearRuntimeResult(runtime);
  }

  function requireActiveSession() {
    if (!activeSessionId.value) throw new Error("Database session is not connected");
    return activeSessionId.value;
  }

  let initializing: Promise<void> | undefined;
  /**
   * 已保存连接列表的加载结果，与 loading / error 分开：后两者也反映单次查询等运行时状态，
   * 不能据此判断连接列表是否可信（例如笔记关联判断连接是否已删除）。
   */
  const connectionsStatus = ref<"idle" | "loading" | "ready" | "failed">("idle");

  /** 加载已保存的连接；并发调用共享同一次加载，调用方返回时连接列表已就绪。 */
  function initialize() {
    if (!initializing) initializing = loadInitialState();
    return initializing;
  }

  async function loadInitialState() {
    if (initialized.value) return;
    initialized.value = true;
    loading.value = true;
    error.value = "";
    connectionsStatus.value = "loading";
    try {
      const [storedConnections, storedGroupOrder] = await Promise.all([
        loadPersistedConnections(),
        loadPersistedValue<unknown>(DATABASE_CONNECTION_GROUP_ORDER_KEY),
      ]);
      connections.value = storedConnections;
      connectionGroupOrder.value = normalizeConnectionGroupOrder(storedGroupOrder);
      connectionsStatus.value = "ready";
    } catch (cause) {
      error.value = String(cause);
      connectionsStatus.value = "failed";
    } finally {
      loading.value = false;
    }
  }

  /**
   * A backup restore rewrote the saved connections: reload them (and drop a
   * pending deferred save of the old list). Before the first load nothing is
   * loaded yet, and that load reads the restored data.
   */
  async function reloadSavedConnections() {
    if (!initialized.value) return;
    await initializing;
    if (deferredConnectionPersistenceTimer !== undefined) {
      clearTimeout(deferredConnectionPersistenceTimer);
      deferredConnectionPersistenceTimer = undefined;
      deferredConnectionPersistenceSnapshot = undefined;
    }
    try {
      const [storedConnections, storedGroupOrder] = await Promise.all([
        loadPersistedConnections(),
        loadPersistedValue<unknown>(DATABASE_CONNECTION_GROUP_ORDER_KEY),
      ]);
      connections.value = storedConnections;
      connectionGroupOrder.value = normalizeConnectionGroupOrder(storedGroupOrder);
      connectionsStatus.value = "ready";
    } catch (cause) {
      error.value = String(cause);
      connectionsStatus.value = "failed";
    }
  }
  onBackupRestored([DATABASE_CONNECTIONS_KEY, DATABASE_CONNECTION_GROUP_ORDER_KEY], reloadSavedConnections);

  async function saveConnectionGroupOrder(order: string[]) {
    const normalized = normalizeConnectionGroupOrder(order);
    connectionGroupOrder.value = normalized;
    await savePersistedValue(DATABASE_CONNECTION_GROUP_ORDER_KEY, normalized);
  }

  async function replaceConnectionGroupOrderName(previousName: string, nextName: string) {
    const order = connectionGroupOrder.value.map((group) =>
      group === previousName ? nextName : group
    );
    await saveConnectionGroupOrder(order);
  }

  async function saveConnection(connection: DatabaseConnection) {
    const now = Date.now();
    const normalized: DatabaseConnection = {
      ...connection,
      name: connection.name.trim(),
      host: connection.host.trim(),
      group: optional(connection.group),
      updatedAt: now,
      createdAt: connection.createdAt || now,
    };
    const index = connections.value.findIndex((item) => item.id === normalized.id);
    if (index >= 0) connections.value[index] = normalized;
    else connections.value.push(normalized);
    await persistConnectionsNow();
  }

  /**
   * 由后端直接写入持久化连接列表的导入（dbimport）：先落盘内存状态，再在持久化队列中执行写入，
   * 然后只把新写入的连接合并进内存（保留未保存密码的现有连接），最后再落盘一次。
   * 队列中写入期间排队的快照不含新连接，末尾的落盘会把它们补回。
   */
  async function importConnectionsFromBackend(write: () => Promise<readonly string[]>) {
    await persistConnectionsNow();
    const task = connectionPersistenceQueue.then(async () => {
      const ids = new Set(await write());
      const known = new Set(connections.value.map((connection) => connection.id));
      const stored = await loadPersistedConnections();
      const added = stored.filter((connection) => ids.has(connection.id) && !known.has(connection.id));
      connections.value.push(...added);
      return added;
    });
    connectionPersistenceQueue = task.then(
      () => undefined,
      () => undefined,
    );
    const added = await task;
    await persistConnectionsNow();
    return added;
  }

  async function deleteConnection(id: string) {
    workspaceRuntimes.set(activeWorkspaceScopeId.value, runtimeSnapshot());
    const sessionIdsToClose = new Set<string>();
    for (const [scopeId, runtime] of workspaceRuntimes) {
      const sessionId = runtime.sessionIds[id];
      if (sessionId) sessionIdsToClose.add(sessionId);
      removeConnectionFromRuntime(runtime, id);
      workspaceRuntimes.set(scopeId, runtime);
    }
    if (isDesktopRuntime()) {
      await Promise.allSettled(
        [...sessionIdsToClose].map((sessionId) =>
          invokeBackend<void>("db_disconnect", { sessionId })
        )
      );
    }
    applyRuntime(workspaceRuntimes.get(activeWorkspaceScopeId.value) ?? emptyRuntime());
    connections.value = connections.value.filter((connection) => connection.id !== id);
    await persistConnectionsNow();
    // 连接删除后它的 SQL 安全网留底不再有入口，一并清理；失败不影响删除（过期清理兜底）。
    if (isDesktopRuntime()) {
      await invokeBackend<void>("db_safety_purge_connection", { connectionId: id }).catch(() => undefined);
    }
  }

  async function testConnection(connection: DatabaseConnection) {
    if (!isDesktopRuntime()) throw new Error("Desktop runtime required");
    const sessionId = await openSession(connection);
    try {
      return await invokeBackend<DatabaseSchemaInfo>("db_get_navigation_schema", { sessionId });
    } finally {
      await invokeBackend<void>("db_disconnect", { sessionId }).catch(() => undefined);
    }
  }

  function clearWorkspaceResult() {
    updateRuntimeForScope(activeWorkspaceScopeId.value, clearRuntimeResult);
  }

  function invalidateDatabaseTableLoads(sessionId: string) {
    for (const key of [...databaseTableLoadGenerations.keys()]) {
      if (!key.startsWith(`${sessionId}:`)) continue;
      databaseTableLoadGenerations.set(
        key,
        (databaseTableLoadGenerations.get(key) ?? 0) + 1
      );
      databaseTableLoads.delete(key);
      loadedDatabaseTables.delete(key);
      fetchedDatabaseTables.delete(key);
    }
  }

  function withFetchedTables(schema: DatabaseSchemaInfo, sessionId: string): DatabaseSchemaInfo {
    let changed = false;
    const databases = schema.databases.map((item) => {
      const tables = fetchedDatabaseTables.get(`${sessionId}:${item.name}`);
      if (!tables) return item;
      changed = true;
      return { ...item, tables };
    });
    return changed ? { ...schema, databases } : schema;
  }

  async function loadTablesForDatabase(connectionId: string, database: string) {
    const sessionId = sessionIds.value[connectionId];
    if (!sessionId) throw new Error("Database session is not connected");
    const key = `${sessionId}:${database}`;
    if (loadedDatabaseTables.has(key)) {
      return fetchedDatabaseTables.get(key) ?? [];
    }
    const existing = databaseTableLoads.get(key);
    if (existing) return existing;
    const generation = databaseTableLoadGenerations.get(key) ?? 0;
    databaseTableLoadGenerations.set(key, generation);
    const load = invokeBackend<SchemaTable[]>("db_get_database_tables", {
      sessionId,
      database,
    }).then((tables) => {
      const isCurrent =
        databaseTableLoadGenerations.get(key) === generation &&
        sessionIds.value[connectionId] === sessionId;
      if (isCurrent) {
        // Right after connecting the navigation schema may still be loading; keeping the tables here
        // lets it merge them in when it arrives instead of this result being dropped.
        fetchedDatabaseTables.set(key, tables ?? []);
        loadedDatabaseTables.add(key);
        const schema = schemas.value[connectionId];
        if (schema) schemas.value = { ...schemas.value, [connectionId]: withFetchedTables(schema, sessionId) };
      }
      return tables ?? [];
    }).finally(() => {
      if (databaseTableLoads.get(key) === load) databaseTableLoads.delete(key);
    });
    databaseTableLoads.set(key, load);
    return load;
  }

  async function loadSchemaForConnection(scopeId: string, connectionId: string, sessionId: string) {
    const request = requestGate.begin(scopeId, sessionId, "schema");
    const startedAt = performance.now();
    try {
      const schema = await invokeBackend<DatabaseSchemaInfo>("db_get_navigation_schema", { sessionId });
      if (!requestGate.isCurrent(request)) return schema;
      if (import.meta.env.DEV) {
        performance.measure("mincode.database.navigation", {
          start: startedAt,
          end: performance.now(),
          detail: {
            type: connections.value.find((item) => item.id === connectionId)?.dbType,
            databaseCount: schema.databases.length,
            objectCount: schema.databases.reduce((total, item) => total + item.tables.length, 0),
          },
        });
      }
      let openDatabase = "";
      updateRuntimeForScope(scopeId, (runtime) => {
        if (runtime.sessionIds[connectionId] !== sessionId) return;
        runtime.schemas = { ...runtime.schemas, [connectionId]: withFetchedTables(schema, sessionId) };
        if (runtime.activeConnectionId === connectionId && !runtime.selectedDatabase) {
          runtime.selectedDatabase = schema.databases[0]?.name ?? "";
        }
        if (runtime.activeConnectionId === connectionId) openDatabase = runtime.selectedDatabase;
      });
      // The navigation schema has no tables for per-database connections; the database it opens (often
      // the one it just selected) needs its objects loaded, or the overview stays empty.
      const connection = connections.value.find((item) => item.id === connectionId);
      if (openDatabase && connection && loadsTablesPerDatabase(connection, connectionDescriptor(connection)) && scopeId === activeWorkspaceScopeId.value) {
        void loadTablesForDatabase(connectionId, openDatabase).catch(() => undefined);
      }
      return schema;
    } catch (cause) {
      if (requestGate.isCurrent(request)) {
        updateRuntimeForScope(scopeId, (runtime) => {
          if (runtime.sessionIds[connectionId] === sessionId) runtime.error = String(cause);
        });
      }
      throw cause;
    } finally {
      requestGate.finish(request);
    }
  }

  async function connect(id: string) {
    const connection = connections.value.find((item) => item.id === id);
    if (!connection) throw new Error("Database connection not found");
    if (!isDesktopRuntime()) throw new Error("Desktop runtime required");
    ensureDatabaseEventListeners();

    const scopeId = activeWorkspaceScopeId.value;
    if (scopeId === activeWorkspaceScopeId.value) error.value = "";
    let openedSessionId: string | undefined;
    try {
      if (runtimeForScope(scopeId).sessionIds[id]) await disconnectFromScope(scopeId, id);
      const connectStartedAt = performance.now();
      try {
        openedSessionId = await openSession(connection);
      } finally {
        if (import.meta.env.DEV) {
          performance.measure("mincode.database.connect", {
            start: connectStartedAt,
            end: performance.now(),
            detail: { type: connection.dbType },
          });
        }
      }
      const connectedAt = Date.now();
      connections.value = connections.value.map((item) =>
        item.id === id ? { ...item, lastConnectedAt: connectedAt, updatedAt: connectedAt } : item
      );

      updateRuntimeForScope(scopeId, (runtime) => {
        runtime.sessionIds = { ...runtime.sessionIds, [id]: openedSessionId! };
        delete runtime.schemas[id];
        runtime.activeConnectionId = id;
        runtime.selectedDatabase = "";
        runtime.error = "";
        clearRuntimeResult(runtime);
      });
      const sessionId = openedSessionId;
      openedSessionId = undefined;

      // 连接建立后立即发布会话；对象树元数据可能很大，后台完成后再增量写入，
      // 避免 information_schema 查询阻塞连接状态和工作区交互。
      void loadSchemaForConnection(scopeId, id, sessionId).catch(() => undefined);
      persistConnectionsAfterInteraction();
    } catch (cause) {
      if (openedSessionId) {
        await invokeBackend<void>("db_disconnect", { sessionId: openedSessionId }).catch(
          () => undefined
        );
      }
      updateRuntimeForScope(scopeId, (runtime) => {
        runtime.error = String(cause);
      });
      throw cause;
    }
  }

  async function disconnectFromScope(scopeId: string, id: string) {
    const sessionId = runtimeForScope(scopeId).sessionIds[id];
    if (sessionId && isDesktopRuntime()) {
      await invokeBackend<void>("db_disconnect", { sessionId }).catch(() => undefined);
    }
    if (sessionId) {
      metadataCache.invalidate({ sessionId });
      requestGate.invalidateSession(sessionId);
      invalidateDatabaseTableLoads(sessionId);
    }
    updateRuntimeForScope(scopeId, (runtime) => {
      removeConnectionFromRuntime(runtime, id);
      runtime.loading = false;
    });
  }

  async function disconnect(id: string) {
    await disconnectFromScope(activeWorkspaceScopeId.value, id);
  }

  function cachedMetadata<T>(
    sessionId: string,
    database: string,
    table: string,
    kind: DatabaseMetadataKind,
    command: BackendCommand
  ) {
    return metadataCache.get({ sessionId, database, table, kind }, () =>
      invokeBackend<T>(command, { sessionId, database, table })
    );
  }

  async function loadTableMetadata(sessionId: string, database: string, table: string) {
    const [columnsResult, keysResult] = await Promise.allSettled([
      cachedMetadata<DatabaseColumnInfo[]>(sessionId, database, table, "columns", "db_get_table_columns"),
      cachedMetadata<DatabasePrimaryKeyInfo>(sessionId, database, table, "primaryKeys", "db_get_primary_keys"),
    ]);
    const columns = columnsResult.status === "fulfilled" ? (columnsResult.value ?? []) : [];
    const keys = keysResult.status === "fulfilled" ? keysResult.value : undefined;
    return {
      columns,
      primaryKeys: keys?.columns?.length
        ? keys.columns
        : columns.filter((column) => column.isPrimaryKey).map((column) => column.name),
    };
  }

  async function refreshSchema() {
    const scopeId = activeWorkspaceScopeId.value;
    const runtime = runtimeForScope(scopeId);
    const connectionId = runtime.activeConnectionId;
    if (!connectionId) throw new Error("Database connection is not active");
    const sessionId = requireSessionForScope(scopeId, connectionId);
    metadataCache.invalidate({ sessionId });
    // The navigation schema lists databases without their tables for lazily loaded connections, so
    // every database whose tables were showing, and the one currently open, must be fetched again or
    // its object list stays empty.
    const connection = connections.value.find((item) => item.id === connectionId);
    const reloadDatabases = new Set(
      [...loadedDatabaseTables]
        .filter((key) => key.startsWith(`${sessionId}:`))
        .map((key) => key.slice(sessionId.length + 1))
    );
    if (connection && loadsTablesPerDatabase(connection, connectionDescriptor(connection)) && runtime.selectedDatabase) {
      reloadDatabases.add(runtime.selectedDatabase);
    }
    invalidateDatabaseTableLoads(sessionId);
    const schema = await loadSchemaForConnection(scopeId, connectionId, sessionId);
    const available = new Set(schema.databases.map((database) => database.name));
    await Promise.all(
      [...reloadDatabases]
        .filter((database) => available.has(database))
        .map((database) => loadTablesForDatabase(connectionId, database))
    );
    return schema;
  }

  function schemaSyncArgs(selection: DatabaseSchemaSyncSelection) {
    const sourceSessionId = sessionIds.value[selection.sourceConnectionId];
    const targetSessionId = sessionIds.value[selection.targetConnectionId];
    if (!sourceSessionId || !targetSessionId) {
      throw new Error("Database schema sync requires connected source and target sessions");
    }
    return {
      sourceSessionId,
      sourceDatabase: selection.sourceDatabase,
      targetSessionId,
      targetDatabase: selection.targetDatabase,
    };
  }

  async function compareSchema(selection: DatabaseSchemaSyncSelection) {
    if (!isDesktopRuntime()) throw new Error("Desktop runtime required");
    return invokeBackend<DatabaseSchemaComparison>("db_compare_schema", schemaSyncArgs(selection));
  }

  async function generateSchemaSync(
    selection: DatabaseSchemaSyncSelection,
    sourceFingerprint: string,
    targetFingerprint: string,
    selectedChangeIds: string[]
  ) {
    if (!isDesktopRuntime()) throw new Error("Desktop runtime required");
    return invokeBackend<DatabaseSchemaSyncPlan>("db_generate_schema_sync", {
      ...schemaSyncArgs(selection),
      sourceFingerprint,
      targetFingerprint,
      selectedChangeIds,
    });
  }

  /** 经 SQL 安全网执行结构同步；用户取消确认时返回 undefined（未执行）。 */
  async function executeSchemaSync(
    selection: DatabaseSchemaSyncSelection,
    sourceFingerprint: string,
    targetFingerprint: string,
    selectedChangeIds: string[],
    origin: SqlSafetyOrigin,
  ) {
    if (!isDesktopRuntime()) throw new Error("Desktop runtime required");
    const args = schemaSyncArgs(selection);
    const result = await runWithSafety({
      sessionId: args.targetSessionId,
      database: args.targetDatabase,
      operation: {
        kind: "schemaSync",
        sourceSessionId: args.sourceSessionId,
        sourceDatabase: args.sourceDatabase,
        sourceFingerprint,
        targetFingerprint,
        selectedChangeIds,
      },
      source: origin === "ai" ? "ai" : "designer",
      origin,
      execute: (token) => invokeBackend<DatabaseSchemaSyncExecutionResult>("db_execute_schema_sync", {
        ...args,
        sourceFingerprint,
        targetFingerprint,
        selectedChangeIds,
        ...sqlSafetyArgs(token),
      }),
    });
    if (result) metadataCache.invalidate({ sessionId: args.targetSessionId });
    return result;
  }

  async function getTableDetails(database = selectedDatabase.value, table = selectedTable.value) {
    if (!database || !table) throw new Error("请先选择数据表");
    const scopeId = activeWorkspaceScopeId.value;
    const runtime = runtimeForScope(scopeId);
    const sessionId = requireSessionForScope(scopeId);
    // Field data is the primary content of the table-details dialog. Keep it
    // independent from DDL/index/trigger introspection so a slow auxiliary
    // metadata query cannot leave the entire dialog covered by a loader.
    const [columnsResult, keysResult] = await Promise.allSettled([
      cachedMetadata<DatabaseColumnInfo[]>(sessionId, database, table, "columns", "db_get_table_columns"),
      cachedMetadata<DatabasePrimaryKeyInfo>(sessionId, database, table, "primaryKeys", "db_get_primary_keys"),
    ]);
    const loadErrors = (
      [
        ["字段", columnsResult],
        ["主键", keysResult],
      ] as Array<[string, PromiseSettledResult<unknown>]>
    ).flatMap(([label, result]) =>
      result.status === "rejected" ? [`${label}：${describeBackendError(result.reason)}`] : []
    );
    const columns =
      columnsResult.status === "fulfilled" && columnsResult.value?.length
        ? columnsResult.value
        : [...runtime.tableColumns];
    const primaryKeyInfo = keysResult.status === "fulfilled" ? keysResult.value : undefined;
    const resolvedPrimaryKeys = resolvePrimaryKeyColumns(
      primaryKeyInfo?.columns ?? [],
      columns,
      [...runtime.primaryKeys]
    );
    const details: DatabaseTableDetails = {
      ddl: "",
      columns,
      primaryKeys: resolvedPrimaryKeys,
      primaryKeyName: primaryKeyInfo?.constraintName,
      indexes: [],
      foreignKeys: [],
      triggers: [],
      loadErrors,
    };
    updateRuntimeForScope(scopeId, (current) => {
      current.tableColumns = details.columns;
      current.primaryKeys = details.primaryKeys;
    });
    return details;
  }

  async function getTableDetailsSupplement(
    database = selectedDatabase.value,
    table = selectedTable.value
  ): Promise<Pick<DatabaseTableDetails, "ddl" | "indexes" | "foreignKeys" | "triggers" | "loadErrors">> {
    if (!database || !table) throw new Error("请先选择数据表");
    // 后端仅对部分会话类型/方言实现了 DDL、索引、外键、触发器内省；
    // 对不支持的类型直接跳过调用，避免把“该会话类型不支持…”当作加载失败展示。
    const descriptor = connectionDescriptor(activeConnection.value);
    const capabilities = tableStructureCapabilities(descriptor?.dialectType, descriptor);
    const sessionId = requireActiveSession();
    const [ddlResult, indexesResult, foreignKeysResult, triggersResult] = await Promise.allSettled([
      capabilities.ddl
        ? cachedMetadata<string>(sessionId, database, table, "ddl", "db_get_table_ddl")
        : Promise.resolve(""),
      capabilities.indexes
        ? cachedMetadata<DatabaseTableDetails["indexes"]>(sessionId, database, table, "indexes", "db_list_indexes")
        : Promise.resolve([]),
      capabilities.foreignKeys
        ? cachedMetadata<DatabaseTableDetails["foreignKeys"]>(sessionId, database, table, "foreignKeys", "db_list_foreign_keys")
        : Promise.resolve([]),
      capabilities.triggers
        ? cachedMetadata<DatabaseTableDetails["triggers"]>(sessionId, database, table, "triggers", "db_list_triggers")
        : Promise.resolve([]),
    ]);
    const loadErrors = (
      [
        ["DDL", ddlResult, capabilities.ddl],
        ["索引", indexesResult, capabilities.indexes],
        ["外键", foreignKeysResult, capabilities.foreignKeys],
        ["触发器", triggersResult, capabilities.triggers],
      ] as Array<[string, PromiseSettledResult<unknown>, boolean]>
    ).flatMap(([label, result, supported]) =>
      supported && result.status === "rejected"
        ? [`${label}：${describeBackendError(result.reason)}`]
        : []
    );
    return {
      ddl: ddlResult.status === "fulfilled" ? ddlResult.value : `-- ${describeBackendError(ddlResult.reason)}`,
      indexes: indexesResult.status === "fulfilled" ? (indexesResult.value ?? []) : [],
      foreignKeys: foreignKeysResult.status === "fulfilled" ? (foreignKeysResult.value ?? []) : [],
      triggers: triggersResult.status === "fulfilled" ? (triggersResult.value ?? []) : [],
      loadErrors,
    };
  }

  async function getDatabaseDiagram(database = selectedDatabase.value, maxTables = 80): Promise<DatabaseDiagram> {
    if (!database) throw new Error("请先选择数据库");
    const schemaDatabase = activeSchema.value?.databases.find((item) => item.name === database);
    if (!schemaDatabase) throw new Error("找不到当前数据库结构");

    const tables = schemaDatabase.tables.filter((item) => {
      const kind = item.kind.trim().toLowerCase();
      return kind === "table" || kind.includes("table");
    });
    const selectedTables = tables.slice(0, Math.max(1, maxTables));
    const sessionId = requireActiveSession();
    const diagramTables: DatabaseDiagramTable[] = [];
    const concurrency = 5;

    for (let index = 0; index < selectedTables.length; index += concurrency) {
      const batch = selectedTables.slice(index, index + concurrency);
      const results = await Promise.all(
        batch.map(async ({ name }) => {
          const [columnsResult, foreignKeysResult] = await Promise.allSettled([
            cachedMetadata<DatabaseColumnInfo[]>(
              sessionId,
              database,
              name,
              "columns",
              "db_get_table_columns"
            ),
            cachedMetadata<DatabaseTableDetails["foreignKeys"]>(
              sessionId,
              database,
              name,
              "foreignKeys",
              "db_list_foreign_keys"
            ),
          ]);
          return {
            name,
            columns: columnsResult.status === "fulfilled" ? (columnsResult.value ?? []) : [],
            foreignKeys:
              foreignKeysResult.status === "fulfilled" ? (foreignKeysResult.value ?? []) : [],
          } satisfies DatabaseDiagramTable;
        })
      );
      diagramTables.push(...results);
    }

    return {
      database,
      tables: diagramTables,
      skippedTables: Math.max(0, tables.length - selectedTables.length),
    };
  }

  async function getDriverStatus() {
    const [drivers, environment] = await Promise.all([
      invokeBackend<OdbcDriverStatus[]>("db_check_odbc_drivers"),
      invokeBackend<DatabaseInstallEnvironment>("db_detect_install_env"),
    ]);
    return { drivers: drivers ?? [], environment };
  }

  function activateTableContext(database: string, table: string) {
    const scopeId = activeWorkspaceScopeId.value;
    const sessionId = requireSessionForScope(scopeId);
    const target = `table:${database}\u0000${table}`;
    const request = requestGate.begin(scopeId, sessionId, target);
    updateRuntimeForScope(scopeId, (current) => {
      current.selectedDatabase = database;
      current.selectedTable = table;
      current.tableColumns = [];
      current.primaryKeys = [];
    });
    void loadTableMetadata(sessionId, database, table)
      .then((metadata) => {
        if (!requestGate.isCurrent(request)) return;
        updateRuntimeForScope(scopeId, (current) => {
          if (current.sessionIds[current.activeConnectionId ?? ""] !== sessionId) return;
          current.tableColumns = metadata.columns;
          current.primaryKeys = metadata.primaryKeys;
        });
      })
      .catch(() => undefined);
  }

  async function openTableForScope(
    scopeId: string,
    database: string,
    table: string,
    requestedPage = 1,
    requestedPageSize = 100,
    options: DatabaseTablePageOptions = {}
  ) {
    const sessionId = requireSessionForScope(scopeId);
    const target = `table:${database}\u0000${table}`;
    const request = requestGate.begin(scopeId, sessionId, target);
    updateRuntimeForScope(scopeId, (runtime) => {
      runtime.loading = true;
      runtime.error = "";
      runtime.tableColumns = [];
      runtime.primaryKeys = [];
    });
    try {
      const safePage = Math.max(1, requestedPage);
      const safePageSize = Math.max(10, requestedPageSize);
      // 字段和主键用于编辑增强，不应阻塞首屏数据。慢元数据完成后仅更新仍处于
      // 当前请求代次的表，避免快速切表时旧响应覆盖新表状态。
      void loadTableMetadata(sessionId, database, table)
        .then((metadata) => {
          if (!requestGate.isCurrent(request)) return;
          updateRuntimeForScope(scopeId, (runtime) => {
            if (runtime.sessionIds[runtime.activeConnectionId ?? ""] !== sessionId) return;
            runtime.tableColumns = metadata.columns;
            runtime.primaryKeys = metadata.primaryKeys;
          });
        })
        .catch(() => undefined);
      const tableResult = await invokeBackend<DatabaseQueryResult>("db_get_table_rows", {
        sessionId,
        database,
        table,
        limit: safePageSize,
        offset: (safePage - 1) * safePageSize,
        pageToken: options.pageToken,
        namespace: options.namespace,
      });
      if (!requestGate.isCurrent(request)) return tableResult;
      updateRuntimeForScope(scopeId, (runtime) => {
        if (runtime.sessionIds[runtime.activeConnectionId ?? ""] !== sessionId) return;
        runtime.result = tableResult;
        runtime.selectedDatabase = database;
        runtime.selectedTable = table;
        runtime.page = safePage;
        runtime.pageSize = safePageSize;
      });
      return tableResult;
    } catch (cause) {
      if (requestGate.isCurrent(request)) {
        updateRuntimeForScope(scopeId, (runtime) => {
          runtime.error = String(cause);
        });
      }
      throw cause;
    } finally {
      if (requestGate.finish(request)) {
        updateRuntimeForScope(scopeId, (runtime) => {
          runtime.loading = false;
        });
      }
    }
  }

  async function openTable(
    database: string,
    table: string,
    requestedPage = 1,
    requestedPageSize = pageSize.value,
    options: DatabaseTablePageOptions = {}
  ) {
    return openTableForScope(
      activeWorkspaceScopeId.value,
      database,
      table,
      requestedPage,
      requestedPageSize,
      options
    );
  }

  async function executeQueryForConnection(
    connectionId: string,
    query: string,
    database = selectedDatabase.value || undefined,
    options: DatabaseQueryExecutionOptions = {},
  ) {
    if (queryRunning.value) throw new Error("Another database query is already running");
    const scopeId = activeWorkspaceScopeId.value;
    const sessionId = requireSessionForScope(scopeId, connectionId);
    const changesStructure = /^(?:CREATE|ALTER|DROP|TRUNCATE|RENAME|COMMENT)\b/i.test(query.trim());
    const queryId = crypto.randomUUID();
    const queryConnection = connections.value.find((item) => item.id === connectionId);
    const connectionType = queryConnection?.dbType;
    const canStream = Boolean(
      connectionType
      && STREAMING_QUERY_DB_TYPES.has(connectionType)
      && /^(?:SELECT|SHOW|WITH|EXPLAIN|DESC|DESCRIBE|PRAGMA|VALUES|TABLE|CALL)\b/i.test(query.trim()),
    );
    ensureDatabaseEventListeners();
    const stream = canStream ? createQueryResultStream(queryId) : undefined;
    const progress = stream && options.onProgress
      ? createQueryResultProgressBatcher(stream, options.onProgress)
      : undefined;
    if (stream) queryStreams.set(queryId, { stream, progress });
    updateRuntimeForScope(scopeId, (runtime) => {
      runtime.currentQueryId = queryId;
      runtime.queryRunning = true;
      runtime.error = "";
    });
    try {
      // SQL editor tabs own their result snapshots in the view. Keep the table
      // result untouched so switching back to a table does not show arbitrary SQL.
      if (stream && queryChunksReady && !(await queryChunksReady)) {
        stopQueryChunks?.();
        stopQueryChunks = undefined;
        queryChunksReady = undefined;
        ensureDatabaseEventListeners();
        if (!queryChunksReady || !(await queryChunksReady)) {
          throw new Error("Database Stream subscription failed");
        }
      }
      const source = options.safety?.source ?? "query";
      // 用户取消安全确认时为 undefined（未执行）。
      const queryResult = await runWithSafety({
        sessionId,
        query,
        database,
        source,
        origin: options.safety?.origin ?? "user",
        // 先不带令牌执行：只读语句没有额外的检查往返；写语句由后端答复 checkRequired 后再检查确认。
        executeFirst: true,
        execute: async (token) => {
          // 只有未输出任何分块的失败才会被要求确认后重跑；已有输出的流绝不重跑。
          if (token.checkId && stream && stream.receivedChunks() > 0) {
            throw new Error("Query result stream already received output; the statement was not re-run");
          }
          const args = { sessionId, query, database, queryId, source, ...sqlSafetyArgs(token) };
          return stream
            ? stream.finish(await invokeBackend<DatabaseQueryStreamSummary>("db_execute_query_stream", args))
            : invokeBackend<DatabaseQueryResult>("db_execute_query", args);
        },
      });
      if (changesStructure && queryResult) metadataCache.invalidate({ sessionId });
      return queryResult;
    } catch (cause) {
      updateRuntimeForScope(scopeId, (runtime) => {
        runtime.error = String(cause);
      });
      throw cause;
    } finally {
      progress?.cancel();
      stream?.release();
      queryStreams.delete(queryId);
      updateRuntimeForScope(scopeId, (runtime) => {
        if (runtime.currentQueryId === queryId) runtime.currentQueryId = "";
        runtime.queryRunning = false;
      });
    }
  }

  async function executeQuery(
    query: string,
    database = selectedDatabase.value || undefined,
    options: DatabaseQueryExecutionOptions = {},
  ) {
    const connectionId = activeConnectionId.value;
    if (!connectionId) throw new Error("Database connection is not active");
    return executeQueryForConnection(connectionId, query, database, options);
  }

  async function cancelQuery() {
    if (!currentQueryId.value) return false;
    return invokeBackend<boolean>("db_cancel_query", { queryId: currentQueryId.value });
  }

  /** Runs a read-only table query without changing the active SQL-editor tab. */
  async function executeTableQuery(query: string, database?: string) {
    const scopeId = activeWorkspaceScopeId.value;
    const tableResult = await executeQuery(query, database);
    if (tableResult) {
      updateRuntimeForScope(scopeId, (runtime) => {
        runtime.result = tableResult;
      });
    }
    return tableResult;
  }

  function closeTable() {
    selectedTable.value = "";
    result.value = undefined;
    tableColumns.value = [];
    primaryKeys.value = [];
    page.value = 1;
  }

  /**
   * 经 SQL 安全网一次提交表格暂存修改（`db_apply_changes`，operation kind rows）。用户取消确认时
   * 返回 undefined（未执行）。后端答复成功后先调用 onCommitted（调用方清空暂存），再刷新当前页；
   * 刷新失败时抛出，但修改已生效。非事务库部分成功后也刷新（刷新错误忽略，抛出原错误）。
   */
  async function applyRowChanges(
    changes: RowChange[],
    options: DatabaseRowMutationOptions & { confirm?: SqlSafetyConfirm; onCommitted?: () => void } = {}
  ) {
    const scopeId = activeWorkspaceScopeId.value;
    const runtime = runtimeForScope(scopeId);
    const sessionId = requireSessionForScope(scopeId);
    const database = runtime.selectedDatabase;
    const table = runtime.selectedTable;
    const refresh = async () => {
      metadataCache.invalidate({ sessionId, database, table });
      await openTableForScope(scopeId, database, table, runtime.page, runtime.pageSize, {
        namespace: options.namespace,
      });
    };
    let result: { applied: number; recordId?: string } | undefined;
    try {
      result = await runWithSafety({
        sessionId,
        database,
        operation: { kind: "rows", table, changes, namespace: options.namespace },
        source: "grid",
        origin: "user",
        confirm: options.confirm,
        execute: (token) => invokeBackend<{ applied: number; recordId?: string }>("db_apply_changes", {
          sessionId,
          database,
          table,
          changes,
          namespace: options.namespace,
          source: "grid",
          ...sqlSafetyArgs(token),
        }),
      });
    } catch (cause) {
      if (cause instanceof SqlSafetyPartialError && cause.applied > 0) await refresh().catch(() => undefined);
      throw cause;
    }
    if (result) {
      options.onCommitted?.();
      await refresh();
    }
    return result;
  }

  async function exportTable(format: TableDataFormat, mode?: InsertSqlMode, limit = 10000) {
    const scopeId = activeWorkspaceScopeId.value;
    const runtime = runtimeForScope(scopeId);
    return invokeBackend<string>("db_export_table", {
      sessionId: requireSessionForScope(scopeId),
      database: runtime.selectedDatabase,
      table: runtime.selectedTable,
      format,
      mode,
      limit,
    });
  }

  /** 表数据在别处被改写后（如撤回）丢弃该表的元数据缓存；连接未打开会话时无事可做。 */
  function invalidateTableMetadata(connectionId: string, database: string, table: string) {
    const sessionId = sessionIds.value[connectionId];
    if (sessionId) metadataCache.invalidate({ sessionId, database, table });
  }

  async function importData(format: "csv" | "json", data: string) {
    const scopeId = activeWorkspaceScopeId.value;
    const runtime = runtimeForScope(scopeId);
    const sessionId = requireSessionForScope(scopeId);
    const affected = await importInlineTableData(sessionId, runtime.selectedDatabase, runtime.selectedTable, format, data);
    if (affected === undefined) return undefined;
    metadataCache.invalidate({ sessionId, database: runtime.selectedDatabase, table: runtime.selectedTable });
    await openTableForScope(
      scopeId,
      runtime.selectedDatabase,
      runtime.selectedTable,
      runtime.page,
      runtime.pageSize
    );
    return affected;
  }

  function nextRedisRequestSequence(sequences: Map<string, number>, scopeId: string) {
    const sequence = (sequences.get(scopeId) ?? 0) + 1;
    sequences.set(scopeId, sequence);
    return sequence;
  }

  function invalidateRedisRequests(scopeId: string) {
    nextRedisRequestSequence(redisListRequestSequences, scopeId);
    nextRedisRequestSequence(redisKeyRequestSequences, scopeId);
  }

  async function listRedisKeysForScope(scopeId: string, reset = false) {
    const requestId = nextRedisRequestSequence(redisListRequestSequences, scopeId);
    const runtime = runtimeForScope(scopeId);
    const sessionId = requireSessionForScope(scopeId);
    const dbIndex = runtime.redisDbIndex;

    // Redis SCAN only does an exact match unless the caller supplies glob tokens.
    // Treat ordinary text as a contains search, while preserving explicit Redis
    // glob patterns such as `user:*` and `session?`.
    const inputPattern = runtime.redisPattern.trim();
    const pattern =
      !inputPattern || inputPattern === "*"
        ? "*"
        : /[*?[]/.test(inputPattern)
          ? inputPattern
          : `*${inputPattern}*`;
    const response = await invokeBackend<RedisKeyListResult>("redis_list_keys", {
      sessionId,
      dbIndex,
      pattern,
      cursor: reset ? "0" : runtime.redisCursor,
      count: 100,
    });

    if (redisListRequestSequences.get(scopeId) !== requestId) return;
    updateRuntimeForScope(scopeId, (current) => {
      if (current.sessionIds[current.activeConnectionId ?? ""] !== sessionId ||
          current.redisDbIndex !== dbIndex || current.redisPattern.trim() !== inputPattern) return;
      if (reset) {
        current.redisKeys = response;
      } else {
        const knownKeys = new Set(current.redisKeys.keys.map((item) => item.key));
        current.redisKeys = {
          ...response,
          keys: [
            ...current.redisKeys.keys,
            ...response.keys.filter((item) => !knownKeys.has(item.key)),
          ],
        };
      }
      current.redisCursor = response.nextCursor;
    });
  }

  async function listRedisKeys(reset = false) {
    return listRedisKeysForScope(activeWorkspaceScopeId.value, reset);
  }

  async function getRedisKeyForScope(scopeId: string, key: string) {
    const requestId = nextRedisRequestSequence(redisKeyRequestSequences, scopeId);
    const runtime = runtimeForScope(scopeId);
    const dbIndex = runtime.redisDbIndex;
    const sessionId = requireSessionForScope(scopeId);
    updateRuntimeForScope(scopeId, (current) => {
      current.selectedRedisKey = key;
      current.redisKeyInfo = undefined;
    });
    const keyInfo = await invokeBackend<RedisKeyInfo>("redis_get_key", {
      sessionId,
      dbIndex,
      key,
    });
    if (redisKeyRequestSequences.get(scopeId) !== requestId) return;
    updateRuntimeForScope(scopeId, (current) => {
      if (current.sessionIds[current.activeConnectionId ?? ""] === sessionId &&
          current.redisDbIndex === dbIndex && current.selectedRedisKey === key) {
        current.redisKeyInfo = keyInfo;
      }
    });
  }

  async function getRedisKey(key: string) {
    return getRedisKeyForScope(activeWorkspaceScopeId.value, key);
  }

  async function selectRedisDatabase(dbIndex: number) {
    if (!Number.isInteger(dbIndex) || dbIndex < 0) {
      throw new Error("Redis database index must be a non-negative integer");
    }

    const scopeId = activeWorkspaceScopeId.value;
    invalidateRedisRequests(scopeId);
    updateRuntimeForScope(scopeId, (current) => {
      current.redisDbIndex = dbIndex;
      current.redisCursor = "0";
      current.redisKeys = { keys: [], nextCursor: "0", total: 0 };
      current.selectedRedisKey = "";
      current.redisKeyInfo = undefined;
    });
    await listRedisKeysForScope(scopeId, true);
  }

  /**
   * Redis 单键写入（set / add / delete / ttl）经 SQL 安全网取令牌。面板已有编辑保存 / 删除确认，
   * 低风险不再弹框（preconfirmed）；插件驱动的 Redis 会话后端不要求令牌。返回 false 表示用户取消。
   */
  async function writeRedisKey(
    scopeId: string,
    command: "redis_set_key" | "redis_add_key" | "redis_delete_key" | "redis_set_ttl",
    key: string,
    extra: Record<string, unknown> = {},
  ) {
    const runtime = runtimeForScope(scopeId);
    const sessionId = requireSessionForScope(scopeId);
    const dbIndex = runtime.redisDbIndex;
    const execute = (token: SqlSafetyToken) =>
      invokeBackend(command, { sessionId, dbIndex, key, ...extra, ...sqlSafetyArgs(token) }).then(() => true);
    const action = { redis_set_key: "set", redis_add_key: "add", redis_delete_key: "delete", redis_set_ttl: "ttl" }[command];
    return (await runWithSafety({
      sessionId,
      operation: { kind: "redis", action, dbIndex, keys: [key] },
      source: "grid",
      origin: "preconfirmed",
      execute,
    })) ?? false;
  }

  async function saveRedisKey(key: string, keyType: string, value: unknown, ttl: number) {
    const scopeId = activeWorkspaceScopeId.value;
    if (!(await writeRedisKey(scopeId, "redis_set_key", key, { keyType, value }))) return;
    await writeRedisKey(scopeId, "redis_set_ttl", key, { ttlSeconds: ttl });
    await listRedisKeysForScope(scopeId, true);
    await getRedisKeyForScope(scopeId, key);
  }

  async function setRedisKeyTtl(key: string, ttl: number) {
    const scopeId = activeWorkspaceScopeId.value;
    if (!(await writeRedisKey(scopeId, "redis_set_ttl", key, { ttlSeconds: ttl }))) return;
    await listRedisKeysForScope(scopeId, true);
    await getRedisKeyForScope(scopeId, key);
  }

  async function addRedisKey(key: string, keyType: string, value: unknown, ttl: number) {
    const scopeId = activeWorkspaceScopeId.value;
    if (!(await writeRedisKey(scopeId, "redis_add_key", key, { keyType, value }))) return;
    if (ttl !== -1) {
      await writeRedisKey(scopeId, "redis_set_ttl", key, { ttlSeconds: ttl });
    }
    await listRedisKeysForScope(scopeId, true);
    await getRedisKeyForScope(scopeId, key);
  }

  async function deleteRedisKey(key: string) {
    const scopeId = activeWorkspaceScopeId.value;
    if (!(await writeRedisKey(scopeId, "redis_delete_key", key))) return;
    updateRuntimeForScope(scopeId, (current) => {
      current.selectedRedisKey = "";
      current.redisKeyInfo = undefined;
    });
    await listRedisKeysForScope(scopeId, true);
  }

  function activateConnection(id: string) {
    if (!sessionIds.value[id]) return;
    activeConnectionId.value = id;
    selectedDatabase.value = schemas.value[id]?.databases[0]?.name ?? "";
    clearWorkspaceResult();
  }

  return {
    initialized,
    connectionsStatus,
    connectionDescriptor,
    connectionCapabilities,
    connectionDialectType,
    importConnectionsFromBackend,
    activeWorkspaceScopeId,
    connections,
    connectionGroupOrder,
    activeConnectionId,
    sessionIds,
    schemas,
    selectedDatabase,
    selectedTable,
    result,
    tableColumns,
    primaryKeys,
    page,
    pageSize,
    currentQueryId,
    queryRunning,
    loading,
    error,
    redisDbIndex,
    redisPattern,
    redisCursor,
    redisKeys,
    selectedRedisKey,
    redisKeyInfo,
    desktopRuntime,
    activeConnection,
    activeSessionId,
    activeSchema,
    activateTableContext,
    loadTableMetadata,
    loadTablesForDatabase,
    connectedConnections,
    isRedis,
    initialize,
    switchWorkspaceScope,
    discardWorkspaceScope,
    saveConnectionGroupOrder,
    replaceConnectionGroupOrderName,
    saveConnection,
    deleteConnection,
    testConnection,
    connect,
    disconnect,
    refreshSchema,
    compareSchema,
    generateSchemaSync,
    executeSchemaSync,
    getTableDetails,
    getTableDetailsSupplement,
    getDatabaseDiagram,
    getDriverStatus,
    openTable,
    executeQuery,
    executeQueryForConnection,
    executeTableQuery,
    cancelQuery,
    closeTable,
    applyRowChanges,
    exportTable,
    importData,
    invalidateTableMetadata,
    listRedisKeys,
    getRedisKey,
    selectRedisDatabase,
    saveRedisKey,
    setRedisKeyTtl,
    addRedisKey,
    deleteRedisKey,
    activateConnection,
  };
});

// Pinia store 实例会被缓存：模块热更新后 useStore 仍返回旧实例，
// 闭包里握着的还是旧函数，于是「代码已更新但行为没变」。
// 参见 https://pinia.vuejs.org/cookbook/hot-module-replacement.html
if (import.meta.hot) {
  import.meta.hot.accept(acceptHMRUpdate(useDatabaseWorkspaceStore, import.meta.hot));
}

import { nextTick, onActivated, onDeactivated, type Ref } from "vue";
import type { DatabaseConnection, DatabaseSchemaChange } from "@/domain/database/types";
import type { QueryTab } from "@/domain/database/query-tab-snapshot";
import { defaultSchemaSyncSelection } from "@/domain/database/schema-sync";
import {
  registerAiWorkspaceActionHandler,
  requestAiWorkspaceAction,
} from "@/services/ai-workspace-actions";
import { useDatabaseWorkspaceStore } from "@/stores/database-workspace";
import type { TableTab } from "./useGridState";
import { useVueI18n } from "@/i18n/messages";

function aiActionString(input: Record<string, unknown>, key: string, action: string) {
  const value = input[key];
  if (typeof value !== "string" || !value.trim()) {
    throw new Error(`Database AI action ${action} requires ${key}`);
  }
  return value.trim();
}

/**
 * 数据库工作区的 AI 工作区动作注册：以注入的视图动作
 * （连接激活/表打开/查询 tab/连接对话框等）执行 AI 侧请求，
 * 并在组件激活/停用之间维护 handler 注册态。
 */
export function useAiDatabaseActions(options: {
  queryTabs: Ref<QueryTab[]>;
  activeQueryTabId: Ref<string>;
  tableTabs: Ref<TableTab[]>;
  activeTableTabId: Ref<string | undefined>;
  selectedConnectionTab: Ref<string>;
  activateConnection: (connection: DatabaseConnection, waitForPaint?: boolean) => Promise<void>;
  selectConnectionTable: (connection: DatabaseConnection, database: string, table: string) => Promise<void>;
  newQuery: (sql?: string, name?: string) => void;
  closeQueryTab: (tabId: string) => void;
  closeTableTab: (tabId: string) => void;
  /** 表标签上有用户暂存、尚未提交的表格修改（AI 不能替用户丢弃）。 */
  tableTabHasPendingChanges: (tabId: string) => boolean;
  openNewConnection: () => void;
  editConnection: (connection: DatabaseConnection) => void;
  connectionDialogVisible: Ref<boolean>;
  duplicateConnection: (connection: DatabaseConnection) => Promise<void>;
  toggleConnectionFavorite: (connection: DatabaseConnection) => Promise<void>;
  openTableDetails: (database: string, table: string) => Promise<void>;
  detailsVisible: Ref<boolean>;
  transferConnectionGroupCollapse: (previousName: string, nextName: string) => void;
}) {
  const databaseStore = useDatabaseWorkspaceStore();
  const { t } = useVueI18n();

  function aiConnection(
    input: Record<string, unknown>,
    action: string,
    expectsRedis?: boolean,
    key = "connectionId"
  ) {
    const connectionId = aiActionString(input, key, action);
    const connection = databaseStore.connections.find((item) => item.id === connectionId);
    if (!connection) throw new Error(`Database AI action ${action} could not find connection: ${connectionId}`);
    if (expectsRedis !== undefined && (connection.dbType === "redis") !== expectsRedis) {
      throw new Error(`Database AI action ${action} received an incompatible connection type`);
    }
    return connection;
  }

  function safeConnectionForAi(connection: DatabaseConnection) {
    return {
      id: connection.id,
      name: connection.name,
      dbType: connection.dbType,
      host: connection.host,
      port: connection.port,
      database: connection.database,
      group: connection.group,
      tags: connection.tags ?? [],
      favorite: Boolean(connection.favorite),
      connected: Boolean(databaseStore.sessionIds[connection.id]),
    };
  }

  /** 结构同步要两端都在线，AI 拿到的只是连接 id，得自己先把会话建起来。 */
  async function connectedSchemaSyncSelection(input: Record<string, unknown>, action: string) {
    const source = aiConnection(input, action, false, "sourceConnectionId");
    const target = aiConnection(input, action, false, "targetConnectionId");
    if (source.id === target.id) {
      throw new Error(`Database AI action ${action} requires two different connections`);
    }
    if (source.driver || target.driver) {
      throw new Error(`unsupported: Database AI action ${action} is not available for plugin database drivers`);
    }
    for (const connection of [source, target]) {
      if (!databaseStore.sessionIds[connection.id]) await databaseStore.connect(connection.id);
      if (!databaseStore.sessionIds[connection.id]) {
        throw new Error(`Database AI action ${action} could not connect to: ${connection.name}`);
      }
    }
    return {
      sourceConnectionId: source.id,
      sourceDatabase: aiActionString(input, "sourceDatabase", action),
      targetConnectionId: target.id,
      targetDatabase: aiActionString(input, "targetDatabase", action),
    };
  }

  function schemaSyncChangeIds(
    input: Record<string, unknown>,
    action: string,
    changes: DatabaseSchemaChange[]
  ) {
    if (input.changeIds === undefined) return defaultSchemaSyncSelection(changes);
    if (!Array.isArray(input.changeIds) || input.changeIds.some((id) => typeof id !== "string")) {
      throw new Error(`Database AI action ${action} requires changeIds to be strings`);
    }
    const requested = input.changeIds.map((id) => String(id));
    const available = new Set(changes.map((change) => change.id));
    const unknown = requested.filter((id) => !available.has(id));
    if (unknown.length) {
      throw new Error(`Database AI action ${action} received unknown change ids: ${unknown.join(", ")}`);
    }
    return requested;
  }

  async function handleAiDatabaseWorkspaceAction(input: Record<string, unknown>, signal?: AbortSignal) {
    const action = aiActionString(input, "action", "request");
    switch (action) {
      case "active-resource":
        return databaseStore.activeConnectionId === options.selectedConnectionTab.value
          ? databaseStore.activeConnectionId : null;
      case "open-resource": {
        await databaseStore.initialize();
        signal?.throwIfAborted();
        const id = String(input.id ?? "");
        const connection = databaseStore.connections.find((item) => item.id === id);
        if (!connection) throw new Error(t("ai.navigation.resourceMissing", { id }));
        await options.activateConnection(connection, false);
        if (options.selectedConnectionTab.value !== id || !databaseStore.sessionIds[id]) {
          throw new Error(t("ai.navigation.resourceUnavailable", { id }));
        }
        return { id };
      }
      case "list-workspace":
        return {
          connections: databaseStore.connections
            .filter((connection) => connection.dbType !== "redis")
            .map(safeConnectionForAi),
          connectionGroupOrder: databaseStore.connectionGroupOrder,
          queryTabs: options.queryTabs.value.map((tab) => ({
            id: tab.id,
            name: tab.name,
            status: tab.status,
            active: tab.id === options.activeQueryTabId.value,
          })),
          tableTabs: options.tableTabs.value.map((tab) => ({
            ...tab,
            active: tab.id === options.activeTableTabId.value,
          })),
        };
      case "open-connection": {
        const connection = aiConnection(input, action, false);
        await options.activateConnection(connection);
        if (databaseStore.activeConnectionId !== connection.id || !databaseStore.sessionIds[connection.id]) {
          throw new Error(`Database AI action open-connection could not connect to: ${connection.name}`);
        }
        return { connection: safeConnectionForAi(connection), active: true };
      }
      case "open-table": {
        const connection = aiConnection(input, action, false);
        await options.selectConnectionTable(
          connection,
          aiActionString(input, "database", action),
          aiActionString(input, "table", action)
        );
        if (databaseStore.activeConnectionId !== connection.id || !databaseStore.sessionIds[connection.id]) {
          throw new Error(`Database AI action open-table could not connect to: ${connection.name}`);
        }
        const tab = options.tableTabs.value.find((item) => item.id === options.activeTableTabId.value);
        return tab ? { ...tab, active: true } : { opened: false };
      }
      case "new-query": {
        const sql = typeof input.sql === "string" ? input.sql : "";
        const name = typeof input.name === "string" ? input.name : undefined;
        options.newQuery(sql, name);
        const tab = options.queryTabs.value.find((item) => item.id === options.activeQueryTabId.value)
          ?? options.queryTabs.value[0];
        return tab ? { id: tab.id, name: tab.name, active: true } : { created: false };
      }
      case "close-query": {
        const tabId = typeof input.tabId === "string" && input.tabId ? input.tabId : options.activeQueryTabId.value;
        if (!tabId || !options.queryTabs.value.some((tab) => tab.id === tabId)) {
          throw new Error("Database AI action close-query could not find the SQL tab");
        }
        options.closeQueryTab(tabId);
        return { closed: tabId };
      }
      case "close-table": {
        const tabId = typeof input.tabId === "string" && input.tabId ? input.tabId : options.activeTableTabId.value;
        if (!tabId || !options.tableTabs.value.some((tab) => tab.id === tabId)) {
          throw new Error("Database AI action close-table could not find the table tab");
        }
        if (options.tableTabHasPendingChanges(tabId)) {
          throw new Error(
            "Database AI action close-table refused: the table tab has unsubmitted grid changes. Ask the user to submit or discard them first"
          );
        }
        options.closeTableTab(tabId);
        return { closed: tabId };
      }
      case "new-connection-dialog":
        options.openNewConnection();
        return { opened: true };
      case "edit-connection-dialog": {
        const connection = aiConnection(input, action);
        options.editConnection(connection);
        return { opened: options.connectionDialogVisible.value, connectionId: connection.id };
      }
      case "duplicate-connection": {
        const connection = aiConnection(input, action);
        const connectionIds = new Set(databaseStore.connections.map((item) => item.id));
        await options.duplicateConnection(connection);
        const duplicate = databaseStore.connections.find((item) => !connectionIds.has(item.id));
        return duplicate ? { connection: safeConnectionForAi(duplicate) } : { duplicated: false };
      }
      case "toggle-favorite": {
        const connection = aiConnection(input, action);
        await options.toggleConnectionFavorite(connection);
        const updated = databaseStore.connections.find((item) => item.id === connection.id) ?? connection;
        return { connection: safeConnectionForAi(updated) };
      }
      case "rename-group": {
        const previousName = aiActionString(input, "previousName", action);
        const nextName = aiActionString(input, "nextName", action);
        if (previousName === nextName) return { renamed: false };
        const members = databaseStore.connections.filter((connection) => connection.group?.trim() === previousName);
        if (!members.length) throw new Error(`Database AI action rename-group could not find group: ${previousName}`);
        await Promise.all(members.map((connection) => databaseStore.saveConnection({ ...connection, group: nextName })));
        await databaseStore.replaceConnectionGroupOrderName(previousName, nextName);
        options.transferConnectionGroupCollapse(previousName, nextName);
        return { previousName, nextName, connectionCount: members.length };
      }
      case "move-group": {
        const connection = aiConnection(input, action);
        const group = typeof input.group === "string" ? input.group.trim() : "";
        await databaseStore.saveConnection({ ...connection, group: group || undefined });
        return { connectionId: connection.id, group: group || undefined };
      }
      case "reorder-groups": {
        if (!Array.isArray(input.groups) || input.groups.some((group) => typeof group !== "string" || !group.trim())) {
          throw new Error("Database AI action reorder-groups requires non-empty group names");
        }
        const groups = input.groups.map((group) => String(group).trim());
        if (new Set(groups).size !== groups.length) throw new Error("Database AI action reorder-groups cannot contain duplicates");
        const existing = [...new Set(databaseStore.connections.flatMap((connection) => {
          const group = connection.group?.trim();
          return group ? [group] : [];
        }))];
        if (groups.length !== existing.length || existing.some((group) => !groups.includes(group))) {
          throw new Error("Database AI action reorder-groups must contain each existing group exactly once");
        }
        await databaseStore.saveConnectionGroupOrder(groups);
        return { groups };
      }
      case "delete-connection": {
        const connection = aiConnection(input, action);
        await databaseStore.deleteConnection(connection.id);
        return { deleted: connection.id };
      }
      case "open-table-details": {
        const connection = aiConnection(input, action, false);
        await options.activateConnection(connection);
        if (databaseStore.activeConnectionId !== connection.id || !databaseStore.sessionIds[connection.id]) {
          throw new Error(`Database AI action open-table-details could not connect to: ${connection.name}`);
        }
        const database = aiActionString(input, "database", action);
        const table = aiActionString(input, "table", action);
        await options.openTableDetails(database, table);
        if (!options.detailsVisible.value) {
          throw new Error(`Database AI action open-table-details could not open: ${database}.${table}`);
        }
        return { connectionId: connection.id, database, table, opened: true };
      }
      case "compare-schema":
        return databaseStore.compareSchema(await connectedSchemaSyncSelection(input, action));
      case "generate-schema-sync": {
        const selection = await connectedSchemaSyncSelection(input, action);
        const comparison = await databaseStore.compareSchema(selection);
        return databaseStore.generateSchemaSync(
          selection,
          comparison.source.fingerprint,
          comparison.target.fingerprint,
          schemaSyncChangeIds(input, action, comparison.changes)
        );
      }
      case "execute-schema-sync": {
        signal?.throwIfAborted();
        const selection = await connectedSchemaSyncSelection(input, action);
        signal?.throwIfAborted();
        const comparison = await databaseStore.compareSchema(selection);
        signal?.throwIfAborted();
        const result = await databaseStore.executeSchemaSync(
          selection,
          comparison.source.fingerprint,
          comparison.target.fingerprint,
          schemaSyncChangeIds(input, action, comparison.changes),
          "ai",
          signal,
        );
        if (!result) throw new Error("Database AI action execute-schema-sync was cancelled by the user");
        return result;
      }
      case "open-redis-key": {
        const connection = aiConnection(input, action, true);
        await options.activateConnection(connection);
        if (databaseStore.activeConnectionId !== connection.id || !databaseStore.sessionIds[connection.id]) {
          throw new Error(`Database AI action open-redis-key could not connect to: ${connection.name}`);
        }
        await nextTick();
        return requestAiWorkspaceAction("redis", { ...input, action: "open-key" });
      }
      case "format-redis-json": {
        if (!databaseStore.isRedis) {
          throw new Error("Redis JSON formatting requires an active Redis connection");
        }
        await nextTick();
        return requestAiWorkspaceAction("redis", { action: "format-json" });
      }
      default:
        throw new Error(`Unknown database AI workspace action: ${action}`);
    }
  }

  let unregisterAiDatabaseWorkspaceActions: (() => void) | undefined;
  function registerAiDatabaseWorkspaceActions() {
    unregisterAiDatabaseWorkspaceActions?.();
    unregisterAiDatabaseWorkspaceActions = registerAiWorkspaceActionHandler(
      "database",
      handleAiDatabaseWorkspaceAction
    );
  }
  registerAiDatabaseWorkspaceActions();
  onActivated(registerAiDatabaseWorkspaceActions);
  onDeactivated(() => {
    unregisterAiDatabaseWorkspaceActions?.();
    unregisterAiDatabaseWorkspaceActions = undefined;
  });

  return {
    registerAiDatabaseWorkspaceActions,
    unregisterAiDatabaseWorkspaceActions: () => unregisterAiDatabaseWorkspaceActions?.(),
  };
}

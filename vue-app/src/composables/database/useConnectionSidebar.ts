import { computed, ref, type Ref } from "vue";
import { ElMessage } from "element-plus";
import { databaseTypeLabels, type DatabaseConnection } from "@/domain/database/types";
import { orderConnectionGroups } from "@/domain/connections/group-order";
import { useVueI18n } from "@/i18n/messages";
import { useDatabaseWorkspaceStore } from "@/stores/database-workspace";

export const UNGROUPED_CONNECTION_FILTER = "__ungrouped__";
const CONNECTION_DRAG_MIME_TYPE = "application/x-mincode-database-connection-sidebar";

export type ConnectionSidebarDragPayload =
  | { type: "connection"; connectionId: string }
  | { type: "group"; group: string };
type ConnectionGroupDropPosition = "before" | "after";
export type ExplorerTab = "objects" | "tables" | "scripts" | "history";

/**
 * 数据库工作区左侧连接侧栏的本地 UI 状态：
 * 搜索/过滤、分组折叠、连接与数据库展开、拖拽排序、explorer tab 切换。
 * 连接、断开等会话动作仍由视图层处理，经组件 emits 冒泡。
 */
export function useConnectionSidebar(options: {
  /** 连接中的连接 ID 集合（由视图层 connect 流程维护，经组件 props 传入）。 */
  connectingConnectionIds: Readonly<Ref<Record<string, boolean>>>;
}) {
  const { t } = useVueI18n();
  const databaseStore = useDatabaseWorkspaceStore();
  const { connectingConnectionIds } = options;

  const searchText = ref("");
  const tableSearchTexts = ref<Record<string, string>>({});
  const connectionFilterVisible = ref(false);
  const connectionFilterScope = ref<"all" | "favorites">("all");
  const connectionGroupFilter = ref<string>();
  const connectionTagFilter = ref<string>();
  const activeExplorerTab = ref<ExplorerTab>("objects");

  const connectionSidebarDrag = ref<ConnectionSidebarDragPayload>();
  const connectionGroupDrop = ref<{ group: string; position: ConnectionGroupDropPosition }>();

  const collapsedConnectionGroupNames = ref<Record<string, boolean>>({});
  const expandedConnectionIds = ref<Record<string, boolean>>({});
  const expandedDatabaseIds = ref<Record<string, boolean>>({});

  const searchKeyword = computed(() => searchText.value.trim().toLowerCase());

  function connectionIsConnected(connection: DatabaseConnection) {
    return Boolean(databaseStore.sessionIds[connection.id]);
  }

  function connectionIsConnecting(connection: DatabaseConnection) {
    return Boolean(connectingConnectionIds.value[connection.id]);
  }

  function connectionIsExpanded(connection: DatabaseConnection) {
    return Boolean(expandedConnectionIds.value[connection.id]);
  }

  function connectionSchema(connection: DatabaseConnection) {
    return databaseStore.schemas[connection.id];
  }

  function databaseExpansionKey(connection: DatabaseConnection, database: string) {
    return `${connection.id}:${database}`;
  }

  function databaseIsExpanded(connection: DatabaseConnection, database: string) {
    return Boolean(expandedDatabaseIds.value[databaseExpansionKey(connection, database)]);
  }

  function setConnectionExpandedById(connectionId: string, expanded: boolean) {
    expandedConnectionIds.value = {
      ...expandedConnectionIds.value,
      [connectionId]: expanded,
    };
  }

  function setConnectionExpanded(connection: DatabaseConnection, expanded: boolean) {
    expandedConnectionIds.value = {
      ...expandedConnectionIds.value,
      [connection.id]: expanded,
    };
  }

  function setDatabaseExpanded(connection: DatabaseConnection, database: string, expanded: boolean) {
    expandedDatabaseIds.value = {
      ...expandedDatabaseIds.value,
      [databaseExpansionKey(connection, database)]: expanded,
    };
  }

  function filteredDatabasesFor(connection: DatabaseConnection) {
    const databases = connectionSchema(connection)?.databases ?? [];
    const keyword = searchKeyword.value;
    // 后端已经按库名、表名排序。无搜索时直接复用原数组，避免连接完成后
    // 为大型 Schema 复制并排序全部对象，减少对象树首屏的主线程长任务。
    if (!keyword) return databases;

    return databases
      .map((database) => {
        if (database.name.toLowerCase().includes(keyword)) return database;
        return {
          ...database,
          tables: database.tables.filter((table) => table.name.toLowerCase().includes(keyword)),
        };
      })
      .filter(
        (database) => database.name.toLowerCase().includes(keyword) || database.tables.length > 0
      );
  }

  function connectionMatchesSearch(connection: DatabaseConnection) {
    const keyword = searchKeyword.value;
    if (!keyword) return true;
    return [
      connection.name,
      databaseTypeLabels[connection.dbType],
      connection.group,
      ...(connection.tags ?? []),
      ...filteredDatabasesFor(connection).flatMap((database) => [
        database.name,
        ...database.tables.map((table) => table.name),
      ]),
    ]
      .filter(Boolean)
      .some((value) => String(value).toLowerCase().includes(keyword));
  }

  function tableSearchKey(connection: DatabaseConnection, databaseName: string) {
    return `${connection.id}:${databaseName}`;
  }

  function getTableSearchText(connection: DatabaseConnection, databaseName: string) {
    return tableSearchTexts.value[tableSearchKey(connection, databaseName)] ?? "";
  }

  function setTableSearchText(
    connection: DatabaseConnection,
    databaseName: string,
    value: string
  ) {
    tableSearchTexts.value = {
      ...tableSearchTexts.value,
      [tableSearchKey(connection, databaseName)]: value,
    };
  }

  function filteredTablesFor(
    connection: DatabaseConnection,
    database: { name: string; tables: { name: string; kind: string }[] }
  ) {
    const keyword = getTableSearchText(connection, database.name).trim().toLowerCase();
    if (!keyword) return database.tables;
    return database.tables.filter((table) => table.name.toLowerCase().includes(keyword));
  }

  const allConnectionsCount = computed(() => databaseStore.connections.length);
  const favoriteConnectionsCount = computed(
    () => databaseStore.connections.filter((connection) => connection.favorite).length
  );
  const connectionFilterScopeOptions = computed(() => [
    { label: `${t("database.allConnections")} (${allConnectionsCount.value})`, value: "all" },
    {
      label: `${t("database.favoriteConnections")} (${favoriteConnectionsCount.value})`,
      value: "favorites",
    },
  ]);
  const connectionGroupOptions = computed(() => {
    const groupNames = new Set<string>();
    let hasUngroupedConnections = false;
    for (const connection of databaseStore.connections) {
      const group = connection.group?.trim();
      if (group) groupNames.add(group);
      else hasUngroupedConnections = true;
    }
    const options = [...groupNames]
      .sort((left, right) => left.localeCompare(right))
      .map((value) => ({ label: value, value }));
    if (hasUngroupedConnections) {
      options.unshift({
        label: t("database.ungroupedConnections"),
        value: UNGROUPED_CONNECTION_FILTER,
      });
    }
    return options;
  });
  const connectionTagOptions = computed(() =>
    [...new Set(databaseStore.connections.flatMap((connection) => connection.tags ?? []))]
      .map((tag) => tag.trim())
      .filter(Boolean)
      .sort((left, right) => left.localeCompare(right))
      .map((value) => ({ label: value, value }))
  );
  const hasConnectionFilters = computed(
    () =>
      connectionFilterScope.value === "favorites" ||
      Boolean(connectionGroupFilter.value) ||
      Boolean(connectionTagFilter.value)
  );

  function connectionMatchesFilters(connection: DatabaseConnection) {
    if (connectionFilterScope.value === "favorites" && !connection.favorite) return false;

    const selectedGroup = connectionGroupFilter.value;
    if (selectedGroup) {
      const connectionGroup = connection.group?.trim();
      if (selectedGroup === UNGROUPED_CONNECTION_FILTER) {
        if (connectionGroup) return false;
      } else if (connectionGroup !== selectedGroup) {
        return false;
      }
    }

    const selectedTag = connectionTagFilter.value;
    return !selectedTag || (connection.tags ?? []).some((tag) => tag.trim() === selectedTag);
  }

  const filteredConnections = computed(() => {
    const matches = databaseStore.connections.filter(
      (connection) => connectionMatchesSearch(connection) && connectionMatchesFilters(connection)
    );
    return [...matches].sort(
      (left, right) =>
        Number(Boolean(right.favorite)) - Number(Boolean(left.favorite)) ||
        (right.lastConnectedAt ?? 0) - (left.lastConnectedAt ?? 0) ||
        left.name.localeCompare(right.name)
    );
  });
  const connectionGroups = computed(() => {
    const groups = new Map<string, DatabaseConnection[]>();
    for (const connection of filteredConnections.value) {
      const key = connection.group?.trim() || "";
      const connections = groups.get(key) ?? [];
      connections.push(connection);
      groups.set(key, connections);
    }
    return orderConnectionGroups(
      [...groups.entries()].map(([key, connections]) => ({
        key,
        label: key || t("database.ungroupedConnections"),
        items: connections,
        name: key || t("database.ungroupedConnections"),
        connections,
      })),
      databaseStore.connectionGroupOrder
    );
  });

  const connectionDragEnabled = computed(
    () => !searchKeyword.value && !hasConnectionFilters.value
  );

  function setConnectionSidebarDrag(payload: ConnectionSidebarDragPayload, event: DragEvent) {
    connectionSidebarDrag.value = payload;
    event.dataTransfer?.setData(CONNECTION_DRAG_MIME_TYPE, JSON.stringify(payload));
    event.dataTransfer?.setData(
      "text/plain",
      payload.type === "connection" ? payload.connectionId : payload.group
    );
    if (event.dataTransfer) event.dataTransfer.effectAllowed = "move";
  }

  function readConnectionSidebarDrag(event: DragEvent): ConnectionSidebarDragPayload | undefined {
    if (connectionSidebarDrag.value) return connectionSidebarDrag.value;
    const value = event.dataTransfer?.getData(CONNECTION_DRAG_MIME_TYPE);
    if (!value) return undefined;
    try {
      const parsed = JSON.parse(value) as ConnectionSidebarDragPayload;
      if (parsed.type === "connection" && typeof parsed.connectionId === "string") return parsed;
      if (parsed.type === "group" && typeof parsed.group === "string") return parsed;
    } catch {
      // Drag data from another source is ignored.
    }
    return undefined;
  }

  function clearConnectionSidebarDrag() {
    connectionSidebarDrag.value = undefined;
    connectionGroupDrop.value = undefined;
  }

  function onConnectionDragStart(connection: DatabaseConnection, event: DragEvent) {
    if (!connectionDragEnabled.value) {
      event.preventDefault();
      return;
    }
    setConnectionSidebarDrag({ type: "connection", connectionId: connection.id }, event);
  }

  function onConnectionGroupDragStart(group: string, event: DragEvent) {
    if (!connectionDragEnabled.value || !group) {
      event.preventDefault();
      return;
    }
    event.stopPropagation();
    setConnectionSidebarDrag({ type: "group", group }, event);
  }

  function onConnectionGroupDragOver(group: string, event: DragEvent) {
    const payload = readConnectionSidebarDrag(event);
    if (!payload || (payload.type === "group" && (!group || payload.group === group))) return;

    event.preventDefault();
    if (event.dataTransfer) event.dataTransfer.dropEffect = "move";
    if (payload.type === "group") {
      const bounds = (event.currentTarget as HTMLElement).getBoundingClientRect();
      connectionGroupDrop.value = {
        group,
        position: event.clientY < bounds.top + bounds.height / 2 ? "before" : "after",
      };
    } else {
      connectionGroupDrop.value = undefined;
    }
  }

  async function onConnectionGroupDrop(group: string, event: DragEvent) {
    const payload = readConnectionSidebarDrag(event);
    const dropPosition = connectionGroupDrop.value;
    clearConnectionSidebarDrag();
    if (!payload) return;

    try {
      if (payload.type === "connection") {
        const connection = databaseStore.connections.find(
          (item) => item.id === payload.connectionId
        );
        const targetGroup = group || undefined;
        if (!connection || (connection.group?.trim() || undefined) === targetGroup) return;
        await databaseStore.saveConnection({ ...connection, group: targetGroup });
        const targetName = group || t("database.ungroupedConnections");
        collapsedConnectionGroupNames.value = {
          ...collapsedConnectionGroupNames.value,
          [targetName]: false,
        };
        ElMessage.success(t("database.connectionMovedToGroup").replace("{group}", targetName));
        return;
      }

      if (!group || payload.group === group) return;
      const position = dropPosition?.group === group ? dropPosition.position : "before";
      const orderedGroups = connectionGroups.value
        .map((item) => item.key)
        .filter(Boolean)
        .filter((name) => name !== payload.group);
      const targetIndex = orderedGroups.indexOf(group);
      if (targetIndex < 0) return;
      orderedGroups.splice(targetIndex + (position === "after" ? 1 : 0), 0, payload.group);
      await databaseStore.saveConnectionGroupOrder(orderedGroups);
    } catch (cause) {
      ElMessage.error(String(cause));
    }
  }

  function connectionGroupIsExpanded(groupName: string) {
    return !collapsedConnectionGroupNames.value[groupName];
  }

  function setConnectionGroupExpanded(groupName: string, expanded: boolean) {
    collapsedConnectionGroupNames.value = {
      ...collapsedConnectionGroupNames.value,
      [groupName]: !expanded,
    };
  }

  function transferConnectionGroupCollapse(previousName: string, nextName: string) {
    const wasCollapsed = Boolean(collapsedConnectionGroupNames.value[previousName]);
    const next = { ...collapsedConnectionGroupNames.value, [nextName]: wasCollapsed };
    delete next[previousName];
    collapsedConnectionGroupNames.value = next;
  }

  function toggleConnectionGroup(groupName: string) {
    collapsedConnectionGroupNames.value = {
      ...collapsedConnectionGroupNames.value,
      [groupName]: connectionGroupIsExpanded(groupName),
    };
  }

  return {
    searchText,
    activeExplorerTab,
    connectionFilterVisible,
    connectionFilterScope,
    connectionGroupFilter,
    connectionTagFilter,
    connectionSidebarDrag,
    connectionGroupDrop,
    connectionFilterScopeOptions,
    connectionGroupOptions,
    connectionTagOptions,
    hasConnectionFilters,
    filteredConnections,
    connectionGroups,
    connectionDragEnabled,
    connectionIsConnected,
    connectionIsConnecting,
    connectionIsExpanded,
    connectionSchema,
    databaseIsExpanded,
    setConnectionExpanded,
    setConnectionExpandedById,
    setDatabaseExpanded,
    setConnectionGroupExpanded,
    transferConnectionGroupCollapse,
    filteredDatabasesFor,
    filteredTablesFor,
    getTableSearchText,
    setTableSearchText,
    onConnectionDragStart,
    onConnectionGroupDragStart,
    onConnectionGroupDragOver,
    onConnectionGroupDrop,
    clearConnectionSidebarDrag,
    connectionGroupIsExpanded,
    toggleConnectionGroup,
  };
}

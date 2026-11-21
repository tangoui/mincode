<script setup lang="ts">
import { computed, ref, shallowRef } from "vue";
import {
  Clock,
  Coin,
  Connection,
  CopyDocument,
  Delete,
  Document,
  Download,
  EditPen,
  Filter,
  Folder,
  FolderAdd,
  Grid,
  MoreFilled,
  Notebook,
  Plus,
  Refresh,
  Star,
  Switch,
  Tickets,
  Upload,
} from "@element-plus/icons-vue";
import AppChevron from "@/components/ui/AppChevron.vue";
import AppFilterButton from "@/components/ui/AppFilterButton.vue";
import AppSearchField from "@/components/ui/AppSearchField.vue";
import DatabaseTypeIcon from "@/components/database/DatabaseTypeIcon.vue";
import ConnectionAvatar, { type ConnectionAvatarStatus } from "@/components/connections/ConnectionAvatar.vue";
import PluginMenuItems from "@/components/plugins/PluginMenuItems.vue";
import LinkedNotesBadge from "@/components/notes/LinkedNotesBadge.vue";
import { useLinkedNotesDrawer } from "@/stores/linked-notes-drawer";
import { usePluginMenuTarget } from "@/composables/use-plugin-menu-target";
import {
  type DatabaseConnection,
  type DatabaseQueryHistoryItem,
  type DatabaseSavedScript,
  type SchemaTable,
} from "@/domain/database/types";
import { isVectorDatabaseType } from "@/domain/database/vector-database";
import { connectionEndpoint } from "@/domain/database/endpoint";
import { databaseConnectionBrand } from "@/domain/database/type-brands";
import { avatarColor } from "@/domain/connections/overview";
import {
  UNGROUPED_CONNECTION_FILTER,
  useConnectionSidebar,
} from "@/composables/database/useConnectionSidebar";
import { useVueI18n } from "@/i18n/messages";
import { useDatabaseWorkspaceStore } from "@/stores/database-workspace";
import { useDatabaseDriverLabels } from "@/composables/database/useDatabaseDriverRuntime";

defineOptions({ name: "DatabaseConnectionSidebar" });

const props = defineProps<{
  explorerCollapsed: boolean;
  activeConnection: DatabaseConnection | undefined;
  connectingConnectionIds: Record<string, boolean>;
  selectedDatabase: string | undefined;
  selectedTable: string | undefined;
  savedScripts: DatabaseSavedScript[];
  activeConnectionHistory: DatabaseQueryHistoryItem[];
}>();

const emit = defineEmits<{
  (e: "new-connection"): void;
  (e: "import-config"): void;
  (e: "import-connections"): void;
  (e: "export-config"): void;
  (e: "refresh-schema"): void;
  (e: "activate-connection", connection: DatabaseConnection): void;
  (e: "toggle-connection-expansion", connection: DatabaseConnection): void;
  (
    e: "toggle-database-expansion",
    connection: DatabaseConnection,
    database: string
  ): void;
  (e: "connection-menu", connection: DatabaseConnection, command: unknown): void;
  (
    e: "database-menu",
    connection: DatabaseConnection,
    database: string,
    tables: SchemaTable[],
    command: unknown
  ): void;
  (
    e: "table-menu",
    connection: DatabaseConnection,
    database: string,
    table: SchemaTable,
    command: unknown
  ): void;
  (e: "rename-group", group: string): void;
  (e: "open-scripts", connection: DatabaseConnection): void;
  (e: "open-external-sql", connection: DatabaseConnection): void;
  (e: "open-audit", connection: DatabaseConnection): void;
  (
    e: "select-table",
    connection: DatabaseConnection,
    database: string,
    table: string
  ): void;
  (e: "save-current-script"): void;
  (e: "open-script", script: DatabaseSavedScript): void;
  (e: "delete-script", script: DatabaseSavedScript): void;
  (e: "open-history", item: DatabaseQueryHistoryItem): void;
  (e: "clear-history"): void;
}>();

const { t } = useVueI18n();
const linkedNotes = useLinkedNotesDrawer();
const databaseStore = useDatabaseWorkspaceStore();
const { driverUnavailable, connectionTypeLabel, canSwitchDriver } = useDatabaseDriverLabels();

/** 菜单项按连接能力门控：内置驱动走内置白名单，插件驱动按描述符声明的能力。 */
function capabilitiesOf(connection: DatabaseConnection) {
  return databaseStore.connectionCapabilities(connection);
}

const {
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
} = useConnectionSidebar({
  connectingConnectionIds: computed(() => props.connectingConnectionIds),
});

const connectedConnections = computed(() => databaseStore.connectedConnections);

/** Row avatar badge, matching the SSH sidebar: connected, connecting, or idle. */
function connectionAvatarStatus(connection: DatabaseConnection): ConnectionAvatarStatus {
  if (connectionIsConnected(connection)) return "online";
  if (connectionIsConnecting(connection)) return "pending";
  return "offline";
}

/** Brand-tinted avatar background, the same wash the connection overview uses. */
function connectionBrandStyle(connection: DatabaseConnection) {
  return { "--db-connection-brand": databaseConnectionBrand(connection).color };
}
// Plugin menu items (contributes.menus database/tree/context) of the open "⋯" menu.
interface OpenTreeMenu {
  key: string;
  connection: DatabaseConnection;
  database?: string;
  table?: SchemaTable;
}
const openTreeMenu = shallowRef<OpenTreeMenu | null>(null);

function treeMenuKey(connection: DatabaseConnection, database?: string, table?: SchemaTable) {
  return JSON.stringify([connection.id, database ?? null, table?.name ?? null]);
}

function handleTreeMenuVisible(visible: boolean, connection: DatabaseConnection, database?: string, table?: SchemaTable) {
  const key = treeMenuKey(connection, database, table);
  if (visible) openTreeMenu.value = { key, connection, database, table };
  else if (openTreeMenu.value?.key === key) openTreeMenu.value = null;
}

const treePluginMenu = usePluginMenuTarget("database/tree/context", () => {
  const open = openTreeMenu.value;
  if (!open) return null;
  const { connection, database, table } = open;
  return {
    // Same database.kind as the SQL editor: the dialect type for plugin-driver connections.
    connection: { id: connection.id, name: connection.name, kind: databaseStore.connectionDialectType(connection) },
    connected: connectionIsConnected(connection),
    objectType: table ? "table" : database !== undefined ? "database" : "connection",
    ...(database !== undefined ? { database } : {}),
    ...(table ? { table: { name: table.name, kind: table.kind } } : {}),
  };
});

function treePluginMenuHost(connection: DatabaseConnection, database?: string, table?: SchemaTable) {
  return openTreeMenu.value?.key === treeMenuKey(connection, database, table) ? treePluginMenu.host.value : null;
}

const activeDatabaseObjectLabel = computed(() =>
  databaseObjectLabel(props.activeConnection)
);
const SIDEBAR_TABLE_BATCH_SIZE = 40;
const tableVisibleCounts = ref<Record<string, number>>({});
const openTableMenuKey = ref("");

function visibleTablesFor(connection: DatabaseConnection, database: { name: string; tables: { name: string; kind: string }[] }) {
  const filtered = filteredTablesFor(connection, database);
  const key = `${connection.id}:${database.name}`;
  const visibleCount = tableVisibleCounts.value[key] ?? SIDEBAR_TABLE_BATCH_SIZE;
  return filtered.slice(0, visibleCount);
}

function remainingTableCount(connection: DatabaseConnection, database: { name: string; tables: { name: string; kind: string }[] }) {
  const filtered = filteredTablesFor(connection, database);
  const key = `${connection.id}:${database.name}`;
  return Math.max(0, filtered.length - (tableVisibleCounts.value[key] ?? SIDEBAR_TABLE_BATCH_SIZE));
}

function loadMoreTables(connection: DatabaseConnection, database: { name: string; tables: { name: string; kind: string }[] }) {
  const key = `${connection.id}:${database.name}`;
  tableVisibleCounts.value = {
    ...tableVisibleCounts.value,
    [key]: (tableVisibleCounts.value[key] ?? SIDEBAR_TABLE_BATCH_SIZE) + SIDEBAR_TABLE_BATCH_SIZE,
  };
}

function connectionIsActive(connection: DatabaseConnection) {
  return connection.id === props.activeConnection?.id;
}

function databaseObjectLabel(connection: DatabaseConnection | undefined) {
  if (connection?.dbType === "pgvector") return t("database.vectorTables");
  if (isVectorDatabaseType(connection?.dbType)) return t("database.collections");
  return t("database.tables");
}

function databaseObjectSearchPlaceholder(connection: DatabaseConnection | undefined) {
  if (connection?.dbType === "pgvector") return t("database.searchVectorTables");
  if (isVectorDatabaseType(connection?.dbType)) return t("database.searchCollections");
  return t("database.searchTables");
}

function formatHistoryTime(timestamp: number) {
  return new Intl.DateTimeFormat(undefined, {
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
  }).format(timestamp);
}

function displaySavedScriptName(name: string) {
  const suffixes = [" 查询", " query"];
  const suffix = suffixes.find((value) => name.toLocaleLowerCase().endsWith(value));
  return suffix ? name.slice(0, -suffix.length) : name;
}

function revealScriptsTab() {
  activeExplorerTab.value = "scripts";
}

// 视图在激活连接/打开库概览、复制/保存连接、分组重命名时需要同步侧栏展开状态，
// 以及切换到脚本 tab。
defineExpose({
  setConnectionExpanded,
  setConnectionExpandedById,
  setDatabaseExpanded,
  connectionIsExpanded,
  databaseIsExpanded,
  setConnectionGroupExpanded,
  transferConnectionGroupCollapse,
  revealScriptsTab,
});
</script>

<template>
  <aside v-if="!explorerCollapsed" class="db-explorer">
      <div class="db-explorer__toolbar">
        <strong class="db-explorer__title">{{ t("database.sidebarTitle") }}</strong>
        <el-button
          text
          native-type="button"
          class="db-square-action"
          :title="t('database.configImport')"
          :aria-label="t('database.configImport')"
          @click="emit('import-config')"
        >
          <el-icon><Upload /></el-icon>
        </el-button>
        <el-button
          text
          native-type="button"
          class="db-square-action db-explorer__secondary"
          :title="t('database.import.title')"
          :aria-label="t('database.import.title')"
          @click="emit('import-connections')"
        >
          <el-icon><FolderAdd /></el-icon>
        </el-button>
        <el-button
          text
          native-type="button"
          class="db-square-action db-explorer__secondary"
          :title="t('database.configExport')"
          :aria-label="t('database.configExport')"
          @click="emit('export-config')"
        >
          <el-icon><Download /></el-icon>
        </el-button>
        <el-button
          text
          native-type="button"
          class="db-square-action"
          :title="t('database.refreshObjects')"
          :aria-label="t('database.refreshObjects')"
          :disabled="!databaseStore.desktopRuntime || !activeConnection"
          @click="emit('refresh-schema')"
        >
          <el-icon><Refresh /></el-icon>
        </el-button>
        <!-- Shown only when the sidebar is too narrow for the secondary buttons (see navigator.css). -->
        <el-dropdown class="db-explorer__more" trigger="click" placement="bottom-end">
          <el-button
            text
            native-type="button"
            class="db-square-action"
            :title="t('common.more')"
            :aria-label="t('common.more')"
          >
            <el-icon><MoreFilled /></el-icon>
          </el-button>
          <template #dropdown>
            <el-dropdown-menu>
              <el-dropdown-item @click="emit('import-connections')"
                ><el-icon><FolderAdd /></el-icon>{{ t("database.import.title") }}</el-dropdown-item
              >
              <el-dropdown-item @click="emit('export-config')"
                ><el-icon><Download /></el-icon>{{ t("database.configExport") }}</el-dropdown-item
              >
            </el-dropdown-menu>
          </template>
        </el-dropdown>
        <el-button
          text
          native-type="button"
          class="db-square-action is-primary"
          :title="t('database.newConnection')"
          :aria-label="t('database.newConnection')"
          @click="emit('new-connection')"
        >
          <el-icon><Plus /></el-icon>
        </el-button>
      </div>

      <div class="db-search-row">
        <AppSearchField
          v-model="searchText"
          class="db-search-box"
          :placeholder="t('database.searchObjects')"
        />
        <AppFilterButton
          class="db-search-filter-trigger"
          :active="connectionFilterVisible || hasConnectionFilters"
          :label="t('database.connectionFilters')"
          :aria-expanded="connectionFilterVisible"
          @click="connectionFilterVisible = !connectionFilterVisible"
        />
      </div>

      <section v-if="connectionFilterVisible" class="db-connection-filter-panel">
        <el-segmented
          v-model="connectionFilterScope"
          class="db-connection-filter-scopes"
          size="small"
          :options="connectionFilterScopeOptions"
          :aria-label="t('database.connectionFilters')"
        />
        <div class="db-connection-filter-fields">
          <el-select
            filterable
            v-model="connectionGroupFilter"
            class="db-connection-filter-select"
            size="small"
            clearable
            :placeholder="t('database.filterGroup')"
          >
            <el-option
              v-for="option in connectionGroupOptions"
              :key="option.value"
              :label="option.label"
              :value="option.value"
            />
          </el-select>
          <el-select
            filterable
            v-model="connectionTagFilter"
            class="db-connection-filter-select"
            size="small"
            clearable
            :placeholder="t('database.filterTag')"
          >
            <el-option
              v-for="option in connectionTagOptions"
              :key="option.value"
              :label="option.label"
              :value="option.value"
            />
          </el-select>
        </div>
      </section>

      <div class="db-explorer-tabs">
        <el-button
          text
          native-type="button"
          :title="t('database.objects')"
          :class="{ 'is-active': activeExplorerTab === 'objects' }"
          @click="activeExplorerTab = 'objects'"
        >
          <el-icon><Folder /></el-icon><span>{{ t("database.objects") }}</span>
        </el-button>
        <el-button
          text
          native-type="button"
          :title="activeDatabaseObjectLabel"
          :class="{ 'is-active': activeExplorerTab === 'tables' }"
          @click="activeExplorerTab = 'tables'"
        >
          <el-icon><Tickets /></el-icon><span>{{ activeDatabaseObjectLabel }}</span>
        </el-button>
        <el-button
          text
          native-type="button"
          :title="t('database.scripts')"
          :class="{ 'is-active': activeExplorerTab === 'scripts' }"
          @click="activeExplorerTab = 'scripts'"
        >
          <el-icon><Document /></el-icon><span>{{ t("database.scripts") }}</span>
        </el-button>
        <el-button
          text
          native-type="button"
          :title="t('database.history')"
          :class="{ 'is-active': activeExplorerTab === 'history' }"
          @click="activeExplorerTab = 'history'"
        >
          <el-icon><Clock /></el-icon><span>{{ t("database.history") }}</span>
        </el-button>
      </div>

      <el-scrollbar class="db-tree-scroll">
        <template v-if="activeExplorerTab === 'objects' || activeExplorerTab === 'tables'">
          <section class="db-connection-navigator">
            <div v-if="!connectionGroups.length" class="db-connection-empty">
              {{
                databaseStore.connections.length
                  ? t("database.noMatchingConnections")
                  : t("database.noConnections")
              }}
            </div>
            <div
              v-for="group in connectionGroups"
              :key="group.key || UNGROUPED_CONNECTION_FILTER"
              class="db-connection-group"
              :class="{
                'is-drop-before': connectionGroupDrop?.group === group.key && connectionGroupDrop.position === 'before',
                'is-drop-after': connectionGroupDrop?.group === group.key && connectionGroupDrop.position === 'after',
                'is-drop-target': connectionSidebarDrag?.type === 'connection',
              }"
              @dragover="onConnectionGroupDragOver(group.key, $event)"
              @drop="onConnectionGroupDrop(group.key, $event)"
            >
              <div class="db-connection-group__header">
                <button
                  type="button"
                  class="db-connection-group__title"
                  :draggable="Boolean(group.key && connectionDragEnabled)"
                  :title="group.key ? t('database.reorderConnectionGroups') : undefined"
                  :aria-expanded="connectionGroupIsExpanded(group.name)"
                  @dragstart="onConnectionGroupDragStart(group.key, $event)"
                  @dragend="clearConnectionSidebarDrag"
                  @click="toggleConnectionGroup(group.name)"
                >
                  <i
                    class="db-connection-group__bar"
                    aria-hidden="true"
                    :style="{ background: avatarColor(group.key || 'ungrouped') }"
                  />
                  <span>{{ group.name }}</span
                  ><small>{{ group.connections.length }}</small>
                  <AppChevron
                    class="db-tree-chevron"
                    :direction="connectionGroupIsExpanded(group.name) ? 'down' : 'right'"
                  />
                </button>
                <el-dropdown
                  v-if="group.name !== t('database.ungroupedConnections')"
                  trigger="click"
                  placement="bottom-end"
                  @command="emit('rename-group', group.name)"
                >
                  <button
                    type="button"
                    class="db-connection-group__menu"
                    :title="t('common.more')"
                    :aria-label="t('common.more')"
                    @click.stop
                  >
                    <el-icon><MoreFilled /></el-icon>
                  </button>
                  <template #dropdown>
                    <el-dropdown-menu>
                      <el-dropdown-item command="rename">{{
                        t("database.renameGroup")
                      }}</el-dropdown-item>
                    </el-dropdown-menu>
                  </template>
                </el-dropdown>
              </div>
              <template v-if="connectionGroupIsExpanded(group.name)">
                <div class="db-connection-group__children">
                <div
                  v-for="connection in group.connections"
                  :key="connection.id"
                  class="db-connection-entry"
                  :class="{
                    'is-active': connectionIsActive(connection),
                    'is-expanded': connectionIsExpanded(connection),
                    'is-connecting': connectionIsConnecting(connection),
                    'is-dragging': connectionSidebarDrag?.type === 'connection' && connectionSidebarDrag.connectionId === connection.id,
                  }"
                >
                  <div class="db-connection-node">
                    <button
                      type="button"
                      class="db-connection-expand"
                      :disabled="connectionIsConnecting(connection)"
                      @click="emit('toggle-connection-expansion', connection)"
                    >
                      <AppChevron
                        class="db-tree-chevron"
                        :direction="connectionIsExpanded(connection) ? 'down' : 'right'"
                      />
                    </button>
                    <button
                      type="button"
                      class="db-connection-node__main"
                      :draggable="connectionDragEnabled && !connectionIsConnecting(connection)"
                      :title="
                        connectionIsConnecting(connection)
                          ? t('database.connecting')
                          : t('database.dragConnectionToGroup')
                      "
                      :aria-busy="connectionIsConnecting(connection)"
                      @dragstart="onConnectionDragStart(connection, $event)"
                      @dragend="clearConnectionSidebarDrag"
                      @click="emit('activate-connection', connection)"
                    >
                      <ConnectionAvatar
                        class="db-connection-node__avatar"
                        :style="connectionBrandStyle(connection)"
                        :name="connection.name"
                        :seed="connection.id"
                        :size="26"
                        :status="connectionAvatarStatus(connection)"
                      >
                        <DatabaseTypeIcon :db-type="connection.dbType" :driver="connection.driver" size="small" />
                      </ConnectionAvatar>
                      <span class="db-connection-node__meta"
                        ><strong><i v-if="connection.favorite">★</i>{{ connection.name }}</strong
                        ><em
                          ><span :class="{ 'is-driver-unavailable': driverUnavailable(connection) }">{{
                            connectionTypeLabel(connection)
                          }}</span>
                          · {{ connectionEndpoint(connection, t("database.overviewManagedService")) }}</em
                        ></span
                      >
                      <span
                        class="db-connection-node__status"
                        :class="{
                          'is-connected': connectionIsConnected(connection),
                          'is-connecting': connectionIsConnecting(connection),
                        }"
                        :title="
                          connectionIsConnecting(connection)
                            ? t('database.connecting')
                            : connectionIsConnected(connection)
                              ? t('database.connected')
                              : undefined
                        "
                      />
                    </button>
                    <LinkedNotesBadge type="database" :id="connection.id" :name="connection.name" />
                    <el-dropdown
                      trigger="click"
                      placement="bottom-end"
                      @command="emit('connection-menu', connection, $event)"
                      @visible-change="handleTreeMenuVisible($event, connection)"
                    >
                      <button
                        type="button"
                        class="db-connection-node__menu"
                        :title="t('database.connectionActions')"
                        :aria-label="t('database.connectionActions')"
                        @click.stop
                      >
                        <el-icon><MoreFilled /></el-icon>
                      </button>
                      <template #dropdown>
                        <el-dropdown-menu>
                          <el-dropdown-item
                            :command="connectionIsConnected(connection) ? 'disconnect' : 'connect'"
                            :disabled="connectionIsConnecting(connection)"
                          >
                            <el-icon><Connection /></el-icon>
                            {{
                              connectionIsConnected(connection)
                                ? t("database.disconnect")
                                : t("database.connect")
                            }}
                          </el-dropdown-item>
                          <el-dropdown-item command="new-query">
                            <el-icon><Document /></el-icon>{{ t("database.newQuery") }}
                          </el-dropdown-item>
                          <el-dropdown-item command="scripts">
                            <el-icon><Folder /></el-icon>{{ t("database.savedQueries") }}
                          </el-dropdown-item>
                          <el-dropdown-item command="open-sql">
                            <el-icon><Document /></el-icon>{{ t("database.openSqlFile") }}
                          </el-dropdown-item>
                          <el-dropdown-item divided command="favorite">
                            <el-icon><Star /></el-icon>
                            {{
                              connection.favorite
                                ? t("database.removeFavorite")
                                : t("database.addFavorite")
                            }}
                          </el-dropdown-item>
                          <el-dropdown-item command="duplicate">
                            <el-icon><CopyDocument /></el-icon
                            >{{ t("database.duplicateConnection") }}
                          </el-dropdown-item>
                          <el-dropdown-item command="copy-info">
                            <el-icon><CopyDocument /></el-icon
                            >{{ t("database.copyConnectionInfo") }}
                          </el-dropdown-item>
                          <el-dropdown-item command="edit">
                            <el-icon><EditPen /></el-icon>{{ t("common.edit") }}
                          </el-dropdown-item>
                          <el-dropdown-item v-if="canSwitchDriver(connection)" command="switch-driver">
                            <el-icon><Switch /></el-icon
                            >{{ t("pluginContrib.databaseDrivers.runtime.switchDriver") }}
                          </el-dropdown-item>
                          <el-dropdown-item divided command="notes-new">
                            <el-icon><Plus /></el-icon>{{ t("notes.link.newLinked") }}
                          </el-dropdown-item>
                          <el-dropdown-item command="notes-view">
                            <el-icon><Notebook /></el-icon
                            >{{ t("notes.link.viewLinked", { count: linkedNotes.countOf("database", connection.id) }) }}
                          </el-dropdown-item>
                          <el-dropdown-item divided command="delete" class="is-danger">
                            <el-icon><Delete /></el-icon>{{ t("common.delete") }}
                          </el-dropdown-item>
                          <PluginMenuItems
                            menu="database/tree/context"
                            :host="treePluginMenuHost(connection)"
                            :context="treePluginMenu.context"
                          />
                        </el-dropdown-menu>
                      </template>
                    </el-dropdown>
                  </div>

                  <div
                    v-if="
                      connectionIsExpanded(connection) &&
                      connection.dbType !== 'redis' &&
                      connectionSchema(connection)
                    "
                    class="db-connection-object-tree"
                  >
                    <template v-if="activeExplorerTab === 'objects'">
                      <button
                        type="button"
                        class="db-connection-utility-row"
                        @click="emit('open-scripts', connection)"
                      >
                        <el-icon><Folder /></el-icon><span>{{ t("database.savedQueries") }}</span>
                      </button>
                      <button
                        type="button"
                        class="db-connection-utility-row"
                        @click="emit('open-external-sql', connection)"
                      >
                        <el-icon><Document /></el-icon
                        ><span>{{ t("database.externalSqlFiles") }}</span>
                      </button>
                      <button
                        type="button"
                        class="db-connection-audit-row"
                        @click="emit('open-audit', connection)"
                      >
                        <el-icon><Filter /></el-icon
                        ><span>{{ t("database.highRiskSqlAudit") }}</span>
                      </button>
                    </template>

                    <div
                      v-for="database in filteredDatabasesFor(connection)"
                      :key="`${connection.id}:${database.name}`"
                      class="db-tree-database db-connection-database"
                    >
                      <div class="db-tree-database__header">
                        <button
                          type="button"
                          class="db-tree-row"
                          :class="{ 'is-open': databaseIsExpanded(connection, database.name) }"
                          @click="emit('toggle-database-expansion', connection, database.name)"
                        >
                          <AppChevron
                            class="db-tree-chevron"
                            :direction="
                              databaseIsExpanded(connection, database.name) ? 'down' : 'right'
                            "
                          /><el-icon class="db-database-icon"><Coin /></el-icon
                          ><span>{{ database.name }}</span>
                          <small>{{ database.tables.length }}</small>
                        </button>
                        <el-dropdown
                          trigger="click"
                          placement="bottom-end"
                          popper-class="db-object-actions-popper"
                          @command="
                            emit(
                              'database-menu',
                              connection,
                              database.name,
                              database.tables,
                              $event
                            )
                          "
                          @visible-change="handleTreeMenuVisible($event, connection, database.name)"
                        >
                          <el-button
                            text
                            native-type="button"
                            class="db-tree-node__menu"
                            :title="t('database.databaseActions')"
                            :aria-label="t('database.databaseActions')"
                            @click.stop
                          >
                            <el-icon><MoreFilled /></el-icon>
                          </el-button>
                          <template #dropdown>
                            <el-dropdown-menu>
                              <el-dropdown-item command="new-query">
                                <el-icon><Document /></el-icon>{{ t("database.newQuery") }}
                              </el-dropdown-item>
                              <el-dropdown-item
                                v-if="capabilitiesOf(connection).tableTemplate"
                                command="new-table"
                              >
                                <el-icon><Plus /></el-icon>{{ t("database.newTable") }}
                              </el-dropdown-item>
                              <el-dropdown-item
                                v-if="!connection.driver && isVectorDatabaseType(connection.dbType) && connection.dbType !== 'pgvector'"
                                command="new-vector-collection"
                              >
                                <el-icon><Plus /></el-icon>{{
                                  t("database.createVectorObject", {
                                    kind: connection.dbType === "pinecone"
                                      ? t("database.vectorIndex")
                                      : t("database.vectorCollection"),
                                  })
                                }}
                              </el-dropdown-item>
                              <el-dropdown-item command="copy-name">
                                <el-icon><CopyDocument /></el-icon
                                >{{ t("database.copyObjectName") }}
                              </el-dropdown-item>
                              <el-dropdown-item
                                v-if="capabilitiesOf(connection).charsetChange"
                                command="change-charset"
                              >
                                <el-icon><EditPen /></el-icon>{{ t("database.changeCharset") }}
                              </el-dropdown-item>
                              <el-dropdown-item
                                v-if="capabilitiesOf(connection).schemaDdl"
                                divided
                                command="export-schema"
                              >
                                <el-icon><Download /></el-icon>{{ t("database.exportSchema") }}
                              </el-dropdown-item>
                              <el-dropdown-item divided command="import-sql">
                                <el-icon><Upload /></el-icon>{{ t("database.importSqlFile") }}
                              </el-dropdown-item>
                              <el-dropdown-item command="refresh">
                                <el-icon><Refresh /></el-icon>{{ t("database.refresh") }}
                              </el-dropdown-item>
                              <el-dropdown-item
                                v-if="capabilitiesOf(connection).clearDatabase"
                                divided
                                command="clear-database"
                                class="is-danger"
                              >
                                <el-icon><Delete /></el-icon>{{ t("database.cleardatabase") }}
                              </el-dropdown-item>
                              <el-dropdown-item
                                v-if="
                                  capabilitiesOf(connection).dropDatabase &&
                                  !(connection.dbType === 'duckdb' && database.name.toLowerCase() === 'main')
                                "
                                command="drop-database"
                                class="is-danger"
                              >
                                <el-icon><Delete /></el-icon>{{ t("database.dropdatabase") }}
                              </el-dropdown-item>
                              <PluginMenuItems
                                menu="database/tree/context"
                                :host="treePluginMenuHost(connection, database.name)"
                                :context="treePluginMenu.context"
                              />
                            </el-dropdown-menu>
                          </template>
                        </el-dropdown>
                      </div>
                      <template v-if="databaseIsExpanded(connection, database.name)">
                        <div v-if="activeExplorerTab === 'objects'" class="db-tree-group-row">
                          <AppChevron class="db-tree-chevron" direction="down" /><el-icon
                            ><Grid /></el-icon
                          ><span>{{ databaseObjectLabel(connection) }} ({{ database.tables.length }})</span>
                        </div>
                        <div
                          v-if="activeExplorerTab === 'objects' && database.tables.length > 3"
                          class="db-table-search-row"
                        >
                          <AppSearchField
                            class="db-table-search-input"
                            :model-value="getTableSearchText(connection, database.name)"
                            :placeholder="databaseObjectSearchPlaceholder(connection)"
                            :aria-label="databaseObjectSearchPlaceholder(connection)"
                            @update:model-value="setTableSearchText(connection, database.name, $event)"
                          />
                        </div>
                        <div
                          v-for="table in visibleTablesFor(connection, database)"
                          :key="`${connection.id}:${database.name}:${table.name}`"
                          class="db-table-row"
                          :class="{
                            'is-active':
                              connectionIsActive(connection) &&
                              table.name === selectedTable &&
                              database.name === selectedDatabase,
                          }"
                        >
                          <button
                            type="button"
                            class="db-table-row__main"
                            @click="emit('select-table', connection, database.name, table.name)"
                          >
                            <el-icon><Grid /></el-icon><span>{{ table.name }}</span
                            ><small>{{ table.kind }}</small>
                          </button>
                          <el-dropdown
                            v-if="openTableMenuKey === `${connection.id}:${database.name}:${table.name}`"
                            trigger="click"
                            placement="bottom-end"
                            popper-class="db-object-actions-popper"
                            @visible-change="
                              (visible: boolean) => {
                                if (!visible) openTableMenuKey = '';
                                handleTreeMenuVisible(visible, connection, database.name, table);
                              }
                            "
                            @command="
                              emit('table-menu', connection, database.name, table, $event)
                            "
                          >
                            <el-button
                              text
                              native-type="button"
                              class="db-tree-node__menu"
                              :title="t('database.tableActions')"
                              :aria-label="t('database.tableActions')"
                              @click.stop
                            >
                              <el-icon><MoreFilled /></el-icon>
                            </el-button>
                            <template #dropdown>
                              <el-dropdown-menu>
                                <el-dropdown-item command="open">
                                  <el-icon><Grid /></el-icon>{{ t("database.open") }}
                                </el-dropdown-item>
                                <el-dropdown-item command="query">
                                  <el-icon><Document /></el-icon>{{ t("database.queryTable") }}
                                </el-dropdown-item>
                                <el-dropdown-item command="details">
                                  <el-icon><EditPen /></el-icon>{{ t("database.tableStructure") }}
                                </el-dropdown-item>
                                <el-dropdown-item divided command="copy-name">
                                  <el-icon><CopyDocument /></el-icon
                                  >{{ t("database.copyObjectName") }}
                                </el-dropdown-item>
                                <el-dropdown-item
                                  v-if="capabilitiesOf(connection).schemaDdl"
                                  command="copy-ddl"
                                >
                                  <el-icon><CopyDocument /></el-icon>{{ t("database.copyDdl") }}
                                </el-dropdown-item>
                                <el-dropdown-item
                                  v-if="
                                    table.kind.toLowerCase() === 'table' &&
                                    capabilitiesOf(connection).charsetChange
                                  "
                                  command="change-charset"
                                >
                                  <el-icon><EditPen /></el-icon>{{ t("database.changeCharset") }}
                                </el-dropdown-item>
                                <template v-if="capabilitiesOf(connection).tableDataExport">
                                  <el-dropdown-item divided command="export-csv">
                                    <el-icon><Download /></el-icon>{{ t("database.exportTableCsv") }}
                                  </el-dropdown-item>
                                  <el-dropdown-item command="export-json">
                                    <el-icon><Download /></el-icon>{{ t("database.exportTableJson") }}
                                  </el-dropdown-item>
                                  <el-dropdown-item command="export-xlsx">
                                    <el-icon><Download /></el-icon>{{ t("database.exportTableXlsx") }}
                                  </el-dropdown-item>
                                  <el-dropdown-item command="export-markdown">
                                    <el-icon><Download /></el-icon>{{ t("database.exportTableMarkdown") }}
                                  </el-dropdown-item>
                                  <el-dropdown-item command="export-sql">
                                    <el-icon><Download /></el-icon>{{ t("database.exportTableSql") }}
                                  </el-dropdown-item>
                                  <el-dropdown-item command="export-txt">
                                    <el-icon><Download /></el-icon>{{ t("database.exportTableTxt") }}
                                  </el-dropdown-item>
                                </template>
                                <el-dropdown-item
                                  v-if="capabilitiesOf(connection).tableDataImport"
                                  :divided="!capabilitiesOf(connection).tableDataExport"
                                  command="import-data"
                                >
                                  <el-icon><Upload /></el-icon>{{ t("database.importTableData") }}
                                </el-dropdown-item>
                                <template
                                  v-if="
                                    (table.kind.toLowerCase() === 'table' ||
                                      isVectorDatabaseType(connection.dbType)) &&
                                    (capabilitiesOf(connection).clearTable ||
                                      capabilitiesOf(connection).dropTable)
                                  "
                                >
                                  <el-dropdown-item
                                    v-if="
                                      capabilitiesOf(connection).clearTable &&
                                      (!isVectorDatabaseType(connection.dbType) ||
                                        connection.dbType === 'pgvector')
                                    "
                                    divided
                                    command="clear-table"
                                    class="is-danger"
                                  >
                                    <el-icon><Delete /></el-icon>{{ t("database.cleartable") }}
                                  </el-dropdown-item>
                                  <el-dropdown-item
                                    v-if="capabilitiesOf(connection).dropTable"
                                    :divided="
                                      !capabilitiesOf(connection).clearTable ||
                                      (isVectorDatabaseType(connection.dbType) &&
                                        connection.dbType !== 'pgvector')
                                    "
                                    command="drop-table"
                                    class="is-danger"
                                  >
                                    <el-icon><Delete /></el-icon>{{
                                      isVectorDatabaseType(connection.dbType) && connection.dbType !== "pgvector"
                                        ? t("database.deleteVectorObject", {
                                            kind: connection.dbType === "pinecone"
                                              ? t("database.vectorIndex")
                                              : t("database.vectorCollection"),
                                          })
                                        : t("database.droptable")
                                    }}
                                  </el-dropdown-item>
                                </template>
                                <PluginMenuItems
                                  menu="database/tree/context"
                                  :host="treePluginMenuHost(connection, database.name, table)"
                                  :context="treePluginMenu.context"
                                />
                              </el-dropdown-menu>
                            </template>
                          </el-dropdown>
                          <el-button
                            v-if="openTableMenuKey !== `${connection.id}:${database.name}:${table.name}`"
                            text
                            native-type="button"
                            class="db-tree-node__menu"
                            :title="t('database.tableActions')"
                            :aria-label="t('database.tableActions')"
                            @click.stop="openTableMenuKey = `${connection.id}:${database.name}:${table.name}`"
                          >
                            <el-icon><MoreFilled /></el-icon>
                          </el-button>
                        </div>
                        <el-button
                          v-if="remainingTableCount(connection, database) > 0"
                          text
                          native-type="button"
                          class="db-table-load-more"
                          @click="loadMoreTables(connection, database)"
                        >
                          {{ t("database.loadMoreTables", { count: remainingTableCount(connection, database) }) }}
                        </el-button>
                      </template>
                    </div>
                  </div>
                </div>
                </div>
              </template>
            </div>
          </section>
        </template>

        <template v-else-if="activeExplorerTab === 'scripts'">
          <div class="db-side-section-header">
            <strong>{{ t("database.scripts") }}</strong
            ><el-button text native-type="button" @click="emit('save-current-script')">
              <el-icon><Plus /></el-icon>{{ t("database.saveCurrent") }}
            </el-button>
          </div>
          <div v-if="!savedScripts.length" class="db-side-empty">{{ t("database.noScripts") }}</div>
          <div v-for="script in savedScripts" :key="script.id" class="db-side-list-item">
            <el-button text native-type="button" @click="emit('open-script', script)">
              <el-icon><Document /></el-icon
              ><span class="db-side-list-item__content"
                ><strong>{{ displaySavedScriptName(script.name) }}</strong
                ><small>{{ new Date(script.updatedAt).toLocaleString() }}</small></span
              >
            </el-button>
            <el-button text native-type="button" :title="t('database.deleteScript')" @click="emit('delete-script', script)">
              <el-icon><Delete /></el-icon>
            </el-button>
          </div>
        </template>

        <template v-else>
          <div class="db-side-section-header">
            <strong>{{ t("database.history") }}</strong
            ><el-button text native-type="button"
              :disabled="!activeConnectionHistory.length"
              @click="emit('clear-history')"
            >
              {{ t("common.clear") }}
            </el-button>
          </div>
          <div v-if="!activeConnectionHistory.length" class="db-side-empty">
            {{ t("database.noHistory") }}
          </div>
          <el-button text native-type="button"
            v-for="item in activeConnectionHistory"
            :key="item.id"
            class="db-history-item"
            @click="emit('open-history', item)"
          >
            <span :class="item.success ? 'is-success' : 'is-error'" />
            <span
              ><strong>{{ item.query }}</strong
              ><small
                >{{ formatHistoryTime(item.startedAt) }} · {{ item.executionMs }} ms ·
                {{ item.rowCount }} {{ t("database.rows") }}</small
              ><em v-if="item.error">{{ item.error }}</em></span
            >
          </el-button>
        </template>
      </el-scrollbar>

      <footer class="db-explorer__footer">
        <span class="db-explorer__footer-summary">{{
          t("database.sidebarFooterSummary", {
            total: databaseStore.connections.length,
            connected: connectedConnections.length,
          })
        }}</span>
      </footer>
  </aside>
</template>

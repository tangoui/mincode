<script setup lang="ts">
import { resolveConnectionDriver } from "@/domain/database/driver-descriptor";
import { computed, onActivated, onBeforeUnmount, onDeactivated, onMounted, ref, watch } from "vue";
import { ArrowDown, ArrowRight, Plus, StarFilled, Upload } from "@element-plus/icons-vue";
import AppSearchField from "@/components/ui/AppSearchField.vue";
import ConnectionAvatar, { type ConnectionAvatarStatus } from "@/components/connections/ConnectionAvatar.vue";
import ConnectionRecentStrip, { type RecentStripItem } from "@/components/connections/ConnectionRecentStrip.vue";
import DatabaseTypeIcon from "@/components/database/DatabaseTypeIcon.vue";
import { orderConnectionGroups } from "@/domain/connections/group-order";
import { isRepeatClick, useOverviewSearchKeyboard } from "@/composables/connections/useOverviewSearchKeyboard";
import { pickRecent, relativeTime } from "@/domain/connections/overview";
import { connectionEndpoint } from "@/domain/database/endpoint";
import { databaseConnectionBrand } from "@/domain/database/type-brands";
import {
  type DatabaseConnection,
  type DatabaseSchemaInfo,
} from "@/domain/database/types";
import { useVueI18n } from "@/i18n/messages";
import { loadPersistedValue, savePersistedValue } from "@/services/persistent-store";

/** Connections last connected at or before this time are hidden from "Recently opened". */
const RECENT_CLEARED_AT_KEY = "app_db_recent_cleared_at_v1";

const props = defineProps<{
  connections: DatabaseConnection[];
  connectedConnectionIds: string[];
  connectingConnectionIds: Record<string, boolean>;
  schemas: Record<string, DatabaseSchemaInfo | undefined>;
  groupOrder: string[];
}>();

const emit = defineEmits<{
  connect: [connection: DatabaseConnection];
  open: [connection: DatabaseConnection];
  "new-connection": [];
  "import-config": [];
}>();

const { t, intlLocale } = useVueI18n();
const driverUnavailable = (connection: DatabaseConnection) => !resolveConnectionDriver(connection).available;
const connectionTypeLabel = (connection: DatabaseConnection) => resolveConnectionDriver(connection).title;
const connectionsSection = ref<HTMLElement>();
const searchField = ref<{ focus: () => void }>();
const query = ref("");
const selectedGroup = ref("");
/** Collapsed table groups; local to this page and not persisted. */
const collapsedGroups = ref<Record<string, boolean>>({});
const recentClearedAt = ref(0);
// Relative times are refreshed on activation and connection changes; the page runs no timers.
const now = ref(Date.now());

const connectedIds = computed(() => new Set(props.connectedConnectionIds));
const connectionsById = computed(() => new Map(props.connections.map((connection) => [connection.id, connection])));
const favoriteCount = computed(
  () => props.connections.filter((connection) => connection.favorite).length
);
const typeCount = computed(() => new Set(props.connections.map((connection) => connection.dbType)).size);

const groupOptions = computed(() =>
  orderConnectionGroups(
    [...new Set(props.connections.map((connection) => connection.group?.trim()).filter(Boolean) as string[])]
      .map((group) => ({ key: group, label: group, items: [] })),
    props.groupOrder
  ).map((group) => group.key)
);

const filteredConnections = computed(() => {
  const keyword = query.value.trim().toLocaleLowerCase();
  return props.connections.filter((connection) => {
    const group = connection.group?.trim() || "";
    if (selectedGroup.value && group !== selectedGroup.value) return false;
    if (!keyword) return true;
    return [
      connection.name,
      connection.host,
      connection.filePath,
      connection.database,
      connection.username,
      group,
      ...(connection.tags ?? []),
    ]
      .filter(Boolean)
      .join(" ")
      .toLocaleLowerCase()
      .includes(keyword);
  });
});

const groupedConnections = computed(() => {
  const groups = new Map<string, DatabaseConnection[]>();
  for (const connection of filteredConnections.value) {
    const group = connection.group?.trim() || "";
    groups.set(group, [...(groups.get(group) ?? []), connection]);
  }
  return orderConnectionGroups(
    [...groups.entries()].map(([key, items]) => ({
      key,
      label: key || t("database.overviewUngrouped"),
      items: [...items].sort((left, right) => {
        if (Boolean(left.favorite) !== Boolean(right.favorite)) return left.favorite ? -1 : 1;
        return left.name.localeCompare(right.name, undefined, { numeric: true, sensitivity: "base" });
      }),
    })),
    props.groupOrder
  );
});

function summaryLabel() {
  return t("database.overviewSummary")
    .replace("{total}", String(props.connections.length))
    .replace("{connected}", String(connectedIds.value.size))
    .replace("{favorites}", String(favoriteCount.value))
    .replace("{types}", String(typeCount.value));
}

function isConnected(connection: DatabaseConnection) {
  return connectedIds.value.has(connection.id);
}

function isConnecting(connection: DatabaseConnection) {
  return Boolean(props.connectingConnectionIds[connection.id]);
}

function statusClass(connection: DatabaseConnection) {
  if (isConnected(connection)) return "is-online";
  if (isConnecting(connection)) return "is-pending";
  return "is-offline";
}

function avatarStatus(connection: DatabaseConnection): ConnectionAvatarStatus {
  if (isConnected(connection)) return "online";
  if (isConnecting(connection)) return "pending";
  return "offline";
}

function statusLabel(connection: DatabaseConnection) {
  if (isConnected(connection)) return t("database.overviewConnected");
  if (isConnecting(connection)) return t("database.connecting");
  return t("database.overviewDisconnected");
}

/** Brand color for the type swatch and the icon tile. */
function brandStyle(connection: DatabaseConnection) {
  return { "--db-overview-brand": databaseConnectionBrand(connection).color };
}

function endpoint(connection: DatabaseConnection) {
  return connectionEndpoint(connection, t("database.overviewManagedService"));
}

function databaseCount(connection: DatabaseConnection) {
  return props.schemas[connection.id]?.databases.length ?? 0;
}

function lastConnectedLabel(connection: DatabaseConnection) {
  const at = connection.lastConnectedAt;
  if (typeof at !== "number" || !Number.isFinite(at) || at <= 0) return t("database.overviewNeverConnected");
  return relativeTime(at, now.value, intlLocale.value);
}

/** A connected database switches to its tab; otherwise it gets connected. */
function activateConnection(connection: DatabaseConnection) {
  if (isConnected(connection)) emit("open", connection);
  else if (!isConnecting(connection)) emit("connect", connection);
}

// ---- Recently opened -------------------------------------------------------

const recentItems = computed<RecentStripItem[]>(() =>
  pickRecent(props.connections, (connection) => connection.lastConnectedAt, recentClearedAt.value).map(
    (connection) => ({
      id: connection.id,
      name: connection.name,
      seed: connection.id,
      status: avatarStatus(connection),
      detail: [connectionTypeLabel(connection), connection.database?.trim(), lastConnectedLabel(connection)]
        .filter(Boolean)
        .join(" · "),
    })
  )
);

function openRecent(connectionId: string) {
  const connection = connectionsById.value.get(connectionId);
  if (connection) activateConnection(connection);
}

let clearedBeforeLoad = false;

function clearRecent() {
  clearedBeforeLoad = true;
  const at = Date.now();
  recentClearedAt.value = at;
  now.value = at;
  void savePersistedValue(RECENT_CLEARED_AT_KEY, at).catch(() => undefined);
}

async function loadRecentClearedAt() {
  try {
    const stored = await loadPersistedValue<number>(RECENT_CLEARED_AT_KEY);
    if (clearedBeforeLoad) return;
    if (typeof stored === "number" && Number.isFinite(stored) && stored >= 0) recentClearedAt.value = stored;
  } catch {
    // Unreadable storage only means the full recent list stays visible.
  }
}

// ---- In use ----------------------------------------------------------------

const liveConnections = computed(() => props.connections.filter((connection) => isConnected(connection)));

function loadedDatabasesLabel(connection: DatabaseConnection) {
  return t("database.overviewLoadedCount").replace("{count}", String(databaseCount(connection)));
}

function connectedForLabel(connection: DatabaseConnection) {
  const at = connection.lastConnectedAt;
  if (typeof at !== "number" || !Number.isFinite(at) || at <= 0) return "";
  const minutes = Math.max(0, Math.floor((now.value - at) / 60_000));
  return t("database.overviewConnectedFor").replace("{count}", String(minutes));
}

// ---- All connections table and keyboard highlight ---------------------------

function groupIsCollapsed(group: string) {
  return collapsedGroups.value[group] === true;
}

function toggleGroup(group: string) {
  const highlightedId = highlightedConnectionId.value;
  collapsedGroups.value = { ...collapsedGroups.value, [group]: !groupIsCollapsed(group) };
  // Keep the same connection highlighted when it is still visible; otherwise start from the top.
  const index = highlightedId ? rowIndexById.value.get(highlightedId) : undefined;
  if (index === undefined) resetHighlight();
  else highlightIndex.value = index;
}

/** Rows in the order they appear in the table; arrow keys walk this list. */
const visibleRows = computed(() =>
  groupedConnections.value.flatMap((group) => (groupIsCollapsed(group.key) ? [] : group.items))
);

const rowIndexById = computed(() => new Map(visibleRows.value.map((connection, index) => [connection.id, index])));

// Also focuses the search box on mount/activation unless a dialog is open.
const { highlightIndex, resetHighlight, onSearchKeydown } = useOverviewSearchKeyboard({
  query,
  count: computed(() => visibleRows.value.length),
  activate: (index) => {
    const connection = visibleRows.value[index];
    if (connection) activateConnection(connection);
  },
  searchField,
  scrollContainer: connectionsSection,
});

const highlightedConnectionId = computed(() => visibleRows.value[highlightIndex.value]?.id);

function selectRow(connection: DatabaseConnection) {
  highlightIndex.value = rowIndexById.value.get(connection.id) ?? -1;
}

function onRowAction(event: MouseEvent, connection: DatabaseConnection) {
  if (isRepeatClick(event)) return;
  activateConnection(connection);
}

watch([query, selectedGroup], () => {
  // Searching shows every match, including those in collapsed groups.
  if (query.value.trim()) collapsedGroups.value = {};
  resetHighlight();
});

watch(
  () => props.connectedConnectionIds.join("|"),
  () => {
    now.value = Date.now();
  }
);

// Relative times ("5 min ago", "Connected N min") tick once a minute while the
// overview is on screen. The database workspace is cached by KeepAlive, so the
// clock stops when it is deactivated and catches up when it comes back.
const CLOCK_INTERVAL_MS = 60_000;
let clockTimer: ReturnType<typeof setInterval> | undefined;

function stopClock() {
  if (clockTimer === undefined) return;
  clearInterval(clockTimer);
  clockTimer = undefined;
}

function startClock() {
  stopClock();
  now.value = Date.now();
  clockTimer = setInterval(() => {
    now.value = Date.now();
  }, CLOCK_INTERVAL_MS);
}

onMounted(() => {
  void loadRecentClearedAt();
  startClock();
});
onActivated(startClock);
onDeactivated(stopClock);
onBeforeUnmount(stopClock);
</script>

<template>
  <section class="db-connection-overview">
    <el-scrollbar class="db-connection-overview__scroll">
      <div class="db-connection-overview__content">
        <header class="connection-overview__head">
          <h2 class="connection-overview__title">{{ t("database.overviewTitle") }}</h2>
          <span class="connection-overview__stat">{{ summaryLabel() }}</span>
          <div class="connection-overview__actions">
            <el-button class="db-connection-overview__import" :icon="Upload" @click="emit('import-config')">
              {{ t("common.import") }}
            </el-button>
            <el-button type="primary" :icon="Plus" @click="emit('new-connection')">
              {{ t("database.addConnection") }}
            </el-button>
          </div>
        </header>

        <div class="connection-overview__search">
          <AppSearchField
            ref="searchField"
            v-model="query"
            clearable
            :placeholder="t('database.overviewSearchPlaceholder')"
            :aria-label="t('database.overviewSearchPlaceholder')"
            @keydown="onSearchKeydown"
          />
        </div>

        <el-empty
          v-if="!props.connections.length"
          class="db-connection-overview__empty"
          :image-size="88"
          :description="t('database.overviewNoConnections')"
        >
          <el-button type="primary" @click="emit('new-connection')">
            {{ t("database.addConnection") }}
          </el-button>
        </el-empty>

        <template v-else>
          <ConnectionRecentStrip
            v-if="!query.trim()"
            :items="recentItems"
            :title="t('database.overviewRecent')"
            :clear-label="t('database.overviewClearRecent')"
            @open="openRecent"
            @clear="clearRecent"
          >
            <template #avatar="{ item }">
              <ConnectionAvatar
                v-if="connectionsById.get(item.id)"
                class="db-connection-overview__avatar"
                :style="brandStyle(connectionsById.get(item.id)!)"
                :name="item.name"
                :seed="item.seed"
                :status="item.status"
              >
                <DatabaseTypeIcon
                  :db-type="connectionsById.get(item.id)!.dbType"

                />
              </ConnectionAvatar>
            </template>
          </ConnectionRecentStrip>

          <section
            v-if="liveConnections.length"
            class="db-connection-overview__live"
            :aria-label="t('database.overviewInUse')"
          >
            <div class="connection-overview__section">
              <h3 class="connection-overview__section-title">{{ t("database.overviewInUse") }}</h3>
              <span class="connection-overview__section-count">{{ liveConnections.length }}</span>
            </div>
            <div class="db-connection-overview__live-grid">
              <button
                v-for="connection in liveConnections"
                :key="connection.id"
                type="button"
                class="db-connection-overview__live-card"
                :aria-label="connection.name"
                @click="emit('open', connection)"
              >
                <ConnectionAvatar
                  class="db-connection-overview__avatar"
                  :style="brandStyle(connection)"
                  :name="connection.name"
                  :seed="connection.id"
                  status="online"
                  :size="26"
                >
                  <DatabaseTypeIcon :db-type="connection.dbType" size="small" />
                </ConnectionAvatar>
                <span class="db-connection-overview__live-text">
                  <strong class="connection-overview__name">{{ connection.name }}</strong>
                  <span class="connection-overview__muted">
                    {{ [connectionTypeLabel(connection), loadedDatabasesLabel(connection)].join(" · ") }}
                  </span>
                </span>
                <span class="db-connection-overview__live-since">{{ connectedForLabel(connection) }}</span>
              </button>
            </div>
          </section>

          <section
            ref="connectionsSection"
            class="db-connection-overview__connections"
            :aria-label="t('database.overviewAllConnections')"
          >
            <div class="connection-overview__section">
              <h3 class="connection-overview__section-title">{{ t("database.overviewAllConnections") }}</h3>
              <span class="connection-overview__section-count">{{ filteredConnections.length }}</span>
              <div class="connection-overview__section-action db-connection-overview__filters">
                <!-- "" is the "all groups" option; keep it a real value so its label shows. -->
                <el-select
                  v-model="selectedGroup"
                  :empty-values="[null, undefined]"
                  size="small"
                  :aria-label="t('database.overviewGroupFilter')"
                >
                  <el-option :label="t('database.overviewAllGroups')" value="" />
                  <el-option v-for="group in groupOptions" :key="group" :label="group" :value="group" />
                </el-select>
              </div>
            </div>

            <el-empty
              v-if="!groupedConnections.length"
              class="db-connection-overview__empty"
              :image-size="72"
              :description="t('database.overviewNoMatches')"
            />

            <div v-else class="connection-overview__table">
              <div
                v-for="group in groupedConnections"
                :key="group.key"
                role="group"
                class="db-connection-overview__group"
                :aria-label="group.label"
              >
                <button
                  type="button"
                  class="connection-overview__group"
                  :aria-expanded="!groupIsCollapsed(group.key)"
                  @click="toggleGroup(group.key)"
                >
                  <el-icon aria-hidden="true"><ArrowRight v-if="groupIsCollapsed(group.key)" /><ArrowDown v-else /></el-icon>
                  <span>{{ group.label }}</span>
                  <span class="connection-overview__section-count">{{ group.items.length }}</span>
                </button>
                <div v-if="!groupIsCollapsed(group.key)" role="list">
                  <div
                    v-for="connection in group.items"
                    :key="connection.id"
                    role="listitem"
                    tabindex="0"
                    class="connection-overview__row is-clickable"
                    :class="{ 'is-highlighted': connection.id === highlightedConnectionId }"
                    @click="selectRow(connection)"
                    @focus="selectRow(connection)"
                    @dblclick="activateConnection(connection)"
                    @keydown.enter.self.prevent="activateConnection(connection)"
                  >
                    <span
                      class="connection-overview__dot"
                      :class="statusClass(connection)"
                      :title="statusLabel(connection)"
                    >
                      <span class="connection-overview__sr-only">{{ statusLabel(connection) }}</span>
                    </span>
                    <span class="db-connection-overview__name">
                      <span class="connection-overview__name" :title="connection.name">{{ connection.name }}</span>
                      <el-icon
                        v-if="connection.favorite"
                        class="db-connection-overview__favorite"
                        :aria-label="t('database.overviewFavorites')"
                      ><StarFilled /></el-icon>
                    </span>
                    <span
                      class="db-connection-overview__type"
                      :class="{ 'is-driver-unavailable': driverUnavailable(connection) }"
                      :title="connectionTypeLabel(connection)"
                    >
                      <i
                        class="db-connection-overview__type-swatch"
                        :style="{ background: databaseConnectionBrand(connection).color }"
                        aria-hidden="true"
                      />
                      <span>{{ connectionTypeLabel(connection) }}</span>
                    </span>
                    <span class="connection-overview__mono" :title="endpoint(connection)">{{ endpoint(connection) }}</span>
                    <span class="connection-overview__muted connection-overview__row-optional">
                      {{ lastConnectedLabel(connection) }}
                    </span>
                    <el-button
                      link
                      type="primary"
                      class="db-connection-overview__action"
                      :loading="isConnecting(connection)"
                      @click.stop="onRowAction($event, connection)"
                      @dblclick.stop
                    >
                      {{ t("database.overviewOpen") }}
                    </el-button>
                  </div>
                </div>
              </div>
            </div>
          </section>
        </template>
      </div>
    </el-scrollbar>
  </section>
</template>

<style scoped>
.db-connection-overview {
  display: flex;
  min-width: 0;
  min-height: 0;
  flex: 1;
  overflow: hidden;
  background: var(--app-surface);
  /* status dot · name · type · endpoint · last connected · action */
  --connection-overview-row-columns: 12px minmax(110px, 1.2fr) minmax(80px, 0.8fr) minmax(0, 1.5fr) minmax(72px, 0.7fr)
    64px;
  --connection-overview-row-columns-narrow: 12px minmax(0, 1fr) minmax(0, 0.8fr) minmax(0, 1fr) 64px;
}

.db-connection-overview__scroll {
  min-height: 0;
  flex: 1;
}

.db-connection-overview__content {
  min-height: 100%;
  padding: 18px 26px 28px;
}

/* Group filter sits at the right end of the "All connections" heading. */
.db-connection-overview__filters {
  display: flex;
  align-items: center;
}

.db-connection-overview__filters .el-select {
  width: 150px;
}

/* Type icon tile: a light wash of the brand color so the icon keeps its own color in both themes. */
.db-connection-overview__avatar.connection-avatar {
  background: color-mix(in srgb, var(--db-overview-brand, var(--app-accent)) 14%, var(--app-surface));
}

/* "In use": one compact card per connected database. */
.db-connection-overview__live-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(min(260px, 100%), 1fr));
  gap: 10px;
}

.db-connection-overview__live-card {
  display: flex;
  min-width: 0;
  align-items: center;
  gap: 10px;
  border: 1px solid var(--app-success-border);
  border-radius: var(--connection-overview-radius);
  padding: 10px 12px;
  background: var(--app-success-soft);
  color: var(--app-text);
  font: inherit;
  text-align: left;
  cursor: pointer;
  transition: border-color 0.15s ease;
}

.db-connection-overview__live-card:hover {
  border-color: var(--app-success);
}

.db-connection-overview__live-card:focus-visible {
  outline: none;
  border-color: var(--app-accent);
  box-shadow: 0 0 0 2px var(--app-accent-ring);
}

.db-connection-overview__live-text {
  display: flex;
  min-width: 0;
  flex: 1;
  flex-direction: column;
  gap: 1px;
}

.db-connection-overview__live-text .connection-overview__name {
  font-size: 13px;
}

.db-connection-overview__live-since {
  flex: none;
  color: var(--app-success);
  font-size: 11.5px;
  white-space: nowrap;
}

/* "All connections" table: each group wraps its header and rows, so restore the row divider between groups. */
.db-connection-overview__group:not(:last-child) .connection-overview__row:last-child {
  border-bottom: 1px solid var(--app-border);
}

.db-connection-overview__group:last-child > .connection-overview__group:last-child {
  border-bottom: 0;
}

.db-connection-overview__name {
  display: flex;
  min-width: 0;
  align-items: center;
  gap: 4px;
}

.db-connection-overview__favorite {
  flex: none;
  color: var(--app-warning);
}

.db-connection-overview__type {
  display: flex;
  min-width: 0;
  align-items: center;
  gap: 6px;
  color: var(--app-text-muted);
  font-size: 12px;
}

.db-connection-overview__type > span {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.db-connection-overview__type.is-driver-unavailable {
  color: var(--app-warning);
}

.db-connection-overview__type-swatch {
  width: 8px;
  height: 8px;
  flex: none;
  border-radius: 2px;
}

.db-connection-overview__action.el-button {
  justify-self: end;
  font-size: 12px;
  font-weight: 600;
}

.db-connection-overview__empty {
  min-height: 240px;
}

@media (prefers-reduced-motion: reduce) {
  .db-connection-overview__live-card {
    transition: none;
  }
}

@media (max-width: 680px) {
  .db-connection-overview__content {
    padding-right: 14px;
    padding-left: 14px;
  }

  .db-connection-overview__filters .el-select {
    width: 120px;
  }

  .db-connection-overview__live-since {
    display: none;
  }
}
</style>

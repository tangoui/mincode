<script setup lang="ts">
/**
 * Import connections saved by other tools (DBeaver, Navicat, DataGrip,
 * MySQL Workbench, HeidiSQL) or pasted as URLs. Tool files are read and
 * decrypted in Go only after the user picks a location; the preview holds no
 * passwords. On confirm the backend stores the selected rows with encrypted
 * secrets; pasted URLs go through the regular saveConnection path.
 */
import { computed, nextTick, onBeforeUnmount, ref, watch } from "vue";
import { ElMessage, type TableInstance } from "element-plus";
import { Document, FolderOpened } from "@element-plus/icons-vue";
import {
  DATABASE_IMPORT_FILE_SOURCES,
  DATABASE_IMPORT_MAX_URL_LINES,
  databaseConnectionFromImportUrl,
  databaseImportCommitRows,
  databaseImportErrorCode,
  databaseImportPickers,
  databaseImportSourceLabels,
  databaseImportTargetGroup,
  parseDatabaseImportUrls,
  resolveDatabaseImportRows,
  type DatabaseImportPreview,
  type DatabaseImportSource,
  type DetectedImportLocation,
  type ResolvedDatabaseImportRow,
} from "@/domain/database/connection-import";
import { databaseTypeLabels, type DbType } from "@/domain/database/types";
import { useVueI18n, type MessageKey } from "@/i18n/messages";
import { loadSupportedDatabaseTypes } from "@/services/database-capabilities";
import {
  commitDatabaseImport,
  detectDatabaseImportLocations,
  discardDatabaseImport,
  previewDatabaseImport,
} from "@/services/database-import-service";
import { chooseNativeDirectory, chooseNativeFile, chooseNativeFileOrDirectory, isDesktopRuntime } from "@/services/desktop-bridge";
import { useDatabaseDriversStore } from "@/stores/database-drivers";
import { useDatabaseWorkspaceStore } from "@/stores/database-workspace";

const props = defineProps<{ modelValue: boolean }>();
const emit = defineEmits<{
  "update:modelValue": [value: boolean];
  imported: [ids: string[]];
}>();

const { t } = useVueI18n();
const databaseStore = useDatabaseWorkspaceStore();
const driversStore = useDatabaseDriversStore();
const desktop = isDesktopRuntime();

const sourceOptions: DatabaseImportSource[] = [...DATABASE_IMPORT_FILE_SOURCES, "url"];
const source = ref<DatabaseImportSource>(desktop ? "dbeaver" : "url");
const detected = ref<DetectedImportLocation[]>([]);
const preview = ref<DatabaseImportPreview>();
const urlText = ref("");
const loading = ref(false);
const committing = ref(false);
const errorMessage = ref("");
const group = ref("");
const keepFolders = ref(true);
const selectedIds = ref(new Set<string>());
const supportedTypes = ref<Set<DbType> | null>(null);
const tableRef = ref<TableInstance>();
let previewRequest = 0;
// The group filled in from the source name; cleared again when the source changes.
let autoGroup = "";

const fileSource = computed(() => (source.value === "url" ? undefined : source.value));
const detectedForSource = computed(() => detected.value.filter((item) => item.source === source.value));
const urlResult = computed(() => (source.value === "url" ? parseDatabaseImportUrls(urlText.value) : undefined));
const groupOptions = computed(() =>
  [...new Set(databaseStore.connections.map((connection) => connection.group?.trim()).filter(Boolean) as string[])].sort(),
);

const rows = computed<ResolvedDatabaseImportRow[]>(() => {
  const candidates =
    source.value === "url"
      ? (urlResult.value?.rows.map((row) => row.candidate) ?? [])
      : preview.value?.source === source.value
        ? preview.value.rows
        : [];
  return resolveDatabaseImportRows(candidates, {
    connections: databaseStore.connections,
    drivers: driversStore.drivers,
    supportedTypes: supportedTypes.value,
  });
});
const selectedCount = computed(() => rows.value.filter((row) => row.status === "ready" && selectedIds.value.has(row.candidate.id)).length);
const hasFolders = computed(() => rows.value.some((row) => row.candidate.folder));

const sourceHints: Record<DatabaseImportSource, MessageKey> = {
  dbeaver: "database.import.hint.dbeaver",
  navicat: "database.import.hint.navicat",
  datagrip: "database.import.hint.datagrip",
  workbench: "database.import.hint.workbench",
  heidisql: "database.import.hint.heidisql",
  url: "database.import.hint.url",
};

const warningKeys: Record<string, MessageKey> = {
  tunnel_incomplete: "database.import.warning.tunnel_incomplete",
  tunnel_unsupported: "database.import.warning.tunnel_unsupported",
  tunnel_partial: "database.import.warning.tunnel_partial",
  host_missing: "database.import.warning.host_missing",
  password_undecryptable: "database.import.warning.password_undecryptable",
  credentials_missing: "database.import.warning.credentials_missing",
  credentials_unreadable: "database.import.warning.credentials_unreadable",
  url_unparsed: "database.import.warning.url_unparsed",
  socket_unsupported: "database.import.warning.socket_unsupported",
  truncated: "database.import.warning.truncated",
  entries_skipped: "database.import.warning.entries_skipped",
};

const errorKeys: Record<string, MessageKey> = {
  path_invalid: "database.import.error.path_invalid",
  not_found: "database.import.error.not_found",
  read_failed: "database.import.error.read_failed",
  not_a_file: "database.import.error.not_a_file",
  file_too_large: "database.import.error.file_too_large",
  malformed: "database.import.error.malformed",
  session_expired: "database.import.error.session_expired",
  driver_required: "database.import.error.driver_required",
  store_failed: "database.import.error.store_failed",
};

function warningText(code: string) {
  const key = warningKeys[code];
  return key ? t(key) : code;
}

function errorText(error: unknown) {
  const code = databaseImportErrorCode(error);
  const key = code ? errorKeys[code] : undefined;
  if (key) return t(key);
  return t("database.import.error.generic", { message: error instanceof Error ? error.message : String(error) });
}

function typeLabel(row: ResolvedDatabaseImportRow) {
  const { candidate } = row;
  if (candidate.dbType) return databaseTypeLabels[candidate.dbType];
  return candidate.pluginDriverId ?? candidate.sourceType;
}

function endpoint(row: ResolvedDatabaseImportRow) {
  const { candidate } = row;
  if (candidate.filePath && !candidate.host) return candidate.filePath;
  const address = candidate.port ? `${candidate.host}:${candidate.port}` : candidate.host;
  return [address, candidate.database].filter(Boolean).join(" / ");
}

function credentialText(row: ResolvedDatabaseImportRow) {
  const { candidate } = row;
  if (candidate.hasPassword) return t("database.import.password.included");
  if (candidate.passwordUnavailable) return t("database.import.password.needed");
  return t("database.import.password.none");
}

function tunnelText(row: ResolvedDatabaseImportRow) {
  const tunnel = row.candidate.tunnel;
  // Plugin drivers do not use MinCode tunnels; the commit drops them.
  if (!tunnel || row.driver || row.status === "needsPlugin") return "";
  const user = tunnel.username ? `${tunnel.username}@` : "";
  return `${tunnel.type === "ssh" ? "SSH" : "SOCKS5"} ${user}${tunnel.host}:${tunnel.port}`;
}

function statusTag(row: ResolvedDatabaseImportRow): { type: "success" | "warning" | "info" | "danger"; text: string } {
  if (row.status === "unsupported") {
    return { type: "info", text: t("database.import.status.unsupported") };
  }
  if (row.status === "needsPlugin") {
    return {
      type: "warning",
      text: row.requiredDriverId
        ? t("database.import.status.needsPlugin", { driver: row.requiredDriverId })
        : t("database.import.status.needsPluginUnknown"),
    };
  }
  if (row.duplicateOf) return { type: "danger", text: t("database.import.status.duplicate", { name: row.duplicateOf }) };
  if (row.driver) return { type: "success", text: t("database.import.status.plugin", { plugin: row.driver.pluginId }) };
  return { type: "success", text: t("database.import.status.ready") };
}

function selectable(row: ResolvedDatabaseImportRow) {
  return row.status === "ready";
}

function onSelectionChange(selection: ResolvedDatabaseImportRow[]) {
  selectedIds.value = new Set(selection.map((row) => row.candidate.id));
}

// Default selection follows each fresh set of rows (ready, not duplicate).
watch(
  () => rows.value.map((row) => `${row.candidate.id}:${row.defaultSelected}`).join(","),
  async () => {
    await nextTick();
    const table = tableRef.value;
    const next = new Set<string>();
    table?.clearSelection();
    for (const row of rows.value) {
      if (!row.defaultSelected) continue;
      next.add(row.candidate.id);
      table?.toggleRowSelection(row, true);
    }
    selectedIds.value = next;
  },
);

function dropPreview() {
  const id = preview.value?.importId;
  preview.value = undefined;
  if (id) void discardDatabaseImport(id);
}

async function loadPreview(path: string) {
  const current = fileSource.value;
  if (!current || !path) return;
  const request = ++previewRequest;
  dropPreview();
  errorMessage.value = "";
  loading.value = true;
  try {
    const result = await previewDatabaseImport(current, path);
    if (request !== previewRequest || source.value !== current) {
      void discardDatabaseImport(result.importId);
      return;
    }
    preview.value = result;
    if (!group.value || group.value === autoGroup) {
      autoGroup = databaseImportSourceLabels[current];
      group.value = autoGroup;
    }
  } catch (error) {
    if (request === previewRequest) errorMessage.value = errorText(error);
  } finally {
    if (request === previewRequest) loading.value = false;
  }
}

async function choosePath() {
  const current = fileSource.value;
  if (!current) return;
  const title = t("database.import.chooseTitle", { source: databaseImportSourceLabels[current] });
  const picker = databaseImportPickers[current];
  let path: string | null;
  try {
    if (picker === "directory") path = await chooseNativeDirectory(title);
    else if (picker === "either") path = await chooseNativeFileOrDirectory(title);
    else if (current === "navicat") path = await chooseNativeFile(title, [{ DisplayName: "Navicat (*.ncx)", Pattern: "*.ncx" }]);
    else if (current === "workbench") path = await chooseNativeFile(title, [{ DisplayName: "connections.xml", Pattern: "*.xml" }]);
    else path = await chooseNativeFile(title);
  } catch (error) {
    errorMessage.value = errorText(error);
    return;
  }
  if (path) await loadPreview(path);
}

async function chooseFolder() {
  const current = fileSource.value;
  if (!current) return;
  let path: string | null;
  try {
    path = await chooseNativeDirectory(t("database.import.chooseTitle", { source: databaseImportSourceLabels[current] }));
  } catch (error) {
    errorMessage.value = errorText(error);
    return;
  }
  if (path) await loadPreview(path);
}

watch(source, () => {
  previewRequest += 1;
  loading.value = false;
  errorMessage.value = "";
  if (group.value === autoGroup) group.value = "";
  autoGroup = "";
  dropPreview();
});

watch(
  () => props.modelValue,
  (visible) => {
    if (!visible) {
      previewRequest += 1;
      dropPreview();
      return;
    }
    errorMessage.value = "";
    void loadSupportedDatabaseTypes().then((types) => {
      supportedTypes.value = types;
    });
    if (desktop) {
      void detectDatabaseImportLocations()
        .then((locations) => {
          detected.value = locations;
        })
        .catch(() => {
          detected.value = [];
        });
    }
  },
  { immediate: true },
);

// The parent unmounts the dialog on close; drop the Go session (and its secrets) then too.
onBeforeUnmount(() => {
  previewRequest += 1;
  dropPreview();
});

function close() {
  emit("update:modelValue", false);
}

async function importSelected() {
  if (!selectedCount.value || committing.value) return;
  committing.value = true;
  errorMessage.value = "";
  try {
    let ids: string[] = [];
    if (source.value === "url") {
      const now = Date.now();
      const byId = new Map(urlResult.value?.rows.map((row) => [row.candidate.id, row]) ?? []);
      for (const row of rows.value) {
        const urlRow = byId.get(row.candidate.id);
        if (!urlRow || row.status !== "ready" || !selectedIds.value.has(row.candidate.id)) continue;
        const connection = databaseConnectionFromImportUrl(urlRow, row, { id: crypto.randomUUID(), group: group.value, now });
        await databaseStore.saveConnection(connection);
        ids.push(connection.id);
      }
      await databaseStore.refreshDriverStatuses(ids);
      urlText.value = "";
    } else if (preview.value) {
      const current = preview.value;
      const commitRows = databaseImportCommitRows(rows.value, selectedIds.value);
      const added = await databaseStore.importConnectionsFromBackend(async () => {
        const result = await commitDatabaseImport({
          importId: current.importId,
          group: group.value.trim(),
          keepFolders: keepFolders.value,
          rows: commitRows,
        });
        return result.connectionIds;
      });
      ids = added.map((connection) => connection.id);
      preview.value = undefined;
    }
    ElMessage.success(t("database.import.done", { count: ids.length }));
    emit("imported", ids);
    close();
  } catch (error) {
    errorMessage.value = errorText(error);
  } finally {
    committing.value = false;
  }
}

function rowGroup(row: ResolvedDatabaseImportRow) {
  return databaseImportTargetGroup(group.value, row.candidate.folder, keepFolders.value && source.value !== "url");
}
</script>

<template>
  <el-dialog
    append-to-body
    class="db-import-dialog"
    :model-value="modelValue"
    :title="t('database.import.title')"
    width="min(920px, calc(100vw - 32px))"
    @update:model-value="emit('update:modelValue', $event)"
  >
    <div class="db-import">
      <div class="db-import__source">
        <el-radio-group v-model="source" size="small">
          <el-radio-button v-for="item in sourceOptions" :key="item" :value="item" :disabled="item !== 'url' && !desktop">
            {{ item === "url" ? t("database.import.sourceUrl") : databaseImportSourceLabels[item] }}
          </el-radio-button>
        </el-radio-group>
        <el-tag v-if="!desktop" size="small" type="info" effect="plain">{{ t("database.import.desktopOnly") }}</el-tag>
      </div>
      <p class="db-import__hint">{{ t(sourceHints[source]) }}</p>

      <template v-if="fileSource">
        <div class="db-import__pick">
          <el-button size="small" :icon="Document" :disabled="!desktop || loading" @click="choosePath">
            {{ t("database.import.chooseFile") }}
          </el-button>
          <el-button
            v-if="databaseImportPickers[fileSource] !== 'file'"
            size="small"
            :icon="FolderOpened"
            :disabled="!desktop || loading"
            @click="chooseFolder"
          >
            {{ t("database.import.chooseFolder") }}
          </el-button>
          <span v-if="preview" class="db-import__path" :title="preview.path">{{ preview.path }}</span>
        </div>
        <div v-if="detectedForSource.length" class="db-import__detected">
          <span class="db-import__label">{{ t("database.import.detected") }}</span>
          <el-button
            v-for="location in detectedForSource"
            :key="location.path"
            size="small"
            text
            bg
            :title="location.path"
            :disabled="loading"
            @click="loadPreview(location.path)"
          >
            {{ location.label }}
          </el-button>
        </div>
      </template>
      <template v-else>
        <el-input
          v-model="urlText"
          type="textarea"
          :rows="4"
          :placeholder="t('database.import.urlPlaceholder')"
          :aria-label="t('database.import.sourceUrl')"
        />
        <p v-if="urlResult?.invalidLines.length" class="db-import__warning">
          {{ t("database.import.urlInvalid", { lines: urlResult.invalidLines.slice(0, 10).join(", ") }) }}
        </p>
        <p v-if="urlResult?.truncated" class="db-import__warning">
          {{ t("database.import.urlTruncated", { count: DATABASE_IMPORT_MAX_URL_LINES }) }}
        </p>
      </template>

      <el-alert v-if="errorMessage" type="error" :closable="false" show-icon :title="errorMessage" />
      <el-alert
        v-for="code in preview?.warnings ?? []"
        :key="code"
        type="warning"
        :closable="false"
        show-icon
        :title="warningText(code)"
      />

      <el-table
        ref="tableRef"
        v-loading="loading"
        class="db-import__table"
        :data="rows"
        row-key="candidate.id"
        size="small"
        height="320"
        :empty-text="t('database.import.empty')"
        @selection-change="onSelectionChange"
      >
        <el-table-column type="selection" width="36" :selectable="selectable" />
        <el-table-column :label="t('database.import.column.name')" min-width="170">
          <template #default="{ row }">
            <div class="db-import__name">{{ row.candidate.name }}</div>
            <small v-if="rowGroup(row)" class="db-import__muted">{{ rowGroup(row) }}</small>
          </template>
        </el-table-column>
        <el-table-column :label="t('database.import.column.type')" min-width="110">
          <template #default="{ row }">
            <div>{{ typeLabel(row) }}</div>
            <small class="db-import__muted">{{ row.candidate.sourceType }}</small>
          </template>
        </el-table-column>
        <el-table-column :label="t('database.import.column.endpoint')" min-width="190">
          <template #default="{ row }">
            <div class="db-import__ellipsis" :title="endpoint(row)">{{ endpoint(row) }}</div>
            <small v-if="row.candidate.username" class="db-import__muted">{{ row.candidate.username }}</small>
          </template>
        </el-table-column>
        <el-table-column :label="t('database.import.column.credentials')" min-width="120">
          <template #default="{ row }">
            <div>{{ credentialText(row) }}</div>
            <small v-if="tunnelText(row)" class="db-import__muted db-import__ellipsis" :title="tunnelText(row)">
              {{ tunnelText(row) }}
            </small>
          </template>
        </el-table-column>
        <el-table-column :label="t('database.import.column.status')" min-width="150">
          <template #default="{ row }">
            <el-tag size="small" :type="statusTag(row).type" effect="plain" disable-transitions>
              {{ statusTag(row).text }}
            </el-tag>
            <small v-for="code in row.candidate.warnings" :key="code" class="db-import__warning db-import__row-warning">
              {{ warningText(code) }}
            </small>
          </template>
        </el-table-column>
      </el-table>

      <el-form class="db-import__target" label-position="left" size="small" @submit.prevent>
        <el-form-item :label="t('database.import.group')">
          <el-select
            v-model="group"
            class="db-import__group"
            filterable
            allow-create
            clearable
            default-first-option
            :placeholder="t('database.import.groupPlaceholder')"
          >
            <el-option v-for="item in groupOptions" :key="item" :label="item" :value="item" />
          </el-select>
        </el-form-item>
        <el-checkbox v-if="fileSource && hasFolders" v-model="keepFolders">{{ t("database.import.keepFolders") }}</el-checkbox>
      </el-form>
    </div>
    <template #footer>
      <span class="db-import__footnote">{{ t("database.import.footnote") }}</span>
      <el-button @click="close">{{ t("common.cancel") }}</el-button>
      <el-button type="primary" :disabled="!selectedCount" :loading="committing" @click="importSelected">
        {{ t("database.import.confirm", { count: selectedCount }) }}
      </el-button>
    </template>
  </el-dialog>
</template>

<style scoped>
.db-import {
  display: flex;
  flex-direction: column;
  gap: 10px;
  min-width: 0;
}
.db-import__source,
.db-import__pick,
.db-import__detected {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px;
  min-width: 0;
}
.db-import__detected .el-button + .el-button {
  margin-left: 0;
}
.db-import__hint {
  margin: 0;
  color: var(--app-text-muted);
  font-size: 12px;
  line-height: 1.5;
}
.db-import__label,
.db-import__muted {
  color: var(--app-text-muted);
  font-size: 12px;
}
.db-import__path {
  min-width: 0;
  flex: 1;
  overflow: hidden;
  color: var(--app-text-muted);
  font-size: 12px;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.db-import__table {
  width: 100%;
  border: 1px solid var(--app-border);
  border-radius: 6px;
}
.db-import__name {
  color: var(--app-text);
  font-weight: 500;
}
.db-import__ellipsis {
  display: block;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.db-import__warning {
  margin: 0;
  color: var(--el-color-warning);
  font-size: 12px;
}
.db-import__row-warning {
  display: block;
  margin-top: 2px;
  line-height: 1.4;
}
.db-import__target {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 12px;
}
.db-import__target .el-form-item {
  margin-bottom: 0;
}
.db-import__group {
  width: 240px;
}
.db-import__footnote {
  float: left;
  max-width: 55%;
  color: var(--app-text-muted);
  font-size: 12px;
  line-height: 32px;
  text-align: left;
}
</style>

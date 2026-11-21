<script setup lang="ts">
import { computed, defineAsyncComponent, onBeforeUnmount, ref, watch } from "vue";
import { Close, DocumentCopy, EditPen, Refresh } from "@element-plus/icons-vue";
import { ElMessage } from "element-plus";
import AppSearchField from "@/components/ui/AppSearchField.vue";
import DatabaseObjectTable from "@/components/database/DatabaseObjectTable.vue";
import AppSplitPane from "@/components/ui/AppSplitPane.vue";
import type { DatabaseTableDetails, DbType, SchemaTable } from "@/domain/database/types";
import { indexesWithPrimaryKey, resolvePrimaryKeyColumns } from "@/domain/database/table-index";
import { tableStructureCapabilities } from "@/domain/database/table-structure-capabilities";
import type { DatabaseDriverDescriptor } from "@/domain/database/driver-descriptor";
import { useVueI18n } from "@/i18n/messages";
import { copyText } from "@/services/desktop-bridge";

const ReadonlyCodeViewer = defineAsyncComponent(() => import("@/components/ui/ReadonlyCodeViewer.vue"));

type ObjectFilter = "all" | "table" | "view";
type ObjectSort = "name" | "rows" | "size";

const props = defineProps<{
  database: string;
  databases: string[];
  objects: SchemaTable[];
  selectedName?: string;
  details?: DatabaseTableDetails;
  detailsLoading?: boolean;
  dbType?: DbType;
  /** 插件驱动描述符：存在时按其 metadata.* features 判断结构能力，dbType 为方言代表类型。 */
  driverDescriptor?: DatabaseDriverDescriptor;
}>();

const emit = defineEmits<{
  refresh: [];
  switchDatabase: [database: string];
  select: [object: SchemaTable];
  open: [object: SchemaTable];
  editStructure: [object: SchemaTable];
  closeDetails: [];
  contextMenu: [payload: { object: SchemaTable; x: number; y: number }];
}>();

const { t } = useVueI18n();
// 概览面板会被 v-if 卸载（切到表/查询标签时），关键字存在父组件里才不会丢。
const searchText = defineModel<string>("searchText", { required: true });
const debouncedSearchText = ref(searchText.value);
const objectFilter = ref<ObjectFilter>("all");
const objectSort = ref<ObjectSort>("name");
const detailsTab = ref("ddl");
const detailsWidth = ref(52);
const objectNameCollator = new Intl.Collator(undefined, {
  numeric: true,
  sensitivity: "base",
});
let searchTimer: ReturnType<typeof setTimeout> | undefined;

const selectedObject = computed(() =>
  props.objects.find((object) => object.name === props.selectedName)
);
const capabilities = computed(() => tableStructureCapabilities(props.dbType, props.driverDescriptor));
const primaryKeys = computed(() =>
  resolvePrimaryKeyColumns(props.details?.primaryKeys ?? [], props.details?.columns ?? [])
);
const primaryKeySet = computed(() => new Set(primaryKeys.value));
const indexes = computed(() =>
  indexesWithPrimaryKey(
    props.details?.indexes ?? [],
    primaryKeys.value,
    props.details?.primaryKeyName
  )
);
const counts = computed(() => {
  let tables = 0;
  let views = 0;
  for (const object of props.objects) {
    if (normalizedKind(object) === "view") views += 1;
    else tables += 1;
  }
  return { all: props.objects.length, table: tables, view: views };
});
const filterOptions = computed(() => [
  { label: `${t("database.allObjects")} ${counts.value.all}`, value: "all" },
  { label: `${t("database.tables")} ${counts.value.table}`, value: "table" },
  { label: `${t("database.views")} ${counts.value.view}`, value: "view" },
]);
const filteredObjects = computed(() => {
  const keyword = debouncedSearchText.value.trim().toLocaleLowerCase();
  const objects = props.objects.filter((object) => {
    const kind = normalizedKind(object);
    if (objectFilter.value !== "all" && kind !== objectFilter.value) return false;
    if (!keyword) return true;
    return [object.name, object.kind, object.comment]
      .filter(Boolean)
      .some((value) => String(value).toLocaleLowerCase().includes(keyword));
  });
  return [...objects].sort((left, right) => {
    if (objectSort.value === "rows") {
      return compareOptionalNumberDesc(left.rowCount, right.rowCount) || compareName(left, right);
    }
    if (objectSort.value === "size") {
      return compareOptionalNumberDesc(left.sizeBytes, right.sizeBytes) || compareName(left, right);
    }
    return compareName(left, right);
  });
});

watch(
  () => props.selectedName,
  () => {
    detailsTab.value = capabilities.value.ddl ? "ddl" : "columns";
  }
);

watch(
  searchText,
  (value) => {
    if (searchTimer !== undefined) clearTimeout(searchTimer);
    if (!value.trim()) {
      debouncedSearchText.value = "";
      searchTimer = undefined;
      return;
    }
    searchTimer = setTimeout(() => {
      searchTimer = undefined;
      debouncedSearchText.value = value;
    }, 120);
  },
  { immediate: true }
);

onBeforeUnmount(() => {
  if (searchTimer !== undefined) clearTimeout(searchTimer);
});

function normalizedKind(object: SchemaTable) {
  const kind = object.kind.toLocaleLowerCase();
  return kind.includes("view") ? "view" : "table";
}

function compareName(left: SchemaTable, right: SchemaTable) {
  return objectNameCollator.compare(left.name, right.name);
}

function compareOptionalNumberDesc(left?: number | null, right?: number | null) {
  if (left == null && right == null) return 0;
  if (left == null) return 1;
  if (right == null) return -1;
  return right - left;
}

async function copyDdl() {
  const ddl = props.details?.ddl?.trim();
  if (!ddl) return;
  try {
    await copyText(ddl);
    ElMessage.success(t("database.ddlCopied"));
  } catch (cause) {
    ElMessage.error(t("database.copyFailed", { message: String(cause) }));
  }
}
</script>

<template>
  <section class="db-object-browser">
    <header class="db-object-browser__toolbar">
      <el-select
        filterable
        class="db-object-browser__database"
        :model-value="database"
        :aria-label="t('database.switchDatabase')"
        :title="database"
        @change="emit('switchDatabase', $event)"
      >
        <el-option
          v-for="databaseName in databases"
          :key="databaseName"
          :label="databaseName"
          :value="databaseName"
        />
      </el-select>
      <AppSearchField
        v-model="searchText"
        class="db-object-browser__search"
        :placeholder="t('database.searchDatabaseObjects')"
        :clearable="true"
        :clear-label="t('database.clearObjectSearch')"
      />
      <el-segmented
        v-model="objectFilter"
        class="db-object-browser__filters"
        :options="filterOptions"
        :aria-label="t('database.objectTypeFilter')"
      />
      <el-select
        v-model="objectSort"
        class="db-object-browser__sort"
        :aria-label="t('database.objectSort')"
      >
        <el-option :label="t('database.sortByName')" value="name" />
        <el-option :label="t('database.sortByRows')" value="rows" />
        <el-option :label="t('database.sortBySize')" value="size" />
      </el-select>
      <el-button
        text
        native-type="button"
        :title="t('database.refreshObjects')"
        :aria-label="t('database.refreshObjects')"
        @click="emit('refresh')"
      >
        <el-icon><Refresh /></el-icon>
      </el-button>
    </header>

    <AppSplitPane
      v-model="detailsWidth"
      class="db-object-browser__split"
      side="second"
      :collapsed="!selectedObject"
      unit="percent"
      :min="34"
      :max="70"
      :second-min="360"
      :aria-label="t('database.objectDetailsResize')"
    >
      <template #first>
        <div class="db-object-browser__list">
          <DatabaseObjectTable
            :objects="filteredObjects"
            :selected-name="selectedName"
            @select="emit('select', $event)"
            @open="emit('open', $event)"
            @context-menu="emit('contextMenu', $event)"
          />
        </div>
      </template>

      <template #second>
        <aside v-if="selectedObject" class="db-object-details">
          <header class="db-object-details__header">
            <strong>{{ selectedObject.name }}</strong>
            <div>
              <el-button
                v-if="capabilities.ddl"
                text
                native-type="button"
                :disabled="!details?.ddl"
                @click="copyDdl"
              >
                <el-icon><DocumentCopy /></el-icon>{{ t("database.copyDdl") }}
              </el-button>
              <el-button
                v-if="normalizedKind(selectedObject) === 'table' && capabilities.schemaChangeSql"
                text
                native-type="button"
                @click="emit('editStructure', selectedObject)"
              >
                <el-icon><EditPen /></el-icon>{{ t("database.editTableStructure") }}
              </el-button>
              <el-button
                text
                circle
                native-type="button"
                :title="t('database.closeDetails')"
                :aria-label="t('database.closeDetails')"
                @click="emit('closeDetails')"
              >
                <el-icon><Close /></el-icon>
              </el-button>
            </div>
          </header>

          <el-tabs v-model="detailsTab" class="db-object-details__tabs" stretch>
            <el-tab-pane v-if="capabilities.ddl" name="ddl" :label="t('database.ddl')" />
            <el-tab-pane name="columns" :label="t('database.columns')" />
            <el-tab-pane v-if="capabilities.indexes" name="indexes" :label="t('database.indexes')" />
            <el-tab-pane v-if="capabilities.foreignKeys" name="foreignKeys" :label="t('database.foreignKeys')" />
            <el-tab-pane v-if="capabilities.triggers" name="triggers" :label="t('database.triggers')" />
          </el-tabs>

          <div v-loading="detailsLoading" class="db-object-details__body">
            <el-alert
              v-if="details?.loadErrors?.length"
              class="db-object-details__warning"
              type="warning"
              :closable="false"
              :title="t('database.tableStructurePartialError')"
            />
            <template v-if="detailsTab === 'ddl'">
              <ReadonlyCodeViewer
                v-if="details?.ddl"
                language="sql"
                editor-kind="ddl"
                class="db-object-details__ddl-editor"
                :value="details.ddl"
                :ariaLabel="t('database.ddl')"
              />
              <div v-else class="db-object-details__empty-ddl">{{ t('database.noDdl') }}</div>
            </template>
            <el-table
              v-else-if="detailsTab === 'columns'"
              :data="details?.columns ?? []"
              height="100%"
              :empty-text="t('database.noTableColumns')"
            >
              <el-table-column prop="name" :label="t('database.column')" min-width="150" />
              <el-table-column prop="dataType" :label="t('database.objectType')" min-width="135" />
              <el-table-column :label="t('database.primaryKey')" width="72" align="center">
                <template #default="{ row }">{{ primaryKeySet.has(row.name) ? "✓" : "" }}</template>
              </el-table-column>
              <el-table-column :label="t('database.nullable')" width="72" align="center">
                <template #default="{ row }">{{ row.nullable ? "✓" : "" }}</template>
              </el-table-column>
              <el-table-column prop="defaultValue" :label="t('database.defaultValue')" min-width="110">
                <template #default="{ row }">{{ row.defaultValue ?? "—" }}</template>
              </el-table-column>
              <el-table-column prop="comment" :label="t('database.comment')" min-width="150">
                <template #default="{ row }">{{ row.comment || "—" }}</template>
              </el-table-column>
            </el-table>
            <el-table
              v-else-if="detailsTab === 'indexes'"
              :data="indexes"
              height="100%"
              :empty-text="t('database.noIndexes')"
            >
              <el-table-column prop="name" :label="t('database.indexName')" min-width="160" />
              <el-table-column :label="t('database.indexColumns')" min-width="210">
                <template #default="{ row }">{{ row.columns.join(", ") }}</template>
              </el-table-column>
              <el-table-column :label="t('database.unique')" width="82" align="center">
                <template #default="{ row }">{{ row.unique ? "✓" : "" }}</template>
              </el-table-column>
            </el-table>
            <el-table
              v-else-if="detailsTab === 'foreignKeys'"
              :data="details?.foreignKeys ?? []"
              height="100%"
              :empty-text="t('database.noForeignKeys')"
            >
              <el-table-column prop="name" :label="t('database.foreignKeyName')" min-width="150" />
              <el-table-column :label="t('database.localColumns')" min-width="150">
                <template #default="{ row }">{{ row.columns.join(", ") }}</template>
              </el-table-column>
              <el-table-column prop="referencedTable" :label="t('database.referencedTable')" min-width="150" />
              <el-table-column :label="t('database.referencedColumns')" min-width="150">
                <template #default="{ row }">{{ row.referencedColumns.join(", ") }}</template>
              </el-table-column>
            </el-table>
            <el-table
              v-else
              :data="details?.triggers ?? []"
              height="100%"
              :empty-text="t('database.noTriggers')"
            >
              <el-table-column prop="name" :label="t('database.trigger')" min-width="180" />
              <el-table-column prop="timing" :label="t('database.timing')" min-width="100" />
              <el-table-column prop="event" :label="t('database.event')" min-width="120" />
            </el-table>
          </div>
        </aside>
      </template>
    </AppSplitPane>
  </section>
</template>

<script setup lang="ts">
import { computed, defineAsyncComponent, reactive, ref, watch } from "vue";
import { DocumentCopy } from "@element-plus/icons-vue";
import { ElMessage } from "element-plus";
import type { DatabaseColumnInfo, DatabaseIndexInfo, DatabaseTableDetails, DbType } from "@/domain/database/types";
import { columnDataTypeOptions, defaultColumnDataType } from "@/domain/database/column-data-types";
import {
  indexesWithPrimaryKey,
  resolvePrimaryKeyColumns,
  type DatabaseDisplayIndexInfo,
} from "@/domain/database/table-index";
import {
  buildCreateIndexStatements,
  buildDropIndexStatements,
  buildEditIndexStatements,
  databaseIndexSqlDialect,
  formatIndexChangeSql,
} from "@/domain/database/index-change-sql";
import { tableStructureCapabilities } from "@/domain/database/table-structure-capabilities";
import type { DatabaseDriverDescriptor } from "@/domain/database/driver-descriptor";
import { useVueI18n } from "@/i18n/messages";
import { copyText } from "@/services/desktop-bridge";

const ReadonlyCodeViewer = defineAsyncComponent(() => import("@/components/ui/ReadonlyCodeViewer.vue"));

const props = defineProps<{
  modelValue: boolean;
  database: string;
  table: string;
  loading?: boolean;
  details?: DatabaseTableDetails;
  dbType?: DbType;
  /** 插件驱动描述符：存在时按其 metadata.* features 判断结构能力，dbType 为方言代表类型。 */
  driverDescriptor?: DatabaseDriverDescriptor;
  applying?: boolean;
}>();

const emit = defineEmits<{
  "update:modelValue": [value: boolean];
  refresh: [];
  apply: [sql: string, statements?: string[]];
}>();

const { t } = useVueI18n();
const activeTab = ref("columns");
// 后端对非 SQL 会话/部分方言不提供 DDL、索引、外键、触发器内省，
// 隐藏对应标签页避免误导为加载失败。
const capabilities = computed(() => tableStructureCapabilities(props.dbType, props.driverDescriptor));
const indexMutationDialect = computed(() => databaseIndexSqlDialect(props.dbType));
const dataTypeOptions = computed(() => columnDataTypeOptions(props.dbType));
const displayPrimaryKeys = computed(() =>
  resolvePrimaryKeyColumns(props.details?.primaryKeys ?? [], props.details?.columns ?? [])
);
const displayIndexes = computed(() =>
  indexesWithPrimaryKey(
    props.details?.indexes ?? [],
    displayPrimaryKeys.value,
    props.details?.primaryKeyName
  )
);
const ddl = computed(() => props.details?.ddl || t("database.noDdl"));
const advancedSql = ref("");
const generatedSqlSnapshot = ref("");
const generatedStatements = ref<string[]>([]);
const visualDialogVisible = ref(false);
const visualAction = ref<"add" | "edit" | "delete">("add");
const originalColumnName = ref("");
const indexDialogVisible = ref(false);
const indexAction = ref<"add" | "edit" | "delete">("add");
const originalIndexName = ref("");
const indexDraft = reactive({
  name: "",
  columns: [] as string[],
  unique: false,
});
const indexEditUsesRecreate = computed(
  () =>
    indexAction.value === "edit" &&
    indexMutationDialect.value !== "mysql" &&
    !(
      indexMutationDialect.value === "sqlserver" &&
      originalIndexName.value === indexDraft.name.trim()
    )
);
const columnDraft = reactive({
  name: "",
  dataType: "VARCHAR(255)",
  nullable: true,
  defaultValue: "",
  comment: "",
});
const visualEditSupported = computed(() =>
  [
    "mysql",
    "mariadb",
    "tidb",
    "oceanbase",
    "polardb",
    "tdsql",
    "dolt",
    "greatsql",
    "postgres",
    "kingbase",
    "gaussdb",
    "opengauss",
    "highgo",
    "vastbase",
    "opentenbase",
    "gbase8c",
  ].includes(props.dbType ?? "")
);

watch(
  () => props.modelValue,
  (visible) => {
    if (visible) {
      activeTab.value = "columns";
      advancedSql.value = "";
      generatedSqlSnapshot.value = "";
      generatedStatements.value = [];
    }
  }
);

function quoteIdentifier(value: string) {
  if (["mysql", "mariadb", "tidb", "oceanbase", "polardb", "tdsql", "dolt", "greatsql"].includes(props.dbType ?? "")) {
    return `\`${value.replace(/`/g, "``")}\``;
  }
  return `"${value.replace(/"/g, '""')}"`;
}

function qualifiedTable() {
  const quotedTable = quoteIdentifier(props.table);
  if (props.dbType === "firebird") return quotedTable;
  return props.database ? `${quoteIdentifier(props.database)}.${quotedTable}` : quotedTable;
}

function sqlLiteral(value: string) {
  return `'${value.replace(/'/g, "''")}'`;
}

function columnDefinition() {
  const parts = [quoteIdentifier(columnDraft.name.trim()), columnDraft.dataType.trim()];
  parts.push(columnDraft.nullable ? "NULL" : "NOT NULL");
  if (columnDraft.defaultValue.trim()) parts.push(`DEFAULT ${columnDraft.defaultValue.trim()}`);
  if (columnDraft.comment.trim() && ["mysql", "mariadb", "tidb", "oceanbase", "polardb", "tdsql", "dolt", "greatsql"].includes(props.dbType ?? "")) {
    parts.push(`COMMENT ${sqlLiteral(columnDraft.comment.trim())}`);
  }
  return parts.join(" ");
}

function openAddColumn() {
  visualAction.value = "add";
  originalColumnName.value = "";
  Object.assign(columnDraft, {
    name: "",
    dataType: defaultColumnDataType(props.dbType),
    nullable: true,
    defaultValue: "",
    comment: "",
  });
  visualDialogVisible.value = true;
}

function openEditColumn(column: DatabaseColumnInfo) {
  visualAction.value = "edit";
  originalColumnName.value = column.name;
  Object.assign(columnDraft, {
    name: column.name,
    dataType: column.dataType,
    nullable: column.nullable,
    defaultValue: column.defaultValue ?? "",
    comment: column.comment ?? "",
  });
  visualDialogVisible.value = true;
}

function openDeleteColumn(column: DatabaseColumnInfo) {
  visualAction.value = "delete";
  originalColumnName.value = column.name;
  Object.assign(columnDraft, { name: column.name, dataType: column.dataType, nullable: column.nullable, defaultValue: "", comment: "" });
  visualDialogVisible.value = true;
}

function setGeneratedStatements(statements: string[]) {
  const sql = formatIndexChangeSql(statements);
  advancedSql.value = sql;
  generatedSqlSnapshot.value = sql;
  generatedStatements.value = [...statements];
  activeTab.value = "changeSql";
}

function indexSqlTarget() {
  if (!props.dbType) return undefined;
  return {
    dbType: props.dbType,
    database: props.database,
    table: props.table,
  };
}

function openAddIndex() {
  indexAction.value = "add";
  originalIndexName.value = "";
  Object.assign(indexDraft, { name: "", columns: [], unique: false });
  indexDialogVisible.value = true;
}

function openEditIndex(index: DatabaseDisplayIndexInfo) {
  if (index.primary) return;
  indexAction.value = "edit";
  originalIndexName.value = index.name;
  Object.assign(indexDraft, {
    name: index.name,
    columns: [...index.columns],
    unique: index.unique,
  });
  indexDialogVisible.value = true;
}

function openDeleteIndex(index: DatabaseDisplayIndexInfo) {
  if (index.primary) return;
  indexAction.value = "delete";
  originalIndexName.value = index.name;
  Object.assign(indexDraft, {
    name: index.name,
    columns: [...index.columns],
    unique: index.unique,
  });
  indexDialogVisible.value = true;
}

function duplicateIndexName(name: string) {
  const normalizedName = name.toLocaleLowerCase();
  return (props.details?.indexes ?? []).some((index) => {
    if (
      indexAction.value === "edit" &&
      index.name.toLocaleLowerCase() === originalIndexName.value.toLocaleLowerCase()
    ) {
      return false;
    }
    return index.name.toLocaleLowerCase() === normalizedName;
  });
}

function submitIndexChange() {
  const target = indexSqlTarget();
  if (!target) return ElMessage.warning(t("database.indexChangeUnsupported"));

  let statements: string[];
  if (indexAction.value === "delete") {
    statements = buildDropIndexStatements(target, originalIndexName.value);
  } else {
    const index: DatabaseIndexInfo = {
      name: indexDraft.name.trim(),
      columns: [...indexDraft.columns],
      unique: indexDraft.unique,
    };
    if (!index.name || !index.columns.length) {
      return ElMessage.warning(t("database.indexDefinitionRequired"));
    }
    if (duplicateIndexName(index.name)) {
      return ElMessage.warning(t("database.indexNameExists", { name: index.name }));
    }
    statements =
      indexAction.value === "add"
        ? buildCreateIndexStatements(target, index)
        : buildEditIndexStatements(target, originalIndexName.value, index);
  }

  setGeneratedStatements(statements);
  indexDialogVisible.value = false;
}

function buildVisualSql() {
  const table = qualifiedTable();
  if (visualAction.value === "add") return `ALTER TABLE ${table} ADD COLUMN ${columnDefinition()};`;
  if (visualAction.value === "delete") return `ALTER TABLE ${table} DROP COLUMN ${quoteIdentifier(originalColumnName.value)};`;

  const mysqlLike = ["mysql", "mariadb", "tidb", "oceanbase", "polardb", "tdsql", "dolt", "greatsql"].includes(props.dbType ?? "");
  if (mysqlLike) {
    return `ALTER TABLE ${table} CHANGE COLUMN ${quoteIdentifier(originalColumnName.value)} ${columnDefinition()};`;
  }
  const statements: string[] = [];
  const oldName = quoteIdentifier(originalColumnName.value);
  const newName = quoteIdentifier(columnDraft.name.trim());
  if (originalColumnName.value !== columnDraft.name.trim()) {
    statements.push(`ALTER TABLE ${table} RENAME COLUMN ${oldName} TO ${newName};`);
  }
  statements.push(`ALTER TABLE ${table} ALTER COLUMN ${newName} TYPE ${columnDraft.dataType.trim()};`);
  statements.push(`ALTER TABLE ${table} ALTER COLUMN ${newName} ${columnDraft.nullable ? "DROP" : "SET"} NOT NULL;`);
  statements.push(
    columnDraft.defaultValue.trim()
      ? `ALTER TABLE ${table} ALTER COLUMN ${newName} SET DEFAULT ${columnDraft.defaultValue.trim()};`
      : `ALTER TABLE ${table} ALTER COLUMN ${newName} DROP DEFAULT;`
  );
  if (columnDraft.comment.trim()) {
    statements.push(`COMMENT ON COLUMN ${table}.${newName} IS ${sqlLiteral(columnDraft.comment.trim())};`);
  }
  return statements.join("\n");
}

function submitVisualChange() {
  if (!columnDraft.name.trim() || (visualAction.value !== "delete" && !columnDraft.dataType.trim())) {
    ElMessage.warning(t("database.schemaColumnRequired"));
    return;
  }
  advancedSql.value = buildVisualSql();
  visualDialogVisible.value = false;
  activeTab.value = "changeSql";
}

async function copyDdl() {
  try {
    await copyText(ddl.value);
    ElMessage.success(t("database.ddlCopied"));
  } catch (cause) {
    ElMessage.error(t("database.copyFailed", { message: String(cause) }));
  }
}

function applyAdvancedSql() {
  const sql = advancedSql.value.trim();
  if (!sql) return ElMessage.warning(t("database.schemaSqlRequired"));
  const statements =
    sql === generatedSqlSnapshot.value && generatedStatements.value.length
      ? [...generatedStatements.value]
      : undefined;
  emit("apply", sql, statements);
}
</script>

<template>
  <el-drawer
    append-to-body
    :model-value="modelValue"
    :title="`${database}.${table} · ${t('database.tableStructure')}`"
    direction="rtl"
    size="min(920px, 92vw)"
    @update:model-value="emit('update:modelValue', $event)"
  >
    <div v-loading="loading" class="db-details-drawer">
      <el-alert
        v-if="details?.loadErrors?.length"
        class="db-details-drawer__warning"
        :title="t('database.tableStructurePartialError')"
        :description="details.loadErrors.join('\n')"
        type="warning"
        :closable="false"
        show-icon
      />
      <div
        class="db-details-tabs-shell"
        :class="{
          'db-details-tabs-shell--has-action':
            (activeTab === 'columns' && visualEditSupported) ||
            (activeTab === 'indexes' && capabilities.createIndexes),
        }"
      >
        <el-button
          v-if="activeTab === 'columns' && visualEditSupported"
          class="db-details-tabs__action"
          size="small"
          type="primary"
          @click="openAddColumn"
        >
          {{ t("database.addColumn") }}
        </el-button>
        <el-button
          v-else-if="activeTab === 'indexes' && capabilities.createIndexes"
          class="db-details-tabs__action"
          size="small"
          type="primary"
          @click="openAddIndex"
        >
          {{ t("database.addIndex") }}
        </el-button>
        <el-tabs v-model="activeTab" class="db-details-tabs">
        <el-tab-pane :label="t('database.columns')" name="columns" lazy>
          <el-empty
            v-if="activeTab === 'columns' && !loading && !details?.columns.length"
            :description="t('database.noTableColumns')"
            :image-size="64"
          />
          <el-table v-else-if="activeTab === 'columns'" :data="details?.columns ?? []" height="100%" stripe>
            <el-table-column prop="name" :label="t('database.column')" min-width="150" fixed />
            <el-table-column prop="dataType" :label="t('database.type')" min-width="140" />
            <el-table-column :label="t('database.primaryKey')" width="86" align="center">
              <template #default="scope"><el-tag v-if="scope.row.isPrimaryKey" size="small" type="warning">PK</el-tag></template>
            </el-table-column>
            <el-table-column :label="t('database.nullable')" width="76" align="center">
              <template #default="scope">{{ scope.row.nullable ? t("database.yes") : t("database.no") }}</template>
            </el-table-column>
            <el-table-column :label="t('database.autoIncrement')" width="90" align="center">
              <template #default="scope">{{ scope.row.autoIncrement ? t("database.yes") : t("database.no") }}</template>
            </el-table-column>
            <el-table-column prop="defaultValue" :label="t('database.defaultValue')" min-width="120" />
            <el-table-column prop="comment" :label="t('database.comment')" min-width="160" />
            <el-table-column v-if="visualEditSupported" :label="t('database.actions')" width="150" fixed="right">
              <template #default="scope">
                <el-button link type="primary" @click="openEditColumn(scope.row)">{{ t("common.edit") }}</el-button>
                <el-button link type="danger" @click="openDeleteColumn(scope.row)">{{ t("common.delete") }}</el-button>
              </template>
            </el-table-column>
          </el-table>
        </el-tab-pane>
        <el-tab-pane
          v-if="capabilities.indexes"
          :label="`${t('database.indexes')} (${displayIndexes.length})`"
          name="indexes"
          lazy
        >
          <el-alert
            v-if="
              activeTab === 'indexes' &&
              !capabilities.createIndexes &&
              !capabilities.editIndexes &&
              !capabilities.deleteIndexes
            "
            class="db-details-drawer__warning"
            :title="t('database.indexChangeUnsupportedHint')"
            type="info"
            :closable="false"
            show-icon
          />
          <el-empty
            v-if="activeTab === 'indexes' && !loading && !displayIndexes.length"
            :description="t('database.noIndexes')"
            :image-size="64"
          />
          <el-table v-else-if="activeTab === 'indexes'" :data="displayIndexes" height="100%" stripe>
            <el-table-column prop="name" :label="t('database.indexName')" min-width="180" />
            <el-table-column :label="t('database.columns')" min-width="260">
              <template #default="scope">{{ scope.row.columns.join(", ") }}</template>
            </el-table-column>
            <el-table-column :label="t('database.unique')" width="90" align="center">
              <template #default="scope">{{ scope.row.unique ? t("database.yes") : t("database.no") }}</template>
            </el-table-column>
            <el-table-column
              v-if="capabilities.editIndexes || capabilities.deleteIndexes"
              :label="t('database.actions')"
              width="150"
              fixed="right"
            >
              <template #default="scope">
                <el-tag v-if="scope.row.primary" size="small" type="info">
                  {{ t("database.primaryIndexManagedSeparately") }}
                </el-tag>
                <template v-else>
                  <el-button
                    v-if="capabilities.editIndexes"
                    link
                    type="primary"
                    @click="openEditIndex(scope.row)"
                  >
                    {{ t("common.edit") }}
                  </el-button>
                  <el-button
                    v-if="capabilities.deleteIndexes"
                    link
                    type="danger"
                    @click="openDeleteIndex(scope.row)"
                  >
                    {{ t("common.delete") }}
                  </el-button>
                </template>
              </template>
            </el-table-column>
          </el-table>
        </el-tab-pane>
        <el-tab-pane
          v-if="capabilities.foreignKeys"
          :label="`${t('database.foreignKeys')} (${details?.foreignKeys.length ?? 0})`"
          name="foreignKeys"
          lazy
        >
          <el-table
            v-if="activeTab === 'foreignKeys'"
            :data="details?.foreignKeys ?? []"
            height="100%"
            stripe
          >
            <el-table-column prop="name" :label="t('database.foreignKeyName')" min-width="160" />
            <el-table-column :label="t('database.localColumns')" min-width="170">
              <template #default="scope">{{ scope.row.columns.join(", ") }}</template>
            </el-table-column>
            <el-table-column prop="referencedTable" :label="t('database.referencedTable')" min-width="160" />
            <el-table-column :label="t('database.referencedColumns')" min-width="170">
              <template #default="scope">{{ scope.row.referencedColumns.join(", ") }}</template>
            </el-table-column>
          </el-table>
        </el-tab-pane>
        <el-tab-pane
          v-if="capabilities.triggers"
          :label="`${t('database.triggers')} (${details?.triggers.length ?? 0})`"
          name="triggers"
          lazy
        >
          <el-table v-if="activeTab === 'triggers'" :data="details?.triggers ?? []" height="100%" stripe>
            <el-table-column prop="name" :label="t('database.trigger')" min-width="220" />
            <el-table-column prop="timing" :label="t('database.timing')" min-width="120" />
            <el-table-column prop="event" :label="t('database.event')" min-width="160" />
          </el-table>
        </el-tab-pane>
        <el-tab-pane v-if="capabilities.ddl" label="DDL" name="ddl" lazy>
          <div class="db-ddl-code">
            <ReadonlyCodeViewer :value="ddl" :ariaLabel="t('database.ddl')" language="sql" editor-kind="ddl" />
            <el-button
              class="db-ddl-code__copy"
              size="small"
              :icon="DocumentCopy"
              :aria-label="t('database.copyDdl')"
              :title="t('database.copyDdl')"
              @click="copyDdl"
            >
              {{ t("database.copyDdl") }}
            </el-button>
          </div>
        </el-tab-pane>
        <el-tab-pane v-if="capabilities.schemaChangeSql" :label="t('database.schemaChangeSql')" name="changeSql" lazy>
          <el-alert :title="t('database.schemaChangeWarning')" type="warning" :closable="false" show-icon />
          <el-input
            v-model="advancedSql"
            class="db-schema-sql-editor"
            type="textarea"
            :rows="16"
            resize="vertical"
            spellcheck="false"
            :placeholder="t('database.schemaSqlPlaceholder')"
          />
          <div class="db-ddl-toolbar">
            <el-button type="danger" :loading="applying" @click="applyAdvancedSql">{{ t("database.executeSchemaChange") }}</el-button>
          </div>
        </el-tab-pane>
        </el-tabs>
      </div>
    </div>

    <el-dialog
      v-model="visualDialogVisible"
      :title="t(`database.${visualAction}Column`)"
      width="560px"
      append-to-body
    >
      <el-alert
        v-if="visualAction === 'delete'"
        :title="t('database.dropColumnWarning', { name: originalColumnName })"
        type="error"
        :closable="false"
        show-icon
      />
      <el-form v-else label-position="top">
        <el-form-item :label="t('database.column')" required>
          <el-input v-model="columnDraft.name" />
        </el-form-item>
        <el-form-item :label="t('database.type')" required>
          <el-select
            v-model="columnDraft.dataType"
            filterable
            allow-create
            default-first-option
            :reserve-keyword="false"
            :placeholder="defaultColumnDataType(dbType)"
          >
            <el-option v-for="dataType in dataTypeOptions" :key="dataType" :label="dataType" :value="dataType" />
          </el-select>
        </el-form-item>
        <div class="db-schema-form-row">
          <el-form-item :label="t('database.nullable')">
            <el-switch v-model="columnDraft.nullable" />
          </el-form-item>
          <el-form-item :label="t('database.defaultValue')">
            <el-input v-model="columnDraft.defaultValue" :placeholder="t('database.defaultExpressionPlaceholder')" />
          </el-form-item>
        </div>
        <el-form-item :label="t('database.comment')">
          <el-input v-model="columnDraft.comment" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="visualDialogVisible = false">{{ t("common.cancel") }}</el-button>
        <el-button :type="visualAction === 'delete' ? 'danger' : 'primary'" @click="submitVisualChange">
          {{ t("database.generateChangeSql") }}
        </el-button>
      </template>
    </el-dialog>

    <el-dialog
      v-model="indexDialogVisible"
      :title="t(`database.${indexAction}Index`)"
      width="560px"
      append-to-body
    >
      <el-alert
        v-if="indexAction === 'delete'"
        :title="t('database.dropIndexWarning', { name: originalIndexName })"
        type="error"
        :closable="false"
        show-icon
      />
      <template v-else>
        <el-alert
          v-if="indexEditUsesRecreate"
          class="db-details-drawer__warning"
          :title="t('database.editIndexRecreateWarning')"
          type="warning"
          :closable="false"
          show-icon
        />
        <el-form label-position="top">
          <el-form-item :label="t('database.indexName')" required>
            <el-input v-model="indexDraft.name" :placeholder="t('database.indexNamePlaceholder')" />
          </el-form-item>
          <el-form-item :label="t('database.indexColumns')" required>
            <el-select
              v-model="indexDraft.columns"
              multiple
              filterable
              collapse-tags
              collapse-tags-tooltip
              :placeholder="t('database.indexColumnsPlaceholder')"
            >
              <el-option
                v-for="column in details?.columns ?? []"
                :key="column.name"
                :label="column.name"
                :value="column.name"
              />
            </el-select>
          </el-form-item>
          <el-form-item :label="t('database.uniqueIndex')">
            <el-switch v-model="indexDraft.unique" />
          </el-form-item>
        </el-form>
      </template>
      <template #footer>
        <el-button @click="indexDialogVisible = false">{{ t("common.cancel") }}</el-button>
        <el-button
          :type="indexAction === 'delete' ? 'danger' : 'primary'"
          @click="submitIndexChange"
        >
          {{ t("database.generateChangeSql") }}
        </el-button>
      </template>
    </el-dialog>

    <template #footer>
      <el-button :loading="loading" @click="emit('refresh')">{{ t("common.refresh") }}</el-button>
      <el-button type="primary" @click="emit('update:modelValue', false)">{{ t("common.close") }}</el-button>
    </template>
  </el-drawer>
</template>

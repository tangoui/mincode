<script setup lang="ts">
import { computed } from "vue";
import { Grid, View } from "@element-plus/icons-vue";
import type { Column } from "element-plus";
import type { SchemaTable } from "@/domain/database/types";
import { useVueI18n } from "@/i18n/messages";

const props = defineProps<{
  objects: SchemaTable[];
  selectedName?: string;
}>();

const emit = defineEmits<{
  select: [object: SchemaTable];
  open: [object: SchemaTable];
  contextMenu: [payload: { object: SchemaTable; x: number; y: number }];
}>();

const { t } = useVueI18n();
const integerFormatter = new Intl.NumberFormat();

const columns = computed<Column<SchemaTable>[]>(() => [
  {
    key: "name",
    dataKey: "name",
    title: t("database.objectName"),
    width: 240,
    minWidth: 210,
    flexGrow: 1,
  },
  {
    key: "type",
    title: t("database.objectType"),
    width: 100,
  },
  {
    key: "rowCount",
    dataKey: "rowCount",
    title: t("database.rowCount"),
    width: 120,
    align: "right",
  },
  {
    key: "sizeBytes",
    dataKey: "sizeBytes",
    title: t("database.objectSize"),
    width: 120,
    align: "right",
  },
  {
    key: "createdAt",
    dataKey: "createdAt",
    title: t("database.createdAt"),
    width: 180,
  },
  {
    key: "updatedAt",
    dataKey: "updatedAt",
    title: t("database.updatedAt"),
    width: 180,
  },
  {
    key: "comment",
    dataKey: "comment",
    title: t("database.comment"),
    width: 220,
    flexGrow: 1,
  },
]);

const rowEventHandlers = {
  onClick: ({ rowData }: { rowData: SchemaTable }) => emit("select", rowData),
  onDblclick: ({ rowData }: { rowData: SchemaTable }) => emit("open", rowData),
};

function rowClass({ rowData }: { rowData: SchemaTable }) {
  return rowData.name === props.selectedName ? "is-current" : "";
}

function normalizedKind(object: SchemaTable) {
  return object.kind.toLocaleLowerCase().includes("view") ? "view" : "table";
}

function objectKindLabel(object: SchemaTable) {
  return t(normalizedKind(object) === "view" ? "database.view" : "database.table");
}

function formatInteger(value?: number | null) {
  return value == null ? "—" : integerFormatter.format(value);
}

function formatBytes(value?: number | null) {
  if (value == null) return "—";
  if (value < 1024) return `${value} B`;
  const units = ["KB", "MB", "GB", "TB"];
  let size = value / 1024;
  let unit = units[0];
  for (let index = 1; size >= 1024 && index < units.length; index += 1) {
    size /= 1024;
    unit = units[index];
  }
  return `${size >= 100 ? size.toFixed(0) : size.toFixed(1)} ${unit}`;
}

function handleRowContextMenu(event: MouseEvent, object: SchemaTable) {
  event.preventDefault();
  emit("contextMenu", { object, x: event.clientX, y: event.clientY });
}
</script>

<template>
  <el-auto-resizer class="db-object-table">
    <template #default="{ height, width }">
      <el-table-v2
        v-if="height > 0 && width > 0"
        :columns="columns"
        :data="objects"
        :width="width"
        :height="height"
        :row-height="36"
        :header-height="36"
        row-key="name"
        :cache="4"
        :row-class="rowClass"
        :row-event-handlers="rowEventHandlers"
        :aria-label="t('database.objects')"
      >
        <template #cell="{ column, rowData }">
          <div
            class="db-object-table__cell"
            :class="{ 'is-right': column.align === 'right' }"
            @contextmenu="handleRowContextMenu($event, rowData)"
          >
            <span v-if="column.key === 'name'" class="db-object-browser__name">
              <el-icon :class="`is-${normalizedKind(rowData)}`">
                <View v-if="normalizedKind(rowData) === 'view'" />
                <Grid v-else />
              </el-icon>
              <strong>{{ rowData.name }}</strong>
            </span>
            <template v-else-if="column.key === 'type'">{{ objectKindLabel(rowData) }}</template>
            <template v-else-if="column.key === 'rowCount'">{{ formatInteger(rowData.rowCount) }}</template>
            <template v-else-if="column.key === 'sizeBytes'">{{ formatBytes(rowData.sizeBytes) }}</template>
            <template v-else-if="column.key === 'createdAt'">{{ rowData.createdAt || "—" }}</template>
            <template v-else-if="column.key === 'updatedAt'">{{ rowData.updatedAt || "—" }}</template>
            <template v-else>{{ rowData.comment || "—" }}</template>
          </div>
        </template>
        <template #empty>
          <div class="db-object-table__empty">{{ t("database.noDatabaseObjects") }}</div>
        </template>
      </el-table-v2>
    </template>
  </el-auto-resizer>
</template>

<script setup lang="ts">
import TableV2ResultGrid from "@/components/database/TableV2ResultGrid.vue";
import VirtualResultGrid, {
  type ResultGridColumn,
  type ResultGridContextMenu,
  type ResultGridEditingCell,
  type ResultGridHeaderContextMenu,
  type ResultGridRow,
} from "@/components/database/VirtualResultGrid.vue";
import AppContextMenu from "@/components/ui/AppContextMenu.vue";
import AppChevron from "@/components/ui/AppChevron.vue";
import GridExportSubmenu from "@/components/database/results/GridExportSubmenu.vue";
import GridFilterSubmenu from "@/components/database/results/GridFilterSubmenu.vue";
import type { DatabaseQueryResult } from "@/domain/database/types";
import type { TableDataFormat } from "@/domain/database/object-naming";
import type { QueryTab } from "@/domain/database/query-tab-snapshot";
import type { ResultGridState } from "@/composables/database/useGridState";
import type { InlineCellEdit } from "@/composables/database/useRowMutations";
import type { PendingRowState } from "@/domain/database/grid-pending-changes";
import { useVueI18n } from "@/i18n/messages";


defineProps<{
  object: "query" | "table";
  activeQueryTab?: QueryTab;
  selectedTable?: string;
  activeResult?: DatabaseQueryResult;
  resultRows: unknown[][];
  displayRows: ResultGridRow[];
  columns: ResultGridColumn[];
  gridState: ResultGridState;
  formatCell: (value: unknown) => string;
  selectedRowIndexes: number[];
  allRowsSelected: boolean;
  someRowsSelected: boolean;
  activeQueryRunning: boolean;
  resultRowCountText: string;
  page: number;
  pageSize: number;
  primaryKeys: string[];
  canEditRows: boolean;
  canInsertRows: boolean;
  editingCell?: InlineCellEdit;
  canNextPage: boolean;
  canLastPage: boolean;
  lastPageLoading: boolean;
  supportsOffsetTablePreview: boolean;
  desktopRuntime: boolean;
  isPinecone: boolean;
  contextMenu?: ResultGridContextMenu;
  contextCanCopyInsertSql: boolean;
  contextCanEditCell: boolean;
  contextCanMutateColumn: boolean;
  contextCanSetNull: boolean;
  contextSelectedRowCount: number;
  headerContextMenu?: ResultGridHeaderContextMenu;
  headerCanCopyAlterSql: boolean;
  /** 暂存修改的行状态（表模式）。 */
  pendingRows?: ReadonlyMap<number, PendingRowState>;
  contextCanRevert?: boolean;
}>();
const emit = defineEmits<{
  sort: [column: string];
  "reorder-columns": [source: string, target: string];
  "toggle-row-selection": [rowIndex: number, selected: boolean];
  "toggle-all-row-selection": [selected: boolean];
  "cell-detail": [payload: { column: string; value: unknown }];
  "begin-cell-edit": [payload: { rowIndex: number; column: string }];
  "update-cell-edit": [payload: ResultGridEditingCell];
  "commit-cell-edit": [payload: { rowIndex: number; column: string }];
  "cancel-cell-edit": [payload: { rowIndex: number; column: string }];
  "context-menu": [payload: ResultGridContextMenu];
  "close-context-menu": [];
  "copy-context-value": [];
  "copy-context-row": [];
  "copy-context-insert-sql": [];
  "filter-context-value": [];
  "exclude-context-value": [];
  "filter-context-operator": [operator: "contains" | "notContains" | "lessThan" | "greaterThan" | "isNull" | "isNotNull"];
  "sort-context-column": [direction: "asc" | "desc"];
  "clear-grid-sort": [];
  "clear-grid-filters": [];
  "view-context-cell": [];
  "view-column-details": [];
  "view-context-row": [];
  "edit-context-cell": [];
  "set-context-null": [];
  "bulk-update-context": [mode: "value" | "generator"];
  "duplicate-context-row": [];
  "delete-context-row": [];
  "revert-context-change": [];
  "toggle-layout": [];
  "clear-selection": [];
  "export-grid": [format: TableDataFormat];
  "refresh-grid": [];
  "header-context-menu": [payload: ResultGridHeaderContextMenu];
  "close-header-context-menu": [];
  "copy-header-column-name": [];
  "copy-header-all-column-names": [];
  "copy-header-alter-sql": [];
  "hide-header-column": [];
  "freeze-header-column": [];
  "unfreeze-columns": [];
  "bulk-update-header": [mode: "value" | "generator"];
  "delete-row": [rowIndex: number];
  "edit-row": [rowIndex: number];
  "change-page": [page: number];
  "jump-to-page": [];
  "last-page": [];
  "change-page-size": [size: number];
  "apply-namespace": [];
}>();
const targetPage = defineModel<number>("targetPage", { required: true });
const pineconeNamespaceDraft = defineModel<string>("pineconeNamespaceDraft", { required: true });
const { t } = useVueI18n();

/** 子菜单选择格式后触发导出并关闭所在菜单（子菜单选项不是 el-dropdown-item，不会自动关闭）。 */
function handleContextExportSelect(format: TableDataFormat) {
  emit("export-grid", format);
  emit("close-context-menu");
}
function handleHeaderExportSelect(format: TableDataFormat) {
  emit("export-grid", format);
  emit("close-header-context-menu");
}
function handleClearContextFilters() {
  emit("clear-grid-filters");
  emit("close-context-menu");
}
</script>

<template>
  <template v-if="object === 'query' && activeQueryTab">
    <TableV2ResultGrid
      v-if="activeResult && resultRows.length > 0 && gridState.layout === 'horizontal'"
      :columns="columns"
      :rows="displayRows"
      mode="query"
      :sort="gridState.sort"
      :frozen-count="gridState.frozenColumnCount"
      :format-cell="formatCell"
      :selected-row-indexes="selectedRowIndexes"
      :all-rows-selected="allRowsSelected"
      :some-rows-selected="someRowsSelected"
      :select-all-rows-label="t('database.selectAllRows')"
      :row-selection-label="(row) => t('database.selectRow', { row })"
      :reorder-column-label="(column) => t('database.dragFieldToReorder', { column })"
      :resize-column-label="(column) => t('database.resizeColumn', { column })"
      @sort="emit('sort', $event)"
      @reorder-columns="(source, target) => emit('reorder-columns', source, target)"
      @toggle-row-selection="(rowIndex, selected) => emit('toggle-row-selection', rowIndex, selected)"
      @toggle-all-row-selection="(selected) => emit('toggle-all-row-selection', selected)"
      @cell-detail="({ column, value }) => emit('cell-detail', { column, value })"
      @context-menu="(payload) => emit('context-menu', payload)"
      @header-context-menu="(payload) => emit('header-context-menu', payload)"
    />
    <VirtualResultGrid
      v-else-if="activeResult && resultRows.length > 0"
      :columns="columns"
      :rows="displayRows"
      :field-name="t('database.fieldName')"
      mode="query"
      :sort="gridState.sort"
      :format-cell="formatCell"
      :selected-row-indexes="selectedRowIndexes"
      :all-rows-selected="allRowsSelected"
      :some-rows-selected="someRowsSelected"
      :select-all-rows-label="t('database.selectAllRows')"
      :row-selection-label="(row) => t('database.selectRow', { row })"
      :field-filter-placeholder="
        (column: string) => t('database.fieldFilterPlaceholder', { column })
      "
      :clear-field-filter-label="t('database.clearFieldFilter')"
      :reorder-column-label="(column) => t('database.dragFieldToReorder', { column })"
      @sort="emit('sort', $event)"
      @reorder-columns="(source, target) => emit('reorder-columns', source, target)"
      @toggle-row-selection="(rowIndex, selected) => emit('toggle-row-selection', rowIndex, selected)"
      @toggle-all-row-selection="(selected) => emit('toggle-all-row-selection', selected)"
      @cell-detail="({ column, value }) => emit('cell-detail', { column, value })"
      @context-menu="(payload) => emit('context-menu', payload)"
      @header-context-menu="(payload) => emit('header-context-menu', payload)"
    />
    <div v-else class="db-result-empty">
      <el-empty
        :description="t(activeQueryTab?.resultReleased ? 'database.queryResultReleased' : activeResult ? 'database.noResultData' : 'database.queryResultHint')"
        :image-size="72"
      />
    </div>
    <footer class="db-statusbar">
      <span>{{ resultRowCountText }}</span
      ><span v-if="activeResult">{{ activeResult.executionMs }} ms</span
      ><span v-else-if="activeQueryRunning">{{ t("database.queryRunning") }}</span>
    </footer>
  </template>
  <template v-if="object === 'table' && selectedTable">
    <TableV2ResultGrid
      v-if="activeResult && resultRows.length > 0 && gridState.layout === 'horizontal'"
      :columns="columns"
      :rows="displayRows"
      mode="table"
      :page="page"
      :page-size="pageSize"
      :primary-keys="primaryKeys"
      :can-edit-rows="canEditRows"
      :editing-cell="editingCell"
      :pending-rows="pendingRows"
      :sort="gridState.sort"
      :frozen-count="gridState.frozenColumnCount"
      :format-cell="formatCell"
      :selected-row-indexes="selectedRowIndexes"
      :all-rows-selected="allRowsSelected"
      :some-rows-selected="someRowsSelected"
      :select-all-rows-label="t('database.selectAllRows')"
      :row-selection-label="(row) => t('database.selectRow', { row })"
      :reorder-column-label="(column) => t('database.dragFieldToReorder', { column })"
      :resize-column-label="(column) => t('database.resizeColumn', { column })"
      @sort="emit('sort', $event)"
      @reorder-columns="(source, target) => emit('reorder-columns', source, target)"
      @toggle-row-selection="(rowIndex, selected) => emit('toggle-row-selection', rowIndex, selected)"
      @toggle-all-row-selection="(selected) => emit('toggle-all-row-selection', selected)"
      @cell-detail="({ column, value }) => emit('cell-detail', { column, value })"
      @begin-cell-edit="(payload) => emit('begin-cell-edit', payload)"
      @update-cell-edit="(payload) => emit('update-cell-edit', payload)"
      @commit-cell-edit="(payload) => emit('commit-cell-edit', payload)"
      @cancel-cell-edit="(payload) => emit('cancel-cell-edit', payload)"
      @context-menu="(payload) => emit('context-menu', payload)"
      @header-context-menu="(payload) => emit('header-context-menu', payload)"
      @delete-row="(rowIndex) => emit('delete-row', rowIndex)"
      @edit-row="(rowIndex) => emit('edit-row', rowIndex)"
    />
    <VirtualResultGrid
      v-else-if="activeResult && resultRows.length > 0"
      :columns="columns"
      :rows="displayRows"
      :field-name="t('database.fieldName')"
      mode="table"
      :page="page"
      :page-size="pageSize"
      :primary-keys="primaryKeys"
      :can-edit-rows="canEditRows"
      :editing-cell="editingCell"
      :pending-rows="pendingRows"
      :sort="gridState.sort"
      :format-cell="formatCell"
      :selected-row-indexes="selectedRowIndexes"
      :all-rows-selected="allRowsSelected"
      :some-rows-selected="someRowsSelected"
      :select-all-rows-label="t('database.selectAllRows')"
      :row-selection-label="(row) => t('database.selectRow', { row })"
      :field-filter-placeholder="
        (column: string) => t('database.fieldFilterPlaceholder', { column })
      "
      :clear-field-filter-label="t('database.clearFieldFilter')"
      :reorder-column-label="(column) => t('database.dragFieldToReorder', { column })"
      @sort="emit('sort', $event)"
      @reorder-columns="(source, target) => emit('reorder-columns', source, target)"
      @toggle-row-selection="(rowIndex, selected) => emit('toggle-row-selection', rowIndex, selected)"
      @toggle-all-row-selection="(selected) => emit('toggle-all-row-selection', selected)"
      @cell-detail="({ column, value }) => emit('cell-detail', { column, value })"
      @begin-cell-edit="(payload) => emit('begin-cell-edit', payload)"
      @update-cell-edit="(payload) => emit('update-cell-edit', payload)"
      @commit-cell-edit="(payload) => emit('commit-cell-edit', payload)"
      @cancel-cell-edit="(payload) => emit('cancel-cell-edit', payload)"
      @context-menu="(payload) => emit('context-menu', payload)"
      @header-context-menu="(payload) => emit('header-context-menu', payload)"
      @delete-row="(rowIndex) => emit('delete-row', rowIndex)"
      @edit-row="(rowIndex) => emit('edit-row', rowIndex)"
    />
    <div v-else class="db-result-empty">
      <el-empty
        :description="t(activeResult ? 'database.noResultData' : 'database.selectTableHint')"
        :image-size="72"
      />
    </div>
    <footer class="db-statusbar">
      <div class="db-pagination">
        <div v-if="isPinecone" class="db-vector-namespace">
          <span>{{ t("database.pineconeNamespace") }}</span>
          <el-input
            v-model="pineconeNamespaceDraft"
            class="db-vector-namespace__input"
            size="small"
            clearable
            :placeholder="t('database.pineconeNamespacePlaceholder')"
            @keyup.enter="emit('apply-namespace')"
          />
          <el-button text @click="emit('apply-namespace')">{{ t("database.applyNamespace") }}</el-button>
        </div>
        <span>{{ t("database.rowsPerPage") }}</span
        ><el-select
          :model-value="pageSize"
          size="small"
          style="width: 76px"
          @change="(size: number) => emit('change-page-size', size)"
          ><el-option
            v-for="size in [50, 100, 200, 500, 1000, 5000, 10000]"
            :key="size"
            :label="size"
            :value="size" /></el-select
        ><el-button
          text
          size="small"
          :disabled="
            page <= 1 ||
            !desktopRuntime
          "
          @click="emit('change-page', 1)"
        >
          <AppChevron direction="double-left" /></el-button
        ><el-button
          text
          size="small"
          :disabled="
            page <= 1 ||
            !desktopRuntime
          "
          @click="emit('change-page', page - 1)"
        >
          <AppChevron direction="left" /></el-button
        ><span>{{ t("database.pageNumber", { page }) }}</span
        ><span class="db-pagination__jump-label">{{ t("database.jumpToPage") }}</span
        ><el-input-number
          v-model="targetPage"
          class="db-pagination__jump-input"
          :min="1"
          :precision="0"
          :controls="false"
          size="small"
          :disabled="!desktopRuntime || !supportsOffsetTablePreview"
          :aria-label="t('database.jumpToPage')"
          @keyup.enter="emit('jump-to-page')"
        />
        <el-button
          text
          size="small"
          :disabled="!desktopRuntime || !supportsOffsetTablePreview"
          @click="emit('jump-to-page')"
        >
          {{ t("database.jump") }}
        </el-button>
        <el-button
          text
          size="small"
          :disabled="!canNextPage"
          @click="emit('change-page', page + 1)"
        >
          <AppChevron direction="right" />
        </el-button>
        <el-button
          text
          size="small"
          :disabled="!canLastPage"
          :loading="lastPageLoading"
          :title="t('database.lastPage')"
          @click="emit('last-page')"
        >
          <AppChevron direction="double-right" />
        </el-button>
      </div>
      <span>{{ resultRowCountText }}</span>
    </footer>
  </template>
  <div v-else-if="object !== 'query' || !activeQueryTab" class="db-result-empty">
    <el-empty :description="t('database.emptyWorkspace')" :image-size="72" />
  </div>

  <AppContextMenu
    v-if="contextMenu"
    :open="true"
    :x="contextMenu.x"
    :y="contextMenu.y"
    :width="218"
    popper-class="db-grid-context-menu"
    @close="emit('close-context-menu')"
  >
    <el-dropdown-item @click="emit('sort-context-column', 'asc')">
      {{ t("database.sortAscending") }}
    </el-dropdown-item>
    <el-dropdown-item @click="emit('sort-context-column', 'desc')">
      {{ t("database.sortDescending") }}
    </el-dropdown-item>
    <el-dropdown-item v-if="gridState.sort" @click="emit('clear-grid-sort')">
      {{ t("database.clearSort") }}
    </el-dropdown-item>
    <GridFilterSubmenu
      divided
      :multiple-values="contextSelectedRowCount > 1"
      @filter-value="emit('filter-context-value')"
      @exclude-value="emit('exclude-context-value')"
      @select-operator="emit('filter-context-operator', $event)"
      @clear="handleClearContextFilters"
    />
    <el-dropdown-item v-if="contextSelectedRowCount <= 1" divided @click="emit('view-context-cell')">
      {{ t("database.viewCellDetail") }}
    </el-dropdown-item>
    <el-dropdown-item
      v-if="object === 'table' && selectedTable"
      :divided="contextSelectedRowCount > 1"
      @click="emit('view-column-details')"
    >
      {{ t("database.columnDetails") }}
    </el-dropdown-item>
    <el-dropdown-item
      v-if="canEditRows && contextSelectedRowCount <= 1"
      @click="emit('view-context-row')"
    >
      {{ t("database.rowDetails") }}
    </el-dropdown-item>
    <el-dropdown-item divided @click="emit('copy-context-value')">
      {{ contextSelectedRowCount > 1 ? t("database.copySelectedCells", { count: contextSelectedRowCount }) : t("database.copyCell") }}
    </el-dropdown-item>
    <el-dropdown-item @click="emit('copy-context-row')">
      {{
        contextSelectedRowCount > 1
          ? t("database.copySelectedRowsJson", { count: contextSelectedRowCount })
          : t("database.copyRowJson")
      }}
    </el-dropdown-item>
    <el-dropdown-item v-if="contextCanCopyInsertSql" @click="emit('copy-context-insert-sql')">
      {{
        contextSelectedRowCount > 1
          ? t("database.copySelectedRowsInsertSql", { count: contextSelectedRowCount })
          : t("database.copyRowInsertSql")
      }}
    </el-dropdown-item>
    <el-dropdown-item v-if="contextCanEditCell" divided @click="emit('edit-context-cell')">
      {{ t("database.editCell") }}
    </el-dropdown-item>
    <el-dropdown-item
      v-if="contextCanSetNull"
      :divided="!contextCanEditCell"
      @click="emit('set-context-null')"
    >
      {{ t("database.setNullTitle") }}
    </el-dropdown-item>
    <el-dropdown-item
      v-if="contextCanMutateColumn"
      :divided="!contextCanEditCell && !contextCanSetNull"
      @click="emit('bulk-update-context', 'value')"
    >
      {{ t("database.bulkUpdateSelected") }}
    </el-dropdown-item>
    <el-dropdown-item v-if="contextCanMutateColumn" @click="emit('bulk-update-context', 'generator')">
      {{ t("database.generateValue") }}
    </el-dropdown-item>
    <el-dropdown-item v-if="contextCanRevert" divided @click="emit('revert-context-change')">
      {{ t("database.pending.revert") }}
    </el-dropdown-item>
    <el-dropdown-item
      v-if="canInsertRows && object === 'table'"
      divided
      @click="emit('duplicate-context-row')"
    >
      {{
        contextSelectedRowCount > 1
          ? t("database.duplicateSelectedRows", { count: contextSelectedRowCount })
          : t("database.duplicateRow")
      }}
    </el-dropdown-item>
    <el-dropdown-item
      v-if="canEditRows"
      :divided="!(canInsertRows && object === 'table')"
      class="is-danger"
      @click="emit('delete-context-row')"
    >
      {{
        contextSelectedRowCount > 1
          ? t("database.deleteSelectedRows", { count: contextSelectedRowCount })
          : t("database.deleteRowTitle")
      }}
    </el-dropdown-item>
    <el-dropdown-item divided @click="emit('toggle-layout')">
      {{ t("database.transposeView") }}
    </el-dropdown-item>
    <el-dropdown-item @click="emit('clear-selection')">
      {{ t("database.clearSelection") }}
    </el-dropdown-item>
    <GridExportSubmenu
      v-if="object === 'table' && selectedTable"
      divided
      @select="handleContextExportSelect"
    />
    <el-dropdown-item divided @click="emit('refresh-grid')">
      {{ t("database.refresh") }}
    </el-dropdown-item>
  </AppContextMenu>

  <AppContextMenu
    v-if="headerContextMenu"
    :open="true"
    :x="headerContextMenu.x"
    :y="headerContextMenu.y"
    :width="218"
    popper-class="db-grid-context-menu"
    @close="emit('close-header-context-menu')"
  >
    <el-dropdown-item @click="emit('copy-header-column-name')">
      {{ t("database.copyColumnName") }}
    </el-dropdown-item>
    <el-dropdown-item @click="emit('copy-header-all-column-names')">
      {{ t("database.copyAllColumnNames") }}
    </el-dropdown-item>
    <el-dropdown-item
      v-if="object === 'table' && selectedTable"
      divided
      @click="emit('view-column-details')"
    >
      {{ t("database.columnDetails") }}
    </el-dropdown-item>
    <el-dropdown-item
      v-if="headerCanCopyAlterSql"
      :divided="!(object === 'table' && selectedTable)"
      @click="emit('copy-header-alter-sql')"
    >
      {{ t("database.copyAsAlterSql") }}
    </el-dropdown-item>
    <el-dropdown-item divided @click="emit('hide-header-column')">
      {{ t("database.hideThisColumn") }}
    </el-dropdown-item>
    <el-dropdown-item @click="emit('freeze-header-column')">
      {{ t("database.freezeUpToColumn") }}
    </el-dropdown-item>
    <el-dropdown-item v-if="gridState.frozenColumnCount > 0" @click="emit('unfreeze-columns')">
      {{ t("database.unfreezeColumns") }}
    </el-dropdown-item>
    <template v-if="canEditRows">
      <el-dropdown-item divided @click="emit('bulk-update-header', 'value')">
        {{ t("database.bulkUpdateSelected") }}
      </el-dropdown-item>
      <el-dropdown-item @click="emit('bulk-update-header', 'generator')">
        {{ t("database.generateValue") }}
      </el-dropdown-item>
    </template>
    <el-dropdown-item divided @click="emit('clear-selection')">
      {{ t("database.clearSelection") }}
    </el-dropdown-item>
    <GridExportSubmenu
      v-if="object === 'table' && selectedTable"
      divided
      @select="handleHeaderExportSelect"
    />
    <el-dropdown-item divided @click="emit('refresh-grid')">
      {{ t("database.refresh") }}
    </el-dropdown-item>
  </AppContextMenu>
</template>

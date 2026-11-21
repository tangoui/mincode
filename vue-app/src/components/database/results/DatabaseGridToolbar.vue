<script setup lang="ts">
import { ref } from "vue";
import { Filter, Grid } from "@element-plus/icons-vue";
import AppSearchField from "@/components/ui/AppSearchField.vue";
import {
  tableFilterOperatorNeedsValue,
  tableFilterOperators,
  type TableFilterOperator,
} from "@/domain/database/table-search";
import type { ResultGridState } from "@/composables/database/useGridState";
import { useVueI18n } from "@/i18n/messages";

defineProps<{
  object: "query" | "table";
  gridState: ResultGridState;
  resultColumns: string[];
  filterColumns: string[];
  visibleColumnCount: number;
  queryRunning: boolean;
  canSearchEntireTable: boolean;
  searchPlaceholder: string;
  /** 当前表的待提交修改数。 */
  pendingCount?: number;
  pendingSubmitting?: boolean;
}>();
const emit = defineEmits<{
  "search-submit": [];
  "search-clear": [];
  "search-all-rows": [];
  "add-condition": [];
  "update-condition": [
    id: string,
    patch: { column?: string; operator?: TableFilterOperator; value?: string; valueTo?: string },
  ];
  "remove-condition": [id: string];
  "set-logic": [logic: "and" | "or"];
  "clear-filters": [];
  "apply-filters": [];
  "toggle-layout": [];
  "set-column-visibility": [column: string, visible: boolean];
  "show-all-columns": [];
  "submit-pending": [];
  "discard-pending": [];
}>();
const filterText = defineModel<string>("filterText", { required: true });
const { t } = useVueI18n();
const searchField = ref<InstanceType<typeof AppSearchField>>();

defineExpose({
  focusSearchField: () => searchField.value?.focus(),
});
</script>

<template>
  <!-- 排在工具栏最前（order: -1），窄窗口下也不会被截掉。 -->
  <div v-if="object === 'table' && pendingCount" class="db-grid-pending">
    <span class="db-grid-pending__count" role="status">{{ t("database.pending.count", { count: pendingCount }) }}</span>
    <el-button
      text
      native-type="button"
      class="db-grid-pending__submit"
      :loading="pendingSubmitting"
      :title="t('database.pending.submitHint')"
      @click="emit('submit-pending')"
    >
      {{ t("database.pending.submit") }}
    </el-button>
    <el-popconfirm
      :title="t('database.pending.discardConfirm', { count: pendingCount })"
      :confirm-button-text="t('database.pending.discard')"
      :cancel-button-text="t('common.cancel')"
      @confirm="emit('discard-pending')"
    >
      <template #reference>
        <el-button text native-type="button" class="db-grid-pending__discard" :disabled="pendingSubmitting">
          {{ t("database.pending.discard") }}
        </el-button>
      </template>
    </el-popconfirm>
    <span class="db-toolbar-divider" />
  </div>
  <span class="db-toolbar-divider" />
    <AppSearchField
      ref="searchField"
      v-model="filterText"
      class="db-grid-filter"
      :placeholder="searchPlaceholder"
      :clearable="object === 'table'"
      :clear-label="t('database.clearTableSearch')"
      @submit="emit('search-submit')"
      @clear="emit('search-clear')"
    />
    <el-button text native-type="button"
      v-if="object === 'table'"
      :disabled="queryRunning || !canSearchEntireTable"
      @click="emit('search-all-rows')"
    >
      {{ t("database.query") }}
    </el-button>
    <el-popover placement="bottom-start" :width="620" trigger="click" :persistent="true">
      <template #reference>
        <el-button text native-type="button"
          class="db-multi-filter-button"
          :class="{ 'is-active': gridState.filters.conditions.length > 0 }"
          :title="t('database.multiFieldFilter')"
        >
          <el-icon><Filter /></el-icon>{{ t("database.multiFieldFilter") }}
          <b v-if="gridState.filters.conditions.length">
            {{ gridState.filters.conditions.length }}
          </b>
        </el-button>
      </template>
      <div class="db-multi-filter-panel">
        <div class="db-multi-filter-panel__header">
          <div>
            <strong>{{ t("database.multiFieldFilter") }}</strong>
            <small>{{ t("database.multiFieldFilterHint") }}</small>
          </div>
          <el-segmented
            class="db-multi-filter-panel__logic"
            :model-value="gridState.filters.logic"
            :options="(['and', 'or'] as const).map((logic) => ({ label: logic.toUpperCase(), value: logic }))"
            :aria-label="t('database.filterLogic')"
            @change="emit('set-logic', $event as 'and' | 'or')"
          />
        </div>
        <el-scrollbar class="db-multi-filter-panel__conditions">
          <div
            v-for="condition in gridState.filters.conditions"
            :key="condition.id"
            class="db-filter-condition"
          >
            <el-select
              filterable
              append-to=".db-multi-filter-panel"
              :model-value="condition.column"
              :aria-label="t('database.filterField')"
              @change="emit('update-condition', condition.id, { column: $event })"
            >
              <el-option v-for="column in filterColumns" :key="column" :label="column" :value="column" />
            </el-select>
            <el-select
              append-to=".db-multi-filter-panel"
              :model-value="condition.operator"
              :aria-label="t('database.filterOperator')"
              @change="emit('update-condition', condition.id, { operator: $event as TableFilterOperator })"
            >
              <el-option
                v-for="operator in tableFilterOperators(condition.fieldType)"
                :key="operator"
                :label="t(`database.filterOperator.${operator}`)"
                :value="operator"
              />
            </el-select>
            <template v-if="tableFilterOperatorNeedsValue(condition.operator)">
              <el-input
                v-if="condition.fieldType === 'number'"
                :model-value="condition.value ?? ''"
                type="number"
                inputmode="decimal"
                :aria-label="t('database.filterValue')"
                @update:model-value="emit('update-condition', condition.id, { value: $event })"
              />
              <el-date-picker
                v-else-if="condition.fieldType === 'date'"
                append-to=".db-multi-filter-panel"
                :model-value="condition.value || undefined"
                type="datetime"
                value-format="YYYY-MM-DDTHH:mm"
                :aria-label="t('database.filterValue')"
                @update:model-value="emit('update-condition', condition.id, { value: $event || '' })"
              />
              <el-input
                v-else
                :model-value="condition.value ?? ''"
                :aria-label="t('database.filterValue')"
                @update:model-value="emit('update-condition', condition.id, { value: $event })"
              />
              <el-input
                v-if="condition.operator === 'between' && condition.fieldType === 'number'"
                :model-value="condition.valueTo ?? ''"
                type="number"
                inputmode="decimal"
                :aria-label="t('database.filterValueTo')"
                @update:model-value="emit('update-condition', condition.id, { valueTo: $event })"
              />
              <el-date-picker
                v-else-if="condition.operator === 'between'"
                append-to=".db-multi-filter-panel"
                :model-value="condition.valueTo || undefined"
                type="datetime"
                value-format="YYYY-MM-DDTHH:mm"
                :aria-label="t('database.filterValueTo')"
                @update:model-value="emit('update-condition', condition.id, { valueTo: $event || '' })"
              />
            </template>
            <el-button text native-type="button"
              class="db-filter-condition__remove"
              :title="t('database.removeFilterCondition')"
              :aria-label="t('database.removeFilterCondition')"
              @click="emit('remove-condition', condition.id)"
            >
              ×
            </el-button>
          </div>
          <div v-if="!gridState.filters.conditions.length" class="db-filter-conditions-empty">
            {{ t("database.noFilterConditions") }}
          </div>
        </el-scrollbar>
        <div class="db-multi-filter-panel__actions">
          <el-button text native-type="button" @click="emit('add-condition')">+ {{ t("database.addFilterCondition") }}</el-button>
          <span />
          <el-button text native-type="button"
            v-if="gridState.filters.conditions.length"
            @click="emit('clear-filters')"
          >
            {{ t("database.clearAllFilters") }}
          </el-button>
          <el-button type="primary" native-type="button"
            v-if="object === 'table'"
            class="is-primary"
            :disabled="queryRunning || !gridState.filters.conditions.length"
            @click="emit('apply-filters')"
          >
            {{ t("database.applyFilters") }}
          </el-button>
        </div>
      </div>
    </el-popover>
    <el-button text native-type="button"
      class="db-grid-layout-button"
      :title="
        gridState.layout === 'vertical'
          ? t('database.switchToHorizontal')
          : t('database.switchToVertical')
      "
      @click="emit('toggle-layout')"
    >
      <el-icon><Grid /></el-icon>
      {{
        gridState.layout === "vertical"
          ? t("database.horizontalLayout")
          : t("database.verticalLayout")
      }}
    </el-button>
    <el-popover placement="bottom-end" :width="230" trigger="click">
      <template #reference
        ><el-button text native-type="button"
          class="db-grid-columns-button"
          :title="t('database.columnPicker')"
        >
          <el-icon><Grid /></el-icon>{{ t("database.columns")
          }}<b v-if="visibleColumnCount !== resultColumns.length"
            >{{ visibleColumnCount }}/{{ resultColumns.length }}</b
          >
        </el-button></template
      >
      <div class="db-column-picker">
        <div class="db-column-picker__header">
          <strong>{{ t("database.columnPicker") }}</strong
          ><el-button text native-type="button" @click="emit('show-all-columns')">
            {{ t("database.showAllColumns") }}
          </el-button>
        </div>
        <el-scrollbar class="db-column-picker__list" max-height="min(50vh, 280px)">
          <el-checkbox
            v-for="column in resultColumns"
            :key="column"
            :model-value="!gridState.hiddenColumns.includes(column)"
            :disabled="
              !gridState.hiddenColumns.includes(column) &&
              visibleColumnCount <= 1
            "
            @change="emit('set-column-visibility', column, Boolean($event))"
            >{{ column }}</el-checkbox
          >
        </el-scrollbar>
      </div>
    </el-popover>
</template>

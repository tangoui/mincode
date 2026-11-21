<script setup lang="ts">
import { computed, nextTick, ref, useId, watch } from "vue";
import { Minus, Plus, Refresh } from "@element-plus/icons-vue";
import type { ScrollbarInstance } from "element-plus";
import AppSearchField from "@/components/ui/AppSearchField.vue";
import type { DatabaseDiagram } from "@/domain/database/types";
import { layoutDatabaseDiagram } from "@/domain/database/er-diagram";
import { useVueI18n } from "@/i18n/messages";

const props = defineProps<{
  modelValue: boolean;
  loading?: boolean;
  diagram?: DatabaseDiagram;
}>();
const emit = defineEmits<{
  "update:modelValue": [value: boolean];
  refresh: [];
  selectTable: [table: string];
}>();

const { t } = useVueI18n();
const query = ref("");
const zoom = ref(1);
const viewport = ref<ScrollbarInstance>();
const markerId = `db-er-arrow-${useId().replace(/:/g, "")}`;
const filteredTables = computed(() => {
  const keyword = query.value.trim().toLowerCase();
  if (!keyword) return props.diagram?.tables ?? [];
  return (props.diagram?.tables ?? []).filter(
    (table) =>
      table.name.toLowerCase().includes(keyword) ||
      table.columns.some((column) => column.name.toLowerCase().includes(keyword))
  );
});
const layout = computed(() => layoutDatabaseDiagram(filteredTables.value));
const stageStyle = computed(() => ({
  width: `${layout.value.width * zoom.value}px`,
  height: `${layout.value.height * zoom.value}px`,
}));
const canvasStyle = computed(() => ({
  width: `${layout.value.width}px`,
  height: `${layout.value.height}px`,
  transform: `scale(${zoom.value})`,
}));

function adjustZoom(delta: number) {
  zoom.value = Math.min(1.6, Math.max(0.5, Math.round((zoom.value + delta) * 10) / 10));
}

function resetView() {
  zoom.value = 1;
  viewport.value?.setScrollTop(0);
  viewport.value?.setScrollLeft(0);
}

watch(
  () => props.modelValue,
  async (visible) => {
    if (!visible) return;
    query.value = "";
    await nextTick();
    resetView();
  }
);
</script>

<template>
  <el-dialog
    :model-value="modelValue"
    :title="diagram ? `${diagram.database} · ${t('database.erDiagram')}` : t('database.erDiagram')"
    width="min(1180px, 94vw)"
    append-to-body
    destroy-on-close
    class="db-er-dialog"
    @update:model-value="emit('update:modelValue', $event)"
  >
    <div class="db-er-toolbar">
      <AppSearchField
        v-model="query"
        clearable
        :placeholder="t('database.erSearchPlaceholder')"
        :aria-label="t('database.erSearchPlaceholder')"
        :clear-label="t('common.clear')"
      />
      <span>{{ t("database.erTableCount", { count: filteredTables.length }) }}</span>
      <el-button-group>
        <el-button
          :icon="Minus"
          :disabled="zoom <= 0.5"
          :aria-label="t('database.erZoomOut')"
          :title="t('database.erZoomOut')"
          @click="adjustZoom(-0.1)"
        />
        <el-button
          class="db-er-toolbar__zoom"
          :aria-label="t('database.erResetView')"
          :title="t('database.erResetView')"
          @click="resetView"
          >{{ Math.round(zoom * 100) }}%</el-button
        >
        <el-button
          :icon="Plus"
          :disabled="zoom >= 1.6"
          :aria-label="t('database.erZoomIn')"
          :title="t('database.erZoomIn')"
          @click="adjustZoom(0.1)"
        />
      </el-button-group>
      <el-button :icon="Refresh" :loading="loading" @click="emit('refresh')">
        {{ t("common.refresh") }}
      </el-button>
    </div>

    <el-alert
      v-if="diagram?.skippedTables"
      type="warning"
      :closable="false"
      :title="t('database.erSkippedTables', { count: diagram.skippedTables })"
    />

    <el-scrollbar ref="viewport" v-loading="loading" class="db-er-viewport">
      <el-empty
        v-if="!loading && filteredTables.length === 0"
        :image-size="76"
        :description="t('database.erEmpty')"
      />
      <div v-else class="db-er-stage" :style="stageStyle">
        <div class="db-er-canvas" :style="canvasStyle">
          <svg class="db-er-edges" :width="layout.width" :height="layout.height" aria-hidden="true">
            <defs>
              <marker
                :id="markerId"
                markerWidth="7"
                markerHeight="7"
                refX="6"
                refY="3.5"
                orient="auto"
              >
                <path d="M0,0 L7,3.5 L0,7 Z" />
              </marker>
            </defs>
            <path
              v-for="edge in layout.edges"
              :key="edge.id"
              :d="`M ${edge.sourceX} ${edge.sourceY} C ${edge.sourceX + 34} ${edge.sourceY}, ${edge.targetX - 34} ${edge.targetY}, ${edge.targetX} ${edge.targetY}`"
              :marker-end="`url(#${markerId})`"
            >
              <title>{{ edge.label }}</title>
            </path>
          </svg>

          <button
            v-for="node in layout.nodes"
            :key="node.table.name"
            type="button"
            class="db-er-table"
            :style="{
              left: `${node.x}px`,
              top: `${node.y}px`,
              width: `${node.width}px`,
              height: `${node.height}px`,
            }"
            :aria-label="t('database.erOpenTable', { table: node.table.name })"
            :title="t('database.erOpenTable', { table: node.table.name })"
            @dblclick="emit('selectTable', node.table.name)"
            @keydown.enter="emit('selectTable', node.table.name)"
            @keydown.space.prevent="emit('selectTable', node.table.name)"
          >
            <strong>{{ node.table.name }}</strong>
            <span
              v-for="column in node.table.columns.slice(0, 12)"
              :key="column.name"
              class="db-er-column"
            >
              <b>{{ column.isPrimaryKey ? "PK" : "" }}</b>
              <span>{{ column.name }}</span>
              <small>{{ column.dataType }}</small>
            </span>
            <span v-if="node.table.columns.length > 12" class="db-er-table__more">
              {{ t("database.erMoreColumns", { count: node.table.columns.length - 12 }) }}
            </span>
          </button>
        </div>
      </div>
    </el-scrollbar>

    <template #footer>
      <span class="db-er-hint">{{ t("database.erOpenHint") }}</span>
      <el-button type="primary" @click="emit('update:modelValue', false)">{{
        t("common.close")
      }}</el-button>
    </template>
  </el-dialog>
</template>
